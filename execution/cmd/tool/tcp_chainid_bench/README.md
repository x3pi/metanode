# TCP-Direct GetChainId Benchmark (`tcp_chainid_bench`)

Đo TPS cho `GetChainId` qua **TCP trực tiếp** (pkg/network protobuf), bypass HTTP/RPC proxy.

## Cách dùng

```bash
# Mặc định: 200 workers, 10s, port 10000
go run .

# Tùy chỉnh
go run . -workers 200 -duration 10s -addr 127.0.0.1:10000

# Build rồi chạy
go build -o /tmp/tcp_bench . && /tmp/tcp_bench -workers 200 -duration 10s
```

## Tham số

| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `-addr` | `127.0.0.1:10000` | Chain node TCP address (host:port) |
| `-workers` | `200` | Số TCP connections song song |
| `-duration` | `10s` | Thời gian chạy test |

## Hoạt động

Mỗi worker:
1. Tạo 1 TCP connection → nhận `InitConnection` từ server
2. Spam `GetChainId` command → đọc `ChainId` response (8 bytes uint64)
3. Đo latency mỗi request

## Kết quả mẫu

```
╔═══════════════════════════════════════════════════════════════════════╗
║  📊 RESULTS                                                          ║
╠═══════════════════════════════════════════════════════════════════════╣
  ✅ GetChainId (TCP)      45,000 req/s | 450000 ok / 0 fail / 450000 total
                           Latency: 0.05 / 4.20 / 85.00 ms (min / avg / max)
╚═══════════════════════════════════════════════════════════════════════╝
```

## So sánh

| Method | Port | Mô tả |
|--------|------|--------|
| TCP trực tiếp (tool này) | `10000` | Nhanh nhất, zero overhead |
| HTTP Go Master | `8747` | Qua HTTP layer |
| HTTP Proxy | `8545` | Qua reverse proxy + HTTP |

## Tips

- Tăng `ulimit -n 65536` trước khi chạy với nhiều workers
- Port `10000` là TCP port mặc định của chain node (Go Master)
