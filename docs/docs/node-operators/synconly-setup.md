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
cd metanode/execution/cmd/simple_chain
sudo bash migrate-to-btrfs-lvm.sh
```

Script sẽ tự động:
- Thử tạo LV 400GB từ LVM (`ubuntu-vg`) nếu có
- Nếu không có LVM: tạo sparse file 400GB làm loop device
- Format BTRFS và mount vào `./sample/`
- Thêm entry vào `/etc/fstab` để tự mount sau reboot

**Hoặc tắt snapshot** nếu không cần (ví dụ: node chỉ dùng nội bộ, không cần cho node khác sync nhanh):
```bash
# Trong synconly.env:
SNAPSHOT_ENABLED=false
```

---

## Bước 5 — Chạy Install Script

```bash
cd metanode/deploy
sudo bash install.sh --config node-sync-1_keys/synconly.env
```

Script tự động: build binary từ source, tạo cấu hình, cài systemd services và khởi động node. Quá trình mất khoảng **10–15 phút**.

**Những gì script thực hiện:**
1. Tạo system user `metanode`
2. Tạo cấu trúc thư mục tại `/opt/metanode/`
3. Build Go (`simple_chain`) và Rust (`metanode`) binary từ source
4. Copy configs và `genesis.json` vào `/opt/metanode/`
5. Cài đặt và enable 2 systemd services
6. Khởi động cả 2 services

---

## Bước 6 — Kiểm tra node đang sync

```bash
# Xem trạng thái services
sudo systemctl status metanode-execution
sudo systemctl status metanode-consensus

# Follow log execution (Go)
journalctl -u metanode-execution -f

# Xem log từ file trực tiếp
tail -f /opt/metanode/logs/execution/go-master/App.log

# Kiểm tra block height — phải tăng dần và bắt kịp network
# Sync-only dùng port 8762 theo mặc định
curl -s -X POST http://localhost:8762 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Node đang sync tốt khi block height tăng dần và bắt kịp block hiện tại của network.

---

## Restore từ Snapshot (Sync Nhanh Hơn)

Thay vì sync từ block 0, bạn có thể tải snapshot từ một validator để tiết kiệm hàng giờ đồng hồ:

```bash
# Liệt kê snapshots có sẵn từ một validator
curl http://VALIDATOR_IP:8600/api/snapshots

# Restore (thay VALIDATOR_IP và chọn snapshot ID từ lệnh trên)
cd metanode/consensus/metanode/scripts/node
./restore_node.sh 5   # 5 = NODE_ID của sync-only node
```

Sau khi restore, khởi động lại services:
```bash
sudo systemctl start metanode-execution
sudo systemctl start metanode-consensus
```

---

## Quản lý node

### Lệnh cơ bản

```bash
# Dừng node (Rust trước, Go sau — quan trọng!)
sudo systemctl stop metanode-consensus
sudo systemctl stop metanode-execution

# Khởi động lại (Go trước, Rust sau)
sudo systemctl start metanode-execution
sudo systemctl start metanode-consensus

# Restart nhanh
sudo systemctl restart metanode-execution metanode-consensus

# Xem trạng thái cả hai
sudo systemctl status metanode-execution metanode-consensus
```

:::warning
Luôn dừng theo thứ tự: **Rust trước → Go sau**. Dừng Go trước khi Rust sẽ khiến Rust crash do mất kết nối Unix socket.
:::

### Xử lý lỗi restart loop (rate-limit)

Nếu service bị crash nhiều lần liên tiếp, systemd sẽ chặn tự động restart (báo lỗi `Start request repeated too quickly`). Để gỡ khóa:

```bash
sudo systemctl reset-failed metanode-execution
sudo systemctl reset-failed metanode-consensus
sudo systemctl start metanode-execution
sudo systemctl start metanode-consensus
```

### Cấu trúc thư mục sau cài đặt

```
/opt/metanode/
├── bin/
│   ├── simple_chain          # Go execution binary
│   ├── metanode              # Rust consensus binary
│   └── genesis.json          # Genesis file (copy)
├── config/
│   ├── execution.json        # Go config
│   ├── consensus.toml        # Rust config
│   └── genesis.json          # Genesis file (copy)
├── keys/
│   ├── protocol_key.json     # {} (empty — sync-only không cần)
│   └── network_key.json      # {} (empty — sync-only không cần)
├── data/
│   ├── execution/            # Go DB, snapshots, backup
│   └── consensus/            # Rust DAG storage
└── logs/
    └── execution/go-master/  # Go application logs
```
