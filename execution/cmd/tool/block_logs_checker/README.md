# block_logs_checker

Tool so sánh **`eth_getLogs`** giữa nhiều node để kiểm tra logs trong receipts có đồng nhất hay không.  
Hoạt động tương tự `block_hash_checker` nhưng thay vì so sánh block hash, tool này kiểm tra nội dung logs từng block trên tất cả nodes.

---

## Build

```bash
cd /home/abc/nhat/con-chain-v2/metanode/execution
go build -o block_logs_checker ./cmd/tool/block_logs_checker/
```

Hoặc build trực tiếp trong thư mục tool:

```bash
cd /home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/block_logs_checker
go build -v .
```

---

## Cách dùng

### 1. Quét 1 lần (scan mode)

```bash
./block_logs_checker \
  --nodes "master=http://192.168.1.233:8757,node2=http://192.168.1.233:10747,node3=http://192.168.1.233:10749" \
  --from 1 \
  --to 5000
```

Nếu `--to 0` (hoặc không truyền), tool tự lấy block mới nhất từ node đầu tiên.

### 2. Quét với filter address/topics (giống config-getlogs.json)

```bash
./block_logs_checker \
  --nodes "master=http://192.168.1.233:8757,node2=http://192.168.1.233:10747" \
  --from 1 --to 100 \
  --address "0x00000000000000000000000000000000B429C0B2" \
  --topics "0xb528e3a3d4cbfd0b61a83cc28a004e801777b8ed6274adee62a727632fee66dd,0xa92be8788ad097ce638b4b327d9930cc1d8545abf05c0a399f37b7a6ce8b94ce"
```

- `--address`: lọc theo contract address (tùy chọn, bỏ qua = lấy tất cả logs)
- `--topics`: danh sách topic0 phân cách bằng dấu `,` (OR logic giống eth_getLogs topics[0])

### 3. Watch mode (giám sát liên tục)

```bash
./block_logs_checker \
  --watch \
  --nodes "master=http://192.168.1.233:8757,node2=http://192.168.1.233:10747,node3=http://192.168.1.233:10749" \
  --interval 15s \
  --check-last 3
```

- Tự động check `--check-last` blocks gần nhất mỗi `--interval`
- **Tự dừng** khi phát hiện lệch, ghi log vào `logs_mismatch_alert.log`
- Nhấn `Ctrl+C` để dừng thủ công

---

## Flags

| Flag | Default | Mô tả |
|------|---------|-------|
| `--nodes` | *(bắt buộc)* | Danh sách nodes: `"name=url,name2=url2"` |
| `--from` | `1` | Block bắt đầu kiểm tra |
| `--to` | `0` | Block kết thúc (`0` = lấy block mới nhất) |
| `--batch` | `20` | Số block kiểm tra song song mỗi lần |
| `--timeout` | `10s` | Timeout mỗi RPC call |
| `--address` | *(rỗng)* | Filter contract address |
| `--topics` | *(rỗng)* | Filter topics (OR, phân cách bằng dấu phẩy) |
| `--watch` | `false` | Bật watch mode |
| `--interval` | `15s` | Chu kỳ check trong watch mode |
| `--check-last` | `3` | Số block gần nhất check mỗi cycle (watch mode) |

---

## Output

### Khi không có lệch:
```
✅ KẾT QUẢ: Tất cả 5000 blocks có LOGS KHỚP giữa 3 nodes (12.5s)
```

### Khi phát hiện lệch:
```
🚨 KẾT QUẢ: Phát hiện 2 blocks LỆCH LOGS!
   ✅ Khớp: 4998 | 🚨 Lệch: 2 | ❌ Lỗi: 0 (12.5s)

⚠️  Block 321:
   master:      3 logs
      [0] addr=0x00000000...B429C0B2 topics=0xb528e3a3... logIdx=0x0
      [1] addr=0x00000000...B429C0B2 topics=0xa92be878... logIdx=0x1
      [2] addr=0x00000000...B429C0B2 topics=0xb528e3a3... logIdx=0x2
   node2:       2 logs
      [0] addr=0x00000000...B429C0B2 topics=0xb528e3a3... logIdx=0x0
      [1] addr=0x00000000...B429C0B2 topics=0xa92be878... logIdx=0x1

📄 Chi tiết đã ghi vào: logs_mismatches_1_5000.csv
```

### Watch mode output:
```
[14:32:01] #1 Heights: master=1205  node2=1205  node3=1204 ✅ logs khớp 3 blocks (block 1203→1205)
[14:32:16] #2 Heights: master=1207  node2=1207  node3=1206 ✅ logs khớp 3 blocks (block 1205→1207)
```

---

## Output files

| File | Nội dung |
|------|---------|
| `logs_mismatch_alert.log` | Alert chi tiết khi watch mode phát hiện lệch |
| `logs_mismatches_<from>_<to>.csv` | CSV với log count của từng node per block lệch |

---

## Logic so sánh

Tool so sánh logs theo **fingerprint** duy nhất cho từng log entry:
```
fingerprint = transactionHash | logIndex | address | topics (joined) | data
```

Hai nodes được coi là **khớp** khi:
1. Số lượng logs bằng nhau
2. Tập fingerprints hoàn toàn giống nhau (kể cả thứ tự không quan trọng)

---

## Ví dụ với config-getlogs.json

File `tool-test-chain/test-rpc/base/config-getlogs.json` có thể chuyển thành lệnh:

```bash
./block_logs_checker \
  --nodes "node1=http://192.168.1.233:8757,node2=http://192.168.1.233:10747,node3=http://192.168.1.233:10749,node4=http://192.168.1.233:10750" \
  --from 21 --to 21 \
  --address "0x00000000000000000000000000000000B429C0B2" \
  --topics "0xb528e3a3d4cbfd0b61a83cc28a004e801777b8ed6274adee62a727632fee66dd,0xa92be8788ad097ce638b4b327d9930cc1d8545abf05c0a399f37b7a6ce8b94ce"
```
