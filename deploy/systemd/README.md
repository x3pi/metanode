# 🚀 Metanode Private Chains & Dashboard Quickstart

Hướng dẫn nhanh khởi động, dừng 2 Private Chains và Web Dashboard giám sát.

---

## 0. Yêu cầu Hệ thống (Prerequisites)

Trước khi chạy các script deploy (`setup_root_anchor.sh`, `setup_2_private_chains.sh`, v.v.), bạn cần đảm bảo môi trường đã cài đặt thư viện Python cần thiết:

```bash
# Cài đặt các thư viện Python để sinh genesis và keys
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
| **Khởi động** | `bash setup_2_private_chains.sh` |
| **Reset mới hoàn toàn** | `bash setup_2_private_chains.sh --clean` |
| **Chạy nhanh (không build)** | `bash setup_2_private_chains.sh --no-build` |
| **Dừng cả 2 chains** | `bash private_chains_data/stop_all.sh` |
| **Dừng khẩn cấp** | `pkill -f simple_chain` |

---

## 2. Giao diện Web Dashboard Giám Sát (P7)

| Thao tác | Lệnh thực hiện |
| :--- | :--- |
| **Bật Dashboard** | `cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible/monitors/cross_chain_dashboard && go run main.go --port 8088` |
| **Tắt Dashboard** | `pkill -f "cross_chain_dashboard"` |

🌐 **Mở xem trên trình duyệt Laptop:**  
👉 **[http://192.168.1.233:8088](http://192.168.1.233:8088)** *(hoặc `http://localhost:8088`)*

---

## 3. Danh sách Cổng Kết Nối (RPC & Ports)

* **Private Chain A (101):** `http://127.0.0.1:8546`
* **Private Chain B (102):** `http://127.0.0.1:8547`
* **Public Chain (991):** `http://192.168.1.233:10746`

---

## 4. Xem Log Thời Gian Thực

* **Chain A:** `tail -f private_chains_data/chain_101/node-0/logs/node-0.log`
* **Chain B:** `tail -f private_chains_data/chain_102/node-0/logs/node-0.log`
