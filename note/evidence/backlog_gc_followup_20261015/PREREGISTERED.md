# Bản đăng ký trước (Preregistration) — Kế hoạch giảm chi phí GC khi backlog lớn & A/B Raft (2026-10-15)

- **Ngày đăng ký:** 2026-10-15 (commit trước khi chạy bất kỳ lượt đo nào)
- **Tài liệu kế hoạch:** `note/plan_backlog_gc_followup_20261015.md`
- **Commit mã nguồn gốc (`bins_base`):** `f85bf683f223b6dbcb60bf1b5ebd4a355a342da6`
- **Commit mã Go đối chứng cũ (`bins_old`, bản lai Go `f97a2fbf` + Rust `f85bf683`):** `f97a2fbf`

---

## 1. Thí nghiệm 0A (Việc 0.2) — Kiểm tra nền (Sanity Check) với `bins_base`
- **Cấu hình:** BFT 4 validator (`run_tps_counted.py`), cụm sạch từ `/tmp/gate_4val_clean_template` mỗi lượt, chế độ `sustained`, `-max-txs 1000000 -duration 300 -batch 1000 -max-wait-sec 600`.
- **Số lượt:** 2 lượt (`base_sanity_1m_run1`, `base_sanity_1m_run2`).
- **Tiêu chí đạt (Cổng dừng):** Cả 2/2 lượt phải thỏa mãn đồng thời:
  1. `bytes_sent_txs == received_sum == committed_ours` (`loss_vs_sent == 0`).
  2. `parity_status` bắt đầu bằng `"OK:"` trên cả 4 validator.
- **Hành động nếu không đạt:** Dừng ngay lập tức và báo cáo, không chạy các thí nghiệm tiếp theo.

---

## 2. Thí nghiệm 0B (Việc 0.3) — Đối chứng nhiễu A/A ở 4 triệu tx
- **Mục tiêu:** Định lượng nhiễu nội tại giữa hai lượt chạy cùng một binary (`bins_base` so với chính `bins_base`) ở khối lượng 4 triệu tx trên máy dùng chung.
- **Thiết kế:** 4 cặp ghép xen kẽ (tổng 8 lượt), hai nhãn nhánh ảo `aa_a` và `aa_b` đều trỏ tới cùng binary `bins_base`. Thứ tự trong mỗi cặp được xáo ngẫu nhiên với seed cố định `2026101500` và ghi ra `order.txt`.
- **Cấu hình mỗi lượt:** `run_tps_counted.py --mode sustained --runs 1 --duration 300 --max-txs 4000000 --batch 1000 --max-wait-sec 900`.
- **Thống kê ghi nhận:**
  - Với mỗi cặp $i \in \{1..4\}$, tính hiệu ghép cặp $\Delta_i = \text{TPS}_{\text{end\_to\_end}}(B_i) - \text{TPS}_{\text{end\_to\_end}}(A_i)$ và tỷ lệ phần trăm tương ứng.
  - Tính trung bình mẫu $\bar{\Delta}_{AA}$, độ lệch chuẩn mẫu của hiệu từng cặp $s_{\Delta, AA}$ (`sample_sd`), và kiểm định t ghép cặp hai phía (`t_two_tailed_p(t, df=3)`).
  - Giá trị $s_{\Delta, AA}$ (theo tx/s và theo %) được dùng làm ngưỡng nhiễu nền bắt buộc cho Thí nghiệm 1.4.

---

## 3. Thí nghiệm 3 (Việc 3.2) — A/B Raft: Binary cũ lai (`bins_old`) so với Binary mới (`bins_base`)
- **Câu hỏi nghiên cứu:** Hiện tượng "Raft sustained dừng tạo block giữa chừng, 19–23% tx không lên chuỗi" từng ghi nhận ở `note/tps_full_measurement_report_20261009.md` mục 5 có xuất hiện trên binary cũ lai (`bins_old`, mã Go tại `f97a2fbf` + Rust hiện tại) và biến mất trên binary mới (`bins_base`, HEAD `f85bf683`) khi đo bằng `run_raft_counted.py` ở khối lượng 1 triệu tx hay không?
- **Thiết kế:** 6 cặp ghép xen kẽ (tổng 12 lượt: 6 lượt `old`, 6 lượt `new`), thứ tự trong mỗi cặp xáo ngẫu nhiên với seed cố định `2026101503` và ghi ra `order.txt`.
- **Cấu hình mỗi lượt:** `run_raft_counted.py --mode sustained --runs 1 --max-txs 1000000 --duration 300 --batch 1000 --max-wait-sec 600`.
- **Chỉ số & Tiêu chí đánh giá:**
  1. Tỷ lệ giao dịch lên chuỗi `committed_ours / bytes_sent_txs` và số giao dịch thiếu `loss_vs_sent`.
  2. Tốc độ đầu-cuối `tps_end_to_end` (trung bình ± SD của mỗi nhánh, hiệu ghép cặp `new - old`, kiểm định t ghép cặp với `df = 5`).
  3. Số replica sống cuối lượt (`alive_at_end` cho `n0, n1, n2`) và trạng thái đồng thuận `parity_status`.
  4. Hiện tượng dừng tạo block giữa chừng (kiểm tra `measurement_stop_reason`, chuỗi block trong JSON report, và `*_events.txt`).
- **Quy tắc kết luận:**
  - Nếu `bins_old` tái hiện việc dừng tạo block / mất tx còn `bins_base` commit đủ 100% ở cả 6 cặp → kết luận các bản sửa phía Go giữa `f97a2fbf` và `f85bf683` đã triệt tiêu hiện tượng này trong điều kiện đo.
  - Nếu `bins_old` cũng không dừng tạo block giữa chừng → ghi đúng "không tái hiện hiện tượng cũ trong phạm vi này", không suy diễn nguyên nhân ngoài dữ liệu.

---

## 4. Thí nghiệm 1.4 (Đăng ký trước cho giai đoạn sau khi Chủ dự án duyệt thiết kế Việc 1.2)
- **Điều kiện kích hoạt:** Chỉ chạy SAU KHI Chủ dự án duyệt thiết kế đề xuất ở Việc 1.2 và mã sửa vượt qua `go test -race` + `build_check.sh`.
- **Thiết kế:** Ghép cặp xen kẽ giữa `bins_base` và `bins_opt`, ít nhất 8 cặp ở mức 1 triệu tx (`-max-txs 1000000`) và ít nhất 8 cặp ở mức 4 triệu tx (`-max-txs 4000000`), thứ tự ngẫu nhiên có seed cố định `2026101501` (4M) và `2026101502` (1M); kèm lượt chụp `--profile-at` giữa pha xả backlog.
- **Tiêu chí ĐẠT (tất cả điều kiện phải thỏa đồng thời):**
  - **(a) Toàn vẹn & Zero-Fork:** Mọi lượt chạy đều có `bytes_sent_txs == received_sum == committed_ours` (`loss_vs_sent == 0`) và `parity_status` là `OK` trên cả 4 validator.
  - **(b) Cải thiện ở 4 triệu tx:** TPS đầu-cuối (`tps_end_to_end`) của `bins_opt` ở 4 triệu tx tăng trung bình $\ge +3,0\%$ so với `bins_base`, có ý nghĩa thống kê với $p < 0,05$ (kiểm định t ghép cặp hai phía, `df >= 7`), VÀ độ tăng tuyệt đối vượt độ lệch chuẩn nhiễu A/A ($\bar{\Delta}_{opt-base} > s_{\Delta, AA}$ từ Thí nghiệm 0B).
  - **(c) Không suy giảm ở 1 triệu tx:** TPS đầu-cuối trung bình của `bins_opt` ở 1 triệu tx không giảm quá $2,0\%$ so với `bins_base` ($\bar{\Delta}_{1M} \ge -2,0\%$).
  - **(d) Bằng chứng cơ chế qua profile:** Lượt chụp `--profile-at` ở 4 triệu tx cho thấy tỷ lệ CPU của GC (`runtime.gcDrain` cum%) hoặc dung lượng heap Go đang sống (`inuse_space` tổng hoặc của các hàm mục tiêu) giảm so với `bins_base`.
- **Quy tắc kết luận:** Nếu bất kỳ tiêu chí nào trong (a)–(d) không thỏa, báo cáo trung thực là **"Không đạt"**, giữ nguyên tiêu chí đã đăng ký.
