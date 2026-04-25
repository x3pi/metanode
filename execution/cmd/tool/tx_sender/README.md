# tx_sender

Công cụ `tx_sender` dùng để gửi các giao dịch theo lô (deploy hợp đồng, gọi hàm on-chain, gọi `eth_call` off-chain) lên mạng lưới blockchain MetaNode. Công cụ giúp quá trình thử nghiệm hợp đồng thông minh, đẩy traffic (spam mode) hoặc lấy dữ liệu trở nên tự động.

## 1. Cấu trúc thư mục
Tool sẽ đọc thông tin cấu hình từ 2 file chính:
- **`config.json`**: Chứa thông tin kết nối và Private Key của người gửi (sẽ được dùng để ký giao dịch).
- **`data.json`**: Chứa danh sách các giao dịch (hành động) cần thực thi tuần tự.

---

## 2. Thông số cấu hình (`config.json`)

**Mẫu `config.json`**:
```json
{
    "version": "0.0.1.0",
    "private_key": "2b3aa0f...<private_key>...dda1203b",
    "parent_connection_address": "127.0.0.1:4200",
    "parent_address": "0x824fef8A3cE4b93C546209CC254D97E5Fee804e0",
    "chain_id": 991,
    "parent_connection_type": "client"
}
```

- **`private_key`**: Private key của tài khoản gửi. Các giao dịch được tạo ra sẽ **được ký** bằng khóa này. *(Lưu ý: Địa chỉ tạo ra từ khóa này phải khớp với địa chỉ từ trường `from_address` khai báo bên trong `data.json`)*.
- **`parent_connection_address`**: Địa chỉ TCP của Sub-node (ví dụ: `127.0.0.1:4200`) nhận giao dịch trên Socket.
- **`chain_id`**: ID của chuỗi (Ví dụ: `991`).
- `parent_address`: Thuộc tính nội bộ để Client nhận diện địa chỉ mạng (không quyết định địa chỉ gửi của các transaction).

---

## 3. Dữ liệu Giao dịch (`data.json`)

Tool sẽ đọc một array chứa nhiều Object, mỗi Object là một transaction cần thực hiện.

**Mẫu `data.json`**:
```json
[
    {
        "from_address": "0x824fef8A3cE4b93C546209CC254D97E5Fee804e0",
        "action": "deploy",
        "address": "",
        "input": "60806040523480156...",
        "amount": "0",
        "name": "0-SimpleStorage"
    },
    {
        "from_address": "0x824fef8A3cE4b93C546209CC254D97E5Fee804e0",
        "action": "call",
        "address": "0",
        "input": "0x6057361d...",
        "amount": "0",
        "name": "SimpleStorage-store(2222)"
    }
]
```

Các hành động (`action`) được hỗ trợ:
1. **`deploy`**: Triển khai smart contract từ bytecodes. Nếu trường `address` bỏ trống hoặc `"0"`, hệ thống sẽ tự động dự đoán và lưu lại địa chỉ contract mới để dùng cho các bước dưới.
2. **`call`**: Gửi giao dịch gọi hàm lên mạng lưới (sửa đổi state thực tế, tốn Nonce và Gas). Nếu `address` truyền là `"0"` hoặc `""` thì tool sẽ tự tương tác với địa chỉ hợp đồng vừa được `deploy` ngay trước đó.
3. **`read_call`**: Gọi hàm truy vấn lấy dữ liệu off-chain bằng `eth_call` JSON-RPC (Read-only, Không thay đổi số Nonce, Không ghi lên chuỗi). Yêu cầu Node phải mở port HTTP API, được trỏ tới bởi cờ `--api-url`.

---

## 4. Các lệnh và tham số chạy (CLI flags)

Khi chạy `go run .` hoặc build ra tệp nhị phân, bạn có thể truyền kèm nhiều cờ bổ trợ:

- **`--config`**: Đường dẫn tới file cấu hình. Phù hợp nếu có nhiều network (Mặc định: `"config.json"`)
- **`--node`**: Ghi đè địa chỉ TCP kết nối tới Node (Ví dụ: `127.0.0.1:4200`). Nếu không truyền, mặc định sẽ lấy từ trường `parent_connection_address` trong file cấu hình.
- **`--data`**: Đường dẫn tới file kịch bản giao dịch (Mặc định: `"data.json"`)
- **`--loop`**: Bật chế độ chạy lặp vô hạn. Tool sẽ thực thi tuần tự danh sách trong data.json, sau khi hoàn tất sẽ lặp lại chu kỳ (Phù hợp test chịu tải mạng lưới/spam tx).
- **`--api-url`**: Cổng HTTP RPC của Node để dành cho các action `read_call` (Mặc định: `"http://127.0.0.1:8757"`)
- **`--register-bls`**: Tool sẽ tự động kiểm tra xem key BLS của address đã được kích hoạt trên hệ thống hay chưa bằng vòng đời `nonce`. Nếu bật `--register-bls`, tool sẽ tự gửi giao dịch BLS cho ví mới trắng tinh. Mặc định là `false` để tránh việc gửi giao dịch làm kẹt luồng lúc Node gặp sự cố.
- **`--async`**: Gửi tất cả giao dịch trong lô đồng loạt vào mạng mà không chờ Receipt cho từng cái ngay. Các giao dịch này có khả năng được đóng gói cùng nhau trong một khối duy nhất.

---

## 5. Ví dụ chạy

**Thực thi bình thường 1 lần (đọc cấu hình mặc định):**
```bash
go run main.go
# hoặc ngắn gọn hơn
go run .
```

**Chạy kịch bản spam lặp đi lặp lại vô thời hạn:**
```bash
go run main.go --loop
```

**Chạy lần đầu tiên cho ví mới để tự đăng ký khoá BLS lên mạng:**
```bash
go run main.go --register-bls
```

**Sử dụng tệp tin riêng biệt (ví dụ testnet):**
```bash
go run main.go --config config_testnet.json --data data_spam.json --api-url http://node.meta:8757
```

---

## 6. Xử lý lỗi thường gặp (Troubleshooting)

- **Lỗi chữ ký không hợp lệ (Invalid Signature)**: Nếu `from_address` khai báo trong `data.json` không khớp toán học với địa chỉ sinh ra từ `private_key` cung cấp trong `config.json`, Node sẽ bắt lỗi sai chữ ký.
- **Treo/Timeout kịch bản đang đợi Receipt**: Nếu Consensus Node của mạng bị chết, bạn vẫn gửi được giao dịch vào Mempool của Sub-node. Do mạng không ra block, Transaction không có Receipt dẫn đến Terminal của bạn chờ đến khi Timeout 60s.
- **Lỗi hiển thị `Another tx_sender is already running`**: Tool có cơ chế bảo vệ chạy song song qua file `/tmp/tx_sender.pid`. Nếu quá trình đang chạy bị văng (Force Kill) khiến file `.pid` vẫn sót lại, bạn hãy tiến hành xoá bằng tay thông qua lệnh: `rm -f /tmp/tx_sender.pid` để khởi động lại.
