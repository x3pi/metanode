# 📊 MetaNode Validator Vote Monitor

Công cụ giám sát chuyên sâu thời gian thực về tiến độ biểu quyết (vote), liveness và các bất thường vận hành của từng Validator trong cụm mạng MetaNode.

---

## 🎯 Mục đích & Tính năng

1. **Giám sát Vote & Advancement (Tuần tự từng block, KHÔNG NHẢY CÓC):**
   - **Tuần tự 100%:** Khi mạng sinh block mới (ví dụ từ `#38` nhảy lên `#42`), monitor đưa toàn bộ các block `#39, #40, #41, #42` vào hàng đợi và kiểm tra biểu quyết của từng Validator tuần tự, tuyệt đối không nhảy cóc.
   - **Khởi động an toàn:** Khi khởi động lại, monitor tự động lấy mốc block cao nhất của mạng làm baseline và bắt đầu theo dõi tuần tự từ block kế tiếp, không quét ngược block cũ.
   - **Cảnh báo Vote Stalled theo từng block ($\ge 10$ phút):** Nếu một block bất kỳ đã sinh ra quá 10 phút mà có Validator chưa vote/sync tới, bot Telegram sẽ lập tức cảnh báo đích danh node và số block bị kẹt.
   - **Bảng Kiểm Toán Quorum & Lịch Sử Vote (Recent Block Voting & Quorum Audit):** Trực quan hóa 5 block mới nhất trên terminal và log: Leader sinh block, danh sách Quorum Voters ($\ge 2f+1$) tham gia đúc block ban đầu, danh sách node bị chậm trễ/miss kèm thời gian bắt kịp (`Catch-up Latency`: ví dụ `m1(+12s)`), và trạng thái hoàn thành (`ALL VOTED` hoặc `PENDING`).
   - Nhận diện chính xác Validator Leader đúc block dựa trên đối chiếu địa chỉ `LeaderAddress` / `miner` từ Genesis.
   - Hiển thị độ trễ RPC (latency), block lag và trạng thái vote thời gian thực.
   - Tự động lọc bỏ các node `synconly`, chỉ tập trung theo dõi các Validator tham gia BFT.

2. **Phát hiện Bất Thường (Anomaly Detection) & Cảnh báo Telegram:**
   - ⏱️ **Block Vote Stalled ($\ge 10$ phút):** Cảnh báo khi một validator không vote cho một block cụ thể trong suốt 10 phút (tùy chỉnh `--stall-timeout`).
   - 🛑 **Chain Stalled ($\ge 10$ phút):** Cảnh báo khi toàn bộ mạng blockchain ngừng sinh block mới suốt 10 phút (tùy chỉnh `--chain-stall-timeout`).
   - 🛡️ **Quorum Loss Risk ($< 2f+1$):** Cảnh báo khi số Validator online $< 2f+1$ (ví dụ: cụm 4 node chỉ còn $\le 2$ node online, không thể đạt quorum đồng thuận).
   - 🚨 **Zero-Fork Hash Mismatch:** Đối chiếu block hash giữa các node ở cùng chiều cao block; cảnh báo khẩn cấp nếu phát hiện fork.
   - ⚠️ **Consecutive Missed Blocks:** Cảnh báo khi một validator lỡ vote liên tiếp $\ge N$ blocks (mặc định: 3 blocks).
   - 🟢 **Tự động phục hồi (Recovery Alerts):** Gửi thông báo Telegram khi node hoặc cụm hoạt động bình thường trở lại.

3. **Cơ chế Bỏ qua Node khi Chạy Test (Test Exemption):**
   - Hỗ trợ cờ CLI: `--ignore-nodes m3` hoặc `--stopped-nodes 3,m2`.
   - Tự động nhận diện động file `/tmp/monitors_ignore_nodes` (được sinh ra tự động bởi `run_restart_test.sh` và `run_snapshot_test.sh`).
   - Node đang trong kịch bản test sẽ được gắn nhãn `⚪ STOPPED (TEST)`, **tuyệt đối không gửi cảnh báo giả lên Telegram**.

---

## 🚀 Cách sử dụng

### 1. Khởi chạy nhanh qua runner script:
```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible/monitors/vote_monitor
./run.sh
```

### 2. Bỏ qua node đang test chủ động:
```bash
# Bỏ qua node m3 khi chạy test restart riêng node 3:
./run.sh --ignore-nodes m3

# Hoặc bỏ qua nhiều node:
./run.sh --ignore-nodes m2,m3
```
*(Lưu ý: Nếu đang chạy `run_restart_test.sh` hoặc `run_snapshot_test.sh`, script test đã tự động ghi node vào `/tmp/monitors_ignore_nodes`, monitor sẽ tự động nhận diện mà không cần gõ flag thủ công).*

### 3. Tự biên dịch:
```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible/monitors/vote_monitor
go build -buildvcs=false -o vote_monitor main.go
```

### 4. Chạy chế độ Daemon (chạy ngầm ghi log, gửi Telegram):
```bash
nohup ./vote_monitor --daemon --interval 2s > vote_monitor.log 2>&1 &
```

### 5. Chạy không gửi cảnh báo Telegram (`--no-alert`):
Dành cho trường hợp đang debug, chạy test local hoặc chỉ muốn quan sát terminal/log mà không làm phiền nhóm Telegram:
```bash
# Chạy trực tiếp qua runner script:
./run.sh --no-alert

# Hoặc chạy binary:
./vote_monitor --no-alert

# Chạy daemon ngầm không gửi Telegram:
nohup ./vote_monitor --daemon --no-alert > vote_monitor.log 2>&1 &

# Kết hợp bỏ qua node test và tắt cảnh báo Telegram:
./run.sh --ignore-nodes m3 --no-alert
```

---

## ⚙️ Bảng tham số (CLI Flags)

| Cờ (Flag) | Mặc định | Ý nghĩa |
| :--- | :--- | :--- |
| `--config <path>` | `/tmp/rpc_nodes.json` | Đường dẫn file cấu hình nodes |
| `--interval <duration>` | `2s` | Tần suất quét trạng thái các node |
| `--max-misses <int>` | `3` | Số block lỡ vote liên tiếp để báo động |
| `--stall-timeout <duration>` | `10m` | Thời gian tối đa 1 node không vote trước khi báo động |
| `--chain-stall-timeout <duration>` | `10m` | Thời gian tối đa toàn mạng kẹt block trước khi báo động |
| `--ignore-nodes <list>` | `""` | Danh sách node tạm thời bỏ qua (vd: `m3` hoặc `3,m2`) |
| `--stopped-nodes <list>` | `""` | Alias tương đương với `--ignore-nodes` |
| `--bot-token <token>` | auto | Token Telegram Bot (tự tìm trong env/inventory.yml) |
| `--chat-id <id>` | auto | Chat ID nhận thông báo Telegram |
| `--daemon` | `false` | Chạy nền ghi log, không xóa màn hình terminal |
| `--no-alert` | `false` | Tắt hoàn toàn việc gửi cảnh báo Telegram |
| `--no-stop-flag` | `false` | Tắt tạo file `/tmp/MTN_CHAIN_ERROR_STOP` và không dừng process khi phát hiện fork |
| `--log <path>` | `vote_monitor.log`| Đường dẫn file lưu nhật ký sự kiện |

