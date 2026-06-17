### 2. Giám sát liên tục theo thời gian thực (Watch mode)

Cú pháp cho mạng 5 Nodes (cấu hình trong `config.json`):

```bash
# single machine
#### KHÔNG ẢNH HƯỞNG LUỒNG TELE ####
go run main.go --watch --interval 5s --no-stop-flag

# CHO TELE CHẠY ###############
go run main.go --watch --interval 5s

# multiple machine (bằng lệnh trực tiếp)
go run main.go --watch --interval 5s --nodes "m0=http://192.168.1.234:8757,m1=http://192.168.1.233:10747,m2=http://192.168.1.231:10749"

# multiple machine (sử dụng file config riêng biệt)
go run main.go --watch --interval 5s --config config-m-nodes.json --no-stop-flag
```
