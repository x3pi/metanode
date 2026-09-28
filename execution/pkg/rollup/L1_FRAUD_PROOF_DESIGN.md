# Thiết Kế L1 Smart Contract: Giải Quyết Tranh Chấp & Fraud Proof
**Trạng thái:** CHỐT PHƯƠNG ÁN (FINAL) | **Ngày:** 2026-09-28

Tài liệu này xác định kiến trúc **Bằng chứng Gian lận (Fraud Proof)** của Rollup trên L1 (Parent Chain). 
**Mục tiêu cốt lõi:** 
- **Bảo mật 100%:** An toàn tuyệt đối trước Node thực thi độc hại.
- **Zero-DA:** Chi phí L1 gần như bằng không (Không lưu Data Availability).
- **Trải nghiệm 100% Web3 (Gửi & Quên):** Không bắt người dùng tương tác phức tạp (Không cần ký 2 lần, không bắt buộc P2P).
- **Phạm vi hỗ trợ:** Tập trung giải quyết triệt để **Giao dịch chuẩn (Chuyển tiền ERC20 / Native Coin)** nhờ sức mạnh kiểm tra phép cộng/trừ của L1.

---

## 1. Nguyên lý Hoạt động & Cấu trúc Dữ liệu (Core Mechanisms)

Hệ thống hoàn toàn **không lưu dữ liệu giao dịch** trên L1. 
Thay vào đó, Node duy trì một **Cây Merkle Trạng Thái (Event-List Merkle Tree)** đặc thù cho từng Tài khoản:
- Mỗi chiếc lá (Leaf) đại diện cho một Tài khoản.
- Lá lưu trữ: `[Số dư (Balance)]` + `[EventRoot]`.
- `EventRoot` là mã băm của danh sách toàn bộ các giao dịch gửi/nhận ảnh hưởng đến tài khoản đó trong Block hiện tại.

**Định lý Toán học khóa cứng trên L1:** 
`Số dư cuối Block = Số dư đầu Block + Tổng (Các giao dịch trong EventRoot)`

---

## 2. Kiến trúc Giải Quyết Tranh Chấp (Tòa Án L1)

### 2.1. Lớp bảo vệ 1: Ngăn chặn Trộm cắp (Forgery)
Nếu Node lén trừ tiền của Alice (không có giao dịch ủy quyền), Alice nộp Merkle Path để kiện.
- `challengeForgery`: L1 kiểm tra `EventRoot` của Alice. Node phải nộp Chữ ký của Alice cho cái giao dịch làm suy giảm số dư đó. Node không có chữ ký 👉 Bị Slash.

### 2.2. Lớp bảo vệ 2: Chống Bỏ Lọt & Gian Lận Số Dư Đầu Cuối (Challenge Omission & Math)
**Kịch bản:** Alice chuyển 100 Coin cho Bob (Ký qua MetaMask). Node trừ 100 của Alice nhưng không cộng cho Bob (tuồn cho Hacker).
Alice nộp Chữ ký giao dịch `Chuyển 100 cho Bob` lên L1 để kiện tội "Bỏ lọt giao dịch":
- L1 ép Node phải nộp Merkle Path của Bob (Receiver) bao gồm Số dư và `EventRoot`.
- **Ngõ cụt 1 (Bỏ lọt):** L1 kiểm tra `EventRoot` của Bob, nếu không thấy giao dịch 100 Coin của Alice 👉 Node bị Slash vì bỏ lọt giao dịch.
- **Ngõ cụt 2 (Sai phép toán):** Node đưa giao dịch của Alice vào `EventRoot` của Bob để trốn tội bỏ lọt. Nhưng theo định lý toán học, L1 cộng 100 Coin vào số dư cũ của Bob. Nếu số dư cuối của Bob trên cây Merkle không tăng lên tương ứng 👉 L1 lập tức phát hiện sai lệch phép cộng 👉 Node bị Slash.

Nhờ L1 hiểu được phép cộng trừ, Node không có bất kỳ khe hở nào để giấu tiền hoặc tính sai!

### 2.3. Lớp bảo vệ 3: Giải quyết Giấu Dữ Liệu (Cưỡng chế Mở sổ)
Để có dữ liệu đi kiện, người dùng gọi RPC xin Node cấp Merkle Path. Nếu Node giả chết, từ chối cấp:
- Người dùng gọi hàm `demandToOpen(accountId)` trên L1.
- L1 phát tối hậu thư yêu cầu Node phải nộp Merkle Path của người dùng lên L1 trong 24 giờ.
- **Node im lặng:** Hết 24h, Node bị Slash vì tội giấu dữ liệu.
- **Node nộp Merkle Path:** Người dùng tải về từ L1 và dùng nó để tiếp tục kiện tội Bịa đặt (Forgery) hoặc Bỏ lọt (Omission). Node không có đường thoát.

---

## 3. Lợi ích Đột phá của Giải pháp này

- **Giữ nguyên Trải nghiệm (UX):** Giải quyết hoàn toàn bài toán Inbound Transfer (Tiền người khác chuyển tới) mà không cần thay đổi thói quen người dùng. Alice chuyển tiền cho Bob xong là xong, Bob không cần làm gì cũng chắc chắn nhận được tiền.
- **Không cần Watchtower / Data Availability:** L1 đóng vai trò "Máy tính Casio". Chỉ khi có kiện tụng, L1 mới yêu cầu Node nộp đúng nhánh Merkle của 2 tài khoản (Gửi và Nhận) để làm toán cộng trừ. Không một Bytes dữ liệu thừa nào bị lưu lên L1.

---

## 4. Kế hoạch Triển khai Smart Contract (Next Steps)
1. **Thiết kế Data Structure trên Solidity:** Viết cấu trúc struct cho `AccountLeaf` (gồm Balance và EventRoot).
2. **Viết logic Math Verifier:** Cài đặt hàm xác minh đường dẫn Merkle kết hợp kiểm tra vòng lặp cộng/trừ các giao dịch trong `EventRoot`.
3. **Cài đặt Tòa án L1 (`DisputeResolution.sol`):** Xây dựng các hàm `challengeForgery`, `challengeOmission`, và cơ chế Cưỡng chế Mở sổ (`demandToOpen`).
