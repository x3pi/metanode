---
sidebar_position: 7
title: 🌐 Triển khai Private Chain
---

# 🌐 Hướng dẫn Triển khai Private Chain (1 Validator & Multi-Validator)

Tài liệu này hướng dẫn chi tiết cách khởi tạo và triển khai một mạng **Metanode Private Chain (Chuỗi khối riêng tư)** độc lập. Bạn có thể triển khai một Private Chain với **1 validator (Single Node Private Network)** để phục vụ phát triển/thử nghiệm smart contract, hoặc với $N$ validator nodes cho cụm mạng doanh nghiệp riêng tư.

---

## 💡 Tổng quan & Đặc điểm Private Chain

- **Hỗ trợ 1 Validator (Single Node):** Nhờ cơ chế BFT `Committee::new` ($N=1, f=0, Quorum=100\%$), node đơn lẻ tự chốt khối (Commit) ngay lập tức mà không cần mạng P2P bên ngoài hay phụ thuộc timeout.
- **Tự chọn Chain ID:** Khởi tạo Chain ID riêng biệt (mặc định: `1337`), tránh xung đột giao dịch với Testnet/Mainnet công khai.
- **Nạp tiền tự động (Pre-funded Accounts):** Tự động sinh danh sách tài khoản dev kèm Private Keys (`dev_accounts.json`) và nạp sẵn tiền MTN token trong file `genesis.json`.
- **Script 1-Click Management:** Công cụ tự sinh các script `start_private_chain.sh` và `stop_private_chain.sh` giúp khởi chạy và dừng mạng lưới dễ dàng.

---

## 🚀 Quick Start — Khởi tạo Private Chain 1 Validator

### Bước 1: Chạy công cụ khởi tạo

Chạy lệnh sau từ thư mục root của dự án `metanode`:

```bash
# Khởi tạo Private Chain với Chain ID 1337, 1 validator và nạp tiền 5 ví dev
./scripts/init_private_chain.sh --chain-id 1337 --validators 1 --output-dir ./my_private_chain
```

#### Các tham số tùy chỉnh khả dụng:
| Tham số | Ý nghĩa | Mặc định |
| :--- | :--- | :--- |
| `--chain-id` | EVM Chain ID cho mạng riêng tư | `1337` |
| `--validators` | Số lượng validator node ($1, 2, 3, \dots$) | `1` |
| `--ip` | Địa chỉ IP của node | `127.0.0.1` |
| `--output-dir` | Thư mục lưu dữ liệu & cấu hình | `./private_chain_data` |
| `--alloc-balance` | Số dư MTN nạp sẵn cho mỗi tài khoản ví dev | `1000000` (MTN) |
| `--dev-accounts` | Số lượng ví dev được tự động nạp sẵn tiền | `5` |

---

### Bước 2: Khởi chạy Private Chain

Chuyển vào thư mục vừa tạo và khởi chạy node:

```bash
cd ./my_private_chain
bash start_private_chain.sh
```

**Output mẫu sau khi khởi chạy:**
```text
🚀 Starting Metanode Private Chain (1 validator node(s))...
  → Starting Node-0 (RPC: http://127.0.0.1:8545)...
✅ Private Chain started successfully!
   RPC URL: http://127.0.0.1:8545
   Chain ID: 1337
   Check logs in ./my_private_chain/node-0/logs/node-0.log
```

---

### Bước 3: Kết nối MetaMask / Hardhat / Web3

Sau khi node chạy, bạn có thể kết nối MetaMask hoặc các công cụ EVM (Hardhat, Remix, Foundry, Ethers.js) với các thông số:

- **RPC URL:** `http://127.0.0.1:8545`
- **Chain ID:** `1337`
- **Currency Symbol:** `MTN`
- **Private Keys (Ví đã nạp tiền):** Xem trong file `dev_accounts.json` vừa tạo:

```bash
cat dev_accounts.json
```

Ví dụ output `dev_accounts.json`:
```json
[
  {
    "private_key": "0x4e286399188da7eacd33baa0ea4453db8a938e996e83cafc9a5c51c9a9a754cb",
    "address": "0x5c8272f827697b47083c946cce673ee580d90e2a"
  },
  ...
]
```

Dùng **Private Key** ở trên import vào MetaMask để bắt đầu gửi giao dịch hoặc deploy Smart Contract.

---

## 🌐 Triển khai Private Chain Nhiều Validator ($N > 1$)

Để khởi tạo một Private Chain gồm 3 validator nodes trên cùng 1 máy chủ local:

```bash
./scripts/init_private_chain.sh --chain-id 1337 --validators 3 --output-dir ./multi_node_private_chain
cd ./multi_node_private_chain
bash start_private_chain.sh
```

Hệ thống sẽ tự động gán cổng giao tiếp chéo và cấu hình liên kết P2P cho từng node:
- **Node-0 RPC:** `http://127.0.0.1:8545`
- **Node-1 RPC:** `http://127.0.0.1:8546`
- **Node-2 RPC:** `http://127.0.0.1:8547`

---

## 🛑 Dừng Private Network

Để dừng toàn bộ các tiến trình đang chạy của Private Chain:

```bash
bash stop_private_chain.sh
```

---

## 🔍 Kiểm tra Log & Trạng thái

- Xem log hoạt động của Node 0:
  ```bash
  tail -f node-0/logs/node-0.log
  ```
- Kiểm tra Block Number hiện tại qua curl RPC:
  ```bash
  curl -X POST http://127.0.0.1:8545 \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
  ```
