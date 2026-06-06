# Hướng dẫn sử dụng deploy_systemd_cluster.sh

Script `deploy_systemd_cluster.sh` là công cụ **triển khai tự động (Orchestrator)** cho mạng lưới Metanode. Nó giúp bạn biên dịch (build) mã nguồn tại một máy chủ duy nhất (máy chính), sau đó phân phối (push) và chạy ngầm (systemd) tự động trên nhiều máy chủ khác nhau (máy con) dựa theo cấu hình.

---

## 1. Cơ chế hoạt động (Các Phase)

Khi chạy đầy đủ, script thực hiện tuần tự các bước:
1. **Phase 1 (Build):** Biên dịch mã nguồn Go (`simple_chain`), Rust (`metanode`), và RPC Proxy tại máy chính.
2. **Phase 2 (Keys & Configs):** Đọc file `.env`, tạo các file cấu hình `json`, `toml` và sinh khóa (keys) cho từng node.
3. **Phase 3 (Push):** Đóng gói và gửi các file binary, cấu hình sang các máy con thông qua `rsync`.
4. **Phase 5 (Systemd Setup):** Kết nối sang máy con, ra lệnh cài đặt và khởi động Go/Rust bằng `systemd-cluster.sh`. (Mặc định: xóa sạch data cũ).
5. **Phase 7 (RPC Setup):** Khởi động các cổng RPC Proxy bằng `install-rpc-systemd.sh`.

---

## 2. Cách dùng lệnh

**Lệnh quan trọng nhất (Cài đặt toàn bộ từ A-Z):**
```bash
./deploy_systemd_cluster.sh --env deploy-muti-node.env --all
```
*Lệnh này sẽ tự động Build -> Đẩy file qua mạng -> Tắt các node cũ -> Xóa Data -> Khởi động lại mạng lưới mới hoàn toàn.*

### Các cờ (Flags) thông dụng khác

| Lệnh | Ý nghĩa |
|:---|:---|
| `--env <file.env>` | **Bắt buộc.** Chỉ định file chứa cấu hình IP của các server. |
| `--all` | Chạy đầy đủ quy trình (Build + Push + Start). |
| `--build` | Chỉ thực hiện biên dịch mã nguồn ở máy chính. |
| `--push` | Chỉ đẩy file binary và cấu hình sang máy con. |
| `--start` | Khởi động lại các node ở máy con (nhưng **xóa** data cũ). |
| `--start --keep-data` | Khởi động lại các node nhưng **giữ nguyên** data hiện tại. |
| `--stop` | Dừng toàn bộ các tiến trình đang chạy trên toàn mạng lưới. |
| `--only-node N` | Chỉ áp dụng thao tác cho 1 Node cụ thể (thay N bằng số). |
| `--restore-node N` | Khôi phục dữ liệu từ Snapshot cho Node cụ thể (dùng `"1 2"` nếu nhiều node). Tự động tải snapshot dựa theo cấu hình `SNAPSHOT_SOURCE_NODE` trong file `.env`. Chỉ áp dụng khi tạo mới data. |

---

## 3. Các ví dụ thực tế

**Khởi động lại toàn mạng lưới nhưng KHÔNG mất dữ liệu:**
```bash
./deploy_systemd_cluster.sh --env deploy-muti-node.env --start --keep-data
```

**Chỉ build và khởi động lại duy nhất Node 2:**
```bash
./deploy_systemd_cluster.sh --env deploy-muti-node.env --all --only-node 2
```

**Dừng toàn bộ mạng lưới:**
```bash
./deploy_systemd_cluster.sh --env deploy-muti-node.env --stop
```

**Khởi động chỉ 1 Node (ví dụ Node 2):**
```bash
# Sẽ khởi động và xóa data cũ của Node 2
./deploy_systemd_cluster.sh --env deploy-muti-node.env --start --only-node 2

# Nếu muốn giữ lại data cũ của Node 2
./deploy_systemd_cluster.sh --env deploy-muti-node.env --start --keep-data --only-node 2
```

**Khôi phục CHỈ DUY NHẤT một Node từ Snapshot (ví dụ khôi phục Node 3):**
*Lưu ý: Bạn cần phải cấu hình `SNAPSHOT_SOURCE_NODE` (ví dụ `4`) và `SNAPSHOT_SERVER_PORT` trong file `.env` trước.*
```bash
# Cờ --restore-node 3 giờ đây đã hoạt động độc lập (không cần --start hay --only-node)
# Lệnh này sẽ tự SSH sang server chứa Node 3 và gọi script restore_snapshot_systemd.sh
./deploy_systemd_cluster.sh --env deploy-muti-node.env --restore-node 2
```

> **💡 Mẹo:** Nếu máy chưa có script restore mới nhất, bạn có thể kèm thêm `--push`:
> `./deploy_systemd_cluster.sh --env deploy-muti-node.env --push --restore-node 3`
> Hoặc nếu bạn đã ở sẵn bên trong máy chứa Node 3, bạn chạy trực tiếp luôn cho khỏe:
> `sudo bash restore_snapshot_systemd.sh --node 3 --snapshot-url http://192.168.1.230:8604`

**Dừng chỉ 1 Node (ví dụ Node 2):**
```bash
./deploy_systemd_cluster.sh --env deploy-muti-node.env --stop --only-node 2
```

---

## 4. Kiểm tra trạng thái sau khi Deploy

Vì kịch bản này điều khiển `systemd`, bạn có thể kiểm tra các tiến trình đang chạy tại bất kỳ máy con nào bằng các lệnh:

```bash
# Xem Execution Engine (Go)
sudo systemctl status metanode-execution-0

# Xem Consensus Engine (Rust)
sudo systemctl status metanode-consensus-0

# Xem RPC Proxy (EVM)
sudo systemctl status metanode-rpc-0
```

---

## 5. Thu thập Logs tự động từ các máy con (Systemd Logs)

Nếu bạn gặp lỗi hoặc cần kiểm tra log của các node đang chạy trên systemd qua nhiều server, bạn có thể dùng script `fetch_systemd_logs.sh` để tải toàn bộ log về máy tính hiện tại.

**Cách sử dụng:**
```bash
./fetch_systemd_logs.sh --env deploy-muti-node.env
```

**Hoạt động của script:**
- Đọc file cấu hình `.env` để biết danh sách các server.
- Tự động SSH vào từng server và tải về:
  - File log vật lý của Execution và Consensus (`/opt/metanode-<id>/logs/`).
  - Log từ `journalctl` của cả 3 services: `metanode-execution-<id>`, `metanode-consensus-<id>`, và `metanode-rpc-<id>`.
- Toàn bộ log sẽ được gom gọn lại trong thư mục nội bộ `logs_systemd/run_YYYYMMDD_HHMMSS/` tại máy hiện tại để bạn dễ dàng mở và debug.
