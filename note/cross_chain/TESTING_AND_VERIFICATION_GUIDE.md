# 📘 Hướng Dẫn Chi Tiết Chạy Test Toàn Bộ Hệ Thống: Public Chain, Private Chain & Cross-Chain

Tài liệu này hướng dẫn từng bước kiểm chứng hệ thống đa chuỗi (**Multi-Chain System**) bao gồm:
1. **Public Chain (Root Anchor - Chain ID: 991)**: Mạng gốc quản lý an ninh, danh bạ Gateway, tổng cung và Data Availability (DA).
2. **Private Chains (Chain 101, Chain 102)**: Các chuỗi ứng dụng riêng biệt có thể lưu trữ dữ liệu và backup state/transactions lên Root Anchor theo cơ chế DA / Blob EIP-4844 tương tự Nitro/Arbitrum Orbit.
3. **Cross-Chain Relayer & Client Transfer**: Chuyển giao tài sản/thông điệp liên chuỗi giữa các Private Chain và Root Anchor qua Gateway Contract (`0x1002`).

---

## 🏗️ 1. Tổng Quan Kiến Trúc & Cổng Dịch Vụ (Ports)

| Tên Mạng | Chain ID | Vai Trò | RPC URL | Validator Nodes | Thư Mục Cài Đặt |
| :--- | :---: | :--- | :--- | :---: | :--- |
| **Root Anchor (Public)** | `991` | L1 Security Hub, Quản lý Tổng Cung, Lưu trữ Blob DA | `http://192.168.1.234:10746` | 5 Nodes (Node 0-4) | `/opt/metanode/node-0..4` |
| **Private Chain 1** | `101` | L2/AppChain (Sender test cross-chain) | `http://192.168.1.234:8546` | 1 Node (Node 0) | `/opt/metanode/chain-101/node-0` |
| **Private Chain 2** | `102` | L2/AppChain (Recipient test cross-chain) | `http://192.168.1.234:8566` | 1 Node (Node 0) | `/opt/metanode/chain-102/node-0` |

---

## 📋 2. Quy Trình Test Từng Bước (Step-by-Step)

### 🔹 Bước 1: Kiểm tra & Khởi động Public Chain (Root Anchor)
Script triển khai Public Chain bằng Ansible:
`metanode/deploy/ansible/ansible_deploy.sh`

1. **Kiểm tra trạng thái Public Chain:**
   ```bash
   cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible
   ./ansible_deploy.sh --status
   ```
   *Hoặc truy vấn RPC trực tiếp:*
   ```bash
   curl -s -X POST http://192.168.1.234:10746 \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
   ```
   *Kết quả mong đợi:* Trả về block number ở dạng hex (ví dụ `"result":"0x3e"` tức block 62 trở lên) và block tăng đều đặn.

2. **Nếu cần triển khai lại Public Chain từ đầu (Reset):**
   ```bash
   ./ansible_deploy.sh --reset-all --open-ports
   ```

---

### 🔹 Bước 2: Triển khai & Khởi động Private Chains (Chain 101 & 102)
Script quản lý Private Chains bằng Ansible:
`metanode/deploy/ansible_private_chains/deploy_private_chains.sh`

1. **Cấu hình danh bạ máy & thông số:**
   File cấu hình: `metanode/deploy/ansible_private_chains/inventory.yml`
   - Khai báo các chuỗi `chain-101`, `chain-102` cùng các cổng RPC `8546`, `8566`.

2. **Triển khai sạch hoặc khởi động lại Private Chains:**
   ```bash
   cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_private_chains
   
   # Triển khai mới hoàn toàn (reset dữ liệu + cấu hình dịch vụ):
   ./deploy_private_chains.sh --reset-all --open-ports
   ```

3. **Kiểm tra trạng thái Private Chains:**
   ```bash
   ./deploy_private_chains.sh --status
   ```
   *Hoặc truy vấn RPC:*
   ```bash
   # Chain 101:
   curl -s -X POST http://192.168.1.234:8546 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
   
   # Chain 102:
   curl -s -X POST http://192.168.1.234:8566 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
   ```

---

### 🔹 Bước 3: Đăng Ký Danh Bạ Cross-Chain & Cấp Hạn Mức Genesis
Để các chain nhận biết nhau và có hạn mức gửi tiền qua Gateway Precompile (`0x1002`), chạy lệnh đăng ký:

```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_private_chains
./deploy_private_chains.sh --register
```

**Tự động thực hiện:**
1. Trích xuất BLS Public Key & Proof-of-Possession (PoP) của Chain 991, 101, 102.
2. Gọi hàm `registerChainViaStake` trên Root Anchor để đăng ký 101 và 102.
3. Gọi hàm `registerChainViaStake` trên Chain 101 và 102 để đăng ký Root Anchor (991) và chuỗi đối tác.
4. Mint và phân bổ quỹ Genesis Supply trên Root Anchor cho các chuỗi (`allocateSupplyWithCert`).

---

### 🔹 Bước 4: Khởi Chạy Cross-Chain Relayer Daemon
Relayer là dịch vụ lắng nghe sự kiện từ các chain và chuyển tiếp chứng chỉ xác thực (Attestation / State Commitments) giữa các chain:

```bash
cd /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_private_chains

# 1. Khởi chạy Relayer trong tmux:
./run_relayer_tmux.sh start

# 2. Kiểm tra trạng thái:
./run_relayer_tmux.sh status

# 3. Xem log thời gian thực:
./run_relayer_tmux.sh logs
# (Hoặc: tail -f relayer.log)
```

---

### 🔹 Bước 5: Chạy Kịch Bản Test Chuyển Tiền Cross-Chain (Client Transfer)
Chạy script kiểm thử giao dịch chuyển tiền từ Private Chain 101 sang Private Chain 102:

```bash
cd /home/abc/nhat/con-chain-v2/metanode-suite/test-simple/test-rpc/test-blockstm/cross-chain/02-client-only-transfer

go run . \
  -rpcA "http://192.168.1.234:8546" \
  -rpcB "http://192.168.1.234:8566"
```

**Tiêu chí thành công:**
- Giao dịch Lock/Burn trên Chain 101 được sinh ra và xác nhận thành công.
- Relayer bắt được sự kiện và tạo bằng chứng Merkle / BLS Attestation.
- Giao dịch Mint/Unlock được thực thi tự động trên Chain 102.
- Số dư tài khoản đích trên Chain 102 tăng đúng bằng số tiền gửi.

---

### 🔹 Bước 6: Kiểm Tra Cơ Chế Backup & Data Availability (DA / Blob EIP-4844)
Để kiểm tra việc Private Chain đăng batch giao dịch / state snapshot lên Root Anchor để lưu trữ phục vụ Disaster Recovery (khôi phục sau sự cố tương tự Nitro):

1. **Gửi batch giao dịch DA lên Root Anchor:**
   Sử dụng công cụ batch submission hoặc RPC:
   - Dữ liệu giao dịch được nén và đóng gói thành Blob EIP-4844 hoặc giao dịch calldata gửi về Root Anchor.
   - Blob hash được lưu trên Root Anchor tại block tương ứng.

2. **Kiểm tra Blob / DA trên Root Anchor:**
   ```bash
   # Truy vấn thông tin block chứa blob hoặc giao dịch DA:
   curl -s -X POST http://192.168.1.234:10746 \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true],"id":1}'
   ```

3. **Thử nghiệm khôi phục từ Anchor (Disaster Recovery):**
   ```bash
   cd /home/abc/nhat/con-chain-v2/metanode/deploy/cluster/local_devnet
   # Script kiểm tra restore state từ anchor data:
   go run ./recover_from_anchor.go -anchor-rpc "http://192.168.1.234:10746" -chain-id 101
   ```

---

## 🛠️ 3. Bảng Tra Cứu Lệnh Vận Hành Nhanh

| Mục Đích | Câu Lệnh |
| :--- | :--- |
| **Xem log Public Chain (Node 0)** | `journalctl -u metanode-execution-0.service -f` |
| **Xem log Private Chain 101** | `tail -f /opt/metanode/chain-101/node-0/logs/execution/$(date +%Y-%m-%d)/execution.log` |
| **Xem log Private Chain 102** | `tail -f /opt/metanode/chain-102/node-0/logs/execution/$(date +%Y-%m-%d)/execution.log` |
| **Xem log Relayer** | `tail -f /home/abc/nhat/con-chain-v2/metanode/deploy/ansible_private_chains/relayer.log` |
| **Restart Private Chain 101** | `sudo systemctl restart metanode-private-101-node-0.service` |
| **Restart Private Chain 102** | `sudo systemctl restart metanode-private-102-node-0.service` |
| **Dừng Relayer** | `./run_relayer_tmux.sh stop` |
| **Khởi động lại Relayer** | `./run_relayer_tmux.sh restart` |
