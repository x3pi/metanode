# 🧪 Hướng dẫn Chạy Cụm 5 Node trên 1 Máy (Systemd)

Tất cả 5 node chạy trên cùng 1 máy qua **systemd**, quản lý bởi script [`systemd-cluster.sh`](./systemd-cluster.sh).

| Node | Loại | Service execution | Service consensus |
|------|------|-------------------|-------------------|
| **Node 0** | Validator | `metanode-execution-0` | `metanode-consensus-0` |
| **Node 1** | Validator | `metanode-execution-1` | `metanode-consensus-1` |
| **Node 2** | Validator | `metanode-execution-2` | `metanode-consensus-2` |
| **Node 3** | Validator | `metanode-execution-3` | `metanode-consensus-3` |
| **Node 4** | Sync-Only | `metanode-execution-4` | `metanode-consensus-4` |

Mỗi node có thư mục riêng: `/opt/metanode-0/`, `/opt/metanode-1/`, ..., `/opt/metanode-4/`

---

## Bước 1 — Chuẩn bị keys & config

Tạo keys và file `.env` cho từng node:

```bash
cd metanode/deploy

# 4 Validators (node 0-3)
python3 gen_validator_entry.py --hostname node-0 --node-type validator --ip 127.0.0.1 --node-id 0
python3 gen_validator_entry.py --hostname node-1 --node-type validator --ip 127.0.0.1 --node-id 1
python3 gen_validator_entry.py --hostname node-2 --node-type validator --ip 127.0.0.1 --node-id 2
python3 gen_validator_entry.py --hostname node-3 --node-type validator --ip 127.0.0.1 --node-id 3

# 1 Sync-Only (node 4)
python3 gen_validator_entry.py --hostname node-4 --node-type synconly --ip 127.0.0.1
```

> ⚠️ Các node chạy cùng máy **phải dùng port khác nhau**. Script `gen_validator_entry.py` tự động gán port theo NODE_ID.

Đặt `genesis.json` vào đúng vị trí:
```bash
cp genesis.json metanode/execution/cmd/simple_chain/genesis.json
```

---

## Bước 2 — (Node 4) Setup BTRFS cho Snapshot

Node 4 là sync-only với `SNAPSHOT_ENABLED=true`. Cần BTRFS trước khi cài:

```bash
cd metanode/execution/cmd/simple_chain
sudo bash migrate-to-btrfs-lvm.sh
```

---

## Bước 3 — Cài đặt lần đầu (1 lệnh)

```bash
cd metanode/deploy
sudo bash systemd-cluster.sh install
```

Script sẽ lần lượt chạy `install.sh` cho từng node (0→4). Mỗi node tự build binary, tạo cấu hình, cài service, và khởi động.

Cài chỉ 1 node cụ thể:
```bash
sudo bash systemd-cluster.sh install --node 4
```

---

## Lệnh quản lý hàng ngày

### Khởi động / Dừng / Restart toàn cụm

```bash
# Khởi động tất cả
sudo bash systemd-cluster.sh start

# Dừng tất cả (ngược thứ tự, an toàn)
sudo bash systemd-cluster.sh stop

# Restart tất cả
sudo bash systemd-cluster.sh restart

# Chỉ restart 1 node
sudo bash systemd-cluster.sh restart 4
```

### Xem trạng thái

```bash
# Tổng quan tất cả nodes
bash systemd-cluster.sh status
```

Output ví dụ:
```
  Node 0  (validator)  │ execution: active   │ consensus: active
  Node 1  (validator)  │ execution: active   │ consensus: active
  Node 2  (validator)  │ execution: active   │ consensus: active
  Node 3  (validator)  │ execution: active   │ consensus: active
  Node 4  (synconly)   │ execution: active   │ consensus: active
```

### Kiểm tra block height toàn cụm

```bash
bash systemd-cluster.sh check
```

Output ví dụ:
```
  Node 0  (validator) port:8757  │ Block #1234
  Node 1  (validator) port:8758  │ Block #1234
  Node 2  (validator) port:8759  │ Block #1234
  Node 3  (validator) port:8760  │ Block #1234
  Node 4  (synconly)  port:8762  │ Block #1234
```

### Xem log

```bash
# Log execution Node 0 (real-time)
bash systemd-cluster.sh logs 0

# Log consensus Node 0
bash systemd-cluster.sh logs 0 consensus

# Log cả 2 service Node 0
bash systemd-cluster.sh logs 0 both

# Hoặc dùng journalctl trực tiếp
journalctl -u metanode-execution-0 -f
journalctl -u metanode-consensus-4 -f
```

### Xử lý lỗi crash loop (rate-limit)

```bash
sudo bash systemd-cluster.sh reset-failed
sudo bash systemd-cluster.sh start
```

---

## Cấu trúc thư mục sau khi cài

```
/opt/
├── metanode-0/          # Node 0 Validator
│   ├── bin/             #   Binaries + genesis.json
│   ├── config/          #   execution.json, consensus.toml
│   ├── keys/            #   protocol_key.json, network_key.json
│   ├── data/            #   DB, snapshots
│   └── logs/            #   App logs
├── metanode-1/          # Node 1 Validator
├── metanode-2/          # Node 2 Validator
├── metanode-3/          # Node 3 Validator
└── metanode-4/          # Node 4 Sync-Only (BTRFS)
```

---

## Dấu hiệu cụm hoạt động tốt

- ✅ `bash systemd-cluster.sh status` — tất cả `active`
- ✅ `bash systemd-cluster.sh check` — block height tăng đồng đều
- ✅ Log không có `FORK`, `Conflict`, hay `panic`
- ✅ Node 4 log có `Committed block` và snapshot được tạo định kỳ
