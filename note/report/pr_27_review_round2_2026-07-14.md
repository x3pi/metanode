# Review vòng 2: PR #27 — "Fix search ram" (`fix-search-ram` → `dev`)

- **Ngày review:** 2026-07-14
- **Tác giả PR:** PearTNhat
- **Base / Head:** `dev` (1684f38e) ← `fix-search-ram` (2004aa07)
- **Phạm vi:** 36 file, +1010 / −1280 dòng
- **Link:** https://github.com/x3pi/metanode/pull/27
- **Review vòng 1:** [pr_27_review_fix_search_ram.md](pr_27_review_fix_search_ram.md) (review tại head `df5e39c0`)

## 0. Kết luận: Sẵn sàng merge chưa?

**CHƯA. Có 1 blocker cứng: PR đang conflict với `dev` (GitHub báo `mergeable: false / dirty`).**

File conflict duy nhất (xác minh bằng `git merge-tree origin/dev origin/fix-search-ram`):

```
execution/pkg/blockchain/tx_processor/true_block_stm.go
```

Nguyên nhân: sau khi PR merge `dev` lần cuối (tại `f90773e2`), `dev` có thêm commit `1684f38e` **viết lại toàn bộ TrueBlockSTM** sang mô hình sequential + segment (kèm 3 file test mới, +1473 dòng), trong khi PR sửa chính file đó (thay channel bounded bằng unbounded queue + thêm đếm exec/abort). Hai thay đổi đè lên nhau và không thể auto-merge.

Ngoài blocker trên, phần còn lại của PR (Xapian pool, registry trie, ABI bounds-check, fix `isDebug`) **đã ở trạng thái khá tốt** — tác giả đã xử lý cả 3 điểm chính của review vòng 1 (xem mục 2). Các vấn đề còn lại ở mục 4 là mức trung bình/thấp, có thể sửa trong PR này hoặc theo dõi sau.

**Trạng thái khác:**
- CI: repo không có check nào chạy trên commit head (`total_count: 0`) — không có tín hiệu build/test tự động.
- Chưa có review/approval hay comment nào trên GitHub.

## 1. Hướng xử lý conflict (quan trọng nhất)

Khi merge `dev` vào `fix-search-ram`, với `true_block_stm.go` khuyến nghị **lấy phiên bản của `dev`** (`1684f38e`) làm nền, vì:

1. Bản `dev` là bản viết lại mới hơn, có kèm test (`true_block_stm_cascade_test.go`, `true_block_stm_integration_test.go`, `mvcc_memory_test.go`), và sửa lỗi barrier transaction / leader reward.
2. Các thay đổi của PR trong file này (unbounded queue + debug counter) là cải tiến cho kiến trúc cũ.

**Nhưng lưu ý:** bản `dev` mới trong `runParallelSegment` **vẫn dùng channel bounded** (`make(chan uint32, segSize*5)`) — tức là vấn đề mà PR định fix (nghẽn producer khi "bão abort" làm số lần re-execute vượt buffer) **vẫn tồn tại trên dev** ở dạng mới. Vì vậy:

- `unbounded_queue.go` (file mới của PR, không conflict) nên được **giữ lại và port vào `runParallelSegment`** của bản dev, hoặc
- Nếu quyết định không port ngay, phải **xóa `unbounded_queue.go`** khỏi PR để không để lại dead code, và mở issue theo dõi.

Đây là quyết định của tác giả/maintainer — không nên resolve máy móc.

## 2. Tiến triển so với review vòng 1 — cả 3 điểm chính đã được xử lý ✔

| Vấn đề vòng 1 | Trạng thái ở head hiện tại (`2004aa07`) |
|---|---|
| 3.1 `getInstance` dùng `unique_lock` ở fast path | **Đã fix** — quay lại `shared_lock` + double-checked locking với `unique_lock` chỉ khi phải tạo instance (qua commit `0f15b97a` trên dev, đã merge vào PR). |
| 3.2 Đọc document đơn giản tranh chấp pool search 4 slot | **Đã fix** — thêm `SimpleReadDbPool` riêng 64 slot cho `get_data`/`get_value`/`get_document`/`get_terms`, tách khỏi `SearchDbPool` 4 slot của full-text search (commit `2004aa07`). |
| 3.3 Registry dùng chung mất defensive copy trong `AlignWithExpectedRoot` | **Đã fix một phần** — đoạn ghi ngược vào `n.registry.keys` từ `kvs` đã copy key (`keyCopy`). Đường fallback đọc (`knownKeys[hexKey] = origKey`) vẫn giữ tham chiếu trực tiếp, chấp nhận được vì `knownKeys` là map cục bộ chỉ đọc — miễn là caller không mutate byte slice. |
| File rác `deploy/ansible/temp.md` chứa IP nội bộ | **Đã gỡ** khỏi diff. |

Ngoài ra PR có thêm một **fix bug thực sự** mới phát hiện ở vòng này: trong `transaction_processor_offchain.go`, đường `Deploy` offchain trước đây truyền `executeTransaction.GetReadOnly()` vào đúng vị trí tham số `isDebug` của `MVMApi.Deploy(...)` (đã đối chiếu chữ ký hàm: `..., bTxHash, isDebug, isCache, isOffChain`). PR đổi thành `GetIsDebug()` — đúng ngữ nghĩa.

## 3. Tổng quan thay đổi (tóm tắt)

1. **Xapian concurrency**: bỏ `read_db` shared pointer; thay bằng 2 pool `Xapian::Database` per-manager (4 slot search + 64 slot simple-read) với `db_generation` counter để `reopen()` slot cũ sau commit; `tx_buffers` chuyển sang `unordered_map` + `std::mutex`; fix TOCTOU trong `destroyInstance(onlyIfIdle)`; cleaner idle threshold 5 phút → 1 phút.
2. **Retry `DatabaseModifiedError`** trong `XapianSearcher::search` (reopen + retry tối đa 3 lần), và **không throw exception qua biên CGO** nữa (trả kết quả rỗng thay vì throw).
3. **ABI decode bounds-check** chống tràn số (`abiOutOfBounds` cộng trên `uint64_t`) cho toàn bộ `decodeXxx` — fix bảo mật thật (OOB read từ calldata do attacker kiểm soát).
4. **Storage-read tracking**: pipeline `addresses_storage_read` từ C++ → C ABI → Go (`MapStorageRead`), kèm đọc uncommitted value từ Go qua `GetStorageValue` trong `injectVirtualDependency`.
5. **Xóa dead code**: `processSingleGroup` + 5 `sync.Pool` (~770 dòng) trong `tx_executor.go`.
6. **Registry `nomt_state_trie.go`**: chuyển từ clone-mỗi-lần-đọc sang `*sharedRegistry` dùng chung — phần fix RAM chính.
7. **`CommitAllXapian()` có điều kiện**: chỉ gọi khi block có ≥1 giao dịch contract thành công.
8. **Build/deploy**: cờ `--debug-cpp` xuyên suốt ansible → build_release → CMake (`ENABLE_DEBUG`); `LimitCORE=infinity`, `GOTRACEBACK=crash`, pprof bind `127.0.0.1`; comment-out các log PERF-RUST bên consensus Rust.

## 4. Vấn đề còn lại (không blocker, xếp theo mức độ)

### 4.1 Trung bình — Lỗi Xapian giờ "im lặng trả rỗng" trên đường consensus
`xapian_search.cpp`, `xapian_manager.cpp` (`get_data`/`get_value`/...), `xapian_handlers.cpp`

Trước đây lỗi Xapian trong search sẽ `throw` (crash node — fail-stop). Giờ mọi lỗi (kể cả timeout 5s chờ pool → `nullptr`) đều trả về kết quả rỗng / `""` / `mvm::Code(32,0)`. Không throw qua CGO là đúng (throw cũ gây SIGSEGV/SIGABRT), **nhưng** các opcode search/get_data này chạy **bên trong thực thi EVM đồng thuận**: một node gặp lỗi I/O cục bộ sẽ trả kết quả khác các node còn lại → nguy cơ **lệch state / fork âm thầm**, khó debug hơn nhiều so với crash. Không cần chặn merge, nhưng nên có cơ chế: khi đường on-chain (không phải offchain call) gặp lỗi Xapian → log FATAL + dừng node có chủ đích thay vì trả rỗng.

### 4.2 Trung bình — Footprint 68 DB handle mở sẵn cho mỗi XapianManager
`xapian_manager.cpp` constructor

Mỗi instance mở eager 4 + 64 = 68 `Xapian::Database` (mỗi DB glass mở nhiều file). Với nhiều contract DB cùng lúc → tốn RAM và file descriptor đáng kể — hơi ngược tên PR "fix search ram". Cleaner 1 phút idle có giảm thiểu, nhưng nên chuyển sang **lazy init** (tạo slot khi lần đầu cần) hoặc giảm `MAX_CONCURRENT_SIMPLE_READS` — 64 slot cho thao tác "nhẹ, giữ trong thời gian ngắn" có vẻ quá tay.

### 4.3 Thấp — `static_cast` luôn khác null → nhánh fallback là dead code
`xapian_handlers.cpp` (`injectVirtualDependency`)

```cpp
if (auto my_gs = static_cast<mvm::MyGlobalState*>(gs)) { ... } else { /* fallback */ }
```

`static_cast` con trỏ không bao giờ trả null nếu `gs != nullptr` (đã check ở đầu hàm) — nhánh `else` không bao giờ chạy, và nếu `gs` thực sự không phải `MyGlobalState` thì `static_cast` là UB. Nên dùng `dynamic_cast` (đúng ý đồ "fallback for non-MyGlobalState instances") hoặc bỏ hẳn nhánh else.

### 4.4 Thấp — `struct GetStorageValue_return` định nghĩa lặp 3 nơi
`my_global_state.cpp`, `my_storage.cpp`, `xapian_handlers.cpp` — cùng một struct khai báo tay ở 3 file (1 nơi có `#ifndef` guard, 2 nơi không). Nếu layout lệch nhau sau này sẽ là bug ABI khó tìm. Nên đưa vào 1 header chung.

### 4.5 Thấp — `MapStorageRead` sinh ra nhưng chưa có nơi tiêu thụ
Toàn bộ pipeline C++→Go cho storage-read (marshal/copy qua CGO mỗi lần execute) hiện chỉ dừng ở getter/setter của `ExecuteSCResult` — chưa code nào đọc. Nếu là chuẩn bị cho conflict-detection của TrueBlockSTM bản mới trên dev thì nên nói rõ trong PR description; nếu không, đang trả phí marshal vô ích mỗi transaction.

### 4.6 Thấp — Rác còn sót
- `block_verifier.rs`: xóa log nhưng để lại 2 khối `if ... { }` **rỗng** và biến `num_txs` tính ra không dùng → warning khi build Rust; nên xóa cả khối.
- `block_manager/mod.rs`: log bị comment-out thay vì xóa.
- `tx_processor.go`: khối `var ( ... )` để lại dòng trống sau khi xóa pool.
- `mvm_linker.cpp` dòng ~296: sót lại mảnh comment `//           << std::endl;` lơ lửng.
- `c_mvm/CMakeLists.txt`: thêm `-g` vô điều kiện vào cả release (`-Os -g` rồi lại `-O3`) — có chủ đích cho core dump thì nên comment rõ; cờ `-Os` + `-O3` chồng nhau là vấn đề có sẵn.
- `commitAllInstances()` copy-paste lại ruột của `commit_changes()` thay vì tái sử dụng (khác biệt duy nhất là lock context) — dễ lệch nhau khi sửa sau này.

## 5. Test coverage

- PR **không thêm test nào**. Toàn bộ logic concurrency mới (2 pool + generation counter, TOCTOU destroy, unbounded queue) chỉ được kiểm chứng thủ công.
- Điểm cộng gián tiếp: `dev` (`1684f38e`) đã thêm 3 file test cho TrueBlockSTM/MVCC — sau khi resolve conflict theo hướng mục 1, **các test này phải pass** trên nhánh merge.
- Khuyến nghị giữ nguyên từ vòng 1: một stress test ngắn cho `XapianManager` (N thread search + M thread get_data + commit xen kẽ + destroyInstance) sẽ trả giá trị cao nhất, vì lịch sử branch cho thấy khu vực này sửa race nhiều lần.

## 6. Checklist trước khi merge

1. [ ] **Merge `origin/dev` vào `fix-search-ram`, resolve `true_block_stm.go`** (khuyến nghị lấy bản dev làm nền — xem mục 1).
2. [ ] Quyết định số phận `unbounded_queue.go`: port vào `runParallelSegment` của bản dev, hoặc xóa + mở issue.
3. [ ] Chạy `go build ./... && go test ./execution/pkg/blockchain/tx_processor/...` + build lại linker C++ (`build_release.sh`) sau resolve.
4. [ ] (Nên) Sửa 4.3 (`dynamic_cast`) và dọn 4.6 — đều là sửa 5 phút.
5. [ ] (Nên) Ghi rõ trong PR description: mục đích `MapStorageRead`, và lý do chọn 64 slot simple-read.
6. [ ] (Theo dõi sau nếu không kịp) 4.1 chiến lược fail-stop cho lỗi Xapian on-chain; 4.2 lazy init pool.

## 7. Tổng kết

So với vòng 1, PR đã tiến bộ rõ: cả 3 vấn đề chính đã được xử lý, thêm được fix bug `isDebug` thật và giữ nguyên các giá trị lớn (ABI bounds-check, run() trong try/catch, TOCTOU fix, registry RAM fix). **Rào cản duy nhất mang tính cơ học là merge conflict với `dev` ở `true_block_stm.go`** — nhưng cách resolve có tính quyết định kiến trúc (sequential mới của dev vs unbounded queue của PR) nên cần tác giả xử lý có chủ đích, không auto-resolve. Sau khi resolve + build/test xanh, PR đủ điều kiện merge; các mục 4.x còn lại ở mức nên-sửa, không chặn.
