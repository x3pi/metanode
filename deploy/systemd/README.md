# 🏢 Metanode Private Chains Deployment (Systemd / Local Mode)

Tài liệu hướng dẫn khởi tạo, vận hành và dừng cụm **2 Private Chains độc lập** (Chain 101 và Chain 102) phục vụ kiểm thử Cross-Chain và tích hợp hệ thống.

---

## 🚀 1. Các lệnh vận hành chính

Vào thư mục:
```bash
cd /home/abc/nhat/consensus-chain/metanode/deploy/systemd
```

### ▶️ Khởi động / Khởi tạo 2 Private Chains:
- **Chạy tiếp tục (Giữ nguyên database cũ + build code):**
  ```bash
  bash setup_2_private_chains.sh
  ```

- **Khởi tạo mới hoàn toàn (Xóa sạch data cũ & sinh genesis mới):**
  ```bash
  bash setup_2_private_chains.sh --clean
  ```

- **Khởi động nhanh (Bỏ qua bước build Go/Rust nếu không sửa code):**
  ```bash
  bash setup_2_private_chains.sh --no-build
  ```

- **Kết hợp Reset sạch data + Chạy nhanh:**
  ```bash
  bash setup_2_private_chains.sh --clean --no-build
  ```

---

## 🛑 2. Các lệnh dừng (Stop)

### ⏹️ Dừng cả 2 Private Chains cùng lúc:
```bash
bash private_chains_data/stop_all.sh
```

### ⏹️ Dừng từng chain riêng biệt:
- **Dừng Chain A (101):**
  ```bash
  bash private_chains_data/chain_101/stop_single_chain.sh
  ```
- **Dừng Chain B (102):**
  ```bash
  bash private_chains_data/chain_102/stop_single_chain.sh
  ```

### ⚡ Dừng khẩn cấp toàn bộ tiến trình node trên máy:
```bash
pkill -f simple_chain
```

---

## 🌐 3. Thông số Mạng & Cổng Kết Nối (Port Isolation)

Hai private chain được cấp phát port offset riêng biệt để **không bao giờ bị xung đột (trùng port)** với cụm Public Chain (Ansible 3-Node Cluster):

| Thông số | Private Chain A (Source) | Private Chain B (Dest) | Public Cluster (Ansible) |
| :--- | :--- | :--- | :--- |
| **Chain ID** | `101` | `102` | `991` |
| **HTTP RPC** | `http://127.0.0.1:8546` | `http://127.0.0.1:8547` | `http://192.168.1.233:10746` |
| **Primary TCP** | `4210` | `4220` | `6200 - 6202` |
| **Worker TCP** | `5022` | `5032` | `4012 - 4014` |
| **P2P Port** | `10210` | `10220` | `9100 - 9102` |
| **Storage Port** | `13010` | `13020` | `9200 - 9202` |

---

## 📂 4. Cấu trúc thư mục dữ liệu (`private_chains_data/`)

Thư mục `private_chains_data/` đã được cấu hình trong `.gitignore` để không bị commit vào Git repo:

```text
deploy/systemd/private_chains_data/
├── start_all.sh                     # Script khởi động cả 2 chain
├── stop_all.sh                      # Script dừng cả 2 chain
├── chain_101/                       # Dữ liệu Chain A (ChainID 101)
│   ├── dev_accounts.json            # Danh sách ví test có sẵn tiền
│   ├── start_single_chain.sh        # Khởi động riêng Chain 101
│   ├── stop_single_chain.sh         # Dừng riêng Chain 101
│   └── node-0/
│       ├── data/                    # NOMT DB & Trie storage
│       └── logs/node-0.log          # Log hoạt động thời gian thực
└── chain_102/                       # Dữ liệu Chain B (ChainID 102)
    ├── dev_accounts.json
    ├── start_single_chain.sh
    ├── stop_single_chain.sh
    └── node-0/
        ├── data/
        └── logs/node-0.log
```

---

## 🔍 5. Xem log hoạt động theo thời gian thực

- **Xem log Chain A (101):**
  ```bash
  tail -f deploy/systemd/private_chains_data/chain_101/node-0/logs/node-0.log
  ```
- **Xem log Chain B (102):**
  ```bash
  tail -f deploy/systemd/private_chains_data/chain_102/node-0/logs/node-0.log
  ```
