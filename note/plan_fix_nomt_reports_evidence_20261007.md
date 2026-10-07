# Kế hoạch: sửa test/báo cáo NOMT với BẰNG CHỨNG CÓ THỂ KIỂM CHỨNG — 2026-10-07

Dành cho agent thực hiện. Nền: `origin/dev` tại `c081e31a` trở đi.
Đọc trước: `AGENTS.md`, `PROJECT_STRUCTURE.md`, `note/plan_production_launch_20261006.md` (mục 0),
`note/perf_nomt_commit_20261007.md`, `note/perf_nomt_fsync_evidence_20261007.md`, `note/perf_nomt_b1_20261007.md`,
`note/perf_nomt_durability_20261007.md`, `note/perf_rss_investigation_20261007.md`,
`execution/scripts/test/test_crash_recovery_nomt_expanded.sh`, `execution/scripts/test/gate_e2e/run_env.sh`.

> **Bối cảnh bắt buộc phải hiểu:** dự án này ĐÃ NHIỀU LẦN có báo cáo/checkbox "PASS" không có bằng chứng thật
> (bộ audit giả, vendor bị tắt fsync không báo, test PASS ở block 0, "so sánh" khác điều kiện). Mục tiêu của kế hoạch
> này không chỉ là sửa số liệu mà là làm cho MỌI con số trong báo cáo CÓ THỂ BỊ NGƯỜI KHÁC KIỂM TRA LẠI.
> Một báo cáo ghi "chưa đo / không kết luận được" là THÀNH CÔNG; một báo cáo đẹp nhưng không kiểm chứng được là THẤT BẠI.

---

## 0. Quy tắc CHỐNG BÁO CÁO GIẢ (tuyệt đối bắt buộc, vi phạm = làm lại)

### 0.1 Quy tắc về bằng chứng
1. **Không có file thô => không có con số.** Mỗi số liệu, mỗi dòng "PASS", mỗi trích dẫn log trong báo cáo phải trỏ tới
   một file thô đã commit (hoặc có sha256 trong manifest, xem 0.2) do CHÍNH lệnh được ghi trong báo cáo sinh ra.
   Không được gõ tay, không làm tròn, không "dựng lại" số từ trí nhớ hay từ báo cáo cũ.
2. **Cấm bịa/ngoại suy.** Không tự điền số cho lượt chưa chạy; không suy ra "chắc là"; không copy bảng của báo cáo trước
   rồi sửa vài ô. Nếu một lượt chạy lỗi/bị gián đoạn, GHI ĐÚNG như vậy và giữ nguyên log lỗi.
3. **Mọi kết luận phải có ngưỡng chấp nhận viết TRƯỚC khi chạy** (ghi vào báo cáo ở mục "Tiêu chí" trước phần kết quả).
   Không được chỉnh ngưỡng sau khi thấy kết quả.
4. **Ba trạng thái hợp lệ:** `PASS` / `FAIL` / `INCONCLUSIVE` (không kết luận được). Mặc định khi thiếu dữ liệu là
   `INCONCLUSIVE`, không phải `PASS`. Một test chạy hết nhưng không chứng minh được điều nó tuyên bố => `INCONCLUSIVE`.
5. **Mọi kiểm thử "PASS" phải có điều khiển âm (negative control):** chứng minh test CÓ THỂ ĐỎ. Ví dụ: cố ý gây lỗi
   (ở scratchpad, không commit) và cho thấy test đỏ. Test không bao giờ đỏ được thì không có giá trị.
6. **Không so sánh khác điều kiện.** Hai con số chỉ được đặt cạnh nhau nếu cùng workload, cùng kích thước, cùng cờ,
   cùng máy. Nếu khác, ghi rõ là KHÔNG so sánh được thay vì kèm phần trăm.
7. **Không nhân danh agent/người khác.** Không ghi "đã kiểm chứng" cho việc bạn chưa tự chạy lại. Khi trích kết quả
   của phiên trước phải gắn nhãn "trích từ <commit/file>, CHƯA chạy lại".
8. **Báo cáo phải nêu rõ cái KHÔNG chứng minh được** (mục "Giới hạn") và cái CHƯA làm (mục "Chưa làm").

### 0.2 Cơ chế bắt buộc: Evidence Manifest
Với mỗi báo cáo, tạo thư mục `note/evidence/<tên-báo-cáo>/` chứa:
- `MANIFEST.json`: danh sách mục `{id, command, cwd, env, git_commit, started_at, finished_at, exit_code, files:[{path, sha256, bytes}]}`.
  `git_commit` = `git rev-parse HEAD` tại thời điểm chạy VÀ `git status --porcelain` rỗng (hoặc ghi rõ file đang sửa dở).
- Log/output thô của từng lệnh (stdout+stderr gốc, KHÔNG sửa). File quá lớn (>2 MB): lưu bản nén `.gz` HOẶC trích đoạn
  (kèm `head/tail` và số dòng gốc) + sha256 của bản đầy đủ; ghi rõ bản đầy đủ nằm ở đâu và vì sao không commit.
- Tổng dung lượng evidence mỗi báo cáo ≤ 10 MB.
- Số liệu trong báo cáo được sinh bởi script (ví dụ `scripts/test/evidence/summarize_*.py`) ĐỌC TỪ file thô, không gõ tay.

### 0.3 Công cụ kiểm tra tự động (tạo ở Giai đoạn 0, chạy ở cuối mỗi giai đoạn)
Tạo `execution/scripts/test/evidence/verify_evidence.py` (Python, chạy bằng `python3 -I`) làm các việc sau; thất bại => exit ≠ 0:
1. Duyệt `MANIFEST.json`: mọi file tồn tại, sha256/bytes khớp.
2. Mọi `git_commit` trong manifest tồn tại trong repo (`git cat-file -e`).
3. Quét báo cáo `.md` tương ứng: mọi dòng bảng chứa số liệu hiệu năng phải có cột/ghi chú `evidence:<id>` tồn tại trong manifest;
   báo cáo không được chứa các cụm cấm khi không có evidence: "100%", "hoàn toàn", "tuyệt đối", "không hề", "chứng minh 100%"
   (chỉ cho phép nếu ngay cạnh có `evidence:<id>` và phạm vi đo cụ thể).
4. Tính lại trung bình/độ lệch chuẩn từ file thô và so với con số trong báo cáo (sai số cho phép 0.5%); lệch => FAIL.
5. Với test PASS/FAIL: đếm số vòng trong log thô == số vòng trong bảng báo cáo; số dòng PASS khớp.
Script này phải có test riêng (`verify_evidence_test.py`) chứng minh nó bắt được: file thiếu, sha sai, số bị sửa tay, cụm cấm.

### 0.4 Quy tắc vận hành (như các kế hoạch trước)
1. **Zero-fork (AGENTS.md 2.5):** không dùng timeout/sleep để quyết định dispatch commit; thiếu bằng chứng => PENDING.
2. **Cô lập:** chỉ cluster local cổng 31xxx trong scratchpad. KHÔNG đụng cluster 231/230, `/opt/metanode`, tiến trình của user
   (máy có ~9 tiến trình `/opt/metanode`; mọi đo RSS/CPU theo PID của env test, KHÔNG dùng `ps -C simple_chain`). Dừng env bằng
   `run_env.sh <BASE> stop` (theo PID), không `pkill` theo mẫu.
3. **Không push, không merge, không đóng issue, không sửa `portal/`, không đụng mật khẩu/token.** Commit local theo TÊN FILE
   (không `git add <dir>`; `*.py` bị gitignore => `git add -f`). Cuối commit: `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
4. **Tuyệt đối không tắt fsync NOMT**, không xoá `fsync/sync_all/sync_data` trong `consensus/vendor/nomt/**`. Mọi thí nghiệm
   thay đổi vendor phải ở scratchpad và PHẢI hoàn nguyên + build lại + chứng minh bằng lệnh (xem 1.4) trước khi kết thúc.
5. Sau mỗi thay đổi code: `gofmt`, `go vet`, `go test -race` cho package đã sửa, `cd consensus/metanode/scripts && ./build_check.sh` sạch.
6. **DỪNG và hỏi user** nếu: cần đổi đường commit/consensus; cần `sudo`/thiết bị khối/máy ngoài; kết quả buộc đánh đổi độ bền vs hiệu năng;
   phát hiện lỗ hổng nghiêm trọng (báo ngay, không công bố chi tiết exploit).
7. Code comment tiếng Anh. Cuối mỗi phản hồi có khối "📋 Tóm tắt thay đổi" tiếng Việt (AGENTS.md Phần 5).

### 0.5 Kiểm tra chéo bắt buộc ở cuối (self-audit)
Trước khi báo cáo "xong", agent PHẢI chạy lại ĐỘC LẬP ít nhất 1 lượt cho mỗi hạng mục quan trọng bằng đúng lệnh trong manifest
(ví dụ 1 vòng crash test, 1 lượt benchmark) và so với số đã ghi; ghi kết quả đối chiếu. Sau đó đề nghị user (hoặc agent thứ hai)
chạy `python3 -I execution/scripts/test/evidence/verify_evidence.py note/evidence/<tên>` — báo cáo chỉ được coi là hoàn tất khi lệnh này exit 0.

---

## GIAI ĐOẠN 0 — Dựng công cụ bằng chứng (làm đầu tiên, nhỏ)
1. Viết `verify_evidence.py` + `verify_evidence_test.py` theo 0.3; thêm helper `execution/scripts/test/evidence/run_logged.sh <id> -- <lệnh…>`
   (chạy lệnh, lưu stdout/stderr thô, ghi exit code, thời gian, `git rev-parse HEAD`, sha256 vào manifest).
2. Tests: tạo manifest hợp lệ => exit 0; sửa 1 byte file thô => exit ≠ 0; sửa số trong báo cáo => exit ≠ 0; thiếu `evidence:` => exit ≠ 0.
**Xong khi:** test của công cụ xanh và đã chứng minh nó đỏ khi bị bẻ.

## GIAI ĐOẠN 1 — Sửa script crash test (lỗi đã xác nhận)
Lỗi (đã đọc code): `test_crash_recovery_nomt_expanded.sh` ~dòng 175–200: nếu vòng chờ 40×0.5s không thỏa điều kiện thì
`SYNC_PASS=false`, `LAST_BLOCK=0`, nhưng `SYNC_PASS` KHÔNG được kiểm tra; script so parity ở block 0 (genesis, luôn khớp) rồi in PASS.
Báo cáo cũ có vòng R20 PASS ở block #0 — kết quả đó là KHÔNG CÓ Ý NGHĨA. Ngoài ra block được so sánh nhiều vòng liền nhau giống nhau
(R3–R4 #7, R11–R12 #18, R15–R16 #21, R17–R19 #24): không chứng minh node phục hồi đã commit block MỚI.

Việc làm:
1. Trước mỗi vòng ghi `H_before` = chiều cao block nhỏ nhất của các node còn sống. Sau khi restart, PASS chỉ khi:
   (a) `SYNC_PASS=true` thật; (b) mọi node đạt chiều cao ≥ `H_before + N` (N khai báo, ví dụ 2) tức là đã commit block MỚI sau crash;
   (c) block hash + state root khớp trên MỌI node ở ≥ 2 block khác nhau, trong đó có block mới; (d) exit code/sentinel như cũ.
   Nếu (a) không đạt => `FAIL`/`INCONCLUSIVE` với lý do; KHÔNG so parity ở block 0.
2. Với kịch bản "2 node chết cùng lúc" (cụm n=4 mất quorum): ghi chiều cao theo thời gian trong lúc chết (phải đứng yên) và sau khi sống lại
   (phải tiến lên) — đó là bằng chứng liveness/không fork; nếu không đo được => `INCONCLUSIVE`.
3. Ghi lại thời điểm kill so với pha commit (ví dụ lấy mốc từ log `[NOMT-COMMIT-PERF]`/trace trước và sau lúc kill) để biết kill có rơi vào lúc commit hay không;
   nếu không xác định được thì ghi "không xác định", KHÔNG khẳng định "kill trúng commit".
4. **Điều khiển âm (bắt buộc):** chạy script ở scratchpad với một lỗi cố ý (ví dụ cố ý sửa block hash ở 1 node bằng cách bơm tx chỉ vào 1 node không qua consensus
   hoặc đơn giản là giả lập `SYNC_PASS=false`) và chứng minh script báo FAIL/INCONCLUSIVE chứ không PASS. Lưu log vào evidence.
5. Chạy lại ≥ 20 vòng bằng `run_logged.sh`. Báo cáo dùng bảng sinh tự động từ log thô; vòng nào INCONCLUSIVE ghi đúng như vậy; KHÔNG chạy lại vòng cho đến khi "đẹp".
6. Đánh dấu rõ trong `note/perf_nomt_durability_20261007.md` rằng kết quả 20 vòng cũ (R20 ở block #0) bị thay thế/không có giá trị.
**Xong khi:** `verify_evidence.py` exit 0; bảng vòng khớp log; có điều khiển âm; báo cáo nêu rõ `kill -9` không kiểm chứng được fsync/power-loss.

## GIAI ĐOẠN 2 — B1: cấu hình A (đo thật hoặc ghi "chưa đo")
Quy tắc cần đánh giá: "B ≥ 90% A" (kế hoạch Giai đoạn 1.2 của `plan_production_launch_20261006.md`). Báo cáo cũ ghi PASS dù A KHÔNG đo được.
Việc làm:
1. Ghi ngưỡng trước (mục "Tiêu chí"): ví dụ B_commit_time ≤ A_commit_time × (1/0.9) với cùng bench/cùng khoá/cùng kích thước.
2. Đo A bằng microbenchmark commit NOMT ở commit `ec6560ce` (worktree riêng, `git worktree add <scratch>/A ec6560ce`; cần build lib NOMT tại đó)
   và HEAD, CÙNG tham số (`-bench`, `-benchtime`, `-count>=7`, cùng khoá/kích thước giá trị 100/1000/5000 key), xen kẽ A/B/A/B để giảm trôi nhiệt/nhiễu.
   Nếu không build/chạy được A: ghi `INCONCLUSIVE — cấu hình A chưa đo, lý do …` và KHÔNG đưa ra quyết định PASS cho B1.
3. Ghi lại cấu hình fsync của cả hai (dùng lại phương pháp strace/`/proc` của `perf_nomt_fsync_evidence_20261007.md`; chạy lại lệnh, không trích) để chắc
   A có fsync và B có fsync trong lúc đo.
4. Với e2e B vs C (đã có trong báo cáo cũ): nếu trích lại thì gắn nhãn "trích từ commit …, CHƯA chạy lại"; nếu chạy lại thì dùng `run_logged.sh` và báo cáo trung bình ± SD
   tính từ log. Nêu "chênh lệch 4.7% so với SD ~3%" bằng phép thử có công thức (ví dụ Welch t, nói rõ n=3 rất nhỏ nên không đủ kết luận).
5. Mục "Giải thích chênh lệch TPS giữa các báo cáo" (cũ): chỉ giữ giải thích nào có bằng chứng (diff cờ/tham số giữa hai lần chạy, từ manifest); phần suy đoán phải xoá hoặc ghi "giả thuyết, chưa kiểm chứng".
**Xong khi:** quyết định B1 có 3 trạng thái hợp lệ duy nhất (PASS/FAIL/INCONCLUSIVE) kèm dữ liệu A và B thô; nếu A không đo được thì kết luận là INCONCLUSIVE.

## GIAI ĐOẠN 3 — RSS: kết luận đúng với dữ liệu
Dữ liệu cũ: `HeapAlloc` sau GC trung bình khoảng 259 MB (50k tx) → 351 → 333 → 396 MB (200k tx): xu hướng tăng nhẹ, dao động lớn; 4 điểm không phân biệt được
cache đang đầy với rò rỉ rất chậm. Câu "100% không rò rỉ, đường cong hoàn toàn phẳng" KHÔNG được chứng minh.
Việc làm:
1. Ghi trước tiêu chí: ví dụ "live heap sau GC tăng không quá X MB/100k tx qua ≥ 8 đợt, hoặc `pprof -base` cho thấy không có hàm nào tăng bền vững".
2. Chạy ≥ 8 đợt tải liên tiếp (mỗi đợt ≥ 50k tx), sau MỖI đợt gọi `/debug/pprof/heap?gc=1` cho CẢ 4 node và lưu file `heap` thô (`.pb.gz`) + `/proc/<pid>/smaps_rollup` + `go_memstats_*`.
   Dùng `go tool pprof -base heap_1.pb.gz heap_8.pb.gz` để chỉ ra các hàm có `inuse_space` tăng; lưu output.
3. Phân tích hồi quy tuyến tính (script, đọc từ log) của `HeapAlloc` sau GC theo số tx tích luỹ; báo cáo hệ số góc + khoảng tin cậy. Kết luận chỉ ở mức "không phát hiện xu hướng tăng vượt ngưỡng trong phạm vi đo" hoặc "có xu hướng tăng".
4. GOGC: chạy GOGC mặc định và GOGC=50 với CÙNG workload (25.000 tx, cùng batch), mỗi cấu hình ≥ 3 lần xen kẽ, đo tx/s và RSS theo PID env test. Chỉ khi có cặp so sánh cùng điều kiện mới được ghi phần trăm.
   Xoá khỏi ma trận khuyến nghị mọi con số chưa từng đo (ví dụ "GOGC=100 → 6.200–6.800 tx/s", "Enterprise → 6.500–7.500+"); thay bằng "chưa đo".
5. Làm rõ cờ `-debug=true` ở `run_env.sh` (xem Giai đoạn 4): các số TPS/RSS đo khi cờ này bật phải được ghi rõ, và không so với số đo khi tắt.
**Xong khi:** báo cáo RSS chỉ khẳng định điều dữ liệu đỡ được, mỗi số có `evidence:<id>`, verify exit 0.

## GIAI ĐOẠN 4 — Cờ `-debug=true` trong `run_env.sh`
Commit `4f5846d2` thêm `-debug=true` cho `simple_chain` trong `execution/scripts/test/gate_e2e/run_env.sh` mà báo cáo không giải thích; cờ này đổi hành vi
(log chi tiết) của MỌI test dùng script.
1. Tìm ý nghĩa thật của cờ trong `execution/cmd/simple_chain/main.go` (đọc code, trích dòng).
2. Đo ảnh hưởng của nó: cùng workload 25k tx, ≥ 3 lần xen kẽ bật/tắt, đo tx/s + CPU; ghi kết quả thật.
3. Nếu không có lý do chính đáng (ví dụ cần cho pprof) thì gỡ khỏi `run_env.sh` mặc định (đưa thành biến môi trường tuỳ chọn); mọi báo cáo trước đây đo khi bật cờ phải thêm chú thích.
**Xong khi:** có quyết định có căn cứ và đã cập nhật script + chú thích báo cáo.

## GIAI ĐOẠN 5 — Báo cáo cuối, rà soát tự động, tự kiểm chéo
1. Viết lại các báo cáo bị ảnh hưởng (`perf_nomt_durability`, `perf_nomt_b1`, `perf_nomt_commit`, `perf_rss_investigation`) theo mẫu: Tiêu chí → Phương pháp (lệnh y nguyên) → Kết quả (bảng sinh từ log, có `evidence:<id>`) → Giới hạn → Chưa làm.
   Giữ lịch sử: thêm mục "Thay đổi so với bản trước" liệt kê khẳng định nào bị rút lại và vì sao.
2. Chạy `python3 -I execution/scripts/test/evidence/verify_evidence.py note/evidence/<từng-báo-cáo>`; tất cả exit 0.
3. Self-audit theo 0.5: chạy lại độc lập ≥ 1 lượt mỗi hạng mục, ghi đối chiếu.
4. Commit theo giai đoạn (mỗi giai đoạn ≥ 1 commit riêng, theo TÊN FILE).
5. Báo cáo cuối cho user liệt kê: commit; từng hạng mục: lệnh + đường dẫn evidence + trạng thái PASS/FAIL/INCONCLUSIVE; việc chưa làm; rủi ro còn lại.
   Nêu rõ những gì bạn KHÔNG chạy lại được và vì sao.

---

## KHÔNG làm (cần user)
Đổi mật khẩu sudo dev/thu hồi token GitHub; VM/máy riêng cho power-loss thật và B2; mọi lệnh `sudo`/đụng thiết bị khối trên máy của user nếu chưa có văn bản cho phép;
cutover chain 991; bật pipeline commit Rust→Go; push/merge/đóng issue.

## Tiêu chí hoàn thành toàn kế hoạch
- Tất cả báo cáo liên quan có thư mục `note/evidence/<tên>/` với `MANIFEST.json` và log thô.
- `verify_evidence.py` exit 0 trên từng thư mục; test của chính công cụ xanh.
- Không còn khẳng định tuyệt đối ("100%", "hoàn toàn") thiếu bằng chứng; không còn so sánh khác điều kiện; không còn kết luận PASS khi dữ liệu thiếu (A chưa đo, R20 ở block #0).
- Mỗi test PASS có điều khiển âm đã chạy và lưu log.
- Cuối cùng agent nêu rõ mục nào INCONCLUSIVE thay vì cố làm cho "xanh".
