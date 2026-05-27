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

Script `gen_validator_entry.py` tự động sử dụng Rust `keytool` để tạo toàn bộ validator keys, ETH keypair, và sinh file cấu hình `.env` tự động:

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
| `authority_key.json` | BLS authority key (Go execution) | 🔴 Bí mật tuyệt đối |
| `protocol_key.json` | Ed25519 protocol key (Rust consensus) | 🔴 Bí mật tuyệt đối |
| `network_key.json` | Ed25519 network key (Rust P2P) | 🔴 Bí mật tuyệt đối |
| `eth_key.json` | secp256k1 ETH key (phục vụ nhận thưởng) | 🔴 Bí mật tuyệt đối |
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

## Bước 4 — Cấu hình Tường lửa (Firewall) ⚠️ Quan trọng

Để các node có thể kết nối với nhau qua mạng, bạn cần mở các cổng mạng tương ứng. Script `setup_firewall.sh` đã được tự động tạo sẵn và cấu hình chính xác các cổng riêng biệt cho node này.

**Chạy script để mở cổng qua UFW:**

```bash
cd metanode/deploy
sudo bash ./node-0_keys/setup_firewall.sh
```

---

## Bước 5 — Khởi chạy Node (Install & Start)

Thay vì cài đặt thủ công, bạn có thể dùng công cụ tự động hóa `cluster/systemd-cluster.sh` để biên dịch binary, tạo cấu hình và khởi chạy các service dưới nền chỉ với 1 lệnh:

```bash
cd metanode/deploy
sudo bash cluster/systemd-cluster.sh setup --node 0 -y
```

Script này tự động làm mọi việc: tạo system user, build binary, cài cấu hình và khởi chạy cả 2 tiến trình Go (Execution) và Rust (Consensus). Sau khoảng **10–15 phút**, quá trình build sẽ hoàn tất.

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

### Khởi chạy RPC Proxy (Tùy chọn cho MetaMask/dApp)

Mặc định, service trên chỉ khởi chạy core node. Nếu bạn muốn mở endpoint RPC tương thích EVM (cho MetaMask hoặc dApp kết nối), hãy chạy thêm lệnh cài đặt RPC Proxy sau:

```bash
# Khởi chạy RPC Proxy cho Node 0 (tự động đọc port từ file .env)
sudo bash install-rpc-systemd.sh --node 0
```

Lệnh này sẽ tự động build RPC client và tạo service `metanode-rpc-0` chạy ngầm.

**Lệnh quản lý RPC:**
```bash
# Xem trạng thái và log RPC
sudo systemctl status metanode-rpc-0
journalctl -u metanode-rpc-0 -f

# Dừng RPC (nếu muốn đóng endpoint)
sudo bash install-rpc-systemd.sh --stop --node 0
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

