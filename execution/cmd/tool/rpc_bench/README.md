# RPC Read Benchmark (`rpc_bench`)

Đo throughput (req/s) và latency cho các JSON-RPC read endpoints.

## Cách dùng

```bash
# Test tất cả methods (mặc định: 100 workers, 10s, port 8545)
go run .

# Tùy chỉnh workers và thời gian
go run . -workers 200 -duration 10s

# Test trực tiếp vào Go Master (bỏ qua proxy)
go run . -url http://127.0.0.1:8747 -workers 200 -duration 10s

# Chỉ test 1 method
go run . -method blockNumber -workers 500 -duration 30s
go run . -method chainId -workers 100 -duration 15s
```

## Tham số

| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `-url` | `http://127.0.0.1:8545` | JSON-RPC endpoint URL |
| `-workers` | `100` | Số goroutines gửi request song song |
| `-duration` | `10s` | Thời gian chạy test (`10s`, `30s`, `1m`) |
| `-method` | `all` | Method test: `blockNumber`, `chainId`, `all` |

## Methods được test

| Method | Payload | Mô tả |
|--------|---------|-------|
| `eth_blockNumber` | ~60 bytes | Đọc block number hiện tại |
| `eth_chainId` | ~55 bytes | Đọc chain ID |

## Kết quả mẫu

```
╔═══════════════════════════════════════════════════════════════╗
║  Method                     TPS      Latency (min/avg/max)   ║
╠═══════════════════════════════════════════════════════════════╣
  eth_blockNumber       22,196 req/s   0.13 / 8.46 / 106 ms
  eth_chainId           23,135 req/s   0.19 / 8.18 / 99 ms
╚═══════════════════════════════════════════════════════════════╝
```

## Ports

| Port | Mô tả | Ghi chú |
|------|--------|---------|
| `8545` | RPC Proxy | Qua reverse proxy, thêm ~3-5K overhead |
| `8747` | Go Master trực tiếp | Nhanh hơn ~20% |
| `6200-6220` | Sub nodes | Test RPC trên sub nodes |

## Tips

- Tăng `ulimit -n 65536` trước khi chạy với nhiều workers
- Workers > 500 thường không tăng TPS do server-side bottleneck
- So sánh port 8545 vs 8747 để đo overhead của proxy layer
