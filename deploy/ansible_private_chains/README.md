# 🌐 Metanode Private Chains — Ansible Deployment Guide

Thư mục này cung cấp hệ thống tự động hóa **Ansible độc lập 100%** dành riêng cho việc triển khai, cấu hình, phân bổ cổng mạng và quản lý vòng đời **Private Chains** trên nhiều máy chủ khác nhau hoặc trên cùng 1 máy chủ phát triển.

Toàn bộ các Private Chains được triển khai chuẩn hóa tại `/opt/metanode/chain-XXX` và chạy dưới quyền user hệ thống `metanode:metanode` (chuẩn kiến trúc production).

---

## 📁 Cấu Trúc Thư Mục

```
deploy/ansible_private_chains/
├── inventory.yml             # Cấu hình danh sách máy chủ, IP, Chain ID, RPC Port và Port Offset
├── inventory.example.yml     # Mẫu cấu hình phân tán nhiều máy
├── deploy.yml                # Ansible Playbook triển khai và cấu hình tự động
├── deploy_private_chains.sh  # Script CLI điều khiển đa năng (Setup, Start, Stop, Reset, Register...)
├── fetch_node_logs.sh        # Script kéo log tập trung từ các node về máy local
├── run_relayer_tmux.sh       # Khởi chạy Cross-Chain Relayer Daemon ngầm qua tmux
├── README.md                 # Tài liệu hướng dẫn chi tiết
└── roles/
    └── private_node/
        └── templates/
            └── metanode-private.service.j2  # File mẫu Systemd Service cho từng chain
```

---

## 🚀 Bảng Lệnh Điều Khiển (`./deploy_private_chains.sh`)

Mặc định mọi lệnh áp dụng cho **tất cả (`all`)** Private Chains trong inventory. Bạn có thể dùng cờ `--chain=ID` (hoặc `-c=ID`) để chỉ định thao tác trên duy nhất 1 chain cụ thể.

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./deploy_private_chains.sh` | Khởi tạo cấu hình, copy binary vào `/opt/metanode/chain-XXX` và bật service toàn bộ chains |
| `./deploy_private_chains.sh --setup --open-ports` | Khởi tạo và tự động mở tường lửa (UFW) cho tất cả port |
| `./deploy_private_chains.sh --setup --chain=101` | **Khởi tạo và cấu hình riêng cho Chain 101** |
| `./deploy_private_chains.sh --start --chain=101` | **Khởi động lại duy nhất Chain 101** |
| `./deploy_private_chains.sh --stop --chain=101` | **Dừng duy nhất Chain 101** |
| `./deploy_private_chains.sh --restart` | Khởi động lại toàn bộ Private Chains |
| `./deploy_private_chains.sh --clean-data --chain=102` | **Xóa sạch database & logs riêng Chain 102** (giữ nguyên keys & config) |
| `./deploy_private_chains.sh --reset-all` | Xóa trắng toàn bộ, sinh genesis & keys mới và chạy lại từ block 0 |
| `./deploy_private_chains.sh --status` | Kiểm tra trạng thái tiến trình và `eth_blockNumber` của các chains |
| `./deploy_private_chains.sh --fetch-logs --chain=101` | **Kéo logs và systemd journal riêng Chain 101 về máy cục bộ** |
| `./deploy_private_chains.sh --register` | Đăng ký toàn bộ danh bạ các Private Chains lên Gateway (Root Anchor) |
| `./deploy_private_chains.sh --register --chain=101` | **Đăng ký CHỈ RIÊNG Chain 101 lên Gateway (Root Anchor)** |
| `./run_relayer_tmux.sh` | Khởi chạy Cross-Chain Relayer Daemon ngầm bằng tmux |

---

## 🛡️ Quy Tắc Phân Bổ Cổng Mạng (Tránh Trùng Port Khi Chạy Cùng 1 Máy)

Khi chạy nhiều Private Chain (hoặc nhiều Node trong 1 Chain) trên cùng một máy chủ vật lý, hệ thống sử dụng 2 tham số trong `inventory.yml`:
1. `rpc_port`: Cổng JSON-RPC của Chain.
2. `port_offset`: Độ lệch cổng cho các dịch vụ nội bộ (P2P Consensus, Primary TCP, Metrics, DNS, Peer RPC).

### 📊 Bảng Phân Bổ Mẫu (Chain 101 vs Chain 102 trên cùng 1 Server):

| Dịch vụ | Công thức tính Port | Chain 101 (Offset 10) | Chain 102 (Offset 20) | Trạng thái |
| :--- | :--- | :--- | :--- | :--- |
| **RPC Endpoint** | `rpc_port + node_id` | `8546`, `8547`, `8548` | `8550`, `8551`, `8552` | ✅ Độc lập |
| **Primary TCP (Sync)**| `4200 + port_offset + node_id` | `4210`, `4211`, `4212` | `4220`, `4221`, `4222` | ✅ Độc lập |
| **P2P Consensus** | `10200 + port_offset + node_id` | `10210`, `10211`, `10212`| `10220`, `10221`, `10222`| ✅ Độc lập |
| **Meta RPC** | `11100 + port_offset + node_id` | `11110`, `11111`, `11112`| `11120`, `11121`, `11122`| ✅ Độc lập |
| **Metrics Exporter**| `12100 + port_offset + node_id` | `12110`, `12111`, `12112`| `12120`, `12121`, `12122`| ✅ Độc lập |
| **DNS Service** | `13000 + port_offset + node_id` | `13010`, `13011`, `13012`| `13020`, `13021`, `13022`| ✅ Độc lập |
| **Peer RPC (Rust)** | `20200 + port_offset + node_id` | `20210`, `20211`, `20212`| `20220`, `20221`, `20222`| ✅ Độc lập |

> **Khuyến nghị:** Bước nhảy `port_offset` nên cách nhau tối thiểu **10 đơn vị** cho mỗi chain (ví dụ: `10`, `20`, `30`, `40`...) để đảm bảo các node con không bao giờ bị xung đột cổng mạng.

---

## ➕ Hướng Dẫn Thêm 1 Private Chain Mới Vào Hệ Thống

Để thêm 1 chain mới (ví dụ: Chain ID `105`), thực hiện theo 3 bước:

### Bước 1: Khai báo vào `inventory.yml`
Mở `deploy/ansible_private_chains/inventory.yml` và thêm:
```yaml
        # Private Chain 5 (Chain ID 105)
        private_chain_105:
          ansible_host: 192.168.1.233          # IP máy chủ
          ansible_connection: local            # Thêm nếu là máy local (bỏ nếu là remote SSH)
          chain_id: 105                        # Chain ID duy nhất
          rpc_port: 8554                       # Port RPC không trùng lặp
          port_offset: 50                      # Offset port tiếp theo (50)
          install_dir: "/opt/metanode/chain-105"
```

### Bước 2: Triển khai và khởi chạy riêng Chain mới
```bash
./deploy_private_chains.sh --setup --chain=105 --open-ports
```

### Bước 3: Đăng ký Chain mới lên Gateway Root Anchor
```bash
./deploy_private_chains.sh --register --chain=105
```

---

## 🔍 Kiểm Tra Trạng Thái & Xem Logs

### 1. Kiểm tra trạng thái Block Number:
```bash
./deploy_private_chains.sh --status
```

### 2. Xem logs trực tiếp qua Systemd:
```bash
# Xem log realtime của Chain 101:
journalctl -u metanode-private-101.service -f

# Quản lý service trực tiếp qua systemctl:
sudo systemctl status metanode-private-101.service
sudo systemctl restart metanode-private-101.service
sudo systemctl stop metanode-private-101.service
```

### 3. Kéo toàn bộ log từ các server về máy cục bộ:
```bash
# Kéo log tất cả chains về thư mục logs/run_YYYYMMDD_HHMMSS/:
./deploy_private_chains.sh --fetch-logs

# Hoặc chỉ kéo riêng Chain 101 với 1000 dòng journal:
./fetch_node_logs.sh --chain=101 --lines=1000
```
