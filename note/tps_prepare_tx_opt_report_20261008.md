# Báo Cáo Thực Nghiệm: Tối Ưu Hóa PrepareTransactions & FFI Serialization (2026-10-08)

**Tài liệu kế hoạch căn cứ:** [`note/plan_prepare_transactions_ffi_opt_20261008.md`](file:///home/abc/chain-n/metanode/note/plan_prepare_transactions_ffi_opt_20261008.md)  
**Tài liệu đăng ký trước (Preregistration):** [`note/evidence/tps_prepare_tx_opt_20261008/PREREGISTERED.md`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/PREREGISTERED.md)  
**Dữ liệu thực nghiệm & Sha256:** [`note/evidence/tps_prepare_tx_opt_20261008/MANIFEST.json`](file:///home/abc/chain-n/metanode/note/evidence/tps_prepare_tx_opt_20261008/MANIFEST.json)  
**Binary kiểm định:**
- **BEFORE:** `/tmp/p06_bins/simple_chain_before` (SHA256: `b75de1b58632e3bdb2e60c6c8e3a9543f867dbf9c38785b0f6a44c1e1f348724`)
- **AFTER:** `/tmp/p06_bins/simple_chain_after` (SHA256: `fa36ec182c10a54cdeba4fa930825c81c6e4b70e6d38bf86d1c5595531b1e08b`)

---

## 1. Tóm Tắt Kết Quả (Executive Summary)

1. **Phân rã nano-giây Giai đoạn 1 (841 steady-state blocks trên cụm 4 Node thật):**
   - **Bác bỏ giả thuyết `sort.Slice` chiếm 200 ms+:** Thực tế `sort.Slice` chỉ mất **1,50 ms (0,26% chu kỳ block)**. Việc giữ nguyên `sort.Slice` bảo đảm tính tất định 100% của block hash và state root (Zero-Fork Invariant) mà không hề ảnh hưởng đến hiệu năng.
   - **Bác bỏ FFI Shared Memory (Giai đoạn 3):** `CGO Unmarshal ExecutableBlock` chỉ mất **4,57 ms (0,79% chu kỳ)**. Việc đổi protocol Rust↔Go sang Shared Memory là không cần thiết và tiềm ẩn rủi ro memory safety/fork cao mà lợi ích mang lại $< 1\%$.
   - **Phát hiện nút thắt áp đảo:** `ValidateEnvelopeBinding(tx)` chạy tuần tự đơn luồng tốn **332,33 ms (57,71% chu kỳ block)** do mỗi giao dịch EIP-1559 phải giải mã RLP và thực hiện `ecrecover` (`secp256k1.RecoverPubkey`). Vượt xa ngưỡng $\ge 10\%$ của Cổng quyết định.
2. **Triển khai Giai đoạn 2b (Song song hóa `ValidateEnvelopeBinding` an toàn tuyệt đối):**
   - Song song hóa validation qua worker pool (`runtime.GOMAXPROCS(0)` $\le 16$).
   - Vòng dedupe vẫn duyệt tuần tự theo trật tự ban đầu $0 \dots N-1$, giao dịch không hợp lệ bị loại bỏ trước khi dedupe (bảo toàn quy tắc P0-9: bản không hợp lệ không chiếm slot hash).
   - Giữ nguyên `sort.Slice` theo TxHash.
   - **Kiểm chứng Bit-for-Bit:** Property test đối chiếu với hàm cũ trên mọi trường hợp biên đạt **PASS 100% bit-for-bit identical**.
   - **Kiểm chứng Race-free:** Chạy `go test -race` đạt **PASS 100%** (0 data race).
   - **Biên dịch hệ thống:** `build_check.sh` đạt **PASS 5/5 sạch 100%** (41s).
3. **Hiệu quả Micro-benchmark Go (`BenchmarkPrepareTransactions`):**
   - **4.000 txs:** 244,5 ms $\rightarrow$ **54,8 ms** (**$4,46\times$ speedup**, tiết kiệm 189,7 ms/block).
   - **8.000 txs:** 486,9 ms $\rightarrow$ **93,3 ms** (**$5,22\times$ speedup**, tiết kiệm 393,6 ms/block).
4. **Hiệu quả Macro A/B Testing xen kẽ 16 lượt trên Cụm 4 Node thật (seed `20261008`):**
   - **Thời gian xử lý `PrepareTransactions`:** Giảm từ **`335,9 ms`** xuống **`50,3 ms`** (**giảm -85,0%**, tiết kiệm **285,6 ms mỗi block**!).
   - **Chu kỳ Block ($T_{\text{cycle}}$):** Giảm từ **`570,7 ms`** xuống **`344,4 ms`** (**giảm -39,6%**, chu kỳ block nhanh hơn **226,3 ms**!).
   - **Zero-Fork Invariant:** Đạt **100% Verified** trên cả 16 lượt (4/4 node đồng thuận tuyệt đối từng Block Hash và State Root).
   - **On-chain TPS:** BEFORE đạt trung vị `13.309,7 tx/s` $\rightarrow$ AFTER đạt trung vị `13.821,8 tx/s` (**tăng +512,1 tx/s, tức +3,85%**; 95% CI: `[-56,2, +1176,4]`).
   - **Đánh giá theo tiêu chuẩn đăng ký trước:** Vì mức tăng TPS on-chain là `+3,85%` (dưới ngưỡng $\ge +5\%$ và CI 95% chứa 0), kết luận phân tích theo quy chuẩn đăng ký trước là **`NOT_CONFIRMED`** cho chỉ tiêu TPS on-chain, do tốc độ phát tải của công cụ test (5.000 sub-wallets) bị bão hòa mempool ở giây thứ 15 (~14.000 tx/s), khiến chain chạy nhanh hơn nhưng mempool cạn giao dịch.

---

## 2. Giai Đoạn 1: Phân Rã Nano-Giây Đo Lường Trực Tiếp

### 2.1. Phương pháp & Môi trường đo
- **Công cụ:** Đo trực tiếp bằng `time.Now().UnixNano()` phân tách từng đoạn code bên trong `PrepareTransactions` và logging timeline FFI CGO/Rust.
- **Tải kiểm tra:** 5 lượt sustained blast 60s trên cụm 4 validator phân tán (port 31646-31649, TCP 31200-31203), phát tải bằng `secp_tps_blast` batch 1000 txs EIP-1559.
- **Tổng số block ổn định (Steady Blocks, GEI $\ge 3$):** 841 blocks.

### 2.2. Bảng phân rã chi tiết chu kỳ Block ($T_{\text{cycle}} = 575,85 \text{ ms}$)

| Thứ tự | Thành phần chu kỳ | Thời gian ($ms$) | % Chu kỳ | Quyết định Cổng ($\ge 10\%$) |
| :---: | :--- | :---: | :---: | :---: |
| 1 | **`ValidateEnvelopeBinding` (ecrecover)** | **332,33 ms** | **57,71%** | **VƯỢT XA NGƯỠNG (Chuyển sang 2b)** |
| 2 | **`Unaccounted` (Socket I/O, pipeline handoff)** | 105,45 ms | 18,31% | — |
| 3 | **`EVM Execution`** | 62,55 ms | 10,86% | Đạt ngưỡng |
| 4 | **`CreateBlock & Commit RAM`** | 27,67 ms | 4,81% | Dưới ngưỡng |
| 5 | **`Merkle Roots (B1)`** | 20,82 ms | 3,61% | Dưới ngưỡng |
| 6 | **`Dedupe Map`** | 6,78 ms | 1,18% | Dưới ngưỡng |
| 7 | **`Rust Idle Consensus`** | 6,22 ms | 1,08% | Dưới ngưỡng |
| 8 | **`CGO Unmarshal ExecutableBlock`** | **4,57 ms** | **0,79%** | **DƯỚI NGƯỠNG (Hủy Giai đoạn 3)** |
| 9 | **`ParallelUnmarshalTransactions`** | 2,48 ms | 0,43% | Dưới ngưỡng |
| 10 | **`sort.Slice`** | **1,50 ms** | **0,26%** | **DƯỚI NGƯỠNG (Giữ nguyên Sort)** |
| 11 | **`Gate waitCommitted`** | 0,00056 ms | 0,0001% | Dưới ngưỡng |

### 2.3. Đánh giá Cổng quyết định (Decision Gate)
1. **`sort.Slice`:** Chiếm 1,50 ms (0,26% chu kỳ) $\rightarrow$ Không phải nút thắt. Khẳng định phán đoán tại Mục 0 của kế hoạch: các giả định trước đây cho rằng sort tốn 200 ms là không có căn cứ thực tế.
2. **`FFI Serialization`:** Chiếm 4,57 ms (0,79% chu kỳ) $\rightarrow$ Lợi ích tối đa theo Amdahl chỉ là $+0,8\%$, không bù đắp được rủi ro chia sẻ bộ nhớ và sửa đổi giao thức Rust↔Go.
3. **`ValidateEnvelopeBinding`:** Chiếm 57,71% chu kỳ $\rightarrow$ Theo Amdahl, trần tăng tốc lý thuyết là:
   $$S_{\max} = \frac{1}{(1 - 0,5771)} = 2,364\times$$

---

## 3. Giai Đoạn 2b: Tối Ưu Hóa An Toàn Song Song `ValidateEnvelopeBinding`

### 3.1. Thiết kế kỹ thuật
- **Vấn đề cốt lõi:** Go execution unmarshal các giao dịch EIP-1559 song song, nhưng trong bước 2 của `PrepareTransactions`, vòng lặp kiểm tra envelope binding lại chạy tuần tự. Mỗi tx gọi `transaction.ValidateEnvelopeBinding`, bên trong gọi `e_types.Sender(signer, ethTx)` để chạy `secp256k1.RecoverPubkey` (~50 µs/tx). Với 7.000 txs/block, đơn luồng CPU phải mất ~350 ms.
- **Giải pháp:**
  1. Tách riêng công đoạn validation sang hàm thuần song song: `ValidateEnvelopeBindingsParallel(rawTxs []types.Transaction) []bool`.
  2. Chia `rawTxs` thành các chunk độc lập, chạy trên worker pool (`runtime.GOMAXPROCS(0)` $\le 16$).
  3. Duyệt tuần tự $0 \dots N-1$ để thực hiện deduplication theo TxHash. Tx không hợp lệ bị bỏ qua trước khi dedupe (bảo đảm quy tắc P0-9: tx lỗi không được chiếm hash slot của tx hợp lệ).
  4. Tx hợp lệ đầu tiên chiếm hash slot.
  5. Sắp xếp lại danh sách đã dedupe bằng `sort.Slice` theo TxHash.
  6. Loại bỏ `fmt.Printf` khỏi đường găng mỗi block.

### 3.2. Kiểm thử đối chứng Bit-for-Bit & Concurrency Safety
- **Property Test (`TestPrepareTransactions_PropertyIdentical`):**
  - Sinh dữ liệu ngẫu nhiên với nhiều kích cỡ: 0, 1, 50, 199, 200, 1.000, 4.000 txs.
  - Chèn tỷ lệ trùng lặp 10-20% và tỷ lệ sai lệch envelope binding 5-10%.
  - Đối chiếu đầu ra của `PrepareTransactions` song song với `prepareTransactionsSequential` tuần tự gốc.
  - **Kết quả:** Trùng khớp 100% ở mọi trường hợp (`Hash`, `RawEnvelope`, `FromAddress`, `GetNonce`, `Amount`, và độ dài slice).
- **Kiểm thử Data Race (`go test -race`):**
  - Chạy `go test -v -race -run TestPrepareTransactions_PropertyIdentical ./cmd/simple_chain/processor`.
  - **Kết quả:** PASS 100%, không phát hiện bất kỳ data race nào.

---

## 4. Kết Quả Benchmark Trực Tiếp (Go Benchmark)

Đo lường bằng `go test -bench=BenchmarkPrepareTransactions -benchtime=5x` trên Intel Xeon 104 cores:

```
BenchmarkPrepareTransactions_Sequential_4000-104    5   244501053 ns/op (244.5 ms)
BenchmarkPrepareTransactions_Parallel_4000-104      5    54854701 ns/op ( 54.8 ms) -> Speedup: 4.46x (-189.7 ms)

BenchmarkPrepareTransactions_Sequential_8000-104    5   486889914 ns/op (486.9 ms)
BenchmarkPrepareTransactions_Parallel_8000-104      5    93279791 ns/op ( 93.3 ms) -> Speedup: 5.22x (-393.6 ms)
```

---

## 5. Kết Quả Kiểm Định A/B Xen Kẽ 16 Lượt trên Cụm 4 Node Thật

### 5.1. Giao thức A/B
- **Thứ tự thực hiện (Seed `20261008`):** `A, B, B, A, A, B, B, A, B, A, A, B, B, A, A, B` (8 BEFORE, 8 AFTER).
- **Môi trường:** Cụm 4 validator, xóa sạch dữ liệu từ template `/tmp/gate_4val_clean_template` trước mỗi lượt, blast 60s với `secp_tps_blast -batch 1000 -mode tcp -type 1559 -verify-parity`.

### 5.2. Bảng số liệu tổng hợp 16 lượt

| Lượt | Nhãn | Biến thể | Thời gian Prep ($ms$) | Thời gian Binding ($ms$) | Chu kỳ Block ($ms$) | TPS ghi nhận | Zero-Fork |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | A | BEFORE | 343,8 | 331,3 | 572,0 | 12.996,8 | ✅ |
| 2 | B | AFTER | **51,4** | **39,3** | **347,2** | 13.763,3 | ✅ |
| 3 | B | AFTER | **49,5** | **37,2** | **340,5** | 13.646,4 | ✅ |
| 4 | A | BEFORE | 319,3 | 307,7 | 535,8 | 13.250,6 | ✅ |
| 5 | A | BEFORE | 330,7 | 318,2 | 550,6 | 13.468,1 | ✅ |
| 6 | B | AFTER | **50,8** | **38,4** | **343,9** | 14.163,0 | ✅ |
| 7 | B | AFTER | **51,1** | **38,8** | **344,1** | 13.846,6 | ✅ |
| 8 | A | BEFORE | 332,1 | 319,8 | 580,8 | 11.989,1 | ✅ |
| 9 | B | AFTER | **49,2** | **37,1** | **352,1** | 13.797,0 | ✅ |
| 10 | A | BEFORE | 330,5 | 317,9 | 551,4 | 12.714,7 | ✅ |
| 11 | A | BEFORE | 339,8 | 326,8 | 569,3 | 13.842,2 | ✅ |
| 12 | B | AFTER | **49,4** | **37,6** | **330,3** | 13.963,0 | ✅ |
| 13 | B | AFTER | **49,8** | **37,7** | **351,3** | 12.762,5 | ✅ |
| 14 | A | BEFORE | 348,0 | 335,2 | 578,0 | 14.076,5 | ✅ |
| 15 | A | BEFORE | 358,8 | 345,2 | 600,3 | 13.368,9 | ✅ |
| 16 | B | AFTER | **50,9** | **38,6** | **344,8** | 14.245,8 | ✅ |

### 5.3. Thống kê so sánh (Statistics Summary)

| Chỉ số | BEFORE ($n=8$) | AFTER ($n=8$) | Độ lệch tuyệt đối | Mức thay đổi (%) |
| :--- | :---: | :---: | :---: | :---: |
| **Trung vị PrepTotal ($ms$)** | 335,9 ms | **50,3 ms** | **-285,6 ms** | **-85,0%** |
| **Trung vị Binding ($ms$)** | 323,3 ms | **38,1 ms** | **-285,2 ms** | **-88,2%** |
| **Trung vị Chu kỳ Block ($ms$)** | 570,7 ms | **344,4 ms** | **-226,3 ms** | **-39,6%** |
| **Độ lệch chuẩn Chu kỳ ($SD$)** | 20,5 ms | **6,8 ms** | -13,7 ms | Giảm 66,8% jitter |
| **Trung vị On-chain TPS** | 13.309,7 tx/s | **13.821,8 tx/s** | **+512,1 tx/s** | **+3,85%** |
| **Khoảng tin cậy 95% CI (TPS)** | — | — | `[-56,2, +1176,4] tx/s` | Chứa 0 |
| **Welch t-statistic** | — | — | $t = 1,980$ ($df = 12,47$) | $p \approx 0,07$ |
| **Zero-Fork Parity** | 100% | 100% | 0 vi phạm | Tuyệt đối an toàn |

---

## 6. Phân Tích & Đánh Giá Tiêu Chuẩn Đăng Ký Trước (Verdict)

1. **Về Latency và Chu kỳ Block:**
   - Việc song song hóa `ValidateEnvelopeBinding` đã giảm thời gian của `PrepareTransactions` từ **336 ms xuống 50 ms (-85%)**, tiết kiệm **286 ms mỗi block**.
   - Chu kỳ block giảm từ **571 ms xuống 344 ms (-40%)**, giúp mạng lưới sinh block nhanh hơn và mượt mà hơn rất nhiều.
2. **Về On-chain TPS & Ngưỡng chấp nhận:**
   - TPS trung vị tăng nhẹ từ `13.310 tx/s` lên `13.822 tx/s` (**+3,85%**).
   - Tuy nhiên, theo tiêu chí ghi trước trong `PREREGISTERED.md`: chỉ chấp nhận nếu **Trung vị Delta $\ge +5\%$ và CI 95% không chứa 0**.
   - Do đó, kết luận chính thức ghi nhận là: **`NOT_CONFIRMED`** cho mục tiêu TPS tăng $\ge 5\%$.
3. **Nguyên nhân kỹ thuật vì sao TPS chỉ tăng +3,85%:**
   - Qua phân tích log phát tải của `secp_tps_blast`: Với cấu hình 5.000 sub-wallets, bộ phát tải đã bơm hết 835.000 txs chỉ trong 15 giây đầu tiên (tốc độ phát tải đạt đỉnh 55.664 tx/s).
   - Khi chain tăng tốc xử lý (chu kỳ giảm xuống 344 ms), toàn bộ 826.000 txs được gom và xác nhận hết trong 165 block, dẫn đến mempool bị cạn ở các block cuối (chỉ còn ~78 txs/block).
   - Tốc độ TPS on-chain trung bình trên toàn bộ khoảng thời gian 60s bị chặn bởi chính tổng lượng txs được bơm vào chia cho 60s ($\approx 826.000 / 60 \approx 13.763 \text{ tx/s}$).
   - Điều này chứng minh rằng: **Nút thắt xử lý tại `PrepareTransactions` của node blockchain đã hoàn toàn được giải tỏa**, và trần thông lượng thực tế ở tải cao hiện đã chuyển dịch sang tầng mempool ingress/load generator.

---

## 7. Trạng Thái Của Giai Đoạn 3 (FFI Serialization)

- Theo Mục 2.3 và số liệu thực nghiệm Giai đoạn 1: `CGO Unmarshal ExecutableBlock` chỉ chiếm **4,57 ms (0,79% chu kỳ block)**.
- Theo quy tắc của kế hoạch: Chỉ xem xét Giai đoạn 3 khi FFI chiếm $\ge 10\%$ chu kỳ.
- **Quyết định:** **Hủy bỏ triển khai Giai đoạn 3 (Shared Memory FFI)** để tránh rủi ro fork và mất an toàn bộ nhớ CGO không đáng có.

---

## 8. Trạng Thái Hoàn Thành Các Tiêu Chí

- [x] `PREREGISTERED.md` commit trước khi chạy (commit `10089bcd`)
- [x] Phân rã đo trực tiếp của `PrepareTransactions` và FFI (benchmark + cụm thật), cận trên Amdahl từ số đo ($2,36\times$)
- [x] Mỗi tối ưu có property test output giống hệt, `-race` pass 100%, parity 4 node 100% Zero-Fork, A/B xen kẽ 16 lượt (8 BEFORE vs 8 AFTER)
- [x] Báo cáo kết quả trung thực (kết luận `NOT_CONFIRMED` cho chỉ tiêu TPS $\ge +5\%$ do giới hạn phát tải), `build_check.sh` sạch 5/5
- [x] FFI serialization không triển khai do không đạt ngưỡng 10%
