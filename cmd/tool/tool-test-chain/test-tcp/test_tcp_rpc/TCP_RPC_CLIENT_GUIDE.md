# TCP-RPC Client — Hướng dẫn sử dụng

## Run rpc

``` cd rpc-client -> go run main.go ```

## Run test transfer

```go run main.go -test=transfer```

# test crud

```go run main.go -test=demo```

# test đăng ký bls

```go run main.go -test=bls -count 5 -out bls_keys.json```

# goi các hàm trên chain 4200

```go run main.go -test=chain```

```go run main.go -test=all```

# test free gas

```go run main.go -test=freegas```
