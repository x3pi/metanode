---
sidebar_position: 3
title: Chạy Sync-Only Node
---

# Chạy Sync-Only Node (Full Node)

Sync-Only Node đồng bộ toàn bộ dữ liệu chuỗi nhưng **không tham gia consensus**. Dùng cho: RPC public, block explorer, ví, dApp backend.

---

## Khác biệt so với Validator

| | Validator | Sync-Only |
|---|---|---|
| Tham gia đồng thuận | ✅ | ❌ |
| Cần đăng ký genesis | ✅ | ❌ — Chạy bất kỳ lúc nào |
| Cần `protocol_key`, `network_key` | ✅ | ❌ |
| Cần `BLS_PRIVATE_KEY` | ✅ | ✅ (key bất kỳ, không cần trong genesis) |
| Serve JSON-RPC cho clients | Tùy | ✅ |
| Hardware | 8C/32GB/500GB | 4C/16GB/300GB |

---

## Bước 1 — Clone repo

```bash
git clone https://github.com/x3pi/metanode.git
cd metanode/deploy
```

---

## Bước 2 — Generate file cấu hình

Script `gen_validator_entry.py` tự động tạo ETH keypair và sinh file `synconly.env` cấu hình sẵn:

```bash
python3 gen_validator_entry.py \
  --hostname node-sync-1 \
  --node-type synconly \
  --ip YOUR_PUBLIC_IP
```

**Kết quả:** Thư mục `./node-sync-1_keys/` chứa:
- `synconly.env` — file cấu hình đã điền sẵn keys
- `setup_firewall.sh` — Script mở cổng firewall UFW tự động cho node

Hoặc nếu muốn cấu hình thủ công:

```bash
cp synconly.env.example synconly.env
nano synconly.env
```

---

## Bước 3 — Đặt genesis.json vào đúng vị trí

Bạn cần file `genesis.json` chính thức từ team (hoặc từ một validator). Đặt nó vào đúng vị trí mà script yêu cầu:

```bash
cp /path/to/genesis.json metanode/execution/cmd/simple_chain/genesis.json
```

:::important
`genesis.json` **phải** nằm tại `execution/cmd/simple_chain/genesis.json` trong repo. Script `install.sh` sẽ báo lỗi nếu không tìm thấy file này.
:::

---

## Bước 4 — Cấu hình Tường lửa (Firewall) ⚠️ Quan trọng

Để các node có thể kết nối với nhau qua mạng, bạn cần mở các cổng mạng tương ứng. Script `setup_firewall.sh` đã được tự động tạo sẵn và cấu hình chính xác các cổng riêng biệt cho node này.

**Chạy script để mở cổng qua UFW:**

```bash
**Chạy script để mở cổng qua UFW:**

```bash
cd metanode/deploy
sudo bash ./node-sync-1_keys/setup_firewall.sh
```

---

## Bước 5 — Khởi chạy Node (Install & Start)

Thay vì cài đặt thủ công, bạn có thể dùng công cụ tự động hóa `cluster/systemd-cluster.sh` để biên dịch binary, tạo cấu hình và khởi chạy các service dưới nền chỉ với 1 lệnh:

```bash
cd metanode/deploy
sudo bash cluster/systemd-cluster.sh setup --node 4 -y
```
*(Thay `4` bằng Node ID của bạn).*

Script này tự động làm mọi việc: tạo system user, build binary, cài cấu hình và khởi chạy cả 2 tiến trình Go (Execution) và Rust (Consensus). Sau khoảng **10–15 phút**, quá trình build sẽ hoàn tất.

**Những gì script thực hiện:**
1. Tạo system user `metanode`
2. Tạo cấu trúc thư mục tại `/opt/metanode/node-<node_id>/`
3. Build Go (`simple_chain`) và Rust (`metanode`) binary từ source
4. Copy configs và `genesis.json` vào `/opt/metanode/node-<node_id>/`
5. Cài đặt và enable 2 systemd services (VD: `metanode-execution-4`, `metanode-consensus-4`)
6. Khởi chạy cả 2 services

### Khởi chạy RPC Proxy (Tùy chọn cho MetaMask/dApp)

Mặc định, service trên chỉ khởi chạy core node. Nếu bạn muốn mở endpoint RPC tương thích EVM (cho MetaMask hoặc dApp kết nối), hãy chạy thêm lệnh cài đặt RPC Proxy sau:

```bash
# Khởi chạy RPC Proxy cho Node 4 (tự động đọc port từ file .env)
sudo bash cluster/install-rpc-systemd.sh --node 4
```

Lệnh này sẽ tự động build RPC client và tạo service `metanode-rpc-4` chạy ngầm.

**Lệnh quản lý RPC:**
```bash
# Xem trạng thái và log RPC
sudo systemctl status metanode-rpc-4
journalctl -u metanode-rpc-4 -f

# Dừng RPC (nếu muốn đóng endpoint)
sudo bash cluster/install-rpc-systemd.sh --stop --node 4
```

---

## Bước 6 — Kiểm tra node đang sync

```bash
# Xem trạng thái services (thay 4 bằng node_id của bạn)
sudo systemctl status metanode-execution-4
sudo systemctl status metanode-consensus-4

# Follow log execution (Go)
journalctl -u metanode-execution-4 -f

# Xem log từ file trực tiếp
tail -f /opt/metanode/node-4/logs/execution/execution.log

# Kiểm tra block height — phải tăng dần và bắt kịp network
# Node 4 mặc định sử dụng port 10750
curl -s -X POST http://localhost:10750 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Node đang sync tốt khi block height tăng dần và bắt kịp block hiện tại của network.

---

## Restore từ Snapshot (Sync Nhanh Hơn)

Thay vì sync từ block 0, bạn có thể tải snapshot từ một validator đang chạy tốt để tiết kiệm hàng giờ đồng hồ:

```bash
# 1. Liệt kê snapshots có sẵn từ validator nguồn (VD: Node 0 có snapshot server ở port 8600)
curl http://localhost:8600/api/snapshots

# 2. Chạy script khôi phục tự động cho systemd (tham số: <node_id> [tên_snapshot] [id_node_nguồn])
cd metanode/deploy
# Ví dụ: Khôi phục Node 4 lấy snapshot mới nhất từ Node 0
sudo bash cluster/restore_node_systemd.sh 4
```

Script sẽ tự động dừng services của Node 4, xóa data cũ, tải snapshot qua HTTP, xóa `rust_consensus` thừa, dọn dẹp khóa LOCK, cấp quyền cho user `metanode` và khởi động lại dịch vụ an toàn.

---

## Quản lý node

### Lệnh cơ bản (thay 4 bằng node_id của bạn)

```bash
# Dừng node (Rust trước, Go sau — quan trọng!)
sudo systemctl stop metanode-consensus-4
sudo systemctl stop metanode-execution-4

# Khởi động lại (Go trước, Rust sau)
sudo systemctl start metanode-execution-4
sudo systemctl start metanode-consensus-4

# Restart nhanh
sudo systemctl restart metanode-execution-4 metanode-consensus-4

# Xem trạng thái cả hai
sudo systemctl status metanode-execution-4 metanode-consensus-4
```

:::warning
Luôn dừng theo thứ tự: **Rust trước → Go sau**. Dừng Go trước khi Rust sẽ khiến Rust crash do mất kết nối Unix socket.
:::

### Xử lý lỗi restart loop (rate-limit)

Nếu service bị crash nhiều lần liên tiếp, systemd sẽ chặn tự động restart (báo lỗi `Start request repeated too quickly`). Để gỡ khóa:

```bash
sudo systemctl reset-failed metanode-execution-4
sudo systemctl reset-failed metanode-consensus-4
sudo systemctl start metanode-execution-4
sudo systemctl start metanode-consensus-4
```

### Cấu trúc thư mục sau cài đặt (tại `/opt/metanode/node-4/`)

```
/opt/metanode/node-4/
├── bin/
│   ├── simple_chain          # Go execution binary
│   ├── metanode              # Rust consensus binary
│   └── genesis.json          # Genesis file (copy)
├── config/
│   ├── execution.json        # Go config
│   ├── consensus.toml        # Rust config
│   └── genesis.json          # Genesis file (copy)
├── keys/
│   ├── protocol_key.json     # Chứa key rỗng cho sync-only
│   └── network_key.json      # Chứa key rỗng cho sync-only
├── data/
│   ├── execution/            # Go DB, snapshots, backup
│   └── consensus/            # Rust DAG storage
└── logs/
    ├── execution/            # Go logs
    └── consensus/            # Rust logs
```

