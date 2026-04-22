# Báo cáo: Đánh giá Giải pháp Lưu trữ NOMT & Các Cải tiến Tương lai

Tài liệu này đánh giá khách quan về công nghệ **NOMT (Nearly O(1) Merkle Trie)** đang được sử dụng làm State Backend cốt lõi cho MetaNode hiện tại, đồng thời đưa ra các phương pháp tiếp cận kiến trúc lưu trữ hiện đại hơn từ các Layer 1 hàng đầu (Monad, Sei v2, Aptos).

---

## 1. Đánh giá NOMT hiện hành trong MetaNode

**NOMT** là một công cụ lưu trữ hiệu suất cực cao được viết bằng Rust, tối ưu hóa triệt để để giải quyết nhược điểm cây Merkle Patricia Trie (MPT) nhiều lớp truyền thống.

### Điểm mạnh (Pros)
- **Tốc độ Truy xuất Cực độ**: Thay vì phải đi qua OS Page Cache, NOMT sử dụng `O_DIRECT` và `io_uring` để đẩy dữ liệu trực tiếp từ ổ cứng NVMe lên RAM ứng dụng (Zero-copy).
- **Chi phí Đọc O(1)**: Không giống LevelDB/RocksDB phải đọc nhiều tệp SST ở các tầng LSM, cấu trúc của NOMT giống một bảng Hash Index khổng lồ nằm rải rác trên đĩa, đọc giá trị trie cực nhanh với chỉ 1 luồng IO.
- **Thắt nút I/O**: Giải quyết rất tốt vấn đề Bottleneck đĩa cứng của các hệ thống EVM tỷ đô hiện nay.

### Những "Tử Tịch" (Cons / Pain Points) trong MetaNode
1. **Nút Thắt Phổ quát (FFI CGo Overhead)**: 
   - Phần thi hành (Go Execution Engine) và NOMT (Rust) giao tiếp qua cầu nối `CGo` (C/C++ FFI).
   - *Hậu quả*: Mỗi lần Go đọc State Account hay đọc byte Smart Contract, nó mất đi tính năng Goroutine Context Switch nhẹ nhàng của Go, đồng thời CGo overhead (khoảng chục/trăm nanosecond) đội lên rất lớn nếu một block chứa 10,000 giao dịch.
2. **Kẻ Bóp Méo Bộ Nhớ (Memory Hog)**: 
   - Vì bỏ qua OS Page Cache, NOMT đành phải tự Maintain bộ RAM Cache riêng cho nhánh Trie của nó. Đó là lý do khi bật MetaNode, hệ thống ngốn 4GB-8GB RAM ngay cả khi chưa có nhiều dữ liệu. Khó chạy trên máy cá nhân/Edge node nhẹ.
3. **Giới hạn Nền tảng**:
   - Dính cứng với `io_uring` nghĩa là bạn chỉ chạy được sức mạnh tối đa trên Linux kernel > 5.1. Chạy trên macOS/Windows dev máy khá "ực ạch" với fallback truyền thống.
4. **Đồng bộ Mạng (State Sync) khó khăn**: 
   - Đồng bộ nguyên tệp NOMT cho node mới khó khăn và nặng nề hơn rất nhiều so với gửi từng cục SST file của Pebble/LevelDB.

---

## 2. Có Cách Nào Hay Hơn Không? (Kiến Trúc Tương Lai)

Nếu bạn muốn MetaNode vươn lên mốc **100,000 TPS** với khả năng vận hành trơn tru bảo mật, bạn nên cân nhắc một trong **3 mô hình Storage sau đây** để Refactor hệ thống về sau:

### Lựa chọn A: State-Commitment Decoupling (Giải pháp của Monad / Sei v2 / MegaETH)
**Đây là giải pháp kiến trúc hiện đại & tối ưu I/O mạnh mẽ nhất trong ngành Blockchain hiện nay.**

- **Vấn đề cốt lõi của kiến trúc cũ**: Ở các node MVM/EVM thông thường, mọi transaction đều phải thực thi (Read/Write) **trực tiếp lên Cây Merkle (Trie)** để bảo vệ tính toàn vẹn và tính toán lại `state_root`. Điều này khiến 1 tác vụ đọc/ghi cực kỳ đơn giản bị đội chi phí lên mức O(log N) do phải cập nhật lại băm (hash) của tất cả các node cha lên tận gốc của cây. Quá trình tốn kém này tạo ra chokepoint (nút thắt cổ chai) lớn nhất giới hạn tốc độ TPS.

- **Cách tiếp cận Decoupling (Tách Rời)**: Kiến trúc này TÁCH ĐÔI cơ sở dữ liệu State ra làm 2 mảng hoạt động hoàn toàn độc lập và song song với nhau:
  
  1. **State Access (Lớp Truy Cập Execution - Flat KV)**: 
     Chỉ lưu trữ Account Balance, Nonce, Smart Contract State dưới dạng Cặp Key-Value siêu phẳng (Flat) không có cấu trúc rễ phụ (ví dụ dùng bộ nhớ MMap cực nhanh, PebbleDB siêu tinh chỉnh hoặc B-Tree Native trong Go). 
     - *Nhiệm vụ*: Cung cấp tốc độ đọc trần trụi siêu tốc (Thực sự là O(1) thật). Go Execution Master chỉ cần nhặt dữ liệu ở đây lên xử lý Logic của MVM mà không cần đụng chạm một chút gì đến Merkle Tree hay chịu độ trễ của CGo FFI.

  2. **State Commitment (Lớp Ràng buộc Dữ Liệu - Merkle Tree Hash)**: 
     Lớp này (Sử dụng JMT, Verkle hoặc NOMT) đứng ngoài lề, không bao giờ trực tiếp can thiệp cản chân tiến trình Execute của các Transaction.
     - *Nhiệm vụ*: Sau khi phần Go đã xử lý xong kết quả cuối cùng của Block, nó nhặt danh sách sự thay đổi dữ liệu (gọi là *Delta Diffs*) rồi "vứt" xuống một **Luồng chạy nền (Background Thread) tách biệt toàn phần**. Luồng chạy nền này sẽ thong thả tính toán Hash của nhánh Merkle theo cơ chế Batch (Gom cục lớn để tính 1 lần) để nhả ra cái `state_root` phục vụ cho Consensus.

**Sơ đồ Kiến Trúc State-Commitment Decoupling:**

```mermaid
graph TD
    subgraph Transaction Execution Pipeline (Go Core - MVM)
        direction TB
        Tx[Giao dịch chuyển tiền / Gọi Contract] -->|1. Đọc Data cực nhanh O-1| FlatDB[(Flat State Store \n Native Go / MMap)]
        FlatDB -->|2. Cập nhật K-V Phẳng| Tx
        Tx -->|3. Đóng gói Block \n Trả về Execution Kết quả| BlockDone[Block Execution Hoàn tất]
    end
    
    subgraph Background Hashing Pipeline (Rust - NOMT)
        direction TB
        BlockDone -->|4. Push Delta Diffs \n Async - Không chờ| Queue[Batch Update Queue]
        Queue -->|5. Cập nhật nhánh cây chậm rải| Merkle[(Merkle Tree / NOMT Store)]
        Merkle -->|6. Tính State Root Batch| Root[State Root Hash]
    end
    
    Root -.->|7. Kẹp vào Block N để Chứng minh| Block[Block N Header]
    style FlatDB fill:#10b981,stroke:#047857,stroke-width:2px,color:#fff
    style Merkle fill:#3b82f6,stroke:#1d4ed8,stroke-width:2px,color:#fff
```

- **Lợi ích Vượt mặt**:
  - **Dẹp bỏ Overhead của CGo**: Go hoàn toàn tự chơi trong bộ RAM hoặc Flat DB của riêng nó. Goroutines chạy song song tối đa (với Union-Find) không còn bị block chực chờ bởi IO của việc duyệt cây.
  - **Tối đa hóa năng lực Vi xử lý (Pipeline Parallels)**: Luồng thi hành hợp đồng thông minh (MVM) và luồng Hash Merkle Tree có thể chạy song song chồng gối lên nhau ở 2 vòng đời khác nhau trên các nhân CPU vật lý khác nhau. 
- **Đánh giá chung**: **⭐ 5/5**. Đây chính là "Chén Thánh" được các nền tảng siêu tốc săn lùng. Nó chấm dứt sự kẹt cổ chai ở khâu Storage vĩnh viễn, giải phóng 100% năng lực CPU cho con số hàng ngàn TPS của MetaNode.

### Lựa chọn B: Jellyfish Merkle Trie (JMT - Mô hình của Aptos/Diem)
- **Concept**: Thay vì dùng một database tự quản lý I/O như NOMT, sử dụng cây JMT (viết Native hoàn toàn trên Go, ví dụ thông qua fork repo JMT của Diem). JMT được tối ưu hóa đặc biệt để lưu bên trên RocksDB hoặc PebbleDB.
- **Điểm mạnh**:
   - Tránh hoàn toàn lỗi của CGo. Mọi thứ quản lý 100% bằng Garbage Collector của Go.
   - Hỗ trợ Versioning State bẩm sinh. Bạn có thể truy vấn số dư của một Node vào đúng số Block 5,000 dù mạng chạm ngưỡng 10,000, mà không sợ quá tải đĩa.
- **Đánh giá**: **⭐ 4/5**. Rất an toàn và tiêu chuẩn kỹ nghệ tốt, nhưng tốc độ đỉnh chưa chắc ăn được NOMT ở read/write I/O thuần túy.

### Lựa chọn C: Verkle Trees (Tương lai của Ethereum)
- **Concept**: Thay vì băm nhánh (Hash) 16 node như Ethereum MPT, Verkle dùng Vector Commitment và chiều rộng nhánh 256. 
- **Điểm mạnh**: Bằng chứng State Proofs ngắn hơn gấp 30 lần. Nó sẽ cho phép Stateless Blockchains — tức là các Sub Node của bạn GẦN NHƯ KHÔNG CẦN LƯU DATABASE. Master node gửi Proofs kèm trạng thái, Sub node Verify toán học thẳng tay và dùng luôn.
- **Đánh giá**: **⭐ Lâu dài**. Dễ dàng phát triển Sub Node siêu nhẹ chạy trên Phone/Trình duyệt.

---

## 3. Lời Khuyên cho MetaNode Project

Dựa trên thực tiễn của code base MetaNode hiện tại đang lai ghép giữa Master (Rust) và Sub (Go):

1. **Ngắn hạn (1-12 tháng tới)**: **Giữ nguyên NOMT.** Đây là một nước cờ cực dị và có Performance cực kỳ tàn bạo trên AWS/Cloud NVMe hiện nay. Việc đã fix xong các lỗi thất lạc Genesis và cơ chế Cache Invalidation như vừa rồi giúp hệ thống đạt trạng thái Production Ready. Không đáng để đập đi xây lại lúc này. Hãy tối ưu hoá RAM bằng cơ chế Soft Memory Limit ở Go (đã làm ở file `install-services.sh`).

2. **Dài hạn (Trên con đường vươn tới Cấp độ World-Class L1)**: **Nên xây dựng Stateful Decoupling (Mô hình B hoặc A).**
   - Viết lại một cái `pkg/storage/flat_kv.go` dùng Pebble thuần làm Execution Store chính cho Go.
   - Giữ FFI CGo NOMT chỉ làm "Background State Root Generator". Tức là dùng NOMT nhưng không bao giờ trói nó cản đường thread chính của `processTransaction` hay `TxsProcessor`. 
   - Điều này sẽ tháo xích toàn bộ sức mạnh cho con số TPS kinh khủng, đồng thời vẫn có cấu trúc MPT chắc chắn của sổ cái phân tán.
