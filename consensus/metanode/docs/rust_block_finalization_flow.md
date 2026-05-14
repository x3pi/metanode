# Luồng Thực Thi Chốt Block (Finalize Block) Từ Rust Sang Go

Tài liệu này mô tả chi tiết quá trình một block (hay chính xác hơn là một `CommittedSubDag`) sau khi được chốt bởi hệ thống Consensus (Rust) sẽ được xử lý và chuyển giao sang Go Execution Engine như thế nào.

Quá trình này được thiết kế theo kiến trúc Pipeline bất đồng bộ (Async Pipeline) nhằm tách biệt luồng xử lý consensus tốc độ cao với luồng I/O và thực thi của Go.

---

## 1. Tổng Quan Kiến Trúc (Pipeline Architecture)

Luồng đi của một block trải qua 4 trạm (Station) chính:
1. **Station 1: Consensus Core** - Tạo ra `CommittedSubDag` và đẩy vào channel.
2. **Station 2 & 3: CommitProcessor & Executor** - Nhận commit, kiểm tra điều kiện (epoch, GEI, leader), gán GEI và gửi vào hàng đợi.
3. **Station 4: BlockDeliveryManager** - Quản lý hàng đợi giao nhận (Conveyor belt) để giảm tải backpressure.
4. **Station 5: ExecutorClient (FFI)** - Cắt mảnh (fragmentation), đóng gói Protobuf và gọi hàm C FFI truyền byte sang Go.

---

## 2. Chi Tiết Các Bước Thực Thi (Execution Flow)

### Bước 1: Nhận Commit tại `CommitProcessor`
- **File:** `consensus/metanode/src/consensus/commit_processor/processor.rs`
- **Hàm:** `CommitProcessor::run()`
- **Chi tiết:**
  - `CommitProcessor` chạy một vòng lặp vô hạn `receiver.recv().await` để lấy các `CommittedSubDag` đã được hệ thống đồng thuận (Narwhal/Bullshark) chốt.
  - **Kiểm tra trạng thái Epoch:** Nếu hệ thống đang trong quá trình chuyển epoch (`is_transitioning = true`), luồng này sẽ tạm dừng (pause) để tránh gửi block sai thời điểm sang Go.
  - **Fast-forward:** Bỏ qua các commit cũ (historical commits) mà Go đã xử lý (khi node đang catch-up).
  - **Resolve Leader:** Tính toán và điền địa chỉ Ethereum (20-byte) của node leader dựa trên committee của epoch hiện tại (`resolve_leader_address`).
  - Gọi `super::executor::dispatch_commit` để chuyển sang bước tiếp theo.
  - Kiểm tra xem commit có chứa `EndOfEpoch` system transaction không. Nếu có, trigger quá trình `epoch_transition_callback` và kết thúc vòng lặp epoch hiện tại.

### Bước 2: Tiền Xử Lý tại `Executor`
- **File:** `consensus/metanode/src/consensus/commit_processor/executor.rs`
- **Hàm:** `dispatch_commit()`
- **Chi tiết:**
  - **Fast-Skip:** Nếu block hoàn toàn rỗng (không có TX nào) và không phải là block chứa system TX, nó sẽ bị loại bỏ sớm để tiết kiệm tài nguyên I/O. (Go GEI không tăng trong trường hợp này).
  - **GEI Guard (Replay Protection):** Kiểm tra `go_current_gei`. Nếu Go đã xử lý vượt qua mức `global_exec_index` hiện tại, nó sẽ bỏ qua để chống duplicate (trừ phi có chứa lệnh EndOfEpoch).
  - **Đẩy vào BlockDeliveryManager:** Đóng gói thành `ValidatedCommit` (gồm subdag, GEI, epoch, leader_address) và gửi qua channel mpsc `delivery_sender`. Chờ phản hồi qua `oneshot` channel để biết block đã tốn bao nhiêu slot GEI (fragmentation).
  - **Lưu Transaction Hashes:** Sử dụng `DEFERRED_TASK_SEMAPHORE` (để tránh OOM task) lưu bất đồng bộ mã hash của các transaction vào RocksDB nhằm mục đích deduplication ở các epoch sau.

### Bước 3: Hàng Đợi Giao Nhận `BlockDeliveryManager`
- **File:** `consensus/metanode/src/node/block_delivery.rs`
- **Hàm:** `BlockDeliveryManager::run()`
- **Chi tiết:**
  - Đóng vai trò như một băng chuyền (Conveyor belt). Nó lắng nghe channel nhận `ValidatedCommit`.
  - Mục đích chính của component này là **tách rời (decouple)** luồng Consensus khỏi luồng gọi FFI sang Go. Nếu Go bị nghẽn (backpressure), chỉ có `BlockDeliveryManager` bị chậm lại, còn `CommitProcessor` vẫn có thể xử lý các task khác.
  - Gọi `self.executor_client.send_committed_subdag(...)`.

### Bước 4: Đóng Gói và Truyền Tải tại `ExecutorClient`
- **File:** `consensus/metanode/src/node/executor_client/block_sending.rs`
- **Hàm:** `send_committed_subdag()` và `buffer_and_flush()`
- **Chi tiết:**
  - **Sàng lọc & Sắp xếp TX:** Xây dựng danh sách các User TX và System TX (gọi hàm `build_sorted_transactions`).
  - **Block Fragmentation (Cắt Mảnh):** Nếu một commit chứa quá nhiều transaction (vượt qua `MAX_TXS_PER_GO_BLOCK` = 50.000), Rust sẽ cắt nhỏ thành nhiều phần. Mỗi phần là một `ExecutableBlock` có cùng `commit_index` nhưng `global_exec_index` tăng dần.
  - **Protobuf Serialization:** Biến đổi struct thành cấu trúc Protobuf `ExecutableBlock`.
  - **Sequential Buffering (`buffer_and_flush`):**
    - Do mạng có thể bất đồng bộ, các block có thể đến không theo thứ tự. Dữ liệu protobuf sẽ được đưa vào một `send_buffer` (BTreeMap sắp xếp theo `global_exec_index`).
    - Gọi hàm `flush_buffer()`.
  - **Gọi FFI Sang Go (`flush_buffer`):**
    - Lấy ra tất cả các block **liên tiếp nhau** từ `send_buffer` (bắt đầu từ `next_expected_index`).
    - Lặp qua batch và truyền trực tiếp vào bộ nhớ Go thông qua C FFI:
      ```rust
      // Lấy function pointer do Go đăng ký lúc khởi động
      if let Some(c_fn) = crate::ffi::GO_CALLBACKS.get().and_then(|c| c.execute_block) {
          // Gọi hàm C, truyền mảng byte Protobuf
          let success = c_fn(data.as_ptr(), data.len());
          // ...
      }
      ```
    - Cập nhật `sent_indices` và thỉnh thoảng lưu trạng thái `last_sent_index` xuống RocksDB.

---

## 3. Các Cơ Chế An Toàn (Safety Mechanisms)

1. **Fork-Safety qua Deterministic Fragmentation:** Thuật toán chia block (> 50k TXs) dựa trên mảng TX ban đầu, đảm bảo các node có cùng subdag sẽ tính ra chính xác cùng số lượng mảnh và đánh số GEI giống hệt nhau.
2. **Replay Protection / GEI Dedup:** Cả ở `Executor` lẫn `ExecutorClient`, hệ thống luôn đối chiếu `global_exec_index` hiện tại với `go_current_gei` hoặc `next_expected_index` để loại bỏ các block đã được gửi trước đó khi node crash và restart.
3. **Sequential Enforcement:** Hàm `flush_buffer` dùng BTreeMap và bắt buộc chỉ gửi một batch nếu số GEI nối tiếp liên tục. Go sẽ không bao giờ nhận được block GEI = 5 nếu GEI = 4 chưa được gửi.
4. **Deferred Task Backpressure:** Dùng `tokio::sync::Semaphore` giới hạn (vd 64 permit) số lượng background task ghi transaction hash xuống disk, tránh bùng nổ bộ nhớ.
