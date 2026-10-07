# Kế hoạch: vá công cụ kiểm tra bằng chứng, chuẩn hoá evidence RSS, đo lại GOGC, giới hạn index bộ nhớ — 2026-10-07

Dành cho agent thực hiện. Nền: `dev` local (6 commit chưa push: `97763e22` … `94095f57`) + 108 file evidence RSS chưa commit trong
`note/evidence/perf_rss_20261007/`. Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_fix_nomt_reports_evidence_20261007.md` (đặc biệt mục 0),
`execution/scripts/test/evidence/{verify_evidence.py,verify_evidence_test.py,run_logged.sh,run_rss_investigation.py,run_b1_benchmarks.py,summarize_durability.py}`,
`note/perf_nomt_{b1,durability,commit}_20261007.md`, `note/perf_rss_investigation_20261007.md`, `execution/scripts/test/test_crash_recovery_nomt_expanded.sh`,
`execution/scripts/test/gate_e2e/run_env.sh`.

> **Vì sao có kế hoạch này (đã xác nhận bằng chạy thật khi review):**
> 1. `verify_evidence.py` CHỈ kiểm tra sha256, mã `evidence:<id>` tồn tại và từ cấm. Khi sửa tay `5.495 ± 0.649` → `9.999 ± 0.649` trong bản sao báo cáo B1,
>    công cụ vẫn `exit 0`. Phần "tính lại trung bình/độ lệch chuẩn/số vòng từ log thô rồi so với báo cáo" của kế hoạch trước (mục 0.3.4–0.3.5) CHƯA được làm.
> 2. Báo cáo RSS không được công cụ kiểm tra (tên thư mục `perf_rss_20261007` ≠ `perf_rss_investigation_20261007.md` nên auto-detect không khớp).
> 3. Evidence RSS: 108 file, 90 MB, CHƯA commit, KHÔNG nằm trong manifest (manifest chỉ có log + CSV) → các số trích từ memstats/pprof không kiểm chứng lại được từ repo.
> 4. So sánh `GOGC=800` vs `GOGC=50` không kiểm soát: không xen kẽ, không khởi động lại giữa các lượt, Peak RSS tăng dần theo lượt trong cả hai khối
>    (default 19→28→31 GB; GOGC=50 7→8→9 GB), TPS −7.3% nằm trong nhiễu với n=3 nhưng báo cáo viết "chứng minh hiệu quả cao".
> 5. Điều khiển âm đúng lỗi gốc (`sync_timeout`: đồng bộ thất bại thì KHÔNG được PASS) có trong script nhưng KHÔNG có log trong evidence;
>    điều khiển âm đã chạy (`mismatch_root`) do chính script tự bơm giá trị sai nên chỉ kiểm tra bộ so sánh parity.
> 6. `ENABLE_DEBUG_PPROF` mặc định `true` nên `-debug=true` vẫn bật mặc định; chưa có phép đo ảnh hưởng của cờ này.
> 7. Phát hiện kỹ thuật thật: `HeapAlloc` sau GC tăng **+98.89 MB / 100k tx** (R²=0.986; tôi đã tính lại từ CSV, khớp) do index trong RAM không giới hạn.

---

## 0. QUY TẮC CHỐNG BÁO CÁO GIẢ (tuyệt đối bắt buộc — kế thừa và siết chặt)

1. **Không có file thô trong repo (hoặc sha256 + nơi lưu trong manifest) => không có con số.** Không gõ tay, không làm tròn, không "dựng lại" số từ báo cáo cũ.
2. **Ba trạng thái PASS / FAIL / INCONCLUSIVE.** Thiếu dữ liệu => `INCONCLUSIVE`. Ngưỡng chấp nhận viết TRƯỚC khi chạy, không chỉnh sau khi thấy kết quả.
3. **Mọi test PASS phải có điều khiển âm CHẠY THẬT và lưu log**, bơm lỗi vào HỆ THỐNG hoặc vào đúng nhánh bị nghi (không chỉ vào bộ so sánh).
4. **Không so sánh khác điều kiện.** Hai số chỉ đặt cạnh nhau (và chỉ tính %) khi cùng workload/kích thước/cờ/máy, xen kẽ thứ tự, khởi động lại giữa các lượt.
5. **Báo cáo mới phải tự thân kiểm chứng được:** công cụ `verify_evidence.py` (sau khi vá ở Giai đoạn 1) phải exit ≠ 0 nếu bất kỳ con số trong bảng không khớp log thô.
6. **Cấm khẳng định tuyệt đối** ("100%", "hoàn toàn", "tuyệt đối", "chứng minh", "không hề") cho đến khi có evidence VÀ phạm vi đo cụ thể ngay cạnh. Giữ nguyên danh sách từ cấm của công cụ.
7. **Tự kiểm chéo cuối kế hoạch:** chạy lại độc lập ≥ 1 lượt mỗi hạng mục quan trọng bằng đúng lệnh trong manifest và ghi đối chiếu; báo cáo cuối nêu rõ cái KHÔNG chạy lại được.
8. **Quy tắc vận hành:**
   - Zero-fork (AGENTS.md 2.5): không timeout/sleep để quyết định dispatch commit; thiếu bằng chứng => PENDING.
   - Cô lập: chỉ cluster local cổng 31xxx trong scratchpad; KHÔNG đụng 231/230, `/opt/metanode`, tiến trình của user (đo RSS/CPU theo PID env test, không dùng `ps -C simple_chain`); dừng env bằng `run_env.sh <BASE> stop`.
   - Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token; commit local theo TÊN FILE (không `git add <dir>`; `*.py` gitignore => `git add -f`); cuối commit `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
   - TUYỆT ĐỐI không tắt fsync NOMT; mọi thí nghiệm sửa vendor chỉ ở scratchpad và phải hoàn nguyên + chứng minh bằng lệnh.
   - Sau mỗi thay đổi code: `gofmt`, `go vet`, `go test -race` cho package đã sửa, `cd consensus/metanode/scripts && ./build_check.sh` sạch (0 lỗi/0 warning).
   - DỪNG và hỏi user nếu: cần đổi đường commit/consensus; cần `sudo`/máy ngoài; kết quả buộc đánh đổi độ bền vs hiệu năng; đổi giới hạn bộ nhớ ảnh hưởng tính xác định (xem Giai đoạn 5).
   - Code comment tiếng Anh; cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5).

---

## GIAI ĐOẠN 1 — Vá `verify_evidence.py` để thật sự kiểm tra SỐ LIỆU (làm trước, ưu tiên cao nhất)

Yêu cầu chức năng (tất cả phải có TEST RIÊNG chứng minh công cụ đỏ khi bị bẻ):
1. **Ánh xạ báo cáo ↔ thư mục evidence tường minh:** thêm trường `"report": "note/<tên>.md"` vào `MANIFEST.json` và bắt buộc công cụ đọc nó; nếu thiếu/không tồn tại => FAIL.
   Bỏ cách đoán tên theo thư mục. Sửa manifest của 3 báo cáo hiện có (durability, b1, rss) để có `report`.
2. **Bảng số liệu có cột bắt buộc `evidence:<id>` ở MỖI dòng dữ liệu** (không chỉ "ở đâu đó trong báo cáo"). Dòng bảng chứa số mà thiếu tag => FAIL.
3. **Trích số từ log/CSV thô và so với báo cáo** theo "extractor" khai báo trong manifest: mỗi entry có `"extract": [{ "name":"B1_100_A_mean", "file":"…", "regex" hoặc "csv_col"+"filter", "stat":"mean|sd|count|slope", "unit":"ms" }]`
   và báo cáo trích bằng thẻ `{{B1_100_A_mean}}` hoặc cột có `evidence:<id>#<tên>`; công cụ tính lại và so (sai số tương đối ≤ 0.5%, hoặc đúng bằng cho số nguyên/đếm).
   Báo cáo hiện có phải được chuyển sang cơ chế này (hoặc tối thiểu mọi số trong bảng quan trọng).
4. **Kiểm tra đếm:** số vòng PASS/FAIL/INCONCLUSIVE trong bảng = số dòng tương ứng trong CSV/log thô; số vòng nêu trong văn bản ("7 vòng") phải khớp danh sách (lỗi đã thấy: ghi "7 vòng giết leader" nhưng liệt kê 6).
5. **Manifest hợp lệ hơn:** mọi file trong thư mục evidence phải được liệt kê trong manifest HOẶC nằm trong danh sách `"external"` có `sha256`, `bytes`, `stored_at`, `reason` (xem Giai đoạn 2); file lạ không nằm trong manifest => FAIL.
   `git_status` không rỗng phải được in cảnh báo và ghi rõ danh sách file đang sửa dở; có cờ `--strict` khiến `git_status` bẩn => FAIL.
6. **Giới hạn dung lượng:** tổng bytes các file commit trong thư mục evidence ≤ 10 MB (cờ `--max-mb`), vượt => FAIL.
7. **Test (`verify_evidence_test.py`) bắt buộc có các ca đỏ:** sửa tay 1 số trong bảng; thiếu tag ở 1 dòng bảng; sai sha; thiếu `report`; file lạ ngoài manifest; số vòng văn bản ≠ danh sách; evidence vượt dung lượng; từ cấm không có evidence cạnh nó.
   Chạy bằng `python3 -I`. Phải có ca xanh tương ứng (không báo đỏ oan).
**Xong khi:** bản mutation lặp lại thí nghiệm của reviewer (đổi `5.495 ± 0.649` → `9.999 ± 0.649`) cho `exit ≠ 0`, và test của công cụ xanh.

## GIAI ĐOẠN 2 — Chuẩn hoá evidence RSS (90 MB, chưa commit)
1. Không commit thẳng 90 MB. Phân loại: giữ trong repo (≤ 10 MB) log, CSV, `pprof_diff` text, và `memstats` ĐÃ nén/trích đoạn dùng cho các số được trích dẫn trong báo cáo;
   `heap_*.pb.gz`, `smaps_*`, bản đầy đủ => nén thành `rss_raw_full.tar.zst` (hoặc tar.gz) đặt ở NGOÀI git (ví dụ `/home/abc/evidence_archive/perf_rss_20261007/`) và ghi vào manifest mục `"external"` với `sha256`, `bytes`, `stored_at`, `reason`.
   Báo cáo phải nói rõ "bản đầy đủ không nằm trong git; kiểm chứng bằng sha256".
2. Mọi số trong báo cáo lấy từ memstats/pprof (ví dụ `ethHashMapBlsHashMap.Store` 5.98 → 47.81 MB, `txHashToBlockNumberMap.Store` 0 → 35.06 MB, `pebble.(*Batch).grow` +4 MB) phải có extractor đọc từ file nằm TRONG repo
   (trích đoạn `pprof -top`/`-base` đã lưu dạng text), không phụ thuộc file ngoài. Nếu không trích được => bỏ số đó khỏi báo cáo hoặc ghi "chưa kiểm chứng từ repo".
3. Thêm `"report": "note/perf_rss_investigation_20261007.md"` vào manifest RSS; cho `verify_evidence.py --strict` chạy qua.
**Xong khi:** thư mục evidence RSS ≤ 10 MB, mọi file nằm trong manifest (hoặc `external` có sha256), báo cáo RSS được công cụ kiểm tra và exit 0.

## GIAI ĐOẠN 3 — Điều khiển âm đúng lỗi + dọn crash test
1. Chạy và LƯU LOG điều khiển âm `NEGATIVE_CONTROL=sync_timeout` (ép đồng bộ thất bại): script PHẢI báo `FAIL_SYNC_TIMEOUT`/INCONCLUSIVE và exit ≠ 0, KHÔNG được in PASS và không được so parity ở block 0.
   Thêm vào manifest (`durability_negative_control_sync_timeout`). Chạy 1 lần cả `mismatch_root` cũ để giữ.
2. Thêm điều khiển âm "thật hơn" cho parity: bơm sai lệch vào HỆ THỐNG (không sửa chuỗi trong script), ví dụ gửi tx trực tiếp vào đúng 1 node qua đường không qua consensus ở scratchpad, hoặc ghi rõ "không thực hiện được" => INCONCLUSIVE cho khả năng phát hiện fork thật. Không được khẳng định "đã chứng minh phát hiện fork thật" nếu chỉ có điều khiển âm trong script.
3. Sửa lỗi nhỏ: "7 vòng giết leader" ↔ danh sách 6 vòng (R3, R6, R11, R14, R17, R20) và nêu riêng 5 vòng kill kép có val0 (R8, R18) — để công cụ ở Giai đoạn 1 bắt được kiểu lỗi này.
4. Giải thích hoặc bỏ phần "fsync A=30 lần vs B=42 lần" trong báo cáo B1: B hơn A 12 lần cho cùng bench — đây là khác biệt thật cần nêu nguyên nhân (đọc diff vendor/NOMT giữa `ec6560ce` và HEAD) hoặc ghi "chưa giải thích được".
   Câu "fsync trên các tệp `wal, bbn, ln, ht, meta`" ở B1 chỉ được giữ nếu có strace `-y` cho CẢ A và B trong evidence B1; nếu không thì trích dẫn nhãn "từ perf_nomt_fsync_evidence_20261007.md, chưa chạy lại cho A".
**Xong khi:** có log điều khiển âm `sync_timeout` trong evidence, báo cáo durability/B1 khớp công cụ đã vá.

## GIAI ĐOẠN 4 — Đo lại `GOGC` có kiểm soát và đo ảnh hưởng `-debug=true`
### 4.1 GOGC (800 vs 50)
- **Tiêu chí ghi trước:** ví dụ "Kết luận có ý nghĩa khi khoảng tin cậy 95% của hiệu TPS không chứa 0 với n ≥ 7 mỗi nhánh; RSS so ở cùng trạng thái ban đầu".
- **Thiết kế:** mỗi lượt = khởi động cluster MỚI (genesis mới, cùng kích thước state) với biến môi trường tương ứng, chạy ĐÚNG 1 workload 25.000 tx (batch 1.000, cùng `secp_tps_blast` và cờ), rồi dừng. Xen kẽ A/B/A/B… (≥ 7 lượt mỗi nhánh); ghi TPS, RSS đỉnh theo PID env test, `go_memstats`.
  Tuyệt đối không chạy liên tiếp cùng cấu hình trên cluster đã tích luỹ state.
- Báo cáo: trung bình ± SD, phép thử Welch (công thức + df), kết luận 3 trạng thái. Nếu không phân biệt được thì viết "không phát hiện chênh lệch TPS có ý nghĩa (n=…)", KHÔNG ghi "đánh đổi 7.3%".
- Xoá câu "đã được chứng minh hiệu quả cao"; các dòng ma trận khuyến nghị chưa đo ghi rõ "SUY LUẬN, CHƯA ĐO" (giữ nhãn hiện có) hoặc bỏ.
### 4.2 `-debug=true` / `ENABLE_DEBUG_PPROF`
- Đọc `execution/cmd/simple_chain/main.go` để biết `-debug` làm gì (trích dòng). Đo ≥ 7 lượt xen kẽ bật/tắt (workload 25k tx, cluster mới mỗi lượt): TPS, CPU, kích thước log.
- Nếu ảnh hưởng ≥ ngưỡng ghi trước (ví dụ ≥ 3% TPS có ý nghĩa) hoặc không có lý do chính đáng: đổi mặc định của `ENABLE_DEBUG_PPROF` thành `false` trong `run_env.sh` (pprof riêng vẫn bật được bằng `--pprof-addr` mà không cần `-debug`); cập nhật PROJECT_STRUCTURE.md nếu cần. Ghi chú trong MỌI báo cáo trước đó: "đo khi `-debug=true` bật".
**Xong khi:** hai thí nghiệm có manifest, bảng sinh từ log, công cụ exit 0.

## GIAI ĐOẠN 5 — Phát hiện kỹ thuật thật: index trong RAM không giới hạn (tx_hash→block, Eth→BLS)
Dữ liệu (từ evidence RSS đã có, sẽ được chuẩn hoá ở Giai đoạn 2): `HeapAlloc` sau GC tăng ~99 MB / 100k tx; hai mục tăng rõ nhất là
`pkg/blockchain.(*ethHashMapBlsHashMap).Store` và `(*txHashToBlockNumberMap).Store`. Với hàng triệu tx, RAM sẽ phình tuyến tính.
1. **Phân tích (chưa sửa):** đọc code hai cấu trúc; trả lời có bằng chứng: kích thước/entry, ai ghi/đọc, có xoá khi nào, có persist ở DB không, dữ liệu có thể tái dựng từ DB không,
   và ảnh hưởng tới TÍNH XÁC ĐỊNH (nếu eviction thay đổi kết quả đọc thì có fork/khác receipt giữa các node không). Viết `note/design_bounded_memory_indexes_20261007.md` gồm phương án (LRU có giới hạn + fallback đọc DB; hoặc chuyển hẳn xuống DB), rủi ro, và cách kiểm thử.
2. **KHÔNG tự sửa nếu đường đọc ảnh hưởng thực thi/consensus** (hash→block dùng cho dedup tx hay kiểm tra "đã commit"? Eth→BLS dùng trong xác minh chữ ký?): trong trường hợp đó DỪNG và gửi thiết kế cho user duyệt. Nếu chỉ phục vụ RPC (tra cứu receipt/tx) thì có thể sửa nhỏ nhất có giới hạn bộ nhớ rõ ràng, có test race, và benchmark trước/sau.
3. Mọi cache/queue mới phải có giới hạn bộ nhớ tường minh (AGENTS.md Part 2).
**Xong khi:** có tài liệu thiết kế có số liệu + quyết định (tự sửa an toàn hoặc chờ user duyệt); không có thay đổi nào lén ảnh hưởng thực thi.

## GIAI ĐOẠN 6 — Tổng kết, tự kiểm chéo, commit
1. Viết lại các báo cáo bị ảnh hưởng theo mẫu: Tiêu chí (ghi trước) → Phương pháp (lệnh y nguyên) → Kết quả (bảng có `evidence:<id>` + extractor) → Giới hạn → Chưa làm → Thay đổi so với bản trước.
2. Chạy `python3 -I execution/scripts/test/evidence/verify_evidence.py --strict note/evidence/<từng-thư-mục>`; tất cả exit 0. Chạy lại mutation: sửa 1 số trong bản sao báo cáo => exit ≠ 0 (lưu log).
3. Tự kiểm chéo (mục 0.7): chạy lại ≥ 1 vòng crash test, 1 lượt benchmark B1, 1 lượt GOGC; ghi đối chiếu so với số đã báo cáo.
4. Commit theo giai đoạn (mỗi giai đoạn ≥ 1 commit, theo TÊN FILE). Không push.
5. Báo cáo cuối cho user: danh sách commit; mỗi hạng mục: lệnh + đường dẫn evidence + trạng thái PASS/FAIL/INCONCLUSIVE; việc chưa làm; rủi ro còn lại; những gì KHÔNG chạy lại được và vì sao.

---

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev trong repo/lịch sử git; thu hồi token GitHub; VM/máy riêng cho power-loss thật và B2; mọi lệnh `sudo`/đụng thiết bị khối trên máy của user nếu chưa có văn bản cho phép;
cutover chain 991; bật pipeline commit Rust→Go; thay đổi bộ nhớ index ảnh hưởng thực thi/consensus khi chưa được duyệt; push/merge/đóng issue.

## Tiêu chí hoàn thành toàn kế hoạch
- Mutation test (đổi 1 số trong báo cáo) cho `verify_evidence.py` exit ≠ 0 và có log chứng minh.
- Mọi thư mục evidence: ≤ 10 MB trong git, mọi file trong manifest hoặc `external` có sha256, có trường `report`, `--strict` exit 0.
- Có log điều khiển âm `sync_timeout`; không còn khẳng định "chứng minh phát hiện fork thật" nếu chưa có điều khiển âm bơm vào hệ thống.
- So sánh GOGC và `-debug` được đo xen kẽ trên cluster mới mỗi lượt, kết luận 3 trạng thái, không còn so sánh khác điều kiện.
- Có tài liệu thiết kế giới hạn index bộ nhớ và quyết định rõ (không tự đổi đường ảnh hưởng consensus).
- Báo cáo cuối nêu rõ mọi mục INCONCLUSIVE thay vì cố làm cho "xanh".
