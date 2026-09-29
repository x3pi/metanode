# 📖 HƯỚNG DẪN TRIỂN KHAI VÀ KIỂM THỬ HỆ THỐNG CỤM METANODE
### Architecture: Parent Chain (Coordinator & Float Ledger) + Sharded Execution Clusters (Rollup)

Tài liệu này cung cấp toàn bộ quy trình từ A-Z để thiết lập, cấu hình, triển khai tự động qua Ansible, vận hành môi trường Production/Devnet và thực thi bộ kiểm thử tích hợp (End-to-End Resilience & Cross-Cluster Scenarios) đi kèm hệ thống thông báo trạng thái tức thời qua **Telegram Bot**.

---

## 📑 MỤC LỤC
1. [Kiến Trúc & Nguyên Lý Hoạt Động](#1-kiến-trúc--nguyên-lý-hoạt-động)
2. [Yêu Cầu Hệ Thống & Cài Đặt Tiên Quyết](#2-yêu-cầu-hệ-thống--cài-đặt-tiên-quyết)
3. [Cấu Trúc Thư Mục & Vai Trò](#3-cấu-trúc-thư-mục--vai-trò)
4. [Cấu Hình Môi Trường](#4-cấu-hình-môi-trường)
   - [Cấu hình Telegram Bot Alerts](#41-cấu-hình-telegram-bot-alerts)
   - [Cấu hình Inventory (Local Devnet vs Multi-Server Production)](#42-cấu-hình-inventory)
   - [Cấu hình Biến Toàn Cục (ChainID & Ports)](#43-cấu-hình-biến-toàn-cục)
5. [Quy Trình Triển Khai](#5-quy-trình-triển-khai)
   - [Cách 1: Triển khai 1-Click tự động](#51-cách-1-triển-khai-1-click-tự-động-khuyến-nghị)
   - [Cách 2: Triển khai qua Ansible Playbook](#52-cách-2-triển-khai-qua-ansible-playbook-trực-tiếp)
   - [Chế độ Systemd Service (Production) vs Daemon (Devnet)](#53-chế-độ-systemd-service-vs-daemon)
6. [Quy Trình Kiểm Thử Tích Hợp (Testing Guide)](#6-quy-trình-kiểm-thử-tích-hợp)
   - [Giải thích chi tiết 5 Kịch bản kiểm thử](#61-giải-thích-chi-tiết-5-kịch-bản-kiểm-thử)
   - [Các lệnh chạy test](#62-các-lệnh-chạy-test)
   - [Kiểm tra báo cáo Telegram & Logs](#63-kiểm-tra-báo-cáo-telegram--logs)
7. [Cơ Chế Phục Hồi Khi Parent Chain Offline](#7-cơ-chế-phục-hồi-khi-parent-chain-offline)
8. [Vận Hành & Giám Sát Thường Nhật](#8-vận-hành--giám-sát-thường-nhật)
9. [Xử Lý Sự Cố Thường Gặp (Troubleshooting)](#9-xử-lý-sự-cố-thường-gặp-troubleshooting)

---

## 1. KIẾN TRÚC & NGUYÊN LÝ HOẠT ĐỘNG

Kiến trúc MetaNode tuân thủ mô hình **Đồng Thuận Hai Tầng (Two-Tier Consensus Architecture)** phân định ranh giới nghiêm ngặt giữa **Lớp Điều Phối / Bảo Lãnh Thanh Khoản (Parent Chain)** và **Các Cụm Thực Thi Phân Đoạn (Sharded Execution Clusters)**.

```
                           ┌──────────────────────────────────────────────┐
                           │          PARENT CHAIN (COORDINATOR)          │
                           │   - 100% Rust BFT Engine (Quorum 2f+1)       │
                           │   - Zero-Fork Invariant (Thà pending ko fork)│
                           │   - LevelDB Native Float Store (:8547)       │
                           │   - Account & Cluster Registry               │
                           └──────────────▲────────────────▲──────────────┘
                                          │                │
            Chuyển tiền xuyên cụm:        │                │ Nhận tiền & Xác nhận:
            - Float Deposit / Send        │                │ - Poll Inbound Transfers
            - Ký chứng thực BLS           │                │ - SendMarkClaimed
                                          │                │
             ┌────────────────────────────┴───┐        ┌───┴────────────────────────────┐
             │       EXEC CLUSTER 1           │        │       EXEC CLUSTER 2           │
             │   - ClusterID: 1               │        │   - ClusterID: 2               │
             │   - EVM ChainID: 991           │        │   - EVM ChainID: 991           │
             │   - RPC: http://...:8646       │        │   - RPC: http://...:8647       │
             │   - Consensus: HashiCorp Raft  │        │   - Consensus: HashiCorp Raft  │
             │     (3 Replicas HA Cluster)    │        │     (Single-Node Feed)         │
             │   - Auto-Failover: ~200ms      │        │   - Workers:                   │
             │   - Replicas n0, n1, n2        │        │     • SendWorker               │
             │   - Workers:                   │        │     • ReceiveWorker            │
             │     • SendWorker               │        │     • ReclaimWorker            │
             │     • ReceiveWorker            │        │                                │
             │     • ReclaimWorker            │        │                                │
             └────────────────────────────────┘        └────────────────────────────────┘
```

### Các Thành Phần Trọng Yếu:
1. **Parent Chain (Cổng RPC `:8547`, P2P `:4000`):**
   - **Động cơ đồng thuận:** **100% Rust BFT Engine** (`consensus/metanode`). Sử dụng HotStuff BFT, Quorum 2f+1, DAG digest votes và chữ ký BLS tổng hợp.
   - **Zero-Fork Invariant (Tối Thượng):** Thà pending chờ Quorum confirmation chứ tuyệt đối không bao giờ fork. Không dùng timeout/sleep để tự ý dispatch commit.
   - Đóng vai trò là nguồn sự thật (Single Source of Truth) về danh bạ cụm (`Cluster Registry`), danh bạ tài khoản (`Account Registry`) và sổ cái ký quỹ thanh khoản (`Native Float Accounts`).
2. **Execution Clusters (`Cluster 1: :8646`, `Cluster 2: :8647`):**
   - **Động cơ đồng thuận:** **HashiCorp Raft v1.7.1** (`consensus_mode = "raft"`) tích hợp trực tiếp trong `simple_chain` thuần Go.
   - **Khả năng chịu lỗi (CFT / High Availability):** Chạy nhiều replica (ví dụ 3 replica `n0`, `n1`, `n2`), tự động phát hiện mất kết nối và **bầu Leader mới trong ~200ms**.
   - **Shared Key Pattern:** Tất cả replica trong cùng 1 cụm thực thi dùng chung cùng 1 cặp khóa Sequencer (`address` & `private_key`), bảo đảm tính xác định 100% của block hash bất kể replica nào đang giữ quyền Leader.
   - Chạy engine máy ảo MVM (Meta Virtual Machine) tương thích hoàn toàn EVM Cancun (EVM ChainID `991`).
3. **Bộ Ba Rollup Workers trên mỗi Cụm:**
   - **`SendWorker`**: Quét các giao dịch xuyên cụm nội bộ (`StateLocalAppliedPendingSend`), ký chứng thực BLS và đẩy lên Parent Chain qua `SendInboundTransfer`.
   - **`ReceiveWorker`**: Thăm dò định kỳ (poll) Parent Chain từ con trỏ `cursor`, tiếp nhận các giao dịch gửi đến cụm mình, đề xuất block nội bộ để ghi có (`credit`) tiền cho người nhận, sau đó gọi `SendMarkClaimed` về Parent Chain.
   - **`ReclaimWorker`**: Đảm bảo an toàn tài sản; nếu giao dịch quá hạn (`TimeoutBlocks`) mà chưa được nhận, tự động gửi `SendReclaimFloat` để thu hồi tiền cọc và hoàn trả cho người gửi ban đầu.

---

## 2. YÊU CẦU HỆ THỐNG & CÀI ĐẶT TIÊN QUYẾT

### Yêu cầu phần cứng khuyến nghị:
- **CPU:** Tối thiểu 4 Cores (Khuyến nghị 8+ Cores).
- **RAM:** Tối thiểu 8 GB (Khuyến nghị 16 GB).
- **Ổ cứng:** NVMe SSD tối thiểu 50 GB dung lượng trống (tối ưu hóa cho cơ chế I/O của LevelDB/NOMT).
- **Hệ điều hành:** Linux (Ubuntu 22.04 / 24.04 LTS, Debian 12, RHEL/Rocky Linux 9).

### Yêu cầu phần mềm & Dependencies:
Cần cài đặt sẵn trên máy điều khiển (Ansible Controller) hoặc các máy mục tiêu:
```bash
# 1. Cài đặt các công cụ cơ bản
sudo apt-get update && sudo apt-get install -y \
    build-essential cmake clang pkg-config libssl-dev git curl python3 python3-pip jq

# 2. Cài đặt Ansible
pip3 install ansible

# 3. Yêu cầu Go (>= 1.22) & Rust (>= 1.78)
go version
rustc --version
```

### Danh mục các cổng mạng (Network Ports):
Đảm bảo firewall cho phép thông các cổng sau:
| Cổng | Giao thức | Dịch vụ | Phạm vi truy cập |
| :--- | :--- | :--- | :--- |
| `8547` | TCP (HTTP) | Parent Chain JSON-RPC | Nội bộ cụm / Node quản trị |
| `4000` | TCP/UDP | Parent Chain P2P Consensus (Rust BFT) | Giữa các node Parent Chain |
| `8646`, `8648`, `8649` | TCP (HTTP) | Exec Cluster 1 EVM RPC (Replicas 1, 2, 3) | DApps, Wallets, Tests |
| `7110`, `7111`, `7112` | TCP | Exec Cluster 1 Raft Transport | Nội bộ giữa các Replicas Raft |
| `7210`, `7211`, `7212` | TCP (HTTP) | Exec Cluster 1 Forward/Admin HMAC | Forwarding tx Follower -> Leader |
| `8647` | TCP (HTTP) | Exec Cluster 2 EVM RPC | DApps, Wallets, Tests |
| `7120` / `7220` | TCP | Exec Cluster 2 Raft Transport & Forward | Nội bộ Cluster 2 |

---

## 3. CẤU TRÚC THƯ MỤC & VAI TRÒ

Toàn bộ giải pháp triển khai nằm trong thư mục `deploy/ansible_clusters/`:

```
deploy/ansible_clusters/
├── ansible.cfg                # Cấu hình Ansible: kích hoạt pipelining, callback YAML, timeout
├── inventory.yml              # Khai báo máy chủ, địa chỉ IP, roles và cluster_id
├── inventory.example.yml      # Mẫu inventory tham khảo
├── .env                       # Cấu hình Token Bot và Chat ID Telegram (bảo mật)
├── .env.example               # Mẫu cấu hình biến môi trường Telegram
├── deploy.yml                 # Playbook tổng hợp: Build, Setup, Parent Chain, Exec Clusters, Test
├── deploy_clusters.sh         # Script bash 1-click điều phối thông minh, tích hợp Telegram alerts
├── group_vars/
│   └── all.yml                # Biến dùng chung (ChainID 991, paths, URLs mặc định)
├── roles/
│   ├── build/                 # Kiểm tra và biên dịch tự động binaries (parent_chain, simple_chain)
│   ├── common/                # Tạo thư mục /opt/metanode, phân phối nhị phân và chuẩn bị runtime
│   ├── parent_chain/          # Tạo config, systemd service, khởi chạy & healthcheck cổng 8547
│   ├── exec_cluster/          # Sinh genesis động, cấu hình cluster_id & ports, khởi chạy cụm
│   ├── testing/               # Thực thi bộ kiểm thử tích hợp 5 kịch bản thực tế
│   └── telegram/              # Bắn thông báo Telegram sự kiện qua playbook
└── scripts/
    └── telegram_notify.py     # Module Python thuần gửi thông báo HTML đẹp mắt (zero-dependency)
```

---

## 4. CẤU HÌNH MÔI TRƯỜNG

### 4.1 Cấu hình Telegram Bot Alerts
1. Tạo file cấu hình bảo mật từ file mẫu:
   ```bash
   cd deploy/ansible_clusters
   cp .env.example .env
   ```
2. Mở file `.env` và điền thông tin Bot Telegram của bạn:
   ```env
   TELEGRAM_BOT_TOKEN="1234567890:ABCdefGHIjklMNOpqrsTUVwxyz"
   TELEGRAM_CHAT_ID="-1001234567890"
   ```
3. Kiểm tra kết nối Telegram:
   ```bash
   python3 scripts/telegram_notify.py --test
   ```
   *Nếu cấu hình đúng, bạn sẽ nhận được một tin nhắn kiểm tra tức thì trong nhóm Telegram.*

### 4.2 Cấu hình Inventory

#### Chế độ A: Triển khai Local Devnet (Chuẩn Raft 3 Replicas HA + Single Feed)
File `inventory.yml` mô hình hóa chuẩn: Cluster 1 chạy cụm Raft 3 node chịu lỗi, Cluster 2 chạy Single Feed:
```yaml
all:
  vars:
    ansible_user: "metanode"
    evm_chain_id: 991
    install_dir: "/opt/metanode"
    raft_secret_file: "/opt/metanode/raft_secret.key"

  children:
    # ── Parent Chain Node (100% Rust BFT Consensus Engine) ──────────────
    parent_chain_nodes:
      hosts:
        parent_node:
          ansible_host: 127.0.0.1
          ansible_connection: local
          parent_http_port: 8547
          node_data_dir: "/opt/metanode/parent_chain"
          rust_config_path: "consensus/metanode/config/node_devnet_parent.toml"

    # ── Execution Clusters (Rollup Shards with Raft HA Consensus) ────────
    exec_clusters:
      hosts:
        # Cluster 1: 3-Replica High Availability Raft Cluster
        exec1_replica1:
          ansible_host: 127.0.0.1
          ansible_connection: local
          cluster_id: 1
          cluster_name: "exec1"
          consensus_mode: "raft"
          node_id: "exec1_r1"
          rpc_port: 8646
          raft_port: 7110
          forward_port: 7210
          raft_bootstrap: true
          node_data_dir: "/opt/metanode/exec1_r1"
          address: "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
          raft_peers:
            - { id: "exec1_r1", address: "127.0.0.1:7110", forward_address: "127.0.0.1:7210" }
            - { id: "exec1_r2", address: "127.0.0.1:7111", forward_address: "127.0.0.1:7211" }
            - { id: "exec1_r3", address: "127.0.0.1:7112", forward_address: "127.0.0.1:7212" }

        exec1_replica2:
          ansible_host: 127.0.0.1
          ansible_connection: local
          cluster_id: 1
          cluster_name: "exec1"
          consensus_mode: "raft"
          node_id: "exec1_r2"
          rpc_port: 8648
          raft_port: 7111
          forward_port: 7211
          raft_bootstrap: false
          node_data_dir: "/opt/metanode/exec1_r2"
          address: "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
          raft_peers:
            - { id: "exec1_r1", address: "127.0.0.1:7110", forward_address: "127.0.0.1:7210" }
            - { id: "exec1_r2", address: "127.0.0.1:7111", forward_address: "127.0.0.1:7211" }
            - { id: "exec1_r3", address: "127.0.0.1:7112", forward_address: "127.0.0.1:7212" }

        exec1_replica3:
          ansible_host: 127.0.0.1
          ansible_connection: local
          cluster_id: 1
          cluster_name: "exec1"
          consensus_mode: "raft"
          node_id: "exec1_r3"
          rpc_port: 8649
          raft_port: 7112
          forward_port: 7212
          raft_bootstrap: false
          node_data_dir: "/opt/metanode/exec1_r3"
          address: "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
          raft_peers:
            - { id: "exec1_r1", address: "127.0.0.1:7110", forward_address: "127.0.0.1:7210" }
            - { id: "exec1_r2", address: "127.0.0.1:7111", forward_address: "127.0.0.1:7211" }
            - { id: "exec1_r3", address: "127.0.0.1:7112", forward_address: "127.0.0.1:7212" }

        # Cluster 2: Single-Replica Raft Node
        exec2_replica1:
          ansible_host: 127.0.0.1
          ansible_connection: local
          cluster_id: 2
          cluster_name: "exec2"
          consensus_mode: "raft"
          node_id: "exec2_r1"
          rpc_port: 8647
          raft_port: 7120
          forward_port: 7220
          raft_bootstrap: true
          node_data_dir: "/opt/metanode/exec2"
          address: "0x0d4CC97b62a149a8fe8DE81262270426A80B0935"
          raft_peers:
            - { id: "exec2_r1", address: "127.0.0.1:7120", forward_address: "127.0.0.1:7220" }
```

#### Chế độ B: Triển khai Multi-Server Production (Phân tán qua mạng nội bộ / VPN)
Chỉ cần chỉnh sửa `ansible_host` trỏ IP máy chủ thật, đặt `ansible_connection: ssh`, và trỏ các địa chỉ `raft_peers` tương ứng:
```yaml
all:
  vars:
    ansible_connection: ssh
    ansible_user: metanode
    ansible_ssh_private_key_file: ~/.ssh/metanode_deploy_key

parent_chain_nodes:
  hosts:
    parent_node:
      ansible_host: 10.0.0.10
      parent_http_port: 8547

exec_clusters:
  hosts:
    exec1_replica1:
      ansible_host: 10.0.0.11
      cluster_id: 1
      raft_bootstrap: true
      # ...
    exec1_replica2:
      ansible_host: 10.0.0.12
      cluster_id: 1
      raft_bootstrap: false
      # ...
    exec1_replica3:
      ansible_host: 10.0.0.13
      cluster_id: 1
      raft_bootstrap: false
      # ...
```

### 4.3 Cấu hình Biến Toàn Cục
Trong `group_vars/all.yml`:
- `metanode_evm_chain_id: 991`: Tất cả các cụm thực thi đều sử dụng EVM ChainID 991 để các ví Web3 (Metamask, Rabby) không cần chuyển mạng khi tương tác.
- `metanode_base_dir: /opt/metanode`: Thư mục cài đặt và chứa dữ liệu runtime trên từng node.

---

## 5. QUY TRÌNH TRIỂN KHAI

### 5.1 Cách 1: Triển khai 1-Click tự động (Khuyến nghị)
Script `deploy_clusters.sh` đóng gói toàn bộ quy trình: build, cấu hình, khởi động, healthcheck và gửi thông báo Telegram.

#### Lệnh khởi chạy và test hoàn chỉnh:
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --setup --test
```

**Các bước diễn ra tự động:**
1. 📢 Bắn thông báo **Deploy Bắt Đầu** lên Telegram kèm thông tin commit hash và author.
2. 🔨 Tự động kiểm tra và build nhị phân Go (`parent_chain`, `simple_chain`).
3. 🚀 Khởi chạy **Parent Chain** tại cổng `:8547` và kiểm tra RPC sẵn sàng.
4. ⚡ Khởi chạy đồng thời **Exec Cluster 1** (`:8646`) và **Exec Cluster 2** (`:8647`).
5. 📊 Ping lấy Block Height, Validator Address, BLS Key và bắn thông báo **Dịch Vụ Sẵn Sàng** lên Telegram.
6. 🧪 Tự động kích hoạt **Bộ kiểm thử 5 kịch bản thực tế**.
7. 🏆 Báo cáo kết quả kiểm thử (Thành công / Thất bại) lên Telegram.

### 5.2 Cách 2: Triển khai qua Ansible Playbook trực tiếp
Nếu muốn can thiệp chi tiết bằng lệnh Ansible gốc:
```bash
# Triển khai toàn bộ không chạy test
ansible-playbook -i inventory.yml deploy.yml

# Triển khai và bao gồm chạy test
ansible-playbook -i inventory.yml deploy.yml -e "run_tests=true"

# Chỉ triển khai riêng Parent Chain
ansible-playbook -i inventory.yml deploy.yml --tags parent_chain

# Chỉ triển khai riêng các Execution Clusters
ansible-playbook -i inventory.yml deploy.yml --tags exec_clusters
```

### 5.3 Chế độ Systemd Service vs Daemon
- **Mặc định (Daemon Mode):** Phù hợp môi trường local devnet; tiến trình chạy ngầm qua background job, log ghi vào thư mục `/opt/metanode/logs/` (hoặc `devnet_data/`).
- **Chế độ Systemd (Production Mode):** Để chạy dưới dạng dịch vụ Linux có quản lý vòng đời và tự khởi động lại:
  ```bash
  ./deploy_clusters.sh --setup --systemd --test
  ```
  Hệ thống sẽ tạo 3 service độc lập:
  - `metanode-parentchain.service`
  - `metanode-cluster-1.service`
  - `metanode-cluster-2.service`

---

## 6. QUY TRÌNH KIỂM THỬ TÍCH HỢP

Bộ kiểm thử được viết bằng Go tại [`execution/scripts/test/test_real_world_scenarios.go`](file:///home/abc/chain-n/metanode/execution/scripts/test/test_real_world_scenarios.go), mô phỏng 100% các hành vi giao dịch trong thực tế.

### 6.1 Giải thích chi tiết 5 Kịch bản kiểm thử Tích hợp Xuyên Cụm

| Kịch Bản | Mục Tiêu Kiểm Thử | Hành Động Kỹ Thuật & Luồng Xử Lý | Tiêu Chí Đạt (Pass) |
| :--- | :--- | :--- | :--- |
| **Kịch bản 1: Đăng ký tài khoản** | Đăng ký ví mới vào danh bạ cụm (`Account Registry`) | 1. Sinh cặp khóa ECDSA mới.<br>2. Tạo thông điệp đăng ký ánh xạ ví vào Cluster 2.<br>3. Ký kép: Chữ ký ECDSA của người dùng + Chữ ký BLS của Cluster 2.<br>4. Gửi `SendRegisterAccount` lên Parent Chain. | Truy vấn `GetAccountRegistry(addr)` trên Parent Chain trả về đúng BLS Public Key của Cluster 2. |
| **Kịch bản 2: Nạp tiền Float & Ghi có số dư** | Nạp tiền ký quỹ từ Parent Chain vào cụm thực thi | 1. Gửi `SendDepositToFloat` từ ví nguồn trên Parent Chain tới tài khoản mới tại Cluster 2.<br>2. `ReceiveWorker` của Cluster 2 thăm dò (poll) Parent Chain.<br>3. Cluster 2 tự động sinh block ghi có (credit) số dư. | Số dư của ví trên Cluster 2 (`eth_getBalance`) tăng đúng bằng số tiền nạp. |
| **Kịch bản 3: Thực thi Contract nội bộ** | Kiểm tra engine MVM / EVM Cancun độc lập | 1. Thực hiện `eth_call` truy vấn smart contract hệ thống Staking (`0x1001`).<br>2. Tạo giao dịch gọi hàm `setBlsPublicKey(bytes)` trên Smart Contract `AccountSetting`.<br>3. Ký transaction EVM chuẩn với ChainID 991 và gửi `eth_sendRawTransaction`. | Giao dịch được đóng gói thành công vào block của Cluster 1, trả về mã băm TxHash hợp lệ. |
| **Kịch bản 4: Chuyển tiền xuyên 2 cụm** | Chuyển tiền liên shard (Cluster 1 -> Cluster 2) | 1. Cluster 1 gọi RPC `mtn_sendCrossChainTransfer`.<br>2. Cluster 1 trừ tiền người gửi, `SendWorker` ký BLS gửi lên Parent Chain.<br>3. Parent Chain ghi nhận vào danh sách inbound.<br>4. `ReceiveWorker` của Cluster 2 bắt được và credit tiền cho người nhận. | Số dư tài khoản đích trên Cluster 2 tăng đúng lượng tiền chuyển trong vòng < 40 giây. |
| **Kịch bản 5: Parent Chain sập, Cụm tự vận hành** | Khả năng độc lập và chịu lỗi (Resilience) | 1. Cưỡng chế dừng hoàn toàn tiến trình Parent Chain (`pkill parent_chain`).<br>2. Gửi một giao dịch chuyển tiền nội bộ trên Cluster 1.<br>3. Cluster 1 tiếp tục tự đào block, khớp lệnh và cập nhật state trie bình thường.<br>4. Khởi động lại Parent Chain để phục hồi mạng. | Block Height Cluster 1 tiếp tục tăng, số dư người nhận cập nhật chính xác dù Parent Chain chết hoàn toàn. |

### 6.2 Kiểm thử Khả Năng Chịu Lỗi & Auto-Failover Cụm Raft (HA Testing)

Bộ kiểm thử tại [`execution/scripts/test/test_raft_fault_tolerance.go`](file:///home/abc/chain-n/metanode/execution/scripts/test/test_raft_fault_tolerance.go) tự động chứng minh năng lực phục hồi tức thời của cụm thực thi chạy Raft (`hashicorp/raft v1.7.1`):

```
                                  SỰ CỐ XẢY RA: KILL -9 LEADER n0
                                               │
                                 ┌─────────────┴─────────────┐
                                 │                           │
                   Follower n1 phát hiện       Follower n2 phát hiện
                   heartbeat timeout (100ms)   heartbeat timeout (100ms)
                                 │                           │
                                 └─────────────┬─────────────┘
                                               ▼
                               Bầu cử Leader mới (Election)
                               Term 2 -> Term 3 (Tốn ~207ms)
                                               ▼
                                      👑 LEADER MỚI: n2
                                  Quorum 2/3 tiếp tục đóng block
                                               ▼
                              n0 khởi động lại -> Tự động Catch-up
                             100% Khớp Block Height (Zero-Fork)
```

#### Quy trình 5 bước kiểm thử chịu lỗi Raft:
1. **Khởi tạo & Bầu Leader ban đầu:** Khởi chạy 3 replica (`n0`, `n1`, `n2`), xác minh `n0` được bầu làm Leader ban đầu (Term 2).
2. **Giao dịch ban đầu:** Gửi transaction khi cả 3 node đang online -> cụm commit batch và sinh Block Height #1.
3. **Giả lập sự cố phần cứng (`kill -9 n0`):** Cưỡng chế tắt Leader ngay lập tức.
4. **Bầu Leader tự động (Auto-Failover):** 2 node còn lại (`n1`, `n2`) phát hiện mất tín hiệu và tự động bầu `n2` làm Leader mới chỉ trong **207 ms**. Cụm tiếp tục nhận giao dịch và đóng Block Height #2 bình thường (không gián đoạn dịch vụ).
5. **Phục hồi & Đồng bộ bắt kịp (Catch-Up):** Bật lại `n0`. Node này tự động gia nhập lại mạng Raft, đồng bộ state/log và đạt cùng Block Height #3 với toàn cụm.

#### Lệnh chạy kiểm thử Raft & tự động bắn Telegram:
```bash
cd /home/abc/chain-n/metanode
go run execution/scripts/test/test_raft_fault_tolerance.go
```
*Kết quả failover sub-second và bằng chứng Zero-Fork sẽ được bot Telegram tự động đẩy về máy của bạn.*

### 6.3 Các lệnh chạy test tích hợp toàn diện

#### Chạy test tích hợp kèm thông báo Telegram:
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --test-only
```

#### Chạy test trực tiếp bằng Go (để debug chi tiết):
```bash
cd /home/abc/chain-n/metanode
go run execution/scripts/test/test_real_world_scenarios.go
```
*Tùy biến URL endpoint qua biến môi trường:*
```bash
PARENT_CHAIN_URL="http://127.0.0.1:8547" \
EXEC1_URL="http://127.0.0.1:8646" \
EXEC2_URL="http://127.0.0.1:8647" \
go run execution/scripts/test/test_real_world_scenarios.go
```

### 6.4 Kiểm tra báo cáo Telegram & Logs
- Khi test chạy xong, bot Telegram sẽ gửi báo cáo dạng bảng chi tiết từng kịch bản (Kèm emoji ✅ / ❌ và thời gian thực thi).
- Log chi tiết từng bước được ghi tại: `deploy/ansible_clusters/test_run.log`.

---

## 7. CƠ CHẾ PHỤC HỒI KHI PARENT CHAIN OFFLINE

Hệ thống được thiết kế theo nguyên lý **100% Không Fork (Zero-Fork Invariant)** và **Thà Pending chứ tuyệt đối không fork**.

### Khi Parent Chain Offline:
1. **Giao dịch nội bộ:** Hoàn toàn không bị ảnh hưởng, cụm tiếp tục sinh block độc lập.
2. **Giao dịch xuyên cụm tại Cụm Gửi:** Số dư người gửi bị trừ cục bộ, giao dịch lưu vào `rollup.Store` ở trạng thái `StateLocalAppliedPendingSend`. `SendWorker` gặp lỗi kết nối sẽ lùi lại (backoff) và retry định kỳ, tuyệt đối không làm crash node hay hủy bỏ giao dịch sai lệch.
3. **Tại Cụm Nhận:** `ReceiveWorker` không thể poll dữ liệu mới nên tạm hoãn, chờ kết nối phục hồi.

### Khi Parent Chain Online Trở Lại:
1. **Bắt tay đồng bộ (`GetFloatSeq`):** `SendWorker` tự động kết nối lại Parent Chain, lấy sequence counter hiện hành để tránh xung đột nonce.
2. **Xả hàng đợi (Drain Non-Terminal):** `SendWorker` quét toàn bộ giao dịch đang pending trong LevelDB, ký chữ ký BLS của cụm và đẩy tuần tự lên Parent Chain.
3. **Bắt kịp con trỏ (Cursor Catch-Up):** `ReceiveWorker` của Cụm Nhận tiếp tục poll từ `cursor` cũ, nhận trọn vẹn danh sách transfer tồn đọng, credit tiền cho người nhận và gửi `SendMarkClaimed` về Parent Chain.
4. **Bảo vệ tài sản quá hạn (`ReclaimWorker`):** Nếu sự cố kéo dài quá `TimeoutBlocks` quy định, `ReclaimWorker` sẽ gửi `SendReclaimFloat` lên Parent Chain để hoàn tiền lại vào tài khoản người gửi ban đầu.

---

## 8. VẬN HÀNH & GIÁM SÁT THƯỜNG NHẬT

### 8.1 Danh mục lệnh điều hành nhanh

| Mục Đích | Lệnh Thực Hiện |
| :--- | :--- |
| **Kiểm tra trạng thái các cụm** | `./deploy_clusters.sh --status` |
| **Chỉ chạy bộ kiểm thử** | `./deploy_clusters.sh --test-only` |
| **Khởi động lại toàn bộ cụm** | `./deploy_clusters.sh --restart` |
| **Dừng an toàn toàn bộ cụm** | `./deploy_clusters.sh --stop` |
| **Cập nhật mã nguồn & restart** | `./deploy_clusters.sh --deploy` |
| **Dọn dẹp logs và database** | `./deploy_clusters.sh --clean` |
| **Reset toàn bộ về Genesis (Block 0)** | `./deploy_clusters.sh --reset` |

### 8.2 Truy vấn thủ công qua JSON-RPC (curl)

#### Kiểm tra Block Number:
```bash
# Parent Chain
curl -s -X POST http://127.0.0.1:8547 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq

# Exec Cluster 1
curl -s -X POST http://127.0.0.1:8646 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq

# Exec Cluster 2
curl -s -X POST http://127.0.0.1:8647 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq
```

#### Kiểm tra EVM ChainID:
```bash
curl -s -X POST http://127.0.0.1:8646 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"net_version","params":[],"id":1}' | jq
# Kết quả mong đợi: "result": "991"
```

#### Kiểm tra số dư tài khoản:
```bash
curl -s -X POST http://127.0.0.1:8646 -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5","latest"],"id":1}' | jq
```

### 8.3 Giám sát Logs

- **Với chế độ Daemon:**
  ```bash
  tail -f devnet_data/parent/node.log
  tail -f devnet_data/exec_1/node.log
  tail -f devnet_data/exec_2/node.log
  ```
- **Với chế độ Systemd:**
  ```bash
  journalctl -u metanode-parentchain.service -f
  journalctl -u metanode-cluster-1.service -f
  journalctl -u metanode-cluster-2.service -f
  ```

---

## 9. XỬ LÝ SỰ CỐ THƯỜNG GẶP (TROUBLESHOOTING)

### 1. Sự cố: Trùng cổng mạng (`Address already in use`)
- **Triệu chứng:** Node không thể khởi động, log báo lỗi bind cổng `8547`, `8646`, hoặc `4000`.
- **Cách xử lý:**
  ```bash
  # Tìm và tắt tiến trình đang chiếm dụng cổng
  sudo lsof -i :8547 -i :8646 -i :8647
  ./deploy_clusters.sh --stop
  ```

### 2. Sự cố: Parent Chain crash do sai đường dẫn relative (`os error 2`)
- **Triệu chứng:** Log Parent Chain báo `No such file or directory` đối với `node_0_protocol_key.json`.
- **Nguyên nhân:** File cấu hình Rust `node_devnet_parent.toml` sử dụng đường dẫn tương đối `../../../consensus/...`, đòi hỏi tiến trình `parent_chain` phải có Working Directory (CWD) chuẩn là `execution/scripts/test`.
- **Cách xử lý:** Script `deploy_clusters.sh` và role `parent_chain` đã được thiết lập CWD tự động chuẩn xác. Nếu khởi chạy thủ công, luôn đảm bảo lệnh chạy có dạng:
  ```bash
  cd /home/abc/chain-n/metanode/execution/scripts/test && ./parent_chain ...
  ```

### 3. Sự cố: Giao dịch xuyên cụm bị pending kéo dài
- **Triệu chứng:** Ví người nhận trên Cụm 2 chưa có số dư sau 40s.
- **Cách kiểm tra:**
  1. Kiểm tra xem Parent Chain `:8547` có đang phản hồi không (`curl http://127.0.0.1:8547`).
  2. Kiểm tra log của `SendWorker` trên Cụm 1 xem đã nạp cọc Float và gửi `SendInboundTransfer` thành công chưa.
  3. Kiểm tra log của `ReceiveWorker` trên Cụm 2 xem `cursor` hiện tại có đang bắt kịp không.
- **Xử lý:** Khởi động lại Parent Chain; các worker sẽ tự động tái kết nối và đẩy các giao dịch pending sang Cụm 2.

---
*Tài liệu này được biên soạn cho hệ thống MetaNode Core Blockchain. Mọi cập nhật cấu trúc hoặc giao thức FFI cần được đồng bộ vào `PROJECT_STRUCTURE.md`.*
