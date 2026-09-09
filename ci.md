# 🚀 HƯỚNG DẪN SỬ DỤNG METANODE CI/CD SYSTEM (`ci.sh`)

Tài liệu hướng dẫn nhanh, ngắn gọn và dễ hiểu về cách sử dụng hệ thống CI/CD tự động cho Metanode.

---

## 📌 1. LỆNH CHẠY TEST THỦ CÔNG (`run-now`)

Tất cả các lệnh đều bắt đầu bằng `./ci.sh run-now` từ thư mục gốc `metanode`:

```bash
cd /home/abc/nhat/con-chain-v2/metanode
```

### 🔹 Chạy toàn bộ Test Pipeline
```bash
./ci.sh run-now
```

### 🔹 Chạy MỘT bài test cụ thể (`--only <test_id>`)
```bash
# 1. Test tắt/bật node luân phiên & kiểm chứng Zero-Fork:
./ci.sh run-now --only node_chaos_restart

# 2. Test đo hiệu năng đỉnh Max TPS (TPS Blast):
./ci.sh run-now --only tps_blast

# 3. Test spam 10,000 giao dịch song song (Xapian):
./ci.sh run-now --only spam_xapian_10k

# 4. Test Cross-Chain & Relayer Gateway:
./ci.sh run-now --only cross_chain_gateway

# 5. Chạy bộ Unit Test & E2E cơ bản:
./ci.sh run-now --only snapshot_recovery
```

---

## ⚡ 2. CÁC TÍNH NĂNG QUẢN LÝ CHAIN CỦA CI

Hệ thống CI cho phép can thiệp trực tiếp trạng thái cụm node trước khi chạy test thông qua các cờ (flag):

### 🔄 Khởi động lại cụm node trước khi test (`--restart-chain` hoặc `--restart`)
Tự động gọi `ansible_deploy.sh --restart` để khởi động lại toàn bộ service của các node, chờ RPC sẵn sàng rồi mới test.
```bash
# Khởi động lại chain và chỉ chạy bài test restart recovery:
./ci.sh run-now --only node_chaos_restart --restart-chain

# Hoặc dùng cờ ngắn gọn tương đương:
./ci.sh run-now --only node_chaos_restart --restart

# Khởi động lại chain và chạy toàn bộ pipeline:
./ci.sh run-now --restart-chain
```

### 🧼 Reset toàn bộ chain về Genesis sạch (`--reset-chain` hoặc `--reset`)
Xóa toàn bộ database cũ, nạp genesis mới và đồng bộ lại IP endpoints:
```bash
./ci.sh run-now --only node_chaos_restart --reset-chain

# Hoặc dùng cờ ngắn:
./ci.sh run-now --reset
```

### 🔍 Xem trước luồng thực thi mà không chạy thật (`--dry-run`)
```bash
./ci.sh run-now --dry-run --only node_chaos_restart --restart
```

---

## 🤖 3. QUẢN LÝ CI WATCHER DAEMON (TỰ ĐỘNG CHẠY KHI CÓ COMMIT MỚI)

Daemon chạy ngầm liên tục theo dõi Git remote nhánh `main`. Khi có commit mới được push lên, daemon sẽ tự động kéo code và chạy toàn bộ pipeline.

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./ci.sh start` | Khởi động CI Watcher Daemon chạy ngầm |
| `./ci.sh status` | Xem trạng thái daemon (PID, commit đã test gần nhất) |
| `./ci.sh logs` | Theo dõi log thời gian thực của daemon (`Ctrl+C` để thoát) |
| `./ci.sh restart` | Khởi động lại daemon |
| `./ci.sh stop` | Dừng daemon |

---

## 📋 4. DANH SÁCH TEST ID TRONG HỆ THỐNG

| Test ID | Tên bài test | Mô tả ngắn gọn |
| :--- | :--- | :--- |
| `unit_and_e2e_tests` | Unit & E2E Tests | Chạy 30+ bài test logic RPC, BlockSTM, Double Spending... |
| `cross_chain_gateway` | Cross-Chain & Gateway | Test luân chuyển tài sản giữa các chain con và Public Chain |
| `spam_xapian_10k` | Spam 10k Transactions | Gửi 10,000 txs song song kiểm tra độ ổn định mempool |
| `tps_blast` | TPS Blast Benchmark | Bơm 25,000 txs đo thông lượng đỉnh (Max TPS) |
| `node_chaos_restart` | Chaos Restart & Zero-Fork | Tắt/bật luân phiên từng node, restart cả cụm & verify Zero-Fork |

---

## 🔔 5. THÔNG BÁO TELEGRAM

Hệ thống CI tự động gửi thông báo về nhóm Telegram:
- 🚀 **Bắt đầu pipeline:** Khi phát hiện commit mới hoặc kích hoạt thủ công.
- ⚡ **Tiến độ từng bài test:** Báo ngay khi xong mỗi bài kèm thời gian thực thi (ví dụ `Bước 1/5: 45s`).
- 🚨 **Báo lỗi tức thì:** Trích xuất log nguyên nhân lỗi nếu có bài test bị FAIL.
- 🏁 **Tổng kết:** Bảng thống kê toàn bộ kết quả khi hoàn thành.

Cấu hình bot token và chat ID tại file [`deploy/ci/ci_config.yaml`](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ci/ci_config.yaml).
