---
sidebar_position: 4
title: Quản lý Keys
---

# Quản lý Keys Validator

---

## Tổng quan các loại key

Khi khởi chạy một Validator Node, bạn cần 4 loại khóa chính để ký các loại giao thức/tin nhắn khác nhau:

| Key | Loại | File | Trong Genesis? | Secret? | Mô tả |
|-----|------|------|---------------|---------|-------|
| `authority_key` | BLS12-381 private | `authority_key.json` | Public key ✅ | 🔴 Bí mật | Dùng trong Go execution layer để biểu quyết ủy quyền/ký đồng thuận block |
| `protocol_key` | Ed25519 private | `protocol_key.json` | Public key ✅ | 🔴 Bí mật | Dùng trong Rust consensus layer để ký biểu quyết Mysticeti/chứng thực |
| `network_key` | Ed25519 private | `network_key.json` | Public key ✅ | 🔴 Bí mật | Dùng cho P2P network (Anemo/TLS) định danh kết nối giữa các node |
| `eth_key` | secp256k1 private | `eth_key.json` | Address ✅ | 🔴 Bí mật | Chứa ETH Address nhận thưởng và private key đăng ký validator |

---

## Generate Keys

Công cụ `keytool` được tích hợp sẵn trực tiếp vào binary `metanode`.

```bash
# Build metanode CLI (nếu chưa build)
cargo build --release -p metanode

# Sinh trọn bộ validator keys (4 keys) vào thư mục chỉ định
./target/release/metanode keytool generate validator --out-dir ./my_keys
```

Output trong thư mục `./my_keys/`:
```
my_keys/
├── authority_key.json    ← BLS authority key (private & public)
├── protocol_key.json     ← Ed25519 consensus protocol key (private & public)
├── network_key.json      ← Ed25519 P2P network key (private & public)
├── eth_key.json          ← secp256k1 ETH key (private & address)
└── keys_summary.json     ← Tổng hợp public keys để cấu hình Genesis
```

---

## Generate lẻ từng loại key (Key Rotation)

Nếu bạn cần thay thế hoặc cập nhật (rotate) riêng một loại key cụ thể mà không muốn tạo lại tất cả:

```bash
# Chỉ sinh riêng BLS authority key
./target/release/metanode keytool generate bls --out-dir ./my_keys

# Chỉ sinh riêng Protocol key
./target/release/metanode keytool generate protocol --out-dir ./my_keys

# Chỉ sinh riêng Network key
./target/release/metanode keytool generate network --out-dir ./my_keys

# Chỉ sinh riêng ETH key
./target/release/metanode keytool generate eth --out-dir ./my_keys
```

---

## Xem thông tin Public Key từ File

Bạn có thể đọc nhanh thông tin khóa công khai hoặc địa chỉ từ một file key bí mật bằng lệnh `show`:

```bash
./target/release/metanode keytool show ./my_keys/authority_key.json
./target/release/metanode keytool show ./my_keys/eth_key.json
```

---

## Backup & Bảo mật

```bash
# Backup keys ra ngoài server an toàn
BACKUP=~/metanode_keys_$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP" && chmod 700 "$BACKUP"
cp ./my_keys/*.json "$BACKUP/"
chmod 600 "$BACKUP/"*.json

# Verify backup
ls -la "$BACKUP/"
```

**Quy tắc bảo mật:**
- ✅ Lưu backup ở **ổ cứng ngoài** hoặc **USB offline**
- ✅ Chạy `chmod 600` cho tất cả file key
- ❌ Tuyệt đối không commit file `.json` lên GitHub (đã được cấu hình trong `.gitignore`)
- ❌ Không gửi file key qua các kênh chat công cộng (Slack, Telegram, Discord, Email)
- ❌ Không chạy chung một bộ key trên nhiều node đồng thời (dẫn đến lỗi đồng thuận và bị slash)

---

## Đăng ký Ethereum Account

`eth_key.json` sinh ra từ `keytool` chứa một tài khoản Ethereum tiêu chuẩn. Nếu bạn muốn sử dụng tài khoản có sẵn tự tạo bằng công cụ ngoài (ví dụ: `cast` từ Foundry):

```bash
# Dùng cast tạo ví mới
cast wallet new

# Output:
# Successfully created new keypair.
# Address:     0xYOUR_ADDRESS
# Private key: 0xYOUR_PRIVATE_KEY
```

Hoặc dùng MetaMask để xuất private key, sau đó tạo file `eth_key.json` thủ công theo định dạng sau:
```json
{
  "ETH_PRIVATE_KEY": "0xYOUR_PRIVATE_KEY",
  "ETH_ADDRESS": "0xYOUR_ADDRESS"
}
```

:::note
Địa chỉ ETH được lưu ở dạng chữ thường và chứa tiền tố `0x`.
:::
