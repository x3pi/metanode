# Kiến trúc Hệ thống TEE-EVM (Trusted EVM)

Tài liệu này tổng hợp các khái niệm kiến trúc và luồng hoạt động khi triển khai một máy ảo EVM bên trong Môi trường Thực thi Tin cậy (TEE - Trusted Execution Environment như Intel SGX, TDX).

## 1. Quản lý Trạng thái Contract (State Management)

> [!NOTE]
> Môi trường bộ nhớ mã hóa (EPC) của TEE có dung lượng rất giới hạn, không thể chứa toàn bộ cơ sở dữ liệu State của EVM (hàng trăm GB).

Giải pháp cốt lõi là **Stateless Execution (Thực thi không trạng thái)** kết hợp với **Merkle Proofs**:
- **Host (Bên ngoài TEE - Không tin cậy):** Lưu trữ toàn bộ dữ liệu State (thường dùng `nomt` hoặc LevelDB/RocksDB) và mã nguồn của EVM.
- **Enclave (Bên trong TEE - Tin cậy):** Chỉ lưu trữ **State Root** hiện tại.
- **Quy trình Xác thực:** Khi EVM trong TEE cần đọc dữ liệu, Host truyền giá trị kèm theo Merkle Proof. TEE băm lại giá trị này và so khớp với State Root đang giữ. Nếu khớp -> Dữ liệu an toàn.

## 2. Chứng minh Đầu vào (Input Verification)

> [!IMPORTANT]
> Làm sao để ngăn Host đẩy các giao dịch giả mạo vào TEE?

- **Remote Attestation (Chứng thực từ xa):** TEE phần cứng tạo ra chứng chỉ (Quote) khẳng định code EVM đang chạy trong một con chip vật lý an toàn, kèm theo một **Public Key** do Enclave tự sinh ra.
- **Mã hóa:** Người dùng verify Quote, sau đó dùng Public Key của Enclave mã hóa giao dịch. Host không thể đọc được nội dung, chỉ TEE mới có Private Key để giải mã.
- **Tích hợp Light Client:** TEE chạy một Light Client bên trong để xác minh Block Headers của Layer 1 (chứa các giao dịch). TEE tự kiểm chứng chữ ký đồng thuận của L1 thay vì tin tưởng Host.

## 3. Sự hoàn hảo của `nomt` trong TEE-EVM

Mặc dù cấu trúc `nomt` (Nearly Optimal Merkle Trie) chỉ duy trì **Trạng thái mới nhất (Current State)** và không lưu lịch sử, nó lại là mảnh ghép hoàn hảo cho Stateless Execution. TEE không cần quá khứ, nó chỉ đi về phía trước.

**Luồng kết hợp giữa Host (`nomt`) và TEE:**
1. **Pre-execution (Mô phỏng):** Host chạy thử (dry-run) các block giao dịch mới bên ngoài TEE để gom toàn bộ các địa chỉ tài khoản và storage slots sẽ bị ảnh hưởng (Read/Write set).
2. **Witness Generation (Tạo bằng chứng):** Host yêu cầu `nomt` tạo ra **Witness** (Bằng chứng Merkle rút gọn) cho tập hợp các keys vừa lấy được, đối chiếu với State Root hiện tại.
3. **Thực thi trong TEE:** Host ném `[Transactions, Witness]` vào TEE. TEE verify Witness bằng State Root hiện tại, sau đó dùng dữ liệu từ Witness để chạy thật các giao dịch.
4. **Commit:** TEE tính ra `New_State_Root` và ký kết quả. Host báo `nomt` tiến cây Merkle lên trạng thái mới.

> [!TIP]
> `nomt` chỉ không phù hợp nếu bạn cần chạy RPC Archive Node (hỗ trợ `eth_getProof` trong quá khứ) hoặc khi mạng bị lệch State sâu (State drift) cần truy xuất lịch sử.

## 4. Đưa bằng chứng lên Layer 1

Sau khi EVM trong TEE thực thi xong, TEE và Host sẽ phối hợp nộp kết quả lên Smart Contract trên Layer 1.

### A. TEE ký vào cái gì? (Attestation Payload)
Bên trong Enclave, TEE tạo ra một gói **State Transition Commitment** và dùng Private Key của chính nó để ký. Nội dung gồm:
- `Chain_ID` (Chống Replay attack).
- `Old_State_Root` (Trạng thái gốc làm mốc bắt đầu).
- `New_State_Root` (Trạng thái mới sau khi thực thi).
- `Batch_Hash` (Mã băm danh sách các giao dịch được chạy).

> **Công thức:** `Signature = TEE_PrivateKey.Sign(Hash(Chain_ID, Old_State_Root, New_State_Root, Batch_Hash))`

### B. Layer 1 xử lý như thế nào?
Layer 1 nhận được `Signature` và Payload từ Host. Quá trình xác minh trên L1 rất nhanh và rẻ:
1. Verify chữ ký `Signature` có đúng thuộc về Public Key hợp lệ của TEE hay không.
2. Kiểm tra `Old_State_Root` nộp lên có khớp với `Current_State_Root` đang lưu trên L1.
3. Verify dữ liệu Data Availability nộp kèm (nếu có).
4. Cập nhật `New_State_Root` và mở khóa các giao dịch rút tiền.

## 5. Lưu trữ Dữ liệu trên Layer 1 (Data Availability)

Trong EVM, **số dư tiền (Balance)** và **trạng thái (Contract Storage)** đều là biến thuộc State Trie. L1 đối xử với chúng như nhau tùy thuộc vào mô hình thiết kế của hệ thống:

> [!WARNING]
> Quyết định mô hình Data Availability ảnh hưởng trực tiếp đến chi phí Gas trên L1 và tính bảo mật của dự án.

- **Mô hình Rollup (Ví dụ: Arbitrum, Optimism):** TẤT CẢ các thay đổi (tiền & contract) đều bị nén lại thành **State Diffs** và lưu lên L1 (CallData/Blobs). L1 lưu backup toàn bộ để đề phòng L2 sập, user vẫn có dữ liệu để tự build cây Merkle và rút tiền.
- **Mô hình Validium / Plasma:** L1 **KHÔNG** nhận thông tin chuyển tiền hay trạng thái contract. L1 CHỈ lưu Root Hash và xác minh chữ ký của TEE. Toàn bộ chi tiết nằm dưới L2. Phí cực rẻ nhưng rủi ro Data Withholding cao.

### Quá trình Rút tiền (Withdrawal)
Chuyển tiền thực sự trên L1 chỉ xảy ra khi quá trình rút tiền hoàn tất: L2 ghi nhận lệnh rút, sinh ra bằng chứng (Merkle Proof). User cầm Merkle Proof này nộp cho Smart Contract trên L1. L1 đối chiếu Proof với `New_State_Root`, nếu khớp thì mở khóa chuyển tiền về ví user trên L1.
