---
sidebar_position: 5
title: 🚀 Hướng dẫn Triển khai Cụm Node
---

# 🚀 Hướng dẫn Triển khai Cụm Node

Metanode hỗ trợ hệ thống triển khai tự động hóa thông qua các script shell. Bạn chỉ cần cấu hình một file duy nhất `deploy.env`, hệ thống sẽ tự động build, đẩy binary qua SSH, tự động cập nhật IP và khởi động cụm node.

---

## 📐 Mô hình Triển khai Cụm

Sơ đồ phân bổ tiến trình chạy trên 2 máy chủ mẫu:

```
┌─────────────────────┐    ┌─────────────────────┐
│  Server B           │    │  Server C           │
│  192.168.1.231      │    │  192.168.1.232      │
│                     │    │                     │
│  Node 0 (Leader)    │    │  Node 2             │
│  Node 1             │    │  Node 3             │
└─────────────────────┘    └─────────────────────┘
```

Mỗi node đại diện cho 3 tiến trình chạy độc lập trong các session `tmux`: `go-master-N` + `go-sub-N` + `metanode-N`.

---

## ⚙️ Cấu hình Tự động — `deploy.env`

**Đây là file cấu hình duy nhất cần điều chỉnh.** Tất cả các địa chỉ IP trong tệp cấu hình của các node sẽ tự động đồng bộ theo mapping `NODE_SERVER`.

```bash
# Cấu hình kết nối SSH
SSH_USER="abc"
SSH_AUTH="password"          # Chọn "key" hoặc "password"
SSH_PASSWORD="1234@abcd"

# Địa chỉ IP của các Server đích
SERVER_B="192.168.1.231"
SERVER_C="192.168.1.232"

# Node → Server mapping (Chỉ định Node chạy trên Server nào)
NODE_SERVER[0]="$SERVER_B"   # Node 0 chạy trên Server B
NODE_SERVER[1]="$SERVER_B"   # Node 1 chạy trên Server B
NODE_SERVER[2]="$SERVER_C"   # Node 2 chạy trên Server C
NODE_SERVER[3]="$SERVER_C"   # Node 3 chạy trên Server C
```

---

## 🛠️ Sử dụng Lệnh Triển khai

### Triển khai Toàn bộ (Full Deploy)
```bash
./deploy_cluster.sh --all
```

Các bước script sẽ tự động thực hiện:
1. **Kiểm tra kết nối SSH** tới toàn bộ danh sách server.
2. **Biên dịch cục bộ (Build local)** mã nguồn Rust & Go ra binary.
3. **Dừng cụm node cũ (Stop old cluster)** đang chạy trên remote server.
4. **Đẩy tài nguyên (Push)** binary và config files sang các server đích.
5. **Cập nhật IP tự động (Update IPs)** dựa theo phân vùng map của `deploy.env`.
6. **Làm sạch database cũ (Clean data)** và **Khởi chạy cụm node mới (Start nodes)**.

### Các tùy chọn lệnh linh hoạt khác

* **Chỉ build binary:**
  ```bash
  ./deploy_cluster.sh --build
  ```
* **Chỉ đẩy binary và cập nhật file cấu hình IP:**
  ```bash
  ./deploy_cluster.sh --push --ips
  ```
* **Chỉ khởi động cụm node:**
  ```bash
  ./deploy_cluster.sh --start
  ```
* **Dừng toàn bộ cụm node:**
  ```bash
  ./deploy_stop.sh
  ```
* **Kiểm tra trạng thái sức khỏe cụm node:**
  ```bash
  ./deploy_status.sh
  ```

---

## 🔍 Kiểm tra & Xử lý sự cố (Troubleshooting)

### Kiểm tra Trạng thái Node
```bash
./deploy_status.sh
```
Lệnh này sẽ kiểm tra trạng thái hoạt động của các session `tmux`, gRPC API health, chiều cao khối (block height), đồng bộ hóa consensus, và log tail của từng node.

### Xem log trực tiếp từ Remote Server
```bash
# Xem log của Go Master Node 0
ssh abc@192.168.1.231 "tail -50 logs/node_0/go-master-stdout.log"

# Xem log của Rust Consensus Node 0
ssh abc@192.168.1.231 "tail -50 logs/node_0/rust.log"
```

### Kết nối vào TMUX session để Debug trực quan
```bash
ssh abc@192.168.1.231 "tmux attach -t go-master-0"
```
