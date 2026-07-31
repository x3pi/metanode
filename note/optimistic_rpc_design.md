# Tài Liệu Thiết Kế: Hệ Thống Optimistic RPC (Giao Dịch Tức Thời)

Hệ thống Optimistic RPC (hay Speculative Gateway) là một kiến trúc được thiết kế đặc biệt cho **Metanode Private Chain** nhằm giải quyết bài toán độ trễ (latency) của công nghệ Blockchain truyền thống. 

Mục tiêu chính là cung cấp **trải nghiệm người dùng (UX) thời gian thực**, tương tự như Web2: Người dùng gửi giao dịch và nhận kết quả thành công/thất bại **ngay lập tức**, trong khi giao dịch vẫn đang được xử lý ngầm (asynchronous) để ghi nhận vào chuỗi khối (blockchain) thực tế.

---

## 1. ⚙️ Ý Tưởng Hoạt Động Cốt Lõi (Core Concept)

Thay vì bắt Client (Ví MetaMask, dApp) phải chờ đợi qua quá trình: 
`Gửi TX -> Chờ đợi Đồng Thuận (Consensus) -> Đóng Block -> Có Receipt` (Thường mất từ vài giây đến hàng chục giây).

Hệ thống cắt luồng này thành **2 kênh độc lập (Tách biệt Send và Receive stream):**

1. **Fast-path (Kênh Tức Thời):** Thực thi ảo (Virtual Execution) giao dịch ngay trên node RPC, sinh ra một biên lai giả lập (Mock Receipt) và báo kết quả về cho Client trong vòng vài mili-giây.
2. **Slow-path (Kênh Ký Quỹ):** Đưa giao dịch vào hàng đợi bất đồng bộ (Async Queue) để chạy ngầm qua quy trình đồng thuận BFT/DAG và ghi cố định (finalized) vào khối (block).

---

## 2. 🏛️ Kiến Trúc Và Luồng Xử Lý Chi Tiết (Workflow)

Quá trình bắt đầu khi người dùng gửi một `eth_sendRawTransaction` qua API RPC.

### Bước 1: Tiếp Nhận & Xác Thực Nhanh (Fast Validation)
- Node RPC nhận gói tin Raw Transaction.
- Decode và xác thực chữ ký số cơ bản (ECDSA/BLS), kiểm tra số dư sơ bộ (dựa trên trạng thái `stateRoot` mới nhất hiện có).

### Bước 2: Thực Thi Đầu Cơ Off-chain (Speculative Execution)
- Giao dịch được đẩy vào `TxVirtualExecutor`. Bộ máy này chạy một môi trường giả lập (không thay đổi database thật) để mô phỏng chính xác kết quả của giao dịch (bao gồm cả chạy Smart Contract).
- Nếu giao dịch thất bại (Hết gas, lỗi logic contract): Trả về lỗi ngay lập tức.
- Nếu thành công: Tính toán lượng `GasUsed` và sinh ra trạng thái thành công.

### Bước 3: Đúc Biên Lai Giả Lập (Mock Receipt)
- Hệ thống tạo ra một `RpcReceipt` đặc biệt lưu vào bộ nhớ đệm nhanh (Memory Cache - `SpeculativeReceiptCache`).
- Biên lai này có đầy đủ `gasUsed`, `status`, nhưng `blockNumber` và `blockHash` sẽ được đánh dấu là chưa hoàn thành (`0x0` hoặc Pending).
- RPC trả về mã Hash của giao dịch cho người dùng.

### Bước 4: Chuyển Đổi Sang Xử Lý Đồng Bộ (Cập Nhật Kiến Trúc)
> [!NOTE]
> **Quyết định thiết kế:** Trước đây, hệ thống sử dụng một hàng đợi bất đồng bộ (`TxAsyncQueue`) để xử lý các giao dịch ở background. Tuy nhiên, hàng đợi này đã được gỡ bỏ hoàn toàn.
> **Lý do:** Hệ thống cần trả về chính xác lỗi thực thi (execution errors) trực tiếp cho Client ngay trên kết nối RPC hiện tại (ví dụ: lỗi HTTP/WebSocket gốc). Nếu sử dụng Async Queue, kết nối gốc đã bị đóng trả về sớm, khiến việc báo lỗi ngược lại cho Client trở nên bất khả thi. Do đó, giao dịch hiện tại được đẩy trực tiếp (synchronous) vào bộ xử lý để có thể ném lỗi (throw error) ngay lập tức về cho người dùng.

- Đồng thời với Bước 3, bản gốc của giao dịch được đưa thẳng vào bộ xử lý thực tế (`ProcessTransactionFromRpc`) thay vì qua hàng đợi trung gian.

### Bước 5: Chốt Giao Dịch Dưới Nền (Real Consensus)
- Các Background Workers gửi giao dịch sang mạng lưới BFT Consensus (viết bằng Rust).
- Mạng lưới đạt đồng thuận, đóng block, cập nhật State thực và ghi Receipt thực vào LevelDB/RocksDB.

### Bước 6: Ảo Giác Finality (Khi Client hỏi Receipt)
- Gần như ngay lập tức sau Bước 3, ví MetaMask (hoặc dApp) sẽ gọi `eth_getTransactionReceipt` để kiểm tra.
- Server RPC can thiệp vào lệnh gọi này: 
  - Nếu Block chưa đóng, DB chưa có -> Nó vào Memory Cache lấy **Mock Receipt** trả về cho Client. Client sẽ hiển thị "Giao dịch thành công" ngay lập tức trên màn hình.
  - Vài giây sau, khi Block thực tế đã đóng, DB đã có -> Trả về Receipt thực tế với đầy đủ BlockNumber và BlockHash.

---

## 3. 🛡️ Xử Lý Ngoại Lệ (Edge Cases & Fallbacks)

- **Từ chối trong Virtual Execution nhưng thành công ở thực tế?** Gần như không xảy ra vì Virtual Execution chạy trên máy trạng thái y hệt.
- **Thành công trong Virtual Execution nhưng thất bại khi chạy thật?** Có thể xảy ra nếu nhiều giao dịch thao tác trên cùng một biến state trong cùng một millisecond (Race condition). Khi đó State thật bị revert, nhưng UI đã báo thành công. (Chấp nhận sự đánh đổi này trong Private Chain / GameFi để đổi lấy UX).
- **Tràn hàng đợi (Queue Full):** Nếu hàng đợi bị quá tải, RPC sẽ kích hoạt cơ chế Backpressure, chủ động trả về lỗi "System overloaded" ngay ở Bước 1.
- **Dọn dẹp Memory Cache:** Các Mock Receipt chỉ tồn tại trong vòng 5 phút (Result TTL). Sau đó `resultCleanupLoop` sẽ tự động dọn rác để tránh tràn RAM.

---

## 4. 📈 Lợi Ích & Ứng Dụng (Use Cases)

- **UX Đột Phá:** Phù hợp cho các ứng dụng GameFi, SocialFi, thanh toán siêu nhỏ (micro-transactions) nơi người dùng không muốn chờ 5-10 giây cho mỗi cú click.
- **Giảm Tải Chờ Đồng Bộ:** Tách rời I/O của HTTP Request khỏi I/O của Consensus Engine, giúp Node nhận tải (TPS) cao hơn trong các đợt tăng vọt (Spike).
