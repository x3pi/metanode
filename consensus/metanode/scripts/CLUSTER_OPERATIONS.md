# Vận hành Cluster Metanode — Khởi động, Tắt & Khởi động lại

> **Script chính**: `mtn-orchestrator.sh`
> **Vị trí**: `mtn-consensus/metanode/scripts/mtn-orchestrator.sh`
> **Symlink**: `chain-n/mtn-orchestrator.sh` → file trên

---

## Mục lục

1. [Kiến trúc Cluster](#kiến-trúc-cluster)
2. [Khởi động (Start)](#khởi-động-start)
3. [Tắt (Stop)](#tắt-stop)
4. [Khởi động lại (Restart)](#khởi-động-lại-restart)
5. [Quản lý từng Node](#quản-lý-từng-node)
6. [Cơ chế Crash Safety](#cơ-chế-crash-safety)
7. [Xử lý sự cố](#xử-lý-sự-cố)
8. [Kiểm tra trạng thái](#kiểm-tra-trạng-thái)

---

## Kiến trúc Cluster

Mỗi cluster gồm **5 nodes** (0–4), mỗi node vận hành theo **kiến trúc quy trình đơn (Unified Process)** thông qua FFI:

```
┌─────────────────────────────────────────────────────┐
│                    Node N                            │
│                                                      │
│  ┌────────────────────────────────────────────────┐  │
│  │           MetaNode Unified Process             │  │
│  │                                                │  │
│  │   ┌───────────────┐  FFI   ┌───────────────┐   │  │
│  │   │  Rust Consensus│◄──────►│  Go Executor  │   │  │
│  │   │   (Embedded)   │       │ (State & Pool)│   │  │
│  │   └───────────────┘        └───────────────┘   │  │
│  └────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

### Chế độ Activity (Node Modes)

| Mode | Vai trò | Binary |
|:--------|:--------|:-------|
| **Validator** | Nhận giao dịch (TX), chạy giả (virtual execution) để tính relative address ➔ đưa vào đồng thuận tạo Block ➔ thực thi và lưu State. | `simple_chain`|
| **SyncOnly** | Nhận giao dịch (TX), chạy giả để tính relative address ➔ chuyển tiếp (forward) trực tiếp tới mạng lưới đồng thuận tạo block. Chỉ đồng bộ block về thực thi chứ không tự sinh block. | `simple_chain`|

### Thứ tự khởi động / tắt

```
Khởi động: Tiến trình Unified Node (tích hợp cả Validator và RPC)
Tắt:       Tiến trình Unified Node (Tự handle ngắt FFI và lưu State gốc)
```

---

## Khởi động (Start)

### Fresh Start (lần đầu hoặc reset hoàn toàn)

```bash
./mtn-orchestrator.sh start --fresh
```

**Hành động:**

1. Dừng tất cả session cũ (nếu có)
2. **Xóa toàn bộ dữ liệu**: Rust storage + Go data/backup
3. Node khởi tạo **genesis block** (deploy system contracts)
4. Rust bắt đầu đồng thuận

**Thời gian**: ~30-60 giây (genesis processing mất ~1-2 phút)

> ⚠️ **Lưu ý**: Sau `--fresh`, hệ thống cần vài phút xử lý genesis trước khi bắt đầu tạo blocks. Trong thời gian này `block=0` là bình thường.

### Start với dữ liệu cũ (restart sau khi stop)

```bash
./mtn-orchestrator.sh start
```

**Hành động:**

1. Go Executor load block cuối cùng từ PebbleDB
2. Xóa `executor_state` của Rust (tránh recovery gap)
3. Rust query Go Executor để lấy block number hiện tại
4. Rust bắt đầu đồng thuận từ vị trí Go hiện hành

**Log mong đợi (Go Node)**:

```
Using existing block (not init genesis)
✅ [STARTUP] Initialized LastGlobalExecIndex from last block header: gei=XXXX (block=#YY)
```

---

## Tắt (Stop)

```bash
./mtn-orchestrator.sh stop
```

### Quy trình tắt an toàn

```
SIGTERM → Node (tất cả 5 nodes, cả Validator lẫn SyncOnly)
         Tiến trình thực hiện quá trình StopWait():
           1. Rust FFI ngắt consensus, ngưng nhận TX.
           2. StopWait(12s) — chờ pending operations hoàn thành.
           3. SaveLastBlockSync() — ghi block cuối xuống disk (atomic).
           4. FlushAll() — flush toàn bộ PebbleDB memtable → SST files.
           5. CloseAll() — đóng tất cả database handles an toàn.
```

### Timeout & SIGKILL

- Mỗi process được chờ tối đa **30 giây** để tự dừng
- Nếu quá thời gian → gửi SIGKILL (có thể mất dữ liệu!)
- Log cảnh báo: `⚠️ ... chưa dừng sau 30s → SIGKILL`

### Kiểm tra sau khi stop

```bash
# Kiểm tra không còn process orphan
pgrep -f "simple_chain.*config-" && echo "WARN: Go orphans!" || echo "OK"
pgrep -f "metanode start" && echo "WARN: Rust orphans!" || echo "OK"
```

---

## Khởi động lại (Restart)

### Restart giữ dữ liệu

```bash
./mtn-orchestrator.sh restart
```

Tương đương: `stop` → chờ 3s → `start`

### Restart xóa dữ liệu và Tự động Build Code mới

```bash
./mtn-orchestrator.sh restart --fresh
```

Tương đương: `stop` → xóa sạch dữ liệu → `start` (mặc định không build lại code để tiết kiệm thời gian khởi động).

Nếu bạn vừa sửa source code và muốn **Build lại dự án (Go và Rust)** trước khi chạy:

```bash
./mtn-orchestrator.sh restart --fresh --build
```

Nếu bạn có thay đổi liên quan đến C++ EVM precompile và muốn **Build lại toàn bộ (C++ EVM, Go và Rust)**:

```bash
./mtn-orchestrator.sh restart --fresh --build-all
```

### Restart loại trừ một node (giả lập node sập/catch-up)

Nếu bạn muốn khởi động hệ thống nhưng **không chạy một node cụ thể đồng bộ** (ví dụ node 4) nhằm phục vụ test cơ chế catch-up (sync block), hãy thêm tùy chọn `--exclude-node <ID>`. Khi dùng cờ này cùng với `--fresh`, dữ liệu và log ổ cứng của node bị loại trừ sẽ **không bị xóa**.

```bash
./mtn-orchestrator.sh restart --fresh --build-all --exclude-node 4
```

Khi hệ thống 4 node kia đã hoạt động hoàn tất, bạn có thể khởi động riêng node 4 sau đó bằng lệnh:
```bash
./mtn-orchestrator.sh start-node 4
```

> 💡 **Mẹo**: Các lệnh build (`cargo build`, `go build`, `make`) đều được thiết kế theo cơ chế **incremental compilation** (build tăng dần). Nên kể cả khi bạn thêm cờ `--build` hoặc `--build-all`, nếu code không bị thay đổi thì quá trình vượt qua bước build vẫn sẽ cực kỳ nhanh do không bị compile lại từ đầu. Gần như có thể dùng cờ này một cách thoải mái.
---

## Quản lý từng Node

### Dừng 1 node

```bash
./mtn-orchestrator.sh stop-node <node_id>     # VD: stop-node 2
```

### Khởi động 1 node

```bash
./mtn-orchestrator.sh start-node <node_id>    # VD: start-node 2
```

### Restart 1 node

```bash
./mtn-orchestrator.sh restart-node <node_id>  # VD: restart-node 2
```

---

## Cơ chế Crash Safety

*(Chống lệch block / lệch hash khi restart)*

Cơ chế này giải quyết triệt để lỗi lệch block/hash (Gap detected in block sequence) bằng chiến thuật phối hợp: **"Rust phải quên đi quá khứ, còn Go không bao giờ được quên block cuối"**.

### 1. Phía Go: Lưu giữ block cuối cùng một cách "Tuyệt đối" (Atomic Save)

- **PebbleDB Full Flush**: Khi mạng đang chạy, Go tự động flush toàn bộ memtable → SST files mỗi 10 giây (không chỉ WAL). Đảm bảo dữ liệu bám chặt vào ổ cứng ngay cả khi crash.
- **Quy trình Shutdown an toàn (Drain & Flush)**: Khi chạy script `./mtn-orchestrator.sh stop`, hệ thống sẽ:
  1. Ngắt Rust Consensus và đợi 10 giây (Drain pipeline) để Node (Go Executor) nhai nốt 100% số block còn kẹt trong hàng đợi.
  2. Khóa cửa (chạy `StopWait()`), kích hoạt `SaveLastBlockSync()` ghi đồng bộ cứng ngắc đúng cái block cuối cùng xuất ra file `back_up/last_block_backup.json` và chốt thẳng PebbleDB ra đĩa. Go sẽ không bao giờ chết bất đắc kỳ tử để mất block.
- **Safety Guard (Chống Genesis Re-init)**: Mở mạng lên mà có data nhưng mất chìa khóa last_block thì báo 🚨 **REFUSE** ngay lập tức, từ chối chạy tàn phá database.

### 2. Phía Rust: Ép buộc "quên" đi trí nhớ cũ của Executor State

- **Vấn đề cũ**: Rust lưu giữ file `executor_state/last_sent_index.bin` để nhớ GEI cuối cùng gửi cho Go. Sau sự kiện Garbage Collection của DAG, chỉ số GEI cũ này trong Rust không còn khớp với Go, dẫn đến nổ chain nổ gap khi restart.
- **Rust Setup Cleanup**: Trong code khởi động của `mtn-orchestrator.sh`, nó sẽ tự động chạy lệnh **xóa thẳng tay `executor_state`** của từng node Rust.
- **Tác dụng**: Bị mất file trí nhớ, Rust buộc phải đi **hỏi ngược lại Go Executor** ngay khi khởi động: *"Hiện tại ông đang đến GEI bao nhiêu để tôi cấp block mới?"*. Cứ thế nó sẽ xuất phát đồng thuận **từ đúng vị trí khớp nối**, vĩnh viễn không còn gặp lại lỗi Gap hay lệch Hash khi bật tắt máy.

---

## Xử lý sự cố

### ❌ Lỗi: "Gap detected in block sequence"

**Nguyên nhân**: Rust DAG storage chứa stale commits từ epoch cũ, không khớp Go block number.

**Giải pháp**: Đã được fix tự động — orchestrator xóa toàn bộ Rust storage trước mỗi lần start. Nếu vẫn gặp (chạy Rust thủ công):

```bash
# Xóa thủ công
for i in 0 1 2 3 4; do
  rm -rf mtn-consensus/metanode/config/storage/node_${i}
done
# Restart
./mtn-orchestrator.sh start
```

### ❌ Lỗi: "CORRUPTED BLOCK DATABASE: lastBlock not found but data exists"

**Nguyên nhân**: Block database mất key `lastBlockHash` nhưng account state vẫn còn (crash giữa chừng).

**Giải pháp**:

```bash
# Kiểm tra backup file
cat mtn-simple-2025/cmd/simple_chain/sample/node0/back_up/last_block_backup.json

# Nếu backup có dữ liệu → Go sẽ tự phục hồi từ backup khi restart
./mtn-orchestrator.sh start

# Nếu không có backup → cần fresh start
./mtn-orchestrator.sh start --fresh
```

### ❌ Lỗi: "No existing block found" sau restart (không dùng --fresh)

**Nguyên nhân**: PebbleDB chưa flush lastBlock xuống SST trước khi process bị kill.

**Giải pháp**: Luôn dùng `./mtn-orchestrator.sh stop` thay vì `kill -9`. Nếu đã xảy ra:

```bash
# Kiểm tra backup
cat mtn-simple-2025/cmd/simple_chain/sample/node0/back_up/last_block_backup.json

# Fresh start nếu không thể phục hồi
./mtn-orchestrator.sh start --fresh
```

### ❌ Hệ thống không tạo block (block=0 sau khi start)

**Nguyên nhân**: Chưa có transactions. Consensus chạy nhưng chỉ gửi empty commits.

**Giải pháp**: Gửi transactions:

```bash
cd mtn-simple-2025/cmd/tool/tx_sender
go run . -loop
```

---

## Kiểm tra trạng thái

### Status tổng quan

```bash
./mtn-orchestrator.sh status
```

Output mẫu:

```
╔════════════════════════════════════════════════════╗
║  Node  │ Block Height    │ Mode                    ║
╠════════════════════════════════════════════════════╣
║    0   │ ✅ 495358      │   Validator ✅          ║
║    1   │ ✅ 495441      │   Validator ✅          ║
╚════════════════════════════════════════════════════╝
```

### Xem log theo node

```bash
./mtn-orchestrator.sh logs 0           # Tất cả log node 0

# Hoặc dùng script riêng
cd metanode/scripts/logs
./node-logs.sh 0 -f     # Follow log Node 0 (bao gồm Rust core logs và Go Executor logs)
```

### Kiểm tra block production

```bash
# Block hiện tại
grep "block=#" mtn-consensus/metanode/logs/node_0/node-stdout.log | tail -3

# GEI hiện tại từ Rust Consensus
grep "\[GLOBAL_EXEC_INDEX\]" mtn-consensus/metanode/logs/node_0/node-stdout.log | tail -1

# Backup file
cat mtn-simple-2025/cmd/simple_chain/sample/node0/back_up/last_block_backup.json
```

---

## Tham chiếu nhanh

| Lệnh | Mô tả |
|:------|:------|
| `start --fresh` | Xóa hết, khởi động mới hoàn toàn (Tự động compile Go) |
| `start` | Khởi động giữ dữ liệu cũ |
| `stop` | Tắt an toàn (flush disk) |
| `restart` | Tắt rồi khởi động (giữ data) |
| `restart --fresh` | Khởi động mới (Xoá sạch số liệu cũ, không tự build) |
| `restart --fresh --build` | Khởi động mới và kiểm tra/Build lại Go và Rust |
| `restart --fresh --build-all` | Khởi động mới và kiểm tra/Build lại toàn bộ (EVM, Go, Rust) |
| `start --build` | Khởi động giữ data cũ nhưng có Build lại Go và Rust |
| `start --build-all` | Khởi động giữ data cũ nhưng có Build lại toàn bộ (EVM, Go, Rust) |
| `<cmd> --exclude-node <N>`| (Dùng kèm start/restart) Không wipe data và không khởi động node N |
| `status` | Hiển thị trạng thái cluster |
| `logs <N> [layer]` | Xem log node N |
| `stop-node <N>` | Tắt 1 node |
| `start-node <N>` | Khởi động 1 node |
| `restart-node <N>` | Restart 1 node |

---

## 9. Đưa Hệ Thống Lên Production (Mainnet/Testnet)

Kịch bản `mtn-orchestrator.sh` ở trên được thiết kế dành cho **Môi Trường Testing/Dev** chạy dưới các process rời rạc / Tmux để dễ kiểm thử (Catch-up, Restart, Crash Safety, Wipe block...). Nó không phù hợp để làm script giữ core chạy liên tục dài ngày cho máy chủ Mainnet.

Để đi vào môi trường Production thực tế, mời bạn tham khảo và dùng bộ công cụ cấu hình chuyên biệt ở thư mục `deploy/`:
👉 **[Xem Hướng Dẫn Vận Hành Môi Trường Production 🚀](../deploy/README.md)**

Bộ công cụ Production này sẽ giải quyết:
- Chạy thông qua Systemd tự khởi động sau Server Reboot.
- Tự động Xoay nén các tập tin Log, chống sập/đầy ổ cứng.
- Cân bằng và Bảo Mật RPC qua Nginx Proxy.
- Có Dashboard Giám Sát Grafana, cảnh báo Crash.

---

## Cấu trúc thư mục dữ liệu

```
mtn-simple-2025/cmd/simple_chain/sample/
├── node0/
│   ├── data/data/              # PebbleDB chính (blocks, account_state, ...)
│   └── back_up/                # Backup storage + last_block_backup.json
└── node1/ ... node4/

mtn-consensus/metanode/config/storage/
├── node_0/
│   ├── executor_state/         # Persisted GEI (tự động xóa khi restart)
│   └── ...                     # DAG storage
└── node_1/ ... node_4/
```
