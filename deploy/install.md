
# 🔧 Giải thích chi tiết `install.sh` và file cấu hình `.env`

> Tài liệu này dành cho developer/maintainer của dự án muốn hiểu **cơ chế bên trong** của script deployment. Người vận hành node (node operator) bình thường chỉ cần đọc tài liệu [Validator Setup](./validator-setup) hoặc [Sync-Only Setup](./synconly-setup).

---

## Tổng quan kiến trúc deployment

Luồng cài đặt được thiết kế theo mô hình giống Sui Network:

```
validator.env          install.sh            /opt/metanode/
┌────────────────┐    ┌──────────────┐      ┌───────────────────────────┐
│ NODE_TYPE      │    │ Step 1       │      │ bin/                      │
│ NODE_ID        │───►│ Create user  │─────►│   simple_chain  (Go)      │
│ BLS_PRIVATE_KEY│    │ & dirs       │      │   metanode      (Rust)    │
│ ETH_PRIVATE_KEY│    ├──────────────┤      ├───────────────────────────┤
│ PEER_RPC_...   │    │ Step 2       │      │ config/                   │
│ PORTS, ...     │    │ Clone & Build│      │   execution.json  (Go)    │
└────────────────┘    ├──────────────┤      │   consensus.toml  (Rust)  │
                      │ Step 3       │      │   genesis.json            │
                      │ Gen configs  │      ├───────────────────────────┤
                      ├──────────────┤      │ keys/                     │
                      │ Step 4       │      │   protocol_key.json       │
                      │ Install keys │      │   network_key.json        │
                      ├──────────────┤      ├───────────────────────────┤
                      │ Step 5       │      │ data/                     │
                      │ Create data  │      │   execution/db/           │
                      │ directories  │      │   execution/snapshots/    │
                      ├──────────────┤      │   consensus/              │
                      │ Step 6       │      ├───────────────────────────┤
                      │ Install 2    │      │ logs/                     │
                      │ systemd units│      │   execution/              │
                      ├──────────────┤      │   consensus/              │
                      │ Step 7       │      └───────────────────────────┘
                      │ Enable &     │
                      │ Start        │       systemd:
                      └──────────────┘       metanode-execution.service
                                             metanode-consensus.service
```

---

## Giải thích từng bước của `install.sh`

### Bước 1 — Tạo system user và thư mục

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin metanode
mkdir -p /opt/metanode/{bin,config,keys,data,logs}
chown -R metanode:metanode /opt/metanode
chmod 750 /opt/metanode/keys   # Chỉ owner đọc được keys
```

**Tại sao cần system user riêng?**  
Đây là best practice bảo mật: process chạy dưới user không có shell, không có home directory, không thể login. Kể cả nếu binary bị compromise, attacker cũng không thể escalate privilege.

**Cấu trúc thư mục:**

| Đường dẫn | Mục đích |
|-----------|---------|
| `/opt/metanode/bin/` | Chứa 2 binary: `simple_chain` (Go), `metanode` (Rust) |
| `/opt/metanode/config/` | File cấu hình sinh ra từ `.env` |
| `/opt/metanode/keys/` | Private keys (chmod 600, chỉ user `metanode` đọc được) |
| `/opt/metanode/data/execution/` | Database của Go execution layer (NOMT state backend) |
| `/opt/metanode/data/consensus/` | DAG storage của Rust consensus engine |
| `/opt/metanode/logs/` | Log files (thêm vào ngoài journald) |

---

### Bước 2 — Clone và Build từ source

```bash
git clone --branch main https://github.com/x3pi/metanode.git /opt/metanode/src

# Build Rust binary (~10 phút)
cd /opt/metanode/src/consensus/metanode
cargo build --release --bin metanode

# Build Go binary (~2 phút)
cd /opt/metanode/src/execution/cmd/simple_chain
CGO_ENABLED=1 go build -o simple_chain .
```

**Tại sao `CGO_ENABLED=1` cho Go?**  
Vì Go execution layer giao tiếp với Rust qua FFI (Foreign Function Interface). `CGO_ENABLED=1` cho phép Go biên dịch C bridge code cần thiết cho FFI.

**Tại sao không dùng pre-built binary?**  
Hiện tại chưa có release pipeline tự động publish binary. Ngoài ra, build từ source đảm bảo binary khớp chính xác với phiên bản source trên máy.

---

### Bước 3 — Sinh config files từ biến `.env`

Script tạo **2 file config** từ các biến trong `.env`:

#### `execution.json` (Go layer config)

| Field quan trọng | Nguồn | Mô tả |
|-----------------|-------|-------|
| `private_key` | `BLS_PRIVATE_KEY` | Khóa BLS private key dùng làm node network identity |
| `address` | `ETH_ADDRESS` | Ethereum address tương ứng của validator |
| `BLSPrivateKey` | `BLS_PRIVATE_KEY` | Khóa BLS private key dùng để ký Block (block signer) |
| `rpc_port` | `RPC_PORT` | JSON-RPC endpoint cho DApp |
| `peer_rpc_port` | `PEER_RPC_PORT` | Giao tiếp giữa các validator |
| `rust_*_socket_path` | `NODE_ID` | IPC sockets với Rust layer |
| `genesis_file_path` | Cố định | Trỏ vào `/opt/metanode/config/genesis.json` |
| `RootPath` | Cố định | `/opt/metanode/data/execution/db` |

#### `consensus.toml` (Rust layer config)

| Field quan trọng | Nguồn | Mô tả |
|-----------------|-------|-------|
| `node_id` | `NODE_ID` | Index trong genesis.validators[] |
| `network_address` | `CONSENSUS_PORT` | Cổng P2P của Rust |
| `executor_commit_enabled` | `NODE_TYPE` | `false` nếu sync-only |
| `peer_rpc_port` | `PEER_RPC_PORT` | Phải khớp với Go |
| `peer_rpc_addresses` | `PEER_RPC_ADDRESSES` | Danh sách validator khác |
| `protocol_key_path` | Cố định | `/opt/metanode/keys/protocol_key.json` |

---

### Bước 4 — Cài đặt Keys

**Validator node:**
```bash
cp /path/to/node_0_protocol_key.json /opt/metanode/keys/protocol_key.json
cp /path/to/node_0_network_key.json  /opt/metanode/keys/network_key.json
chmod 600 /opt/metanode/keys/*.json
chown metanode:metanode /opt/metanode/keys/*.json
```

**Sync-only node:**  
Không cần key thật. Script tạo file JSON rỗng `{}` để consensus.toml không bị lỗi khi parse.

:::important
Key files phải có `chmod 600`. Nếu user khác đọc được private key, toàn bộ stake và quyền validator bị compromise.
:::

---

### Bước 5 — Tạo cấu trúc data directories

```bash
mkdir -p /opt/metanode/data/execution/db/data/xapian_node
#                                      ↑ NOMT state backend yêu cầu cấu trúc này
mkdir -p /opt/metanode/data/execution/backup
mkdir -p /opt/metanode/data/execution/snapshots
mkdir -p /opt/metanode/data/consensus
```

`xapian_node` là thư mục database phụ dùng cho full-text search trong block explorer. Nó phải tồn tại trước khi node chạy, kể cả khi `is_explorer=false`.

---

### Bước 6 — Cài đặt 2 systemd service units

#### `metanode-execution.service` (Go)

```ini
[Unit]
After=network-online.target
Before=metanode-consensus.service   # Go phải start TRƯỚC Rust

[Service]
User=metanode
ExecStart=/opt/metanode/bin/simple_chain -config=/opt/metanode/config/execution.json
TimeoutStopSec=90  # ← BẮT BUỘC để DB flush đúng khi shutdown
```

**`TimeoutStopSec=90` — Tại sao bắt buộc?**  
Khi nhận SIGTERM, Go layer cần ~90s để:
1. Flush pending blocks vào NOMT state database
2. Ghi `last_block.dat` (dùng cho crash recovery)
3. Đóng tất cả BoltDB/LevelDB files sạch sẽ

Nếu systemd kill quá sớm (mặc định 90s), database có thể corrupt và phải restore từ snapshot.

#### `metanode-consensus.service` (Rust)

```ini
[Unit]
Requires=metanode-execution.service   # Rust PHỤ THUỘC vào Go
After=metanode-execution.service       # Rust khởi động SAU Go
```

**Tại sao Rust phụ thuộc vào Go?**  
Rust consensus engine kết nối vào Go qua Unix Domain Socket (`/tmp/rust-go-nodeX-master.sock`). Socket này chỉ tồn tại sau khi Go đã khởi động và sẵn sàng. Nếu Rust start trước → không có socket → crash.

---

### Bước 7 — Enable và Start services

```bash
systemctl daemon-reload
systemctl enable metanode-execution metanode-consensus  # Auto-start on boot
systemctl start metanode-execution   # Start Go trước
sleep 5                               # Chờ Go tạo socket
systemctl start metanode-consensus   # Start Rust sau
```

---

## Giải thích chi tiết các biến trong `.env`

### Biến bắt buộc

| Biến | Ví dụ | Mô tả |
|------|-------|-------|
| `NODE_TYPE` | `validator` | `validator` hoặc `synconly` |
| `NODE_ID` | `0` | Index trong `genesis.json` validators array |
| `BLS_PRIVATE_KEY` | `5dd973...` | Khóa BLS private key (không có `0x`) - Dùng cho cả định danh P2P và ký block |
| `ETH_PRIVATE_KEY` | `b252eb...` | Ethereum private key (không có `0x`) - Dùng để chạy mining/giao dịch thưởng |
| `ETH_ADDRESS` | `1ea550...` | Địa chỉ Ethereum của validator (không có `0x`) |
| `GENESIS_FILE` | `/path/to/genesis.json` | Genesis.json nhận từ team |
| `PEER_RPC_ADDRESSES` | `"IP1:19201", "IP2:19202"` | Tất cả validator KHÁC (không ghi IP của mình) |

### Biến cổng mạng

Khi chạy **nhiều node trên cùng 1 server**, mỗi node phải có bộ cổng không trùng nhau:

| Biến | Node 0 | Node 1 | Node 2 | Mô tả |
|------|--------|--------|--------|-------|
| `RPC_PORT` | `:8757` | `:10747` | `:10749` | JSON-RPC (Ethereum compat.) |
| `P2P_PORT` | `4000` | `6201` | `6202` | Go P2P sync |
| `PEER_RPC_PORT` | `19200` | `19201` | `19202` | Consensus communication |
| `CONSENSUS_PORT` | `9000` | `9001` | `9002` | Rust P2P |
| `SNAPSHOT_SERVER_PORT` | `8600` | `8601` | `8602` | Snapshot HTTP |

Khi mỗi node **chạy trên server riêng**, tất cả cổng có thể giữ nguyên giá trị mặc định.

---

## Cấu trúc thư mục sau khi cài đặt

```
/opt/metanode/
├── bin/
│   ├── simple_chain          # Go execution binary
│   └── metanode              # Rust consensus binary
├── config/
│   ├── execution.json        # Go config (sinh từ .env)
│   ├── consensus.toml        # Rust config (sinh từ .env)
│   └── genesis.json          # Copy từ đường dẫn chỉ định trong .env
├── keys/
│   ├── protocol_key.json     # BLS protocol key (chmod 600)
│   └── network_key.json      # Ed25519 network key (chmod 600)
├── data/
│   ├── execution/
│   │   ├── db/               # NOMT state + BoltDB (block data)
│   │   ├── snapshots/        # Snapshot archives
│   │   ├── backup/           # Epoch backup data
│   │   └── explorer/         # Block explorer DB
│   └── consensus/            # Narwhal/Bullshark DAG storage
├── logs/
│   ├── execution/            # Go stderr log files
│   └── consensus/            # Rust stderr log files
└── src/                      # Source code (clone từ GitHub)
    └── ...

---

## 🎮 Các lệnh vận hành hệ thống

Dưới đây là các lệnh cần thiết để quản trị, kiểm tra và khắc phục sự cố dịch vụ:

### 1. Quản lý dịch vụ (Systemd)
```bash
# Khởi động dịch vụ
sudo systemctl start metanode-execution metanode-consensus

# Dừng dịch vụ
sudo systemctl stop metanode-consensus metanode-execution

# Khởi động lại dịch vụ
sudo systemctl restart metanode-execution metanode-consensus

# Kiểm tra trạng thái hoạt động của cả Go và Rust
sudo systemctl status metanode-execution metanode-consensus
```

### 2. Gỡ bỏ rate-limit của Systemd (Khi service crash nhiều lần)
Nếu dịch vụ bị crash liên tục do sai cấu hình, systemd sẽ tạm thời khóa dịch vụ và báo lỗi `Start request repeated too quickly`. Để mở khóa và chạy lại:
```bash
sudo systemctl reset-failed metanode-execution
sudo systemctl start metanode-execution metanode-consensus
```

### 3. Xem logs hoạt động
* **Xem từ file log trực tiếp (Go Layer):**
  ```bash
  tail -f /opt/metanode/logs/execution/go-master/App.log
  ```
* **Xem qua journalctl (cả Go và Rust):**
  ```bash
  # Log của Go execution layer
  journalctl -u metanode-execution -f

  # Log của Rust consensus layer
  journalctl -u metanode-consensus -f
  ```
```
