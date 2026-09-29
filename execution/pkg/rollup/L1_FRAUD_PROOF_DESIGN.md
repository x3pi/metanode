# Thiết Kế L1 Smart Contract / L1 Logic: Giải Quyết Tranh Chấp & Fraud Proof
**Trạng thái:** BẢN THẢO (DRAFT) - Cập nhật phản hồi | **Ngày:** 2026-09-29

Tài liệu này xác định kiến trúc **Bằng chứng Gian lận (Fraud Proof)** của Rollup (L2) trên L1 (Parent Chain). 
**Phạm vi áp dụng:** Áp dụng cho các L2 Rollup neo vào Parent Chain. (Lưu ý: Luồng Float/TransferFloat cross-node nội mạng vừa hoàn thiện hoạt động dựa trên đồng thuận Quorum nội bộ, không dùng Fraud Proof này - hai hệ thống hoạt động độc lập và bổ sung cho nhau tùy mô hình mạng).

## 1. Nguyên lý Hoạt động & Cấu trúc Dữ liệu (Core Mechanisms)

Hệ thống **không lưu toàn bộ dữ liệu giao dịch** (Zero-DA) trên L1. 
Thay vào đó, Node duy trì một **Cây Merkle Trạng Thái (State Merkle Tree)**. Cấu trúc lá (Leaf) của tài khoản:
- `[Account, TokenID]` (Để tách biệt rõ Native Balance và các tài sản khác).
- `[Số dư (Balance)]`
- `[Nonce]` (Chống Replay Attack).
- `[EventRoot]` (Mã băm đại diện danh sách giao dịch có thứ tự trong Block).

**Định lý Toán học & Tính Tuần Tự trên L1:** 
L1 xác minh sự thay đổi trạng thái theo thứ tự cố định của các event trong `EventRoot`. 
- `Số dư cuối = Số dư đầu + Σ (Events theo đúng thứ tự)`
- **Prefix-sum Rule:** Tại bất kỳ bước trung gian nào trong chuỗi event, `Số dư trung gian ≥ 0`.
- **Nonce Rule:** Mỗi event phải có `Nonce` tăng dần duy nhất và khớp với trạng thái tài khoản.

*Lưu ý Ranh giới Smart Contract (MVM/ERC20):* Thiết kế này chỉ áp dụng cho Native Balance (hoặc Token được ánh xạ ở cấp độ giao thức lá). Số dư ERC20 nằm trong storage của smart contract MVM sẽ cần cơ chế Fraud Proof tương tác (Interactive Fraud Proof) tiêu chuẩn của MVM, nằm ngoài phạm vi của kiến trúc kiểm chứng Native Math này.

## 2. Kiến trúc Giải Quyết Tranh Chấp (Tòa Án L1)

### 2.1. Lớp bảo vệ 1: Ngăn chặn Trộm cắp & Replay (Challenge Forgery)
Nếu Node lén trừ tiền của Alice (không có giao dịch ủy quyền) hoặc replay giao dịch cũ:
- `challengeForgery`: Alice (hoặc Challenger) nộp Merkle Path để kiện.
- L1 kiểm tra `EventRoot`. Node phải nộp Chữ ký của Alice và giao dịch đó phải có `Nonce` hợp lệ.
- Nếu giao dịch là Gas Fee, System Reward hoặc Deposit/Stake (không có chữ ký trực tiếp của user), L1 sẽ kiểm chứng dựa trên các **luật giao thức định sẵn (Protocol Verifiable Rules)**. 
- Node sai phạm 👉 Bị Slash.

### 2.2. Lớp bảo vệ 2: Chống Bỏ Lọt & Gian Lận Thứ Tự (Challenge Omission & Math)
**Kịch bản:** Alice chuyển 100 Coin cho Bob. Node trừ tiền Alice nhưng không cộng cho Bob.
Alice kiện "Bỏ lọt giao dịch":
- L1 ép Node nộp Merkle Path của Bob (Receiver) kèm `EventRoot`.
- **Ngõ cụt 1 (Bỏ lọt):** Không tìm thấy giao dịch của Alice trong `EventRoot` của Bob 👉 Slash.
- **Ngõ cụt 2 (Sai phép toán & Thứ tự):** Node đưa giao dịch vào nhưng cộng sai số dư, hoặc xếp sai thứ tự khiến số dư Bob bị âm trung gian (vi phạm Prefix-sum) 👉 Slash.

### 2.3. Lớp bảo vệ 3: Giải quyết Giấu Dữ Liệu & Thoát Cưỡng Chế (Forced Exit)
Nếu Node giả chết, từ chối cấp Merkle Path hoặc giấu dữ liệu (Data Withholding):
- Người dùng gọi `demandToOpen(accountId)` trên L1, hoặc kích hoạt `forceExit`.
- L1 phát tối hậu thư yêu cầu Node nộp Merkle Path hiện tại để chứng minh trạng thái.
- **Node im lặng:** Hết thời gian ân hạn, Node bị Slash. L1 không chỉ phạt Node mà còn cung cấp đường thoát cưỡng chế.
- **Cơ chế Mass Exit:** Khi Node bị Slash do giấu dữ liệu, hệ thống L2 sẽ bị đóng băng. Người dùng được phép thực hiện rút tiền (withdraw) hàng loạt dựa trên State Root hợp lệ gần nhất (đã được chốt trước đó trên L1).

## 3. Mô hình An Ninh Thực Tế (1-of-N Watchtowers)

Hệ thống **không yêu cầu 100% người dùng phải tự online bảo vệ mình**.
- Bảo mật dựa trên mô hình **1-of-N Honest Challenger** (Cần ít nhất 1 Watchtower trung thực online trong thời gian cửa sổ tranh chấp).
- Bất kỳ ai cũng có thể làm Challenger (Watchtower) để giám sát các State Root.
- Ngay cả khi tài khoản nạn nhân đang ngủ đông, Challenger vẫn có động cơ (phần thưởng từ tiền Slash) để gọi `demandToOpen` hoặc kiện lên L1 nếu phát hiện Node tự in tiền cho chính nó.

## 4. Tham Số Vận Hành (Operational Parameters)
- **Tần suất nộp Root:** Proposer (Validator được ủy quyền) nộp State Root lên L1 định kỳ (VD: mỗi epoch).
- **Cửa sổ Tranh chấp (Dispute Window):** Thời gian chờ để State Root được chốt cứng vĩnh viễn (VD: 7 ngày).
- **Mức Bond / Slash:** L2 Node phải cọc một lượng lớn Native Coin (Bond) trên L1. Khi gian lận, tiền cọc bị tịch thu (Slash): một phần bù đắp cho nạn nhân và trả thưởng cho Challenger, phần còn lại bị đốt.
- **Chi phí Gas kiện tụng (Merkle Depth):** Người kiện phải cọc một khoản phí nhỏ để chống Spam. Độ sâu của Cây Merkle được thiết kế tối ưu để phí Gas verify trên L1 là khả thi. Nếu kiện thành công, phí này được hoàn trả hoàn toàn.

## 5. Kế Hoạch Triển Khai Kiến Trúc (Công nghệ)
Mạng Parent Chain sử dụng kiến trúc State Native Go (không dùng EVM base cho lõi hệ thống). Do đó:
1. **Thiết kế Data Structure:** Tòa án không viết bằng Solidity. L1 sẽ thiết lập cấu trúc lá Merkle `AccountLeaf` chứa `Balance`, `Nonce`, và `EventRoot` ngay trong lõi Go.
2. **Xây dựng Math & Sequence Verifier:** Lập trình Logic Tòa án L1 bằng Go (triển khai trong `pkg/parentchain` hoặc module tương tự `ParentChainGatewayHandler`).
3. **Cài đặt Handler (DisputeResolution):** Thiết lập các API nội bộ / Barrier Transaction cho `challengeForgery`, `challengeOmission`, `demandToOpen` và `forceExit`. L1 trực tiếp kiểm tra Prefix-sum và Nonce rules trên Go.
