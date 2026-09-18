# 🚀 HƯỚNG DẪN SỬ DỤNG `auto_rebuild_deploy.sh`

Tài liệu hướng dẫn sử dụng công cụ **Git Auto-Rebuild & Deploy Daemon** cho cụm blockchain Metanode.

---

## 🎯 1. Mục Đích & Cơ Chế Hoạt Động (2-Phase Architecture)

`auto_rebuild_deploy.sh` là daemon chạy ngầm định kỳ (mỗi 5 giây) kiểm tra commit mới trên Git remote theo mô hình **2 pha (Kiểm tra trước - Triển khai sau)** nhằm đảm bảo an toàn tuyệt đối cho mạng lưới:

### 🔹 Pha 1: Phát hiện & Kiểm thử biên dịch (Build Verification)
- **Phát hiện Commit:** Khi dev push commit mới lên Git remote (`origin/main`), script lập tức phát hiện.
- **Báo cáo Telegram tức thì:** Gửi thông báo chi tiết mã commit, tác giả, lời nhắn commit và trạng thái đang kéo code.
- **Tự động kéo code (`git pull --rebase`):** Tự động rebase code mới về máy chủ mà không làm gián đoạn hay sinh merge commit rác.
- **Chạy kiểm tra biên dịch (`build_check.sh`):** Tự động biên dịch thử toàn bộ hệ thống (EVM & NOMT FFI, Rust Consensus, Go Execution) ở môi trường cách ly:
  - **✅ Nếu Build THÀNH CÔNG:** Gửi thông báo Telegram xác nhận mã nguồn hợp lệ, sẵn sàng cho lịch deploy ban đêm.
  - **❌ Nếu Build THẤT BẠI:** Bắn cảnh báo đỏ lên Telegram kèm trích xuất đoạn log lỗi, đồng thời **TỰ ĐỘNG KHÓA / TẠM HOÃN** lịch deploy tối nay để ngăn ngừa đưa code lỗi vào mạng lưới.
- **Không gián đoạn mạng lưới:** Toàn bộ quá trình build check diễn ra độc lập, các node blockchain đang chạy vẫn hoạt động bình thường 100%.

### 🔹 Pha 2: Triển khai theo lịch hẹn (Scheduled Deployment)
- **Khung giờ mặc định:** **`21:00` hàng ngày theo giờ Việt Nam (`Asia/Ho_Chi_Minh` / GMT+7)** (có thể tùy chỉnh qua cờ `--at`).
- **Triển khai an toàn:** Đúng 21:00:00, nếu bản commit mới nhất đã vượt qua Build Check, script mới kích hoạt `./ansible_deploy.sh --start --fast` để cập nhật binary và restart toàn bộ cụm node.
- **Downtime tối thiểu (Zero-downtime):** Nhờ đã được biên dịch sẵn từ ban ngày, thời gian restart toàn bộ cụm node diễn ra siêu tốc (chỉ mất 5 - 10 giây).

---

## 🔒 2. Cơ Chế Khóa Độc Quyền (Singleton Guarantee)

Script được tích hợp **3 tầng bảo vệ chống chạy trùng lặp**:
1. **Global Singleton Check:** Kiểm tra toàn cục trước mọi chế độ thực thi (cả background daemon lẫn chạy trực tiếp foreground). Nếu đã có tiến trình chạy, mọi lệnh gọi sau sẽ bị từ chối ngay lập tức kèm cảnh báo PID.
2. **File Lock cấp Linux Kernel (`flock`):** Khóa độc quyền tệp `auto_deploy.lock` thông qua cơ chế `flock` của hệ điều hành Linux, triệt tiêu 100% race condition.
3. **Dọn dẹp triệt để (`cmd_stop`):** Khi gọi `stop`, script quét sạch các tiến trình cha và con (`sleep`, `git`, `build_check`), thu hồi file lock và PID file sạch sẽ.

---

## ⚡ 3. Các Lệnh Điều Khiển Nhanh

Đứng tại thư mục `deploy/ansible`:
```bash
cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible
```

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./auto_rebuild_deploy.sh start` | **Khởi động watcher chạy ngầm** (Mặc định hẹn giờ deploy lúc `21:00` VN) |
| `./auto_rebuild_deploy.sh start --at 22:30` | Khởi động watcher hẹn giờ deploy vào thời điểm khác (ví dụ: `22:30`) |
| `./auto_rebuild_deploy.sh start --at ""` | Khởi động watcher ở chế độ **Deploy ngay lập tức** sau khi build pass |
| `./auto_rebuild_deploy.sh stop` | **Dừng watcher** và dọn dẹp sạch sẽ toàn bộ tiến trình con |
| `./auto_rebuild_deploy.sh status` | Xem trạng thái hoạt động (Đang chạy / Đã dừng, PID, commit đã deploy) |
| `./auto_rebuild_deploy.sh logs` | **Xem log trực tiếp theo thời gian thực** (`Ctrl+C` để thoát) |

---

## 📋 4. Chi Tiết Các Kịch Bản Sử Dụng

### 🔹 Kịch bản 1: Chạy chuẩn sản xuất (Hẹn giờ 21h00 tối) — *Khuyên dùng*
```bash
./auto_rebuild_deploy.sh start
```
*Hệ thống sẽ chạy ngầm, ban ngày có commit mới sẽ tự kéo về build kiểm tra và bắn Telegram. Đúng 21:00 tối mới khởi động lại các node.*

### 🔹 Kịch bản 2: Hẹn giờ deploy vào khung giờ tùy chọn
```bash
# Hẹn giờ 23:00 đêm:
./auto_rebuild_deploy.sh start --at 23:00

# Chỉ định rõ nhánh git cần theo dõi:
./auto_rebuild_deploy.sh start --branch main --at 21:00
```

### 🔹 Kịch bản 3: Chế độ Continuous Deployment (Có commit là build & deploy ngay)
```bash
./auto_rebuild_deploy.sh start --at ""
```
*Ở chế độ này, mỗi khi có commit mới trên remote, script kéo về $\rightarrow$ build check pass $\rightarrow$ restart deploy node ngay lập tức.*

### 🔹 Kịch bản 4: Kiểm tra trạng thái hoạt động
```bash
./auto_rebuild_deploy.sh status
```
*Kết quả mẫu:*
```text
==========================================================
📊 TRẠNG THÁI GIT AUTO-DEPLOY WATCHER
==========================================================
🟢 Trạng thái       : ĐANG CHẠY (PID: 4138714)
📌 Commit đã deploy : adefb682
📍 Commit hiện tại  : 5d7ada7b
📜 File log         : /home/abc/nhat/consensus-chain/metanode/deploy/ansible/auto_deploy.log
==========================================================
```

### 🔹 Kịch bản 5: Dừng hoàn toàn tiến trình
```bash
./auto_rebuild_deploy.sh stop
```
*Kết quả mẫu:*
```text
🛑 Đang dừng Auto-Deploy Watcher (PID: 4138714)...
✅ Watcher đã được dừng thành công.
```

---

## 🔔 5. Tích Hợp Thông Báo Telegram Tự Động

Script tự động nạp cấu hình từ file `.env` (hoặc `inventory.yml`) tại `deploy/ansible/.env`:
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_CHAT_ID`

Các thông báo bot tự động gửi lên nhóm Telegram:
1. **Phát hiện Commit mới:**
   ```text
   🔔 [Phát hiện Commit mới trên Remote]
   Hệ thống phát hiện commit mới trên nhánh main:
   • Commit: e8e88e9d
   • Tác giả: Nguyen Van A
   • Nội dung: fix(consensus): optimize commit voting
   🔄 Hành động: Đang tự động kéo mã nguồn về và chạy Build Check...
   ⏰ Lưu ý: Hệ thống sẽ tự động deploy vào lúc 21:00 (Asia/Ho_Chi_Minh).
   ```
2. **Kết quả Build Check thành công:**
   ```text
   ✅ [Build Check THÀNH CÔNG]
   • Commit: e8e88e9d
   • Thời gian kiểm tra: 68s
   • Trạng thái: Đã biên dịch sạch (Go + Rust + FFI)!
   ⏰ Lịch deploy: Đã sẵn sàng deploy vào lúc 21:00 (Asia/Ho_Chi_Minh). Các node hiện tại vẫn hoạt động bình thường.
   ```
3. **Cảnh báo Build Check thất bại (Khóa deploy):**
   ```text
   ❌ [CẢNH BÁO: Build Check THẤT BẠI]
   • Commit: e8e88e9d
   • Thời gian kiểm tra: 15s
   • Chi tiết lỗi:
   error[E0425]: cannot find value `foo` in this scope
   ⚠️ Cảnh báo: Lịch deploy lúc 21:00 sẽ BỊ TẠM HOÃN để bảo vệ mạng lưới!
   ```
4. **Đến giờ hẹn triển khai (21:00):**
   ```text
   🚀 [Đến Giờ Hẹn Deploy 21:00]
   Đã đến lịch hẹn! Tiến hành triển khai commit e8e88e9d (đã vượt qua Build Check) lên toàn bộ cụm node...
   ```

---

## ⚙️ 6. Bảng Tham Số Tùy Chọn (Flags)

| Tham số | Giá trị mặc định | Mô tả |
| :--- | :--- | :--- |
| `start`, `-d`, `--daemon` | `false` | Khởi chạy watcher ở chế độ chạy ngầm background |
| `--at <HH:MM>`, `--schedule` | `"21:00"` | Hẹn giờ tự động deploy theo múi giờ `Asia/Ho_Chi_Minh` (GMT+7). Truyền `""` nếu muốn deploy ngay sau khi build pass |
| `--branch <nhánh>` | Nhánh hiện tại / `main` | Nhánh Git remote cần theo dõi |
| `--initial-deploy` | `false` | Ép buộc deploy ngay lập tức 1 lần khi vừa bật script |
| `stop` | - | Dừng toàn bộ tiến trình daemon và tiến trình con |
| `status` | - | Kiểm tra trạng thái sống/chết, PID, commit |
| `logs` | - | Xem file log trực tiếp |

---

## 📂 7. Các File Liên Quan

- `auto_deploy.pid`: Lưu PID của tiến trình watcher đang chạy.
- `auto_deploy.lock`: Khóa tệp độc quyền cấp Linux kernel (`flock`) chống chạy trùng lặp.
- `auto_deploy.log`: Ghi nhận toàn bộ nhật ký theo dõi, fetch git, build check và deploy.
- `.last_deployed_commit`: Lưu mã hash SHA-1 của commit đã deploy thành công gần nhất.
- `consensus/metanode/scripts/build_check.sh`: Công cụ chạy kiểm thử biên dịch độc lập (EVM, Rust, Go).
