# Kế hoạch giai đoạn 4 cho Gemini — truy tìm nguyên nhân treo NOMT/Block-STM (ưu tiên số 1) và chốt số đo

> Ngày: 2026-10-11 · Đọc trước (áp dụng nguyên văn): `note/plan_gemini_verify_20261011.md` (luật chống pass giả), `note/plan_gemini_phase2_20261011.md` §0–§1 (quy tắc, build, cạm bẫy), `note/plan_gemini_phase3_20261011.md` §9 (các lỗi lập luận cần tránh).
> HEAD khi giao: kiểm tra `git log --oneline -1` (tối thiểu `f70dcf3e`).
> Giai đoạn 2–3 đã xác nhận độc lập: Raft +19.1%, BFT không đổi, fix fetcher có hiệu quả (BASE lỗi 3/6 ở cap 16 MiB và 1/6 ở cap thật; HEAD 0 lần chữ ký lỗi đó). **Rủi ro lớn nhất còn lại là một validator treo vĩnh viễn** (chữ ký `SIG_2`: `WATCHDOG-EVM … ProcessTransactionsOptimistic`), ví dụ `b_orig_head_3`: val0 kẹt ở block #26, chỉ 270,000/4,773,567 tx lên chain. Giai đoạn này **chỉ** tập trung vào lỗi đó và sửa phần báo cáo bị che.

---

## 0. Luật bổ sung cho giai đoạn này

1. **Mọi lập luận về mã phải trích dẫn nguyên văn `file:dòng`** (copy từ `sed -n` / `grep -n`, không diễn giải lại). Giai đoạn 2 bị trả lại vì trích sai `IoHandle` và đưa ra giả thuyết tự mâu thuẫn với mã. Trước khi đưa một giả thuyết vào báo cáo, **viết ra chuỗi sự kiện từng bước và đối chiếu từng bước với dòng mã**; nếu một bước mâu thuẫn mã → bỏ giả thuyết.
2. **Không thêm kết luận suy đoán vào phần "Nguyên nhân".** Chỉ ba nhãn được phép: `ĐÃ CHỨNG MINH` (có số đo/log trực tiếp), `LOẠI TRỪ` (có thí nghiệm hoặc dòng mã chứng minh không thể xảy ra), `GIẢ THUYẾT` (ghi rõ thí nghiệm nào sẽ kiểm).
3. **Không sửa mã sản phẩm trong cây làm việc chính.** Log chẩn đoán đặt trong **bản copy tạm** (worktree riêng `git worktree add /tmp/gemini_verify/wt_diag HEAD`). Không commit. Không đụng `git checkout/stash/reset` lên file của người khác.
4. **Không dùng timeout/sleep để "thoát" vòng chờ** trong bất kỳ bản sửa nào được đề xuất (Zero-Fork, `AGENTS.md` Part 2.5). Chỉ đề xuất sửa nếu có nguyên nhân đã chứng minh.
5. Lệnh cần quyền root (`gdb`, `dmesg`): **kế hoạch này được thiết kế để không phụ thuộc vào chúng**. Chỉ xin chủ dự án chạy khi bộ đếm trong tiến trình không đủ để kết luận, và nêu rõ PID + lệnh.
6. Mọi số liệu do chính bạn đo trong phiên, có `uptime`, timestamp, sha256 binary. Không dùng số của người khác làm bằng chứng.

---

## 1. Việc N1 — Dựng bản chẩn đoán (làm trước, ~1 giờ)

Hiện tượng đã biết (đã xác nhận 2 lần bằng gdb + goroutine dump, `note/evidence/plan_gemini_verify_20261011/nomt_hang_evidence/`): thread commit kẹt ở `nomt_commit_payload → Store::commit → bitbox::SyncController::post_meta → writeout::write_ht`, đang chờ `io_handle.recv()`; 9 thread `io-worker` rảnh ở `command_rx.recv()` (theo `run_worker`, nghĩa là `pending` rỗng); 110 thread đọc kẹt ở `Nomt::read → RawRwLock::lock_shared_slow`.

Trong worktree `wt_diag`, thêm log chẩn đoán **không đổi hành vi**:

**(a) `consensus/vendor/nomt/nomt/src/bitbox/writeout.rs::write_ht`** — đếm `sent`, đếm số completion nhận được, đếm số completion có `result` lỗi; bắt đầu/kết thúc kèm timestamp. Để phát hiện treo **mà không cần gdb**, tạo một **thread giám sát tách biệt** (chỉ đọc bộ đếm atomic, không can thiệp vòng lặp chờ) in cảnh báo mỗi 30 giây khi một `write_ht` chưa xong: `sent`, `received`, thời gian đã chờ, `Arc::strong_count` của handle nếu truy cập được.

**(b) `consensus/vendor/nomt/nomt/src/io/linux.rs::run_worker`** — mỗi worker có id; ghi bộ đếm atomic toàn cục: `packets_received`, `sqes_pushed`, `cqes_reaped`, `completions_sent_ok`, `completions_sent_err` (kết quả của `completion_sender.send`, hiện bị bỏ qua bằng `let _ =`), `retries_pushed`. Log cùng thread giám sát ở (a) khi phát hiện treo: in toàn bộ bộ đếm.

**(c) `consensus/vendor/nomt/nomt/src/io/mod.rs::IoHandle::send`** — đếm lần `Weak::upgrade` thất bại và lần `sender.send` trả `Err` (hiện bị `unwrap()` ở `write_ht`).

**(d) In-process thread dump không cần gdb:** trong thread giám sát, khi phát hiện treo, ghi `/proc/self/task/*/{comm,stat,wchan}` vào file trong `meas_out/`, đồng thời gọi `backtrace`/`std::backtrace::Backtrace::force_capture()` **chỉ trên thread giám sát và, nếu khả thi, trên thread đang chờ** (nếu không khả thi, nêu rõ lý do — đừng giả vờ có stack của thread khác).

Sau khi build: **xác nhận bản chẩn đoán thực sự chạy** (`strings simple_chain_DIAG | grep -c "<chuỗi log mới>"` ≥ 1) và kiểm tra nó **không làm đổi hành vi**: chạy 2 lần Raft + 1 lần BFT ngắn, so sánh trạng thái (`zero_fork`, `roots`) và `complete`.

Nhớ cạm bẫy build: `cp -p target/release/libmetanode.a consensus/metanode/target/release/` rồi `touch execution/executor/ffi_bridge.go execution/pkg/nomt_ffi/bridge.go` trước `go build`.

---

## 2. Việc N2 — Tái hiện nhanh, không cần BFT 7 phút (chạy song song với N3 nếu có thể, không chiếm cùng cổng)

Một lần BFT bão hòa mất ~7 phút và treo chỉ ~12%. Hãy thử **tái hiện cô lập** để rút ngắn vòng lặp:

1. **Stress riêng cho tầng I/O của NOMT** (không cần cụm): tạo test/binary trong `wt_diag` (ví dụ `consensus/vendor/nomt/nomt/examples/` hoặc `tests/`) dựng một `IoPool` giống production (cùng số worker = 3, cùng `RING_CAPACITY`/`MAX_IN_FLIGHT` đọc từ `io/linux.rs`), rồi từ **nhiều thread đồng thời** mỗi thread dùng handle riêng: gửi hàng loạt `WriteArc`/`Write`/`Read` xen kẽ (kích thước giống `write_ht` thật: hàng nghìn trang mỗi lần) và kiểm đếm completion nhận == số gửi. Chạy hàng triệu lượt. **Đối chứng âm:** cố ý bỏ một completion trong bản test (mock) → test phải phát hiện.
2. **Torture test có sẵn:** `consensus/vendor/nomt/torture/` và `trickfs/` — đọc README, chạy cấu hình giống production nếu khả thi; báo kết quả (kể cả "không chạy được vì …").
3. Nếu stress không bắt được lỗi trong ≥10⁷ lượt: ghi rõ "KHÔNG TÁI HIỆN CÔ LẬP", đừng kết luận lỗi ở nơi khác.

---

## 3. Việc N3 — Vòng lặp BFT với bản chẩn đoán (bắt treo trong tự nhiên)

- Binary `simple_chain_DIAG` (cap thật 128 MiB, tool blast **gốc** — kiểm sha256 trước khi chạy). `e2e3.py bft` với `-duration 60 -batch 1000`.
- Dừng vòng lặp **ngay khi** (`complete=false` **và** log chứa chữ ký `SIG_2` hoặc cảnh báo "write_ht chờ >30s" từ thread giám sát). Giữ cụm sống 5 phút để thu thập: `kill -QUIT <pid>` (dump goroutine vào `execution.log`), `/proc/<pid>/task/*/{comm,stat,wchan}`, toàn bộ log chẩn đoán.
- Tối đa **25 lần**. Mỗi lần chạy thêm `uptime`. Nếu không bắt được: báo "KHÔNG TÁI HIỆN trong 25 lần" (xác suất 25 lần sạch ≈ 4% nếu tỷ lệ ~12%), kèm bộ đếm từ các lần **không treo** (để thấy `sent == received` luôn đúng ở trạng thái bình thường — đây là đường cơ sở hữu ích).
- Khi bắt được, bảng bắt buộc: `sent`, `received`, `completions_sent_ok/err`, `sqes_pushed`, `cqes_reaped`, `packets_received`, `retries_pushed`, `Weak::upgrade` thất bại, `send` thất bại; thread nào còn sống; có panic không.

### Cây quyết định để diễn giải (dùng để suy luận, không phải kết luận sẵn)
| Quan sát khi treo | Hàm ý | Thí nghiệm tiếp |
| :-- | :-- | :-- |
| `packets_received < sent` | Lệnh bị mất giữa `IoHandle::send` và worker (kênh/`Weak`) | kiểm kênh unbounded, nhiều pool, thứ tự drop |
| `packets_received == sent`, `cqes_reaped < sqes_pushed` | Kernel không trả CQE (khác với mô hình "worker ngủ khi pending rỗng") | `io_uring` kernel/flag `SINGLE_ISSUER`, thread khác submit? |
| `cqes_reaped == sqes_pushed`, `completions_sent_ok < cqes_reaped` | Completion bị bỏ qua trong worker (nhánh `continue`/Retry) | đọc kỹ nhánh `pending.get(...).is_none()` và `Retry` |
| `completions_sent_err > 0` | Receiver đã bị drop sớm (handle bị thay/drop) | ai drop `completion_receiver` |
| `completions_sent_ok == sent` nhưng `received < sent` | Completion đã vào kênh nhưng không tới thread chờ (kênh khác/consumer khác) | ai gọi `recv`/`try_recv`/`receiver()` trên cùng receiver |
| Tất cả bằng nhau mà vẫn kẹt | Không phải mất completion; nhìn lại thread nào thực sự đang kẹt ở đâu | gdb (xin chủ dự án) |

---

## 4. Việc N4 — Vấn đề phụ cần phân tích (không sửa)

1. **Rò goroutine khi NOMT kẹt:** dump goroutine cho thấy ~110 goroutine kẹt trong `nomt_ffi.state_db_get` do `main.NewApp.func3` (`execution/cmd/simple_chain/app.go` quanh dòng 464–469, gọi `VerifyNodeCommitteeKeyLight`) sinh ra mỗi lần kiểm tra và không có giới hạn. Báo cáo: tần suất sinh, tại sao không giới hạn, và đề xuất giới hạn đồng thời (AGENTS.md: *Bounded Concurrency*). **Chỉ phân tích và đề xuất**, không sửa.
2. **Watchdog hiện chỉ in cảnh báo.** Đề xuất (không làm) cách phát hiện treo NOMT tự động mà **không dùng timeout để quyết định dispatch** (ví dụ: chỉ cảnh báo/kêu gọi hạ validator khỏi tập đề xuất, dựa trên dữ liệu ngang hàng, đúng tinh thần Peer Attestation).

---

## 5. Việc N5 — Sửa `summarize.py` để không che thất bại (bắt buộc, rẻ)

`note/evidence/gemini_phase2_20261011/summarize.py` hiện loại các lần `complete=false` khỏi trung vị rồi in "PASS", nên che mất `b_orig_head_3` (270,000/4,773,567 tx) và `s16_orig_head_3`. Sửa để mỗi bảng thống kê in thêm:
- `Hoàn tất: k/n` cho mỗi bản, và **tỷ lệ không hoàn tất**;
- danh sách lần không hoàn tất kèm chữ ký phân loại và `on_chain/submitted`;
- dòng kết luận chỉ là "PASS" khi **cả** trung vị đạt **và** tỷ lệ hoàn tất của HEAD không thấp hơn BASE; nếu không → "KHÔNG KẾT LUẬN (xem lần không hoàn tất)".
Sau đó chạy lại trên dữ liệu có sẵn và cập nhật phần tóm tắt của `note/gemini_phase2_report_20261011.md` cho khớp (mục "Tổng quan"): nêu rõ HEAD có 1 lần treo `SIG_2` ở cap thật và BASE có 1 lần `SIG_1` ở cap thật.

---

## 6. Việc N6 — Soak cap thật (chỉ khi N1–N3 đã xong hoặc đã đạt giới hạn)

Binary `HEAD` sạch (không DIAG): **≥20 lần** BFT cap thật xen kẽ `BASE` ≥10 lần, tool gốc. Phân loại mọi lần không hoàn tất theo bảng chữ ký (`plan_gemini_phase2_20261011.md` §4.1). Hồi quy nghiêm trọng nếu HEAD có `SIG_1`. Báo cáo tỷ lệ `SIG_2` của HEAD vs BASE với khoảng tin cậy (nói rõ cỡ mẫu nhỏ nên khoảng rộng).

---

## 7. Sản phẩm bàn giao

1. `note/gemini_phase4_report_20261011.md` với các mục: (1) môi trường, sha256 mọi binary; (2) N1: patch chẩn đoán (đính kèm file `.patch` trong `note/evidence/gemini_phase4_20261011/`), bằng chứng bản chẩn đoán không đổi hành vi; (3) N2: kết quả stress cô lập + đối chứng âm; (4) N3: bảng bộ đếm ở mọi lần chạy (kể cả không treo) và, nếu bắt được, bảng treo + diễn giải theo cây quyết định, mỗi kết luận có nhãn `ĐÃ CHỨNG MINH/LOẠI TRỪ/GIẢ THUYẾT`; (5) N4 phân tích; (6) N5 bảng đã sửa; (7) N6; (8) **Lần chạy xấu**, **Chưa kiểm chứng/giới hạn**, danh sách file thô + sha256.
2. Dữ liệu thô trong `note/evidence/gemini_phase4_20261011/raw/` và một `summarize.py` in toàn bộ bảng (không gõ tay số vào báo cáo).
3. **Không commit.** Trước khi báo xong: `git status --short` chỉ thấy file báo cáo/evidence/script của bạn; không có file mã sản phẩm bị sửa trong cây chính; `cd consensus/metanode/scripts && ./build_check.sh` 5/5.

## 8. Thứ tự khi thiếu thời gian
N5 (30 phút, độc lập) → N1 → N3 (chạy nền) song song N2 → N4 → N6. Nếu phải bỏ, bỏ N6 trước, ghi "KHÔNG LÀM ĐƯỢC".

## 9. Tiêu chí chấp nhận (Claude sẽ kiểm)
- Mọi trích dẫn mã khớp `sed -n` tại dòng ghi; không có bước nào trong chuỗi suy luận mâu thuẫn mã.
- Bộ đếm chẩn đoán xuất hiện trong binary (`strings`) và trong log các lần chạy.
- Claude sẽ tự chạy lại `summarize.py` và 2–3 test (`cargo test … commit_syncer`, `go test … TestSortEquivalence`), đối chiếu sha256, và kiểm `git status`.
