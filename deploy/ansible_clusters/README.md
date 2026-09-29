# 🚀 MetaNode Multi-Cluster Ansible Deployment & Automated Testing

Hệ thống Ansible tự động hóa triển khai, quản lý vòng đời và kiểm thử tích hợp cho kiến trúc **Parent Chain + Sharded Execution Clusters (Rollup Architecture)** của MetaNode, đi kèm tích hợp thông báo trạng thái & cảnh báo thời gian thực qua **Telegram Bot**.

---

## 📐 Kiến Trúc Triển Khai

| Thành Phần | Định Danh & Cổng | Vai Trò & Cơ Chế |
| :--- | :--- | :--- |
| **Parent Chain** | HTTP RPC: `:8547`<br>P2P: `:4000` | Native State Store (Float Accounts, Account Registry, Cluster Registry, Claimed Messages). Quản lý cọc float và điều phối bảo lãnh chuyển tiền. |
| **Exec Cluster 1** | ClusterID: `1`<br>EVM ChainID: `991`<br>RPC: `:8646`<br>P2P: `:4200` | Cụm thực thi Shard 1. Chạy Rollup workers (`SendWorker`, `ReceiveWorker`, `ReclaimWorker`). Tiếp nhận giao dịch EVM chuẩn. |
| **Exec Cluster 2** | ClusterID: `2`<br>EVM ChainID: `991`<br>RPC: `:8647`<br>P2P: `:4202` | Cụm thực thi Shard 2. Tương tác giao dịch xuyên cụm (cross-cluster transfer) với Cluster 1 qua Float Account trên Parent Chain. |

---

## 📁 Cấu Trúc Thư Mục

```
deploy/ansible_clusters/
├── ansible.cfg                # Cấu hình Ansible tối ưu (pipelining, timeouts, callbacks)
├── inventory.example.yml      # Mẫu file inventory khai báo hosts và cluster
├── inventory.yml              # File inventory hiện hành
├── .env.example               # Mẫu cấu hình Telegram Bot Token & Chat ID
├── .env                       # File cấu hình Telegram bí mật
├── deploy.yml                 # Ansible Playbook chính (Build, Parent Chain, Exec Clusters, Test)
├── deploy_clusters.sh         # Script điều phối 1-click tích hợp thông báo Telegram
├── group_vars/
│   └── all.yml                # Biến toàn cục (ChainID 991, paths, RPC URLs)
├── roles/
│   ├── build/                 # Biên dịch binaries (parent_chain, simple_chain)
│   ├── common/                # Tạo thư mục /opt/metanode, phân phối binaries
│   ├── parent_chain/          # Tạo config, template systemd/daemon, khởi chạy & health check :8547
│   ├── exec_cluster/          # Sinh genesis, cấu hình cluster_id, khởi chạy & health check RPC
│   ├── testing/               # Chạy bộ test tích hợp 5 kịch bản thực tế
│   └── telegram/              # Gửi thông báo Telegram theo sự kiện playbook
├── scripts/
│   └── telegram_notify.py     # Module Python gửi tin nhắn HTML đẹp qua Telegram API (zero-dep)
└── README.md                  # Hướng dẫn chi tiết này
```

---

## ⚡ Hướng Dẫn Sử Dụng Nhanh (1-Click)

### 1. Cấu hình Telegram (Tùy chọn)
Chỉnh sửa file `.env`:
```bash
TELEGRAM_BOT_TOKEN="your_bot_token_here"
TELEGRAM_CHAT_ID="your_chat_id_here"
```
Kiểm tra kết nối Bot:
```bash
python3 scripts/telegram_notify.py --test
```

### 2. Triển khai toàn bộ cụm node và chạy test tự động
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --setup --test
```
Lệnh trên sẽ:
1. Gửi thông báo 🚀 **Deploy Bắt đầu** lên Telegram (kèm commit hash, author, nhánh git).
2. Tự động kiểm tra và build các binary Go (`parent_chain`, `simple_chain`).
3. Khởi chạy **Parent Chain** trên cổng `:8547` và kiểm tra HTTP RPC sẵn sàng.
4. Khởi chạy **Exec Cluster 1** (`:8646`) và **Exec Cluster 2** (`:8647`) với `cluster_id: 1, 2` và `chainId: 991`.
5. Gửi thông báo ✅ **Dịch Vụ Sẵn Sàng** lên Telegram (kèm block heights và ports).
6. Tự động thực thi **Bộ kiểm thử tích hợp 5 kịch bản thực tế**:
   - *Kịch bản 1:* Đăng ký tài khoản mới & ánh xạ vào Account Registry trên Parent Chain.
   - *Kịch bản 2:* Nạp tiền Float & Rollup ReceiveWorker ghi có số dư.
   - *Kịch bản 3:* Tương tác gọi Smart Contract nội bộ trên node thực thi.
   - *Kịch bản 4:* Giao dịch chuyển tiền xuyên 2 cụm node (Exec 1 -> Exec 2).
   - *Kịch bản 5:* Khả năng tự vận hành độc lập khi Parent Chain offline.
7. Gửi báo cáo chi tiết 🎉 **Kết Quả Kiểm Thử** lên Telegram.

---

## 🛠️ Danh Sách Các Lệnh Vận Hành

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./deploy_clusters.sh --status` | Xem trạng thái RPC và Block Height hiện tại của tất cả cụm node |
| `./deploy_clusters.sh --test-only` | Chỉ chạy bộ kiểm thử kịch bản thực tế (không deploy lại) |
| `./deploy_clusters.sh --deploy` | Cập nhật mã nguồn/binary mới và restart các cụm |
| `./deploy_clusters.sh --restart` | Khởi động lại toàn bộ Parent Chain và các Execution Clusters |
| `./deploy_clusters.sh --stop` | Dừng an toàn toàn bộ tiến trình cụm node |
| `./deploy_clusters.sh --clean` | Xóa database và logs, giữ lại cấu hình và keys |
| `./deploy_clusters.sh --reset` | Reset toàn bộ hệ sinh thái về Block 0 và khởi chạy lại |
| `./deploy_clusters.sh --systemd` | Sử dụng `systemd` service thay vì background daemon |

---

## 🌐 Triển Khai Multi-Server (Production)

Mặc định `inventory.yml` chạy trên localhost (`127.0.0.1`). Để deploy lên nhiều server vật lý hoặc VPS:
1. Mở `inventory.yml` và thay đổi `ansible_host` thành IP của server tương ứng.
2. Đổi `ansible_connection: ssh`.
3. Bổ sung SSH key hoặc password:
```yaml
parent_chain_nodes:
  hosts:
    parent_node:
      ansible_host: 192.168.1.100
      ansible_user: metanode

exec_clusters:
  hosts:
    exec_cluster_1:
      ansible_host: 192.168.1.101
      ansible_user: metanode
      cluster_id: 1
    exec_cluster_2:
      ansible_host: 192.168.1.102
      ansible_user: metanode
      cluster_id: 2
```
4. Chạy với cờ `--systemd`:
```bash
./deploy_clusters.sh --setup --systemd --test
```
Mỗi server sẽ tự động tạo systemd service riêng (`metanode-parentchain.service`, `metanode-cluster-1.service`, `metanode-cluster-2.service`) với cấu hình tự khởi động lại (`Restart=always`) và giới hạn file descriptors cao (`LimitNOFILE=65536`).
