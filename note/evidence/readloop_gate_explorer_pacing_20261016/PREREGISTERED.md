# Bản đăng ký trước (Preregistration) — Backpressure Lớp Sớm tại `Connection.readLoop` & Điều Tiết `indexSingleBlock` -> `s.indexQueue` (2026-10-16)

- **Ngày đăng ký:** 2026-10-16 (ghi nhận trước khi chạy bất kỳ lượt đo A/B nào)
- **Tài liệu cơ sở:** `note/cgocall_backpressure_explorer_report_20261015.md` (§6.1, §6.2, §7.1, §7.2) và `note/CLAIMS_LEDGER.md` (Mục "Chưa làm", dòng 35)
- **Commit đối chứng cơ sở (`bins_prev`):** `86e49ecab44637d8c655df538644156607debc27` (mã thực thi Go/Rust trùng khớp `4a2b63879b7fadbbf2c3be0ce08db0d0c5e84d20`)
  - `bins_prev/simple_chain` SHA256: `bd95a149d8b04bc3ae4723bdde382417f2940de48539b889ea6dedb9b182be32`
  - `bins_prev/parent_chain` SHA256: `35152a71ec3364cde8ca917c2c805688f47126199e4f7e44991ab2e2b9405675`
  - `bins_prev/secp_tps_blast` SHA256: `bc9adf73ed71a66b3ace68e3d69acbbefbe9a3cc4bfb40d8377bfed4d9e66d6c`
- **Binary ứng viên (`bins_next`):** Biên dịch sau khi áp dụng 2 nhóm thay đổi (Backpressure lớp sớm tại `Connection.readLoop` + Điều tiết `indexSingleBlock` -> `s.indexQueue` & sửa gom lô `Commit()` 64 block)
  - `bins_next/simple_chain` SHA256: `ada7a3f70128c7b4304f0259dd9df56bc6ee971ea13b525c10923ed737bacfdb`
  - `bins_next/parent_chain` SHA256: `35152a71ec3364cde8ca917c2c805688f47126199e4f7e44991ab2e2b9405675`
  - `bins_next/secp_tps_blast` SHA256: `bc9adf73ed71a66b3ace68e3d69acbbefbe9a3cc4bfb40d8377bfed4d9e66d6c`

---

## 1. Nhóm thay đổi 1 — Đặt `loadTxAdmissionGate()` trực tiếp trong `Connection.readLoop` & Giảm `ConnRequestChanSize` từ `256` xuống `16`
- **Vấn đề ở `bins_prev` (`4a2b6387`):**
  1. Cổng `loadTxAdmissionGate()` (`waitNotOverloaded`) chỉ được kiểm tra tại `SocketServer.HandleConnection` (`execution/pkg/network/server.go:239`), nằm phía **sau** kênh đệm `conn.RequestChan()` (`ConnRequestChanSize = 256` batch = `256,000` giao dịch tại `execution/cmd/simple_chain/app.go:880`).
  2. Khi `inFlightCount > HighWatermark (300,000)`, dù `HandleConnection` dừng nhận từ `conn.RequestChan()`, goroutine `Connection.readLoop` (`execution/pkg/network/connection.go:908`) vẫn tiếp tục đọc và giải mã protobuf thêm `256` batch (`256,000` giao dịch) vào bộ nhớ Go trên mỗi kết nối trước khi bị chặn.
- **Thay đổi kỹ thuật ở `bins_next`:**
  1. Trong `Connection.readLoop` (`execution/pkg/network/connection.go:812-820, 915-935`), tạo ngữ cảnh `readCtx` gắn với kênh `quit` của kết nối (1 lần/kết nối, 0 cấp phát/batch) và kiểm tra `loadTxAdmissionGate()` trực tiếp cho các lệnh `isTxSubmissionCommand` **trước** khi đẩy vào `requestChan`.
  2. Đánh dấu `req.admitted = true` (`execution/pkg/network/request.go`) để `SocketServer.HandleConnection` (`execution/pkg/network/server.go:239`) không gọi lặp `gate(s.ctx)` lần thứ hai trên cùng `Request`.
  3. Giảm `socketCfg.ConnRequestChanSize` mặc định trong `execution/cmd/simple_chain/app.go:881` từ `256` batch (`256,000` tx) xuống `16` batch (`16,000` tx, cho phép ghi đè qua `META_CONN_REQUEST_CHAN_SIZE`).

---

## 2. Nhóm thay đổi 2 — Điều tiết tốc độ đẩy `indexSingleBlock` -> `s.indexQueue` & Sửa gom lô `explorerSvc.Commit()` mỗi 64 block
- **Vấn đề ở `bins_prev` (`4a2b6387`):**
  1. Trong `execution/pkg/config/config.go:399`, `ConfigApp.ExplorerQueueSize` mặc định bị gán cứng bằng `8192` (ghi đè `defaultQueueSize = 65536` của `explorer/service.go`), nhỏ hơn kích thước 1 block BFT dưới tải cao (`8,500–10,000` tx/block).
  2. Trong `execution/cmd/simple_chain/processor/block_processor_indexing.go:140`, điều kiện `if len(bp.indexingChannel) == 0 || bp.uncommittedIndexBlocks >= explorerCommitBatchBlocks` luôn đúng ở **mọi block** vì `indexSingleBlock` đọc từ `pendingIndexBlocks` trong `< 1ms` trong khi block mới chỉ commit mỗi `~400–500ms` (nên `len(bp.indexingChannel)` luôn bằng `0` ngay sau khi lấy block ra). Hệ quả là `explorerSvc.Commit()` (`_Cfunc_database_commit`) vẫn bị gọi ở **mọi block** (chiếm `11.63s` CPU trong cửa sổ 30s và khóa cả 32 mutex shard Xapian làm nghẽn 32 worker).
  3. Khi `s.indexQueue` (`8192`) đầy, `IndexTransactionFast` drop ngay lập tức và `indexSingleBlock` gọi `logger.Error` trên từng giao dịch bị drop (`690k–1.06M` dòng log/lượt).
- **Thay đổi kỹ thuật ở `bins_next`:**
  1. Nâng mặc định `ConfigApp.ExplorerQueueSize` trong `execution/pkg/config/config.go:399` từ `8192` lên `65536` (khớp `defaultQueueSize = 65536`).
  2. Trong `ExplorerSearchService.IndexTransactionFast` (`execution/pkg/explorer/service.go:230-250`), giữ nguyên đường nhanh không chặn (`select` 0 cấp phát khi hàng đợi còn chỗ), và khi `s.indexQueue` đầy tạm thời thì chờ có giới hạn tối đa `50ms` để điều tiết tốc độ của goroutine nền `indexSingleBlock` khớp với tốc độ tiêu thụ của 32 worker Xapian trước khi fallback sang drop.
  3. Giới hạn số block giữ trong `bp.pendingIndexBlocks` ở mức tối đa `maxPendingIndexBlocks = 32` (`bp.pendingIndexBlockCount`) tại `block_processor_commit.go` và `block_processor_indexing.go`.
  4. Trong `finalizeIndexedBlock` (`block_processor_indexing.go:155`), chỉ gọi `explorerSvc.Commit()` khi `bp.uncommittedIndexBlocks >= explorerCommitBatchBlocks` (`64` block); khi `indexingChannel` và `s.indexQueue` đều rỗng (`explorerSvc.QueueDepth() == 0`), `tickerSync` (`2s`) trong `startIndexingProcess` sẽ flush các block còn lại. Bỏ log `logger.Error` trùng lặp trên từng giao dịch bị drop trong `indexSingleBlock`, chỉ giữ log tổng hợp theo block tại `finalizeIndexedBlock`.

---

## 3. Thiết kế thí nghiệm A/B (`bins_prev` vs `bins_next`)
- **Cấu hình cụm:** BFT 4 validator (`run_tps_counted.py`), bản sao sạch từ `/tmp/gate_4val_clean_template` mỗi lượt, chế độ `sustained`, `--duration 300 --batch 1000`.
- **Thiết kế ghép cặp đan xen:**
  1. **Ở mức 1 triệu tx (`ab_1m`):** 4 cặp ghép đan xen (tổng 8 lượt: 4 lượt `prev`, 4 lượt `next`), thứ tự trong mỗi cặp xáo ngẫu nhiên với seed `2026101601`, kèm `--profile-at 20` ở Cặp 1 (`ab_1m_prev_p1`, `ab_1m_next_p1`).
  2. **Ở mức 4 triệu tx (`ab_4m`):** 4 cặp ghép đan xen (tổng 8 lượt: 4 lượt `prev`, 4 lượt `next`), thứ tự trong mỗi cặp xáo ngẫu nhiên với seed `2026101602`, kèm `--profile-at 150` ở Cặp 1 (`ab_4m_prev_p1`, `ab_4m_next_p1`).
- **Tiêu chí đánh giá:**
  1. **Toàn vẹn & Zero-Fork (Bắt buộc):** Cả 16/16 lượt chạy phải có `loss_vs_sent == 0` (hoặc `committed_ours == received_sum`) và `parity_status` bắt đầu bằng `"OK:"` trên cả 4 validator.
  2. **Hiệu quả điều tiết `indexSingleBlock` -> `s.indexQueue` & gom lô `Commit()`:**
     - Số dòng bất thường `node_anomaly_lines` (do drop hàng đợi Explorer) trên `bins_next` giảm mạnh về `0` (hoặc xấp xỉ `0`) so với `bins_prev` (`~690k` ở 1M, `~1.06M` ở 4M).
     - Trên profile CPU 30s (`cpu30.pb.gz`), thời gian `_Cfunc_database_commit` dưới `ExplorerSearchService.Commit` giảm mạnh nhờ gom thực sự mỗi 64 block thay vì chạy mỗi block.
  3. **Hiệu quả Backpressure Lớp Sớm tại `Connection.readLoop` & Thông lượng (`tps_end_to_end`, `inject_seconds`, `peak_rss_mb_tool`):**
     - Ghi nhận số tx mỗi kết nối đọc vào trước khi cổng chặn ở 4M tx (`inject_seconds` kéo dài hơn khi TCP flow control chặn sớm client ngay sau khi vượt `HighWatermark`, thay vì nuốt trước `256,000` tx/kết nối vào `conn.RequestChan()`), cùng thống kê kiểm định cặp (`Mean ± SD`, `t_stat`, `p_two_tailed`, `df = 3`) cho `tps_end_to_end`, `tps_active_commit`, `peak_rss_mb_tool`, và `node_anomaly_lines`.
