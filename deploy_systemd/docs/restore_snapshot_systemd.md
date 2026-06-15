# Hướng dẫn sử dụng restore_snapshot_systemd.sh

Script `restore_snapshot_systemd.sh` được dùng để khôi phục (restore) một node chạy dưới dạng **systemd service** (`metanode-execution-N`, `metanode-consensus-N`, `metanode-rpc-N`) từ bản sao lưu (snapshot) của một node khác thông qua HTTP API một cách an toàn và tự động.

## Yêu cầu
- Script phải được chạy dưới quyền **root** (`sudo`) vì cần tương tác với `systemctl` và thực hiện phân quyền sở hữu thư mục dữ liệu cho user hệ thống (`metanode`).
- Phải biết **URL của Snapshot Server** từ node cung cấp dữ liệu (ví dụ: `http://192.168.1.100:8604`).

## Cú pháp

```bash
sudo bash restore_snapshot_systemd.sh --node <node_id> --snapshot-url <url> [--snapshot-name <name>]
```

## Các tham số (Flags)

1. **`--node` (hoặc `-n`) (Bắt buộc)**: 
   - ID của node bạn muốn khôi phục dữ liệu (Node đích).
   - Giá trị hợp lệ: Từ `0` đến `4`.

2. **`--snapshot-url` (hoặc `-u`) (Bắt buộc)**:
   - URL cơ sở của node cung cấp dữ liệu snapshot.
   - Ví dụ: `http://192.168.1.100:8604` (Lưu ý: Không bao gồm `/api/...` ở sau, script sẽ tự động thêm vào).

3. **`--snapshot-name` (hoặc `-s`) (Tùy chọn)**:
   - Tên thư mục snapshot cụ thể bạn muốn khôi phục (ví dụ: `snap_epoch_5_block_4220`).
   - Nếu để trống, script sẽ tự động gọi API của `snapshot-url` để tìm bản snapshot mới nhất (latest).

---

## Ví dụ sử dụng

### 1. Khôi phục tự động từ snapshot mới nhất
Dừng các service của Node 2, xóa dữ liệu cũ và tải snapshot mới nhất từ máy chủ có địa chỉ `http://192.168.1.100:8604` (ví dụ node 4 - synconly trên máy đó):
```bash
sudo bash restore_snapshot_systemd.sh --node 2 --snapshot-url http://192.168.1.100:8604
```

### 2. Khôi phục từ một snapshot cụ thể
Khôi phục dữ liệu Node 1 bằng bản snapshot có tên `snap_epoch_1_block_50`:
```bash
sudo bash restore_snapshot_systemd.sh --node 1 --snapshot-url http://192.168.1.100:8600 --snapshot-name snap_epoch_1_block_50
```

---

## Quy trình khôi phục tự động của script

Khi được thực thi, script sẽ chạy qua 7 bước:

1. **🛑 Dừng các service systemd**: Dừng `metanode-rpc-N`, `metanode-consensus-N`, và `metanode-execution-N`.
2. **🗑️ Xóa dữ liệu cũ**: Xóa toàn bộ database và file log cũ tại thư mục `/opt/metanode/node-N/data/` và `/opt/metanode/node-N/logs/`.
3. **📥 Tải xuống snapshot**: Sử dụng `wget` tải dữ liệu snapshot từ URL được chỉ định về thư mục tạm `/tmp`.
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
6. **📊 Giám sát đồng bộ (Sync Monitor)**: Thực hiện gọi JSON-RPC liên tục trong tối đa 120s để theo dõi tiến trình `block height`, `GEI` và `epoch` của node vừa khôi phục.
7. **🔒 Kiểm tra khớp mã băm (Hash Divergence Check)**: Nếu có một node khác đang chạy trên cùng máy, script sẽ tự động lấy băm (hash) của block hiện tại và đối chiếu để đảm bảo không bị rẽ nhánh (fork). Nếu không có node tham chiếu trên cùng máy, bước này sẽ được bỏ qua an toàn.
