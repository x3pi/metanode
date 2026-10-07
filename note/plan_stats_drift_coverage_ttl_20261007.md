# Kế hoạch: sửa thống kê p-value, điều tra drift RSS, mở rộng độ phủ công cụ kiểm tra, thí nghiệm TTL ≥ 30 phút — 2026-10-07

Dành cho agent thực hiện. Nền: `dev` local gồm các commit `48390877`, `efe7ec61`, `6bf1081c`, `09e7dbd9`, `5ce291c3` (chưa push).
Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_fix_nomt_reports_evidence_20261007.md` (mục 0),
`note/plan_evidence_tool_rss_memory_followup_20261007.md` (mục 0), `execution/scripts/test/evidence/{verify_evidence.py,verify_evidence_test.py,run_controlled_benchmarks.py,run_rss_investigation.py,run_logged.sh}`,
`execution/scripts/test/gate_e2e/{run_env.sh,gen_env.py}`, `note/perf_rss_investigation_20261007.md`, `note/perf_nomt_durability_20261007.md`, `note/perf_nomt_b1_20261007.md`,
`note/design_bounded_memory_indexes_20261007.md`, `execution/pkg/blockchain/blockchain.go` (hằng số `*TTL`, `Prune`).

> **Vì sao có kế hoạch này (tất cả đã được reviewer tự tính lại / thử đột biến):**
> 1. **p-value trong `run_controlled_benchmarks.py` dùng xấp xỉ chuẩn thay vì phân phối t**, nên thấp hơn thực tế. Tính lại từ cùng (t, df):
>    TPS GOGC (t=1.98, df=9.6): báo cáo ghi p=0.0480, đúng là **0.0771**; RSS (t=3.50, df=11.7): ghi 0.0005, đúng **0.0045**; debug (t=1.00, df=9.5): ghi 0.3187, đúng **0.3421**.
>    Theo tiêu chí GHI TRƯỚC của chính báo cáo (p<0.05 VÀ khoảng tin cậy 95% không chứa 0), khoảng tin cậy TPS là [−36, 741] (chứa 0) và p thật 0.077 → "GOGC=50 giảm TPS ~5.8%" phải là **INCONCLUSIVE**. Kết luận RSS (−30%) vẫn đứng vững.
> 2. **Drift RSS giữa các lượt dù tuyên bố "cụm sạch mỗi lượt":** Spearman 0.96 theo thứ tự lượt trong cả hai khối (default 6.7→9.0→9.0→10.7→10.4→11.0→12.0 GB; GOGC=50 4.4→5.5→6.4→7.4→7.6→8.3→8.2 GB). Cụm mới thì không được có xu hướng này → nguồn sót chưa rõ; số RSS tuyệt đối chưa đáng tin.
> 3. **Cột "Log Size" luôn 0.0 KB** ở mọi lượt → đường đo log hỏng/vô nghĩa.
> 4. **Độ phủ của `verify_evidence.py` thấp:** durability 0 extractor, B1 7, RSS 18. Các đột biến sau vẫn **không bị bắt** (exit 0): đổi block hash/số block/`PAUSED_AT_#…` trong báo cáo durability; đổi `98.89` (độ dốc) và `47.81` (memstats) trong báo cáo RSS; đổi `+1.62%` và PASS→FAIL trong B1; xoá thẻ `evidence:` ở một dòng bảng B1.
> 5. **Hai map index đã có TTL 30 phút** (`mappingCacheTTL`, `cleanupInterval=1m`, `Prune`). Báo cáo RSS gọi chúng "unbounded" nhưng đúng hơn là "chặn theo thời gian, không chặn theo dung lượng". Benchmark 8 đợt chỉ kéo dài vài phút nên CHƯA phân biệt được "đang tăng tới trần TTL" với "tăng mãi".

---

## 0. QUY TẮC CHỐNG BÁO CÁO GIẢ (tuyệt đối bắt buộc — kế thừa, KHÔNG nới lỏng)

1. **Không có file thô trong repo (hoặc sha256 + `stored_at` trong manifest) => không có con số.** Không gõ tay, không làm tròn, không dựng lại số từ báo cáo cũ.
2. **Ba trạng thái PASS / FAIL / INCONCLUSIVE.** Thiếu dữ liệu hoặc không đạt tiêu chí ghi trước => INCONCLUSIVE/FAIL, KHÔNG "làm đẹp". Ngưỡng ghi trước, không chỉnh sau khi thấy kết quả.
3. **Mọi PASS phải có điều khiển âm chạy thật, lưu log.** Với thống kê: điều khiển âm = chạy hàm kiểm định trên dữ liệu đã biết đáp án (xem Giai đoạn 1).
4. **Không so sánh khác điều kiện;** xen kẽ thứ tự; khởi động lại giữa các lượt; nêu cờ/cấu hình trong báo cáo.
5. **Số trong báo cáo phải kiểm chứng tự động được.** Sau Giai đoạn 3, `verify_evidence.py --strict` phải exit ≠ 0 khi sửa tay BẤT KỲ con số quan trọng nào trong báo cáo (bài kiểm tra đột biến lưu log).
6. **Cấm khẳng định tuyệt đối** ("100%", "hoàn toàn", "tuyệt đối", "chứng minh", "không hề", "0%") thiếu evidence và phạm vi đo cụ thể ngay cạnh.
7. **Tự kiểm chéo cuối:** chạy lại độc lập ≥ 1 lượt mỗi hạng mục quan trọng bằng đúng lệnh trong manifest, ghi đối chiếu; nêu rõ cái KHÔNG chạy lại được.
8. **Quy tắc vận hành:**
   - Zero-fork (AGENTS.md 2.5): không timeout/sleep để quyết định dispatch commit; thiếu bằng chứng => PENDING.
   - Cô lập: chỉ cluster local cổng 31xxx trong scratchpad; KHÔNG đụng 231/230, `/opt/metanode`, tiến trình của user (máy có ~9 tiến trình `/opt/metanode`; đo RSS/CPU theo PID env test, không dùng `ps -C simple_chain`); dừng env bằng `run_env.sh <BASE> stop`.
   - Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token; commit local theo TÊN FILE (không `git add <dir>`; `*.py` gitignore => `git add -f`); cuối commit `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
   - TUYỆT ĐỐI không tắt fsync NOMT; thí nghiệm sửa vendor chỉ ở scratchpad và phải hoàn nguyên + chứng minh.
   - Sau mỗi thay đổi code: `gofmt`, `go vet`, `go test -race` cho package đã sửa, `cd consensus/metanode/scripts && ./build_check.sh` sạch (0 lỗi/0 warning).
   - DỪNG và hỏi user nếu: cần đổi đường commit/consensus; cần `sudo`/máy ngoài; đổi giới hạn bộ nhớ có thể ảnh hưởng tính xác định; kết quả buộc đánh đổi độ bền vs hiệu năng.
   - Code comment tiếng Anh; cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5).

---

## GIAI ĐOẠN 1 — Sửa kiểm định thống kê (p-value, khoảng tin cậy) — làm trước
1. Trong `run_controlled_benchmarks.py` (và mọi script khác dùng Welch/p-value, ví dụ `run_b1_benchmarks.py`, `run_rss_investigation.py`): thay xấp xỉ chuẩn bằng **phân phối t thật** với bậc tự do Welch–Satterthwaite.
   Nếu không muốn thêm phụ thuộc `scipy`, tự cài hàm CDF t qua hàm beta không đầy đủ (regularized incomplete beta) hoặc tích phân số ổn định; KHÔNG dùng xấp xỉ chuẩn. Khoảng tin cậy dùng `t_crit(0.975, df)` chính xác.
2. Tạo module dùng chung `execution/scripts/test/evidence/stats_util.py` (Welch t, df, p hai phía, CI, mean, SD mẫu, Spearman/hồi quy tuyến tính + CI độ dốc) kèm `stats_util_test.py`:
   - Kiểm tra với các giá trị tham chiếu độc lập (ví dụ bảng t tra cứu): (t=1.98, df=9.6) → p≈0.0771; (t=3.50, df=11.7) → p≈0.0045; (t=1.00, df=9.5) → p≈0.3421; sai số ≤ 1e-3.
   - Điều khiển âm: nếu ai đó đổi lại sang xấp xỉ chuẩn, test phải đỏ (ví dụ so với p chuẩn 0.0477 cho ca đầu tiên).
3. Tính lại TOÀN BỘ p-value/CI trong các báo cáo GOGC/debug từ log thô (CSV/log đã có trong evidence, KHÔNG chạy lại nếu không cần): báo cáo kết quả mới bằng số đúng.
   - TPS GOGC=800 vs 50: theo tiêu chí ghi trước → ghi **INCONCLUSIVE** (p thật ≈ 0.077, CI chứa 0), kèm câu "không phát hiện chênh lệch TPS có ý nghĩa thống kê với n=7"; bỏ cụm "mức đánh đổi 7.3%/5.77%" khỏi phần kết luận.
   - RSS: giữ kết luận giảm ~30% nhưng ghi p chính xác.
   - Debug on/off: ghi p chính xác; kết luận "không phát hiện khác biệt".
4. Khai báo extractor cho các số p/CI/t/df trong manifest (để Giai đoạn 3 kiểm tra tự động).
**Xong khi:** `stats_util_test.py` xanh (kể cả ca âm), báo cáo GOGC/debug dùng số đúng và nhãn INCONCLUSIVE cho TPS, công cụ kiểm tra exit 0.

## GIAI ĐOẠN 2 — Điều tra drift RSS giữa các lượt và sửa đo log size
### 2.1 Drift RSS (Spearman 0.96)
Giả thuyết cần kiểm chứng (từng cái một, có bằng chứng, không đoán):
(a) `stop_and_clean_bench` không dừng hết tiến trình (hoặc dừng nhưng tiến trình con/`parent_chain` còn sống) → PID mới đo cộng dồn.
(b) Thư mục dữ liệu `BENCH_BASE` không được xoá sạch → state tích luỹ qua các lượt.
(c) Hàm đo RSS cộng RSS của PID cũ/PID tái sử dụng, hoặc đo "Peak RSS" bằng cách lấy max theo thời gian toàn script thay vì theo lượt.
(d) Bộ nhớ hệ điều hành chưa trả lại (page cache/cgroup) bị tính vào RSS. RSS thuần của tiến trình không gồm page cache; kiểm tra `smaps_rollup` thật.
(e) Thật sự có tích luỹ trong nhị phân dù cluster mới (ví dụ file tĩnh, biến môi trường kế thừa) — cần bằng chứng qua `/proc/<pid>/status`, `smaps_rollup`, `ps -eo pid,rss,cmd | grep <BENCH_BASE>` trước/sau mỗi lượt.
Việc làm:
1. Thêm bước chẩn đoán vào `run_controlled_benchmarks.py` (hoặc script riêng ở scratchpad): trước và sau mỗi lượt ghi (i) danh sách tiến trình còn sống liên quan `BENCH_BASE` (PID, RSS, cmdline), (ii) kích thước thư mục dữ liệu, (iii) số block/khoá trong genesis, (iv) RSS từng node theo thời gian (mẫu mỗi 1 s).
2. Chạy thử nghiệm chẩn đoán: 3 lượt cùng cấu hình, mỗi lượt cluster mới; so điểm khởi đầu RSS của lượt 1 với lượt 3. Lưu log thô vào evidence.
3. Sửa nguyên nhân tìm được (ví dụ dừng/giết đúng PID, xoá `BENCH_BASE`, đo RSS theo PID của lượt) và chạy lại: **tiêu chí ghi trước:** RSS khởi động của lượt k phải nằm trong ±10% lượt 1, và Spearman của RSS đỉnh theo thứ tự lượt không còn ≥ 0.8 (hoặc nêu rõ hệ số, ý nghĩa).
4. Nếu không tìm ra nguyên nhân: ghi **INCONCLUSIVE** + mô tả những gì đã loại trừ; cấm khẳng định "cluster sạch".
### 2.2 Cột Log Size luôn 0.0 KB
Đọc `get_log_size_kb()` trong `run_controlled_benchmarks.py`: xác định vì sao luôn 0 (đường dẫn sai? log chưa flush? `run_env.sh` ghi vào `$BASE/logs/*.log`). Sửa và chứng minh bằng một lượt có log thật; hoặc bỏ cột nếu không đo được và ghi "không đo được". Thêm test/kiểm tra rằng giá trị > 0 khi có log.
**Xong khi:** có log chẩn đoán trong evidence, nguyên nhân drift được xác định (hoặc INCONCLUSIVE có lý do), log size đúng hoặc bị bỏ.

## GIAI ĐOẠN 3 — Mở rộng độ phủ `verify_evidence.py` (chặn mọi sửa tay số liệu quan trọng)
Mục tiêu: lặp lại các đột biến của reviewer phải cho `exit ≠ 0`.
1. **Durability (0 extractor):** thêm extractor đọc `durability_20rounds_summary.csv` + log: số vòng, mỗi vòng `TargetNodes`, `H_before`, `CheckBlock`, `BlockHash`, `StateRoot`, `QuorumPause`, `Verdict`; kiểm tra đếm vòng PASS/FAIL/INCONCLUSIVE khớp bảng và văn bản ("5 vòng kill kép", "6 vòng giết leader"…). Mọi giá trị trong bảng báo cáo phải khớp CSV theo từng ô.
2. **RSS:** extractor cho độ dốc hồi quy (m, R², CI), các giá trị `memstats_top_inuse_summary.txt` (ví dụ 5.98 / 47.81 / 35.06 MB), các bảng 8 đợt và hai bảng đối chứng; kiểm tra p/t/df/CI sau Giai đoạn 1.
3. **B1:** extractor cho `% Diff`, `Welch t (df)`, cột `Ngưỡng 90%`, `Kết luận`; kiểm tra rằng `PASS/FAIL` trong bảng khớp quy tắc (B ≤ A/0.9) tính lại từ số thô (không tin chữ).
4. **Mỗi dòng dữ liệu của MỌI bảng số có `evidence:<id>`;** thiếu tag => FAIL (hiện xoá tag ở dòng bảng B1 vẫn exit 0 — sửa cho đúng).
5. **Số trong văn bản tự do** (không chỉ trong bảng) mà nằm trong danh sách "số quan trọng" khai báo trong manifest (`"claims": [{"text_regex": "...", "extractor": "..."}]`) cũng phải khớp.
6. Test mới (`verify_evidence_test.py`) tái hiện đúng từng đột biến đã nêu ở mục "Vì sao" #4 và đòi `exit ≠ 0`; kèm ca xanh để chứng minh không báo đỏ oan.
7. Lưu log chạy lại bộ đột biến (script `mutation_check.sh` sinh bản sao báo cáo, sửa, chạy công cụ, tổng hợp bắt/trượt) vào evidence; **mọi đột biến phải "CAUGHT"**.
**Xong khi:** bộ đột biến của reviewer 100% CAUGHT (số cụ thể "k/k" ghi trong log), `--strict` exit 0 cho 3 thư mục evidence.

## GIAI ĐOẠN 4 — Thí nghiệm TTL ≥ 30 phút: heap có bão hoà không?
Mục tiêu: phân biệt "index tăng tới trần TTL 30 phút rồi phẳng" với "tăng mãi".
1. **Tiêu chí ghi trước:** đặt mức tải cố định (ví dụ rate-limit ổn định, cùng workload/ví); thời lượng ≥ 45 phút (> `mappingCacheTTL`=30 phút + vài chu kỳ prune). Kết luận "bão hoà" khi hệ số góc `HeapAlloc` sau GC trong cửa sổ cuối (≥ 15 phút sau khi qua TTL) ≤ ngưỡng ghi trước (ví dụ ≤ 5% mức tăng của 30 phút đầu), ngược lại FAIL.
2. Cách đo: mỗi 60 s gọi `/debug/pprof/heap?gc=1` (cổng pprof 31446..31449; cần bật pprof cho lượt này qua `ENABLE_DEBUG_PPROF=true`, ghi rõ trong báo cáo) trên cả 4 node; lưu `go_memstats`, `smaps_rollup`, số entry của hai map nếu có hook/metric (nếu chưa có, ghi "không đo được số entry", không bịa).
3. Có một đối chứng: lặp lại trong cùng điều kiện với TTL ngắn hơn (ví dụ sửa hằng số `mappingCacheTTL` ở build thí nghiệm trong scratchpad, KHÔNG commit) để chứng minh đường cong phụ thuộc TTL (nếu TTL ngắn → trần thấp hơn). Nếu không làm được, ghi "chưa có đối chứng TTL".
4. Dùng `stats_util.py` (Giai đoạn 1) cho hồi quy theo cửa sổ; báo cáo kèm biểu đồ số liệu dạng bảng (không cần ảnh).
5. Cập nhật báo cáo RSS: đổi mô tả "unbounded cache" thành mô tả chính xác theo kết quả (chặn theo TTL / không bão hoà); cập nhật `note/design_bounded_memory_indexes_20261007.md` mục 2.3 bằng số đo thật.
**Xong khi:** có chuỗi thời gian ≥ 45 phút trong evidence (nén/trích đoạn ≤ 10 MB trong git, bản đầy đủ ngoài git kèm sha256), kết luận 3 trạng thái theo tiêu chí ghi trước.

## GIAI ĐOẠN 5 — Hoàn thiện tài liệu thiết kế bounded index (CHỜ USER DUYỆT, không tự sửa code)
1. Sửa `note/design_bounded_memory_indexes_20261007.md`: bỏ khẳng định "TUYỆT ĐỐI KHÔNG GÂY FORK … 0%"; thay bằng mô tả phạm vi chứng minh: liệt kê TẤT CẢ nơi gọi `GetBlockNumberByTxHashFast`/`ethHashMapBlsHash.Load` (kèm `file:dòng`, đã thấy ở `receipt_helper.go`, `rpc_transaction.go`, `block_processor_receipt.go`) và nêu rõ cái nào chạy trong luồng thực thi/commit (nếu có thì phải phân tích riêng). Nêu "chưa phát hiện đường ảnh hưởng consensus trong phạm vi grep; cần user xác nhận".
2. Cập nhật phần "vì sao cần giới hạn dung lượng" bằng kết quả Giai đoạn 4.
3. **KHÔNG viết code giới hạn bộ nhớ** cho đến khi user duyệt phương án. Chỉ cập nhật tài liệu.
**Xong khi:** tài liệu có danh sách nơi gọi đầy đủ, kết quả đo Giai đoạn 4, và vẫn ở trạng thái "chờ duyệt".

## GIAI ĐOẠN 6 — Tổng kết, tự kiểm chéo, commit
1. Cập nhật các báo cáo bị ảnh hưởng theo mẫu: Tiêu chí (ghi trước) → Phương pháp (lệnh y nguyên) → Kết quả (bảng có `evidence:<id>` + extractor) → Giới hạn → Chưa làm → "Thay đổi so với bản trước" (nêu rõ các p-value cũ bị sửa và kết luận bị rút/đổi).
2. Chạy `python3 -I execution/scripts/test/evidence/verify_evidence.py --strict note/evidence/<từng-thư-mục>`: tất cả exit 0. Chạy bộ đột biến (Giai đoạn 3): tất cả CAUGHT; lưu log.
3. Tự kiểm chéo (mục 0.7): chạy lại ≥ 1 vòng crash test, 1 lượt B1, 1 lượt GOGC (cụm mới), ghi đối chiếu số.
4. Cập nhật `PROJECT_STRUCTURE.md` nếu thêm module/script mới (`stats_util.py`, `mutation_check.sh`).
5. Commit theo giai đoạn (mỗi giai đoạn ≥ 1 commit, theo TÊN FILE). Không push.
6. Báo cáo cuối cho user: danh sách commit; mỗi hạng mục: lệnh + đường dẫn evidence + trạng thái PASS/FAIL/INCONCLUSIVE; việc chưa làm; rủi ro; những gì KHÔNG chạy lại được.

---

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev trong repo/lịch sử git; thu hồi token GitHub; VM/máy riêng cho power-loss thật và B2; mọi lệnh `sudo`/đụng thiết bị khối trên máy của user nếu chưa có văn bản cho phép;
cutover chain 991; bật pipeline commit Rust→Go; **viết/sửa code giới hạn bộ nhớ index khi chưa được user duyệt thiết kế**; push/merge/đóng issue.

## Tiêu chí hoàn thành toàn kế hoạch
- Mọi p-value/CI trong báo cáo dùng phân phối t chính xác, có test với giá trị tham chiếu và điều khiển âm; TPS GOGC ghi INCONCLUSIVE theo tiêu chí ghi trước.
- Drift RSS có nguyên nhân được xác định bằng chứng (hoặc INCONCLUSIVE có lý do); cột log size đúng hoặc bị bỏ.
- Bộ đột biến của reviewer (sửa số trong durability/RSS/B1, xoá thẻ evidence ở dòng bảng) 100% CAUGHT, log lưu trong evidence.
- Có chuỗi thời gian ≥ 45 phút trả lời "index tăng tới trần TTL hay tăng mãi"; báo cáo và tài liệu thiết kế dùng mô tả đúng ("chặn theo TTL"/"không bão hoà" tuỳ kết quả).
- Không còn khẳng định tuyệt đối thiếu bằng chứng; mọi mục INCONCLUSIVE được ghi rõ thay vì cố làm cho "xanh".
