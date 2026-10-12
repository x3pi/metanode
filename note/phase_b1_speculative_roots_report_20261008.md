# BÁO CÁO THỰC NGHIỆM GIAI ĐOẠN B1: PARALLEL SPECULATIVE ROOT DERIVATION

**Ngày thực hiện:** 08/10/2026  
**Mục tiêu:** Cắt giảm thời gian tính toán Merkle Roots (txsRoot và receiptsRoot) trên luồng committer bằng cách thực hiện tính toán song song ngay trong goroutine đầu cơ của `SpeculativeExecutor`.  
**Cam kết:** 100% Zero-Fork Invariant, đo kiểm thực tế không giả mạo, test parity bit-for-bit.  

---

## 1. THIẾT KẾ GIẢI PHÁP KỸ THUẬT (B1)

### 1.1 Điểm Nghẽn Cũ
- Trước B1, hàm `createBlockFromResults` của `BlockProcessor` giữ lock độc quyền `blockWriteMutex` và tuần tự tính toán:
  1. Vòng lặp forensic tính hàng ngàn phép băm Keccak256 đồng bộ dù cờ debug tắt (~15–20 ms).
  2. `calculateReceiptsRoot`: Duyệt in-memory trie receipts và tính root (~5–10 ms).
  3. `txDB.AddTransactions` + `IntermediateRoot()`: Duyệt in-memory trie transactions và tính root (~40–60 ms).
- Tổng thời gian Phase 1: **~50–70 ms / block** nằm trực tiếp trên critical path của luồng Committer, chặn mọi block tiếp theo tiến triển.

### 1.2 Giải Pháp Kỹ Thuật Đã Triển Khai
1. **Parallel Speculative Root Derivation:**
   - Khi goroutine đầu cơ hoàn thành `tx_processor.ProcessTransactions`, danh sách `Transactions` (đã qua signature filter) và `Receipts` đã được chốt 100%.
   - Goroutine đầu cơ lập tức kích hoạt `PrecomputeRoots` trong background thông qua channel `precomputeRootsChan chan *PrecomputedBlockRoots`.
   - `PrecomputeRoots` tính toán `txDB.IntermediateRoot()` và `calculateReceiptsRoot` song song trên 2 worker threads độc lập.
2. **Zero Overhead on Committer Critical Path:**
   - Khi Committer nhận speculative block, kết quả roots đã sẵn sàng (`precomputedRoots = <-res.precomputeRootsChan`).
   - `createBlockFromResults` nhận `precomputedRoots` và đi thẳng vào Phase 2 mà không tốn thêm bất kỳ chu kỳ CPU nào trên luồng commit.
3. **Forensic Hashing Guard:**
   - Thêm `logger.IsDebugEnabled()`. Toàn bộ vòng lặp băm Keccak256 forensic chỉ chạy khi cờ DEBUG được bật, giảm 100% overhead khi chạy production.
4. **Fallback An Toàn & Bảo Toàn Zero-Fork:**
   - Nếu block chưa có precomputed roots (ví dụ sync block), `createBlockFromResults` tự động fallback tính inline tuần tự như cũ.
   - Thuật toán băm và cấu trúc trie hoàn toàn giống hệt 100% giữa hai luồng.

---

## 2. KẾT QUẢ TEST PARITY BIT-FOR-BIT

- Đã viết unit test `execution/cmd/simple_chain/processor/block_processor_roots_test.go`:
  - `TestPrecomputeRoots_BitForBitParity`: Tạo tập mock transactions và receipts, tính roots qua cả 2 cơ chế (precomputed và inline).
  - Kết quả: `TxsRoot` và `ReceiptsRoot` khớp **100% BIT-FOR-BIT** (`assert.Equal`).
  - Unit test pass 100% (`go test -v ./cmd/simple_chain/processor` PASS toàn bộ suite).
  - Biên dịch hệ thống: `build_check.sh` **5/5 PASS** sạch sẽ không lỗi hay warning (EVM FFI, Consensus Metanode, Rust NOMT FFI, Go simple_chain, Go packages).

---

## 3. SỐ LIỆU ĐO KIỂM CHỨNG THỰC TẾ (BENCHMARK 5 LƯỢT ĐỘC LẬP)

- Cụm test: 4 Validator Nodes (cổng 31xxx) deploy từ template sạch `/tmp/gate_4val_clean_template`.
- Giao thức đo: Workload A1 lặp lại (60 giây sustained blast liên tục, batch size 1.000, rate unlimited).
- Cờ kiểm tra: Bắt buộc `-verify-parity` sau mỗi lượt trên cả 4 node.

### 3.1 Bảng Số Liệu Chi Tiết 5 Lượt Đo B1

| Lượt | Confirmed TXs | Blocks | Effective TPS | Zero-Fork Verified | Node Roots Consistent | Avg $T_a$ | Avg $T_b$ | Cycle Time |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Run 1** | 827.898 | 167 | 13.794,5 tx/s | **PASS (True)** | **PASS (True)** | 148,8 ms | 40,7 ms | 1.103,1 ms |
| **Run 2** | 835.633 | 168 | 13.923,7 tx/s | **PASS (True)** | **PASS (True)** | 142,6 ms | 38,7 ms | 1.080,5 ms |
| **Run 3** | 810.773 | 163 | 13.509,6 tx/s | **PASS (True)** | **PASS (True)** | 151,3 ms | 39,7 ms | 1.060,8 ms |
| **Run 4** | 805.713 | 162 | 13.425,5 tx/s | **PASS (True)** | **PASS (True)** | 146,7 ms | 38,5 ms | 1.103,0 ms |
| **Run 5** | 794.834 | 160 | 13.244,3 tx/s | **PASS (True)** | **PASS (True)** | 152,9 ms | 44,2 ms | 1.135,4 ms |
| **Mean** | **814.970** | **164** | **13.579,5 tx/s** | **100% PASS** | **100% PASS** | **148,5 ms** | **40,4 ms** | **1.096,6 ms** |
| **SD** | — | — | **276,4 tx/s** | — | — | — | — | — |

---

### 3.2 So Sánh Với Baseline Giai Đoạn A1

| Chỉ Số | Baseline A1 (Trước B1) | Đo Được B1 (Sau B1) | Chênh Lệch Tuyệt Đối | % Thay Đổi |
| :--- | :---: | :---: | :---: | :---: |
| **Mean TPS** | **10.898,0 tx/s** | **13.579,5 tx/s** | **+2.681,5 tx/s** | **+24,61%** |
| **Độ lệch chuẩn (SD)** | 391,5 tx/s | 276,4 tx/s | -115,1 tx/s | Độ ổn định cao hơn |
| **Min TPS** | 10.373,1 tx/s | 13.244,3 tx/s | +2.871,2 tx/s | +27,68% |
| **Max TPS** | 11.415,2 tx/s | 13.923,7 tx/s | +2.508,5 tx/s | +21,97% |
| **Computation $T_a$** | 168,3 ms / block | 148,5 ms / block | -19,8 ms / block | Cắt giảm ~20 ms |
| **Zero-Fork Status** | 100% PASS (5/5) | 100% PASS (5/5) | Giữ nguyên | Tuyệt đối an toàn |

### 3.3 Kiểm Định Thống Kê Welch's t-test
- $t = 12,511$
- Bậc tự do $df = 7,2$
- Giá trị $p < 0,0001$ ($p \ll 0,05$).
- **Kết luận:** Mức tăng trưởng +24,61% TPS là hoàn toàn có ý nghĩa thống kê thực tế, không phải do biến thiên ngẫu nhiên hay nhiễu môi trường.

---

## 4. QUẢN LÝ BẰNG CHỨNG (EVIDENCE MANIFEST)

- Toàn bộ 161 file bằng chứng mới (gồm file tổng hợp `b1_speculative_roots_summary.json`, 5 report JSON, 5 blast logs, 5 thư mục logs của 4 validator) đã được tính hash SHA256 và cập nhật vào `note/evidence/tps_improvement_20261008/MANIFEST.json`.
- Tổng số mục kiểm định trong MANIFEST hiện tại: **1.475 files**.
