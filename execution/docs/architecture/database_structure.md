# Cấu Trúc Database Của Metanode

Tài liệu này mô tả chi tiết về cấu trúc thư mục của các loại cơ sở dữ liệu (Database) được sử dụng trong Metanode. Kể từ phiên bản mới, cấu hình Database đã được đơn giản hóa: người dùng chỉ cần khai báo đường dẫn gốc (`RootPath`) trong file `config.json`. Tất cả các database con đều tự động được sinh ra dưới `RootPath` theo các hằng số quy định trước.

## 1. Kiến Trúc Chung

Cấu hình trong `config.json` chỉ yêu cầu một trường hợp nhất:

```json
"Databases": {
    "RootPath": "./sample/node0/data/data",
    "NodeType": "STORAGE_LOCAL"
}
```

Tất cả các database sẽ được mount tự động vào các thư mục con bên trong `RootPath`.

## 2. Cấu Trúc Thư Mục Các Thư Mục Con

Dưới đây là bảng tổng hợp tất cả các thư mục con lưu trữ trong hệ thống và mục đích của chúng:

| Tên Hằng Số (Go) | Thư Mục Tương Đối | Ý Nghĩa / Mục Đích Sử Dụng | Loại Database (Backend) |
| :--- | :--- | :--- | :--- |
| `PathAccountState` | `/account_state/` | Lưu trữ số dư, nonce và các thông tin cơ bản của tài khoản người dùng. | NOMT / ShardelDB |
| `PathTrie` | `/trie_database/` | Hệ thống Trie Storage (State Trie) phục vụ cho truy xuất và tính toán State Root. | ShardelDB / NOMT |
| `PathSmartContractCode` | `/smart_contract_code/` | Chứa mã byte-code tĩnh của các smart contract đã deploy. | ShardelDB |
| `PathSmartContractStorage` | `/smart_contract_storage/` | Lưu trữ state map động (các biến state) của các smart contract. | ShardelDB |
| `PathBlocks` | `/blocks/` | Lưu trữ dữ liệu block hoàn chỉnh sau khi có consensus, phục vụ đồng bộ mạng. | ShardelDB |
| `PathReceipts` | `/receipts/` | Lưu lại kết quả, event logs của các transactions sau khi thực thi thành công. | ShardelDB |
| `PathTxsEth` | `/txs_eth/` | Log các giao dịch liên thông Ethereum (Cross-chain transactions). | ShardelDB |
| `PathBlocksHash` | `/blocks_hash/` | Lưu trữ mapping nhanh từ Block Number sang Block Hash. | ShardelDB |
| `PathBackupDeviceKey` | `/backup_device_key_storage/` | Lưu trữ khóa mã hóa thiết bị dự phòng cho bảo mật node. | ShardelDB |
| `PathTransactionBlockNumber` | `/transaction_block_number/` | Lập chỉ mục giao dịch để truy xuất giao dịch nằm ở block nào. | ShardelDB |
| `PathTransactionState` | `/transaction_state/` | Tình trạng tức thời của các pending transactions trong Pool. | ShardelDB |
| `PathBlockHashToNumber` | `/block_hash_to_number/` | Tra cứu hai chiều giữa mã Hash của Block và cao độ (Block Height). | ShardelDB |
| `PathWallets` | `/wallets/` | Chứa thông tin ví của người vận hành node (Node Operator). | ShardelDB |
| `PathMapping` | `/mapping/` | CSDL mapping cục bộ cho phục vụ các hệ thống con phụ trợ của Metanode. | ShardelDB |
| `PathBackup` | `/backup_db/` | Thư mục lưu trữ các snapshot/backup state trước epoch transitions. | ShardelDB |
| `PathStake` | `/stake_db/` | Quản lý danh sách Validators, số dư Stake và cấu trúc biểu quyết. | NOMT / ShardelDB |
| `PathXapian` | `/xapian/` | Lưu trữ chỉ mục tìm kiếm văn bản toàn văn phục vụ trình khám phá Explorer. | Xapian C++ |
| `nomt_db` (Fixed) | `/nomt_db/` | Thư mục gốc cho CSDL NOMT phân bổ nếu hệ thống kích hoạt `state_backend="nomt"`. | NOMT |
| `rust_consensus` (Rust)| `/rust_consensus/` | **(Thuộc tầng Consensus)** CSDL của lõi Rust Mysticeti (DAG, Certificates, Block proposals). Được FFI Bridge tự động override cấu hình của `node_X.toml` để gom chung vào `RootPath`. | RocksDB |

## 3. Quy Ước Hoạt Động (Shared DB vs Fragmented DB)

Metanode hiện đang hoạt động 100% bằng cơ sở dữ liệu cục bộ (Local Database), không còn hỗ trợ chế độ Client-Server (Remote DB). Tùy thuộc vào loại node (Exec Node hay Simple Chain), cách lưu trữ sẽ khác nhau:

1. **Unified SharedDB (Exec Node - Khuyên Dùng)**: 
   Sử dụng chung một hệ thống cơ sở dữ liệu duy nhất tại `/chaindata` (sử dụng PebbleDB hoặc LevelDB). Việc chia không gian cho các hệ thống con sẽ do backend tự động dàn trải dựa vào column families hoặc sub-level paths.

2. **Fragmented DB (Simple Chain)**: 
   Sử dụng nhiều instance database độc lập tách rời. Hàm `initStorageDatabases()` sẽ duyệt qua từng hằng số ở trên, gắn chuỗi vào `RootPath` (Ví dụ: `filepath.Join(RootPath, "/account_state/")`), và tạo các instance DB hoàn toàn riêng biệt trên ổ đĩa.
