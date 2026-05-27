# systemd-cluster.sh — Hướng dẫn sử dụng

Script điều phối tổng (**Orchestrator**) để quản lý toàn bộ vòng đời của Cluster Metanode (5 node: 0–4) trên 1 máy chủ Linux qua `systemd`.

---

## Kiến trúc hệ thống

Mỗi node trong cluster bao gồm **2 tiến trình systemd độc lập**:

```
Node N
├── metanode-execution-N   (Go — Execution Layer, tầng xử lý giao dịch)
└── metanode-consensus-N   (Rust — Consensus Engine, tầng đồng thuận)
```

- **Execution phải khởi động TRƯỚC** Consensus (do Consensus kết nối vào Execution qua FFI socket).
- **Dừng thì Consensus TRƯỚC**, sau mới Execution (để tránh mất data DB khi tắt).
- **RPC Client** (`metanode-rpc-N`) là một tiến trình **riêng biệt hoàn toàn**, không do script này quản lý.

---

## Cài đặt lần đầu

### Bước 1 — Sinh Keys & Genesis

```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy

# Sinh key cho 4 Validator (node 0-3)
python3 gen_validator_entry.py --hostname node-0 --node-type validator --ip 127.0.0.1 --node-id 0
python3 gen_validator_entry.py --hostname node-1 --node-type validator --ip 127.0.0.1 --node-id 1
python3 gen_validator_entry.py --hostname node-2 --node-type validator --ip 127.0.0.1 --node-id 2
python3 gen_validator_entry.py --hostname node-3 --node-type validator --ip 127.0.0.1 --node-id 3

# Sinh key cho 1 Sync-only (node 4)
python3 gen_validator_entry.py --hostname node-4 --node-type synconly --ip 127.0.0.1 --node-id 4
```

Script tự động tạo `genesis.json` và copy vào `execution/cmd/simple_chain/`.

### Bước 2 — Setup & Cài đặt

> ⚠️ Lệnh `setup` sẽ **XÓA DATA CŨ** (nếu có) và cài mới hoàn toàn từ block 0.

```bash
sudo bash systemd-cluster.sh setup -y
```

---

## Danh sách lệnh

### `setup` — Xóa data + cài mới (Fresh Start)

**Dùng khi:** Khởi tạo mạng lần đầu, reset testnet, thay `genesis.json`, muốn chạy lại từ Block 0.

> 🔴 **CẢNH BÁO:** Xóa vĩnh viễn toàn bộ blockchain data trong `/opt/metanode/node-X/data/` và `/logs/`. Không thể khôi phục!

```bash
# Xóa data TẤT CẢ 5 node + cài mới (có hỏi xác nhận)
sudo bash systemd-cluster.sh setup

# Xóa data + cài mới, tự động đồng ý (không hỏi)
sudo bash systemd-cluster.sh setup -y

# Xóa data + cài mới chỉ Node 4
sudo bash systemd-cluster.sh setup --node 4 -y
```

---

### `install` — Cập nhật binary/config, **GIỮ NGUYÊN data**

**Dùng khi:** Update code mới, thay đổi cấu hình, node sync tiếp từ block hiện tại.

> ✅ **An toàn:** Không đụng vào `data/`. Node sẽ tiếp tục sync từ block cũ sau khi restart.

```bash
# Cập nhật TẤT CẢ 5 node
sudo bash systemd-cluster.sh install

# Cập nhật không hỏi xác nhận
sudo bash systemd-cluster.sh install -y

# Cập nhật chỉ Node 4
sudo bash systemd-cluster.sh install --node 4
```

---

### `start` — Khởi động

```bash
# Khởi động toàn bộ cluster
sudo bash systemd-cluster.sh start

# Khởi động chỉ Node 2
sudo bash systemd-cluster.sh start 2
```

Thứ tự khởi động: Execution trước → chờ 3 giây → Consensus.

---

### `stop` — Dừng an toàn

```bash
# Dừng toàn bộ cluster
sudo bash systemd-cluster.sh stop

# Dừng chỉ Node 0
sudo bash systemd-cluster.sh stop 0
```

Gửi `SIGTERM` và chờ process flush DB xuống đĩa (timeout 90s cho Execution, 60s cho Consensus) để tránh vỡ data.

---

### `restart` — Khởi động lại

```bash
# Restart toàn bộ
sudo bash systemd-cluster.sh restart

# Restart chỉ Node 3
sudo bash systemd-cluster.sh restart 3
```

---

### `status` — Xem trạng thái dashboard

```bash
bash systemd-cluster.sh status
```

In ra bảng màu sắc cho biết trạng thái **Active (xanh)** / **Inactive (đỏ)** của tất cả 10 tiến trình (5 execution + 5 consensus).

---

### `check` — Kiểm tra block height

```bash
bash systemd-cluster.sh check
```

Quét qua RPC Port của tất cả 5 node và in ra **Block Height** hiện tại. Dùng để kiểm tra node nào đang bị tụt lại so với mạng.

---

### `logs` — Xem log theo thời gian thực

```bash
# Log execution (Go) của Node 0
bash systemd-cluster.sh logs 0

# Log consensus (Rust) của Node 0
bash systemd-cluster.sh logs 0 consensus

# Log cả 2 service của Node 0
bash systemd-cluster.sh logs 0 both
```

Nhấn `Ctrl+C` để thoát.

---

### `reset-failed` — Gỡ rate-limit systemd

```bash
sudo bash systemd-cluster.sh reset-failed
```

Nếu một node sập quá 3 lần trong 10 phút, `systemd` sẽ khoá không cho bật lại (trạng thái `failed`). Lệnh này gỡ khoá để bạn có thể `start` lại sau khi fix xong bug.

---

## Bảng tóm tắt nhanh

| Lệnh | Xóa data? | Dùng khi nào |
|:---|:---:|:---|
| `setup` | ✅ **CÓ** | Lần đầu, reset testnet, thay genesis |
| `install` | ❌ Không | Update code, thay config, giữ dữ liệu |
| `start` | ❌ Không | Bật node đang tắt |
| `stop` | ❌ Không | Tắt node an toàn |
| `restart` | ❌ Không | Bật lại sau khi thay config |
| `status` | ❌ Không | Xem sức khoẻ cluster |
| `check` | ❌ Không | Kiểm tra block height |
| `reset-failed` | ❌ Không | Sau crash loop |

---

## Quản lý RPC Client (riêng biệt)

RPC Client **không** nằm trong `systemd-cluster.sh`. Quản lý riêng bằng:

```bash
# Cài đặt/cập nhật RPC cho tất cả node
sudo bash install-rpc-systemd.sh

# Xem trạng thái
sudo systemctl status metanode-rpc-0
sudo systemctl status metanode-rpc-4

# Xem log
journalctl -u metanode-rpc-4 -f
```

---

## Cấu trúc thư mục node

```
/opt/metanode/node-X/
├── bin/
│   ├── simple_chain          # Go Execution binary
│   └── metanode              # Rust Consensus binary
├── config/
│   ├── execution.json
│   └── consensus.toml
├── data/                     ← setup xóa thư mục này
│   ├── execution/db/
│   └── consensus/
├── keys/                     ← KHÔNG bao giờ bị xóa
│   ├── protocol_key.json
│   └── network_key.json
└── logs/                     ← setup xóa thư mục này
    ├── execution/
    └── consensus/
```
