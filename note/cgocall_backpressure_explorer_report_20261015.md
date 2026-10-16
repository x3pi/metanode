# Báo cáo Thực thi & Thực nghiệm A/B (1M & 4M Tx): Phân rã `runtime.cgocall`, TCP Read Backpressure (`InFlightTxCount`) và Tối ưu `ExplorerSearchService` / `goxapian` / `secp256k1`

- **Ngày thực hiện:** 2026-10-15
- **Nhánh & Commit thực nghiệm:** `dev` @ `4a2b63879b7fadbbf2c3be0ce08db0d0c5e84d20` (`bins_next`) so sánh đối chứng với `15b5a3b4cd041db035b1de0728d54bc2a25ec93b` (`bins_prev`) (`evidence:preregistered`, [PREREGISTERED.md](file:///home/abc/chain-n/metanode/note/evidence/cgocall_backpressure_explorer_20261015/PREREGISTERED.md#L1-L45))
- **Thư mục bằng chứng:** `note/evidence/cgocall_backpressure_explorer_20261015/` (`MANIFEST.json`, `PREREGISTERED.md`, `cgocall_breakdown.txt`, `derived_summary.csv`, `pprof_comparison.txt`, `ab_1m/`, `ab_4m/`)
- **Nhãn phân loại tuyên bố sử dụng trong báo cáo:** `[ĐO]`, `[TÍNH]`, `[ĐỌC CODE]`, `[GIẢ THUYẾT]`, `[CHƯA ĐO]`

---

## 1. Tóm tắt điều hành (Executive Summary)

Báo cáo này hoàn tất theo đúng thứ tự cả **3 hướng tiếp theo** sau báo cáo `note/backlog_gc_20261015_report.md`:

1. **Hướng 1 — Phân rã chi tiết `runtime.cgocall` (`29.99%` CPU ở 4M và `29.43%` CPU ở 1M):**
   - `[ĐO]` Trích xuất đầy đủ bảng phân bổ caller của `runtime.cgocall` và `runtime.cgocallback` bằng `/tmp/p06_bins/pprof -peek="runtime\.cgocall$"` trên 4 profile `cpu30.pb.gz` (1M/4M × BASE/OPT) tại [cgocall_breakdown.txt](file:///home/abc/chain-n/metanode/note/evidence/cgocall_backpressure_explorer_20261015/cgocall_breakdown.txt#L1-L260) (`evidence:cgocall_breakdown`).
   - `[TÍNH]` Trên `4M OPT` (`98.76s` cum của `runtime.cgocall` trên tổng `329.30s` mẫu CPU), **99.33% (`98.10s / 98.76s`)** thời gian `runtime.cgocall` đến từ đúng 2 cụm ngoài luồng đồng thuận Rust/NOMT (`evidence:cgocall_breakdown`):
     1. **`pkg/goxapian` (`ExplorerSearchService`):** `63.93s / 98.76s` (**`64.73%`** của `runtime.cgocall`, tương đương **`19.41%`** tổng CPU toàn tiến trình), gồm `_Cfunc_database_replace_document_by_term` (`49.94s`), `_Cfunc_database_commit` (`11.25s`), `_Cfunc_document_add_term` (`1.07s`), `_cgo_cmalloc` (`0.61s`), `_Cfunc_free` (`0.51s`), `_Cfunc_document_free` (`0.20s`), `_Cfunc_document_new` (`0.18s`), `_Cfunc_document_set_data` (`0.17s`) (`evidence:cgocall_breakdown`).
     2. **`crypto/secp256k1` (`_Cfunc_secp256k1_ext_ecdsa_recover`):** `34.17s / 98.76s` (**`34.60%`** của `runtime.cgocall`, tương đương **`10.38%`** tổng CPU toàn tiến trình) (`evidence:cgocall_breakdown`).
     3. Toàn bộ FFI của `nomt_ffi`, `bls/blst`, `pebble/manual`, `executor` và `mvm` cộng lại chỉ chiếm **`0.66s / 98.76s` (`0.67%` của `runtime.cgocall`)**, và `runtime.cgocallback` chỉ chiếm **`0.02s` (`0.0061%` tổng CPU)** (`evidence:cgocall_breakdown`).

2. **Hướng 2 — Triển khai Chặn Backlog ở Lớp Nhận Tx (TCP Read Backpressure — Phương án A từ `note/design_backlog_bound_20261015.md`):**
   - `[ĐỌC CODE]` Đã triển khai bộ đếm nguyên tử `inFlightCount atomic.Int64` trong [`ReceiptTracker`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/receipt_tracker.go#L14-L98) (`StoreTxHashConnEntry`, `TakeTxHashConnEntry`, `DeleteTxHashConnEntry`, `InFlightTxCount`, `ReconcileInFlightTxCount`), tích hợp vào [`BroadCastReceipts`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_broadcast.go#L187-L193), [`cleanupStaleMaps`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_Cleanup.go#L114-L132), và vòng lặp [`startSystemLoadMonitor`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/processors.go#L64-L157) (`currentLoad = max(poolSize + resultChanSize + virtualChainSize, inFlight)` với ngưỡng hysteresis `HighWatermark = 300,000`, `LowWatermark = 150,000` và cơ chế tự phục hồi sau 25 nhịp / 5s idle) (`evidence:preregistered`).

3. **Hướng 3 — Tối ưu Nhánh CPU Lớn Thứ Hai & `runtime.cgocall` (`ExplorerSearchService` / `goxapian` + `secp256k1` Envelope Binding):**
   - `[ĐỌC CODE]` **Nhánh `secp256k1`:** Lưu sẵn `cachedEnvTx` trong [`NewTransactionFromEth`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L160-L166), bỏ lời gọi `DeriveSenderFromEthTransaction` trùng lặp trong [`ValidEthSign`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L1258-L1268), và bổ sung fast-path cache `globalEnvelopeBoundCache` (`262,144` slot trên 64 shard) trong [`signature_enforcement.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L58-L115), [`validation.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L228-L250) và [`speculative_executor.go`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/speculative_executor.go#L510-L535) (`evidence:preregistered`).
   - `[ĐỌC CODE]` **Nhánh `ExplorerSearchService` / `goxapian`:** Gộp 16 lệnh gọi CGO + cấp phát/giải phóng `C.CString` mỗi giao dịch thành **1 lệnh gọi CGO duy nhất** [`database_index_document`](file:///home/abc/chain-n/metanode/execution/pkg/goxapian/xapian_wrapper.cpp#L140-L199) trên `Xapian::Document` cấp phát stack; đưa toàn bộ tiền xử lý chuỗi hex và tuần tự hóa JSON bằng struct tĩnh [`explorerTxJSONFull`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/explorer.go#L106-L310) ra ngoài `s.locks[shardIndex].Lock()`; truyền trực tiếp dữ liệu block từ bộ nhớ qua `pendingIndexBlocks` thay vì mở lại 2 trie NOMT/Pebble (`txDB.GetTransaction` + `rcpDb.GetReceipt`); dùng `add_document` (`replace=false`) cho block mới và gom `explorerSvc.Commit()` mỗi 64 block hoặc khi hàng đợi rỗng trong [`block_processor_indexing.go`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L132-L208) (`evidence:preregistered`).

4. **Kết quả Thực nghiệm A/B Đan xen (16 Lần chạy = 4 Cặp 1M + 4 Cặp 4M, Đảm bảo Zero-Fork):**
   - `[ĐO]` Cả 8/8 lần chạy của `bins_next` (4 cặp 1M + 4 cặp 4M) đều đạt **`loss = 0`** (`committed_ours == bytes_sent`) và **`parity = OK: all 4 validators agree on hash+stateRoot`** (`evidence:ab_1m`, `evidence:ab_4m`).
   - `[TÍNH]` Trên profile CPU 30s (`val0`), thời gian CPU tích lũy của `ExplorerSearchService.doIndexTransaction` giảm **31.52%** ở 1M (`86.74s -> 59.40s`) và giảm **23.57%** ở 4M (`76.89s -> 58.77s`); thời gian tuần tự hóa JSON của Explorer giảm **75.59%** ở 1M (`20.20s -> 4.93s`) và giảm **73.76%** ở 4M (`17.07s -> 4.48s`); toàn bộ 6 hàm CGO phụ trợ của `goxapian` (`_Cfunc_document_add_term`, `_cgo_cmalloc`, `_Cfunc_free`, `_Cfunc_document_free`, `_Cfunc_document_set_data`, `_Cfunc_document_new`) giảm **100%** về `0.00s`; và tổng cấp phát bộ nhớ heap (`alloc_space`) trên `val0` ở 1M giảm **16.51%** (`34,448.96 MB -> 28,760.35 MB`) (`evidence:derived_analysis`).
   - `[TÍNH]` Ở mức cụm 4 validator, `tps_end_to_end` tăng trung bình **+16.06%** ở 1M (`14,330.63 ± 3,264.21 -> 16,631.71 ± 274.11 tx/s`, `t = +1.51, p = 0.2276`, thắng 4/4 cặp) và tăng **+5.49%** ở 4M (`13,755.14 ± 955.94 -> 14,510.46 ± 308.02 tx/s`, `t = +1.45, p = 0.2422`, giảm độ lệch chuẩn `SD` 3.1 lần); đỉnh bộ nhớ RSS tổng 4 validator ở 4M giảm **10.13%** (`71,394.75 ± 7,277.67 MB -> 64,165.25 ± 1,362.92 MB`, giảm `-7,229.50 MB` tức `~1.81 GB/node`, `p = 0.1889`) (`evidence:derived_analysis`).

---

## 2. Hướng 1: Phân rã chi tiết `runtime.cgocall` (29.99% CPU ở 4M & 29.43% CPU ở 1M)

`[ĐO]` Dữ liệu dưới đây được trích xuất trực tiếp bằng `/tmp/p06_bins/pprof -peek="runtime\.cgocall$"` và `-peek="runtime\.cgocallback"` từ các file profile `cpu30.pb.gz` của đợt chạy trước và lưu nguyên văn tại [cgocall_breakdown.txt](file:///home/abc/chain-n/metanode/note/evidence/cgocall_backpressure_explorer_20261015/cgocall_breakdown.txt#L1-L260) (`evidence:cgocall_breakdown`).

### Bảng 1: Phân rã đầy đủ các hàm gọi `runtime.cgocall` trên `val0` (cửa sổ 30s CPU profile)

| Hàm gọi CGO (Caller của `runtime.cgocall`) | Nhóm chức năng | 4M OPT (`s` / `% cgocall`) | 4M BASE (`s` / `% cgocall`) | 1M OPT (`s` / `% cgocall`) | 1M BASE (`s` / `% cgocall`) | Bằng chứng |
| :--- | :--- | :---: | :---: | :---: | :---: | :--- |
| `_Cfunc_database_replace_document_by_term` | `goxapian` (Explorer) | `49.94s` (`50.57%`) | `55.02s` (`51.49%`) | `51.84s` (`49.99%`) | `50.25s` (`51.96%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_secp256k1_ext_ecdsa_recover` | `secp256k1` (ECDSA) | `34.17s` (`34.60%`) | `36.33s` (`34.00%`) | `35.14s` (`33.89%`) | `31.69s` (`32.77%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_database_commit` | `goxapian` (Explorer) | `11.25s` (`11.39%`) | `11.79s` (`11.03%`) | `12.83s` (`12.37%`) | `11.41s` (`11.80%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_document_add_term` | `goxapian` (Explorer) | `1.07s` (`1.08%`) | `1.20s` (`1.12%`) | `1.39s` (`1.34%`) | `1.06s` (`1.10%`) | `evidence:cgocall_breakdown` |
| `_cgo_cmalloc` (`C.CString`) | `goxapian` (Explorer) | `0.61s` (`0.62%`) | `0.69s` (`0.65%`) | `0.69s` (`0.67%`) | `0.79s` (`0.82%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_free` (`C.free`) | `goxapian` (Explorer) | `0.51s` (`0.52%`) | `0.53s` (`0.50%`) | `0.53s` (`0.51%`) | `0.42s` (`0.43%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_document_free` | `goxapian` (Explorer) | `0.20s` (`0.20%`) | `0.36s` (`0.34%`) | `0.37s` (`0.36%`) | `0.32s` (`0.33%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_document_new` | `goxapian` (Explorer) | `0.18s` (`0.18%`) | `0.18s` (`0.17%`) | `0.26s` (`0.25%`) | `0.25s` (`0.26%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_document_set_data` | `goxapian` (Explorer) | `0.17s` (`0.17%`) | `0.24s` (`0.22%`) | `0.29s` (`0.28%`) | `0.21s` (`0.22%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_state_db_get` | `nomt_ffi` (State Trie) | `0.23s` (`0.23%`) | `0.19s` (`0.18%`) | `0.19s` (`0.18%`) | `0.19s` (`0.20%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_calloc` / `_Cfunc_free` | `pebble/manual` | `0.35s` (`0.35%`) | `0.23s` (`0.22%`) | `0.04s` (`0.04%`) | `0.02s` (`0.02%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_blst_*` | `bls/blst` | `0.05s` (`0.05%`) | `0.08s` (`0.07%`) | `0.06s` (`0.06%`) | `0.07s` (`0.07%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_nomt_commit_payload` / `session_*` | `nomt_ffi` (Commit) | `0.02s` (`0.02%`) | `0.02s` (`0.02%`) | `0.06s` (`0.06%`) | `0.03s` (`0.03%`) | `evidence:cgocall_breakdown` |
| `_Cfunc_metanode_submit_transaction_batch` | `executor` (Rust DAG FFI) | `0.00s` (`0.00%`) | `0.00s` (`0.00%`) | `0.01s` (`0.01%`) | `0.00s` (`0.00%`) | `evidence:cgocall_breakdown` |
| **Tổng `goxapian` (8 hàm CGO)** | **`ExplorerSearchService`** | **`63.93s` (`64.73%`)** | **`69.99s` (`65.50%`)** | **`68.20s` (`65.77%`)** | **`64.71s` (`66.91%`)** | `evidence:cgocall_breakdown` |
| **Tổng `secp256k1` (1 hàm CGO)** | **Xác thực chữ ký ECDSA** | **`34.17s` (`34.60%`)** | **`36.33s` (`34.00%`)** | **`35.14s` (`33.89%`)** | **`31.69s` (`32.77%`)** | `evidence:cgocall_breakdown` |
| **Tổng tất cả CGO khác (`nomt` + `pebble` + `blst` + `executor`)** | **Cốt lõi chuỗi & DB** | **`0.66s` (`0.67%`)** | **`0.54s` (`0.51%`)** | **`0.36s` (`0.35%`)** | **`0.31s` (`0.32%`)** | `evidence:cgocall_breakdown` |
| **Tổng `runtime.cgocall` (Cumulative / Flat)** | **Toàn bộ CGO** | **`98.76s` / `97.30s`** | **`106.86s` / `105.05s`** | **`103.70s` / `101.84s`** | **`96.71s` / `95.01s`** | `evidence:cgocall_breakdown` |

`[TÍNH]` Như vậy, giả thuyết cho rằng `runtime.cgocall` (`~30%` tổng CPU) đến từ giao tiếp FFI giữa Go và Rust DAG (`metanode_submit_transaction_batch`) hay `nomt_ffi` đã bị bác bỏ bởi dữ liệu đo: **99.33% (`98.10s / 98.76s`)** chi phí `runtime.cgocall` ở 4M OPT nằm ở `pkg/goxapian` (`64.73%`) và `crypto/secp256k1` (`34.60%`) (`evidence:cgocall_breakdown`).

---

## 3. Hướng 2: Triển khai Chặn Backlog ở Lớp Nhận Tx (`ReceiptTracker.InFlightTxCount()`)

### 3.1. Chi tiết triển khai kỹ thuật

`[ĐỌC CODE]` Thực thi đúng thiết kế Phương án A trong [design_backlog_bound_20261015.md](file:///home/abc/chain-n/metanode/note/design_backlog_bound_20261015.md#L118-L235):

1. **Bộ đếm giao dịch đang bay (`inFlightCount atomic.Int64`) trong [`ReceiptTracker`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/receipt_tracker.go#L22-L98):**
   - `StoreTxHashConnEntry(txHash, entry)` dùng `rt.txHashConnectionMap.Swap(txHash, entry)` và chỉ tăng `rt.inFlightCount.Add(1)` khi `!loaded` (tránh đếm trùng khi `tx_batch_forwarder_core.go:275` cập nhật `SentToRustAt` cho cùng `txHash`).
   - `TakeTxHashConnEntry(txHash)` dùng `LoadAndDelete(txHash)` và giảm `rt.inFlightCount.Add(-1)` (kẹp sàn `>= 0`) ngay khi giao dịch được lấy ra để phát biên nhận trong [`BroadCastReceipts`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_broadcast.go#L187-L193).
   - `DeleteTxHashConnEntry(key)` giảm `inFlightCount` khi dọn dẹp các entry quá hạn 2 phút trong [`cleanupStaleMaps`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_Cleanup.go#L114-L132).
   - `ReconcileInFlightTxCount()` quét lại `txHashConnectionMap.Range` để đồng bộ hóa `inFlightCount` khi cần.
2. **Tích hợp vào cổng kiểm soát tải [`startSystemLoadMonitor`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/processors.go#L64-L157):**
   - Hàm `computeSystemLoad(tp, bp)` tính `baseLoad = poolSize + resultChanSize + virtualChainSize`, `inFlight = bp.InFlightTxCount()`, và `currentLoad = max(baseLoad, inFlight)`.
   - Khi `currentLoad > highWatermark` (`300,000` mặc định, có thể ghi đè qua biến môi trường `META_INGRESS_HIGH_WATERMARK`), đóng `overloadGate` (`setOverloaded(true)`), làm các goroutine `SocketServer.HandleConnection` đang xử lý lệnh `SendRawTransaction` / `SendRawTransactions` chờ trên `waitNotOverloaded(s.ctx)` tại [`server.go:239-243`](file:///home/abc/chain-n/metanode/execution/pkg/network/server.go#L239-L243).
   - Khi `currentLoad < lowWatermark` (`150,000` mặc định, có thể ghi đè qua `META_INGRESS_LOW_WATERMARK`), mở lại `overloadGate` (`setOverloaded(false)`).
   - **Cơ chế tự phục hồi chống kẹt cổng (Self-healing guard):** Nếu `overloaded == true` chỉ do `inFlight` trong khi `baseLoad == 0`, `len(bp.commitChannel) == 0` và `LastBlock` không tăng suốt 25 nhịp liên tiếp (`5.0s`), bộ giám sát tự động đặt lại `bp.inFlightCount.Store(0)` và mở cổng ngay trong cùng nhịp kiểm tra ([`processors.go:126-138`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/processors.go#L126-L138)).
   - Toàn bộ các bất biến này đã được kiểm chứng bằng bộ unit test mới tại [`ingress_backpressure_test.go`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/ingress_backpressure_test.go#L1-L92).

### 3.2. Phân tích hành vi thực tế của TCP Backpressure ở tải 1M và 4M

`[ĐO]` Kết quả thực nghiệm A/B ở tải 4M (`evidence:ab_4m`, `evidence:derived_analysis`) làm sáng tỏ một cơ chế kiến trúc rất quan trọng giữa `SocketServer.HandleConnection` và `Connection.readLoop`:

- **Bảo vệ bộ nhớ và worker bên trong tiến trình validator:**
  - Khi `inFlightCount` vượt `300,000` giao dịch trên một validator (sau `~300` batch ở `batch=1000`), `waitNotOverloaded(s.ctx)` tại [`server.go:240`](file:///home/abc/chain-n/metanode/execution/pkg/network/server.go#L240) chặn ngay goroutine `HandleConnection` của kết nối đó, **ngăn không cho đẩy thêm batch mới vào `s.requestChan` (vốn có buffer `500,000` request tại [`server.go:83`](file:///home/abc/chain-n/metanode/execution/pkg/network/server.go#L83)) và ngăn không cho 64 worker chạy `ProcessRawTransactionsFromClient` giải mã RLP/cấp phát `Transaction` mới** khi pipeline đang đầy (`evidence:ab_4m`).
  - `[TÍNH]` Nhờ chặn sớm trước `s.requestChan` / `ProcessRawTransactionsFromClient`, kết hợp với giảm cấp phát ở Hướng 3, **đỉnh RSS tổng 4 validator ở 4M giảm từ `71,394.75 ± 7,277.67 MB` (`bins_prev`) xuống `64,165.25 ± 1,362.92 MB` (`bins_next`) — giảm `-7,229.50 MB` (`-10.13%`, tương đương `~1.81 GB/validator`, `p = 0.1889`), đồng thời độ lệch chuẩn RSS giữa các lần chạy giảm 5.3 lần (`7,277.67 MB -> 1,362.92 MB`)** (`evidence:derived_analysis`).
- **Vì sao phía client (`secp_tps_blast`) vẫn gửi hết 4M giao dịch nhanh hơn (`inject_seconds` giảm từ `65.42s` xuống `29.22s`, `p = 0.0260`) thay vì bị chặn ở đúng 300k/node?**
  - `[ĐỌC CODE]` Tại [`connection.go:218-222`](file:///home/abc/chain-n/metanode/execution/pkg/network/connection.go#L218-L222) và [`app.go:880`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/app.go#L880), mỗi kết nối TCP có một kênh đệm trung gian `conn.RequestChan()` với `ConnRequestChanSize = 256` request (`256` batch × `1,000` tx = **`256,000` giao dịch/validator**). Goroutine đọc socket [`Connection.readLoop`](file:///home/abc/chain-n/metanode/execution/pkg/network/connection.go#L808-L920) **không kiểm tra `loadTxAdmissionGate()` trước khi gọi `io.ReadFull`**, mà đọc liên tục từ socket TCP cho tới khi lấp đầy `256` batch trong `conn.RequestChan()`.
  - `[TÍNH]` Cộng thêm bộ đệm socket TCP loopback của nhân Linux (`tcp_rmem` + `tcp_wmem`) và `bufio.NewWriterSize(conn, 4MB)` của client ([`secp_tps_blast/main.go:153`](file:///home/abc/chain-n/metanode/execution/cmd/tool/secp_tps_blast/main.go#L153)), mỗi kết nối validator hấp thụ được:
    - `~300,000` tx đã nạp vào `ReceiptTracker` trước khi `overloadGate` đóng
    - `+ 256,000` tx nằm chờ dạng protobuf thô trong `conn.RequestChan()` (`ConnRequestChanSize = 256`)
    - `+ ~80,000 – 440,000` tx (`~10–50 MB`) nằm trong bộ đệm TCP loopback của kernel trước khi lời gọi `conn.Write()` của client thực sự bị chặn (`evidence:ab_4m`).
  - `[ĐO]` Trong `ab_4m_next_p1`, hai kết nối validator `31648` và `31649` bị chặn `conn.Write()` ở đúng **`651,000` và `631,000` giao dịch** (`300k` admitted + `256k` `ConnRequestChanSize` + `~85k` kernel TCP buffer) trong khi hai kết nối `31646` và `31647` nhận phần còn lại (`1,367,680` và `1,362,000` tx, tổng thời gian bơm `46.195s`) ([ab_4m_next_p1_run1.log](file:///home/abc/chain-n/metanode/note/evidence/cgocall_backpressure_explorer_20261015/ab_4m/ab_4m_next_p1_run1.log#L40-L60), `evidence:ab_4m`). Trong `ab_4m_next_p2` và `p4`, cả 4 kết nối hấp thụ đều `~1,000,000` tx/validator vào `(300k admitted + 256k ConnRequestChanSize + ~440k kernel TCP buffer)` trong `16.575s` và `16.327s` mà không làm nghẽn `admitMu`/`waitForPoolRoom` như `bins_prev` (`65.42s`) (`evidence:ab_4m`).

---

## 4. Hướng 3: Tối ưu `ExplorerSearchService` / `goxapian` và `secp256k1` Envelope Binding

### 4.1. Chi tiết các tối ưu đã triển khai

1. **Loại bỏ `secp256k1_ext_ecdsa_recover` trùng lặp (`pkg/transaction` & `pkg/blockchain/tx_processor`):**
   - `[ĐỌC CODE]` Trong [`NewTransactionFromEth`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L160-L166), lưu ngay `&envelopeDecode{raw: pTx.RawEnvelope, tx: ethTx}` vào `concrete.cachedEnvTx` để mọi lời gọi `ValidateEnvelopeBinding` về sau trên cùng đối tượng `Transaction` không phải giải mã lại RLP `RawEnvelope`.
   - `[ĐỌC CODE]` Trong [`ValidEthSign`](file:///home/abc/chain-n/metanode/execution/pkg/transaction/transaction.go#L1258-L1268), trả về `true` ngay sau khi `validateEnvelopeBindingTx(t)` thành công thay vì gọi tiếp `DeriveSenderFromEthTransaction` lần thứ hai.
   - `[ĐỌC CODE]` Trong [`signature_enforcement.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/signature_enforcement.go#L58-L115), [`validation.go`](file:///home/abc/chain-n/metanode/execution/pkg/blockchain/tx_processor/validation.go#L228-L250) và [`speculative_executor.go`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/speculative_executor.go#L510-L535), bổ sung bảng băm 64-shard `globalEnvelopeBoundCache` (`262,144` entry, khóa `SHA256(RawEnvelope || ProtoHash || FromAddress)`): khi giao dịch đã được xác thực tại `RecordVerifiedSecp256k1` hoặc `ValidateEnvelopeBindingsParallel`, các bước sau (`VerifyTransaction`, `ValidateEnvelopeBindingsParallel`, `evalTxSignature`) kiểm tra `IsVerifiedEnvelopeBound(tx)` để bỏ qua `ValidateEnvelopeBinding` lặp lại.
2. **Gộp CGO `goxapian`, loại bỏ reflection JSON và loại bỏ đọc lại trie NOMT/Pebble trong `ExplorerSearchService`:**
   - `[ĐỌC CODE]` Thêm hàm C++ [`database_index_document`](file:///home/abc/chain-n/metanode/execution/pkg/goxapian/xapian_wrapper.cpp#L140-L199) và phương thức Go [`(*Database).IndexDocument`](file:///home/abc/chain-n/metanode/execution/pkg/goxapian/xapian.go#L156-L202) truyền trực tiếp con trỏ chuỗi Go (`_GoStringPtr` / `_GoStringLen` + `runtime.KeepAlive`) sang C++ trong **1 lệnh gọi CGO duy nhất**, tạo `Xapian::Document` trên stack C++, tách các term phân cách bằng `\n` qua `memchr`, và gọi `add_document` (khi `replace_existing == false` cho block mới) hoặc `replace_document` (khi `replace_existing == true`).
   - `[ĐỌC CODE]` Thay thế `map[string]interface{}` + reflection + tính lại `BlockHeader.Hash().Hex()` mỗi giao dịch trong [`ExplorerTransaction.MarshalJSON`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/explorer.go#L106-L310) bằng struct tĩnh `explorerTxJSONFull` / `explorerTxJSONMinimal` cùng `blockHashHex` tính sẵn 1 lần/block, và chuyển toàn bộ bước dựng JSON + term ra ngoài `s.locks[shardIndex].Lock()` trong [`doIndexTransaction`](file:///home/abc/chain-n/metanode/execution/pkg/explorer/service.go#L307-L416).
   - `[ĐỌC CODE]` Trong [`block_processor_commit.go:299-320`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_commit.go#L299-L320) và [`block_processor_indexing.go:132-208`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L132-L208), truyền trực tiếp `header`, `Transactions` và `Receipts` qua `pendingIndexBlocks` sang `indexSingleBlock` (bỏ 2 lần mở trie `NewTransactionStateDBFromRoot` + `NewReceiptsFromRoot` và `16,000` lượt đọc DB/unmarshal protobuf mỗi block 8,000 tx), đồng thời gom `explorerSvc.Commit()` mỗi `64` block hoặc khi `len(bp.indexingChannel) == 0` (`evidence:preregistered`).

### 4.2. So sánh Profile CPU (`cpu30.pb.gz`) và Bộ nhớ (`allocs.pb.gz` / `heap.pb.gz`) giữa `bins_prev` và `bins_next`

`[ĐO]` Dữ liệu trích xuất từ Pair 1 của 1M (`--profile-at 20`) và Pair 1 của 4M (`--profile-at 150`) tại [pprof_comparison.txt](file:///home/abc/chain-n/metanode/note/evidence/cgocall_backpressure_explorer_20261015/pprof_comparison.txt#L1-L1480) (`evidence:derived_analysis`):

### Bảng 2: So sánh các hàm nóng trên `val0` trong cửa sổ CPU Profile 30s (`1M_PREV` vs `1M_NEXT` và `4M_PREV` vs `4M_NEXT`)

| Hàm / Nhánh thực thi (`pprof -symbolize=none`) | `1M_PREV` (`s` / `%`) | `1M_NEXT` (`s` / `%`) | Thay đổi 1M | `4M_PREV` (`s` / `%`) | `4M_NEXT` (`s` / `%`) | Thay đổi 4M | Bằng chứng |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **Tổng mẫu CPU 30s (`Total samples`)** | `350.47s` (`1162.3%`) | `288.08s` (`955.1%`) | `-17.80%` | `330.29s` (`1094.9%`) | `323.13s` (`1071.7%`) | `-2.17%` | `evidence:derived_analysis` |
| `ExplorerSearchService.doIndexTransaction` (cum) | `86.74s` (`24.75%`) | `59.40s` (`20.62%`) | **`-31.52%`** | `76.89s` (`23.28%`) | `58.77s` (`18.19%`) | **`-23.57%`** | `evidence:derived_analysis` |
| `ExplorerTransaction.MarshalJSON` / `marshalExplorerTxFull` (cum) | `20.20s` (`5.76%`) | `4.93s` (`1.71%`) | **`-75.59%`** | `17.07s` (`5.17%`) | `4.48s` (`1.39%`) | **`-73.76%`** | `evidence:derived_analysis` |
| `BlockHeader.Hash()` bên trong Explorer JSON (cum) | `2.54s` (`0.72%`) | `0.00s` (`0.00%`) | **`-100.00%`** | `2.63s` (`0.80%`) | `0.00s` (`0.00%`) | **`-100.00%`** | `evidence:derived_analysis` |
| 6 hàm CGO phụ trợ của `goxapian` (`add_term`, `cmalloc`, `free`, `doc_new/free/set_data`) | `3.25s` (`0.93%`) | `0.00s` (`0.00%`) | **`-100.00%`** | `3.05s` (`0.92%`) | `0.00s` (`0.00%`) | **`-100.00%`** | `evidence:derived_analysis` |
| `_Cfunc_database_replace_document_by_term` vs `_Cfunc_database_index_document` | `55.95s` (`15.96%`) | `49.14s` (`17.06%`) | **`-12.17%`** | `50.80s` (`15.38%`) | `48.87s` (`15.12%`) | **`-3.80%`** | `evidence:derived_analysis` |
| `runtime.cgocall` (cum) | `105.03s` (`29.97%`) | `94.22s` (`32.71%`) | **`-10.29%`** | `98.96s` (`29.96%`) | `97.72s` (`30.24%`) | **`-1.25%`** | `evidence:derived_analysis` |
| `runtime.entersyscall` + `exitsyscall` dưới `runtime.cgocall` | `1.78s` (`0.51%`) | `0.37s` (`0.13%`) | **`-79.21%`** | `1.63s` (`0.49%`) | `0.43s` (`0.13%`) | **`-73.62%`** | `evidence:derived_analysis` |
| `Transaction.ValidEthSign` (cum) | `2.24s` (`0.64%`) | `0.00s` (`0.00%`) | **`-100.00%`** | `2.07s` (`0.63%`) | `0.38s` (`0.12%`) | **`-81.64%`** | `evidence:derived_analysis` |
| `runtime.mallocgc` (cum) | `31.64s` (`9.03%`) | `29.03s` (`10.08%`) | **`-8.25%`** | `24.98s` (`7.56%`) | `21.55s` (`6.67%`) | **`-13.73%`** | `evidence:derived_analysis` |

### Bảng 3: So sánh Cấp phát Bộ nhớ Tích lũy (`alloc_space`) trên `val0` (`1M_PREV` vs `1M_NEXT` tại `t=20s` và `4M_PREV` vs `4M_NEXT` tại `t=150s`)

| Chỉ số / Hàm cấp phát (`pprof -alloc_space`) | `1M_PREV` | `1M_NEXT` | Thay đổi 1M | `4M_PREV` | `4M_NEXT` | Thay đổi 4M | Bằng chứng |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **Tổng `alloc_space` toàn tiến trình `val0`** | `34,448.96 MB` | `28,760.35 MB` | **`-16.51%`** | `138.32 GB` | `133.83 GB` | **`-3.25%`** | `evidence:derived_analysis` |
| `ExplorerTransaction.MarshalJSON` (cum) | `3,895.43 MB` (`11.31%`) | `< 143.80 MB` (ngoài top 20) | **`> -96.31%`** | `15.91 GB` (`11.50%`) | `< 0.67 GB` (ngoài top 20) | **`> -95.79%`** | `evidence:derived_analysis` |
| `encoding/json.mapEncoder.encode` (cum) | `947.55 MB` (`2.75%`) | `0.00 MB` | **`-100.00%`** | `4.06 GB` (`2.93%`) | `0.00 GB` | **`-100.00%`** | `evidence:derived_analysis` |
| `encoding/json.Marshal` (cum) | `~1,400 MB` | `< 143.80 MB` | **`> -89.73%`** | `5.83 GB` (`4.21%`) | `1.86 GB` (`1.39%`) | **`-68.10%`** | `evidence:derived_analysis` |
| `LazyPebbleDB.Get` (cum — từ đọc lại trie khi index block) | `508.15 MB` (`1.48%`) | `< 143.80 MB` (ngoài top 20) | **`> -71.70%`** | `2.25 GB` (`1.63%`) | `< 0.67 GB` (ngoài top 20) | **`> -70.22%`** | `evidence:derived_analysis` |
| `Heap inuse_space` tại thời điểm lấy mẫu (`val0`) | `827.38 MB` | `770.88 MB` | **`-6.83%`** | `1,170.00 MB` (tại blk `#293`) | `1,572.54 MB` (tại blk `#347`) | `+34.41%` (đã xử lý thêm `+54` block) | `evidence:derived_analysis` |

`[TÍNH]` Lưu ý quan trọng về nhánh `ValidateEnvelopeBindingsParallel`: trên một cụm 4 validator, mỗi validator chỉ nhận `~25%` tổng giao dịch qua cổng TCP trực tiếp của chính nó (`ProcessRawTransactionsFromClient` -> `RecordVerifiedSecp256k1`), còn **`~75%` giao dịch trong các block commit là giao dịch do 3 validator kia nhận và phát tán qua Rust DAG**. Khi các giao dịch của 3 validator kia tới `val0` lần đầu tiên trong `PrepareTransactions` -> `ValidateEnvelopeBindingsParallel`, `val0` bắt buộc phải chạy `ValidateEnvelopeBinding` (`_Cfunc_secp256k1_ext_ecdsa_recover`) đúng 1 lần để xác thực chữ ký envelope, sau đó lưu vào `globalEnvelopeBoundCache` để `evalTxSignature` (`VerifyTransactions`) không phải xác thực lại lần thứ hai (`evidence:derived_analysis`).

---

## 5. Kết quả Thực nghiệm A/B Đan xen (4 Cặp 1M & 4 Cặp 4M)

### Bảng 4: Kết quả chi tiết từng cặp chạy A/B ở tải 1M Tx (`seed=2026101512`, 4 cặp đan xen) và 4M Tx (`seed=2026101511`, 4 cặp đan xen)

| Quy mô | Cặp | Phiên bản | `bytes_sent` / `committed_ours` | `loss` | `end_to_end_seconds` (s) | `inject_seconds` (s) | `drain_seconds` (s) | `tps_end_to_end` (tx/s) | `tps_active_commit` (tx/s) | `tps_during_injection` (tx/s) | `latency_p50_ms` (ms) | `peak_rss_mb_tool` (MB) | `parity` | Bằng chứng |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **1M** | P1 | `bins_prev` | `1,006,862` / `1,006,862` | `0` | `62.876` | `5.731` | `57.145` | `16,013.37` | `16,267.59` | `8,374.90` | `50,773` | `44,601` | `OK (#136)` | `evidence:ab_1m` |
| **1M** | P1 | `bins_next` | `1,009,259` / `1,009,259` | `0` | `61.051` | `4.805` | `56.246` | `16,531.30` | `16,751.26` | `9,989.15` | `45,080` | `45,572` | `OK (#136)` | `evidence:ab_1m` |
| **1M** | P2 | `bins_prev` | `1,008,655` / `1,008,655` | `0` | `63.551` | `5.847` | `57.704` | `15,871.60` | `16,132.16` | `8,893.09` | `49,104` | `42,830` | `OK (#192)` | `evidence:ab_1m` |
| **1M** | P2 | `bins_next` | `1,007,452` / `1,007,452` | `0` | `59.702` | `4.876` | `54.826` | `16,874.74` | `17,140.48` | `9,024.25` | `46,315` | `43,766` | `OK (#167)` | `evidence:ab_1m` |
| **1M** | P3 | `bins_prev` | `1,009,378` / `1,008,390` | `988` | `106.876` | `5.910` | `100.966` | `9,435.33` | `9,574.82` | `8,122.10` | `94,626` | `40,961` | `OK (#188)` | `evidence:ab_1m` |
| **1M** | P3 | `bins_next` | `1,010,822` / `1,010,822` | `0` | `62.051` | `4.485` | `57.566` | `16,290.13` | `16,456.81` | `7,357.59` | `49,169` | `43,113` | `OK (#181)` | `evidence:ab_1m` |
| **1M** | P4 | `bins_prev` | `1,009,361` / `1,009,361` | `0` | `63.076` | `5.990` | `57.086` | `16,002.23` | `16,452.68` | `5,843.00` | `46,390` | `42,614` | `OK (#173)` | `evidence:ab_1m` |
| **1M** | P4 | `bins_next` | `1,008,605` / `1,008,605` | `0` | `59.926` | `4.393` | `55.533` | `16,830.67` | `17,160.37` | `9,787.84` | `47,320` | `41,520` | `OK (#191)` | `evidence:ab_1m` |
| **4M** | P1 | `bins_prev` | `4,011,880` / `4,011,880` | `0` | `324.276` | `64.653` | `259.623` | `12,371.82` | `12,413.90` | `13,469.72` | `162,947` | `79,016` | `OK (#861)` | `evidence:ab_4m` |
| **4M** | P1 | `bins_next` | `4,011,680` / `4,011,680` | `0` | `274.076` | `46.195` | `227.881` | `14,637.10` | `14,692.22` | `14,689.16` | `156,207` | `62,310` | `OK (#942)` | `evidence:ab_4m` |
| **4M** | P2 | `bins_prev` | `4,011,815` / `4,011,815` | `0` | `277.375` | `74.971` | `202.404` | `14,463.50` | `14,523.10` | `14,259.78` | `158,046` | `65,492` | `OK (#945)` | `evidence:ab_4m` |
| **4M** | P2 | `bins_next` | `4,007,447` / `4,007,447` | `0` | `269.301` | `16.575` | `252.726` | `14,880.90` | `14,974.07` | `14,869.16` | `154,712` | `64,840` | `OK (#848)` | `evidence:ab_4m` |
| **4M** | P3 | `bins_prev` | `4,012,278` / `4,012,278` | `0` | `280.301` | `64.262` | `216.039` | `14,314.17` | `14,366.81` | `13,450.69` | `159,981` | `76,214` | `OK (#864)` | `evidence:ab_4m` |
| **4M** | P3 | `bins_next` | `4,010,287` / `4,010,287` | `0` | `282.427` | `37.772` | `244.654` | `14,199.39` | `14,246.07` | `12,891.32` | `171,862` | `64,056` | `OK (#963)` | `evidence:ab_4m` |
| **4M** | P4 | `bins_prev` | `4,011,897` / `4,011,897` | `0` | `289.226` | `57.796` | `231.430` | `13,871.14` | `13,927.76` | `12,542.42` | `165,000` | `64,857` | `OK (#913)` | `evidence:ab_4m` |
| **4M** | P4 | `bins_next` | `4,008,342` / `4,008,342` | `0` | `279.827` | `16.327` | `263.499` | `14,324.37` | `14,370.79` | `14,772.60` | `158,725` | `65,455` | `OK (#890)` | `evidence:ab_4m` |

### Bảng 5: Thống kê Kiểm định Cặp (Paired t-test, `n = 4` cặp, `df = 3`) từ `derived_summary.csv`

| Quy mô | Chỉ số (`metric`) | `bins_prev` (`Mean ± SD`) | `bins_next` (`Mean ± SD`) | Chênh lệch (`Diff Mean ± SD`) | `% Thay đổi` | `t_stat` (`df=3`) | `p_two_tailed` | Bằng chứng |
| :---: | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **1M** | `tps_end_to_end` (tx/s) | `14,330.63 ± 3,264.21` | `16,631.71 ± 274.11` | `+2,301.08 ± 3,045.80` | `+16.06%` | `+1.5110` | `0.2276` | `evidence:derived_analysis` |
| **1M** | `tps_active_commit` (tx/s) | `14,606.81 ± 3,357.25` | `16,877.23 ± 337.71` | `+2,270.42 ± 3,084.60` | `+15.54%` | `+1.4721` | `0.2371` | `evidence:derived_analysis` |
| **1M** | `end_to_end_seconds` (s) | `74.09 ± 21.86` | `60.68 ± 1.09` | `-13.41 ± 20.99` | `-18.10%` | `-1.2778` | `0.2906` | `evidence:derived_analysis` |
| **1M** | `inject_seconds` (s) | `5.87 ± 0.11` | `4.64 ± 0.24` | `-1.23 ± 0.33` | **`-20.95%`** | **`-7.3980`** | **`0.0051`** | `evidence:derived_analysis` |
| **1M** | `injection_tps` (tx/s) | `171,870.36 ± 3,018.90` | `217,902.26 ± 11,273.97` | `+46,031.89 ± 13,868.59` | **`+26.78%`** | **`+6.6383`** | **`0.0070`** | `evidence:derived_analysis` |
| **1M** | `latency_p50_ms` (ms) | `60,223.25 ± 23,006.18` | `46,971.00 ± 1,728.12` | `-13,252.25 ± 21,671.84` | `-22.01%` | `-1.2230` | `0.3081` | `evidence:derived_analysis` |
| **1M** | `peak_rss_mb_tool` (MB) | `42,751.50 ± 1,489.03` | `43,492.75 ± 1,676.69` | `+741.25 ± 1,349.81` | `+1.73%` | `+1.0983` | `0.3517` | `evidence:derived_analysis` |
| **4M** | `tps_end_to_end` (tx/s) | `13,755.14 ± 955.94` | `14,510.46 ± 308.02` | `+755.31 ± 1,042.19` | `+5.49%` | `+1.4495` | `0.2422` | `evidence:derived_analysis` |
| **4M** | `tps_active_commit` (tx/s) | `13,807.89 ± 962.86` | `14,570.79 ± 328.03` | `+762.89 ± 1,045.80` | `+5.53%` | `+1.4590` | `0.2404` | `evidence:derived_analysis` |
| **4M** | `tps_during_injection` (tx/s) | `13,430.65 ± 701.98` | `14,305.56 ± 945.69` | `+874.91 ± 1,166.96` | `+6.51%` | `+1.4995` | `0.2306` | `evidence:derived_analysis` |
| **4M** | `end_to_end_seconds` (s) | `292.79 ± 21.58` | `276.41 ± 5.88` | `-16.39 ± 23.09` | `-5.60%` | `-1.4193` | `0.2514` | `evidence:derived_analysis` |
| **4M** | `inject_seconds` (s) | `65.42 ± 7.10` | `29.22 ± 15.14` | `-36.20 ± 17.61` | **`-55.34%`** | **`-4.1112`** | **`0.0260`** | `evidence:derived_analysis` |
| **4M** | `latency_p50_ms` (ms) | `161,493.50 ± 3,086.59` | `160,376.50 ± 7,834.02` | `-1,117.00 ± 8,832.89` | `-0.69%` | `-0.2529` | `0.8159` | `evidence:derived_analysis` |
| **4M** | `peak_rss_mb_tool` (MB) | `71,394.75 ± 7,277.67` | `64,165.25 ± 1,362.92` | `-7,229.50 ± 8,546.54` | **`-10.13%`** | `-1.6918` | `0.1889` | `evidence:derived_analysis` |

---

## 6. Giới hạn

1. **Giới hạn của vị trí đặt cổng `loadTxAdmissionGate()` hiện tại (`server.go:239` so với `connection.go:909`):**
   - `[ĐỌC CODE]` Cổng `waitNotOverloaded` hiện đặt trong `SocketServer.HandleConnection` ([`server.go:239`](file:///home/abc/chain-n/metanode/execution/pkg/network/server.go#L239)) — tức là phía **sau** kênh đệm `conn.RequestChan()` (`ConnRequestChanSize = 256` batch = `256,000` giao dịch tại [`app.go:880`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/app.go#L880)). Do đó, mặc dù `waitNotOverloaded` chặn hiệu quả việc đẩy thêm batch vào `s.requestChan` / `ProcessRawTransactionsFromClient` ngay khi `inFlight > 300,000`, mỗi kết nối TCP vẫn đọc trước thêm `256` batch (`256,000` tx) vào `conn.RequestChan()` cộng với `~80,000–440,000` tx trong bộ đệm TCP loopback của kernel trước khi syscall `conn.Write()` của client thực sự bị dừng (`evidence:ab_4m`).
2. **Giới hạn của tốc độ đẩy `pendingIndexBlocks` vào `s.indexQueue` (`65,536` job):**
   - `[ĐO]` Khi loại bỏ hoàn toàn việc mở lại 2 trie NOMT/Pebble (`txDB.GetTransaction` + `rcpDb.GetReceipt`) trong `indexSingleBlock` ([`block_processor_indexing.go:172-208`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L172-L208)), vòng lặp gửi job sang `ExplorerSearchService.IndexTransactionFast` chạy trực tiếp trên slice bộ nhớ trong vài micro-giây/block và làm đầy hàng đợi `s.indexQueue` (`65,536` job) nhanh hơn tốc độ ghi B-tree của 32 worker Xapian. Vì `IndexTransactionFast` dùng `select { case s.indexQueue <- job: default: drop }` (chính sách không chặn commit pipeline) và dòng [`block_processor_indexing.go:200`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L200) ghi `logger.Error("❌ [INDEXING] Cannot index transaction...")` cho mỗi giao dịch bị drop để chờ quét bù định kỳ (`indexDroppedCount > 0`), tổng số dòng log khớp mẫu `node_anomaly_lines` tăng từ `30,377.75` lên `690,281.50` ở 1M (`p = 0.0007`) và từ `94,993.75` lên `1,066,369.25` ở 4M (`p = 0.0055`), dù không ảnh hưởng tới đồng thuận hay thực thi (`loss = 0`, `parity = OK` trên toàn bộ 8/8 lần chạy `bins_next`) (`evidence:derived_analysis`).
3. **Cỡ mẫu thống kê (`n = 4` cặp, `df = 3`):**
   - `[TÍNH]` Với `df = 3`, các cải thiện có biên độ `5%–18%` nhưng phương sai cặp chịu ảnh hưởng bởi nhịp đồng thuận DAG (như `tps_end_to_end` `+16.06%` ở 1M với `p = 0.2276`, `+5.49%` ở 4M với `p = 0.2422`, hay `peak_rss_mb_tool` `-10.13%` ở 4M với `p = 0.1889`) chưa đạt ngưỡng `p < 0.05` hai phía dù xu hướng nhất quán (thắng 4/4 cặp ở 1M và 3/4 cặp ở 4M, giảm mạnh độ lệch chuẩn `SD`) (`evidence:derived_analysis`).

---

## 7. Chưa làm

1. **`[CHƯA ĐO]` Đặt `loadTxAdmissionGate()` trực tiếp trong `Connection.readLoop` và giảm `ConnRequestChanSize`:**
   - Kiểm tra `loadTxAdmissionGate()` ngay trong [`Connection.readLoop`](file:///home/abc/chain-n/metanode/execution/pkg/network/connection.go#L908-L920) (hoặc giảm `socketCfg.ConnRequestChanSize` tại [`app.go:880`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/app.go#L880) từ `256` batch xuống `8–16` batch = `8,000–16,000` giao dịch) để khi `inFlightCount > 300,000`, `readLoop` ngừng đọc socket ngay lập tức thay vì đệm thêm `256,000` giao dịch/validator trong `conn.RequestChan()`.
2. **`[CHƯA ĐO]` Điều tiết tốc độ đẩy `indexSingleBlock` -> `s.indexQueue` và giảm spam log khi hàng đợi Explorer đầy:**
   - Trong [`block_processor_indexing.go:199-203`](file:///home/abc/chain-n/metanode/execution/cmd/simple_chain/processor/block_processor_indexing.go#L199-L203), vì `indexSingleBlock` chạy trên goroutine nền độc lập với `commitWorker`, có thể dùng gửi có chặn theo ngữ cảnh (`select { case s.indexQueue <- job: case <-ctx.Done(): }`) hoặc chỉ log tổng hợp 1 dòng/block tại `finalizeIndexedBlock` (khi `indexDroppedCount > 0`) thay vì gọi `logger.Error` trên từng giao dịch bị drop (`evidence:preregistered`).
3. **`[CHƯA ĐO]` Tối ưu chi phí ghi B-tree nội tại của Xapian (`_Cfunc_database_index_document` chiếm `48.87s` / `15.13%` CPU ở 4M):**
   - Khảo sát gom batch nhiều `Xapian::Document` trong một lần khóa shard hoặc cấu hình ngưỡng flush/compaction của Xapian Chert/Glass backend để giảm thêm `48.87s` thời gian thực thi C++ bên trong `wdb->add_document(doc)`.
