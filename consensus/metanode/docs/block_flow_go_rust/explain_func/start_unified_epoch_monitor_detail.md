# Chi tiết logic hàm `start_unified_epoch_monitor`

Hàm `start_unified_epoch_monitor` là trái tim của hệ thống theo dõi Kỷ nguyên (Epoch) trong Metanode. Nó là một luồng (thread) chạy ngầm vĩnh viễn không bao giờ dừng, làm nhiệm vụ "canh gác" xem mạng lưới đã sang Kỷ nguyên mới chưa để kích hoạt quá trình chuyển đổi.

Dưới đây là mô tả chi tiết từng block code bên trong hàm này (dành cho người mới tiếp cận Rust).

---

## 1. Khởi tạo và Thiết lập Vòng lặp nền (Background Task)
**Đoạn code:** Từ dòng 32 đến dòng 62
```rust
pub fn start_unified_epoch_monitor(...) -> Result<Option<JoinHandle<()>>> {
    // 1. Lấy thông tin kết nối sang Go
    let client_arc = match executor_client { ... };
    
    // 2. Chạy một luồng ngầm (background task) bằng Tokio
    let handle = tokio::spawn(async move {
        let normal_interval = Duration::from_secs(10);
        let fast_interval = Duration::from_secs(1);
        // ...
        loop { // Vòng lặp vô hạn
```
**Giải thích:** 
- Hàm trả về một `JoinHandle<()>`, đây là cách Rust quản lý các luồng bất đồng bộ (async thread). Khi gọi `tokio::spawn`, đoạn code bên trong sẽ chạy ngầm giống như một tiến trình daemon.
- **Adaptive Polling (Thích ứng thời gian):** Nó định nghĩa hai mức độ "ngủ" là 10 giây (bình thường để đỡ tốn CPU) và 1 giây (chế độ tăng tốc khi phát hiện mạng đang có biến động chuyển Kỷ nguyên).

---

## 2. Lấy thông tin 3 loại Kỷ nguyên (Epochs)
Trong vòng lặp, hệ thống cần biết 3 con số Kỷ nguyên khác nhau để so sánh:

**Bước A: Lấy `local_go_epoch`** (Kỷ nguyên của CSDL Go)
```rust
let local_go_epoch = client_arc.get_current_epoch().await.unwrap_or(0);
```
- Hỏi phía CSDL Go xem hiện tại nó đang xử lý giao dịch ở Kỷ nguyên nào.

**Bước B: Lấy `network_epoch`** (Kỷ nguyên của Mạng lưới)
```rust
let (network_epoch, peer_best_block) = if !peer_rpc.is_empty() {
    match query_peer_epochs_network(&peer_rpc).await { ... }
}
```
- Gọi ra mạng ngoài (TCP/P2P) tới các Node khác để hỏi xem mạng lưới đang ở Kỷ nguyên số mấy và Block cao nhất là bao nhiêu.

**Bước C: Lấy `rust_epoch` và `current_mode`** (Trạng thái nội tại của Rust)
```rust
let (rust_epoch, current_mode) = {
    let node_guard = node_arc.lock().await;
    (node_guard.current_epoch, node_guard.node_mode.clone())
};
```
- Kiểm tra xem phần mềm Consensus (Rust) của chính máy mình đang ghi nhận Kỷ nguyên nào, và vai trò hiện tại là gì (`Validator` hay `SyncOnly`).

---

## 3. Xử lý Cứu hộ Validator bị kẹt (Stall Recovery)
**Đoạn code:** Từ dòng 163 đến dòng 253
```rust
if matches!(current_mode, crate::node::NodeMode::Validator) && !peer_rpc.is_empty() {
    // Kiểm tra xem Go block có bị đứng im (Stalled) trong khi Peer đã tiến xa không
    if peer_best_block > go_block + STALL_MIN_GAP {
        stall_count += 1;
        if stall_count >= STALL_THRESHOLD {
            // TẢI BLOCK TỪ PEER ĐỂ TỰ CỨU MÌNH
            match fetch_blocks_from_peer(...).await {
                Ok(blocks) => client_arc.sync_and_execute_blocks(blocks).await;
            }
        }
    }
}
```
**Giải thích:**
- Phân đoạn này chỉ chạy khi mạng lưới **chưa** chuyển Kỷ nguyên.
- **Tại sao Validator phải tải block?** Bình thường Validator tự sinh block. Nhưng nếu mạng bị chia cắt hoặc nó bị kẹt, số block của nó đứng im (`stall_count` tăng dần). Lúc này, nó phải mở cổng P2P tải block của người khác về để chạy, nhằm kích hoạt lại bộ máy đồng thuận (CommitSyncer/DAG). 

---

## 4. Xử lý ép chuyển Epoch cho SyncOnly (Đoạn mã gây lệch GEI cũ)
**Đoạn code:** Từ dòng 264 đến dòng 342
```rust
if matches!(current_mode, crate::node::NodeMode::SyncOnly) {
    for target_epoch in (local_go_epoch + 1)..=network_epoch {
        // ... tải ranh giới boundary ...
        // ... tải block thật ...
        if current_go_gei < data.boundary_gei {
             fetch_executable_blocks_from_peer(...).await; // TẢI EMPTY COMMITS
        }
    }
}
```
**Giải thích:** 
- Đây là đoạn mã mà bạn đã nhận ra sự bất hợp lý. Nó chạy vòng lặp đuổi theo `network_epoch`. 
- Nếu thiếu GEI, nó nhồi `empty commits` (giao dịch rỗng) vào Go Master qua FFI để GEI của nó bằng với ranh giới của mạng. Việc này tạo ra State rỗng, gây lệch Hash toàn mạng lưới đối với Node này.

---

## 5. Kích hoạt Chuyển giao Kỷ nguyên ở tầng Rust
**Đoạn code:** Từ dòng 346 trở đi
```rust
if target_epoch > rust_epoch {
    let epoch_manager = get_epoch_manager();
    epoch_manager.try_start_epoch_transition(new_epoch, "epoch_monitor").await;
}
```
**Giải thích:**
- Nếu Epoch của mạng > Epoch nội tại của Rust, nó lấy `EpochTransitionManager` (một trình quản lý trạng thái có nhiệm vụ đảm bảo không có 2 tiến trình cùng chuyển Epoch một lúc).
- Nó phát động quá trình chuyển Kỷ nguyên bằng cách gọi `try_start_epoch_transition`. Hàm này về sau sẽ kích hoạt hàm `transition_to_epoch_from_system_tx` mà bạn đã xem ở phần trước.
- Sau đó, luồng chạy nghỉ ngơi (`tokio::time::sleep`) theo thời gian interval và tiếp tục vòng lặp vô tận của mình.

---
**Tóm tắt toàn bộ:** Hàm này giống như một "Radar" hoạt động liên tục 24/7. Cứ mỗi 10 giây nó quét mạng 1 lần. Thấy Mạng có gì thay đổi (kẹt block thì đi tải bù, sang Kỷ nguyên mới thì báo động chuyển Kỷ nguyên), nó sẽ điều phối các module khác xử lý.
