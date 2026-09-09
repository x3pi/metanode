# 📘 Cẩm Nang Triển Khai & Vận Hành Cụm Metanode

> Tài liệu hướng dẫn cài đặt, cấu hình và các kịch bản vận hành cụm blockchain Metanode bằng script tự động hóa [`ansible_deploy.sh`](./ansible_deploy.sh).

---

## 📑 Mục Lục
1. [Yêu Cầu Chuẩn Bị (Prerequisites)](#1-yêu-cầu-chuẩn-bị-prerequisites)
2. [Thiết Lập Cấu Hình (`inventory.yml`)](#2-thiết-lập-cấu-hình-inventoryyml)
3. [Bảng Ma Trận Cấp Cứu Cho Người Mới (Runbook Cheat Sheet)](#3-bảng-ma-trận-cấp-cứu-cho-người-mới-runbook-cheat-sheet)
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
   - [🚨 Kịch bản Đặc biệt: Khởi tạo Chain mới tinh (Genesis Block 0)](#-kịch-bản-đặc-biệt-khởi-tạo-chain-mới-tinh-genesis-block-0)
5. [Tự Tạo Key & Chạy Độc Lập Bằng Các Tool Sẵn Có (Không Cần Sửa Ansible)](#5-tự-tạo-key--chạy-độc-lập-bằng-các-tool-sẵn-có-không-cần-sửa-ansible)
   - [5.1. Danh Mục Các Tool Sẵn Có & Cách cd Tới Sử Dụng](#51-danh-mục-các-tool-sẵn-có--cách-cd-tới-sử-dụng)
   - [5.2. Kiến Trúc 4 Loại Khóa Bắt Buộc Của Mỗi Validator](#52-kiến-trúc-4-loại-khóa-bắt-buộc-của-mỗi-validator)
   - [5.3. Hướng Dẫn: Muốn Chạy Riêng / Dùng Key Riêng Thì Làm Thế Nào?](#53-hướng-dẫn-muốn-chạy-riêng--dùng-key-riêng-thì-làm-thế-nào)
6. [Kiểm Tra Trạng Thái & Giám Sát Mạng](#6-kiểm-tra-trạng-thái--giám-sát-mạng)

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

## 3. Bảng Ma Trận Cấp Cứu Cho Người Mới (Runbook Cheat Sheet)

Khi hệ thống báo lỗi qua **Telegram** hoặc **Monitor CLI**, tra cứu ngay bảng dưới đây để lấy lệnh copy-paste chạy xử lý:

| Cảnh báo / Hiện tượng nhận được | Bản chất lỗi | Kịch bản | Câu lệnh Copy-Paste chạy ngay | Mức an toàn Data |
| :--- | :--- | :---: | :--- | :---: |
| **Node X bị chết (`mX=ERR` / Crash)** | Service tắt hoặc panic | **KB 1** | `./ansible_deploy.sh --start --only-node X` | 🟢 Giữ nguyên Data |
| **Muốn tắt Node X để sửa / deploy lại** | Chủ động bảo trì Node X | **KB 2** | `./ansible_deploy.sh --stop --only-node X` | 🟢 An toàn |
| **Node X lệch hash / hỏng DB / tụt > 5 epoch** | Out-of-sync / Data corrupt | **KB 3** | `./ansible_deploy.sh --reset-all --only-node X --restore-node X --snapshot-url http://<IP>:8604` | 🟡 Chỉ reset data Node X |
| **Chuỗi đứng im (Chain Stall / block kẹt)** | Consensus kẹt round/view | **KB 4** | `./ansible_deploy.sh --restart` | 🟢 Giữ nguyên Data (2s) |
| **Cập nhật code mới từ Git cho toàn cụm** | Dev push binary mới | **KB 5** | `./ansible_deploy.sh --start` | 🟢 Giữ nguyên Data |
| **Node không kết nối được P2P / RPC lỗi** | Chưa mở cổng tường lửa | **KB 6** | `./ansible_deploy.sh --open-ports` | 🟢 Không ảnh hưởng Data |
| **Đang sửa Node X mà Telegram spam chuông** | Monitor phát hiện node tắt | **KB 7** | `echo "X" >> /tmp/monitors_ignore_nodes` | 🟢 Tắt báo động tạm thời |
| **Cần lấy log gửi cho Dev / Lead debug** | Lỗi lạ cần phân tích log | **KB 8** | `./fetch_node_logs.sh` | 🟢 Tự gom log về máy |
| **Cần bảo trì tắt toàn bộ mạng** | Shutdown server / hạ tầng | **KB 9** | `./ansible_deploy.sh --stop` | 🟢 Dừng an toàn |
| **Dựng mạng mới từ đầu (Genesis Block 0)** | Setup chain lần đầu | 🚨 **Đặc biệt** | `./ansible_deploy.sh --reset-all --open-ports` | 🔴 **XÓA TOÀN BỘ DATA** |

> 🚨 **QUY TẮC BẤT KHẢ XÂM PHẠM:**
> - **KHÔNG BAO GIỜ** dùng `./ansible_deploy.sh --reset-all` khi vận hành hàng ngày vì lệnh này sẽ **xóa sạch toàn bộ dữ liệu blockchain của cả cluster**!
> - Khi khôi phục 1 node từ snapshot, **BẮT BUỘC** phải có cờ `--only-node <ID>` đi kèm.

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
* **Hành vi:** Build binary mới $ightarrow$ Tắt service $ightarrow$ Chép binary mới $ightarrow$ Khởi động lại.
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

### 🚨 Kịch bản Đặc biệt: Khởi tạo Chain mới tinh (Genesis Block 0)
* **Khi nào dùng:** **CHỈ DÙNG ĐÚNG 1 LẦN** khi lần đầu tiên dựng mạng lưới mới, hoặc khi có sự cố rẽ nhánh bất khả kháng trên môi trường Testnet/Dev và **đã được sự đồng ý của toàn bộ Team/Lead**.
* **⚠️ CẢNH BÁO MẤT DỮ LIỆU:** Lệnh này sẽ **XÓA SẠCH HOÀN TOÀN TOÀN BỘ DATABASE VÀ KEYS TRÊN MỌI NODE**, đưa mạng về Block 0.
* **Câu lệnh:**
  ```bash
  ./ansible_deploy.sh --reset-all --open-ports
  ```

* **📍 Danh sách Key sinh ra nằm ở đâu?**
  Toàn bộ file key và genesis sau khi chạy lệnh sẽ nằm tại máy deploy ở thư mục:
  * **Keys của từng Node:** `deploy/systemd/node-0_keys/`, `node-1_keys/`, `node-2_keys/`... (chứa đủ 4 key: `authority_key.json`, `protocol_key.json`, `network_key.json`, `eth_key.json`).
  * **File Genesis chung:** `deploy/systemd/genesis.json`.

* **✏️ Muốn sửa Key hoặc Genesis thì làm thế nào?**
  1. **Sửa Key:** Vào thẳng thư mục `deploy/systemd/node-X_keys/` chép đè file key của bạn vào.
  2. **Sửa Genesis:** Mở `deploy/systemd/genesis.json` chỉnh Chain ID, ví nhận tiền (`alloc`) hoặc thông số tuỳ ý.
  3. **Deploy lên server:** Mở `deploy/ansible` và chạy lệnh cập nhật:
     ```bash
     ./ansible_deploy.sh --start
     # Hoặc nếu muốn xóa sạch DB cũ để chạy từ block 0 với bộ key mới vừa sửa:
     ./ansible_deploy.sh --clean
     ```
     > ⚠️ **LƯU Ý QUAN TRỌNG:** **KHÔNG DÙNG `--reset-all`** sau khi đã sửa, vì `--reset-all` sẽ tự động đúc đè mất bộ key bạn vừa chỉnh sửa!

---

## 5. Tự Tạo Key & Chạy Độc Lập Bằng Các Tool Sẵn Có (Không Cần Sửa Ansible)

> 💡 **NGUYÊN TẮC CỐT LÕI:**
> * **Script Ansible (`ansible_deploy.sh`)** được thiết kế để tự động hóa toàn diện từ A-Z (tự gen key mẫu, tạo genesis, thiết lập systemd service, phân phối và khởi chạy cụm mạng). Khi vận hành thông thường, bạn **không cần can thiệp hay sửa đổi bất kỳ code nào trong Ansible**.
> * **Nếu bạn muốn tự tạo key riêng, dựng chuỗi riêng (Private Chain) hoặc chạy thử nghiệm node độc lập:** Bạn **TUYỆT ĐỐI KHÔNG CẦN SỬA CODE ANSIBLE**. Trong repository Metanode đã tích hợp sẵn toàn bộ các bộ công cụ (tools) độc lập. Bạn chỉ cần mở terminal, **tự `cd` trực tiếp tới các thư mục tool đó để tạo key và cấu hình theo ý muốn**.

---

### 5.1. Danh Mục Các Tool Sẵn Có & Cách `cd` Tới Sử Dụng

Bạn có thể tự mở terminal và `cd` tới các thư mục sau để sử dụng các công cụ có sẵn:

#### 🛠️ Nhóm 1: Thư mục `deploy/systemd/` — Bộ Tool Quản Lý Key & Dựng Chain Độc Lập
Thư mục [`deploy/systemd/`](../systemd/) là trung tâm của các script Python/Bash chạy trực tiếp không qua Ansible:

1. **Tự sinh bộ Key + Genesis Entry cho 1 Validator bất kỳ:**
   ```bash
   cd deploy/systemd

   python3 gen_validator_entry.py \
     --hostname node-0 \
     --node-id 0 \
     --ip 127.0.0.1 \
     --keys-dir ./my_validator_0_keys \
     --output ./my_validator_0_keys/node-0_genesis.json
   ```
   *👉 Tự sinh đủ 4 file key (BLS, Eth ví, P2P network, Protocol) và xuất ra file JSON entry để gắn vào genesis.*

2. **Tự sinh toàn bộ Genesis & Keys cho một Private Chain mới (ví dụ 4 nodes):**
   ```bash
   cd deploy/systemd

   python3 gen_single_chain.py --chain-id 991 --total-nodes 4
   ```
   *👉 Tự động tạo một mạng blockchain độc lập với Chain ID 991 và sinh sẵn thư mục key cho 4 node.*

3. **Tự khởi chạy 1 node cục bộ trên máy cá nhân (không cần cụm server):**
   ```bash
   cd deploy/systemd

   ./setup_and_run.sh
   ```
   *👉 Dành cho dev muốn chạy test 1 node local nhanh trên máy để debug hoặc test smart contract.*

4. **Dựng cụm Root Anchor 4-validator (Kiến trúc Cross-Chain đa chuỗi):**
   ```bash
   cd deploy/systemd

   bash setup_root_anchor.sh --clean
   ```
   *👉 Dựng cụm 4 node Root Anchor (Chain 9099) phục vụ kiểm thử mô hình multi-chain.*

5. **Dừng toàn bộ các node đang chạy độc lập trên máy local:**
   ```bash
   cd deploy/systemd

   bash stop.sh
   ```

---

#### 🛠️ Nhóm 2: CLI Rust `metanode keytool` — Công Cụ Sinh Khóa Mật Mã Cốt Lõi
Nằm trực tiếp trong binary `metanode` (sau khi build `cargo build --release`):

```bash
# Đứng tại thư mục gốc của repository
cd metanode

# 1. Sinh trọn bộ 4 key cho 1 Validator trong 1 câu lệnh duy nhất:
./target/release/metanode keytool generate validator --out-dir ./my_keys

# 2. Sinh riêng lẻ từng loại khóa nếu chỉ muốn tạo/thay thế 1 loại:
./target/release/metanode keytool generate bls --out-dir ./my_keys      # Khóa BLS12-381 (Consensus commit vote)
./target/release/metanode keytool generate eth --out-dir ./my_keys      # Khóa ví Ethereum secp256k1 (Execution)
./target/release/metanode keytool generate network --out-dir ./my_keys  # Khóa Libp2p Ed25519 (P2P Network)
./target/release/metanode keytool generate protocol --out-dir ./my_keys # Khóa Protocol Ed25519 (Consensus internal)

# 3. Xem và trích xuất Public Key từ file khóa đã có:
./target/release/metanode keytool show authority-key --file ./my_keys/authority_key.json
./target/release/metanode keytool show eth-key --file ./my_keys/eth_key.json
```

---

#### 🛠️ Nhóm 3: Thư mục `execution/cmd/tool/` — Các Công Cụ Go Chuyên Dụng
Dành cho các nhu cầu chuyên sâu ở tầng Execution Layer:

1. **`execution/cmd/tool/gen_bls` — Sinh cặp khóa BLS thuần túy bằng Go:**
   ```bash
   cd execution/cmd/tool/gen_bls
   go run main.go
   ```

2. **`execution/cmd/tool/founding_entry` & `assemble_root_anchor` — Nghi thức Genesis Ceremony (Phi tập trung):**
   - Áp dụng khi nhiều đối tác/validator độc lập cùng tham gia khởi tạo mạng (mỗi bên tự giữ private key, không chia sẻ cho ai).
   - Mỗi bên tự `cd execution/cmd/tool/founding_entry` chạy tool để tạo file `founding_entry.json` (chỉ chứa public key).
   - Điều phối viên thu thập các file public entry và `cd execution/cmd/tool/assemble_root_anchor` ráp lại thành `genesis.json` chính thức.
   *(Chi tiết xem tài liệu `note/runbook_root_anchor_genesis_ceremony.md`).*

---

### 5.2. Kiến Trúc 4 Loại Khóa Bắt Buộc Của Mỗi Validator

Khi bạn dùng bất kỳ tool nào ở trên để tạo key cho 1 Validator, luôn đảm bảo thư mục của node có đủ 4 file sau:

| Tên file Key | Thuật toán | Vai trò trong mạng |
| :--- | :--- | :--- |
| **`authority_key.json`** | **BLS12-381** | **Khóa biểu quyết Consensus:** Dùng để ký commit votes trong mạng BFT. Cực kỳ quan trọng, quyết định tính hợp lệ của vote. |
| **`protocol_key.json`** | **Ed25519** | **Khóa giao thức:** Xác thực các gói tin trao đổi nội bộ giữa các Validator trong consensus engine. |
| **`network_key.json`** | **Ed25519** | **Khóa mạng P2P:** Định danh máy chủ trong mạng lưới Libp2p, mã hóa kết nối giữa các node. |
| **`eth_key.json`** | **secp256k1** | **Khóa ví Execution:** Chứa địa chỉ ví Ethereum (`0x...`) của validator, dùng nhận phần thưởng block và quản lý stake. |
| **`keys_summary.json`** | JSON | File tóm tắt các Public Key tương ứng (dùng để copy nhanh vào file `genesis.json`). |

---

### 5.3. Hướng Dẫn: Muốn Chạy Riêng / Dùng Key Riêng Thì Làm Thế Nào?

Tùy theo nhu cầu thực tế của bạn:

#### 🎯 Trường hợp A: Muốn chạy Node / Chain hoàn toàn độc lập (Không dùng Ansible)
* **Mục đích:** Chạy local devnet, smoke test, hoặc tự quản lý node trên VPS cá nhân qua systemd mà không cần qua cụm Ansible.
* **Cách thực hiện:**
  1. `cd deploy/systemd`
  2. Tạo chuỗi riêng: `python3 gen_single_chain.py --chain-id 991 --total-nodes 4`
  3. Hoặc chạy nhanh node đơn lẻ: `./setup_and_run.sh`
  4. Quản lý trạng thái bằng `bash stop.sh` hoặc xem log trực tiếp tại `logs/execution/...`.

#### 🎯 Trường hợp B: Tự tạo Key riêng nhưng VẪN MUỐN DÙNG ANSIBLE để deploy lên nhiều Server
* **Mục đích:** Bạn muốn dùng Ansible để tự động hóa việc chép binary, cấu hình systemd, mở port trên 5-10 server từ xa, nhưng muốn dùng **bộ key và địa chỉ ví của riêng bạn** thay vì để Ansible tự tạo ngẫu nhiên.
* **Cách thực hiện (HOÀN TOÀN KHÔNG CẦN SỬA CODE ANSIBLE):**
  1. **Tự tạo key trước:**
     - Mở terminal: `cd deploy/systemd`
     - Chạy `python3 gen_validator_entry.py` cho từng node (node-0, node-1, ...) vào các thư mục `deploy/systemd/node-X_keys/`.
     - Chỉnh sửa file `genesis.json` với Chain ID, địa chỉ ví và public keys tương ứng bạn vừa sinh.
  2. **Deploy lên cụm server mà KHÔNG làm mất key vừa tạo:**
     - Mở terminal: `cd deploy/ansible`
     - Cập nhật IP các server vào `inventory.yml`.
     - Chạy lệnh:
       ```bash
       ./ansible_deploy.sh --start
       ```
       *(Hoặc `./ansible_deploy.sh --clean` nếu muốn xóa sạch database cũ nhưng vẫn giữ nguyên keys).*
     - **Lưu ý:** **KHÔNG** dùng cờ `--reset-all` vì `--reset-all` sẽ kích hoạt Ansible tự động sinh đè lại key ngẫu nhiên. Lệnh `--start` sẽ copy nguyên vẹn bộ key và genesis bạn đã chuẩn bị sẵn lên toàn bộ cụm server!

---

## 6. Kiểm Tra Trạng Thái & Giám Sát Mạng

Sau khi thực hiện bất kỳ kịch bản nào, người mới có thể kiểm tra xem mạng đã hoạt động ổn định và các node đã bắt kịp nhau hay chưa:

### Cách 1: Chạy công cụ kiểm tra độ cao & hash thời gian thực (Khuyên dùng):
```bash
cd monitors/block_hash_checker
go run main.go --watch --interval 5s --config config-m-nodes.json --no-stop-flag
```
*Quan sát bảng `Heights: m0=185 m1=185 m2=185...` tăng đều và không còn chữ `ERR` là hệ thống đã hoàn toàn khỏe mạnh.*

### Cách 2: Kiểm tra cổng RPC sinh Block qua curl:
```bash
curl -s -X POST http://<IP_NODE_RPC>:10746 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```
*Nếu giá trị `result` (block hex) liên tục tăng theo thời gian là mạng đang hoạt động ổn định.*

### Cách 3: Hệ thống giám sát cảnh báo Telegram ngầm:
> 💡 **Tự động:** Khi bạn chạy `./ansible_deploy.sh` (dù là `--start` hay `--restart`), script đã **tự động khởi động hệ thống monitor ngầm** sau khi hoàn tất. Bạn **không cần phải gõ lệnh tay**.

Chỉ cần chạy thủ công nếu muốn bật lại monitor riêng lẻ:
```bash
cd deploy/ansible/monitors
./start_monitors.sh
```
