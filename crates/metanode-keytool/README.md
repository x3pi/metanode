# Metanode Keytool — CLI Hướng dẫn sử dụng

`metanode-keytool` là công cụ dùng để sinh và kiểm tra các loại khóa (cryptographic keys) cần thiết cho validator của Metanode, bao gồm:
1. **BLS12-381** (Authority key / Consensus key)
2. **Ed25519** (Protocol key & Network key)
3. **secp256k1 / ECDSA** (Ethereum Key / Ví nhận thưởng / ETH Address)

Bạn có thể chạy công cụ này dưới 2 dạng:
* **Tích hợp (Khuyên dùng):** chạy thông qua subcommand `metanode keytool ...`
* **Độc lập:** chạy thông qua binary `metanode-keytool ...`

---

## 🛠️ 1. Hướng dẫn Build

Để build và tạo ra file thực thi:

### Cách A: Build binary `metanode` chính (chứa subcommand `keytool`)
```bash
# Build binary metanode chính
cargo build --release -p metanode
# File thực thi sẽ nằm ở: ./target/release/metanode
```

### Cách B: Build binary độc lập `metanode-keytool`
```bash
# Chỉ build riêng tool keytool
cargo build --release -p metanode-keytool
# File thực thi sẽ nằm ở: ./target/release/metanode-keytool
```

---

## 🚀 2. Cách Sử Dụng (Subcommands)

> 💡 *Dưới đây sử dụng cú pháp tích hợp `./target/release/metanode keytool`. Nếu dùng binary độc lập, bạn chỉ cần thay bằng `./target/release/metanode-keytool`.*

### 2.1 Sinh Đầy Đủ Khóa cho Validator (`generate validator`)
Tạo tất cả 4 loại key cần thiết cùng lúc và lưu vào thư mục chỉ định:
```bash
./target/release/metanode keytool generate validator --out-dir ./my_validator_keys
```
**Output sinh ra trong thư mục gồm 5 file:**
* `authority_key.json` : Khóa BLS12-381 (private key dạng hex, public key dạng base64).
* `protocol_key.json` : Khóa Ed25519 cho protocol.
* `network_key.json`  : Khóa Ed25519 cho network.
* `eth_key.json`      : Khóa secp256k1 (chứa `ETH_PRIVATE_KEY` và `ETH_ADDRESS` dạng `0x...`).
* `keys_summary.json` : File tóm tắt tất cả public keys để tiện cấu hình genesis.

*Tất cả file chứa private key sẽ tự động được set quyền bảo mật `chmod 600` (chỉ owner đọc/ghi).*

### 2.2 Chỉ Sinh Khóa Ethereum (`generate eth`)
Nếu chỉ cần sinh khóa Ethereum (để lấy địa chỉ ETH và private key):
```bash
./target/release/metanode keytool generate eth --out-dir ./eth_key_dir
```
*Lệnh này sẽ in ra địa chỉ ETH vừa tạo và lưu file `eth_key.json`.*

### 2.3 Chỉ Sinh Khóa BLS/Protocol/Network
```bash
./target/release/metanode keytool generate bls --out-dir ./keys
./target/release/metanode keytool generate protocol --out-dir ./keys
./target/release/metanode keytool generate network --out-dir ./keys
```

### 2.4 Xem Thông Tin Khóa Từ File Đã Có (`show`)
Để kiểm tra thông tin public key mà không làm lộ private key:
```bash
./target/release/metanode keytool show ./my_validator_keys/authority_key.json
# Hoặc kiểm tra file ETH key:
./target/release/metanode keytool show ./my_validator_keys/eth_key.json
```

---

## 📋 3. Định Dạng File JSON

### `eth_key.json`
Định dạng tương thích hoàn toàn với các script cấu hình tự động:
```json
{
  "ETH_PRIVATE_KEY": "0x1234567890abcdef...",
  "ETH_ADDRESS": "0x9876543210fedcba..."
}
```

### `keys_summary.json`
Chứa toàn bộ thông tin public keys dùng để khai báo validator mới trong genesis entry:
```json
{
  "authority_key": "base64_encoded_bls_pubkey...",
  "protocol_key": "base64_encoded_ed25519_pubkey...",
  "network_key": "base64_encoded_ed25519_pubkey...",
  "eth_address": "0xabc..."
}
```
