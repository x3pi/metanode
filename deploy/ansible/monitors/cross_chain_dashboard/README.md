# ⚓ Cross-Chain Dashboard Quickstart

Giao diện Web giám sát thời gian thực chu trình liên chuỗi Root Anchor (P7).

---

## 1. Lệnh Bật & Tắt Dashboard

Vào thư mục:
```bash
cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible/monitors/cross_chain_dashboard
```

* **▶️ Bật Dashboard:**
  ```bash
  go run main.go --port 8088
  ```
* **⏹️ Tắt Dashboard:**
  ```bash
  pkill -f "cross_chain_dashboard"
  ```

---

## 2. Đường Dẫn Truy Cập Web

Mở trình duyệt trên máy Laptop:
👉 **[http://192.168.1.233:8088](http://192.168.1.233:8088)** *(hoặc `http://localhost:8088`)*
