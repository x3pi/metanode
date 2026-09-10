# 🚀 Hướng Dẫn Chạy Kiểm Thử Deploy Cụm Node (`test_remote_deploy.sh`)

Tài liệu hướng dẫn nhanh cách sử dụng script [`test_remote_deploy.sh`](./test_remote_deploy.sh) để tự động hóa toàn bộ quy trình:
1. Dọn dẹp dữ liệu cũ trên các máy chủ (`/opt/metanode/node-*`).
2. Tự lấy/đóng gói ZIP bộ cài đặt và chuyển sang máy deployer.
3. Chạy Ansible khởi tạo key, genesis, mở cổng firewall và kích hoạt dịch vụ cụm node.
4. Tự động bật hệ thống giám sát (Monitors) và gửi giao dịch RPC kiểm chứng trên chain.



### 1. Lệnh chạy thường dùng:
- **Khi vừa sửa code Go/Rust (Tự động build binaries mới + đóng gói ZIP + deploy + test)**:
  ```bash
  ./test_remote_deploy.sh --build
  ```
  *(Script sẽ tự động gọi `build_chain_bins.sh` biên dịch đủ 6 file nhị phân vào `deploy/bin/`, rồi gọi `package_deploy.sh` tạo file ZIP mới nhất và chuyển sang máy đích deploy).*

- **Khi dùng binaries và ZIP đã có sẵn (chỉ deploy & test)**:
  ```bash
  ./test_remote_deploy.sh
  ```

### 2. Các tùy chọn hữu ích khác:
- **Chỉ định máy đích nhận gói và điều phối deploy (`--target-host`)**:
  *(Mặc định nếu không truyền, script tự lấy IP máy đầu tiên trong `inventory.yml`)*:
  ```bash
  ./test_remote_deploy.sh --target-host 192.168.1.231
  # Hoặc kết hợp vừa build vừa chỉ định máy:
  ./test_remote_deploy.sh --build --target-host 192.168.1.231
  ```

- **Chỉ đóng gói lại file ZIP (không build lại code binaries)**:
  ```bash
  ./test_remote_deploy.sh --build-zip
  ```

- **Bỏ qua dọn dẹp dữ liệu cũ** (giữ lại blockchain data cũ):
  ```bash
  ./test_remote_deploy.sh --skip-clean
  ```

- **Chỉ deploy cụm node, không gửi giao dịch test**:
  ```bash
  ./test_remote_deploy.sh --skip-tx
  ```

- **Chỉ định file inventory khác**:
  ```bash
  ./test_remote_deploy.sh --inventory /path/to/custom_inventory.yml
  ```

---

### 3. Hướng dẫn chuyển sang kiểm thử bằng SSH Key (Không dùng mật khẩu)

Để không cần lưu mật khẩu plaintext trong `inventory.yml`, thực hiện theo 4 bước sau:

1. **Sinh cặp SSH Key trên máy điều phối** (nếu chưa có):
   ```bash
   ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519
   ```

2. **Copy Public Key sang tất cả các máy chủ trong cụm**:
   ```bash
   ssh-copy-id -i ~/.ssh/id_ed25519.pub abc@192.168.1.231
   ssh-copy-id -i ~/.ssh/id_ed25519.pub abc@192.168.1.223
   ```
   *(Kiểm tra kết nối: `ssh abc@192.168.1.231 "echo OK"` và `ssh abc@192.168.1.223 "echo OK"` không còn hỏi mật khẩu).*

3. **Cập nhật file `inventory.yml`**:
   - Bỏ comment hoặc thêm:
     ```yaml
     ansible_ssh_private_key_file: "~/.ssh/id_ed25519"
     ```
   - Comment (tắt) dòng `ansible_ssh_pass`.
   - Vẫn giữ `ansible_become_pass` (hoặc cấu hình `NOPASSWD` trong `/etc/sudoers.d/abc` trên các server).

4. **Chạy script kiểm thử**:
   ```bash
   ./test_remote_deploy.sh
   # Hoặc chỉ định key trực tiếp qua CLI:
   ./test_remote_deploy.sh --key ~/.ssh/id_ed25519
   ```
