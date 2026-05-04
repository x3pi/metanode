``` bash
go run main.go -config=config-server.json -data=data.json
go run main.go -config=config-nhat.json -data=transfer.json
go run main.go -config=config-nhat.json -data=data.json


```

``` bash
go run main-no-none.go -config=config-local.json -data=data.json
```

## Tính năng xác thực kết quả (Verify)
Thêm trường `"verify": [...]` vào task có `action: "call"` (hoặc `"read"`) trong file `data.json` để tự động so sánh giá trị trả về. Nếu kết quả không khớp, tool sẽ báo lỗi và dừng ngay lập tức.

**Ví dụ:**
```json
{
  "abi_path": "../../abi/normal-test.json",
  "action": "call",
  "method": "getValue",
  "args": [],
  "verify": [1234]
}
```
