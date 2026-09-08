# 🚀 Metanode CI/CD Test Automation & Telegram Notification System

Hệ thống tự động hóa kiểm thử liên tục (CI/CD) cho Metanode Core và Metanode Suite:
- **Lắng nghe commit mới**: Theo dõi branch `main` (hoặc cấu hình tùy ý) từ remote Git.
- **Tự động kéo mã nguồn & kiểm tra build**: Kiểm tra `build_check.sh` (Go/Rust/FFI).
- **Môi trường linh hoạt cho từng bài test (`pre_action`)**: Tự động reset chain sạch (`reset_chain`) trước khi chạy bài test TPS để đạt throughput tối đa, hoặc restart nhanh (`restart_chain`).
- **Cấu hình bài test dễ dàng qua YAML (`ci_config.yaml`)**: Cho phép thêm comment, bật/tắt bài test, cấu hình timeout và thư mục chạy.
- **Thông báo thời gian thực qua Telegram**: Gửi thông báo bắt đầu kèm thông tin commit, thông báo tổng kết khi thành công, và trích xuất log lỗi trực tiếp khi bài test thất bại.

---

## ⚡ 1. Hướng Dẫn Vận Hành Nhanh (Quick Start)

Bạn có thể chạy trực tiếp lệnh `./ci.sh` từ thư mục gốc của repository `metanode`:

| Nhu Cầu | Lệnh Thực Thi Tại Root (`metanode/`) | Lệnh Trong `deploy/ci/` |
| :--- | :--- | :--- |
| **Khởi động Watcher Daemon ngầm** | `./ci.sh start` | `./ci_watcher.sh start` |
| **Xem trạng thái & commit gần nhất** | `./ci.sh status` | `./ci_watcher.sh status` |
| **Xem log realtime của Watcher** | `./ci.sh logs` | `./ci_watcher.sh logs` |
| **Dừng Watcher Daemon** | `./ci.sh stop` | `./ci_watcher.sh stop` |
| **Chạy test thủ công ngay lập tức** | `./ci.sh run-now` | `./ci_watcher.sh run-now` |
| **Chạy thử nghiệm (Dry-Run)** | `./ci.sh run-now --dry-run` | `./ci_watcher.sh run-now --dry-run` |
| **Chạy duy nhất 1 bài test** | `./ci.sh run-now --only tps_blast` | `./ci_watcher.sh run-now --only tps_blast` |

---

## ⚙️ 2. Cách Thêm Hoặc Bật/Tắt Bài Test Trong `ci_config.yaml`

Mở file [`ci_config.yaml`](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ci/ci_config.yaml). Trong mục `tests:`, mỗi bài test được định nghĩa rõ ràng.

### Ví dụ thêm 1 bài test mới:
```yaml
  # ----------------------------------------------------------------------------
  # 5. BÀI TEST GIAO DỊCH PARALLEL CONFLICT (VÍ DỤ BỔ SUNG)
  # ----------------------------------------------------------------------------
  - id: "parallel_conflict_test"
    name: "Block-STM Parallel Conflict Test"
    enabled: true                  # true: bật chạy, false: bỏ qua
    pre_action: "none"             # "reset_chain" (reset data), "restart_chain" (restart service), "none"
    # Đường dẫn tương đối từ metanode-suite (hoàn toàn portable giữa các máy)
    cwd: "test-simple/test-rpc/test-chain/30-eip7702-parallel-contention"
    command: "go run main.go"
    timeout_seconds: 300           # Giới hạn thời gian chạy tối đa tránh treo CI
    continue_on_failure: false     # Dừng pipeline ngay nếu bài test này thất bại
```

### 💡 Các cơ chế `pre_action` hỗ trợ:
- **`prepare_tps`** (Dành riêng cho test TPS):
  1. Kiểm tra file `generated_keys.json`. Nếu chưa tồn tại hoặc số lượng < 50,000 ví, tự động kích hoạt `main.go` trong `gen_spam_keys` để sinh mới đúng số lượng.
  2. Nạp toàn bộ danh sách ví TPS và BLS keys vào `genesis.json.example` qua `manage_genesis.py` (tự động lọc chống trùng lặp địa chỉ 100%).
  3. Xóa `genesis.json` cũ để buộc Ansible tạo cấu hình genesis mới.
  4. Chạy `./ansible_deploy.sh --reset-all --open-ports` để reset cụm node với genesis mới.
  5. Cập nhật IP/RPC endpoints vào `metanode-suite`.
- **`reset_chain` / `reset_public`** (Dành cho test Block-STM logic): Tự động reset cụm Public Chain và cập nhật IP.
- **`deploy_private`** (Dành cho test Cross-Chain): Tự động deploy 4 chuỗi Private Chains (101-104) + khởi động lại Relayer daemon trong Tmux + cập nhật IP.
- **`restart_chain`**: Chỉ restart service systemd mà không xóa dữ liệu.
- **`none`**: Không can thiệp vào node, chạy trực tiếp test trên cụm hiện tại.

---

## 📱 3. Cấu Hình Telegram Bot

Trong file `ci_config.yaml`:
```yaml
telegram:
  enabled: true
  bot_token: "8230176859:AAGoZ_78xzb1q4rgJJ5SYLxRhZBYBTSz_xo"
  chat_id: "-1003867050625"
  notify_on_start: true     # Báo khi phát hiện commit mới
  notify_on_finish: true    # Báo tổng kết thành công
  notify_on_test_fail: true # Báo ngay khi có lỗi kèm 25 dòng tail logs
```

### Kiểm tra kết nối Telegram:
```bash
python3 deploy/ci/telegram_notify.py --test
```

---

## 📂 4. Cấu Trúc Thư Mục Hệ Thống

```
deploy/ci/
├── ci_config.yaml         # Cấu hình chính (Git, Telegram, Chain Actions, Test Matrix)
├── ci_watcher.sh          # Daemon giám sát remote git và quản lý tiến trình
├── ci_runner.py           # Bộ điều phối chạy test, đo đạc thời gian, timeout và xử lý log
├── telegram_notify.py     # Module định dạng HTML và gửi thông báo Telegram
├── ci_watcher.log         # File log hoạt động của Watcher Daemon
├── .last_tested_commit    # Lưu trữ commit hash đã test gần nhất
└── logs/                  # Thư mục lưu trữ log chi tiết của từng bài test
    ├── latest_<test_id>.log
    └── run_20260907_103000/
        ├── blockstm_logic.log
        ├── cross_chain_core.log
        ├── spam_contract.log
        └── tps_blast.log
```

---

## 🛡️ 5. Cài Đặt Systemd Service Tự Động (Tùy Chọn)

Nếu bạn muốn CI Watcher luôn tự động chạy ngầm kể cả khi server reboot:
1. Tạo file `/etc/systemd/system/metanode-ci.service`:
```ini
[Unit]
Description=Metanode CI/CD Test Watcher Daemon
After=network.target

[Service]
Type=simple
User=abc
WorkingDirectory=/home/abc/nhat/con-chain-v2/metanode/deploy/ci
ExecStart=/home/abc/nhat/con-chain-v2/metanode/deploy/ci/ci_watcher.sh __internal_loop
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```
2. Kích hoạt service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now metanode-ci.service
```
