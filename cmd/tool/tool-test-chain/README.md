# 🚀 Caller-RPC: Siêu công cụ Tự động hóa Smart Contract (MVM)

---

## 🛠 Cách sử dụng cơ bản

Tất cả những gì bạn cần làm là chạy `main.go` với 2 file cấu hình cơ bản là `config.json` và `data.json`:

```bash
# Chạy trực tiếp mã nguồn bằng Go
go run main.go -config=config.json -data=data.json
```

### 🚩 CLI Override Flags (Thay đổi cấu hình động)

Nếu bạn không muốn sửa file `config.json`, bạn có thể đổi cấu hình trực tiếp từ tham số dòng lệnh:

* `-rpc`: Ghi đè URL của Node (vd: `http://192.168.1.100:8545`)
* `-chain`: Ghi đè Chain ID (vd: `2026`)
* `-pk`: Đăng nhập bằng Private Key khác.

**Ví dụ:**

```bash
go run main.go -config=config.json -data=data.json -rpc=http://localhost:8545 -chain=2026
```

---

## 📂 1. Cấu hình Mạng lưới (`config.json`)

Nơi định nghĩa gốc rễ để kết nối lên Node MVM.

```json
{
    "rpc_url": "http://127.0.0.1:8545",
    "private_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
    "chain_id": 991
}
```

---

## 📋 2. Kịch bản Giao dịch (`data.json`)

File `data.json` **bắt buộc phải là một MẢNG `[]`**. Bên trong chứa các Object tương ứng với các Task.
Có 3 dạng `action` chính được hệ thống hỗ trợ:

### A. Triển khai Contract Mới (`"action": "deploy"`)

Để triển khai một hợp đồng, action phải là `deploy`. Bạn dán Bytecode HEX vào trường `input_data`, **không cần điền** thuộc tính `contract`.

```json
{
  "action": "deploy",
  "input_data": "0x60806040523480156100... (BYTECODE DÀI)"
}
```

### B. Gọi hàm Thay đổi Trạng thái (`"action": "send"` hoặc `"write"`)

Thực thi lệnh gửi một `eth_sendRawTransaction`. Lệnh này tốn Gas và sẽ in ra thời gian chờ hệ thống Mining.

```json
{
  "contract": "0x123...abc",
  "abi_path": "../contract/build/contract.abi",
  "action": "send",
  "method": "updateProfile",
  "args": [
    {
      "name": "Alex",
      "age": 28
    }
  ]
}
```

### C. Phản vấn/Đọc Dữ Liệu (`"action": "call"` hoặc `"read"`)

Thực thi một mô phỏng `eth_call`. Không tốn Gas, trả về tức thì. Kết quả Output từ MVM sẽ được Tool nhìn vào ABI và Unpack ra mảng dữ liệu dễ nhìn. Nhớ để trống tham số mảng `"args": []` nếu hàm không cần đầu vào.

```json
{
  "abi_path": "../contract/build/contract.abi",
  "action": "call",
  "method": "getProfileAndScores",
  "args": []
}
```

### D. Bypass ABI bằng RAW Hash (Cấp cao)

Nếu bạn không có file JSON ABI, hoặc bạn muốn đánh lừa mạng lưới/test case bằng một String HEX tự chế: chỉ cần cung cấp `"input_data"`. (Mã Hex của Signature Name + Data encode).

```json
{
  "contract": "0x123...abc",
  "action": "call",
  "method": "getProfile",
  "input_data": "0xd6afc9b1"
}
```

*Lưu ý: Nếu một Task cung cấp **CẢ** Method và Input_Data, Tool sẽ đứng hình chờ bạn bấm phím trên Terminal để ra quyết định muốn test cái nào!*

---

## 🧩 Luồng hoạt động của "Smart Inheritance" (Kế thừa Address)

1. Giả sử Task 1 của bạn là `"action": "deploy"`. Tool thực hiện Deploy và nhận về Address `0x9F945...`.
2. Task 2 của bạn là `"action": "send"`, nhưng bạn lại **Không khai báo** tên `"contract": ...` trong file JSON.
3. Tool `caller-rpc` sẽ cực kì thông minh **Tự Nhặt** cái Address `0x9F945...` từ Task 1 lắp vào Task 2 và gửi đi.
4. Tính năng này giúp bạn viết một chuỗi quy trình Auto-Test rắc rối từ gốc đến ngọn mà không cần nhọc công copy Address sau mỗi lần deploy lại!

---

💡 **Developer Tips:** Nếu bạn thắc mắc làm sao lấy mã băm `0x...` cho `input_data`, hãy sử dụng công cụ băm `contract_sodility/getHashFunc.go` có sẵn trong bộ tool test.

---

## 🔨 Biên Dịch Hợp Đồng (Compile Contract)

Thay vì cài đặt phức tạp hay dùng trình duyệt, bạn có thể build nhanh bất kì file Solidity (`.sol`) nào để lấy ngay Bytecode và file định nghĩa ABI bằng cách sử dụng công cụ:

```bash
./build_contract.sh contract/demo-test.sol
```

Nó sẽ chạy npx solc, tự động đẩy Bytecode ra màn hình cho bạn copy dán vào file cấu hình `data.json`, cũng như lưu ABI vào thư mục `build/` ngay cạnh script của bạn.
