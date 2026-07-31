# Hướng dẫn cấu hình giới hạn Transaction (TX) trong 1 Block

Hệ thống Metanode được thiết kế kết hợp giữa **Go (Thực thi EVM & Mempool)** và **Rust (Đồng thuận DAG)**. Do đó, để thay đổi số lượng TX tối đa trong một block (ví dụ: tăng từ 4,000 lên 50,000 hoặc 100,000), bạn cần điều chỉnh các tham số ở **cả 2 hệ thống**.

Dưới đây là 3 vị trí bạn cần sửa đổi để đảm bảo mạng không bị nghẽn hay cắt cụt block.

---

## 1. Tầng Rust (Lõi Đồng thuận - Quyết định kích thước Block cứng)

Mạng lưới đồng thuận Rust có giới hạn cứng về (1) Số lượng TX tối đa và (2) Dung lượng byte tối đa của một block. Nếu Go gửi qua số lượng lớn hơn giới hạn này, Rust sẽ tự động "cắt cụt" block.

📂 **File cần sửa:** `crates/meta-protocol-config/src/lib.rs`
🔍 **Tìm kiếm từ khóa:** `MetaNode performance overrides for >30K TPS target` (Khoảng dòng `4470`)

```rust
// Thay đổi số Byte tối đa (VD: 50MB)
cfg.consensus_max_transactions_in_block_bytes = Some(50 * 1024 * 1024); 

// Thay đổi số TX tối đa (VD: 50,000)
cfg.consensus_max_num_transactions_in_block = Some(50000); 
```

> **Lưu ý:** Trung bình 50,000 TX tốn khoảng 12.5MB - 15MB. Hãy đảm bảo `consensus_max_transactions_in_block_bytes` đủ lớn (ví dụ 50MB) để không bị drop payload khi số lượng TX quá nhiều. Tránh đặt quá lớn (ví dụ 100MB) vì có thể gây lỗi FFI.

---

## 2. Tầng Go (Mempool FFI - Gom Batch gửi sang Rust)

Tầng Go chịu trách nhiệm gom các TX trong Mempool lại thành một Batch (mảng) trước khi đẩy sang lõi Rust qua Unix Domain Socket. Nếu ngưỡng này nhỏ, hệ thống sẽ đẩy liên tục các block nhỏ thay vì một block to.

📂 **File cần sửa:** `execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go`
🔍 **Tìm kiếm từ khóa:** `maxTransactionsPerBatch` (Khoảng dòng `50`) và `targetBlockSize` (Khoảng dòng `145`)

```go
// 1. Giới hạn số TX tối đa cho mỗi lượt gom batch (TBAB adaptive)
const maxTransactionsPerBatch = 50000

// ...

// 2. Trả lại phần thừa vào Mempool nếu vượt quá mốc Block Size
const targetBlockSize = 50000
```

---

## 3. Tầng Go (Verfication Chunking - Xử lý song song CPU)

Khi RPC tiêm TX vào Mempool, Go sẽ chia thành các chunk (khối nhỏ) để verify song song giúp tăng tốc độ xử lý CPU.

📂 **File cần sửa:** `execution/cmd/simple_chain/processor/transaction_processor.go`
🔍 **Tìm kiếm từ khóa:** `maxChunkSize := tp.chainState.GetConfig().TxVerificationChunkSize` (Khoảng dòng `530`)

```go
maxChunkSize := tp.chainState.GetConfig().TxVerificationChunkSize
if maxChunkSize <= 0 {
    // Sửa con số này bằng với số TX mong muốn trong block
    maxChunkSize = 50000
}
```

---

## 🚀 Hướng dẫn Apply cấu hình sau khi sửa xong

Sau khi đã thay đổi các con số ở 3 file trên, bạn BẮT BUỘC phải build lại mã nguồn và dọn dẹp data cũ thì cấu hình mới mới có hiệu lực.

1. Di chuyển vào thư mục deploy mạng:
   ```bash
   cd deploy/systemd
   ```
2. Chạy lệnh reset (Lệnh này sẽ tự động gọi `build_check.sh` để biên dịch lại toàn bộ Go và Rust, sau đó xoá data cũ, tạo data mới):
   ```bash
   ./setup_and_run.sh --clean
   ```

---
> 💡 **Mẹo (Best Practice) khi Deploy lên Server Production:** 
> Nếu chạy ở Localhost, bạn có thể đẩy lên `50,000` đến `100,000` TX/block để test max throughput của CPU (do Localhost truyền tải mạng 0ms). 
> Tuy nhiên, trên Server phân tán (WAN), việc đóng block kích thước khổng lồ (>15MB) sẽ dẫn đến chậm tốc độ chia sẻ qua mạng P2P, gây ra timeout đồng thuận. Trên Server thực tế, ngưỡng khuyên dùng là: **10,000 - 20,000 TXs / Block** (Để payload giữ ở mức <5MB).
