# 📖 SỔ TAY VẬN HÀNH & HƯỚNG DẪN TRIỂN KHAI TOÀN DIỆN
### HỆ THỐNG BLOCKCHAIN METANODE (PUBLIC ROOT ANCHOR + PRIVATE CHAINS + CROSS-CHAIN RELAYER)

---

## 📑 MỤC LỤC
1. [Tổng quan Kiến trúc Hệ thống & Cổng Dịch vụ (Topology)](#1-tổng-quan-kiến-trúc-hệ-thống--cổng-dịch-vụ)
2. [Quy trình Triển khai Mới Toàn Bộ Hệ Thống (4 Bước Chuẩn)](#2-quy-trình-triển-khai-mới-toàn-bộ-hệ-thống)
   - [Bước 1: Cấu hình Inventory](#bước-1-cấu-hình-inventory)
   - [Bước 2: Triển khai Cụm Public Chain (Root Anchor 991)](#bước-2-triển-khai-cụm-public-chain-root-anchor-991)
   - [Bước 3: Triển khai & Đăng ký Private Chains (101 & 102)](#bước-3-triển-khai--đăng-ký-private-chains-101--102)
   - [Bước 4: Khởi chạy Cross-Chain Relayer Daemon](#bước-4-khởi-chạy-cross-chain-relayer-daemon)
3. [Quy trình Kiểm thử & Xác thực Mạng Lưới (Verification & Testing)](#3-quy-trình-kiểm-thử--xác-thực-mạng-lưới)
4. [Các Lệnh Vận Hành Thường Nhật (Day-2 Operations Cheat Sheet)](#4-các-lệnh-vận-hành-thường-nhật)
   - [Nâng cấp Code (Không làm mất dữ liệu)](#nâng-cấp-code-không-làm-mất-dữ-liệu)
   - [Khởi động lại nhanh Service](#khởi-động-lại-nhanh-service)
   - [Thao tác trên 1 Node / 1 Chain duy nhất](#thao-tác-trên-1-node--1-chain-duy-nhất)
   - [Thu thập Logs & Giám sát từ xa](#thu-thập-logs--giám-sát-từ-xa)
5. [Sổ tay Xử lý Sự cố & Cứu Hộ Khẩn Cấp (Troubleshooting Runbook)](#5-sổ-tay-xử-lý-sự-cố--cứu-hộ-khẩn-cấp)

---

## 1. Tổng quan Kiến trúc Hệ thống & Cổng Dịch vụ

Toàn bộ hệ thống Metanode gồm 3 thành phần chính hoạt động phối hợp:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    ROOT ANCHOR CLUSTER (Public Chain - ChainID: 991)        │
│    Node 0 (:10746)    Node 1 (:10747)    Node 2 (:10748)    Node 3 (:10749)  │
│    • BFT Quorum: 2f + 1 (>= 3/4 validators)                                  │
│    • Quản trị ChainRegistry, Mint Reserve, Phân bổ Hạn ngạch (Allocation)   │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                         Cross-Chain Relayer Daemon (tmux)
                                       │
        ┌──────────────────────────────┴──────────────────────────────┐
        ▼                                                             ▼
┌──────────────────────────────────────┐    ┌─────────────────────────────────┐
│ PRIVATE CHAIN 101 (Source Chain)     │    │ PRIVATE CHAIN 102 (Dest Chain)  │
│ • RPC Port: 8546                     │    │ • RPC Port: 8547                │
│ • TCP P2P: 20210, Consensus: 10210   │    │ • TCP P2P: 20220, Consensus: 10220│
│ • Thư mục: /opt/metanode/chain-101   │    │ • Thư mục: /opt/metanode/chain-102│
└──────────────────────────────────────┘    └─────────────────────────────────┘
```

### Bảng phân bổ Cổng (Ports Map):
| Thành phần | Cổng RPC (HTTP JSON-RPC) | Cổng TCP Consensus / P2P | Thư mục cài đặt |
| :--- | :--- | :--- | :--- |
| **Root Anchor Node 0** | `10746` | `6200` (TCP), `9100` (P2P) | `/opt/metanode/node-0` |
| **Root Anchor Node 1** | `10747` | `6201` (TCP), `9101` (P2P) | `/opt/metanode/node-1` |
| **Root Anchor Node 2** | `10748` | `6202` (TCP), `9102` (P2P) | `/opt/metanode/node-2` |
| **Root Anchor Node 3** | `10749` | `6203` (TCP), `9103` (P2P) | `/opt/metanode/node-3` |
| **Private Chain 101** | `8546` | `20210` (TCP), `10210` (P2P) | `/opt/metanode/chain-101` |
| **Private Chain 102** | `8547` | `20220` (TCP), `10220` (P2P) | `/opt/metanode/chain-102` |

---

## 2. Quy trình Triển khai Mới Toàn Bộ Hệ Thống

Khi cài đặt mới trên một máy chủ mới hoặc reset toàn bộ mạng lưới từ đầu (block 0), đội ngũ vận hành thực hiện tuần tự **4 bước**:

### Bước 1: Cấu hình Inventory
Kiểm tra và cập nhật file `inventory.yml` theo đúng IP của máy chủ hiện tại:

1. **File cấu hình Root Anchor:** `deploy/ansible/inventory.yml`
```yaml
all:
  children:
    metanode_cluster:
      vars:
        ansible_user: "abc"
        ansible_ssh_pass: "1234@abcd"
        ansible_become_pass: "1234@abcd"
      hosts:
        192.168.1.232_validator:
          node_ids: [0]
          rpc_nodes: [0]
          ansible_connection: local
          ansible_host: 192.168.1.232
        192.168.1.233_validator:
          node_ids: [1]
          rpc_nodes: [1]
          ansible_host: 192.168.1.232
        192.168.1.230_validator:
          node_ids: [2]
          rpc_nodes: [2]
          ansible_host: 192.168.1.232
        192.168.1.231_validator:
          node_ids: [3]
          rpc_nodes: [3]
          ansible_host: 192.168.1.232
```

2. **File cấu hình Private Chains:** `deploy/ansible_private_chains/inventory.yml`
```yaml
all:
  vars:
    root_anchor_rpc: "http://192.168.1.232:10746"
    ansible_user: "abc"
    ansible_ssh_pass: "1234@abcd"
    ansible_become_pass: "1234@abcd"
    root_anchor_submitter_key: "d3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9"

  children:
    private_chains:
      hosts:
        server_201_chain_101:
          ansible_connection: local
          ansible_host: 127.0.0.1
          chain_id: 101
          rpc_port: 8546
          port_offset: 10
          install_dir: "/opt/metanode/chain-101"
        server_202_chain_102:
          ansible_connection: local
          ansible_host: 127.0.0.1
          chain_id: 102
          rpc_port: 8547
          port_offset: 20
          install_dir: "/opt/metanode/chain-102"
```

---

### Bước 2: Triển khai Cụm Public Chain (Root Anchor 991)
Chạy lệnh triển khai sạch:
```bash
cd /home/abc/chain-n/metanode/deploy/ansible
./ansible_deploy.sh --reset-all
```
*(Thêm cờ `--fast` nếu muốn bỏ qua khâu tối ưu hóa Rust compiler để deploy siêu tốc khi dev/test: `./ansible_deploy.sh --reset-all --fast`)*

**Xác thực sau khi chạy:**
Kiểm tra số block tăng trưởng trên RPC Node 0:
```bash
curl -s -X POST http://192.168.1.232:10746 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```
*Kết quả trả về dạng `{"jsonrpc":"2.0","id":1,"result":"0x1"}` (hoặc cao hơn) là cụm 4 node đã kết nối và đạt Quorum thành công.*

---

### Bước 3: Triển khai & Đăng ký Private Chains (101 & 102)
Chạy lệnh triển khai sạch cho các Private Chains:
```bash
cd /home/abc/chain-n/metanode/deploy/ansible_private_chains
./deploy_private_chains.sh --reset-all
```

**Tiến trình này tự động thực hiện các thao tác:**
1. Tạo thư mục `/opt/metanode/chain-101` và `/opt/metanode/chain-102`.
2. Đúc Genesis và Keys sạch cho từng Chain.
3. Kích hoạt Systemd service `metanode-private-101.service` và `metanode-private-102.service`.
4. Gọi tool `register_chains` để:
   - Gửi `registerChainViaStake(chain 101)` và `registerChainViaStake(chain 102)` lên Root Anchor (Chain 991).
   - Tạo Proposal mint Genesis Supply (400,000,000 MTN) lên quỹ Reserve của Root Anchor.
   - Bỏ phiếu (Vote) và thực thi chuyển Allocation (100,000,000 MTN) về từng Private Chain.
   - Đăng ký đối ứng Root Anchor và Private Chain lên danh bạ của Chain 101 và 102.

**Xác thực sau khi chạy:**
```bash
curl -s -X POST http://127.0.0.1:8546 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
curl -s -X POST http://127.0.0.1:8547 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

---

### Bước 4: Khởi chạy Cross-Chain Relayer Daemon
Relayer chịu trách nhiệm quét các Outbound Message trên Chain 101/102, nộp AttestCommit lên Root Anchor, và claim tiền/gọi smart contract tại chuỗi đích.

Chạy Relayer ngầm trong Tmux session:
```bash
cd /home/abc/chain-n/metanode/deploy/ansible_private_chains
./run_relayer_tmux.sh restart
```

**Các lệnh quản lý Relayer:**
* **Xem logs realtime:** `./run_relayer_tmux.sh logs`
* **Kiểm tra trạng thái:** `./run_relayer_tmux.sh status`
* **Tắt Relayer:** `./run_relayer_tmux.sh stop`
* **Truy cập Tmux Console:** `./run_relayer_tmux.sh attach` *(Nhấn `Ctrl+B`, sau đó bấm `D` để thoát ra ngoài mà không làm tắt Relayer)*

---

## 3. Quy trình Kiểm thử & Xác thực Mạng Lưới

Để kiểm tra toàn bộ tính năng chuyển tiền, hợp đồng thông minh xuyên chuỗi (EVM GMP), cơ chế hoàn tiền khi lỗi (Refund) và chống tấn công:

```bash
cd /home/abc/chain-n/metanode-suite/test-simple/test-rpc/test-blockstm/cross-chain/01-e2e-cross-chain-full
go run main.go
```

**Kỳ vọng kết quả đạt 100% (Passed):**
* ✅ BƯỚC 1: Health check 3 chain (Root Anchor 991, Chain 101, Chain 102)
* ✅ BƯỚC 2: Kiểm tra số dư ví ban đầu
* ✅ BƯỚC 3: Chuyển 500 MTN từ Chain 101 sang Chain 102 qua Root Anchor
* ✅ BƯỚC 4: Bảng đối soát số dư biến động đúng chính xác (`-501 MTN` ở Chain A, `+500 MTN` ở Chain B, `+1 MTN` tip cho Relayer)
* ✅ BƯỚC 5: Gọi hàm Smart Contract xuyên chuỗi `TestCounter.increment()` (0 ➔ 1)
* ✅ BƯỚC 6: Thử rút quá hạn ngạch ➔ Bị Root Anchor Gateway chặn (AllocationExceeded)
* ✅ BƯỚC 7: Thử gửi lại Proof cũ (Replay Attack) ➔ Bị từ chối ngay lập tức
* ✅ BƯỚC 8: Khóa Token gốc và Đúc Wrapped Token ERC-20 (AssetRegistry)
* ✅ BƯỚC 9: Diễn tập cứu hộ khi chain chết (Chain-Death Recovery Runbook)
* ✅ BƯỚC 10: Thử chuyển tiền vào contract bị lỗi ➔ Kích hoạt cơ chế Revert & Refund hoàn tiền 100% cho người gửi.

---

## 4. Các Lệnh Vận Hành Thường Nhật (Day-2 Operations)

### Nâng cấp Code (Không làm mất dữ liệu)
Khi dev push code mới và cần cập nhật binary lên toàn bộ cluster mà **giữ nguyên dữ liệu blockchain và khóa**:
```bash
# Cho Root Anchor:
cd /home/abc/chain-n/metanode/deploy/ansible
./ansible_deploy.sh --start

# Cho Private Chains:
cd /home/abc/chain-n/metanode/deploy/ansible_private_chains
./deploy_private_chains.sh --start
```

### Khởi động lại nhanh Service
Dùng khi chỉ cần khởi động lại các process Systemd:
```bash
# Root Anchor:
cd /home/abc/chain-n/metanode/deploy/ansible
./ansible_deploy.sh --restart

# Private Chains:
cd /home/abc/chain-n/metanode/deploy/ansible_private_chains
./deploy_private_chains.sh --restart
```

### Thao tác trên 1 Node / 1 Chain duy nhất
```bash
# Chỉ restart Node 2 của Root Anchor:
./ansible_deploy.sh --restart --only-node 2

# Chỉ dừng riêng Chain 101:
./deploy_private_chains.sh --stop --chain=101

# Chỉ xóa dữ liệu và khởi động lại riêng Chain 102:
./deploy_private_chains.sh --clean-data --chain=102
```

### Thu thập Logs & Giám sát từ xa
```bash
# Kéo logs toàn bộ 4 Node Root Anchor về máy:
cd /home/abc/chain-n/metanode/deploy/ansible
./fetch_node_logs.sh

# Kéo logs các Private Chains:
cd /home/abc/chain-n/metanode/deploy/ansible_private_chains
./fetch_node_logs.sh
```

---

## 5. Sổ tay Xử lý Sự cố & Cứu Hộ Khẩn Cấp (Troubleshooting)

### 🔴 Sự cố 1: Node Root Anchor bị kẹt ở Block 0 / Treo Quorum
- **Hiện tượng:** `curl eth_blockNumber` trả về lỗi hoặc log Rust báo `⚠️ Potential deadlock at commit 0, lacking Quorum to confirm! (Polled: X / Quorum: Y)`.
- **Nguyên nhân:** File `genesis.json` chứa số lượng validator nhiều hơn số node thực tế đang chạy (ví dụ genesis chứa 6 node nhưng cluster chỉ có 4 node).
- **Cách xử lý:**
  1. Kiểm tra file `deploy/ansible/inventory.yml` xem danh sách `node_ids` có đủ 4 node (0, 1, 2, 3) không.
  2. Chạy `./ansible_deploy.sh --reset-all` để script tự động xóa `genesis.json` cũ và đúc lại genesis chuẩn đúng 4 nodes.

### 🔴 Sự cố 2: Lỗi `registerChainViaStake requires cross_chain.min_native_stake_to_register_wei`
- **Hiện tượng:** Lệnh đăng ký chain bị revert trên Root Anchor hoặc Private Chain.
- **Nguyên nhân:** File `execution.json` thiếu trường cấu hình `min_native_stake_to_register_wei`.
- **Cách xử lý:** Đảm bảo `execution.json` (hoặc các script `gen_validator_entry.py` / `gen_single_chain.py`) có cấu hình:
  ```json
  "cross_chain": {
      "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198",
      "reserve_chain_id": 991,
      "min_native_stake_to_register_wei": "1000000000000000000",
      "devnet_governance_timelock_seconds_override": 10
  }
  ```

### 🔴 Sự cố 3: Lỗi `governance proposal not found` khi Vote Proposal
- **Hiện tượng:** Proposal được `propose` thành công nhưng bước `vote` hoặc `executeProposal` bị báo `governance proposal not found`.
- **Nguyên nhân:** Mã hash `proposalID` bị lệch timestamp giữa client và server consensus.
- **Cách xử lý:** Đảm bảo tool client luôn lấy `header.Time` từ `receipt.BlockNumber` của giao dịch `propose` để tính `proposalID`.

### 🔴 Sự cố 4: Xem Log Trực Tiếp trên Server qua Systemd
```bash
# Xem log Root Anchor Node 0:
journalctl -u metanode-execution-0.service -f

# Xem log Private Chain 101:
journalctl -u metanode-private-101.service -f

# Xem log Relayer:
tail -f /home/abc/chain-n/metanode/deploy/ansible_private_chains/relayer.log
```
