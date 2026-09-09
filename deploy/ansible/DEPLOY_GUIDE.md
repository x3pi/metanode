# 📘 Cẩm Nang Triển Khai & Vận Hành Cụm Metanode

> Tài liệu hướng dẫn cài đặt, cấu hình và các kịch bản vận hành cụm blockchain Metanode bằng script tự động hóa [`ansible_deploy.sh`](./ansible_deploy.sh).

---

## 📑 Mục Lục
1. [Yêu Cầu Chuẩn Bị (Prerequisites)](#1-yêu-cầu-chuẩn-bị-prerequisites)
2. [Thiết Lập Cấu Hình (`inventory.yml`)](#2-thiết-lập-cấu-hình-inventoryyml)
3. [Các Kịch Bản Vận Hành Cụm Node](#3-các-kịch-bản-vận-hành-cụm-node)
   - [Kịch bản 1: Khởi tạo Chain mới tinh & Mở Port tường lửa (Fresh Genesis & Open Ports)](#kịch-bản-1-khởi-tạo-chain-mới-tinh--mở-port-tường-lửa-fresh-genesis--open-ports)
   - [Kịch bản 2: Cập nhật code / Khởi động KHÔNG xóa dữ liệu](#kịch-bản-2-cập-nhật-code--khởi-động-không-xóa-dữ-liệu)
   - [Kịch bản 3: Dừng toàn bộ các Node trong mạng](#kịch-bản-3-dừng-toàn-bộ-các-node-trong-mạng)
   - [Kịch bản 4: Thao tác trên 1 Node riêng biệt (Stop / Start / Restart)](#kịch-bản-4-thao-tác-trên-1-node-riêng-biệt)
   - [Kịch bản 5: Khởi động lại nhanh (Fast Restart)](#kịch-bản-5-khởi-động-lại-nhanh-fast-restart)
   - [Kịch bản 6: Khôi phục 1 Node bị lỗi từ Snapshot](#kịch-bản-6-khôi-phục-1-node-bị-lỗi-từ-snapshot)
4. [Kiểm Tra Trạng Thái & Giám Sát Mạng](#4-kiểm-tra-trạng-thái--giám-sát-mạng)

---

## 1. Yêu Cầu Chuẩn Bị (Prerequisites)

Trên máy tính điều khiển deploy (Deployer Machine):
```bash
sudo apt update && sudo apt install -y ansible sshpass jq python3-yaml curl
```
> Đảm bảo máy deploy có thể kết nối SSH tới tất cả các máy chủ node (user có quyền `sudo`).

---

## 2. Thiết Lập Cấu Hình (`inventory.yml`)

Bạn chỉ cần tạo file `inventory.yml` từ file mẫu [`inventory.example.yml`](./inventory.example.yml):

```bash
cd deploy/ansible
cp inventory.example.yml inventory.yml
```

> 💡 **Lưu ý:** Toàn bộ ý nghĩa của từng trường cấu hình (`node_ids`, `rpc_nodes`, `prune_nodes`, `epochs_to_keep`, cách dùng SSH Key vs Password...) đã được **chú thích chi tiết trong file [`inventory.example.yml`](./inventory.example.yml)**. Bạn chỉ cần mở file `inventory.yml` lên và chỉnh sửa lại IP, tài khoản theo đúng cụm server của mình.

---

## 3. Các Kịch Bản Vận Hành Cụm Node

Mọi thao tác đều thực hiện thông qua script bọc ngoài [`ansible_deploy.sh`](./ansible_deploy.sh):
```bash
cd deploy/ansible
```

### Kịch bản 1: Khởi tạo Chain mới tinh & Mở Port tường lửa (Fresh Genesis & Open Ports)
* **Khi nào dùng:** Lần đầu dựng mạng lưới mới, hoặc khi muốn xóa sạch toàn bộ dữ liệu cũ để chạy lại chuỗi từ đầu.
* **Ý nghĩa cờ:**
  * `--reset-all`: **Xóa sạch DB cũ** trên mọi node, tạo mới toàn bộ key validator/genesis và khởi động chain từ **Block 0**.
  * `--open-ports`: **Mở cổng tường lửa (UFW)** (P2P consensus, execution, RPC, snapshot) để các node kết nối được với nhau và client gọi được RPC.
* **Câu lệnh khuyên dùng khi dựng mới hoàn toàn:**
  ```bash
  ./ansible_deploy.sh --reset-all --open-ports
  ```
  *(💡 Nếu server đã mở port sẵn và chỉ cần reset dữ liệu: `./ansible_deploy.sh --reset-all`)*

---

### Kịch bản 2: Cập nhật code / Khởi động KHÔNG xóa dữ liệu
* **Khi nào dùng:** Khi lập trình viên cập nhật tính năng mới hoặc sửa lỗi, cần đưa binary mới lên server mà **KHÔNG làm mất dữ liệu** (giữ nguyên block hiện tại, giữ nguyên DB và keys).
* **Hành vi:** Build binary mới $\rightarrow$ Tắt service $\rightarrow$ Chép đè binary mới lên server $\rightarrow$ Khởi động lại. Mạng tiếp tục sinh block từ độ cao hiện tại.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --start
  ```
  *(Gõ `./ansible_deploy.sh` không truyền tham số cũng tương đương `--start`)*.

---

### Kịch bản 3: Dừng toàn bộ các Node trong mạng
* **Khi nào dùng:** Cần bảo trì server, di dời hạ tầng hoặc dừng mạng an toàn.
* **Hành vi:** Gửi tín hiệu `systemctl stop` tới toàn bộ dịch vụ execution và consensus trên tất cả các server.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --stop
  ```

---

### Kịch bản 4: Thao tác trên 1 Node riêng biệt
Khi chỉ muốn can thiệp vào một node cụ thể mà không làm gián đoạn các node khác, dùng thêm cờ `--only-node <ID>`.

#### 4.1. Dừng riêng Node 2:
```bash
./ansible_deploy.sh --stop --only-node 2
```

#### 4.2. Khởi động / Cập nhật lại riêng Node 2 (giữ nguyên data):
```bash
./ansible_deploy.sh --start --only-node 2
```

#### 4.3. Khởi động lại nhanh riêng Node 2:
```bash
./ansible_deploy.sh --restart --only-node 2
```

---

### Kịch bản 5: Khởi động lại nhanh (Fast Restart)
* **Khi nào dùng:** Khi bạn vừa sửa file cấu hình bằng tay trên server hoặc server vừa reboot, cần restart tiến trình ngay mà **không cần mất thời gian build lại code hay copy file**.
* **Hành vi:** Chỉ chạy lệnh `systemctl restart` các dịch vụ. Thời gian thực thi chỉ mất 1-2 giây.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --restart
  ```

---

### Kịch bản 6: Khôi phục 1 Node bị lỗi từ Snapshot
* **Khi nào dùng:** Một node bị hỏng ổ đĩa hoặc bị lệch trạng thái (out-of-sync) quá xa, cần tải snapshot từ một node khác về để nhanh chóng bắt kịp block mới nhất.
* **⚠️ BẮT BUỘC phải kèm `--only-node <N>`:** `--reset-all` tự nó xóa data của **TẤT CẢ** active nodes (`ACTION=setup, KEEP_DATA=false` áp dụng cho toàn bộ `active_nodes`, không riêng node truyền vào `--restore-node`). Thiếu `--only-node` sẽ xóa sạch dữ liệu của mọi node đang chạy, không chỉ node cần khôi phục.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --reset-all --only-node 2 --restore-node 2 --snapshot-url http://192.168.1.234:8604
  ```
* **Nguồn snapshot khuyến nghị:** dùng node SyncOnly chuyên trách (không phải validator) nếu cluster có, xem biến `snapshot_url` trong `inventory.yml` — tránh kéo snapshot từ 1 validator khác vì việc đó tạm khóa ghi RocksDB của chính validator đó (`RUST_EXECUTION_LOCK`), ảnh hưởng tới nhịp biểu quyết đúng lúc cluster cần validator đó khỏe nhất.

---

## 4. Kiểm Tra Trạng Thái & Giám Sát Mạng

Sau khi triển khai, bạn có thể kiểm tra sức khỏe cụm node qua các cách sau:

### 1. Kiểm tra trạng thái service (trên máy chủ Node):
```bash
systemctl status metanode-execution-0 metanode-consensus-0
```

### 2. Xem log trực tiếp thời gian thực:
```bash
# Log tầng Execution
journalctl -u metanode-execution-0 -f

# Log tầng Consensus
journalctl -u metanode-consensus-0 -f
```

### 3. Kiểm tra cổng RPC sinh Block (gọi từ máy bất kỳ):
```bash
curl -s -X POST http://<IP_NODE_RPC>:10746 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```
*Nếu giá trị `result` (block number hex) liên tục tăng theo thời gian là mạng đang hoạt động ổn định.*

### 4. Hệ thống giám sát cảnh báo Telegram ngầm:
> 💡 **Tự động:** Khi bạn chạy `./ansible_deploy.sh` (dù là `--reset-all`, `--start` hay `--restart`), script đã **tự động khởi động hệ thống monitor ngầm** sau khi hoàn tất. Bạn **không cần phải gõ lệnh tay**.

Chỉ cần chạy thủ công nếu bạn muốn bật lại monitor riêng lẻ mà không chạy lại deploy:
```bash
cd deploy/ansible/monitors
./start_monitors.sh
```
*(Script theo dõi tài nguyên RAM/CPU/Disk, bắt lỗi panic/crash, kiểm tra block hash và gửi cảnh báo về Telegram).*
