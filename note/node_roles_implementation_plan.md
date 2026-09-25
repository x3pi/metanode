# Kế hoạch triển khai cấu hình Sub / Master / Consensus

Ngày cập nhật: 2026-09-24  
Trạng thái: **Kế hoạch kiến trúc & kiểm thử toàn diện (v2.1) — Đã hợp nhất phản biện Reviewer (R1–R9, S1–S3) và bổ sung chi tiết các bài test.**  
Repo triển khai: `/home/abc/nhat/con-chain-v2/metanode`  
Repo tham chiếu luồng nghiệp vụ master–sub: `/home/abc/nhat/consensus-chain/mtn-2026`  

---

## 📌 1. LÝ DO SỬA ĐỔI & PHẢN HỒI REVIEWER (R1–R9, S1–S3)

Bản kế hoạch v2.1 được hoàn thiện dựa trên sự đồng thuận tuyệt đối với các điểm phản biện kỹ thuật sâu sắc của Reviewer tại [note/node_roles_v2_review_feedback.md](file:///home/abc/nhat/con-chain-v2/metanode/note/node_roles_v2_review_feedback.md):

1. **Bỏ Quorum 2/3 ACK — Dùng Asynchronous Streaming Replication:** Master là Single Source of Truth (Sequencer duy nhất). Master thực thi, lưu DB Master rồi stream ngay từng block xuống các Sub, không chờ ACK từ Sub.
2. **Khóa loại dữ liệu (R1 — Data Family Guard):** Tạo metadata file `data_family.json` (`master_sub` hoặc `consensus`) trong thư mục data. Ngăn chặn tuyệt đối việc operator đổi role mà chạy nhầm trên DB cũ.
3. **Trình tự Failover chuẩn xác (R2):** Khi Master chết: Cô lập Master cũ -> Chọn Sub có block hoàn chỉnh cao nhất -> Bật Sub đó lên Master mới nhưng **giữ Ingress đóng** -> Các Sub còn lại kết nối kéo block thiếu -> Khi tất cả đạt cùng checkpoint thì mới **mở Ingress nhận tx từ client**.
4. **Giới hạn của Async Replication (R3):** Ghi rõ mô hình rủi ro: Nếu Master chết đồng thời với Sub chứa block mới nhất bị mất kết nối, hệ thống sẽ tạm dừng chờ operator can thiệp phục hồi dữ liệu; không hứa suông "zero loss" trong kịch bản thảm họa kép.
5. **Làm rõ phạm vi FFI (R4):** Chỉ tắt **Rust BFT Consensus Runtime** (`executor.InitFFIBridge` / `metanode_start_consensus`). Thư viện Rust NOMT cho state trie (`pkg/nomt_ffi`) và C++ MVM vẫn hoạt động bình thường qua FFI/CGO nếu được cấu hình.
6. **Khái niệm Block hoàn chỉnh (R5 — Complete Block):** Chỉ cập nhật `last_complete_height` khi cả block header, state trie changes và receipts index đã được ghi bền vững vào disk (do `metanode` dùng `pebble.NoSync` và flush 5s).
7. **Xác thực phòng thủ tại Sub (R6):** Sub kiểm tra block nhận từ Master: đúng cluster, đúng chain ID, cùng height/hash thì bỏ qua (idempotent), cùng height khác hash thì báo lỗi dừng, có gap thì kích hoạt catch-up.
8. **Hàng đợi có giới hạn (R7 — Bounded Concurrency):** Bounded queue cho forward tx và stream block (tránh OOM RAM khi Master lag hoặc Sub chậm). Đóng kết nối an toàn với `context.Context`.
9. **Khớp proto chuẩn (R8):** Dùng đúng message proto có sẵn `GetBlocksRangeRequest { from_block, to_block }` để Sub kéo bù block từ Master.
10. **Tận dụng TxPool có sẵn (S1):** Dùng bộ index tx hash sẵn có trong `pkg/transaction_pool/` để loại bỏ trùng lặp tx, không tạo thêm cache thừa.
11. **Chuẩn RPC (S2):** `eth_sendRawTransaction` trả `TxHash` ngay khi Master nhận tx vào pool; `eth_getTransactionReceipt` chỉ trả trạng thái thành công sau khi Sub đã áp dụng block chứa tx đó vào DB cục bộ.
12. **Public Chain trung tính (S3):** Dùng tên chung "Public Chain", hoãn hàng đợi outbox sang Phase 7 (YAGNI).

---

## 2. Kiến trúc đích

```text
[ Client ]
   │
   │ 1. Gửi Transaction (eth_sendRawTransaction)
   ▼
[ Sub Node ] (Sub 1, Sub 2 hoặc Sub 3...)
   │ 2. Stateless Verification (Kiểm tra format, verify chữ ký ECDSA/BLS)
   │ 3. Đưa vào Bounded Forward Queue -> Gửi lên Master
   ▼
[ Master Node ] (Sequencer & Execution Engine)
   │ 4. Dedup qua TxPool + Stateful Verification (Nonce, Balance)
   │ 5. Thực thi EVM/Block-STM (State Transition) -> Lưu DB Master
   │ 6. Đóng Block N -> Phát sóng (Stream) Block N xuống các Sub đang kết nối
   ├───┬───────────────┐
   │   │               │
   ▼   ▼               ▼
[ Sub 1 ]           [ Sub 2 ]           [ Sub 3 ]
   │ 7. Nhận Block N từ Master (Xác thực nguồn, kiểm tra tính liên tục)
   │ 8. Ghi tuần tự: Block N + State Changes + Receipts vào DB Sub
   ▼
[ Client ]
   ▲ 9. Tra cứu eth_getTransactionReceipt, eth_call, eth_getBalance từ Sub
```

### Bảng phân định trách nhiệm giữa ba vai trò

| Chức năng | Sub Node | Master Node | Consensus Node |
|---|---|---|---|
| **Cổng RPC nhận TX client** | Có (chính) | Tùy chọn (nội bộ/test) | Có (cho public chain) |
| **Verify chữ ký / Chain ID** | Có (Stateless verify) | Kiểm tra dedup & verify lại | Theo chuẩn consensus |
| **Kiểm tra Nonce / Balance** | Không (tránh drop nhầm khi lag state) | Có (Stateful verify tại mempool) | Theo commit state |
| **Thực thi giao dịch ghi State** | Không (chỉ apply state diff từ Master) | Có (độc quyền trong cụm) | Theo commit BFT |
| **Tự đóng Block** | Không | Có (đóng block cục bộ cho cụm) | Theo Rust DAG/BFT |
| **Rust BFT Consensus (FFI)** | **TẮT HOÀN TOÀN** | **TẮT HOÀN TOÀN** | **BẬT** |
| **Rust NOMT Storage (FFI)** | Vẫn hoạt động nếu config nomt | Vẫn hoạt động nếu config nomt | Vẫn hoạt động nếu config nomt |
| **Replication Block / State** | Nhận và lưu tuần tự từng block | Stream block cho nhiều Sub | Sync qua Rust P2P |
| **Data Directory** | `data_family: master_sub` | `data_family: master_sub` | `data_family: consensus` |

---

## 3. Cấu hình đề xuất & Khóa dữ liệu (Data Family Lock)

### 3.1. Các file cấu hình mẫu

* **Sub Node (`sub.json`):**
  ```json
  {
    "node_role": "sub",
    "node_id": "sub-1",
    "cluster_id": "private-cluster-a",
    "connection_address": "0.0.0.0:4201",
    "rpc_port": ":8545",
    "nodes": {
      "master_address": "10.0.0.10:4101"
    },
    "Databases": {
      "RootPath": "./data_sub_1"
    }
  }
  ```

* **Master Node (`master.json`):**
  ```json
  {
    "node_role": "master",
    "node_id": "master-0",
    "cluster_id": "private-cluster-a",
    "connection_address": "0.0.0.0:4101",
    "rpc_port": ":8645",
    "Databases": {
      "RootPath": "./data_master"
    }
  }
  ```

* **Consensus Node (`consensus.json`):**
  ```json
  {
    "node_role": "consensus",
    "node_id": "validator-1",
    "connection_address": "0.0.0.0:4301",
    "rpc_port": ":8745",
    "rust_config_path": "./consensus/node-1.toml",
    "Databases": {
      "RootPath": "./data_consensus_1"
    }
  }
  ```

### 3.2. Cơ chế Khóa dữ liệu (Data Family Lock — R1)
Tại thư mục `Databases.RootPath`, hệ thống sẽ lưu file metadata `data_family.json`:
```json
{
  "data_family": "master_sub",
  "chain_id": "1337",
  "genesis_hash": "0xabc...",
  "cluster_id": "private-cluster-a"
}
```
* **Quy tắc kiểm tra lúc startup (trước khi mở bất kỳ Pebble/NOMT DB nào):**
  1. Nếu thư mục data rỗng: Khởi tạo file `data_family.json` theo `node_role` tương ứng (`master_sub` cho master/sub; `consensus` cho consensus).
  2. Nếu thư mục data đã có dữ liệu: Đọc file `data_family.json`.
     - Node role `master` hoặc `sub` mà gặp `data_family == "consensus"` -> **Dừng ngay lập tức (Panic/Fatal)** kèm thông báo: *"FATAL: Cannot run master/sub on consensus database. Please use a clean directory."*
     - Node role `consensus` mà gặp `data_family == "master_sub"` -> **Dừng ngay lập tức** kèm thông báo lỗi tương tự.

---

## 4. Chi tiết luồng nghiệp vụ Master – Sub

### 4.1. Luồng tiếp nhận & Chuyển tiếp giao dịch (Ingress & Forwarding)
1. **Tại Sub:**
   - Nhận tx từ RPC client (`eth_sendRawTransaction`).
   - Kiểm tra định dạng thô, kiểm tra Chain ID và verify chữ ký ECDSA/BLS (Stateless). Nếu chữ ký sai -> trả lỗi ngay cho client.
   - Nếu hợp lệ: Đưa vào hàng đợi chuyển tiếp có giới hạn (**Bounded Forward Queue**, tối đa 10,000 txs trong RAM). Nếu queue đầy, trả mã lỗi retryable / backpressure cho client.
   - Gom batch tx nhỏ (tối đa 10ms hoặc 50 txs) gửi qua TCP socket lên Master.
   - Trả mã `TxHash` về cho client với trạng thái Pending.

2. **Tại Master:**
   - Nhận batch tx từ Sub.
   - Kiểm tra trùng lặp thông qua `pkg/transaction_pool/`: Nếu tx đã tồn tại trong pool hoặc đã thực thi -> drop ngay ở network boundary (S1).
   - Kiểm tra Stateful: Kiểm tra nonce hợp lệ theo state hiện tại của Master, kiểm tra số dư gas.
   - Đưa tx hợp lệ vào mempool thực thi.

### 4.2. Luồng thực thi & Đóng block tại Master
- Master có Block Producer gom các tx hợp lệ từ mempool.
- Thực thi EVM (Block-STM song song hoặc tuần tự), tính toán State Root, Block Hash, tạo Receipts.
- Lưu bền vững Block, State changes, Receipts vào DB của Master và flush disk.
- Cập nhật `last_complete_height`.
- Stream payload block (`raw_block_bytes` kèm state diffs/receipts) xuống tất cả các Sub đang kết nối qua Bounded Channel (tối đa 100 blocks in-flight).

### 4.3. Luồng nhận Block & Áp dụng State tại Sub
- Sub nhận block từ Master stream:
  - **Kiểm tra phòng thủ (R6):** Kiểm tra `cluster_id`, `chain_id`.
  - Cùng height và cùng hash: Bỏ qua (idempotent).
  - Cùng height nhưng khác hash: Báo lỗi xung đột nghiêm trọng và dừng apply.
  - Nếu đúng là Block $N = last\_complete\_height + 1$: Tiến hành apply state changes và lưu receipts vào DB của Sub. Hoàn tất -> cập nhật `last_complete_height`.
  - Nếu phát hiện bị hổng block (Gap, ví dụ nhận $N+5$ trong khi đang ở $N$): Sub gửi message `GetBlocksRangeRequest { from_block: N+1, to_block: N+5 }` (R8) lên Master để kéo các block thiếu về bù tuần tự.
- **Phục vụ Client:**
  - Client gọi `eth_getTransactionReceipt(txHash)`: Sub tra cứu trong DB cục bộ, trả về receipt thành công/thất bại kèm logs.
  - Client gọi `eth_getBalance`, `eth_call`: Sub đọc trực tiếp từ State DB cục bộ đã đồng bộ.

---

## 5. Quy trình Chuyển đổi & Khắc phục sự cố (Failover — R2)

### 5.1. Xử lý khi Master gặp sự cố đột ngột (Crash / Outage)
1. **Bước 1 — Cô lập Master cũ:** Tắt tiến trình Master cũ hoặc ngắt mạng (tránh tình trạng split-brain).
2. **Bước 2 — Xác định Sub có block hoàn chỉnh cao nhất:** 
   - Kiểm tra `last_complete_height` và `last_complete_hash` giữa các Sub truy cập được (ví dụ Sub 1 = 100, Sub 2 = 99, Sub 3 = 98).
   - Chọn Sub 1 làm Master mới.
3. **Bước 3 — Khởi động Master mới với Ingress ĐÓNG (`ready = false`):**
   - Đổi file cấu hình của Sub 1 thành `"node_role": "master"`.
   - Khởi động Sub 1. Master mới mở cổng nhận kết nối từ các Sub khác nhưng **chưa mở cổng RPC nhận tx mới từ client**.
4. **Bước 4 — Các Sub còn lại kết nối và đồng bộ bù (Catch-up):**
   - Đổi `master_address` trên Sub 2 và Sub 3 trỏ về IP của Sub 1.
   - Sub 2 gửi `GetBlocksRangeRequest(100, 100)` -> kéo block 100 từ Sub 1.
   - Sub 3 gửi `GetBlocksRangeRequest(99, 100)` -> kéo block 99, 100 từ Sub 1.
5. **Bước 5 — Đồng bộ hoàn tất & Mở lại Ingress (`ready = true`):**
   - Khi các Sub trong cụm đạt cùng checkpoint block 100 -> Master 1 chuyển trạng thái sang `ready = true`, mở cổng RPC tiếp nhận giao dịch mới từ client.
6. **Bước 6 — Master cũ quay trở lại:**
   - Master cũ chỉ được khởi động lại dưới vai trò **Sub**, trỏ `master_address` về Sub 1 và pull dữ liệu từ Sub 1. Tuyệt đối không tự động phục hồi quyền master.

---

## 6. Kế hoạch triển khai theo từng giai đoạn (Phases)

| Giai đoạn | Nội dung công việc | Điều kiện hoàn tất |
|---|---|---|
| **Phase 1: Config & Data Guard (R1)** | Thêm `NodeRole`, parser/validator trong `pkg/config`. Thêm kiểm tra `data_family.json` trước khi mở DB. | Khởi động sai role hoặc sai data family bị dừng ngay lập tức |
| **Phase 2: Lifecycle & FFI Isolation (R4)** | Phân nhánh startup trong `app.go`. Chặn gọi `executor.InitFFIBridge` và socket Rust ở Sub/Master. Giữ nguyên NOMT FFI nếu dùng nomt backend. | Sub/Master khởi động nhẹ, không chạy Rust BFT consensus |
| **Phase 3: Master Execution Pipeline (S1, R7)** | Bounded mempool, tận dụng TxPool dedup, Block Producer nội bộ đóng block và lưu DB mà không phụ thuộc vào Rust DAG. | Master tự nhận tx, tự đóng block, tự thực thi và lưu DB bền vững |
| **Phase 4: Sub Forwarding & Block Streaming (R6, R7, S2)** | Bounded forward queue tại Sub (verify chữ ký -> forward Master); Master stream block -> Sub nhận tuần tự -> ghi DB Sub -> trả receipt. | 1 Master + 3 Sub chạy mượt: gửi tx vào Sub, Master đóng block, Sub nhận block và trả receipt |
| **Phase 5: Catch-up Protocol (R8)** | Triển khai handler cho `GetBlocksRangeRequest` tại Master và sync adapter tại Sub để kéo bù block khi rớt mạng. | Sub bị tắt, Master chạy thêm block, bật lại Sub -> Sub tự kéo bù đủ block |
| **Phase 6: Failover & Handover (R2, R5)** | Triển khai cơ chế đóng/mở Ingress khi startup, script/runbook chuyển đổi Sub cao nhất lên Master và đồng bộ các Sub còn lại. | Kịch bản chuyển Sub lên Master hoạt động trơn tru theo đúng 6 bước ở mục 5 |
| **Phase 7: Kiểm tra Build & Chạy Test Suite** | Chạy toàn bộ Unit Tests, Cluster Integration Tests và `build_check.sh`. | Toàn bộ bài test pass sạch 100%, không cảnh báo |
| **Phase 8 (Tương lai): Public Chain Outbox** | Thiết kế hàng đợi đẩy state hash/checkpoint từ Master lên Public Chain. | Triển khai sau khi cụm Master–Sub đã vận hành ổn định |

---

## 7. 🧪 KẾ HOẠCH KIỂM THỬ TOÀN DIỆN (COMPREHENSIVE TEST SUITE)

Để đảm bảo code viết ra hoạt động chuẩn xác và không phát sinh lỗi tiềm ẩn, kế hoạch bổ sung **5 bộ bài test tự động hóa (Test Suites)**:

### 7.1. Bộ Unit Test (Go Unit Tests — `go test`)

1. **`TestConfigNodeRoles` (`execution/pkg/config/config_test.go`):**
   - Test parse JSON hợp lệ cho 3 role: `sub`, `master`, `consensus`.
   - Test validate lỗi: `sub` thiếu `master_address` -> fail; `consensus` thiếu `rust_config_path` -> fail; role lạ (ví dụ `"validator"`) -> fail.
2. **`TestDataFamilyGuard` (`execution/pkg/storage/data_family_test.go`):**
   - Tạo thư mục tạm, chạy init với role `master` -> kiểm tra file `data_family.json` có `master_sub`.
   - Dùng thư mục đó khởi động với role `consensus` -> kiểm tra hàm trả về lỗi `ErrDataFamilyMismatch`, không được phép mở DB.
   - Dùng thư mục đó khởi động với role `sub` -> thành công.
3. **`TestTxPoolDedup` (`execution/pkg/transaction_pool/dedup_test.go`):**
   - Đưa cùng 1 tx (cùng tx hash) vào pool 2 lần liên tiếp -> lần 2 bị bỏ qua, không tăng kích thước pool.
4. **`TestBoundedForwardQueue` (`execution/cmd/simple_chain/processor/tx_forwarder_test.go`):**
   - Giả lập Master offline. Đẩy 10,001 txs vào queue có dung lượng 10,000.
   - Kiểm tra tx thứ 10,001 bị từ chối với lỗi `ErrQueueFull`, bộ nhớ RAM không tăng đột biến.
5. **`TestSubBlockEnvelopeVerification` (`execution/cmd/simple_chain/processor/sub_block_apply_test.go`):**
   - Block có `cluster_id` khác -> từ chối.
   - Block $N$ có hash giống block $N$ đã lưu -> bỏ qua (idempotent test).
   - Block $N$ có hash khác block $N$ đã lưu -> báo lỗi xung đột `ErrBlockHashConflict`.

---

### 7.2. Bộ Test Cách ly FFI & Runtime (`test_ffi_isolation.sh`)
* **Mục đích:** Chứng minh rằng Master và Sub tuyệt đối không chạy ngầm Rust BFT Consensus.
* **Cách thực hiện:**
  - Khởi động 1 node `sub` và 1 node `master` với log level `DEBUG`.
  - Script kiểm tra log đầu ra:
    - ✅ Không có log: `[FFI BRIDGE]`, `InitFFIBridge`, `metanode_start_consensus`.
    - ✅ Không có file socket consensus unix domain socket được tạo.
    - ✅ Không có port consensus BFT (ví dụ 4301) bị chiếm dụng.
  - Khởi động node `consensus`:
    - ✅ Log hiển thị `[FFI BRIDGE] MetaNode Consensus initialized via FFI` bình thường.

---

### 7.3. Bộ Test Tích hợp Cụm 1 Master – 3 Sub (`scripts/test_master_sub_cluster.sh`)
* **Mục đích:** Kiểm tra toàn bộ luồng gửi tx, thực thi và replication trong điều kiện thực tế.
* **Kịch bản chạy:**
  1. Script tự động khởi tạo 4 data directory sạch (`/tmp/node_master`, `/tmp/node_sub1`, `/tmp/node_sub2`, `/tmp/node_sub3`).
  2. Bật Master (cổng RPC 8645, socket 4101).
  3. Bật Sub 1, Sub 2, Sub 3 (cổng RPC 8545, 8546, 8547; đều trỏ `master_address` về `127.0.0.1:4101`).
  4. Gửi 1 giao dịch chuyển native coin hợp lệ vào **Sub 1** qua RPC `eth_sendRawTransaction`.
  5. **Kiểm tra tự động:**
     - Sub 1 trả về `TxHash` ngay.
     - Log Master hiển thị: Nhận tx từ Sub 1 -> Thực thi EVM -> Tạo Block #1 -> Stream xuống Sub 1, 2, 3.
     - Sau 1 giây, script query RPC `eth_getBlockByNumber("latest")` trên cả Sub 1, Sub 2, Sub 3:
       - Cả 3 Sub đều phải có `number = 1`.
       - `hash` của Block #1 trên cả 3 Sub và Master phải **giống hệt nhau từng byte**.
     - Query `eth_getTransactionReceipt(TxHash)` trên **Sub 2** và **Sub 3**: Cả hai đều phải trả về `status: "0x1"` (Thành công).
  6. Gửi cùng 1 giao dịch đó vào cả Sub 1 và Sub 2 cùng một lúc:
     - Master chỉ thực thi đúng 1 lần, nonce tài khoản chỉ tăng 1, không sinh lỗi double execution.

---

### 7.4. Bộ Test Rớt mạng & Kéo bù Block (`scripts/test_sub_catchup.sh`)
* **Mục đích:** Kiểm tra cơ chế `GetBlocksRangeRequest` khi Sub bị lag.
* **Kịch bản chạy:**
  1. Cụm 1 Master + 3 Sub đang chạy ở Block #5.
  2. Tắt tiến trình **Sub 3** (`kill -STOP` hoặc `kill -9`).
  3. Gửi tiếp các giao dịch vào Sub 1 -> Master đóng tiếp các Block #6, #7, #8, #9, #10. Sub 1 và Sub 2 đã ở Block #10.
  4. Bật lại **Sub 3**.
  5. **Kiểm tra tự động:**
     - Log Sub 3 nhận Block #10 từ stream của Master -> phát hiện hổng từ Block #6 đến #9.
     - Sub 3 gửi `GetBlocksRangeRequest(6, 10)` lên Master.
     - Master trả về danh sách các block từ 6 đến 10.
     - Sub 3 apply tuần tự -> Block height của Sub 3 nhảy lên 10.
     - Toàn bộ State Root và số dư tài khoản trên Sub 3 khớp 100% với Master và Sub 1, 2.

---

### 7.5. Bộ Test Kịch bản Chuyển vai trò & Failover (`scripts/test_failover_handover.sh`)
* **Mục đích:** Tự động hóa kiểm tra quy trình Master chết và Sub cao nhất lên thay thế (R2).
* **Kịch bản chạy:**
  1. Cho cụm chạy đến Block #20.
  2. Tạo độ trễ nhân tạo: Làm cho Sub 1 nhận đủ Block #20; Sub 2 ở Block #19; Sub 3 ở Block #19.
  3. Giết tiến trình Master (`kill -9`).
  4. Script failover tự động:
     - Quét `last_complete_height` của 3 Sub: Sub 1 (20) > Sub 2 (19), Sub 3 (19) -> Chọn Sub 1.
     - Cập nhật config Sub 1 thành `master` (`ready = false`). Khởi động Sub 1.
     - Trỏ Sub 2 và Sub 3 về địa chỉ của Sub 1. Khởi động lại Sub 2 và Sub 3.
     - Sub 2 và Sub 3 kết nối vào Sub 1, pull Block #20 từ Sub 1.
     - Khi Sub 2 và Sub 3 đạt Block #20 -> Sub 1 chuyển `ready = true` (mở Ingress).
  5. Gửi giao dịch mới vào Sub 2 -> Sub 2 forward lên Sub 1 (Master mới).
  6. Sub 1 đóng thành công Block #21 và stream về Sub 2, Sub 3.
  7. Khởi động Master cũ với config `sub` trỏ về Sub 1 -> Master cũ kéo bù đủ Block #20, #21 và hoạt động bình thường như một Sub node.

---

## 8. Tiêu chí nghiệm thu cuối cùng (Acceptance Criteria)

1. **Biên dịch sạch:** Chạy `./build_check.sh` trong `consensus/metanode/scripts/` hoàn tất thành công 100% cho Go, Rust và FFI, không có bất kỳ warning/error nào.
2. **Unit Tests:** Toàn bộ các bài unit test ở mục 7.1 pass (`go test -v ./...`).
3. **FFI Isolation:** Script `test_ffi_isolation.sh` xác nhận không có bất kỳ component BFT consensus nào chạy ở role `sub` hoặc `master`.
4. **Cluster Integration:** Script `test_master_sub_cluster.sh` chạy thông suốt toàn bộ vòng đời: gửi tx -> forward -> block stream -> receipt.
5. **Catch-up Test:** Script `test_sub_catchup.sh` phục hồi thành công dữ liệu khi có node bị rớt mạng.
6. **Failover Test:** Script `test_failover_handover.sh` chuyển đổi Sub cao nhất lên Master thành công, không làm mất block, không split-brain và tiếp tục nhận tx bình thường.
