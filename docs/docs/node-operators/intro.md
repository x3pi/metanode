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

## 🗺️ Hướng dẫn theo loại node

* **[⚡ Chạy Validator Node](./validator-setup)** — Build, generate keys, đăng ký genesis, cấu hình, chạy với systemd
* **[🔄 Chạy Sync-Only Node](./synconly-setup)** — Full node / RPC node, không cần keys, dùng cho explorer/dApp
* **[🔑 Quản lý Keys](./key-management)** — Generate, backup, bảo mật, rotation

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
