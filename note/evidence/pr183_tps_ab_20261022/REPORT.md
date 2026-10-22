# Báo cáo A/B TPS Raft — trước và sau PR #183 (2026-10-22)

Đăng ký trước: [PREREGISTERED.md](PREREGISTERED.md) (commit `21bdea6f`, ghi trước mọi lượt đo). Dữ liệu thô: `raft_ab_1m/ab_*_p*_run1.json`; tổng hợp: `raft_ab_1m/summary.csv`, `raft_ab_1m/stats_summary.json`; mã tính: `analyze_ab.py.txt` (dùng `execution/scripts/test/evidence/stats_util.py`).

## Kết quả
- [ĐO] Cổng toàn vẹn: 20/20 lượt có `loss_vs_sent = 0`, `committed_ours = bytes_sent_txs`, `parity_status` bắt đầu bằng `OK:` (3 replica đồng thuận hash + stateRoot) và cả 3 replica còn sống cuối lượt.
- [ĐO] `tps_end_to_end` (tx/s, trung bình ± SD mẫu, n = 10 mỗi nhánh): `bins_prev` (3ba207e7) 23625.6 ± 358.7; `bins_next` (a167cfad) 23353.0 ± 550.2.
- [TÍNH] Hiệu ghép cặp `next − prev`: -272.6 tx/s (-1.13%, SD của hiệu theo % = 3.04%); t = -1.192, df = 9, p hai phía = 0.2636; khoảng tin cậy 95% của hiệu trung bình: [-3.30%, +1.05%].
- [TÍNH] Hiệu từng cặp (%): +4.17, -0.10, -1.49, -2.25, -0.27, -1.21, -5.81, +0.31, -5.85, +1.24. 7/10 cặp âm; kiểm định dấu chính xác hai phía p = 0.34.
- Theo quy tắc đặt trước (suy giảm nếu ≤ −3,0% và p < 0,05; cải thiện nếu ≥ +3,0% và p < 0,05): **no_significant_difference** — không phát hiện khác biệt có ý nghĩa với n = 10.

## Giới hạn (đọc kèm kết quả)
- Cận dưới của khoảng tin cậy (-3.30%) hơi vượt qua ngưỡng −3%: phép đo **không loại trừ** được việc PR làm chậm cỡ 3%, và cũng không loại trừ cải thiện cỡ 1%. Trung bình −1,13% nằm trong độ nhiễu mà lần A/B trước đã thấy (SD hiệu từng cặp 3,7%); đây không phải bằng chứng hai bản bằng nhau.
- Chưa đo nhiễu A/A [CHƯA ĐO]; máy dùng chung.
- Phép đo chỉ phủ đường: TCP → admission → forwarder → `raftfeed` trên leader, cấu hình mặc định (`propose_queue_size` = 1024, không có nhiều chunk, không qua follower forward). Các nhánh còn lại của PR (forward qua follower, cắt batch theo chunk) chưa được đo [CHƯA ĐO].
- Mỗi lượt chỉ kéo dài khoảng 42 giây từ lúc gửi đến khi commit đủ, nên không phản ánh hành vi dài hạn.
- Lượt CI cục bộ đã kết thúc trước khi bắt đầu đo và `pgrep` không thấy tiến trình `ci.sh` nào lúc bắt đầu; tôi không kiểm soát được các tiến trình khác trên máy dùng chung (`env_before_runs.txt` chỉ ghi tải và bộ nhớ lúc đầu).
