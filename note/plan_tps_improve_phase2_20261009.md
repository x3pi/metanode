# Kế hoạch giao agent: kiểm chứng nguyên nhân và cải thiện TPS BFT/Raft — đợt 2 (2026-10-09)

**Nền:** `note/tps_root_cause_report_20261008.md` + `note/evidence/tps_root_cause_20261008/` (Gemini, n=8, cụm sạch mỗi lượt), `note/tps_remeasure_bft_vs_raft_report_20261008.md` (đo lại độc lập, n=3–5), mã `execution/pkg/rollup/raftfeed/`.
**Trạng thái git lúc lập kế hoạch:** `dev` local ahead 3 so với `origin/dev` (`204c3cd5`, `23acd40b`, `1c1b0c53`), chưa push, và các commit này cũng chứa file kế hoạch đợt trước. Chưa ai review `execution/cmd/tool/secp_tps_blast/main.go` đã sửa.

## 0. Review kết quả của đợt điều tra vừa rồi

**Số liệu (tin được):** tính lại từ `raw/*.json`, n=8 mỗi cấu hình, zero-fork PASS hết. Trung vị tx/s: BFT burst10k 4.012 / 20k 6.731 / 25k 7.830 (SD 189) / 30k 8.665 / sustained 14.313 (SD 691); Raft burst10k 11.502 / 20k 17.373 (SD 4.852, khoảng 12.817–23.878) / 25k 14.125 / 30k 13.219 / sustained 10.155 (SD 305). Khớp với bản đo lại độc lập của mình (BFT sustained 14.826, Raft sustained 9.998, Raft 25k 13.650), nên các mức này tin được. Riêng BFT burst 25k: bản đo độc lập có 10.612 và 6.542 mà bản n=8 không thấy; chưa giải thích được (xem Q3).

**Lời giải thích (chưa vững):** báo cáo ghi "Đã xác nhận bằng số liệu" nhưng chỉ `secp_tps_blast/main.go` được sửa. Không có thêm quan sát, profile hay timeline nào trong `evidence/tps_root_cause_20261008/` (chỉ có `raw/` và `phase1_standardized_summary.json`). Nên các giải thích dưới đây mới là giả thuyết.

| Câu | Khẳng định của báo cáo | Đánh giá |
|---|---|---|
| Q1 Raft sustained thấp | "Chi phí cố định mỗi block ~35% CPU (fsync BoltDB, FSM lock, NOMT commit) là trần cứng ~10,1k" | **Giả thuyết hợp lý** (Raft 341 block vs BFT ~150–175 block, ~1.800 vs ~5.000 tx/block) nhưng **con số 35% và "trần cứng" không có đo**. Dự đoán +35–48% theo Amdahl là phỏng đoán. |
| Q2 Raft 25k < 20k | "propose_queue (4.096) và buffer ingress chạm ngưỡng ở 25–30 batch, gây backpressure" | **Rất khó đúng.** `proposeQ` có dung lượng 4.096 *proposal*, mà 25–30 batch chỉ là 25–30 proposal; `statusFull` cần `cap−len < số mảnh`. Batch 1.000 tx nhỏ hơn nhiều `max_batch_bytes` (4 MiB) nên không bị tách. Raft 20k lại có SD 4.852 (12.817–23.878), khoảng chồng lấn với cả 25k, nên chưa chắc 20k nhanh hơn 25k theo nghĩa ổn định. Không có bộ đếm `statusFull`/split nào được thu. |
| Q3 BFT burst dao động | "báo cáo cũ không wipe cụm giữa các lượt" | **Sai với bản đo lại của mình:** mỗi lượt đều dựng cụm mới. Đúng là n=8 gọn hơn (SD 189), nhưng nguyên nhân khác biệt (harness khác, thứ tự giao thức xen kẽ, thời gian nghỉ, tracker poll, binary) chưa được xác định. Lời giải "2 vs 3 round DAG" chưa có timeline hỗ trợ. |
| Q4 BFT sustained ổn định | "chi phí khởi động DAG ~2,0–2,5 s" | **Giả thuyết hợp lý** (phù hợp với 4.012 tx/s ở 10k) nhưng phép kiểm đã đăng ký trước (10 s đầu so với 50 s sau trong sustained) không có trong evidence. |
| Q5 độ trễ sustained | "sau khi vá: Raft P50 1,88 s, BFT P50 29,1 s" | **Hướng sửa đúng, nhưng số vẫn khó diễn giải:** (a) deadline theo dõi mỗi mẫu là 30 s và mẫu không xác nhận kịp bị đếm là "reverted" và **không vào danh sách độ trễ**, nên phân bố bị cắt ở 30 s (P50 BFT 29,1 s sát giới hạn); (b) tải không giới hạn tốc độ nên độ trễ là backlog phụ thuộc tốc độ bơm, không phải độ trễ nội tại. Raft chậm lại phía client (Submit đồng bộ chờ quorum) nên backlog nằm ở client, BFT nhận hết vào mempool nên backlog nằm ở chain. Hai con số không so sánh trực tiếp được. |

## Quy tắc bắt buộc
- Đọc `AGENTS.md` (zero-fork tối thượng; không dùng timeout/sleep để quyết định dispatch commit; thà pending chứ không fork) và `PROJECT_STRUCTURE.md`.
- Ghi giả thuyết + tiêu chí PASS/FAIL vào `PREREGISTERED.md` và commit **trước** khi chạy. Không đổi tiêu chí sau khi thấy kết quả. Không dùng cụm từ "đã xác nhận" cho điều chưa đo.
- Tự tính thống kê từ file thô; ≥ 8 lượt mỗi cấu hình, cụm sạch mỗi lượt; A/B xen kẽ ngẫu nhiên có seed; ghi hash binary và commit được đo. Lưu vào `note/evidence/tps_improve_phase2_20261009/` kèm `MANIFEST.json` (sha256).
- Thay đổi code không được đổi kết quả xác định (thứ tự tx, hash, state root) so với hiện tại. Raft: chỉ ack sau quorum. BFT: dispatch chỉ khi đủ 2f+1. Mọi thay đổi cần cutover giao thức/FFI phải có thiết kế được user duyệt trước.
- `git add` theo tên file; không push/merge main khi chưa được user duyệt; không squash. Dừng tiến trình theo PID.
- `consensus/metanode/scripts/build_check.sh` sạch lỗi/warning sau mỗi thay đổi code.

## Giai đoạn 0 — Dọn nền
1. Review `secp_tps_blast/main.go` (commit `23acd40b`): sửa thống kê độ trễ để (a) báo riêng số mẫu đã xác nhận / chưa xác nhận kịp deadline và **không gộp vào "reverted"**, (b) ghi rõ tỷ lệ mẫu bị cắt ở deadline, (c) tăng deadline hoặc đo đến hết thời gian drain. Kiểm tra tracker (16 worker, poll 100 ms) không làm nhiễu thông lượng (đo A/B có/không tracker).
2. Sửa `tps_root_cause_report_20261008.md`: đổi "Đã xác nhận bằng số liệu" thành "Giả thuyết (chưa đo)" cho Q1–Q4 nếu chưa có evidence, ghi rõ hạn chế độ trễ (Q5). Không bỏ số liệu thô.
3. Giải thích chênh lệch BFT burst 25k giữa hai bộ đo (n=8 gọn vs bản đo lại có 10.612 và 6.542): chạy bộ giao thức của cả hai harness xen kẽ trên cùng binary, kiểm tra từng khác biệt (thứ tự giao thức, nghỉ 4 s, tracker, binary).

## Giai đoạn 1 — Đo trực tiếp nguyên nhân (chỉ quan sát, không đổi hành vi)
**Raft (Q1, Q2):**
- Thêm bộ đếm/histogram vào `raftfeed/node.go`, `fsm.go`: độ sâu `proposeQ`, số lần `statusFull`, số lần `splitBatch` tách, thời gian chờ trong `proposeQ`, thời gian `raft.Apply` đến quorum ack, thời gian `FSM.Apply` (`f.st.build`), thời gian ghi block/commit trie, số tx mỗi block, và thời gian fsync log (BoltDB) nếu lấy được từ metric của HashiCorp Raft.
- Chạy sustained 60 s ≥ 8 lượt: dựng bảng ms/block theo từng khâu, xác định khâu lớn nhất và tỷ lệ chu kỳ thực. Profile CPU + mutex/block của leader và follower (nếu `go tool pprof` crash `mallocgc called without a P`, thử bản Go khác và ghi rõ; không suy luận từ string table).
- Q2: quét burst 20k/25k/30k với bộ đếm trên, ≥ 12 lượt cho 20k để hiểu phân bố (có hai chế độ nhanh/chậm không), liên hệ với số block và số lần `statusFull`/tách.
**BFT (Q3, Q4):**
- Dùng `[TIMELINE-RUST]`/`[TIMELINE-CGO]`: so lượt nhanh/chậm của burst 25k; đo 10 s đầu so với 50 s sau của sustained (kiểm tra đã đăng ký cho Q4); đo chi phí cold-start với một lượt warm-up cố định.
**Cổng quyết định (ghi trước):** chỉ sang Giai đoạn 2 cho khâu chiếm ≥ 10% chu kỳ block bằng số đo và cận trên Amdahl ≥ +5% TPS.

## Giai đoạn 2 — Ứng viên cải thiện (chỉ khi qua cổng; mỗi cái một commit riêng)
**Raft (nếu Giai đoạn 1 xác nhận chi phí cố định theo block là nút thắt):**
- **R1. Gộp proposal theo độ sâu hàng đợi (không dùng bộ đếm thời gian):** `proposeLoop` khi lấy một proposal thì lấy luôn các proposal đang chờ (tối đa `max_batch_bytes`/số tx) thành một entry Raft, để giảm số block và số lần fsync/FSM/commit trie. Vì mỗi entry hiện tạo đúng một block, kiểm tra kỹ rằng follower tái tạo block giống hệt nhau từ log (xác định), restart/replay (`blk.BlockNumber <= f.durable()`) vẫn đúng, và `tx_count` khớp. Kiểm tra ảnh hưởng độ trễ burst (không được tăng đáng kể).
- **R2. Quét cấu hình:** `commit_timeout_ms`, `heartbeat_timeout_ms`, `max_batch_bytes`, `propose_queue_size`. Đây chỉ là cấu hình; chấp nhận nếu qua cổng thống kê.
- **R3. Giảm chi phí mỗi block** (fsync gộp, bớt khóa trong `FSM.Apply`) nếu profile chỉ ra.
**BFT (ưu tiên nếu BFT là đường chính của sản phẩm — cần user xác nhận):**
- **B-a.** Giảm chi phí khởi động: làm ấm cache/trie/pool trước khi nhận tải thật, không đổi kết quả xác định.
- **B-b.** Chỉ sau khi có timeline: điều chỉnh cỡ block/nhịp gom commit nếu số đo chứng minh; mọi thay đổi gần consensus cần thiết kế riêng, được user duyệt và chaos test.
**Cấm trong đợt này:** gỡ gate `waitCommitted` (đo ~533 ns), đổi FFI/protobuf, bỏ sort `PrepareTransactions`, nâng `MaxBatchTxCount` (giới hạn 1.000 là bảo vệ giao thức).

## Giai đoạn 3 — Kiểm chứng cải tiến
- A/B xen kẽ (BEFORE/AFTER), ≥ 8 lượt mỗi bên, sustained 60 s và burst 25k, trung vị + CI 95% + Welch t. Chấp nhận nếu trung vị ≥ +5% và CI 95% của hiệu không chứa 0; ngược lại ghi "không xác nhận" và đề xuất giữ/revert.
- Zero-fork parity 100% mọi lượt; `go test -race` cho phần đổi; chaos `kill -9` ≥ 5 chu kỳ trong lúc blast (Raft: kill leader và follower; so block hash + state root giữa node và giữa replay với live; không mất/trùng tx).
- Đo độ trễ **ở tốc độ bơm cố định** (ví dụ 50% và 75% của đỉnh sustained mỗi chế độ), báo P50/P90/P99 kèm tỷ lệ mẫu bị cắt. Chỉ dùng số này để so sánh độ trễ BFT/Raft.

## Giai đoạn 4 — Báo cáo
`note/tps_improve_phase2_report_20261009.md`: trả lời Q1–Q5 với trạng thái từng câu (đã xác nhận bằng số đo / vẫn là giả thuyết), bảng đo trước/sau cho mọi cải tiến (kể cả cái không đạt), hạn chế (node chung máy; hai chế độ khác mô hình tin cậy BFT/CFT), danh sách việc để sau.

## Ngoài phạm vi
RSS/heap (xem `note/design_bounded_memory_indexes_20261007.md`), thiết kế lại FFI Rust↔Go, so sánh đa máy, thay đổi cần cutover giao thức.

## Tiêu chí hoàn thành
- [ ] Công cụ đo độ trễ sửa xong, không gộp mẫu chưa xác nhận vào "reverted"; báo cáo cũ được đính chính
- [ ] Chênh lệch BFT burst 25k giữa hai harness được giải thích bằng thực nghiệm
- [ ] Q1–Q4 có số đo trực tiếp (bộ đếm Raft, timeline BFT, profile); `PREREGISTERED.md` commit trước khi chạy
- [ ] Mọi cải tiến qua cổng ≥ +5% có ý nghĩa thống kê, parity 100%, chaos pass, `build_check.sh` sạch
- [ ] Báo cáo cuối phân biệt rõ "đã đo" và "giả thuyết"
