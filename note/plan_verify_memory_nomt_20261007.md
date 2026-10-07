# Kế hoạch: kiểm chứng độc lập, điều tra RSS, đo NOMT fsync (B1) — 2026-10-07

Dành cho agent thực hiện. Nền: `dev` tại `2e049c51` (đã gồm PR #158, #159 và các commit `4868dc61`, `bf07df7a`, `5e3b9e1d`, `f20d9b44`).
Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_production_launch_20261006.md` (mục "0. Quy tắc bắt buộc" và Giai đoạn 1),
`note/perf_secp_tps_20261007.md`, `note/benchmark_results_perf_followups.md`, `note/plan_perf_followups.md`.

## 0. Quy tắc bắt buộc (không có ngoại lệ)
1. **Zero-fork (AGENTS.md 2.5):** không dùng timeout/sleep để quyết định dispatch commit; thiếu bằng chứng => PENDING. Queue/worker mới phải có giới hạn bộ nhớ. Không I/O đồng bộ trong vòng lặp event/consensus.
2. **Cô lập:** chỉ chạy cluster local trên cổng 31xxx, trong scratchpad của phiên. KHÔNG đụng cluster 231/230, `/opt/metanode`, hay tiến trình của user. Dừng env bằng `run_env.sh <BASE> stop` (theo PID), không `pkill` theo mẫu, sau mỗi lần rebuild kiểm tra không còn tiến trình cũ.
3. **Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token.** Chỉ commit local, theo TÊN FILE (không `git add <dir>`; `*.py` bị gitignore => `git add -f`). Cuối commit: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
4. **Báo cáo trung thực:** chỉ ghi OUTPUT THẬT kèm commit hash và lệnh đã chạy. Chưa chạy => ghi "chưa đo". Không bịa số. Đã từng có báo cáo/checkbox PASS giả và một vendor bị tắt fsync không báo: luôn đọc diff thật, không tin tóm tắt của agent khác.
5. Sau mỗi thay đổi: `gofmt`, `go vet`, `go test -race` cho package đã sửa, và `cd consensus/metanode/scripts && ./build_check.sh` sạch (0 lỗi, 0 warning). Mỗi bug sửa phải có test đỏ trước/xanh sau (bỏ fix thì test đỏ lại).
6. Code comment tiếng Anh. Cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5). Cập nhật `PROJECT_STRUCTURE.md` nếu đổi cấu trúc.
7. **DỪNG và hỏi user** nếu: cần đổi đường commit/consensus; benchmark buộc phải đánh đổi độ bền vs hiệu năng; cần máy ngoài; phát hiện lỗ hổng bảo mật nghiêm trọng (báo ngay, không công bố chi tiết exploit).
8. **Tuyệt đối không tắt fsync NOMT.** Bất biến: NOMT không bao giờ đi trước block DB (xem `storage.SyncDurable` trong `CommitBlockState`).

---

## GIAI ĐOẠN A — Review độc lập các commit của agent trước (làm trước)
Mục tiêu: xác nhận hoặc bác bỏ từng khẳng định. Với mỗi commit: đọc toàn bộ diff, chạy lại test liên quan, ghi kết luận "đúng / sai / chưa kiểm" kèm bằng chứng vào `note/review_20261007_agent_commits.md`.

### A1. `f20d9b44` + `5e3b9e1d` — tool `secp_tps_blast` và báo cáo TPS
- **Log `[SIG-ENFORCE]`:** `execution/pkg/blockchain/tx_processor/signature_enforcement.go` đã đổi điều kiện log thành `total > 0` (log Info MỌI block trên đường critical). Khôi phục ngưỡng cũ (`elapsed > 20ms || dropped > 0`) hoặc hạ xuống Debug có cờ bật. Giữ khả năng đo bằng một metric Prometheus thay vì log mỗi block.
- **`cache_hit=0` gây hiểu lầm:** bộ đếm `sigStats.cacheHits` chỉ tăng ở đường BLS. Thêm bộ đếm riêng cho trúng `boundSigKey` ở đường secp (trong `checkTxSignature`/`verifySignatures`), hiển thị ở log/metric, có test. Sau đó mới được nói "bound cache hit" trong báo cáo.
- **Số liệu:** xác minh `note/perf_secp_tps_20261007.md` bằng cách chạy lại ÍT NHẤT 5 lần mỗi bản (baseline `87d825dd` vs HEAD hiện tại) trên cùng máy, tính trung bình + độ lệch chuẩn. Báo cáo cũ chỉ 3 lần và khoảng giá trị chồng nhau (+6.6% nằm gần ngưỡng nhiễu): nêu rõ liệu cải thiện TPS có ý nghĩa thống kê hay không; giảm CPU và giảm thời gian lọc chữ ký là các chỉ số đáng tin hơn.
- **"Sustained 999.9 tx/s"** chỉ là rate-limit 1000 tx/s, KHÔNG phải năng lực tối đa. Sửa nhãn trong báo cáo; thêm một lần chạy không giới hạn rate (hoặc rate cao dần) để tìm trần thật ≥ 5 phút.
- Kiểm tra tool không có đường dùng khóa/ví thật; ví sinh ngẫu nhiên; `-verify-parity` thực sự so block hash và state root của từng node (đọc code, đừng tin banner).

### A2. `4868dc61` — P1-2 (cache cumulative gas)
- Đọc `rpc_block.go`/`rpc_transaction.go` sau sửa: không cache khi lỗi/nil receipt; fallback `cumulativeGasUsed` chỉ khi `gasInfo == nil` và có log cảnh báo.
- Kiểm chứng thứ tự: test nhiều nhóm Block-STM thật sự kiểm `transactionIndex` theo thứ tự thực thi (xem lại test mới, thử đảo thứ tự xem test có đỏ không — mutation).
- Metric hit/miss `master_rpc_block_gas_cache_*` xuất hiện trong `/metrics`.
- Chạy `go test -race ./cmd/simple_chain` (lâu ~2 phút).

### A3. `bf07df7a` — bảo mật/CI
- **Rà lại kết luận "không biến thể giả nào chiếm chỗ tx thật" (điểm bảo mật):** đọc từng nơi gọi `calculate_transaction_hash_single` trong `consensus/metanode/src` (`node/queue.rs`, `node/executor_client/block_sending.rs`, `network/tx_socket_server.rs`, `network/rpc.rs`, `consensus/commit_processor/executor.rs`, `tx_recovery.rs`) cùng `types/tx_hash.rs`. Hàm đó băm toàn bộ bytes hay chỉ envelope? Với mỗi nơi: ai dùng hash làm khóa dedup, có thể một biến thể (cùng envelope, proto field khác) đến trước và chiếm chỗ không? Viết test Rust tái hiện nếu có thể. Báo cáo từng nơi: "an toàn vì …" hoặc "RỦI RO".
- `/health`, `/readiness` có CORS `*`: đọc code che khóa trong `committee_key_warning`; test xác nhận không còn bytes khóa/bí mật trong response (kể cả khi mismatch).
- `test_exec_cluster_assert.sh`: chạy thật; thử đưa inventory thiếu `raft_secret_source` phải FAIL ở assert; inventory đúng phải PASS syntax-check.
- `build_check.sh` mở rộng `./cmd/...`: chạy thật, xác nhận không làm chậm bất thường hoặc fail do package cần thư viện chưa build.

**Xong A khi:** có `note/review_20261007_agent_commits.md` với kết luận từng mục + output lệnh; các sửa nhỏ (log, bộ đếm bound-hit, nhãn báo cáo) đã commit local, test xanh.

---

## GIAI ĐOẠN B — Điều tra RSS ~9.7 GB/node (ưu tiên cao: nghi rò rỉ/cache không giới hạn)
Dữ kiện: báo cáo ghi đỉnh RSS ~39 GB toàn cụm 4 node (~9.7 GB/node) chỉ sau 304.000 tx on-chain (50.000 ví), không giải thích.

1. Dựng cluster local cô lập 4 validator (như A1) với `GODEBUG`/pprof bật (`/debug/pprof` nếu có; nếu không, thêm cờ chỉ ở devnet).
2. Chạy workload cố định (ví dụ 100.000 tx, rồi 300.000 tx) và ghi RSS, `go_memstats_*` (heap inuse/idle/released), số goroutine theo thời gian cho từng node + tiến trình Rust consensus (RSS riêng).
3. Phân tách: RSS do **Go heap** hay **Rust**, hay **NOMT page/leaf cache**, **mmap**, **Xapian**, **cache chữ ký** (`verifiedSignatures` rotate), **mempool/`GLOBAL_TX_CACHE`**, **blockGasCache/receipt cache**, `TxPayloadCache`? Dùng `go tool pprof -sample_index=inuse_space`, `perf`/`smem` (RSS vs PSS), `/proc/<pid>/smaps_rollup`.
4. Kiểm tra: dừng tải và chờ — RSS có giảm không (Go: `debug.FreeOSMemory` hoặc GC)? Chạy tải lần 2 cùng quy mô: RSS có tăng tuyến tính theo số tx tích luỹ không (=> rò rỉ) hay bão hoà (=> cache có giới hạn)?
5. Nếu tìm ra thành phần không giới hạn: đề xuất sửa nhỏ nhất, có giới hạn rõ ràng; **không** đụng đường commit/consensus nếu chưa hỏi user. Nếu là cấu hình NOMT cache (MB), ghi giá trị và khuyến nghị.
6. Ghi `note/perf_rss_investigation_20261007.md`: bảng RSS theo thời gian, nhận định từng thành phần, kết luận có/không có rò rỉ, đề xuất.

**Xong B khi:** có kết luận có bằng chứng (profile/ảnh chụp số liệu) về nguồn bộ nhớ; nếu là bug thì có test/benchmark tái hiện và bản sửa an toàn, hoặc ghi rõ cần user quyết định.

---

## GIAI ĐOẠN C — B1: hiệu năng và độ bền NOMT fsync (đo thật)
Đã có `execution/pkg/trie/nomt_commit_bench_test.go` và `execution/scripts/test/test_crash_recovery_nomt.sh` nhưng KHÔNG có báo cáo kết quả.

1. Đọc cả hai file; xác nhận các "cấu hình fsync" mà bench so sánh thật sự khác nhau (không có cấu hình nào tắt fsync trong production path). Nếu bench có biến thể tắt fsync, ghi rõ nó CHỈ để đo chi phí.
2. Chạy bench `go test -bench=NomtCommit -benchmem -count=5 ./pkg/trie/...` (tên thật theo file), ghi output đầy đủ.
3. Đo end-to-end 3 cấu hình × 3 lần trên CÙNG máy với workload `secp_tps_blast` (kiểm chữ ký BẬT): (A) commit `ec6560ce` (trước vendor), (B) HEAD, (C) HEAD + thử nghiệm bỏ `commitWg.Wait()` ở `Commit` (CHỈ để biết chi phí, không dùng nếu không chứng minh an toàn). Ghi tx/s, p50/p99 receipt, thời gian `Commit` mỗi block (`[NOMT-COMMIT-PERF]`), CPU, RSS.
4. Quyết định theo kế hoạch Giai đoạn 1.2: nếu B ≥ 90% A thì chấp nhận; nếu tụt hơn thì profile và đề xuất tối ưu có kiểm soát (không tắt fsync). Nếu cần đánh đổi độ bền/hiệu năng => DỪNG, hỏi user.
5. Chạy `test_crash_recovery_nomt.sh` ĐỦ 10 vòng kill -9 dưới tải (thời điểm ngẫu nhiên): sau mỗi lần khởi động lại, state root từng block khớp giữa các node, không exit 78 `[INTEGRITY]`. Ghi log thật của từng vòng.
6. Ghi `note/perf_nomt_commit_20261007.md` với bảng 3 cấu hình × 3 lần, kết luận và kết quả 10 vòng.

**Xong C khi:** có báo cáo số thật; 10/10 vòng kill -9 không lệch state root (hoặc ghi rõ lỗi và log); quyết định ghi rõ.

---

## GIAI ĐOẠN D — Việc nhỏ còn lại (làm sau A–C)
- Cập nhật tài liệu client: mã lỗi admission mới `90 = ErrInvalidBlobProof` ("KZG proof verification failed") ở nơi liệt kê bảng mã lỗi (grep `ErrInvalidEnvelope`/`89` trong `docs/`, `note/`).
- Nếu A1 cho thấy `secp_tps` trong `deploy/ci/ci_config.yaml` thực sự chạy được qua `./ci.sh run-now --only secp_tps` trên cụm cô lập (không phải cụm của user), ghi lại lệnh và kết quả; nếu không chạy được, nói rõ lý do.

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev trong repo/lịch sử git; thu hồi token GitHub; chạy nhiều máy thật/soak 24h (B2); cutover chain 991 thật; bật pipeline commit Rust→Go (cần duyệt `note/design_pipelined_commit_delivery.md`); push/merge/đóng issue.

## Báo cáo cuối
Danh sách commit; mỗi giai đoạn: lệnh đã chạy + output thật + kết luận; việc nào chưa làm được và vì sao; rủi ro còn lại. Mọi nghi ngờ phải ghi dưới dạng "chưa kiểm chứng", không khẳng định.
