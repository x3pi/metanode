# Tài Liệu Hướng Dẫn Vận Hành & Triển Khai Metanode Private Chain

Tài liệu này cung cấp cái nhìn tổng quan và hướng dẫn chi tiết cách thức cấu trúc, triển khai và kiểm thử hiệu năng cho mạng lưới **Metanode Private Chain**.

---

## 1. 📐 Kiến Trúc Tổng Quan (Architecture)

Metanode được thiết kế với kiến trúc phân tầng rõ ràng, kết hợp hiệu suất cao của **Go** và sự an toàn bộ nhớ của **Rust**:

*   **Tầng Thực thi (Execution Layer - Go):** Chịu trách nhiệm quản lý EVM, trạng thái tài khoản (Account State), trie, xác thực giao dịch, xử lý RPC và thực thi hợp đồng thông minh. Nằm trong thư mục `execution/`.
*   **Tầng Đồng thuận (Consensus Layer - Rust):** Sử dụng thuật toán BFT/DAG (Mysticeti variant) để đảm bảo tính nhất quán của dữ liệu trên toàn mạng, sắp xếp giao dịch (linearizer) và ngăn chặn fork. Nằm trong thư mục `consensus/`.
*   **Giao tiếp chéo (Cross-Layer):** Hai tầng này giao tiếp thông qua Unix Domain Sockets (UDS) và FFI (C-ABI) để tối ưu hiệu năng I/O.

---

## 2. 🚀 Hệ Thống Triển Khai (Ansible Deployment)

Việc vận hành Private Chain được tự động hóa hoàn toàn bằng Ansible. Mọi lệnh thao tác mạng lưới được gói gọn trong script `ansible_deploy.sh` (Nằm tại `deploy/ansible/`). 

Bạn chỉ cần cấu hình danh sách server (IP, tài khoản SSH) tại file `inventory.yml`.

> [!TIP]
> Bạn **KHÔNG CẦN** gõ lệnh ansible phức tạp, hãy dùng `ansible_deploy.sh`.

### Các Lệnh Vận Hành Cơ Bản

*   **Khởi tạo mạng mới hoàn toàn (Xóa Data & Tạo Key mới):**
    ```bash
    ./ansible_deploy.sh --reset-all
    ```
    *Dùng khi muốn thiết lập lại Blockchain từ Block 0, xóa sạch dữ liệu cũ.*

*   **Cập nhật code / Khởi động lại (Giữ nguyên Data & Key):**
    ```bash
    ./ansible_deploy.sh --start
    ```
    *Dùng khi nâng cấp phiên bản node nhưng không muốn làm mất dữ liệu chuỗi khối hiện tại.*

*   **Dừng toàn bộ mạng lưới:**
    ```bash
    ./ansible_deploy.sh --stop
    ```

*   **Mở cổng tường lửa (Chạy 1 lần trên server mới):**
    ```bash
    ./ansible_deploy.sh --start --open-ports
    ```

### Thao Tác Trên Một Node Đơn Lẻ (Chỉ định Node ID)

Nếu bạn chỉ muốn tác động lên một Validator cụ thể (Ví dụ: Node 2) mà không ảnh hưởng tới mạng, sử dụng cờ `--only-node`:

```bash
# Chỉ tắt Node 2
./ansible_deploy.sh --stop --only-node 2

# Reset dữ liệu và cài lại Node 2
./ansible_deploy.sh --reset-all --only-node 2

# Khôi phục Node 2 từ Snapshot server
./ansible_deploy.sh --reset-all --only-node 2 --restore-node 2 --snapshot-url http://192.168.1.230:8604
```

> [!IMPORTANT]
> Cấu trúc Ansible gồm 6 phase (Roles) chạy đồng bộ: Local Build -> Stop Services -> Clean Data (Tùy chọn) -> Node Setup -> Snapshot Restore (Tùy chọn) -> Systemd Services Start. Node luôn bật Execution (Go) trước khi bật Consensus (Rust).

---

## 3. 🔍 Giám Sát Sức Khỏe Mạng Lưới (Monitoring)

Metanode được tích hợp các công cụ chạy ngầm để giám sát tính nhất quán:

*   **Bật hệ thống giám sát tự động:**
    ```bash
    cd deploy/ansible/monitors
    ./start_monitors.sh
    ```
    *Công cụ `Health Monitor` và `Block Hash Checker` sẽ liên tục kiểm tra RPC các node để phát hiện Fork hoặc node sập (Gửi cảnh báo Telegram).*

*   **Dừng tất cả các tiến trình theo dõi ngầm:**
    ```bash
    cd deploy/ansible
    ./stop_all.sh
    ```

---

## 4. 📊 Công Cụ Kiểm Thử Hiệu Năng (TPS Blast Test)

Bộ công cụ `tps_blast_cc` nằm trong `metanode-suite/test_tps/tps_blast_cc/` cho phép bơm tải giả lập (spam TX) cực mạnh vào mạng lưới private chain để đo lường khả năng xử lý (TPS).

### Kịch Bản Tự Động Toàn Diện (`run_tps_test.sh`)

Sử dụng kịch bản này để chạy bài test chỉ với một lệnh.

*   **Khởi tạo lại mạng (Reset) và bắn 50,000 TX:**
    ```bash
    ./run_tps_test.sh 50000 --rounds 3 --batch 20000
    ```
    
*   **Chạy bài Test (Không reset data, load-balancing trên tất cả RPC):**
    ```bash
    ./run_tps_test.sh --no-reset 50000 --rounds 3 --load_balance true --batch 20000 --tps-target 50000 --epoch-wait 0 --config config-multi.json
    ```

### Bắn Tải Thủ Công Bằng Go (`main.go`)

Các tham số phổ biến:
*   `--count`: Tổng số TX muốn bắn.
*   `--batch`: Cỡ của mỗi gói (batch) TX gửi qua TCP.
*   `--target-node <ID>`: Trỏ thẳng tải vào 1 node cụ thể theo định tuyến trong `config.json`.
*   `--load_balance true`: Chia đều các gói TX xoay vòng qua tất cả các Node.

**Ví dụ:**
```bash
# Bắn 20,000 TX vào Node 1, tắt chờ Epoch
go run main.go --count 20000 --epoch-wait 0 --batch 500 --target-node 1

# Bắn 20,000 TX chia đều (load balance) chạy 30 vòng liên tục và lấy trace báo cáo bottleneck
go run main.go --count 20000 --rounds 30 --load_balance=true --batch=10000 --amount 1 --config=config-multi.json --trace
```

> [!NOTE]
> Tính năng **Crash-on-Error**: Công cụ test TPS được thiết kế rất chặt chẽ. Nếu phát hiện lỗi nonce, ngắt kết nối TCP, hoặc Timeout chờ đóng gói khối, script sẽ tự động dừng khẩn cấp thay vì lờ đi lỗi. Kết quả Test TPS sẽ được xuất báo cáo chi tiết về độ trễ Commit, EVM trace, và I/O disk sau mỗi bài test.
