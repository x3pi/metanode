# tps_measure — Đo TPS hệ thống phân tán

Tool **chỉ đo TPS** (không gửi TX), chạy từ bất kỳ máy nào có mạng tới các node RPC.

## Build

```bash
cd /home/abc/chain-n/mtn-simple-2025
go build -o tps_measure ./cmd/tool/tps_measure
```

## Flags

| Flag | Mặc định | Mô tả |
|------|----------|-------|
| `--nodes` | `http://localhost:8757` | Danh sách RPC endpoints, phân cách bằng dấu `,` |
| `--watch` | `false` | Chế độ live monitoring (Ctrl+C để dừng) |
| `--from` | `0` | Block bắt đầu cho phân tích range |
| `--to` | `0` | Block kết thúc (`0` = block mới nhất) |
| `--poll` | `2s` | Khoảng cách polling (watch mode) |
| `--window` | `30` | Window tính Peak TPS (giây) |
| `--verify-forks` | `true` | So sánh block hash giữa các nodes |
| `--out` | auto | File JSON report đầu ra |

## Chế Độ Hoạt Động

### 1. Range Mode — Phân tích block đã có

Phân tích blocks đã tồn tại trên chain. Dùng **sau khi** chạy load test xong.

```bash
# Đơn giản — 1 node
tps_measure --nodes "http://127.0.0.1:8757" --from 10 --to 100

# Multi-node + fork check
tps_measure \
  --nodes "http://127.0.0.1:8757,http://192.168.1.232:10749,http://192.168.1.232:10750" \
  --from 10 --to 100

# Chỉ xem TPS, không check fork
tps_measure --nodes "http://127.0.0.1:8757" --from 10 --to 100 --verify-forks=false
```

### 2. Watch Mode — Theo dõi TPS realtime

Chạy **song song** với load generator (`tps_blast`, `tx_sender`, v.v.). Hiển thị TPS realtime cho mỗi block mới.

```bash
# Terminal 1: Bật watch
tps_measure \
  --nodes "http://127.0.0.1:8757,http://192.168.1.232:10749" \
  --watch --window 30

# Terminal 2: Chạy load test
cd cmd/tool/tps_blast
./tps_blast -config config.json -count 5000 -batch 500
```

Nhấn **Ctrl+C** để dừng watch → tool tự sinh report tổng kết.

## Thông Số Kết Quả

### Bảng Block

| Cột | Ý nghĩa |
|-----|---------|
| Block | Số block |
| TXs | Số transactions trong block |
| Instant TPS | `TXs / (timestamp hiện tại - timestamp block trước)`. `—` nếu cùng timestamp |
| Hash | Block hash (rút gọn) |

### Tổng Kết

| Thông số | Ý nghĩa |
|----------|---------|
| **Overall TPS** | Tổng TX / (timestamp block cuối - timestamp block đầu). Phụ thuộc vào timestamp precision |
| **Peak Ns TPS** | TPS cao nhất trong bất kỳ window N giây nào. **Chính xác nhất** khi block interval < 1s |
| **Max TXs/block** | Block lớn nhất chứa bao nhiêu TX |
| **Avg TXs/block** | Trung bình TX mỗi block |
| **Empty blocks** | Số block không có TX (0 = tốt) |
| **Fork check** | So sánh block hash giữa tất cả nodes — PASSED = hệ thống nhất quán |

## Topology Nodes

Cluster hiện tại:

| Node | Server | RPC Port | Accessible từ xa? |
|------|--------|----------|-------------------|
| Node 0 | 192.168.1.231 | 8757 | ❌ Chỉ localhost |
| Node 1 | 192.168.1.231 | 10747 | ❌ Chỉ localhost |
| Node 2 | 192.168.1.232 | 10749 | ✅ |
| Node 3 | 192.168.1.232 | 10750 | ✅ |

**Từ server 231** (đang SSH vào):
```bash
tps_measure --nodes "http://127.0.0.1:8757,http://192.168.1.232:10749,http://192.168.1.232:10750" ...
```

**Từ server 232:**
```bash
tps_measure --nodes "http://127.0.0.1:10749,http://127.0.0.1:10750" ...
```

**Từ máy khác** (cần SSH tunnel cho server 231):
```bash
ssh -L 8757:127.0.0.1:8757 abc@192.168.1.231  # Terminal riêng
tps_measure --nodes "http://127.0.0.1:8757,http://192.168.1.232:10749" ...
```

## Ví Dụ Quy Trình Test TPS

```bash
# 1. Ghi nhận block hiện tại
START_BLOCK=$(curl -s http://127.0.0.1:8757 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
  | python3 -c "import sys,json; print(int(json.load(sys.stdin)['result'],16))")
echo "Start block: $START_BLOCK"

# 2. Chạy load test
cd cmd/tool/tps_blast
./tps_blast -config config.json -count 10000 -batch 1000 -skip-verify

# 3. Đo TPS
cd ../../..
tps_measure \
  --nodes "http://127.0.0.1:8757,http://192.168.1.232:10749,http://192.168.1.232:10750" \
  --from $START_BLOCK --to 0 \
  --out tps_results.json

# 4. Xem report
cat tps_results.json | python3 -m json.tool
```
