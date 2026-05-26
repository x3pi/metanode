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

## Bước 4 — Hoàn thiện file cấu hình `.env`

Mở file `node-sync-1_keys/synconly.env` và điền thêm các thông số còn thiếu:

```bash
nano node-sync-1_keys/synconly.env
```

**Các trường cần điền thêm:**

```bash
# Trỏ tới các validator để đồng bộ
PEER_RPC_ADDRESSES="\"VALIDATOR_0_IP:19200\", \"VALIDATOR_1_IP:19201\", \"VALIDATOR_2_IP:19202\""
```

**Bảng các biến quan trọng trong `.env`:**

| Biến | Ý nghĩa | Giá trị mặc định |
|------|---------|-----------------|
| `NODE_TYPE` | Loại node | `synconly` |
| `NODE_ID` | ID node — dùng số lớn hơn số validator (VD: genesis có 4 validators → dùng 5+) | `5` |
| `BLS_PRIVATE_KEY` | BLS key bất kỳ (không cần trong genesis) | tự động điền |
| `ETH_PRIVATE_KEY` | ETH private key bất kỳ | tự động điền |
| `ETH_ADDRESS` | ETH address tương ứng | tự động điền |
| `RPC_PORT` | JSON-RPC port cho DApp/wallet | `:8762` |
| `PEER_RPC_PORT` | Consensus sync port | `19205` |
| `PEER_RPC_ADDRESSES` | IP:port các validator để sync | **cần điền thủ công** |
| `SNAPSHOT_ENABLED` | Bật snapshot để node khác fast-sync từ bạn | `true` |
| `IS_EXPLORER` | Archive toàn bộ lịch sử | `true` |
| `EPOCHS_TO_KEEP` | `0` = lưu tất cả (archive mode) | `0` |

:::note
Sync-Only node **không cần keys thật**. `BLS_PRIVATE_KEY` và `ETH_PRIVATE_KEY` chỉ dùng để ký MetaTx outgoing, không ảnh hưởng đến consensus. Bạn có thể dùng keypair mới hoàn toàn (script sẽ tự generate).
:::

---

---

## Bước 4.5 — Setup BTRFS cho Snapshot ⚠️ Bắt buộc

Sync-Only node mặc định bật `SNAPSHOT_ENABLED=true` trong `.env`. Nếu file system không phải BTRFS/XFS, **node sẽ crash ngay khi khởi động** với lỗi:
```
CRITICAL: Reflink (btrfs/xfs) is required for snapshotting. Please disable snapshot_enabled or use a supported filesystem.
```

Snapshot cần **reflink** (Copy-on-Write) của BTRFS/XFS để sao chép dữ liệu tức thì, không cần thời gian chờ. Ext4 thông thường không hỗ trợ.

**Chạy script setup BTRFS một lần trước khi install:**

```bash
cd metanode/deploy
sudo bash setup-cluster-btrfs.sh
```

Script sẽ tự động:
- Thử tạo phân vùng 400GB chuẩn BTRFS từ LVM (`ubuntu-vg`) nếu có.
- Nếu không có LVM: tạo sparse file 400GB tại `/opt/metanode_cluster_btrfs.img` làm loop device.
- Format BTRFS và mount vào `/opt/metanode`.
- Thêm entry vào `/etc/fstab` để tự mount sau khi reboot.

**Hoặc tắt snapshot** nếu không cần (ví dụ: node chỉ dùng nội bộ, không cần cho node khác sync nhanh):
```bash
# Trong synconly.env:
SNAPSHOT_ENABLED=false
```

---

## Bước 4.6 — Cấu hình Tường lửa (Firewall) ⚠️ Quan trọng

Để các node có thể kết nối với nhau qua internet hoặc mạng nội bộ, bạn cần mở các cổng mạng tương ứng. Script `setup_firewall.sh` đã được tự động tạo sẵn trong thư mục keys và cấu hình chính xác các cổng riêng biệt cho node này.

**Chạy script để mở cổng qua UFW:**

```bash
cd metanode/deploy
sudo bash ./node-sync-1_keys/setup_firewall.sh
```

Script này tự động:
1. Đảm bảo luật SSH (cổng `22`) được bật để bạn không bị ngắt kết nối.
2. Mở cổng **Consensus P2P** (`CONSENSUS_PORT`, mặc định `9000 + node_id`).
3. Mở cổng **Execution P2P** (`P2P_PORT`, mặc định `6200 + node_id`).
4. Mở cổng **Peer RPC** (`PEER_RPC_PORT`, mặc định `19200 + node_id`) phục vụ sync & attestation.
5. Mở cổng **Snapshot Server** (`SNAPSHOT_SERVER_PORT`, mặc định `8600 + node_id`).
6. Mở cổng **Metrics** (`METRICS_PORT`, mặc định `9100 + node_id`).
7. Mở cổng **RPC Proxy (MetaMask)** (`8545 + node_id`) để các dApp bên ngoài kết nối.

---

## Bước 5 — Chạy Install Script

```bash
cd metanode/deploy
sudo bash install.sh --config node-sync-1_keys/synconly.env
```

Script tự động: build binary từ source, tạo cấu hình, cài systemd services và khởi động node. Quá trình mất khoảng **10–15 phút**.

**Những gì script thực hiện:**
1. Tạo system user `metanode`
2. Tạo cấu trúc thư mục tại `/opt/metanode/node-<node_id>/`
3. Build Go (`simple_chain`) và Rust (`metanode`) binary từ source
4. Copy configs và `genesis.json` vào `/opt/metanode/node-<node_id>/`
5. Cài đặt và enable 2 systemd services (VD: `metanode-execution-4`, `metanode-consensus-4`)
6. Khởi động cả 2 services

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
sudo bash restore_node_systemd.sh 4
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

