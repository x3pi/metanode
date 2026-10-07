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

### ⚡ Chạy Test Cho Chain Con (Execution Clusters - `chain_a`)
```bash
# Cách 1: Dùng lệnh tắt tiện lợi (Tự động reset Execution Clusters và chạy 34 bài test Block-STM):
./ci.sh test-child

# Cách 2: Chạy trực tiếp qua Test ID:
./ci.sh run-now --only child_chain_a

# Cách 3: Chạy test ngay mà KHÔNG reset lại cụm node (tiết kiệm thời gian khi node đã chạy):
./ci.sh test-child --no-reset
# hoặc:
./ci.sh run-now --only child_chain_a --no-reset

# Cách 4: Ép reset Execution Clusters trước khi test:
./ci.sh run-now --only child_chain_a --reset-exec

# Cách 5: Chạy toàn bộ các bài test cho Child Chain (chain con):
./ci.sh run-now --child-chain
# hoặc cờ ngắn:
./ci.sh run-now --child
```

### 🔹 Chạy MỘT bài test cụ thể khác (`--only <test_id>`)
```bash
# 1. Test 34 kịch bản Block-STM trên Child Chain (chain_a):
./ci.sh run-now --only child_chain_a

# 2. Test đo hiệu năng đỉnh Max TPS trên Child Chain (chain_a - 25,000 txs từ 50k ví spam):
./ci.sh run-now --only tps_blast_child_a

# 3. Test đo hiệu năng đỉnh Max TPS trên Public Chain (Root Chain - 25,000 txs):
./ci.sh run-now --only tps_blast

# 4. Test spam hợp đồng thông minh đa node RPC (Spam Contract):
./ci.sh run-now --only spam_contract

# 5. Test tắt/bật node luân phiên & kiểm chứng Zero-Fork:
./ci.sh run-now --only node_chaos_restart

# 6. Test tạo snapshot & khôi phục node qua Ansible (Zero-Fork):
./ci.sh run-now --only snapshot_recovery
```

---

## ⚡ 2. CÁC TÍNH NĂNG QUẢN LÝ CHAIN CỦA CI

Hệ thống CI cho phép can thiệp trực tiếp trạng thái cụm node trước khi chạy test thông qua các cờ (flag):

### 🔄 Khởi động lại cụm node trước khi test (`--restart-chain` hoặc `--restart`)
Tự động gọi `ansible_deploy.sh --restart` để khởi động lại toàn bộ service của các node Public Chain, chờ RPC sẵn sàng rồi mới test.
```bash
# Khởi động lại Public Chain và chạy bài test recovery:
./ci.sh run-now --only node_chaos_restart --restart-chain

# Hoặc dùng cờ ngắn gọn tương đương:
./ci.sh run-now --only node_chaos_restart --restart

# Khởi động lại chain và chạy toàn bộ pipeline:
./ci.sh run-now --restart-chain
```

### 🧼 Reset toàn bộ Public Chain về Genesis sạch (`--reset-chain` hoặc `--reset`)
Xóa toàn bộ database cũ của Public Chain, nạp genesis mới và đồng bộ lại IP endpoints:
```bash
./ci.sh run-now --only node_chaos_restart --reset-chain

# Hoặc dùng cờ ngắn:
./ci.sh run-now --reset
```

### ⛓️ Quản lý riêng Execution Clusters / Child Chain (`--restart-exec` và `--reset-exec`)
Thao tác chuyên biệt trên các cụm node chain con (Chain ID 991: `chain_a`, `chain_b`):
```bash
# Restart nhanh các tiến trình của Execution Clusters:
./ci.sh run-now --child-chain --restart-exec

# Reset sạch sẽ cụm Execution Clusters về Genesis (nạp sẵn 50,000 ví TPS):
./ci.sh run-now --child-chain --reset-exec

# Reset chain con và chỉ chạy bài test TPS:
./ci.sh run-now --only tps_blast_child_a --reset-exec
```

### 🔍 Xem trước luồng thực thi mà không chạy thật (`--dry-run`)
```bash
./ci.sh run-now --dry-run --child-chain
./ci.sh run-now --dry-run --only tps_blast_child_a
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

| Test ID | Tên bài test | Phân loại | Mô tả ngắn gọn |
| :--- | :--- | :--- | :--- |
| `child_chain_a` | Child Chain Block-STM Tests | **Child Chain** | Chạy 34 kịch bản kiểm tra logic Block-STM trên `chain_a` |
| `tps_blast_child_a` | Child Chain TPS Blast Benchmark | **Child Chain** | Bơm 25,000 txs từ 50,000 ví độc lập đo Max TPS trên `chain_a` |
| `spam_contract` | Spam Contract Multi-RPC Test | **Chung** | Gửi giao dịch spam smart contract dàn trải trên các node RPC |
| `tps_blast` | TPS Blast Benchmark (Public Chain) | **Public Chain** | Bơm 25,000 txs đo Max TPS trên Public Chain |
| `blockstm_logic` | Block-STM Logic Tests | **Public Chain** | Chạy bộ test logic 32+ kịch bản trên chain chính |
| `node_chaos_restart` | Chaos Rolling Restart & Zero-Fork | **Hạ tầng** | Tắt/bật luân phiên từng node, restart cả cụm & verify Zero-Fork |
| `snapshot_recovery` | Snapshot Generation & State Recovery | **Hạ tầng** | Tạo snapshot và khôi phục trạng thái node luân phiên qua Ansible |

---

## 🔔 5. THÔNG BÁO TELEGRAM

Hệ thống CI tự động gửi thông báo về nhóm Telegram:
- 🚀 **Bắt đầu pipeline:** Khi phát hiện commit mới hoặc kích hoạt thủ công.
- ⚡ **Tiến độ từng bài test:** Báo ngay khi xong mỗi bài kèm thời gian thực thi (ví dụ `Bước 1/5: 45s`).
- 🚨 **Báo lỗi tức thì:** Trích xuất log nguyên nhân lỗi nếu có bài test bị FAIL.
- 🏁 **Tổng kết:** Bảng thống kê toàn bộ kết quả khi hoàn thành.

Cấu hình bot token và chat ID tại file [`deploy/ci/ci_config.yaml`](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ci/ci_config.yaml).
