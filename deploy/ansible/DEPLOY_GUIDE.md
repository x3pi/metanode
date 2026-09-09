# 📘 Cẩm Nang Triển Khai & Vận Hành Cụm Metanode

> Tài liệu hướng dẫn cài đặt, cấu hình và các kịch bản vận hành cụm blockchain Metanode bằng script tự động hóa [`ansible_deploy.sh`](./ansible_deploy.sh).

---

## 📑 Mục Lục
1. [Yêu Cầu Chuẩn Bị (Prerequisites)](#1-yêu-cầu-chuẩn-bị-prerequisites)
2. [Thiết Lập Cấu Hình (`inventory.yml`)](#2-thiết-lập-cấu-hình-inventoryyml)
3. [Khởi Tạo Cụm Chain Mới Tinh Từng Bước An Toàn (Genesis Block 0)](#3-khởi-tạo-cụm-chain-mới-tinh-từng-bước-an-toàn-genesis-block-0)
   - [Bước 1: Sinh bộ Key & Genesis mẫu](#bước-1-sinh-bộ-key--genesis-mẫu-chỉ-chạy-nội-bộ-máy-deploy-không-đụng-server)
   - [Bước 2: Tùy biến Genesis](#bước-2-tùy-chọn-tùy-biến-genesis)
   - [Bước 3: Triển khai, mở port & khởi chạy chuỗi](#bước-3-triển-khai-lên-server-mở-cổng-tường-lửa-và-khởi-chạy-chuỗi-từ-block-0)
4. [Chi Tiết Các Kịch Bản Vận Hành Cụm Node](#4-chi-tiết-các-kịch-bản-vận-hành-cụm-node)
   - [Kịch bản 1: Start lại 1 Node bị chết / lỗi (An toàn, giữ nguyên Data)](#kịch-bản-1-start-lại-1-node-bị-chết--lỗi-an-toàn-giữ-nguyên-data)
   - [Kịch bản 2: Dừng 1 Node để deploy lại / bảo trì](#kịch-bản-2-dừng-1-node-để-deploy-lại--bảo-trì)
   - [Kịch bản 3: Khôi phục 1 Node bị lỗi từ Snapshot (Lệch Hash / Hỏng DB / Lag > 5 Epoch)](#kịch-bản-3-khôi-phục-1-node-bị-lỗi-từ-snapshot-lệch-hash--hỏng-db--lag--5-epoch)
   - [Kịch bản 4: Chuỗi bị đứng im (Chain Stall / Consensus kẹt Round)](#kịch-bản-4-chuỗi-bị-đứng-im-chain-stall--consensus-kẹt-round)
   - [Kịch bản 5: Cập nhật code mới cho toàn mạng (KHÔNG xóa data)](#kịch-bản-5-cập-nhật-code-mới-cho-toàn-mạng-không-xóa-data)
   - [Kịch bản 6: Mở cổng tường lửa UFW (Firewall / Open Ports)](#kịch-bản-6-mở-cổng-tường-lửa-ufw-firewall--open-ports)
   - [Kịch bản 7: Chế độ bảo trì — Tạm tắt cảnh báo Telegram cho Node đang sửa](#kịch-bản-7-chế-độ-bảo-trì--tạm-tắt-cảnh-báo-telegram-cho-node-đang-sửa)
   - [Kịch bản 8: Kéo toàn bộ Log của các Node về máy để Debug](#kịch-bản-8-kéo-toàn-bộ-log-của-các-node-về-máy-để-debug)
   - [Kịch bản 9: Dừng toàn bộ các Node trong mạng (Bảo trì Server)](#kịch-bản-9-dừng-toàn-bộ-các-node-trong-mạng-bảo-trì-server)
5. [Kiểm Tra Trạng Thái & Giám Sát Mạng](#5-kiểm-tra-trạng-thái--giám-sát-mạng)

---

## 1. Yêu Cầu Chuẩn Bị (Prerequisites)

### 1.1. Trên máy tính điều khiển (Deployer Machine):
* **Công cụ vận hành Ansible:**
  ```bash
  sudo apt update && sudo apt install -y ansible sshpass jq python3-yaml curl
  ```
* **Môi trường Build source code (Nếu máy deploy tự compile binary Go/Rust/C++):**
  - **Go:** `go version` $\ge$ 1.22
  - **Rust & Cargo:** `curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh`
  - **Bộ biên dịch C++ & Thư viện (cho EVM & NOMT FFI):**
    ```bash
    sudo apt install -y build-essential cmake clang libssl-dev pkg-config
    ```

### 1.2. Trên các máy chủ Node từ xa (Remote Target Servers):
* Hệ điều hành Ubuntu 20.04 / 22.04 / 24.04 LTS tiêu chuẩn.
* Tài khoản SSH có quyền `sudo` (dùng SSH Key hoặc mật khẩu được cấu hình trong `inventory.yml`).
* *(Tùy chọn)* Nếu máy chủ chạy Node có bật tính năng snapshot (`snapshot_enabled: true`), cài thêm công cụ BTRFS:
  ```bash
  sudo apt install -y btrfs-progs
  ```

---

## 2. Thiết Lập Cấu Hình (`inventory.yml`)

Bạn chỉ cần tạo file `inventory.yml` từ file mẫu [`inventory.example.yml`](./inventory.example.yml):

```bash
cd deploy/ansible
cp inventory.example.yml inventory.yml
```

> 💡 **Lưu ý:** Toàn bộ ý nghĩa của từng trường cấu hình (`node_ids`, `rpc_nodes`, `prune_nodes`, `epochs_to_keep`, cách dùng SSH Key vs Password...) đã được **chú thích chi tiết trong file [`inventory.example.yml`](./inventory.example.yml)**. Bạn chỉ cần mở file `inventory.yml` lên và chỉnh sửa lại IP, tài khoản theo đúng cụm server của mình.

---

## 3. Khởi Tạo Cụm Chain Mới Tinh Từng Bước An Toàn (Genesis Block 0)

> 💡 **Khi nào dùng:** Khi lần đầu tiên triển khai cụm mạng mới lên dàn máy chủ, hoặc khi cần thiết lập lại chuỗi từ Block 0 trên môi trường Testnet/Dev.
> ⚠️ **CẢNH BÁO:** Thao tác này sẽ dọn dẹp sạch cơ sở dữ liệu cũ trên các máy chủ được chỉ định để mạng bắt đầu đồng thuận từ Block 0.

### 🪜 3 Bước Triển Khai Chuẩn Thực Tế:

#### Bước 1: Sinh bộ Key & Genesis mẫu (Chỉ chạy nội bộ máy deploy, KHÔNG đụng server)

##### 1.1. Sinh tự động toàn bộ cụm theo `inventory.yml` (Khuyên dùng - Nhanh nhất):
```bash
cd deploy/ansible
./ansible_deploy.sh --gen-keys
```
*👉 Lệnh này tự tạo đủ bộ key cho toàn bộ nodes trong `deploy/systemd/node-X_keys/` và file `deploy/systemd/genesis.json`.*

##### 1.2. (Tùy chọn) Thay Thế Key Cho 1 Node Bất Kỳ:
Nếu muốn đổi bộ key của riêng 1 Node (ví dụ Node 2), dùng tool có sẵn để vừa tạo key mới vừa tự động cập nhật vào `genesis.json`:
```bash
cd deploy/systemd
python3 gen_validator_entry.py \
  --hostname node-2 \
  --node-id 2 \
  --ip <IP_NODE_2> \
  --keys-dir ./node-2_keys
```
*👉 Script sẽ tự sinh mới 4 file key vào `node-2_keys/` và tự động cập nhật thông tin Node 2 vào file `genesis.json` (không cần sửa file JSON hay chạm vào Ansible).*

---

#### Bước 2: (Tùy chọn) Tùy biến Genesis 
Mở file `deploy/systemd/genesis.json` nếu muốn chỉnh sửa:
* `config.chainId`: Đổi Chain ID mong muốn (ví dụ: `991`, `1001`...).
* `alloc`: Thêm địa chỉ ví nhận coin khởi tạo ban đầu (balance tính theo Wei).

---

#### Bước 3: Triển khai lên server, mở cổng tường lửa và khởi chạy chuỗi từ Block 0:
```bash
cd deploy/ansible
./ansible_deploy.sh --start --clean --open-ports
```
*👉 Cờ `--open-ports` sẽ tự động mở thông suốt toàn bộ cổng firewall UFW trên các server từ xa; kết hợp với `--start --clean` sẽ xóa sạch database cũ và khởi chạy toàn cụm mạng từ Genesis Block 0!*
*(⚠️ **Tuyệt đối không dùng `--reset-all`** sau khi đã sửa key, vì `--reset-all` sẽ tự động sinh đè mất key bạn vừa tạo).*
*(💡 Nếu server đã từng mở port trước đó hoặc chỉ muốn mở firewall riêng biệt mà không chạy node: gõ `./ansible_deploy.sh --open-ports`).*

##### 🔍 Sau khi chạy lệnh trên, Ansible sẽ tự động thực hiện trên các server từ xa:
1. **Dọn sạch Database cũ (`--clean`):** Xóa toàn bộ dữ liệu blockchain và log cũ tại `/opt/metanode/node-X/data` và `logs` để chuỗi sẵn sàng đồng thuận từ Genesis Block 0.
2. **Đẩy bộ Binary Release:** Tải các binary thực thi (`simple_chain`, `metanode`) lên server.
3. **Mở cổng tường lửa (`--open-ports`):** Tự động cấu hình rule UFW cho các cổng P2P Consensus (620x), Execution (900x), RPC (1074x), Snapshot (860x).
4. **Tạo cấu trúc thư mục node chuẩn tại `/opt/metanode/node-X/`:**
   ```text
   /opt/metanode/node-X/
   ├── bin/       # Binary thực thi chính: simple_chain (Go execution + FFI) và metanode (Rust consensus)
   ├── config/    # File cấu hình: execution.json, consensus.toml và genesis.json
   ├── keys/      # 4 file khóa bảo mật: authority_key, protocol_key, network_key, eth_key (quyền 0600)
   ├── data/      # Cơ sở dữ liệu blockchain: State Trie NOMT/MVM và Consensus DAG
   └── logs/      # Thư mục lưu file nhật ký log chạy ngầm (execution, consensus)
   ```
5. **Cài đặt & Khởi động Systemd Service:**
   - Cài đặt dịch vụ `/etc/systemd/system/metanode-execution-X.service` (Consensus chạy nhúng trực tiếp qua FFI cgo/Rust bên trong process Execution này).
   - Chạy lệnh `systemctl daemon-reload && systemctl restart metanode-execution-X.service`.
   - Các node tự động kết nối P2P với nhau dựa theo `genesis.json` và sinh tiếp các Block từ Block 0!
6. **Kích hoạt Monitor ngầm:** Tự động đồng bộ RPC và khởi động hệ thống giám sát kiểm tra hash block đồng đều trên các node.

---

## 4. Chi Tiết Các Kịch Bản Vận Hành Cụm Node

Mọi thao tác đều thực hiện từ thư mục `deploy/ansible`:
```bash
cd deploy/ansible
```

### Kịch bản 1: Start lại 1 Node bị chết / lỗi (An toàn, giữ nguyên Data)
* **Khi nào dùng:** Bot Telegram báo `[SỰ CỐ: NODE CRASH / SERVICE SẬP]`, hoặc khi check monitor thấy `mX=ERR`.
* **Hành vi:** Bật lại dịch vụ Execution và Consensus của riêng node đó, giữ nguyên trạng thái blockchain và DB hiện tại.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --start --only-node 2
  ```
  *(💡 Nếu chỉ muốn fast-restart lại systemd services trong 1 giây: `./ansible_deploy.sh --restart --only-node 2`)*.

---

### Kịch bản 2: Dừng 1 Node để deploy lại / bảo trì
* **Khi nào dùng:** Khi bạn muốn dừng riêng 1 node (ví dụ Node 2) để cấu hình lại, thay thế phần cứng, kiểm tra log hoặc thử nghiệm mà không làm ảnh hưởng đến các node khác trong cluster.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --stop --only-node 2
  ```

---

### Kịch bản 3: Khôi phục 1 Node bị lỗi từ Snapshot (Lệch Hash / Hỏng DB / Lag > 5 Epoch)
* **Khi nào dùng:** 
  - [block_hash_checker](file:///home/abc/nhat/con-chain-v2/metanode/deploy/ansible/monitors/block_hash_checker/main.go) báo lệch hash / stateRoot trên riêng Node X.
  - Ổ đĩa của Node X bị hỏng, corrupt RocksDB, hoặc node bị offline quá lâu dẫn đến tụt lại phía sau quá 5 epochs không sync kịp P2P.
* **⚠️ BẮT BUỘC kèm `--only-node <N>`:** `--reset-all` tự nó xóa data của **TẤT CẢ** nodes. Bắt buộc phải có `--only-node <N>` để chỉ xóa dữ liệu cũ của node cần sửa và kéo snapshot sạch về!
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --reset-all --only-node 2 --restore-node 2 --snapshot-url http://192.168.1.234:8604
  ```
* **Nguồn snapshot:** Khuyến nghị dùng endpoint của node `SyncOnly` (ví dụ port `8604` trong cụm) thay vì validator để tránh khóa ghi RocksDB của validator.

---

### Kịch bản 4: Chuỗi bị đứng im (Chain Stall / Consensus kẹt Round)
* **Khi nào dùng:** Bot Telegram báo `[NGHIÊM TRỌNG: CHUỖI BỊ ĐỨNG IM / CHAIN STALL]`. Tất cả các node vẫn sống (HTTP 200) nhưng số block không tăng sau 60 giây do deadlock round hoặc consensus kẹt view.
* **Hành vi:** Chạy Fast Restart toàn cụm trong 1–2 giây. Các node khởi động lại, kích hoạt vòng bầu Leader mới và tiếp tục sinh block ngay lập tức mà **KHÔNG mất bất kỳ block hay dữ liệu nào**.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --restart
  ```

---

### Kịch bản 5: Cập nhật code mới cho toàn mạng (KHÔNG xóa data)
* **Khi nào dùng:** Khi lập trình viên cập nhật tính năng mới hoặc sửa lỗi trong source code Git, cần đưa binary mới lên toàn bộ server mà **giữ nguyên toàn bộ dữ liệu** (không reset block, không đổi genesis/keys).
* **Hành vi:** Build binary mới → Tắt service → Chép binary mới → Khởi động lại.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --start
  # Mẹo: Thêm --fast để build nhanh bỏ qua các bước kiểm tra thừa:
  ./ansible_deploy.sh --start --fast
  ```

---

### Kịch bản 6: Mở cổng tường lửa UFW (Firewall / Open Ports)
* **Khi nào dùng:** Khi mới thêm máy chủ mới vào cluster, hoặc các node không thấy nhau (Consensus P2P port 620x, Execution P2P 900x, RPC 1074x).
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --open-ports
  ```

---

### Kịch bản 7: Chế độ bảo trì — Tạm tắt cảnh báo Telegram cho Node đang sửa
* **Khi nào dùng:** Khi bạn chủ động stop 1 node để bảo trì (`--stop --only-node X`), tránh việc bot Telegram cứ 10 giây lại bắn chuông báo động `[SỰ CỐ: NODE CRASH]`.
* **Thao tác:**
  * **Trước khi tắt node:** Thêm ID node vào danh sách bỏ qua giám sát:
    ```bash
    echo "2" >> /tmp/monitors_ignore_nodes
    ```
  * **Sau khi sửa xong và bật lại node:** Xóa khỏi danh sách bỏ qua:
    ```bash
    sed -i '/2/d' /tmp/monitors_ignore_nodes
    ```

---

### Kịch bản 8: Kéo toàn bộ Log của các Node về máy để Debug
* **Khi nào dùng:** Khi xảy ra lỗi phức tạp, người mới không cần phải SSH vào từng con server để gõ `journalctl`. Script tự động gom toàn bộ systemd logs từ tất cả các máy chủ về máy điều khiển.
* **Câu lệnh:**
  ```bash
  ./fetch_node_logs.sh
  ```
  *(Toàn bộ log sẽ được lưu tại thư mục `logs_systemd/run_<TIMESTAMP>/`)*.

---

### Kịch bản 9: Dừng toàn bộ các Node trong mạng (Bảo trì Server)
* **Khi nào dùng:** Cần bảo trì server vật lý, nâng cấp hạ tầng hoặc dừng mạng có kiểm soát.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --stop
  ```

---

## 5. Kiểm Tra Trạng Thái & Giám Sát Mạng

Sau khi thực hiện bất kỳ kịch bản nào, người mới có thể kiểm tra xem mạng đã hoạt động ổn định và các node đã bắt kịp nhau hay chưa:

### Cách 1: Kiểm tra service và xem log trực tiếp trên máy node

```bash
sudo systemctl status metanode-execution-<N> --no-pager
sudo journalctl -u metanode-execution-<N> -f
```

Thay `<N>` bằng ID node. Lệnh đầu cho biết service có đang chạy hoặc crash-loop hay không; lệnh thứ hai theo dõi log trực tiếp. Nhấn `Ctrl+C` để thoát chế độ theo dõi log.

### Cách 2: Chạy công cụ kiểm tra độ cao & hash thời gian thực (Khuyên dùng):
```bash
cd monitors/block_hash_checker
go run main.go --watch --interval 5s --config config-m-nodes.json --no-stop-flag
```
*Quan sát bảng `Heights: m0=185 m1=185 m2=185...` tăng đều và không còn chữ `ERR` là hệ thống đã hoàn toàn khỏe mạnh.*

### Cách 3: Kiểm tra cổng RPC sinh Block qua curl:
```bash
curl -s -X POST http://<IP_NODE_RPC>:10746 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```
*Nếu giá trị `result` (block hex) liên tục tăng theo thời gian là mạng đang hoạt động ổn định.*

### Cách 4: Hệ thống giám sát cảnh báo Telegram ngầm:
> 💡 **Tự động:** Khi bạn chạy `./ansible_deploy.sh` (dù là `--start` hay `--restart`), script đã **tự động khởi động hệ thống monitor ngầm** sau khi hoàn tất. Bạn **không cần phải gõ lệnh tay**.

Chỉ cần chạy thủ công nếu muốn bật lại monitor riêng lẻ:
```bash
cd deploy/ansible/monitors
./start_monitors.sh
```
