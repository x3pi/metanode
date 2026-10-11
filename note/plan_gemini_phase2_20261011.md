# Kế hoạch giai đoạn 2 cho Gemini — đo độc lập thật + điều tra treo NOMT + củng cố test

> Ngày: 2026-10-11 · Nhánh: `dev` · HEAD khi giao: `adbe1ab8`
> Đọc trước: `note/plan_gemini_verify_20261011.md` (luật chống pass giả — **vẫn áp dụng nguyên văn**) và `note/gemini_verify_report_20261011.md` (kết quả giai đoạn 1).
> Khác biệt với giai đoạn 1: giai đoạn 1 Gemini chỉ **phân tích lại dữ liệu của Claude** và viết test. Giai đoạn 2 yêu cầu **tự chạy máy, tự đo**.

---

## 0. Quy tắc bổ sung (rút ra từ giai đoạn 1)

1. **Không hoàn nguyên, không ghi đè file bạn không tạo.** Trước mỗi lần sửa: `git status --short` và `git diff <file>`. **Cấm** `git checkout -- <file>`, `git stash`, `git reset` lên file nào bạn không tự sửa trong phiên này. (Ở giai đoạn 1 bản sửa `waitForPoolRoom` của Claude bị mất vì lý do này và test lại panic.)
2. **Phạm vi sửa mã:** chỉ được thêm/sửa **file test** và **log chẩn đoán tạm** (không commit log chẩn đoán). Mọi sửa đổi mã sản phẩm phải: nằm trong danh sách ở §5, có test FAIL-trước/PASS-sau, và được chủ dự án duyệt trước khi commit.
3. **Mọi số liệu trong báo cáo phải do chính bạn đo trong phiên này**, có `uptime` và timestamp file thô. Số của người khác (kể cả Claude) chỉ được đưa vào mục "Tham chiếu", ghi rõ nguồn, **không** dùng làm bằng chứng cho kết luận.
4. **Phân loại mọi lần chạy không hoàn tất theo chữ ký log** (bảng ở §4.1). Không gán nguyên nhân bằng suy đoán; nếu không khớp chữ ký nào → ghi "CHƯA PHÂN LOẠI" và giữ log.
5. Sau mỗi thay đổi mã bạn tự thêm (kể cả test): `cd consensus/metanode/scripts && ./build_check.sh` phải "ALL BUILDS PASSED (5/5)", và `cd execution && gofmt -l <file>` không in tên file mới của bạn.
6. Mật khẩu sudo **không** được đưa vào tài liệu. `gdb`/`dmesg` cần sudo → hỏi chủ dự án khi cần, ghi rõ lệnh bạn xin chạy.
7. Báo cáo cuối: `note/gemini_phase2_report_20261011.md`, thô trong `note/evidence/gemini_phase2_20261011/raw/` (`*.tar.gz` bị gitignore; vẫn lưu trên đĩa và ghi đường dẫn tuyệt đối + sha256).

---

## 1. Chuẩn bị (làm trước tiên, ghi lại từng bước)

```bash
cd /home/abc/chain-n/metanode
git log --oneline -7                      # HEAD phải là adbe1ab8 (hoặc mới hơn, ghi rõ)
git status --short                        # phải sạch trước khi bắt đầu
cd consensus/metanode/scripts && ./build_check.sh   # 5/5
```

**Harness:** `note/evidence/harness_20261011/` (đọc `README.md` ở đó: phải đổi biến `S`, tạo `bins/`, `meas_out/`; build `secp_tps_blast`; kiểm tra `/tmp/gate_4val_clean_template` còn tồn tại — nếu không, DỪNG và báo).

**Build các bản so sánh bằng `git worktree`** (không đụng cây làm việc chính):
```bash
git worktree add /tmp/gemini_verify/wt_base f610b0f7     # TRƯỚC hai commit perf/fix
git worktree add /tmp/gemini_verify/wt_head adbe1ab8      # HEAD
# trong mỗi worktree: build Rust -> cp -p target/release/libmetanode.a consensus/metanode/target/release/ ->
#   touch execution/executor/ffi_bridge.go execution/pkg/nomt_ffi/bridge.go ->
#   cd execution && CGO_ENABLED=1 go build -o /tmp/gemini_verify/bins/simple_chain_<TAG> ./cmd/simple_chain
```
Xác nhận mỗi binary link đúng bản: Rust — `strings <binary> | grep -c "Commit sync: partial block response"` (HEAD: 1, base: 0). Go — `strings` hoặc `go version -m`/`git rev-parse` ghi vào log build. **Không tin tên file binary.**

---

## 2. Nhiệm vụ T1 — Đo độc lập thật (ưu tiên cao nhất)

Mục tiêu: xác nhận hoặc bác bỏ con số của Claude (Raft +22%, BFT không đổi, BFT không hồi quy) bằng số đo của chính bạn.

### T1a. Raft A/B
- Bản `BASE` (= `f610b0f7`) và `HEAD`, **xen kẽ** `BASE,HEAD,BASE,HEAD,…`, **≥5 lần mỗi bản**, `e2e3.py raft`, `-duration 60 -batch 1000`.
- **Đối chứng nhiễu:** thêm 3 lần `BASE` chạy lại cùng binary (BASE-vs-BASE) vào chuỗi để đo biên nhiễu.
- Báo cáo: bảng từng lần (`submitted`, `on_chain`, `complete`, `e2e_secs`, `e2e_tps`, `uptime`), trung vị, min/max, chênh lệch %, biên nhiễu BASE-vs-BASE.
- **PASS** nếu: mọi lần `complete=true`, và trung vị HEAD ≥ 1.10 × trung vị BASE **và** chênh lệch lớn hơn biên nhiễu. Nếu không đạt → ghi **"không tái hiện được"** với số liệu thật. Không chỉnh ngưỡng.

### T1b. BFT hồi quy
- 4 validator, `BASE` vs `HEAD` xen kẽ, **≥6 lần mỗi bản**. Mọi lần phải ghi `complete`.
- Phân loại mọi lần `complete=false` theo §4.1 (đọc `meas_out/logs_<tag>/val*_execution.log`).
- **PASS** nếu: HEAD không có tỷ lệ không-hoàn-tất cao hơn BASE một cách có ý nghĩa **và** trung vị TPS HEAD không thấp hơn BASE quá 5%. Cỡ mẫu nhỏ → nói thẳng "không đủ để kết luận" thay vì PASS.

### T1c. Thời gian và tài nguyên
Mỗi lần BFT ~6–8 phút → T1b mất ~2 giờ. Chạy nền (`nohup`) và ghi tiến độ; không rút ngắn số lần để kịp.

---

## 3. Nhiệm vụ T2 — Điều tra treo NOMT `write_ht` (chưa có bản sửa)

**Đã biết (bằng chứng ở `note/evidence/plan_gemini_verify_20261011/nomt_hang_evidence/`):** một thread ở `nomt_commit_payload → Store::commit → bitbox::SyncController::post_meta → writeout::write_ht` chờ `io_handle.recv()` mãi; 9 thread `io-worker` (`consensus/vendor/nomt/nomt/src/io/linux.rs::run_worker`) đều rảnh ở `command_rx.recv()`; 110 thread đọc kẹt ở `Nomt::read → RawRwLock::lock_shared_slow`. Không có lỗi I/O trong `dmesg`. Tần suất ~3/25 lần BFT bão hòa.
**Chưa biết:** vì sao thiếu completion. Giai đoạn 1 đã thu hồi giả thuyết "retry nuốt completion" (mã nộp lại retry vào queue).

**Việc cần làm (chẩn đoán, KHÔNG đổi hành vi):**
1. Đọc và báo cáo sơ đồ: ai tạo `IoHandle` cho bitbox/beatree (`io/mod.rs`: `IoHandle::clone` có chia sẻ `completion_receiver` hay không; `new_handle`/`receiver()`); có thread nào khác gọi `recv`/`try_recv`/`receiver()` trên **cùng** handle trong lúc `write_ht` chạy không (grep `.receiver()` và các nơi gọi `recv` trong `consensus/vendor/nomt/nomt/src`).
2. Thêm log chẩn đoán tạm (không commit) trong bản copy vendored:
   - `write_ht`: số lệnh `sent`, số completion nhận được theo thời gian, địa chỉ/`Arc` của handle, thời điểm bắt đầu/kết thúc; in cảnh báo (chỉ log, KHÔNG thoát vòng lặp) khi chờ > 30 s kèm `sent` còn lại.
   - `run_worker`: mỗi lần gửi completion, log `user_data`, kết quả; log khi `completion_sender.send` trả `Err`; log `pending.len()`/`retries.len()` khi vào `command_rx.recv()`.
3. Chạy vòng lặp BFT bão hòa (`e2e3.py bft`, dừng ngay khi `complete=false` — xem `chain*.sh` mẫu trong harness README), **giữ cụm sống** khi gặp treo (không để harness tắt cụm), rồi: `kill -QUIT <pid>` lấy dump goroutine; xin chủ dự án chạy `sudo gdb -batch -p <pid> -ex "thread apply all bt 30"`; lưu `/proc/<pid>/task/*/{comm,stat,wchan}`.
4. Phân biệt rõ hai loại "không hoàn tất" khác (xem §4.1) với treo NOMT; chỉ tính là NOMT-hang khi thấy thread kẹt ở `write_ht` trong gdb hoặc log chẩn đoán xác nhận `sent > received` kéo dài.
5. Giữ ≥10 lần chạy nếu chưa bắt được; **không** kết luận "đã hết" từ vài lần sạch (xác suất ~12%/lần).

**Sản phẩm:** `received` vs `sent` tại thời điểm treo; completion cuối cùng nhận được ứng với lệnh nào; có `Err` ở `send` không; có panic thread không (`grep -i panicked`); giả thuyết còn lại sau khi loại trừ. Nếu tìm được nguyên nhân, **chỉ đề xuất** bản sửa kèm test tái hiện (không áp dụng khi chưa được duyệt; tuyệt đối không dùng timeout/sleep để thoát vòng lặp — Zero-Fork, AGENTS.md Part 2.5).

---

## 4. Nhiệm vụ T3 — Soak cho bản fix fetcher + bộ phân loại lỗi

### 4.1 Bảng chữ ký log để phân loại lần chạy BFT không hoàn tất
| Chữ ký trong `val*_execution.log` | Nghĩa | Đã biết |
| :-- | :-- | :-- |
| `Failed to fetch CommitRange(...): Expected N but received M blocks returned` kèm `fetch_blocks() fetched bytes exceeded limit` | Fetch bị cắt theo byte (lỗi đã sửa ở `27330f8d`) | Không được xuất hiện ở HEAD. Nếu xuất hiện → **hồi quy nghiêm trọng**, báo ngay |
| `WATCHDOG-EVM ... ProcessTransactionsOptimistic` lặp nhiều lần, block cao nhất của validator đó đứng yên | Go Block-STM treo | Thường do NOMT hang (T2) — xác nhận bằng dump/gdb |
| Validator có block cao nhất thấp hơn hẳn, `Phase=CatchingUp`, `Local DAG Commit` tăng chậm, không có hai chữ ký trên | Catch-up chậm (go_rate ~1–2 blk/s) | Harness dừng poll sau 60 s không có tx mới nên có thể cắt sớm; cần chạy thêm thời gian chờ để biết hồi phục hay kẹt |
| Khác | CHƯA PHÂN LOẠI | Giữ nguyên log |

### 4.2 Soak
- Với `HEAD` ở **cap thật** (128 MiB): ≥20 lần BFT bão hòa xen kẽ với `BASE` ≥20 lần. Báo cáo bảng tỷ lệ hoàn tất và phân loại theo §4.1. Kỳ vọng: không có chữ ký loại 1 ở HEAD.
- Với **cap 16 MiB** (sửa tạm `MAX_TOTAL_FETCHED_BYTES` trong `network/tonic_network.rs`, **không commit**): `HEAD` và `BASE` mỗi bản ≥3 lần. `BASE` phải thất bại với chữ ký loại 1; `HEAD` không có chữ ký loại 1. Với HEAD cap thấp, đo tốc độ catch-up (block/s của validator tụt lại) và báo cáo; chạy đủ lâu (không cắt sau 60 s) để biết cuối cùng validator có bắt kịp không.

---

## 5. Nhiệm vụ T4 — Test an toàn cho fix fetcher (được phép thêm test, KHÔNG sửa fetcher)

Hiện chỉ có `test_fetch_blocks_recovers_when_peer_returns_partial_prefix` (trong `consensus/metanode/meta-consensus/core/src/commit_syncer/mod.rs`). Fix chỉ an toàn nếu thêm được các tính chất sau, mỗi test phải **FAIL** khi bạn cố tình phá tính chất đó (mutation) và PASS ở HEAD:
1. **Peer bỏ sót block giữa chừng** (trả `b0,b2,b3` thay vì `b0,b1,b2,b3`) → `fetch_once` phải trả lỗi (không nhận block sai vị trí). Dùng mock client có chế độ này.
2. **Peer không tiến triển** (trả rỗng ở lần thứ hai) → phải trả `UnexpectedNumberOfBlocksFetched`, không lặp vô hạn (đặt giới hạn thời gian chạy test bằng `tokio::time::timeout` trong **test**, không phải trong mã sản phẩm).
3. **Peer trả sai nội dung** (đúng số lượng nhưng một block có digest khác) → phải trả `UnexpectedBlockForCommit`.
4. **Peer trả nhiều hơn yêu cầu** → phải bị từ chối.
Ghi rõ cách mutation thực hiện (diff tạm) và kết quả FAIL/PASS của từng test. Test mới nằm trong `mod.rs` (module `tests`), cùng phong cách test hiện có. Chạy `cargo test --release --locked -p consensus-core commit_syncer` và báo số test.

---

## 6. Báo cáo cuối (bắt buộc)

`note/gemini_phase2_report_20261011.md` gồm: (1) môi trường & build (hash binary đã xác nhận); (2) T1 bảng thô + kết luận; (3) T2 phát hiện + giả thuyết còn lại; (4) T3 bảng phân loại; (5) T4 test + kết quả mutation; (6) **Lần chạy xấu** (liệt kê hết); (7) **Chưa kiểm chứng / giới hạn**; (8) danh sách file thô kèm sha256. Mục nào không làm được → ghi "KHÔNG LÀM ĐƯỢC" kèm lý do, không bỏ trống hay thay bằng số của người khác.

## 7. Thứ tự ưu tiên khi thiếu thời gian
T1a → T4 → T3 (cap 16 MiB) → T1b → T2 → T3 (soak cap thật). T1a và T4 độc lập, rẻ, và nên xong trước.
