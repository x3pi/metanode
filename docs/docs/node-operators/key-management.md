---
sidebar_position: 4
title: Quản lý Keys
---

# Quản lý Keys Validator

---

## Tổng quan 4 loại key

| Key | Loại | File | Trong Genesis? | Secret? |
|-----|------|------|---------------|---------|
| `protocol_key` | BLS private | `node_0_protocol_key.json` | Public key ✅ | 🔴 Bí mật |
| `network_key` | Ed25519 private | `node_0_network_key.json` | Public key ✅ | 🔴 Bí mật |
| `authority_key` | BLS private | `node_0_authority_key.json` | Public key ✅ | 🔴 Bí mật |
| `private_key` | Ethereum hex | Trong config JSON | ❌ | 🔴 Bí mật |

---

## Generate Keys

```bash
cd metanode/consensus/metanode

# Generate 1 bộ key (cho 1 validator)
./target/release/metanode generate -n 1 -o ./my_keys
```

Output:
```
my_keys/
├── node_0_protocol_key.json   ← BLS consensus key (private)
├── node_0_network_key.json    ← Ed25519 P2P key (private)
├── node_0_authority_key.json  ← BLS authority key (private)
├── node_0.toml                ← Config template
└── committee.json             ← Public keys để đăng ký genesis
```

## Generate cho nhiều validators cùng lúc (genesis ceremony)

```bash
# Tạo 4 bộ key (4 validators)
./target/release/metanode generate -n 4 -o ./genesis_keys
```

---

## Backup & Bảo mật

```bash
# Backup keys ra ngoài server
BACKUP=~/metanode_keys_$(date +%Y%m%d_%H%M%S)
mkdir -p "$BACKUP" && chmod 700 "$BACKUP"
cp -r ./my_keys/*.json "$BACKUP/"
chmod 600 "$BACKUP/"*.json

# Verify backup
ls -la "$BACKUP/"
```

**Quy tắc bảo mật:**
- ✅ Lưu backup ở **ổ cứng ngoài** hoặc **USB offline**
- ✅ `chmod 600` cho tất cả file key
- ❌ Không commit vào git (đã có trong `.gitignore`)
- ❌ Không gửi qua email, Slack, Discord
- ❌ Không dùng chung key cho nhiều node

---

## Key Rotation (Thay Keys)

:::warning
Thay key validator yêu cầu **phối hợp với team** và cập nhật genesis/committee. Không tự ý thay keys khi network đang chạy.
:::

Quy trình:
1. Generate bộ key mới
2. Thông báo cho team committee key mới
3. Chờ epoch transition
4. Cập nhật config trỏ sang key file mới
5. Restart node

---

## Đăng ký Ethereum Account

`private_key` và `address` trong config là **Ethereum account** tiêu chuẩn. Tạo bằng:

```bash
# Dùng cast (từ foundry)
cast wallet new

# Output:
# Successfully created new keypair.
# Address:     0xYOUR_ADDRESS
# Private key: 0xYOUR_PRIVATE_KEY
```

Hoặc dùng MetaMask / bất kỳ Ethereum wallet nào để export private key.

:::note
`address` trong `node_config.json` bỏ prefix `0x` (chỉ hex thuần).
:::
