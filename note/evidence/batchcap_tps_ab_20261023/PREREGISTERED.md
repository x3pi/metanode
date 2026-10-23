# Bản đăng ký trước (Preregistration) — A/B TPS Raft: `maxTransactionsPerBatch` 2000 so với 4000 (2026-10-23)

- **Ngày đăng ký:** 2026-10-23 (ghi trước khi chạy bất kỳ lượt đo nào của tập đo chính)
- **Câu hỏi:** nâng hằng số `maxTransactionsPerBatch` của forwarder (`execution/cmd/simple_chain/processor/tx_batch_forwarder_core.go`, hiện 2000) lên 4000 có làm thay đổi TPS đầu-cuối của cụm Raft 3 replica không?
- **Commit nền (cả hai nhánh):** `bfe86427` (`dev`).
  - **`bins_A` (cap = 2000):** build nguyên trạng từ `bfe86427`. `simple_chain` SHA256 `790f5257b5094dbf536d4d1f1156a949cee201314bc20fd38806498e5661492c`.
  - **`bins_B` (cap = 4000):** `bfe86427` cộng đúng một thay đổi một dòng trong [`batchcap.diff.txt`](batchcap.diff.txt) (SHA256 `bd9ea4a5ff9b3369c326e3a0c6967765902de5ce5fdce20e1e2fc147dee8501c`). `simple_chain` SHA256 `d9190f959a5f63d619294ddfecb483340006aa00b0b79928452b482f5f2e279b`. Bản B **không được commit** vào `dev`; đây là bản thử nghiệm.
  - **`secp_tps_blast` (dùng chung, build từ `a167cfad`, giống A/B của PR #183):** SHA256 `ce4fa60c5818bffd2e0d494722314c02a134b4ba50bdeb306111b94b2ddd5636`.
- **Hai `simple_chain` có SHA256 khác nhau**; cùng thư viện native, cùng toolchain, cùng `GO_ROOT/cmd/simple_chain`, build bằng `CGO_ENABLED=1 go build` trong hai worktree riêng.
- **Giả thuyết `[GIẢ THUYẾT]` (hai phía, chưa biết chiều):** batch lớn hơn làm giảm số entry/block mỗi giây và chi phí Raft/fsync trên mỗi tx, nhưng làm mỗi block thực thi lâu hơn và tăng độ trễ; hiệu ứng ròng lên TPS chưa biết.

## 1. Thiết kế
- **Công cụ:** `execution/scripts/test/evidence/run_raft_counted.py` (Raft 3 replica, quorum 2/3, đếm tx theo block, `-verify-parity`), cụm sạch dựng lại cho mỗi lượt. Driver: [`driver_ab.py.txt`](driver_ab.py.txt).
- **Lượt chạy:** `--mode sustained --runs 1 --max-txs 1000000 --duration 300 --batch 1000 --max-wait-sec 600`, cấu hình Raft của harness (`propose_queue_size` = 2048, các tham số khác như harness).
- **Số cặp:** **10** cặp xen kẽ (20 lượt), thứ tự trong mỗi cặp xáo ngẫu nhiên với seed cố định `2026102301`, ghi ra `raft_ab_1m/order.txt`.
- **Môi trường:** máy dùng chung 104 lõi. Cụm CI cục bộ (5 node dưới `/opt/metanode`) và các monitor của nó **vẫn chạy ở trạng thái rảnh** trong suốt phép đo; ảnh chụp ở [`env_before.txt`](env_before.txt). Không có `ci.sh` hay A/B khác chạy cùng lúc.
- **Lượt chạy thử đã làm trước khi đăng ký (không đưa vào phân tích):** đúng **một** lượt chạy thử bản B với 200 000 tx để kiểm tra quy trình và việc đọc dòng `RAFT-METRICS`: `loss_vs_sent` 0, parity OK, `txs/entry` ≈ 3211 (160548 tx / 50 entry) `[ĐO]`. Chưa chạy thử bản A.

## 2. Cổng toàn vẹn (Zero-Fork Invariant)
Cả 20/20 lượt phải đồng thời thỏa: `loss_vs_sent == 0`, `bytes_sent_txs == committed_ours`, `parity_status` bắt đầu bằng `"OK:"`, và cả 3 replica còn sống cuối lượt. Nếu một lượt vi phạm: dừng, báo cáo nguyên văn, không loại lượt đó.

## 3. Kiểm chứng thao tác (manipulation check)
Với mỗi lượt, lấy dòng `RAFT-METRICS` cuối cùng của replica `n0` (`applied` = số entry, `txs` = số tx cộng dồn) và tính `txs/applied`. Điều kiện hợp lệ của phép so sánh: **trung bình `txs/applied` của nhánh B lớn hơn của nhánh A ít nhất 25%**. Nếu không thỏa, kết quả TPS không được diễn giải là hiệu ứng của cap và phải báo cáo như vậy. Đây là chỉ số mô tả, không dùng để loại lượt.

## 4. Chỉ số và quy tắc kết luận (đặt trước)
- **Chỉ số chính duy nhất:** `tps_end_to_end`, hiệu ghép cặp `B − A` theo % của `A` trong cùng cặp.
- **Thống kê:** trung bình hiệu, độ lệch chuẩn mẫu, kiểm định t ghép cặp hai phía, df = 9, khoảng tin cậy 95% (`execution/scripts/test/evidence/stats_util.py`).
- **Chỉ số phụ (chỉ mô tả):** `tps_during_injection`, `tps_active_commit_window`, `inject_seconds`, `end_to_end_seconds`, `txs/applied`.
- **Quy tắc:** cải thiện nếu trung bình hiệu ≥ +3,0% và p < 0,05; suy giảm nếu ≤ −3,0% và p < 0,05; ngược lại "không phát hiện khác biệt có ý nghĩa với n = 10" (không có nghĩa hai bản bằng nhau).
- **Giới hạn đã biết:** chưa đo nhiễu A/A `[CHƯA ĐO]`; chỉ phủ đường TCP → admission → forwarder → `raftfeed` trên leader với tải bão hòa 1M tx; mỗi lượt ngắn (khoảng nửa phút), không phản ánh hành vi dài hạn hay tải thấp; kết quả báo cáo nguyên văn, kể cả khi không cải thiện hoặc giảm ở chỉ số phụ; không loại lượt nào sau khi thấy số.
