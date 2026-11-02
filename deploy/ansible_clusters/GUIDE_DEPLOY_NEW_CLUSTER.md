# 📘 Hướng Dẫn Chi Tiết Triển Khai Private Chain (Execution Cluster) Lên Server Mới

Tài liệu này ghi lại toàn bộ các bước kỹ thuật chuẩn hóa để triển khai một cụm Private Chain mới (Raft Consensus, Chain ID tùy chỉnh) lên các máy chủ VPS/Cloud mới từ đầu đến cuối.

---

## 📋 Mục Lục
1. [Yêu cầu tiên quyết trên Server mới (OS & Runtime)](#1-yêu-cầu-tiên-quyết-trên-server-mới)
2. [Cấu hình Inventory Ansible](#2-cấu-hình-inventory-ansible)
3. [Build Binary trên máy điều khiển (Controller 234)](#3-build-binary-trên-máy-điều-khiển)
4. [Lệnh Deploy & Khởi chạy tự động](#4-lệnh-deploy--khởi-chạy-tự-động)
5. [Kiểm tra sau Deploy & Cẩm nang Vận hành](#5-kiểm-tra-sau-deploy--cẩm-nang-vận-hành)

---

## 1. Yêu Cầu Tiên Quyết Trên Server Mới

> 🚨 **CỰC KỲ QUAN TRỌNG:** Cơ sở dữ liệu State của MetaNode (NOMT) sử dụng trực tiếp tính năng `io_uring` của Linux Kernel và thư viện C++ MVM.

- **Hệ điều hành:** Bắt buộc cài đặt **Ubuntu 24.04 LTS (Noble Numbat)** với **Linux Kernel 6.8+** và **GLIBC 2.39**. *(Không dùng Ubuntu 22.04 do thiếu tính năng `IORING_SETUP_SINGLE_ISSUER` của io_uring)*.
- **Cài đặt thư viện C++ runtime và công cụ mạng:**
  Chạy lệnh sau trên tất cả các server mới:
  ```bash
  sudo apt-get update && sudo apt-get install -y libtbb12 libxapian30 ufw curl
  ```
- **Tài khoản SSH:**
  - Có user SSH (ví dụ `stackops` hoặc `abc`) có quyền `sudo`.
  - Mở cổng SSH (mặc định port `22` hoặc custom port như `234`).

---

## 2. Cấu Hình Inventory Ansible

Trong thư mục `deploy/ansible_clusters/`, tạo hoặc chỉnh sửa file inventory (ví dụ: `inventory_cloud.yml`):

### 2.1. Cấu hình thông số cụm (`vars`):
```yaml
    exec_clusters:
      children:
        cluster_cloud:
          vars:
            cluster_id: 1                       # ID cụm (ví dụ: 1)
            cluster_name: "pri_cloud"           # Tên cụm
            consensus_mode: "raft"              # Chế độ đồng thuận Raft
            ansible_user: "stackops"            # User SSH trên server
            ansible_port: 234                   # Cổng SSH
            ansible_become_pass: !vault |...    # Mật khẩu sudo (mã hóa qua ansible-vault)
            ansible_ssh_pass: "{{ ansible_become_pass }}"
            
            # Kết nối về Parent Chain (để neo rollup & nạp rút)
            standalone_chain: false
            account_gate: "parent_registered"
            parent_chain_url: "http://<PARENT_IP>:28601"
            parent_chain_urls: "http://<PARENT_IP>:28601,http://<PARENT_IP>:28602,..."
            
            # Cổng mạng của Execution Node
            rpc_port: 8750                      # Cổng JSON-RPC cho MetaMask / dApp
            p2p_port: 4200                      # Cổng P2P
            raft_port: 7110                     # Cổng đồng thuận Raft
            forward_port: 7210                  # Cổng chuyển tiếp giao dịch
            raft_bind_address: "0.0.0.0:7110"   # Lắng nghe trên tất cả card mạng
            forward_bind_address: "0.0.0.0:7210"
            raft_secret_source: "pri_1a"        # Node nguồn tạo secret key Raft
            node_data_dir: "/opt/metanode/pri_cloud"

            # Cặp khóa Sequencer & BLS Validator của cụm
            private_key: "<SEQUENCER_PRIVATE_KEY_HEX>"
            address: "<SEQUENCER_ETH_ADDRESS>"
            bls_priv: "<BLS_PRIVATE_KEY_HEX>"
            bls_pubkey: "<BLS_PUBLIC_KEY_HEX>"
```

### 2.2. Khai báo danh sách các Node (`hosts`):
Mỗi cụm Raft chuẩn cần tối thiểu **3 nodes** để chịu lỗi 1 node:
```yaml
          hosts:
            pri_1a:
              ansible_host: 61.28.233.146       # IP máy chủ 1
              node_id: "pri_r1"
              raft_bootstrap: true              # Node đầu tiên khởi tạo cụm Raft
            pri_1b:
              ansible_host: 116.118.92.254      # IP máy chủ 2
              node_id: "pri_r2"
              raft_bootstrap: false
            pri_1c:
              ansible_host: 49.213.71.191       # IP máy chủ 3
              node_id: "pri_r3"
              raft_bootstrap: false
```

### 2.3. 🏷️ Quy Tắc Đặt Tên (Naming Convention) — Đặt Tên Ở Đâu?

Trong file Inventory có **3 vị trí quan trọng quyết định tên** của hệ thống:

1. **`cluster_name: "pri_cloud"`** (Dòng ~43):
   - Đặt tên cho **Toàn bộ Cụm Chain** (ví dụ: `pri_cloud`, `game_shard1`, `payment_cluster`...).
   - Tên này dùng để:
     - Đặt tên file xuất: `/tmp/rpc_nodes.<cluster_name>.json`
     - Phân vùng tiến trình Monitor ngầm: `[pri_cloud] health: RUNNING`
     - Hiển thị trong thông báo Telegram: `Cụm pri_cloud đã khởi động`.

2. **Tên Node trong `hosts:` (ví dụ `pri_1a`, `pri_1b`, `pri_1c`)** (Dòng ~77):
   - Đây là **Tên định danh của từng máy chủ Node** (Hostname trong Ansible).
   - Tên này trực tiếp quyết định:
     - **Tên Service Systemd:** `metanode-pri_1a.service` (hoặc `metanode-<tên_node>.service`)
     - **Tên File Log:** `/var/log/metanode/pri_1a.log`
     - **Tên hiển thị bảng Status:** `• pri_1a: http://61.28.233.146:8750`

3. **`node_id: "pri_r1"`** (Dòng ~79):
   - ID định danh nội bộ của replica trong **thuật toán đồng thuận Raft** (ví dụ: `pri_r1`, `pri_r2`, `pri_r3` hoặc `r1`, `r2`, `r3`).
   - Giúp các node trong cluster biết ai là ai để bầu chọn Leader và gửi heartbeat.

> **Mẹo mã hóa mật khẩu sudo vào file inventory:**
> ```bash
> cd deploy/ansible_clusters
> ansible-vault encrypt_string --vault-password-file .vault_pass '<MẬT_KHẨU>' --name ansible_become_pass
> ```

---

## 3. Build Binary Trên Máy Điều Khiển (Controller 234)

Trước khi đẩy lên server mới, đảm bảo code đã được build release sạch sẽ:

```bash
cd /home/abc/nhat/con-chain-v2/metanode/consensus/metanode/scripts
bash build_check.sh --release
```

Sau khi build xong, copy binary vào thư mục deploy của Ansible:
```bash
cp /home/abc/nhat/con-chain-v2/metanode/execution/cmd/simple_chain/simple_chain \
   /home/abc/nhat/con-chain-v2/metanode/execution/scripts/test/simple_chain
```

---

## 4. Lệnh Deploy & Khởi Chạy Tự Động

Tại thư mục `deploy/ansible_clusters`:

### Bước 4.1: Kiểm tra kết nối mạng tới các server mới
```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_clusters
ansible -i inventory_cloud.yml exec_clusters -m ping
```
*(Đảm bảo cả 3 server đều trả về `pong`)*.

### Bước 4.2: Chạy lệnh Setup toàn bộ cụm
```bash
./deploy_clusters.sh -i inventory_cloud.yml --setup --exec-only --env=devnet
```

> **Hệ thống tự động thực hiện 100%:**
> 1. Tạo cây thư mục `/opt/metanode/...` và phân quyền cho user `stackops`.
> 2. Phân phối file binary `simple_chain` và keygenesis.
> 3. Tự động sinh `genesis.json` và đồng bộ khóa bảo mật `raft_secret.key` giữa 3 server.
> 4. Cấu hình mở cổng firewall UFW (`8750, 4200, 7110, 7210`).
> 5. Cài đặt và khởi chạy Systemd Service: `metanode-pri_1a/b/c`.
> 6. Kiểm tra RPC online tự động.

---

## 5. Kiểm Tra Sau Deploy & Cẩm Nang Vận Hành

### 5.1. Xem bảng trạng thái toàn diện (Live Status)
> ⚠️ **LƯU Ý:** Phải luôn có tham số `-i inventory_cloud.yml`, nếu không có `-i` script sẽ kiểm tra `inventory.yml` mặc định của các node cũ!

```bash
./deploy_clusters.sh -i inventory_cloud.yml --status
```

### 5.2. Test gọi JSON-RPC trực tiếp:
```bash
# Kiểm tra block number:
curl -s http://61.28.233.146:8750 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

# Kiểm tra chain ID (kết quả 0x3df = 991):
curl -s http://61.28.233.146:8750 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}'
```

### 5.3. Xem log thời gian thực trên server:
```bash
ssh stackops@61.28.233.146 -p 234 "tail -f /var/log/metanode/pri_1a.log"
```

### 5.4. Các lệnh quản trị cụm (Luôn kèm `--exec-only`):
- **Cập nhật binary mới khi sửa code:**
  `./deploy_clusters.sh -i inventory_cloud.yml --deploy --exec-only --env=devnet`
- **Xóa sạch dữ liệu & Reset về Block 0:**
  `./deploy_clusters.sh -i inventory_cloud.yml --reset --exec-only --env=devnet`
- **Khởi động lại (Restart):**
  `./deploy_clusters.sh -i inventory_cloud.yml --restart --exec-only`
- **Tạm dừng (Stop):**
  `./deploy_clusters.sh -i inventory_cloud.yml --stop --exec-only`
