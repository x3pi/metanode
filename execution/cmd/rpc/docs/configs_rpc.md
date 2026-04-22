# Hướng dẫn chạy ứng dụng (How to Run App)

## 1.1. Cấu hình RPC
file `config-client-tcp.json` 

```json
{
  "version": "0.0.1.0",
  "private_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "parent_connection_address": "139.59.243.85:4200",
  "parent_address": "0x7d03201fee4675987894617138e5ee7e038a6b39",
  "chain_id": 991,
  "parent_connection_type:": "client",
  "owner_file_storage_address":"0x7d03201fee4675987894617138e5ee7e038a6b39",
  "pk_admin_file_storage":"87d931eaa2f76709f2615586e0d560ca9b80f247c9cc431e197ba3e7167db623",
  "bls_admin_storage":"2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "extra_account": 1000000000000000000,
  "disable_free_gas": true
}
```
### Giải thích các trường quan trọng:
- **`parent_connection_address`**: Ip đến chain .
- **`extra_account`**: Số tiền gửi thêm khi user k đủ tiền giao dịch trên chain .
- **`disable_free_gas`**: user tự trả phí nếu k đủ gas.

file `config-rpc.json` tại `cmd/rpc-client` (hoặc cập nhật cấu hình tương ứng) với nội dung sau:

```json
{
  "rpc_server_url": "http://139.59.243.85:8646",
  "wss_server_url": "ws://139.59.243.85:8646/ws",
  "private_key": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "server_port": ":8545",
  "chain_id": 991,
  "https_port": ":8666",
  "cert_file": "certificate.pem",
  "key_file": "private.key",
  "master_password": "your_strong_master_password_here",
  "app_pepper": "your_unique_secret_pepper_here",
  "ldb_bls_wallet_path": "./db/bls_wallets",
  "ldb_bls_account_noti": "./db/bls_account_noti",
  "ldb_contract_free_gas": "./db/ldb_contract_free_gas",
  "ldb_artifact_registry": "./db/ldb_artifact_registry",
  "ldb_robot_transaction": "./db/ldb_robot_transaction",
  "owner_rpc_address": "0x0b143e894a600114c4a3729874214e5fc5ea9cbc",
  "contracts_interceptor": ["0x00000000000000000000000000000000D844bb55","0xE74A88071fdc26f6b0453fE2B8b1d3e805b314E5"],
  "reward_amount": 1000000000000000000
}
```
### Giải thích các trường quan trọng:
- **`rpc_server_url / wss_server_url`**: Đường dẫn đến chain. 
- **`private_key`**: private_key của bls.
- **`server_port`**: port của rpc.
- **`ldb_bls_wallet_path`**: Đường dẫn đến database lưu trữ ví BLS.
- **`ldb_bls_account_noti`**: Đường dẫn đến database lưu thông tin notification của các tài khoản BLS.
- **`owner_rpc_address`**: Địa chỉ ví của chủ sở hữu (Admin) để ký các giao dịch quản lý tài khoản, xác nhận đăng ký bls, gửi phần thưởng.
- **`contracts_interceptor`**: Danh sách contract chặn lại để xử lý event (phần tử index 0 là địa chỉ của contract address quản lý tài khoản, phần tử index 1 là địa chỉ của contract address quản lý robot).
- **`reward_amount`**: Phần thưởng khi xác nhận tài khoản đăng ký bls, sẻ gửi cho người đăng ký phần thương 1 số tiền.
