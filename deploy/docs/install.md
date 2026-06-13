
# 🔧 Giải thích chi tiết `install.sh` và file cấu hình JSON/TOML

> Tài liệu này dành cho developer/maintainer của dự án muốn hiểu **cơ chế bên trong** của script deployment. Người vận hành node (node operator) bình thường chỉ cần đọc tài liệu [Validator Setup](./validator-setup) hoặc [Sync-Only Setup](./synconly-setup).

---

## Tổng quan kiến trúc deployment

Luồng cài đặt được thiết kế theo mô hình giống Sui Network:

```
node-N_keys/            install.sh            /opt/metanode/
┌────────────────┐    ┌──────────────┐      ┌───────────────────────────┐
│ execution.json │    │ Step 1       │      │ bin/                      │
│ consensus.toml │───►│ Create user  │─────►│   simple_chain  (Go)      │
│ network_key    │    │ & dirs       │      │   metanode      (Rust)    │
│ protocol_key   │    ├──────────────┤      ├───────────────────────────┤
│                │    │ Step 2       │      │ config/                   │
└────────────────┘    │ Clone & Build│      │   execution.json  (Go)    │
                      ├──────────────┤      │   consensus.toml  (Rust)  │
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
# Thư mục cài đặt mặc định là /opt/metanode-${NODE_ID} để hỗ trợ chạy nhiều node trên cùng server
mkdir -p /opt/metanode-${NODE_ID}/{bin,config,keys,data,logs}
chown -R metanode:metanode /opt/metanode-${NODE_ID}
chmod 750 /opt/metanode-${NODE_ID}/keys   # Chỉ owner đọc được keys
```

**Tại sao cần system user riêng?**  
Đây là best practice bảo mật: process chạy dưới user không có shell, không có home directory, không thể login. Kể cả nếu binary bị compromise, attacker cũng không thể escalate privilege.

**Cấu trúc thư mục:**

| Đường dẫn | Mục đích |
|-----------|---------|
| `/opt/metanode-${NODE_ID}/bin/` | Chứa 2 binary: `simple_chain` (Go), `metanode` (Rust) |
| `/opt/metanode-${NODE_ID}/config/` | File cấu hình `execution.json` và `consensus.toml` |
| `/opt/metanode-${NODE_ID}/keys/` | Private keys (chmod 600, chỉ user `metanode` đọc được) |
| `/opt/metanode-${NODE_ID}/data/execution/` | Database của Go execution layer (NOMT state backend) |
| `/opt/metanode-${NODE_ID}/data/consensus/` | DAG storage của Rust consensus engine |
| `/opt/metanode-${NODE_ID}/logs/` | Log files (thêm vào ngoài journald) |

---

### Bước 2 — Clone và Build từ source

```bash
git clone --branch main https://github.com/x3pi/metanode.git /opt/metanode-${NODE_ID}/src

# Build Rust binary (~10 phút)
cd /opt/metanode-${NODE_ID}/src/consensus/metanode
cargo build --release --bin metanode

# Build Go binary (~2 phút)
cd /opt/metanode-${NODE_ID}/src/execution/cmd/simple_chain
CGO_ENABLED=1 go build -o simple_chain .
```

**Tại sao `CGO_ENABLED=1` cho Go?**  
Vì Go execution layer giao tiếp với Rust qua FFI (Foreign Function Interface). `CGO_ENABLED=1` cho phép Go biên dịch C bridge code cần thiết cho FFI.

**Tại sao không dùng pre-built binary?**  
Hiện tại chưa có release pipeline tự động publish binary. Ngoài ra, build từ source đảm bảo binary khớp chính xác với phiên bản source trên máy.

---

### Bước 3 — Copy config files và keys

Script copy **2 file config** từ thư mục cấu hình truyền vào (`--config-dir`):

#### `execution.json` (Go layer config)

| `protocol_key_path` | Cố định | `/opt/metanode-${NODE_ID}/keys/protocol_key.json` |

---

### Bước 4 — Cài đặt Keys

**Validator node:**
```bash
cp /path/to/protocol_key.json /opt/metanode-${NODE_ID}/keys/protocol_key.json
cp /path/to/network_key.json  /opt/metanode-${NODE_ID}/keys/network_key.json
chmod 600 /opt/metanode-${NODE_ID}/keys/*.json
chown metanode:metanode /opt/metanode-${NODE_ID}/keys/*.json
```

**Sync-only node:**  
Không cần key thật. Script tạo file JSON rỗng `{}` để consensus.toml không bị lỗi khi parse.

:::important
Key files phải có `chmod 600`. Nếu user khác đọc được private key, toàn bộ stake và quyền validator bị compromise.
:::

---

### Bước 5 — Tạo cấu trúc data directories

```bash
mkdir -p /opt/metanode-${NODE_ID}/data/execution/db/data/xapian_node
#                                      ↑ NOMT state backend yêu cầu cấu trúc này
mkdir -p /opt/metanode-${NODE_ID}/data/execution/backup
mkdir -p /opt/metanode-${NODE_ID}/data/execution/snapshots
mkdir -p /opt/metanode-${NODE_ID}/data/consensus
```

`xapian_node` là thư mục database phụ dùng cho full-text search trong block explorer. Nó phải tồn tại trước khi node chạy, kể cả khi `is_explorer=false`.

---

### Bước 6 — Cài đặt 2 systemd service units (Giải thích chi tiết cấu hình)

Khi chạy node, hệ thống không chạy binary trực tiếp mà chạy dưới nền thông qua **systemd**. Cấu hình này giúp tự động khởi động lại khi crash, quản lý log và kiểm soát tài nguyên.

#### 1. `metanode-execution.service` (Go Layer)

```ini
[Unit]
Description=Metanode Execution Layer (Go) — Node {NODE_ID}
After=network-online.target
Wants=network-online.target
Before=metanode-consensus.service
StartLimitIntervalSec=600
StartLimitBurst=3

[Service]
User=metanode
WorkingDirectory=/opt/metanode-${NODE_ID}/bin
ExecStart=/opt/metanode-${NODE_ID}/bin/simple_chain -config=/opt/metanode-${NODE_ID}/config/execution.json
ExecStop=/bin/kill -SIGTERM $MAINPID
TimeoutStopSec=90
Restart=on-failure
RestartSec=15s
Environment=GOTRACEBACK=all
Environment=GOMEMLIMIT=4GiB
LimitNOFILE=100000
StandardOutput=append:/opt/metanode-${NODE_ID}/logs/execution/execution.log
StandardError=append:/opt/metanode-${NODE_ID}/logs/execution/execution.log
```

**Phân tích chi tiết cấu hình Execution:**
- **`Before=metanode-consensus.service`**: Systemd đảm bảo rằng dịch vụ Go sẽ được khởi động **trước** dịch vụ Rust. Điều này là bắt buộc vì Rust cần kết nối vào Unix Socket do Go tạo ra.
- **`StartLimitBurst=3` / `StartLimitIntervalSec=600`**: Giới hạn khởi động lại (Rate-limit). Nếu tiến trình crash quá 3 lần trong vòng 10 phút (600s), systemd sẽ khóa không cho tự động chạy lại nữa để tránh boot-loop. (Để gỡ khóa dùng lệnh `systemctl reset-failed`).
- **`ExecStop=/bin/kill -SIGTERM $MAINPID` & `TimeoutStopSec=90`**: Khi dừng dịch vụ, gửi tín hiệu `SIGTERM` một cách mềm mại (graceful shutdown) và chờ tối đa 90 giây. **Đây là cấu hình quan trọng nhất (CRITICAL)**. Go layer cần thời gian để flush pending blocks vào NOMT state database và đóng tất cả tệp BoltDB/LevelDB sạch sẽ. Nếu systemd ép đóng (SIGKILL) quá sớm, database sẽ bị corrupt.
- **`Restart=on-failure` & `RestartSec=15s`**: Nếu process chết bất thường, tự động khởi động lại sau 15 giây.
- **`Environment=GOMEMLIMIT=4GiB`**: Giới hạn soft-limit cho Garbage Collector của Go, giúp Go xả bộ nhớ hiệu quả hơn khi đạt ngưỡng 4GB RAM, tránh bị OOM (Out Of Memory) trên các máy cấu hình thấp.
- **`LimitNOFILE=100000`**: Tăng giới hạn số lượng file mở đồng thời (file descriptors). Hệ thống blockchain cần mở hàng nghìn connection P2P và các file cơ sở dữ liệu. Giới hạn mặc định (1024) của Linux là quá nhỏ và sẽ gây lỗi `too many open files`.
- **`StandardOutput / StandardError`**: Chuyển hướng toàn bộ console logs ghi đè (append) vào file `execution.log` để dễ dàng tracking, thay vì chỉ lưu trong memory buffer của journald.

#### 2. `metanode-consensus.service` (Rust Layer)

```ini
[Unit]
Description=Metanode Consensus Engine (Rust) — Node {NODE_ID}
After=network-online.target metanode-execution.service
Wants=network-online.target
Requires=metanode-execution.service
...
[Service]
ExecStart=/opt/metanode-${NODE_ID}/bin/metanode start --config /opt/metanode-${NODE_ID}/config/consensus.toml
TimeoutStopSec=60
Restart=on-failure
RestartSec=10s
Environment=RUST_BACKTRACE=full
LimitNOFILE=100000
...
```

**Phân tích chi tiết cấu hình Consensus:**
- **`Requires=metanode-execution-${NODE_ID}.service` & `After=metanode-execution-${NODE_ID}.service`**: Ràng buộc cứng (Hard dependency). Rust service chỉ được khởi động SAU KHI Go service đã khởi động. Nếu Go service bị crash hoặc bị dừng bằng tay, systemd sẽ **tự động dừng** theo dịch vụ Rust. Tại sao? Vì Rust kết nối qua Unix Domain Socket của Go, không có Go thì Rust sẽ crash do mất liên lạc.
- **`Environment=RUST_BACKTRACE=full`**: Nếu Rust gặp lỗi nghiêm trọng (panic), nó sẽ in ra toàn bộ lịch sử call stack (backtrace) vào file log để các lập trình viên dễ dàng debug.
- **`TimeoutStopSec=60`**: Tương tự Go, Rust cũng có 60 giây để ghi nhận trạng thái DAG/Committee và đóng cơ sở dữ liệu RocksDB an toàn.

---

### Bước 7 — Enable và Start services

```bash
systemctl daemon-reload
systemctl enable metanode-execution-${NODE_ID} metanode-consensus-${NODE_ID}  # Auto-start on boot
systemctl start metanode-execution-${NODE_ID}   # Start Go trước
sleep 5                                         # Chờ Go tạo socket
systemctl start metanode-consensus-${NODE_ID}   # Start Rust sau
```

---

### Khởi chạy nhiều node trên cùng server

Khi chạy **nhiều node trên cùng 1 server**, mỗi node phải cấu hình một bộ cổng không trùng nhau trong file `execution.json` và `consensus.toml`:

| Loại Cổng | Node 0 | Node 1 | Node 2 | Mô tả |
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
/opt/metanode-${NODE_ID}/
├── bin/
│   ├── simple_chain          # Go execution binary
│   └── metanode              # Rust consensus binary
├── config/
│   ├── execution.json        # Go config
│   ├── consensus.toml        # Rust config
│   └── genesis.json          # Copy từ config dir
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
