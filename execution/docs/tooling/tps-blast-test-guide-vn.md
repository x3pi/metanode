# Hướng dẫn Kiểm tra Hiệu năng với tps_blast

Tài liệu này hướng dẫn cách sử dụng công cụ `tps_blast` để kiểm tra hiệu năng hệ thống (Stress Test) và cách phân tích các log đo đạc hiệu năng (`[PERF]`) để tìm điểm nghẽn.

## 1. Chuẩn bị

Trước khi chạy blast, hãy đảm bảo cluster đã được khởi động sạch (fresh start) để có kết quả chính xác nhất:

```bash
cd ~/chain-n/mtn-consensus/metanode/scripts/node
./run_all.sh
```

## 2. Cách chạy tps_blast

### Bước 1: Build công cụ
```bash
cd ~/chain-n/mtn-simple-2025/cmd/tool/tps_blast
go build -o tps_blast .
```

### Bước 2: Chạy lệnh Blast
Công cụ này sẽ tạo tài khoản mới và gửi giao dịch đăng ký BLS Public Key theo lô (batch).

```bash
./tps_blast --count 10000 --batch 500 --sleep 10
```

### 3. Chạy test với file Account khác (Tránh trùng lặp)
Để tránh sử dụng lại các account đã đăng ký BLS trong file mặc định (`blast_accounts.json`), bạn có thể chỉ định một file account mới. Tool sẽ tự động tạo account mới nếu file chưa tồn tại:

```bash
# Chạy với 10,000 account mới, lưu vào file accounts_v2.json
./tps_blast --count 10000 --batch 500 --sleep 10 --accounts accounts_v2.json
```

> [!TIP]
> Sử dụng các tên file khác nhau như `accounts_dev1.json`, `accounts_dev2.json` giúp bạn quản lý các bộ test data riêng biệt mà không bị lẫn lộn trạng thái `Registered`.

**Các tham số quan trọng:**
*   `--count`: Tổng số giao dịch muốn gửi (ví dụ: 10,000).
*   `--batch`: Số lượng giao dịch trong mỗi đợt gửi (lô).
*   `--sleep`: Thời gian nghỉ giữa các lô (miliseconds). Nếu bạn có 100 CPU, có thể giảm xuống 1-5ms hoặc bằng 0 để đẩy tốc độ tối đa.
*   `--rpc`: (Mới cập nhật) Địa chỉ HTTP RPC API của Node để công cụ truy vấn trạng thái Account sau khi gửi (ví dụ: `127.0.0.1:8747` cho Node 0, hoặc `192.168.1.232:10747` cho Node 1). Nếu không truyền, mặc định sẽ là `127.0.0.1:8747`.

## 3. Theo dõi hệ thống (Real-time)

Trong khi `tps_blast` đang chạy, bạn có thể mở các tab tmux khác để theo dõi tốc độ xử lý của Node:

*   **Theo dõi Block được tạo:**
    ```bash
    tail -f ~/chain-n/mtn-consensus/metanode/logs/node_0/target/simple_chain.log | grep "committed"
    ```

*   **Theo dõi log hiệu năng (Profiling):**
    ```bash
    tail -f ~/chain-n/mtn-consensus/metanode/logs/node_0/target/simple_chain.log | grep "\[PERF"
    ```

## 4. Phân tích điểm nghẽn qua Log

Tôi đã thêm các log đặc biệt để bạn biết chính xác hệ thống chậm ở đâu. Hãy tìm các dòng có prefix `[PERF]` hoặc `[PERF-VIRTUAL]`.

### Các chỉ số cần chú ý:

1.  **`[PERF-VIRTUAL] NewChainState creation`**:
    *   **Mô tả**: Thời gian khởi tạo môi trường (trie database) cho mỗi giao dịch.
    *   **Phân tích**: Nếu con số này > 2ms, hệ thống đang tốn quá nhiều thời gian cho việc đọc/ghi Disk và quản lý bộ nhớ Database. Đây thường là điểm nghẽn lớn nhất.

2.  **`[PERF-VIRTUAL] EVM execution (Call/Deploy)`**:
    *   **Mô tả**: Thời gian chạy logic Smart Contract bên trong máy ảo.
    *   **Phân tích**: Cho biết tốc độ tính toán thực tế của CPU cho logic nghiệp vụ.

3.  **`[PERF] Injection Virtual Execution`**:
    *   **Mô tả**: Tổng thời gian xử lý ảo trước khi giao dịch được nạp vào Pool.
    *   **Phân tích**: Nếu tổng thời gian này cao, việc tăng `--sleep` (giảm tốc độ nạp) có thể giúp Node không bị tràn hàng đợi.

4.  **`[PERF] Injection AddToPool`**:
    *   **Mô tả**: Thời gian đưa giao dịch vào Pool bộ nhớ.
    *   **Phân tích**: Nếu > 1ms, có nghĩa là Transaction Pool đang bị tranh chấp khóa (mutex contention) giữa quá nhiều worker.

## 5. Kiểm tra kết quả sau khi chạy

Sau khi `tps_blast` chạy xong, nó sẽ tạo ra file kết quả:
*   `blast_results.json`: Thống kê tỉ lệ thành công, TPS nạp (Injection TPS).
*   `blast_accounts.json`: Danh sách chi tiết các tài khoản và trạng thái của chúng.

Nếu tỉ lệ thành công không đạt 100%, hãy kiểm tra log Node để xem có lỗi "system overloaded" hay không. Với 100 CPU, bạn có thể tự tin tăng các thông số worker trong `constants.go` nếu thấy các worker bị sử dụng hết công suất (Pool usage cao).

## 6. Chạy Benchmark Toàn Diện (> 10,000 TPS)

Gần đây hệ thống đã được nâng cấp kịch bản tải nạp để dễ dàng vượt ngưỡng 10,000 TPS bằng cách phân bổ tải (Load-balancing) qua nhiều Clients và Nodes.
Để thực hiện bài kiểm tra này, bạn sẽ sử dụng file kịch bản `run_multinode_load.sh` nằm ở thư mục `cmd/tool/tps_blast` của Go project.

### Các bước chạy bài test lớn:
1. **Reset và khởi động lại toàn bộ mạng (Fresh Start):**
    ```bash
    cd ~/chain-n/mtn-consensus/metanode
    ./scripts/node/stop_all.sh
    ./scripts/node/run_all.sh
    ```
2. **Chạy kịch bản phân bổ tải (Multinode Load Test):**
    Chuyển về thư mục chứa script và chạy lệnh benchmark:
    ```bash
    cd ~/chain-n/mtn-simple-2025/cmd/tool/tps_blast
    ./run_multinode_load.sh 10 10000
    ```
    - `10` là số lượng clients thực hiện gửi giao dịch đồng thời. Các client này sẽ được tự động trỏ đều tới 4 validator nodes (ports: 4201, 6201, 6211, 6221) để tránh nghẽn socket tại một Node. Node 4 (SyncOnly) không nhận TX nên không được bao gồm.
    - `10000` là số giao dịch mỗi client sẽ nạp nhanh (Tổng cộng hệ thống nhận 100,000 TXs).
    - Các thông số tối ưu độ trễ nạp (như `batch = 500` và `sleep = 50ms`) đã được tinh chỉnh mềm mượt trong script để đẩy tốc độ lên trên 10,000 TPS.
    
    > Hệ thống sẽ bơm 100,000 giao dịch vào mempool và sau đó tự động poll (chờ) các Block được sản xuất hoàn chỉnh trước khi in ra bảng kết quả Global TPS và xác thực không Fork (0 forks).

## 7. Chạy Load Test Chỉ Trên Node 0 (Local Test)

Nếu bạn đang đứng trực tiếp tại máy tính chứa Node 0 và chỉ muốn dội tải vào duy nhất node này để quan sát log cục bộ, hãy chạy:

```bash
cd ~/chain-n/mtn-simple-2025/cmd/tool/tps_blast
./run_node0_only_load.sh 10 20000
```
- `10`: Số lượng Client tạo ra.
- `20000`: Số lượng giao dịch mỗi Client sẽ bắn vào cổng `4201` của Node 0.

## 8. Công thức tính TPS của Công Cụ

Công cụ `tps_blast` phân tích và bóc tách rất rõ ràng 2 loại TPS khác nhau để đánh giá đúng bản chất của mạng lưới Blockchain:

### 1. Injection TPS (Tốc độ nạp Mempool)
Là tốc độ mà các Client có thể đẩy (nạp) thành công giao dịch vào bộ đệm của Node qua kết nối TCP.
> **Công thức:** `Injection TPS = Tổng số TX đã gửi / Thời gian gửi (blastDuration)`

Chỉ số này phụ thuộc vào băng thông mạng (Client -> Node) và tốc độ tiếp nhận Socket của quá trình Injection.

### 2. Processing TPS / System TPS (Tốc độ Đồng thuận & Xử lý khối)
Đây là con số **quan trọng nhất**, phản ánh khả năng xử lý thực tế của hệ thống (từ lúc gom giao dịch vào Block, chạy thực thi máy ảo EVM đến khi chốt (commit) tệp xuống chain).

Được đo đạc qua 2 phương pháp song song:
*   **Dựa trên Thời gian Xác minh Mạng (Network Confirmation)** (Có trong `main.go`):
    > **Công thức:** `Processing TPS = Tổng TX được confirm thành công / Thời gian chờ confirm (Processing Duration)`
    *Thời gian được đo từ lúc Tool ngừng nạp giao dịch cho đến khi Tool tự động query (poll) tất cả các Account và nhận được kết quả xác nhận thay đổi trạng thái On-chain từ mạng.*

*   **Dựa trên Engine Log của Node 0** (Có trong `run_node0_only_load.sh`):
    > **Công thức:** `SYSTEM TPS = Tổng số lượng TX nằm trong chuỗi Blocks / (Thời gian sinh Block cuối - Thời gian sinh Block đầu)`
    *Script trích xuất trực tiếp số liệu từ file `App.log` để đo chuẩn xác tốc độ Engine Master đóng gói, phân giải (Resolve) và tạo khối.*
