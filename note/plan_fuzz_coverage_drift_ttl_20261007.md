# Kế hoạch: độ phủ kiểm tra bằng đột biến NGẪU NHIÊN, ghi nhận drift RSS, chạy thí nghiệm TTL, dọn khẳng định "100%" — 2026-10-07

Dành cho agent thực hiện. Nền: `dev` local gồm `558c5eb3`, `af5052e0`, `eef4585f`, `7bf15e29` (chưa push).
Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_fix_nomt_reports_evidence_20261007.md` (mục 0), `note/plan_stats_drift_coverage_ttl_20261007.md` (mục 0),
`execution/scripts/test/evidence/{verify_evidence.py,verify_evidence_test.py,mutation_check.sh,stats_util.py,stats_util_test.py,diagnose_rss_drift.py,run_controlled_benchmarks.py,run_ttl_experiment.py}`,
`note/perf_rss_investigation_20261007.md`, `note/perf_nomt_b1_20261007.md`, `note/perf_nomt_durability_20261007.md`, `note/design_bounded_memory_indexes_20261007.md`,
`note/evidence/{mutation_check.log,perf_rss_20261007/rss_drift_diagnostic.log}`.

> **Vì sao có kế hoạch này (reviewer đã tự chạy lại, không dựa vào báo cáo):**
> 1. **Thống kê đã đúng:** `stats_util.py` cho p (t=1.98, df=9.6)=0.0771; (3.50, 11.7)=0.0045; (1.00, 9.5)=0.3421 — khớp tính độc lập. Báo cáo GOGC ghi TPS là INCONCLUSIVE (p=0.0774, CI chứa 0). KHÔNG làm lại phần này.
> 2. **"AIRTIGHT / 14/14 CAUGHT (100%)" là quá lời.** `mutation_check.sh` dùng bộ 14 đột biến CỐ ĐỊNH do chính agent chọn (gần như trùng danh sách reviewer đã đưa) → công cụ bị chỉnh cho đúng các ca đó (overfit).
>    Reviewer chạy bộ đột biến NGẪU NHIÊN khác (đổi chữ số cuối của số trong **dòng dữ liệu bảng có thẻ `evidence:`**, mẫu 60 số/báo cáo, seed 11):
>    **B1: bắt 7/60 (12%)**; **Durability: 40/60 (67%)**; **RSS: 0/60 (0%)**. Số lọt tiêu biểu: cột `R1..R7` và tỉ số `B/A` của B1; cột `KillTiming` (`0.05s`) của durability;
>    toàn bộ bảng TPS/RSS theo từng lượt (GOGC, debug) và bảng đợt 1–8 của RSS (ví dụ `6,198.6`, `5,487.28`).
> 3. **Drift RSS chưa giải thích và BÁO CÁO KHÔNG NHẮC:** `rss_drift_diagnostic.log` (3 cụm mới, 0 tiến trình sót, kích thước đĩa không đổi) cho thấy RSS ngay sau khi khởi động tăng
>    7.385 → 7.761 → 7.993 GB (+8.24%), Spearman 1.0, nhưng `note/perf_rss_investigation_20261007.md` không có chữ "drift"; các bảng RSS vẫn trình bày như cụm sạch.
> 4. **Cột "Log Size 0.0 KB" còn trong báo cáo** (lượt cũ) không có chú thích dù script đo đã sửa.
> 5. **Thí nghiệm TTL ≥ 45 phút CHƯA chạy** (chỉ có `run_ttl_experiment.py`, không có log/kết quả). Hai map index đã có TTL 30 phút (`mappingCacheTTL`) nên "index tăng mãi hay tới trần TTL" vẫn chưa trả lời được.
> 6. **Tài liệu thiết kế còn khẳng định "100%" không có evidence cạnh** (ví dụ "100% dữ liệu `txHash->blockNumber` và `ethHash->blsHash` ĐÃ ĐƯỢC LƯU BỀN VỮNG xuống Pebble DB"; "Pebble DB vốn dĩ đã lưu 100%…").

---

## 0. QUY TẮC CHỐNG BÁO CÁO GIẢ (tuyệt đối bắt buộc — kế thừa, KHÔNG nới lỏng)

1. **Không có file thô trong repo (hoặc sha256 + `stored_at` trong manifest) => không có con số.** Không gõ tay, không làm tròn, không dựng lại số từ báo cáo cũ.
2. **Ba trạng thái PASS / FAIL / INCONCLUSIVE.** Thiếu dữ liệu/không đạt tiêu chí ghi trước => INCONCLUSIVE/FAIL, KHÔNG làm đẹp. Ngưỡng ghi trước, không chỉnh sau khi thấy kết quả.
3. **Cấm "dạy theo đề".** Công cụ kiểm tra KHÔNG được chỉnh để vượt đúng một bộ đột biến cố định. Tiêu chí nghiệm thu dùng bộ đột biến ngẫu nhiên với seed do REVIEWER chọn khi nghiệm thu (xem Giai đoạn 1) — agent không biết trước seed. Cấm các cụm "AIRTIGHT", "100% mutations caught" trong log/báo cáo; chỉ ghi số đo thật (k/n, tỉ lệ, seed, danh sách lọt).
4. **Mọi PASS phải có điều khiển âm chạy thật, lưu log.** Công cụ kiểm tra mới = kiểm tra "bắt được lỗi cố ý", không chỉ "không báo lỗi".
5. **Không so sánh khác điều kiện;** xen kẽ thứ tự; khởi động lại giữa các lượt; nêu cờ/cấu hình trong báo cáo.
6. **Cấm khẳng định tuyệt đối** ("100%", "hoàn toàn", "tuyệt đối", "chứng minh", "không hề", "0%") không có `evidence:<id>` + phạm vi đo cụ thể ngay cạnh — kể cả trong tài liệu thiết kế (`note/design_*.md`), không chỉ trong báo cáo hiệu năng.
7. **Tự kiểm chéo cuối:** chạy lại độc lập ≥ 1 lượt mỗi hạng mục quan trọng bằng đúng lệnh trong manifest; nêu rõ cái KHÔNG chạy lại được.
8. **Quy tắc vận hành:**
   - Zero-fork (AGENTS.md 2.5): không timeout/sleep để quyết định dispatch commit; thiếu bằng chứng => PENDING.
   - Cô lập: chỉ cluster local cổng 31xxx trong scratchpad; KHÔNG đụng 231/230, `/opt/metanode`, tiến trình của user (máy có ~9 tiến trình `/opt/metanode`; đo RSS/CPU theo PID env test, không dùng `ps -C simple_chain`); dừng env bằng `run_env.sh <BASE> stop`.
   - Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token; commit local theo TÊN FILE (không `git add <dir>`; `*.py` gitignore => `git add -f`); cuối commit `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
   - TUYỆT ĐỐI không tắt fsync NOMT; thí nghiệm sửa vendor/hằng số chỉ ở scratchpad (KHÔNG commit) và phải hoàn nguyên + chứng minh.
   - Sau mỗi thay đổi code: `gofmt`, `go vet`, `go test -race` cho package đã sửa, `cd consensus/metanode/scripts && ./build_check.sh` sạch (0 lỗi/0 warning).
   - DỪNG và hỏi user nếu: cần đổi đường commit/consensus; cần `sudo`/máy ngoài; đổi giới hạn bộ nhớ có thể ảnh hưởng tính xác định; thí nghiệm dài sẽ chạy chồng với cluster/tiến trình của user.
   - Code comment tiếng Anh; cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5).

---

## GIAI ĐOẠN 1 — Đột biến ngẫu nhiên làm tiêu chí nghiệm thu (làm trước)
### 1.1 Viết harness đột biến ngẫu nhiên: `execution/scripts/test/evidence/fuzz_mutation_check.py`
Hành vi (đây là cách reviewer đã đo; không được nới lỏng):
1. Với mỗi cặp (thư mục evidence, báo cáo) trong `note/evidence/*/MANIFEST.json` (đọc trường `report`), lấy các **dòng dữ liệu của bảng Markdown** (dòng bắt đầu `|` và chứa `evidence:`), bỏ khối code và bỏ số nằm trong thẻ `evidence:`, URL, đoạn trong dấu backtick, số thứ tự danh sách, ngày tháng.
2. Mỗi "token số" = số thập phân/số nguyên có dấu phẩy ngăn cách hàng nghìn/`#<số>` (số block). Mỗi đột biến: thay CHỮ SỐ CUỐI của đúng 1 token bằng `(chữ số + 3) mod 10` (hoặc nối thêm `7` nếu không phải chữ số), ghi ra bản sao tạm của báo cáo, chạy `verify_evidence.py --strict <dir> --report <bản sao>`.
3. Tham số: `--seed N` (BẮT BUỘC), `--sample K` (mặc định 60/báo cáo; `--all` để thử mọi token), in: tổng số token, số bị bắt, số lọt, **danh sách token lọt kèm số dòng và nội dung dòng**, và tỉ lệ bắt. Ghi JSON kết quả.
4. Test cho chính harness (`fuzz_mutation_check_test.py`): trên một báo cáo giả có 10 số, đã biết công cụ chỉ bắt được k số → harness báo đúng k/10; seed giống nhau cho cùng mẫu.
5. **Không tích hợp seed cố định vào CI/tiêu chí.** Seed dùng khi nghiệm thu do reviewer chọn.
### 1.2 Mở rộng độ phủ cho tới khi đạt tiêu chí
Tiêu chí ghi trước: với **seed bất kỳ** mà reviewer chọn khi nghiệm thu, và `--all` trên dòng dữ liệu của cả 3 báo cáo: **tỉ lệ bắt ≥ 95%** các token số trong dòng dữ liệu bảng có thẻ `evidence:`; mọi token lọt phải thuộc danh sách "ngoại lệ có lý do" khai báo trong manifest (`"unchecked": [{"line_regex":…, "reason":…}]`) và được in ra trong kết quả.
Việc cần làm (không phụ thuộc kết quả hiện tại, dùng harness để dẫn đường):
1. **B1:** extractor cho từng ô bảng chi tiết theo vòng (`R1…R7`: A/B ms và tỉ số `B/A`), `Mean ± SD` của cả A và B, `Ngưỡng 90%`, t, df, p; bảng fsync (30 / 42 lần, µs/call, tổng thời gian kernel).
2. **Durability:** extractor cho cột `KillTiming` (và mọi cột còn lại), số ghi trong văn bản ("20 vòng", "5 vòng kill kép"…), hash/sha trích dẫn trong báo cáo (so với manifest/log).
3. **RSS:** extractor cho **mọi** bảng: bảng 8 đợt (HeapAlloc/HeapInuse/HeapSys/RSS theo node), bảng GOGC 800 vs 50 (từng lượt: thời gian, TPS, Peak RSS, CPU), bảng debug on/off (từng lượt), các thống kê tóm tắt (mean, SD, t, df, p, CI, %), hồi quy (m, ± SE, R², hằng số).
   Nên làm cơ chế tổng quát: mỗi dòng bảng có `evidence:<id>` ánh xạ tới hàng của CSV/log theo khoá (Wave/Node, Run/Config…), công cụ tự đối chiếu từng ô thay vì khai báo extractor cho từng số.
4. Mọi con số lọt còn lại: giải thích và thêm vào `unchecked` với lý do, hoặc bổ sung extractor.
5. Chạy harness với ≥ 3 seed khác nhau do agent tự chọn để phát triển; log ghi seed. Khi nghiệm thu, reviewer sẽ chạy seed khác — KHÔNG được tối ưu riêng cho seed của agent.
6. Cập nhật `mutation_check.sh`: giữ bộ cố định như "ca hồi quy" nhưng đổi tiêu đề, bỏ chữ "AIRTIGHT/100%", ghi rõ đây là bộ cố định và không phản ánh độ phủ tổng thể; thêm bước chạy `fuzz_mutation_check.py` (seed truyền từ tham số) và in kết quả thật.
**Xong khi:** `fuzz_mutation_check.py --all` cho 3 báo cáo đạt ≥ 95% (có danh sách lọt + lý do), test của harness xanh, log lưu trong `note/evidence/` (kèm seed), không còn từ "AIRTIGHT/100%" trong log/báo cáo.

## GIAI ĐOẠN 2 — Ghi nhận drift RSS vào báo cáo (và điều tra tiếp có giới hạn)
### 2.1 Báo cáo (bắt buộc)
Thêm mục "Drift RSS khi khởi động cụm mới" vào `note/perf_rss_investigation_20261007.md`:
- Số liệu 3 lượt từ `rss_drift_diagnostic.log` (baseline RSS tổng 7384.7 / 7760.8 / 7993.3 MB; theo node; đỉnh theo công cụ và theo `/proc`; Spearman; +8.24%) kèm extractor/`evidence:`.
- Các giả thuyết ĐÃ LOẠI TRỪ có bằng chứng (tiến trình sót = 0 trước/sau; kích thước đĩa val0 không đổi) và các giả thuyết CHƯA kiểm.
- Kết luận theo tiêu chí: nếu chưa xác định nguyên nhân → **INCONCLUSIVE** cho "RSS đỉnh tuyệt đối"; chỉ so sánh giữa cấu hình xen kẽ cùng chỉ số lượt mới có giá trị tương đối. Gắn chú thích ngay trước/sau các bảng RSS của GOGC và debug.
- Ghi rõ baseline mỗi node ~1.8–2.1 GB ngay sau khởi động (từ diagnostic) — con số này có đặc điểm gì (genesis 50k tài khoản? page/leaf cache NOMT khởi tạo?) — chỉ nêu nếu có bằng chứng, không đoán.
### 2.2 Điều tra tiếp (có giới hạn thời gian; dừng lại nếu không ra)
Giả thuyết cần kiểm bằng thí nghiệm có điều khiển (mỗi giả thuyết 1 lượt đo + log):
(a) Cache hệ điều hành/THP/NUMA: so `smaps_rollup` (Anonymous vs File vs Shared) và `/proc/<pid>/status` (`RssAnon`, `RssFile`, `RssShmem`) ở baseline của lượt 1 vs 3.
(b) Bộ nhớ vẫn bị giữ do cluster cũ vừa dừng (khoảng nghỉ giữa các lượt): đo khi chèn `sleep` + `sync; drop_caches` KHÔNG được dùng (cần root) — thay bằng chờ tới khi `MemAvailable` ổn định (ghi lại) trước khi khởi động lượt kế.
(c) Khác biệt khởi động giữa các lượt (cùng binary/genesis/cờ?) bằng `diff` cấu hình và biến môi trường; số khoá trong genesis; thời điểm đo baseline (độ trễ sau start).
Nếu không có giả thuyết nào giải thích được drift trong phạm vi đo: ghi `INCONCLUSIVE — nguyên nhân drift chưa xác định, đã loại trừ …`.
### 2.3 Log Size
Gắn chú thích rõ cho các lượt cũ có `0.0 KB` ("giá trị không hợp lệ do lỗi đo, đã sửa trong script, các lượt cũ không chạy lại"). Chạy lại ít nhất 3 lượt với script đã sửa để có log size thật > 0 (lưu evidence); nếu vẫn 0 thì giải thích bằng chứng (đường dẫn log thật, `ls -l`).
**Xong khi:** báo cáo có mục drift, chú thích đúng cho từng bảng RSS, log size có số thật hoặc có giải thích; verify/fuzz vẫn đạt tiêu chí Giai đoạn 1.

## GIAI ĐOẠN 3 — Chạy thí nghiệm TTL ≥ 45 phút (đã có runner, CHƯA chạy)
1. **Chuẩn bị:** đọc `run_ttl_experiment.py`; kiểm tra bằng lượt thử ngắn (3–5 phút, `--duration`) rằng runner chạy, ghi đủ metric (HeapAlloc/HeapInuse sau GC, RSS/PSS, thời gian), và `stats_util.linear_regression` hoạt động. Lưu log thử nhưng KHÔNG dùng làm kết quả chính.
2. **Tiêu chí ghi trước** (ghi vào báo cáo TRƯỚC khi chạy): tải cố định (rate-limit ổn định, cùng workload/ví), tổng ≥ 45 phút (> `mappingCacheTTL`=30 phút + ≥ 3 chu kỳ prune). "Bão hoà" nếu hệ số góc `HeapAlloc` sau GC trong cửa sổ cuối (≥ 15 phút sau mốc 30 phút) ≤ 5% hệ số góc của 30 phút đầu, kèm khoảng tin cậy; ngược lại "chưa bão hoà". Có thể điều chỉnh con số 5% CHỈ trước khi chạy.
3. **Cô lập và xin phép:** thí nghiệm kéo dài ~1 giờ chiếm CPU/RAM lớn (4 validator + blast). Hỏi user trước khi chạy nếu máy đang có tiến trình của user (`/opt/metanode`). Chỉ cluster 31xxx, cổng pprof 31446..31449 (cần `ENABLE_DEBUG_PPROF=true` cho lượt này; ghi rõ).
4. **Đối chiếu TTL ngắn** (tuỳ chọn, chỉ ở scratchpad): build thí nghiệm với `mappingCacheTTL`/`txCacheTTL` ngắn hơn (ví dụ 5 phút) để chứng minh đường cong phụ thuộc TTL. KHÔNG commit thay đổi hằng số; hoàn nguyên và chứng minh bằng `git diff` rỗng. Nếu không làm: ghi "chưa có đối chứng TTL".
5. Lưu chuỗi thời gian: trích ≤ 10 MB trong git (CSV tóm tắt theo phút, log chính), bản đầy đủ ngoài git (`stored_at` + sha256 trong manifest `external`). Báo cáo bằng bảng số + hồi quy theo cửa sổ (dùng `stats_util.py`), có extractor để Giai đoạn 1 kiểm tra.
6. Cập nhật `note/perf_rss_investigation_20261007.md` (đổi "unbounded" thành mô tả đúng theo kết quả: "chặn theo TTL"/"không bão hoà"/"chưa kết luận") và mục 2.3 của `note/design_bounded_memory_indexes_20261007.md` bằng số đo thật.
**Xong khi:** có log ≥ 45 phút trong evidence, kết luận 3 trạng thái theo tiêu chí ghi trước, các tài liệu liên quan đã cập nhật; nếu không chạy được thì ghi rõ lý do và để mục này INCONCLUSIVE (không bịa).

## GIAI ĐOẠN 4 — Dọn khẳng định "100%" trong tài liệu thiết kế và tạo evidence cho khẳng định kỹ thuật
1. Rà `note/design_bounded_memory_indexes_20261007.md`: mọi "100%", "TUYỆT ĐỐI", "hoàn toàn", "0%" phải (a) bị thay bằng mô tả cụ thể kèm `file:dòng` (ví dụ "ghi xuống Pebble ở `blockchain.go:<dòng>`, khoá tiền tố `<…>`"), hoặc (b) kèm `evidence:<id>`.
2. Khẳng định quan trọng "mọi mapping `txHash->blockNumber` và `ethHash->blsHash` đã nằm trong Pebble DB (có thể đọc lại khi bị evict)": tạo **bằng chứng chạy thật**, không chỉ đọc code: ở cluster cô lập, commit N tx rồi xoá/đẩy khỏi map RAM (qua prune hoặc build thí nghiệm TTL ngắn ở scratchpad) và chứng minh đọc lại được từ Pebble qua RPC receipt; lưu log.
   Nếu không làm được, ghi "đã đọc code, chưa kiểm chứng bằng chạy thật" thay vì khẳng định.
3. Tạo thư mục evidence cho tài liệu thiết kế (ví dụ `note/evidence/design_bounded_memory_indexes_20261007/` với danh sách `file:dòng` của mọi nơi gọi, sinh bằng grep/AST và lưu output thô) và cho công cụ kiểm tra chạy trên tài liệu thiết kế (từ cấm, tag evidence).
4. **KHÔNG viết code giới hạn bộ nhớ** cho tới khi user duyệt thiết kế. Tài liệu giữ trạng thái "DRAFT — chờ duyệt".
**Xong khi:** tài liệu thiết kế qua kiểm tra từ cấm/evidence, khẳng định "đã lưu Pebble" có log chạy thật hoặc nhãn "chưa kiểm chứng bằng chạy thật".

## GIAI ĐOẠN 5 — Tổng kết, tự kiểm chéo, commit
1. Cập nhật báo cáo theo mẫu: Tiêu chí (ghi trước) → Phương pháp (lệnh y nguyên) → Kết quả (bảng có `evidence:<id>`) → Giới hạn → Chưa làm → "Thay đổi so với bản trước".
2. Chạy `python3 -I execution/scripts/test/evidence/verify_evidence.py --strict note/evidence/<từng-thư-mục>`: tất cả exit 0. Chạy `fuzz_mutation_check.py --all` với ≥ 3 seed bất kỳ: ghi kết quả thật (k/n, danh sách lọt).
3. Tự kiểm chéo (mục 0.7): chạy lại ≥ 1 vòng crash test, 1 lượt B1, 1 lượt GOGC (cụm mới), ghi đối chiếu số.
4. Cập nhật `PROJECT_STRUCTURE.md` nếu thêm module/script (`fuzz_mutation_check.py`).
5. Commit theo giai đoạn (mỗi giai đoạn ≥ 1 commit, theo TÊN FILE). Không push.
6. Báo cáo cuối cho user: danh sách commit; mỗi hạng mục: lệnh + đường dẫn evidence + trạng thái PASS/FAIL/INCONCLUSIVE; việc chưa làm; rủi ro; những gì KHÔNG chạy lại được. Nêu rõ seed đã dùng và để user/reviewer chạy seed mới.

---

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev trong repo/lịch sử git; thu hồi token GitHub; VM/máy riêng cho power-loss thật và B2; mọi lệnh `sudo`/`drop_caches`/đụng thiết bị khối trên máy của user nếu chưa có văn bản cho phép;
cutover chain 991; bật pipeline commit Rust→Go; **viết/sửa code giới hạn bộ nhớ index khi chưa được user duyệt thiết kế**; chạy thí nghiệm TTL đè lên tiến trình của user khi chưa hỏi; push/merge/đóng issue.

## Tiêu chí hoàn thành toàn kế hoạch
- Với seed bất kỳ reviewer chọn: `fuzz_mutation_check.py --all` trên dòng dữ liệu bảng có thẻ evidence của 3 báo cáo bắt ≥ 95% (có danh sách lọt + lý do); không còn từ "AIRTIGHT/100% caught".
- Báo cáo RSS có mục drift (số liệu thật, loại trừ, kết luận 3 trạng thái) và chú thích cho từng bảng RSS/log size.
- Có log thí nghiệm TTL ≥ 45 phút (hoặc INCONCLUSIVE có lý do) và kết luận được cập nhật ở báo cáo + tài liệu thiết kế.
- Tài liệu thiết kế không còn khẳng định tuyệt đối thiếu evidence; khẳng định "đã lưu Pebble" có bằng chứng chạy thật hoặc nhãn "chưa kiểm chứng".
- Báo cáo cuối nêu rõ mọi mục INCONCLUSIVE thay vì cố làm cho "xanh".
