# Test History Tool

Tool này kiểm tra xem Node có thể truy xuất chính xác dữ liệu lịch sử (Archive State) hay không. Nó so sánh số dư (Balance) và Nonce của cùng 1 ví tại 2 block khác nhau.
**Lưu ý**: Chỉ chạy đúng với cấu hình `"state_backend": "mpt"`. Với `"nomt"`, Node sẽ luôn trả về state hiện tại.

## Lệnh Chạy

### 1. Test Nhanh (Mặc định)
Lập tức tạo 2 block sát nhau và kiểm tra lịch sử giữa 2 block đó.
```bash
go run main.go -config config-local.json
```

### 2. Test Khoảng Cách Xa (Kiểm tra Pruning)
Tạo mốc Block A, sau đó liên tục tạo giao dịch ép mạng lưới chạy tới **Block B** (cách A một số lượng block nhất định), rồi mới kiểm tra đối chiếu. Dùng để test xem Node có bị xóa mất dữ liệu cũ hay không.
```bash
# Ví dụ: Test khoảng cách 50 block
go run main.go -config config-local.json -wait 50
```

## Đọc Kết Quả
- **Thành công**: Balance và Nonce ở Block A và Block B **KHÁC NHAU**. Tức là truy xuất lịch sử thành công.
- **Thất bại**: Kết quả ở Block A và Block B **GIỐNG NHAU**. Tức là RPC đang trả về trạng thái mới nhất thay vì trạng thái quá khứ (do cấu hình NOMT hoặc bị lỗi Pruning).
