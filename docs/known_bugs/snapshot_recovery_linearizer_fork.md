# Lỗi Phân Nhánh (Fork) Tại Block #1227 Trong Quá Trình Phục Hồi Snapshot

**Ngày ghi nhận**: Tháng 5, 2026  
**Thành phần bị ảnh hưởng**: Quá trình khởi động đồng bộ (`STARTUP-SYNC`), Phục hồi Snapshot, Rust Consensus (`linearizer.rs`), Lưu trữ PebbleDB.

## 1. Mô tả sự cố
Trong quá trình kiểm thử vòng lặp phục hồi snapshot (`test_snapshot_stability_loop.sh`), Node 3 (`m3`) sau khi phục hồi từ bản snapshot của Node 0 (`m0`) tại block #1222 đã liên tục báo lỗi phân nhánh (fork) ngược lại quá khứ tại block #1197.  
Mặc dù Node 3 đang cố gắng phục hồi dữ liệu từ Node 0, dữ liệu block #1197 của nó lại khác biệt hoàn toàn:
- **Node `m0` (Live)**: `timestamp = 0x69fc4064`, `txRoot = 0x717ca1...`
- **Node `m3` (Syncing)**: `timestamp = 0x69fc4067`, `txRoot = 0x74fc32...`

## 2. Nguyên nhân gốc rễ (Root Causes)
Sự cố này vô cùng khó bắt vì nó là kết quả của **2 lỗi nghiêm trọng** xảy ra đồng thời ở 2 tầng khác nhau (Bash script và Rust Consensus):

### Lỗi 1: Script Bash không copy thư mục PebbleDB Sharded (`chaindata`)
- **Vấn đề**: Trong phiên bản lưu trữ mới sử dụng PebbleDB với cấu hình `DBEngine: "sharded"`, `StorageManager` của hệ thống sẽ gộp toàn bộ dữ liệu vào một thư mục duy nhất có tên là `chaindata` (thay vì chia nhỏ ra). Tuy nhiên, file `test_snapshot_stability_loop.sh` vẫn sử dụng vòng lặp cũ để tìm và copy các thư mục rời rạc (như `account_state`, `blocks`, `mapping`, v.v.).
- **Hậu quả**: Vì script không tìm thấy các thư mục cũ, nó đã **không copy bất kỳ dữ liệu chaindata nào** sang Node 3. Kết quả là `m3` khởi động lại với một database hoàn toàn trống (bắt đầu từ block 0) và buộc phải chạy quá trình `STARTUP-SYNC` tải lại toàn bộ block từ mạng thay vì tiếp tục từ block #1222.

### Lỗi 2: Tính không xác định (Non-determinism) của Linearizer Fallback
- **Vấn đề**: Do `m3` phải đồng bộ lại từ block 0, `CommitSyncer` liên tục tải các sub-dag từ mạng. Khi xử lý commit cho block #1197, do đặc thù đồng bộ nhanh (cold-sync), bộ nhớ tạm (`dag_state`) có thể thiếu mất một số block cha (ancestor blocks). Do thiếu block cha, hàm `median_timestamp_by_stake` trong `linearizer.rs` thất bại trong việc tính toán trung vị.
- **Cơ chế Fallback sai lệch**: Để tránh việc node bị crash (panic) do thiếu block, hệ thống trước đó đã dùng `unwrap_or_else` để trả về timestamp gốc của block leader (`leader_block.timestamp_ms()`). 
  - Node `m0` (đang chạy live) có đầy đủ các block cha trong RAM, nên nó tính ra được *true median* là `0x69fc4064`.
  - Node `m3` (đang đồng bộ) do thiếu block, nên rơi vào fallback và dùng timestamp của leader là `0x69fc4067`.
Sự lệch pha 3 mili-giây này khiến hàm băm của block bị thay đổi, kéo theo toàn bộ giao dịch (`txRoot`) thay đổi và dẫn đến phân nhánh vĩnh viễn.

## 3. Giải pháp đã triển khai (Fixes)

### [Bash Script] Cập nhật logic copy Snapshot
Đã thêm logic kiểm tra và copy trực tiếp thư mục `chaindata` trong `test_snapshot_stability_loop.sh`. Việc này đảm bảo Node đích nhận được đầy đủ dữ liệu trạng thái và có thể bắt đầu từ block #1222 một cách trơn tru, bỏ qua hoàn toàn quá trình `STARTUP-SYNC` từ block 0.

### [Rust Consensus] Cố định mỏ neo thời gian cho Linearizer (`linearizer.rs`)
Thay thế cơ chế fallback không đồng nhất `leader_block.timestamp_ms()` bằng một giá trị mỏ neo hoàn toàn xác định (deterministic anchor) là `last_commit_timestamp_ms`. 
Giờ đây, nếu một node trong quá trình đồng bộ (catch-up) bị thiếu block cha và không thể tính trung vị, nó sẽ an toàn lùi về sử dụng timestamp của *commit thành công trước đó*. Vì `last_commit_timestamp_ms` là một giá trị mà toàn mạng đã đồng thuận từ trước, nên không node nào có thể sinh ra timestamp rác (divergence), từ đó ngăn chặn triệt để fork.

## 4. Bài học kinh nghiệm (Takeaways)
1. **Kiểm tra tương thích cấu hình lưu trữ**: Bất kỳ thay đổi nào về cấu trúc thư mục (như chuyển sang Sharded PebbleDB) phải được rà soát và đồng bộ ngay lập tức với các công cụ CI/CD, script kiểm thử và hệ thống quản lý Snapshot.
2. **Nguyên tắc thiết kế Consensus**: Tuyệt đối không sử dụng các giá trị fallback (dự phòng) phụ thuộc vào trạng thái cục bộ của Node (như việc RAM node có sẵn một block nào đó hay không). Tất cả các tính toán trong vùng đồng thuận (Consensus) bắt buộc phải là **xác định tuyệt đối (strictly deterministic)** dựa trên những giá trị mỏ neo đã được mạng lưới khóa chặt.
