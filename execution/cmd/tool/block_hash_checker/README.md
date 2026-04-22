# Block Hash Checker

Công cụ dòng lệnh dùng để tự động cào (fetch) và đối chiếu mã băm (Block Hash) của các khối (blocks) ở nhiều node cục bộ hoặc trên mạng thông qua giao thức `JSON-RPC`.
Giúp phát hiện nhanh hiện tượng chệch/lệch (fork/mismatch) của chuỗi khối (blockchain) đang chạy giữa các node Validator.

## Chức năng

- **Quét 1 lần theo khoảng (Range mode):** Dò từ block `X` đến block `Y` để đếm số block khớp và xuất ra những block bị lệch.
- **Giám sát liên tục theo thời gian thực (Watch mode):** Chạy lặp lại (ví dụ mỗi khoảng 5 giây), kiểm tra hash của N block mới nhất xem có đồng nhất hay không.
- **Xuất CSV:** Các block lỗi hoặc không khớp hash sẽ được lưu vào file CSV để tiện phân tích sau.

## Cách sử dụng

Công cụ yêu cầu ngôn ngữ **Go 1.23+**.
Chạy trực tiếp từ mã nguồn: `go run main.go [các tham số]`

### 1. Quét 1 lần (từ block 1 đến block mới nhất)

Cú pháp:

```bash
go run main.go --nodes "node0=http://localhost:8757,node1=http://localhost:10747,node2=http://localhost:10749,node3=http://localhost:10750" --from 1 --to 0
```

- `--nodes`: Danh sách các node cần kiểm tra, định dạng `Tên Node=URL_RPC`, nối nhau bằng dấu phẩy.
- `--from`: Block bắt đầu (Mặc định: 1)
- `--to`: Block kết thúc. Truyền `0` nếu muốn tự động lấy block mới nhất của mạng.
- `--batch`: Quét bao nhiêu block đồng thời (Mặc định: 50).

### 2. Giám sát liên tục theo thời gian thực (Watch mode)

Cú pháp cho mạng 5 Nodes:

```bash

# single machine
go run main.go --watch --interval 5s --check-last 10 --nodes "m0=http://127.0.0.1:8757,m1=http://127.0.0.1:10747,m2=http://127.0.0.1:10749,m3=http://127.0.0.1:10750,m4=http://127.0.0.1:10748"

# on multiple machine
go run main.go --watch --interval 5s --check-last 10 --nodes "m0=http://192.168.1.234:8757,m1=http://192.168.1.234:10747,m2=http://192.168.1.233:10749,m3=http://192.168.1.231:10750,m4=http://192.168.1.231:10748"


# single machine
go run main.go --watch --interval 5s --check-last 10 --nodes "m0=http://localhost:8757,s0=http://localhost:8646,m1=http://localhost:10747,s1=http://localhost:10646,m2=http://localhost:10749,s2=http://localhost:10650,m3=http://localhost:10750,s3=http://localhost:10651,m4=http://localhost:10748,s4=http://localhost:10649"


# on multiple machine
go run main.go --watch --interval 5s --check-last 10 --nodes "m0=http://192.168.1.234:8757,s0=http://192.168.1.234:8646,m1=http://192.168.1.234:10747,s1=http://192.168.1.234:10646,m2=http://192.168.1.233:10749,s2=http://192.168.1.233:10650,m3=http://192.168.1.231:10750,s3=http://192.168.1.231:10651,m4=http://192.168.1.231:10748,s4=http://192.168.1.231:10649"

```

- `--watch`: Bật chế độ chạy nền, giám sát liên tục.
- `--interval`: Khoảng thời gian giữa mỗi chu kỳ quét (Mặc định: 10s).
- `--check-last`: Lấy bao nhiêu block gần nhất trên đỉnh blockchain để đối chiếu ở mỗi vòng lặp (Mặc định: 5).

## Đầu ra

- Khi tất cả đều OK: `✅ KẾT QUẢ: Tất cả [x] blocks KHỚP giữa [y] nodes`.
- Khi có sai lệch: `🚨 KẾT QUẢ: Phát hiện [z] blocks LỆCH HASH!`. Sẽ in chi tiết hash của từng node đối với các block bị lệch, và hỗ trợ xuất lưu thành file `mismatches_[from]_[to].csv` tại thư mục chạy tool để dễ đối soát.
