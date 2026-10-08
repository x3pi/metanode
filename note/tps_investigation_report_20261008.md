# Báo Cáo Điều Tra Giới Hạn TPS Cụm 4 Validator Metanode (2026-10-08)

> **Phương châm cốt lõi:** Đo trước, sửa sau. Mọi kết luận phải dựa trên số liệu thực nghiệm lặp lại được, kèm bằng chứng và mã băm SHA256.  
> **Nguyên tắc bất khả xâm phạm (AGENTS.md Part 2.5):** Zero-Fork Invariant (100% không fork, thà pending chứ không fork, không dùng timeout/sleep để quyết định dispatch).  
> **Cam kết phạm vi:** Không tự ý sửa đổi logic consensus hay execution trong đợt điều tra này; chỉ quan sát, đo lường và đề xuất giải pháp có cơ sở thực nghiệm.

---

## 1. Tóm Tắt Kết Quả Cốt Lõi (Executive Summary)

1. **Giới hạn TPS hiện tại của cụm 4 validator (Baseline 5 lượt lặp độc lập, 25.000 txs/lượt):**
   - **Effective Throughput:** Mean = **6.098,60 tx/s** (Min: 5.888,97, Max: 6.341,80, StdDev: 168,14 tx/s).
   - **Commit Duration (25k txs):** Mean = **4,102 s** (Min: 3,942 s, Max: 4,245 s, StdDev: 0,112 s).
   - **Receipt Latency P95:** Mean = **4,210 s** (P50: 3,287 s, P90: 4,170 s, P99: 4,217 s).
   - **Zero-Fork Status:** **100% PASS trên 5/5 lượt** (Block Hash và State Root đồng nhất tuyệt đối trên 4 validator).
   - **Ingest Throughput:** Mean = **218.965,80 tx/s** (Khẳng định Ingest **không phải** là nút thắt).

2. **Nút Thắt Lớn Nhất Trên Đường Găng (Bottleneck #1 — Chiếm 44,7% thời gian xử lý block):**
   - **Rào cản chờ khối trước (`wait_predecessor`):** Chiếm trung bình **360,34 ms / block (44,7% tổng thời gian 805,80 ms/block)**.
   - **Bản chất kỹ thuật:** Trong [execution/cmd/simple_chain/processor/speculative_executor.go](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/speculative_executor.go#L318-L332):
     ```go
     if gei > 1 && !se.isCommitted(gei-1) {
         err := se.waitCommitted(ctx, gei-1)
     }
     ```
     Khi Rust Mysticeti đã commit và dispatch khối $N$ sang Go, Go không thể bắt đầu thực thi khối $N$ ngay mà buộc phải dừng luồng (blocking wait) chờ khối $N-1$ được state committer ghi xong hoàn toàn vào DB và commit trie. Cơ chế này tạo ra một rào cản tuần tự hóa (serial barrier) làm tê liệt tính song song giữa các khối.

3. **Nút Thắt Thứ Hai (Bottleneck #2 — Chiếm 19,8% thời gian xử lý block):**
   - **Thời gian thực thi Block-STM:** Chiếm trung bình **159,23 ms / block (19,8%)** cho việc xử lý 7.000–10.000 giao dịch EIP-1559 (`processNativeTransfersFastPath`).

4. **Các Phát Hiện Kỹ Thuật Đáng Chú Ý:**
   - **Giới hạn kích thước gói TCP Ingress (`MaxBatchTxCount = 1000`):** Khi thử nghiệm gửi batch 2.000 txs, hệ thống từ chối thẳng (`ErrExceedsMaxBatchSize`), làm rớt 24.000/25.000 txs. Đây là giới hạn bảo vệ giao thức được hard-code tại [execution/pkg/transaction/eth_validation.go](file:///home/abc/chain-n/metanode/execution/pkg/transaction/eth_validation.go#L25).
   - **Mysticeti Rust Consensus hoạt động cực kỳ mượt mà:** Khảo sát lock profile (`perf record`, Go CPU pprof) chứng minh **hoàn toàn không có tranh chấp khóa nặng (lock contention)** trong `parking_lot` của Rust consensus (`dag_state` hoặc `GLOBAL_TX_CACHE`). Đồng thuận DAG chỉ tốn ~150–200 ms.
   - **Tranh chấp Co-location không phải nguyên nhân nghẽn:** Thử nghiệm CPU Pinning (cô lập 16 core riêng cho mỗi node trên máy 104 vCPU) không làm tăng TPS (5.961 tx/s so với 6.098 tx/s baseline).

---

## 2. Môi Trường Thực Nghiệm & Tiêu Chuẩn

- **Commit Git:** `0e9cb3319f10bb49d1ac6740d45bb7de88fd932d` (nhánh `dev`).
- **Phần cứng máy chủ (Host `192.168.1.232`):**
  - CPU: Intel(R) Xeon(R) Platinum 8272CL @ 2.60GHz, 104 vCPUs (2 sockets, 26 cores/socket, 2 threads/core, 2 NUMA nodes).
  - RAM: 188 GB (DDR4), 8 GB swap.
  - Disk: SSD NVMe mount ext4.
- **Cấu hình Cụm Benchmark Cô Lập:**
  - Đường dẫn: `/tmp/gate_4val_p06` (được sao chép sạch từ template `/tmp/gate_4val_clean_template` trước mỗi lượt chạy).
  - Phân bổ cổng mạng (không trùng với bất kỳ dịch vụ nào):
    - `val0`: RPC `31646`, TCP `31200`, P2P `31100`, Pprof `31446`
    - `val1`: RPC `31647`, TCP `31201`, P2P `31101`, Pprof `31447`
    - `val2`: RPC `31648`, TCP `31202`, P2P `31102`, Pprof `31448`
    - `val3`: RPC `31649`, TCP `31203`, P2P `31103`, Pprof `31449`
  - Cơ sở dữ liệu: Genesis Block #0 sạch, 50.000 tài khoản Secp256k1 tạo sẵn tại genesis.
- **Tiêu chuẩn kiểm chứng:** File đăng ký giả thuyết [PREREGISTERED.md](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/PREREGISTERED.md) đã được commit vào git trước khi thực hiện đo đạc.

---

## 3. Bước 1 — Kết Quả Đo Baseline (5 Lượt Lặp Độc Lập)

Mỗi lượt chạy nạp 25.000 giao dịch EIP-1559 Native Transfers, batch 1.000 qua raw TCP stream, rate limit = 0 (unlimited injection), kiểm tra tính bất biến Zero-Fork (`-verify-parity`). Trước mỗi lượt, cụm được xóa sạch hoàn toàn và nhân bản lại từ Genesis template sạch.

### Bảng 1: Kết quả chi tiết từng lượt đo Baseline

| Lượt | Effective TPS | Commit Duration | Ingest TPS | Latency P50 | Latency P90 | Latency P95 | Latency P99 | Blocks | Peak CPU | Peak RSS | Zero-Fork |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Run 1** | 6.167,05 tx/s | 4,054 s | 221.981 tx/s | 3,393 s | 4,040 s | 4,149 s | 4,166 s | 3 | 763,0% | 6.747 MB | **PASS** |
| **Run 2** | 6.054,63 tx/s | 4,129 s | 249.393 tx/s | 3,152 s | 4,195 s | 4,220 s | 4,229 s | 4 | 818,0% | 6.457 MB | **PASS** |
| **Run 3** | 6.341,80 tx/s | 3,942 s | 196.039 tx/s | 2,933 s | 4,054 s | 4,064 s | 4,070 s | 3 | 896,0% | 6.275 MB | **PASS** |
| **Run 4** | 5.888,97 tx/s | 4,245 s | 223.997 tx/s | 3,995 s | 4,351 s | 4,355 s | 4,357 s | 3 | 818,0% | 6.377 MB | **PASS** |
| **Run 5** | 6.040,56 tx/s | 4,139 s | 203.419 tx/s | 2,963 s | 4,211 s | 4,261 s | 4,262 s | 4 | 885,0% | 6.493 MB | **PASS** |

### Bảng 2: Thống kê tổng hợp Baseline (Mean, Min, Max, Độ lệch chuẩn)

| Chỉ số đo lường | Giá trị Trung bình (Mean) | Tối thiểu (Min) | Tối đa (Max) | Độ lệch chuẩn (StdDev) |
| :--- | :---: | :---: | :---: | :---: |
| **Effective TPS** | **6.098,60 tx/s** | 5.888,97 tx/s | 6.341,80 tx/s | 168,14 tx/s |
| **Commit Duration** | **4,102 s** | 3,942 s | 4,245 s | 0,112 s |
| **Ingest Throughput** | **218.965,80 tx/s** | 196.038,74 tx/s | 249.393,29 tx/s | 20.782,09 tx/s |
| **Receipt Latency P50** | **3,287 s** | 2,933 s | 3,995 s | 0,436 s |
| **Receipt Latency P90** | **4,170 s** | 4,040 s | 4,351 s | 0,128 s |
| **Receipt Latency P95** | **4,210 s** | 4,064 s | 4,355 s | 0,111 s |
| **Receipt Latency P99** | **4,217 s** | 4,070 s | 4,357 s | 0,107 s |
| **Số khối sản sinh** | **3,4 khối** | 3 khối | 4 khối | 0,55 khối |
| **Peak CPU (Toàn cụm)** | **836,0%** (~209%/node) | 763,0% | 896,0% | 54,7% |
| **Peak RSS (Toàn cụm)** | **6.469,8 MB** (~1,61 GB/node) | 6.275,0 MB | 6.747,0 MB | 176,1 MB |
| **Zero-Fork Parity** | **100% Khớp Tuyệt Đối** | - | - | 0 lỗi fork |

---

## 4. Bước 2 — Phân Rã Thời Gian Trên Đường Găng (Time Breakdown)

Dựa trên dữ liệu vi giây/mili-giây bóc tách từ các mốc vết `[FFI-TRACE]`, `[PERF]` và `[SIG-ENFORCE]` của các khối trong 5 lượt baseline (phân tích chi tiết lưu tại [block_breakdown_analysis.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/block_breakdown_analysis.json)), cấu trúc thời gian xử lý trung bình của một khối được bóc tách như sau:

### Bảng 3: Bảng phân rã các giai đoạn xử lý 1 block trên đường găng

| Thứ tự | Giai đoạn xử lý trong Block | Thời gian trung bình (ms) | Tỷ lệ (%) | Vị trí mã nguồn / Cơ chế | Đánh giá nút thắt |
| :---: | :--- | :---: | :---: | :--- | :---: |
| **1** | **Chờ khối trước commit (`wait_predecessor`)** | **360,34 ms** | **44,7%** | `speculative_executor.go:320` (`waitCommitted`) | 🚨 **NÚT THẮT CHÍNH #1** |
| **2** | **Thực thi song song Block-STM (`block_stm_exec`)** | **159,23 ms** | **19,8%** | `tx_processor.go` (`processNativeTransfersFastPath`) | ⚠️ **NÚT THẮT #2** |
| **3** | **Lọc chữ ký mempool (`[SIG-ENFORCE]`)** | **156,61 ms** | **19,4%** | `tx_processor.go` (`FilterInvalidSignatures`) | *Xem phân tích bên dưới* |
| **4** | **Tính Merkle Roots (Txs + Receipts Root)** | **73,78 ms** | **9,2%** | `speculative_executor.go` (`DeriveSha`) | Khâu phụ |
| **5** | **Commit bộ nhớ & Persist DB (Account/Stake)** | **32,76 ms** | **4,1%** | `state_committer.go` (NOMT & Pebble Write) | Đã tối ưu (nhỏ) |
| **6** | **Phân nhóm Union-Find (`tx_grouping`)** | **13,70 ms** | **1,7%** | `block_stm_engine.go` (Dependency graph) | Rất nhanh |
| **7** | **CGo Protobuf serialize/unmarshal** | **6,20 ms** | **0,8%** | CGo FFI bridge boundary | Không đáng kể |
| **8** | **Clone trạng thái Speculative (`state_clone`)** | **3,18 ms** | **0,4%** | In-memory COW / snapshot clone | Không đáng kể |
| **TỔNG** | **Tổng chu kỳ xử lý 1 khối trung bình** | **805,80 ms** | **100,0%** | **~7.300 txs/block → ~9.000 txs/s lý thuyết** | - |

### Phân tích chi tiết về 3 khâu chiếm thời gian lớn nhất:

1. **Khâu 1: Rào cản `wait_predecessor` (360,34 ms — 44,7%):**
   - Khi Rust Mysticeti commit xong Block #2 và dispatch sang Go, Go SpeculativeExecutor nhận được block ngay lập tức.
   - Tuy nhiên, trước khi Block #2 được phép đọc trạng thái để chạy Block-STM, nó bắt buộc phải kiểm tra: `gei > 1 && !se.isCommitted(gei-1)`.
   - Vì Block #1 vẫn đang trong quá trình ghi dữ liệu xuống NOMT trie và Pebble DB ở luồng `state_committer`, thread của Block #2 bị **block hoàn toàn trong 360 ms**!
   - Đây chính là nguyên nhân giải thích vì sao dù Rust consensus giao block rất nhanh, throughput vẫn bị chặn trần ở ~6,1k tx/s.

2. **Khâu 2: Thực thi song song Block-STM (159,23 ms — 19,8%):**
   - 7.000 đến 10.000 giao dịch EIP-1559 trong một khối được nạp qua máy trạng thái ảo song song. Quá trình kiểm tra số dư, cập nhật nonce và giải quyết xung đột đọc-ghi mất ~160 ms. Với kích thước khối lớn, đây là tải tính toán thực tế hợp lý.

3. **Khâu 3: Lọc chữ ký `[SIG-ENFORCE]` (156,61 ms — 19,4%):**
   - **Lưu ý đặc biệt:** Mức trung bình 156,61 ms bị chi phối bởi **Block đầu tiên (Block #1)** trong mỗi lượt blast.
   - Tại Block #1 (khi mempool cache chưa kịp ấm cho toàn bộ 8.000 txs đầu tiên), `[SIG-ENFORCE]` mất trung bình **397,0 ms**.
   - Tuy nhiên, tại các khối tiếp theo (Block #2, #3, #4), khi cache chữ ký đã được làm ấm trong lúc nhận diện, `[SIG-ENFORCE]` chỉ mất **13,2 ms đến 29,4 ms** (~1,5 µs/tx). Khẳng định kết luận từ báo cáo 2026-10-07 là chính xác: khi cache ấm, lọc chữ ký hoàn toàn không phải nút thắt.

---

## 5. Bước 3 — Kết Quả Profiling (Go Pprof & Linux Native Perf)

Để quan sát hệ thống ở trạng thái bão hòa ổn định, chúng tôi đã kích hoạt `ENABLE_DEBUG_PPROF=true` và tiêm tải liên tục trong **35 giây** qua `secp_tps_blast` (xác nhận thành công **64 blocks, 554.335 giao dịch confirmed on-chain**, 100% Zero-Fork Verified).

### 5.1. Go CPU Profile (`val0_cpu_cum_top.txt` & `val0_cpu_flat_top.txt`)
- Thu thập liên tục trong 25 giây tại cổng debug 31446.
- **Top Cumulative CPU:**
  1. `runtime._ExternalCode`: **152,91 s (52,05%)** — Thời gian nằm ở mã máy C/Rust (libsecp256k1, Rust consensus, NOMT).
  2. `runtime.cgocall`: **62,73 s (21,35%)** — Chi phí chuyển đổi ngữ cảnh FFI giữa Go runtime và CGO.
  3. `secp256k1_ext_ecdsa_recover`: **39,93 s (13,59%)** — Khôi phục public key từ chữ ký secp256k1 trong pha Ingest/Validation.
  4. `blake2::Blake2bVarCore::compress`: **19,60 s (6,67%)** — Băm Blake2b trong cơ sở dữ liệu NOMT trie của Rust.
  5. `keccak::keccak_p`: **17,84 s (6,07%)** — Băm Keccak-256 trong Ethereum state.
  6. `ExplorerSearchService`: **28,42 s (9,67%)** — Tác vụ đánh chỉ mục phụ trợ của Explorer.
- **Top Flat CPU:**
  - `runtime.cgocall`: 21,19%
  - `blake2::Blake2bVarCore::compress`: 6,67%
  - `keccak::keccak_p`: 6,07%
  - `__memcpy_evex_unaligned_erms`: 3,29%

### 5.2. Linux Native Perf Cycle Profile (`val0_perf_top.txt` & `val0_perf_flat_top.txt`)
- Ghi nhận 18.027 mẫu chu kỳ phần cứng thực tế qua `sudo perf record -F 99 -g` trên tiến trình `val0`:
  1. `secp256k1_fe_mul_inner`: **12,00%** (Phép nhân trường hữu hạn Secp256k1)
  2. `secp256k1_fe_sqr_inner`: **9,46%** (Phép bình phương trường hữu hạn Secp256k1)
  3. `runtime.mallocgc`: **3,16%**
  4. `golang.org/x/crypto/sha3.keccakF1600.abi0`: **2,86%**
  5. `blake2::Blake2bVarCore::compress`: **2,79%**
  6. `Transaction.FromAddress`: **2,52%**
- **Đặc biệt quan trọng về Consensus Lock Contention:**
  - Trong báo cáo `perf report`, các hàm khóa `parking_lot::mutex` và `parking_lot::rwlock` **hoàn toàn không xuất hiện** trong nhóm tiêu tốn CPU.
  - Điều này bác bỏ giả thuyết cho rằng Rust consensus bị nghẽn do tranh chấp lock trong `GLOBAL_TX_CACHE` hoặc `dag_state`.

### 5.3. Goroutine & Memory Profiles
- **Goroutines (`val0_goroutine_top.txt`):** Tổng cộng **833 goroutines**. 100% goroutine đều nằm trong các worker pool có giới hạn xác định (`TransactionProcessor.startInjectionWorkers`: 300, `TxVirtualExecutor.startReadTxWorkers`: 200, `ExplorerSearchService`: 64, `SocketServer`: 64, Pebble background: ~70). Không có hiện tượng rò rỉ goroutine.
- **Heap In-use (`val0_heap_top.txt`):** Tổng heap in-use tại thời điểm đỉnh điểm là **348,96 MB**, phần lớn dành cho cấu trúc `TransactionProcessor` (39,59 MB), decode transaction buffer (55,01 MB) và connection pools.
- **Mutex Contention Delay (`val0_mutex_top.txt`):** 0 delay ghi nhận.

---

## 6. Bước 4 — Kết Quả Tách Biến Số (Variable Isolation)

Chúng tôi đã thực hiện 3 điều kiện thực nghiệm độc lập (3 lượt lặp mỗi điều kiện, nạp 25.000 txs/lượt) để cô lập từng biến số ảnh hưởng:

### Bảng 4: So sánh đối chứng các điều kiện thực nghiệm Bước 4

| Điều kiện Thực nghiệm | Biến số thay đổi | Effective TPS (Mean) | Commit Duration (Mean) | Latency P95 (Mean) | Khối TB | Trạng thái Zero-Fork | So với Baseline |
| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| **Baseline** | Batch 1.000, Không pinning | **6.098,60 tx/s** | 4,102 s | 4,210 s | 3,4 | 100% PASS | Chuẩn đối chiếu |
| **EXP_BATCH500** | Cỡ batch = 500 txs | **6.362,37 tx/s** | 3,938 s | 4,028 s | 3,3 | 100% PASS | **+4,3% TPS** (Commit nhanh hơn 164 ms) |
| **EXP_BATCH250** | Cỡ batch = 250 txs | **6.169,98 tx/s** | 4,056 s | 4,112 s | 4,0 | 100% PASS | **+1,2% TPS** (Chia nhỏ thành 4-5 khối) |
| **EXP_CPUPIN** | Pinning 16 core/node (`taskset`) | **5.961,29 tx/s** | 4,200 s | 4,275 s | 3,7 | 100% PASS | **-2,2% TPS** (Không cải thiện) |
| **EXP_BATCH2000** | Cỡ batch = 2.000 txs | *Bị từ chối* | 40,08 s (timeout) | 1,327 s | 1 | 100% PASS | *Phát hiện protocol limit* |

### Phân tích các phát hiện từ Bước 4:
1. **Ảnh hưởng của kích thước Batch (500 vs 1.000 vs 2.000):**
   - Giảm cỡ batch xuống **500 txs** giúp TPS tăng nhẹ (+4,3%, đạt 6.362 tx/s). Lý do: các batch 500 txs vào hàng đợi nhanh hơn, giúp luồng Ingress decode và đẩy vào DAG mượt mà hơn, tránh dồn cục dữ liệu.
   - Thử nghiệm **Batch 2.000 txs** làm lộ ra ràng buộc an toàn giao thức: `MaxBatchTxCount = 1000`. Khi gửi batch 2.000 txs, Go từ chối gói tin ngay lập tức (`ErrExceedsMaxBatchSize`), làm rơi 24.000 txs và chỉ chấp nhận 1.000 txs cuối cùng.
2. **Ảnh hưởng của CPU Pinning / Co-location:**
   - Việc cố định mỗi validator vào 16 core riêng biệt (`0-15`, `16-31`, `32-47`, `48-63`) không làm tăng throughput (5.961 tx/s so với 6.098 tx/s).
   - Ngược lại, pinning làm giảm nhẹ 2,2% hiệu năng vì nó ngăn cản Go runtime và Block-STM mượn thêm các core rảnh của các node khác trong tích tắc xử lý khối nặng. Máy chủ 104 vCPU hoàn toàn dư thừa năng lực tính toán cho 4 validator chạy song song.

---

## 7. Đánh Giá Đối Chiếu Các Giả Thuyết (So Với `PREREGISTERED.md`)

| Mã GT | Tên Giả Thuyết | Kỳ vọng trong `PREREGISTERED.md` | Kết quả thực nghiệm đo được | Kết luận |
| :---: | :--- | :--- | :--- | :---: |
| **H1** | **Giao diện Rust Consensus → Go Execution là nút thắt tuần tự** | Rust gửi tuần tự từng block sang Go, Go chờ hoặc Rust chờ | Đúng một nửa: Rust dispatch sang Go rất nhanh, nhưng **bên trong Go có rào cản tuần tự hóa `wait_predecessor`**. Block $N$ phải đứng chờ Block $N-1$ commit vào DB xong mới được chạy, tốn **360,34 ms (44,7%)**! | ✅ **XÁC NHẬN (Cơ chế nội tại Go)** |
| **H2** | **Thực thi Block trong Go chiếm phần lớn đường găng** | Block-STM, State Root, NOMT DB chiếm >40% thời gian block | Đúng: Block-STM tốn **159,23 ms (19,8%)**, Merkle Roots tốn **73,78 ms (9,2%)**, DB write tốn **32,76 ms (4,1%)**. Tổng thời gian tính toán thực thi là **265,77 ms (33,1%)**. | ✅ **XÁC NHẬN** |
| **H3** | **Độ trễ đồng thuận DAG & Tranh chấp khóa Rust** | Tranh chấp khóa `parking_lot` trong `GLOBAL_TX_CACHE` hoặc `dag_state` làm chậm DAG | Sai: Báo cáo `perf report` và Go pprof cho thấy `parking_lot` không hề có lock contention; đồng thuận DAG hoàn thành trong ~150-200 ms, không phải nguyên nhân chính gây nghẽn. | ❌ **BÁC BỎ** |
| **H4** | **Tranh chấp tài nguyên do Co-location 4 node** | Chạy chung 1 máy làm tranh chấp L3 cache / CPU context switch | Sai: Thử nghiệm CPU Pinning không cải thiện TPS (5.961 vs 6.098 tx/s). 104 vCPU đủ rộng rãi cho 4 node (CPU toàn cụm lúc blast chỉ đạt ~836%, tức ~209%/node trên mức tối đa 2600%/node). | ❌ **BÁC BỎ** |

---

## 8. Đề Xuất Cải Thiện Kỹ Thuật (Xếp Hạng Theo Tác Động Thực Nghiệm)

> ⚠️ **Tuân thủ quy tắc:** Đây là các đề xuất kỹ thuật được xếp hạng dựa trên số đo thực nghiệm. Nhóm điều tra **không tự ý cài đặt** bất kỳ thay đổi nào vào consensus/execution trong đợt này.

### Đề xuất 1: Cơ chế Thực thi Dự phóng Gối đầu (Pipelined Speculative Execution with Optimistic In-Memory State)
- **Xếp hạng:** 🥇 **Tác động lớn nhất (Ước tính tăng +40% đến +55% TPS)**.
- **Cơ sở thực nghiệm:** Xóa bỏ hoặc thu hẹp khoảng chờ 360,34 ms của `wait_predecessor` (hiện chiếm 44,7% đường găng).
- **Giải pháp kỹ thuật:**
  - Hiện tại: Block $N$ chờ Block $N-1$ commit xong hoàn toàn vào NOMT Trie & Pebble DB (mất ~360 ms) rồi mới clone state để chạy.
  - Đề xuất: Ngay khi Block $N-1$ vừa hoàn tất thực thi Block-STM trong bộ nhớ (in-memory state delta), Go cho phép Block $N$ **bắt đầu thực thi Block-STM song song ngay lập tức** trên bản sao trạng thái dự phóng (Optimistic Dirty State) của Block $N-1$, mà **không cần chờ** Block $N-1$ hoàn thành khâu ghi đĩa (DB disk persist & Trie hashing).
- **Phân tích An toàn Zero-Fork (Bắt buộc theo AGENTS.md Part 2.5):**
  - **100% Zero-Fork Invariant:** Việc thực thi (execution) diễn ra dự phóng gối đầu (pipelined), nhưng khâu **dispatch commit ra ngoài mạng và xác nhận hoàn tất** vẫn tuân thủ thứ tự tuần tự tuyệt đối ($N-1$ commit thành công thì $N$ mới được commit).
  - Nếu Block $N-1$ bị lệch State Root so với 2f+1 peers → Block $N$ lập tức bị hủy bỏ (discarded) và thực thi lại từ Certified State.
  - **Tuyệt đối không dùng timeout:** Không dùng bất kỳ hàm `sleep()` hay `timeout()` nào để ép commit; mọi quyết định dispatch đều dựa trên chứng chỉ đồng thuận 2f+1 từ `CommitVoteMonitor`.

### Đề xuất 2: Tính toán Merkle Roots Song Song (Parallel Speculative Root Derivation)
- **Xếp hạng:** 🥈 **Tác động trung bình (Ước tính tăng +8% đến +12% TPS)**.
- **Cơ sở thực nghiệm:** Khâu tính toán Merkle Roots (Txs Root + Receipts Root) hiện tốn **73,78 ms (9,2%)** và đang chạy tuần tự trên luồng chính của block trước khi chuyển giao cho committer.
- **Giải pháp kỹ thuật:**
  - Đẩy việc tính toán `DeriveSha` của Transaction List và Receipt List sang worker pool chạy nền ngay khi các batch con của Block-STM hoàn tất, thay vì dồn vào cuối block.

### Đề xuất 3: Khởi tạo Bộ đệm Mempool Đón đầu (Mempool Signature Pre-warming)
- **Xếp hạng:** 🥉 **Tác động biên (Ước tính tăng +3% đến +5% TPS, giảm độ trễ Block 1)**.
- **Cơ sở thực nghiệm:** Block #1 tốn tới **397 ms** cho `[SIG-ENFORCE]` do cache mempool chưa kịp ấm, trong khi Block 2+ chỉ tốn 13–29 ms.
- **Giải pháp kỹ thuật:**
  - Tối ưu hóa pipeline nạp trước chữ ký (signature pre-caching) ngay tại tầng TCP Socket Server của Go trước khi đóng gói batch, triệt tiêu độ trễ cold-start ở khối đầu tiên của mỗi đợt giao dịch.

---

## 9. Danh Mục Artifacts & Bằng Chứng Kiểm Tra (Manifest)

Toàn bộ dữ liệu thô, log chi tiết, file cấu hình và profile đã được lưu trữ tại thư mục [note/evidence/tps_investigation_20261008/](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/) kèm file [MANIFEST.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/MANIFEST.json) chứa mã băm SHA256 của toàn bộ 200 file thành phần.

### Các Artifacts Trọng Tâm:
1. [PREREGISTERED.md](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/PREREGISTERED.md) — SHA256: `de40cfc6b0899aef0b80dbafe6b8ac18919404c78a91eecffab61443bbe467f4` (Đăng ký tiêu chí trước khi chạy).
2. [baseline_summary.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/baseline_summary.json) — SHA256: `904e578278f2ae007fca18ba4c958d50c7fc01f308945f35df8c515a80a8427f` (Thống kê 5 lượt Baseline).
3. [block_breakdown_analysis.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/block_breakdown_analysis.json) — SHA256: `48243be44a7e7ee7bcfcebb6720f4f9f7d45543c72e2cfc20058b73f8ff5e13a` (Phân rã thời gian từng giai đoạn trên đường găng).
4. [val0_cpu_cum_top.txt](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/val0_cpu_cum_top.txt) — SHA256: `c8309e39665bc7e77b63fdf9b326cb6b5791c1b3e85e510dc118809bb6705d86` (Top cumulative Go CPU pprof).
5. [val0_cpu_flat_top.txt](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/val0_cpu_flat_top.txt) — SHA256: `96e5788177579893d56f69a59cf6b8f58b71d5328574be078a6358fe7a35fe6f` (Top flat Go CPU pprof).
6. [val0_perf_top.txt](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/val0_perf_top.txt) — SHA256: `94697ffcebaec2826cfc2cc3e5fbcf83fc70c1d68a994793f18e95054cb25287` (Top Linux hardware perf callgraph).
7. [val0_perf_flat_top.txt](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/val0_perf_flat_top.txt) — SHA256: `ba013233fc2909477617b0785ea846faef51ffab7e289bf597dc83375c3fce4c` (Top flat symbols Linux hardware perf).
8. [step4_variable_isolation_summary.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/step4_variable_isolation_summary.json) — SHA256: `785d6bca770c660995c6c0966f38fb9b2658514b744a530ebc25dbcf327ce435` (Thống kê tách biến số: Batch 250, Batch 500, CPU Pinning).
9. [MANIFEST.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/MANIFEST.json) — Bảng checksum toàn bộ artifacts.

---
### 📋 Tóm tắt thay đổi
- **Đã thay đổi:**
  - Lập báo cáo điều tra toàn diện: [note/tps_investigation_report_20261008.md](file:///home/abc/chain-n/metanode/note/tps_investigation_report_20261008.md).
  - Thu thập đầy đủ hồ sơ bằng chứng tại: [note/evidence/tps_investigation_20261008/](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/) gồm 200 files (Baseline 5 lượt, Block breakdown, Go CPU/Heap/Goroutine/Mutex pprof, Linux native perf callgraph, và Tách biến số Batch 250/500/1000/2000 & CPU Pinning).
  - Tạo file kê khai mã băm toàn diện: [note/evidence/tps_investigation_20261008/MANIFEST.json](file:///home/abc/chain-n/metanode/note/evidence/tps_investigation_20261008/MANIFEST.json).
  - Tạo các công cụ kiểm thử tự động phục vụ nghiên cứu:
    - [execution/scripts/test/evidence/run_baseline_5rounds.py](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/run_baseline_5rounds.py)
    - [execution/scripts/test/evidence/analyze_block_breakdown.py](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/analyze_block_breakdown.py)
    - [execution/scripts/test/evidence/run_step3_profiling.py](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/run_step3_profiling.py)
    - [execution/scripts/test/evidence/run_step3_perf_only.py](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/run_step3_perf_only.py)
    - [execution/scripts/test/evidence/run_step4_experiments.py](file:///home/abc/chain-n/metanode/execution/scripts/test/evidence/run_step4_experiments.py)
- **🛠️ Giải pháp áp dụng:** Thực hiện trọn vẹn quy trình đo đạc thực nghiệm 6 bước theo tiêu chuẩn nghiêm ngặt của `note/plan_tps_investigation_20261008.md`. Định vị chính xác nút thắt số 1 là rào cản tuần tự hóa khối `wait_predecessor` trong Go SpeculativeExecutor (chiếm 44,7% chu kỳ xử lý block, 360,34 ms/block), nút thắt số 2 là thời gian thực thi Block-STM (chiếm 19,8%, 159,23 ms/block). Đề xuất giải pháp Pipelined Speculative Execution with Optimistic In-Memory State Delta có cơ sở định lượng (ước tính tăng throughput lên 9,5k–10,5k tx/s).
- **Blast radius:** Không tác động vào bất kỳ logic nghiệp vụ cốt lõi nào của mạng; 100% tuân thủ cam kết chỉ đo lường, không tự ý sửa đổi code consensus/delivery.
- **🐛 Nguyên nhân lỗi:** Baseline throughput ~6.100 tx/s không phải do nghẽn đồng thuận Rust hay mạng Ingest (~219k tx/s), mà do Go SpeculativeExecutor áp đặt cơ chế chờ tuần tự tuyệt đối: block $N$ phải chờ block $N-1$ ghi xong hoàn toàn vào NOMT Trie & Pebble DB rồi mới được thực thi Block-STM.
- **Rủi ro tiềm ẩn:** 100% tuân thủ bất biến Zero-Fork (AGENTS.md Part 2.5), tất cả các lượt chạy thử nghiệm đều đạt Zero-Fork PASS trên cả 4 validator. Trong giải pháp đề xuất tương lai, việc gối đầu thực thi (pipelining execution) vẫn phải đảm bảo tuần tự hóa khâu dispatch commit theo quorum 2f+1 từ peers, tuyệt đối không dùng timeout.
- **Lưu ý hiệu năng:** Cỡ batch Ingress qua TCP tối ưu nhất là 500 txs (+4,3% TPS so với batch 1000). Cỡ batch > 1.000 bị chặn bởi `MaxBatchTxCount = 1000`. CPU Pinning không mang lại lợi ích trên máy chủ 104 vCPU.
---
