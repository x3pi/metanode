# Review: PR #27 — "Fix search ram" (`fix-search-ram` → `dev`)

- **Tác giả:** PearTNhat
- **Base / Head:** `dev` (a55477f7) ← `fix-search-ram` (df5e39c0)
- **Phạm vi:** 37 file thay đổi, +940 / −1280 dòng, 21 commit
- **Link:** https://github.com/x3pi/metanode/pull/27

## 0. Kết luận: Sẵn sàng merge chưa?

**Chưa nên merge thẳng — cần xử lý 3 điểm ở mục 3.1–3.3 trước, hoặc merge kèm ghi chú rõ theo dõi sau.**

Lý do:
- Không có bug "chết người" nào chắc chắn (không phát hiện lỗi biên dịch, deadlock chắc chắn, hay mất an toàn dữ liệu 100% chắc chắn từ diff).
- Nhưng có **2 thay đổi nhiều khả năng đi ngược lại chính mục tiêu của PR** (tăng tốc/giảm tranh chấp search) — mục 3.1 và 3.2 — và **1 thay đổi bỏ lớp bảo vệ dữ liệu dùng chung** (mục 3.3) mà không có test nào xác nhận an toàn.
- **Không có test mới** cho toàn bộ phần logic concurrency phức tạp vừa sửa (pool 4 slot, generation counter, TOCTOU fix, registry dùng chung) — trong khi chính lịch sử commit của branch này cho thấy khu vực này đã bị sửa đi sửa lại nhiều lần vì race condition (`f5daf613`, `52b12219`, `4e5e6fcc`).
- Có 1 file rác `deploy/ansible/temp.md` chứa IP nội bộ thật, có vẻ commit nhầm, nên loại khỏi PR.

**Đề xuất:** merge được nếu tác giả xác nhận đã test tải thực tế (stress test) cho `XapianManager` sau đổi này, hoặc sửa nhanh 3.1 (revert về `shared_lock`) trước khi merge — đây là thay đổi rẻ và an toàn nhất để loại bỏ rủi ro lớn nhất.

## 1. Tổng quan PR làm gì

PR này gộp nhiều thay đổi không hoàn toàn liên quan dưới tên "search RAM":

1. **Viết lại concurrency/memory cho Xapian** (phần lớn diff): thay cơ chế `read_db` (1 con trỏ dùng chung) bằng một **pool cố định 4 slot** `Xapian::Database` cho mỗi `XapianManager` (`acquireSearchDb`/`releaseSearchDb`), thêm bộ đếm `db_generation` để `reopen()` các slot cũ sau khi commit, đổi `tx_buffers_mutex` từ `shared_mutex` sang `mutex` thường, và sửa race TOCTOU trong `destroyInstance()` (kiểm tra idle + xoá khỏi map giờ atomic trong cùng 1 lock).
2. **Theo dõi storage-read**: thêm `MapStorageRead` / `addresses_storage_read` xuyên suốt từ C++ EVM (`MyGlobalState`) → C ABI (`ExecuteResult`) → Go (`MVMExecuteResult`, `ExecuteSCResult`) — hiện chỉ mới **sinh ra dữ liệu, chưa thấy nơi nào tiêu thụ** trong diff này.
3. **Bảo vệ ABI decode**: thêm kiểm tra biên an toàn tràn số (`abiOutOfBounds`) cho mọi hàm `decodeXxx` trong `abi_decode.hpp`, chặn đường đọc-ngoài-vùng-nhớ (OOB read) từ offset/length trong calldata do kẻ tấn công kiểm soát.
4. **Xoá dead code**: xoá `processSingleGroup` và 5 `sync.Pool` liên quan trong `tx_executor.go` (767 dòng). Đã xác minh bằng `git grep` là hàm này **không có nơi nào gọi**, kể cả trước PR này — nghĩa là code đã chết từ trước, xoá là an toàn.
5. **Chỉnh Block-STM**: thay channel có buffer cố định `execCh`/`validateCh` bằng cơ chế hàng đợi không giới hạn (`unbounded_queue.go`) để tránh nghẽn producer khi có "bão abort", kèm log đếm debug (exec/validate/abort mỗi 10.000 lần).
6. **Registry trong `nomt_state_trie.go`**: đổi cache registry key theo namespace từ "clone mỗi lần đọc" sang **1 con trỏ `*sharedRegistry` dùng chung** cho mọi `NomtStateTrie` cùng `(dbPath, namespace)` — đây mới thực sự là phần fix RAM cho "search".
7. **Bỏ bớt commit Xapian**: `CommitAllXapian()` trong `block_processor_commit.go` giờ chỉ gọi khi block có ít nhất 1 giao dịch tương tác contract thành công, thay vì gọi vô điều kiện mỗi block.
8. Linh tinh: thêm cờ debug build C++ và core dump cho ansible deploy, xoá `std::cerr` debug thừa, và đưa `run()` vào trong khối `try` ở `mvm_linker.cpp` (xem mục 3.5).

## 2. Chất lượng code & style

- Comment tiếng Việt trong phần C++ khá chi tiết, giải thích rõ *lý do* (đặc biệt phần TOCTOU/generation counter trong `xapian_manager.h`/`.cpp`) — tốt.
- Nhiều `std::cerr` debug đã bị dọn ở các đường hot path (`processResult`, `commitBufferForTxHash`), hợp lý với mục tiêu RAM/perf của PR, nhưng `XAPIAN_QUERY_SEARCH` vẫn còn in từng dòng kết quả ra `std::cerr` — nên dọn nốt nếu đường này thực sự chạy nhiều.
- `deploy/ansible/temp.md` (44 dòng) trông như một file inventory Ansible nháp bị commit nhầm, chứa IP nội bộ thật (`192.168.1.230/233/234`). Không có role/playbook nào tham chiếu tới nó — nên xoá khỏi PR (và thêm vào `.gitignore`, giống cách `.gitignore` đã loại `note/commit_review_*.md`).
- `tx_processor.go`: sau khi xoá các pool và struct `groupResultExt`, diff để lại 2 khối `(...)` rỗng thay vì xoá hẳn — không gây lỗi nhưng hơi luộm thuộm.
- Không có test tự động mới đi kèm — xem mục 4.

## 3. Các vấn đề về tính đúng đắn

### 3.1 `XapianManager::getInstance` — nhánh nhanh giờ bị serialize mọi lần gọi (Mức độ: Trung bình)
`execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp:76-82`

```cpp
{
    std::unique_lock<std::shared_mutex> read_lock(instances_mutex);   // trước đó là std::shared_lock
    auto it = instances.find(db_path_str);
    if (!isReset && it != instances.end()) {
        if (it->second) it->second->touch();
        return it->second;
    }
}
```
Khối này chỉ đọc map và gọi `touch()` (hàm này đã tự có `access_mutex` riêng, nên vốn đã an toàn khi chạy dưới `shared_lock`). Việc đổi sang `unique_lock` biến nhánh phổ biến nhất ("manager đã tồn tại rồi") — chạy trên **mọi thao tác Xapian, không trừ cái nào** (`getInstance` được gọi ở đầu `FullDatabase` cho mọi opcode) — thành 1 vùng khoá độc quyền (exclusive) cho toàn bộ caller EVM/search đang chạy song song. Fix TOCTOU thực sự mà PR nhắm tới nằm ở `destroyInstance(..., onlyIfIdle=true)`, hàm này đã tự kiểm tra lại idle/refcount dưới `unique_lock` riêng của nó; một `shared_lock` ở đây vẫn sẽ loại trừ lẫn nhau (mutually exclusive) với `unique_lock` đó, nên đổi lại thành `shared_lock` ở khối này nhiều khả năng vẫn an toàn. Như hiện tại, thay đổi này có vẻ đi ngược lại chính mục tiêu PR (tăng concurrency cho search).

**Đề xuất:** đổi lại thành `std::shared_lock` — đây là thay đổi rẻ, rủi ro thấp, nên làm trước khi merge.

### 3.2 Pool search cố định 4 slot giờ cũng chặn cả các lệnh đọc document đơn giản (Mức độ: Trung bình)
`execution/pkg/mvm/linker/src/xapian/xapian_manager.cpp` (`get_data`, `get_value`, `get_document`, `get_terms`) + `xapian_handlers.cpp` (`XAPIAN_QUERY_SEARCH`)

Trước đây `get_data`/`get_value`/`get_document`/`get_terms` dùng `db`/`read_db` riêng của manager, không hề tranh chấp với các lệnh full-text search. Giờ tất cả các hàm này đều đi qua `ScopedSearchDb` → `acquireSearchDb()`, tranh chấp chung 1 pool `MAX_CONCURRENT_SEARCHES = 4` với các lệnh `XAPIAN_QUERY_SEARCH` thực sự. Khi tải cao (nhiều tx EVM chạy song song, mỗi tx chỉ gọi `get_data` đơn giản, cộng thêm 1 block có nhiều lệnh search), `acquireSearchDb()` có thể block tối đa 5 giây rồi **âm thầm trả về `nullptr`**, lúc đó `get_data`/`get_value`/`get_terms` trả về `""`/rỗng và `get_document` trả về `DocumentInfo` rỗng — **không thể phân biệt được với "document không tồn tại"**. Đây là rủi ro về tính đúng đắn (không chỉ hiệu năng): một lệnh đọc contract bị timeout do tải cao sẽ trông y hệt như đọc miss thật.

**Đề xuất:** tăng kích thước pool theo mức độ tải kỳ vọng, hoặc tách riêng đường đọc 1-document đơn giản khỏi pool (không dùng pool, hoặc dùng pool riêng lớn hơn) để không bị đói tài nguyên vì các lệnh search song song.

### 3.3 `nomt_state_trie.go`: registry dùng chung bỏ mất bản copy phòng vệ (Mức độ: Trung bình — cần xác minh thêm)
`execution/pkg/trie/nomt_state_trie.go` — `getSharedRegistry` / `AlignWithExpectedRoot`

Cache registry đổi từ "clone map mỗi lần đọc" sang **1 con trỏ `*sharedRegistry` dùng chung** cho mọi `NomtStateTrie` cùng `(dbPath, namespace)` — đây đúng là phần fix RAM, và về nguyên tắc là hợp lý (tránh clone O(n) mỗi lần mở trie). Nhưng trong `AlignWithExpectedRoot`, đoạn từng copy phòng vệ từng key:

```go
// trước
keyCopy := make([]byte, len(origKey))
copy(keyCopy, origKey)
knownKeys[hexKey] = keyCopy
// sau
knownKeys[hexKey] = origKey
```

giờ lưu trực tiếp tham chiếu vào byte slice gốc của registry dùng chung. Vì `reg.keys` được chia sẻ giữa mọi instance trie cho cùng namespace (có thể xuyên suốt nhiều block/fork/retry), bất kỳ đoạn code nào phía sau mutate byte slice trả về từ `AlignWithExpectedRoot` **tại chỗ** sẽ âm thầm làm hỏng dữ liệu registry dùng chung cho tất cả mọi nơi khác đang dùng nó. Nên rà soát nhanh các nơi tiêu thụ `knownKeys` để chắc chắn không có chỗ nào mutate byte slice, vì kiểu lỗi này (dữ liệu dùng chung bị hỏng chéo giữa các request) nghiêm trọng hơn nhiều so với lượng RAM tiết kiệm được.

**Đề xuất:** audit các nơi dùng `knownKeys` từ hàm này, hoặc giữ lại copy phòng vệ ở riêng điểm này (chi phí không lớn vì chỉ 1 loop, không phải toàn bộ cache).

### 3.4 `commitBufferForTxHash`: copy thay vì move, erase có điều kiện (Mức độ: Thấp/Thông tin)
`execution/pkg/mvm/linker/src/xapian/xapian_registry.cpp:98-134`

Buffer giờ được **copy** (`buffer_logs = it->second.xapian_doc_logs;`) thay vì move-và-xoá atomic như trước, và `tx_buffers`/`tx_counters` chỉ bị xoá bên trong `if (!buffer_logs.empty())`. Mục đích nêu ra ("giờ mới an toàn để xoá... vì read_db đã có dữ liệu") là một fix hợp lý cho khoảng hở về visibility trong lúc `replay_log()` chạy. Hai tác dụng phụ nhỏ cần lưu ý: (a) tốn gấp đôi bộ nhớ tạm thời cho log trong lúc commit (ngược nhẹ với mục tiêu RAM của PR, dù chỉ tạm thời), và (b) nếu có khả năng tồn tại 1 entry `tx_buffers` với danh sách `xapian_doc_logs` rỗng, nó sẽ không bao giờ bị xoá qua đường này và rò rỉ dần trong map. Kiểm tra các nơi ghi vào map thì thấy entry chỉ được tạo cùng lúc với `push_back`, nên (b) hiện tại có vẻ không xảy ra được — nêu ra để lưu ý nếu sau này có code path khác phá vỡ giả định đó.

### 3.5 Điểm tốt: `run()` giờ đã nằm trong try/catch (Tích cực)
`execution/pkg/mvm/linker/src/mvm_linker.cpp` (`call`, `execute`)

Trước đây `run(...)` được gọi **trước** khối `try`, nên nếu bản thân `run()` ném exception (chính codebase có comment ở nơi khác: "Throwing across CGO boundary causes Go runtime to crash with SIGSEGV/SIGABRT"), exception sẽ văng thẳng qua biên CGO. Đưa lời gọi `run(...)` vào trong `try` đã bịt lỗ hổng này. Đây là một fix an toàn-crash thực sự, đáng ghi nhận vì nằm lẫn trong một hunk diff trông có vẻ chỉ là cosmetic.

### 3.6 Điểm tốt: bounds-check cho ABI decode (Tích cực)
`execution/pkg/mvm/linker/include/abi_decode.hpp`

`abiOutOfBounds(start, need, totalLength)` cộng đúng trên `uint64_t` trước khi so sánh, bịt được lỗ hổng tràn số của các check cũ kiểu `i + 32 > totalLength` / `offset + 32 + length > totalLength` (một offset/length do kẻ tấn công kiểm soát gần `UINT32_MAX` có thể wrap-around và vượt qua check, dẫn tới đọc ngoài vùng nhớ trên calldata). Tất cả các hàm `decodeXxx` đã được cập nhật đồng bộ.

## 4. Test coverage

Không có file test nào bị đụng tới trong diff này. Với phạm vi thay đổi (pool DB 4 slot mới kèm cơ chế invalidation theo generation, fix TOCTOU trong việc huỷ instance, registry key dùng chung có thể mutate, và hàng đợi nội bộ không giới hạn trong Block-STM), toàn bộ logic nhạy cảm về concurrency này hiện **không có test tự động nào bao phủ** — chỉ được kiểm chứng qua quan sát thủ công/production. Lịch sử commit của chính branch này cho thấy khu vực này đã bị sửa lại nhiều lần vì race condition (`f5daf613`, `52b12219`, `4e5e6fcc`) — nên có 1 bài test chịu tải (stress test) cho pool search + generation counter trước khi merge thêm thay đổi lên trên khu vực này.

## 5. Hiệu năng

- Thiết kế pool search + generation counter là hướng đi hợp lý để tránh nghẽn cổ chai của cơ chế `read_db` cũ khi search full-text thật sự nhiều, nhưng xem mục 3.1/3.2 — 2 thay đổi ảnh hưởng nhiều nhất đến đúng mục tiêu PR (RAM/perf cho search) có thể triệt tiêu lẫn nhau khi tải cao.
- `unbounded_queue.go` đánh đổi bộ nhớ goroutine-local tăng không giới hạn để loại bỏ tình trạng nghẽn producer khi Block-STM abort dồn dập. Vì độ dài hàng đợi bị chặn trên bởi `numTxs` mỗi block, đây là đánh đổi an toàn trong thực tế.
- `CommitAllXapian()` giờ bỏ qua với block không có tương tác contract thành công nào — một cải thiện hợp lý, rủi ro thấp, nhất quán với logic bỏ-qua-buffer-theo-tx đã có sẵn.

## 6. Bảo mật

- Fix tràn số cho ABI decode (mục 3.6) là một cải thiện bảo mật thực sự — có lẽ là fix quan trọng nhất trong PR này.
- `deploy/.../metanode-execution.service.j2` giờ set `LimitCORE=infinity` và bật `--pprof-addr=127.0.0.1:606{{ item }}`. pprof chỉ bind localhost nên rủi ro thấp. Core dump không giới hạn trên node production (validator/execution) là đánh đổi hợp lý để debug, nhưng nên đảm bảo ops nắm rõ — 1 lần crash trên node dùng nhiều RAM có thể tạo ra core file rất lớn (và core dump có thể chứa private key nằm trong bộ nhớ tiến trình lúc đó), nên giới hạn quyền truy cập thư mục chứa core dump.
- `deploy/ansible/temp.md` để lộ IP LAN nội bộ mức độ thấp (địa chỉ RFC1918 riêng tư) nhưng vẫn nên dọn khỏi lịch sử/PR vì lý do vệ sinh code.

## 7. Tổng kết

**PR có giá trị thực chất** — riêng phần bounds-check ABI decode và fix an toàn-exception cho `run()` đã đáng để merge độc lập. Thiết kế lại pool Xapian/generation-counter và registry trie dùng chung là hướng tiếp cận hợp lý cho vấn đề RAM, nhưng 2 thay đổi (3.1 dùng `unique_lock` ở đường hot path, 3.2 pool dùng chung làm đói các lệnh đọc document đơn giản) có vẻ đang làm giảm chính lợi ích về concurrency mà PR này hướng tới, và 3.3 bỏ đi lớp bảo vệ quanh dữ liệu dùng chung đáng để xem lại. Không có phát hiện nào trong số này là lỗi phá vỡ tính đúng đắn mà tôi có thể khẳng định chắc chắn 100% chỉ từ diff — nhưng đây đúng là kiểu lỗi rất dễ mắc phải một cách tinh vi, giống hệt như những gì lịch sử commit của chính branch này (nhiều lần sửa TOCTOU/race liên tiếp) cho thấy đã từng xảy ra ở file này.

**Khuyến nghị:** làm 1 bài test chịu tải nhanh cho `XapianManager` trước khi merge, và quyết định có loại `deploy/ansible/temp.md` khỏi PR hay không. Nếu thời gian gấp, ít nhất nên sửa mục 3.1 (đổi lại `shared_lock`) trước khi merge vì đây là thay đổi rẻ và rủi ro thấp nhất để loại bỏ.
