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
6. Tự động nộp transaction đăng ký cả 4 chuỗi lên Gateway của Root Anchor (`registerChainViaStake`
   — `bootstrapFoundingChains()` đã bị xoá 2026-08-28, không còn dùng nữa).
7. Tự động mint genesis supply 1 lần trên Reserve (Root Anchor) và chia cho 4 founding chain
   (`allocateSupplyWithCert` + `transferAllocationWithCert`, tự-ký bởi uỷ ban BLS thật của Reserve
   — không qua vote, qua `register_chains -fund-genesis`; `ProposalAllocateSupply`/
   `ProposalTransferAllocation` cùng toàn bộ `GovernanceEngine` propose/vote/timelock/execute đã
   bị xoá 2026-09-04, xem `note/eurozone_unified_native_coin_plan.md`)
   — **bắt buộc phải có bước này** thì Bước 5 (kiểm tra chuyển tiền cross-chain thật) mới chạy
   được, vì mỗi chain khởi tạo xong đều có `PerChainAllocation = 0` cho tới khi được cấp thật qua
   đúng luồng này (xem `note/cross_chain_attack_scenario_catalog.md` mục C7/C8 — sửa
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

## 🆕 5. Hướng Dẫn Đăng Ký Chain Mới & Quản Trị Hạn Mức

Tất cả các tác vụ quản trị Gateway Contract (`0x1002`) đã được hợp nhất vào công cụ duy nhất: [`register_chains`](file:///home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/register_chains/README.md).

```bash
cd /home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/register_chains
go build -o register_chains .
```

### Bước 1: Khởi động node cho Chain Mới (nếu dùng Ansible)
```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_private_chains
./deploy_private_chains.sh --setup --chain=103 --open-ports
```

### Bước 2: Đăng ký danh bạ & Khóa BLS (`register`)
Tool tự động đọc khóa BLS, tạo bằng chứng PoP và đăng ký danh bạ chéo lên Root Anchor và toàn bộ các Private Chains:
```bash
cd /home/abc/nhat/con-chain-v2/metanode/execution/cmd/tool/register_chains
./register_chains -chains "101,102,103"
```

### Bước 3: Cấp hạn mức tiền cọc cho Chain Mới (`transfer-alloc`)
Nếu Chain Mới cần xuất tiền liên chuỗi (Value Transfer), trích chuyển hạn mức từ một chain đang dư (VD: Chain 101) sang Chain Mới:
```bash
./register_chains -action transfer-alloc -from-chain 101 -to-chain 103 -amount-mtn 10000000
```

### Bước 4: Tra cứu kiểm tra (`query-alloc` & `query-registry`)
```bash
# Kiểm tra hạn mức của các chain:
./register_chains -action query-alloc -chains "991,101,102,103"

# Kiểm tra danh bạ và validator keys:
./register_chains -action query-registry -chains "101,102,103"
```

### Bước 5: Khởi động lại Relayer
```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_private_chains
./run_relayer_tmux.sh restart
```

