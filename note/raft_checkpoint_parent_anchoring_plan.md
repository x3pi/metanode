# Kế hoạch: Raft checkpoint định kỳ (mỗi N block, gửi theo batch) lên Parent Chain

> **Trạng thái:** KẾ HOẠCH v2.1, chưa triển khai (2026-11-20). v2.1 xử lý phản biện vòng 2 (mục 8.1).
> v2 thay v1: tạo tx riêng `METHOD_SUBMIT_BLOCK_CHECKPOINTS` có batch và `block_hash`, không dùng lại `SubmitStateRootArgs`. Đã xét 5 phản biện ở mục 8.
> **Phạm vi:** `consensus_mode = "raft"` (cluster nhiều replica, `raftfeed.StartCluster`).
> **Nhãn:** `[ĐỌC CODE]` = đã đối chiếu mã nguồn, `[GIẢ THUYẾT]` = chưa kiểm chứng, `[CHƯA ĐO]` = chưa chạy.

---

## 1. Bối cảnh

- Raft **không có epoch**: stamper của cluster luôn có `epoch: 0` (`pkg/rollup/raftfeed/node.go:265`) [ĐỌC CODE]. Mọi cơ chế trigger theo epoch đều không chạy với Raft.
- Hiện **chưa mode nào gửi checkpoint** [ĐỌC CODE]:
  - `CheckpointWorker` (`pkg/rollup/worker_checkpoint.go`) không được khởi tạo ở đâu.
  - Interface `StateProvider.GetLatestEpoch()` không có implementation.
  - `SendSubmitStateRoot` không có ai gọi.
- Tx cũ `METHOD_SUBMIT_STATE_ROOT` (`SubmitStateRootArgs{cluster_key, epoch, state_root, cert}`) có ngữ nghĩa theo epoch, mỗi tx 1 root, và không có `block_hash`. **Không dùng lại** tx này cho Raft. Tx cũ giữ nguyên cho DAG.

## 2. Yêu cầu

1. Cứ **N block** (mặc định 100, cấu hình được) có 1 checkpoint. **Chỉ Raft leader gửi.**
2. **Batch:** một tx mang nhiều checkpoint (dùng khi gửi bù).
3. Parent chết thì chờ, khi sống lại thì gửi bù đủ theo thứ tự. Một node parent chết thì gửi qua node khác.
4. Parent chưa có checkpoint nào của cluster thì bootstrap từ checkpoint hiện tại.
5. **Chỉ neo block chắc chắn không fork**: block phải đã được đa số replica attest.
6. Worker riêng, không làm nghẽn luồng xử lý block hay pipeline nonce của các worker khác.

## 3. Thiết kế

### 3.1. Dữ liệu neo (tx mới)

```proto
// parent_chain.proto
METHOD_SUBMIT_BLOCK_CHECKPOINTS = 10;

message BlockCheckpoint {
  uint64 block_number = 1;  // bội số của interval (100, 200, ...)
  bytes  block_hash   = 2;  // 32 byte, header hash (nối chuỗi qua LastBlockHash)
  bytes  state_root   = 3;  // 32 byte, header.AccountStatesRoot()
}

message SubmitBlockCheckpointsArgs {
  bytes cluster_key = 1;                    // 48 byte BLS pubkey của cluster
  repeated BlockCheckpoint checkpoints = 2; // 1..MaxBlockCheckpointsBatch (64), block_number tăng ngặt
  bytes cert = 3;                           // 96 byte BLS ký trên digest của toàn batch
}
// ParentChainCallData.oneof: SubmitBlockCheckpointsArgs submit_block_checkpoints = 11;
```

- **Digest ký:** chỉ có **một hàm dùng chung** `parentchain.ComputeBlockCheckpointsMessage(clusterKey, checkpoints) []byte`. Worker gọi nó để ký, parent gọi nó để verify, không bên nào tự dựng buffer riêng.
  ```
  msg = "MTN_BLOCK_CHECKPOINTS_V1" ‖ cluster_key(48) ‖ be32(count) ‖ for each: be64(block_number) ‖ block_hash(32) ‖ state_root(32)
  ```
  - Domain tag riêng để không trùng với digest của `SUBMIT_STATE_ROOT`.
  - Mỗi entry dài cố định 72 byte, nên chuỗi trên chỉ có một cách tách. `be32(count)` là cẩn thận thêm, không phải để vá lỗi.
  - Ký thẳng buffer thô, không keccak trước, đúng như các message hiện có (`ComputeSubmitStateRootMessage`, `state.go:654`). `bls.Sign` và `bls.VerifySign` tự hash-to-curve với DST (`pkg/bls/bls.go:20-39`). Buffer tối đa khoảng 4.7KB.
- **Kích thước tx:** 64 × 72 = 4.608 byte, cộng phần đầu khoảng 200 byte. Rất xa giới hạn `maxRawTxBytes = 1 << 20` (`http_rpc.go:634`) [ĐỌC CODE].
- Có `block_hash` ngay từ đầu, để sau này làm proof hay fraud proof không phải đổi proto lần hai (phản biện 2).
- Không gửi `receipts_root` hay `transactions_root` riêng: `block_hash` đã cam kết cả header. Nếu sau này cần các root đó, chỉ cần nộp header gốc rồi so hash với `block_hash` đã neo.

### 3.2. Quy tắc phía parent (idempotent)

Hàm `SubmitBlockCheckpoints(store, clusterKey, checkpoints, cert)`:
1. Cluster đã đăng ký và được authorize (giống `SubmitStateRoot`), `senderKey == clusterKey`, chữ ký hợp lệ.
2. Batch có 1 tới 64 phần tử, `block_number` tăng ngặt, `block_number > 0`. Sai thì từ chối toàn bộ tx.
3. Đọc `latest` của cluster. Duyệt theo thứ tự:
   - `block_number <= latest`: nếu parent đã có đúng entry đó (cùng hash và root) thì **bỏ qua**, đây là gửi trùng và là trường hợp idempotent. Nếu khác hash, hoặc parent không có entry cho block đó (rơi vào khoảng trống trước điểm bootstrap), thì **từ chối toàn bộ tx** (`ErrCheckpointConflict`) và phát event, vì đây là dấu hiệu fork.
   - `block_number > latest`: ghi entry và cập nhật `latest`.
4. Tx gửi trùng (leader cũ và mới, gửi lại sau khi tx bị rớt) vẫn có **status = 1**, nên không làm hỏng pipeline nonce chung (phản biện 3).

**Không bắt buộc khoảng cách giữa các block** (không ép `next = latest + interval`). Nhờ vậy cluster được phép bootstrap từ block bất kỳ, được đổi interval, và vẫn neo được sau khi dữ liệu cũ bị prune (mục 3.5). Tính liên tục là nghĩa vụ của worker, còn parent chỉ đảm bảo dãy checkpoint tăng ngặt và không có xung đột.

Lưu trữ:
- `TreeKey(NamespaceBlockCheckpoint=0x0F, clusterKeyHash ‖ be64(block))` có value `block_hash ‖ state_root`.
- `TreeKey(NamespaceBlockCheckpointLatest=0x10, clusterKeyHash)` có value `be64(block) ‖ block_hash ‖ state_root`.

### 3.3. Bất biến an toàn (không fork)

- Chỉ neo block `n` khi **`n <= lastAttested`**, trong đó `lastAttested` là block mới nhất mà `attestOnce` (`pkg/rollup/raftfeed/attest.go`) thấy **đa số voter có cùng header hash với local**. Peer chỉ trả lời hash cho block đã durable (`handleBlockHash`).
- Header hash nối chuỗi qua `LastBlockHash`, nên block `M >= n` khớp đa số thì block `n` cũng khớp.
- Leader lệch so với đa số thì `attestOnce` gọi `n.fatal`, node dừng (fail closed).
- Anchoring nằm **ngoài đường dispatch commit**. Ticker hay backoff chỉ dùng để điều nhịp, không quyết định block nào được thực thi, nên không vi phạm Zero-Fork (AGENTS.md Part 2.5).

### 3.4. Worker: một tx batch mỗi vòng, tiến độ đọc từ parent

Mỗi vòng (ticker 2s, hoặc `Wakeup()`):

```
if !IsLeader():                       inflight = nil; confirmed = unknown; return
I = interval (config, mặc định 100)
target = floor(AttestedBlock() / I) * I
if target == 0:                       return
rounds++
// Cache: không có việc gì mới thì không đọc parent
if inflight == nil && confirmed != unknown && target <= confirmed
   && rounds % refreshRounds(30) != 0:  return
(latest, found) = GetLatestBlockCheckpoint(clusterKey)  // strong quorum read
if lỗi / không đủ quorum:             return             // chờ, không đoán
confirmed = found ? latest : 0                            // luôn ghi đè bằng giá trị đọc từ parent
if inflight != nil:                                       // tối đa 1 tx đang chờ
    if latest >= inflight.last:       inflight = nil      // parent đã áp dụng
    else if inflight.rounds++ < stallRounds(30): return   // chờ tiếp (~60s)
    else:                             inflight = nil      // tx rớt: gửi lại
start = found ? (floor(latest/I) + 1) * I : target       // !found => bootstrap
start = max(start, floor(lastPruned/I + 1) * I)          // xem 3.5
batch = [start, start+I, ...] ∩ [.., target], tối đa 64 phần tử
với mỗi n: (hash, root) = BlockAt(n); thiếu thì cắt batch tại đó
nếu batch rỗng:                        return
ký và SendSubmitBlockCheckpoints(batch)                   // failover mọi node parent
thành công: inflight = {last: batch[-1], rounds: 0}
```

- **Cache `confirmed`:** chỉ để tối ưu, giá trị chuẩn luôn là `latest` trên parent.
  - Khi cluster nhàn rỗi hoặc đã neo kịp, worker không gọi parent. Worker chỉ đọc quorum khi có block mới cần neo, khi đang có tx chờ, khi mới lên leader, hoặc khi tới lượt làm mới định kỳ (mỗi `refreshRounds = 30` vòng, khoảng 60s).
  - Việc làm mới định kỳ giúp phát hiện parent bị reset hay mất dữ liệu (`latest` giảm hoặc không còn), để worker tự gửi bù thay vì tin mãi vào cache.
  - Mất quyền leader thì xóa cache.
- **`stallRounds = 30`** (khoảng 60s với ticker 2s) mới gửi lại tx đang chờ. Hằng số này không dựa trên block time đo được của parent [GIẢ THUYẾT]: parent không có block time cố định, block lấy `CommitTimestampMs` theo consensus (`cmd/parent_chain/processor/block_processor.go:293`). Gửi lại là vô hại (parent idempotent), nên chọn rộng tay. Chưa đưa vào config (YAGNI), sẽ chỉnh nếu đo được latency thực tế.
- **Mỗi vòng chỉ có 1 tx** và tối đa 1 tx đang chờ. Checkpoint không cần nhanh, nên nonce được dùng rất ít: tối đa 1 nonce mỗi vòng, thay vì 8 như bản v1 (phản biện 3).
- Ở trạng thái ổn định, mỗi batch có 1 phần tử, tức 1 tx mỗi 100 block. Khi gửi bù, mỗi tx mang tối đa 64 checkpoint (bằng 6.400 block).
- Leader mới, node vừa restart hay parent vừa sống lại đều tính lại `start` từ `latest` trên parent.

### 3.5. Pruning (phản biện 1)

- Trong Raft hiện tại, pruning **thực tế không chạy** [ĐỌC CODE]: `PruningManager.pruneTick` (`pkg/pruning/pruning_manager.go:78`) chỉ prune khi `currentEpoch > EpochsToKeep` (mặc định 128), mà epoch của Raft luôn là 0. Phản biện đúng về nguyên tắc nhưng hiện chưa xảy ra.
- Vẫn cần thêm chốt chặn, phòng khi sau này Raft có pruning theo block. Nếu `start <= bc.GetLastPrunedBlockNumber()` thì log **CRITICAL** và nhảy tới checkpoint đầu tiên còn dữ liệu, `floor(lastPruned/I + 1) * I`.
  - Việc nhảy này **an toàn**: parent không ép khoảng cách (mục 3.2), và nhảy chỉ tạo ra khoảng trống trong lịch sử neo. Nó không neo block chưa attest và không gây fork.
  - Không dùng cách "break mãi mãi" như v1, vì sẽ kẹt vĩnh viễn.
  - Metric `checkpoint_gap_total` đếm số lần nhảy.

### 3.6. Failover parent

- **Ghi:** dùng `QuorumClient.sendRawTxFailover` (`quorum_client.go:257`), thử lần lượt các URL trong `PARENT_CHAIN_URLS`.
- **Đọc `latest`:** dùng strong quorum (`strongQuorumThreshold()`, `quorum_client.go:109`) trên bộ `(block, hash, root, found)`. Không đủ quorum thì chờ.

### 3.7. Khóa ký

- Dùng `app.keyPair`, cũng là khóa của `QuorumClient` (`cmd/simple_chain/app.go:393`).
- Các replica Raft **dùng chung khóa ký** [ĐỌC CODE]:
  - Code bắt buộc `raft.sequencer_address` phải khớp địa chỉ ký của node (`block_processor_network.go:112`).
  - Trong deploy, `private_key` và `bls_priv` khai báo ở cấp cluster (`deploy/ansible_clusters/inventory.yml:117-119`, cụm `exec1`). Các host `exec1_replica1/2/3` không ghi đè. Template `roles/exec_cluster/templates/exec_config.json.j2:8,50` dùng chung giá trị đó.
  - Vì vậy replica nào lên làm leader cũng ký được với đúng cluster key.
- ⚠️ `inventory.yml` đang chứa private key dạng plaintext trong git. Nếu dùng cho môi trường thật thì phải chuyển sang vault.

## 4. Các thay đổi cụ thể

### 4.1. Proto và parent chain
⚠️ Thêm method mới cũng là thay đổi state transition. Node parent cũ trả về `ErrUnknownMethod` (`tx.go:766`, status 0), còn node mới thì ghi state, nên các node sẽ lệch state root. **Phải nâng cấp mọi node parent trước, sau đó mới bật worker** (giống quy trình của `METHOD_REGISTER_ACCOUNTS_BATCH`).

| File | Thay đổi |
|---|---|
| `pkg/proto/parent_chain.proto` | `METHOD_SUBMIT_BLOCK_CHECKPOINTS = 10`, các message `BlockCheckpoint` và `SubmitBlockCheckpointsArgs`, thêm vào oneof. Regenerate bằng `pkg/proto/protoc.sh` |
| `pkg/parentchain/tx.go` | `MaxBlockCheckpointsBatch = 64`, Encode/Decode call data, `ComputeBlockCheckpointsMessage` (hàm digest dùng chung, xem 3.1), case mới trong apply. **Receipt chỉ phát 2 event tóm tắt**, không phát từng checkpoint: (1) `keccak256(msg ‖ cert)`, (2) `be64(first_block) ‖ be64(last_block)`. Khớp mẫu event của `SUBMIT_STATE_ROOT` (`tx.go:724-726`) |
| `pkg/parentchain/state.go` | `SubmitBlockCheckpoints` theo quy tắc 3.2, `ErrCheckpointConflict`, `ErrCheckpointBatchInvalid` |
| `pkg/parentchain/encoding.go` | `NamespaceBlockCheckpoint = 0x0F`, `NamespaceBlockCheckpointLatest = 0x10` |
| `pkg/parentchain/store.go`, `tree_store.go`, `db_store.go` | `GetBlockCheckpoint`, `SetBlockCheckpoint`, `GetLatestBlockCheckpoint`, `SetLatestBlockCheckpoint`. Với `MemoryStore`, thêm map và cập nhật `NewMemoryStore`, `cloneInternal`, `Drop` |
| `pkg/parentchain/http_rpc.go` | `/block_checkpoint?pubkey=&number=` và `/block_checkpoint_latest?pubkey=`, cùng client tương ứng |
| `pkg/parentchain/client.go`, `quorum_client.go` | `SendSubmitBlockCheckpoints`, `GetLatestBlockCheckpoint` (strong quorum) |
| Fake client trong `pkg/rollup/e2e_test.go`, `worker_send_test.go` | Thêm 2 method mới |

Phát hiện phụ [ĐỌC CODE]: `MemoryStore.cloneInternal` (`store.go:114`) không copy `stateRoots`, nên `Push()` rồi `Drop()` sẽ làm mất state root. Store này chỉ dùng trong test. Sửa luôn khi động vào file.

### 4.2. raftfeed
| File | Thay đổi |
|---|---|
| `pkg/rollup/raftfeed/node.go` | Field `lastAttested atomic.Uint64` và `LastAttested()` |
| `pkg/rollup/raftfeed/attest.go` | `attestOnce`: khi hash đa số **bằng `mine`** và `num > lastAttested` thì lưu `num` |
| `pkg/rollup/raftfeed/raftfeed.go` | Hàm package-level `AttestedBlock()` theo mẫu `IsLeader()` |

### 4.3. Worker: viết lại `pkg/rollup/worker_checkpoint.go`
```go
type CheckpointSource interface {
    IsLeader() bool
    AttestedBlock() uint64
    BlockAt(n uint64) (hash, stateRoot common.Hash, ok bool)
    LastPrunedBlock() uint64
}
func NewCheckpointWorker(client parentchain.Client, src CheckpointSource,
    keyPair *bls.KeyPair, intervalBlocks uint64, tick time.Duration) *CheckpointWorker
```
- Logic gom vào `runOnce()` theo 3.4. Giữ `Start/Stop/Wakeup` (kênh buffer 1). Bỏ `StateProvider`.

### 4.4. Config và nối vào app
- `pkg/config/config.go`, trong cấu hình Raft: `checkpoint_interval_blocks` (mặc định 100, giá trị 0 thì tắt worker) (phản biện 5).
- `cmd/simple_chain/app.go`: khi `rollupEnabled && config.Raft != nil && interval > 0` thì tạo worker với adapter:
  - `raftfeed.IsLeader`, `raftfeed.AttestedBlock`;
  - `BlockAt` dùng `blockchain.GetBlockChainInstance().GetBlockByNumber(n)` để lấy `Header().Hash()` và `AccountStatesRoot()`;
  - `LastPrunedBlock` dùng `GetLastPrunedBlockNumber()`.
- Start cùng nhóm rollup worker (`app.go:1111`), Stop khi shutdown. Mode DAG không đổi.

### 4.5. Tài liệu
- `PROJECT_STRUCTURE.md`: thêm dòng "Raft block checkpoint anchoring" vào bảng cross-layer, cập nhật bảng proto, cập nhật `Last updated`.
- `pkg/rollup/RAFT_CLUSTER_RUNBOOK.md`: cơ chế, cấu hình, quy trình nâng cấp parent, cách kiểm tra (`/block_checkpoint_latest`), cách reset devnet (mục 8, phản biện 4).

## 5. Thứ tự triển khai
1. 4.1 Proto và parent, kèm test. Deploy toàn bộ parent.
2. 4.2 raftfeed, kèm test.
3. 4.3 Worker, kèm test.
4. 4.4 Config và nối vào app.
5. 4.5 Tài liệu.
6. `build_check.sh` và Go test.

## 6. Kiểm chứng

**Unit test worker** (`pkg/rollup/worker_checkpoint_test.go`, fake source và fake parent có áp dụng tx):
- không phải leader thì không gửi;
- `attested < interval` thì không gửi;
- bootstrap đúng `floor(attested/I)*I`;
- **leader chạy nhanh:** block local 1000, attested 250 thì chỉ neo tới 200;
- parent chết rồi sống lại: gửi bù theo batch (tối đa 64 phần tử mỗi tx) và đủ, đúng thứ tự;
- chỉ có tối đa 1 tx đang chờ; tx rớt thì gửi lại sau `stallRounds` vòng;
- cache: khi nhàn rỗi (`target <= confirmed`) thì không gọi parent, nhưng tới vòng `refreshRounds` vẫn đọc lại;
- parent bị reset (`latest` giảm hoặc không còn) thì gửi bù lại sau lần làm mới định kỳ;
- đổi leader: leader mới tiếp tục từ `latest + I`;
- block đã bị prune: nhảy tới checkpoint còn dữ liệu, không kẹt.

**Unit test parent:**
- batch hợp lệ thì ghi và cập nhật latest;
- gửi trùng y hệt thì status 1, không đổi state;
- trùng block nhưng khác hash thì `ErrCheckpointConflict`;
- batch không tăng ngặt, rỗng, hoặc hơn 64 phần tử thì từ chối;
- digest: chữ ký do worker tạo bằng `ComputeBlockCheckpointsMessage` verify được ở parent; đổi bất kỳ trường nào (kể cả thứ tự entry) thì verify thất bại;
- receipt có đúng 2 event `(keccak(msg‖cert), first‖last)`;
- chữ ký sai hoặc cluster chưa authorize thì từ chối;
- `MemoryStore` `Push/Drop` giữ nguyên dữ liệu.

**raftfeed:** trong `cluster_test.go`, assert `LastAttested()` nằm trong khoảng `1..40` trên cluster khỏe.

**Build và test:**
- `cd consensus/metanode/scripts && ./build_check.sh` phải exit 0.
- Từ `execution/`: `CGO_ENABLED=1 go test -p 1 -count=1 ./pkg/parentchain/... ./pkg/rollup/`; `./pkg/rollup/raftfeed/` chạy riêng với `-timeout 15m`.

**E2E** (người dùng tự chạy) [CHƯA ĐO]: Raft 3 replica và parent 3 node.
- Mỗi 100 block, `latest` tăng.
- Tắt hết parent, chạy thêm vài nghìn block, bật lại: gửi bù đủ với ít tx (batch).
- Tắt 1 node parent: vẫn neo được.
- Kill leader: không trùng, không thiếu.

## 7. Rủi ro

| Rủi ro | Xử lý |
|---|---|
| Nâng cấp parent không đồng loạt thì parent bị lệch state | Deploy toàn bộ parent trước, `checkpoint_interval_blocks` để 0 tới khi xong |
| Replica không dùng chung BLS key | Kiểm tra cấu hình deploy trước khi bật |
| Pruning sau này xóa block chưa neo | Nhảy tới checkpoint còn dữ liệu và log CRITICAL, có metric |
| Parent lệch height nên quorum read không đạt | Chờ vòng sau, checkpoint chỉ bị chậm |
| Hiệu năng | Ổn định ở mức 1 tx mỗi 100 block. Gửi bù tối đa 1 tx (64 checkpoint) mỗi vòng [CHƯA ĐO] |

## 8. Đánh giá các phản biện

| # | Phản biện | Kết luận | Xử lý trong v2 |
|---|---|---|---|
| 1 | Pruning làm worker kẹt vĩnh viễn | **Đúng về logic của v1.** Tuy nhiên với Raft hiện tại, pruning không chạy vì `pruneTick` dựa vào epoch, mà epoch luôn là 0 [ĐỌC CODE] | Nhảy tới checkpoint còn dữ liệu và log CRITICAL (3.5). Parent không ép khoảng cách nên nhảy không bị từ chối |
| 2 | Nên thêm `block_hash` ngay | **Đúng** | Có trong `BlockCheckpoint` (3.1) |
| 3 | Gửi 8 tx liên tiếp làm rối pipeline nonce chung | **Đúng.** `getNextNonce` (`quorum_client.go:~165`) thấy một receipt in-flight có `Status != 1` thì reset toàn bộ pipeline, làm ảnh hưởng tx của `SendWorker`. Ở v1, các tx gửi trùng bị parent từ chối (status 0), nên sẽ kích hoạt đúng việc reset này | Mỗi vòng 1 tx batch, tối đa 1 tx đang chờ. Parent xử lý gửi trùng theo kiểu idempotent (status 1) (3.2, 3.4) |
| 4 | Reset devnet cluster mà không reset parent thì kẹt | **Đúng một phần.** Nếu cluster reset về block 0 mà vẫn dùng key cũ, parent sẽ thấy block 100 với hash khác và trả `ErrCheckpointConflict` | Không thêm RPC admin để xóa checkpoint, vì nó mở thêm bề mặt tấn công lên dữ liệu neo. Runbook hướng dẫn reset parent cùng lúc, hoặc dùng cluster key mới khi reset devnet |
| 5 | Không hardcode 100 | **Đúng** | `checkpoint_interval_blocks`, mặc định 100, giá trị 0 thì tắt (4.4) |

### 8.1. Phản biện vòng 2 (cho v2)

| # | Phản biện | Kết luận | Xử lý |
|---|---|---|---|
| 1 | Nhàn rỗi mà vẫn đọc quorum mỗi 2s | **Đúng** | Thêm cache `confirmed`, xóa khi mất quyền leader, đọc lại mỗi 30 vòng để phát hiện parent bị reset (3.4) |
| 2 | `stallRounds = 6` (12s) hơi gấp | **Hợp lý**, nhưng con số latency 5–15s chưa được đo | Nâng lên 30 vòng (khoảng 60s), giữ là hằng số (3.4) |
| 3 | Digest cần `count` và keccak trước khi ký | **Đúng một nửa.** Entry dài cố định 72 byte nên không có parsing ambiguity. Keccak không bắt buộc vì BLS tự hash-to-curve với DST | Thêm `be32(count)` để cẩn thận. Ký buffer thô như các message hiện có. Bắt buộc dùng **một hàm digest chung** cho cả hai đầu (3.1) |
| 4 | Các replica có dùng chung BLS key không | **Đúng, đã xác nhận** trong inventory và template | Gỡ nhãn [GIẢ THUYẾT] (3.7) |
| 5 | Receipt chỉ nên phát event tóm tắt | **Đúng** | 2 event: `keccak(msg‖cert)` và `first‖last` (4.1) |

### 8.2. Phản biện vòng 3 (Vận hành & Cảnh báo Telegram Anti-spam)

| # | Phản biện | Kết luận | Xử lý |
|---|---|---|---|
| 1 | Lỗi gửi Parent Chain liên tục (mạng đứt, Parent Chain treo/sập) có bắn Telegram không, và làm sao chống spam nếu tick 2s/lần? | **Cực kỳ chính xác.** Nếu lỗi liên tục mà bắn mỗi 2s thì Telegram sẽ bị rate limit và ngập lụt group; còn nếu không báo gì thì operator không biết Parent Chain sập. | Triển khai Circuit Breaker State-Machine cho Telegram: (1) Jitter 1-4 lần: chỉ log local; (2) Lần 5: cảnh báo Telegram ban đầu; (3) Lần 15 (~30s): chốt hạ 1 câu xác nhận Parent Chain treo/chết và tạm ngừng bắn tin; (4) Silent retry ngầm; (5) Khi Parent Chain sống lại: bắn 1 thông báo phục hồi duy nhất và tiếp tục neo. |

### 8.3. Phản biện vòng 4 (Tx Rejection Backoff, Fork Alert riêng & Quy chuẩn Comment)

| # | Phản biện | Kết luận | Xử lý |
|---|---|---|---|
| 1 | Tx bị parent từ chối (Status 0) thì worker gửi lại ngay sau 4 giây, làm reset pipeline nonce dùng chung liên tục. Nếu xung đột kéo dài thì spam 12 tin/giờ. | **Cực kỳ chính xác.** Khi receipt có `Status == 0`, nếu chỉ set `inflight = nil` thì vòng kế tiếp worker tính lại start và gửi lại y nguyên batch, lặp vô tận và reset pipeline nonce của `SendWorker` trong `getNextNonce`. Nếu cứ 5 phút bắn 1 tin thì 1 giờ nhận 12 tin spam. | Thêm `rejectBackoffRounds = 150` (~5 phút ở nhịp 2s) và `backoffUntil uint64`. Dùng cờ `isTxRejected`: khi bị từ chối lần đầu, gửi đúng 1 tin Telegram cảnh báo đỏ và kích hoạt backoff 150 vòng. Các lần thử lại sau 5 phút nếu vẫn lỗi thì chỉ log local, không spam Telegram. Khi checkpoint thành công trở lại: gửi đúng 1 tin phục hồi và tắt cờ `isTxRejected`. |
| 2 | Phát hiện fork bị lặp và cảnh báo sai nội dung thành "Mất kết nối N lần" | **Đúng.** Lệch hash / fork là sự cố nghiêm trọng hơn mất mạng rất nhiều, parent vẫn sống. | Tách cờ `isForkDetected`, gửi riêng thông báo đỏ `🚨 [ParentChain] PHÁT HIỆN LỆCH HASH / FORK: ...` đúng 1 lần, kích hoạt backoff 150 vòng (không đọc quorum liên tục mỗi 2s), và chỉ phục hồi khi hash trùng khớp. |
| 3 | Comment trong code Go phải tuân thủ chuẩn tiếng Anh của AGENTS.md | **Đúng.** AGENTS.md quy định toàn bộ comment trong code phải bằng tiếng Anh (nội dung alert Telegram tiếng Việt giữ nguyên). | Đã rà soát và dịch toàn bộ comment trong `worker_checkpoint.go`, `worker_checkpoint_test.go`, và `checkpoint_e2e_test.go` sang tiếng Anh chuẩn xác. |
