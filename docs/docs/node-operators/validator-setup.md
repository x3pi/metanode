---
sidebar_position: 2
title: Chạy Validator Node
---

# Chạy Validator Node

Validator tham gia đồng thuận, ký block, và nhận phần thưởng. Cần được đăng ký trong `genesis.json` trước khi network khởi động.

---

## Yêu cầu

**Hardware tối thiểu:**
- CPU: 8 cores
- RAM: 32 GB
- Disk: 500 GB NVMe SSD
- Network: 1 Gbps, IP tĩnh

**Software yêu cầu:**
- Hệ điều hành: Ubuntu 22.04 / 24.04 LTS
- Toolchain: `git`, `curl`, `build-essential`, `pkg-config`, `libssl-dev`, `python3`
- Ngôn ngữ lập trình: **Rust** (stable) và **Go 1.23.5+**
- Tiện ích hệ thống: `chrony` (đồng bộ thời gian)

:::important
**Đồng bộ thời gian là bắt buộc.** Nếu đồng hồ hệ thống lệch > 1s, quá trình consensus sẽ bị treo. Hãy đảm bảo `chronyd` đang chạy và `System time offset` < 0.1s.
:::

---

## Bước 1 — Clone repo & Build CLI

```bash
git clone https://github.com/x3pi/metanode.git
cd metanode/consensus/metanode

# Build metanode CLI (dùng để generate keys)
cargo build --release --bin metanode
```

---

## Bước 2 — Generate Keys & File cấu hình

Script `gen_validator_entry.py` tự động tạo toàn bộ BLS keys, ETH keypair, và sinh file `.env` cấu hình sẵn:

```bash
cd metanode/deploy

python3 gen_validator_entry.py \
  --hostname node-0 \
  --node-type validator \
  --ip YOUR_PUBLIC_IP \
  --node-id 0
```

Thay `YOUR_PUBLIC_IP` bằng địa chỉ IP public của server và `0` bằng index của bạn trong genesis.

**Kết quả:** Thư mục `./node-0_keys/` chứa:

| File | Mô tả | Bảo mật |
|------|--------|---------|
| `node-0_protocol_key.json` | BLS protocol key (Rust consensus) | 🔴 Bí mật tuyệt đối |
| `node-0_network_key.json` | Ed25519 network key (Rust P2P) | 🔴 Bí mật tuyệt đối |
| `node-0_authority_key.json` | BLS authority key (Go execution) | 🔴 Bí mật tuyệt đối |
| `setup_firewall.sh` | Script mở cổng firewall UFW tự động cho node | ✅ Công khai |
| `validator.env` | File cấu hình đã điền sẵn keys | 🔴 Không commit lên Git |
| `node-0_genesis.json` | Thông tin đăng ký gửi genesis coordinator | ✅ Gửi cho team |

:::caution
Backup toàn bộ thư mục keys ngay lập tức. Mất keys = mất quyền validator.
```bash
cp -r ./node-0_keys ~/metanode_keys_backup_$(date +%Y%m%d)
chmod 700 ~/metanode_keys_backup_*/
chmod 600 ~/metanode_keys_backup_*/*.json
```
:::

---

## Bước 3 — Đăng ký vào Genesis

Gửi file `node-0_genesis.json` cho genesis coordinator để được đưa vào `genesis.json` chính thức.

Sau khi genesis ceremony hoàn tất, bạn sẽ nhận lại file `genesis.json`. **Lưu file này vào cùng thư mục với `execution/cmd/simple_chain/`** — đây là vị trí bắt buộc mà script sẽ tìm:

```bash
cp genesis.json metanode/execution/cmd/simple_chain/genesis.json
```

:::important
`genesis.json` **phải** nằm tại `execution/cmd/simple_chain/genesis.json` trong repo. Script `install.sh` sẽ báo lỗi nếu không tìm thấy file này.
:::

---

## Bước 4 — Hoàn thiện file cấu hình `.env`

Mở file `node-0_keys/validator.env` vừa được tạo và điền thêm 2 thông số còn thiếu:

```bash
nano node-0_keys/validator.env
```

**Các trường cần điền thêm:**

```bash
# Danh sách IP:port của TẤT CẢ validator KHÁC (không ghi IP của mình)
PEER_RPC_ADDRESSES="\"VALIDATOR_1_IP:19201\", \"VALIDATOR_2_IP:19202\", \"VALIDATOR_3_IP:19203\""
```

:::note
Tất cả các trường khác (keys, ports) đã được `gen_validator_entry.py` điền sẵn. Chỉ cần bổ sung `PEER_RPC_ADDRESSES`.
:::

**Bảng các biến quan trọng trong `.env`:**

| Biến | Ý nghĩa | Ví dụ |
|------|---------|-------|
| `NODE_TYPE` | Loại node | `validator` |
| `NODE_ID` | Index trong genesis.json (0-based) | `0` |
| `PROTOCOL_KEY_FILE` | Đường dẫn tới `node_0_protocol_key.json` | tự động điền |
| `NETWORK_KEY_FILE` | Đường dẫn tới `node_0_network_key.json` | tự động điền |
| `BLS_PRIVATE_KEY` | Nội dung hex từ `node_0_authority_key.json` | tự động điền |
| `ETH_PRIVATE_KEY` | Ethereum private key (hex, không có 0x) | tự động điền |
| `ETH_ADDRESS` | Ethereum address (hex, không có 0x) | tự động điền |
| `PEER_RPC_ADDRESSES` | IP:port của các validator khác | **cần điền thủ công** |
| `RPC_PORT` | JSON-RPC port (DApp/wallet) | `:10746` |
| `PEER_RPC_PORT` | Consensus port giữa các validators | `19200` |

---

## Bước 4.5 — Cấu hình Tường lửa (Firewall) ⚠️ Quan trọng

Để các node có thể kết nối với nhau qua internet hoặc mạng nội bộ, bạn cần mở các cổng mạng tương ứng. Script `setup_firewall.sh` đã được tự động tạo sẵn trong thư mục keys và cấu hình chính xác các cổng riêng biệt cho node này.

**Chạy script để mở cổng qua UFW:**

```bash
cd metanode/deploy
sudo bash ./node-0_keys/setup_firewall.sh
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

Script này tự động làm mọi việc: tạo system user, build binary, cài cấu hình, cài systemd services và khởi động node.

```bash
cd metanode/deploy
sudo bash install.sh --config node-0_keys/validator.env
```

Script sẽ hỏi xác nhận trước khi bắt đầu. Sau khi xác nhận, quá trình build mất khoảng **10–15 phút** (chủ yếu là build Rust).

**Những gì script thực hiện:**
1. Tạo system user `metanode` (nếu chưa có)
2. Tạo cấu trúc thư mục tại `/opt/metanode/node-0/`
3. Build Go binary (`simple_chain`) và Rust binary (`metanode`) từ source
4. Copy configs, keys, và `genesis.json` vào `/opt/metanode/node-0/`
5. Cài đặt và enable 2 systemd services (có đính kèm ID node ở đuôi service)
6. Khởi động cả 2 services

Kết thúc thành công sẽ hiển thị:
```
══════════════════════════════════════
  Installation complete!
  Node ID: 0
  Services installed:
  - metanode-execution-0.service
  - metanode-consensus-0.service
══════════════════════════════════════
```

---

## Bước 6 — Kiểm tra node

```bash
# Xem trạng thái services (thay 0 bằng node_id của bạn)
sudo systemctl status metanode-execution-0
sudo systemctl status metanode-consensus-0

# Follow log execution (Go) — real-time
journalctl -u metanode-execution-0 -f

# Follow log consensus (Rust) — lọc các sự kiện quan trọng
journalctl -u metanode-consensus-0 -f | grep -i "commit\|epoch\|peer\|block"

# Xem log trực tiếp từ file (Go execution layer)
tail -f /opt/metanode/node-0/logs/execution/execution.log

# Kiểm tra block height (phải tăng dần)
curl -s -X POST http://localhost:10746 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Dấu hiệu node hoạt động tốt:
- Block height tăng đều
- Log consensus có `Committed block #XXXX`
- Kết nối đủ `N-1` peers (N = tổng số validator)

---

## Quản lý node

### Lệnh cơ bản (thay 0 bằng node_id của bạn)

```bash
# Dừng node (Rust trước, Go sau — quan trọng!)
sudo systemctl stop metanode-consensus-0
sudo systemctl stop metanode-execution-0

# Khởi động lại (Go trước, Rust sau)
sudo systemctl start metanode-execution-0
sudo systemctl start metanode-consensus-0

# Restart nhanh
sudo systemctl restart metanode-execution-0 metanode-consensus-0

# Xem trạng thái cả hai
sudo systemctl status metanode-execution-0 metanode-consensus-0
```

:::warning
Luôn dừng theo thứ tự: **Rust trước → Go sau**. Dừng Go trước khi Rust sẽ khiến Rust crash do mất kết nối Unix socket.
:::

### Xử lý lỗi restart loop (rate-limit)

Nếu service bị crash nhiều lần liên tiếp, systemd sẽ chặn tự động restart (báo lỗi `Start request repeated too quickly`). Để gỡ khóa:

```bash
sudo systemctl reset-failed metanode-execution-0
sudo systemctl reset-failed metanode-consensus-0
sudo systemctl start metanode-execution-0
sudo systemctl start metanode-consensus-0
```

### Cập nhật phiên bản mới

```bash
cd /home/$USER/metanode   # hoặc thư mục repo gốc
git pull
sudo bash deploy/install.sh --config /path/to/node-0_keys/validator.env
```

### Cấu trúc thư mục sau cài đặt (tại `/opt/metanode/node-0`)

```
/opt/metanode/node-0/
├── bin/
│   ├── simple_chain          # Go execution binary
│   ├── metanode              # Rust consensus binary
│   └── genesis.json          # Genesis file (copy)
├── config/
│   ├── execution.json        # Go config
│   ├── consensus.toml        # Rust config
│   └── genesis.json          # Genesis file (copy)
├── keys/
│   ├── protocol_key.json     # BLS protocol key
│   └── network_key.json      # Ed25519 network key
├── data/
│   ├── execution/            # Go DB, snapshots, backup
│   └── consensus/            # Rust DAG storage
└── logs/
    ├── execution/            # Go logs
    └── consensus/            # Rust logs
```

