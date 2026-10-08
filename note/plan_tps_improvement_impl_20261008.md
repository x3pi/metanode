# Kế hoạch triển khai cải thiện TPS (giao agent khác) — 2026-10-08

**Nền:** `note/tps_investigation_report_20261008.md` + `note/evidence/tps_investigation_20261008/` (commit `7785a47b`, chưa push lúc lập kế hoạch). Baseline 5 lượt: 6.098,6 tx/s (min 5.888, max 6.341, SD 168).

## 0. Review báo cáo: điều gì tin được, điều gì chưa

**Tin được (có đo, lặp lại):** baseline ~6,1k tx/s, zero-fork PASS 5/5; ingest ~219k tx/s không phải nút thắt; không thấy contention `parking_lot` ở Rust; CPU toàn cụm chỉ ~836% trên 104 vCPU nên co-location không phải nguyên nhân (pinning 16 core/node không cải thiện).

**Chưa đủ cơ sở (phải giải quyết trước khi viết code):**
1. **"Nút thắt #1 = `wait_predecessor` 44,7%" bị diễn giải quá mức.** Chỉ có **23 block** cho 5 lượt (4–5 block/lượt). Trong đợt burst, Rust đẩy các block sang Go gần như cùng lúc, nên thời gian chờ của block N chủ yếu là **hàng đợi** = tổng thời gian xử lý các block trước (dữ liệu: GEI 3 chờ 1.022 ms ≈ thời gian cả GEI 2). Các giai đoạn trên đường găng nối tiếp nhau, nên phần trăm trung bình của từng block cộng gộp **không** cho biết xóa được bao nhiêu TPS. Chờ predecessor chỉ là phần *không chồng lấn được* nếu N cần state sau-commit của N-1.
2. **Block 1–2 là cold-start** (SIG-ENFORCE 397 ms ở block #1, sau đó 13–29 ms). Trung bình 156 ms/block của `sig_enforce` bị kéo bởi block lạnh; con số tác động của Đề xuất 3 cũng vậy.
3. **"+40% đến +55% TPS (9,5–10,5k tx/s)" là ước tính, không phải số đo.** Kế hoạch trước cấm ước đoán; cần cận trên (ceiling) tính từ dữ liệu đo, không phải phỏng đoán.
4. **Gate hiện tại là biện pháp zero-fork có chủ đích.** `speculative_executor.go` ~dòng 316–332 ghi "ZERO-FORK SERIALIZATION GATE: S_N = f(S_{N-1}, Block_N) đòi hỏi S_{N-1} đã commit". Đề xuất 1 gỡ gate này; kèm các bài học cũ: deadlock speculative-executor ↔ NOMT session, drift đếm GEI replay/live, nil-pointer `CloneSpeculative`. Đây là thay đổi **rủi ro cao nhất** của repo, không được làm vì "đo thấy chờ nhiều".
5. Batch 500 (+4,3%) so với SD 2,8% của baseline: chưa chắc ngoài nhiễu nếu chỉ 1–3 lượt.

## Quy tắc bắt buộc
- Đọc `AGENTS.md` (zero-fork, không timeout để dispatch, thà pending chứ không fork), `PROJECT_STRUCTURE.md`. Số liệu phải từ lệnh chạy thật; lưu evidence + MANIFEST sha256; ghi tiêu chí PASS/FAIL vào `PREREGISTERED.md` và commit **trước** khi chạy.
- `git add` theo tên file; không push/merge main khi chưa được user duyệt; không squash. Dừng tiến trình theo PID (không `pkill -f` chuỗi khớp chính shell).
- Mỗi giai đoạn phải qua `consensus/metanode/scripts/build_check.sh` sạch lỗi/warning.

## Giai đoạn A — Đo lại cho đúng (bắt buộc, không sửa code hành vi)
1. Chạy workload **dài hơn** (≥60–120 s, hàng chục đến hàng trăm block, rate cố định thấp hơn trần như 4.000–5.000 tx/s **và** không giới hạn) để có trạng thái ổn định; loại block 1–2 (cold) khỏi thống kê. ≥5 lượt mỗi chế độ.
2. Với từng block, ghi **dòng thời gian tuyệt đối** (timestamp bắt đầu/kết thúc từng giai đoạn: nhận từ Rust, đợi predecessor, exec, root, commit_memory, persist NOMT/Pebble, phản hồi Rust). Vẽ Gantt/đường găng. Tính **thời gian chu kỳ giữa hai block liên tiếp** (steady-state throughput = txs/block ÷ chu kỳ).
3. Tách thời gian của predecessor thành: (a) phần cần *trước* khi block N có thể clone state (execution + root), (b) phần *persist* (NOMT + Pebble + GEI async) chạy sau. **Chỉ (b) mới có thể chồng lấn với execution của N** nếu bỏ gate. Báo cáo (b) tính bằng ms/block và % chu kỳ.
4. **Cận trên (Amdahl) từ số đo:** nếu (b) bị ẩn hoàn toàn thì chu kỳ giảm còn bao nhiêu → TPS tối đa. Đây là con số duy nhất được dùng để quyết định có làm Giai đoạn D hay không.
5. Lặp lại thí nghiệm batch 250/500/1000 với ≥10 lượt mỗi mức để biết có vượt nhiễu không.
- **Tiêu chí quyết định (ghi trước):** chỉ làm Giai đoạn D nếu cận trên (b) cho ≥ +15% TPS ở steady-state; nếu <15% thì dừng ở B/C.

## Giai đoạn B — Thắng lợi rủi ro thấp (độc lập với consensus)
Mỗi mục là một commit riêng, có benchmark trước/sau ≥5 lượt và test.
- **B1. Tính `txs_root` và `receipts_root` song song** (hiện `root_calc` ~74 ms/block; hai root độc lập nhau). Kết quả phải bit-for-bit giống bản tuần tự: thêm test so sánh trên tập tx ngẫu nhiên + kiểm tra zero-fork trên cụm 4 node. Không đổi thứ tự/định dạng băm. Giới hạn worker (bounded concurrency).
- **B2. Làm ấm cache chữ ký ở ingest TCP** (Đề xuất 3). Chỉ là tối ưu cache; verdict của `FilterInvalidSignatures` vẫn là hàm thuần của (tx, state, policy) và **mọi validator vẫn tự verify khi thực thi block**. Đo tác động *sau khi loại block cold*; nếu <2% thì bỏ.
- **B3. Cỡ batch**: nếu A5 cho thấy 500 tx/batch tốt hơn có ý nghĩa thống kê, chỉ đổi mặc định ở tool/client; **không** nâng `MaxBatchTxCount = 1000` (giới hạn giao thức, `eth_validation.go`).

## Giai đoạn C — Khảo sát chi tiết điều kiện chồng lấn (chỉ đọc code, không sửa)
Viết `note/design_pipelined_execution_20261008.md` (DRAFT, chờ user duyệt) trả lời:
1. Thực chất `waitCommitted(gei-1)` đợi sự kiện nào (commit memory, persist NOMT, Pebble, GEI async)? Có thể thay bằng điều kiện yếu hơn mà vẫn an toàn không?
2. Block N cần gì từ N-1: root, account/stake state, nonce, receipts, code? Mô tả lớp overlay trong bộ nhớ (delta của N-1) để N đọc.
3. Khi N-1 **bị discard/halt/Conflict** (`PeerAttestResult::Conflict`, chờ CertifiedCommit) thì N phải bị hủy và chạy lại; cách đảm bảo kết quả N không bao giờ được phản hồi/ghi nhận trước khi N-1 đã verified (dispatch vẫn tuần tự, dựa 2f+1 peer, không timeout).
4. Tương tác với: `ExecutionMutex`, P2P BlockSync, `CleanGEI`, replay/recovery (`recovery.rs` vs `executor.rs` — nguồn drift GEI cũ), NOMT session deadlock cũ, `CloneSpeculative`.
5. Chiến lược test: fault injection (kill -9 giữa chừng, N-1 fail), chaos 2/2 split, so sánh state root sau replay.
6. Kế hoạch rollout/rollback (cờ cấu hình mặc định TẮT; bật cho devnet trước).

## Giai đoạn D — Triển khai pipelined speculative execution (CHỈ nếu A4 vượt ngưỡng VÀ user duyệt thiết kế ở C)
- Đây là thay đổi consensus-adjacent: **cần user duyệt rõ ràng bằng văn bản** trước khi sửa code. Làm trên nhánh riêng, không merge thẳng.
- Giữ nguyên: dispatch/commit ra ngoài tuần tự tuyệt đối theo 2f+1; kết quả speculative của N chỉ "mở" sau khi N-1 verified; discard N khi N-1 discard.
- Test bắt buộc: unit + test đua `-race`; kill -9 / chaos nhiều chu kỳ; so sánh block hash + state root toàn cụm sau mỗi kịch bản; recovery replay khớp live.
- Chỉ bật mặc định khi: ≥+15% TPS ở steady-state đã đo lặp lại, **0 fork** trong toàn bộ chaos, và user duyệt kết quả.

## Ngoài phạm vi
RSS/heap tăng (xem `note/design_bounded_memory_indexes_20261007.md`); tối ưu thêm `FilterInvalidSignatures` (đã ~1,5 µs/tx khi cache ấm); các thay đổi giao thức nâng `MaxBatchTxCount`.

## Tiêu chí hoàn thành
- [x] Giai đoạn A: Gantt đường găng steady-state + cận trên TPS bằng số đo, kèm PREREGISTERED (xem `note/phase_a_steady_state_report_20261008.md`)
- [x] Giai đoạn B (Thắng lợi lớn rủi ro thấp):
  - [x] B1: Tính Merkle Roots song song (txsRoot & receiptsRoot) hoàn tất (commit `919da0e0`). Đo 5 lượt 60s sustained blast: Mean TPS 10.898 -> 13.580 tx/s (+24,61%), Welch t=12,511 (p < 0,0001), 100% Zero-Fork PASS.
  - [x] B2: Đánh giá Mempool Signature Pre-warming hoàn tất (commit `da23217e`). Xác nhận cache hiện tại đã đạt 100% bound_hit (1,2 ms / 1.000 txs), quyết định giữ nguyên hiện trạng, không over-engineer.
  - [x] B3: Đánh giá cỡ batch hoàn tất từ Giai đoạn A (p = 0,104 > 0,05), giữ nguyên giới hạn giao thức MaxBatchTxCount = 1000.
- [ ] Giai đoạn C: `design_pipelined_execution_20261008.md` hoàn thành, **chờ duyệt**
- [ ] Giai đoạn D chỉ khởi động khi đủ điều kiện ở trên
- [x] `build_check.sh` sạch sau mỗi thay đổi code (PASS 5/5)
