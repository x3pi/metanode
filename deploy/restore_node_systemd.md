# Hướng dẫn sử dụng restore_node_systemd.sh

Script `restore_node_systemd.sh` được dùng để khôi phục (restore) một node chạy dưới dạng **systemd service** (`metanode-execution-N`, `metanode-consensus-N`, `metanode-rpc-N`) từ bản sao lưu (snapshot) của một node khác trên local cluster một cách an toàn và tự động.

## Yêu cầu
- Script phải được chạy dưới quyền **root** (`sudo`) vì cần tương tác với `systemctl` và thực hiện phân quyền sở hữu thư mục dữ liệu cho user hệ thống (`metanode`).

## Cú pháp

```bash
sudo bash restore_node_systemd.sh <node_id> [snapshot_name] [source_node_id]
```

## Các tham số

1. **`node_id` (Bắt buộc)**: 
   - ID của node bạn muốn khôi phục dữ liệu. 
   - Giá trị hợp lệ: Từ `0` đến `4`.

2. **`snapshot_name` (Tùy chọn)**:
   - Tên thư mục snapshot bạn muốn khôi phục (ví dụ: `snap_epoch_5_block_4220`).
   - Nếu để trống, script sẽ tự động gọi API của node nguồn để tìm bản snapshot mới nhất (latest).

3. **`source_node_id` (Tùy chọn)**:
   - ID của node cung cấp dữ liệu snapshot để tải về thông qua HTTP server (Mặc định: `0`).
   - Script tự động đọc file `.env` tương ứng của node nguồn để lấy cổng HTTP snapshot server (`SNAPSHOT_SERVER_PORT`, Mặc định: `8600 + source_node_id`).
   - Giá trị hợp lệ: Từ `0` đến `4`.

---

## Ví dụ sử dụng

### 1. Khôi phục tự động từ snapshot mới nhất của Node 0 về Node 2
Dừng các service của Node 2, xóa dữ liệu cũ và tải snapshot mới nhất từ Node 0:
```bash
sudo bash restore_node_systemd.sh 2
```

### 2. Khôi phục Node 1 từ một snapshot cụ thể của Node 0
Khôi phục dữ liệu Node 1 bằng bản snapshot có tên `snap_epoch_1_block_50` từ Node 0:
```bash
sudo bash restore_node_systemd.sh 1 snap_epoch_1_block_50
```

### 3. Khôi phục tự động từ snapshot mới nhất của Node 1 về Node 2
Nếu bạn không biết tên snapshot cụ thể mà muốn khôi phục Node 2 lấy từ Node 1 mới nhất (truyền chuỗi rỗng `""` cho tham số thứ 2):
```bash
sudo bash restore_node_systemd.sh 2 "" 4
```

### 4. Khôi phục Node 3 từ snapshot cụ thể của Node 4 (synconly)
```bash
sudo bash restore_node_systemd.sh 3 snap_epoch_10_block_15000 4
```

---

## Quy trình khôi phục tự động của script

Khi được thực thi, script sẽ chạy qua 7 bước:

1. **🛑 Dừng các service systemd**: Dừng `metanode-rpc-N`, `metanode-consensus-N`, và `metanode-execution-N`.
2. **🗑️ Xóa dữ liệu cũ**: Xóa toàn bộ database và file log cũ tại thư mục `/opt/metanode/node-N/data/` và `/opt/metanode/node-N/logs/`.
3. **📥 Tải xuống snapshot**: Sử dụng `wget` tải dữ liệu snapshot từ HTTP server của node nguồn về thư mục tạm `/tmp`.
4. **📂 Ánh xạ dữ liệu & Phân quyền**:
   - Chuyển dữ liệu PebbleDB (`back_up/*`) vào thư mục `data/execution/backup/`.
   - Chuyển dữ liệu LevelDB/Nomt (`blocks`, `nomt_db`, v.v.) vào thư mục `data/execution/db/`.
   - 🚨 **Bảo mật split-brain**: Xóa thư mục `rust_consensus` thừa trong snapshot để ép node chạy Bootstrapping đồng bộ sạch từ Rust layer.
   - Dọn dẹp các tệp tin `LOCK` và `.lock` cũ.
   - Thực hiện `chown -R metanode:metanode` để đảm bảo các service systemd có đủ quyền ghi vào thư mục dữ liệu mới.
5. **🚀 Khởi động tuần tự**:
   - Khởi động Execution Layer (Go) trước và chờ 10 giây để Go tải cấu trúc database.
   - Khởi động Consensus Layer (Rust) tiếp theo.
   - Khởi động RPC Proxy (nếu service này có tồn tại).
6. **📊 Giám sát đồng bộ (Sync Monitor)**: Thực hiện gọi JSON-RPC liên tục trong tối đa 120s để đối chiếu `block height`, `GEI` và `epoch` của node vừa khôi phục với một node khỏe mạnh khác trong mạng.
7. **🔒 Kiểm tra khớp mã băm (Hash Divergence Check)**: Lấy băm (hash) của block hiện tại và đối chiếu với node tham chiếu để đảm bảo không bị rẽ nhánh (fork) sau khi khôi phục.
