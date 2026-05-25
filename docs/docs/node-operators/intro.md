---
sidebar_position: 1
title: Tổng quan Vận hành Node
---

# Vận hành Node Metanode (Node Operators)

Trang tài liệu này hướng dẫn chi tiết dành cho các **Node Operators** (Nhà vận hành nút) cách triển khai, cấu hình, vận hành và giám sát cụm node (cluster) của hệ thống Metanode Blockchain.

---

## 🏗️ Kiến trúc Cụm Node (Node Cluster)

Một validator node hoàn chỉnh của Metanode bao gồm 3 tiến trình hoạt động đồng bộ và giao tiếp chéo qua Unix Domain Socket (UDS) và TCP:

1. **`go-master-N`**: Tiến trình xử lý chính (Execution engine) ở Go.
2. **`go-sub-N`**: Tiến trình xử lý phụ hỗ trợ giao dịch song song ở Go.
3. **`metanode-N`**: Tiến trình đồng thuận BFT/DAG lõi viết bằng Rust.

---

## 🗺️ Các tài liệu vận hành cốt lõi

Hãy đọc các hướng dẫn chi tiết dưới đây để triển khai node của bạn:

* **[🚀 Hướng dẫn Triển khai Cụm Node](./deployment-guide)**: Cách cấu hình file `deploy.env` và sử dụng script tự động hóa để biên dịch, đẩy binary và khởi chạy cụm node 4 validator.
* **[🌐 Triển khai Phân tán (Multi-Server)](./distributed-deployment)**: Cách thiết lập liên kết mạng giữa các node chạy trên các máy chủ vật lý khác nhau sử dụng Unix Domain Socket cho local IPC và TCP Socket cho P2P cross-machine peer discovery.

---

## 🔒 Port Mapping mặc định

Khi chạy cụm node thử nghiệm (ví dụ 4 nodes), các cổng mạng cố định được ánh xạ như sau để tránh xung đột:

| Dịch vụ | Node 0 | Node 1 | Node 2 | Node 3 |
| :--- | :--- | :--- | :--- | :--- |
| **Go Master RPC** | `8757` | `10747` | `10749` | `10750` |
| **Go Sub RPC** | `8646` | `10646` | `10650` | `10651` |
| **Go Master Connect**| `4201` | `6201` | `6211` | `6221` |
| **Rust P2P Consensus**| `9000` | `9001` | `9002` | `9003` |
| **Rust Peer RPC** | `19000` | `19001` | `19002` | `19003` |
| **Rust Metrics** | `9100` | `9101` | `9102` | `9103` |
