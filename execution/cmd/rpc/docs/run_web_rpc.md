Dưới đây là phiên bản được định dạng lại cho rõ ràng, chuyên nghiệp và dễ theo dõi hơn:

---

# 🚀 Hướng Dẫn Triển Khai & Cấu Hình

### 1. Xây dựng & Tích hợp Giao diện (Frontend)

**Bước 1: Build source code**
Di chuyển vào thư mục dự án DApp và thực hiện lệnh build:

```bash
cd metaCoSign/web3/dapp/register-private-key-rpc
yarn build
```

**Bước 2: Cập nhật RPC Client**
Sao chép thư mục `dist` vừa được tạo ra và đưa vào thư mục client:

* **Copy từ:** `dist` (tại thư mục hiện tại)
* **Paste vào:** `metaCoSign/cmd/rpc-client`

**Bước 3: Kiểm tra truy cập**
Truy cập vào đường dẫn sau trên trình duyệt (thay `<ip-rpc>` bằng IP máy chủ của bạn):
👉 `http://<ip-rpc>:8545/register-bls-key/`

---

### 2. Quản trị Smart Contract (Free Gas)

Để thêm một địa chỉ contract mới vào danh sách được hỗ trợ gas (**Free Gas**), bạn cần thực hiện transaction trực tiếp trên Smart Contract.

* **Mục tiêu:** Gọi hàm `addContractFreeGas`.
* **Yêu cầu quyền:** Phải sử dụng ví **Admin** (Owner).
* **File ABI:** Sử dụng file ABI tại đường dẫn sau để tương tác:
```filepath
/metaCoSign/web3/dapp/register-private-key-rpc/src/abi/Account.json

```