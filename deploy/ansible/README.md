# Hệ Thống Triển Khai Metanode (Ansible Edition)

Tài liệu này bao gồm 2 phần:
- **Phần 1:** Cẩm nang lệnh chạy siêu tốc & Giải thích chi tiết trình tự xử lý của từng lệnh.
- **Phần 2:** Giải thích chuyên sâu về kiến trúc 6 Roles của Ansible.

---

## Phần 1: Cẩm Nang Lệnh Chạy (Cheat Sheet)

Bạn **KHÔNG CẦN** phải gõ lệnh `ansible-playbook` dài dòng nữa. Hãy sử dụng file script bọc ngoài `ansible_deploy.sh` nằm trong thư mục `ansible/`.

> **Lưu ý:** Mọi cấu hình về IP, tài khoản SSH, mật khẩu đều được tự động lấy từ file `inventory.yml`. Bạn chỉ cần sửa file đó 1 lần duy nhất!

### 📌 Tổng hợp các Cờ (Flags) và Giá trị Mặc định

Dưới đây là danh sách đầy đủ các tham số cấu hình mà bạn có thể truyền vào khi chạy `ansible_deploy.sh`:

| Tham số (Flag) | Giá trị Mặc định | Ý nghĩa & Tác dụng |
| --- | --- | --- |
| `--start` | Được gọi tự động nếu chạy không tham số | Khởi động Node, cập nhật file chạy (binary) mới nhất. **Giữ nguyên** Dữ liệu (Database) và Chìa khóa (Keys). |
| `--reset-all` | N/A | Chế độ cài mới. **Xóa sạch** toàn bộ Dữ liệu cũ, đúc lại bộ Chìa khóa/Genesis mới và khởi động mạng lưới mới tinh. |
| `--stop` | N/A | Gửi lệnh dừng an toàn (Stop) đến toàn bộ các dịch vụ (Execution, Consensus, RPC Proxy) đang chạy. |
| `--clean` | `false` | Ép hệ thống **Xóa sạch Database cũ** nhưng KHÔNG đúc lại chìa khóa (Keys) mới. Hữu ích khi bạn muốn reset Blockchain về block 0 nhưng vẫn giữ nguyên danh tính Node. Kết hợp: `--start --clean`. |
| `--only-node N` | `all` (Mặc định chạy trên tất cả các node) | Chỉ định thực hiện các thao tác (start, stop, reset) **DUY NHẤT** trên máy chủ chứa Node số `N`. |
| `--restore-node N`| `none` (Không thực hiện khôi phục) | Cờ đặc biệt: Báo hiệu sẽ khôi phục dữ liệu cho Node `N`. Hệ thống sẽ tải Snapshot và giải nén vào thư mục `data`. Thường kết hợp với `--reset-all`. |
| `--snapshot-url` | Rỗng (`""`) | Cung cấp đường link tải Snapshot (Ví dụ: `http://192.168.1.230:8604`). Bắt buộc đi kèm khi sử dụng `--restore-node`. |
| `--open-ports` | `false` (Không mở port) | Gọi script cấu hình Firewall (UFW) trên Server để mở thông tất cả các cổng (P2P, RPC, Metrics...). Thường chỉ chạy 1 lần lúc cài đặt máy chủ mới. |
| `--debug-cpp` | `false` | Ép trình biên dịch C++ (EVM Linker) build ở chế độ Debug (`-O0 -g`) thay vì Release (`-O3`). Dùng khi cần `gdb` dò lỗi CGO. |

---

### 1. Cài đặt mới từ đầu (Tạo Keys, Cấu hình, Xóa DATA)
Để tạo cấu hình mới, sinh lại chìa khóa (Keys) và làm sạch toàn bộ dữ liệu cũ từ đầu đến cuối, bạn hãy dùng cờ `--reset-all`.
```bash
./ansible_deploy.sh --reset-all
```
**Lệnh này sẽ làm gì?**
1. **Build Code:** Biên dịch mã nguồn (Rust/Go) mới nhất trên máy tính cá nhân.
2. **Generate Keys:** Đúc một bộ chìa khóa (Keys/Genesis) mới tinh cho các Node.
3. **Stop Nodes:** Tắt toàn bộ các ứng dụng (Consensus/Execution) đang chạy trên các máy Server để nhả file lock.
4. **Clean Data (Xóa sạch DB):** Xóa rỗng thư mục `data` và `logs` để phá bỏ Blockchain cũ.
5. **Copy:** Chép đè mã nguồn và bộ chìa khóa mới lên Server.
6. **Start Nodes:** Bật các ứng dụng lên để chạy một mạng lưới Blockchain hoàn toàn mới.

---

### 2. Khởi động / Cập nhật Code (GIỮ NGUYÊN KEYS & DATA)
Nếu bạn chỉ thay đổi mã nguồn hoặc chỉnh sửa service mà muốn cập nhật nhưng KHÔNG làm hỏng Blockchain hiện tại:
```bash
./ansible_deploy.sh --start
```
**Lệnh này sẽ làm gì?**
1. **Build Code:** Biên dịch mã nguồn mới nhất trên máy tính cá nhân.
2. ⏭️ *(BỎ QUA Generate Keys)*
3. **Stop Nodes:** Tắt toàn bộ các ứng dụng đang chạy trên Server.
4. ⏭️ *(BỎ QUA Clean Data - Giữ nguyên Database và cấu hình cũ)*
5. **Copy:** Chỉ chép đè file chạy (Binary) mới lên Server. Các file Keys không thay đổi.
6. **Start Nodes:** Bật các ứng dụng lên lại. Mạng lưới sẽ chạy phiên bản code mới nhất trên nền dữ liệu Blockchain cũ.

---

### 3. Tắt toàn bộ mạng lưới
```bash
./ansible_deploy.sh --stop
```
**Lệnh này sẽ làm gì?**
1. SSH kết nối vào các máy chủ Server.
2. Gửi lệnh tắt hệ thống an toàn (`systemctl stop`) cho các tiến trình `metanode-execution` và `metanode-consensus`.
3. Kết thúc (Không build code, không copy, không đụng chạm data).

---

### 4. Chỉ thao tác trên một Node duy nhất
Bạn có thể kết hợp cờ `--only-node N` vào bất kỳ lệnh nào ở trên. Hệ thống sẽ tự động lọc thông tin và chỉ tác động đến duy nhất máy chủ chứa Node đó.
**Ví dụ: Bạn chỉ muốn tắt riêng Node 2:**
```bash
./ansible_deploy.sh --stop --only-node 2
```
**Hoặc muốn cài đặt lại (xóa data) duy nhất Node 2 (trong khi các node khác chạy bình thường):**
```bash
./ansible_deploy.sh --reset-all --only-node 2
```

---

### 5. Khôi phục Node từ Snapshot
Giả sử Node 2 bị hỏng data, bạn muốn cài đặt lại Node 2 và tự động kéo dữ liệu Snapshot từ máy có IP `192.168.1.230` cổng `8604`:
```bash
./ansible_deploy.sh --reset-all --only-node 2 --restore-node 2 --snapshot-url http://192.168.1.230:8604
```
**Lệnh này sẽ làm gì?**
Thực hiện toàn bộ quy trình của lệnh `--reset-all` đối với Node 2. Tuy nhiên, thay vì bật máy lên ngay để Node 2 tự sync từ khối số 0, nó sẽ mồi (gọi bash script) tải file Snapshot từ URL được cấp và bung nén thẳng vào thư mục `data` của Node 2 trước khi Start Node.

---

### 6. Tự động khởi động RPC Proxy & Mở Tường Lửa (Firewall)
Hệ thống Ansible giờ đây sẽ lo trọn gói việc khởi động **Proxy RPC** và định tuyến tường lửa cho bạn. 
- Mặc định sau quá trình deploy (chạy `--start` hoặc `--reset-all`), service `metanode-rpc-<id>.service` sẽ được tự động cấu hình và bật song song với hai tiến trình chính.
- Nếu bạn cài đặt trên một cụm Server mới tinh và muốn mở port Firewall (UFW) một cách tự động, hãy thêm cờ `--open-ports` vào lệnh:
```bash
./ansible_deploy.sh --start --open-ports
# Hoặc kết hợp với reset:
./ansible_deploy.sh --reset-all --open-ports
```
**Lệnh này sẽ làm gì?**
Thực thi script `open_ports.sh` trên từng máy chủ tương ứng để tự động thêm rule `ufw allow` cho tất cả các cổng cần thiết (Execution, Consensus, RPC, Snapshot, Metrics). Vì Firewall chỉ cần mở 1 lần duy nhất, bạn không cần dùng cờ này trong các lần cập nhật tiếp theo.

---

## Phần 2: Kiến Trúc Ansible Hoạt Động Như Thế Nào?

**Ansible** là một công cụ "Quản lý Cấu hình và Triển khai" cực kỳ mạnh mẽ. Hệ thống triển khai của Metanode đã được thiết kế thành **6 Roles (Phân hệ)** hoạt động theo một trình tự tuyến tính nghiêm ngặt để đảm bảo an toàn tuyệt đối cho dữ liệu:

### 1. `local_build` (Chạy ở máy cá nhân)
- Không kết nối đi đâu cả.
- Kích hoạt script để compile mã nguồn Rust/Go. Đóng gói toàn bộ thành file `metanode-deploy.tar.gz`.
- *(Tùy chọn nếu gọi lệnh Setup)*: Chạy mã Python để sinh ra Keys (chứng chỉ bảo mật) và định hình file `genesis.json`.

### 2. `stop_services` (Dừng tiến trình)
- SSH song song vào tất cả các server đích.
- Dừng ngầm các service `metanode-execution` và `metanode-consensus` để nhả khóa file (file lock).

### 3. `clean_data` (Dọn dẹp tùy chọn)
- Nếu lệnh chạy của bạn là `--start` (giữ data), Ansible sẽ **bỏ qua (skip)** role này.
- Nếu lệnh là `--setup`, nó sẽ xóa sạch hai thư mục `data` và `logs`.

### 4. `node_setup` (Phân phối cấu hình)
- Giải nén tệp `metanode-deploy.tar.gz` trên remote server.
- Phân phối "ai về nhà nấy": Đẩy chính xác file cấu hình và Keys vào từng thư mục Node tương ứng. Đảm bảo node nào chỉ cầm khóa của node đó.

### 5. `snapshot_restore` (Tải dữ liệu Snapshot)
- Role đặc biệt chỉ kích hoạt khi có cờ `--restore-node`. Chạy lệnh ngầm tải trực tiếp kho dữ liệu snapshot và bung nén vào thư mục `data`.

### 6. `systemd_services` (Thiết lập dịch vụ hệ điều hành)
- Render ra các file cấu hình `metanode-*.service` và đăng ký với `systemd` của Linux.
- **Boot Sequence (Trình tự mồi):** Bật tiến trình Execution (Go) lên trước, ngủ (pause) 5 giây cho các API RPC khởi động xong, cuối cùng mới bật tiến trình Consensus (Rust).

### "Danh bạ Điện thoại": `inventory.yml`
Toàn bộ 6 Role phía trên không hề chứa IP cứng (hardcode). Mọi cấu hình (Tài khoản SSH, sơ đồ IP Node) đều được tự động trích xuất từ file `inventory.yml`. Bạn chỉ cần thêm hoặc sửa IP ở đây, Ansible sẽ tự biết phải làm gì!

---

## Phần 3: Hệ Thống Giám Sát & Công Cụ Tiện Ích

Bộ công cụ Ansible deploy đi kèm bộ giám sát (Monitors) chạy ngầm nội bộ độc lập hoàn toàn, hỗ trợ giám sát sức khỏe cụm node và tính nhất quán của chuỗi khối.

### 1. Bộ Giám Sát Cục Bộ (Monitors)
Bộ giám sát nằm tại thư mục [deploy/ansible/monitors/](file:///home/abc/chain-n/metanode/deploy/ansible/monitors/) bao gồm:
- **Health Monitor** (`start_monitors.sh health`): Liên tục kiểm tra các endpoint RPC của các node trong cụm. Nếu phát hiện node chết, tự động dùng `sshpass` kéo thư mục logs bị crash về máy master (lưu tại `monitors/logs_crash/`) và gửi cảnh báo đỏ lên Telegram.
- **Block Hash Checker** (`block_hash_checker`): Một công cụ viết bằng Go chạy ở dạng Daemon liên tục so sánh chiều cao block, hash, parentHash, stateRoot... giữa các node với nhau để phát hiện sớm các hiện tượng phân nhánh (fork) hoặc lệch trạng thái, hỗ trợ gửi cảnh báo trực tiếp lên Telegram.

Bộ giám sát này được tự động bật lại ở cuối mỗi phiên deploy thành công (chạy qua `ansible_deploy.sh`). Bạn có thể bật thủ công bất kỳ lúc nào bằng cách chạy:
```bash
cd deploy/ansible/monitors
./start_monitors.sh
```

### 2. Dừng các tiến trình nền (`stop_all.sh`)
Để tắt nhanh toàn bộ các công cụ nền đang chạy trên máy Master, hãy sử dụng tệp tiện ích [stop_all.sh](file:///home/abc/chain-n/metanode/deploy/ansible/stop_all.sh):
- **Tắt monitors & watcher daemon:**
  ```bash
  ./stop_all.sh
  ```
- **Tắt monitors, watcher daemon và dừng cả cụm Validator từ xa:**
  ```bash
  ./stop_all.sh --cluster
  ```
