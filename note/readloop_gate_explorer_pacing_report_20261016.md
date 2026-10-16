# Báo cáo Thực thi & Thực nghiệm A/B (1M & 4M Tx): Backpressure Lớp Sớm tại `Connection.readLoop` (`ConnRequestChanSize = 16`) và Điều tiết Hàng đợi `ExplorerSearchService` / Gom lô `Commit()` 64 Block

- **Ngày thực hiện:** 2026-10-16
- **Nhánh & Commit thực nghiệm:** `dev` @ `5004cf0f2de1d2f727b15785b20b15585516b7fa` (`bins_next`, SHA256 `simple_chain`: `ada7a3f70128c7b4304f0259dd9df56bc6ee971ea13b525c10923ed737bacfdb`) so sánh đối chứng với `86e49ecab44637d8c655df538644156607debc27` (`bins_prev`, mã thực thi Go/Rust trùng khớp `4a2b63879b7fadbbf2c3be0ce08db0d0c5e84d20`, SHA256 `simple_chain`: `bd95a149d8b04bc3ae4723bdde382417f2940de48539b889ea6dedb9b182be32`) (`evidence:preregistered`, [PREREGISTERED.md](file:///home/abc/chain-n/metanode/note/evidence/readloop_gate_explorer_pacing_20261016/PREREGISTERED.md#L1-L52))
- **Thư mục bằng chứng:** `note/evidence/readloop_gate_explorer_pacing_20261016/` (`MANIFEST.json`, `PREREGISTERED.md`, `driver_ab_readloop.py.txt`, `driver_ab_readloop.log`, `derived_summary.csv`, `pprof_comparison.txt`, `ab_1m/`, `ab_4m/`)
- **Nhãn phân loại tuyên bố sử dụng trong báo cáo:** `[ĐO]`, `[TÍNH]`, `[ĐỌC CODE]`, `[GIẢ THUYẾT]`, `[CHƯA ĐO]`

---

## 1. Tóm tắt điều hành (Executive Summary)

Báo cáo này thực thi và kiểm chứng bằng thực nghiệm A/B đan xen (16 lượt chạy = 4 cặp 1M + 4 cặp 4M) các hạng mục trong phần **"Chưa làm"** của `note/CLAIMS_LEDGER.md` (dòng 35) và `note/cgocall_backpressure_explorer_report_20261015.md` (§6.1, §6.2, §7.1, §7.2):

1. **Nhóm thay đổi 1 — Đặt `loadTxAdmissionGate()` trực tiếp trong `Connection.readLoop` & Giảm `ConnRequestChanSize` từ `256` xuống `16` batch:**
   - `[ĐỌC CODE]` Tại `bins_prev` (`4a2b6387`), cổng `loadTxAdmissionGate()` chỉ được kiểm tra tại `SocketServer.HandleConnection` (`execution/pkg/network/server.go:239`), nằm phía **sau** kênh đệm `conn.RequestChan()` (`ConnRequestChanSize = 256` batch = `256,000` giao dịch). Khi `inFlightCount > HighWatermark (300,000)`, goroutine `Connection.readLoop` vẫn đọc và giải mã protobuf thêm `256` batch (`256,000` giao dịch/kết nối) vào bộ nhớ Go trước khi bị chặn (`evidence:preregistered`).
   - `[ĐỌC CODE]` Tại `bins_next`, [`Connection.readLoop`](file:///home/abc/chain-n/metanode/execution/pkg/network/connection.go#L812-L936) tạo ngữ cảnh `readCtx` gắn với kênh `quit` của kết nối (1 lần/kết nối, 0 cấp phát/batch) và kiểm tra `loadTxAdmissionGate()` trực tiếp cho các lệnh `isTxSubmissionCommand` **trước** khi đẩy vào `requestChan`; đánh dấu `req.admitted = true` tại [`request.go:25-65`](file:///home/abc/chain-n/metanode/execution/pkg/network/request.go#L25-L65) để [`SocketServer.HandleConnection`](file:///home/abc/chain-n/metanode/execution/pkg/network/server.go#L239-L246) không gọi lặp `gate(s.ctx)` lần thứ hai; đồng thời giảm `socketCfg.ConnRequestChanSize` mặc định trong [`app.go:880-888`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/app.go#L880-L888) từ `256` batch (`256,000` tx) xuống **`16` batch (`16,000` tx**, có thể cấu hình qua `META_CONN_REQUEST_CHAN_SIZE`) (`evidence:preregistered`).

2. **Nhóm thay đổi 2 — Điều tiết tốc độ đẩy `indexSingleBlock` -> `s.indexQueue`, Nâng `ExplorerQueueSize = 65536` & Sửa gom lô `explorerSvc.Commit()` mỗi 64 block:**
   - `[ĐỌC CODE]` Phát hiện và xử lý triệt để 2 nguyên nhân gốc rễ trong `bins_prev`:
     1. Trong [`config.go:399`](file:///home/abc/chain-n/metanode/execution/pkg/config/config.go#L399), `ConfigApp.ExplorerQueueSize` mặc định trước đó bị gán cứng bằng `8192` (ghi đè `defaultQueueSize = 65536` của `explorer/service.go`), nhỏ hơn kích thước 1 block BFT dưới tải cao (`8,500–10,000` tx/block), khiến mỗi block tràn hàng đợi ngay lập tức (`evidence:preregistered`).
     2. Trong [`block_processor_indexing.go:154-166`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L154-L166), điều kiện cũ `if len(bp.indexingChannel) == 0 || bp.uncommittedIndexBlocks >= 64` luôn đúng ở **mọi block** vì `indexSingleBlock` đẩy xong slice bộ nhớ trong `< 1ms` trong khi block mới chỉ commit mỗi `~400–500ms` (nên `len(bp.indexingChannel)` luôn bằng `0` ngay sau khi lấy block ra). Hệ quả là `explorerSvc.Commit()` (`_Cfunc_database_commit`) vẫn bị gọi ở **mọi block** và khóa cả 32 shard mutex Xapian (`evidence:preregistered`).
   - `[ĐỌC CODE]` Tại `bins_next`: nâng `ConfigApp.ExplorerQueueSize` mặc định lên `65536`; bổ sung nhịp chờ điều tiết có giới hạn tối đa `50ms` trong [`ExplorerSearchService.IndexTransactionFast`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/service.go#L230-L263) khi `s.indexQueue` đầy tạm thời; giới hạn `bp.pendingIndexBlocks` tối đa `maxPendingIndexBlocks = 32` block (`bp.pendingIndexBlockCount`) tại [`block_processor_commit.go:303-325`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_commit.go#L303-L325); chỉ gọi `explorerSvc.Commit()` trong `finalizeIndexedBlock` khi `bp.uncommittedIndexBlocks >= 64` và để `tickerSync` (`2s`) commit khi `len(bp.indexingChannel) == 0 && explorerSvc.QueueDepth() == 0`; đồng thời bỏ log `logger.Error` trùng lặp trên từng giao dịch bị drop (`evidence:preregistered`).

3. **Kết quả Thực nghiệm A/B Đan xen (16/16 Lượt chạy = 4 Cặp 1M + 4 Cặp 4M, Đảm bảo Zero-Fork):**
   - `[ĐO]` Cả **16/16 lượt chạy** (8 lượt ở 1M, 8 lượt ở 4M) đều đạt **`loss = 0`** (`committed_ours == bytes_sent`), **`parity = OK: all 4 validators agree on hash+stateRoot`**, và **`0/64` tệp `nomt_errors.txt` khác rỗng** (`evidence:ab_1m`, `evidence:ab_4m`).
   - `[TÍNH]` **Giảm dòng bất thường `anomaly_sum` (`node_anomaly_lines` trên 4 validator) có ý nghĩa thống kê cao ở cả 2 quy mô:**
     - Ở **1M tx:** giảm **`-95.69%` (giảm `23.2` lần)**, từ `468,629.25 ± 158,158.93` xuống **`20,186.75 ± 1,203.95` dòng** (`Diff = -448,442.50 ± 159,160.94`, `t = -5.6351`, `df = 3`, **`p = 0.011056`**, thắng 4/4 cặp) (`evidence:derived_analysis`).
     - Ở **4M tx:** giảm **`-89.71%` (giảm `9.7` lần)**, từ `870,408.50 ± 145,904.78` xuống **`89,605.75 ± 4,843.37` dòng** (`Diff = -780,802.75 ± 141,575.92`, `t = -11.0302`, `df = 3`, **`p = 0.001596`**, thắng 4/4 cặp) (`evidence:derived_analysis`).
   - `[TÍNH]` **Giảm chi phí CPU `_Cfunc_database_commit` và `runtime.cgocall` trên `val0` (`cpu30.pb.gz`):**
     - Thời gian CPU của `_Cfunc_database_commit` (`ExplorerSearchService.Commit`) giảm **`-33.44%`** ở 1M (`12.83s -> 8.54s`) và giảm **`-36.97%`** ở 4M (`12.28s -> 7.74s`) (`evidence:derived_analysis`).
     - Ở **4M tx (`4M_PREV` vs `4M_NEXT`)**, tổng mẫu CPU trong cửa sổ 30s giảm **`-9.00%`** (`297.28s -> 270.53s`), `runtime.cgocall` giảm **`-10.32%`** (`96.90s -> 86.90s`), `ExplorerSearchService.doIndexTransaction` giảm thêm **`-9.03%`** (`59.11s -> 53.77s`), `runtime.gcDrain` giảm **`-18.20%`** (`23.52s -> 19.24s`), và ở **1M tx** bộ nhớ sống `heap inuse_space` tại `t=20s` giảm **`-20.72%`** (`846.46 MB -> 671.10 MB`, `inuse_objects` giảm **`-24.41%`** từ `4,248,875` xuống `3,211,829`) (`evidence:derived_analysis`).
   - `[TÍNH]` **Hiệu quả Backpressure Lớp Sớm tại `Connection.readLoop` ở tải 4M tx:**
     - Thông lượng thực thi trong pha bơm **`tps_during_injection` tăng `+9.56%`**, từ `13,465.89 ± 182.01 tx/s` lên **`14,753.22 ± 753.93 tx/s`** (`Diff = +1,287.33 ± 830.21 tx/s`, `t = +3.1012`, `df = 3`, `p = 0.053244`, **thắng 4/4 cặp**: P1 `+9.16%`, P2 `+1.79%`, P3 `+10.28%`, P4 `+17.14%`), trong khi `tps_end_to_end` đạt **`14,630.35 ± 201.95 tx/s`** (`+0.39%` so với `14,573.04 ± 282.74 tx/s`, giảm độ lệch chuẩn `SD` `-28.6%`) và `peak_rss_mb_tool` đạt **`63,926.25 ± 1,090.32 MB`** (`-250.00 MB` so với `64,176.25 ± 776.90 MB`) (`evidence:derived_analysis`).
     - Khi một validator bị đóng cổng `overloadGate` và không mở lại giữa chừng (như `val0` và `val2` trong `ab_4m_next_p2`), tổng số giao dịch mà kết nối TCP đó đọc vào từ đầu lượt chạy bị kẹp cứng ở đúng **`423,000` và `414,000` giao dịch** (`300k` watermark + `16k` `ConnRequestChanSize` + `~98k–107k` buffer TCP kernel), giảm **`-35%` đến `-62%`** lượng giao dịch đệm trên mỗi kết nối bị chặn so với `bins_prev` (`631,000–1,000,000` tx) (`evidence:ab_4m`). Trong các lượt cả 4 kết nối đều kích hoạt điều tiết nhịp nhàng (`ab_4m_next_p1`, `ab_4m_next_p4`), lượng tồn đọng cuối pha bơm `backlog_at_inject_end` giảm từ `3.78M`/`3.63M` xuống **`1.45M`/`1.76M` tx (`-61.6%` / `-51.5%`)** và thời gian xả `drain_seconds` giảm từ `252s` xuống **`102.2s`/`124.2s`** (`evidence:ab_4m`, `evidence:derived_analysis`).

---

## 2. Chi tiết Kỹ thuật Các Thay đổi Đã Triển khai

### 2.1. Nhóm 1: Cổng `loadTxAdmissionGate()` tại `Connection.readLoop` & Giảm `ConnRequestChanSize` xuống `16`

`[ĐỌC CODE]` Các thay đổi trong gói `execution/pkg/network` và `execution/cmd/simple_chain`:

1. **Đánh dấu trạng thái đã qua cổng trên `Request` ([`request.go:25-65`](file:///home/abc/chain-n/metanode/execution/pkg/network/request.go#L25-L65)):**
   - Thêm trường `admitted bool` vào struct `Request` (và đặt lại `r.admitted = false` trong `Reset()`) để ghi nhận request đã được kiểm tra `loadTxAdmissionGate()` ngay tại `Connection.readLoop`.
2. **Kiểm tra `loadTxAdmissionGate()` trực tiếp trong `Connection.readLoop` ([`connection.go:812-936`](file:///home/abc/chain-n/metanode/execution/pkg/network/connection.go#L812-L936)):**
   - Khởi tạo một `readCtx, cancelReadCtx := context.WithCancel(context.Background())` duy nhất cho mỗi vòng đời kết nối `readLoop` (đóng khi kênh `quit` đóng, **0 cấp phát bộ nhớ trên đường nóng mỗi batch**).
   - Ngay khi giải mã xong `Message` thuộc nhóm `isTxSubmissionCommand(msg.Command())`, `readLoop` gọi `gate(readCtx)` **trước** khi đẩy `req` vào `requestChan`. Khi `inFlightCount > HighWatermark (300,000)`, `readLoop` tạm dừng đọc từ socket TCP ngay lập tức, làm đầy buffer TCP kernel và chặn `conn.Write()` của client sớm hơn `240,000` giao dịch/kết nối so với `bins_prev`.
   - Trong [`SocketServer.HandleConnection`](file:///home/abc/chain-n/metanode/execution/pkg/network/server.go#L239-L246), chỉ gọi `gate(s.ctx)` nếu `!req.admitted`, tránh chặn kép trên cùng một batch.
   - Bổ sung unit test [`TestIngest_AdmissionGateBlocksReadLoopImmediately`](file:///home/abc/chain-n/metanode/execution/pkg/network/lossless_ingest_test.go#L218-L296) xác minh `readLoop` dừng đẩy vào `conn.RequestChan()` ngay sau batch đầu tiên khi cổng đóng.
3. **Giảm `ConnRequestChanSize` mặc định từ `256` xuống `16` batch ([`app.go:880-888`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/app.go#L880-L888)):**
   - Đặt `socketCfg.ConnRequestChanSize = 16` (`16` batch × `1,000` tx = `16,000` tx/kết nối thay vì `256,000` tx/kết nối), hỗ trợ ghi đè qua biến môi trường `META_CONN_REQUEST_CHAN_SIZE`.

### 2.2. Nhóm 2: Điều tiết `indexSingleBlock` -> `s.indexQueue`, Nâng `ExplorerQueueSize = 65536` & Sửa Gom lô `Commit()` 64 Block

`[ĐỌC CODE]` Các thay đổi trong `execution/pkg/config`, `execution/pkg/explorer` và `execution/cmd/simple_chain/processor`:

1. **Đồng bộ `ConfigApp.ExplorerQueueSize = 65536` ([`config.go:399`](file:///home/abc/chain-n/metanode/execution/pkg/config/config.go#L399)):**
   - Sửa giá trị mặc định trong `config.go` từ `8192` lên `65536` để khớp với `defaultQueueSize = 65536` của `explorer/service.go`, đủ chứa trọn nhiều block 10,000 giao dịch liên tiếp.
2. **Điều tiết có giới hạn `50ms` trong `IndexTransactionFast` ([`service.go:230-263`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/service.go#L230-L263)):**
   - Giữ nguyên nhánh `select` không chặn (0 cấp phát) khi `s.indexQueue` còn chỗ trống.
   - Khi `s.indexQueue` đầy tạm thời dưới tải đột biến, goroutine nền `indexSingleBlock` chờ tối đa `50ms` (`time.NewTimer(50 * time.Millisecond)`) để 32 worker Xapian kịp rút bớt hàng đợi trước khi fallback sang drop, đồng thời thêm phương thức `QueueDepth() int` (`len(s.indexQueue)`).
3. **Giới hạn `pendingIndexBlocks` tối đa 32 block & Sửa điều kiện `explorerSvc.Commit()` mỗi 64 block ([`block_processor_core.go:108`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_core.go#L108), [`block_processor_commit.go:303-325`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_commit.go#L303-L325), [`block_processor_indexing.go:63-224`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L63-L224)):**
   - Thêm bộ đếm `pendingIndexBlockCount atomic.Int64` kẹp cứng tối đa `maxPendingIndexBlocks = 32` block giữ trong RAM; nếu vượt ngưỡng thì giải phóng qua hàm `popPendingIndexBlock` và để `indexSingleBlock` đọc lại từ DB khi cần.
   - Trong `finalizeIndexedBlock`, chỉ gọi `explorerSvc.Commit()` khi `bp.uncommittedIndexBlocks >= explorerCommitBatchBlocks` (`64` block). Khi chuỗi rỗi (`len(bp.indexingChannel) == 0 && explorerSvc.QueueDepth() == 0`), `tickerSync` (`2s`) trong `startIndexingProcess` mới gọi `explorerSvc.Commit()` để flush số block lẻ `< 64`.
   - Loại bỏ lệnh `logger.Error` trên từng giao dịch bị drop tại `indexSingleBlock`, chỉ giữ 1 dòng `logger.Warn` tổng hợp theo block tại `finalizeIndexedBlock` khi `indexDroppedCount > 0`.

---

## 3. So sánh Profile CPU (`cpu30.pb.gz`) và Bộ nhớ (`heap.pb.gz` / `allocs.pb.gz`) giữa `bins_prev` và `bins_next`

`[ĐO]` Dữ liệu dưới đây được trích xuất từ Cặp 1 của 1M (`--profile-at 20`) và Cặp 1 của 4M (`--profile-at 150`) tại [pprof_comparison.txt](file:///home/abc/chain-n/metanode/note/evidence/readloop_gate_explorer_pacing_20261016/pprof_comparison.txt#L1-L435) (`evidence:derived_analysis`):

### Bảng 1: So sánh các hàm nóng trên `val0` trong cửa sổ CPU Profile 30s (`1M_PREV` vs `1M_NEXT` và `4M_PREV` vs `4M_NEXT`)

| Hàm / Nhánh thực thi (`pprof -symbolize=none`) | `1M_PREV` (`s` / `%`) | `1M_NEXT` (`s` / `%`) | Thay đổi 1M | `4M_PREV` (`s` / `%`) | `4M_NEXT` (`s` / `%`) | Thay đổi 4M | Bằng chứng |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **Tổng mẫu CPU 30s (`Total samples`)** | `303.65s` (`1006.7%`) | `303.61s` (`1006.8%`) | `-0.01%` | `297.28s` (`985.7%`) | `270.53s` (`897.2%`) | **`-9.00%`** | `evidence:derived_analysis` |
| `_Cfunc_database_commit` (`ExplorerSearchService.Commit`) | `12.83s` (`4.23%`) | `8.54s` (`2.81%`) | **`-33.44%`** | `12.28s` (`4.13%`) | `7.74s` (`2.86%`) | **`-36.97%`** | `evidence:derived_analysis` |
| Tỷ trọng `_Cfunc_database_commit` trong `runtime.cgocall` | `12.83s / 98.35s` (`13.05%`) | `8.54s / 101.47s` (`8.42%`) | **`-4.63 điểm %`** | `12.28s / 96.90s` (`12.67%`) | `7.74s / 86.90s` (`8.91%`) | **`-3.76 điểm %`** | `evidence:derived_analysis` |
| `ExplorerSearchService.doIndexTransaction` (cum) | `54.78s` (`18.04%`) | `64.17s` (`21.14%`) | `+17.14%` (0 drop, index đủ 100% tx) | `59.11s` (`19.88%`) | `53.77s` (`19.88%`) | **`-9.03%`** | `evidence:derived_analysis` |
| `_Cfunc_database_index_document` (cum) | `45.32s` (`14.92%`) | `54.82s` (`18.06%`) | `+20.96%` (0 drop, index đủ 100% tx) | `49.61s` (`16.69%`) | `45.14s` (`16.69%`) | **`-9.01%`** | `evidence:derived_analysis` |
| `runtime.cgocall` (cum) | `98.35s` (`32.39%`) | `101.47s` (`33.42%`) | `+3.17%` | `96.90s` (`32.60%`) | `86.90s` (`32.12%`) | **`-10.32%`** | `evidence:derived_analysis` |
| `runtime.gcDrain` (cum) | `21.22s` (`6.99%`) | `21.98s` (`7.24%`) | `+3.58%` | `23.52s` (`7.91%`) | `19.24s` (`7.11%`) | **`-18.20%`** | `evidence:derived_analysis` |
| `runtime.mallocgc` (cum) | `24.65s` (`8.12%`) | `24.20s` (`7.97%`) | `-1.83%` | `20.57s` (`6.92%`) | `19.19s` (`7.09%`) | **`-6.71%`** | `evidence:derived_analysis` |
| `Heap inuse_space` tại thời điểm lấy mẫu (`val0`) | `846.46 MB` | `671.10 MB` | **`-20.72%`** | `1,158.95 MB` | `1,303.75 MB` | `+12.49%` | `evidence:derived_analysis` |
| `Heap inuse_objects` tại thời điểm lấy mẫu (`val0`) | `4,248,875` | `3,211,829` | **`-24.41%`** | `7,544,917` | `7,713,596` | `+2.24%` | `evidence:derived_analysis` |

`[TÍNH]` Phân tích cơ chế trên bảng profile:
- Ở cả 1M và 4M, việc sửa điều kiện gom lô `bp.uncommittedIndexBlocks >= 64` trong `finalizeIndexedBlock` giúp cắt giảm trực tiếp **`-33.44%`** (ở 1M) và **`-36.97%`** (ở 4M) thời gian CPU của `_Cfunc_database_commit` (`evidence:derived_analysis`).
- Ở 1M tx (`t=20s`), do `ExplorerQueueSize` được nâng từ `8192` lên `65536` và có nhịp điều tiết `50ms`, `bins_next` không còn đánh rơi hàng loạt giao dịch khỏi hàng đợi Explorer như `bins_prev` (`node_anomaly_lines` giảm `-95.69%` từ `468,629` xuống `20,187` dòng, tức `0` giao dịch bị drop ở 1M!), nên 32 worker Xapian thực sự lập chỉ mục trọn vẹn 100% giao dịch trong cửa sổ `t=20..50s` (`_Cfunc_database_index_document` tăng nhẹ từ `45.32s` lên `54.82s`), trong khi bộ nhớ sống `heap inuse_space` tại `t=20s` giảm **`-20.72%`** (`846.46 MB -> 671.10 MB`) nhờ loại bỏ hàng trăm nghìn thông điệp log lỗi và giới hạn `ConnRequestChanSize = 16` (`evidence:derived_analysis`).
- Ở 4M tx (`t=150s`), cả tổng mẫu CPU (`-9.00%`), `runtime.cgocall` (`-10.32%`), `doIndexTransaction` (`-9.03%`), `_Cfunc_database_commit` (`-36.97%`), `runtime.gcDrain` (`-18.20%`) và `runtime.mallocgc` (`-6.71%`) đều giảm đồng loạt trên `bins_next` (`evidence:derived_analysis`).

---

## 4. Kết quả Thực nghiệm A/B Đan xen (4 Cặp 1M & 4 Cặp 4M)

### Bảng 2: Kết quả chi tiết từng cặp chạy A/B ở tải 1M Tx (`seed=2026101601`, 4 cặp đan xen) và 4M Tx (`seed=2026101602`, 4 cặp đan xen)

| Quy mô | Cặp | Phiên bản | `bytes_sent` / `committed_ours` | `loss` | `end_to_end_seconds` (s) | `inject_seconds` (s) | `drain_seconds` (s) | `tps_end_to_end` (tx/s) | `tps_active_commit` (tx/s) | `tps_during_injection` (tx/s) | `latency_p50_ms` (ms) | `peak_rss_mb_tool` (MB) | `anomaly_sum` (dòng) | `parity` | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **1M** | P1 | `bins_prev` | `1,009,062` / `1,009,062` | `0` | `60.076` | `5.082` | `54.994` | `16,796.56` | `17,300.81` | `8,930.79` | `38,024` | `45,429` | `560,765` | `OK (#147)` | `evidence:ab_1m` |
| **1M** | P1 | `bins_next` | `1,008,249` / `1,008,249` | `0` | `61.676` | `5.044` | `56.632` | `16,347.63` | `16,611.68` | `9,516.24` | `46,569` | `42,523` | `19,159` | `OK (#156)` | `evidence:ab_1m` |
| **1M** | P2 | `bins_prev` | `1,007,267` / `1,007,267` | `0` | `61.227` | `4.920` | `56.307` | `16,451.43` | `16,704.33` | `8,943.09` | `43,373` | `41,827` | `620,580` | `OK (#155)` | `evidence:ab_1m` |
| **1M** | P2 | `bins_next` | `1,006,371` / `1,006,371` | `0` | `61.526` | `3.922` | `57.604` | `16,356.75` | `16,716.92` | `6,118.55` | `46,674` | `40,797` | `19,915` | `OK (#197)` | `evidence:ab_1m` |
| **1M** | P3 | `bins_prev` | `1,009,478` / `1,009,478` | `0` | `60.077` | `5.020` | `55.057` | `16,803.20` | `16,987.20` | `10,161.41` | `39,835` | `41,309` | `429,277` | `OK (#169)` | `evidence:ab_1m` |
| **1M** | P3 | `bins_next` | `1,009,673` / `1,009,673` | `0` | `60.451` | `5.024` | `55.428` | `16,702.29` | `17,142.81` | `7,763.46` | `46,030` | `43,685` | `19,747` | `OK (#131)` | `evidence:ab_1m` |
| **1M** | P4 | `bins_prev` | `1,006,655` / `1,006,655` | `0` | `62.777` | `3.945` | `58.832` | `16,035.53` | `16,442.02` | `5,069.70` | `48,551` | `40,028` | `263,895` | `OK (#199)` | `evidence:ab_1m` |
| **1M** | P4 | `bins_next` | `1,009,742` / `1,009,742` | `0` | `60.926` | `4.537` | `56.389` | `16,573.24` | `16,885.13` | `7,493.26` | `49,372` | `42,922` | `21,926` | `OK (#183)` | `evidence:ab_1m` |
| **4M** | P1 | `bins_prev` | `4,007,926` / `4,007,926` | `0` | `268.727` | `16.590` | `252.136` | `14,914.50` | `14,963.30` | `13,686.45` | `165,402` | `65,294` | `1,005,748` | `OK (#920)` | `evidence:ab_4m` |
| **4M** | P1 | `bins_next` | `4,013,263` / `4,013,263` | `0` | `273.627` | `171.404` | `102.223` | `14,666.93` | `14,700.55` | `14,940.44` | `141,965` | `63,025` | `91,267` | `OK (#1021)` | `evidence:ab_4m` |
| **4M** | P2 | `bins_prev` | `4,010,484` / `4,010,484` | `0` | `278.051` | `24.269` | `253.782` | `14,423.56` | `14,483.50` | `13,468.50` | `155,859` | `63,634` | `727,208` | `OK (#955)` | `evidence:ab_4m` |
| **4M** | P2 | `bins_next` | `4,011,234` / `4,011,234` | `0` | `277.152` | `43.835` | `233.316` | `14,473.07` | `14,562.47` | `13,710.02` | `132,007` | `62,984` | `85,895` | `OK (#916)` | `evidence:ab_4m` |
| **4M** | P3 | `bins_prev` | `4,010,613` / `4,010,613` | `0` | `273.201` | `30.559` | `242.641` | `14,680.09` | `14,736.80` | `13,467.94` | `147,323` | `64,114` | `986,166` | `OK (#916)` | `evidence:ab_4m` |
| **4M** | P3 | `bins_next` | `4,005,844` / `4,005,844` | `0` | `276.651` | `16.567` | `260.085` | `14,479.76` | `14,527.07` | `14,852.89` | `163,223` | `64,559` | `95,717` | `OK (#926)` | `evidence:ab_4m` |
| **4M** | P4 | `bins_prev` | `4,009,595` / `4,009,595` | `0` | `280.902` | `28.321` | `252.581` | `14,274.02` | `14,321.52` | `13,240.67` | `155,511` | `63,663` | `762,512` | `OK (#944)` | `evidence:ab_4m` |
| **4M** | P4 | `bins_next` | `4,013,036` / `4,013,036` | `0` | `269.302` | `145.123` | `124.178` | `14,901.64` | `14,965.59` | `15,509.52` | `163,588` | `65,137` | `85,544` | `OK (#934)` | `evidence:ab_4m` |

### Bảng 3: Thống kê Kiểm định Cặp (Paired t-test, `n = 4` cặp, `df = 3`) từ `derived_summary.csv`

| Quy mô | Chỉ số (`metric`) | `bins_prev` (`Mean ± SD`) | `bins_next` (`Mean ± SD`) | Chênh lệch (`Diff Mean ± SD`) | `% Thay đổi` | `t_stat` (`df=3`) | `p_two_tailed` | Bằng chứng |
| :---: | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **1M** | `tps_end_to_end` (tx/s) | `16,521.68 ± 363.36` | `16,494.98 ± 173.13` | `-26.70 ± 411.08` | `-0.16%` | `-0.1299` | `0.9049` | `evidence:derived_analysis` |
| **1M** | `tps_active_commit` (tx/s) | `16,858.59 ± 369.42` | `16,839.14 ± 231.66` | `-19.45 ± 481.00` | `-0.12%` | `-0.0809` | `0.9406` | `evidence:derived_analysis` |
| **1M** | `end_to_end_seconds` (s) | `61.04 ± 1.28` | `61.14 ± 0.56` | `+0.11 ± 1.43` | `+0.17%` | `+0.1478` | `0.8919` | `evidence:derived_analysis` |
| **1M** | `inject_seconds` (s) | `4.74 ± 0.54` | `4.63 ± 0.53` | `-0.11 ± 0.66` | `-2.32%` | `-0.3336` | `0.7606` | `evidence:derived_analysis` |
| **1M** | `latency_p50_ms` (ms) | `42,445.75 ± 4,636.87` | `47,161.25 ± 1,500.59` | `+4,715.50 ± 3,367.59` | `+11.11%` | `+2.8005` | `0.0678` | `evidence:derived_analysis` |
| **1M** | `peak_rss_mb_tool` (MB) | `42,148.25 ± 2,314.18` | `42,481.75 ± 1,222.25` | `+333.50 ± 2,773.77` | `+0.79%` | `+0.2405` | `0.8255` | `evidence:derived_analysis` |
| **1M** | **`anomaly_sum` (dòng)** | **`468,629.25 ± 158,158.93`** | **`20,186.75 ± 1,203.95`** | **`-448,442.50 ± 159,160.94`** | **`-95.69%`** | **`-5.6351`** | **`0.011056`** | `evidence:derived_analysis` |
| **4M** | `tps_end_to_end` (tx/s) | `14,573.04 ± 282.74` | `14,630.35 ± 201.95` | `+57.30 ± 401.93` | `+0.39%` | `+0.2851` | `0.7941` | `evidence:derived_analysis` |
| **4M** | `tps_active_commit` (tx/s) | `14,626.28 ± 282.29` | `14,688.92 ± 199.06` | `+62.64 ± 415.69` | `+0.43%` | `+0.3014` | `0.7828` | `evidence:derived_analysis` |
| **4M** | **`tps_during_injection` (tx/s)** | **`13,465.89 ± 182.01`** | **`14,753.22 ± 753.93`** | **`+1,287.33 ± 830.21`** | **`+9.56%`** | **`+3.1012`** | **`0.053244`** | `evidence:derived_analysis` |
| **4M** | `end_to_end_seconds` (s) | `275.22 ± 5.37` | `274.18 ± 3.61` | `-1.04 ± 7.46` | `-0.38%` | `-0.2780` | `0.7990` | `evidence:derived_analysis` |
| **4M** | `inject_seconds` (s) | `24.93 ± 6.14` | `94.23 ± 75.54` | `+69.30 ± 79.54` | `+277.91%` | `+1.7424` | `0.1798` | `evidence:derived_analysis` |
| **4M** | `drain_seconds` (s) | `250.29 ± 5.14` | `179.95 ± 78.36` | `-70.33 ± 81.44` | `-28.10%` | `-1.7273` | `0.1826` | `evidence:derived_analysis` |
| **4M** | `backlog_at_inject_end` (tx) | `3,674,532.75 ± 78,911.73` | `2,596,173.25 ± 1,157,628.34` | `-1,078,359.50 ± 1,207,826.08` | `-29.35%` | `-1.7856` | `0.1721` | `evidence:derived_analysis` |
| **4M** | `latency_p50_ms` (ms) | `156,023.75 ± 7,392.44` | `150,195.75 ± 15,786.47` | `-5,828.00 ± 20,819.83` | `-3.74%` | `-0.5599` | `0.6147` | `evidence:derived_analysis` |
| **4M** | `peak_rss_mb_tool` (MB) | `64,176.25 ± 776.90` | `63,926.25 ± 1,090.32` | `-250.00 ± 1,601.20` | `-0.39%` | `-0.3123` | `0.7753` | `evidence:derived_analysis` |
| **4M** | **`anomaly_sum` (dòng)** | **`870,408.50 ± 145,904.78`** | **`89,605.75 ± 4,843.37`** | **`-780,802.75 ± 141,575.92`** | **`-89.71%`** | **`-11.0302`** | **`0.001596`** | `evidence:derived_analysis` |

---

## 5. Phân tích Cơ chế Backpressure Lớp Sớm (`Connection.readLoop` + `ConnRequestChanSize = 16`) ở Tải 4M Tx

`[ĐO]` So sánh hành vi nhận giao dịch trên từng kết nối validator (`rec=[val0, val1, val2, val3]`) trong các file log `ab_4m` ([ab_4m_next_p1_run1.log](file:///home/abc/chain-n/metanode/note/evidence/readloop_gate_explorer_pacing_20261016/ab_4m/ab_4m_next_p1_run1.log#L40-L75), [ab_4m_next_p2_run1.log](file:///home/abc/chain-n/metanode/note/evidence/readloop_gate_explorer_pacing_20261016/ab_4m/ab_4m_next_p2_run1.log#L40-L65), [ab_4m_next_p4_run1.log](file:///home/abc/chain-n/metanode/note/evidence/readloop_gate_explorer_pacing_20261016/ab_4m/ab_4m_next_p4_run1.log#L40-L75), `evidence:ab_4m`):

1. **Lượng giao dịch tối đa một kết nối bị chặn hấp thụ trước khi `conn.Write()` của client dừng hẳn:**
   - Trong `ab_4m_next_p2` (`inject_seconds = 43.835s`), hai validator `val0` (`31646`) và `val2` (`31648`) đóng cổng tại `HighWatermark = 300,000` và giữ nguyên trạng thái đóng trong suốt pha bơm. Tổng số giao dịch `val0` và `val2` nhận được bị kẹp cứng ở đúng **`423,000` và `414,000` giao dịch** (`300,000` tx trước khi đóng cổng + `16,000` tx trong `ConnRequestChanSize = 16` + `~98,000–107,000` tx trong buffer TCP loopback của kernel) (`evidence:ab_4m`).
   - So với `bins_prev` (nơi một validator bị đóng cổng vẫn nuốt `631,000–651,000` tx hoặc tới `1,000,000` tx do `ConnRequestChanSize = 256` = `256,000` tx), lượng giao dịch đệm phía sau cổng trên mỗi kết nối đã giảm đúng **`~220,000–240,000` tx/validator** như thiết kế lý thuyết (`256k - 16k = 240k`) (`evidence:ab_4m`).
2. **`[TÍNH]` Điều tiết nhịp bơm (`inject_seconds` & `backlog_at_inject_end`) và cải thiện `tps_during_injection` (`+9.56%`, `evidence:derived_analysis`):**
   - Trong `ab_4m_next_p1` (`inject_seconds = 171.404s`) và `ab_4m_next_p4` (`inject_seconds = 145.123s`), cả 4 kết nối đều đóng/mở cổng nhịp nhàng theo chu kỳ hysteresis `300,000 <-> 150,000` trong suốt quá trình bơm, giúp giữ `backlog_at_inject_end` ở mức **`1,452,419` và `1,762,245` giao dịch** (giảm **`-61.6%` và `-51.5%`** so với `3,780,862` và `3,634,611` giao dịch ở `bins_prev`), kéo giảm `drain_seconds` từ `252.1s`/`252.6s` xuống **`102.2s`/`124.2s`** (`evidence:ab_4m`, `evidence:derived_analysis`).
   - Nhờ không bị dồn cục bộ hàng triệu giao dịch vào `conn.RequestChan()` ngay trong 20 giây đầu, thông lượng thực thi trong suốt pha bơm (`tps_during_injection`) của `bins_next` tăng **`+9.56%`** (`13,465.89 -> 14,753.22 tx/s`, `t = +3.1012`, `p = 0.053244`, thắng 4/4 cặp) (`evidence:derived_analysis`).

---

## 6. Giới hạn

1. **Phân phối bất đối xứng phía client `secp_tps_blast` khi một số validator mở cổng sớm hơn (`ab_4m_next_p2`, `ab_4m_next_p3`):**
   - `[ĐO]` Công cụ `secp_tps_blast` đẩy giao dịch qua 4 kết nối TCP độc lập; khi kết nối tới `val0`/`val2` bị chặn `conn.Write()` ở `414k–423k` tx nhưng `val1`/`val3` vừa xả xuống dưới `LowWatermark = 150,000` và mở lại cổng `overloadGate`, client sẽ dồn phần giao dịch còn lại của đợt 4M sang `val1`/`val3` (`1.58M` tx/node ở P2) hoặc khi cả 4 validator cùng xả qua `150,000` trong một nhịp commit lớn (P3, `inject_seconds = 16.567s`) (`evidence:ab_4m`). Đây là hành vi tự nhiên của bộ điều tiết hysteresis per-node (`300k <-> 150k`) khi client bắn tải tổng cố định 4M tx qua 4 kết nối độc lập.
2. **Dòng khớp mẫu `node_anomaly_lines` còn lại (`~20.2k` ở 1M và `~89.6k` ở 4M):**
   - `[ĐO]` Tại 1M tx, `node_anomaly_lines` trên `bins_next` (`20,186.75 ± 1,203.95` dòng trên 4 validator = `~5,046` dòng/validator) hoàn toàn **không còn bất kỳ dòng lỗi `Cannot index transaction` nào** (`0` dòng), mà chỉ là các dòng log thông tin định kỳ của node có chứa từ khóa khớp biểu thức chính quy tổng quát của `run_tps_counted.py` (tương đương mức nền `~30,377` dòng ở báo cáo trước). Tại 4M tx, số dòng giảm `-89.71%` từ `870,408.50` xuống `89,605.75` dòng (`p = 0.001596`) (`evidence:derived_analysis`).

---

## 7. Chưa làm

1. **`[CHƯA ĐO]` Các mục tồn đọng ngoài luồng Backpressure/Explorer trong `note/CLAIMS_LEDGER.md`:**
   - Dòng 8, 9 (thí nghiệm can thiệp trực tiếp trên NOMT `recv_timeout`), dòng 13 (đo lại độc lập bộ tham số GOGC / drift RSS / TTL / fsync từ báo cáo cũ), và dòng 15 (theo dõi thêm lỗi hiếm `Store is poisoned due to prior error` với định dạng log `{:#}` — đến nay đã chạy thêm 16 lượt = 64 validator-run trong phiên 2026-10-16 với `0/64` tệp `nomt_errors.txt` khác rỗng) (`evidence:ab_1m`, `evidence:ab_4m`).
2. **`[CHƯA ĐO]` Tối ưu chi phí ghi B-tree nội tại của Xapian (`_Cfunc_database_index_document` chiếm `45.14s` / `16.69%` CPU ở 4M):**
   - Sau khi `_Cfunc_database_commit` đã giảm `-36.97%` xuống chỉ còn `7.74s` (`2.86%` tổng CPU ở 4M), phần thời gian C++ còn lại của `ExplorerSearchService` nằm ở `wdb->add_document(doc)` (`45.14s`). Nếu cần giảm thêm CPU cho Explorer trong tương lai, có thể khảo sát cấu hình backend Xapian Glass hoặc tắt lập chỉ mục full-text cho các trường không truy vấn (`evidence:derived_analysis`).
