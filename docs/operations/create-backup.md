Để back up có thể gọi các lệnh qua rpc:
# Backup

## Trạng thái chain 3 trạng thái

```
const (
	StateNotLook State = iota
	StatePendingLook
	StateLook
)
```



## Cần look trước khi backup set trạng thái pending look để chain tự look sau khi hoàn tất các giao dịch còn lại.
tạm thời chạy curl với ``mysecretpassword`` là mật khẩu set trong file config (sau này có thể làm phương thức phức xác tạp hơn)

curl -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "admin_setState",
    "params": ["mysecretpassword", 1],
    "id": 1
  }' \
  http://localhost:8646



## Xem trang thái có thể backup chưa nếu 

```
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "admin_getState",
    "params": [],
    "id": 1
  }' \
  http://localhost:8646

```
Kết quả trả về dạng
```
{"jsonrpc":"2.0","id":1,"result":1}

```

Nếu kết quả chuyển sang ``"result":2`` ở trạng thái StateLook có thể gọi lệnh back up 



## Gọi curl lệnh backup kết quả sẽ trả về file name sau khi backup thành công


  curl -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "admin_setState",
    "params": ["mysecretpassword", 0],
    "id": 1
  }' \
  http://localhost:8646



  ## Hiện tại khôi phục chain thì phải tắc chain sử dụng file đã backup chiểu vào thư mục theo config rồi chạy lại



# Lưu và gửi lại nhiều giao dịch từ file


Hiện tại cứ backup giao dịch trong 1 giờ vào 1 file trong thư mục cmd/simple_chain/sample/simple/txs


Cách gửi lại nhiều giao dịch đã backup thì chạy hàm `TestReadAndSendTransactions` trong file `cmd/client/client_test.go`
