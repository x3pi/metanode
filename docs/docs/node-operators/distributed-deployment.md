---
sidebar_position: 6
title: 🌐 Triển khai Phân tán (Multi-Server)
---

# 🌐 Triển khai Phân tán (Multi-Server)

Khi triển khai các validator node của Metanode trên nhiều máy chủ vật lý khác nhau qua môi trường Internet hoặc mạng LAN, cấu hình kết nối mạng cần tuân thủ nguyên lý giao tiếp tối ưu của hệ thống.

---

## 📐 Nguyên lý Giao tiếp Mạng Cốt lõi

* **Giao tiếp nội bộ (Local IPC giữa Rust và Go trên cùng 1 máy):** Sử dụng **Unix Domain Socket** để đạt tốc độ truyền tải bộ nhớ nhanh nhất và giảm độ trễ tối đa.
* **Đồng thuận P2P & Phát hiện Peer (Giữa các máy chủ vật lý khác nhau):** Sử dụng kết nối mạng **TCP Socket** bảo mật và tin cậy.

---

## 🗺️ Cấu hình Tô-pô ví dụ (3 Nodes trên 3 máy)

* **Machine 1 (192.168.1.100):** Đảm nhận chạy Node 0
* **Machine 2 (192.168.1.101):** Đảm nhận chạy Node 1
* **Machine 3 (192.168.1.102):** Đảm nhận chạy Node 2

---

## ⚙️ Chi tiết file cấu hình của từng Node (`node_N.toml`)

### 1. Cấu hình Machine 1 - Node 0
```toml
node_id = 0
network_address = "192.168.1.100:9000"

# Giao tiếp local Rust ↔ Go qua Unix Domain Sockets (nội bộ máy)

# Khám phá Peer: Kết nối TCP tới Go Master của các Node khác
peer_go_master_sockets = [
    "tcp://192.168.1.101:19201",  # Nhắm tới Node 1 Go Master
    "tcp://192.168.1.102:19202",  # Nhắm tới Node 2 Go Master
]
peer_rpc_port = 19200  # Cổng TCP Go Master của Node này dùng để lắng nghe peer truy vấn
```

### 2. Cấu hình Machine 2 - Node 1
```toml
node_id = 1
network_address = "192.168.1.101:9001"

# Giao tiếp local

# Khám phá Peer
peer_go_master_sockets = [
    "tcp://192.168.1.100:19200",  # Nhắm tới Node 0
    "tcp://192.168.1.102:19202",  # Nhắm tới Node 2
]
peer_rpc_port = 19201
```

### 3. Cấu hình Machine 3 - Node 2
```toml
node_id = 2
network_address = "192.168.1.102:9002"

# Giao tiếp local

# Khám phá Peer
peer_go_master_sockets = [
    "tcp://192.168.1.100:19200",  # Nhắm tới Node 0
    "tcp://192.168.1.101:19201",  # Nhắm tới Node 1
]
peer_rpc_port = 19202
```

---

## 💻 Cấu hình Lắng nghe TCP trên Go Master

Để hỗ trợ truy vấn chéo P2P giữa các máy chủ từ xa, lớp Go Master cần được mở thêm một cổng lắng nghe TCP ngoài Unix socket local.

Cập nhật trong file khởi chạy `cmd/simple_chain/main.go`:

```go
// Lắng nghe nội bộ từ Rust node local (Unix socket):
requestSockPath := "/tmp/rust-go-master.sock"

// Lắng nghe truy vấn từ các remote node qua mạng (TCP socket):
peerListenerSockPath := "tcp://0.0.0.0:19200"  // Trùng khớp với cấu hình peer_rpc_port của node
```

> [!NOTE]
> Phía Go Master sẽ duy trì đồng thời **2 listener**: Một listener Unix socket nhận chỉ thị từ Rust Consensus cục bộ, và một listener TCP socket tiếp nhận yêu cầu đồng bộ hóa hoặc attest từ các máy chủ đồng thuận từ xa.

---

## 🛡️ Cấu hình Tường lửa (Firewall Settings)

Để đảm bảo các gói tin giao tiếp đồng thuận không bị chặn bởi tường lửa hệ điều hành, hãy thực thi các lệnh mở cổng trên mỗi máy chủ:

```bash
# Mở cổng TCP cho dịch vụ Peer Discovery (19200, 19201, 19202)
sudo ufw allow from 192.168.1.0/24 to any port 19200:19202 proto tcp

# Mở cổng TCP cho dịch vụ Đồng thuận P2P (9000, 9001, 9002)
sudo ufw allow from 192.168.1.0/24 to any port 9000:9002 proto tcp
```
