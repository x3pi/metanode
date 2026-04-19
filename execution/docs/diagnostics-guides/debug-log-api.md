## Debug Log API

Tài liệu này hướng dẫn sử dụng hai phương thức JSON-RPC mới trên namespace `debug` nhằm hỗ trợ kiểm tra nhanh log theo thư mục ngày tháng.

### 1. Các phương thức
- `debug_listLogFiles` &rarr; liệt kê tên file log trong một thư mục ngày cụ thể.
- `debug_getLogFileContent` &rarr; đọc nội dung file log, tự động giới hạn dung lượng trả về.

### 2. Tham số chung
| Trường | Kiểu | Bắt buộc | Mô tả |
| --- | --- | --- | --- |
| `root` | string | Không | Đường dẫn thư mục gốc chứa log. Bỏ trống để sử dụng giá trị từ cấu hình `log_path` (ví dụ `./sample/simple/data/logs`). Chấp nhận cả đường dẫn tuyệt đối. |
| `date` | string | Không | Chuỗi ngày cần truy cập. Hỗ trợ định dạng `YYYY-MM-DD`, `YYYY/MM/DD` hoặc `YYYYMMDD`. Nếu bỏ trống, thao tác trực tiếp trên `root`. |

### 3. Liệt kê danh sách file log
```bash
curl -X POST http://127.0.0.1:8646 \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "debug_listLogFiles",
    "params": [{
      "root": "./cmd/simple_chain/sample/simple/data/logs",
      "date": "2025-11-13"
    }]
  }'
```

**Kết quả mẫu**
```bash
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": [
    "App__.log",
    "Commit.log",
    "IntermediateRoot_.log"
  ]
}
```

> Tip: Để đọc log trên node sub-write, thay `root` thành `./cmd/simple_chain/sample/simple/data-write/logs`.

### 4. Đọc nội dung file log
```bash
curl -X POST http://127.0.0.1:8646 \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "debug_getLogFileContent",
    "params": [{
      "root": "./cmd/simple_chain/sample/simple/data-write/logs",
      "date": "2025/11/13",
      "fileName": "App__.log",
      "maxBytes": 1048576
    }]
  }'
```
curl -X POST https://139.59.243.85:8446 \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "debug_getLogFileContent",
    "params": [{
      "root": "./cmd/rpc-client/log/2025/12/10/rpc-client.log",
      "date": "2025/11/13",
      "fileName": "App__.log",
      "maxBytes": 1048576
    }]
  }'
**Kết quả mẫu**
```bash
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": "2025-11-13T00:00:01Z: App khởi động...\n..."
}
```

### 5. Lưu ý an toàn
- Hệ thống kiểm tra `fileName` để tránh truy cập vượt ngoài thư mục log.
- `maxBytes` (mặc định 4MB) giúp hạn chế kích thước phản hồi; nếu file lớn, chỉ phần cuối file được trả về và tự động bỏ dòng đầu bị cắt dở.
- Đường dẫn trong ví dụ sử dụng relative path; có thể chuyển sang path tuyệt đối khi triển khai thực tế (ví dụ `/mnt/Data/AAA/mtn-simple-2025/cmd/simple_chain/sample/simple/data/logs`).

### 6. Gợi ý sử dụng
- Tạo script gom log theo node (master vs sub-write) bằng cách thay đổi `root`.
- Tích hợp vào dashboard nội bộ để hỗ trợ kiểm tra nhanh mà không cần SSH vào máy chủ.

