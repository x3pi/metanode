# 🚀 HƯỚNG DẪN SỬ DỤNG METANODE CI/CD SYSTEM (`ci.sh`)

Tài liệu hướng dẫn nhanh, ngắn gọn và dễ hiểu về cách sử dụng hệ thống CI/CD tự động cho Metanode.

---

## ✅ 0. TRƯỚC KHI TRIỂN KHAI THẬT: `production_readiness_check.sh`

**Một lệnh duy nhất trả lời "có được deploy lên môi trường thật không":**

```bash
cd deploy/ci
./production_readiness_check.sh              # build + 3 vòng reset + bộ test correctness
./production_readiness_check.sh --reset-rounds 5   # kỹ hơn ở tầng deploy
./production_readiness_check.sh --full        # + toàn bộ benchmark (TPS/spam), lâu hơn nhiều
```

Chạy tuần tự 3 tầng, dừng ngay ở tầng đầu tiên fail:
1. **Build** — Go + Rust + FFI compile sạch (`build_check.sh --all`).
2. **Deploy stability** — `--reset-all` lặp lại N vòng liên tiếp trên cụm nhiều node/host
   (`verify_multi_reset_stability.sh`) — bắt các race condition chỉ lộ ra khi lặp lại,
   không phải lần chạy đầu.
3. **Ứng dụng** — `blockstm_logic`, `node_chaos_restart`, `snapshot_recovery` (mặc định;
   `--full` chạy thêm cả benchmark hiệu năng).

Kết thúc in báo cáo PASS/FAIL từng tầng + log chi tiết tại `/tmp/production_readiness_*.log`.
Chỉ khi cả 3 tầng đều PASS mới nên bấm nút triển khai thật.

### Dùng riêng lẻ từng phần

- **Chỉ kiểm tra deploy có ổn định qua nhiều lần reset không** (không chạy
  test ứng dụng):
  ```bash
  cd deploy/ci
  ./verify_multi_reset_stability.sh 5   # 5 vòng --reset-all liên tiếp
  ```
- **Diễn tập sự cố vận hành thật** (cảnh báo có tới người trực không, node
  hỏng thật có tự phục hồi qua P2P không, đĩa đầy/mất mạng có đúng runbook
  không) — 4 kịch bản KHÔNG nằm trong test ứng dụng thông thường:
  ```bash
  cd deploy/ci/incident_drills
  cat README.md   # đọc trước: nguyên tắc an toàn + thứ tự khuyến nghị
  ./drill_telegram_alert.sh   # an toàn, chạy được ngay
  # 3 drill còn lại xâm lấn thật (ghi disk, xoá data node, chặn mạng) --
  # mặc định dry-run, cần thêm --confirm và cửa sổ bảo trì để chạy thật
  ```

📄 Bối cảnh đầy đủ (bug đã fix, trạng thái cụm, việc còn mở) của lần hardening
gần nhất: xem `note/deploy_hardening_and_incident_drills_2026-09.md`.

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
