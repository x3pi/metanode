# 🌐 Hướng Dẫn Thiết Lập & Vận Hành Toàn Diện Multi-Machine Private Chains & Cross-Chain

Tài liệu này hướng dẫn chi tiết quy trình triển khai mạng lưới **4 Private Chains (101, 102, 103, 104)** độc lập trên nhiều máy chủ vật lý khác nhau (hoặc trên cùng 1 server) kết nối về **Root Anchor (Public Chain)**, tự động đăng ký danh bạ Gateway Precompile (`0x1002`), chạy Relayer Daemon ngầm và thực hiện giao dịch cross-chain từ phía Client.

---

## 📊 1. Bảng Thông Tin Mạng Lưới & Các Cổng Kết Nối

| Mạng | Chain ID | RPC Endpoint | Cài đặt tại | Vai trò |
| :--- | :---: | :--- | :--- | :--- |
| **Root Anchor (Public)** | `991` | `http://<IP_PUBLIC_CHAIN>:10746` | `/opt/metanode/node-0..3` | Hub bảo mật & quản lý trần cung toàn cầu |
| **Private Chain 1** | `101` | `http://<IP_CHAIN_101>:8546` | `/opt/metanode/chain-101` | Chuỗi ứng dụng 1 (Sender mặc định) |
| **Private Chain 2** | `102` | `http://<IP_CHAIN_102>:8547` | `/opt/metanode/chain-102` | Chuỗi ứng dụng 2 (Recipient mặc định) |
| **Private Chain 3** | `103` | `http://<IP_CHAIN_103>:8548` | `/opt/metanode/chain-103` | Chuỗi ứng dụng 3 |
| **Private Chain 4** | `104` | `http://<IP_CHAIN_104>:8549` | `/opt/metanode/chain-104` | Chuỗi ứng dụng 4 |


> **📄 File JSON Lưu Trữ Toàn Bộ IP & RPC Endpoint:**
> Sau khi chạy setup, Ansible tự động xuất file thông tin tại:
> [`/tmp/private_chains.json`](file:///tmp/private_chains.json)

---

## 🚀 2. Quy Trình Triển Khai 4 Private Chains Từ Đầu (Multi-Machine)

### 🔹 Bước 1: Cấu hình danh sách IP các máy trong `inventory.yml`
Chỉnh sửa file [`deploy/ansible_private_chains/inventory.yml`](file:///home/abc/nhat/consensus-chain/metanode/deploy/ansible_private_chains/inventory.yml) với IP và thông tin xác thực của các máy chủ:

```yaml
all:
  vars:
    # URL RPC của máy chạy Public Chain (Root Anchor)
    root_anchor_rpc: "http://<IP_PUBLIC_CHAIN>:10746"
    ansible_user: "<USER_NAME>"
    ansible_ssh_pass: "<SSH_PASSWORD>"
    ansible_become_pass: "<BECOME_PASSWORD>"
    ansible_connection: ssh  # Dùng ssh khi chạy trên các máy remote thật

  children:
    private_chains:
      hosts:
        # Máy 1: Chạy Chain 101
        server_chain_101:
          ansible_host: <IP_CHAIN_101>
          chain_id: 101
          rpc_port: 8546
          port_offset: 10
          install_dir: "/opt/metanode/chain-101"

        # Máy 2: Chạy Chain 102
        server_chain_102:
          ansible_host: <IP_CHAIN_102>
          chain_id: 102
          rpc_port: 8546
          port_offset: 20
          install_dir: "/opt/metanode/chain-102"

        # Máy 3: Chạy Chain 103
        server_chain_103:
          ansible_host: <IP_CHAIN_103>
          chain_id: 103
          rpc_port: 8546
          port_offset: 30
          install_dir: "/opt/metanode/chain-103"

        # Máy 4: Chạy Chain 104
        server_chain_104:
          ansible_host: <IP_CHAIN_104>
          chain_id: 104
          rpc_port: 8546
          port_offset: 40
          install_dir: "/opt/metanode/chain-104"
```

---

### 🔹 Bước 2: Triển khai 4 Private Chains Từ Đầu (`--reset-all` & `--open-ports`)
Mở terminal trên máy quản trị và chạy:

```bash
cd ~/nhat/consensus-chain/metanode/deploy/ansible_private_chains

# Lệnh triển khai sạch từ đầu + mở tường lửa UFW:
./deploy_private_chains.sh --reset-all --open-ports
```

**Quá trình này tự động thực hiện:**
1. Tạo user hệ thống `metanode` trên tất cả các máy chủ.
2. Sinh genesis block mới (pre-fund các tài khoản trong `private_dev_keys.json`).
3. Copy binary `simple_chain` và `metanode` vào `/opt/metanode/chain-XXX`.
4. Thiết lập systemd service `/etc/systemd/system/metanode-private-XXX.service`.
5. Bật service và mở tường lửa cho các cổng RPC, Peer, Consensus.
6. Tự động nộp transaction đăng ký cả 4 chuỗi lên Gateway của Root Anchor (bootstrapFoundingChains).
7. Tự động mint genesis supply 1 lần trên Reserve (Root Anchor) và chia cho 4 founding chain
   (`ProposalAllocateSupply` + `ProposalTransferAllocation`, qua `register_chains -fund-genesis`)
   — **bắt buộc phải có bước này** thì Bước 5 (kiểm tra chuyển tiền cross-chain thật) mới chạy
   được, vì mỗi chain khởi tạo xong đều có `PerChainAllocation = 0` cho tới khi được cấp thật qua
   đúng luồng governance này (xem `note/cross_chain_attack_scenario_catalog.md` mục C7/C8 — sửa
   2026-08-28, PR #84 review). Số lượng mint mặc định là giá trị devnet
   (`root_anchor_genesis_supply`/`root_anchor_per_chain_allocation` trong `inventory.yml`, có thể
   chỉnh) — **không dùng mặc định này cho triển khai thật**, số thật phải qua ceremony quyết định.

---

### 🔹 Bước 3: Kiểm tra trạng thái hoạt động của 4 Private Chains
```bash
./deploy_private_chains.sh --status
```
> Kết quả mong đợi: Cả 4 chuỗi đều báo `Block Number 0x1` trở lên.

---

### 🔹 Bước 4: Khởi chạy Cross-Chain Relayer Daemon bằng `tmux` (1 Lệnh Tự Động)

Chỉ cần chạy script [`run_relayer_tmux.sh`](file:///home/abc/nhat/consensus-chain/metanode/deploy/ansible_private_chains/run_relayer_tmux.sh), script sẽ **tự động đọc IP và các chain từ `/tmp/private_chains.json`**, khởi tạo phiên `tmux` tên `relayer`, biên dịch và chạy ngầm, đồng thời ghi log cùng cấp tại `relayer.log`:

```bash
cd ~/nhat/consensus-chain/metanode/deploy/ansible_private_chains

# 1 Lệnh duy nhất khởi chạy Relayer trong tmux:
./run_relayer_tmux.sh
```

#### 📋 Các lệnh quản lý Relayer thuận tiện:
* **Xem log realtime:** `./run_relayer_tmux.sh logs` *(hoặc `tail -f relayer.log`)*
* **Kiểm tra trạng thái:** `./run_relayer_tmux.sh status`
* **Vào màn hình tmux tương tác:** `./run_relayer_tmux.sh attach` *(Thoát ra bấm `Ctrl+B` rồi bấm `D`)*
* **Dừng Relayer:** `./run_relayer_tmux.sh stop`


---

### 🔹 Bước 5: Chạy kịch bản Client kiểm tra chuyển tiền (`02-client-only-transfer`)
```bash
cd ~/nhat/consensus-chain/metanode-suite/test-simple/test-rpc/test-blockstm/cross-chain/02-client-only-transfer

go run . -rpcA "http://<IP_CHAIN_101>:8546" -rpcB "http://<IP_CHAIN_102>:8546"
```

---

## 🛠️ 3. Bảng Lệnh Quản Lý Vận Hành Hàng Ngày

| Nhu Cầu | Câu Lệnh Thực Hiện |
| :--- | :--- |
| **Dừng duy nhất 1 chain (ví dụ Chain 101)** | `./deploy_private_chains.sh --stop --chain=101` |
| **Khởi động duy nhất 1 chain (ví dụ Chain 101)** | `./deploy_private_chains.sh --start --chain=101` |
| **Restart duy nhất 1 chain (ví dụ Chain 102)** | `./deploy_private_chains.sh --restart --chain=102` |
| **Dừng tất cả 4 chains** | `./deploy_private_chains.sh --stop` |
| **Khởi động lại tất cả 4 chains** | `./deploy_private_chains.sh --restart` |
| **Xóa dữ liệu DB riêng Chain 102 (giữ keys & genesis)** | `./deploy_private_chains.sh --clean-data --chain=102` |
| **Xóa dữ liệu DB tất cả 4 chains** | `./deploy_private_chains.sh --clean-data` |
| **Kéo logs tất cả các node về xem** | `./fetch_node_logs.sh` |
| **Kéo logs riêng Chain 101 về xem** | `./fetch_node_logs.sh 101` |
| **Xem log realtime của 1 chain** | `journalctl -u metanode-private-101.service -f` |

---

## 🔐 4. Cơ Chế Xác Thực & Quản Lý Khóa (Lưu ý cho Dev & AI)

### 1. `BootstrapFoundingChains` & Xác thực Proof-of-Possession (`PopVerify`):
* **Nguyên lý:** Khi khởi tạo hoặc re-deploy/reset các Private Chain, hàm `BootstrapFoundingChains` trên Gateway Precompile (`0x1002`) cho phép nạp/cập nhật lại danh sách committee sáng lập của các chain vào `ChainRegistry`.
* **Bảo mật tuyệt đối:** Mọi validator entry bắt buộc phải đính kèm chữ ký Proof-of-Possession (`PopSignature`) hợp lệ và được kiểm tra nghiêm ngặt qua hàm `PopVerify(v.PubkeyBLS, v.PopSignature)`. Không có bất kỳ node hay validator nào có thể mạo danh hoặc nạp key giả vào Root Anchor.

### 2. Phân biệt & Trích xuất Khóa của Validator:
* **Khóa ETH (`eth_key.json` - secp256k1):** Dùng để định danh địa chỉ EVM (`address`), nộp gas fee và ký giao dịch thông thường.
* **Khóa BLS (`Databases.BLSPrivateKey` / `authority_key` - BLS12-381):** Dùng để ký các phần chữ ký xác thực khối và attestation cross-chain (`commitRoot` / `committeeUpdate`).
* **Cơ chế Fallback trích xuất BLS Public Key:** `CommitAttestationWorker` và `CommitteeAttestationWorker` luôn ưu tiên dẫn xuất trực tiếp BLS Public Key (48 bytes G1) từ `Databases.BLSPrivateKey` trong file cấu hình `execution.json`. Nếu không cấu hình mới tìm trong `AccountStateDB` (tránh lỗi rỗng tại Genesis khi trạng thái tài khoản chưa được nạp thông tin BLS vào state trie).

---

## 🆕 5. Hướng Dẫn Đăng Ký Chain Mới (Sau Genesis)

Đối với một Private Chain hoàn toàn mới (không nằm trong danh sách founding chains lúc khởi tạo), quy trình đăng ký hiện tại (từ bản vá 2026-08-28) là **hoàn toàn Permissionless (Không cần Vote)**. Bất kỳ người dùng nào có đủ số dư Native Coin thật trong ví đều có thể đăng ký chuỗi mới.

### Bước 1: Đăng ký danh bạ bằng Tiền cọc Thật (`RegisterChainViaStake`)
Để ngăn chặn Sybil Attack, thao tác đăng ký yêu cầu bạn phải nạp một khoản tiền cọc (bằng đúng `MinNativeStakeToRegister`). Số tiền này bị trừ trực tiếp từ số dư Native Coin (EVM) trong ví cá nhân của bạn và khóa lại tại Smart Contract (`GATEWAY_CONTRACT_ADDRESS`) trên Root Anchor.

Dùng công cụ `register_chains` để đăng ký Chain Mới. Tool sẽ tự đọc file `execution.json` của Chain Mới lấy Public Key BLS, tạo bằng chứng PoP hợp lệ, và dùng khóa Private Key ETH của bạn để trả tiền cọc:

```bash
cd ~/nhat/consensus-chain/metanode/execution/cmd/tool/register_chains
go build -o register_chains main.go

./register_chains \
  -chains <NEW_CHAIN_ID> \
  -chains-dir /home/abc/chain-n/metanode/deploy/ansible_private_chains/data \
  -root-anchor http://<IP_PUBLIC_CHAIN>:10746 \
  -target-rpcs "101=http://<IP_CHAIN_101>:8546,102=http://<IP_CHAIN_102>:8547,<NEW_CHAIN_ID>=http://<IP_NEW_CHAIN>:85XX" \
  -key <PRIVATE_KEY_ETH_CÓ_SẴN_SỐ_DƯ_ĐỂ_CỌC>
```
*(Ngay sau khi lệnh này chạy thành công, Smart Contract tự động trừ tiền ví của bạn và thêm ngay Chain Mới vào danh bạ của Root Anchor và tất cả các chuỗi đích mà **KHÔNG CẦN CHỜ AI VOTE DUYỆT**).*

### Bước 2: Đăng ký danh bạ các Chain cũ sang Chain mới
Để Chain mới nhận diện được tất cả các chain đã có từ trước (101, 102, 103...), chạy lệnh sau trỏ thẳng vào RPC của Chain mới:

```bash
./register_chains \
  -chains 101,102,103 \
  -chains-dir /home/abc/chain-n/metanode/deploy/ansible_private_chains/data \
  -root-anchor http://<IP_PUBLIC_CHAIN>:10746 \
  -target-rpcs "<NEW_CHAIN_ID>=http://<IP_NEW_CHAIN>:85XX" \
  -key <PRIVATE_KEY_ETH>
```

### Bước 3: Tích hợp Relayer (Auto-Discovery - Không cần khởi động lại)
Chương trình **Relayer Daemon** đã được trang bị tính năng dò tìm tự động (Dynamic Auto-Discovery). Nó sẽ tự động quét file cấu hình mỗi 2 giây. Bạn **không cần** phải khởi động lại Relayer.
- Chỉ cần mở file cấu hình gốc của mạng lưới (thường là `/tmp/private_chains.json`):
```bash
nano /tmp/private_chains.json
```
- Bổ sung Chain ID và RPC của chuỗi mới vào mục `nodes`. Ví dụ:
```json
"nodes": {
  "101": "http://<IP_CHAIN_101>:8546",
  "102": "http://<IP_CHAIN_102>:8547",
  "<NEW_CHAIN_ID>": "http://<IP_NEW_CHAIN>:85XX"
}
```
- Lưu file lại. Relayer sẽ tự động phát hiện, kết nối và bắt đầu định tuyến tin nhắn 2 chiều cho chuỗi mới ngay lập tức!
