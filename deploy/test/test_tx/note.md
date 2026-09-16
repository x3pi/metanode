# Chạy test RPC chỉ định Node 0 (trên máy 192.168.1.231):
go run main.go -config=config-local.json -data=data.json -url=http://192.168.1.231:10746

# Hoặc test Node 1 (chạy local trên máy này 192.168.1.223, cổng 10747):
go run main.go -config=config-local.json -data=data.json -url=http://127.0.0.1:10747