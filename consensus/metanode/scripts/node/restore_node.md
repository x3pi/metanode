# Hướng dẫn sử dụng restore_node.sh

Script `restore_node.sh` được dùng để khôi phục (restore) một node (cả Go và Rust) từ một bản sao lưu (snapshot) một cách an toàn (sequential & fork-safe).

## Cú pháp

```bash
./restore_node.sh <node_id> [snapshot_name] [source_node_id]
```

## Các tham số

1. **`node_id` (Bắt buộc)**: 
   - ID của node bạn muốn khôi phục dữ liệu. 
   - Giá trị hợp lệ: Từ `0` đến `4`.

2. **`snapshot_name` (Tùy chọn)**:
   - Tên thư mục snapshot bạn muốn khôi phục (ví dụ: `snap_epoch_5_block_4220`).
   - Nếu để trống, script sẽ tự động gọi API để tìm bản snapshot mới nhất (latest).

3. **`source_node_id` (Tùy chọn)**:
   - ID của node cung cấp dữ liệu snapshot để tải về thông qua HTTP server.
   - Script tự động ánh xạ sang port HTTP server tương ứng của node cung cấp (Mặc định: Node 0 = port `8600`, Node 1 = port `8601`,..., Node 4 = port `8604`).
   - Nếu để trống, mặc định sẽ tải snapshot từ **node 0** (port `8600`).
   - Giá trị hợp lệ: Từ `0` đến `4`.

## Ví dụ sử dụng

### 1. Khôi phục tự động từ snapshot mới nhất của node 0
Dừng Node 2, xóa dữ liệu và tải snapshot mới nhất từ Node 0:
```bash
./restore_node.sh 2
```

### 2. Khôi phục từ một snapshot cụ thể lấy từ node 0
Khôi phục Node 1 bằng bản snapshot có tên `snap_epoch_1_block_50` từ Node 0:
```bash
./restore_node.sh 1 snap_epoch_1_block_50
```

### 3. Khôi phục từ một snapshot cụ thể lấy từ một node khác (VD: Node 4)
Khôi phục Node 3 bằng bản snapshot `snap_epoch_10_block_15000` tải về từ Node 4 (port 8604):
```bash
./restore_node.sh 3 snap_epoch_10_block_15000 4
```

### 4. Khôi phục tự động (mới nhất) từ một node khác
Nếu bạn không biết tên snapshot mà muốn khôi phục Node 2 lấy từ Node 1 mới nhất (chú ý bạn cần truyền rỗng `""` cho tham số thứ 2):
```bash
./restore_node.sh 2 "" 1
```

## Các bước hệ thống thực hiện tự động
Khi chạy, script sẽ lần lượt:
1. **Dừng Node mục tiêu**: Gửi SIGINT cho Go Master/Sub và Rust để kết thúc gracefully.
2. **Xóa TOÀN BỘ dữ liệu (Data & Logs)**: Của cả Go và Rust DAG trên Node đó.
3. **Download & Khôi phục**: Tải (hoặc sao chép) dữ liệu từ `source_node_id` và thiết lập lại các thư mục.
4. **Xác minh (Validate)**: Kiểm tra file `epoch_data_backup.json` và cấu trúc các bảng của cơ sở dữ liệu (PebbleDB, NOMT).
5. **Khởi động**: Khởi chạy lại Go Master và Go Sub tuần tự trong `tmux`.
6. **Giám sát (Sync Monitor)**: So sánh dữ liệu Block/GEI/Epoch với một Node khỏe mạnh trong vòng 120s.
7. **Kiểm tra rẽ nhánh (Hash Divergence Check)**: Xác thực Hash của block hiện tại để phòng chống fork mạng.
