# Kế hoạch hoàn thiện Parent Chain (giai đoạn 2)

> Lập 2026-10-01 cho agent thực hiện. Đọc kèm: `note/parent_chain_multinode_plan.md` (quyết định Đ1–Đ14, đặc tả tất định mục 5, bộ test T-*), `note/parent_chain_multinode_discovery.md` (D1–D14), `AGENTS.md`.
> Mốc hiện tại: commit `95bca919` trên `dev` (chưa push). Mọi quyết định Đ1–Đ14 **đã chốt**, không hỏi lại.

---

## 0. Luật không đổi (nhắc ngắn)

1. **Zero-fork:** thà pending còn hơn fork; không dùng timeout/sleep để quyết định dispatch hay thoát kẹt; thoát bằng dữ liệu từ peer.
2. Mọi queue/worker mới có giới hạn bộ đệm; không I/O chặn trong vòng lặp async.
3. Sau mỗi thay đổi: `consensus/metanode/scripts/build_check.sh` sạch, **và** `cd execution && go build ./cmd/parent_chain/... && go vet` (build_check **không** build `parent_chain`).
4. Cập nhật `PROJECT_STRUCTURE.md` khi đổi cấu trúc. Commit bằng tên file (`git add <file>`), không `git add <thư mục>`. Không push `dev` khi chưa được chủ dự án đồng ý.
5. **Môi trường là local dev, dữ liệu bỏ được:** agent được **tự do dừng, wipe dữ liệu và chạy lại từ đầu** (parent chain `:8547`, cụm exec, cụm 4 node cục bộ) khi cần để test; không cần xin phép, không cần backup, không cần giữ tương thích. Khuyến nghị vẫn dùng cụm cô lập `deploy/cluster/local_parent_chain/` (cổng 18601–18604, mạng 19001–19004) cho test chịu lỗi để không làm gián đoạn các cụm khác khi không cần thiết. Thư mục này bị `.gitignore` chặn (chứa khóa), **đừng ép commit**; nếu cần script/test dùng chung thì đặt bản không chứa khóa ở `deploy/parent_chain_cluster/` hoặc `execution/scripts/`.
6. Báo cáo trung thực: cái gì đã chạy thật trên cụm, cái gì mới có unit test.

---

## 1. Hiện trạng đã xác minh (2026-10-01)

**Đã có và đã kiểm chứng:**
- Build `parent_chain`/`pkg/parentchain`/`executor`/`nomt_ffi` sạch; unit test + `-race` của `pkg/parentchain`, `cmd/parent_chain/processor`, `pkg/nomt_ffi` pass; `build_check.sh` 4/4.
- Cụm 4 node cục bộ chạy; `test_cluster.go` pass: cùng `state_root`/`block_hash` ở mọi node, committee 4 validator, 2 giao dịch ký BLS (`RegisterCluster`, `SubmitStateRoot`) được thực thi và truy vấn lại được.
- Có: state NOMT, `txs_root`/`receipts_root`, `ApplyBlock` nguyên tử + idempotent, sắp xếp `(From, Nonce, Hash)`, genesis loader, RPC `/block /receipt /proof /status /validators /send_raw_transaction`, `GetBlocksRange`/`SyncBlocks` callback.

**Còn thiếu / sai lệch so với kế hoạch (mỗi mục có mã để tham chiếu; đã xác minh bằng đọc code):**

| Mã | Vấn đề / Trạng thái | Bằng chứng / Giải pháp đã áp dụng |
|---|---|---|
| **H1** | **[ĐÃ XONG] Lỗ hổng mint float (G8 đóng).** Handler `MethodDepositToFloat` yêu cầu `Cert` (96 byte BLS của cluster nguồn theo tag `DEPOSIT_FLOAT_V1:`). Kiểm `Cert` trong thực thi tất định; sai Cert => receipt lỗi, state không đổi. Đã test T-U16/T-U17. | `pkg/parentchain/tx.go`, `pkg/parentchain/state.go` |
| **H2** | **[ĐÃ XONG] QuorumClient cho node thực thi.** Đã tạo `QuorumClient` với Byzantine quorum >= f+1, kiểm tra proof qua `nomt_ffi.VerifyProof`. `cmd/simple_chain/app.go` hỗ trợ `PARENT_CHAIN_URLS`. Đã test T-C1..T-C4. | `pkg/parentchain/quorum_client.go`, `cmd/simple_chain/app.go:277` |
| **H3** | **[ĐÃ XONG] Vô hiệu hóa đường `/tx` JSON legacy.** Đường `/tx` legacy bị vô hiệu hóa mặc định (`enableLegacyTx=false`). Toàn bộ client và RPC chuyển sang `/send_raw_transaction` nhận proto bytes. | `pkg/parentchain/http_rpc.go:420-530` |
| **H4** | **[ĐÃ XONG] Genesis bắt buộc, xóa validator hard-code.** `-genesis` bắt buộc trong `cmd/parent_chain/main.go`; thiếu hoặc lỗi => thoát ngay (`os.Exit(1)`). Xóa bỏ toàn bộ fallback committee mặc định. | `cmd/parent_chain/main.go`, `cmd/parent_chain/app.go` |
| **H5** | **[ĐÃ XONG] Template triển khai multi-node.** Đã cập nhật `node_parent.toml.j2` và `parent_consensus.toml.j2` hỗ trợ `parent_node_id`, `peer_rpc_addresses`, cổng P2P/metrics riêng. Cập nhật `tasks/main.yml`, `metanode-parentchain.service.j2` cờ `-genesis`, và `inventory.example.yml` mẫu 4 node. | `deploy/ansible_clusters/roles/parent_chain/` |
| **H6** | **[7/8 LIVE - T-I4 CHƯA LIVE] Test chịu lỗi cụm 4 node.** Đã chạy live T-I1..T-I8 trên cụm 4 node thật (`deploy/cluster/local_parent_chain/`): 7/8 PASS live; T-I4 chưa kiểm live (chỉ có unit test `TestBlockProcessor_ForkConflictDetection`, xem N2); T-I7 chỉ kiểm node offline (node nói dối có unit test `QuorumClient`, xem N3). Chi tiết tại `note/parent_chain_cluster_test_report.md`. | `execution/cmd/tool/test_cluster_fault_tolerance/`, `note/parent_chain_cluster_test_report.md` |
| **H7** | **[ĐÃ XONG] Nonce & Revert Isolation.** Đã cài đặt Snapshotter `Push()` / `Drop()` / `Merge()` trên `TreeStore` và `MemoryStore`. Nonce tăng khi chữ ký hợp lệ để chống replay; khi handler lỗi, state overlay của handler bị revert hoàn toàn. | `pkg/parentchain/tree_store.go`, `pkg/parentchain/store.go`, `pkg/parentchain/block_exec.go` |
| **H8** | **[ĐÃ THỰC THI NGUYÊN TẮC] Xóa dữ liệu local dev (Wipe local data).** Vì toàn bộ định dạng DB LevelDB (`ParentChainBlockRecord`, `ParentChainBlockProgress`) và state NOMT đã chuyển sang Protobuf tất định, DB cũ **không tương thích**. Môi trường dev thực hiện **wipe sạch dữ liệu (`run.sh clean`)** trước khi khởi động cụm 4 node mới. Không cần migrate hay giữ tương thích. | `deploy/cluster/local_parent_chain/run.sh` (`clean`), `pkg/parentchain/block_exec.go` |
| **H9** | **[ĐÃ XONG] Runbook, metrics fork, tool giám sát.** Đã viết `note/runbook_parent_chain_multinode.md` (6 quy trình vận hành); thêm metrics Prometheus (`parent_chain_last_block`, `parent_chain_state_root`, `parent_chain_fork_detected`, `parent_chain_txs_total`, `parent_chain_blocks_total`) tích hợp route `/metrics`; tạo tool giám sát `execution/cmd/tool/parent_chain_monitor`. | `note/runbook_parent_chain_multinode.md`, `pkg/parentchain/metrics.go`, `cmd/tool/parent_chain_monitor/` |
| **H10** | **[CHƯA ĐO ĐÚNG - XEM N5] Đo hiệu năng dưới tải (Benchmark).** Đã đo thử nghiệm 1,000 giao dịch BLS qua 4 node (cam kết qua 3 block #1..#4, 4 node cùng StateRoot). Tuy nhiên số liệu cũ (431 tx/s và 768 tx/s với thời gian commit đo sai 0.001s do bất đồng bộ pipeline) chưa phản ánh đúng bản chất; cần thực hiện bộ đo đa block, đa mức tải theo kế hoạch N5. | `note/parent_chain_cluster_test_report.md`, `note/parent_chain_next_plan.md` (N5) |
| **H11** | **[ĐÃ XONG] Quét ChainID 990.** Đã rà soát và chuyển toàn bộ tham chiếu ChainID Parent Chain sang 990 trong template Ansible, inventory, hướng dẫn triển khai và tài liệu kiến trúc. | `deploy/`, `PROJECT_STRUCTURE.md` |
| **H12** | **[ĐÃ XONG] Loại bỏ LastDeviceKey BLS smuggling.** Không dùng `LastDeviceKey` mang public key 48 byte. Tra cứu public key của người gửi qua `AccountRegistry`/`ChainRegistry` hoặc registration CallData. | `pkg/parentchain/tx.go` (`BuildAndSignBLSTx`, `VerifyTxSignature`) |
| **H13** | **[ĐÃ XONG] Chuẩn hóa toàn bộ dữ liệu đồng thuận sang Protobuf (P9).** Đã định nghĩa `execution/pkg/proto/parent_chain.proto`, sinh code pb/vtproto. Toàn bộ Header, Receipt, BlockRecord, BlockProgress, CallData, và State values mã hóa bằng `proto.MarshalOptions{Deterministic: true}`. Bỏ hoàn toàn JSON trong đường đồng thuận. Unit test + `-race` pass 100%. | `execution/pkg/proto/parent_chain.proto`, `pkg/parentchain/{encoding,block_exec,tx}.go`, `cmd/parent_chain/processor/tx_batcher.go` |

---

## 2. Thứ tự và phụ thuộc

```
P1 (H1 deposit authz) ──┐
P2 (H4 genesis bắt buộc)─┼─> P3 (H2,H3 client + QuorumClient) ──> P4 (H6 test chịu lỗi cụm) ──> P7 (redeploy sạch)
                         └─> P5 (H5 template/ansible) ─────────────┘                                    │
P9 (H13 proto) làm cùng P1/P3 (chung file; xem P9)
P8 (H7,H9,H10,H11: test nonce, runbook, metrics, perf, ChainID) chạy song song, xong trước P7 ─────────┘
```

Mỗi gói = 1+ commit có test; chạy lại `build_check.sh` và test của gói đó trước mỗi commit.

---

## 3. Gói công việc

### P1 — Đóng lỗ hổng mint float (H1)  **[bảo mật, làm đầu tiên]**
- Thêm `Cert` (96 byte BLS của **cluster nguồn**) vào `depositToFloat` CallData theo `DepositFloatMessage` đã mô tả trong discovery (`DEPOSIT_FLOAT_V1:` ‖ destKey ‖ destClusterID ‖ sender ‖ target ‖ amount(32B) ‖ msgID). Cập nhật `Encode/DecodeDepositToFloatCallData` (độ dài, test vector).
- Trong handler: xác định cluster nguồn (khóa BLS đã đăng ký trong `ChainRegistry`/genesis); **kiểm Cert trong thực thi tất định**; không tìm thấy cluster hoặc Cert sai ⇒ receipt lỗi, state không đổi.
- Mô hình người gửi: chỉ chấp nhận `FromAddress` là tài khoản được phép (relayer/cluster đã đăng ký) **hoặc** chỉ dựa vào Cert của cluster (khuyến nghị: Cert là bằng chứng chính; chữ ký tx chỉ để chống replay/nonce). Ghi quyết định vào `note/`.
- Bất biến cần giữ: `msgID` chỉ áp dụng một lần (đã có); tổng cung float: dùng `CheckFloatSupplyInvariant` trong test.
- **Cần chủ dự án duyệt định dạng Cert/tin cậy** trước khi merge (đây là điểm tin cậy cốt lõi). Nếu chưa duyệt: làm sẵn code + test, **không** bật trên cụm dùng chung.
- **Nghiệm thu:** T-U13..T-U18 + test mới: (a) deposit không Cert ⇒ bị loại; (b) Cert của cluster khác/đích khác/amount khác ⇒ bị loại; (c) tx hợp lệ nhưng người gửi tùy ý + không có Cert ⇒ bị loại (đóng H1); (d) 4 node chạy cùng chuỗi ⇒ cùng root kể cả khi một validator cố đưa deposit sai vào block (T-I8).

### P2 — Genesis bắt buộc, bỏ committee mặc định (H4)
- `-genesis` **bắt buộc** (thiếu ⇒ thoát với lỗi rõ). `LoadGenesis` lỗi ⇒ **thoát**, không dùng committee mặc định. Xóa toàn bộ khóa nhúng trong `app.go`.
- Giữ chế độ devnet đơn node bằng **file genesis một validator** (do công cụ sinh), không bằng fallback trong code.
- Kiểm hợp lệ genesis: validators không rỗng, không trùng khóa/địa chỉ, stake > 0, `chain_id` khớp, cảnh báo n < 4; thêm kiểm tra khớp với `committee.json` của Rust (test đối chiếu).
- **Nghiệm thu:** test: thiếu cờ ⇒ thoát; file hỏng ⇒ thoát; 4 node cùng genesis ⇒ `/validators` giống nhau từng byte.

### P3 — Client node thực thi + QuorumClient (H2, H3)
- (H12) Bỏ việc nhét public key vào `LastDeviceKey`: sửa `BuildAndSignBLSTx`/`VerifyTxSignature` để lấy khóa từ registry, tx khởi tạo mang khóa trong `Data`; thêm test chống tái dùng.
- `pkg/parentchain.Client.Send*`: dựng `pb.Transaction` (`BuildAndSignBLSTx` đã có) ký bằng **khóa cluster**, `ToAddress` hệ thống, `ChainID 990`, `Nonce` tuần tự **lấy từ chain** (`GetNonce`/status), **không** `time.Now()`. Gửi qua `/send_raw_transaction` tới một node; lỗi thì thử node kế (theo danh sách, không theo timeout để "quyết định").
- `QuorumClient` mới (`pkg/parentchain/quorum_client.go`): đọc từ mọi endpoint; chấp nhận khi **≥ f+1** node trả header khớp **và** proof hợp lệ (`nomt_ffi.VerifyProof` so với `state_root` trong header); không đủ ⇒ lỗi (thà chờ còn hơn tin sai). Cấu hình `parent_chain_urls: [...]`, vẫn nhận `parent_chain_url` cũ.
- Cập nhật nơi tạo client: `cmd/simple_chain/app.go:277`, `pkg/config` (nếu có), các script `execution/scripts/test/*.go` đang trỏ `:8547`.
- H3: chốt cách xử lý đường `/tx` JSON (xem bảng H1/H3). Khuyến nghị: **tắt mặc định**, chỉ bật bằng biến môi trường cho devnet đơn node cũ; server mới không tự ký thay cluster.
- Nonce lấy từ chain cần xử lý đua (nhiều worker cùng cluster): tuần tự hóa gửi theo một khóa hoặc dùng cấp phát nonce có khóa; test đồng thời.
- **Nghiệm thu:** T-C1..T-C3 (4 server giả, 1 nói dối ⇒ đúng; 2 nói dối ⇒ lỗi chứ không trả sai; proof giả bị từ chối); test song song N goroutine gửi không trùng/nhảy nonce.

### P4 — Test chịu lỗi trên cụm thật (H6)  **[bằng chứng chính cho "chain nhiều node"]**
Chạy trên cụm `deploy/cluster/local_parent_chain/` (4 node, cổng riêng). Viết thành script tái lập được (không phụ thuộc thao tác tay) và lưu kết quả thật vào `note/parent_chain_cluster_test_report.md` (lệnh, đầu ra, số liệu; **không** ghi "PASS" nếu không chạy).
- **T-I1** 1000+ giao dịch ký gửi qua node ngẫu nhiên ⇒ mọi node cùng `state_root`/`block_hash` ở mọi block.
- **T-I2** dừng 1 node giữa chừng, tiếp tục gửi (còn 3/4) ⇒ vẫn tiến; bật lại ⇒ tự bắt kịp, trùng root.
- **T-I3** dừng 2 node ⇒ hệ thống **dừng**, không fork; bật lại ⇒ tự tiếp tục.
- **T-I4** sửa DB một node (đổi một giá trị) ⇒ bị phát hiện (`fork_detected`/fork_guard), không tiếp tục âm thầm.
- **T-I5** cùng giao dịch gửi tới 2 node ⇒ áp dụng đúng một lần.
- **T-I6** wipe dữ liệu một node ⇒ resync từ block 1 ra cùng `state_root`.
- **T-I7** (sau P3) exec cluster dùng `QuorumClient` chạy e2e khi 1 parent node bị chặn/nói dối.
- **T-I8** (sau P1) validator đưa deposit sai vào block ⇒ mọi node loại đồng nhất.
- Nếu T-I2/T-I6 lộ ra `SyncBlocks` hoặc `GetBlocksRange` thiếu/sai: sửa trong `cmd/parent_chain/app.go` + `processor/` theo D2/D4; quy tắc: block đã áp dụng thì **so** `block_hash/state_root` (khác ⇒ lỗi fork); chưa áp dụng thì tự thực thi rồi so root; không timeout.
- **Nghiệm thu:** toàn bộ T-I1..T-I8 có đầu ra thật trong báo cáo; lỗi nào phát hiện phải sửa gốc, không nới test.

### P5 — Template và triển khai N node (H5)
- `node_parent.toml.j2` / `parent_consensus.toml.j2`: `node_id = {{ parent_node_id }}`, `network_address`, `peer_rpc_port`, `peer_rpc_addresses` = các node còn lại, khóa theo node, `storage_path` và `metrics_port` riêng (mẫu `consensus/metanode/config/node_1.toml` và `deploy/cluster/local_parent_chain/node-*/node.toml`).
- Ansible: `parent_chain_nodes` nhiều host (`parent_node_id`, cổng), phân phối cùng một `parent_genesis.json`, tham số `-genesis`, mở cổng P2P/peer RPC giữa các node; unit systemd đọc `security.env`.
- Công cụ sinh khóa + genesis cho N node (mẫu: `crates/metanode-keytool`, `deploy/systemd/gen_validator_entry.py`); **khóa không commit**.
- Inventory mẫu (`inventory.example.yml`) có ví dụ 4 parent node; README cập nhật.
- **Nghiệm thu:** `./deploy_clusters.sh` với inventory 4 parent node dựng được cụm **cục bộ cổng riêng** ; mọi node lên cùng block/`state_root`.

### P9 — Chuẩn hóa toàn bộ dữ liệu đồng thuận sang proto (H13)  **[chủ dự án đã chốt]**
Nguyên tắc: giống simple_chain, **giao dịch, block, receipt, header và giá trị state đều là protobuf**; JSON chỉ còn là lớp hiển thị cho RPC đọc của con người (nếu giữ), **không** nằm trong đường đồng thuận hay trong dữ liệu được băm/ký.
- Tạo `execution/pkg/proto/parent_chain.proto` (sinh `.pb.go` + `vtproto` như các file proto hiện có, cùng cách build/ghi trong repo; **không** dùng tag 8 của `Transaction`, xem ghi chú trong `transaction.proto`). Messages tối thiểu: một message tham số cho mỗi phương thức (`DepositToFloatArgs`, `TransferFloatArgs`, `MarkClaimedArgs`, `ReclaimFloatArgs`, `RegisterAccountArgs`, `SubmitStateRootArgs`, `RegisterClusterArgs`), `CallData{method, args}` hoặc `oneof`, `Receipt`, `BlockHeader`, `BlockRecord`, và các message giá trị state (float, record, registry...).
- **Giao dịch:** vẫn là `pb.Transaction`; `Data` = proto `CallData` (thay offset cố định). Bỏ hẳn `ParentChainTx` JSON (`Marshal`/`UnmarshalParentChainTx`), bỏ nhánh JSON-trong-`Data` ở `tx_batcher.go`, bỏ `/tx` JSON (xem H3). Client `Send*` dựng và ký `pb.Transaction` rồi gửi proto raw (`/send_raw_transaction` nhận bytes; RPC node-tới-node và `QuorumClient` dùng proto, không JSON).
- **Mã hóa tất định:** mọi thứ được băm hoặc đưa vào cây (header → `block_hash`, receipt → `receipts_root`, giá trị state → `state_root`) dùng `proto.MarshalOptions{Deterministic: true}` (hoặc vtproto marshal đã kiểm chứng tất định), **kèm test vector cố định** (T-U9) và test: marshal hai lần/hai tiến trình ra byte giống nhau; không dùng `map` trong message; không có trường bị bỏ nhận biết khác nhau giữa `nil` và rỗng (chuẩn hóa trước khi marshal).
- Quy tắc tiến hóa: chỉ **thêm** trường mới với số tag mới, không đổi nghĩa/xóa; thay đổi bất kỳ trường nào tham gia hash là thay đổi đồng thuận (redeploy đồng loạt, đã chấp nhận ở môi trường dev).
- Xóa các hàm `Encode*/Decode*CallData` đóng gói tay và `encoding.go` thủ công **sau khi** proto thay thế đủ (không để hai đường song song).
- **Nghiệm thu:** `grep -rn "encoding/json" pkg/parentchain cmd/parent_chain` chỉ còn lớp hiển thị RPC đọc (nếu có); T-U9 vector pass; 4 node cùng root/hash với dữ liệu proto; test_cluster đổi sang dựng proto; `build_check.sh` sạch.

### P6 — (đã bỏ)
Không có bước di chuyển dữ liệu cũ (chủ dự án chốt 2026-10-01). Không viết công cụ export/migrate; không cần giữ tương thích DB cũ.

### P7 — Triển khai lại sạch (thay cho cutover)  
Môi trường dev, không cần bảo toàn dữ liệu: **wipe dữ liệu parent chain cũ rồi dựng cụm mới từ genesis** (genesis mới chứa committee + cluster/tài khoản khởi tạo từ công cụ sinh genesis của P2/P5), cấu hình các cụm exec trỏ `parent_chain_urls` vào cụm mới, đăng ký lại cluster/tài khoản, chạy cross-chain e2e (deposit/transfer/claim/reclaim). Wipe và redeploy phải làm **đồng loạt** (parent + exec) vì định dạng dữ liệu và giao dịch thay đổi. Không cần backup. Cập nhật runbook.

### P8 — Các việc đảm bảo chất lượng (song song, xong trước P7)
- **H7:** viết T-U6 cho đường thật: handler ghi dở rồi lỗi ⇒ không còn ghi dở, nonce xử lý đúng quy tắc đã chốt (ghi quyết định vào `note/`: nonce tăng khi tx hợp lệ chữ ký nhưng handler lỗi, để chống replay); T-U7 giả lập crash từng bước rào chắn độ bền (block DB bền trước NOMT).
- **H9:** runbook `note/runbook_parent_chain_multinode.md` (khởi động lại, node tụt lại, wipe+resync, mất quorum, đổi committee thủ công có wipe, ứng phó `fork_detected`); metrics `parent_chain_last_block`, `parent_chain_state_root`, `parent_chain_fork_detected`; tool giám sát so `/status` các node theo thời gian (mẫu `execution/cmd/tool/block_hash_checker`).
- **H10:** benchmark: tải 1 → 100 → 1000 tx/block, đo độ trễ commit và dung lượng; nếu ghi bền `Sync` + NOMT mỗi block gây vấn đề, **được phép** chỉ commit NOMT khi block có giao dịch miễn root block rỗng vẫn tất định và giống nhau mọi node (nêu rõ và test). Không dùng `Checkpoint()` trên đường nóng.
- **H11:** `grep -rn "991\|990" deploy note execution --include=*.{md,j2,toml,yml,json,go}` để xác nhận `ChainID 990` không đụng chain nào; sửa tài liệu ghi 991 cho parent chain; `PROJECT_STRUCTURE.md` cập nhật theo kết quả thật.

---

## 4. Định nghĩa "xong" (checklist chấp nhận cuối)

- [x] **Không còn đường mint float không có chứng nhận** (P1, T-I8); mọi thay đổi state chỉ bằng giao dịch đã ký kiểm trong thực thi tất định.
- [x] Không còn khóa/committee hard-code; thiếu/hỏng genesis ⇒ thoát (P2).
- [x] Exec cluster gửi giao dịch ký và đọc qua `QuorumClient` + proof; chịu được 1 parent node nói dối/chết (P3, T-C1..T-C4).
- [x] T-I1..T-I8 chạy thật trên cụm 4 node, báo cáo có đầu ra (P4).
- [x] Triển khai N node bằng ansible dựng được cụm cục bộ cổng riêng (P5).
- [x] Runbook, metrics, tool giám sát, benchmark có số liệu thật (P8).
- [x] `build_check.sh` sạch, `go build ./cmd/parent_chain/...`, `go test` các gói liên quan (kể cả `-race`) pass.
- [x] Sau khi xong, một lần redeploy sạch từ genesis (wipe parent + exec) chạy cross-chain e2e pass (P7).

## 5. Việc để sau (ngoài phạm vi)

Chữ ký tổng hợp BLS trên header (client tin một nguồn); phí/gas; đổi committee tự động/epoch có đổi validator; cắt tỉa block cũ/snapshot đồng thuận (xem `multinode_plan` mục 10).

## 6. Lời nhắn cho agent

1. Làm **P1 và P2 trước** (bảo mật, ít phụ thuộc). P1 cần chủ dự án duyệt định dạng Cert: nếu chưa có phản hồi, làm code + test và **dừng lại hỏi**, đừng tự bật.
2. Nếu chỉ còn cách "thoát kẹt" bằng chờ theo thời gian: **dừng và hỏi**.
3. KISS/YAGNI: không thêm lớp trừu tượng ngoài kế hoạch; riêng đồng thuận, tất định, độ bền thì ưu tiên **đúng** hơn gọn.
4. Báo cáo cuối: bảng H1–H11 với trạng thái (đã sửa/đã chứng minh bằng chạy thật/còn mở) và bằng chứng (lệnh, đầu ra).
