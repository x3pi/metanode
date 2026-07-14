# Review vòng 3: PR #27 — "Fix search ram" (`fix-search-ram` → `dev`)

- **Ngày review:** 2026-07-14
- **Tác giả PR:** PearTNhat
- **Base / Head:** `dev` (1684f38e) ← `fix-search-ram` (**8cfaa8a5**)
- **Phạm vi:** 40 file, +1091 / −1333 dòng
- **Link:** https://github.com/x3pi/metanode/pull/27
- **Review trước:** [vòng 1](pr_27_review_fix_search_ram.md) · [vòng 2](pr_27_review_round2_2026-07-14.md)

## 0. Kết luận: Sẵn sàng merge chưa?

**GẦN SẴN SÀNG — chỉ còn 1 việc bắt buộc rất nhỏ: xóa 4 file rác `test_decl*` (trong đó có 2 file binary `.o`) bị commit nhầm.** Sau khi dọn xong, PR đủ điều kiện merge; các điểm còn lại ở mục 3 là nên-sửa hoặc theo-dõi-sau, không chặn.

Trạng thái then chốt:
- **Conflict với `dev` đã hết** — GitHub báo `mergeable: true / clean`.
- **Blocker vòng 2 được resolve đúng hướng khuyến nghị**: giữ bản TrueBlockSTM sequential + segment của `dev` (`1684f38e`, kèm 3 file test) làm nền, và **port unbounded queue của PR vào `runParallelSegment`** — vừa giữ fix của dev, vừa loại được điểm nghẽn channel bounded (`segSize*5`) khi "bão abort". Đây chính là phương án tốt nhất trong 2 phương án vòng 2 nêu ra.
- Toàn bộ các điểm 4.x của vòng 2 đã được xử lý (chi tiết mục 1).
- CI: repo vẫn không có check nào chạy trên PR; chưa có approval trên GitHub.

Kiểm chứng cục bộ tôi đã chạy trên head `8cfaa8a5`:
- `go test ./pkg/blockchain/tx_processor/mvcc/` → **PASS**.
- `go vet ./pkg/trie/` → sạch.
- `go build ./pkg/blockchain/tx_processor/` không chạy được trên máy này vì package `mvm` cần linker C++ đã build (`mvm_linker.hpp` qua CGO) — **các test TrueBlockSTM mới cũng vì thế chưa được chạy ở đây**; cần chạy trên máy build đầy đủ trước khi merge (mục 4).

## 1. Đối chiếu khuyến nghị vòng 2 — kết quả

| # | Vấn đề vòng 2 | Trạng thái ở `8cfaa8a5` |
|---|---|---|
| Blocker | Conflict `true_block_stm.go` với dev | **Resolve đúng**: nền dev sequential, port `runUnboundedQueue` vào `runParallelSegment` (`execIn/execOut`, `validateIn/validateOut`). `unbounded_queue.go` được dùng thật, không còn là dead code. |
| 4.1 | Lỗi Xapian "im lặng trả rỗng" trên đường consensus | **Xử lý phần lớn**: `get_data`/`get_value`/`get_document`/`get_terms` giờ chỉ nuốt `DocNotFoundError`, còn lại `throw`; pool timeout cũng `throw` thay vì trả rỗng. `FullDatabaseV1` outer-catch và mọi điểm `getInstance` fail đều `std::abort()` khi on-chain (fail-stop, kèm `-g` + core dump để truy vết). **Còn 2 gap — xem mục 3.1.** |
| 4.2 | 68 DB handle mở eager mỗi XapianManager | **Đã fix**: cả 2 pool chuyển sang **lazy init** (slot `nullptr`, chỉ `new Xapian::Database` khi lần đầu được acquire). |
| 4.3 | `static_cast` + nhánh fallback dead-code | **Đã fix**: đổi sang `dynamic_cast`, nhánh fallback giờ có ý nghĩa thật. |
| 4.4 | `GetStorageValue_return` định nghĩa lặp 3 nơi | **Đã fix**: đưa vào `mvm_linker.hpp` dưới guard `-DMVM_LINKER_BUILD` (flag thêm vào cả 2 chế độ build CMake), xóa 3 bản khai báo tay. |
| 4.6 | Rác Rust/CMake | **Đã fix phần chính**: `block_verifier.rs` xóa hẳn timer chết + 2 khối `if {}` rỗng; `block_manager` xóa timing code; `c_mvm/CMakeLists.txt` bỏ xung đột `-Os`/`-O3`, giữ `-O3 -g` **có comment giải thích chủ đích** (giữ symbol cho stack trace khi fail-stop). |
| — | `commitAllInstances` copy-paste ruột `commit_changes` | **Đã fix**: giờ gọi thẳng `manager->commit_changes()` (hàm này tự lock `changes_mutex` — không double-lock). |

## 2. Nội dung mới của vòng 3 (commit `8cfaa8a5` + merge `3323faaa`)

1. Merge `dev` → resolve conflict TrueBlockSTM như trên; giữ nguyên 3 file test mới của dev (`mvcc_memory_test.go`, `true_block_stm_cascade_test.go`, `true_block_stm_integration_test.go`).
2. Chiến lược **fail-stop on-chain** cho Xapian: `std::abort()` tại ~19 điểm lỗi `getInstance` + outer-catch của `FullDatabaseV1`; offchain vẫn trả lỗi mềm (`Code(32,0)`) — phân biệt đúng giữa đường consensus và đường RPC.
3. Lazy init 2 pool DB; đơn giản hóa logic acquire (check `nullptr` → tạo mới → reopen theo generation).
4. `-O3 -g -DMVM_LINKER_BUILD` cho cả release/debug, comment giải thích rõ.

## 3. Vấn đề còn lại

### 3.1 Nên sửa (trong PR này hoặc follow-up ngay): fail-stop chưa phủ hết 2 chỗ
- **Outer-catch của `FullDatabase` (V0)** vẫn nuốt mọi exception và trả `Code(32,0)` kể cả on-chain — trong khi `FullDatabaseV1` đã abort. Các `throw` mới từ `get_data`/pool-timeout đi qua opcode V0 sẽ lại rơi về "im lặng trả rỗng" đúng kiểu lỗi mà vòng này định diệt. Nếu opcode V0 còn được contract on-chain sử dụng, nên thêm cùng khối abort như V1 (copy 6 dòng).
- **`XapianSearcher::search`** vẫn tự nuốt `Xapian::Error` bên trong và trả kết quả rỗng (`return {results, 0}`) — lỗi I/O thật trong search không bao giờ chạm tới outer-catch của handler, nên đường full-text search on-chain vẫn có thể lệch kết quả giữa các node thay vì fail-stop. (Retry `DatabaseModifiedError` thì đúng và nên giữ.) Cân nhắc: on-chain → ném tiếp sau khi hết retry để handler abort; offchain → giữ như hiện tại.

### 3.2 Bắt buộc dọn: file rác commit nhầm
`execution/pkg/mvm/linker/test_decl.cpp`, `test_decl2.cpp`, **`test_decl.o`, `test_decl2.o`** — file thí nghiệm khai báo struct (2 dòng mỗi file) kèm **object file binary** đã compile. Không được để binary `.o` vào repo. Xóa 4 file (và cân nhắc thêm `*.o` vào `.gitignore`).

### 3.3 Ghi nhận vận hành (không cần sửa code)
- **Fail-stop có chủ đích = node có thể tự crash dưới tải/lỗi đĩa** (ví dụ acquire pool timeout 5s → throw → abort on-chain). Đây là trade-off đúng cho blockchain (thà chết hơn fork), và đã có `LimitCORE=infinity` + `GOTRACEBACK=crash` + `-g` để chẩn đoán — nhưng ops cần được báo trước hành vi mới này, và nên có alert khi node restart bởi SIGABRT.
- Edge case nhỏ ở shutdown `runParallelSegment`: các điểm push `execIn <- txIndex` / `validateIn <- txIndex` trong `execOne`/`validateOne` là bare-send; nếu context bị cancel đúng lúc buffer (`segSize`) đầy và goroutine `runUnboundedQueue` đã thoát, worker có thể kẹt ở send → `wg.Wait()` treo. Xác suất rất thấp (drainer chạy liên tục), fix rẻ là bọc `select { case ch <- x: case <-ctx.Done(): }`. Ghi nhận để theo dõi, không chặn merge.
- `MapStorageRead` vẫn chưa có consumer phía Go (như vòng 2 đã nêu) — chấp nhận nếu là chuẩn bị cho conflict-detection của TrueBlockSTM mới; nên nói rõ trong PR description.

## 4. Checklist merge

1. [ ] **Xóa 4 file `test_decl*`** (bắt buộc — việc duy nhất còn chặn).
2. [ ] (Nên, 10 phút) Thêm abort on-chain vào outer-catch của `FullDatabase` V0 cho nhất quán với V1.
3. [ ] Trên máy build đầy đủ: build linker C++ cả 2 chế độ (`build_release.sh` và `--debug-cpp`) + `go build ./... && go test ./pkg/blockchain/tx_processor/...` — đặc biệt 2 file test TrueBlockSTM mới phải pass trên code sau merge (ở máy review này không chạy được vì thiếu CGO linker; MVCC tests đã pass).
4. [ ] (Theo dõi sau) 3.1-search fail-stop, 3.3 bare-send khi cancel, consumer cho `MapStorageRead`.

## 5. Tổng kết

Vòng 3 xử lý **tất cả** các điểm của vòng 2, và quan trọng nhất là resolve conflict theo đúng phương án tối ưu (giữ kiến trúc mới của dev, port unbounded queue vào thay vì vứt bỏ). Chuỗi 3 vòng review cho thấy PR đã hội tụ: từ 3 vấn đề thiết kế concurrency (vòng 1) → 1 blocker cơ học + 6 điểm vừa/nhỏ (vòng 2) → còn 1 việc dọn file rác + 2 gap nhỏ về độ phủ fail-stop (vòng 3). **Sau khi xóa `test_decl*` và chạy được bộ test TrueBlockSTM trên máy build đầy đủ, khuyến nghị approve & merge.**
