# Bản đăng ký trước (Preregistration) — A/B TPS Raft: trước và sau PR #182 (2026-10-21)

- **Ngày đăng ký:** 2026-10-21 (ghi trước khi chạy bất kỳ lượt đo nào)
- **Câu hỏi:** PR #182 (`fix(network, raft)`: chờ InitConnection xong, cắt batch Raft có tính overhead protobuf, forwarder nhận trạng thái chi tiết, bộ đếm height dời ra sau barrier block DB) có làm thay đổi TPS đầu-cuối của cụm Raft 3 replica không?
- **Commit `bins_prev`:** `4aee6172` (commit ngay trước PR #182 trên `dev`)
  - `bins_prev/simple_chain` SHA256: `27615b0330deefe0595ed061cdd574ed175a77a70db1530c890f7a6e3b240eba`
- **Commit `bins_next`:** `8c7233ec` (`dev` hiện tại: PR #182 squash `4cdf60b0` + sửa nhỏ `8c7233ec`)
  - `bins_next/simple_chain` SHA256: `6537a14d8f0f319042ecb9cc754eb0b98d57fdb1cf32c36ce49c3515e64ff27d`
- **`secp_tps_blast` (dùng chung cho cả hai nhánh, build từ `8c7233ec`):** SHA256 `17a566b4c8c764fb078c00d6945d2981534e67c080bd5c03c6f056334fd4a5ec`
- **Kiểm tra binary khớp commit:** hai `simple_chain` có SHA256 khác nhau. Bảng ký hiệu (`go tool nm`): `bins_prev` không có `raftfeed.SubmitBatchStatus` và `network.(*initDoneRequest)`; `bins_next` có cả hai.
- **Lưu ý khác biệt:** `bins_prev` và `bins_next` chỉ khác phần Go giữa `4aee6172` và `8c7233ec`; thư viện native (`libmtn_nomt`, MVM) là cùng một bản build.

## 1. Thiết kế
- **Công cụ:** `execution/scripts/test/evidence/run_raft_counted.py` (Raft 3 replica, quorum 2/3, đếm tx theo block, `-verify-parity`), cụm sạch dựng lại cho mỗi lượt.
- **Lượt chạy:** `--mode sustained --runs 1 --max-txs 1000000 --duration 300 --batch 1000 --max-wait-sec 600`.
- **Số cặp:** 6 cặp xen kẽ (12 lượt: 6 `prev`, 6 `next`). Thứ tự trong mỗi cặp xáo ngẫu nhiên với seed cố định `2026102101`, ghi ra `order.txt`.
- **Môi trường:** máy dùng chung 104 lõi, 188 GB RAM; trước lượt đầu có cụm CI cục bộ (`/opt/metanode/node-*`) đang chạy rảnh. Load average ghi lại trong `env_before.txt`.

## 2. Cổng toàn vẹn (Zero-Fork Invariant)
Cả 12/12 lượt phải đồng thời thỏa: `loss_vs_sent == 0`, `bytes_sent_txs == received_sum == committed_ours`, `parity_status` bắt đầu bằng `"OK:"`, và cả 3 replica còn sống cuối lượt. Nếu một lượt vi phạm: dừng, báo cáo nguyên văn, không loại lượt đó khỏi báo cáo.

## 3. Chỉ số và quy tắc kết luận (đặt trước)
- **Chỉ số chính:** `tps_end_to_end` (hiệu ghép cặp `next − prev`, tính theo % của `prev` trong cùng cặp).
- **Thống kê:** trung bình hiệu, độ lệch chuẩn mẫu, kiểm định t ghép cặp hai phía, df = 5 (`execution/scripts/test/evidence/stats_util.py`).
- **Chỉ số phụ (chỉ mô tả):** `tps_during_injection`, `tps_active_commit_window`, `inject_seconds`, `end_to_end_seconds`.
- **Quy tắc:**
  - **Suy giảm** nếu trung bình hiệu ≤ −3,0% và p < 0,05.
  - **Cải thiện** nếu trung bình hiệu ≥ +3,0% và p < 0,05.
  - Ngược lại: **"không phát hiện khác biệt có ý nghĩa với n = 6"** (không có nghĩa là hai bản bằng nhau).
- **Giới hạn đã biết:** chưa đo nhiễu A/A trong đợt này [CHƯA ĐO]. Kết quả báo cáo nguyên văn, kể cả khi không cải thiện hoặc giảm ở chỉ số phụ; không loại lượt nào sau khi thấy số.
