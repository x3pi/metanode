# Giải Quyết Lỗi Fork/Deadlock Trong Kiến Trúc Phase-B & Snapshot Recovery

Tài liệu này tổng hợp nguyên nhân gốc rễ (Root Cause) của hai vấn đề nghiêm trọng trong hệ thống đồng thuận Metanode và cách chúng đã được khắc phục hoàn toàn thông qua kiến trúc **Phase-B** và cơ chế **Data-driven Recovery**.

---

## 1. Lỗi Lệch Hash Block & Đóng Băng StateRoot (Đã Khắc Phục Bằng Phase-B)

### Hiện Tượng
Trước đây, sau khi khởi động lại từ Snapshot, các block mới (ví dụ Block 61) sinh ra trên node phục hồi có hash hoàn toàn khác với các node sống liên tục, tạo ra một nhánh fork hoàn toàn độc lập dù `stateRoot` và `parentHash` khớp nhau. Kéo theo đó, từ các block tiếp theo (ví dụ Block 62+), `stateRoot` bị đóng băng và không thay đổi.

### Nguyên Nhân Gốc Rễ
Nguyên nhân cốt lõi nằm ở việc quản lý **GlobalExecIndex (GEI)** bị phân mảnh giữa Rust và Go:
- Khi node phục hồi (khởi động lại từ Snapshot), bộ đếm `cumulative_fragment_offset` nội bộ của Rust bị reset về `0`.
- Do đó, GEI truyền từ Rust sang Go bị **sai lệch** (nhỏ hơn mức GEI thực tế đã được lưu trong LevelDB của Go).
- **Lệch Hash**: Trong kiến trúc cũ, `block_header.go` đưa cả `GlobalExecIndex` vào thuật toán sinh Hash. Khi GEI lệch, Block Hash lập tức bị sai, dẫn đến Fork.
- **Đóng băng StateRoot**: Go có cơ chế `GEI REGRESSION GUARD`. Khi nhận thấy GEI từ Rust truyền sang nhỏ hơn `lastBlockGEI` trong DB, nó cho rằng đây là block cũ (stale) nên từ chối thực thi các giao dịch bên trong, dẫn đến stateRoot không thay đổi.

### Giải Pháp Kiến Trúc: Phase-B (Go-Authoritative GEI)
Chúng ta đã chuyển sang kiến trúc **Phase-B**:
1. **Loại bỏ sự phụ thuộc vào Rust**: Rust không còn theo dõi hay tự tính toán `hint_gei` thông qua `cumulative_fragment_offset`.
2. **Go là nguồn chân lý duy nhất (Sole Authority)**: Rust chỉ gửi `commit_index` và danh sách giao dịch sang Go. Go tự động cấp phát GEI chính xác thông qua `GEIAuthority` nội bộ.
3. **Sửa đổi Block Hash**: Loại bỏ trường `GlobalExecIndex` khỏi quá trình compute `bData` trong hàm `Hash()`, đảm bảo tính determinisitic 100% khi tính block hash.

---

## 2. Lỗi "Healthy But Locked" Deadlock (Khi Khôi Phục Snapshot)

### Hiện Tượng
Trong các bài stress test, sau khi node khôi phục từ Snapshot, nó báo cáo đã theo kịp mạng lưới (`lag = 0`) và vào trạng thái `Healthy`, nhưng log liên tục in ra:
```
🛡️ [SCHEDULE-RECOVERY-GUARD] Blocking local committer: snapshot recovery detected, LeaderSchedule needs re-confirmation from network.
```
Node bị kẹt vĩnh viễn ở trạng thái này, không bao giờ bầu được LeaderSchedule mới và không thể tự sinh block.

### Nguyên Nhân Gốc Rễ
Sự cố xuất phát từ lỗ hổng logic trong quá trình đồng bộ lại các commit lịch sử (`STARTUP-SYNC`):
1. **Phát hiện Stale Schedule**: Khi khôi phục Snapshot, DAG của Rust trống. `CommitSyncer` kích hoạt cơ chế `schedule_recovery_pending` và yêu cầu tải về 300 commit cũ (ví dụ: commit 901 -> 1200) để tái tạo bảng điểm uy tín (`LeaderSwapTable`).
2. **Hàng rào `filter_new_commits` đánh chặn**: Khi 300 commit này được mạng lưới trả về, hàm `filter_new_commits` trong Rust kiểm tra thấy `commit.index() <= last_commit_index` (vì node đã cập nhật index mới nhất từ Go trước đó), nên nó **loại bỏ hoàn toàn** các commit lịch sử này.
3. **Deadlock xảy ra**: Vì các commit cũ bị loại bỏ, chúng không bao giờ lọt vào `scoring_subdag`. Biến đếm `commits_until_update` không bao giờ giảm xuống 0. Bảng LeaderSchedule không bao giờ được tính lại, khiến cờ `schedule_recovery_pending` mãi mãi bị kẹt ở `true`.

### Giải Pháp (Data-driven Recovery)
Chúng ta đã khắc phục lỗi này mà không gây ra tác dụng phụ (double-execution) bằng cách kết hợp nới lỏng kiểm tra và Fast-Forward:
1. **Mở cổng cho Schedule Recovery**: 
   Sửa `filter_new_commits` (trong `commit_manager.rs`) để chủ động cho phép các commit lịch sử đi qua nếu hệ thống đang cần khôi phục Schedule:
   ```rust
   if commit.index() > last_commit_index || is_schedule_recovery {
       true
   }
   ```
2. **Ngăn chặn Double-Execution bằng Fast-Forward**:
   Các commit cũ khi vượt qua cổng lọc sẽ được nạp vào `scoring_subdag` để tính điểm uy tín cho Leader. Nhưng khi chúng đến `CommitProcessor` để đẩy sang Go, cơ chế **Fast-Forward** sẽ lập tức nhảy cóc (skip) chúng:
   ```rust
   if commit_index <= go_last_commit_index {
       info!("⏭️  [FAST-FORWARD] Skipping historical commit {}...", commit_index);
       continue;
   }
   ```
3. **Kết quả**: Node lấy lại đủ 300 commit, tính lại thành công `LeaderSwapTable`, gỡ cờ `schedule_recovery_pending = false`, và chuyển sang trạng thái `Ready` để tiếp tục tạo block bình thường. Mọi vòng test (lên đến hàng trăm round) hiện đều vượt qua hoàn hảo không còn fork hay deadlock.
