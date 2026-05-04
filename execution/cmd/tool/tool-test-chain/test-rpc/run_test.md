# Set get

``` bash
go run main.go -config=config-server.json -data=data.json


go run main.go -config=config-local.json -data=data-test.json
```

# Xapiant read write (server)

```bash
# local
#chạy v0
go run main.go -config=./config-local.json -data=./test_read_wire_xapian/data-xapian-v0.json
go run main.go -config=./config-local.json -data=./test_read_wire_xapian/data-xapian-v2.json
# server
go run main.go -config=./config-server.json -data=./test_read_wire_xapian/data-xapian-v0.json
go run main.go -config=./config-server.json -data=./test_read_wire_xapian/data-xapian-v2.json
```

## Tính năng xác thực Sự kiện (Expected Events)
Thêm trường `"expected_events"` vào task có `action: "send"` trong file `data.json` để tự động kiểm tra Event phát ra. Nếu không tìm thấy Event trùng khớp, tool sẽ báo lỗi và tự động dừng pipeline.

**Ví dụ:**
```json
{
  "abi_path": "./abi/xapian.json",
  "action": "send",
  "method": "runStep2_ReadBack",
  "args": [],
  "expected_events": [
    {
      "name": "Read_Data_Ext",
      "contains": ["Iphone 13 Pro"]
    }
  ]
}
```

## Chạy thử Native Transfer & Auto Test
Test Native Transfer đã được tích hợp trực tiếp vào trong luồng chạy của `auto_test.sh` ở **Bước 4.1** (chạy ngay sau Xapian V0). 
Trong quá trình test tự động, kịch bản sẽ thực hiện:
1. Đọc account sender từ `config-local.json`.
2. Truy vấn số dư người gửi và người nhận.
3. Bắn lệnh Send 1 META (10^18 wei).
4. Tính toán phí gas và kiểm tra số dư đối chiếu (Verify logic account state update).

Nếu bạn muốn chạy riêng lẻ test Send Native coin:
```bash
cd cmd/tool/tool-test-chain/test-rpc/send-native
go run main.go
```
