# Kế hoạch kiểm chứng độc lập — chống "pass giả" (gửi Gemini thực hiện)

> Ngày: 2026-10-11 · Nhánh: `dev` · Người giao: Claude (theo yêu cầu của chủ dự án)
> Mục tiêu: **kiểm chứng độc lập, chính xác** 3 thay đổi + 1 lỗi còn mở. Kết quả phải **tái tạo được từ log thô**.
> Nguyên tắc tối cao: **một bài test không thể FAIL thì không có giá trị.** Mọi kết luận "PASS" phải có (a) số liệu thô, (b) lệnh đã chạy, (c) đối chứng âm (negative control) chứng minh bài test có khả năng phát hiện lỗi.
> Bài học đã xảy ra trong repo: một `audit_pack/` + script T0–T3 từng in "PASSED" giả (xem memory `feedback_fabricated_audit_pack_found`); báo cáo Gemini trước đó từng sai số liệu 4 lần. Vì vậy phần "Luật chống pass giả" bên dưới là **bắt buộc**.

---

## 0. Luật chống pass giả (BẮT BUỘC, vi phạm = kết quả bị loại)

1. **Không sửa ngưỡng, không sửa script đo, không sửa mã sản phẩm** để làm test xanh. Nếu cần sửa harness thì ghi rõ trong báo cáo (diff + lý do) và chạy lại toàn bộ từ đầu.
2. **Đọc xem banner xanh thực sự chạy gì.** Trước khi tin một script "ALL PASSED", mở script ra, liệt kê các lệnh nó chạy và các điều kiện nó kiểm tra. Nếu script chỉ `echo PASS` hoặc không kiểm tra giá trị thật → báo cáo là **không hợp lệ**.
3. **Mọi con số TPS phải kèm tính đầy đủ:** `submitted`, `on_chain` (đếm lại bằng RPC `eth_getBlockByNumber`, không lấy từ báo cáo của tool), `complete = on_chain >= submitted`, thời gian từ lần gửi đầu đến tx cuối lên chain. Một lần chạy mà `complete=false` **không được tính vào trung vị** và phải được liệt kê riêng như một lần FAIL.
4. **Đo e2e phải poll chain song song với lúc gửi** (không đợi tool gửi thoát rồi mới poll — tool gửi có pha verify riêng làm sai số). Dùng `e2e3.py` kèm theo (đã poll song song và lưu `*_timeline.json`), hoặc viết harness tương đương và đối chiếu với nó trên 1 lần chạy.
5. **Mỗi so sánh A/B phải chạy xen kẽ (A,B,A,B,…) tối thiểu 3 lần mỗi bản**, báo cả giá trị từng lần, trung vị và min/max. Chênh lệch < 5% coi là **nhiễu, không phải cải thiện**. Máy dùng chung (load average thường ~20) → ghi `uptime` trước/sau mỗi lần chạy.
6. **Bắt buộc có đối chứng âm** cho mỗi mục (mô tả ở từng mục). Nếu đối chứng âm **không fail** → bài test vô hiệu, dừng và báo.
7. **Lưu log thô** của mọi lần chạy (`execution.log` của từng node, JSON của tool, timeline) vào `note/evidence/gemini_verify_20261011/raw/`. Báo cáo chỉ được trích số có thể truy ngược tới file trong thư mục này.
8. **Không xóa/ẩn lần chạy xấu.** Phải báo cáo tổng số lần chạy, số lần hoàn tất, số lần thất bại, kèm nguyên nhân thất bại nếu xác định được.
9. **Ghi rõ điều gì CHƯA kiểm chứng.** "Không thấy lỗi" ≠ "đã chứng minh không có lỗi". Mẫu nhỏ phải nói là mẫu nhỏ.
10. **Zero-fork (AGENTS.md Part 2.5):** không dùng timeout/sleep để quyết định dispatch, không bypass digest. Mọi đề xuất sửa phải qua checklist này.

---

## 1. Chuẩn bị môi trường

```bash
cd /home/abc/chain-n/metanode
git fetch && git log --oneline -5          # phải thấy 27330f8d và 1408ddd6
git status --short                         # phải sạch (nếu bẩn, ghi lại và dừng)
cd consensus/metanode/scripts && ./build_check.sh   # phải "ALL BUILDS PASSED (5/5)"
```

- **Cạm bẫy build:** Go link thư viện Rust từ `consensus/metanode/target/release/libmetanode.a`, nhưng `cargo` ghi vào `/home/abc/chain-n/metanode/target/release/`. `build_check.sh` có bước `cp`; nếu build tay thì phải `cp -p target/release/libmetanode.a consensus/metanode/target/release/` rồi `touch execution/executor/ffi_bridge.go execution/pkg/nomt_ffi/bridge.go` trước `go build`. Kiểm tra bản link đúng bằng `strings <binary> | grep <chuỗi log mới thêm>`.
- Cụm Raft 3 node: dùng harness có sẵn ở `execution/scripts/test/` (xem `run_ab_interleaved_validation.py`). Cụm BFT 4 validator: `consensus/metanode/scripts/` (xem `CLUSTER_OPERATIONS.md`).
- Dọn tiến trình/dữ liệu cũ trước mỗi lần chạy (wipe data devnet), nếu không số liệu bị nhiễm.
- Mật khẩu sudo **không** có trong tài liệu này; nếu cần `gdb`/`sudo` hãy hỏi chủ dự án.

---

## 2. Mục A — Sort khoá tính trước + signer đồng nhất (commit `1408ddd6`)

**Khẳng định cần kiểm chứng:** tăng e2e Raft ~22% (13.9k → 17.0k tx/s), BFT không đổi, **kết quả logic không đổi**.

### A1. Tính đúng đắn (quan trọng nhất, làm TRƯỚC khi đo TPS)
- Chạy `go test ./execution/pkg/transaction/ ./execution/cmd/simple_chain/processor/` (ghi số test chạy/pass, không chỉ "ok").
- **Test tương đương thứ tự (tự viết, ≥1 test mới):** sinh ≥10⁵ tx ngẫu nhiên (nhiều địa chỉ, nonce lộn xộn, có trùng cặp (địa chỉ,nonce)), chạy sort **cũ** (`git show 1408ddd6^:execution/cmd/simple_chain/processor/tx_validator_pool_core.go`) và **mới** → hai kết quả phải **giống hệt từng phần tử** (so hash tx). Đây là điều kiện để không phát sinh fork.
- **Đối chứng âm:** sửa tạm bản mới (ví dụ đảo so sánh nonce) → test phải FAIL. Nếu vẫn pass → test vô hiệu.
- Test signer: với mỗi loại tx (legacy EIP-155, 2930, 1559, 4844, 7702) so sánh địa chỉ người gửi giữa `ValidateEthTxEnvelope` bản cũ và mới → phải giống nhau; tx chữ ký sai/ chainId sai vẫn phải bị từ chối với **cùng mã lỗi**.

### A2. Đo TPS Raft (A/B xen kẽ)
- Bản A = `1408ddd6^` (trước thay đổi), bản B = `1408ddd6`. Raft 3 node, `secp_tps_blast -mode tcp -type 1559 -duration 60 -batch 1000`, ≥3 lần mỗi bản, đo bằng `e2e3.py raft ...` (hoặc harness tương đương).
- **Tiêu chí PASS:** trung vị B ≥ 1.10 × trung vị A **và** mọi lần `complete=true`, `zero_fork=true`, `roots=true`. Báo cáo con số thật dù có đạt hay không.
- Đối chứng: chạy A-vs-A (cùng binary hai lần) để biết biên nhiễu của máy; chênh lệch B–A phải vượt rõ biên này.

### A3. BFT không hồi quy
- 4 validator, ≥3 lần mỗi bản, mọi lần phải `complete=true`. Nếu có lần thiếu tx → xem Mục C trước khi kết luận.

---

## 3. Mục B — Fix commit syncer (commit `27330f8d`)

**Khẳng định:** khi `fetch_blocks` bị cắt ở `MAX_TOTAL_FETCHED_BYTES`, fetcher giờ lấy tiếp phần còn lại, validator tụt lại **không còn kẹt vĩnh viễn**.

Lỗi gốc rất hiếm ở cap thật 128 MiB (~1/8 lần chạy BFT bão hòa), nên **bắt buộc dùng cap thấp để ép tái hiện**:

1. Tạo 2 bản Rust tạm (KHÔNG commit): sửa `consensus/metanode/meta-consensus/core/src/network/tonic_network.rs` dòng `const MAX_TOTAL_FETCHED_BYTES` từ `128` MiB xuống `16` MiB.
   - **Bản NOFIX:** checkout `fetcher.rs` về `27330f8d^`.
   - **Bản FIX:** `fetcher.rs` ở `27330f8d`.
2. Với mỗi bản: build Rust → `cp` thư viện → `touch` file cgo → `go build` binary riêng. Xác nhận bằng `strings`/log rằng đúng bản được link (bản FIX có chuỗi `"Commit sync: partial block response"`).
3. Chạy BFT 4 validator bão hòa (`-duration 60 -batch 1000`), lưu log mỗi validator. Lệnh kiểm tra trong log:
   - `grep -c "Expected [0-9]* but received [0-9]* blocks returned"` (lỗi cũ),
   - `grep -c "fetched bytes exceeded limit"` (chứng tỏ cap thực sự bị chạm),
   - block cao nhất của từng validator (so sánh giữa các validator).
- **Tiêu chí PASS:** NOFIX phải **fail** (đã thấy 1.06M/4.32M tx, cụm đứng; nếu NOFIX không fail thì thí nghiệm vô hiệu) **và** FIX không còn lỗi `Expected N but received`, đồng thời mọi validator cùng tiến tới cuối. Lưu ý: FIX có thể vẫn chậm khi catch-up (cap 16 MiB làm mỗi lần lấy ít) — báo cáo tốc độ catch-up (block/giây) thay vì chỉ "pass/fail".
- Chạy lại `cargo test --release --locked -p consensus-core commit_syncer` (7 test) và ghi số lượng.
- **Việc còn thiếu:** chưa có unit test cho đường "nhận prefix rồi lấy tiếp". Hãy đề xuất/viết một test dùng mock `NetworkClient` trả prefix (hiện `fetch_blocks` trong mock là `unimplemented!`, `commit_syncer/mod.rs` ~dòng 3153). Test đó phải FAIL với `fetcher.rs` cũ và PASS với bản mới.

---

## 4. Mục C — Lỗi treo NOMT `write_ht` (CHƯA SỬA — điều tra, không "pass")

**Hiện tượng (đã thấy 3 lần/~25 lần chạy BFT bão hòa):** một validator đứng vĩnh viễn; Go log `WATCHDOG-EVM ... ProcessTransactionsOptimistic` lặp mãi.
**Bằng chứng đã có:** `note/evidence/plan_gemini_verify_20261011/nomt_hang_evidence/` (`val2_gdb.txt`, `val2_goroutines.txt`):
- 1 thread kẹt ở `nomt_commit_payload → Store::commit → bitbox::SyncController::post_meta → writeout::write_ht` chờ `io_handle.recv()`;
- 9 thread `io-worker` (io_uring, `consensus/vendor/nomt/nomt/src/io/linux.rs`) đều rảnh ở `command_rx.recv()`;
- 110 thread đọc kẹt ở `Nomt::read → RawRwLock::lock_shared_slow`.
→ Có completion ghi trang bị mất (hoặc `sent` đếm sai). Chưa biết cơ chế.

**Nhiệm vụ (điều tra, không được tuyên bố "đã sửa" nếu chưa tái hiện được):**
1. Thêm log chẩn đoán KHÔNG đổi hành vi vào `write_ht` (số lệnh đã gửi, số completion đã nhận, mã lỗi nếu có, thời điểm), và ở `run_worker` (số `pending`, số `retries`, completion trả về). Chạy vòng lặp BFT bão hòa (dùng `e2e3.py bft ...`, dừng ngay khi `complete=false`) để bắt lần treo kế tiếp **khi cụm còn sống**.
2. Khi treo: giữ nguyên tiến trình, lấy `kill -QUIT <pid>` (dump goroutine vào `execution.log`) và `sudo gdb -batch -p <pid> -ex "thread apply all bt 30"` (hỏi chủ dự án về sudo). Lưu vào `raw/`.
3. Báo cáo: `sent` vs `received` tại thời điểm treo; thread nào còn `pending`; có panic ở worker không (`grep -i panicked`); IoHandle có bị dùng chung giữa các thread không.
4. Chỉ **đề xuất** bản sửa; mọi bản sửa phải kèm test tái hiện FAIL-trước/PASS-sau và không dùng timeout để thoát (Zero-Fork).

**Cấm:** "chạy 5 lần không thấy treo nên đã ổn". Tần suất ~12% → 5 lần sạch có xác suất ~50% ngay cả khi lỗi còn đó. Muốn nói "đã giảm" phải ≥30 lần chạy xen kẽ có đối chứng.

---

## 5. Mục D — Patch drain 2× (CHƯA áp dụng, chỉ đo)

`note/evidence/plan_gemini_verify_20261011/drain2x.patch` đổi `maxPoolDrainPerTick` 5× → 2× (`tx_batch_forwarder_core.go`). Đã đo Raft ~20.6k tx/s (+20% trên 17.0k) nhưng chưa biết có làm tăng khả năng validator tụt lại trên BFT hay không.
- Đo Raft A/B xen kẽ ≥5 lần mỗi bản (trước = HEAD, sau = HEAD + patch). Đo BFT ≥15 lần mỗi bản xen kẽ, **báo tỷ lệ lần không hoàn tất** và nguyên nhân từng lần (dùng Mục B/C để phân loại: fetcher / NOMT / khác).
- **Không áp dụng vào mã** nếu BFT có tỷ lệ không hoàn tất cao hơn bản gốc; chỉ báo cáo.

---

## 6. Định dạng báo cáo yêu cầu

Tạo `note/gemini_verify_report_20261011.md` gồm:
1. Bảng mỗi mục: lệnh đã chạy, số lần chạy, số lần hoàn tất, từng giá trị thô, trung vị, min/max, kết luận PASS/FAIL/KHÔNG KẾT LUẬN.
2. Phần **"Đối chứng âm"**: mỗi mục một dòng — đối chứng nào đã chạy, kết quả (phải thấy FAIL như mong đợi).
3. Phần **"Lần chạy xấu"**: liệt kê tất cả lần `complete=false` hoặc nhiễu bất thường.
4. Phần **"Chưa kiểm chứng / giới hạn"**: máy đơn, tải nền, cỡ mẫu.
5. Phụ lục: đường dẫn từng file thô trong `raw/`.

Báo cáo nào thiếu một trong các mục trên, hoặc có con số không truy ngược được tới `raw/`, sẽ bị coi là không hợp lệ.

## 7. Tham chiếu
- Báo cáo TPS trước: `note/tps_final_report_20261010.md`
- Ghi chú lỗi/nguyên nhân: memory `project_tps_bottleneck_forwarder_20261010.md`
- Quy tắc dự án: `AGENTS.md` (Zero-Fork Invariant, build verification, KISS)
