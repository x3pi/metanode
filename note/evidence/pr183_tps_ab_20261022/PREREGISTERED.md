# Bản đăng ký trước (Preregistration) — A/B TPS Raft: trước và sau PR #183 (2026-10-22)

- **Ngày đăng ký:** 2026-10-22 (ghi trước khi chạy bất kỳ lượt đo nào)
- **Câu hỏi:** PR #183 (`fix(raft,processor)`: kiểm kích thước admission cho cả đường batch, forwarder cắt batch theo số lượng và kích thước mã hóa, `submitLocal` đề xuất theo chunk có reset timer, `forwardToLeader` cắt batch, giới hạn hiệu dụng `effectiveBatchLimit`) có làm thay đổi TPS đầu-cuối của cụm Raft 3 replica không?
- **Commit `bins_prev`:** `3ba207e7` (commit ngay trước PR #183 trên `dev`)
  - `bins_prev/simple_chain` SHA256: `0d4166f907a2e4a6fff761337046b3a07f61704331413efcf52059053eacbcda`
- **Commit `bins_next`:** `a167cfad` (squash của PR #183; chỉ có đúng một commit giữa hai mốc — `git log 3ba207e7..a167cfad`)
  - `bins_next/simple_chain` SHA256: `5011fa7623e071293f44493e98f861a3aeb6b88d6dd494398a193f64fc8703c9`
- **`secp_tps_blast` (dùng chung cho hai nhánh, build từ `a167cfad`):** SHA256 `ce4fa60c5818bffd2e0d494722314c02a134b4ba50bdeb306111b94b2ddd5636`
- **Kiểm tra binary khớp commit:** hai `simple_chain` có SHA256 khác nhau; bảng ký hiệu (`go tool nm`): `bins_prev` không có `processor.splitForwardBatches`, `bins_next` có.
- **Lưu ý:** hai bản dùng cùng thư viện native (`libmtn_nomt`, MVM); chỉ khác phần Go của PR.

## 1. Thiết kế
- **Công cụ:** `execution/scripts/test/evidence/run_raft_counted.py` (Raft 3 replica, quorum 2/3, đếm tx theo block, `-verify-parity`), cụm sạch dựng lại cho mỗi lượt.
- **Lượt chạy:** `--mode sustained --runs 1 --max-txs 1000000 --duration 300 --batch 1000 --max-wait-sec 600`.
- **Số cặp:** **10** cặp xen kẽ (20 lượt). Thứ tự trong mỗi cặp xáo ngẫu nhiên với seed cố định `2026102201`, ghi ra `order.txt`. (Lần A/B trước với 6 cặp cho khoảng tin cậy 95% khoảng ±4%; tăng lên 10 cặp để thu hẹp.)
- **Môi trường:** máy dùng chung 104 lõi; các lượt chạy SAU khi lượt CI cục bộ đầy đủ kết thúc, để hai việc không tranh CPU. Tải máy ghi trong `env_before.txt`.
- **Phạm vi đường được phủ:** TCP → validator pool (admission) → forwarder → `raftfeed` (leader local, không qua follower forward); `propose_queue_size` mặc định 1024 nên không chạy nhánh chunk. Những nhánh khác của PR (forward qua follower, nhiều chunk) **không** nằm trong phép đo này.

## 2. Cổng toàn vẹn (Zero-Fork Invariant)
Cả 20/20 lượt phải đồng thời thỏa: `loss_vs_sent == 0`, `bytes_sent_txs == received_sum == committed_ours`, `parity_status` bắt đầu bằng `"OK:"`, và cả 3 replica còn sống cuối lượt. Nếu một lượt vi phạm: dừng, báo cáo nguyên văn, không loại lượt đó.

## 3. Chỉ số và quy tắc kết luận (đặt trước)
- **Chỉ số chính:** `tps_end_to_end` (hiệu ghép cặp `next − prev`, tính theo % của `prev` trong cùng cặp).
- **Thống kê:** trung bình hiệu, độ lệch chuẩn mẫu, kiểm định t ghép cặp hai phía, df = 9, khoảng tin cậy 95% (`execution/scripts/test/evidence/stats_util.py`).
- **Chỉ số phụ (chỉ mô tả):** `tps_during_injection`, `tps_active_commit_window`, `inject_seconds`, `end_to_end_seconds`.
- **Quy tắc:** suy giảm nếu trung bình hiệu ≤ −3,0% và p < 0,05; cải thiện nếu ≥ +3,0% và p < 0,05; ngược lại "không phát hiện khác biệt có ý nghĩa với n = 10" (không có nghĩa là hai bản bằng nhau).
- **Giới hạn đã biết:** chưa đo nhiễu A/A [CHƯA ĐO]; kết quả báo cáo nguyên văn, kể cả khi không cải thiện hoặc giảm ở chỉ số phụ; không loại lượt nào sau khi thấy số.
