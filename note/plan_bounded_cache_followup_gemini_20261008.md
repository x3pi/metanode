# Kế hoạch giao Gemini (đợt 2): hoàn tất kiểm chứng Bounded Mapping Cache (2026-10-08)

**Bối cảnh:** commit `4221d60a` (code) và `bdf8df43` (evidence/doc) đã đưa two-generation cache vào `execution/pkg/blockchain/blockchain.go`. Review độc lập: `go test -race -run 'Bounded|Mapping|Pebble' ./pkg/blockchain/` pass. Logic swap/promote đúng. Còn thiếu những mục dưới đây.
Quy tắc chung: như `note/plan_bounded_mapping_cache_gemini_20261008.md` (đọc `AGENTS.md`; số liệu phải từ lệnh chạy thật; `git add` theo tên file; không push khi chưa được duyệt; tóm tắt tiếng Việt cuối response).

## Việc 1 — Dọn gofmt (nhỏ, làm trước)
- `gofmt -l execution/pkg/blockchain/` báo `blockchain.go` (3 dòng trống thừa quanh chỗ đã xóa `pruneTxHashCache`/`pruneEthHashCache`, ~dòng 313) và `bounded_mapping_cache_test.go`.
- Chỉ `gofmt -w` hai file này (không format các file khác trong thư mục `tx_processor/`, `scrubber.go` vì không thuộc thay đổi này).
- Chạy lại `go vet` và test liên quan, rồi commit `style(blockchain): gofmt bounded mapping cache`.

## Việc 2 — Thí nghiệm bộ nhớ 45 phút (Bước 5 của kế hoạch trước, CHƯA làm)
Design doc hiện ghi "Implemented, đã kiểm chứng", nhưng evidence mới chỉ gồm unit test race và RPC cold-restart. Chưa có số đo cho thấy heap thực sự được chặn.
1. Trước khi chạy, ghi tiêu chí vào `note/evidence/bounded_mapping_cache_20261008/PREREGISTERED.md` và commit: tải 50 tx/s qua `secp_tps_blast`, 45 phút, `ENABLE_DEBUG_PPROF=true`, so sánh cửa sổ 0–30 và 30–45 phút. PASS nếu slope HeapAlloc W2 ≤ 0.5 MB/phút hoặc ≤ 5% W1. Không đổi tiêu chí sau khi thấy kết quả.
2. Wipe data cụm cô lập, chạy `run_ttl_experiment.py`, **ít nhất 2 lần**; mỗi lần thu `pprof heap` ở phút 10/30/45.
3. Dùng `go tool pprof -top` để kiểm tra `ethHashMapBlsHashMap.Store*` và `txHashToBlockNumberMap.Store*` có còn là top cấp phát tích lũy không.
4. Lưu log/CSV/pprof vào `note/evidence/bounded_mapping_cache_20261008/`, cập nhật `MANIFEST.json` với sha256 thật.
5. Báo kết quả đúng như đo được, kể cả khi FAIL. Nếu FAIL, phân tích phần nào khác chiếm heap (đừng sửa tiêu chí).
6. Nếu chưa chạy được 45 phút ×2, giữ trạng thái design doc là "Implemented — memory verification pending", không ghi "đã kiểm chứng".

## Việc 3 — Sửa lại câu overclaim trong design doc
Trong `note/design_bounded_memory_indexes_20261007.md` mục 7:
- Đổi "giải tỏa triệt để lock contention" thành mô tả đã đo/chưa đo. Chưa có benchmark lock contention nên chỉ nói "loại bỏ quét O(N) định kỳ".
- Ghi rõ cap là 50_000 entry/map × 2 thế hệ, tức tối đa ~100_000 entry mỗi map, và `make(map, limit)` cấp phát trước mỗi lần swap. Nêu ước lượng RAM thật sau khi đo ở Việc 2, không dùng "≤ 10 MB" nếu số đo cho thấy khác.

## Việc 4 — Benchmark đường đọc (nhỏ)
- Viết benchmark `Load` hit/miss/promote cho hai map (trong `bounded_mapping_cache_test.go` hoặc file `_bench_test.go` riêng) và so với bản trước `94861cc9` nếu cần.
- Quan sát: khi `current` đầy mà `Load` promote entry từ `old` sẽ kích hoạt swap, bỏ cả `old`. Chỉ ghi nhận kết quả đo, không tối ưu nếu không có số liệu cho thấy vấn đề (KISS).

## Việc 5 — Xem lại hai commit khác chưa được review
`e6cdccb0` (raft `is_leader` trên `/health` và `mtn_getClusterIdentity`) và `2161ef74` (portal FullTx Explorer) đến từ nhánh công việc khác.
- Với `e6cdccb0`: kiểm tra `raftfeed/node.go` đọc trạng thái leader có đúng khóa/an toàn đồng thời, endpoint không lộ thông tin nhạy cảm, và có test. Nếu không có test, thêm test nhỏ.
- Với `2161ef74`: chỉ cần `npm run build` trong `portal/` sạch lỗi.
- Báo kết quả, không refactor.

## Ngoài phạm vi
`txsCache` và phần RSS ~9.5 GB không do hai map này gây ra; cần task profile riêng (NOMT, Pebble block cache, Rust).

## Tiêu chí hoàn thành
- [ ] `gofmt -l` sạch cho hai file đã sửa
- [ ] PREREGISTERED commit trước thí nghiệm; ≥2 lần chạy 45 phút có pprof và MANIFEST
- [ ] Design doc phản ánh đúng trạng thái đã đo
- [ ] Benchmark đọc có số liệu thật
- [ ] Review `e6cdccb0`, build portal đã chạy
- [ ] `build_check.sh` sạch
