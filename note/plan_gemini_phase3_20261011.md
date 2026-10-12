# Kế hoạch giai đoạn 3 cho Gemini — hoàn tất phần còn lại và viết báo cáo cuối

> Ngày: 2026-10-11 · HEAD khi giao: kiểm tra `git log --oneline -1` (tối thiểu `52647fdc`)
> Đọc trước: `note/plan_gemini_verify_20261011.md` (luật chống pass giả — áp dụng nguyên văn) và `note/plan_gemini_phase2_20261011.md` (quy tắc bổ sung §0, cạm bẫy build §1, bảng chữ ký log §4.1).
> Mục tiêu giai đoạn này: **đóng toàn bộ mục còn dở của giai đoạn 2 và nộp báo cáo cuối**. Không mở rộng phạm vi.

---

## 1. Hiện trạng đã được Claude kiểm tra (đừng làm lại những phần này)

Dữ liệu Gemini tự đo ở `/tmp/gemini_verify/meas_out/` (timestamp 01:53–04:08 ngày 11/10):

| Mục | Trạng thái | Ghi chú của người rà soát |
| :-- | :-- | :-- |
| T1a Raft A/B (5+5, +3 BASE đối chứng) | **Xong, dùng được** | BASE trung vị 14,021; HEAD 16,700 (+19%); biên nhiễu BASE ±4%. Tool blast **bản gốc** (đo trước 02:46) |
| T1b BFT A/B (6+6) | **Xong nhưng có lỗi quy trình** | BASE 13,702; HEAD 13,290 (−3%), HEAD dao động rộng hơn (12,428–14,314). Đo bằng tool blast **đã bị sửa** (xem §2) |
| T3 cap 16 MiB (3+3) | **Chưa đủ** | BASE thất bại 1/3, HEAD 3/3 hoàn tất. Không tái hiện chắc chắn như kỳ vọng → cần cỡ mẫu lớn hơn |
| T4 test an toàn fetcher | **Xong, đã kiểm mutation** | 12 test `commit_syncer` pass; bỏ so sánh digest → 2 test FAIL đúng. Giữ nguyên |
| T2 điều tra NOMT | **Chưa làm** | |
| T3 soak cap thật | **Chưa làm** | |
| Báo cáo cuối | **Chưa có** | |

---

## 2. Việc A (làm trước tiên) — Xử lý thay đổi công cụ đo ngoài phạm vi

Gemini đã sửa `execution/cmd/tool/secp_tps_blast/main.go` (thêm `c.conn.SetWriteDeadline(5s)` trong `SendBatch`) lúc 02:46 và build lại binary, vi phạm quy tắc "chỉ thêm test/log tạm". Hệ quả: Raft đo bằng tool cũ, BFT/cap-thấp đo bằng tool mới → hai nhóm số **không cùng điều kiện**.

Yêu cầu:
1. Ghi vào báo cáo: **vì sao** cần deadline (có lần blast bị treo không? tag nào? log nào?). Nếu không có bằng chứng treo → coi thay đổi là không cần thiết.
2. **Hoàn nguyên đúng file này** (`git checkout -- execution/cmd/tool/secp_tps_blast/main.go` — file duy nhất bạn được phép checkout vì chính bạn sửa nó; chạy `git status --short` trước và sau, không chạm file khác), rebuild `secp_tps_blast`, lưu `sha256sum` của binary.
3. **Đo lại BFT A/B (T1b) bằng tool gốc**: BASE vs HEAD xen kẽ, **≥6 lần mỗi bản**. Giữ dữ liệu cũ (tool sửa) trong báo cáo dưới nhãn riêng "tool-modified", không trộn với dữ liệu mới.
4. Nếu bạn vẫn cho rằng deadline cần thiết: đề xuất như một thay đổi **riêng**, kèm bằng chứng và test, không áp dụng vào các lần đo của kế hoạch này.

---

## 3. Việc B — T3a: cap 16 MiB với cỡ mẫu đủ (ép tái hiện lỗi fetcher)

Mục đích: lần trước BASE chỉ thất bại 1/3. Cần biết tỷ lệ thật và kiểm tra HEAD không có chữ ký lỗi.

- Dùng 2 binary đã build (`simple_chain_BASE_16M`, `simple_chain_HEAD_16M`) — **xác nhận lại bằng `strings`** rằng hằng số/chuỗi log đúng (HEAD có `Commit sync: partial block response`, BASE không).
- Xen kẽ **≥6 lần mỗi bản**, tool blast gốc (§2). Với mỗi lần chạy, **bắt buộc**:
  - đếm trong từng `val*_execution.log`: `grep -c "Expected [0-9]* but received [0-9]* blocks returned"` và `grep -c "fetched bytes exceeded limit"`;
  - ghi block cao nhất của từng validator ở cuối lần chạy;
  - **sau khi harness kết thúc, đợi thêm tối đa 10 phút** (cụm vẫn chạy) và đo lại block cao nhất của validator tụt lại, để biết nó **bắt kịp** hay **kẹt vĩnh viễn** (harness dừng poll sau 60 s không có tx mới nên có thể cắt sớm — cần sửa harness cục bộ để chờ, ghi rõ diff).
- Kết luận cần có: (a) tỷ lệ BASE có chữ ký lỗi; (b) HEAD có chữ ký lỗi nào không (kỳ vọng 0); (c) tốc độ catch-up của HEAD (block/s) và có bắt kịp hay không.
- Đối chứng âm: nếu BASE_16M **không bao giờ** có chữ ký lỗi trong ≥6 lần → thí nghiệm không ép được lỗi; dừng, báo, không tuyên bố PASS.

---

## 4. Việc C — T2: điều tra treo NOMT `write_ht` (nhiệm vụ như giai đoạn 2 §3, tóm tắt lại)

Bằng chứng gốc: `note/evidence/plan_gemini_verify_20261011/nomt_hang_evidence/`. Triệu chứng: thread commit kẹt ở `write_ht → io_handle.recv()`; 9 `io-worker` rảnh ở `command_rx.recv()`; 110 reader kẹt ở `lock_shared_slow`.

1. Đọc mã và báo cáo sơ đồ: `IoHandle` của bitbox/beatree được tạo ở đâu, có chia sẻ `completion_receiver` giữa thread không (`consensus/vendor/nomt/nomt/src/io/mod.rs`; grep `.receiver()`, `.recv()`, `try_recv`, `clone()` của `IoHandle` trong `consensus/vendor/nomt/nomt/src`). Kết luận phải dựa trên đường dẫn mã cụ thể (file:dòng).
2. Tạo **bản copy tạm** để thêm log chẩn đoán (KHÔNG commit, không đổi hành vi): `write_ht` (số `sent`, số completion nhận, thời điểm; cảnh báo chỉ log khi chờ > 30 s), `run_worker` (mỗi completion gửi đi, kết quả `send`, `pending.len()`/`retries.len()`). Build thành binary riêng `simple_chain_HEAD_DIAG` (nhớ bước `cp -p` thư viện + `touch`).
3. Chạy vòng lặp BFT bão hòa với `simple_chain_HEAD_DIAG` (tool gốc), dừng ngay khi bắt được treo (`complete=false` **và** log chẩn đoán/WATCHDOG khớp), **giữ cụm sống**, lấy `kill -QUIT <pid>` + `/proc/<pid>/task/*/{comm,wchan}`, rồi **báo chủ dự án** để chạy `sudo gdb -batch -p <pid> -ex "thread apply all bt 30"` (nêu rõ lệnh và PID bạn cần).
4. Giới hạn: tối đa **20 lần** chạy. Nếu không bắt được → báo "KHÔNG TÁI HIỆN trong 20 lần" kèm tỷ lệ kỳ vọng (~12%/lần → xác suất 20 lần sạch ≈ 8%), **không** kết luận lỗi đã hết.
5. Sản phẩm: `sent` vs `received` tại thời điểm treo; completion cuối cùng nhận được; có `Err` ở `send` hay panic thread không; giả thuyết còn lại. Chỉ **đề xuất** sửa (không áp dụng), tuyệt đối không dùng timeout/sleep để thoát vòng lặp (Zero-Fork, `AGENTS.md` Part 2.5).

---

## 5. Việc D — T3b: soak cap thật (128 MiB)

- `HEAD` sạch (không DIAG, không cap thấp): **≥20 lần**; `BASE`: **≥10 lần**, xen kẽ. Tool blast gốc.
- Phân loại **mọi** lần không hoàn tất theo bảng §4.1 của kế hoạch giai đoạn 2. Với HEAD, **chữ ký loại 1 (Expected N but received M + exceeded limit) xuất hiện dù chỉ một lần = hồi quy nghiêm trọng, báo ngay cho chủ dự án**.
- Nêu rõ giới hạn thống kê: 0/20 lỗi chỉ cho cận trên 95% ≈ 14%.
- Việc D chạy lâu (30 lần × ~7 phút ≈ 3.5 giờ): chạy nền bằng `nohup`, ghi tiến độ vào `meas_out/PROGRESS.txt` mỗi lần xong.

---

## 6. Việc E — Đóng gói bằng chứng và báo cáo cuối

1. Chuyển **toàn bộ dữ liệu thô các lần chạy của giai đoạn 2 và 3** từ `/tmp/gemini_verify/meas_out/` vào `note/evidence/gemini_phase2_20261011/raw/` (JSON, `.log`, `_timeline.json`; `*.tar.gz` log validator bị gitignore nhưng vẫn phải lưu trên đĩa: ghi đường dẫn tuyệt đối + `sha256`). Kèm `uptime` của từng lần chạy.
2. Viết script nhỏ (`note/evidence/gemini_phase2_20261011/summarize.py`) **tính bảng từ file thô** (trung vị, min/max, số lần complete, chênh lệch %). Báo cáo chỉ được trích số do script này in ra; không gõ tay số vào báo cáo.
3. Báo cáo `note/gemini_phase2_report_20261011.md` gồm: (1) môi trường & hash binary đã xác nhận; (2) T1a/T1b — **tách bảng theo phiên bản tool blast**; (3) T3a/T3b; (4) T2 — phát hiện + giả thuyết còn lại + trạng thái "xong/chưa xong"; (5) T4 — danh sách test + kết quả mutation (đã được Claude kiểm lại); (6) **Lần chạy xấu** (liệt kê hết, kèm chữ ký log); (7) **Chưa kiểm chứng / giới hạn**; (8) danh sách file thô + sha256. Mục nào không làm được → viết "KHÔNG LÀM ĐƯỢC" kèm lý do.
4. **Không commit.** Để nguyên trong cây làm việc cho Claude/chủ dự án rà soát và commit. Trước khi báo hoàn tất chạy: `git status --short` (chỉ được thấy các file test T4, file báo cáo/evidence/script của bạn; **không** còn `secp_tps_blast/main.go` bị sửa) và `cd consensus/metanode/scripts && ./build_check.sh` (5/5).

---

## 7. Thứ tự và ước lượng thời gian
1. Việc A (~1.5 giờ gồm đo lại BFT 12 lần) →
2. Việc B (~1.5 giờ, 12 lần) →
3. Việc C (tối đa ~2.5 giờ, 20 lần) →
4. Việc D (~3.5 giờ, 30 lần) →
5. Việc E (~1 giờ).
Nếu thiếu thời gian, **không cắt số lần** của một việc để kịp: bỏ hẳn việc ưu tiên thấp (D) và ghi "KHÔNG LÀM ĐƯỢC" trong báo cáo.

## 8. Tiêu chí chấp nhận (Claude sẽ kiểm bằng cách nào)
- Mọi con số trong báo cáo truy ngược được tới file thô và khớp `summarize.py`.
- Claude sẽ tự chạy lại ngẫu nhiên 2–3 test (`cargo test ... commit_syncer`, `go test ... TestSortEquivalence`) và đối chiếu sha256 binary/file thô.
- `git status` sạch ngoài các file được phép; không có thay đổi mã sản phẩm nằm ngoài danh sách đã duyệt.

---

## 9. Bổ sung sau khi Claude đọc `note/gemini_phase2_report_20261011.md` (bản nháp Gemini nộp trong lúc kế hoạch này được soạn)

Báo cáo đã tồn tại ⇒ **sửa bản này, không viết lại từ đầu**. Các điểm phải sửa (đã đối chiếu với mã):

1. **§3.3 giả thuyết 2 (CQ ring overflow, worker đi ngủ khi CQE chưa được gặt) mâu thuẫn với chính mã `run_worker`** (`consensus/vendor/nomt/nomt/src/io/linux.rs`): một lệnh chỉ rời `pending` khi CQE của nó được gặt (`pending.remove(...)` trong vòng xử lý completion). Worker chỉ vào nhánh `command_rx.recv()` khi `pending.is_empty()` — tức mọi CQE đã được gặt và completion đã `send`. Vì vậy kịch bản "CQE chưa được gặt mà worker vẫn ngủ" không thể xảy ra theo mã này. Hoặc (a) chỉ ra đường dẫn mã cụ thể (file:dòng) mà kịch bản đó có thể xảy ra, hoặc (b) bỏ giả thuyết, hoặc (c) ghi rõ là **suy đoán chưa có căn cứ**. Cụm "hàng trăm ngàn I/O requests" cần số liệu hoặc bỏ.
2. **Đoạn mã trích trong §3.2.1 (`make_handle`, `IoHandle { sender, receiver }`) không khớp mã thật** (`IoHandle` thật có `sender: Weak<Sender<IoPacket>>`, `completion_sender`, `completion_receiver`; xem `io/mod.rs` quanh dòng 202). Mọi đoạn mã trích trong báo cáo phải **sao chép nguyên văn kèm `file:dòng`**; không diễn giải lại thành "mã minh hoạ".
3. **"Loại trừ 100%"** chỉ đúng nếu có kết quả grep cụ thể kèm lệnh. Ghi lệnh và số kết quả; nếu không, hạ xuống "không tìm thấy trong grep …".
4. Giả thuyết còn mở hợp lý hơn cần xem xét thay cho (2): completion bị mất ở kênh `completion_sender → completion_receiver` do `Err` bị nuốt (`let _ = completion_sender.send(...)` trong `run_worker`), hoặc `write_ht` đếm `sent` lệch với số lệnh worker thực sự nhận (ví dụ lệnh bị `Weak::upgrade` thất bại ở `IoHandle::send` rồi `unwrap`). Dùng log chẩn đoán ở Việc C để kiểm, không kết luận trước.
5. **§ về `secp_tps_blast` treo socket:** báo cáo nói "bị treo vĩnh viễn" nhưng không dẫn tag/log. Thêm bằng chứng (tag lần chạy, thời gian treo, stack nếu có) hoặc rút lại (xem Việc A).
6. Các bảng số TPS trong báo cáo phải **tách theo phiên bản tool blast** và được in bởi `summarize.py` (Việc E), không gõ tay.
