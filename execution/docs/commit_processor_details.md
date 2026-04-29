# Giải phẫu luồng xử lý của CommitProcessor (Trạm 1)

Tài liệu này giải thích chi tiết TỪNG DÒNG CODE cách `CommitProcessor` hoạt động khi nhận được dữ liệu (mẻ block) từ Lõi Đồng Thuận (Core Consensus) trả về.

## Nơi chứa code:
- **File:** `consensus/metanode/src/consensus/commit_processor/processor.rs`
- **Hàm:** `pub async fn run(self)` (Bắt đầu từ dòng 242)

---

## Chi tiết Từng Dòng Xử Lý (Line by Line)

Khi chạy, `CommitProcessor` sẽ đi vào một vòng lặp vô tận (loop) để đứng canh ống nước nhận mẻ dữ liệu. Dưới đây là các hành động chính:

### 1. Đứng chờ nhận mẻ dữ liệu mới
```rust
// Dòng 340: Vòng lặp vô tận
loop {
    // Dòng 370: Đứng chờ (block) cho đến khi Lõi đồng thuận đẩy mẻ dữ liệu (subdag) vào
    match receiver.recv().await {
        Some(subdag) => {
            // Dòng 372: Lấy số thứ tự (index) của mẻ này.
            // Ví dụ: Lõi đồng thuận nói đây là mẻ số 3.
            let commit_index: u32 = subdag.commit_ref.index;
```
**Giải thích:** `CommitProcessor` luôn trong trạng thái ngủ chờ. Ngay khi Lõi đồng thuận chốt xong 1 mẻ, mẻ đó sẽ rơi xuống đây thông qua `receiver`.

### 2. Kiểm tra AUTO-JUMP (Khi node vừa khởi động)
```rust
// Dòng 404
if next_expected_index == 1 && commit_index > 1 {
    warn!("🚀 [AUTO-JUMP] Initial commit {} > expected 1...", commit_index);
    next_expected_index = commit_index;
}
```
**Giải thích:** Đây là code xử lý khi Node của bạn vừa bị sập và bật lại. Lúc bật lên, máy tính tưởng nó phải chờ mẻ số `1`. Nhưng thực tế mạng lưới đã chạy đến mẻ số `5000` rồi. Thay vì chờ mẻ 1 vô vọng, tính năng `AUTO-JUMP` sẽ ép Node nhảy cóc lên số `5000` luôn để hòa mạng.

### 3. Kiểm tra tính Tuần tự (Sắp xếp đúng thứ tự)
```rust
// Dòng 427: Chốt chặn quan trọng nhất
if commit_index == next_expected_index {
```
**Giải thích:** Trạm 1 là kẻ giữ trật tự. Nó chỉ cho phép xử lý nếu mẻ nhận được ĐÚNG với mẻ nó đang chờ. (Ví dụ: Đang chờ mẻ 3 thì phải đúng mẻ 3 mới làm tiếp. Mẻ 4 đến thì tạm cất đi).

### 4. Tính toán Chỉ Số Toàn Cầu (Global Execution Index - GEI)
```rust
// Dòng 434
let global_exec_index = calculate_global_exec_index(
    current_epoch,
    commit_index as u64 + cumulative_fragment_offset,
    epoch_base_index,
);
```
**Giải thích:** Số thứ tự của mẻ (`commit_index`) chỉ có ý nghĩa cục bộ trong 1 Epoch (Kỷ nguyên). Hệ thống cần một con số đếm tiến liên tục từ khi sinh ra mạng lưới (từ Epoch 1 đến Epoch N). Đó là `global_exec_index`. Con số này bắt buộc phải khớp nhau trên mọi Validator để đảm bảo không bị dẽ nhánh (Fork).

### 5. Giao việc cho Trạm 2 (Chuyển giao)
```rust
// Dòng 463
let geis_consumed = super::executor::dispatch_commit(
    &subdag,
    global_exec_index,
    current_epoch,
    executor_client.clone(),
    // ...
).await?;
```
**Giải thích:** Đây là ĐIỂM KẾT THÚC của Trạm 1. Sau khi đã xác nhận mẻ này hợp lệ, đúng thứ tự, tính toán GEI xong xuôi, `CommitProcessor` sẽ gọi thẳng hàm `dispatch_commit` để ném mẻ này qua Trạm 2 (Hải Quan). Từ đây, Trạm 2 sẽ làm nhiệm vụ dọn rác, loại bỏ các giao dịch rỗng và kiểm tra chữ ký.

### 6. Tái chế Giao Dịch (Tx Recycler)
```rust
// Dòng 477
if let Some(ref recycler) = self.tx_recycler {
    if total_txs_in_commit > 0 {
        // ... Lấy danh sách giao dịch ...
        recycler.confirm_committed(&committed_tx_data).await;
    }
}
```
**Giải thích:** Khi mẻ đã được xác nhận, hệ thống phải báo cho bể giao dịch (Mempool) biết là: *"Này, mấy cái giao dịch này đã được chốt rồi nhé, xóa đi đừng nộp lại nữa"*. Đây gọi là bước Tái chế/Chốt sổ Mempool.

---

## Giải thích các Tham số (Fields) của CommitProcessor

Để cấu trúc `CommitProcessor` có thể chạy trơn tru, nó mang theo các "đồ nghề" (tham số) sau:

### 📥 Nhóm Nhận & Chờ Dữ Liệu
- **`receiver: UnboundedReceiver<CommittedSubDag>`**: "Ống nước" nối trực tiếp từ lõi đồng thuận. Mẻ nào chốt xong sẽ rơi vào đây.
- **`next_expected_index: u32`**: Biến ghi nhớ mẻ tiếp theo hệ thống đang chờ (Ví dụ đang đợi mẻ 3).
- **`pending_commits: BTreeMap<u32, CommittedSubDag>`**: "Kho tạm". Nhận được mẻ 5 mà đang chờ mẻ 3 thì cất mẻ 5 vào đây.

### 🔢 Nhóm Tính Toán Số Thứ Tự (GEI & Epoch)
- **`current_epoch: u64`**: Kỷ nguyên hiện tại (Ví dụ: Epoch 60).
- **`epoch_base_index_override: Option<u64>`**: "Gốc tọa độ" GEI lúc bắt đầu Epoch. Dùng để cộng dồn thứ tự các mẻ mới.
- **`shared_last_global_exec_index: Option<Arc<tokio::sync::Mutex<u64>>>`**: Biến dùng chung (có khóa Mutex bảo vệ) lưu GEI mới nhất của Node để các thành phần khác đọc.

### 🚚 Nhóm Gửi Dữ Liệu (Đầu Ra)
- **`executor_client: Option<Arc<ExecutorClient>>`**: Dùng để đóng gói và gửi qua cổng CGo FFI về cho máy ảo Go.
- **`delivery_sender: Option<tokio::sync::mpsc::Sender<...>>`**: "Ống nước" đầu ra. Mẻ xử lý xong được đẩy vào đây cho Trạm 3 phát đi các Node khác.

### 🔄 Nhóm Callbacks (Sự kiện Kích hoạt)
- **`commit_index_callback` & `global_exec_index_callback`**: Báo cáo lên màn hình hoặc các module khác rằng số thứ tự đã tăng.
- **`epoch_transition_callback`**: "Báo động đỏ". Gọi khi có giao dịch `EndOfEpoch` để toàn bộ mạng chuyển giao Kỷ nguyên.

### 🛠 Nhóm Quản Lý Trạng Thái & Tiện Ích
- **`is_transitioning: Option<Arc<AtomicBool>>`**: Cờ True/False. Đang chuyển Epoch thì bật True để hàm `run()` TẠM DỪNG.
- **`pending_transactions_queue: Option<Arc<tokio::sync::Mutex<Vec<Vec<u8>>>>>`**: Hàng đợi các giao dịch bị kẹt lại khi hết Epoch, để dành cho Epoch sau.
- **`epoch_eth_addresses: Arc<tokio::sync::RwLock<HashMap<...>>>`**: Danh bạ lưu ví của các Validator để trả lương.
- **`tx_recycler: Option<Arc<TxRecycler>>`**: Cỗ máy móc giao dịch đã được chốt ra khỏi Mempool để tránh nộp đúp.

### 💾 Nhóm Chống Phân Mảnh & Giám Sát
- **`storage_path: Option<std::path::PathBuf>`**: Lưu file trên đĩa để ghi lại `fragment_offset` (số lần chặt nhỏ block), giúp tính lại GEI nếu Node sập đột ngột.
- **`lag_alert_sender: Option<UnboundedSender<...>>`**: Còi báo động. Node xử lý block quá chậm so với tốc độ mạng thì nó sẽ hú lên.

---
**Tổng kết:** `CommitProcessor` rất thông minh nhưng cực kỳ cẩn thận. Nó không tự ý mở gói hàng (giao dịch) ra xem. Nhiệm vụ duy nhất của nó là nhìn vào "Tem dãn" (Index), sắp xếp theo đúng thứ tự (1, 2, 3...) và dán thêm mã bưu điện toàn cầu (GEI) rồi chuyển cho phòng ban tiếp theo (`dispatch_commit`).
