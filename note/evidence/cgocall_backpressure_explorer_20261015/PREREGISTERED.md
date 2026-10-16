# Bản đăng ký trước (Preregistration) — Phân rã `runtime.cgocall`, Chặn Backlog ở Lớp Nhận Tx (Phương án A) & Tối ưu `ExplorerSearchService`/`secp256k1` (2026-10-15)

- **Ngày đăng ký:** 2026-10-15 (commit trước khi chạy bất kỳ lượt đo A/B nào)
- **Tài liệu cơ sở:** `note/backlog_gc_followup_report_20261015.md` và `note/design_backlog_bound_20261015.md`
- **Commit đối chứng cơ sở (`bins_prev`):** `15b5a3b48272eb33db1ff792d0166ab92e145e49`
  - `bins_prev/simple_chain` SHA256: `e494c83ac60de6806ffd84f2c2de40cbd6d37f069579a0af626d8c0ec0d61f35`
  - `bins_prev/parent_chain` SHA256: `35152a71ec3364cde8ca917c2c805688f47126199e4f7e44991ab2e2b9405675`
  - `bins_prev/secp_tps_blast` SHA256: `8c27c508dd9a4e26bbc3e21144004a38eae7d8c9722a95e3295ca5843529f82c`
- **Binary ứng viên (`bins_next`):** Biên dịch sau khi áp dụng 3 nhóm thay đổi (Hướng 2 + Hướng 3 Phần 1 + Hướng 3 Phần 2)
  - `bins_next/simple_chain` SHA256: `a0e162658f6cfa46f4724318ac80fdb1d9831a3d047a3721ce7579d63a4012b2`
  - `bins_next/parent_chain` SHA256: `35152a71ec3364cde8ca917c2c805688f47126199e4f7e44991ab2e2b9405675`
  - `bins_next/secp_tps_blast` SHA256: `8c27c508dd9a4e26bbc3e21144004a38eae7d8c9722a95e3295ca5843529f82c`

---

## 1. Hướng 1 — Phân rã chi tiết `runtime.cgocall` từ dữ liệu pprof sẵn có
- **Dữ liệu đầu vào:** 4 file profile CPU 30 giây từ `note/evidence/backlog_gc_followup_20261015/`:
  - `task1_ab_4m/ab_4m_opt_p1_run1_val0_cpu30.pb.gz`
  - `task1_ab_4m/ab_4m_base_p1_run1_val0_cpu30.pb.gz`
  - `task1_ab_1m/ab_1m_opt_p1_run1_val0_cpu30.pb.gz`
  - `task1_ab_1m/ab_1m_base_p1_run1_val0_cpu30.pb.gz`
- **Công cụ trích xuất:** `/tmp/p06_bins/pprof -peek="runtime\.cgocall$"` và `/tmp/p06_bins/pprof -peek="runtime\.cgocallback"`, ghi nguyên văn tại `note/evidence/cgocall_backpressure_explorer_20261015/cgocall_breakdown.txt`.

---

## 2. Hướng 2 — Triển khai Chặn Backlog ở Lớp Nhận Tx (Phương án A từ `note/design_backlog_bound_20261015.md`)
- **Thay đổi kỹ thuật:**
  1. Bổ sung bộ đếm nguyên tử `inFlightCount atomic.Int64` trong `ReceiptTracker` (`execution/cmd/simple_chain/processor/receipt_tracker.go`), chỉ tăng khi thêm khóa mới vào `txHashConnectionMap` (`Swap`) và giảm đúng 1 lần khi lấy ra tại `BroadCastReceipts` (`TakeTxHashConnEntry`) hoặc dọn quá hạn tại `cleanupTxHashConnectionMap` (`DeleteTxHashConnEntry`).
  2. Mở rộng `computeSystemLoad(tp, bp)` trong `execution/cmd/simple_chain/processor/processors.go` để lấy `max(poolSize + resultChanSize + virtualChainSize, inFlight)`, có cơ chế tự hồi phục (`idleOverloadTicks >= 25` tương đương 5 giây không có block mới khi `baseLoad == 0`) để chống lệch bộ đếm vĩnh viễn.
  3. Ngưỡng mặc định trên mỗi validator: `HighWatermark = 300000`, `LowWatermark = 150000` (cho phép ghi đè qua `META_INGRESS_HIGH_WATERMARK` và `META_INGRESS_LOW_WATERMARK`).

---

## 3. Hướng 3 — Khử trùng lặp `secp256k1` (`ValidateEnvelopeBinding`) & Tối ưu `ExplorerSearchService` / `goxapian`
- **Phần 1 (`secp256k1` — 34,60% của `runtime.cgocall` ở 4M):**
  1. `NewTransactionFromEth` (`execution/pkg/transaction/transaction.go`) gán sẵn `concrete.cachedEnvTx` cùng lúc với `concrete.cachedEthTx` để `validateEnvelopeBindingTx` tái sử dụng đối tượng `*e_types.Transaction` đã cache địa chỉ người gửi (`types.Sender`), và `ValidEthSign()` trả về `true` ngay sau khi `validateEnvelopeBindingTx(t)` thành công.
  2. `VerifyTransaction` (`execution/pkg/blockchain/tx_processor/validation.go`) và `ValidateEnvelopeBindingsParallel` (`execution/cmd/simple_chain/processor/speculative_executor.go`) kiểm tra/ghi nhận `IsVerifiedEnvelopeBound(tx)` / `RecordVerifiedEnvelopeBound(tx)` để không giải mã RLP và không gọi `secp256k1_ext_ecdsa_recover` lặp lại ở bước tạo block sau khi mempool đã xác thực.
- **Phần 2 (`goxapian` + `ExplorerSearchService` — 64,73% của `runtime.cgocall` & 26,66% tổng CPU ở 4M):**
  1. Bổ sung `database_index_document` (`execution/pkg/goxapian/xapian_wrapper.cpp`, `xapian.go`) gộp việc tạo `Xapian::Document` trên stack, gán data, tách danh sách term và gọi `add_document` (khi `replace_existing == false` cho block mới commit) hoặc `replace_document` (khi `replace_existing == true` cho quét bù) chỉ trong **1 lệnh CGO duy nhất**, không cấp phát `C.CString`/`C.free`.
  2. Đưa toàn bộ việc tạo chuỗi hex, đóng gói term và tuần tự hóa JSON (`marshalExplorerTxFull` dùng struct tĩnh `explorerTxFullJSON`) ra **ngoài** `s.locks[shardIndex].Lock()` trong `ExplorerSearchService.doIndexTransaction` (`execution/pkg/explorer/service.go`).
  3. Truyền trực tiếp `header`, `Transactions` và `Receipts` từ `commitWorker` sang `indexSingleBlock` qua `pendingIndexBlocks` (`execution/cmd/simple_chain/processor/block_processor_indexing.go`) để loại bỏ việc mở lại 2 trie (`txDB`, `rcpDb`) và giải mã 16.000 protobuf mỗi block; đồng thời gom lệnh `ExplorerSearchService.Commit()` theo lô (khi hàng đợi `indexingChannel` cạn hoặc đủ 64 block).

---

## 4. Thiết kế thí nghiệm A/B (`bins_prev` vs `bins_next`)
- **Cấu hình cụm:** BFT 4 validator (`run_tps_counted.py`), cụm sạch từ `/tmp/gate_4val_clean_template` mỗi lượt, chế độ `sustained`, `-duration 300 -batch 1000`.
- **Thiết kế ghép cặp xen kẽ:**
  1. **Ở mức 1 triệu tx (`ab_1m`):** 4 cặp ghép xen kẽ (tổng 8 lượt: 4 lượt `prev`, 4 lượt `next`), thứ tự trong mỗi cặp xáo ngẫu nhiên với seed `2026101512`, kèm `--profile-at 20` ở Cặp 1 (`ab_1m_prev_p1`, `ab_1m_next_p1`).
  2. **Ở mức 4 triệu tx (`ab_4m`):** 4 cặp ghép xen kẽ (tổng 8 lượt: 4 lượt `prev`, 4 lượt `next`), thứ tự trong mỗi cặp xáo ngẫu nhiên với seed `2026101511`, kèm `--profile-at 150` ở Cặp 1 (`ab_4m_prev_p1`, `ab_4m_next_p1`).
- **Tiêu chí đánh giá:**
  1. **Toàn vẹn & Zero-Fork (Bắt buộc):** Cả 16/16 lượt chạy phải có `bytes_sent_txs == received_sum == committed_ours` (`loss_vs_sent == 0`) và `parity_status` bắt đầu bằng `"OK:"` trên cả 4 validator.
  2. **Bằng chứng cơ chế qua pprof (`cpu30.pb.gz` & `heap.pb.gz` ở Cặp 1):**
     - Tỷ lệ CPU của `runtime.cgocall`, `ExplorerSearchService.doIndexTransaction`, `indexSingleBlock`, và `ValidateEnvelopeBindingsParallel` trên `bins_next` giảm rõ rệt so với `bins_prev`.
     - Ở mức 4 triệu tx, dung lượng bộ nhớ `val0_rss_mb` cực đại hoặc `inuse_space` của `ReceiptTracker` / `blockchain` được giới hạn nhờ cơ chế chặn backlog ở lớp nhận tx.
  3. **Thông lượng đầu-cuối (`tps_end_to_end`) và cửa sổ commit (`tps_commit_window`):** Ghi nhận trung bình ± SD của mỗi nhánh, hiệu ghép cặp $\Delta_i = \text{next}_i - \text{prev}_i$, và kiểm định t ghép cặp hai phía (`df = 3`) ở cả 1M và 4M tx.
