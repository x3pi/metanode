# 🛠️ Metanode Deployment & Key Generator

Script `gen_validator_entry.py` tự động hóa việc tạo toàn bộ keys (BLS + ETH) và sinh file cấu hình `.env` cho node trong 1 lệnh duy nhất.

---

## 📋 Yêu cầu
* **Python 3**
* **Go** và **Rust/Cargo** (Đã build binary `metanode` trước bằng lệnh: `cd consensus/metanode && cargo build --release --bin metanode`)

---

## 🚀 Lệnh sử dụng nhanh

### 1. Dành cho Validator Node
Tạo keys, file cấu hình `.env` và file JSON để đăng ký Genesis:
```bash
python3 gen_validator_entry.py \
  --hostname node-0 \
  --node-type validator \
  --ip 192.168.1.234 \
  --node-id 0
```
* **Kết quả:** 
  - Thư mục `./node-0_keys/` chứa:
    - Các file private keys.
    - File cấu hình `validator.env`.
    - File `node-0_genesis.json` chứa thông tin đăng ký validator gửi cho genesis coordinator.

### 2. Dành cho Sync-Only Node
Tạo ETH key và file cấu hình `.env`:
```bash
python3 gen_validator_entry.py \
  --hostname node-sync-1 \
  --node-type synconly \
  --ip 192.168.1.101
```
* **Kết quả:** Thư mục `./node-sync-1_keys/` chứa file cấu hình `synconly.env`.

---

## 🔄 Các bước tiếp theo

1. **Cấu hình bổ sung `.env`**:
   Mở file `.env` vừa sinh ra (trong thư mục `*_keys/`) để điền:
   * `GENESIS_FILE`: Đường dẫn tuyệt đối tới file `genesis.json` chính thức.
   * `PEER_RPC_ADDRESSES`: IP:port của các node validator khác trong mạng.
     *(Ví dụ: `PEER_RPC_ADDRESSES='"192.168.1.102:19202", "192.168.1.103:19203"'`)*

2. **Chạy cài đặt**:
   ```bash
   sudo bash install.sh --config ./node-1_keys/validator.env
   ```

---

## 📊 Quản lý dịch vụ & Xem Logs

### 1. Lệnh quản lý nhanh
```bash
# Khởi động dịch vụ
sudo systemctl start metanode-execution metanode-consensus

# Dừng dịch vụ
sudo systemctl stop metanode-execution metanode-consensus

# Khởi động lại dịch vụ
sudo systemctl restart metanode-execution metanode-consensus

# Kiểm tra trạng thái hoạt động
sudo systemctl status metanode-execution metanode-consensus
```

> [!TIP]
> Nếu dịch vụ bị crash liên tục (ví dụ do cấu hình sai), systemd sẽ tự động khóa dịch vụ lại (báo lỗi `Start request repeated too quickly`).
> Để gỡ bỏ khóa và cho phép dịch vụ khởi động lại, chạy lệnh:
> ```bash
> sudo systemctl reset-failed metanode-execution
> ```

### 2. Cách xem logs
Có hai cách để theo dõi logs hoạt động của hệ thống:

* **Cách 1: Xem trực tiếp từ file logs** (Go execution layer tự động ghi vào thư mục cài đặt):
  ```bash
  # Theo dõi log chính của ứng dụng Go
  tail -f /opt/metanode/logs/execution/go-master/App.log
  ```

* **Cách 2: Xem qua systemd journal** (Toàn bộ log stdout/stderr bao gồm cả Go và Rust):
  ```bash
  # Theo dõi log của Go execution layer
  journalctl -u metanode-execution -f

  # Theo dõi log của Rust consensus layer
  journalctl -u metanode-consensus -f
  ```

---

## ⚠️ Bảo mật
* **TUYỆT ĐỐI KHÔNG** commit các thư mục `*_keys/` hoặc file `.env` chứa private key lên Git.
* Thư mục `deploy/` đã được cấu hình tự động bỏ qua (ignore) các file nhạy cảm này trong `.gitignore`.

