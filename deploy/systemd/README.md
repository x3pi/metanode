# 🚀 Metanode Private Chains & Cross-Chain Quickstart

Hướng dẫn nhanh khởi động, đăng ký danh bạ committee, chạy Relayer và giám sát 2 Private Chains (101 & 102).

---

## 0. Yêu cầu Hệ thống (Prerequisites)

Trước khi chạy các script deploy (`setup_root_anchor.sh`, `setup_2_private_chains.sh`, v.v.), bạn cần đảm bảo môi trường đã cài đặt thư viện Python cần thiết:

```bash
pip3 install web3 eth-account eth-keys --break-system-packages
```

---

## 1. Vận hành 2 Private Chains (Chain 101 & 102)

Vào thư mục:
```bash
cd /home/abc/nhat/consensus-chain/metanode/deploy/systemd
```

| Thao tác | Lệnh thực hiện |
| :--- | :--- |
| **Khởi động 2 Chains** | `bash setup_2_private_chains.sh` |
| **Reset mới hoàn toàn** | `bash setup_2_private_chains.sh --clean` |
| **Khởi động kèm Root Anchor RPC khác** | `bash setup_2_private_chains.sh --clean --root-anchor-rpc=http://<IP_PUBLIC>:10746` |
| **Chạy nhanh (không build lại)** | `bash setup_2_private_chains.sh --no-build` |
| **Dừng cả 2 chains** | `bash private_chains_data/stop_all.sh` |
| **Dừng khẩn cấp** | `pkill -f simple_chain` |

---

## 2. Đăng ký & Vận hành Cross-Chain (2 Chains)

| Thao tác | Lệnh thực hiện |
| :--- | :--- |
| **Đăng ký Gateway (Root Anchor Local)** | `bash register_2_private_chains.sh "http://127.0.0.1:10746"` |
| **Đăng ký Gateway (Root Anchor Remote/Multi-node)** | `bash register_2_private_chains.sh "http://<IP_PUBLIC_CHAIN>:10746"` |
| **Chạy Relayer Daemon** | `ROOT_ANCHOR="http://<IP_PUBLIC_CHAIN>:10746" bash start_relayer_daemon.sh` |
| **Dừng Relayer Daemon** | `pkill -f cross_chain_relayer` |

---

## 3. Giao diện Web Dashboard Giám Sát (P7)

| Thao tác | Lệnh thực hiện |
| :--- | :--- |
| **Bật Dashboard** | `cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible/monitors/cross_chain_dashboard && go run main.go --port 8088` |
| **Tắt Dashboard** | `pkill -f "cross_chain_dashboard"` |

🌐 **Mở xem trên trình duyệt:**  
👉 **[http://192.168.1.233:8088](http://192.168.1.233:8088)** *(hoặc `http://localhost:8088`)*

---

## 4. Danh sách Cổng Kết Nối (RPC & Ports)

* **Private Chain A (101):** `http://127.0.0.1:8546`
* **Private Chain B (102):** `http://127.0.0.1:8547`
* **Public Chain Root Anchor (991):** `http://192.168.1.233:10746` (hoặc `http://127.0.0.1:10746`)

---

## 5. Xem Log Thời Gian Thực

* **Chain A:** `tail -f private_chains_data/chain_101/node-0/logs/node-0.log`
* **Chain B:** `tail -f private_chains_data/chain_102/node-0/logs/node-0.log`
* **Relayer:** `tail -f relayer_logs/relayer.log` (hoặc stdout của `start_relayer_daemon.sh`)

