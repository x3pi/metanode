# Báo cáo Thực thi Kế hoạch Tiếp nối: Giảm Giữ Heap/GC dưới Backlog Lớn, Thiết kế Chặn Backlog & Kiểm chứng A/B Raft (2026-10-15)

- **Ngày thực hiện:** 2026-10-15
- **Kế hoạch gốc:** [`note/plan_backlog_gc_followup_20261015.md`](plan_backlog_gc_followup_20261015.md)
- **Đăng ký trước thí nghiệm (Preregistration):** [`note/evidence/backlog_gc_followup_20261015/PREREGISTERED.md`](evidence/backlog_gc_followup_20261015/PREREGISTERED.md) (commit `bdcfc7fb` trước khi chạy đo) (`evidence:preregistered`)
- **Commit nền (`bins_base`):** `f85bf6838d5809e556f36f77202bf289c3b8394b` (`simple_chain` sha256 `9ff49edd63941e6357bb4528fea13b9755e9d3be30e643f2cfded239b731b2af`) [ĐO] (`evidence:preregistered`)
- **Commit tối ưu (`bins_opt`):** `c270b2814bd25a70bc9b2236cfac8eb5bd64a9a2` (`simple_chain` sha256 `2bed5014855a691b9ce9a5fa63347bce9e265e3c431200e2ace0e0ef1247873d`) [ĐO] (`evidence:summary_and_recheck`)
- **Commit lai cũ cho Raft (`bins_old`):** Go tree tại `f97a2fbf` + Rust FFI hiện tại (`simple_chain` sha256 `97a9ebbc39a7d08d37dc9230829e65a8dfef0bc2f73784369e47f0c7849c5784`) [ĐO] (`evidence:preregistered`)
- **Thư mục bằng chứng thô:** [`note/evidence/backlog_gc_followup_20261015/`](evidence/backlog_gc_followup_20261015/) (`MANIFEST.json`, `stats_summary.json`, `independent_recheck.txt`)

---

## 0. Bảng Tổng hợp Trạng thái Từng Việc

| Hạng mục | Mô tả | Trạng thái | Tệp bằng chứng chính |
|---|---|---|---|
| **Việc 0.1–0.2** | Dựng `bins_base` (`f85bf683`), chạy 2 lượt sanity sustained 1 triệu tx | **Xong (có evidence qua verify --strict)** | [`task0_sanity/derived_summary.csv`](evidence/backlog_gc_followup_20261015/task0_sanity/derived_summary.csv) (`evidence:task0_sanity`) |
| **Việc 0.3** | Đối chứng A/A BFT 4 cặp (8 lượt) ở 4 triệu tx để định lượng nhiễu nền | **Xong (có evidence qua verify --strict)** | [`task0_aa_4m/derived_summary.csv`](evidence/backlog_gc_followup_20261015/task0_aa_4m/derived_summary.csv) (`evidence:task0_aa_4m`) |
| **Việc 1.1–1.2** | Phân tích vòng đời 5 nhóm đối tượng đầu bảng heap Go & duyệt thiết kế 3 đề xuất | **Xong (có evidence qua verify --strict)** | [`task1_heap_analysis/pprof_heap_summary.txt`](evidence/backlog_gc_followup_20261015/task1_heap_analysis/pprof_heap_summary.txt) (`evidence:task1_heap_analysis`) |
| **Việc 1.3** | Triển khai 3 tối ưu thu gọn đối tượng/map, thêm unit test, pass `go test -race` & `build_check.sh` | **Xong (có evidence qua verify --strict)** | Commit `c270b281` (`evidence:summary_and_recheck`) |
| **Việc 1.4** | Thí nghiệm A/B (`bins_base` vs `bins_opt`) 8 cặp × {1M, 4M} tx = 32 lượt + profile cơ chế | **Xong — Đạt cả 4/4 tiêu chí đăng ký trước (có evidence qua verify --strict)** | [`task1_ab_1m/derived_summary.csv`](evidence/backlog_gc_followup_20261015/task1_ab_1m/derived_summary.csv), [`task1_ab_4m/derived_summary.csv`](evidence/backlog_gc_followup_20261015/task1_ab_4m/derived_summary.csv) (`evidence:task1_ab_1m`, `evidence:task1_ab_4m`) |
| **Việc 2** | Đề xuất thiết kế chặn backlog ở lớp nhận tx (chỉ tài liệu, không sửa mã) | **Xong (có evidence qua claims_lint)** | [`note/design_backlog_bound_20261015.md`](design_backlog_bound_20261015.md) (`evidence:preregistered`) |
| **Việc 3** | Thí nghiệm A/B Raft 6 cặp (12 lượt) ở 1 triệu tx: `bins_old` (`f97a2fbf`) vs `bins_base` (`f85bf683`) | **Xong — Tái hiện lỗi dừng tạo block ở 6/6 lượt cũ, 0/6 lượt mới (có evidence qua verify --strict)** | [`task3_raft_ab/derived_summary.csv`](evidence/backlog_gc_followup_20261015/task3_raft_ab/derived_summary.csv) (`evidence:task3_raft_ab`) |
| **Việc 4** | Phân rã chi phí phía Rust/FFI qua `perf` không cần `sudo` | **Chưa làm được (bị chặn: `kernel.perf_event_paranoid = 4`, thiếu `CAP_PERFMON`/`CAP_SYS_ADMIN`)** | [`task4_perf/perf_check.txt`](evidence/backlog_gc_followup_20261015/task4_perf/perf_check.txt) (`evidence:task4_perf`) |
| **Việc 5** | Tự kiểm tra độc lập từ file thô, cập nhật `CLAIMS_LEDGER.md`, dọn dẹp `/tmp`, `verify_evidence --strict` | **Xong (có evidence qua verify --strict)** | [`independent_recheck.txt`](evidence/backlog_gc_followup_20261015/independent_recheck.txt) (`evidence:summary_and_recheck`) |

---

## 1. Việc 0 — Chuẩn bị, Sanity 1M tx và Đối chứng A/A ở 4M tx

### 1.1 Kiểm tra Sanity 1 triệu tx (`bins_base`, 2 lượt)
Hai lượt chạy sustained `-max-txs 1000000 -duration 60` với `bins_base` (`f85bf683`) đều đạt `bytes_sent_txs == received_sum == committed_ours` (`loss_vs_sent = 0`) và `parity_ok = OK` trên cả 4 validator [ĐO] (`evidence:task0_sanity`). Trung bình `tps_end_to_end` đạt `15314.5840 ± 105.1427 tx/s` [TÍNH] (`evidence:summary_and_recheck`).

| Lượt | Variant | Tag | Tx gửi (`bytes_sent_txs`) | Tx nhận (`received_sum`) | Tx commit (`committed_ours`) | Mất (`loss_vs_sent`) | `tps_end_to_end` (tx/s) | `end_to_end_seconds` (s) | Parity | Bằng chứng |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `base` | `base_sanity_1m_run1` | 1007408 | 1007408 | 1007408 | 0 | 15240.2368 | 66.1019 | OK | `evidence:task0_sanity` |
| 2 | `base` | `base_sanity_1m_run2` | 1008000 | 1008000 | 1008000 | 0 | 15388.9311 | 65.5016 | OK | `evidence:task0_sanity` |

### 1.2 Đối chứng A/A BFT ở 4 triệu tx (4 cặp = 8 lượt, cùng `bins_base`)
Thứ tự ngẫu nhiên từng cặp được sinh với `seed = 2026101501` (`order.txt`: Pair 1 `A1, A2`; Pair 2 `A1, A2`; Pair 3 `A1, A2`; Pair 4 `A1, A2`) [ĐO] (`evidence:task0_aa_4m`). Cả 8/8 lượt đều đạt `loss_vs_sent = 0` và `parity_ok = OK` [ĐO] (`evidence:task0_aa_4m`):

| Cặp | Nhánh | Tag | Tx gửi (`bytes_sent_txs`) | Tx commit (`committed_ours`) | Mất (`loss_vs_sent`) | `tps_end_to_end` (tx/s) | `end_to_end_seconds` (s) | Block cuối (`end_block`) | RSS đỉnh (`max_rss_mb`) | Parity | Bằng chứng |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `A1_base` | `aa_a_p1_run1` | 4014430 | 4014430 | 0 | 13050.7631 | 307.6012 | 981 | 63593 | OK | `evidence:task0_aa_4m` |
| 1 | `A2_base` | `aa_b_p1_run1` | 4012383 | 4012383 | 0 | 12957.7309 | 309.6517 | 949 | 64391 | OK | `evidence:task0_aa_4m` |
| 2 | `A1_base` | `aa_a_p2_run1` | 4014511 | 4014511 | 0 | 13333.8558 | 301.0765 | 940 | 64903 | OK | `evidence:task0_aa_4m` |
| 2 | `A2_base` | `aa_b_p2_run1` | 4011866 | 4011866 | 0 | 12890.5172 | 311.2261 | 916 | 64390 | OK | `evidence:task0_aa_4m` |
| 3 | `A1_base` | `aa_a_p3_run1` | 4013608 | 4013608 | 0 | 13184.2003 | 304.4256 | 911 | 64958 | OK | `evidence:task0_aa_4m` |
| 3 | `A2_base` | `aa_b_p3_run1` | 4011371 | 4011371 | 0 | 12685.1394 | 316.2260 | 866 | 80862 | OK | `evidence:task0_aa_4m` |
| 4 | `A1_base` | `aa_a_p4_run1` | 4013223 | 4013223 | 0 | 10983.0433 | 365.4017 | 773 | 82954 | OK | `evidence:task0_aa_4m` |
| 4 | `A2_base` | `aa_b_p4_run1` | 4011134 | 4011134 | 0 | 12449.1643 | 322.2011 | 871 | 63977 | OK | `evidence:task0_aa_4m` |

- **Thống kê ghép cặp A/A (`A2 - A1`, `n = 4` cặp):**
  - `mean_a = 12637.9656 ± 1109.3249 tx/s`, `mean_b = 12745.6380 ± 229.1507 tx/s` (gộp 8 lượt: `12691.8018 ± 743.7859 tx/s`) [TÍNH] (`evidence:summary_and_recheck`).
  - Hiệu trung bình từng cặp $\bar{\Delta}_{AA} = +107.6723\text{ tx/s}$ (`+1.3815%`), độ lệch chuẩn hiệu từng cặp **$s_{\Delta, AA} = 923.2918\text{ tx/s}$** (`8.0922%`), $t = 0.2332, df = 3, p = 0.830587$ [TÍNH] (`evidence:summary_and_recheck`).
  - Lưu ý: ở lượt `aa_b_p3_run1` và `aa_a_p4_run1`, RSS đỉnh lên ~81–83 GB kéo tốc độ lượt `aa_a_p4_run1` xuống `10983.0433 tx/s`, làm $s_{\Delta, AA}$ đạt `923.2918 tx/s`; ngưỡng vượt nhiễu A/A cho Việc 1.4 do đó là $|\bar{\Delta}_{opt-base}| > 923.2918\text{ tx/s}$ [TÍNH] (`evidence:summary_and_recheck`).

---

## 2. Việc 1 — Giảm Giữ Đối tượng Heap Go và Quét GC dưới Backlog Lớn

### 2.1 Phân tích Vòng đời Đối tượng Đầu Bảng Heap (Việc 1.1)
Từ tệp heap ở mức 4 triệu tx (`prof_v4000k_n3_run1_val0_heap.pb.gz`: `2044.72 MB inuse_space`, `27752337 inuse_objects`, chi tiết trong [`task1_heap_analysis/pprof_heap_summary.txt`](evidence/backlog_gc_followup_20261015/task1_heap_analysis/pprof_heap_summary.txt)) và đọc mã nguồn [ĐO][ĐỌC CODE] (`evidence:task1_heap_analysis`):

| Nhóm đối tượng / Hàm cấp phát | `inuse_space` (4M tx, trước sửa) | `inuse_objects` (4M tx, trước sửa) | Vòng đời & Vì sao còn sống | Giữ cho tx chưa thực thi? | Quyết định tối ưu | Bằng chứng |
|---|---|---|---|---|---|---|
| `smart_contract.NewExecuteSCResult` | 542.13 MB (26.51%) | 1835022 (6.61%) | Cấp phát sẵn 3 map rỗng (`MapAddBalance`, `MapSubBalance`, `MapCodeHash`) cho mọi tx chuyển tiền tự nhiên dù không ghi phần tử nào; sống trong `processedTxWindow` (64 block) | Không (tạo khi thực thi block) | **Đề xuất 1:** Chỉ cấp phát map khi thực sự ghi; bỏ truyền map rỗng tại 3 đường fast-path | `evidence:task1_heap_analysis` |
| `tx_processor.storeVerifiedBound` + `sync.(*Map).Swap` / `dirtyLocked` / `newEntry` | 756.90 MB (37.02% gộp) | 19465093 (70.14% gộp) | Cache chữ ký `verifiedSignatureCache` dùng `sync.Map` với khóa chuỗi `string(txHash[:])` và con trỏ `*verifiedTxBound`; mỗi mục tạo 4–5 đối tượng chứa con trỏ cho cả tx đang chờ trong backlog | **Có** (tạo ngay khi verify ở ingress/pool, sống suốt thời gian chờ trong mempool + TTL 5–10 phút) | **Đề xuất 2:** Thay `sync.Map` bằng 64 shard `RWMutex` + `map[common.Hash]verifiedTxBound` (value struct phẳng, không cấp phát `string` hay `*Entry`) | `evidence:task1_heap_analysis` |
| `ReceiptTracker.StoreTxHashConnEntry` | 94.13 MB cum (4.60%) | 2064400 cum (7.44%) | Dùng `sync.Map` + `string(bHash[:])` + `*txHashConnEntry` cho mỗi tx TCP ingress đang chờ thực thi để trả receipt | **Có** (sống từ lúc nhận TCP tới khi commit block hoặc TTL) | **Đề xuất 3:** Thay `sync.Map` bằng 64 shard `RWMutex` + `map[common.Hash]txHashConnEntry` (value struct) | `evidence:task1_heap_analysis` |
| `BlockChain.AddTxToCache` | 211.76 MB cum (10.36%) | 1968466 cum (7.09%) | Lưu `proto.Transaction` đã commit trong cửa sổ 64 block (`processedTxWindow`) để phục vụ truy vấn/chống trùng | Không | Giữ nguyên (cần cho truy vấn tx gần đây) | `evidence:task1_heap_analysis` |

### 2.2 Triển khai & Kiểm chứng Đơn vị (Việc 1.2 & 1.3)
Sau khi chủ dự án duyệt cả 3 đề xuất, các thay đổi được triển khai trong commit `c270b2814bd25a70bc9b2236cfac8eb5bd64a9a2` [ĐỌC CODE] (`evidence:summary_and_recheck`):
1. **Đề xuất 1 (`pkg/smart_contract/execute_sc_result.go`, `pkg/blockchain/tx_processor/tx_processor.go`, `pkg/blockchain/blockchain.go`):** `NewExecuteSCResult` không cấp phát trước `MapAddBalance`/`MapSubBalance` khi truyền `nil`, không cấp phát sẵn `MapCodeHash`; thêm lazy-init trong `Unmarshal`/`FromProto` và 3 đường `processNativeTransfersFastPath` truyền `nil, nil`.
2. **Đề xuất 2 (`pkg/blockchain/tx_processor/signature_cache.go`, `verify.go`, `tx_processor.go`, `tx_validator_worker.go`):** Thay `verifiedSignatureCache` (`sync.Map`) bằng `shardedSignatureCache` gồm 64 shard `sync.RWMutex` + `map[common.Hash]verifiedTxBound` (value struct).
3. **Đề xuất 3 (`cmd/simple_chain/processor/receipt_tracker.go`, `message_handler_helper.go`, `transaction_processor.go`):** Thay `txHashToConnection` (`sync.Map`) bằng 64 shard `txConnShard` (`map[common.Hash]txHashConnEntry` value struct), thêm `StoreTxHashConnEntryHash(common.Hash, ...)`.
4. **Kiểm chứng:** Thêm unit test (`execute_sc_result_test.go`, `signature_cache_test.go`, `receipt_tracker_test.go`); chạy `go test -race -count=1 ./pkg/smart_contract ./pkg/blockchain ./pkg/blockchain/tx_processor ./cmd/simple_chain/processor` đạt PASS và `./consensus/metanode/scripts/build_check.sh` đạt 5/5 bước với 0 lỗi, 0 cảnh báo.

### 2.3 Kết quả Thí nghiệm A/B (`bins_base` vs `bins_opt`) ở 1 triệu tx và 4 triệu tx (Việc 1.4)

#### A. Mức 1 triệu tx (8 cặp = 16 lượt, `seed = 2026101503`)
Thứ tự ngẫu nhiên từng cặp được lưu tại [`task1_ab_1m/order.txt`](evidence/backlog_gc_followup_20261015/task1_ab_1m/order.txt). Cả 16/16 lượt đạt `loss_vs_sent = 0` và `parity_ok = OK` [ĐO] (`evidence:task1_ab_1m`):

| Cặp | Variant | Tag | Tx gửi (`bytes_sent_txs`) | Tx commit (`committed_ours`) | Mất (`loss_vs_sent`) | `tps_end_to_end` (tx/s) | `end_to_end_seconds` (s) | Block cuối (`end_block`) | RSS đỉnh (`max_rss_mb`) | Parity | Bằng chứng |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `base_f85bf683` | `ab_1m_base_p1_run1` | 1009713 | 1009713 | 0 | 15678.5002 | 64.4011 | 151 | 47897 | OK | `evidence:task1_ab_1m` |
| 1 | `opt_c270b281` | `ab_1m_opt_p1_run1` | 1008559 | 1008559 | 0 | 16156.1639 | 62.4256 | 163 | 42552 | OK | `evidence:task1_ab_1m` |
| 2 | `base_f85bf683` | `ab_1m_base_p2_run1` | 1008297 | 1008297 | 0 | 15375.8501 | 65.5767 | 135 | 48669 | OK | `evidence:task1_ab_1m` |
| 2 | `opt_c270b281` | `ab_1m_opt_p2_run1` | 1008127 | 1008127 | 0 | 16214.1390 | 62.1758 | 108 | 44239 | OK | `evidence:task1_ab_1m` |
| 3 | `base_f85bf683` | `ab_1m_base_p3_run1` | 1006849 | 1006849 | 0 | 15301.3321 | 65.8014 | 180 | 46932 | OK | `evidence:task1_ab_1m` |
| 3 | `opt_c270b281` | `ab_1m_opt_p3_run1` | 1009048 | 1009048 | 0 | 15619.6128 | 64.6013 | 103 | 43344 | OK | `evidence:task1_ab_1m` |
| 4 | `base_f85bf683` | `ab_1m_base_p4_run1` | 1005429 | 1005429 | 0 | 15245.0985 | 65.9510 | 137 | 45649 | OK | `evidence:task1_ab_1m` |
| 4 | `opt_c270b281` | `ab_1m_opt_p4_run1` | 1008123 | 1008123 | 0 | 16246.6712 | 62.0510 | 188 | 42197 | OK | `evidence:task1_ab_1m` |
| 5 | `base_f85bf683` | `ab_1m_base_p5_run1` | 1008178 | 1008178 | 0 | 15527.9558 | 64.9266 | 169 | 47213 | OK | `evidence:task1_ab_1m` |
| 5 | `opt_c270b281` | `ab_1m_opt_p5_run1` | 1009753 | 1009753 | 0 | 16634.8180 | 60.7012 | 155 | 44496 | OK | `evidence:task1_ab_1m` |
| 6 | `base_f85bf683` | `ab_1m_base_p6_run1` | 1009125 | 1009125 | 0 | 15129.1352 | 66.7008 | 154 | 47107 | OK | `evidence:task1_ab_1m` |
| 6 | `opt_c270b281` | `ab_1m_opt_p6_run1` | 1007366 | 1007366 | 0 | 16280.4748 | 61.8757 | 187 | 42771 | OK | `evidence:task1_ab_1m` |
| 7 | `base_f85bf683` | `ab_1m_base_p7_run1` | 1006263 | 1006263 | 0 | 15415.3818 | 65.2766 | 199 | 47113 | OK | `evidence:task1_ab_1m` |
| 7 | `opt_c270b281` | `ab_1m_opt_p7_run1` | 1006334 | 1006334 | 0 | 15966.8904 | 63.0263 | 106 | 42196 | OK | `evidence:task1_ab_1m` |
| 8 | `base_f85bf683` | `ab_1m_base_p8_run1` | 1008404 | 1008404 | 0 | 15652.2421 | 64.4255 | 147 | 46043 | OK | `evidence:task1_ab_1m` |
| 8 | `opt_c270b281` | `ab_1m_opt_p8_run1` | 1008792 | 1008792 | 0 | 15639.7831 | 64.5017 | 92 | 44634 | OK | `evidence:task1_ab_1m` |

- **Tổng hợp thống kê ghép cặp ở 1 triệu tx (`n = 8` cặp):**
  - `tps_end_to_end`: `bins_base = 15415.6870 ± 194.0213 tx/s` so với `bins_opt = 16094.8191 ± 341.5032 tx/s`; hiệu ghép cặp $\bar{\Delta} = +679.1322 ± 414.1465\text{ tx/s}$, tăng `+4.4231%` ($t = 4.6382, df = 7, p = 0.002375$, CI95% `[+332.8971, +1025.3673] tx/s`) [TÍNH] (`evidence:summary_and_recheck`).
  - `max_rss_mb` (tổng RSS đỉnh của 4 validator): giảm từ `47077.8750 ± 952.9589 MB` xuống `43303.6250 ± 1025.9695 MB`, tức giảm `-3774.2500 ± 1274.4424 MB` (`-7.9906%`, $t = -8.3764, df = 7, p = 0.000068$) ở cả 8/8 cặp [TÍNH] (`evidence:summary_and_recheck`).

#### B. Mức 4 triệu tx (8 cặp = 16 lượt, `seed = 2026101504`)
Thứ tự ngẫu nhiên từng cặp được lưu tại [`task1_ab_4m/order.txt`](evidence/backlog_gc_followup_20261015/task1_ab_4m/order.txt). Cả 16/16 lượt đạt `loss_vs_sent = 0` và `parity_ok = OK`; trong cả 8/8 cặp, `bins_opt` đều đạt `tps_end_to_end` cao hơn `bins_base` [ĐO] (`evidence:task1_ab_4m`):

| Cặp | Variant | Tag | Tx gửi (`bytes_sent_txs`) | Tx commit (`committed_ours`) | Mất (`loss_vs_sent`) | `tps_end_to_end` (tx/s) | `end_to_end_seconds` (s) | Block cuối (`end_block`) | RSS đỉnh (`max_rss_mb`) | Parity | Bằng chứng |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `base_f85bf683` | `ab_4m_base_p1_run1` | 4010798 | 4010798 | 0 | 13099.6268 | 306.1765 | 940 | 64137 | OK | `evidence:task1_ab_4m` |
| 1 | `opt_c270b281` | `ab_4m_opt_p1_run1` | 4009062 | 4009062 | 0 | 13686.2463 | 292.9263 | 960 | 65206 | OK | `evidence:task1_ab_4m` |
| 2 | `base_f85bf683` | `ab_4m_base_p2_run1` | 4008867 | 4008867 | 0 | 12969.4522 | 309.1007 | 957 | 66201 | OK | `evidence:task1_ab_4m` |
| 2 | `opt_c270b281` | `ab_4m_opt_p2_run1` | 4013134 | 4013134 | 0 | 14714.8582 | 272.7267 | 939 | 64392 | OK | `evidence:task1_ab_4m` |
| 3 | `base_f85bf683` | `ab_4m_base_p3_run1` | 4011773 | 4011773 | 0 | 13111.3695 | 305.9767 | 950 | 63527 | OK | `evidence:task1_ab_4m` |
| 3 | `opt_c270b281` | `ab_4m_opt_p3_run1` | 4009877 | 4009877 | 0 | 14654.5994 | 273.6258 | 817 | 79972 | OK | `evidence:task1_ab_4m` |
| 4 | `base_f85bf683` | `ab_4m_base_p4_run1` | 4009686 | 4009686 | 0 | 12927.1092 | 310.1765 | 919 | 63535 | OK | `evidence:task1_ab_4m` |
| 4 | `opt_c270b281` | `ab_4m_opt_p4_run1` | 4009985 | 4009985 | 0 | 14303.3996 | 280.3519 | 846 | 65546 | OK | `evidence:task1_ab_4m` |
| 5 | `base_f85bf683` | `ab_4m_base_p5_run1` | 4013072 | 4013072 | 0 | 12820.2484 | 313.0261 | 963 | 63918 | OK | `evidence:task1_ab_4m` |
| 5 | `opt_c270b281` | `ab_4m_opt_p5_run1` | 4014006 | 4014006 | 0 | 14167.4168 | 283.3266 | 927 | 65576 | OK | `evidence:task1_ab_4m` |
| 6 | `base_f85bf683` | `ab_4m_base_p6_run1` | 4011711 | 4011711 | 0 | 12736.5248 | 314.9769 | 810 | 64544 | OK | `evidence:task1_ab_4m` |
| 6 | `opt_c270b281` | `ab_4m_opt_p6_run1` | 4009040 | 4009040 | 0 | 13736.6066 | 291.8508 | 942 | 65051 | OK | `evidence:task1_ab_4m` |
| 7 | `base_f85bf683` | `ab_4m_base_p7_run1` | 4012842 | 4012842 | 0 | 13091.3456 | 306.5263 | 953 | 64161 | OK | `evidence:task1_ab_4m` |
| 7 | `opt_c270b281` | `ab_4m_opt_p7_run1` | 4009172 | 4009172 | 0 | 14389.0441 | 278.6267 | 958 | 64531 | OK | `evidence:task1_ab_4m` |
| 8 | `base_f85bf683` | `ab_4m_base_p8_run1` | 4008353 | 4008353 | 0 | 13183.1487 | 304.0513 | 967 | 64706 | OK | `evidence:task1_ab_4m` |
| 8 | `opt_c270b281` | `ab_4m_opt_p8_run1` | 4011718 | 4011718 | 0 | 14482.6569 | 277.0015 | 946 | 65435 | OK | `evidence:task1_ab_4m` |

- **Tổng hợp thống kê ghép cặp ở 4 triệu tx (`n = 8` cặp):**
  - `tps_end_to_end`: `bins_base = 12992.3532 ± 156.6271 tx/s` so với `bins_opt = 14266.8535 ± 385.8287 tx/s`; hiệu ghép cặp $\bar{\Delta}_{opt-base} = +1274.5003 ± 350.3587\text{ tx/s}$, tăng **`+9.8104%`** ($t = 10.2890, df = 7, p = 0.000018$, CI95% `[+981.5931, +1567.4076] tx/s`) [TÍNH] (`evidence:summary_and_recheck`).
  - Thời gian hoàn tất đầu-cuối (`end_to_end_seconds`): giảm từ `308.7514 ± 3.7901 s` xuống `281.3045 ± 7.6487 s`, tức giảm `-27.4468 ± 6.9184 s` (`-8.8890%`, $t = -11.2210, df = 7, p = 0.000010$) [TÍNH] (`evidence:summary_and_recheck`).
  - Ghi chú về Cặp 1: Cặp 1 chạy kèm `--profile-at 150` (thu profile CPU 30s + heap/allocs lúc `t = 150s`), nên hiệu tăng ở Cặp 1 là `+586.6195 tx/s` (`+4.4781%`), trong khi các cặp 2–8 không chịu tải thu profile tăng từ `+1000.0818` đến `+1745.4060 tx/s` (`+7.8521%` đến `+13.4578%`) [ĐO] (`evidence:task1_ab_4m`).

#### C. Bằng chứng Cơ chế từ Profile (`--profile-at`) ở 4M tx và 1M tx
Trích xuất bằng `/tmp/p06_bins/pprof` từ Cặp 1 của cả hai mức tải (lưu tại [`task1_heap_analysis/pprof_ab_comparison.txt`](evidence/backlog_gc_followup_20261015/task1_heap_analysis/pprof_ab_comparison.txt)) [ĐO] (`evidence:task1_heap_analysis`):

| Chỉ số Profile trên Validator 0 | `bins_base` (`f85bf683`) | `bins_opt` (`c270b281`) | Chênh lệch & tỷ lệ (%) | Bằng chứng |
|---|---|---|---|---|
| **4M tx (`t=150s`) — `runtime.gcDrain` CPU cum** | 171.80s / 459.45s (**37.39%**) | 34.32s / 323.72s (**10.60%**) | Giảm **-26.79 điểm %** (giảm **-80.02%** số giây CPU quét GC) | `evidence:task1_heap_analysis` |
| **4M tx (`t=150s`) — `runtime.scanobject` CPU cum** | 141.85s (**30.87%**) | Dưới ngưỡng top 25 (`< 8.05%`) | Giảm hơn 74% thời gian quét con trỏ | `evidence:task1_heap_analysis` |
| **4M tx (`t=150s`) — Tổng `inuse_space` Go heap** | **2266.66 MB** | **1108.07 MB** | Giảm **-1158.59 MB** (**-51.11%**) | `evidence:task1_heap_analysis` |
| **4M tx (`t=150s`) — Tổng `inuse_objects` Go heap** | **32523158** đối tượng | **10649843** đối tượng | Giảm **-21873315** đối tượng (**-67.25%**) | `evidence:task1_heap_analysis` |
| **4M tx (`t=150s`) — `NewExecuteSCResult` `inuse_space`** | **570.14 MB** | **261.53 MB** | Giảm **-308.61 MB** (**-54.13%**) | `evidence:task1_heap_analysis` |
| **4M tx (`t=150s`) — Signature cache `inuse_space`** | `storeVerifiedBound` cum **717.78 MB** (`sync.Map.Swap` cum **807.80 MB**) | `(*shardedSignatureCache).Store` **79.83 MB** | Giảm **-637.95 MB** (**-88.88%**) | `evidence:task1_heap_analysis` |
| **4M tx (`t=150s`) — `StoreTxHashConnEntry` cum `inuse_space`** | **91.63 MB** (2367676 objects) | **46.63 MB** (684868 objects) | Giảm **-45.00 MB** (**-49.11%** space, **-71.07%** objects) | `evidence:task1_heap_analysis` |
| **1M tx (`t=30s`) — `runtime.gcDrain` CPU cum** | 32.34s / 351.85s (**9.19%**) | 11.92s / 329.63s (**3.62%**) | Giảm **-5.57 điểm %** (giảm **-63.14%** số giây CPU quét GC) | `evidence:task1_heap_analysis` |
| **1M tx (`t=30s`) — Tổng `inuse_space` / `inuse_objects`** | **899.57 MB** / **10990385** objects | **745.98 MB** / **4275198** objects | Giảm **-17.07%** `inuse_space`, giảm **-61.10%** `inuse_objects` | `evidence:task1_heap_analysis` |

#### D. Đối chiếu với 4 Tiêu chí Đăng ký Trước (`PREREGISTERED.md` §4)

| Tiêu chí trong `PREREGISTERED.md` | Yêu cầu | Kết quả đo thực tế | Kết luận | Bằng chứng |
|---|---|---|---|---|
| **(a) Tính đúng đắn & Zero-Fork** | Mọi lượt trong 32 lượt có `bytes_sent == received_sum == committed_ours` (`loss = 0`) và `parity_ok = OK` | 32/32 lượt đạt `loss_vs_sent = 0` và `parity_ok = OK` trên cả 4 validator | **ĐẠT** | `evidence:task1_ab_1m`, `evidence:task1_ab_4m` |
| **(b) Cải thiện ở 4 triệu tx** | $\bar{\Delta}_{opt-base}$ tăng $\ge +3.0\%$ với $p < 0.05$ và $\bar{\Delta}_{opt-base} > s_{\Delta, AA}$ | Tăng **`+9.8104%`** (`+1274.5003 tx/s`), $p = 0.000018 < 0.05$, và $1274.5003 > 923.2918\text{ tx/s}$ ($s_{\Delta, AA}$) | **ĐẠT** | `evidence:summary_and_recheck` |
| **(c) Không hồi quy ở 1 triệu tx** | $\bar{\Delta}_{opt-base}$ không giảm quá $-2.0\%$ ($\ge -2.0\%$) | Tăng **`+4.4231%`** (`+679.1322 tx/s`, $p = 0.002375$), RSS đỉnh giảm `-7.9906%` | **ĐẠT** | `evidence:summary_and_recheck` |
| **(d) Bằng chứng cơ chế qua profile** | `runtime.gcDrain` cum% hoặc `inuse_space` của 3 mục tiêu giảm rõ rệt | Ở 4M tx: `gcDrain` giảm từ `37.39%` xuống `10.60%`, `inuse_space` giảm `-51.11%`, `inuse_objects` giảm `-67.25%` | **ĐẠT** | `evidence:task1_heap_analysis` |

---

## 3. Việc 2 — Đề xuất Thiết kế Chặn Backlog ở Lớp Nhận Tx (Chỉ Tài liệu)

Đã hoàn thành tài liệu thiết kế [`note/design_backlog_bound_20261015.md`](design_backlog_bound_20261015.md) [ĐỌC CODE]:
- Phân tích hành vi hiện tại của `MaxMempoolSize` (`execution/pkg/network/admission.go`, `execution/cmd/simple_chain/processor/message_handler.go`) và luồng đọc socket bất đồng bộ trong `execution/pkg/network/connection.go`.
- So sánh 3 phương án: (1) Drop/NACK ở ứng dụng (bác bỏ vì gây lỗ hổng nonce `nonce gap` cho client fire-and-forget); (2) Chặn đọc TCP bằng áp lực ngược có giới hạn (TCP Read Backpressure qua tách kênh/tạm dừng đọc cho kết nối client khi `txPool.Count() + len(msgChan)` vượt ngưỡng cao, giữ nguyên kết nối P2P nội bộ); (3) Kết hợp giới hạn kích thước hàng đợi `msgChan` của kết nối client.
- Phân tích tính bất biến Zero-Fork và tính tiến triển (không deadlock), kèm 4 câu hỏi thiết kế để chủ dự án quyết định trước khi triển khai.

---

## 4. Việc 3 — Thí nghiệm A/B Raft: Binary Lai Cũ (`f97a2fbf`) so với Binary Mới (`f85bf683`)

### 4.1 Thiết lập & Quy trình Worktree An toàn
- Dựng binary lai `bins_old/simple_chain` từ `git worktree` tách rời tại commit `f97a2fbf` (trước bản sửa `HandleConnection`), liên kết với thư viện Rust/C hiện tại qua 4 symlink tạm; sau khi biên dịch xong đã xóa từng symlink bằng `rm` trước khi `git worktree remove` và xác nhận các thư mục build ở cây chính còn nguyên vẹn [ĐO] (`evidence:preregistered`).
- Chạy 6 cặp xen kẽ ngẫu nhiên (`seed = 2026101502`, lưu tại [`task3_raft_ab/order.txt`](evidence/backlog_gc_followup_20261015/task3_raft_ab/order.txt)) với `run_raft_counted.py --txs 1000000 --duration 300` [ĐO] (`evidence:task3_raft_ab`).

### 4.2 Kết quả 6 Cặp A/B Raft (12 lượt)

| Cặp | Variant | Tag | Tx gửi (`bytes_sent_txs`) | Tx nhận (`received_sum`) | Tx commit (`committed_ours`) | Mất (`loss_vs_sent`) | Tỉ lệ commit (`commit_ratio`) | `tps_end_to_end` (tx/s) | `end_to_end_seconds` (s) | Dừng tạo block (`stalled`) | Parity | Bằng chứng |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | `old_f97a2fbf` | `raft_ab_old_p1_run1` | 715000 | 709000 | 709000 | 6000 | 0.991608 | 18706.6250 | 37.9010 | 1 | OK | `evidence:task3_raft_ab` |
| 1 | `new_f85bf683` | `raft_ab_new_p1_run1` | 1003936 | 1003936 | 1003936 | 0 | 1.000000 | 20667.1348 | 48.5764 | 0 | OK | `evidence:task3_raft_ab` |
| 2 | `old_f97a2fbf` | `raft_ab_old_p2_run1` | 634000 | 628000 | 628000 | 6000 | 0.990536 | 18971.9488 | 33.1015 | 1 | OK | `evidence:task3_raft_ab` |
| 2 | `new_f85bf683` | `raft_ab_new_p2_run1` | 1004057 | 1004057 | 1004057 | 0 | 1.000000 | 20532.3018 | 48.9013 | 0 | OK | `evidence:task3_raft_ab` |
| 3 | `old_f97a2fbf` | `raft_ab_old_p3_run1` | 672000 | 666000 | 666000 | 6000 | 0.991071 | 19220.2106 | 34.6510 | 1 | OK | `evidence:task3_raft_ab` |
| 3 | `new_f85bf683` | `raft_ab_new_p3_run1` | 1005138 | 1005138 | 1005138 | 0 | 1.000000 | 20132.4634 | 49.9262 | 0 | OK | `evidence:task3_raft_ab` |
| 4 | `old_f97a2fbf` | `raft_ab_old_p4_run1` | 671000 | 665000 | 665000 | 6000 | 0.991058 | 19954.4177 | 33.3260 | 1 | OK | `evidence:task3_raft_ab` |
| 4 | `new_f85bf683` | `raft_ab_new_p4_run1` | 1007111 | 1007111 | 1007111 | 0 | 1.000000 | 20242.9017 | 49.7513 | 0 | OK | `evidence:task3_raft_ab` |
| 5 | `old_f97a2fbf` | `raft_ab_old_p5_run1` | 664000 | 658000 | 658000 | 6000 | 0.990964 | 18573.7846 | 35.4263 | 1 | OK | `evidence:task3_raft_ab` |
| 5 | `new_f85bf683` | `raft_ab_new_p5_run1` | 1006968 | 1006968 | 1006968 | 0 | 1.000000 | 20934.2402 | 48.1015 | 0 | OK | `evidence:task3_raft_ab` |
| 6 | `old_f97a2fbf` | `raft_ab_old_p6_run1` | 653000 | 646000 | 646000 | 7000 | 0.989280 | 18915.9173 | 34.1511 | 1 | OK | `evidence:task3_raft_ab` |
| 6 | `new_f85bf683` | `raft_ab_new_p6_run1` | 1005634 | 1005634 | 1005634 | 0 | 1.000000 | 19660.0950 | 51.1510 | 0 | OK | `evidence:task3_raft_ab` |

### 4.3 Phân tích & Kết luận Việc 3
- **Tái hiện đầy đủ hiện tượng cũ trên `bins_old` (`f97a2fbf`):** Trong **cả 6/6 lượt**, `bins_old` dừng tạo block mang giao dịch sau `33.10–37.90 s` (`no new tx-bearing block for 8s`), chỉ gửi được `668166.6667 ± 26947.4798 tx` (do `HandleConnection` cũ đọc đồng bộ chặn socket TCP khiến client bị kẹt ở `inject_seconds = 300.01 s`) và làm rơi `6000–7000 tx` đang nằm trong hàng đợi khi kết nối đóng (`committed_ours = 662000.0000 ± 27062.8897 tx`, mất ~33.8% so với mục tiêu 1 triệu tx) [ĐO][TÍNH] (`evidence:task3_raft_ab`, `evidence:summary_and_recheck`).
- **Xác nhận bản sửa trên `bins_base` (`f85bf683`):** Trong **cả 6/6 lượt**, `bins_base` hoàn tất toàn bộ `1005474.0000 ± 1372.8535 tx` trong `5.31–5.38 s` nạp và `48.10–51.15 s` đầu-cuối, `loss_vs_sent = 0`, `stalled = 0/6`, tăng `+52.0922%` số giao dịch được commit ($t = 30.8052, df = 5, p = 0.000001$) và tăng `+6.9233%` tốc độ `tps_end_to_end` từ `19057.1507 ± 493.0302` lên `20361.5228 ± 449.2459 tx/s` ($t = 4.0506, df = 5, p = 0.009820$) [TÍNH] (`evidence:summary_and_recheck`).
- Như vậy, hiện tượng "Raft sustained dừng tạo block giữa chừng và hụt giao dịch" trong báo cáo cũ đã được giải thích bằng thực nghiệm A/B có đối chứng: nguyên nhân trực tiếp là lỗi `HandleConnection` chặn đọc / bỏ hàng đợi trong mã Go cũ `f97a2fbf` [ĐO] (`evidence:task3_raft_ab`).

---

## 5. Việc 4 — Kiểm tra Khả năng Phân rã Chi phí Rust/FFI qua `perf`

Đã kiểm tra trực tiếp không dùng `sudo` và lưu kết quả nguyên văn tại [`task4_perf/perf_check.txt`](evidence/backlog_gc_followup_20261015/task4_perf/perf_check.txt) [ĐO] (`evidence:task4_perf`):
- `which perf`: `/usr/bin/perf` có sẵn trên máy.
- `/proc/sys/kernel/perf_event_paranoid`: giá trị hiện tại là **`4`** (mức chặn nghiêm ngặt trên Ubuntu/Debian kernel).
- Thử chạy `perf record -o /tmp/perf_test.data -- sleep 0.1` trên chính tiến trình của user hiện tại trả về lỗi:
  `Access to performance monitoring and observability operations is limited. Consider adjusting /proc/sys/kernel/perf_event_paranoid setting to open access to performance monitoring and observability operations for processes without CAP_PERFMON, CAP_SYS_PTRACE or CAP_SYS_ADMIN Linux capability.`
- Theo đúng quy tắc của kế hoạch ("Nếu không làm được nếu không có quyền quản trị, ghi 'bị chặn: cần quyền X' và dừng việc này, không dùng sudo, không đòi mật khẩu"), Việc 4 được ghi nhận ở trạng thái **Chưa làm được (bị chặn: `kernel.perf_event_paranoid = 4`, cần quyền `CAP_PERFMON` hoặc `CAP_SYS_ADMIN` hoặc hạ `perf_event_paranoid <= 1`)** [ĐO] (`evidence:task4_perf`).

---

## 6. Việc 5 — Tự Kiểm tra Độc lập, Dọn dẹp và Cập nhật Sổ Kê khai

### 6.1 Tự kiểm tra độc lập từ tệp JSON thô (Mandatory Self-Check)
Một đoạn mã độc lập bên ngoài `extract_summary.py` đã đọc trực tiếp 16 tệp `task1_ab_4m/ab_4m_{base,opt}_p{1..8}_run1.json` và gọi `stats_util.py` để tính lại thống kê quan trọng nhất của báo cáo (lưu tại [`independent_recheck.txt`](evidence/backlog_gc_followup_20261015/independent_recheck.txt)) [TÍNH] (`evidence:summary_and_recheck`):
- `mean_base = 12992.3532 ± 156.6271 tx/s`
- `mean_opt = 14266.8535 ± 385.8287 tx/s`
- `mean_diff = +1274.5003 ± 350.3588 tx/s` (`+9.8104%`)
- `t_stat = 10.2890, df = 7, p_two_tailed = 0.000018`
Kết quả khớp tới chữ số thập phân thứ 4 với `stats_summary.json` và bảng trong Mục 2.3 [TÍNH] (`evidence:summary_and_recheck`).

### 6.2 Dọn dẹp Môi trường
- Đã dừng toàn bộ tiến trình thử nghiệm, xóa `/tmp/gate_4val_tps_counted`, `/tmp/raft_counted_cluster`, các bản binary tạm trong `scratch/bins_*`, và kiểm tra `ss -ltn | grep -c ":31[0-9][0-9][0-9]"` trả về `0`.
- Dung lượng thư mục bằng chứng `note/evidence/backlog_gc_followup_20261015/` là ~7,2 MB (dưới ngưỡng 10 MB của `verify_evidence.py --strict`).

---

## Giới hạn

1. **Môi trường 1 máy dùng chung (4 validator trên cùng host):** Dù thiết kế ghép cặp xen kẽ (interleaved paired A/B) triệt tiêu độ trôi nền theo thời gian, nhiễu tức thời khi bộ nhớ hệ thống chạm ngưỡng cao vẫn có thể xuất hiện ở các lượt 4 triệu tx (ví dụ lượt `aa_a_p4_run1` ở Việc 0.3 và RSS đỉnh của `ab_4m_opt_p3_run1` ở Việc 1.4) [ĐO] (`evidence:task0_aa_4m`, `evidence:task1_ab_4m`).
2. **Binary cũ trong Việc 3 là bản lai (`bins_old`):** Gồm mã Go tại `f97a2fbf` liên kết với thư viện Rust/C hiện tại để cô lập đúng biến số tầng Go (`HandleConnection`), không biên dịch lại Rust tại thời điểm `f97a2fbf` [ĐO] (`evidence:preregistered`).
3. **Chưa phân rã được bên trong `runtime.cgocall` (22–30% CPU):** Do `kernel.perf_event_paranoid = 4` chặn `perf record` không có `CAP_PERFMON`/`CAP_SYS_ADMIN` [ĐO] (`evidence:task4_perf`).

---

## Chưa làm

1. **Việc 4 (Lấy mẫu `perf` bên trong Rust/CGO FFI):** Bị chặn bởi `kernel.perf_event_paranoid = 4` (cần quản trị viên cấp `CAP_PERFMON` cho `/usr/bin/perf` hoặc đặt `sysctl kernel.perf_event_paranoid=1`) [CHƯA ĐO] (`evidence:task4_perf`).
2. **Triển khai mã chặn backlog ở lớp nhận tx (Việc 2):** Theo đúng yêu cầu của kế hoạch, Việc 2 chỉ viết tài liệu thiết kế [`note/design_backlog_bound_20261015.md`](design_backlog_bound_20261015.md) và chờ chủ dự án duyệt phương án cùng 4 tham số ngưỡng trước khi viết mã.
3. **Các mục ngoài phạm vi kế hoạch:** Lỗi hiếm `nomt_ffi: failed to commit payload` (đang chờ log `{:#}` nếu tái발), thu hồi token/đổi mật khẩu, và quyết định cutover chain 991.

---
### 📋 Tóm tắt thay đổi
- **Đã thay đổi:**
  - Code Go & Unit tests (commit `c270b281`): `execution/pkg/smart_contract/execute_sc_result.go`, `execution/pkg/smart_contract/execute_sc_result_test.go`, `execution/pkg/blockchain/blockchain.go`, `execution/pkg/blockchain/tx_processor/signature_cache.go`, `execution/pkg/blockchain/tx_processor/signature_cache_test.go`, `execution/pkg/blockchain/tx_processor/tx_processor.go`, `execution/pkg/blockchain/tx_processor/tx_validator_worker.go`, `execution/pkg/blockchain/tx_processor/verify.go`, `execution/cmd/simple_chain/processor/receipt_tracker.go`, `execution/cmd/simple_chain/processor/receipt_tracker_test.go`, `execution/cmd/simple_chain/processor/message_handler_helper.go`, `execution/cmd/simple_chain/processor/transaction_processor.go`.
  - Tài liệu & Bằng chứng: `note/design_backlog_bound_20261015.md`, `note/backlog_gc_followup_report_20261015.md`, `note/CLAIMS_LEDGER.md`, và thư mục `note/evidence/backlog_gc_followup_20261015/`.
- **🛠️ Giải pháp áp dụng:**
  - Cấp phát lười (lazy-init) các map trong `ExecuteSCResult` và bỏ cấp phát map rỗng ở đường nhanh chuyển tiền tự nhiên (`processNativeTransfersFastPath`).
  - Thay thế `sync.Map` lưu khóa chuỗi và con trỏ trong `verifiedSignatureCache` và `ReceiptTracker.txHashToConnection` bằng 64 shard `sync.RWMutex` + map khóa `common.Hash` tới value struct phẳng, giảm `-67.25%` số đối tượng sống trên heap Go (`32.52M -> 10.65M`), giảm `-51.11%` `inuse_space` (`2266.66 MB -> 1108.07 MB`), giảm tỷ trọng CPU `runtime.gcDrain` từ `37.39%` xuống `10.60%` ở 4M tx (`evidence:task1_heap_analysis`), giúp TPS đầu-cuối tăng `+9.8104%` ở 4M tx ($p = 0.000018$) và tăng `+4.4231%` ở 1M tx ($p = 0.002375$) (`evidence:summary_and_recheck`).
  - Thực hiện đối chứng A/B Raft 6 cặp xác nhận lỗi dừng tạo block ở bản cũ `f97a2fbf` (6/6 lượt dừng) đã được khắc phục ở bản mới `f85bf683` (0/6 lượt dừng, tăng `+52.0922%` số tx commit) (`evidence:summary_and_recheck`).
- **Blast radius:** Nội bộ tầng thực thi Go (`smart_contract`, `tx_processor`, `simple_chain/processor`); giữ nguyên giao thức mạng, cấu trúc block, state root và FFI Rust.
- **🐛 Nguyên nhân lỗi:**
  - Suy giảm TPS khi backlog lớn (Việc 1): giữ hàng chục triệu đối tượng con trỏ (`sync.newEntry`, khóa `string` 32 byte, map rỗng trong `ExecuteSCResult`) cho các giao dịch đang chờ/vừa xử lý làm `runtime.gcDrain` chiếm tới `37.39%` CPU (`evidence:task1_heap_analysis`).
  - Raft dừng tạo block giữa chừng (Việc 3): `HandleConnection` trong mã Go cũ (`f97a2fbf`) đọc đồng bộ chặn TCP và bỏ hàng đợi khi đóng kết nối (`evidence:task3_raft_ab`).
  - Không lấy được mẫu `perf` Rust/FFI (Việc 4): bị chặn bởi cấu hình kernel `perf_event_paranoid = 4` khi không có quyền `CAP_PERFMON`/`CAP_SYS_ADMIN` (`evidence:task4_perf`).
- **Rủi ro tiềm ẩn:** Không thay đổi logic đồng thuận hay thứ tự giao dịch; đảm bảo nguyên tắc không fork (thà pending chứ không fork, miễn đủ số node hoạt động thì hệ thống luôn tiến triển không deadlock) — đã kiểm chứng qua toàn bộ 54 lượt chạy thực nghiệm trong phiên (`loss_vs_sent = 0`, `parity_ok = OK` trên mọi lượt của `bins_base` và `bins_opt`) (`evidence:task1_ab_4m`).
- **Lưu ý hiệu năng:** Ở mức tải 4 triệu tx, thời gian hoàn tất đầu-cuối giảm `-27.45 s` (`-8.89%`) và thông lượng tăng `+1274.50 tx/s` (`+9.81%`); ở 1 triệu tx, thông lượng tăng `+679.13 tx/s` (`+4.42%`) và tổng RSS đỉnh của 4 validator giảm `-3774.25 MB` (`-7.99%`) (`evidence:summary_and_recheck`).
---
