# 🚀 HƯỚNG DẪN SỬ DỤNG `auto_rebuild_deploy.sh`

Tài liệu hướng dẫn sử dụng công cụ Git Auto-Deploy Watcher cho cụm Metanode.

---

## 🎯 1. Mục Đích & Cơ Chế Hoạt Động

`auto_rebuild_deploy.sh` là daemon chạy ngầm định kỳ (mỗi 5 giây) kiểm tra commit mới trên Git remote:
- **Khi vừa bật script:** Ghi nhận commit hiện tại của local làm mốc ban đầu, **KHÔNG restart server ngay**.
- **Khi có commit mới trên remote:** Tự động `git pull`, kích hoạt `./ansible_deploy.sh --restart` và cập nhật mã commit đã deploy vào file `.last_deployed_commit` để tránh lặp lại.
- **Hoàn toàn KHÔNG chạy test suites** (không tốn tài nguyên chạy benchmark TPS, spam hay chaos restart).

---

## ⚡ 2. Các Lệnh Điều Khiển Nhanh

Đứng tại thư mục `deploy/ansible`:
```bash
cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible
```

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./auto_rebuild_deploy.sh --branch main --daemon` | **Bật watcher chạy ngầm** theo dõi nhánh `main` |
| `./auto_rebuild_deploy.sh start` | Khởi động watcher chạy ngầm (tự nhận nhánh hiện tại) |
| `./auto_rebuild_deploy.sh stop` | **Dừng watcher** và dọn dẹp tiến trình con sạch sẽ |
| `./auto_rebuild_deploy.sh status` | Xem trạng thái hoạt động (Đang chạy / Đã dừng, PID, commit mốc) |
| `./auto_rebuild_deploy.sh logs` | **Xem log trực tiếp theo thời gian thực** (`Ctrl+C` để thoát) |

---

## 📋 3. Chi Tiết Cách Sử Dụng

### 🔹 Khởi động theo dõi nhánh `main` (Khuyên dùng)
```bash
# Chạy ngầm dưới nền:
./auto_rebuild_deploy.sh --branch main --daemon

# Hoặc chạy trực tiếp trên terminal (xem log xuất ra màn hình):
./auto_rebuild_deploy.sh --branch main
```

### 🔹 Kiểm tra trạng thái hoạt động
```bash
./auto_rebuild_deploy.sh status
```
*Kết quả mẫu:*
```text
==========================================================
📊 TRẠNG THÁI GIT AUTO-DEPLOY WATCHER
==========================================================
🟢 Trạng thái : ĐANG CHẠY (PID: 1800332)
📌 Commit mốc : 552d2e5df2fe2f749511814aa3fd7df5120d9463
📜 File log   : .../deploy/ansible/auto_deploy.log
==========================================================
```

### 🔹 Dừng watcher
```bash
./auto_rebuild_deploy.sh stop
```
*Kết quả mẫu:*
```text
🛑 Đang dừng Auto-Deploy Watcher (PID: 1800332)...
✅ Watcher đã được dừng thành công.
```

---

## ⚙️ 4. Các Tham Số Tùy Chọn (Flags)

- `--branch <tên_nhánh>`: Chỉ định nhánh Git remote cần theo dõi (mặc định: tự động lấy nhánh đang checkout hoặc `main`).
- `-d`, `--daemon`: Bật chế độ chạy ngầm background, ghi log vào `auto_deploy.log`.
- `--initial-deploy`: Ép buộc chạy deploy/restart 1 lần ngay khi vừa bật script (mặc định: tắt, chỉ lưu mốc commit).
- Có thể truyền kèm các cờ của Ansible, ví dụ:
  ```bash
  ./auto_rebuild_deploy.sh --branch main --daemon --only-node 1
  ```

---

## 📂 5. Các File Liên Quan

- `auto_deploy.pid`: Lưu PID tiến trình daemon đang chạy.
- `auto_deploy.log`: Lưu toàn bộ log kiểm tra và log deploy.
- `.last_deployed_commit`: Lưu mã SHA-1 của commit đã restart gần nhất để đối chiếu.
