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
./deploy_systemd_cluster.sh --env deploy-3nodes.env --all
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

---

## 3. Các ví dụ thực tế

**Khởi động lại toàn mạng lưới nhưng KHÔNG mất dữ liệu:**
```bash
./deploy_systemd_cluster.sh --env deploy-3nodes.env --start --keep-data
```

**Chỉ build và khởi động lại duy nhất Node 2:**
```bash
./deploy_systemd_cluster.sh --env deploy-3nodes.env --all --only-node 2
```

**Dừng toàn bộ mạng lưới:**
```bash
./deploy_systemd_cluster.sh --env deploy-3nodes.env --stop
```

**Khởi động chỉ 1 Node (ví dụ Node 2):**
```bash
# Sẽ khởi động và xóa data cũ của Node 2
./deploy_systemd_cluster.sh --env deploy-3nodes.env --start --only-node 2

# Nếu muốn giữ lại data cũ của Node 2
./deploy_systemd_cluster.sh --env deploy-3nodes.env --start --keep-data --only-node 2
```

**Dừng chỉ 1 Node (ví dụ Node 2):**
```bash
./deploy_systemd_cluster.sh --env deploy-3nodes.env --stop --only-node 2
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
