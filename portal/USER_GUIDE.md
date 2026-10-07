# 📖 HƯỚNG DẪN SỬ DỤNG METANODE PORTAL
*(Account Gate Onboarding, Giao dịch chuyển tiền & Cầu nối Rollup liên cụm)*

---

## 📸 Giao diện thực tế của MetaNode Portal

![MetaNode Portal Onboarding](docs/images/portal_pk_onboarding.png)

---

## 🌟 1. Giới thiệu tổng quan

**MetaNode Portal** là ứng dụng Web quản lý tài khoản và tương tác trực tiếp với mạng lưới **MetaNode Core** (gồm **Parent Chain** đồng thuận BFT DAG và các cụm thực thi **Execution Clusters**).

Ứng dụng cung cấp 4 phân hệ chính:
1. **🛡️ Account Gate & Onboarding:** Đăng ký định danh ví người dùng lên Parent Chain để mở quyền giao dịch trên các cụm thực thi.
2. **💸 Send Transactions:** Gửi giao dịch chuyển tiền native token (MTN) nội cụm.
3. **🌉 Rollup Cross-Cluster:** Cầu nối điều phối nạp / rút tiền giữa các Execution Cluster thông qua chứng thực Parent Chain.
4. **📊 Network Parity & Zero-Fork:** Giám sát sức khỏe mạng lưới, chiều cao block (Block Height) và đảm bảo tính bất biến 100% không phân nhánh (Zero-Fork).

---

## 🛡️ 2. Tại sao phải "Đăng ký ví vào Chain" (Parent Chain Account Gate)?

Trong mô hình đa cụm (Multi-Cluster / Sharding) của MetaNode:
- Mỗi cụm thực thi (Execution Cluster) hoạt động độc lập với tốc độ cao.
- Để **chống tấn công phát lại giao dịch (Replay Attack)** xuyên cụm, Parent Chain đóng vai trò làm sổ cái gốc trung tâm (`AccountRegistry`).
- Mỗi địa chỉ ví (`0x...`) khi tham gia vào mạng lưới sẽ được liên kết 1-1 với một Execution Cluster chủ quản.
- **Nếu chưa đăng ký:** Cụm thực thi sẽ chặn mọi giao dịch gửi ra từ ví đó với mã `Code 69: AccountNotRegistered`.
- **Sau khi đăng ký:** Cổng mở (`Gate PASS`), ví được phép gửi giao dịch, chuyển token và deploy Smart Contract tự do.

---

## 🚀 3. Hướng dẫn chi tiết từng chức năng

### 📍 Phân hệ 1: Account Gate & Onboarding (Đăng ký ví)

Giao diện hỗ trợ **2 chế độ đăng ký linh hoạt**:

```
+---------------------------------------------------------------------------------+
|  [🦊 MetaMask / Web3 Extension]   |   [🔑 Direct Private Key / Custom Key] (Active)  |
+---------------------------------------------------------------------------------+
```

#### 🔹 Lựa chọn A: Dùng Private Key trực tiếp (Khuyên dùng cho Dev & Test)
*(Xem hình ảnh minh họa thực tế bên trên)*

1. **Nhập Private Key:**
   - Dán chuỗi 64 ký tự hex (có hoặc không có tiền tố `0x`) vào ô nhập khóa.
   - Bấm nút **`🎲 Random Key`** nếu bạn muốn Portal tự sinh ngẫu nhiên một cặp khóa mới tinh.
   - Bấm nút **`👁️` (con mắt)** để ẩn hoặc hiện private key; bấm **`📋`** để copy.
2. **Tự động nhận diện Địa chỉ ví (`Derived Address`):**
   - Hệ thống tự động tính toán địa chỉ ví tương ứng dạng `0x...` ngay lập tức.
   - Đồng thời tự động truy vấn thời gian thực:
     - **Cluster Balance:** Số dư MTN trên cụm hiện tại.
     - **Nonce:** Số thứ tự giao dịch (tài khoản mới luôn là `0`).
     - **Parent Gate Status:** Trạng thái đăng ký (`UNREGISTERED (GATED)` nếu chưa đk, hoặc `CONFIRMED (PASS)` nếu đã đk).
3. **Thực hiện Đăng ký lên Chain:**
   - Bấm nút **`✨ Sign & Register to Chain`**.
   - Portal sẽ tự động chạy qua quy trình 4 bước hiển thị trên thanh tiến trình:
     1. `ECDSA Sign`: Dùng Private Key ký trực tiếp mã băm xác thực (`hashToSign`) ngay trên máy bạn.
     2. `Node Relay`: Gửi chữ ký gasless lên node relay thông qua RPC `mtn_registerAccount`.
     3. `Parent Consensus`: Node đóng gói chữ ký BLS của cụm chuyển tiếp lên Parent Chain để đưa vào khối DAG đồng thuận BFT.
     4. `Ready (CONFIRMED)`: Trạng thái đổi thành **`CONFIRMED`**, cổng giao dịch được mở hoàn toàn!
4. **Kích hoạt ví làm việc:**
   - Bấm nút **`🚀 Connect to Portal as Active Wallet`**.
   - Header sẽ hiện nhãn **`PK Mode`** kèm địa chỉ ví vừa kết nối.

---

#### 🔹 Lựa chọn B: Dùng ví MetaMask (Web3 Extension)
1. Bấm chọn tab **`🦊 MetaMask / Web3 Extension`**.
2. Bấm **`Connect MetaMask`** và chấp nhận kết nối trên tiện ích trình duyệt.
3. Nếu ví chưa đăng ký, bấm nút **`Sign & Register Account`**:
   - Cửa sổ MetaMask sẽ hiển thị yêu cầu ký xác thực (`personal_sign`).
   - Bấm **Ký (Sign)**. Toàn bộ quá trình đăng ký là **hoàn toàn miễn phí gas**.
   - Chờ 1 - 2 giây trạng thái trên màn hình chuyển sang xanh lá **`PASS (Gate Open)`**.

---
<!-- 
### 📍 Phân hệ 2: Send Transactions (Chuyển tiền nội cụm)

1. Bấm vào tab **Send Transactions** trên thanh menu.
2. Nhập thông tin:
   - **Recipient Address:** Địa chỉ ví người nhận (`0x...`, 20 bytes).
   - **Amount (MTN):** Số lượng token muốn chuyển (ví dụ: `0.5`).
3. Bấm **Send Transaction**:
   - Nếu đang dùng **Private Key (`PK Mode`)**: Giao dịch được ký bằng ECDSA cục bộ và đẩy trực tiếp lên RPC node.
   - Nếu đang dùng **MetaMask**: MetaMask sẽ bật lên để bạn xác nhận gửi giao dịch.
4. Ngay khi giao dịch được xác nhận trong block, bảng biên nhận bên dưới sẽ hiển thị chi tiết: `Tx Hash`, `Block Number`, `Timestamp`, và trạng thái `Success`.

---

### 📍 Phân hệ 3: Rollup Cross-Cluster (Chuyển tiền liên cụm)

Dùng khi bạn có nhu cầu điều phối tài sản giữa các Execution Cluster (ví dụ từ Cụm 1 sang Cụm 2):
1. Chuyển sang tab **Rollup Cross-Cluster**.
2. Chọn cụm nguồn và cụm đích.
3. Nhập số tiền MTN muốn chuyển và bấm **Initiate Rollup Transfer**.
4. Luồng giao dịch tự động diễn ra:
   - **Bước 1:** Tiền tại Cụm 1 được Lock vào hợp đồng cầu nối Rollup.
   - **Bước 2:** Bằng chứng giao dịch được đồng thuận và xác minh trên Parent Chain.
   - **Bước 3:** Cụm 2 tự động Credit số dư tương ứng cho ví của bạn.

---

### 📍 Phân hệ 4: Network Parity & Zero-Fork (Giám sát mạng lưới)

Cung cấp thông tin kỹ thuật phục vụ việc kiểm toán và giám sát hệ thống:
- Chiều cao block (`Block Height`) thời gian thực của Parent Chain và từng Cụm.
- Băm trạng thái (`State Root`) lưu trữ trên cây NOMT/TrueBlockSTM.
- Đảm bảo 100% không xảy ra phân nhánh (Zero-Fork Invariant): các node luôn đồng thuận trên cùng một digest trước khi cam kết trạng thái.

--- -->