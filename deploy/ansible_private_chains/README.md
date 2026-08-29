# 🌐 Metanode Private Chains — Ansible Deployment Guide

> 📘 **Tài liệu Hướng dẫn Vận hành Toàn diện:** Xem file [OPERATIONS_GUIDE.md](../../OPERATIONS_GUIDE.md) để nắm toàn bộ quy trình 4 bước chuẩn kết nối Root Anchor, Private Chains, Relayer Daemon và bộ kiểm thử E2E.

Thư mục này cung cấp hệ thống tự động hóa **Ansible độc lập 100%** dành riêng cho việc triển khai, cấu hình và quản lý **Private Chains** trên nhiều máy chủ khác nhau (hoặc trên cùng 1 máy chủ phát triển).

Toàn bộ các Private Chains được triển khai chuẩn hóa tại `/opt/metanode/chain-XXX` và chạy dưới quyền user hệ thống `metanode:metanode` (giống y hệt kiến trúc triển khai của Public Chain trong `deploy/ansible`).

---

## 📁 Cấu Trúc Thư Mục

```
deploy/ansible_private_chains/
├── inventory.yml             # Cấu hình danh sách các máy chủ và Private Chains
├── inventory.example.yml     # Mẫu cấu hình phân tán nhiều máy
├── deploy.yml                # Ansible Playbook triển khai và cấu hình
├── deploy_private_chains.sh  # Script CLI điều khiển đa năng
├── README.md                 # Tài liệu hướng dẫn chi tiết
└── roles/
    └── private_node/
        └── templates/
            └── metanode-private.service.j2  # File mẫu Systemd Service
```

---

## 🚀 Các Tùy Chọn & Lệnh Vận Hành (`./deploy_private_chains.sh`)

### 🎯 1. Nhắm Mục Tiêu Từng Chain (Single Chain Targeting):
Mặc định mọi lệnh áp dụng cho **tất cả (`all`)** Private Chains trong inventory. Bạn có thể dùng cờ `--chain=ID` (hoặc `-c=ID`) để chỉ áp dụng cho 1 chain cụ thể:

* `--chain=101`: Chỉ thao tác trên Chain 101.
* `--chain=102`: Chỉ thao tác trên Chain 102.

---

### ⚡ 2. Bảng Lệnh Đầy Đủ:

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./deploy_private_chains.sh` | Khởi tạo, copy binary vào `/opt/metanode/chain-XXX` và bật service |
| `./deploy_private_chains.sh --setup --open-ports` | Khởi tạo và tự động mở tường lửa (UFW) cho tất cả port |
| `./deploy_private_chains.sh --stop` | Dừng toàn bộ các Private Chains |
| `./deploy_private_chains.sh --stop --chain=101` | **Dừng duy nhất 1 chain (Chain 101)** |
| `./deploy_private_chains.sh --start --chain=101` | **Khởi động lại duy nhất 1 chain (Chain 101)** |
| `./deploy_private_chains.sh --restart` | Khởi động lại tất cả Private Chains |
| `./deploy_private_chains.sh --clean-data` | **Xóa sạch database & logs của tất cả chains** (giữ nguyên keys & config) |
| `./deploy_private_chains.sh --clean-data --chain=102` | **Xóa sạch database & logs riêng cho Chain 102** |
| `./deploy_private_chains.sh --reset-all` | Xóa trắng toàn bộ, sinh genesis & keys mới và chạy lại từ block 0 |
| `./deploy_private_chains.sh --status` | Kiểm tra trạng thái và `eth_blockNumber` của tất cả chains |
| `./deploy_private_chains.sh --fetch-logs` | **Kéo toàn bộ logs và systemd journal từ các server về máy cục bộ** |
| `./deploy_private_chains.sh --fetch-logs --chain=101` | **Kéo logs và systemd journal riêng Chain 101 về máy cục bộ** |
| `./deploy_private_chains.sh --register` | Đăng ký toàn bộ danh bạ các Private Chains lên Gateway (Root Anchor) |
| `./run_relayer_tmux.sh` | Khởi chạy Cross-Chain Relayer Daemon ngầm bằng tmux |

---

### 📥 3. Kéo Logs Về Máy Cục Bộ (`fetch_node_logs.sh`)
Lệnh này tự động tải toàn bộ file log trong `/opt/metanode/chain-XXX/logs/` và trích xuất `journalctl` của service từ các máy remote về lưu tập trung tại thư mục `deploy/ansible_private_chains/logs/run_YYYYMMDD_HHMMSS/`:

```bash
# Kéo logs toàn bộ 4 Private Chains:
./deploy_private_chains.sh --fetch-logs

# Hoặc kéo logs riêng Chain 102 với 1000 dòng journal:
./fetch_node_logs.sh --chain=102 --lines=1000
```

---

### 🛡️ 4. Tường Lửa & Các Cổng Được Mở (`--open-ports`)
Khi chạy với cờ `--open-ports`, script sẽ tự động tạo rule `ufw allow` cho:
* **RPC Port:** `8546`, `8547`, `8548`, `8549`...
* **Peer RPC Port:** `20210`, `20220`, `20230`, `20240`...
* **Consensus Network Port:** `10210`, `10220`, `10230`, `10240`...
* **Metrics & DNS Ports:** `12110`, `13010`...

---

### 🔍 5. Xem Log & Quản Lý Systemd Trực Tiếp:
```bash
# Xem log realtime của Chain 101:
journalctl -u metanode-private-101.service -f

# Quản lý service trực tiếp qua systemctl:
sudo systemctl status metanode-private-101.service
sudo systemctl restart metanode-private-101.service
sudo systemctl stop metanode-private-101.service
```

