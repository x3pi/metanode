# 🛠️ Metanode Gateway Admin Tool (`register_chains`)

Công cụ dòng lệnh (CLI) hợp nhất để quản trị **Gateway Precompile (`0x1002`)**, phục vụ đăng ký danh bạ, điều phối hạn mức tiền cọc và tra cứu thông tin liên chuỗi.

---

## 🚀 1. Biên Dịch Nhanh

```bash
cd /home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/register_chains
go build -o register_chains .
```

> 💡 **Tự động nhận diện cấu hình:** Tool tự động tìm Root Anchor RPC từ `/tmp/private_chains.json` và tự động tìm thư mục chứa khóa BLS. Bạn không cần truyền lại các cờ đường dẫn dài dòng nếu đang chạy trên máy chủ chứa node.

---

## 📋 2. Các Lệnh Thường Dùng (Copy-Paste)

### 1️⃣ Đăng Ký Danh Bạ & Khóa BLS Cho Chain Mới (`register`)
> Đăng ký danh tính và khóa BLS của các Private Chain lên Root Anchor và đăng ký chéo cho nhau.

```bash
# Đăng ký danh bạ cho các chain 101, 102, 103, 104:
./register_chains -chains "101,102,103,104"
```

---

### 2️⃣ Chuyển Hạn Mức Tiền Cọc Giữa 2 Chain (`transfer-alloc`)
> Trích chuyển hạn mức (Custodial Ceiling) từ một chain đang dư sang chain mới thông qua biểu quyết Quản trị của ủy ban.

```bash
# Cú pháp: ./register_chains -action transfer-alloc -from-chain <Nguồn> -to-chain <Đích> -amount-mtn <Số_MTN>

# Ví dụ: Chuyển 20,000,000 MTN hạn mức từ Chain 101 sang Chain 103:
./register_chains -action transfer-alloc -from-chain 101 -to-chain 103 -amount-mtn 20000000
```

---

### 3️⃣ Tra Cứu Hạn Mức Cung Tiền Của Các Chain (`query-alloc`)
> Xem số dư hạn mức tiền cọc còn lại của từng chain để biết chain nào có thể xuất tiền liên chuỗi.

```bash
./register_chains -action query-alloc -chains "991,101,102,103,104"
```

---

### 4️⃣ Tra Cứu Danh Bạ & Khóa Validator Của Chain (`query-registry`)
> Kiểm tra trạng thái đã đăng ký trong danh bạ, Epoch hiện tại và danh sách Public Key BLS của từng chain.

```bash
./register_chains -action query-registry -chains "101,102,103,104"
```

---

## 📌 3. Bảng Tóm Tắt Tham Số Cờ Lệnh (CLI Flags)

| Cờ Lệnh | Giá trị mặc định | Giải thích |
| :--- | :--- | :--- |
| `-action` | `register` | Hành động: `register` \| `transfer-alloc` \| `query-alloc` \| `query-registry` |
| `-chains` | `101,102,103,104` | Danh sách Chain ID (ngăn cách bằng dấu phẩy) |
| `-from-chain` | `101` | Chain nguồn trích hạn mức (dùng cho `transfer-alloc`) |
| `-to-chain` | `103` | Chain đích nhận hạn mức (dùng cho `transfer-alloc`) |
| `-amount-mtn` | `20000000` | Số lượng MTN muốn chuyển (dùng cho `transfer-alloc`) |
| `-root-anchor`| *(Tự động dò)* | Địa chỉ RPC của Root Anchor (VD: `http://192.168.1.234:10746`) |
| `-chains-dir` | *(Tự động dò)* | Thư mục chứa cấu hình node (VD: `deploy/ansible_private_chains/data`) |
| `-key` | *(Devnet key)* | Khóa ECDSA Private Key trả phí gas nộp proposal |
| `-timelock-wait`| `12` | Số giây chờ Timelock biểu quyết quản trị (Devnet override = 10s) |
