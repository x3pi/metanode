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
