# Kế hoạch giao agent: tối ưu PrepareTransactions và FFI serialization (2026-10-08)

**Nguồn:** `note/tps_phase_b_validation_report_20261008.md` mục 3.3–3.4 và bảng xếp hạng mục 5. Báo cáo đó đã sửa đúng số liệu B1 (thật là +1,25%…+3,2%, không đạt ngưỡng) nên giao thức A/B trong `note/evidence/tps_phase_b_validation_20261008/PREREGISTERED.md` là chuẩn để dùng lại.

Đề xuất cần kiểm tra: (1) tối ưu `PrepareTransactions` (loại `sort.Slice`, cache decode) tiết kiệm ~200–280 ms/block, TPS +35–50%; (2) FFI serialization Rust↔Go (flat struct/shared memory) tiết kiệm ~60–90 ms/block, TPS +10–15%.

## 0. Review đề xuất trước khi làm (đã đọc code)

1. **320 ms của `PrepareTransactions` và 122 ms của FFI là phần dư, chưa được đo trực tiếp.** Báo cáo lấy `rust_wait_go − go_busy = 442 ms` rồi chia cho hai nguyên nhân bằng suy luận. Không có đồng hồ riêng cho từng bước của `PrepareTransactions` (grep không thấy). Con số +35–50% TPS vì thế là ước tính, không phải số đo.
2. **`sort.Slice` khó có thể là 200+ ms.** Với 8.000 tx, ~104k phép so sánh `bytes.Compare` trên 32 byte đã cache (`Transaction.Hash()` trả `cachedHash` atomic) thường chỉ vài đến vài chục ms; mỗi lần gọi `Hash()` còn có `defer recover` nhưng không đủ để thành hàng trăm ms. Nghi vấn lớn hơn: vòng dedupe **tuần tự** gọi `transaction.ValidateEnvelopeBinding(tx)` cho từng tx (có thể gồm ecrecover) và `ParallelUnmarshalTransactions` giới hạn ≤16 worker. **Phải đo trước khi chọn việc tối ưu.**
3. **Không được bỏ sort "vì Rust đã sắp xếp".** Thứ tự tx trong block là một phần của kết quả xác định (state root/receipts root). Rust (`block_sending.rs`) có sắp xếp riêng nhằm tránh fork String-vs-Bytes, nhưng Go sắp xếp lại theo TxHash là một bất biến độc lập, và thứ tự cuối cùng ảnh hưởng thứ tự thực thi. Bỏ sort có thể đổi thứ tự → đổi state root → **khác chuỗi hiện có** (cần cutover có phối hợp, wipe/redeploy đồng thời). Chỉ làm được nếu chứng minh được thứ tự đầu vào đã giống hệt thứ tự TxHash tăng dần trong **mọi** trường hợp (kể cả tx do proposer Byzantine chèn, trùng lặp, system tx). Mặc định: GIỮ sort, chỉ làm nó rẻ hơn mà không đổi kết quả.
4. **"Cache cấu trúc decode"** chỉ có lợi nếu cùng một tx được decode nhiều lần giữa mempool và block; cần đo trước. Cache chia sẻ giữa các validator sẽ không tồn tại: mỗi node phải tự verify, không tin cache của proposer.
5. **FFI shared memory/flat struct là thay đổi giao thức Rust↔Go.** Ảnh hưởng `executor/ffi_bridge.go`, `block_delivery.rs`, `block_sending.rs`, proto `ExecutableBlock`, recovery/replay. Rủi ro cao; không làm trước khi (a) đo trực tiếp phần FFI và (b) có thiết kế được duyệt.

## Quy tắc bắt buộc
- Đọc `AGENTS.md` (zero-fork tối thượng, không timeout để dispatch, thà pending chứ không fork), `PROJECT_STRUCTURE.md`. Cập nhật `PROJECT_STRUCTURE.md` nếu đổi giao diện FFI hoặc proto.
- Số liệu phải từ lệnh chạy thật, tự tính thống kê từ file thô, lưu vào `note/evidence/tps_prepare_tx_opt_20261008/` kèm `MANIFEST.json` (sha256). Ghi `PREREGISTERED.md` (tiêu chí PASS/FAIL, giao thức) và commit **trước** khi chạy.
- Không thay đổi kết quả thứ tự/hash/state root của block so với hiện tại. Mọi tối ưu phải cho **đầu ra bit-for-bit giống** hàm cũ.
- `git add` theo tên file; không push/merge main khi chưa được user duyệt; không squash. Dừng tiến trình theo PID.
- `consensus/metanode/scripts/build_check.sh` sạch lỗi/warning sau mỗi thay đổi code.

## Giai đoạn 1 — Đo trực tiếp (chưa sửa hành vi)
1. Thêm đồng hồ (chỉ quan sát) cho từng bước trong `PrepareTransactions`: unmarshal song song, vòng `ValidateEnvelopeBinding`, dedupe map, `sort.Slice`, tổng. Log nano-giây kèm số tx. Cũng đo phía FFI: Rust serialize, truyền, Go unmarshal `ExecutableBlock` (`[TIMELINE-RUST]`/`[TIMELINE-CGO]` đã có, bổ sung cho phần thiếu).
2. Viết benchmark Go `BenchmarkPrepareTransactions` với 4.000 / 8.000 / 14.000 tx EIP-1559 secp thật (có trùng lặp, một số envelope sai binding), `-benchmem`, `-cpuprofile`. Lấy pprof top (nếu `go tool pprof` crash `mallocgc called without a P`, thử bản Go khác và ghi rõ, không suy luận từ string table).
3. Chạy blast 60 s ≥ 5 lượt trên binary hiện tại để có phân rã trong cụm thật (bỏ block 1–2 cold). Báo bảng ms/block: unmarshal / envelope-binding / dedupe / sort / FFI serialize / FFI deserialize, và phần chưa gán nguồn còn lại.
4. **Cổng quyết định (ghi trước):** chỉ sang Giai đoạn 2 cho bước nào chiếm ≥ 10% chu kỳ block bằng số đo. Cận trên TPS tính bằng Amdahl từ số đo, không dùng ước tính.

## Giai đoạn 2 — Tối ưu `PrepareTransactions` an toàn (theo kết quả Giai đoạn 1)
Mỗi mục một commit riêng, kèm benchmark trước/sau.
- **2a. Sort rẻ hơn, kết quả giống hệt:** lấy khóa `[32]byte` một lần cho mỗi tx rồi sắp xếp theo khóa đó (`slices.SortFunc` hoặc radix/sort theo mảng khóa song song). Vì đã dedupe nên khóa duy nhất → thứ tự tổng tuyệt đối, kết quả y hệt. Không bỏ sort.
- **2b. Song song hóa `ValidateEnvelopeBinding`** nếu số đo cho thấy nó chiếm nhiều: hàm thuần (tx → ok/lỗi), chạy trong worker có giới hạn, **giữ nguyên thứ tự loại bỏ/giữ lại và quy tắc "bản đầu tiên chiếm slot hash"** (chỉ tx hợp lệ mới chiếm slot, giữ thứ tự gốc trước khi dedupe).
- **2c. Tránh cấp phát lặp** (map dedupe cỡ sẵn, slice cỡ sẵn, bỏ `fmt.Printf` mỗi block nếu nằm trên đường găng) khi số đo chứng minh có lợi.
- **Test bắt buộc:** property test so sánh output với hàm gốc trên dữ liệu ngẫu nhiên có seed (trùng lặp, envelope sai, hash trùng khác nội dung, tx rỗng/digest toàn 0, multi-tx digest); `-race`; chạy trên cụm 4 node `-verify-parity` ≥ 5 lượt, block hash + state root khớp tuyệt đối.
- **Đo hiệu quả:** A/B xen kẽ theo `PREREGISTERED.md` của Phase B (binary BEFORE/AFTER, ≥ 8 lượt mỗi bên, thứ tự ngẫu nhiên có seed, wipe cụm giữa lượt, trung vị + CI 95% + Welch t). Chấp nhận nếu trung vị ≥ +5% và CI 95% không chứa 0; dưới ngưỡng thì ghi "không xác nhận".

## Giai đoạn 3 — FFI serialization (chỉ thiết kế, cần duyệt)
Nếu Giai đoạn 1 cho thấy FFI chiếm ≥ 10% chu kỳ: viết `note/design_ffi_serialization_20261008.md` (DRAFT): định dạng mới (flat/zero-copy), sở hữu bộ nhớ qua ranh giới CGO (Go GC không được giữ con trỏ vào bộ nhớ Rust và ngược lại), tương thích ngược khi nâng cấp cụm (cutover đồng thời), tương tác recovery/replay và `CommitVoteMonitor`/`PeerAttestResult`, chiến lược test (fuzz, ASAN/Miri nếu có, chaos `kill -9`). **Không cài đặt khi chưa có phê duyệt bằng văn bản của user.**

## Ngoài phạm vi
Giai đoạn D (gỡ gate `waitCommitted`): báo cáo Phase B đo `gate_wait_ns` trung bình 533 ns nên không còn cơ sở. RSS/heap (xem `note/design_bounded_memory_indexes_20261007.md`). Nâng `MaxBatchTxCount`.

## Tiêu chí hoàn thành
- [ ] `PREREGISTERED.md` commit trước khi chạy
- [ ] Phân rã đo trực tiếp của `PrepareTransactions` và FFI (benchmark + cụm thật), cận trên Amdahl từ số đo
- [ ] Mỗi tối ưu có property test output giống hệt, `-race`, parity 4 node, A/B xen kẽ ≥ 8 lượt/bên
- [ ] Báo cáo kết quả trung thực (kể cả khi không đạt ngưỡng), `build_check.sh` sạch
- [ ] Thiết kế FFI (nếu cần) ở trạng thái DRAFT chờ duyệt
