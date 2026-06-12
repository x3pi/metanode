# Báo cáo Review Pull Request #1: Metanode Deployment & Configuration

Báo cáo chi tiết về việc đánh giá và kiểm thử tĩnh các thay đổi trong **PR #1** liên quan đến việc chuyển đổi cơ chế cấu hình từ `.env` sang thư mục JSON/TOML và chuyển mạng lưới sang dải IP cục bộ (`192.168.1.xxx`).

---

## 1. Tóm tắt các thay đổi trong PR
* **Đơn giản hóa cấu hình:** Loại bỏ các file cấu hình `.env` đơn lẻ (`validator.env`, `synconly.env`), thay thế bằng việc quản lý theo thư mục cấu hình hợp nhất `node-N_keys/` chứa sẵn `execution.json` và `consensus.toml`.
* **Multi-server Support:** Cập nhật toàn bộ cấu hình mạng lưới từ địa chỉ local loopback `127.0.0.1` sang các IP thực tế trong mạng nội bộ (`192.168.1.230` - `192.168.1.234`).
* **Tự động hóa:** Bổ sung kịch bản cấu hình cổng tường lửa (UFW) tự động thông qua script `open_ports.sh` sinh ra cho từng node.

---

## 2. Các lỗi nghiêm trọng & Vấn đề kỹ thuật

### 🚨 Lỗi 1: Đường dẫn tương đối không neo (CWD-dependent relative path)
> [!IMPORTANT]
> **Vị trí ảnh hưởng:** File [deploy_systemd_cluster.sh](file:///home/abc/chain-n/metanode/consensus/metanode/scripts/node/deploy_systemd_cluster.sh) tại Phase 6 (dòng 523 trong file nguyên bản/741 trong PR diff).
>
> **Chi tiết:**
> ```bash
> local_cfg_dir="../../../../deploy/node-${id}_keys"
> ```
> Biểu thức trên sử dụng đường dẫn tương đối trực tiếp. Nếu script được gọi từ thư mục gốc của dự án (Project Root) thay vì gọi bên trong thư mục chứa script, đường dẫn sẽ trỏ sai mục tiêu (ra ngoài thư mục dự án 4 cấp) và gây ra crash khi đọc file cấu hình:
> `❌ Không thể đọc rpc_port hoặc connection_address từ .../execution.json. Dừng lại!`
>
> **Giải pháp đề xuất:** Sử dụng đường dẫn tuyệt đối neo theo biến `${PROJECT_ROOT}` đã định nghĩa ở đầu script:
> ```bash
> local_cfg_dir="${PROJECT_ROOT}/deploy/node-${id}_keys"
> ```

---

### ⚠️ Lỗi 2: Lệch cấu hình cổng HTTP RPC (`server_port`) của Node 1 -> 4
> [!WARNING]
> **Chi tiết:** PR cập nhật cơ chế sinh cổng tự động trong `install-rpc-systemd.sh` và `deploy_systemd_cluster.sh` sang dải cổng mới `8650 + i` (Node 0: 8650, Node 1: 8651, ...). Tuy nhiên, các file cấu hình tĩnh lưu trên disk lại được cập nhật lệch chuẩn:
> * [config-rpc-node0.json](file:///home/abc/chain-n/metanode/execution/cmd/rpc/cmd/rpc-client/config-rpc-node0.json) được đổi đúng thành `:8650`.
> * [config-rpc-node1.json](file:///home/abc/chain-n/metanode/execution/cmd/rpc/cmd/rpc-client/config-rpc-node1.json) đổi thành `:8546` (công thức cũ `8545 + i`).
> * [config-rpc-node4.json](file:///home/abc/chain-n/metanode/execution/cmd/rpc/cmd/rpc-client/config-rpc-node4.json) đổi thành `:8549` (công thức cũ `8545 + i`).
> * File Node 2 và 3 không được thay đổi (giữ nguyên `:8548` và `:8549`).
>
> **Hệ quả:** Nếu khởi động trực tiếp RPC client mà không qua script cài đặt tự động (systemd helper), các node sẽ lắng nghe ở cổng cũ (8546, 8549, ...), gây lệch cấu hình với file `/tmp/rpc_nodes.json` dẫn đến lỗi kết nối trong các kịch bản test tự động.

#### Bảng Đối Chiếu Cấu Hình Cổng HTTP RPC:
| Node ID | Công thức đúng (PR) | Cấu hình thực tế trên disk | Trạng thái |
| :--- | :--- | :--- | :--- |
| **Node 0** | `8650` | `:8650` | ✅ Khớp |
| **Node 1** | `8651` | `:8546` | ❌ Lệch (sử dụng cổng cũ) |
| **Node 2** | `8652` | `:8548` | ❌ Lệch (chưa cập nhật) |
| **Node 3** | `8653` | `:8549` | ❌ Lệch (chưa cập nhật) |
| **Node 4** | `8654` | `:8549` | ❌ Lệch (sử dụng cổng cũ) |

* **Giải pháp đề xuất:** Cập nhật đồng bộ trường `"server_port"` trong các file `config-rpc-node*.json` tĩnh trên disk khớp với dải cổng `8650 + i`.

---

### ⚠️ Vấn đề 3: Đè định dạng `.json` của Key làm hỏng lệnh `keytool show`
> [!NOTE]
> **Vị trí ảnh hưởng:** File [gen_validator_entry.py](file:///home/abc/chain-n/metanode/deploy/gen_validator_entry.py)
>
> **Chi tiết:** Hàm `rewrite_key_as_base64` ghi đè các file JSON của key (`protocol_key.json` và `network_key.json`) thành một chuỗi Base64 thô (không còn là JSON hợp lệ).
>
> **Hệ quả:** Lệnh CLI `metanode keytool show --file protocol_key.json` sẽ bị crash do lỗi parse JSON. Đồng thời việc giữ extension `.json` cho file raw base64 dễ gây nhầm lẫn cho nhà phát triển.
>
> **Giải pháp đề xuất:** Đổi extension của file khóa xuất ra thành `.key` hoặc `.bin` thay vì `.json` (Ví dụ: `protocol_key.key`), hoặc sửa hàm parse trong Rust để đọc được cả 2 định dạng JSON và Raw.

---

### ⚠️ Vấn đề 4: Lỗi crash tiềm ẩn khi thiếu tham số trong `fetch_systemd_logs.sh`
> [!NOTE]
> **Vị trí ảnh hưởng:** File [fetch_systemd_logs.sh](file:///home/abc/chain-n/metanode/consensus/metanode/scripts/node/fetch_systemd_logs.sh)
>
> **Chi tiết:** Khi parse `--env` ở cuối tham số dòng lệnh mà không truyền file (ví dụ: `./fetch_systemd_logs.sh --env`), lệnh `shift 2` sẽ cố dịch chuyển vượt chỉ số tham số khả dụng, trả về exit code khác 0. Dưới chế độ `set -euo pipefail`, việc này sẽ crash script lập tức mà không có thông báo lỗi rõ ràng.
>
> **Giải pháp đề xuất:** Kiểm tra tham số đi kèm trước khi thực hiện `shift 2`:
> ```bash
> --env)
>     if [ -z "${2:-}" ]; then
>         log_err "Thiếu đường dẫn file env sau tham số --env"
>         exit 1
>     fi
>     ENV_FILE="$2"
>     ENV_FILE_SET=1
>     shift 2
>     ;;
> ```

---

## 3. Kết luận và Đánh giá
PR đạt yêu cầu về mặt cải tiến kiến trúc cấu hình gọn gàng và phân vùng IP mạng LAN nội bộ. Tuy nhiên, **bắt buộc phải sửa lại Lỗi 1 (Relative Path)** để tránh lỗi crash script triển khai cluster và đồng bộ lại **Lỗi 2 (Cổng RPC tĩnh)** trên disk trước khi tiến hành Merge PR vào nhánh chính.