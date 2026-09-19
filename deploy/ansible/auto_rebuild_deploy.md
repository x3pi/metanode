# 🚀 HƯỚNG DẪN SỬ DỤNG `auto_rebuild_deploy.sh`

Tài liệu hướng dẫn sử dụng công cụ **Git Auto-Rebuild & Deploy Daemon** cho cụm blockchain Metanode.

---

## 🎯 1. Mục Đích & Cơ Chế Hoạt Động

`auto_rebuild_deploy.sh` là daemon chạy ngầm định kỳ (mỗi 5 giây) kiểm tra commit mới trên Git remote với 2 chế độ hoạt động:

### 🔹 Chế độ 1: Hẹn giờ deploy ban đêm (`--at 21:00`) — An Toàn & Sạch Sẽ Cho Dev
- **Ban ngày:** Khi có commit mới trên Git remote (`origin/main`), watcher phát hiện và gửi thông báo Telegram xác nhận đã ghi nhận commit mới.
- **KHÔNG kéo code về trước:** Tuyệt đối **không** đụng chạm hay pull vào local working tree trong giờ làm việc. Thư mục mã nguồn trên máy chủ được giữ nguyên vẹn 100%, không gây xung đột với công việc của dev.
- **Đúng giờ hẹn (21:00 tối):** Script mới tự động thực thi:
  1. `git pull --rebase`: Kéo mã nguồn mới nhất từ remote về.
  2. Kích hoạt `./ansible_deploy.sh deploy --all --fast` (đã tích hợp sẵn quy trình biên dịch Go + Rust + EVM FFI; nếu compile fail sẽ dừng ngay lập tức và giữ nguyên cụm node an toàn).

### 🔹 Chế độ 2: Deploy ngay lập tức (Mặc định khi không có `--at`)
- Daemon liên tục theo dõi remote: Khi phát hiện commit mới trên remote $\rightarrow$ Kéo về ngay $\rightarrow$ Kích hoạt `./ansible_deploy.sh deploy --all --fast` để cập nhật và khởi động lại cụm node.
- Script chỉ theo dõi commit từ remote, **không tự động deploy code local** chưa push lên Git để tránh gián đoạn công việc dev.

### 🔹 Chế độ 3: Kích hoạt thủ công ngay lập tức (`run-now`)
- Bất cứ khi nào bạn muốn deploy ngay lập tức (dù đang hẹn giờ hay daemon đang dừng):
  `./auto_rebuild_deploy.sh run-now`
  Script sẽ tự động kéo code mới nhất từ remote về và kích hoạt deploy ngay lập tức.

---

## 🔒 2. Cơ Chế Khóa Độc Quyền & An Toàn Tuyệt Đối (Zero-Fork & Mutex Guarantee)

Script được tích hợp **4 tầng bảo vệ chống chạy trùng lặp và xung đột mã nguồn**:
1. **Daemon Singleton Check:** Kiểm tra toàn cục trước khi khởi chạy watcher daemon. Khóa độc quyền tệp `auto_deploy.lock` (trên file descriptor riêng) thông qua cơ chế `flock` của Linux kernel, đảm bảo chỉ có duy nhất 1 daemon hoạt động.
2. **Operation Mutex Lock (`.deploy_operation.lock`):** Mỗi khi diễn ra thao tác kéo code / biên dịch / deploy (cả từ daemon lẫn từ lệnh thủ công `run-now`), script chiếm quyền khóa Mutex độc quyền (non-blocking). Nếu một tiến trình deploy đang chạy thì tiến trình kia sẽ bị từ chối hoặc trì hoãn sang chu kỳ sau, triệt tiêu 100% race condition tranh chấp git hoặc build nhị phân.
3. **Clean HEAD Deployment & Fail-Closed Stash Guard:** Mọi đợt build và deploy chỉ diễn ra trên working tree 100% sạch tương ứng đúng với Git HEAD. Nếu local có thay đổi chưa commit, script sẽ tạm stash chúng trước; nếu stash thất bại sẽ lập tức dừng deploy (fail-closed). Quá trình biên dịch và restart chỉ chạy trên mã nguồn sạch của commit remote. Sau khi deploy và kiểm thử giao dịch hoàn tất, script mới phục hồi lại các thay đổi local (`git stash pop`), đảm bảo binary trên cụm node phản ánh chính xác 100% commit SHA được ghi nhận.
4. **Post-Deploy Transaction Verification (Kiểm thử giao dịch tự động):** Sau khi hoàn tất restart cụm node, hệ thống đợi 10 giây để mạng lưới ổn định, sau đó tự động kích hoạt script test giao dịch (`metanode-suite/scripts/rpc-tcp-simple.sh`) lần lượt trên **Node 1** và **Node 0**:
   - Nếu cả 2 node PASS: Gửi thông báo Telegram xác nhận mạng hoạt động bình thường.
   - Nếu thất bại: Gửi thông báo Telegram báo lỗi chi tiết kèm **20 dòng log cuối cùng** để xử lý kịp thời.
5. **Dọn dẹp triệt để (`cmd_stop`):** Khi gọi `stop`, script quét sạch các tiến trình cha và con, thu hồi toàn bộ file lock và PID file sạch sẽ.

---

## ⚡ 3. Các Lệnh Điều Khiển Nhanh

Đứng tại thư mục `deploy/ansible`:
```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible
```

| Lệnh | Ý nghĩa |
| :--- | :--- |
| `./auto_rebuild_deploy.sh start` | **Khởi động watcher mặc định: Deploy ngay lập tức** khi có commit mới và build check pass (không hẹn giờ) |
| `./auto_rebuild_deploy.sh start --at 21:00` | Khởi động watcher **Hẹn giờ deploy** vào lúc 21:00 tối (không kéo code về trước, đúng giờ mới kéo & deploy) |
| `./auto_rebuild_deploy.sh run-now` | **Kích hoạt deploy ngay lập tức** bản commit mới nhất (không cần chờ giờ hẹn) |
| `./auto_rebuild_deploy.sh run-now --force` | Ép buộc build và deploy lại commit hiện tại |
| `./auto_rebuild_deploy.sh stop` | **Dừng watcher** và dọn dẹp sạch sẽ toàn bộ tiến trình con |
| `./auto_rebuild_deploy.sh status` | Xem trạng thái hoạt động (Đang chạy / Đã dừng, Chế độ, PID, commit đã deploy) |
| `./auto_rebuild_deploy.sh logs` | **Xem log trực tiếp theo thời gian thực** (`Ctrl+C` để thoát) |
| `./auto_rebuild_deploy.sh help` | Hiển thị menu trợ giúp cú pháp |

---

## 📋 4. Chi Tiết Các Kịch Bản Sử Dụng

### 🔹 Kịch bản 1: Mặc định — Deploy ngay lập tức (Continuous Deployment - Không hẹn giờ)
```bash
./auto_rebuild_deploy.sh start
```
*Hệ thống liên tục theo dõi remote: Mỗi khi có commit mới trên remote $\rightarrow$ Kéo về $\rightarrow$ Chạy Build Check (Go + Rust + FFI) độc lập $\rightarrow$ Nếu PASS sẽ tự động kích hoạt deploy và khởi động lại cụm node ngay lập tức.*

### 🔹 Kịch bản 2: Hẹn giờ deploy ban đêm (Ví dụ: 21h00 tối)
```bash
./auto_rebuild_deploy.sh start --at 21:00
```
*Hệ thống chạy ngầm theo dõi remote mà KHÔNG làm thay đổi working tree local ban ngày. Đúng 21:00 tối mới kéo code về, biên dịch kiểm tra và deploy lên cluster.*

### 🔹 Kịch bản 3: Kích hoạt triển khai ngay lập tức (Run Now)
```bash
./auto_rebuild_deploy.sh run-now
```
*Kéo code mới nhất từ remote, kiểm tra build và deploy ngay lập tức.*

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
