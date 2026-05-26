# Kiến trúc luồng đồng bộ Lịch sử cho Explorer Node (Explorer History Sync)

## 1. Vấn đề (The Problem)

Trong kiến trúc của Metanode, khi một node (ví dụ: Node 4) được cấu hình là Explorer (`IsExplorer: true`), nó cần lưu trữ toàn bộ lịch sử (bao gồm Transactions và Receipts).
Tuy nhiên, để một node bắt kịp nhanh chóng với mạng lưới, Metanode sử dụng cơ chế **Snapshot Sync (Fast Sync)** hoặc **Hybrid Sync**. Cơ chế này chỉ tải trạng thái (state trie root) mới nhất từ một Master Node để giúp node hoạt động đồng thuận ngay lập tức.
Hệ quả là: Mặc dù node đã đồng bộ và tham gia vào mạng lưới, cơ sở dữ liệu PebbleDB nội bộ của nó (`TransactionStateDB`, `ReceiptDB`) bị rỗng đối với các block trong quá khứ do chưa từng thực thi. Khi các Client truy vấn dữ liệu cũ qua RPC `eth_getTransactionReceipt` hoặc `eth_getTransactionByHash`, hệ thống sẽ trả về `null`.

## 2. Luồng Kiến trúc Đồng bộ (History Healing Flow)

Để giải quyết vấn đề trên mà không cản trở quá trình đồng thuận, một luồng đồng bộ ngầm (background sync) đã được thiết kế riêng biệt trong file `explorer_history_sync.go`.

### 2.1. Khởi chạy và Quét (Boot & Scan)
- Tiến trình này là một Goroutine độc lập, chạy song song với các service nền khác của hệ thống.
- Tiến trình sẽ chờ cho đến khi quá trình đồng bộ chính (CatchUp) hoàn tất (`bp.IsSyncCompleted() == true`).
- Nó dùng một vòng lặp và một `ticker` (30 giây) để quét tịnh tiến theo thời gian các block từ 1 đến `currentTip` để kiểm tra độ trọn vẹn dữ liệu.

### 2.2. Nhận diện Dữ liệu Khuyết (Missing Data Detection)
Với mỗi `blockNum`:
1. Node gọi nội bộ `GetBlockByNumber` để lấy metadata của block đó.
2. Kiểm tra danh sách Transactions. Nếu block rỗng (0 txs), bỏ qua.
3. Node truy cập thẳng vào PebbleDB thông qua `receipt.NewReceiptsFromRoot(...)`.
4. Lấy hash của transaction đầu tiên trong block và thử `GetReceipt(txHash)`. Nếu hàm này trả về lỗi (chứng tỏ dữ liệu Receipt đã mất ở máy local), node sẽ đánh dấu block này bị thiếu lịch sử.

### 2.3. Sửa chữa Dữ liệu (Healing Injection)
Khi phát hiện thiếu dữ liệu:
1. Node Explorer sẽ gọi `bp.node.GetBlockStorageBatch(blockNum, blockNum)` để giao tiếp qua giao thức TCP với một Master node khác trong mạng lưới, nhằm lấy khối dữ liệu `BackupDb` thô (raw bytes) của block bị thiếu.
2. Sau khi nhận được `BackupDb`, thay vì chạy lại block qua máy ảo EVM (quá chậm và rủi ro fork), Explorer node sẽ trích xuất 2 mảng byte dữ liệu đã được commit sẵn ở Master:
   - `TxBatchPut`: Dữ liệu byte-level của Transactions.
   - `ReceiptBatchPut`: Dữ liệu byte-level của Receipts.
3. Node thực hiện **Direct DB Injection** (bơm trực tiếp xuống đĩa):
   - Ghi đè `TxBatchPut` xuống `bp.storageManager.GetStorageTransaction().BatchPut(txBatch)`
   - Ghi đè `ReceiptBatchPut` xuống `bp.storageManager.GetStorageReceipt().BatchPut(rcpBatch)`
4. Quá trình này giúp tái thiết lập dữ liệu lịch sử cực kỳ nhanh với chi phí CPU thấp, chủ yếu tốn thời gian I/O và network.

## 3. Tính An Toàn và Bất Biến (Safety & Invariants)

- **Không Fork State (Zero-Fork Invariant):** Việc đồng bộ history này chỉ thao tác trên bộ lưu trữ lịch sử (`StorageTransaction` và `StorageReceipt`). Nó tuyệt đối KHÔNG can thiệp vào `AccountStateDB` (số dư, nonce) hay Trie State. Do đó, logic Consensus hay trạng thái Account State không hề bị ảnh hưởng.
- **Tránh Quá Tải:** Tiến trình được đặt `time.Sleep` 10ms sau mỗi block để nhường CPU cho các module cốt lõi khác, đảm bảo node không bị đứng hoặc bị loại khỏi mạng lưới do giật lag khi đang quét hàng ngàn block lịch sử.
- **Eventual Consistency:** Ngay cả khi node Explorer bị tắt hoặc ngắt kết nối với Master giữa chừng, lần bật tiếp theo nó sẽ tự tiếp tục quét từ điểm bị lỗi. Nhờ các thao tác Ghi-Đè (Idempotent), quá trình này đảm bảo an toàn dù lặp lại.

## 4. Tổng Kết Dòng Chảy

1. **Explorer Node** khởi động ➔ Fast Sync / Snapshot Restore ➔ Bắt đầu tham gia mạng lưới.
2. Khởi chạy **Background Sync Worker**.
3. Quét tịnh tiến các block cũ trong DB Local ➔ **Phát hiện Dữ liệu Rỗng**.
4. Truy vấn Network TCP ➔ Lấy `BackupDb` từ **Master Node**.
5. Trích xuất mảng Byte ➔ Ghi đè trực tiếp xuống PebbleDB (Khôi phục Lịch sử).
6. Khách hàng/API gọi `eth_getTransactionReceipt` ➔ Truy vấn DB nội bộ thành công.
