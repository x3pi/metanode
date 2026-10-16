# 📖 SỔ TAY VẬN HÀNH & HƯỚNG DẪN TRIỂN KHAI (OPERATIONS GUIDE)

Tài liệu này hướng dẫn quy trình triển khai, vận hành thường nhật (Day-2 Operations), giám sát và xử lý sự cố cho hệ thống blockchain **MetaNode** theo hai mô hình triển khai chuẩn:
1. **Mô hình Đa Cụm (Multi-Cluster: Parent Chain + Execution Clusters):** Quản lý bởi [`deploy/ansible_clusters/deploy_clusters.sh`](./deploy/ansible_clusters/deploy_clusters.sh).
2. **Mô hình Cụm Đơn BFT (Single BFT Cluster: 4 Validators + RPC/Explorer):** Quản lý bởi [`deploy/ansible/ansible_deploy.sh`](./deploy/ansible/ansible_deploy.sh).

---

## 📑 MỤC LỤC
1. [Kiến trúc Triển khai & Bản đồ Cổng Dịch vụ (Topology & Ports)](#1-kiến-trúc-triển-khai--bản-đồ-cổng-dịch-vụ)
2. [Triển khai Hệ thống Đa Cụm (`deploy_clusters.sh`)](#2-triển-khai-hệ-thống-đa-cụm-deploy_clusterssh)
3. [Triển khai Cụm Đơn BFT (`ansible_deploy.sh`)](#3-triển-khai-cụm-đơn-bft-ansible_deploysh)
4. [Các Lệnh Vận Hành Thường Nhật (Day-2 Operations)](#4-các-lệnh-vận-hành-thường-nhật-day-2-operations)
5. [Kiểm thử & Xác thực Sức khỏe Mạng lưới](#5-kiểm-thử--xác-thực-sức-khỏe-mạng-lưới)
6. [Sổ tay Xử lý Sự cố & Nguyên tắc An toàn (Troubleshooting Runbook)](#6-sổ-tay-xử-lý-sự-cố--nguyên-tắc-an-toàn)

---

## 1. Kiến trúc Triển khai & Bản đồ Cổng Dịch vụ

```text
PARENT SETTLEMENT & REGISTRY CHAIN  (cmd/parent_chain, Rust BFT consensus, HTTP REST API)
  • Float Accounts, Chain/Rollup Registry, Account Registration Gate
        ▲  Batch Anchor / Co-Attestation
        │
EXECUTION CLUSTERS  (cmd/simple_chain; mode BFT (Mysticeti) hoặc Raft)
  • JSON-RPC (EVM) + TCP Ingress + P2P cho từng validator / RPC / SyncOnly node
```

ChainID và cổng **không cố định trong tài liệu này** — nguồn sự thật là inventory Ansible:
[`deploy/ansible_clusters/inventory.example.yml`](./deploy/ansible_clusters/inventory.example.yml)
(`chain_id`, `parent_chain_id`, `parent_http_port`, `peer_rpc_port`, `rpc_port`, `p2p_port`, ...).
Ví dụ mặc định trong file đó: Parent Chain và Execution Cluster đều dùng ChainID `991`
(`DefaultParentChainID` trong `execution/pkg/parentchain/tx.go`, được ghi đè từ genesis).
Xem thêm [`deploy/ansible_clusters/README.md`](./deploy/ansible_clusters/README.md).

---

## 2. Triển khai Hệ thống Đa Cụm (`deploy_clusters.sh`)

Thư mục [`deploy/ansible_clusters/`](./deploy/ansible_clusters/) tự động hóa toàn bộ quy trình biên dịch, phân phối binary, khởi tạo Genesis, đăng ký Ủy ban BLS và khởi chạy Parent Chain cùng các Execution Clusters.

### 2.1. Khởi tạo mới toàn bộ hệ thống (Từ Block 0)
```bash
cd deploy/ansible_clusters

# Cài đặt cấu hình, build toàn bộ Go + Rust FFI và khởi chạy sạch:
./deploy_clusters.sh --setup

# Hoặc reset toàn bộ dữ liệu hiện có và chạy lại kèm bộ kiểm thử tích hợp:
./deploy_clusters.sh --reset --test
```

### 2.2. Các cờ điều khiển phạm vi (`--parent-only`, `--exec-only`, `--node`, `--host`)
```bash
# Chỉ cập nhật và khởi động lại Parent Chain:
./deploy_clusters.sh --deploy --parent-only

# Chỉ thao tác trên các Execution Clusters:
./deploy_clusters.sh --restart --exec-only

# Chỉ thao tác trên 1 node cụ thể (ví dụ: p0, exec1_r1):
./deploy_clusters.sh --restart --node=exec1_r1

# Chỉ thao tác trên các node thuộc 1 máy chủ IP cụ thể:
./deploy_clusters.sh --restart --host=192.168.1.223

# Kiểm tra trạng thái RPC và chiều cao khối của toàn bộ cụm:
./deploy_clusters.sh --status
```

---

## 3. Triển khai Cụm Đơn BFT (`ansible_deploy.sh`)

Thư mục [`deploy/ansible/`](./deploy/ansible/) dùng cho mô hình cụm 4 Validator + Node RPC/Explorer tiêu chuẩn.

```bash
cd deploy/ansible

# Triển khai mới từ đầu (tạo key, xóa DB cũ, khởi tạo từ block 0):
./ansible_deploy.sh --setup

# Cập nhật binary mới (giữ nguyên dữ liệu blockchain và khóa):
./ansible_deploy.sh --start

# Khởi động lại nhanh systemd services:
./ansible_deploy.sh --restart

# Chỉ thao tác trên 1 node cụ thể (ví dụ Node 2):
./ansible_deploy.sh --restart --only-node 2

# Khôi phục 1 node từ Snapshot của node khác:
./ansible_deploy.sh --restore-node 3 --snapshot-url http://192.168.1.232:8600
```

---

## 4. Các Lệnh Vận Hành Thường Nhật (Day-2 Operations)

### 4.1. Kiểm tra biên dịch trước khi triển khai
Trước khi đẩy code lên cụm, luôn chạy script kiểm tra biên dịch toàn diện (Rust FFI + C++ MVM/Xapian + Go):
```bash
cd consensus/metanode/scripts
./build_check.sh
```

### 4.2. Quản lý tiến trình Monitor ngầm
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --monitor-status   # Xem trạng thái monitor
./deploy_clusters.sh --monitor-only     # Bật lại monitor ngầm
./deploy_clusters.sh --stop-monitor     # Dừng monitor ngầm
```

### 4.3. Mở cổng tường lửa (UFW)
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --open-ports
```

---

## 5. Kiểm thử & Xác thực Sức khỏe Mạng lưới

### 5.1. Kiểm tra chiều cao khối trên Execution Cluster (EVM JSON-RPC)
Thay `<RPC_PORT>` bằng `rpc_port` của node trong inventory (ví dụ mẫu: `8747`).
```bash
curl -s -X POST http://127.0.0.1:<RPC_PORT> \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

### 5.2. Kiểm tra Parent Chain (HTTP REST)
Parent Chain **không** dùng JSON-RPC; API là các route HTTP trong `execution/pkg/parentchain/http_rpc.go`
(`/status`, `/validators`, `/account`, `/block`, `/receipt`, `/state_root`, `/metrics`, ...).
Thay `<PARENT_HTTP_PORT>` bằng `parent_http_port` trong inventory.
```bash
curl -s http://127.0.0.1:<PARENT_HTTP_PORT>/status
curl -s http://127.0.0.1:<PARENT_HTTP_PORT>/validators
```
Tham số cụ thể của `/account`, `/block`, `/receipt`: xem handler tương ứng trong `http_rpc.go`.

### 5.3. Chạy bộ kiểm thử tích hợp sau triển khai
```bash
cd deploy/ansible_clusters
./deploy_clusters.sh --test-only
```

---

## 6. Sổ tay Xử lý Sự cố & Nguyên tắc An toàn

1. **Nguyên tắc Tối thượng — Zero-Fork Invariant:**
   - Hệ thống **thà giữ commit ở trạng thái `PENDING` (chờ) chứ tuyệt đối không fork**.
   - Một commit chỉ được chuyển sang Go thực thi khi có đủ bằng chứng $2f+1$ peers đồng ý trên cùng digest (`CommitVoteMonitor` / `PeerAttestResult::Ok`) hoặc ở trạng thái `TRUE cold-start`.
   - Nếu chỉ có $< 2f+1$ validator đang hoạt động (ví dụ: chỉ bật 2/4 validator trong cụm BFT), mạng lưới sẽ chủ động tạm dừng tạo block mới và tự động tiến triển ngay khi validator thứ 3 hoạt động trở lại.
2. **Xử lý khi 1 Node bị tụt hậu xa (Lagging Node):**
   - Dùng tính năng khôi phục từ Snapshot: `./ansible_deploy.sh --restore-node <N> --snapshot-url <URL>` hoặc khởi động lại node để `CommitSyncer` đồng bộ các `CertifiedCommit` còn thiếu từ mạng P2P.
3. **Kiểm tra Logs:**
   - Sử dụng `journalctl -u metanode-* -f` hoặc kiểm tra trực tiếp file log trong thư mục cài đặt của từng node (`/opt/metanode/.../logs/`).
   - Các từ khóa quan trọng khi chẩn đoán: `ZERO-FORK`, `PEER-ATTEST`, `EPOCH-TRANSITION`, `STATE_ROOT`, `BACKPRESSURE`.
