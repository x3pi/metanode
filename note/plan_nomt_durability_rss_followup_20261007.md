# Kế hoạch tiếp: kiểm chứng fsync NOMT thật, B1 đúng nghĩa, độ bền mạnh hơn, RSS — 2026-10-07

Dành cho agent thực hiện. Nền: `origin/dev` đã gồm `ba2f35df`, `057b24de`, `6e66b1e0`.
Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_production_launch_20261006.md` (mục 0 và Giai đoạn 1),
`consensus/vendor/NOMT_PATCHES.md`, `note/perf_nomt_commit_20261007.md`, `note/perf_rss_investigation_20261007.md`,
`note/review_20261007_agent_commits.md`, và memory sự cố mất điện 2026-09-24 (`note/` về "power-loss NOMT-ahead 231/230", `storage.SyncDurable` trong `CommitBlockState`).

## 0. Quy tắc bắt buộc (không có ngoại lệ)
1. **Zero-fork (AGENTS.md 2.5):** không dùng timeout/sleep để quyết định dispatch; thiếu bằng chứng => PENDING. Queue/worker mới phải có giới hạn bộ nhớ.
2. **Cô lập:** chỉ chạy cluster local cổng 31xxx trong scratchpad của phiên. KHÔNG đụng cluster 231/230, `/opt/metanode`, tiến trình của user (trên máy có ~9 tiến trình `/opt/metanode` đang chạy: mọi phép đo RSS/CPU phải theo PID của env test, KHÔNG dùng `ps -C simple_chain`). Dừng env bằng `run_env.sh <BASE> stop` (theo PID).
3. **Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token.** Commit local theo TÊN FILE (không `git add <dir>`; `*.py` gitignore => `git add -f`). Cuối commit: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
4. **Báo cáo trung thực:** chỉ ghi OUTPUT THẬT kèm commit hash và lệnh. Chưa chạy => "chưa đo". Không suy diễn quá bằng chứng: nếu một thí nghiệm không thể chứng minh điều đang tuyên bố thì NÓI RÕ giới hạn. Đã từng có báo cáo giả/checkbox PASS giả và một vendor bị tắt fsync không báo.
5. Sau mỗi thay đổi: `gofmt`, `go vet`, `go test -race` cho package đã sửa, `cd consensus/metanode/scripts && ./build_check.sh` sạch (0 lỗi, 0 warning). Bug sửa phải có test đỏ trước / xanh sau.
6. **TUYỆT ĐỐI KHÔNG tắt fsync NOMT**, không xoá `fsync/sync_all/sync_data` trong `consensus/vendor/nomt/**`. Bất biến: NOMT không bao giờ đi trước block DB.
7. **DỪNG và hỏi user** nếu: cần đổi đường commit/consensus; kết quả buộc đánh đổi độ bền vs hiệu năng; cần máy/VM ngoài; phát hiện lỗ hổng nghiêm trọng. Phương án "C" (bỏ `commitWg.Wait()`) chỉ là thí nghiệm đo chi phí, không được merge/bật mặc định.
8. Code comment tiếng Anh; cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5).

---

## GIAI ĐOẠN 1 — Sửa báo cáo NOMT cho đúng sự thật (làm trước, nhanh)
Vấn đề đã xác nhận trong `note/perf_nomt_commit_20261007.md`:
- Báo cáo nói `nomt_ffi.Open(..., false)` chứng tỏ fsync bật. SAI: tham số cuối là `preallocate` (`execution/pkg/nomt_ffi/bridge.go:94`), không liên quan fsync.
- B1 (3 cấu hình end-to-end × 3 lần) CHƯA làm nhưng báo cáo không nói rõ; chỉ có microbenchmark BlockPipeline vs Sync.
- `kill -9` không kiểm chứng được fsync: page cache của kernel còn nguyên khi tiến trình chết.
- Câu "đáp ứng >7.000 TPS" không có số liệu đỡ (microbench 100/1000 key, block thật hàng nghìn tới chục nghìn tx).
Việc làm: sửa `note/perf_nomt_commit_20261007.md` — bỏ/chỉnh các khẳng định trên, thêm mục "Giới hạn của các thí nghiệm này" và "Chưa làm". Sửa comment sai trong `execution/pkg/trie/nomt_commit_bench_test.go` (`// Background async flush (re-enabled fsync NOMT)` chỉ đúng nếu Giai đoạn 2 chứng minh). Commit riêng.

## GIAI ĐOẠN 2 — Chứng minh fsync NOMT thật sự được gọi
1. Trên một node/bench cô lập, chạy commit dưới `strace -f -e trace=fsync,fdatasync,sync_file_range,msync,io_uring_enter -o <file>` (hoặc `perf trace`/`bpftrace`). Đếm số lần gọi và tên file trong mỗi lần `Commit`/`CommitAsync` (đối chiếu `io/fsyncer.rs`, `seglog/mod.rs`, `seglog/segment_rw.rs`). Lưu ý NOMT có thể dùng io_uring: nếu fsync đi qua io_uring thì strace thường không thấy `fsync` — khi đó dùng `bpftrace`/`perf` trên tracepoint `io_uring:*` hoặc đọc kết quả `/proc/<pid>/io` (đặc biệt `syscw`, `write_bytes`) kết hợp `iostat -x` (cột flush/`f/s`) để có bằng chứng, và nói rõ độ tin cậy của phương pháp.
2. So sánh 2 biến thể trên CÙNG bench: HEAD (fsync còn) vs một bản chỉ dùng để THÍ NGHIỆM ở scratchpad trong đó fsyncer bị no-op (KHÔNG commit, KHÔNG đưa vào repo): số lần fsync và thời gian commit phải khác rõ rệt, chứng minh bench thực sự nhạy với fsync.
3. Ghi `note/perf_nomt_fsync_evidence_20261007.md`: lệnh, output thật, phương pháp, độ tin cậy và giới hạn.
**Xong khi:** có bằng chứng định lượng rằng đường production gọi fsync (hoặc phát hiện nó KHÔNG gọi => DỪNG, báo user ngay, đây là lỗi nghiêm trọng).

## GIAI ĐOẠN 3 — B1 đúng nghĩa: 3 cấu hình × 3 lần, end-to-end
1. Dựng 3 build trên CÙNG máy: (A) commit `ec6560ce` (trước vendor, fsync gốc), (B) HEAD hiện tại, (C) HEAD + thí nghiệm bỏ `commitWg.Wait()` ở `NomtStateTrie.Commit` (chỉ để đo chi phí, ghi rõ KHÔNG dùng).
2. Workload: `execution/cmd/tool/secp_tps_blast` (kiểm chữ ký BẬT, KHÔNG đặt `SKIP_MEMPOOL_SIG_VERIFY`), cluster 4 validator cô lập, cùng kích thước batch/count/ví cho mọi cấu hình (nêu rõ giá trị; phân biệt lượt "burst 25k tx" và lượt dài ≥5 phút, không rate-limit để thấy trần). Dùng `-pids` để chỉ đo đúng PID env test.
3. Mỗi cấu hình 3 lần (nên 5 để có độ lệch chuẩn). Ghi: tx/s, p50/p99 độ trễ tới receipt, thời gian `Commit` mỗi block (`[NOMT-COMMIT-PERF]`), CPU, RSS (theo PID env test). Tính trung bình ± độ lệch chuẩn; nêu rõ chênh lệch có vượt nhiễu hay không.
4. Quyết định theo Giai đoạn 1.2 của kế hoạch launch: nếu B ≥ 90% A thì chấp nhận; nếu thấp hơn thì profile (`perf`, pprof) và đề xuất tối ưu có kiểm soát (ví dụ chỉ chờ commit async TRƯỚC khi dùng session mới thay vì serialize toàn bộ) — chỉ đề xuất, không tự đổi đường commit. Nếu cần đánh đổi độ bền/hiệu năng => DỪNG, hỏi user.
5. Ghi `note/perf_nomt_b1_20261007.md` với bảng số thật. Giải thích vì sao TPS ở đây (≈7.5k) khác báo cáo trước (≈6.2k) trên cùng HEAD (khác batch/count/genesis?) — đối chiếu và nêu nguyên nhân hoặc "chưa rõ".
**Xong khi:** có bảng 3 cấu hình × ≥3 lần với số thật và quyết định ghi rõ.

## GIAI ĐOẠN 4 — Kiểm thử độ bền mạnh hơn (crash tiến trình + gần với mất điện)
Hiện `test_crash_recovery_nomt.sh` chỉ giết `val1..val3`, mỗi lần một node, ~1.000 tx/vòng (1–2 block/vòng).
1. **Mở rộng crash-process test** (script mới hoặc tham số): giết cả `val0`/leader; giết 2 node cùng lúc (không phá quorum 2f+1 khi n=4: tối đa f=1 node mất liên tục — nếu giết 2 thì chỉ để kiểm hồi phục, ghi rõ hệ thống sẽ tạm dừng và tự tiến tiếp khi đủ node, không fork); tải lớn hơn (≥10k tx/vòng); thời điểm kill ngẫu nhiên ± đồng bộ với lúc commit (kiểm bằng log `[NOMT-COMMIT-PERF]`/trace: ghi lại kill rơi vào giai đoạn nào). ≥20 vòng. Mỗi vòng ghi log thật: exit code, có file `/tmp/MTN_INTEGRITY_FAILED` hay không, state root từng block khớp 4 node.
2. **Mô phỏng mất điện thật (nếu có thể làm an toàn):** `kill -9` KHÔNG xoá page cache nên không thay thế được. Phương án khả thi, chọn cái an toàn nhất và nêu rõ giới hạn:
   - VM/QEMU (kill VM đột ngột, đĩa ảo không bật cache ghi) chạy 1 node; hoặc
   - `dm-flakey`/`dm-log-writes` trên loop device (cần quyền root — nếu không có quyền, DỪNG và báo user, đừng dùng `sudo` tuỳ tiện trên máy của user); hoặc
   - bộ test mất điện của NOMT upstream (`consensus/vendor/nomt/torture`) nếu build được (ít nhất smoke).
   Nếu không làm được phương án nào mà không ảnh hưởng máy user: ghi "chưa kiểm chứng power-loss" và đề xuất user cấp VM riêng (B2).
3. Ghi `note/perf_nomt_durability_20261007.md`: kết quả từng vòng, phạm vi đã/chưa bao phủ.
**Xong khi:** ≥20 vòng kill -9 phạm vi rộng hơn pass (0 lệch state root, 0 exit 78) và có mục "chưa bao phủ power-loss" rõ ràng nếu phần 2 không làm được.

## GIAI ĐOẠN 5 — RSS: đóng nốt (nhẹ)
1. Chứng minh không rò rỉ chậm: chạy ≥4 đợt tải liên tiếp (mỗi đợt ≥50k tx), sau mỗi đợt gọi `/debug/pprof/heap?gc=1` (cổng pprof 31446..31449) rồi ghi live heap (`HeapAlloc` sau GC), RSS theo PID. Live heap sau GC phải bão hoà (không tăng tuyến tính theo số tx tích luỹ). Làm cho cả 4 node (báo cáo cũ chỉ gọi GC trên val0) và cả tiến trình Rust consensus.
2. Giải thích dòng `smaps_rollup` của PID `2823541` trong báo cáo cũ: PID đó là tiến trình nào (env test hay của user)? Nếu là của user thì loại bỏ số liệu đó khỏi báo cáo.
3. Đưa vào báo cáo khuyến nghị cấu hình `GOMEMLIMIT`/`GOGC` theo RAM máy (đã có ở báo cáo cũ), kèm đo ảnh hưởng TPS thật (≥3 lần) nếu đề xuất giảm `GOGC`.
Ghi bổ sung vào `note/perf_rss_investigation_20261007.md`.

---

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev trong repo/lịch sử git; thu hồi token GitHub; máy/VM riêng cho B2 và test power-loss thật; soak 24h; cutover chain 991 thật; bật pipeline commit Rust→Go (cần duyệt `note/design_pipelined_commit_delivery.md`); push/merge/đóng issue; chạy bất kỳ lệnh `sudo`/đụng thiết bị khối trên máy của user nếu chưa được phép bằng văn bản.

## Thứ tự và báo cáo cuối
1 → 2 → 3 → 4 → 5. Mỗi giai đoạn ≥1 commit riêng. Báo cáo cuối: danh sách commit; mỗi giai đoạn lệnh + output thật + kết luận; việc nào chưa làm được và vì sao; rủi ro còn lại. Mọi chỗ chưa kiểm chứng phải ghi rõ "chưa kiểm chứng", không khẳng định.
