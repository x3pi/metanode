# 🚀 HƯỚNG DẪN SỬ DỤNG METANODE CI/CD SYSTEM (`ci.sh`)

Tài liệu hướng dẫn đầy đủ, ngắn gọn và chuẩn xác nhất về hệ thống tự động hóa kiểm thử liên tục (CI/CD) cho Metanode.

Hệ thống cung cấp file script tiện ích `./ci.sh` (symlink trỏ tới [`deploy/ci/ci_watcher.sh`](file:///home/abc/nhat/consensus-chain/metanode/deploy/ci/ci_watcher.sh)) để bạn có thể điều khiển toàn bộ pipeline trực tiếp từ thư mục gốc `metanode`.

---

## 📌 1. CHẠY TEST THỦ CÔNG (`run-now`)

Đứng tại thư mục gốc của dự án:
```bash
cd /home/abc/nhat/consensus-chain/metanode
```

### 🔹 1.1 Chạy mặc định trên nhánh Git hiện tại
Hệ thống **tự động nhận diện nhánh Git local bạn đang đứng** (`git branch --show-current`), không cần sửa cấu hình:
```bash
./ci.sh run-now
```

### 🔹 1.2 Chỉ định nhánh Git kiểm thử (`--branch` hoặc `-b`)
Khi bạn truyền cờ `-b <nhánh>`, hệ thống sẽ **TỰ ĐỘNG `git checkout <nhánh>` VÀ TỰ ĐỘNG `git pull origin <nhánh>`** để kéo mã nguồn mới nhất từ remote về máy trước khi test (bạn không cần phải checkout hay pull thủ công):
```bash
# Tự động checkout sang dev, pull code mới nhất và chạy test:
./ci.sh run-now --branch dev

# Dùng cờ ngắn gọn -b kết hợp bài test cụ thể:
./ci.sh run-now -b dev --only node_chaos_restart
```

### 🔹 1.3 Kéo mã nguồn mới nhất cho nhánh local hiện tại (`--pull`)
Khi bạn đang đứng ở nhánh local (không dùng cờ `-b`) nhưng muốn kéo code mới nhất từ remote về trước khi test:
```bash
./ci.sh run-now --pull
```

### 🔹 1.4 Chạy MỘT bài test cụ thể (`--only <test_id>`)
Chỉ chạy duy nhất bài test bạn cần kiểm tra:
```bash
# 1. Test Block-STM logic (32+ kịch bản):
./ci.sh run-now --only blockstm_logic

# 2. Test Cross-Chain Relayer & Client:
./ci.sh run-now --only cross_chain_core

# 3. Test Spam Contract Multi-RPC (10,000 txs):
./ci.sh run-now --only spam_contract

# 4. Test đo thông lượng đỉnh Max TPS (TPS Blast):
./ci.sh run-now --only tps_blast

# 5. Test tắt/bật node luân phiên & kiểm chứng Zero-Fork:
./ci.sh run-now --only node_chaos_restart

# 6. Test tạo snapshot và phục hồi trạng thái node qua Ansible:
./ci.sh run-now --only snapshot_recovery
```

### 🔹 1.5 Kết hợp chọn bài test VÀ chỉ định nhánh Git (Ví dụ: chạy trên `dev`)
Bạn có thể kết hợp bất kỳ bài test nào với cờ `--branch` (hoặc `-b`):

```bash
# 🎯 Chạy bài test Chaos Restart trên nhánh dev (Tự động checkout dev & pull code mới nhất):
./ci.sh run-now --only node_chaos_restart --branch dev

# Dùng cờ ngắn gọn -b tương đương:
./ci.sh run-now --only node_chaos_restart -b dev

# 🔄 Khởi động lại cụm node và test Chaos Restart trên nhánh dev:
./ci.sh run-now --only node_chaos_restart -b dev --restart

# 🧼 Reset cụm node sạch sẽ và test Chaos Restart trên nhánh dev:
./ci.sh run-now --only node_chaos_restart -b dev --reset
```

---

## ⚡ 2. CÁC TÍNH NĂNG QUẢN LÝ CỤM CHAIN TRƯỚC KHI TEST

Hệ thống CI cho phép can thiệp trực tiếp trạng thái của cụm node trước khi bắt đầu bài test:

### 🔄 Khởi động lại cụm node (`--restart-chain` hoặc `--restart`)
Tự động gọi `ansible_deploy.sh --restart` để khởi động lại toàn bộ service của các node, chờ RPC sẵn sàng rồi mới test:
```bash
# Restart chain và chỉ chạy bài test restart recovery:
./ci.sh run-now --only node_chaos_restart --restart

# Restart chain và chạy toàn bộ pipeline:
./ci.sh run-now --restart-chain
```

### 🧼 Reset toàn bộ chain về Genesis sạch (`--reset-chain` hoặc `--reset`)
Xóa database cũ, nạp genesis mới (kèm ví TPS/Spam) và mở lại ports:
```bash
./ci.sh run-now --only node_chaos_restart --reset-chain

# Hoặc cờ ngắn gọn:
./ci.sh run-now --reset
```

### ⏭️ Bỏ qua bước tiền xử lý của bài test (`--skip-pre-action`)
Bỏ qua hành động reset/deploy tự động định nghĩa trong `ci_config.yaml` cho bài test đó (giữ nguyên trạng thái chain đang chạy):
```bash
./ci.sh run-now --only snapshot_recovery --skip-pre-action
```

### 🔍 Xem trước luồng thực thi mà không chạy thật (`--dry-run`)
Mô phỏng toàn bộ các bước, lệnh thực thi và kiểm tra genesis mà không làm thay đổi chain:
```bash
./ci.sh run-now --dry-run
./ci.sh run-now --dry-run -b dev --only tps_blast
```

---

## 📋 3. DANH SÁCH BÀI TEST CHUẨN TRONG HỆ THỐNG

Danh sách mã bài test (`test_id`) được đồng bộ chuẩn theo [`deploy/ci/ci_config.yaml`](file:///home/abc/nhat/consensus-chain/metanode/deploy/ci/ci_config.yaml):

| Mã bài test (`--only`) | Tên bài test | Hành động trước test (`pre_action`) | Mục tiêu kiểm chứng |
| :--- | :--- | :--- | :--- |
| `blockstm_logic` | Block-STM Logic Tests | `reset_chain` | Chạy 32+ kịch bản logic RPC, BlockSTM, Double Spending |
| `cross_chain_core` | Cross-Chain Relayer Tests | `deploy_private` | Triển khai 4 Private Chains (101-104) và test Relayer Gateway |
| `spam_contract` | Spam Contract Multi-RPC | `none` | Bơm 10,000 giao dịch song song nhiều rounds kiểm tra mempool |
| `tps_blast` | TPS Blast Benchmark | `prepare_tps` | Nạp 50k+ ví vào genesis, reset Public Chain và đo Max TPS |
| `node_chaos_restart` | Chaos Rolling Restart | `reset_chain` | Tắt/bật luân phiên từng node trong cụm và verify Zero-Fork |
| `snapshot_recovery` | Snapshot & Node Recovery | `reset_chain` | Tạo snapshot ở block cao, xóa DB 1 node và khôi phục từ snapshot |

> 💡 **Mẹo:** Bạn có thể bật/tắt các bài test mặc định chạy trong pipeline bằng cách sửa trường `enabled: true / false` trong file `ci_config.yaml`.

---

## 🤖 4. QUẢN LÝ CI WATCHER DAEMON (TỰ ĐỘNG CHẠY KHI CÓ COMMIT MỚI)

Daemon chạy ngầm liên tục theo dõi Git remote. Khi có commit mới được đẩy lên, daemon sẽ tự động kéo code mới về và kích hoạt toàn bộ test pipeline:

| Lệnh | Ý nghĩa & Tùy chọn |
| :--- | :--- |
| `./ci.sh start` | Khởi động daemon theo dõi nhánh hiện tại |
| `./ci.sh start --branch <nhánh>` | Khởi động daemon theo dõi riêng một nhánh cụ thể |
| `./ci.sh status` | Xem trạng thái daemon (PID, nhánh đang theo dõi, commit gần nhất) |
| `./ci.sh logs` | Xem log realtime của daemon (`Ctrl+C` để thoát xem) |
| `./ci.sh restart` | Khởi động lại daemon |
| `./ci.sh stop` | Dừng daemon chạy ngầm |

---

## 🔔 5. THÔNG BÁO TỰ ĐỘNG QUA TELEGRAM

Hệ thống tích hợp thông báo thời gian thực về nhóm Telegram:
- 🚀 **Khởi động:** Gửi thông tin Server, commit hash, người thực hiện, nội dung commit và nhánh kiểm thử.
- ⚡ **Tiến độ từng bước:** Báo ngay khi từng bài test hoàn thành kèm thời gian chạy (ví dụ `Bước 1/6: 45s`).
- 🚨 **Báo lỗi tức thì:** Báo động ngay lập tức kèm đoạn trích xuất log lỗi nếu có bài test bị FAIL.
- 🏁 **Báo cáo tổng kết:** Bảng thống kê toàn bộ kết quả Pass/Fail/Skip khi kết thúc.

Cấu hình bot token và chat ID tại [`deploy/ci/ci_config.yaml`](file:///home/abc/nhat/consensus-chain/metanode/deploy/ci/ci_config.yaml):
```yaml
telegram:
  enabled: true
  bot_token: "YOUR_BOT_TOKEN"
  chat_id: "YOUR_CHAT_ID"
```
