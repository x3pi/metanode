# Kế hoạch giao Gemini: điều tra nguyên nhân và cải thiện TPS BFT/Raft (2026-10-08)

**Nền:** `note/tps_remeasure_bft_vs_raft_report_20261008.md` + evidence `note/evidence/tps_remeasure_20261008/` (cụm sạch mỗi lượt, build từ HEAD `3569c113`). Đọc kỹ trước khi làm; **đừng tin lại số từ các báo cáo Gemini cũ** (`tps_bft_peak_report`, `tps_raft_peak_report`, `tps_raft_vs_bft_comparison_report`), chúng mâu thuẫn với số đo lại.

## 0. Hiện trạng đã đo (trung vị, tx/s)

| Giao thức | BFT 4 validator | Raft 3 node |
|---|---|---|
| Burst 25k | 7.227 (6.542–10.612, SD 1.596) | 13.650 (SD 563) |
| Burst 20k | chưa đo | 19.100 |
| Sustained 60 s | 14.826 (12.830–15.062) | 9.998 (SD 174) |

## 1. Các câu hỏi cần trả lời bằng số đo (không suy luận)

- **Q1.** Vì sao Raft sustained (10,0k) thấp hơn BFT sustained (14,8k) trong khi Raft burst lại cao hơn? Raft tạo ~330–360 block (~1.800 tx/block) trong 60 s, BFT ~150–175 block (~5.000 tx/block). Nút thắt là cỡ block, hàng đợi propose, replicate Raft, hay FSM apply/commit state?
- **Q2.** Vì sao Raft burst 25k (13,7k) thấp hơn burst 20k (19,1k)? Có ngưỡng nào (`propose_queue_size`, `max_batch_bytes`, kích thước hàng đợi ingress) bị chạm giữa 20k và 25k tx?
- **Q3.** Vì sao BFT burst 25k dao động lớn giữa các lượt (6.542–10.612 tx/s, 3 block mỗi lượt, 8.333 tx/block)? Nguồn nhiễu: cách DAG gom commit, lịch propose, hay trạng thái lạnh (cache, JIT, NOMT)?
- **Q4.** Tại sao BFT sustained ổn định hơn nhiều so với burst? Phần nào của burst là chi phí khởi động (cold-start) và có thể giảm được?
- **Q5.** Trong sustained, độ trễ P50 báo ~68–70 s (lớn hơn thời gian chạy 60 s). Công cụ `secp_tps_blast` đang tạo backlog nào, và độ trễ này đang đo cái gì? Cần sửa cách đo/diễn giải trước khi dùng số độ trễ.

## Quy tắc bắt buộc
- Đọc `AGENTS.md` (zero-fork tối thượng, không dùng timeout/sleep để quyết định dispatch, thà pending chứ không fork) và `PROJECT_STRUCTURE.md`.
- Ghi giả thuyết + tiêu chí PASS/FAIL vào `PREREGISTERED.md` và commit **trước** khi chạy. Không đổi tiêu chí sau khi thấy kết quả.
- Mọi số liệu từ lệnh chạy thật, **tự tính thống kê từ file thô**, lưu vào `note/evidence/tps_root_cause_20261008/` kèm `MANIFEST.json` (sha256). Mỗi cấu hình **≥ 8 lượt**, cụm sạch mỗi lượt, báo trung vị + SD + khoảng tin cậy 95%; A/B phải **xen kẽ ngẫu nhiên có seed**.
- Build binary từ commit được đo, ghi hash; không dùng lại binary sẵn có trong `/tmp/p06_bins`.
- Mọi thay đổi code phải giữ nguyên thứ tự/hash/state root hiện có (không đổi kết quả xác định). Thay đổi cấu hình thông lượng không được làm mất bảo đảm commit (Raft: chỉ ack sau quorum; BFT: dispatch chỉ khi đủ 2f+1).
- `git add` theo tên file; không push/merge main khi chưa được user duyệt; không squash. Dừng tiến trình theo PID.
- `consensus/metanode/scripts/build_check.sh` sạch lỗi/warning sau mỗi thay đổi code.

## Giai đoạn 1 — Chuẩn hóa phép đo (làm trước)
1. Dùng lại `bft_remeasure.py` / `raft_remeasure.py` trong `note/evidence/tps_remeasure_20261008/` làm nền. Mở rộng để chạy **cùng một bộ giao thức cho cả hai chế độ**: burst 10k/20k/25k/30k và sustained 60 s, mỗi cấu hình ≥ 8 lượt.
2. Tách rõ trong báo cáo: (a) thời gian bơm, (b) thời gian chờ commit, (c) cách tính `effective_tps` và độ trễ trong `secp_tps_blast` (`main.go`). Sửa/đính chính nếu độ trễ sustained thực chất là backlog (Q5). Không đổi công thức mà không nêu rõ.
3. Ghi thông số máy và load trước mỗi lượt. Chờ máy nguội giữa các lượt (cố định, ghi lại).

## Giai đoạn 2 — Điều tra Raft (Q1, Q2)
1. Thêm quan sát (chỉ log/metric, không đổi hành vi) trong `execution/pkg/rollup/raftfeed/` (`node.go`, `fsm.go`, `raftfeed.go`): độ sâu hàng đợi `proposeQ`, thời gian chờ trong hàng đợi, kích thước batch/entry (`splitBatch`, `MaxBatchBytes`), thời gian từ Submit đến commit quorum, thời gian apply FSM → tạo block → commit state, số tx mỗi block.
2. Trong sustained: lập timeline từng block (nhận, replicate, quorum ack, apply, commit trie). Xác định giai đoạn chiếm nhiều nhất của chu kỳ và liệu leader hay follower là điểm chậm (đo cả hai).
3. Quét **một biến mỗi lần** (≥ 8 lượt/cấu hình): `propose_queue_size` (1.024/4.096/16.384), `max_batch_bytes`, `commit_timeout_ms`, `heartbeat`/`election`/`leader_lease`, cỡ batch của client. Trả lời Q2: có ngưỡng nào giải thích 25k < 20k không.
4. Dùng CPU profile + mutex/block profile của leader trong sustained (nếu `go tool pprof` crash `mallocgc called without a P`, thử bản Go khác và ghi rõ; không suy luận từ string table).
5. **Cổng quyết định (ghi trước):** chỉ đề xuất sửa khi một giai đoạn chiếm ≥ 10% chu kỳ block bằng số đo và cận trên Amdahl cho ≥ +5% sustained.

## Giai đoạn 3 — Điều tra BFT (Q3, Q4)
1. Dùng lại bộ timeline `[TIMELINE-RUST]`/`[TIMELINE-CGO]` (đã có từ Phase B) cho burst 25k: so sánh lượt nhanh (10.612) với lượt chậm (6.542) xem khác nhau ở khâu nào (đợi commit DAG, gom block, `PrepareTransactions`, thực thi, commit trie, cold cache).
2. Đo cold-start: chạy burst ngay sau khởi động so với sau một lượt warm-up cố định (có ghi rõ). Nếu warm-up làm TPS burst gần sustained, nguồn nhiễu là chi phí khởi động; đề xuất cách giảm (làm ấm cache) mà không đổi kết quả xác định.
3. Kiểm tra cỡ block trong DAG: 3 block × 8.333 tx (burst) có phải do lịch commit không; ảnh hưởng của cỡ block tối đa lên TPS và độ trễ.
4. Trả lời Q4 bằng số đo: phần nào của burst là chi phí cố định.

## Giai đoạn 4 — Cải thiện (chỉ khi qua cổng quyết định)
- Mỗi cải tiến một commit riêng, kèm test (race, property test nếu đổi thuật toán gom/chia batch), benchmark trước/sau theo mục "Quy tắc bắt buộc", zero-fork parity 100% và chaos `kill -9` (ít nhất 5 chu kỳ, so block hash + state root giữa node và giữa replay với live).
- Ứng viên (chỉ làm nếu số đo ủng hộ): tăng/điều chỉnh cỡ block hoặc batch Raft, tách đường replicate khỏi đường apply, gộp ghi, giảm cold-start. **Không** làm thay đổi cần cutover giao thức (đổi định dạng block/FFI) khi chưa có thiết kế được user duyệt.
- Thay đổi chỉ chấp nhận nếu trung vị cải thiện ≥ +5% và khoảng tin cậy 95% của hiệu không chứa 0; ngược lại ghi "không xác nhận" và đề xuất giữ/revert.

## Giai đoạn 5 — Báo cáo
`note/tps_root_cause_report_20261008.md`:
- Trả lời Q1–Q5 bằng số liệu, nêu rõ cái nào đã xác nhận, cái nào vẫn là giả thuyết.
- Bảng đo trước/sau cho mọi cải tiến (trung vị, SD, CI), kể cả cái không đạt ngưỡng.
- Đính chính ba báo cáo Gemini cũ bằng ghi chú ở đầu mỗi file, trỏ đến `tps_remeasure_bft_vs_raft_report_20261008.md`.
- Hạn chế: các node chung một máy; hai chế độ khác mô hình tin cậy (BFT vs CFT).

## Ngoài phạm vi
RSS/heap (xem `note/design_bounded_memory_indexes_20261007.md`), Giai đoạn D (gỡ gate `waitCommitted`; gate đo được chờ ~533 ns), đổi giao thức FFI Rust↔Go (cần thiết kế riêng), so sánh đa máy.

## Tiêu chí hoàn thành
- [ ] `PREREGISTERED.md` commit trước khi chạy
- [ ] Phép đo chuẩn hóa ≥ 8 lượt/cấu hình cho cả BFT và Raft; diễn giải độ trễ sustained được làm rõ
- [ ] Q1–Q5 có câu trả lời bằng số, có timeline và profile
- [ ] Mọi cải tiến (nếu có) qua cổng ≥ +5% có ý nghĩa thống kê, parity 100%, chaos pass, `build_check.sh` sạch
- [ ] Báo cáo cuối và ghi chú đính chính các báo cáo cũ
