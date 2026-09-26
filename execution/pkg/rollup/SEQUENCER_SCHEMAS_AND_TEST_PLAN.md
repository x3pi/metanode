# Schema & Kế hoạch Kiểm thử — chế độ `raft` của `simple_chain` + logic rollup

> **Trạng thái:** DRAFT (2026-09-24). Bổ sung cho `SEQUENCER_STEP_BY_STEP_PLAN.md` (kế hoạch từng bước) và `SEQUENCER_DESIGN.md` (thiết kế). Tài liệu này định nghĩa **schema dữ liệu** (mục 1) và **kế hoạch kiểm thử đầy đủ** (mục 2).
>
> **Quy ước độ tin cậy** — mỗi schema/test ghi rõ một trong ba mức:
> - **[XÁC MINH]** đã đối chiếu với code hiện có (ghi vị trí).
> - **[ĐỀ XUẤT]** thiết kế mới, chưa có trong code, sẽ chốt cùng bước tương ứng.
> - **[TẠM]** phụ thuộc quyết định A0 (mở rộng Gateway hiện có hay xây `NodeFloatAccount` song song); có thể đổi.

---

## Mục lục
1. Schema
   - 1.1 Cấu hình
   - 1.2 Bản ghi batch trong log Raft
   - 1.3 Ánh xạ batch → `pb.ExecutableBlock`
   - 1.4 Metadata snapshot của FSM và bố cục lưu trữ Raft
   - 1.5 Kênh chuyển tiếp follower → leader
   - 1.6 Bản ghi giao dịch rollup (state machine) và key lưu trữ
   - 1.7 Bảng chuyển trạng thái đầy đủ
   - 1.8 Sự kiện quan sát từ Parent Chain (đưa vào state qua giao dịch)
   - 1.9 Schema phía Parent Chain và ABI **[TẠM]**
   - 1.10 Metric, log, đầu ra công cụ `rollup-cluster`
   - 1.11 Quy tắc phiên bản và tương thích
2. Kế hoạch kiểm thử
   - 2.1 Nguyên tắc và các tầng
   - 2.2 Hạ tầng test cần xây
   - 2.3 Danh mục test
   - 2.4 Bất biến toàn cục
   - 2.5 Cách chạy và tiêu chí thoát theo phase
   - 2.6 Ma trận truy vết (vấn đề `#N` ↔ test)

---

## 1. Schema

### 1.1. Cấu hình  **[ĐỀ XUẤT]**

Thêm vào `SimpleChainConfig` (`execution/pkg/config/config.go`, cùng phong cách các trường `json:"...,omitempty"` hiện có). Mặc định (không đặt) = **hành vi cũ nguyên vẹn**.

```json
{
  "consensus_mode": "raft",
  "raft": {
    "node_id": "node-0",
    "bind_address": "10.0.0.1:7100",
    "advertise_address": "10.0.0.1:7100",
    "data_dir": "./data/raft",
    "peers": [
      { "id": "node-0", "address": "10.0.0.1:7100" },
      { "id": "node-1", "address": "10.0.0.2:7100" },
      { "id": "node-2", "address": "10.0.0.3:7100" }
    ],
    "bootstrap": true,
    "heartbeat_timeout_ms": 1000,
    "election_timeout_ms": 1000,
    "leader_lease_timeout_ms": 500,
    "commit_timeout_ms": 50,
    "max_batch_bytes": 4194304,
    "propose_queue_size": 1024,
    "block_queue_size": 5000,
    "snapshot_interval_s": 120,
    "snapshot_threshold": 8192,
    "trailing_logs": 10240,
    "forward_secret_file": "./secrets/raft_forward.key",
    "sequencer_address": "0x0000000000000000000000000000000000000000"
  }
}
```

| Trường | Kiểu | Mặc định | Ràng buộc (kiểm khi khởi động, sai → thoát, không đoán) |
|---|---|---|---|
| `consensus_mode` | string | `""` | `""` (cũ, chạy Rust) hoặc `"raft"`; giá trị khác → lỗi |
| `raft.node_id` | string | — | bắt buộc, có trong `peers[].id` |
| `raft.bind_address` / `advertise_address` | `host:port` | — | bắt buộc; `advertise_address` mặc định = `bind_address` |
| `raft.data_dir` | path | — | bắt buộc; phải ghi được; **không** nằm chung thư mục với dữ liệu Rust |
| `raft.peers` | list | — | ≥ 1; `id` và `address` duy nhất; số lẻ được khuyến nghị (cảnh báo nếu chẵn) |
| `raft.bootstrap` | bool | `false` | `true` chỉ ở **một** lần khởi tạo cụm đầu tiên; bỏ qua nếu đã có state Raft |
| `heartbeat_timeout_ms` / `election_timeout_ms` | int | 1000 | `election ≥ heartbeat`; timeout **chỉ** dùng cho bầu leader/heartbeat (đã duyệt, mục 0.1 điểm 3 của kế hoạch) |
| `leader_lease_timeout_ms` | int | 500 | `≤ heartbeat_timeout_ms` |
| `commit_timeout_ms` | int | 50 | > 0 |
| `max_batch_bytes` | int | 4 MiB | > 0; **`Submit` từ chối batch vượt mức này** (không cắt ngầm) |
| `propose_queue_size` | int | 1024 | > 0; **hàng đợi có trần** (quy tắc Bounded Concurrency của `AGENTS.md`) |
| `block_queue_size` | int | 5000 | > 0; khớp `blockQueue` hiện có (`block_processor_network.go`) |
| `snapshot_*`, `trailing_logs` | int | như trên | > 0 |
| `forward_secret_file` | path | — | bắt buộc; đọc 1 lần, ≥ 32 byte |
| `sequencer_address` | 20-byte hex | — | **bắt buộc**; phải bằng địa chỉ suy ra từ khoá ký dùng chung (`Databases.BLSPrivateKey` / khoá tương ứng); lệch → thoát. Đây là `leader_address` cố định trong header block trên mọi replica |

Khi `consensus_mode = "raft"`: `rust_config_path`, `meta_node_rpc_address` bị bỏ qua (ghi cảnh báo 1 lần); không được khởi động `InitFFIBridge`.

### 1.2. Bản ghi batch trong log Raft  **[ĐỀ XUẤT]**

File mới `execution/pkg/rollup/raftfeed/proto/batch.proto`. Mã hoá bằng protobuf với `Deterministic: true`; **cùng một batch phải cho cùng một dãy byte**.

```proto
syntax = "proto3";
package rollup;
option go_package = "github.com/meta-node-blockchain/meta-node/execution/pkg/rollup/raftfeed/pb";

// Một entry của log Raft. Bất biến sau khi leader ghi.
message BatchRecord {
  uint32 schema_version = 1;   // = 1
  uint64 timestamp_ms   = 2;   // do LEADER đóng dấu lúc propose; replica không đọc đồng hồ
  bytes  txs            = 3;   // đúng output của transaction.MarshalTransactions (pb.Transactions)
  uint32 tx_count       = 4;   // kiểm chéo với số tx trong `txs`; lệch → entry bị từ chối tại Apply
  string proposer_id    = 5;   // node_id của leader, CHỈ để chẩn đoán; KHÔNG ảnh hưởng nội dung block
}
```

- `txs` là **đúng batch mà `tx_batch_forwarder` đang tạo** (`transaction.MarshalTransactions(batchTxs)`, `tx_batch_forwarder_core.go`) **[XÁC MINH]**.
- **Không có `prev_batch_hash` trong bản ghi** (khác nháp ở A2): chuỗi băm được tính **trong `FSM.Apply`** trên mọi replica (mục 1.3, trường `commit_hash`) để không phụ thuộc thứ tự propose chưa commit.
- `term` và `index` là metadata của Raft, không nằm trong bản ghi.
- **Không bao giờ propose batch rỗng** (`tx_count = 0`): Go coi commit rỗng là trường hợp riêng (`block_number = 0`), tránh phát sinh.
- Giới hạn: `len(txs) ≤ max_batch_bytes`; `tx_count ≥ 1`.

### 1.3. Ánh xạ batch → `pb.ExecutableBlock`  **[XÁC MINH các trường] / [ĐỀ XUẤT giá trị]**

Nguồn trường: `execution/pkg/proto/executor.proto`. `FSM.Apply` (mọi replica, sau khi commit) dựng một `ExecutableBlock` cho mỗi `BatchRecord`.

**Phát hiện quan trọng:** `TransactionExe.digest` **chứa bytes của giao dịch đầy đủ**, không phải hash — `ParallelUnmarshalTransactions` (`speculative_executor.go:854`) gọi `transaction.UnmarshalTransaction(ms.Digest)` rồi mới thử `UnmarshalTransactions`. Vì vậy `Apply` phải **tách batch thành từng tx** và đặt **mỗi tx vào một `TransactionExe`** (không nhét cả `pb.Transactions` vào một `digest`: protobuf khoan dung có thể khiến `UnmarshalTransaction` "thành công" giả trên dữ liệu batch).

| # | Trường | Giá trị | Ghi chú / kiểm tra |
|---|---|---|---|
| 1 | `transactions[i].digest` | bytes của tx thứ `i`, marshal xác định (`proto.MarshalOptions{Deterministic:true}`) | byte-giống-hệt giữa các replica (test T-DET-04) |
| 1 | `transactions[i].worker_id` | `0` | |
| 2 | `global_exec_index` | bộ đếm `nextGEI` của FSM | chỉ có ý nghĩa khi `is_authoritative_gei=false`; chốt ở C0 |
| 3 | `commit_index` | `uint32(raftIndex)` | **guard:** `raftIndex > 2^32-1` → dừng replica, không cắt |
| 4 | `epoch` | `0` (hằng số) | không có epoch ở chế độ này |
| 5 | `commit_timestamp_ms` | `BatchRecord.timestamp_ms` | Go dùng trường này cho header, không dùng `time.Now()` |
| 6 | `leader_author_index` | `0` | |
| 7 | `leader_address` | `raft.sequencer_address` (20 byte) | **cố định trên mọi replica** → block hash giống nhau bất kể ai là Raft leader |
| 8 | `block_number` | `lastBlockNumber + 1` do FSM theo dõi | chốt cùng `is_authoritative_gei` ở C0 |
| 9 | `commit_hash` | `keccak256("ROLLUP_BATCH_V1:" ‖ prevCommitHash ‖ be64(timestamp_ms) ‖ keccak256(txs))` | chuỗi băm tính tại `Apply`; `prevCommitHash` khởi đầu là 32 byte 0 |
| 10 | `is_authoritative_gei` | chốt ở C0 | `true`: Go tự cấp GEI (bỏ qua trường 2) |
| 11 | `system_transactions` | rỗng | |
| 12 | `authority_key` | rỗng | C0 xác nhận Go chấp nhận rỗng |
| 13 | `commit_digest` | `= commit_hash` | Go lưu để đối chiếu chéo (LAYER-6) |
| 14–15 | `rust_*_timestamp_ms` | `0` | |

- **Thứ tự trong block:** Go dedup rồi **sắp theo hash tx** (`PrepareTransactions`, `speculative_executor.go` ~845) — thứ tự trong batch không ảnh hưởng kết quả **[XÁC MINH]**.
- **Chống thực thi lặp khi restart:** trước khi đẩy vào `blockQueue`, `Apply` bỏ qua entry có `block_number ≤ storage.GetLastBlockNumber()` (Raft phát lại log sau snapshot).
- `Apply` **không được** đọc đồng hồ, số ngẫu nhiên, hay bất kỳ trạng thái cục bộ nào ngoài `BatchRecord` và metadata FSM.

### 1.4. Metadata snapshot của FSM và bố cục lưu trữ Raft  **[ĐỀ XUẤT]**

State thật nằm trong NOMT/DB, không nằm trong bộ nhớ FSM, nên snapshot Raft chỉ giữ metadata:

```proto
message FsmSnapshotMeta {
  uint32 schema_version   = 1;  // = 1
  uint64 applied_index    = 2;  // Raft index cuối đã Apply
  uint64 last_block_number = 3; // block cuối đã đẩy vào pipeline
  uint64 next_gei         = 4;  // bộ đếm GEI kế tiếp (nếu is_authoritative_gei=false)
  bytes  last_commit_hash = 5;  // 32 byte, đầu chuỗi băm
  bytes  last_state_root  = 6;  // state root của block cuối đã commit (để đối chiếu)
}
```

```
<raft.data_dir>/
  logs.db          kho log Raft (bền, fsync mỗi lần ghi — bắt buộc)
  stable.db        term, votedFor
  snapshots/       <index>-<term>-<ts>/  meta.pb (FsmSnapshotMeta) + state.json
  cluster.json     peers đã biết (chỉ đọc khi khởi động; nguồn thật là log cấu hình Raft)
```

- **Không** dùng cấu hình bỏ fsync (bài học sự cố mất điện, commit `634b0d39`: block DB không fsync khiến NOMT đi trước).
- **Quy tắc an toàn dữ liệu khi thu gọn log (bắt buộc):** `FsmSnapshotMeta.last_block_number` và `applied_index` trong snapshot phải phản ánh block **đã commit bền xuống DB** (block DB + NOMT đã fsync), **không** phải block mới nằm trong `blockQueue`/pipeline bộ nhớ. Raft chỉ được xoá các entry có `index ≤ ` chỉ số tương ứng với block đã bền. Nếu vi phạm: crash sau khi log bị thu gọn nhưng DB chưa có block ⇒ entry mất vĩnh viễn (log đã xoá, DB chưa có).
- **Khi restart**, Raft phát lại các entry sau snapshot; `Apply` bỏ qua block `≤` block đã commit bền trong DB (`storage.GetLastBlockNumber()`), và **giao lại** mọi entry chưa bền.
- **Replica tắt lâu hơn cửa sổ `trailing_logs`** không catch-up được bằng log: phải dựng lại state (C4). Cấu hình `trailing_logs` phải đủ lớn cho thời gian bảo trì dự kiến.
- Dựng state cho replica mới **không** dựa vào snapshot Raft (chỉ metadata); xem C4 (sao chép dữ liệu NOMT/DB hoặc dùng `snapshot_manager`).

### 1.5. Kênh chuyển tiếp follower → leader  **[ĐỀ XUẤT]**

`raftfeed.Submit(batch)` trên follower gọi leader qua HTTP nội bộ (cổng riêng, không dùng cổng RPC người dùng).

```
POST /raft/v1/submit
Headers:
  X-Rollup-Node:   <node_id người gửi>
  X-Rollup-Ts:     <unix ms>
  X-Rollup-Mac:    hex(HMAC-SHA256(secret, node_id ‖ ts ‖ sha256(body)))
Body: bytes của transaction.MarshalTransactions(...)   (Content-Type: application/octet-stream)
```

| Mã | Ý nghĩa | `Submit` trả về |
|---|---|---|
| 200 | leader đã đưa vào hàng đợi propose | `true` |
| 307 + `X-Rollup-Leader: <addr>` | máy nhận không còn là leader | `false` (forwarder thử lại sau 100ms, đã có sẵn) |
| 401 | MAC sai / ts lệch quá ngưỡng cấu hình | `false` + log lỗi |
| 413 | vượt `max_batch_bytes` | `false` + log lỗi (không thử lại vô ích: forwarder cần biết) |
| 429 | hàng đợi propose đầy | `false` (backpressure) |
| 503 | chưa có leader | `false` |

`Submit` **không bao giờ chặn** vô hạn: trả ngay kết quả; việc thử lại do vòng lặp hiện có của `tx_batch_forwarder` đảm nhiệm. `ts` lệch chỉ dùng để chống phát lại kênh nội bộ, **không** ảnh hưởng nội dung batch.

### 1.6. Bản ghi giao dịch rollup và key lưu trữ  **[ĐỀ XUẤT], vị trí ghi [TẠM]**

Lưu trong `SmartContractDB` (`SetStorageValue(address, key, value)` — chỉ có `StorageValue`/`SetStorageValue`/`Commit`, **không** có duyệt theo prefix **[XÁC MINH]**). `address` = `GATEWAY_CONTRACT_ADDRESS` (không thêm hằng số địa chỉ; tx ghi các bản ghi này là tx Gateway nên tự thành barrier, tuần tự). Mọi key **không được** trùng `gateway_engine_state_v1`.

| Key (`keccak256(prefix ‖ …)`) | Giá trị | Ghi chú |
|---|---|---|
| `"rollup_msg_v1" ‖ messageID(32)` | `RollupRecord` | một key/record, không dùng blob |
| `"rollup_idx_v1"` | `RollupIndex` | danh sách `messageID` chưa terminal; **có trần** `MaxInFlight` |
| `"rollup_seq_v1" ‖ be64(chainID)` | `be64(nextSourceSeq)` | cấp `sourceSeq` xác định |
| `"rollup_obs_v1" ‖ be64(kind)` | `ObservationCursor` | con trỏ đã xử lý của luồng quan sát (mục 1.8) |

```proto
message RollupRecord {
  uint32 schema_version   = 1;   // = 1
  bytes  message_id       = 2;   // 32 byte
  uint32 role             = 3;   // 1 = SENDER, 2 = RECEIVER
  uint32 state            = 4;   // enum State (1.7)
  uint64 source_chain_id  = 5;
  uint64 dest_chain_id    = 6;
  uint64 source_seq       = 7;
  bytes  sender           = 8;   // 20 byte
  bytes  target           = 9;   // 20 byte
  bytes  value            = 10;  // 32 byte big-endian (padTo32)
  bytes  payload_hash     = 11;  // 32 byte
  bytes  payload          = 12;  // tuỳ chọn (contract call); rỗng nếu chỉ chuyển giá trị
  uint64 created_block    = 13;  // block number lúc tạo
  uint64 parent_confirm_time = 14; // blockTime của Parent Chain lúc Transfer confirm (mốc tính Reclaim)
  bytes  parent_tx_hash   = 15;  // tx Transfer trên Parent Chain
  uint32 outcome          = 16;  // enum Outcome: 0 chưa có, 1 CREDITED, 2 REFUND
}

message RollupIndex {
  repeated bytes message_ids = 1; // tối đa MaxInFlight
}

message ObservationCursor {
  uint64 parent_block = 1;
  uint64 log_index    = 2;
}
```

**Công thức `MessageID`** (A2):
`keccak256("ROLLUP_MSG_V1:" ‖ be64(sourceChainID) ‖ be64(sourceSeq) ‖ sender(20) ‖ target(20) ‖ padTo32(value) ‖ keccak256(payload))`.
`sourceSeq` do state machine cấp từ `rollup_seq_v1`, **không lấy từ đồng hồ**.

**Quy tắc bắt buộc — chỉ state xác định mới vào chain state.** Thông tin chỉ có ý nghĩa cục bộ và **không xác định giữa các replica** (số lần thử lại, thời điểm thử gần nhất, kết quả gọi mạng) **không được** ghi vào `RollupRecord`; chúng nằm trong bộ nhớ hoặc kho cục bộ của worker. Nếu vi phạm, các replica sẽ có state root khác nhau.

### 1.7. Bảng chuyển trạng thái đầy đủ  **[ĐÃ CHỐT A2+B1]**

Hàm lõi (`B1`): `Next(current State, recordRole Role, event Event) (newState, []Action, error)`.
- Thuần túy (pure state machine): Không I/O, không truy cập storage, không thời gian hệ thống, hoàn toàn xác định (deterministic).
- Idempotent: Replay sự kiện cùng trạng thái không sinh lỗi và không phát lại action lần hai.
- Role Invariant: `recordRole` là thuộc tính bền vững của `RollupRecord` (`RoleSender` = 1 hoặc `RoleReceiver` = 2), không được phép là `RoleUnknown`. Mọi chuyển đổi trạng thái (kể cả từ `StateNone`) đều bắt buộc kiểm tra `recordRole` khớp với `current.Role()` và `event.Type.Role()`.
- Trạng thái `20 OBSERVED`: Đã loại bỏ hoàn toàn theo Quyết định A2 (Phương án B); hành động `ActionMarkClaimed` được phát hành trực tiếp cùng bước chuyển sang state 21 hoặc 22.

```
State (uint32):
  0  NONE
  10 LOCAL_APPLIED_PENDING_SEND     (gửi)
  11 SENT_CONFIRMED                 (gửi)
  12 RECLAIM_SUBMITTED              (gửi)
  13 REFUND_IN_TRANSIT              (gửi)
  19 CONFIRMED_SUCCESS              (gửi, terminal)
  18 CONFIRMED_REFUNDED             (gửi, terminal)
  21 MARKED_CLAIMED_PENDING_CREDIT  (nhận)
  22 MARKED_CLAIMED_PENDING_REFUND  (nhận)
  23 REFUND_SENT                    (nhận)
  29 CREDITED                       (nhận, terminal)
  28 REFUNDED                       (nhận, terminal)
  27 SKIPPED_DUP                    (nhận, terminal)
Outcome: 0 NONE, 1 CREDITED, 2 REFUND
Role: 1 SENDER, 2 RECEIVER
```

**Phía gửi**

| Từ | Sự kiện | Điều kiện | Sang | Hành động |
|---|---|---|---|---|
| NONE | `TxSubmitted` | số dư ≥ V, nonce đúng, chữ ký đúng | 10 | `DeductBalance(sender, V)`, `CreateRecord` — **cùng 1 lần ghi** |
| 10 | `ParentConfirmed` | tx Transfer đã confirm | 11 | ghi `parent_confirm_time`, `parent_tx_hash` |
| 10 | `SendFailedTransient` | lỗi mạng | 10 | *(không đổi state; retry ở worker cục bộ)* |
| 11 | `ClaimedObserved(outcome=CREDITED)` | | 19 | — |
| 11 | `ClaimedObserved(outcome=REFUND)` | | 13 | — |
| 13 | `RefundObserved` | Transfer ngược về `FA[src]` | 18 | `CreditLocal(sender, V)` |
| 11 | `ReclaimEligible` | `parentBlockTime ≥ parent_confirm_time + timeout` **và** chưa `Claimed` | 12 | `SendReclaim` |
| 12 | `ReclaimWon` | Parent Chain chấp nhận Reclaim | 18 | `CreditLocal(sender, V)` |
| 12 | `ReclaimLost` | `MessageID` đã `Claimed` | 11 | — |

**Phía nhận**

| Từ | Sự kiện | Điều kiện | Sang | Hành động |
|---|---|---|---|---|
| NONE | `CreditObserved` | `MessageID` đã có record | 27 | — |
| NONE | `CreditObserved` | mới, đích hợp lệ | 21 | `MarkClaimed(outcome=CREDITED)` |
| NONE | `CreditObserved` | mới, đích không hợp lệ / contract revert | 22 | `MarkClaimed(outcome=REFUND)` |
| 21 | `ClaimedConfirmed` | `Claimed` đã lên Parent Chain | 29 | `CreditLocal(target, V)` |
| 22 | `ClaimedConfirmed` | | 23 | `SendRefund(V)` *(chỉ `Value`, không hoàn `GasFee`)* |
| 23 | `RefundConfirmed` | | 28 | — |

- Bảng ghi `role` theo phía; cặp `(state, event)` không có trong bảng → **lỗi, không panic**.
- **Resume sau crash** (`#13`, `#9`): khi khởi động, `ScanNonTerminal` đưa mọi record chưa terminal về đúng hành động kế tiếp: 10 → gửi lại **đúng tx cũ**; 21 → tiếp tục credit (**không** `MarkClaimed` lần 2); 22 → tiếp tục hoàn tiền (**không** hoàn lần 2); 23 → chờ `RefundConfirmed`.
- **Lỗ hổng đã phát hiện khi viết bảng (đề xuất sửa ở A1/A2):** thiết kế gốc (mục 13.3 bước 5) để bên gửi "thấy `Claimed`" rồi coi là thành công hoặc hoàn tiền, nhưng `Claimed` không cho biết **kết quả**. Vì vậy `markClaimed` phải mang `outcome`, và bên gửi cần trạng thái 13 `REFUND_IN_TRANSIT` để phân biệt "đã credit" với "đang có hoàn tiền trên đường tới". Không có `outcome`, bên gửi không thể biết chờ hoàn tiền hay coi xong.

### 1.8. Sự kiện quan sát từ Parent Chain  **[ĐỀ XUẤT]**

Việc đọc Parent Chain của mỗi replica **không xác định** (thời điểm, độ trễ). Để các replica không lệch state, **mọi sự kiện quan sát phải vào chain state bằng giao dịch** (đi qua batch Raft như mọi tx khác), không được replica nào tự đổi record.

- Watcher (chỉ leader chạy) đọc Parent Chain theo cursor rồi gửi **tx hệ thống** tới Gateway: `rollupObserve(bytes eventPayload)`, ký bằng khoá dùng chung.
- `eventPayload`:

```proto
message ObservedEvent {
  uint32 kind           = 1;  // 1 CREDIT_OBSERVED, 2 CLAIMED_OBSERVED, 3 REFUND_OBSERVED,
                              // 4 PARENT_CONFIRMED, 5 RECLAIM_RESULT, 6 CLAIMED_CONFIRMED, 7 REFUND_CONFIRMED
  bytes  message_id     = 2;
  uint32 outcome        = 3;  // với CLAIMED_OBSERVED
  uint32 reclaim_won    = 4;  // với RECLAIM_RESULT: 1 thắng, 0 thua
  uint64 parent_block   = 5;
  uint64 parent_time    = 6;  // blockTime của Parent Chain — nguồn thời gian duy nhất cho Reclaim
  bytes  parent_tx_hash = 7;
  uint64 log_index      = 8;  // vị trí sự kiện trong khối (khử trùng)
}
```

- Xử lý `rollupObserve` cập nhật `ObservationCursor` và gọi `Next(...)`; sự kiện có `(parent_block, log_index)` ≤ cursor bị bỏ qua (idempotent).
- Thời gian Reclaim luôn tính từ `parent_time` do sự kiện mang vào, **không** từ đồng hồ replica.
- Mô hình tin cậy: crash-fault, một operator; replica chấp nhận quan sát của leader. Nếu sau này cần chống leader khai sai, thêm bằng chứng Merkle từ Parent Chain (ngoài phạm vi).

### 1.9. Schema phía Parent Chain và ABI  **[TẠM — chốt sau A0]**

Vị trí: `execution/pkg/cross_chain/` cạnh `gateway.go`; xử lý trong `gateway_handler.go`. **Mỗi entry một storage key riêng**, không nhét vào blob `gateway_engine_state_v1` (bài học per-key proof).

| Key | Giá trị | Ghi chú |
|---|---|---|
| `"rollup_fa_v1" ‖ be64(chainID)` | `padTo32(balance)` | `NodeFloatAccount`; không được âm |
| `"rollup_claimed_v1" ‖ messageID` | `ClaimedEntry{outcome uint8, claimant chainID, block uint64}` | chống credit/hoàn trùng (`#9`, `#10`) và chặn Reclaim (`#12`) |
| `"rollup_xfer_v1" ‖ messageID` | `TransferEntry{src, dst, value, target, blockTime, reclaimed bool}` | mốc thời gian Reclaim tính từ `blockTime` **on-chain** |
| `"rollup_inbound_v1" ‖ be64(dstChainID) ‖ be64(seq)` | `messageID` | phục vụ `getInboundTransfers` phân trang theo cursor (Parent Chain hiện chưa có API liệt kê credit đến — `rootanchor.Client` **[XÁC MINH]**) |
| `UnregisterNonce` (đã có) | `map[chainID]uint64` | **[XÁC MINH]** `gateway.go` |

ABI mới (cùng phong cách `gatewayAbi.go`; `uint256 chainId`, `uint64` cho epoch/nonce, `bytes` cho chữ ký):

| Hàm | Đầu vào | Ghi chú |
|---|---|---|
| `depositToFloat` | `bytes32 messageId, address target` (+ `value` gửi kèm) | user tự nạp; tăng `FA[node]` |
| `transferFloat` | `bytes32 messageId, uint256 dstChainId, address target, uint256 value, bytes payload, uint64 certEpoch, bytes sig, bytes bitmap` | atomic `FA[src]-=V, FA[dst]+=V`; áp velocity-limit (`#11`) |
| `markClaimed` | `bytes32 messageId, uint8 outcome, uint64 certEpoch, bytes sig, bytes bitmap` | chỉ chain đích; `outcome ∈ {1,2}`; lần 2 → từ chối |
| `refundFloat` | `bytes32 originalMessageId, uint256 value, …cert` | Transfer ngược; **không** áp velocity-limit (`#14`); chỉ hợp lệ nếu `originalMessageId` đã `Claimed` với `outcome=2` |
| `reclaimFloat` | `bytes32 messageId, …cert` | chỉ khi `blockTime ≥ transfer.blockTime + timeout` **và** chưa `Claimed`; **không** bị `DeadChains` chặn; **không** áp velocity-limit |
| view `getFloat` | `uint256 chainId` → `uint256` | |
| view `getClaimed` | `bytes32 messageId` → `(bool, uint8 outcome)` | |
| view `getInboundTransfers` | `uint256 dstChainId, uint64 cursor, uint64 limit` → `(bytes32[] ids, uint64 nextCursor)` | phân trang |

Lỗi mới (`errors.New`, cùng phong cách): `ErrFloatInsufficient`, `ErrAlreadyClaimed` (đã có), `ErrReclaimTooEarly`, `ErrReclaimAfterClaimed`, `ErrInvalidOutcome`, `ErrRefundNotOwed`, `ErrOutflowLimit`.

**Bất biến:** `Σ FA == genesis_total_supply` sau mọi tx; `FA[c] ≥ 0`; mỗi `messageId` `Claimed` tối đa 1 lần.

### 1.10. Metric, log, đầu ra công cụ  **[ĐỀ XUẤT]**

Metric (Prometheus, cùng namespace `pkg/metrics`):

| Tên | Loại | Nhãn | Ý nghĩa |
|---|---|---|---|
| `rollup_raft_is_leader` | gauge | `node_id` | 1 nếu là leader |
| `rollup_raft_term` | gauge | | |
| `rollup_raft_commit_index` / `_applied_index` | gauge | | |
| `rollup_raft_leader_changes_total` | counter | | |
| `rollup_batch_propose_queue_len` | gauge | | vs `propose_queue_size` |
| `rollup_block_queue_len` | gauge | | vs `block_queue_size` |
| `rollup_batch_apply_seconds` | histogram | | thời gian `Apply` |
| `rollup_state_root_mismatch_total` | counter | `node_id` | **cảnh báo nghiêm trọng** |
| `rollup_records` | gauge | `role,state` | số record theo trạng thái |
| `rollup_oldest_nonterminal_age_blocks` | gauge | | tuổi record chưa terminal lâu nhất (theo block, không theo giây) |
| `rollup_reclaims_total` | counter | `result` | won/lost |
| `rollup_release_backlog` | gauge | | số hành động chờ nhả |

Log: mọi dòng của module có tiền tố `[RAFT-FEED]` và trường `index=… term=… block=…`; **không** dùng `error!` trên đường nóng có thể chặn (bài học `parking_lot`/FFI: dùng `warn`).

`rollup-cluster check` (`--json`):

```json
{
  "cluster": { "leader": "node-0", "term": 7, "commit_index": 10231, "quorum": 2, "size": 3 },
  "replicas": [
    { "id": "node-0", "role": "leader",   "applied_index": 10231, "last_block": 8890, "state_root": "0x…", "lag_batches": 0 },
    { "id": "node-1", "role": "follower", "applied_index": 10230, "last_block": 8889, "state_root": "0x…", "lag_batches": 1 },
    { "id": "node-2", "role": "follower", "applied_index": 10231, "last_block": 8890, "state_root": "0x…", "lag_batches": 0 }
  ],
  "state_root_consistent": true,
  "safe_to_lose": ["node-1"],
  "warnings": []
}
```

`state_root_consistent` chỉ so các replica cùng `last_block`. `safe_to_lose` = các replica mà bỏ đi vẫn giữ đủ đa số.

### 1.11. Quy tắc phiên bản và tương thích

- Mọi proto có `schema_version`; replica **từ chối** entry/record có phiên bản lớn hơn phiên bản nó hiểu (dừng, không đoán).
- Thêm trường proto chỉ ở cuối, không đổi số thứ tự; không tái dùng số đã bỏ.
- Bất kỳ thay đổi nào làm đổi **byte** của state (record, key, thứ tự ghi) là thay đổi state root → phải nâng cấp **đồng loạt** mọi replica (bài học khi gỡ `RecoveryCommittee`).
- `consensus_mode` rỗng: **không** đọc, không tạo thư mục `raft/`, không import ảnh hưởng runtime (chứng minh bằng T-OFF-*).

---

## 2. Kế hoạch kiểm thử

### 2.1. Nguyên tắc và các tầng

1. **Mỗi bước có test trước khi coi là xong** (Definition of Done, mục 0.3 của kế hoạch): `go test -race`, `build_check.sh` sạch (Go + Rust + FFI, 0 cảnh báo).
2. **Test xác định trước, chaos sau:** logic thuần (state machine, digest) test bằng bảng; Raft test trong tiến trình; chỉ sau đó mới chaos đa tiến trình/đa máy.
3. **Mọi kịch bản lỗi phải chạy tại từng điểm chuyển trạng thái**, không chỉ vài điểm ví dụ (test kill-tại-mọi-transition, T-CH-01).
4. **Không dùng `time.Sleep` để "chờ đủ lâu" trong test logic**: dùng đồng hồ giả (`FakeClock`) và điều kiện đồng bộ; chỉ test cluster thật được phép đợi theo sự kiện có giới hạn.
5. **Test chứng minh "mặc định-tắt"** (T-OFF-*) là điều kiện bắt buộc để được sửa file cũ (H1–H6).

| Tầng | Phạm vi | Công cụ |
|---|---|---|
| L0 Tĩnh | vet, format, lint, kiểm tra diff file cũ chỉ chứa nhánh H1–H6 | `go vet`, `gofmt -l`, script kiểm diff |
| L1 Đơn vị | state machine, digest/MessageID, encode/decode proto, ánh xạ batch→block | `go test` bảng, `testing/quick` |
| L2 Thành phần | store per-key + reload, engine Parent Chain, `FSM.Apply`, `Submit` | `ChainState` tạm (`newPersistentTestChainState`), `t.TempDir()` |
| L3 Tích hợp | `BlockProcessor` không Rust; Raft 3 node **trong tiến trình**; xác định giữa 2 tiến trình | `MockCommitReceiver`, in-mem transport + kho log thật trên đĩa |
| L4 Cụm | 3 tiến trình `simple_chain` chế độ raft | script `scripts/rollup_cluster/` |
| L5 Chaos | kill/partition/chậm/đĩa đầy tại từng điểm | script + hook lỗi |
| L6 E2E | 2 chain + Root Anchor devnet, giao dịch thật qua RPC cũ | script + `ci.sh` |
| L7 Hiệu năng | thông lượng, độ trễ, vòng mạng Raft | `go test -bench`, benchstat, `tps_benchmark_e2e` |

### 2.2. Hạ tầng test cần xây

| Thành phần | Mô tả | Dùng ở |
|---|---|---|
| `FakeClock` | đồng hồ có thể tua, tiêm vào batcher/worker/Reclaim | L1–L3 |
| `FakeParentChain` | cài interface `rootanchor.Client` (cần trích interface nhỏ trong package mới, không sửa client cũ): ghi nhận lời gọi, cho lỗi theo kịch bản, mô phỏng `Claimed`, Reclaim, `blockTime` | L2–L3 |
| `FaultyTransport` | bọc transport Raft: mất gói, trễ, cô lập cặp node | L3–L5 |
| `FaultyStore` | bọc kho log: lỗi ghi, ghi chậm, "mất điện" giữa fsync | L3, L5 |
| `TwoProcessHarness` | chạy cùng chuỗi `ExecutableBlock` trên 2 tiến trình, so `state_root` + `block_hash` từng block | L3 (T-DET-*) |
| `CrashPoints` | tên điểm dừng (`AfterDeduct`, `BeforeSend`, `AfterClaimedBeforeCredit`, …) + `Inject(name)` chỉ hoạt động khi build tag `rollup_faults` | L2–L5 |
| `InvariantChecker` | chạy các bất biến mục 2.4 sau mỗi kịch bản | mọi tầng |
| Tái dùng có sẵn | `newPersistentTestChainState`, `newTx`, `marshalCallData` (`tx_processor`), `setupTestGatewayEngine`, `signUnregisterCert`, `markChainDeadForTest` (`cross_chain`), `MockCommitReceiver` (`executor/mock_executor.go`), `mock_state_test.go` (`processor`) **[XÁC MINH]** | |

Build tag `rollup_faults` **không bao giờ** bật trong bản phát hành; `CrashPoints.Inject` là no-op nếu thiếu tag (kiểm bằng test build).

Bố cục file test dự kiến:
```
execution/pkg/rollup/statemachine_test.go        # T-SM-*
execution/pkg/rollup/store_test.go               # T-ST-*
execution/pkg/rollup/raftfeed/apply_test.go      # T-AP-*, T-DET-*
execution/pkg/rollup/raftfeed/submit_test.go     # T-SUB-*
execution/pkg/rollup/raftfeed/cluster_test.go    # T-RF-*
execution/pkg/cross_chain/float_account_test.go  # T-PC-*
execution/cmd/simple_chain/processor/raft_mode_off_test.go   # T-OFF-*
scripts/rollup_cluster/*.sh                      # T-CL-*, T-CH-*, T-E2E-*
```

### 2.3. Danh mục test

Ký hiệu: **Bước** = bước trong `SEQUENCER_STEP_BY_STEP_PLAN.md`. Cột **Đạt khi** là điều kiện đo được.

#### B1 — State machine thuần (`T-SM-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-SM-01 | Duyệt **mọi cạnh** của bảng 1.7 bằng bảng test | state kế tiếp và danh sách hành động đúng từng dòng |
| T-SM-02 | Mọi cặp `(state, event)` **không** có trong bảng | trả lỗi, không panic, state không đổi |
| T-SM-03 | Idempotent: áp cùng `(state, event)` 2 lần | lần 2 cho cùng kết quả hoặc lỗi "đã áp", không hành động trùng |
| T-SM-04 | Property test: chuỗi sự kiện ngẫu nhiên hợp lệ | không bao giờ vừa `CONFIRMED_SUCCESS` vừa `CONFIRMED_REFUNDED`; luôn kết thúc ở state terminal hoặc đứng yên hợp lệ |
| T-SM-05 | `ReclaimLost` từ 12 | về 11, không mất `parent_confirm_time` |
| T-SM-06 | `ClaimedObserved(REFUND)` rồi `ReclaimEligible` | từ chối Reclaim (đã `Claimed`) |
| T-SM-07 | Package chỉ import stdlib + `common.Hash` | `go list -deps` xác nhận |

#### B2 — Store per-key (`T-ST-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-ST-01 | Ghi rồi "đóng/mở lại" `ChainState` | đọc lại đúng từng trường `RollupRecord` |
| T-ST-02 | Hai `messageID` khác nhau | không đè nhau; key không trùng `gateway_engine_state_v1` |
| T-ST-03 | `ScanNonTerminal` sau restart | trả đúng tập record chưa terminal, đúng thứ tự xác định |
| T-ST-04 | Index đạt trần `MaxInFlight` | tx tạo record mới bị từ chối có lý do rõ; không phình vô hạn |
| T-ST-05 | Trừ số dư + tạo record trong **cùng 1 lần commit**; kill giữa chừng (`CrashPoints AfterDeduct`) | sau restart: hoặc cả hai có, hoặc cả hai không (không mất dấu, không trừ 2 lần) |
| T-ST-06 | `RollupRecord` encode/decode | round-trip bằng nhau; `Deterministic` cho cùng dãy byte |
| T-ST-07 | Trường "không xác định" (số lần thử lại…) | không có trong `RollupRecord` (kiểm bằng reflection/danh sách trường) |

#### B3 — Parent Chain (`T-PC-*`) **[TẠM]**

| ID | Test | Đạt khi |
|---|---|---|
| T-PC-01 | Chuỗi `depositToFloat`/`transferFloat`/`reclaimFloat` ngẫu nhiên | `FA ≥ 0` luôn; `Σ FA == supply` sau mỗi bước |
| T-PC-02 | `reclaimFloat` trước timeout | `ErrReclaimTooEarly` |
| T-PC-03 | `reclaimFloat` sau `markClaimed` | `ErrReclaimAfterClaimed` |
| T-PC-04 | Đua Reclaim vs `markClaimed` (xử lý tuần tự, cả 2 thứ tự) | đúng 1 bên thắng; tổng bảo toàn |
| T-PC-05 | `markClaimed` lần 2 cùng `messageId` | `ErrAlreadyClaimed` (chống credit/hoàn trùng, `#9`, `#10`) |
| T-PC-06 | `outcome ∉ {1,2}` | `ErrInvalidOutcome` |
| T-PC-07 | `refundFloat` khi gốc chưa `Claimed(outcome=2)` | `ErrRefundNotOwed` |
| T-PC-08 | Chain đã `DeadChains` gọi `reclaimFloat` từ FA của nó | thành công; `transferFloat` mới bị chặn |
| T-PC-09 | Đang chạm velocity-limit mà cần `refundFloat`/`reclaimFloat` hợp lệ | vẫn đi qua (`#14`); `transferFloat` mới bị chặn (`#11`) |
| T-PC-10 | Persist qua reload `ChainState` | mọi key đọc lại đúng |
| T-PC-11 | `getInboundTransfers` phân trang | không sót, không trùng qua nhiều trang; cursor đơn điệu |
| T-PC-12 | Mỗi entry một key | không có blob chung; đọc/ghi FA của chain A không đụng key của chain B |

#### B4–B7 — Luồng giao dịch (`T-TX-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-TX-01 | Số dư không đủ | từ chối, không có record, không trừ tiền |
| T-TX-02 | Hai tx cùng user đồng thời, chỉ đủ tiền cho một | đúng một tx qua |
| T-TX-03 | Crash sau khi ghi record (`AfterRecord`) | restart: đúng 1 record `10`, số dư đã trừ |
| T-TX-04 | Worker gửi: lỗi mạng giữa chừng | retry **đúng tx cũ** cho cùng `messageID`; không tạo tx mới |
| T-TX-05 | Worker restart giữa chừng | không gửi trùng; Transfer đã confirm được coi là "đã xong", không phải lỗi |
| T-TX-06 | Credit đến tài khoản hợp lệ | `21 → 29`, số dư đích tăng đúng `V` |
| T-TX-07 | Đích không hợp lệ | `22 → 23 → 28`, hoàn đúng `Value`, **không** hoàn `GasFee` (`#5`) |
| T-TX-08 | Cùng `messageID` tới 2 lần | xử lý 1 lần (`27`), `#10` |
| T-TX-09 | Crash giữa `21` và credit (`AfterClaimedBeforeCredit`) | restart credit tiếp, **không** `markClaimed` lần 2, không credit trùng (`#13`) |
| T-TX-10 | Crash giữa `22` và gửi hoàn (`AfterClaimedBeforeRefund`) | restart gửi hoàn tiếp, không hoàn 2 lần (`#9`) |
| T-TX-11 | Đích chậm, quá timeout theo `parent_time` | `11 → 12`, Reclaim thắng → `18` + hoàn tiền người gửi (`#12`) |
| T-TX-12 | Đua Reclaim vs `markClaimed` | thua → về `11`, kết thúc theo nhánh `Claimed` |
| T-TX-13 | Sự kiện quan sát trùng `(parent_block, log_index)` | bỏ qua (idempotent) |
| T-TX-14 | Đồng hồ replica bị chỉnh | kết quả Reclaim **không đổi** (chỉ phụ thuộc `parent_time`) |
| T-TX-15 | `markClaimed` mang `outcome` | bên gửi phân biệt được thành công / hoàn tiền (`11 → 19` vs `11 → 13`) |
| T-TX-16 | **Kill tại mọi điểm chuyển trạng thái** (sinh từ bảng 1.7) | với mỗi điểm: restart → tới đúng state terminal, `InvariantChecker` sạch |

#### C0 — Xác định (`T-DET-*`), `T-AP-*`

| ID | Test | Đạt khi |
|---|---|---|
| T-DET-01 | Cùng chuỗi `ExecutableBlock` (gồm tx EVM + tx Gateway) trên **2 tiến trình**, lặp ≥ 20 lần | `state_root` **và** `block_hash` từng block giống hệt |
| T-DET-02 | Như trên nhưng có tải song song (Block-STM nhiều worker) | giống hệt |
| T-DET-03 | Đổi `GOMAXPROCS`, đổi thứ tự lên lịch goroutine | giống hệt |
| T-DET-04 | `Apply` trên 2 replica cho cùng `BatchRecord` | dãy byte `ExecutableBlock` (marshal xác định) giống hệt |
| T-DET-05 | Đảo thứ tự tx **trong batch** | kết quả block giống nhau (Go sắp theo hash sau dedup) |
| T-DET-06 | Chạy `BlockProcessor` **không** gọi `InitFFIBridge` | commit được block; không panic vì kênh authoritative `nil` |
| T-DET-07 | Link `libmetanode` nhưng không khởi động | không tạo thread/socket/file của Rust (kiểm `strace`, danh sách tiến trình/luồng) |
| T-AP-01 | Ánh xạ 1.3: từng trường | đúng bảng, đúng giá trị hằng |
| T-AP-02 | `raftIndex > 2^32-1` | replica dừng, có log lỗi rõ, **không** cắt cụt |
| T-AP-03 | `tx_count` lệch số tx thật | entry bị từ chối, replica dừng (không đoán) |
| T-AP-04 | Restart: Raft phát lại entry đã thực thi | bỏ qua entry có `block_number ≤ last`, không thực thi 2 lần |
| T-AP-05 | Batch rỗng | không bao giờ được propose; nếu vẫn tới `Apply` thì bị từ chối |
| T-AP-06 | `Apply` không đọc đồng hồ/ngẫu nhiên | kiểm bằng phân tích tĩnh (cấm `time.Now`, `math/rand` trong package) |
| T-AP-07 | **Thu gọn log vs block chưa bền:** giữ block ở `blockQueue` (chưa commit DB), ép Raft snapshot + thu gọn log, kill -9, restart | mọi entry vẫn còn (log chưa bị xoá quá mức) và được giao lại; **không** entry nào mất |
| T-AP-08 | `last_block_number` trong snapshot luôn ≤ block đã bền trong DB | kiểm bằng invariant sau mỗi snapshot, kể cả khi pipeline đang tải |

#### C1 — Mặc định-tắt (`T-OFF-*`) — **bắt buộc**

| ID | Test | Đạt khi |
|---|---|---|
| T-OFF-01 | Toàn bộ test hiện có của `processor`, `executor`, `pkg/config`, `tx_processor`, `cross_chain` | vẫn đạt, không sửa test cũ |
| T-OFF-02 | `consensus_mode` rỗng: mock `raftfeed.Enabled()`=false | H1 gọi `InitFFIBridge`; H2 gọi `executor.SubmitTransactionBatch`; H3 gọi `IsRustConsensusReadyForTransactions`; H4 gọi FFI votes/admin — **đúng như cũ** |
| T-OFF-03 | `consensus_mode` rỗng | không tạo thư mục `raft/`, không mở cổng Raft, không goroutine Raft |
| T-OFF-04 | `git diff` các file cũ | chỉ chứa nhánh `if raftfeed.Enabled()` của H1–H6 (script kiểm) |
| T-OFF-05 | Build không có tag `rollup_faults` | `CrashPoints.Inject` là no-op; không có mã lỗi trong binary phát hành |
| T-OFF-06 | `consensus_mode` sai giá trị | lỗi khởi động rõ ràng |
| T-OFF-07 | `./ci.sh run-now` (Rust) | vẫn đạt |

#### C1 — Chế độ raft, 1 node (`T-R1-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-R1-01 | Gửi tx qua RPC cũ | có receipt |
| T-R1-02 | Restart giữa chừng | không mất, không thực thi lại block |
| T-R1-03 | `Submit` khi hàng đợi đầy | trả `false` ngay, không chặn; đếm backpressure |
| T-R1-04 | `IsReady` (H3) | `false` khi chưa có leader / chưa bắt kịp; `true` khi sẵn sàng |
| T-R1-05 | H4: gọi RPC votes/admin ở chế độ raft | trả lỗi "không hỗ trợ", **không** gọi FFI |
| T-R1-06 | `sequencer_address` lệch địa chỉ suy ra từ khoá | thoát khi khởi động |

#### C2 — Raft (`T-RF-*`, `T-SUB-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-RF-01 | 3 node trong tiến trình: gửi 1000 batch | mọi replica `applied_index` bằng nhau; `state_root` bằng nhau |
| T-RF-02 | Kill leader ngay **sau** khi commit | replica còn lại có đủ batch đã commit; thực thi đúng |
| T-RF-03 | Kill leader **trước** khi commit | batch chưa commit không bao giờ được thực thi trên bất kỳ replica nào |
| T-RF-04 | 1 follower bị cô lập | cụm vẫn commit (đủ đa số) |
| T-RF-05 | Cô lập cả hai follower | leader **không** commit; không thực thi; không mất dữ liệu đã commit trước đó |
| T-RF-06 | Leader bị cô lập nhưng còn sống; phần kia bầu leader mới | leader cũ không commit được batch mới; nối lại → tự khớp log; không fork |
| T-RF-07 | Restart toàn cụm | tiếp tục đúng, không thực thi lại |
| T-RF-08 | `FaultyStore`: mất điện giữa fsync | sau restart log không hỏng; entry đã báo commit vẫn còn |
| T-RF-09 | Replica cố tình cho state root khác (tiêm lỗi) | `rollup_state_root_mismatch_total` tăng; replica đó dừng; cụm chạy tiếp |
| T-RF-10 | `commit_index` guard 32-bit | xem T-AP-02 |
| T-RF-11 | Bầu leader: đo thời gian mất leader → có leader mới | ghi lại, so với `election_timeout_ms` (không có ngưỡng cứng, chỉ báo cáo) |
| T-SUB-01 | `Submit` trên follower → leader | 200; batch vào log đúng một lần |
| T-SUB-02 | MAC sai / `ts` lệch | 401; batch không vào log |
| T-SUB-03 | Vượt `max_batch_bytes` | 413; không cắt ngầm |
| T-SUB-04 | Leader đổi giữa chừng | 307/503 → `false`; forwarder thử lại; **không mất và không nhân đôi** tx (dedup theo hash tx ở Go) |
| T-SUB-05 | Gửi cùng batch 2 lần (forwarder thử lại sau lỗi mạng mơ hồ) | tx chỉ thực thi 1 lần (dedup của Go) |

#### C3 — Hành động ra ngoài (`T-REL-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-REL-01 | Replica dừng, không đủ đa số | không Transfer nào rời node; backlog dừng ở trần |
| T-REL-02 | Có lại đa số | xả đúng thứ tự, không mất, không trùng |
| T-REL-03 | Leader chết sau khi gửi Transfer | leader mới **không gửi lại** cái đã gửi (idempotent theo `messageID`); gửi tiếp các cái còn thiếu |
| T-REL-04 | Mọi replica cùng gửi 1 Transfer | Parent Chain ghi đúng 1 lần |
| T-REL-05 | Receipt | chỉ tồn tại sau khi block đã thực thi (đã commit) |
| T-REL-06 | Backlog chạm trần | tx mới bị từ chối/làm chậm, không phình bộ nhớ |

#### C4 — Đổi leader, thay replica (`T-LD-*`, `T-CL-*`)

| ID | Test | Đạt khi |
|---|---|---|
| T-LD-01 | `transfer-leader` dưới tải | không mất/không trùng giao dịch; leader cũ thành follower |
| T-LD-02 | `transfer-leader` khi replica đích chưa bắt kịp | công cụ **từ chối** |
| T-LD-03 | `remove-replica` sẽ làm mất đa số | công cụ **từ chối** |
| T-LD-04 | `add-replica`: dựng state từ replica khác + catch-up log | replica mới `state_root` khớp; tính vào đa số **sau** khi bắt kịp |
| T-LD-05 | Thay replica chết vĩnh viễn | cụm trở lại đủ N; không gián đoạn giao dịch đang bay |
| T-LD-06 | `--dry-run` | in đúng các bước, không đổi gì |
| T-CL-01 | `check` trên cụm khoẻ | JSON đúng schema 1.10; `state_root_consistent=true` |
| T-CL-02 | Khoá ký dùng chung | mọi replica ký ra cùng chữ ký/địa chỉ; khoá không đi qua mạng |

#### C5 — Chaos (`T-CH-*`)

| ID | Kịch bản | Đạt khi |
|---|---|---|
| T-CH-01 | Kill leader tại **từng** điểm của `CrashPoints` | `InvariantChecker` sạch sau mỗi lần; mọi tx đã có receipt hoặc đã gửi Parent Chain đều còn |
| T-CH-02 | Kill leader + 1 replica (N=3) | dừng, không thực thi, không mất dữ liệu đã commit; có lại đa số thì chạy tiếp |
| T-CH-03 | Cô lập leader còn sống | không hành động mới ra ngoài; nối lại tự khớp |
| T-CH-04 | Replica chậm rồi bắt kịp | `commit_index` đúng, backlog xả đúng |
| T-CH-05 | Tắt hẳn 1 máy (mỗi replica 1 máy) | cụm tiếp tục |
| T-CH-06 | Đĩa đầy trên 1 replica | replica đó dừng an toàn; cụm còn đa số vẫn chạy |
| T-CH-07 | Mất điện đột ngột toàn cụm | khởi động lại: không fork, không thực thi lại, không mất entry đã commit |
| T-CH-08 | Lặp mỗi kịch bản ≥ 20 lần | không có lần nào vi phạm bất biến (không chấp nhận "flake") |
| T-CH-09 | Cắt điện thật (rút nguồn hoặc `echo b > /proc/sysrq-trigger`) giữa lúc ghi log và giữa lúc commit DB, lặp nhiều lần, có tải | mọi entry mà Raft đã báo commit đều còn; DB và log khớp sau restart; không fork |
| T-CH-10 | Tắt 1 replica lâu hơn cửa sổ `trailing_logs`, rồi bật lại | replica bị buộc dựng lại state (không nhận log thiếu); không gây mất dữ liệu ở replica khác |

#### E2E, bảo mật, hiệu năng

| ID | Test | Đạt khi |
|---|---|---|
| T-E2E-01 | Transfer thành công 2 chain qua RPC cũ + Root Anchor devnet | `CONFIRMED_SUCCESS` + số dư đúng 2 phía |
| T-E2E-02 | Đích không hợp lệ | hoàn đúng `Value`, không hoàn `GasFee` |
| T-E2E-03 | Đích ngừng xử lý | Reclaim thành công không cần đích hợp tác |
| T-E2E-04 | Đổi leader giữa chừng một Transfer | hoàn tất đúng, không trùng |
| T-E2E-05 | `Σ FA == supply` xuyên suốt | `invariant_monitor_daemon.py` không báo lỗi |
| T-SEC-01 | Cert huỷ đăng ký bị phát lại sau đăng ký lại | `ErrInvalidUnregisterNonce` (đã có test `TestUnregisterChain_CertCannotBeReplayedAfterReRegistration`) |
| T-SEC-02 | Kênh chuyển tiếp thiếu/ sai MAC | 401 |
| T-SEC-03 | Khoá dùng chung không xuất hiện trong log/metric/đầu ra `check` | grep sạch |
| T-SEC-04 | `Apply` không có nguồn không xác định | kiểm tĩnh (T-AP-06) |
| T-PF-01 | Baseline: `consensus_mode` rỗng so với trước khi thêm H1–H6 | không chậm hơn ngoài sai số (benchstat) |
| T-PF-02 | Thông lượng chế độ raft, N=1 và N=3 | báo cáo tx/s và p50/p99 độ trễ tới receipt; **không đặt ngưỡng trước khi có số đo** |
| T-PF-03 | Ảnh hưởng tx Gateway tuần tự (barrier) + blob `GatewayEngine` | báo cáo thông lượng cross-node |
| T-PF-04 | Chi phí vòng mạng Raft mỗi batch | báo cáo |

### 2.4. Bất biến toàn cục (`InvariantChecker`)

Sau **mọi** kịch bản L3–L6 phải kiểm (11 bất biến):

1. **Tiền tố log giống nhau:** với mọi cặp replica, phần chung của log đã commit giống hệt.
2. **State root khớp:** cùng `block_number` ⇒ cùng `state_root` và `block_hash` trên mọi replica.
3. **`commit_index` chỉ tiến**, không lùi.
4. **Mỗi tx thực thi tối đa một lần** (trên mỗi replica) và mọi replica thực thi cùng tập tx.
5. **Không hành động ra ngoài từ batch chưa commit.**
6. **`Σ NodeFloatAccount == supply`** và `FA ≥ 0`.
7. **Mỗi `messageID` `Claimed` tối đa 1 lần; credit/hoàn tối đa 1 lần.**
8. **Không entry nào bị thu gọn khỏi log trước khi block tương ứng đã bền trong DB.**
9. **Không record nào kẹt vô hạn:** mọi record chưa terminal có hành động kế tiếp khả thi hoặc đang chờ đúng sự kiện.
10. **`leader_address` trong mọi block = `sequencer_address`.**
11. **Số tx đã có receipt ⊆ số tx đã commit.**

### 2.5. Cách chạy và tiêu chí thoát theo phase

```bash
# L0–L2 (nhanh, chạy mỗi commit)
cd execution && go vet ./pkg/rollup/... ./pkg/cross_chain/... && gofmt -l pkg/rollup pkg/cross_chain
go test -race -count=1 ./pkg/rollup/... ./pkg/cross_chain/... ./pkg/blockchain/tx_processor/...

# L3 (Raft trong tiến trình + xác định 2 tiến trình)
go test -race -count=1 -tags rollup_faults ./pkg/rollup/raftfeed/... -run 'T_DET|T_RF|T_SUB|T_AP'

# Mặc định-tắt (bắt buộc trước khi merge bất kỳ điểm móc H1–H6)
go test -count=1 ./cmd/simple_chain/processor/... ./executor/... ./pkg/config/...
bash scripts/rollup_cluster/check_default_off_diff.sh

# Toàn bộ build (Go + Rust + FFI)
cd ../consensus/metanode/scripts && ./build_check.sh

# L4–L6 (cụm thật, chaos, e2e)
bash scripts/rollup_cluster/run_cluster.sh 3
bash scripts/rollup_cluster/chaos.sh --iterations 20
./ci.sh run-now
```

| Phase | Điều kiện thoát |
|---|---|
| **B (B1–B9)** | T-SM, T-ST, T-PC, T-TX đạt; T-E2E-01..03 đạt lặp lại; `build_check.sh` sạch |
| **C0** | T-DET-01..07 đạt, có tài liệu kết luận; **nếu T-DET-01/02 lệch: dừng, không sang C1** |
| **C1** | T-OFF-01..07 và T-R1-* đạt; diff file cũ chỉ gồm H1–H6 |
| **C2** | T-RF-*, T-SUB-*, T-AP-* đạt (gồm T-AP-07/08 về thu gọn log; lặp ≥ 20 lần, 0 flake) |
| **C3** | T-REL-* đạt |
| **C4** | T-LD-*, T-CL-* đạt |
| **C5** | T-CH-* đạt (gồm cắt điện thật T-CH-09), mọi kịch bản lặp ≥ 20 lần; T-E2E-04..05 đạt; `Σ FA == supply` xuyên suốt; `ci.sh run-now` sạch |
| **C6** | T-PF-* có số đo được ghi lại; `PROJECT_STRUCTURE.md` cập nhật |

### 2.6. Ma trận truy vết — vấn đề `#N` ↔ test

| Vấn đề | Nội dung | Test |
|---|---|---|
| #5 | `GasFee` không hoàn khi thất bại | T-TX-07, T-E2E-02 |
| #8 | Custody 100% device key / khoá ký | T-CL-02, T-SEC-03 |
| #9 | Chống hoàn tiền 2 lần | T-PC-05, T-TX-10 |
| #10 | Chống credit trùng | T-PC-05, T-TX-08 |
| #11 | Velocity-limit outflow | T-PC-09 |
| #12 | Timeout & Reclaim | T-PC-02..04, T-TX-11..12, T-E2E-03 |
| #13 | Crash-recovery giữa `Claimed` và credit | T-TX-09, T-TX-16 |
| #14 | Velocity-limit loại trừ hoàn tiền/Reclaim | T-PC-09 |
| #15 | Signed Receipt / report | ngoài phạm vi các bước này (giai đoạn E) |
| #16 | `SlashOnEquivocation` bắt double-sign `AccountTreeRoot` | test hiện có `TestSecurityBond_SlashOnEquivocation_*` (đã đạt) |
| Zero-Fork | Không dispatch khi thiếu bằng chứng; halt-not-guess | T-RF-03, T-RF-05, T-RF-09, T-AP-02, T-AP-03, T-CH-07 |
| Mặc định-tắt | Không ảnh hưởng code cũ | T-OFF-01..07 |
| Bounded concurrency | Mọi queue có trần | T-ST-04, T-R1-03, T-REL-06 |

**Điểm chưa có test và lý do:** `#1–#4, #6, #7, #15–#16 (mới)` thuộc phạm vi thiết kế/giai đoạn E hoặc đã có test riêng; ma trận trên chỉ liệt kê các vấn đề mà các bước B–C hiện thực trực tiếp.
