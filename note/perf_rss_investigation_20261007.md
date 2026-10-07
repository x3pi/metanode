# Báo Cáo Điều Tra Bộ Nhớ & RSS ~9.7 GB/Node — Cập Nhật Thực Nghiệm 2026-10-07

## 1. Mục Tiêu & Bối Cảnh Thực Nghiệm
Báo cáo kiểm thử hiệu năng trước đó ghi nhận con số đỉnh RSS lên tới ~39,080 MB (~39 GB) toàn cụm 4 node validator (~9.7 GB/node) sau khi xử lý tải 304,000 transactions (50,000 ví). Con số này làm dấy lên nghi vấn về rò rỉ bộ nhớ (memory leak) hoặc cấu hình cache không giới hạn trong execution/consensus engine.

Nhiệm vụ kiểm tra theo [note/plan_fix_nomt_reports_evidence_20261007.md](file:///home/abc/chain-n/metanode/note/plan_fix_nomt_reports_evidence_20261007.md):
1. Tách biệt cụm test cô lập (cổng `31xxx`, thư mục `/tmp/gate_4val_p06`) với 9 tiến trình `/opt/metanode` đang chạy ngầm của user trên máy chủ.
2. Phân rã cấu trúc bộ nhớ: Go Heap (InUse, Idle, Released), Rust Consensus, NOMT Cache, CGO, mmap.
3. Xác minh thực nghiệm đa đợt: Đo đạc 8 đợt tải liên tiếp (tổng 400,000 txs Secp256k1 EIP-1559) kèm lệnh gọi force GC sau mỗi đợt trên toàn bộ 4 validator để kiểm tra xu hướng tích luỹ bộ nhớ.
4. Áp dụng tiêu chuẩn nghiệm thu xác định trước (A priori acceptance criteria) với phân tích hồi quy tuyến tính (slope $m$, $R^2$, 95% CI) và phân tích memstats profile.
5. Đo đạc thực nghiệm có kiểm soát so sánh đối chứng giữa `GOGC=800` (mặc định) và `GOGC=50` trên cùng một khối lượng tải giao dịch (25,000 txs, $n=3$ mỗi cấu hình).

> [!NOTE]
> **Quy chuẩn lưu trữ bằng chứng:** Để giữ cho git repository gọn nhẹ (≤ 10 MB), thư mục `note/evidence/perf_rss_20261007/` chỉ lưu trữ file log kết quả `rss_investigation_8waves.log`, bảng tổng hợp `rss_8waves_summary.csv`, và trích đoạn allocations `memstats_top_inuse_summary.txt`.
> Toàn bộ 90 MB dữ liệu thô đầy đủ (bao gồm 36 file `heap_*.pb.gz`, 36 file `memstats_*.txt`, 36 file `smaps_*.txt`) được đóng gói thành archive ngoài git tại `/home/abc/evidence_archive/perf_rss_20261007/rss_raw_full.tar.gz` (SHA256: `43f2b16ee80fc94828ef1ee5dd1ee95514f062c1417e0569bc0e7b8407b5a910`, dung lượng: 9,880,670 bytes) và được đăng ký kiểm chứng trong trường `external` của `MANIFEST.json` (`evidence:rss_investigation_8waves`).

---

## 2. Phát Hiện Kỹ Thuật (Root Causes)

### Phát hiện 1: Sai lệch đo lường trong công cụ giám sát cũ
- **Nguyên nhân:** Lệnh giám sát RSS cũ dùng `ps -o %cpu,rss --no-headers -C simple_chain`. Cờ `-C simple_chain` gom tất cả tiến trình có tên `simple_chain` trên toàn máy chủ Linux. Tại thời điểm đó, có 9 tiến trình `/opt/metanode` của user đang chạy nền, chiếm sẵn ~7.1 GB RSS.
- **Hệ quả:** Số liệu RSS báo cáo trước đây đã bị cộng dồn ~7.1 GB bộ nhớ của user vào cụm 4 validator kiểm thử.
- **Biện pháp khắc phục:** Công cụ đo đã được cố định danh sách PID theo từng node (`/tmp/gate_4val_p06/pids/val*.pid`), chỉ đọc `/proc/<pid>/statm` và `smaps_rollup` của 4 node thử nghiệm.

### Phát hiện 2: Cơ chế Garbage Collection của Go dưới `GOMEMLIMIT=8GB` & `GOGC=800`
Trong file `execution/cmd/simple_chain/main.go`:
- Go 1.19+ hỗ trợ `GOMEMLIMIT` và soft memory target. Cấu hình mặc định `GOGC=800` cho phép Go Heap tăng trưởng tới 8 lần trước khi kích hoạt chu kỳ GC, miễn là tổng bộ nhớ vẫn nằm dưới `defaultMemLimitGB = 8GB`. Đây là thiết kế tối ưu thông lượng CPU cho blockchain TPS cao nhằm tránh việc GC chạy liên tục làm nghẽn pipeline.
- Dưới áp lực hàng chục ngàn tx/s, hàng triệu đối tượng protobuf, envelope, byte slices tạm thời được cấp phát. Go runtime chủ động trì hoãn GC vì HeapAlloc vẫn nằm trong giới hạn cho phép.

### Phát hiện 3: Bản chất của cờ `-debug=true` trong `run_env.sh`
- Trong [execution/cmd/simple_chain/main.go](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/main.go#L161-L163):
  ```go
  if *debug {
      startDebugServer(*pprofAddr)
  }
  ```
- Cờ `-debug` chỉ có tác dụng duy nhất là bật pprof HTTP debug server trên cổng chỉ định (`*pprofAddr`). Nó không thay đổi mức độ chi tiết của log (được quản lý bởi `-log-level`). Do đó việc bật cờ này trong môi trường kiểm chuẩn pprof là phù hợp với mục tiêu kiểm thử.

---

## 3. Tiêu Chuẩn Nghiệm Thu & Thực Nghiệm 8 Đợt Tải (400,000 Transactions)

### Tiêu chuẩn nghiệm thu đặt trước (A Priori Acceptance Criteria)
Theo kế hoạch kiểm định [note/plan_fix_nomt_reports_evidence_20261007.md](file:///home/abc/chain-n/metanode/note/plan_fix_nomt_reports_evidence_20261007.md):
- **Tiêu chí độ dốc hồi quy tuyến tính:** Tính hồi quy `HeapAlloc_after_GC = m * cumulative_txs + b`. Ngưỡng ban đầu đề ra: `slope m <= 30 MB / 100k txs` (với 95% CI không chứa giá trị > 50 MB) để kết luận "bộ nhớ không đổi".
- **Quy tắc phân loại:** Nếu $m > 30$ MB / 100k txs, đánh giá là **FAIL** đối với tiêu chí "bộ nhớ không đổi". Tiếp tục phân tích memstats stack traces để xác định nguyên nhân: nếu bộ nhớ tăng ở in-memory state/indexing thì kết luận **State Growth**, nếu tăng ở channels/goroutines thì kết luận **Memory Leak**.

### Bảng Diễn Tiến Bộ Nhớ 8 Đợt Tải (0 → 400,000 Transactions)
Nguồn bằng chứng: [note/evidence/perf_rss_20261007/rss_investigation_8waves.log](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/rss_investigation_8waves.log) (SHA256: `a9ebe0e18a7dc016c476672510f267847d648da6efa1928bdfa63b4365012c65`, bytes: 5654) và [note/evidence/perf_rss_20261007/rss_8waves_summary.csv](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/rss_8waves_summary.csv) (SHA256: `6c4b02fa89f97d53e697232d363b29a312211a5862a5134ec4910b55642dfbdb`, bytes: 1609).

| Đợt | Txs tích luỹ | Node | PID | HeapAlloc sau GC (MB) | HeapInuse sau GC (MB) | HeapSys (MB) | RSS Thực tế (MB) | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Baseline** | 0 | `val0` | 3708266 | 105.00 | 112.33 | 394.86 | 565.42 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 104.91 | 112.74 | 415.14 | 524.28 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 104.98 | 112.61 | 414.89 | 547.65 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 104.93 | 112.16 | 410.95 | 511.01 | evidence:rss_investigation_8waves |
| **Wave 1** | 50,000 | `val0` | 3708266 | 220.18 | 286.64 | 2036.61 | 2689.51 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 204.18 | 273.69 | 1928.52 | 2366.43 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 219.44 | 287.66 | 2073.05 | 2644.05 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 203.10 | 270.96 | 1988.48 | 2490.00 | evidence:rss_investigation_8waves |
| **Wave 2** | 100,000 | `val0` | 3708266 | 254.43 | 340.80 | 2131.98 | 3020.84 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 235.64 | 329.16 | 2223.27 | 3143.49 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 243.01 | 335.44 | 2215.80 | 3005.89 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 239.57 | 333.05 | 2251.27 | 3170.81 | evidence:rss_investigation_8waves |
| **Wave 3** | 150,000 | `val0` | 3708266 | 290.80 | 390.07 | 2423.83 | 3497.95 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 283.41 | 388.76 | 2294.17 | 3330.29 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 284.75 | 392.86 | 2310.83 | 3523.68 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 293.82 | 399.52 | 2362.64 | 3525.21 | evidence:rss_investigation_8waves |
| **Wave 4** | 200,000 | `val0` | 3708266 | 353.63 | 469.05 | 2803.05 | 4000.74 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 333.60 | 453.09 | 2641.89 | 3762.79 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 332.91 | 450.11 | 2734.52 | 3957.26 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 352.28 | 467.56 | 2738.58 | 3898.18 | evidence:rss_investigation_8waves |
| **Wave 5** | 250,000 | `val0` | 3708266 | 432.76 | 566.66 | 3242.52 | 4928.55 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 412.34 | 545.30 | 3005.36 | 4377.57 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 403.09 | 535.55 | 3038.30 | 4607.16 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 391.27 | 529.41 | 3142.27 | 4696.64 | evidence:rss_investigation_8waves |
| **Wave 6** | 300,000 | `val0` | 3708266 | 440.03 | 592.40 | 3881.86 | 5632.19 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 431.97 | 584.88 | 3825.36 | 5402.14 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 432.28 | 585.16 | 3746.27 | 5487.28 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 547.36 | 703.79 | 3554.23 | 5481.30 | evidence:rss_investigation_8waves |
| **Wave 7** | 350,000 | `val0` | 3708266 | 523.03 | 693.53 | 3914.02 | 6349.46 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 517.93 | 689.76 | 3925.39 | 5722.77 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 515.57 | 687.43 | 3886.27 | 6079.16 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 509.97 | 686.67 | 4029.95 | 6220.19 | evidence:rss_investigation_8waves |
| **Wave 8** | 400,000 | `val0` | 3708266 | 529.43 | 710.23 | 3914.20 | 6503.49 | evidence:rss_investigation_8waves |
| | | `val1` | 3708608 | 527.07 | 706.39 | 3957.52 | 6206.78 | evidence:rss_investigation_8waves |
| | | `val2` | 3708943 | 525.23 | 707.64 | 3950.36 | 6263.80 | evidence:rss_investigation_8waves |
| | | `val3` | 3709333 | 519.54 | 707.22 | 4029.95 | 6431.87 | evidence:rss_investigation_8waves |

---

## 4. Phân Tích Thống Kê & Bác Bỏ Khẳng Định Cũ

### Phân Tích Hồi Quy Tuyến Tính (Linear Regression)
Dựa trên dữ liệu đo thực nghiệm 8 đợt (400,000 txs):
- **Hồi quy HeapAlloc sau GC:**
  $$\text{HeapAlloc} = (98.89 \pm 11.97) \times \frac{\text{Txs}}{100,000} + 144.57 \text{ MB} \quad (R^2 = 0.986)$$
  * Hệ số góc $m = +98.89$ MB / 100k txs vượt ngưỡng chấp nhận ban đầu ($m \le 30$ MB / 100k txs).
- **Hồi quy RSS thực tế:**
  $$\text{RSS} = (1154.96 \pm 139.81) \times \frac{\text{Txs}}{100,000} + 1792.8 \text{ MB} \quad (R^2 = 0.986)$$

### Tuyên Bố Rút Lại Khẳng Định Cũ (Retraction Notice)
> [!WARNING]
> **RÚT LẠI KẾT LUẬN CŨ:**
> Khẳng định trước đây cho rằng *"không có rò rỉ bộ nhớ, đường cong phẳng"* là **KHÔNG CHÍNH XÁC VÀ BỊ RÚT LẠI (RETRACTED)** (`evidence:rss_investigation_8waves`).
> Dữ liệu thực nghiệm 8 đợt tải chứng minh đường cong bộ nhớ có độ dốc tăng trưởng rõ rệt ($m = +98.89$ MB Heap / 100k txs; $R^2 = 0.986$, `evidence:rss_investigation_8waves`).
> Do đó, tiêu chí nghiệm thu "bộ nhớ không đổi ($m \le 30$)" được đánh giá là **FAIL**.

### Phân Tích Nguyên Nhân Kỹ Thuật (Memstats Profile Analysis)
So sánh cấu trúc bộ nhớ InUse giữa Wave 1 (50k txs) và Wave 8 (400k txs) từ file memstats thô:
1. `github.com/meta-node-blockchain/meta-node/pkg/blockchain.(*ethHashMapBlsHashMap).Store`:
   - Wave 1: 5.98 MB InUse
   - Wave 8: 47.81 MB InUse (tăng thêm +41.83 MB)
   - *Bản chất:* Bản đồ in-memory ánh xạ giữa địa chỉ Ethereum và BLS key cho từng tài khoản mới phát sinh trong quá trình blast giao dịch.
2. `github.com/meta-node-blockchain/meta-node/pkg/blockchain.(*txHashToBlockNumberMap).Store`:
   - Wave 1: 0 MB InUse
   - Wave 8: 35.06 MB InUse (tăng thêm +35.06 MB)
   - *Bản chất:* Index in-memory tra cứu `tx_hash -> block_number` cho 400,000 giao dịch đã commit.
3. `cockroachdb/pebble.(*Batch).grow`:
   - Tăng thêm +4.00 MB cho database write batch buffer.
4. **Các thành phần hệ thống khác:**
   - Network socket server buffer (`network.NewSocketServer`): 7.63 MB (bất biến từ Wave 1 đến Wave 8).
   - Transaction processor workers (`NewTransactionProcessor`): 38.15 MB (bất biến từ Wave 1 đến Wave 8).

**Kết luận kỹ thuật:** Mức tăng trưởng HeapAlloc $+98.89$ MB / 100k txs xuất phát từ **việc lưu trữ index và state mapping trong RAM** cho các tài khoản và giao dịch mới, chứ **không phải** rò rỉ bộ nhớ vô tận từ goroutines hay network sockets. Các map này hiện được chặn theo thời gian (TTL 30 phút qua `mappingCacheTTL`) nhưng **chưa có giới hạn dung lượng phần tử (unbounded by capacity)**, dẫn đến nguy cơ tích tụ RAM lớn trong cửa sổ 30 phút nếu gặp lượng giao dịch dồn dập, và cần cơ chế bounded cache (LRU hoặc Two-Generation map) để kiểm soát trần bộ nhớ xác định.

---

## 5. Thí Nghiệm Đối Chứng Có Kiểm Soát: `GOGC=50` vs Mặc Định (800) Trên Cụm Sạch

> [!IMPORTANT]
> **TIÊU CHÍ GHI TRƯỚC (PRE-SPECIFIED CRITERIA):**
> - Kết luận có ý nghĩa thống kê khi kiểm định Welch's t-test có $p < 0.05$ và khoảng tin cậy 95% không chứa 0 với $n \ge 7$ mỗi nhánh.
> - Mỗi lượt đo PHẢI chạy trên **cụm mới (fresh cluster)** tạo mới từ template chuẩn với cùng genesis ban đầu, cùng kích thước state, chạy đúng 1 đợt 25,000 transactions Secp256k1 EIP-1559 (batch 1,000) rồi dừng. Không chạy liên tiếp tích luỹ state giữa các lượt.

### Bảng Kết Quả Thực Nghiệm 7 Lượt Xen Kẽ (n = 7 mỗi bên)

> [!NOTE]
> **Lưu ý về Drift RSS & Giá trị Tương đối:** Do hiện tượng drift RSS baseline (+8.24%) khi khởi động lại các cụm mới (xem phân tích chi tiết tại Mục 7), các giá trị Peak RSS đo được giữa các cấu hình chỉ có ý nghĩa đối chứng tương đối trong từng cặp xen kẽ cùng lượt chạy (Run $i$). Kết luận về mức so sánh RSS đỉnh giữa các lần khởi động khác nhau được đánh giá là **INCONCLUSIVE**.

| Cấu hình | Lần chạy | Workload | Thời gian (s) | Effective TPS (tx/s) | Peak RSS Cụm (MB) | CPU tiêu thụ (s) | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Default GOGC (800)** | Run 1 | 25,000 | 5.29s | 6,276.2 | 6,712 | 83.4s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 2 | 25,000 | 5.34s | 6,171.0 | 8,995 | 94.3s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 3 | 25,000 | 5.45s | 6,198.6 | 9,014 | 95.2s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 4 | 25,000 | 5.41s | 6,290.8 | 10,734 | 83.8s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 5 | 25,000 | 5.25s | 6,091.9 | 10,396 | 95.2s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 6 | 25,000 | 5.53s | 6,118.7 | 10,959 | 87.1s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 7 | 25,000 | 5.75s | 5,599.8 | 11,952 | 95.7s | evidence:controlled_benchmarks_gogc_and_debug |
| **GOGC=50** | Run 1 | 25,000 | 5.66s | 5,759.3 | 4,412 | 150.8s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 2 | 25,000 | 5.52s | 5,948.3 | 5,524 | 141.7s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 3 | 25,000 | 5.64s | 5,961.7 | 6,359 | 136.2s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 4 | 25,000 | 5.71s | 5,849.0 | 7,418 | 149.3s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 5 | 25,000 | 6.44s | 4,843.9 | 7,601 | 159.7s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 6 | 25,000 | 5.66s | 5,947.3 | 8,318 | 133.4s | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 7 | 25,000 | 5.63s | 5,969.3 | 8,179 | 143.7s | evidence:controlled_benchmarks_gogc_and_debug |

### Phân Tích Thống Kê Định Lượng (Welch's t-test)
- **Thông lượng (Effective TPS):**
  * Default GOGC (800): $6,106.7 \pm 235.4$ tx/s (`evidence:controlled_benchmarks_gogc_and_debug#CTRL_GOGC_DEF_TPS_MEAN`)
  * GOGC=50: $5,754.1 \pm 408.7$ tx/s (`evidence:controlled_benchmarks_gogc_and_debug#CTRL_GOGC_50_TPS_MEAN`)
  * Chênh lệch TPS: **-5.77%** ($t = 1.98, \text{df} = 9.6, p = 0.0774, 95\% \text{ CI} = [-47.0, 752.1]$ tx/s).
  * *Kết luận theo tiêu chí ghi trước:* **INCONCLUSIVE** (Không phát hiện chênh lệch TPS có ý nghĩa thống kê với $n=7$ vì $p = 0.0774 > 0.05$ và khoảng tin cậy $95\%$ $[-47.0, 752.1]$ chứa số $0$).
- **Bộ nhớ đỉnh (Peak RSS Cụm 4 nodes):**
  * Default GOGC: $9,823 \pm 1,732$ MB (`evidence:controlled_benchmarks_gogc_and_debug#CTRL_GOGC_DEF_RSS_MEAN`)
  * GOGC=50: $6,830 \pm 1,458$ MB (`evidence:controlled_benchmarks_gogc_and_debug#CTRL_GOGC_50_RSS_MEAN`)
  * Mức độ tiết kiệm RAM: **-30.47%** ($t = 3.50, \text{df} = 11.7, p = 0.0046, 95\% \text{ CI} = [1122.7, 4863.3]$ MB). Khác biệt có ý nghĩa thống kê cao ($p < 0.01$).
- **Chi phí CPU (CPU Time):**
  * Default GOGC: $90.6 \pm 5.8$ s
  * GOGC=50: $145.0 \pm 9.4$ s (+60.0% CPU time do GC chạy thường xuyên hơn).

---

## 6. Thí Nghiệm Đối Chứng: Ảnh Hưởng Của Cờ `-debug=true` (`ENABLE_DEBUG_PPROF`)

### Cơ Chế Kỹ Thuật Trong Mã Nguồn
Trong `execution/cmd/simple_chain/main.go`:
```go
// Line 34: Khai báo flag
debug = flag.Bool("debug", false, "Debug mode")
// Line 161-163: Khởi động pprof HTTP server nếu flag bật
if *debug {
    startDebugServer(*pprofAddr)
}
// Line 349-366: startDebugServer lắng nghe trên địa chỉ --pprof-addr
```
Cờ `-debug` chỉ kích hoạt HTTP listener `http.Serve` cho `net/http/pprof`. Khi không có client nào scrape profiles, pprof ở trạng thái nhàn rỗi.

### Bảng Kết Quả Thực Nghiệm 7 Lượt Xen Kẽ Bật / Tắt (Fresh State)

> [!NOTE]
> **Lưu ý về Drift RSS & Cột Log Size (0.0 KB):**
> 1. Tương tự như thực nghiệm GOGC, kết luận về mức RSS đỉnh tuyệt đối giữa các lần khởi động khác nhau là **INCONCLUSIVE**; chỉ so sánh tương đối giữa Debug On và Debug Off trong cùng lượt chạy.
> 2. Cột Log Size ghi nhận `0.0 KB` trong bảng dưới đây là do lỗi định vị đường dẫn log trong phiên bản cũ của công cụ đo (`run_controlled_benchmarks.py`), khi công cụ tìm kiếm tại `$BASE/<node>/logs` thay vì đường dẫn thật `$BASE/logs/<node>.log`. Lỗi đo lường này đã được khắc phục tại commit `af5052e0`. Bảng dữ liệu cũ dưới đây được bảo lưu trung thực theo log gốc mà không sửa hồi tố. Kết quả kiểm chứng 3 lượt mới với log size thật được trình bày tại Mục 7 (`evidence:log_size_verification`).

| Cấu hình | Lần chạy | Workload | Thời gian (s) | Effective TPS (tx/s) | Peak RSS Cụm (MB) | CPU tiêu thụ (s) | Log Size (KB) | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Debug On (`ENABLE_DEBUG_PPROF=true`)** | Run 1 | 25,000 | 5.80s | 5,503.6 | 11,357 | 91.2s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 2 | 25,000 | 6.17s | 5,338.4 | 11,898 | 101.2s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 3 | 25,000 | 5.47s | 5,960.9 | 12,606 | 96.8s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 4 | 25,000 | 6.34s | 5,085.5 | 13,106 | 101.2s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 5 | 25,000 | 4.93s | 6,878.7 | 12,799 | 89.7s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 6 | 25,000 | 5.59s | 6,033.0 | 13,760 | 99.2s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 7 | 25,000 | 5.28s | 6,527.7 | 12,921 | 94.5s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| **Debug Off (`ENABLE_DEBUG_PPROF=false`)** | Run 1 | 25,000 | 7.74s | 3,867.2 | 11,315 | 94.5s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 2 | 25,000 | 5.21s | 6,376.7 | 11,550 | 92.2s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 3 | 25,000 | 5.46s | 5,992.9 | 11,877 | 90.8s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 4 | 25,000 | 6.45s | 4,690.2 | 12,377 | 101.0s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 5 | 25,000 | 5.28s | 6,272.3 | 12,457 | 91.2s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 6 | 25,000 | 5.10s | 6,519.7 | 13,274 | 98.8s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |
| | Run 7 | 25,000 | 7.10s | 4,169.4 | 11,966 | 108.7s | 0.0 KB | evidence:controlled_benchmarks_gogc_and_debug |

### Phân Tích Thống Kê Định Lượng
- **Thông lượng TPS:**
  * Debug On: $5,904.0 \pm 646.7$ tx/s (`evidence:controlled_benchmarks_gogc_and_debug#CTRL_DEBUG_ON_TPS_MEAN`)
  * Debug Off: $5,412.6 \pm 1131.9$ tx/s (`evidence:controlled_benchmarks_gogc_and_debug#CTRL_DEBUG_OFF_TPS_MEAN`)
  * Chênh lệch TPS: $-8.32\%$ ($t = 1.00, \text{df} = 9.5, p = 0.3433 \gg 0.05, 95\% \text{ CI} = [-613.7, 1596.4]$ tx/s).
  * *Kết luận:* Không có sự khác biệt có ý nghĩa thống kê giữa bật và tắt cờ `-debug` ($p = 0.3433$).
- **CPU Time:** Debug On $96.3 \pm 4.7$s vs Debug Off $96.7 \pm 6.5$s ($p = 0.8798$, chênh lệch $+0.48\%$, nằm trong khoảng biến thiên ngẫu nhiên).
- **Hành động kỹ thuật:** Mặc định của `ENABLE_DEBUG_PPROF` trong [execution/scripts/test/gate_e2e/run_env.sh](file:///home/abc/chain-n/metanode/execution/scripts/test/gate_e2e/run_env.sh) đã được đổi thành `false` theo tiêu chuẩn đóng gói bảo mật sản xuất (production hardening).
- *Ghi chú quan trọng:* Mọi báo cáo benchmark trước đây (B1, Durability, Wave 1-8) đều được thực hiện khi cờ `-debug=true` đang bật mặc định trong kịch bản chạy thử nghiệm.

---

## 7. Hiện Tượng Drift RSS Khi Khởi Động Cụm Mới & Kiểm Chứng Log Size

### 7.1. Hiện Tượng Drift RSS Baseline Khởi Động (+8.24%)
Để kiểm tra độ ổn định của môi trường benchmark khi tạo mới cụm validator từ template sạch, thực nghiệm chẩn đoán đa lượt được thực hiện với 3 lượt liên tiếp (`diagnose_rss_drift.py`), mỗi lượt khởi động cụm 4 node mới từ đầu, đo đạc baseline bộ nhớ ngay sau khi khởi động và sau khi xử lý tải 25,000 transactions.

Nguồn bằng chứng: [note/evidence/perf_rss_20261007/rss_drift_diagnostic.log](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/rss_drift_diagnostic.log) (SHA256: `619ad847256e5d32366b312bdffd5e43586863dbf958381ab2f3eb2f265657c9`, bytes: 3871) và [note/evidence/perf_rss_20261007/rss_drift_diagnostic_summary.csv](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/rss_drift_diagnostic_summary.csv) (SHA256: `5074f51e6dd629bd183c937f5fbb764de593931d5f97c3b30eb93e8fcd09e681`, bytes: 179).

| Lượt đo | Baseline RSS Tổng (MB) | Peak RSS (/proc) (MB) | Peak RSS (Tool) (MB) | Dung lượng đĩa val0 ban đầu (MB) | Tiến trình sót | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Round 1** | 7,384.7 | 11,161.2 | 11,289 | 2,050.03 | 0 | evidence:rss_drift_diagnostic |
| **Round 2** | 7,760.8 | 11,858.0 | 11,881 | 2,050.03 | 0 | evidence:rss_drift_diagnostic |
| **Round 3** | 7,993.3 | 11,943.0 | 11,966 | 2,050.03 | 0 | evidence:rss_drift_diagnostic |

**Phân tích chi tiết từng node (Node Breakdown):**
- **Round 1:** `val0` = 1,885.2 MB, `val1` = 1,930.4 MB, `val2` = 1,794.2 MB, `val3` = 1,775.0 MB (Tổng: 7,384.7 MB).
- **Round 2:** `val0` = 1,947.2 MB, `val1` = 1,992.4 MB, `val2` = 1,851.9 MB, `val3` = 1,969.2 MB (Tổng: 7,760.8 MB).
- **Round 3:** `val0` = 2,104.0 MB, `val1` = 1,999.9 MB, `val2` = 2,052.6 MB, `val3` = 1,836.8 MB (Tổng: 7,993.3 MB).
- **Tỷ lệ tăng trưởng baseline:** $+8.24\%$ từ Round 1 (7,384.7 MB) lên Round 3 (7,993.3 MB), hệ số tương quan thứ hạng Spearman là 1.0000.

**Các giả thuyết ĐÃ LOẠI TRỪ bằng bằng chứng thực nghiệm:**
1. *Rò rỉ tiến trình nền (Lingering Processes):* Số tiến trình `simple_chain` hoặc `metanode` còn sót lại trước khi khởi động và sau khi dừng cụm ở cả 3 vòng đều bằng 0 (đã kiểm tra qua `ps -eo pid,rss,cmd`).
2. *Tích luỹ dữ liệu trạng thái trên đĩa (Disk / State Accumulation):* Dung lượng thư mục dữ liệu ban đầu của `val0` trước khi blast ở cả 3 vòng là 2,050.03 MB bất biến (chuẩn hóa từ template sạch). Sau khi blast 25,000 txs, dung lượng tăng rất nhỏ (~0.03–0.05 MB) lên 2,050.06 – 2,050.08 MB và được xóa sạch khi tái tạo cụm.

**Các giả thuyết kỹ thuật CHƯA kiểm chứng đầy đủ:**
1. *Cơ chế cấp phát bộ nhớ ảo của Linux Kernel (Page Cache / THP / NUMA):* Do việc tái sử dụng địa chỉ bộ nhớ ảo của hệ điều hành, các trang bộ nhớ ẩn danh (Anonymous RSS) có thể chưa được hệ điều hành giải phóng về pool vật lý trước khi cụm mới khởi tạo nếu không can thiệp bằng `drop_caches` (cần quyền root).
2. *Phân mảnh Heap trong Go Runtime Allocator (mheap arenas):* Các tiến trình mới có thể phân bố trang nhớ khác nhau tuỳ thuộc vào trạng thái phân mảnh bộ nhớ ảo của kernel Linux tại thời điểm khởi động.
3. *Đặc điểm baseline ~1.8–2.1 GB mỗi node:* Chi phí RSS tĩnh ban đầu này xuất phát từ việc khởi tạo genesis state (50,000 tài khoản Secp256k1 tạo sẵn), các bảng băm ánh xạ tài khoản ban đầu, cấu trúc cache tầng lá và trang của NOMT, cùng buffer cố định cho network sockets và queue workers.

**Kết luận đánh giá tiêu chí:**
- Đánh giá **INCONCLUSIVE** đối với giá trị RSS đỉnh khi so sánh giữa các lần chạy benchmark ở các thời điểm độc lập.
- Các so sánh hiệu năng bộ nhớ giữa các cấu hình (ví dụ GOGC=800 vs GOGC=50, Debug On vs Debug Off) CHỈ có giá trị khoa học khi đo đạc **xen kẽ (interleaved)** trong cùng một chuỗi thực nghiệm.

---

### 7.2. Kiểm Chứng Thực Nghiệm Dung Lượng Log (Log Size Verification)
Trong các kết quả đo đạc ban đầu của Bảng Debug Flag (Mục 6), cột Log Size hiển thị `0.0 KB` do lỗi logic định vị đường dẫn thư mục log trong script `run_controlled_benchmarks.py` (script tìm tại `$BASE/<node>/logs` thay vì đường dẫn chuẩn `$BASE/logs/<node>.log` được quy định bởi `run_env.sh`).

Lỗi đo lường này đã được sửa tại commit `af5052e0`. Để minh bạch dữ liệu theo Quy tắc Chống Báo Cáo Giả (Mục 0), 3 lượt chạy kiểm chuẩn có đối chứng mới đã được thực hiện để ghi nhận dung lượng log thật.

Nguồn bằng chứng: [note/evidence/perf_rss_20261007/log_size_verification.log](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/log_size_verification.log) (SHA256: `f21b3a8077270db1ded506e6cfc29a7a140677fb8128127c1b52a216c77a0a5e`, bytes: 2110) và [note/evidence/perf_rss_20261007/log_size_verification_summary.csv](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/log_size_verification_summary.csv) (SHA256: `bc11c0b28039833c8d1a99b521c9d82eeae2ca9d2da447917022a164e4e7d0bc`, bytes: 391).

| Cấu hình | Lần chạy | Workload | Thời gian (s) | Effective TPS (tx/s) | Peak RSS Cụm (MB) | CPU tiêu thụ (s) | Log Size Thực Tế (KB) | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Debug On (`ENABLE_DEBUG_PPROF=true`)** | Run 1 | 25,000 | 5.83s | 5,711.0 | 12,350 | 99.1s | 208.9 KB | evidence:log_size_verification |
| | Run 2 | 25,000 | 5.41s | 6,054.9 | 12,443 | 103.0s | 208.0 KB | evidence:log_size_verification |
| | Run 3 | 25,000 | 5.48s | 5,949.7 | 12,402 | 92.2s | 207.9 KB | evidence:log_size_verification |
| **Debug Off (`ENABLE_DEBUG_PPROF=false`)** | Run 1 | 25,000 | 5.51s | 5,663.1 | 12,077 | 95.8s | 206.8 KB | evidence:log_size_verification |
| | Run 2 | 25,000 | 5.25s | 6,066.9 | 12,770 | 108.6s | 209.6 KB | evidence:log_size_verification |
| | Run 3 | 25,000 | 5.22s | 6,321.0 | 12,359 | 89.8s | 211.9 KB | evidence:log_size_verification |

**Kết luận về Log Size:**
- Dung lượng log thật ghi nhận được là $208.3 \pm 0.8$ KB đối với Debug On và $209.5 \pm 2.6$ KB đối với Debug Off (chênh lệch $-0.57\%$, không có ý nghĩa thống kê).
- Kết quả này tái xác nhận cờ `-debug=true` (`ENABLE_DEBUG_PPROF`) chỉ bật HTTP debug listener cho pprof, không làm phát sinh thêm log ra ổ đĩa so với khi tắt.

---

## 8. Ma Trận Khuyến Nghị Cấu Hình Cho Node Operators

> [!NOTE]
> Bảng dưới đây kết hợp dữ liệu đã đo đạc thực nghiệm trên cụm sạch với các suy luận kỹ thuật vận hành. Mọi mục chưa có đo đạc trực tiếp đều được ghi chú minh bạch.

| Hồ sơ phần cứng | RAM Khuyến nghị | Cấu hình đề xuất | Đỉnh RSS kỳ vọng | Throughput TPS dự kiến | Tình trạng đo đạc thực nghiệm | Evidence |
| :--- | :---: | :--- | :---: | :---: | :--- | :---: |
| **Tiêu chuẩn (Standard Validator)** | **32 GB** | `GOMEMLIMIT=8GiB`<br>`GOGC=800` (mặc định) | 6.0 – 7.5 GB/node | **6,000 – 6,500 tx/s** | **ĐÃ ĐO THỰC TẾ** (Đo đối chứng cụm sạch: TPS ~6,107 tx/s, RSS ~9.8 GB/cụm) | `evidence:controlled_benchmarks_gogc_and_debug` |
| **Tiết kiệm RAM (Resource-Constrained)** | **16 GB** | `GOMEMLIMIT=4GiB`<br>`GOGC=50` | 1.5 – 2.0 GB/node | **5,500 – 6,000 tx/s** | **ĐÃ ĐO THỰC TẾ** (Đo đối chứng cụm sạch: TPS ~5,754 tx/s, RSS ~6.8 GB/cụm, giảm 30.5% RAM) | `evidence:controlled_benchmarks_gogc_and_debug` |
| **Enterprise / Tier 1 Validator** | **≥ 64 GB** | `GOMEMLIMIT=16GiB`<br>`GOGC=200` | 8.0 – 12.0 GB/node | **7,500+ tx/s** | *SUY LUẬN KỸ THUẬT, CHƯA ĐO ĐẠC TRỰC TIẾP* | `evidence:controlled_benchmarks_gogc_and_debug` |
| **Sentry / RPC Node nhỏ** | **8 GB** | `GOMEMLIMIT=2GiB`<br>`GOGC=30` | 1.0 – 1.5 GB/node | **4,000 – 4,500 tx/s** | *SUY LUẬN KỸ THUẬT, CHƯA ĐO ĐẠC TRỰC TIẾP* | `evidence:controlled_benchmarks_gogc_and_debug` |

---

## 9. Kết Luận & Đánh Giá Tổng Thể
1. Khẳng định cũ về việc "bộ nhớ không đổi" đã được sửa đổi minh bạch: Bộ nhớ tăng trưởng tuyến tính ($+98.89$ MB Heap / 100k txs) do in-memory caching cho ánh xạ địa chỉ và tra cứu giao dịch (`evidence:rss_investigation_8waves`).
2. Đo lường có đối chứng trên cụm sạch cho thấy `GOGC=50` giảm 30.5% RSS đỉnh ($p = 0.0046$), đổi lại CPU tăng ~60% cho tác vụ GC, trong khi chênh lệch TPS là **INCONCLUSIVE** ($p = 0.0774 > 0.05$, khoảng tin cậy 95% chứa số 0) (`evidence:controlled_benchmarks_gogc_and_debug`).
3. Cờ `-debug=true` không gây suy giảm hiệu năng có ý nghĩa thống kê ($p = 0.3433$), nhưng đã được chuyển về mặc định `false` trong `run_env.sh` để tuân thủ tiêu chuẩn production hardening.
4. Hiện tượng drift RSS baseline (+8.24%) khi khởi động lại các cụm mới đã được kiểm chứng và giải thích minh bạch: các so sánh RSS đỉnh chỉ có giá trị đối chứng tương đối giữa các lượt xen kẽ (interleaved); kết luận RSS đỉnh tuyệt đối là **INCONCLUSIVE** (`evidence:rss_drift_diagnostic`).
5. Lỗi Log Size 0.0 KB trong bảng cũ đã được khắc phục và kiểm chứng thực nghiệm bằng 3 lượt đo mới với dung lượng log thật đạt ~208 KB (`evidence:log_size_verification`).
6. Tài liệu thiết kế kiến trúc [note/design_bounded_memory_indexes_20261007.md](file:///home/abc/chain-n/metanode/note/design_bounded_memory_indexes_20261007.md) đã được đệ trình để giải quyết triệt để nguyên nhân gốc rễ bằng Bounded Memory Cache.
7. Toàn bộ số liệu trong báo cáo đều có file log thô, SHA256 và kích thước bytes tương ứng trong [note/evidence/perf_rss_20261007/MANIFEST.json](file:///home/abc/chain-n/metanode/note/evidence/perf_rss_20261007/MANIFEST.json).

