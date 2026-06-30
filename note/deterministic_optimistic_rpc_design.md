# Tài Liệu Thiết Kế: Kiến Trúc Deterministic Optimistic RPC (Sequencer Độc Quyền)

## 1. 🎯 Đặt Vấn Đề (The Problem)

Trong thiết kế Optimistic RPC tiêu chuẩn, vẫn tồn tại một rủi ro nhỏ (race condition): giao dịch chạy giả lập off-chain thành công nhưng khi đưa lên chuỗi thật lại thất bại do có một giao dịch của người khác xen vào làm thay đổi trạng thái (state) trước. 

Để đạt được **100% độ chính xác** giữa kết quả giả lập (trả về tức thời cho người dùng) và kết quả ghi nhận cuối cùng trên Public Chain, chúng ta cần loại bỏ hoàn toàn yếu tố "cạnh tranh trạng thái không lường trước" (unpredictable state contention).

---

## 2. 💡 Ý Tưởng Kiến Trúc Cốt Lõi (Core Concept)

Giải pháp là biến Private Chain thành một **Sequencer Độc Quyền (Exclusive Sequencer)**, hoạt động như một Lớp 2 (Layer 2 / AppChain) được quản lý tập trung bởi **một Master BLS Key duy nhất**.

*   **Đại diện tập trung:** 1 Key BLS này quản lý và làm đại diện cho hàng ngàn ví người dùng. 
*   **Người gác cổng duy nhất:** Mạng lưới Private Chain trở thành thực thể duy nhất có quyền tương tác làm thay đổi trạng thái của Smart Contract trên Public Chain.

---

## 3. ⚙️ Cơ Chế Hoạt Động (Workflow)

### 3.1. Độc Quyền Hóa Smart Contract (Smart Contract Exclusivity)
Các Smart Contract trên Public Chain được lập trình đặc biệt: Chúng được đóng gói và cấp quyền sao cho **chỉ chấp nhận các giao dịch được bọc (wrapped) hoặc ký (signed) bởi Master BLS Key**. 
Bất kỳ ai (dù là người dùng hợp lệ) cố gắng gửi trực tiếp giao dịch từ ví cá nhân lên Public Chain nhằm vượt mặt (bypass) Private Chain đều sẽ bị Smart Contract từ chối.

### 3.2. Giả Lập Trạng Thái Chính Xác 100% (100% Deterministic Simulation)
Nhờ sự độc quyền ở Bước 3.1, Private Chain kiểm soát toàn vẹn đầu vào. Trình tự như sau:
1. Hàng ngàn ví người dùng gửi giao dịch (TX) tới RPC của Private Chain.
2. Private Chain đưa TX vào hàng đợi và chạy giả lập (Virtual Execution) trên trạng thái (State) nội bộ mới nhất.
3. **Tính Xác Định Tuyệt Đối:** Vì **KHÔNG CÓ BẤT KỲ AI** ở bên ngoài có thể thay đổi state của Smart Contract trên Public Chain, thứ tự giao dịch do Private Chain sắp xếp là thứ tự tuyệt đối (Absolute Ordering).
4. Do đó, trạng thái sau khi giả lập TX(n) sẽ chính xác 100% là nền tảng cho TX(n+1). Không bao giờ có sự xung đột.
5. RPC trả kết quả giả lập (Mock Receipt) thành công về cho người dùng ngay lập tức với sự cam kết 100% tính chính xác.

### 3.3. Đóng Gói Và Ghi Nhận Lên Public Chain (Batching & Settlement)
1. Các giao dịch sau khi chạy giả lập sẽ được lưu vào cơ sở dữ liệu nội bộ của Private Chain.
2. Thay vì gửi từng giao dịch lẻ tẻ, Private Chain định kỳ **đóng gói (Batching)** các trạng thái hoặc giao dịch này lại.
3. Private Chain sử dụng **Master BLS Key** để ký xác thực gói dữ liệu này và gửi nó lên Public Chain.
4. Public Chain xác minh chữ ký BLS hợp lệ, ghi nhận trạng thái cuối cùng. Trạng thái trên Public Chain lúc này khớp 100% với những gì RPC đã trả về cho người dùng trước đó.

---

## 4. 🚀 Ưu Điểm Tuyệt Đối Của Kiến Trúc

1. **Trải Nghiệm Thời Gian Thực (Zero-Latency UX):** Người dùng nhận kết quả thành công ngay lập tức mà không phải đối mặt với rủi ro giao dịch bị "revert" khi lên chuỗi chính.
2. **Loại Bỏ Hoàn Toàn MEV & Front-Running:** Do Private Chain tự định đoạt thứ tự giao dịch một cách độc quyền, các bot săn MEV trên Public Chain không thể can thiệp hay chèn ép giao dịch của người dùng.
3. **Tiết Kiệm Chi Phí Gas Khổng Lồ:** Hàng ngàn giao dịch của hàng ngàn ví được cuộn lại (Rollup) và cập nhật lên Public Chain thông qua số ít giao dịch của Master BLS Key.
4. **Kiểm Soát & Sàng Lọc Toàn Vẹn:** Private Chain có thể đóng vai trò bộ lọc, từ chối ngay lập tức các giao dịch lỗi, độc hại, hoặc không đủ điều kiện (ví dụ: blacklist) trước khi chúng kịp tiêu tốn tài nguyên trên Public Chain.

---

## 5. ⚠️ Thách Thức Và Hướng Giải Quyết

*   **Tính Tập Trung (Centralization Risk):** Thiết kế này đặt niềm tin tuyệt đối vào Private Chain và Master BLS Key. 
*   **Điểm Yếu Duy Nhất (Single Point of Failure):** Nếu Master BLS Key bị lộ hoặc mất, toàn bộ tài sản và hợp đồng bị ảnh hưởng.
    *   **Khắc phục:** Không lưu Master BLS Key trên một máy chủ duy nhất. Sử dụng công nghệ **Tính toán nhiều bên (Multi-Party Computation - MPC)** hoặc **Lược đồ Chữ ký Ngưỡng (Threshold Signature Scheme - TSS)** để phân mảnh BLS Key ra nhiều node trong Private Chain. Giao dịch chỉ được ký hợp lệ khi có đủ $M/N$ node đồng thuận ký.

---

## 6. ⚡ Tối Ưu Hóa Tốc Độ "Siêu Nhanh" (Ultra-High Performance Ideas)

Để hệ thống không chỉ chính xác mà còn đạt tốc độ tính bằng **micro-giây (µs)** (nhanh hơn hàng nghìn lần so với chạy EVM thông thường), chúng ta có thể áp dụng các kiến trúc nâng cao sau cho Private Chain:

### Ý Tưởng 1: State Engine chạy hoàn toàn trên RAM (In-Memory Database)
Việc máy ảo EVM phải lấy dữ liệu từ đĩa cứng (LevelDB/RocksDB) là nguyên nhân lớn nhất gây chậm trễ.
- **Giải pháp:** Đối với các giao dịch Native (chuyển coin) hoặc Game đơn giản, Private Chain sẽ duy trì một `In-Memory State` (Lưu State hoàn toàn trên RAM). 
- Khi có giao dịch tới, RPC chỉ làm phép toán cộng trừ số dư trực tiếp trên RAM và phản hồi lại ngay trong **< 0.1ms**. Việc ghi xuống đĩa cứng (Disk I/O) và chạy EVM để tạo bằng chứng (Proof) sẽ được đẩy xuống các Asynchronous Workers chạy ngầm.

### Ý Tưởng 2: Kiến trúc Dựa trên Ý định (Intent-based / Matching Engine kiểu CEX)
Thay vì gửi "Transaction" theo chuẩn EVM truyền thống (phải có nonce, gas limit, data payload phức tạp), người dùng chỉ gửi một thông điệp "Ý định" (Intent) được ký bằng private key.
- **Giải pháp:** Private Chain hoạt động hệt như bộ khớp lệnh (Matching Engine) của sàn Binance. Nó nhận hàng ngàn Intent trên RAM, khớp lệnh siêu tốc (Ví dụ: ví A trả tiền cho ví B). 
- Sau đó, hệ thống (Sequencer) mới tổng hợp lại kết quả cuối cùng thành 1 Transaction EVM duy nhất (ký bằng BLS Key) và đẩy nó lên Public Chain. EVM hoàn toàn bị bỏ qua ở lớp tương tác với người dùng.

### Ý Tưởng 3: Cắt đôi quá trình bằng "State Lock" (Lazy Execution)
Tương tự cơ chế của thẻ tín dụng (Authorize & Capture).
- Khi người dùng gửi lệnh, hệ thống chỉ kiểm tra đúng 2 thao tác cực nhanh O(1): (1) Chữ ký đúng không? (2) Số dư khả dụng đủ không?
- Nếu đủ, hệ thống lập tức **"Khóa" (Lock)** phần tiền đó lại và trả về "Thành công" cho người dùng ngay lập tức.
- Các tính toán logic phức tạp của Smart Contract được vứt về phía sau cho Worker chạy thong thả. Cách này triệt tiêu hoàn toàn nút thắt cổ chai về mặt tính toán cho cổng RPC.

### Ý Tưởng 4: Chạy Giả Lập Song Song (Parallel Execution qua Actor Model)
Thay vì giả lập từng giao dịch một (Sequential), hãy dùng kiến trúc Actor (tương tự như `tx_processor.go` hiện tại của Metanode) để phân mảnh các trạng thái.
- Hàng vạn giao dịch độc lập (không chạm vào cùng một biến/tài khoản) sẽ được RPC chạy mô mô phỏng giả lập song song trên hàng chục core CPU khác nhau. Tránh hoàn toàn việc khóa biến (Mutex Lock). Mức TPS trả về kết quả giả lập có thể đạt tới hàng trăm nghìn (100,000+ TPS).

---

## 7. 🔗 Cơ Chế Đảm Bảo Khớp Kết Quả 100% (State Equivalence Mechanism)

Để đảm bảo kết quả chạy giả lập trên Private Chain **khớp chính xác tuyệt đối (100%)** với kết quả thực thi cuối cùng trên Public Chain, hệ thống phải tuân thủ nghiêm ngặt 4 cơ chế bảo chứng (guarantees) sau đây:

### Cơ Chế 1: Đồng Bộ Trạng Thái & Máy Ảo (State Machine Isomorphism)
Cả Private Chain (lúc giả lập) và Public Chain (lúc ghi nhận) bắt buộc phải sử dụng cùng một bộ quy tắc cốt lõi:
- **Môi trường thực thi:** EVM của Private Chain phải ánh xạ 1:1 với EVM của Public Chain (Cùng Gas Schedule, cùng tập Opcodes).
- **Đồng bộ hóa gốc (Pre-state Root):** Trước khi Private Chain chạy giả lập hàng vạn TX, nó phải tải đúng `State Root` mới nhất của Public Contract làm điểm bắt đầu. Nếu đầu vào giống nhau, máy ảo giống nhau thì đầu ra bắt buộc phải giống nhau.

### Cơ Chế 2: Niêm Phong Thứ Tự Tuyệt Đối (Strict Transaction Ordering)
Kết quả của Smart Contract bị thay đổi hoàn toàn nếu thứ tự giao dịch bị đảo lộn (Ví dụ: Lệnh Mua chạy trước lệnh Bán sẽ ra kết quả khác lệnh Bán trước lệnh Mua).
- **Giải pháp:** Private Chain (vai trò Sequencer) sẽ gán **Index (Số thứ tự cố định)** cho từng giao dịch sau khi chạy giả lập thành công.
- Khi đẩy lên Public Chain, toàn bộ mảng giao dịch này được **"Niêm phong" (Sealed)** bằng chữ ký Master BLS. 
- Contract trên Public Chain được lập trình để duyệt mảng này bằng vòng lặp tuần tự tuyệt đối. Miner/Validator của Public Chain **không có quyền** chèn thêm, bớt đi hay xáo trộn vị trí các giao dịch trong gói (Batch) này.

### Cơ Chế 3: Cô Lập Biến Môi Trường (Context Isolation)
Lỗi lệch kết quả lớn nhất thường đến từ việc Smart Contract sử dụng các biến môi trường của chuỗi khối như `block.timestamp` hay `block.number`. Nếu giả lập lúc 8:00 sáng, nhưng Public Chain đóng block lúc 8:05 sáng, các logic liên quan đến thời gian sẽ bị sai lệch.
- **Giải pháp:** Smart Contract trên Public Chain **BỊ CẤM** gọi trực tiếp các biến môi trường toàn cục (global variables). Thay vào đó, thời gian `timestamp` và `block_number` phải được tính toán và chốt cứng trên Private Chain, đóng gói vào Batch, ký bởi BLS Key và truyền vào Contract dưới dạng **Tham số (Parameter)**. Public Contract phải coi dữ liệu từ Private Chain là Nguồn Sự Thật duy nhất (Source of Truth).

### Cơ Chế 4: Ràng Buộc Bằng Toán Học (Cryptographic Proofs - Tùy chọn)
Để Public Chain không cần phải chạy lại toàn bộ tính toán (vừa chậm vừa tốn gas) mà vẫn chắc chắn kết quả là đúng:
- **Mô hình ZK-Rollup:** Private Chain không chỉ gửi danh sách giao dịch, mà còn sinh ra một Bằng chứng Không Tri Thức (ZK-SNARK proof) chứng minh: *"Áp dụng mảng TX này vào State A, kết quả chắc chắn toán học là State B"*. Public Chain chỉ việc xác thực (Verify) bằng chứng này cực kỳ nhanh.
- **Mô hình Optimistic Rollup:** Private Chain đẩy thẳng kết quả State B (Root) lên Public Chain. Mạng lưới cho phép một khoảng thời gian (Challenge Period) để bất kỳ ai phát hiện tính toán sai sót gửi Bằng chứng Gian Lận (Fraud Proof). Nếu sai, kết quả bị revert và Private Chain bị phạt tiền (Slashing). Nhờ vậy kết quả luôn được giữ chuẩn xác.

---

## 8. 🛡️ Tích Hợp TEE (Trusted Execution Environment) Cho RPC Ký Hộ

Để giải quyết triệt để rủi ro tập trung (Centralization Risk) và điểm yếu duy nhất của Master BLS Key (đã nêu ở phần 5), hệ thống tích hợp công nghệ **TEE (như Intel SGX, AMD SEV, AWS Nitro Enclaves)** đóng vai trò như một "Vùng An Toàn Tuyệt Đối" (Secure Enclave).

### 8.1. Vị Trí Của TEE Trong Kiến Trúc
* **Host (Untrusted Zone):** Nhận giao dịch từ người dùng, quản lý kết nối P2P/RPC, duy trì In-Memory Database và chạy giả lập song song cường độ cao.
* **TEE Enclave (Trusted Zone):** Nơi **duy nhất** khởi tạo và lưu giữ Master BLS Private Key. Bộ nhớ của TEE được mã hóa ở cấp độ phần cứng, ngay cả Admin hay hệ điều hành Host cũng không thể trích xuất được Private Key.

### 8.2. Ranh Giới Thực Thi: Host vs TEE (Giải Quyết Bài Toán Dung Lượng TEE)

Việc đưa toàn bộ máy ảo (MVM/EVM) và State Database vào trong TEE (như SGX) là bất khả thi, tốn kém và sẽ làm sập hiệu năng hệ thống. Do đó, kiến trúc áp dụng nguyên tắc **"Thực thi nặng ở ngoài, Xác thực và Ký ở trong" (Execute Outside, Verify & Sign Inside)**.

#### Phần 1: Chạy ở HOST (Môi trường ngoài - Untrusted, Cấu hình siêu mạnh)
* **P2P & RPC Server:** Lắng nghe giao dịch từ người dùng và đẩy vào hàng đợi (Mempool).
* **State Storage:** Quản lý toàn bộ dữ liệu State (In-Memory hoặc LevelDB/RocksDB).
* **MVM (Metanode Virtual Machine):** Đảm nhận 100% việc chạy các Smart Contract phức tạp. Nó tính toán logic, thay đổi biến môi trường, và sinh ra `Post-State Root`.
* **Đóng gói (Batch Builder):** Gộp hàng ngàn giao dịch lại thành một Batch.
=> *Tóm lại: Host làm toàn bộ phần việc "nặng nhọc" (Heavy lifting), nhưng nó không có quyền tự ký chốt hạ.*

#### Phần 2: Chạy ở TEE Enclave (Vùng an toàn - Lightweight, Bảo mật tuyệt đối)
TEE chỉ chứa một bộ mã (Micro-logic) cực nhẹ viết bằng Rust/C++, **hoàn toàn không chứa MVM**. TEE làm 3 nhiệm vụ:
1. **Lưu Trữ Khóa (Key Custody):** Nơi duy nhất chứa Master BLS Private Key.
2. **Thẩm Định Siêu Nhẹ (Stateless Verification):** Host chỉ truyền vào TEE một dữ liệu cực nhỏ qua ECALL gồm: *Danh sách Giao dịch thô, State Root, và Merkle Proofs*.
   - TEE verify chữ ký gốc (Secp256k1/Ed25519) của từng user.
   - TEE chốt cứng danh sách Hash giao dịch, đảm bảo Host không được phép xáo trộn thứ tự (Chống MEV).
   - Với giao dịch Native (chuyển coin), TEE dùng Merkle Proof do Host cấp để tự cộng trừ kiểm tra số dư (bỏ qua MVM).
3. **Ký Hộ (Proxy Signing):** Sau khi xác nhận thứ tự và chữ ký User là hợp lệ, TEE lấy Master BLS Key để **Ký lên Hash của toàn bộ Batch**. Chữ ký BLS này đưa ra ngoài cho Host mang lên Public Chain nộp.

### 8.3. Ưu Điểm Của Kiến Trúc TEE So Với MPC/TSS
- **Độ trễ cực thấp (Micro-seconds):** Thay vì phải gửi thông điệp qua lại giữa nhiều node để ký TSS (gây bottleneck mạng), chữ ký BLS được TEE sinh ra cục bộ trực tiếp trên CPU, phù hợp hoàn hảo với kiến trúc *In-Memory Database*.
- **Đơn giản hóa hạ tầng mạng:** Tránh được các thuật toán phân mảnh khóa phức tạp (DKG) và các kịch bản khôi phục khi node rớt mạng. Tăng tính sẵn sàng (Liveness) của hệ thống.
- **Tính công bằng tuyệt đối (Fair Sequencing):** Logic Sequencer có thể khóa chặt bên trong TEE, đảm bảo người vận hành Private Chain không thể tự ý xáo trộn thứ tự giao dịch để trục lợi cá nhân.

### 8.4. Xử Lý Kịch Bản Host Gian Lận (Host Maliciousness)

Vì phần thực thi nặng (MVM) nằm ở Host (Untrusted), chuyện gì xảy ra nếu Admin của Host cố tình gian lận để trục lợi? Kiến trúc "Execute Outside, Verify & Sign Inside" kết hợp với Rollup giải quyết triệt để như sau:

1. **Host chèn giao dịch giả / Sửa số tiền của người dùng?**
   - **Bị TEE chặn đứng:** TEE tự tay xác minh chữ ký (Secp256k1/Ed25519) của người dùng trên *từng* giao dịch thô truyền vào. Nếu Host tự ý chèn giao dịch không có chữ ký hợp lệ, TEE sẽ từ chối Ký Hộ (Proxy Sign) cho toàn bộ Batch đó.
   
2. **Host thay đổi, xáo trộn thứ tự giao dịch (Reordering, Front-running)?**
   - **Bị TEE chặn đứng:** Mặc dù Host thu thập giao dịch, nhưng TEE mới là người tính toán Hash của toàn bộ mảng giao dịch đó để "niêm phong". TEE ký BLS lên cái Hash chốt này. Nếu Host mang lên Public Chain một thứ tự khác để chèn ép người dùng (MEV), Hash sẽ thay đổi, chữ ký BLS sẽ không khớp và Public Chain sẽ reject lệnh.

3. **Host chạy MVM cố tình sai và lừa TEE bằng một "State Root" giả mạo?**
   - Vì TEE không chứa MVM, nó không biết kết quả chạy Smart Contract phức tạp có đúng hay không. Host có thể báo cáo láo State Root cho TEE. Tuy nhiên, hệ thống được bảo vệ bởi lớp khiên thứ hai: **Cơ Chế Optimistic Rollup (Cơ chế 4 ở phần 7)**.
   - Chữ ký BLS của TEE lúc này chỉ mang ý nghĩa xác thực **Dữ liệu đầu vào (Input/Sequencing)** là công bằng và xuất phát từ Private Chain hợp pháp.
   - Khi State Root (Output) nộp lên Public Chain, nó vẫn có một khoảng thời gian **Challenge Period**. Các node giám sát (Watchtowers) chạy độc lập trên mạng sẽ tự chạy lại MVM. Nếu phát hiện Host tính toán sai, họ lập tức nộp **Fraud Proof (Bằng chứng gian lận)**.
   - Kết quả: Lệnh sai bị Revert, và số tiền cọc (Stake) khổng lồ của Admin Private Chain sẽ bị tịch thu (Slashing). Host hoàn toàn không có động cơ để lừa TEE về mặt State Root.

*(Tóm tắt nguyên lý: TEE bảo đảm Đầu vào sạch và Không bị tráo bài. Optimistic Fraud Proof bảo đảm Đầu ra chính xác tuyệt đối).*
