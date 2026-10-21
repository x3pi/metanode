# Báo cáo A/B TPS Raft — trước và sau PR #182 (2026-10-21)

Đăng ký trước: [PREREGISTERED.md](PREREGISTERED.md) (commit `cd1b042f`, ghi trước mọi lượt đo). Dữ liệu thô: `raft_ab_1m/ab_*_p*_run1.json`; tổng hợp: `raft_ab_1m/summary.csv`, `raft_ab_1m/stats_summary.json`; mã tính: `analyze_ab.py.txt` (dùng `execution/scripts/test/evidence/stats_util.py`).

## Kết quả
- [ĐO] Cổng toàn vẹn: 12/12 lượt có `loss_vs_sent = 0`, `committed_ours = bytes_sent_txs`, `parity_status` bắt đầu bằng `OK:` (3 replica đồng thuận hash + stateRoot) và cả 3 replica còn sống cuối lượt.
- [ĐO] `tps_end_to_end` (tx/s, trung bình ± SD mẫu, n = 6 mỗi nhánh): `bins_prev` (4aee6172) 23607.8 ± 496.7; `bins_next` (8c7233ec) 23513.2 ± 782.0.
- [TÍNH] Hiệu ghép cặp `next − prev`: -94.7 tx/s (-0.37%, SD của hiệu theo % = 3.72%); t = -0.268, df = 5, p hai phía = 0.7992; khoảng tin cậy 95% của hiệu trung bình: [-4.28%, +3.54%].
- [TÍNH] Hiệu từng cặp (%): -5.06, +0.30, -1.46, -3.08, +1.55, +5.52.
- Theo quy tắc đặt trước (suy giảm nếu ≤ −3,0% và p < 0,05; cải thiện nếu ≥ +3,0% và p < 0,05): **no_significant_difference_n6** — không phát hiện khác biệt có ý nghĩa với n = 6.

## Giới hạn (đọc kèm kết quả)
- Khoảng tin cậy 95% rộng [-4.28%, +3.54%]: phép đo này **không loại trừ** được suy giảm hay cải thiện vài phần trăm. Nó chỉ cho thấy không có thay đổi lớn hơn khoảng 4%. Không có nghĩa là hai bản bằng nhau.
- Chưa đo nhiễu A/A trong đợt này [CHƯA ĐO]; máy dùng chung, cụm CI cục bộ chạy rảnh song song (`env_before.txt`).
- Chỉ đo 1M tx, Raft 3 replica cùng một máy, chế độ sustained; chưa đo 4M tx, chưa đo dưới restart/chaos, chưa đo BFT.
- Mỗi lượt chỉ kéo dài khoảng 42 giây từ lúc gửi đến khi commit đủ (`end_to_end_seconds`), nên không phản ánh hành vi dài hạn (GC, backlog lớn).
