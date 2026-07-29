---
sidebar_position: 1
title: Tổng quan Vận hành Node
---

# Vận hành Node Metanode (Node Operators)

Trang tài liệu này hướng dẫn chi tiết dành cho các **Node Operators** (Nhà vận hành nút) cách triển khai, cấu hình, vận hành và giám sát cụm node (cluster) của hệ thống Metanode Blockchain.

---

## 🏗️ Kiến trúc Cụm Node (Node Cluster)

Một node hoàn chỉnh của Metanode chạy dưới dạng **systemd service** và bao gồm 2 tiến trình độc lập giao tiếp chéo qua Unix Domain Socket (UDS) và TCP:

1. **`metanode-execution-N` (Go)**: Tầng thực thi (Execution Layer), xử lý và ghi nhận trạng thái giao dịch (EVM-compatible).
2. **`metanode-consensus-N` (Rust)**: Tầng đồng thuận (Consensus Layer), chạy động cơ đồng thuận BFT/DAG lõi và điều phối thứ tự block.
3. **`metanode-rpc-N` (Go - Tùy chọn)**: RPC Proxy Client mở cổng JSON-RPC/WSS công khai cho MetaMask và dApp (kết nối trực tiếp vào Execution qua TCP).

---

## 🗺️ Hướng dẫn theo loại node

* **[⚡ Chạy Validator Node](./validator-setup)** — Hướng dẫn cài đặt, cấu hình và chạy một validator node độc lập.
* **[🌐 Triển khai Private Chain](./private-chain-setup)** — Hướng dẫn khởi tạo và triển khai Private Chain (1 validator hoặc multi-validator).
* **[📐 So sánh Kiến trúc & Lưu trữ Tệp](./private-chain-architecture)** — Đánh giá ưu nhược điểm các mô hình triển khai & giải pháp Upload/Download File.
* **[🔄 Chạy Sync-Only Node](./synconly-setup)** — Full node đồng bộ dữ liệu mạng để làm RPC công khai hoặc phục vụ Explorer.
* **[🚀 Triển khai Cụm Node Local](./deployment-guide)** — Hướng dẫn sử dụng bộ công cụ điều phối `systemd-cluster.sh` và `install-rpc-systemd.sh` trên cùng một máy chủ.
* **[🔑 Quản lý Keys](./key-management)** — Sinh khóa, sao lưu, bảo mật và phân quyền các file keys.

---

## 🔒 Port Mapping mặc định (Cụm Local 5 Nodes)

Khi chạy thử nghiệm nhiều node trên cùng một máy chủ vật lý, các cổng mạng được cấu hình tự động dựa trên chỉ số của node (`N` từ `0` đến `4`) để tránh xung đột cổng:

| Dịch vụ | Node 0 | Node 1 | Node 2 | Node 3 | Node 4 (Sync-only) | Công thức / File `.env` |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Execution RPC** | `10746` | `10747` | `10748` | `10749` | `10750` | `RPC_PORT` (`10746 + N`) |
| **RPC Proxy (MetaMask)** | `8545` | `8546` | `8547` | `8548` | `8549` | `SERVER_PORT` (`8545 + N`) |
| **Execution P2P** | `6200` | `6201` | `6202` | `6203` | `6204` | `P2P_PORT` (`6200 + N`) |
| **Rust Consensus P2P** | `9000` | `9001` | `9002` | `9003` | `9004` | `CONSENSUS_PORT` (`9000 + N`) |
| **Snapshot Server** | `8600` | `8601` | `8602` | `8603` | `8604` | `SNAPSHOT_SERVER_PORT` (`8600 + N`) |
| **Consensus Metrics** | `9100` | `9101` | `9102` | `9103` | `9104` | `METRICS_PORT` (`9100 + N`) |

