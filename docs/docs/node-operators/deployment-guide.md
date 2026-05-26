---
sidebar_position: 5
title: 🚀 Triển khai Cụm Node Local (Systemd)
---

# 🚀 Triển khai Cụm Node Local (Systemd)

Tài liệu này hướng dẫn cách sử dụng bộ công cụ điều phối để cài đặt, chạy, quản lý và khôi phục cụm gồm **5 node** (4 validator + 1 sync-only) trên cùng một máy chủ vật lý Linux thông qua hệ thống quản lý dịch vụ **systemd**.

---

## 🛠️ Bộ ba công cụ quản trị (Deploy Scripts)

Tất cả các công cụ quản trị nằm tại thư mục `deploy/` của repository:

1. **`setup-cluster-btrfs.sh`**: Khởi tạo phân vùng hệ thống file **BTRFS** tại `/opt/metanode` để hỗ trợ sao lưu (snapshot) copy-on-write tốc độ cao.
2. **`systemd-cluster.sh`**: Công cụ điều phối tổng (**Orchestrator**) quản lý cài đặt, khởi động, dừng và kiểm tra trạng thái của cả 5 node.
3. **`install-rpc-systemd.sh`**: Công cụ cài đặt và chạy dịch vụ **RPC Proxy** (`metanode-rpc-N.service`) riêng biệt cho mỗi node.
4. **`restore_node_systemd.sh`**: Công cụ khôi phục an toàn (Sequential & Fork-Safe) một node từ snapshot của node khác.

---

## 📂 Bước 1 — Thiết lập Phân vùng BTRFS cho Snapshot

Snapshot trong Metanode sử dụng tính năng **reflink** (copy-on-write) để nhân bản dữ liệu tức thì mà không gây nghẽn đĩa. Hệ thống file thông thường (`ext4`) không được hỗ trợ.

Chạy script cấu hình phân vùng BTRFS duy nhất 1 lần trước khi cài đặt cụm node:

```bash
cd deploy/
sudo bash setup-cluster-btrfs.sh
```

**Cách thức hoạt động của script:**
- Kiểm tra nếu máy có LVM (`ubuntu-vg`), nó sẽ tạo một Logical Volume 400GB chuẩn BTRFS.
- Nếu không có LVM, nó sẽ tạo file ảnh ảo (Sparse File) 400GB làm loop device tại `/opt/metanode_cluster_btrfs.img` và định dạng BTRFS.
- Gắn (mount) phân vùng vào `/opt/metanode`.
- Cấu hình mount tự động khi khởi động lại hệ thống trong `/etc/fstab`.

---

## 🔑 Bước 2 — Tạo Keys cho Cụm Node

Tạo thư mục cấu hình và khóa cho các node trong cluster:

```bash
# Tạo keys cho 4 Validator Node (0, 1, 2, 3)
python3 gen_validator_entry.py --hostname node-0 --node-type validator --ip 127.0.0.1 --node-id 0
python3 gen_validator_entry.py --hostname node-1 --node-type validator --ip 127.0.0.1 --node-id 1
python3 gen_validator_entry.py --hostname node-2 --node-type validator --ip 127.0.0.1 --node-id 2
python3 gen_validator_entry.py --hostname node-3 --node-type validator --ip 127.0.0.1 --node-id 3

# Tạo keys cho 1 Sync-Only Node (4)
python3 gen_validator_entry.py --hostname node-4 --node-type synconly --ip 127.0.0.1 --node-id 4
```

---

## 🚀 Bước 3 — Cài đặt và Chạy Cụm Node

### 1. Khởi tạo cụm mới (Xóa sạch dữ liệu cũ)
Sử dụng lệnh `setup` để xóa toàn bộ cơ sở dữ liệu cũ, biên dịch mã nguồn và cài đặt lại cụm từ block 0 (genesis):

```bash
# Cài đặt toàn bộ 5 nodes (sẽ hỏi xác nhận xóa data)
sudo bash systemd-cluster.sh setup

# Cài đặt tự động đồng ý và bỏ qua bước hỏi
sudo bash systemd-cluster.sh setup -y
```

### 2. Khởi động toàn bộ cụm node
```bash
sudo bash systemd-cluster.sh start
```
Thứ tự khởi động tự động: Khởi động execution layer (Go) -> chờ 3 giây -> khởi động consensus layer (Rust).

### 3. Cài đặt và khởi chạy RPC Proxy Services
Dịch vụ RPC Proxy cần được khởi chạy riêng biệt để cung cấp endpoint JSON-RPC chuẩn cho MetaMask và dApps kết nối:

```bash
# Biên dịch rpc-client và cài đặt systemd service cho tất cả 5 node
sudo bash install-rpc-systemd.sh

# Cài đặt lại và bỏ qua bước build lại code (dùng binary hiện có)
sudo bash install-rpc-systemd.sh --no-build

# Cài đặt riêng cho chỉ Node 4
sudo bash install-rpc-systemd.sh --node 4
```

---

## ⚙️ Quản lý và Vận hành Cụm Node

### Lệnh quản lý thông dụng của `systemd-cluster.sh`

* **Xem bảng trạng thái dashboard:**
  ```bash
  bash systemd-cluster.sh status
  ```
  Cho biết trạng thái **Active** / **Inactive** của các service execution và consensus trên từng node.

* **Kiểm tra chiều cao khối (Block Height) thời gian thực:**
  ```bash
  bash systemd-cluster.sh check
  ```

* **Cập nhật binary/config mới (KHÔNG mất dữ liệu):**
  ```bash
  # Biên dịch lại code mới và cập nhật cấu hình cho toàn cụm, dữ liệu blockchain được giữ nguyên
  sudo bash systemd-cluster.sh install
  ```

* **Dừng an toàn (Graceful Stop):**
  ```bash
  sudo bash systemd-cluster.sh stop
  ```

* **Xem log thời gian thực:**
  ```bash
  # Xem log execution (Go) của Node 0
  bash systemd-cluster.sh logs 0
  
  # Xem log consensus (Rust) của Node 0
  bash systemd-cluster.sh logs 0 consensus
  ```

---

## 📸 Khôi phục Node từ Snapshot (Fast Sync)

Nếu một node bị lỗi dữ liệu hoặc bạn muốn đồng bộ nhanh một node mới mà không cần chạy lại từ block 0:

```bash
# Khôi phục Node 2 từ bản snapshot mới nhất lấy từ Node 0 (Local HTTP)
sudo bash restore_node_systemd.sh 2

# Khôi phục Node 1 từ một bản snapshot cụ thể tên snap_epoch_5_block_2500 từ Node 0
sudo bash restore_node_systemd.sh 1 snap_epoch_5_block_2500

# Khôi phục Node 2 lấy snapshot mới nhất từ Node 1
sudo bash restore_node_systemd.sh 2 "" 1
```

Script sẽ tự động dừng dịch vụ của node đích, tải snapshot qua HTTP, xóa dữ liệu cũ, khôi phục cấu trúc thư mục, thiết lập phân quyền sở hữu cho người dùng `metanode`, khởi động lại dịch vụ và giám sát quá trình đồng bộ (sync monitoring) phòng chống rẽ nhánh (fork).

