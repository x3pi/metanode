
# Run 1 explorer là chạy 1 con sub với thêm các tham số  cho `config.json`


```
  "explorer_db_path":"./sample/simple/data-write-2/other/explorer",
  "is_explorer": true,

```

# Hướng dẫn sử dụng curl để truy vấn API tìm kiếm giao dịch

Tài liệu này hướng dẫn cách sử dụng `curl` để gửi yêu cầu đến API JSON-RPC, cụ thể là phương thức `mtn_searchTransactions`.

---

## 1. Cấu trúc lệnh curl cơ bản

Mọi yêu cầu đến API đều có cấu trúc chung như sau:

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["QUERY_STRING", OFFSET, LIMIT],
    "id": 1
}'
```

**Trong đó:**
- `QUERY_STRING`: Chuỗi truy vấn để tìm kiếm. Đây là phần bạn sẽ thay đổi nhiều nhất.
- `OFFSET`: Vị trí bắt đầu lấy kết quả (dùng để phân trang). Ví dụ: 0 là bắt đầu từ kết quả đầu tiên.
- `LIMIT`: Số lượng kết quả tối đa muốn nhận.

---

## 2. Các ví dụ về truy vấn tìm kiếm (`QUERY_STRING`)

Dưới đây là các lệnh curl hoàn chỉnh cho từng loại tìm kiếm dựa trên các chỉ mục đã được thiết lập.

### 2.1. Tìm kiếm theo **Hash** của Giao dịch

Lệnh này tìm kiếm một giao dịch cụ thể bằng hash của nó. Thay `0x...` bằng hash bạn muốn tìm.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["hash:0x123abc...", 0, 1],
    "id": 1
}'
```

---

### 2.2. Tìm kiếm theo địa chỉ **người gửi** (`from`)

Tìm tất cả các giao dịch được gửi từ một địa chỉ ví cụ thể.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["from:0xSENDER_ADDRESS", 0, 10],
    "id": 1
}'
```

---

### 2.3. Tìm kiếm theo địa chỉ **người nhận** (`to`)

Tìm tất cả các giao dịch được gửi đến một địa chỉ ví hoặc contract.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["to:0xRECIPIENT_ADDRESS", 0, 10],
    "id": 1
}'
```

---

### 2.4. Tìm kiếm theo **số Block**

- **Tìm trong một block cụ thể:**

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["block:12345", 0, 20],
    "id": 1
}'
```

- **Tìm trong nhiều block (sử dụng OR):**

```bash
curl -X POST https://rpc-proxy-sequoia.iqnb.com:8446/ \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["block:24 OR block:25 OR block:30", 0, 20],
    "id": 1
}'
```

---

### 2.5. Tìm kiếm theo **Read Hash** (`rHash`)

Tìm kiếm giao dịch theo hash của receipt. Thay `0x...` bằng rHash bạn muốn tìm.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["r_hash:0x456def...", 0, 1],
    "id": 1
}'
```

---

## 3. Các truy vấn liên quan đến **Token (ERC20)**

Đây là các truy vấn mạnh mẽ nhất dựa trên logic bạn đã thêm vào.

### 3.1. Tìm tất cả giao dịch của một **Token** cụ thể

Tìm mọi giao dịch `transfer` hoặc `transferFrom` của một contract token.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["token:0xTOKEN_CONTRACT_ADDRESS", 0, 25],
    "id": 1
}'
```

---

### 3.2. Tìm giao dịch Token theo **người gửi** (`t_from`)

Tìm các giao dịch mà một ví đã gửi token đi. Điều này khác với việc gửi ETH (`from`).

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["t_from:0xTOKEN_SENDER_ADDRESS", 0, 10],
    "id": 1
}'
```

---

### 3.3. Tìm giao dịch Token theo **người nhận** (`t_to`)

Tìm các giao dịch mà một ví đã nhận token.

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["t_to:0xTOKEN_RECIPIENT_ADDRESS", 0, 10],
    "id": 1
}'
```

---

## 4. Kết hợp các truy vấn

Bạn có thể tạo ra các truy vấn rất phức tạp bằng cách kết hợp các điều kiện với **AND**, **OR**, **NOT**.

### 4.1. Tìm giao dịch Token A mà ví B gửi cho ví C

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["token:0xTOKEN_A_ADDRESS AND t_from:0xWALLET_B_ADDRESS AND t_to:0xWALLET_C_ADDRESS", 0, 5],
    "id": 1
}'
```

---

### 4.2. Tìm tất cả giao dịch Token mà ví A đã nhận, **ngoại trừ** từ Token B

```bash
curl -X POST http://localhost:8848 \
-H "Content-Type: application/json" \
-d '{
    "jsonrpc": "2.0",
    "method": "mtn_searchTransactions",
    "params": ["t_to:0xWALLET_A_ADDRESS NOT token:0xTOKEN_B_ADDRESS", 0, 15],
    "id": 1
}'
```

---

> **Lưu ý:** Bạn có thể kết hợp nhiều điều kiện để tạo ra các truy vấn phù hợp với nhu cầu tìm kiếm của mình.




### 4.3. Nếu cấu hình truy cập qua rpc client giao dịch readonly  thì sử dụng tương tự với method `mtn_searchTransactionsReadOnly`
```bash
    curl -X POST http://localhost:8545 \
    -H "Content-Type: application/json" \
    -d '{
        "jsonrpc": "2.0",
        "method": "mtn_searchTransactionsReadOnly",
        "params": ["block:12345", 0, 20],
        "id": 1
    }'
```