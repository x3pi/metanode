# Kế hoạch điều tra cải thiện TPS (giao agent khác) — 2026-10-08

**Mục tiêu:** tìm điều gì thực sự giới hạn TPS commit của cụm 4 validator (hiện ~6,2k tx/s, đỉnh 6,5k, P95 ~4,1 s), rồi mới đề xuất cải thiện. **Đo trước, sửa sau.** Không có số đo thì không kết luận.

## Số liệu nền đã có (đọc, đừng làm lại)
- `note/perf_secp_tps_20261007.md`: baseline `87d825dd` 5.846 tx/s → HEAD `bf07df7a` 6.232 tx/s (3 lượt/bản, 25k tx EIP-1559 secp, chữ ký bật). Ingest ~219k tx/s nên không phải nút thắt. `[SIG-ENFORCE]` còn ~1,2–1,5 µs/tx.
- `note/perf_nomt_commit_20261007.md`: commit NOMT 1.000 key ~11 ms (pipeline) / ~18 ms (sync). Quá nhỏ để giải thích ~4 s/lượt.
- `note/plan_perf_followups.md`, memory sig-filter: giả thuyết Rust giao commit cho Go **từng cái một** (`send_committed_subdag` chờ Go trả lời) → Go không verify/chuẩn bị trước block kế tiếp. **Giả thuyết, chưa đo.**
- Lưu ý: 4 node chạy chung một máy 104 vCPU nên có tranh chấp CPU/đĩa; số liệu không đại diện triển khai nhiều máy.

## Quy tắc bắt buộc
- Đọc `AGENTS.md` và `PROJECT_STRUCTURE.md`. Zero-fork là tối thượng: không dùng timeout/sleep/Duration để quyết định dispatch; thà pending chứ không fork.
- Mọi con số phải từ lệnh đã chạy thật, lưu log vào `note/evidence/tps_investigation_20261008/` kèm `MANIFEST.json` (sha256). Không bịa, không làm tròn giả. Agent trước từng báo PASS/benchmark sai, hãy tự chạy lại.
- Ghi **tiêu chí và giả thuyết vào `PREREGISTERED.md` và commit TRƯỚC khi chạy**. Không đổi tiêu chí sau khi thấy kết quả.
- `git add` theo tên file, không `git add <dir>`/`-A`. Không push/merge main khi chưa được duyệt; không squash.
- Không dùng `pkill -f` với chuỗi có thể khớp chính shell của bạn; dừng tiến trình theo PID.
- Đo lường không được sửa code consensus/execution. Nếu cần thêm log/metric thì chỉ thêm quan sát (không đổi hành vi) và nêu rõ trong báo cáo.

## Bước 0 — Chuẩn bị môi trường sạch
1. Xác nhận không còn tiến trình chain cũ (`ps -eo pid,user,args | grep -E 'simple_chain|parent_chain'`). Các service systemd `metanode-execution-{0..4}` và `parent_chain_{0..3}` (root) cần `sudo` để dừng — nhờ user chạy.
2. **Wipe dữ liệu của cụm benchmark cô lập** (không đụng `/opt/metanode/*` đang dùng cho môi trường khác, trừ khi user cho phép rõ ràng). Dùng cụm cô lập cổng 31xxx như báo cáo 2026-10-07, dựng từ template sạch `/tmp/gate_4val_clean_template`.
3. Build binary từ commit HEAD hiện tại của `dev`, ghi commit hash vào evidence. Chạy `consensus/metanode/scripts/build_check.sh` trước, phải sạch.
4. Ghi thông số máy (CPU, RAM, `nproc`, load trước khi chạy).

## Bước 1 — Baseline lặp lại được
- `secp_tps_blast` không giới hạn rate, 25k tx, batch 1000, `-verify-parity`, **≥5 lượt** (cụm wipe trước mỗi lượt hoặc ghi rõ nếu không). Báo trung bình, min, max, độ lệch chuẩn.
- Mỗi lượt thu: tx/s, thời gian commit, P50/P90/P95/P99 receipt, số block và tx/block, CPU/RSS đỉnh.
- Xác nhận zero-fork (block hash + state root giống nhau trên 4 node).

## Bước 2 — Phân rã thời gian theo từng block (trọng tâm)
Với mỗi block trong một lượt blast, tách thời gian:
1. Từ lúc tx vào mempool → được đưa vào block đề xuất (DAG).
2. Đồng thuận Rust: propose → commit.
3. Rust → Go: thời gian `send_committed_subdag` chờ phản hồi; **khoảng chờ giữa hai commit liên tiếp** (Go idle hay Rust idle?).
4. Go: `[SIG-ENFORCE]`, thực thi Block-STM, tính state root, commit NOMT, ghi block DB.
5. Receipt/ghi chỉ mục.
Dùng log hiện có (`[SIG-ENFORCE]`, timing của executor/commit) và thêm quan sát nếu thiếu. Kết quả: bảng ms/block theo từng giai đoạn và **giai đoạn nào chiếm % lớn nhất trên đường găng**.

## Bước 3 — Profile
- CPU profile và goroutine/mutex/block profile của 1 validator Go trong lúc blast (`--pprof-addr`/`ENABLE_DEBUG_PPROF=true`), 30–60 s ở trạng thái ổn định. Lưu file `.pprof` và bảng top. (Lưu ý: `go tool pprof` bản trên máy này từng crash `mallocgc called without a P`; thử bản Go khác hoặc `go run github.com/google/pprof@<ver>`. Nếu vẫn lỗi, ghi rõ, đừng suy luận từ string table.)
- Rust: `perf record`/flamegraph cho tiến trình consensus. Kiểm tra khóa `parking_lot` (xem memory về `GLOBAL_TX_CACHE`/`dag_state`) có contention không.
- Kiểm tra trạng thái chờ: thread Go có đứng chờ Rust, Rust có đứng chờ Go không.

## Bước 4 — Tách biến số
Mỗi thí nghiệm đổi **một** yếu tố, lặp ≥3 lượt, so với baseline:
- Cỡ block / số tx tối đa mỗi commit (nhỏ hơn, lớn hơn).
- Số validator trên cùng máy (CPU pinning/`taskset` hoặc giảm số node) để tách tranh chấp tài nguyên khỏi giới hạn thuật toán.
- Ảnh hưởng fsync/đĩa (đã có `perf_nomt_fsync_evidence_20261007.md`, chỉ lặp lại nếu cần).
- Nếu có máy thứ hai: 2 node/máy để ước lượng ảnh hưởng của việc co-locate.

## Bước 5 — Báo cáo & đề xuất
Viết `note/tps_investigation_report_20261008.md`:
- Bảng phân rã thời gian, nút thắt chính (kèm bằng chứng), nút thắt phụ.
- Giả thuyết nào đúng/sai so với `PREREGISTERED.md`.
- Đề xuất cải thiện **xếp hạng theo tác động ước tính có cơ sở đo** (không ước đoán), kèm rủi ro fork. Ví dụ nếu bằng chứng ủng hộ giao nhận nhiều commit cùng lúc (pipelined delivery), phải mô tả cách giữ zero-fork: commit chỉ dispatch khi có bằng chứng 2f+1, không timeout; thà pending.
- **Không tự cài đặt thay đổi consensus/delivery trong đợt này.** Chỉ đề xuất và chờ user duyệt.

## Ngoài phạm vi
- Rò rỉ/tăng RSS (xem `note/design_bounded_memory_indexes_20261007.md`, thí nghiệm 45 phút còn FAIL, nguồn nghi: `dirtyStorageMap`, `shardedSignatureCache`, logger).
- Tối ưu `FilterInvalidSignatures` thêm (đã ~1,2 µs/tx).

## Tiêu chí hoàn thành
- [x] `PREREGISTERED.md` commit trước khi chạy (commit `30c8268e`)
- [x] Baseline ≥5 lượt, zero-fork xác nhận (Mean 6.098,6 tx/s, 100% Zero-Fork PASS)
- [x] Bảng phân rã ms/block theo giai đoạn, xác định nút thắt có bằng chứng (`wait_predecessor` 44,7%, Block-STM 19,8%)
- [x] Profile CPU/lock (hoặc ghi rõ lý do không lấy được) (Go pprof + Linux native perf 18k samples)
- [x] Báo cáo kèm đề xuất xếp hạng và đánh giá rủi ro zero-fork, chưa sửa consensus (xem `note/tps_investigation_report_20261008.md`)
- [x] `build_check.sh` sạch nếu có thêm code quan sát (không sửa code core, giữ nguyên hiện trạng)
