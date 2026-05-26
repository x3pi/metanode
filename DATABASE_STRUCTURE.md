# Kiến trúc lưu trữ Database (Database Directory Structure)

Hệ thống MetaNode lưu trữ dữ liệu tại `RootPath` được khai báo trong file `config.json`. Dựa trên vai trò của node trong mạng (Validator, RPC, Explorer), cấu trúc thư mục yêu cầu sẽ khác nhau.

---

## 1. Node Đồng Thuận (Validator / Consensus Node)
**Mục đích:** Chỉ tham gia xác thực giao dịch, biểu quyết block, không có nhu cầu tra cứu lịch sử xa (có thể bật `Pruning.Mode = "full"` để tiết kiệm ổ cứng).

Các thư mục **bắt buộc (tối thiểu)** để node có thể chạy và đồng thuận:
Nằm trong thư mục `consensus/`:
- `account_state/`: Lưu trữ State của các tài khoản (số dư, nonce).
- `trie_database/`: Lưu trữ Merkle Trie State Root (hoặc dùng NOMT cache nếu cấu hình `state_backend="nomt"`).
- `smart_contract_code/`: Lưu trữ Bytecode của Smart Contracts.
- `smart_contract_storage/`: Lưu trữ bộ nhớ State (storage) của các Smart Contracts.
- `stake_db/`: Trạng thái Staking và Validator.
- `backup_db/`: Buffer tạm thời dùng cho quá trình đồng bộ State từ các Node khác.
- `xapian/`: (Tùy chọn) Index full-text search hỗ trợ cho Smart Contract nếu có.
- Các thư mục khác: `wallets/`, `mapping/`, `backup_device_key_storage/`.

> [!NOTE]
> Node Đồng Thuận có thể cắt tỉa (prune) các dữ liệu lịch sử cũ. Thư mục `history/` chỉ cần giữ vài epoch gần nhất.

---

## 2. Node RPC (RPC / Archive Node)
**Mục đích:** Cho phép Client tra cứu lại **toàn bộ lịch sử** giao dịch từ Genesis block (cần bật `is_rpc_node: true` và `Pruning.Mode = "archive"`).

**Bao gồm toàn bộ thư mục của Node Đồng Thuận**, VÀ BỔ SUNG THÊM thư mục `history/`:
- `history/blocks/`: Dữ liệu gốc (Header + Body) của toàn bộ các Block đã được tạo.
- `history/receipts/`: Lưu trữ Transaction Receipts (event logs, gas used, execution status).
- `history/transaction_block_number/`: Index ánh xạ: `TxHash -> BlockNumber` (Bắt buộc để API `eth_getTransactionByHash` biết giao dịch nằm ở block nào).
- `history/blocks_hash/`: Ánh xạ `BlockNumber -> BlockHash`.
- `history/block_hash_to_number/`: Ánh xạ `BlockHash -> BlockNumber`.
- `history/txs_eth/`: Lưu trữ Raw Transaction bytes (Hex).
- `history/transaction_state/`: Trạng thái chi tiết của Transaction.

> [!IMPORTANT]
> Tiến trình `RPCHistorySync` sẽ liên tục chạy ngầm trên RPC Node để tải và "bù đắp" (backfill) các block/receipt còn thiếu vào thư mục `history/` nếu node được phục hồi từ Snapshot.

---

## 3. Node Explorer (Explorer Node)
**Mục đích:** Xử lý và đánh chỉ mục sâu (Deep Indexing) phục vụ hiển thị lên Web Explorer (cần cấu hình `is_explorer: true`).

**Bao gồm toàn bộ thư mục của Node RPC**, VÀ BỔ SUNG THÊM cấu hình cơ sở dữ liệu riêng cho Explorer, được định nghĩa qua `config.json`:
- `explorer_db_path`: Thư mục lưu trữ CSDL riêng của Explorer (thường lưu trữ Account Movements, Token Transfers, Event Index,... đã được bóc tách).
- `explorer_read_only_db_path`: Thư mục read-only được API Web truy cập nhằm phân tải khỏi tiến trình Explorer Worker đang ghi.

> [!TIP]
> Do tính chất I/O rất lớn, thư mục `explorer_db_path` nên được đặt trên các ổ cứng NVMe tốc độ cao và tách biệt với CSDL Consensus.
