# 🔍 Block Hash Checker — Giám Sát Đồng Thuận & Toàn Vẹn Chuỗi Block

Công cụ kiểm tra lệch hash (StateRoot, BlockHash, TxOrder) giữa các node trong cụm và phát hiện đứt gãy chuỗi (`parentHash` không khớp block trước).

---

## 🚀 1. Cách chạy nhanh qua `start_monitors.sh` (Khuyên dùng)

Thư mục: `deploy/ansible/monitors`

```bash
# 1. Bật monitor tiếp tục từ checkpoint gần nhất (hoặc block 1 nếu chưa có checkpoint)
./start_monitors.sh

# 2. Bật monitor bắt đầu từ một block chỉ định trở đi mãi mãi (không cần --to)
./start_monitors.sh --from 15000
# Hoặc cú pháp ngắn:
./start_monitors.sh 15000

# 3. Dừng monitors
./start_monitors.sh stop
```

> 💡 **Cơ chế Checkpoint tự động:**
> - Monitor tự động ghi lại block đã kiểm tra mới nhất vào `checkpoint.json`.
> - Khi restart monitor, nó sẽ **tự động chạy tiếp từ block tiếp theo**, không bao giờ quét lại từ block 0/1.
> - Nếu truyền `--from <N>`, monitor sẽ ghi đè và bắt đầu quét từ block `N`.

---

## ⚡ 2. Chạy trực tiếp `block_hash_checker`

Thư mục: `deploy/ansible/monitors/block_hash_checker`

### A. Giám sát liên tục theo thời gian thực (Watch Mode)
Quét từ block chỉ định tới đỉnh hiện tại, sau đó theo dõi liên tục các block mới sinh ra (mãi mãi):

```bash
# Giám sát từ block 15000 trở đi liên tục (có gửi cảnh báo Telegram & dừng khi lỗi)
./block_hash_checker --watch --interval 5s --from 15000

# Chế độ quan sát thụ động (không dừng monitor khi phát hiện lệch hash)
./block_hash_checker --watch --interval 5s --from 15000 --no-stop-flag

# Tự động tiếp nối từ checkpoint.json (không cần truyền --from)
./block_hash_checker --watch --interval 5s
```

### B. Quét kiểm tra 1 lần một khoảng block cố định (Scan Mode)
```bash
# Quét kiểm tra từ block 14140 đến 15000
./block_hash_checker --watch=false --from 14140 --to 15000

# CHO TELE CHẠY ###############
go run main.go --watch --interval 5s

# multiple machine (bằng lệnh trực tiếp)
go run main.go --watch --interval 5s --nodes "m0=http://192.168.1.234:8757,m1=http://192.168.1.233:10747,m2=http://192.168.1.231:10749"

# multiple machine (sử dụng file config riêng biệt)
go run main.go --watch --interval 5s --config config-m-nodes.json --no-stop-flag
```
```

---

## ⚙️ 3. Bảng tham số (Flags) quan trọng

| Cờ (Flag) | Mặc định | Ý nghĩa |
| :--- | :--- | :--- |
| `--from` | `0` | Block bắt đầu kiểm tra (`0` = tự động đọc từ `checkpoint.json` hoặc bắt đầu từ block 1) |
| `--to` | `0` | Block kết thúc (`0` = lấy block mới nhất). Không áp dụng khi bật `--watch` |
| `--watch` | `true` | Bật chế độ chạy liên tục kiểm tra block mới |
| `--interval` | `2s` | Khoảng thời gian giữa các lần quét trong watch mode |
| `--config` | `config.json` | File chứa danh sách RPC endpoint các node (`config-m-nodes.json`) |
| `--no-stop-flag` | `false` | Không tạo cờ dừng `/tmp/MTN_CHAIN_ERROR_STOP` khi gặp lỗi (chỉ ghi log) |

---

## 📋 4. Các file log & trạng thái

- **`checkpoint.json`**: Lưu mốc block cao nhất đã kiểm tra hợp lệ.
- **`block_checker_daemon.log`**: Log hoạt động thời gian thực của monitor chạy ngầm.
- **`hash_mismatch_alert.log`**: Chi tiết cảnh báo khi phát hiện lệch hash hoặc sai `parentHash`.
- **`mismatches_<from>_<to>.csv`**: Danh sách chi tiết các block lệch xuất ra dạng bảng CSV.
