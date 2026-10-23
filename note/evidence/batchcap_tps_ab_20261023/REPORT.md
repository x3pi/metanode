# Báo cáo A/B TPS Raft — `maxTransactionsPerBatch` 2000 so với 4000 (2026-10-23)

Đăng ký trước: [PREREGISTERED.md](PREREGISTERED.md) (commit `4c13c662`, ghi trước mọi lượt đo của tập chính). Dữ liệu thô: `raft_ab_1m/ab_*_p*_run1.json` và `ab_*_p*_metrics.txt`; tổng hợp: `raft_ab_1m/summary.csv`, `raft_ab_1m/stats_summary.json`; mã tính: `analyze_ab.py.txt` (dùng `execution/scripts/test/evidence/stats_util.py`). Hiệu tính theo `B − A` (B = cap 4000, A = cap 2000).

## Kết quả
- [ĐO] Cổng toàn vẹn: 20/20 lượt có `loss_vs_sent = 0`, `committed_ours = bytes_sent_txs`, `parity_status` bắt đầu bằng `OK:` (3 replica đồng thuận hash + stateRoot) và cả 3 replica còn sống cuối lượt.
- [ĐO] Kiểm chứng thao tác (đạt): số tx trung bình mỗi entry Raft (replica `n0`): A = 1839.3, B = 3555.4 (gấp 1.93 lần; ngưỡng đăng ký ≥ 1.25). Số block cuối lượt khoảng 549–557 ở A và 298–304 ở B.
- [ĐO] `tps_end_to_end` (tx/s, trung bình ± SD mẫu, n = 10 mỗi nhánh): A 23350.7 ± 395.5; B 26156.7 ± 454.7.
- [TÍNH] Hiệu ghép cặp `B − A`: +2806.0 tx/s (**+12.05%**, SD của hiệu theo % = 3.04%); t = 13.448, df = 9, p hai phía ≈ 2.9e-7; khoảng tin cậy 95% của hiệu trung bình: **[+9.88%, +14.23%]**.
- [TÍNH] Hiệu từng cặp (%): +7.91, +11.80, +12.95, +12.07, +10.54, +19.59, +11.82, +10.98, +9.99, +12.88. 10/10 cặp dương.
- [TÍNH] Chỉ số phụ (chỉ mô tả): `tps_active_commit_window` +12.11% (p ≈ 0); `end_to_end_seconds` 43.08 → 38.47 s (−10.69%); `tps_during_injection` +0.79% (p = 0.505, không khác biệt: phía client không đổi).
- Theo quy tắc đặt trước (cải thiện nếu ≥ +3,0% và p < 0,05): **improvement**.

## Giới hạn (đọc kèm kết quả)
- Chỉ đo chế độ **Raft** (3 replica, localhost) với tải bão hòa 1M tx trên leader. Hằng số `maxTransactionsPerBatch` nằm trong forwarder dùng chung với đường BFT/Rust; đường đó **không** được đo [CHƯA ĐO]. Giá trị 4000 cũng bằng giới hạn 4000 tx mỗi block hiện cài cho Rust consensus (`HOW_TO_TUNE_BLOCK_SIZE.md`), nên không được đổi hằng số chung nếu chưa kiểm tra đường BFT.
- Chỉ so hai giá trị (2000 và 4000). Không biết điểm tối ưu, và không biết 8000 (bằng `targetBlockSize`) tốt hay xấu hơn [CHƯA ĐO].
- Block lớn hơn thì mỗi block thực thi lâu hơn. Độ trễ commit của từng giao dịch, thời gian tới finality và hành vi khi tải thấp **không** được đo [CHƯA ĐO]; mỗi lượt chỉ khoảng 40 giây từ lúc gửi đến commit đủ.
- Chưa đo nhiễu A/A [CHƯA ĐO]; máy dùng chung. Cụm CI cục bộ rảnh và monitor của nó vẫn chạy trong suốt phép đo (`env_before.txt`); cả hai nhánh chịu cùng điều kiện nền.
- Bản B chỉ khác bản A đúng một hằng số (`batchcap.diff.txt`); bản B không có trong `dev`. Có một lượt chạy thử bản B (200 000 tx) trước khi đăng ký, không đưa vào phân tích.
