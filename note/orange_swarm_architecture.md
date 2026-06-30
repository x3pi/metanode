# Kiến Trúc "Orange Swarm": Phân Mảnh Hợp Đồng & TEE Phân Tán

Tài liệu này mô tả kiến trúc tổng thể của Metanode khi sử dụng mạng lưới thiết bị giá rẻ (Orange Pi) có TEE (ARM TrustZone) để xử lý Smart Contract (EVM) và quản lý trạng thái giữa Public Chain và Private Chain.

## 1. Tổng Quan Mô Hình

Kiến trúc chia làm 2 lớp chính:
* **Public Chain (Lớp Chốt sổ - Settlement Layer):** Là Ethereum/BSC/v.v.., chứa Bridge Contract. Làm nhiệm vụ giữ tài sản thật (TVL) và xác minh chữ ký cuối cùng.
* **Private Chain (Mạng lưới Orange Swarm):** Mạng lưới hàng ngàn thiết bị Orange Pi hoạt động dưới dạng DePIN (Mạng cơ sở hạ tầng vật lý phi tập trung). Làm nhiệm vụ tính toán, chạy EVM và quản lý trạng thái nội bộ.

---

## 2. Cơ Chế Quản Lý Trạng Thái (State Management)

Để vượt qua điểm yếu về RAM của Orange Pi (TEE thường chỉ có ~16-32MB), trạng thái của Private Chain KHÔNG ĐƯỢC lưu tập trung nguyên một khối. Chúng ta áp dụng **State Sharding theo Smart Contract (Mô hình Actor)**.

### A. Trạng thái trên Private Chain (Micro-State)
1. **Phân Rã State:** Mỗi Smart Contract (ví dụ: Token USDT, Sàn DEX, Game NFT) sở hữu một "Trạng thái vi mô" (Micro-State) độc lập.
2. **Lưu Trữ Phân Tán:** Orange Pi sẽ **không** tải toàn bộ dữ liệu Blockchain. Mạng lưới tự động phân công:
   - **Tổ 1 (Ví dụ 10 Orange Pi):** Chỉ lưu trữ và quản lý State của Contract USDT.
   - **Tổ 2 (Ví dụ 10 Orange Pi):** Chỉ lưu trữ và quản lý State của Contract DEX.
3. Nhờ việc "chia để trị", bộ nhớ cần thiết để chạy một Contract giảm xuống mức cực tiểu, hoàn toàn lọt thỏm vào RAM của TEE.

### B. Trạng thái trên Public Chain
- Public Chain không quan tâm đến từng Micro-State. Nó chỉ lưu trữ một **State Root Tổng** (được băm từ các Micro-State của Private Chain) và danh sách Khóa công khai BLS (Public Key) của các máy Orange Pi.

---

## 3. Kiến Trúc Xử Lý Contract trong TEE (TrustZone)

Mỗi máy Orange Pi chạy một phiên bản **Micro-EVM** (như lõi `revm` viết bằng Rust) nằm sâu bên trong TEE (OP-TEE OS).

### Bước 1: Tiếp nhận Giao dịch
- Người dùng gửi giao dịch gọi Contract USDT.
- Giao dịch được tự động định tuyến thẳng đến **Tổ 1** (Những Orange Pi đang phụ trách USDT).

### Bước 2: Thực thi trong TEE (In-Enclave Execution)
- Máy Orange Pi nạp Micro-State của USDT và Giao dịch vào môi trường TEE.
- Lõi Micro-EVM bên trong TEE thức dậy, chạy tính toán logic (cộng trừ số dư). TEE hoạt động hoàn toàn khép kín, không bị HĐH Linux bên ngoài của Orange Pi nhìn trộm hay can thiệp.

### Bước 3: Ký Xác Nhận (Partial BLS Signature)
- Sau khi chạy ra kết quả (New Micro-State), TEE tự động lấy **Khóa BLS Thành phần (Share Key)** được giấu kín của nó để ký lên kết quả.
- Orange Pi nhả kết quả và chữ ký ra mạng lưới. Xóa sạch RAM.

### Bước 4: Giao Tiếp Chéo (Cross-Contract Call - Bất đồng bộ)
- **Nếu có tương tác:** Giả sử giao dịch yêu cầu đổi USDT lấy Token khác (Tương tác giữa Contract USDT và DEX).
- **Luồng xử lý:** Sau khi TEE Tổ 1 xử lý xong phần trừ USDT, nó xuất ra một **"Biên lai" (Receipt)** có chữ ký. Biên lai này được gửi sang Tổ 2. TEE của Tổ 2 đọc Biên lai, thấy chữ ký TEE của Tổ 1 là hợp lệ, nó sẽ tiếp tục chạy phần logic của DEX. Quá trình diễn ra bất đồng bộ giúp hệ thống chạy song song hàng ngàn thao tác mà không bị thắt cổ chai.

---

## 4. Khớp Nối Private và Public Chain (Cơ Chế Chốt Sổ)

Nhờ uy tín của TEE trên hàng ngàn máy Orange Pi, việc đồng bộ với Public Chain trở nên cực kỳ nhanh chóng và an toàn.

1. **Tổng Hợp Chữ Ký (BLS Aggregation):** Một máy chủ Host nhẹ (Aggregator Node) làm nhiệm vụ gom chữ ký. Khi thu thập đủ chữ ký từ Tổ Orange Pi (ví dụ 7/10 máy đồng ý với kết quả), nó dùng thuật toán toán học gộp 7 chữ ký này thành **1 Chữ ký Đại Diện (Master Signature)**.
2. **Nộp lên Public Chain:** Chữ ký Đại Diện và State Root mới được nộp lên Smart Contract của Public Chain.
3. **Xác thực Tức Thời (Instant Settlement):** 
   - Smart Contract trên Public Chain tốn rất ít gas để kiểm tra Chữ ký Đại Diện.
   - Nếu chữ ký đúng, Public Chain biết chắc chắn rằng kết quả này đã được **chạy thực tế và bảo chứng bởi TEE** của rất nhiều máy Orange Pi.
   - Public Chain chốt State ngay lập tức. Người dùng có thể rút tiền (Withdraw) về ví MetaMask chỉ trong vài giây, KHÔNG CẦN CHỜ ĐỢI 7 ngày như cơ chế Optimistic Rollup thông thường.

---

## 5. Bảo Mật & Chống Lừa Đảo

* **Random Shuffling (Đảo tuyến ngẫu nhiên):** Để tránh việc 10 người chủ của Tổ 1 thông đồng với nhau làm bậy, mạng lưới sẽ tự động "đổi ca" ngẫu nhiên. Orange Pi đang chạy USDT hôm nay có thể bị ép chuyển sang chạy GameFi vào ngày mai. Hacker không thể đoán trước lịch trình để hội đồng tấn công.
* **Hardware Attestation (Xác minh phần cứng):** TEE của Orange Pi định kỳ phải nộp bằng chứng (Quote) về mạng lưới để chứng minh nó đang chạy đúng phần mềm Micro-EVM chưa bị sửa đổi. Máy nào chạy HĐH giả lập sẽ bị mạng lưới loại bỏ lập tức (Slashing).

**Kết Luận:** Kiến trúc này biến một bầy Orange Pi yếu ớt thành một Siêu máy tính xử lý Hợp đồng Thông minh phi tập trung, vừa có tốc độ của Web2 (nhờ chia tải), vừa có bảo mật cấp độ phần cứng (nhờ TEE).
