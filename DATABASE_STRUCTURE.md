# Cấu Trúc Lưu Trữ Dữ Liệu (Node Storage & Database Structure)

Tài liệu này mô tả chi tiết cấu trúc thư mục lưu trữ trên đĩa (`data_dir`, ví dụ `/opt/metanode/node-X/data/` hoặc `execution/cmd/simple_chain/sample/nodeX/`) cho từng loại Node trong mạng lưới MetaNode (**Validator**, **SyncOnly / RPC**, và **Explorer**).

---

## 1. Sơ Đồ Thư Mục Tổng Quan

```text
data/
├── block_db/                # [PebbleDB] Block headers, block bodies, hash ↔ number index
├── transaction_db/          # [PebbleDB] Raw transaction data, txHash → blockNumber/index
├── receipt_db/              # [PebbleDB] Transaction receipts, gas used, return data & logs
├── account_db/              # [PebbleDB] Account metadata, balance, nonce, storage root
├── account_db_nomt/         # [Rust NOMT] Authenticated Binary Merkle Trie cho Account State
├── stake_db/                # [PebbleDB] Validator stake, delegation & committee metadata
├── stake_db_nomt/           # [Rust NOMT] Authenticated Binary Merkle Trie cho Stake State
├── code_db/                 # [PebbleDB] EVM Smart Contract bytecode (codeHash → bytecode)
├── smart_contract_db/       # [PebbleDB] EVM Contract storage key-value & metadata
├── xapian_db/               # [C++ Xapian] Off-chain full-text search DB cho Xapian precompiles
├── explorer_search_db/      # [C++ Xapian] Full-text index cho Block Explorer (chỉ bật trên Explorer Node)
└── snapshot_data/           # [Snapshot] Dữ liệu phục vụ State/Commit Sync cho các node mới
```

*(Lưu ý: Trên các node chạy đồng thuận BFT, tầng Rust Consensus còn lưu trữ DAG per-epoch bằng RocksDB tại thư mục `storage/node_X/epoch_Y/` theo cấu hình `storage_path` trong `node_X.toml`).*

---

## 2. Phân Biệt Theo Vai Trò Node (Validator vs RPC vs Explorer)

Tất cả các node (`Validator`, `SyncOnly` / `RPC`, `Explorer`) đều dùng chung một binary `simple_chain`. Sự khác biệt về các thư mục database trên đĩa phụ thuộc vào cờ cấu hình trong `config.json`:

| Thư mục / Thành phần | Validator Node (`consensus_Validator`) | RPC / SyncOnly Node (`SyncOnly`) | Explorer Node (`EnableExplorerSearch=true`) | Công nghệ & Mục đích |
| :--- | :---: | :---: | :---: | :--- |
| **`block_db/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB:** Lưu trữ Block Header, danh sách Tx Hashes và chỉ mục chiều cao khối. |
| **`transaction_db/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB:** Lưu trữ chi tiết giao dịch gốc phục vụ `eth_getTransactionByHash`. |
| **`receipt_db/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB:** Lưu trữ biên lai thực thi (`Receipt`), trạng thái thành công/lỗi và `EventLogs`. |
| **`account_db/`** + **`account_db_nomt/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB + Rust NOMT (`libmtn_nomt`):** Lưu trữ trạng thái tài khoản và cây Merkle xác thực (`stateRoot`, `eth_getProof`). |
| **`stake_db/`** + **`stake_db_nomt/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB + Rust NOMT (`libmtn_nomt`):** Lưu trữ trạng thái cổ phần Validator, ủy quyền (Delegation) và danh sách Ủy ban (Committee). |
| **`code_db/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB:** Lưu trữ mã máy (Bytecode) của các hợp đồng thông minh đã triển khai. |
| **`smart_contract_db/`** | ✅ Có | ✅ Có | ✅ Có | **PebbleDB:** Lưu trữ biến trạng thái (Storage Slots) của hợp đồng thông minh EVM/MVM. |
| **`xapian_db/`** | ✅ Có | ✅ Có | ✅ Có | **C++ Xapian:** Cơ sở dữ liệu tìm kiếm toàn văn cho các hợp đồng thông minh sử dụng Xapian precompile. |
| **`snapshot_data/`** | ✅ Có | ✅ Có | ✅ Có | Lưu trữ các bản chụp trạng thái (State Snapshot) giúp node mới đồng bộ nhanh. |
| **`explorer_search_db/`** | ❌ Không | ❌ Không *(trừ khi bật cờ)* | ✅ **Có** | **C++ Xapian (`ExplorerSearchService`):** Đánh chỉ mục toàn văn cho Block, Transaction, Address và Event Logs phục vụ giao diện Explorer. |

---

## 3. Các Tối Ưu Hóa Lưu Trữ Quan Trọng

1. **Tích hợp NOMT (`account_db_nomt` & `stake_db_nomt`):**
   - Thay thế cơ chế Merkle Patricia Trie (MPT) truyền thống bằng **NOMT (Nearly Optimal Merkle Trie)** viết bằng Rust và giao tiếp qua CGo FFI (`pkg/nomt_ffi`).
   - Hỗ trợ tính toán `StateRoot` song song tốc độ cao và sinh bằng chứng Merkle (`eth_getProof`) trực tiếp từ trạng thái mới nhất.
2. **Loại bỏ hoàn toàn `changelog_db_*` (Đã dọn dẹp):**
   - Trong kiến trúc cũ, mỗi lần commit trạng thái phải ghi thêm lịch sử thay đổi vào `changelog_db_account/` và `changelog_db_stake/`.
   - Với kiến trúc NOMT hiện tại, cây trạng thái xác thực được quản lý trực tiếp trong `*_nomt/`. Các thư mục `changelog_db_*` đã được loại bỏ hoàn toàn khỏi mã nguồn để giảm tới **50% chi phí Disk I/O** trong quá trình commit block.
3. **Tối ưu hóa Đánh chỉ mục Explorer (`explorer_search_db/`):**
   - Trên các node bật `EnableExplorerSearch` / `ExplorerFullData`, `ExplorerSearchService` sử dụng lời gọi CGo gộp đơn (`database_index_document`), truyền dữ liệu block trực tiếp qua bộ nhớ (`pendingIndexBlocks`) và gom mẻ `Commit()` mỗi 64 block để giảm thiểu chi phí cấp phát bộ nhớ và tải CPU.
   - Trên các node **Validator** thuần túy, `EnableExplorerSearch` và `ExplorerFullData` luôn được tắt (`false`) để dành toàn bộ tài nguyên CPU/Disk I/O cho đồng thuận BFT và thực thi Block-STM.
