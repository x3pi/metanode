# Bug: Missing Block Heuristic Gây Phân Nhánh (Fork)

## 1. Nguyên nhân gốc rễ (Root Cause)
Lỗi phân nhánh này không xuất phát từ việc đồng bộ trạng thái bị thiếu sót, mà bắt nguồn từ một lỗi logic toán học cực kỳ tinh vi trong thành phần `Linearizer` của hệ thống đồng thuận (Mysticeti). Lỗi này có tên là **"Missing Block Heuristic" (Kinh nghiệm dự đoán block bị thiếu)**.

Cụ thể như sau:

- **Ngữ cảnh khi khởi động từ Snapshot:** Khi một node (ví dụ: `m2`) bị xóa dữ liệu và khởi động lại từ đầu, nó sẽ tải các `CertifiedCommits` từ mạng để bắt kịp (catch-up) trạng thái. Tuy nhiên, `CertifiedCommits` chỉ chứa các block **đã được commit**, và hoàn toàn không chứa các block "mồ côi" (orphaned blocks - các block được tạo ra nhưng chưa từng được commit trước thời điểm snapshot).
- **Cách Heuristic hoạt động sai:** Khi `m2` khôi phục xong và bắt đầu tham gia tạo block mới ở round 1116, `Linearizer` của nó phải xây dựng lại đồ thị phụ (subdag). Lúc này, `Linearizer` phát hiện ra có một block "mồ côi" cũ từ trước thời điểm snapshot được tham chiếu bởi leader hiện tại.
- **Đoạn mã Heuristic cũ được viết như sau:** *"Nếu round của block bị thiếu này nhỏ hơn hoặc bằng last_commit_round, thì chắc chắn nó đã được commit từ đời nào rồi nên ta cứ an tâm bỏ qua nó thay vì báo lỗi."*
- **Sự chia nhánh xảy ra:** Node `m2` (vừa khôi phục) bỏ qua block đó và không đưa vào subdag. Tuy nhiên, các node cũ đang chạy bình thường như `m0` vẫn còn lưu block mồ côi đó trong database (vì chúng chưa bao giờ bị xóa). Thế là `m0` đưa block đó vào subdag.
- **Hậu quả dây chuyền:** Vì subdag của `m0` và `m2` chứa số lượng block khác nhau, nên hàm `add_scoring_subdags` tính toán điểm uy tín (reputation scores) cho các validator bị lệch nhau. Sự lệch điểm này dẫn đến việc bảng `LeaderSwapTable` của `m0` và `m2` không đồng nhất. Kết quả là đến chính xác round 1123, `m2` bầu chọn ra leader là `0xb01...` trong khi `m0` lại bầu chọn `0xCCc...`, gây ra một hard fork hoàn toàn không thể cứu vãn!

## 2. Cách Khắc Phục (The Fix)
Đã **xóa bỏ hoàn toàn đoạn mã Missing Block Heuristic** bên trong file `linearizer.rs`.

Bây giờ hệ thống hoạt động như sau:
- Khi `Linearizer` gặp một block "mồ côi" bị thiếu, nó sẽ **hủy bỏ** (abort) quá trình gom subdag.
- Thành phần `BlockManager` sẽ phát hiện block bị thiếu và lập tức gửi yêu cầu tải block đó từ các node P2P khác trong mạng.
- Node `m2` sẽ tạm dừng quá trình đồng thuận cục bộ cho đến khi nó tải đủ các block mồ côi đó.
- Nhờ vậy, subdag của `m2` được xây dựng lại chuẩn xác 100% từng byte so với `m0`, đảm bảo điểm uy tín (reputation scores) trùng khớp và không bao giờ xảy ra fork.
