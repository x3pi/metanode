# 📸 Hướng dẫn sử dụng Snapshot System

## Mục lục
- [Tổng quan](#tổng-quan)
- [Cấu hình](#cấu-hình)
- [Xem danh sách Snapshot](#xem-danh-sách-snapshot)
- [Tải Snapshot](#tải-snapshot)
- [Khôi phục node từ Snapshot](#khôi-phục-node-từ-snapshot)
- [FAQ](#faq)

---

## Tổng quan

Hệ thống snapshot tự động tạo bản sao dữ liệu blockchain sau mỗi epoch transition. Snapshot được phục vụ qua HTTP server, hỗ trợ:

- ✅ **Resume download** — tải lại từ chỗ bị ngắt (Range requests)
- ✅ **Tải đa luồng** — dùng `aria2c` để tải nhanh hơn
- ✅ **Không giới hạn dung lượng** — streaming, không load vào RAM
- ✅ **Tự động xoay vòng** — chỉ giữ 2 snapshot gần nhất

**Port mặc định:** `8700`

---

## Cấu hình

Thêm vào file config JSON của Go node (VD: `config-master.json`):

```json
{
  "snapshot_enabled": true,
  "snapshot_server_port": 8700,
  "snapshot_blocks_delay": 20,
  "Databases": {
    "RootPath": "./sample/simple/data-write/data",
    "SnapshotPath": "./snapshot_data"
  }
}
```

| Field | Mô tả | Mặc định |
|-------|--------|----------|
| `snapshot_enabled` | Bật/tắt snapshot | `false` |
| `snapshot_server_port` | Port HTTP server | `8700` |
| `snapshot_blocks_delay` | Đợi bao nhiêu blocks sau epoch transition mới chụp | `20` |
| `snapshot_method` | Phương pháp snapshot: `"hardlink"`, `"rsync"`, hoặc `"hybrid"` | `"hardlink"` |
| `snapshot_source_dir` | Thư mục cần snapshot (dùng cho rsync/hybrid) | parent of RootPath |
| `Databases.SnapshotPath` | Thư mục lưu snapshot | `./snapshot_data` |

### Cấu hình Hybrid Snapshot (Khuyên dùng — Safe cho Xapian & NOMT)

Hybrid snapshot kết hợp **Native Checkpoint** (PebbleDB), **Atomic Snapshot** (NOMT với reflink), và rsync/copy song song cho Xapian, đảm bảo data nhất quán và không tốn thêm dung lượng (trên btrfs/xfs).

```json
{
  "snapshot_enabled": true,
  "snapshot_server_port": 8700,
  "snapshot_blocks_delay": 20,
  "snapshot_method": "hybrid",
  "snapshot_source_dir": "./sample/simple/data-write"
}
```

| Field | Mô tả | Ví dụ |
|-------|--------|-------|
| `snapshot_method` | Phải là `"rsync"` | `"rsync"` |
| `snapshot_source_dir` | Thư mục cần snapshot | `"./sample/simple/data-write"` |

> **Lưu ý:** Rsync method không cần sudo. Chỉ cần `rsync` cài sẵn trên hệ thống (`sudo apt install rsync`).

---

## Xem danh sách Snapshot

### Cách 1: Trình duyệt web

Mở trình duyệt và truy cập:

```
http://<IP_NODE>:8700/
```

Trang web sẽ hiển thị danh sách tất cả snapshot với thông tin:
- Tên snapshot (VD: `snap_epoch_5_block_4220`)
- Epoch number
- Block number
- Thời gian tạo
- Dung lượng tổng
- Số files

### Cách 2: API JSON (cho script tự động)

```bash
curl http://<IP_NODE>:8700/api/snapshots
```

Kết quả trả về:
```json
[
  {
    "epoch": 5,
    "block_number": 4220,
    "boundary_block": 4200,
    "timestamp": 1738900000000,
    "created_at": "2026-02-07T03:20:00Z",
    "data_dir": "./sample/simple/data-write/data",
    "snapshot_name": "snap_epoch_5_block_4220"
  },
  {
    "epoch": 4,
    "block_number": 4120,
    "boundary_block": 4100,
    "timestamp": 1738800000000,
    "created_at": "2026-02-06T22:10:00Z",
    "data_dir": "./sample/simple/data-write/data",
    "snapshot_name": "snap_epoch_4_block_4120"
  }
]
```

### Cách 3: Xem trực tiếp trên server

```bash
# Liệt kê thư mục snapshot
ls -la ./snapshot_data/

# Xem metadata của từng snapshot
cat ./snapshot_data/snap_epoch_5_block_4220/metadata.json
```

### Cách 4: Duyệt file trong snapshot

Mở trình duyệt:
```
http://<IP_NODE>:8700/files/snap_epoch_5_block_4220/
```

Hoặc dùng curl:
```bash
# Liệt kê thư mục con
curl http://<IP_NODE>:8700/files/snap_epoch_5_block_4220/
```

Cấu trúc bên trong snapshot:
```
snap_epoch_5_block_4220/
├── metadata.json           ← Thông tin epoch, block, thời gian
├── account_state/          ← State accounts
├── blocks/                 ← Block data
├── receipts/               ← Transaction receipts
├── transaction_state/      ← Transaction state trie
├── mapping/                ← Hash mappings
├── smart_contract_code/    ← Contract bytecode
├── smart_contract_storage/ ← Contract storage
├── stake_db/               ← Validator stake data
├── trie_database/          ← Merkle Patricia Trie
├── backup_device_key_storage/
└── xapian_node/
```

---

## Tải Snapshot

### Tải toàn bộ snapshot (cách đơn giản nhất)

```bash
# Tạo thư mục đích
mkdir -p /data/snapshot_restore

# Tải toàn bộ snapshot (recursive, có resume)
wget -c -r -np -nH --cut-dirs=2 \
  http://<IP_NODE>:8700/files/snap_epoch_5_block_4220/ \
  -P /data/snapshot_restore/
```

**Giải thích tham số:**
- `-c` : Resume nếu bị ngắt giữa chừng
- `-r` : Tải đệ quy (tất cả thư mục con)
- `-np` : Không đi lên thư mục cha
- `-nH` : Không tạo thư mục theo hostname
- `--cut-dirs=2` : Bỏ 2 cấp thư mục (`files/snap_name/`)
- `-P` : Thư mục đích

### Tải nhanh với aria2c (đa luồng)

```bash
# Cài đặt aria2c nếu chưa có
sudo apt install aria2

# Tải 1 file cụ thể với 16 kết nối song song
aria2c -x 16 -s 16 -c \
  http://<IP_NODE>:8700/files/snap_epoch_5_block_4220/blocks/000001.ldb \
  -d /data/snapshot_restore/blocks/
```

**Giải thích:**
- `-x 16` : 16 kết nối đồng thời
- `-s 16` : Chia file thành 16 phần tải song song
- `-c` : Resume nếu bị ngắt

### Tải bằng curl (từng file)

```bash
# Tải 1 file, có resume
curl -C - -o metadata.json \
  http://<IP_NODE>:8700/files/snap_epoch_5_block_4220/metadata.json

# Tải 1 file LevelDB cụ thể
curl -C - -o 000001.ldb \
  http://<IP_NODE>:8700/files/snap_epoch_5_block_4220/blocks/000001.ldb
```

### Script tải tự động (recommended cho production)

```bash
#!/bin/bash
# download_snapshot.sh — Tải snapshot mới nhất từ node

NODE_IP="${1:-127.0.0.1}"
PORT="${2:-8700}"
DEST="${3:-./restored_data}"

echo "📡 Đang kiểm tra snapshot trên http://$NODE_IP:$PORT ..."

# Lấy snapshot mới nhất từ API
LATEST=$(curl -s "http://$NODE_IP:$PORT/api/snapshots" | \
  python3 -c "import sys,json; snaps=json.load(sys.stdin); \
  print(max(snaps, key=lambda x: x['block_number'])['snapshot_name'])" 2>/dev/null)

if [ -z "$LATEST" ]; then
  echo "❌ Không tìm thấy snapshot nào!"
  exit 1
fi

echo "📦 Snapshot mới nhất: $LATEST"
echo "📥 Bắt đầu tải vào: $DEST"
echo ""

# Tải toàn bộ snapshot
wget -c -r -np -nH --cut-dirs=2 \
  "http://$NODE_IP:$PORT/files/$LATEST/" \
  -P "$DEST" \
  --progress=bar:force 2>&1

echo ""
echo "✅ Tải xong! Dữ liệu tại: $DEST"
echo "📋 Metadata:"
cat "$DEST/metadata.json" 2>/dev/null
```

Sử dụng:
```bash
chmod +x download_snapshot.sh

# Tải từ node local
./download_snapshot.sh 127.0.0.1 8700 ./my_data

# Tải từ node remote
./download_snapshot.sh 192.168.1.100 8700 /data/restored
```

---

## Khôi phục node từ Snapshot

### Bước 1: Dừng node cần khôi phục

```bash
# Dừng Go node
kill $(pgrep -f simple_chain)

# Dừng Rust MetaNode
kill $(pgrep -f metanode)
```

### Bước 2: Backup dữ liệu cũ (tuỳ chọn)

```bash
mv ./sample/simple/data-write/data ./sample/simple/data-write/data.bak
```

### Bước 3: Tải snapshot

```bash
./download_snapshot.sh <IP_NODE_NGUON> 8700 ./sample/simple/data-write/data
```

### Bước 4: Khởi động lại node

```bash
# Khởi động bình thường, node sẽ đọc dữ liệu từ snapshot
./run.sh
```

Node sẽ tự động:
1. Đọc LevelDB data từ snapshot
2. Xác định block cuối cùng trong snapshot
3. Bắt đầu sync các blocks thiếu từ network

---

## FAQ

### Q: Snapshot có ảnh hưởng đến performance của node không?
**A:** Không đáng kể.
- **Hardlink method:** Tạo tức thì, không tốn thêm dung lượng (chỉ LevelDB)
- **Rsync method:** Copy toàn bộ thư mục data-write, thời gian phụ thuộc vào dung lượng

### Q: Khác biệt giữa Hardlink, Rsync, và Hybrid?
**A:**
| | Hardlink | Rsync | Hybrid (Khuyên dùng) |
|--|---------|-------|----------------------|
| Tốc độ | Tức thì (LevelDB) | Phụ thuộc size | Tức thì (Reflink/Native Checkpoint) |
| Xapian safe? | ❌ Có thể corrupt | ✅ Full copy | ✅ An toàn (Reflink/Parallel copy) |
| NOMT safe? | ❌ Rủi ro lúc flush | ✅ Full copy | ✅ Atomic native snapshot & reflink |
| Cần sudo? | Không | Không | Không |
| Snapshot gì? | Chỉ LevelDB | **Toàn bộ data-write** | Chắt lọc tối ưu từng Engine |
| Khuyên dùng | Dev/test | Legacy | Production (chuẩn mới) |

### Q: Download bị ngắt giữa chừng, phải làm gì?
**A:** Chạy lại lệnh `wget -c` hoặc `curl -C -`. Flag `-c` / `-C -` sẽ tự động tiếp tục từ chỗ bị ngắt (nhờ HTTP Range requests).

### Q: Làm sao biết snapshot đã tải đầy đủ?
**A:** So sánh với metadata:
```bash
# Xem metadata trên server
curl http://<IP>:8700/files/snap_epoch_5_block_4220/metadata.json

# Kiểm tra số file đã tải
find ./restored_data -type f | wc -l
```

### Q: Port 8700 bị block bởi firewall?
**A:** Mở port hoặc đổi port trong config:
```json
{ "snapshot_server_port": 9999 }
```

### Q: Node chưa có snapshot nào (mới bật)?
**A:** Snapshot tự động được tạo sau epoch transition + 20 blocks. Nếu node mới bật, hãy đợi epoch tiếp theo transition.