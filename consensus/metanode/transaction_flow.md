# Tài Liệu: Luồng Xử Lý Giao Dịch & Quy Trình Tạo Block (Meta-Node)

Tài liệu này mô tả chi tiết vòng đời của một giao dịch (Transaction) từ lúc được Client gửi lên cho đến khi được đóng gói thành một Block, lưu trữ vào Database và trả kết quả về cho người dùng. 

Quy trình này trải dài qua cả hai thành phần: **Go Execution Engine** (xử lý state, smart contract) và **Rust Consensus Engine** (mtn-consensus).

---

## 1. Tiếp nhận Giao Dịch (Transaction Injection)
**Mô tả:** Client (hoặc Backend/Dapp) kết nối và gửi giao dịch mới tới tiến trình Go nguyên khối (Monolithic Go). Giao dịch được tiếp nhận qua TCP/Websocket socket và đẩy vào một hàng đợi (pool) nội bộ.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/connection_processor.go`: Quản lý các kết nối network từ Client. Giải mã gói tin byte (Packet) và định tuyến lệnh tới Transaction Processor.
- `cmd/simple_chain/processor/transaction_processor.go`: Nhận request giao dịch cơ bản, đẩy vào channel nội bộ `injectionQueue`. Hàm `startInjectionWorkers()` tạo ra các goroutine chuyên làm nhiệm vụ xử lý hàng đợi này một cách bất đồng bộ.

## 2. Xác thực và đưa vào Pool (Validation & Pooling)
**Mô tả:** Giao dịch thô được bóc tách, đưa qua bộ lọc xác thực để kiểm tra mức độ hợp lệ (chữ ký số, nonce, định dạng, và số dư từ ví cơ bản) trước khi chấp nhận vào Pool.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/transaction_processor_pool.go`: Nơi chứa hàm `AddTransactionToPool()`. Hàm này sẽ gọi hàm xác thực. Nếu hợp lệ, giao dịch sẽ được đưa vào cấu trúc dữ liệu `TransactionPool` và đồng thời lưu vết vào `PendingTransactionManager` với trạng thái `StatusInPool`.
- `pkg/blockchain/tx_processor/validation.go` (hoặc `validation_transaction.go`): Nơi thực thi logic `VerifyTransaction`, xác minh Public Key, thuật toán chữ ký điện tử và điều kiện chi tiêu tối thiểu của account.

## 3. Chuyển giao dịch sang Tầng Đồng Thuận (Forward to Rust Consensus)
**Mô tả:** Ở mô hình kiến trúc này, tiến trình Go không tự quyết định thứ tự (ordering). Các giao dịch hợp lệ nằm trong Pool của Go sẽ được gom lại thành từng mẻ (batch) và đẩy thẳng vào lõi Rust thông qua các lời gọi FFI (CGo) trong cùng không gian tiến trình. Khác biệt với máy ảo đời đầu, IPC qua Unix Domain Sockets (UDS) đã bị loại bỏ để tối đa hóa hiệu suất.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/block_processor_txs.go` (Go): Có hạ tầng `TxsProcessor2` chạy một vòng lặp liên tục (`Run()`), chuyên lấy hàng loạt giao dịch từ Transaction Pool, đóng gói và gọi native CGo function trực tiếp vào thư viện Rust.
- `metanode/src/network/tx_receiver.rs` (Rust): Hàm FFI phía Rust tiếp nhận con trỏ byte batch giao dịch từ Go. Sau đó Rust sẽ decode mẻ giao dịch, đưa trực tiếp vào hàng đợi chờ chốt (`pending_transactions_queue`).

## 4. Giai đoạn Đồng Thuận (mtn-consensus Consensus)
**Mô tả:** Động cơ Rust Consensus sẽ lấy các giao dịch trong khay chờ, vận hành thuật toán mtn-consensus (DAG-based BFT) để thỏa hiệp thứ tự với các Validator khác trên mạng lưới thành chuỗi block tuyến tính.

**Các file chịu trách nhiệm chính:**
- Thư mục `metanode/src/node/consensus/`: Chứa mã nguồn thuật toán mtn-consensus để gom batch và chốt đồng thuận.
- `metanode/src/node/transition/...`: Sau khi Validator Node đã chốt block, Rust sẽ thực hiện một Callback FFI ngược về Listener của quá trình Go nguyên khối, kích hoạt sự kiện yêu cầu **Thực thi** lô giao dịch này (vì lõi đồng thuận tĩnh không giữ State Trie).

## 5. Thực thi Giao dịch và State (Execution Phase)
**Mô tả:** Tiến trình Go nhận lệnh từ Rust mang theo danh sách Giao dịch đã có tem thời gian đồng thuận cứng (Block Timestamp). Go tiến hành phân tích sự phụ thuộc (dependency tree) để nhóm các giao dịch song song, sau đó chạy thực thi State/Smart contract.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/transaction_processor_pool.go`: Hàm `ProcessTransactions()` nhận danh sách từ Rust.
- `pkg/grouptxns/grouptxns.go`: Sử dụng thuật toán đồ thị cơ bản (Union-Find) duyệt qua các thuộc tính `RelatedAddresses` của các tx. Các giao dịch **chạm vào cùng 1 địa chỉ** (dễ suy đột State) sẽ bị gom vào chung 1 nhóm và chạy tuần tự. Các giao dịch không đụng độ với nhau sẽ được chẻ thành hàng ngàn nhóm riêng để **chạy song song phần cứng mức độ cao**.
- `pkg/blockchain/tx_processor/tx_processor.go`: Chứa hàm cốt lõi `processGroupsConcurrently()`. Nó khởi tạo các Worker phân đều các nhóm giao dịch, qua đó tạo môi trường Context và đẩy logic vào thực thi VM / Native Smart Contract (`vm_processor.go`). Tiến trình này sẽ nhả ra mảng kết quả `Receipts` và `EventLogs` ứng với mỗi tx.

## 6. Tính toán State Hash & Tạo Block (Block Generation)
**Mô tả:** Sau khi toàn bộ các Worker hoàn tất việc tính toán State, Master Node sẽ chốt hạ các Merkle Trie để cấp phát StateRootHash và xây dựng lên dữ liệu cấu trúc vật lý của Block.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/block_processor_processing.go`: Tùy theo kiến trúc giới hạn, hàm `GenerateBlock()` tích lũy các kết quả `ProcessResult` và khởi tạo đối tượng block hoàn chỉnh (`createBlockFromResults()`) có chứa Body là các Transaction và Header chứa tổng hòa State Merkle Root Hash (`AccountStateDB.IntermediateRoot()`).

## 7. Lưu trữ và Commit (Commitment & Persistence)
**Mô tả:** Đổ dữ liệu RAM Trie và cấu trúc block cứng xuống Database vật lý định kì sau mỗi một Block được chạy.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/block_processor_commit.go`: Cấu trúc vòng lặp `commitWorker()` tiếp nhận Block đã tạo.
  - Phase 1: Lưu metadata Block cứng vào DB (`block_database.go`).
  - Phase 2: Xử lý các tác vụ dọn dẹp và chuẩn bị phát sóng (broadcast) cho P2P network (SyncOnly nodes). 
- `pkg/account_state_db/account_state_db.go`: Thực hiện lệnh `Commit()`. Nó sẽ chép map tạm `dirtyAccounts` xuống Trie cấu trúc lưu trữ MPT (Merkle-Patricia Trie) thật và xả batch xuống LevelDB (Cập nhật số dư cuối cùng).
- `pkg/transaction_state_db/transaction_state_db.go`: Đóng gói `ReceiptBatch` và `TxBatch` xuống DB cục bộ với logic bảo vệ lock trie đa cấp.

## 8. Phân phối và Trả lại biên lai (Broadcast & Receipt Callback)
**Mô tả:** Báo hiệu hoàn tất cho các node thụ động trên mạng (SyncOnly Nodes) bằng cơ chế P2P, đồng thời trả lời trực tiếp lệnh Callback cho Socket của Client đã kết nối ở bước 1.

**Các file chịu trách nhiệm chính:**
- `cmd/simple_chain/processor/block_processor_commit.go`: Node kích hoạt `broadcastBlockToNetwork()`, đưa block mới lan truyền tới layer mạng P2P (thường xử lý qua anemo).
- `cmd/simple_chain/processor/connection_processor.go` / Event Bus: Khi quy trình execute cục bộ hoàn thành, đối tượng receipt liền được trích xuất. Hệ thống chủ động tra `connectionsManager` (Network WebSocket) tìm đúng connection ID của user và trả thẳng về "Thực thi thành công / Thất bại". (Bỏ qua hoàn toàn quy trình đồng bộ Master-Sub phức tạp).
