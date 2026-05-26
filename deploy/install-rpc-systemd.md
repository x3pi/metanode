# install-rpc-systemd.sh — Hướng dẫn sử dụng

Script cài đặt và quản lý **Metanode RPC Client** thành các `systemd` service.

> **Khác với `systemd-cluster.sh`:** Script này chỉ quản lý tiến trình RPC Gateway (lớp ngoài cùng tiếp nhận request từ MetaMask/dApp). Core Node (Execution + Consensus) được quản lý hoàn toàn độc lập.

---

## Điều kiện tiên quyết

- Đã sinh keys cho các node (`node-N_keys/` tồn tại):
  ```bash
  python3 gen_validator_entry.py --hostname node-N --node-type validator --ip 127.0.0.1 --node-id N
  ```
- Đã cài `jq`: `sudo apt install jq`
- File `config-rpc-nodeN.json` và `config-client-tcp-nodeN.json` tồn tại trong thư mục `rpc-client`

---

## Cách sử dụng

```bash
sudo bash install-rpc-systemd.sh [options]
```

### Tuỳ chọn (Options)

| Option | Mô tả |
|:---|:---|
| *(không có)* | Build binary + cài đặt tất cả 5 node |
| `--node N` | Chỉ xử lý Node N (0–4) |
| `--no-build` | Bỏ qua bước build (dùng binary `rpc-client` đã có sẵn) |
| `--stop` | **Chỉ dừng** RPC service(s), không cài đặt gì |
| `-h`, `--help` | Hiển thị trợ giúp |

---

## Các trường hợp sử dụng thực tế

### Cài đặt lần đầu (tất cả node)

```bash
sudo bash install-rpc-systemd.sh
```
Thực hiện tuần tự:
1. **Build** binary `rpc-client` từ source Go
2. **Đọc port** từ `node-N_keys/*.env` (không cần chỉnh tay)
3. **Cập nhật** `config-rpc-nodeN.json` và `config-client-tcp-nodeN.json`
4. **Tạo** file service `/etc/systemd/system/metanode-rpc-N.service`
5. **Dừng** instance RPC cũ (nếu đang chạy)
6. **Khởi động** service mới

---

### Cài lại / Update config cho 1 node cụ thể

```bash
# Cập nhật và khởi động lại Node 4
sudo bash install-rpc-systemd.sh --node 4

# Nếu binary chưa thay đổi (chỉ sửa config/port), bỏ qua build cho nhanh
sudo bash install-rpc-systemd.sh --node 4 --no-build
```
---

### Dừng RPC

```bash
# Dừng TẤT CẢ RPC (node 0 đến 4)
sudo bash install-rpc-systemd.sh --stop

# Dừng chỉ Node 4
sudo bash install-rpc-systemd.sh --stop --node 4
```

> Script tự kiểm tra xem service có đang chạy không trước khi gửi lệnh dừng, nên an toàn khi gọi lặp lại nhiều lần.

---

### Chỉ cập nhật config (không build lại)

Hữu ích khi bạn thay đổi IP/port hoặc thay file `genesis.json`:

```bash
sudo bash install-rpc-systemd.sh --no-build
```

---

## Quản lý bằng systemctl trực tiếp

Sau khi cài xong, bạn có thể dùng `systemctl` bình thường:

```bash
# Xem trạng thái
sudo systemctl status metanode-rpc-0
sudo systemctl status metanode-rpc-4

# Dừng 1 node
sudo systemctl stop metanode-rpc-0

# Khởi động lại
sudo systemctl restart metanode-rpc-4

# Bật tự động cùng hệ thống (đã được bật mặc định khi install)
sudo systemctl enable metanode-rpc-0
```

---

## Xem log

```bash
# Log realtime của Node 4
journalctl -u metanode-rpc-4 -f

# Log file của Node 0 (ghi thêm vào file)
tail -f /home/abc/nhat/con-chain-v2/metanode/execution/cmd/rpc/cmd/rpc-client/node0_data/logs/systemd.log
```

---

## Bảng Port theo Node

| Node | Execution RPC (nội bộ) | HTTP Client | HTTPS Client | TCP Client | P2P |
|:---:|:---:|:---:|:---:|:---:|:---:|
| 0 | 10746 | **8545** | 8666 | 9545 | 6200 |
| 1 | 10747 | **8546** | 8667 | 9546 | 6201 |
| 2 | 10748 | **8547** | 8668 | 9547 | 6202 |
| 3 | 10749 | **8548** | 8669 | 9548 | 6203 |
| 4 | 10750 | **8549** | 8670 | 9549 | 6204 |

> Port **HTTP Client** (cột 3) là địa chỉ bạn nhập vào MetaMask: `http://localhost:8545`

> Các port này được đọc tự động từ `node-N_keys/*.env`, **không hardcode** trong script.

---

## Cấu trúc file liên quan

```
deploy/
├── install-rpc-systemd.sh      ← Script này
├── node-N_keys/
│   ├── validator.env           ← Nguồn port cho Node 0-3
│   └── synconly.env            ← Nguồn port cho Node 4
execution/cmd/rpc/cmd/rpc-client/
├── rpc-client                  ← Binary (được build bởi script)
├── config-rpc-nodeN.json       ← Config HTTP/WSS (được update tự động)
├── config-client-tcp-nodeN.json← Config TCP/P2P (được update tự động)
└── nodeN_data/logs/
    └── systemd.log             ← Log file của RPC Node N
```
