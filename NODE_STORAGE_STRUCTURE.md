# Cấu trúc lưu trữ dữ liệu của MetaNode (Database & Snapshot)

Tài liệu này mô tả chi tiết các thư mục cơ sở dữ liệu (`data/data/`) được sử dụng bởi một node MetaNode. Việc chuẩn hóa và quản lý các thư mục này rất quan trọng để đảm bảo quá trình đồng thuận, snapshot, và phục hồi (recovery) hoạt động chính xác.

---

## 1. Node Đồng Thuận Tối Thiểu (Validator Node)

Một node chỉ tham gia đồng thuận (không cần tra cứu lịch sử hay phục vụ DApp) chỉ cần lưu trữ **TRẠNG THÁI HIỆN TẠI** (State) và **DỮ LIỆU ĐỒNG THUẬN** (DAG).
Các thư mục tối thiểu cần thiết bao gồm:

- `account_state/`: Trạng thái số dư, nonce của các tài khoản (PebbleDB).
- `blocks/`: Chứa header của các block để xác minh chuỗi (không nhất thiết phải chứa toàn bộ giao dịch, nhưng cần để liên kết block hash).
- `stake_db/`: Trạng thái staking và validator để tính toán bầu cử (PebbleDB).
- `trie_database/`: Cấu trúc MPT (Merkle Patricia Trie) lưu trữ state root để đảm bảo tính toàn vẹn (PebbleDB/Nomt).
- `smart_contract_code/`: Bytecode của các smart contract đã được deploy (PebbleDB).
- `smart_contract_storage/`: Trạng thái biến của các smart contract (PebbleDB).
- `consensus/rust_consensus/`: Dữ liệu nội bộ của Rust (DAG, v.v.).

*Lưu ý:* Các thư mục này là **bắt buộc** phải có trong một bản Snapshot để node có thể khôi phục lại trạng thái và tiếp tục tham gia đồng thuận (Voting) ngay lập tức.

---

## 2. Node Hỗ Trợ RPC (RPC Node)

Nếu node được cấu hình để phục vụ các yêu cầu RPC (ví dụ: `eth_getTransactionReceipt`, `eth_getBlockByNumber`), node này cần lưu trữ **THÊM** lịch sử các giao dịch và biên lai (receipts).

Các thư mục cần có thêm:
- `receipts/`: Lưu trữ log, gas used, và trạng thái thành công của các giao dịch. *(Rất quan trọng cho DApp)*
- `transaction_state/`: Trạng thái cụ thể của từng giao dịch (đã index).
- `history/mapping/`: Các index mapping để tra cứu nhanh từ Hash -> Block hoặc Hash -> Transaction. 

**Quy trình đồng bộ (History Sync):**
Khi node RPC phục hồi từ một Snapshot (thường chỉ chứa trạng thái hiện tại), một tiến trình nền (`rpc_history_sync.go`) sẽ tự động quét và tải về các `receipts` và `history` còn thiếu từ các block cũ. Tiến trình này sử dụng biến `last_checked_receipt_block` được lưu ngay trong DB `receipts` để biết nên bắt đầu đồng bộ từ đâu, đảm bảo không bỏ sót dữ liệu.

---

## 3. Node Trình Duyệt (Explorer Node)

Nếu node được sử dụng làm backend cho Blockchain Explorer (ví dụ: tìm kiếm full-text, theo dõi token chuyển nhượng nội bộ, v.v.), node cần **TẤT CẢ** các dữ liệu của RPC Node và **THÊM** cơ sở dữ liệu tìm kiếm.

Các thư mục cần có thêm:
- `xapian_node/`: Cơ sở dữ liệu Xapian để hỗ trợ tìm kiếm full-text (ví dụ: tìm kiếm địa chỉ, nội dung contract, tên token).
- `other/`: Các metadata hoặc dữ liệu thống kê khác phục vụ biểu đồ cho Explorer.

---

## Tóm tắt phân bổ thư mục Snapshot

Khi hệ thống tạo Snapshot (`snapshot_manager.go`), nó sẽ bao gồm các thư mục theo mức độ cấu hình:

| Thư mục | Loại Node Cần Thiết | Ý nghĩa |
|---|---|---|
| `account_state/` | **Tất cả (Consensus)** | Số dư và thông tin tài khoản cơ bản. |
| `stake_db/` | **Tất cả (Consensus)** | Dữ liệu Validator và Staking. |
| `trie_database/` | **Tất cả (Consensus)** | State root merkle trie (Xác minh trạng thái). |
| `blocks/` | **Tất cả (Consensus)** | Block Headers. |
| `smart_contract_code/` | **Tất cả (Consensus)** | Bytecode Smart Contract. |
| `smart_contract_storage/`| **Tất cả (Consensus)** | Dữ liệu lưu trữ biến của Smart Contract. |
| `consensus/rust_consensus/` | **Tất cả (Consensus)** | Dữ liệu DAG của Rust Mysticeti. |
| `receipts/` | **RPC / Explorer** | Biên lai giao dịch (gas, logs). |
| `transaction_state/` | **RPC / Explorer** | Dữ liệu chi tiết của từng giao dịch. |
| `history/mapping/` | **RPC / Explorer** | Lập chỉ mục tra cứu giao dịch và block. |
| `xapian_node/` | **Explorer** | Dữ liệu tìm kiếm full-text cho Explorer. |

## Thư mục rác (Đã dọn dẹp)
- Các thư mục cũ như `changelog_db_stake`, `changelog_db_*` là các thư mục tạo ra trong quá trình migrate hoặc debug cũ và **KHÔNG CÒN CẦN THIẾT**. Chúng đã được loại bỏ để giảm tải bộ nhớ và đơn giản hóa cây thư mục lưu trữ.
- Các file cấu hình `metanode-suite` nếu không sử dụng cũng có thể được loại bỏ hoặc không cần snapshot.
