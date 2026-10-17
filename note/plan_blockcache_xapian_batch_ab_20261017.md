# Kế hoạch Thực thi & Thực nghiệm A/B Số liệu Thật: Giới hạn `blockCache`, Gom lô CGo Xapian theo Shard (`IndexDocumentsBatch`), và Kiểm chứng Tổng hợp `e26ad843`..`HEAD` — 2026-10-17

- **Ngày lập kế hoạch:** 2026-10-17
- **Nhánh cơ sở hiện tại (`dev`):** `cb4a8d56f75e4cc4aaf5d8db3decc584f150cda2` (đã tích hợp `5004cf0f`, `e26ad843`, PR #171 `b4482040`, và `cb4a8d56`).
- **Mốc đối chứng A/B (`bins_prev`):** `5004cf0f2de1d2f727b15785b20b15585516b7fa` (mốc dòng 22 đã được kiểm chứng trong `note/CLAIMS_LEDGER.md` và `note/readloop_gate_explorer_pacing_report_20261016.md`).
- **Mục tiêu:**
  1. Xử lý triệt để 2 nút thắt lớn nhất còn lại trên Heap Profile (`bc.blockCache` giữ `274.03 MB` / `2,565,540` đối tượng `ExecuteSCResult` suốt 10 phút) và CPU Profile (`_Cfunc_database_index_document` gọi đơn lẻ `10,000` lần/block chiếm `45.14s` CPU).
  2. Chạy thực nghiệm A/B đan xen (1M & 4M tx) và trích xuất `pprof` (`cpu30.pb.gz`, `heap.pb.gz`, `allocs.pb.gz`) bằng **100% số liệu đo thật** để kiểm chứng hiệu quả tổng hợp từ sau `5004cf0f` đến nay (`e26ad843` + `cb4a8d56` + Pha 1 & Pha 2 dưới đây), lưu bằng chứng thô vào `note/evidence/` và cập nhật `note/CLAIMS_LEDGER.md`.

---

## 🚨 QUY TẮC BẮT BUỘC: CHỐNG HOANG TƯỞNG / CHỐNG BỊA SỐ LIỆU (STRICT ANTI-HALLUCINATION PROTOCOL)

Agent thực thi kế hoạch này **BẮT BUỘC** tuân thủ tuyệt đối các quy tắc sau:

1. **100% Số liệu Thật từ File Log/JSON/Pprof Thô:**
   - **NGHIÊM CẤM** tự suy đoán, ước lượng, nội suy, ngoại suy hoặc tự gõ bất kỳ con số TPS, thời gian, CPU (`s` / `%`), bộ nhớ (`MB` / `GB`), số đối tượng, hay `p-value` nào nếu con số đó không được trích xuất trực tiếp bằng lệnh tự động từ file `.json`, `_summary.csv`, `cpu30.pb.gz`, `heap.pb.gz`, `allocs.pb.gz` thực tế sinh ra trong phiên chạy.
   - Mọi tuyên bố trong báo cáo và trong `note/CLAIMS_LEDGER.md` phải gắn nhãn phân loại rõ ràng: `[ĐO]`, `[TÍNH]`, `[ĐỌC CODE]`, `[GIẢ THUYẾT]`, `[CHƯA ĐO]`.
   - Nếu một lượt chạy bị lỗi hoặc một chỉ số không cải thiện (hoặc giảm), **phải báo cáo trung thực 100% con số thực tế đo được** (kèm `t_stat` và `p_two_tailed`), tuyệt đối không chọn lọc hay chỉnh sửa số liệu.
2. **Tuân thủ Tuyệt đối Zero-Fork Invariant (`AGENTS.md` Part 2.5):**
   - Không đụng chạm vào logic đồng thuận BFT hay thứ tự thực thi giao dịch trong block.
   - Mọi lượt chạy A/B trong bộ bằng chứng phải đạt `loss == 0` (`committed_ours == bytes_sent`) và `parity` bắt đầu bằng `"OK: all 4 validators agree on hash+stateRoot"`.
3. **Kiểm tra Build & Race Detector Bắt buộc:**
   - Trước khi chạy benchmark và trước khi commit, phải chạy `go test -race` trên toàn bộ các package sửa đổi và chạy `./consensus/metanode/scripts/build_check.sh` đạt **PASS 5/5 (0 errors, 0 warnings)**.

---

## 🔴 PHA 1: Giới hạn kích thước `blockCache` trong `execution/pkg/blockchain/blockchain.go` (Giải phóng ~250 MB Live Heap & ~2.4M Objects)

### 1.1. Bằng chứng từ Profile hiện tại (`[ĐO]` + `[ĐỌC CODE]`)
- Trên profile bộ nhớ `4M_NEXT_HEAP` tại `t=150s` (`note/evidence/readloop_gate_explorer_pacing_20261016/pprof_comparison.txt:1206-1246`):
  - Nguồn giữ bộ nhớ sống (`inuse_space` và `inuse_objects`) **lớn số 1 toàn hệ thống** là `smart_contract.NewExecuteSCResult` (được tạo từ `processNativeTransfersFastPath.func1` tại `execution/pkg/blockchain/tx_processor/native_fast_path.go:253`), chiếm **`274.03 MB` (`25.70%` tổng live heap)** và **`2,565,540` objects (`27.64%` tổng số object sống)**.
- Nguyên nhân gốc rễ trong code (`execution/pkg/blockchain/blockchain.go:34, 50, 283-294, 313-346`):
  - Mỗi khi một block được commit, `BlockChain.AddBlockToCache(block)` lưu nguyên con trỏ `*block.Block` (bên trong giữ slice `executeSCResults []types.ExecuteSCResult` và `transactions []common.Hash` của `5,000–10,000` giao dịch/block) vào `bc.blockCache` (`*sync.Map`).
  - `blockCacheTTL` đang đặt là `10 * time.Minute` (`blockchain.go:34`) và **không có giới hạn số lượng block tối đa**. Trong một lượt chạy 4M tx (`~400–900` block trong `< 5` phút), toàn bộ 100% số block từ đầu lượt chạy nằm nguyên trong `bc.blockCache`, giữ sống hàng triệu `ExecuteSCResult` trong RAM và buộc Go GC (`runtime.gcDrain`) phải quét qua chúng ở mọi chu kỳ GC.

### 1.2. Thiết kế & Các bước triển khai
1. **Trong `execution/pkg/blockchain/blockchain.go`:**
   - Định nghĩa hằng số giới hạn số lượng block tối đa giữ trong cache:
     ```go
     maxBlockCacheEntries = 64
     ```
     (64 block gần nhất là quá đủ cho mọi truy vấn RPC/Explorer đọc block vừa commit; các block cũ hơn đã nằm sẵn trong `bc.blockDatabase` / PebbleDB).
   - Thay `blockCache *sync.Map` bằng struct `blockCacheMap` có `sync.RWMutex` và `entries map[common.Hash]cachedBlock` (tương tự thiết kế `txCacheMap` đã triển khai tại `execution/pkg/blockchain/tx_cache_map.go` hoặc ngay trong `blockchain.go`):
     - Khi gọi `StoreBlock(hash common.Hash, cb cachedBlock)`: nếu `hash` chưa tồn tại và `len(m.entries) >= maxBlockCacheEntries`, thực hiện quét nhanh trong `m.entries`:
       1. Xóa các mục đã quá hạn `now.Sub(v.addedAt) > blockCacheTTL`.
       2. Nếu vẫn `len(m.entries) >= maxBlockCacheEntries`, tìm và xóa 1 mục có `addedAt` cũ nhất (`oldestHash`), sau đó mới chèn `m.entries[hash] = cb`.
     - Cập nhật `AddBlockToCache`, `GetBlock`, và `pruneBlockCache` sử dụng `blockCacheMap`.
2. **Viết Unit Test kiểm chứng trong `execution/pkg/blockchain/blockchain_test.go`:**
   - Thêm test `TestBlockCache_BoundedCapacity`:
     - Chèn liên tiếp `200` block giả lập vào `bc.AddBlockToCache(...)`.
     - Kiểm chứng số lượng block đang lưu trong `bc.blockCache` **luôn `<= maxBlockCacheEntries` (`64`)**, và các block mới nhất vừa chèn vẫn đọc ra bình thường qua `bc.GetBlock(hash)`.
3. **Chạy kiểm thử Pha 1:**
   ```bash
   go -C execution test -race -v ./pkg/blockchain -run "TestBlockCache|TestTxCache"
   ```

---

## 🟡 PHA 2: Gom lô CGo Xapian theo Shard (`IndexDocumentsBatch`) trong `execution/pkg/goxapian` và `execution/pkg/explorer`

### 2.1. Bằng chứng từ Profile hiện tại (`[ĐO]` + `[ĐỌC CODE]`)
- Trên profile CPU `4M_NEXT_CPU30` (`note/evidence/readloop_gate_explorer_pacing_20261016/pprof_comparison.txt:1063, 1093-1104`):
  - `ExplorerSearchService.doIndexTransaction` chiếm **`53.77s` (`19.88%` tổng CPU)**, trong đó riêng `_Cfunc_database_index_document` chiếm **`45.14s` (`16.69%` tổng CPU, tương đương `51.93%` của toàn bộ `runtime.cgocall`)**, và `sync.(*Mutex).Lock` + `Unlock` chiếm `0.71s`.
- Nguyên nhân gốc rễ (`execution/pkg/explorer/service.go:265-282, 418-429`):
  - 32 worker của `ExplorerSearchService` lấy từng `indexJob` một từ `s.indexQueue`, rồi với **mỗi giao dịch đơn lẻ** lại gọi `s.locks[shardIndex].Lock()` + 1 lệnh CGo `db.IndexDocument(...)` + `s.locks[shardIndex].Unlock()`.
  - Với mỗi block 10,000 giao dịch, hệ thống phải thực hiện **10,000 lần khóa/mở mutex shard và 10,000 lần chuyển ngữ cảnh Go ↔ C++ (`runtime.cgocall`)**.

### 2.2. Thiết kế & Các bước triển khai
1. **Trong `execution/pkg/goxapian/xapian_wrapper.h` và `execution/pkg/goxapian/xapian_wrapper.cpp`:**
   - Bổ sung hàm C `database_index_documents_batch`:
     ```cpp
     bool database_index_documents_batch(
         xapian_database_t db,
         size_t count,
         const char **unique_terms,
         const size_t *unique_term_lens,
         const char **datas,
         const size_t *data_lens,
         const char **terms_packeds,
         const size_t *terms_packed_lens,
         const uint8_t *replace_flags);
     ```
   - Bên trong khối `try { ... } catch (const Xapian::Error &e) { ... } catch (...) { ... }`:
     - Duyệt `for (size_t i = 0; i < count; ++i)`: tạo `Xapian::Document doc`, gán `doc.set_data(...)`, tách các term phân cách bởi `\n` từ `terms_packeds[i]` và gọi `doc.add_boolean_term(std::string(p, len))`, sau đó gọi `wdb->replace_document(...)` (nếu `replace_flags[i] != 0`) hoặc `wdb->add_document(doc)` (nếu `replace_flags[i] == 0`).
2. **Trong `execution/pkg/goxapian/xapian.go`:**
   - Định nghĩa struct:
     ```go
     type BatchDocumentInput struct {
         UniqueTerm      string
         Data            string
         TermsPacked     string
         ReplaceExisting bool
     }
     ```
   - Thêm phương thức `func (db *Database) IndexDocumentsBatch(docs []BatchDocumentInput) error`:
     - Nếu `len(docs) == 0` trả về `nil`; nếu `len(docs) == 1` gọi thẳng `db.IndexDocument(...)`.
     - Với `len(docs) > 1` (giới hạn batch nhỏ ví dụ `<= 128`, có thể dùng mảng stack cố định `[128]*C.char`, `[128]C.size_t`, `[128]C.uint8_t` để **0 cấp phát bộ nhớ trên heap Go**!), điền con trỏ `cStringPtr` cho từng phần tử, gọi `C.database_index_documents_batch(...)` đúng **1 lần**, và gọi `runtime.KeepAlive(docs)`.
3. **Trong `execution/pkg/explorer/service.go`:**
   - Tách phần chuẩn bị dữ liệu không cần lock của `doIndexTransaction(job indexJob)` thành hàm `prepareIndexDoc(job indexJob) (shardIndex int, doc goxapian.BatchDocumentInput, err error)`.
   - Trong `startWorkers` (`service.go:265-282`):
     - Khi một worker nhận được `firstJob, ok := <-s.indexQueue`, worker rút thêm không chặn (`select { case j := <-s.indexQueue: ... default: ... }`) tối đa `63` job nữa (tổng tối đa `64` job/lượt).
     - Nếu chỉ có `1` job: gọi `doIndexTransaction(firstJob)` như cũ (tránh mọi overhead khi tải thấp).
     - Nếu có `> 1` job:
       1. Gọi `prepareIndexDoc` cho toàn bộ các job trong lô **bên ngoài shard lock** (format hex, nối `termsPacked`, marshal JSON hoàn toàn song song giữa các worker).
       2. Gom các `BatchDocumentInput` cùng `shardIndex` (`0..31`) và với mỗi shard có ít nhất 1 tài liệu, chỉ khóa `s.locks[shardIndex].Lock()` **1 lần**, gọi `db.IndexDocumentsBatch(shardDocs)`, rồi `s.locks[shardIndex].Unlock()`.
4. **Viết Unit Test kiểm chứng trong `execution/pkg/goxapian/xapian_test.go` và `execution/pkg/explorer/explorer_test.go`:**
   - Kiểm chứng `IndexDocumentsBatch` với lô nhiều tài liệu (cả `ReplaceExisting: false` và `ReplaceExisting: true`) và kiểm chứng `SearchTransactions` tìm thấy chính xác 100% giao dịch theo `hash:`, `from:`, `to:`, `block:`.
5. **Chạy kiểm thử Pha 2 & Build Check:**
   ```bash
   go -C execution test -race -v ./pkg/goxapian/... ./pkg/explorer/...
   cd consensus/metanode/scripts && ./build_check.sh
   ```

---

## 🟢 PHA 3: Thực nghiệm A/B Đan xen (1M & 4M Tx) với Số liệu Thật & Cập nhật `note/CLAIMS_LEDGER.md`

### 3.1. Chuẩn bị Binary Đối chứng (`bins_prev` vs `bins_next`) và `PREREGISTERED.md`
1. Tạo thư mục bằng chứng mới:
   ```bash
   mkdir -p /home/abc/chain-n/metanode/note/evidence/keccak_blockcache_xapian_batch_20261017
   ```
2. Biên dịch 2 bộ binary thật vào thư mục tạm (ví dụ `/tmp/ab_bins_20261017/bins_prev` và `/tmp/ab_bins_20261017/bins_next`):
   - **`bins_prev`:** Sử dụng `git worktree add --detach /tmp/wt_prev 5004cf0f2de1d2f727b15785b20b15585516b7fa`, biên dịch `simple_chain`, `parent_chain`, `secp_tps_blast` (hoặc copy `parent_chain` và `secp_tps_blast` không đổi và build `simple_chain` từ `/tmp/wt_prev/execution`), sau đó `git worktree remove --force /tmp/wt_prev`.
   - **`bins_next`:** Biên dịch từ `HEAD` mới nhất sau khi hoàn tất Pha 1 và Pha 2.
   - Chạy `sha256sum` trên cả 2 bộ binary và ghi vào file `note/evidence/keccak_blockcache_xapian_batch_20261017/PREREGISTERED.md` **trước khi bắt đầu chạy lượt đo đầu tiên**.

### 3.2. Chạy Thực nghiệm A/B Đan xen (1M & 4M Tx)
1. Sao chép và cập nhật đường dẫn trong driver từ `note/evidence/readloop_gate_explorer_pacing_20261016/driver_ab_readloop.py.txt` sang `note/evidence/keccak_blockcache_xapian_batch_20261017/driver_ab.py.txt`:
   - Kiểm tra điều kiện tài nguyên máy trước mỗi lượt (`MemAvailable >= 20 GB`, `/tmp` trống `>= 100 GB`, không có tiến trình `simple_chain` hay `secp_tps_blast` dư thừa đang chạy).
   - **Ở quy mô 1M tx (`ab_1m`):** 4 cặp đan xen (`prev` vs `next`, tổng 8 lượt), `--profile-at 20` ở Cặp 1 (`ab_1m_prev_p1`, `ab_1m_next_p1`).
   - **Ở quy mô 4M tx (`ab_4m`):** 4 cặp đan xen (`prev` vs `next`, tổng 8 lượt), `--profile-at 150` ở Cặp 1 (`ab_4m_prev_p1`, `ab_4m_next_p1`).
2. Sau khi chạy xong 16 lượt:
   - Kiểm tra bắt buộc cả 16/16 file `*_run1.json` đều có `loss_vs_sent == 0` và `parity_status` bắt đầu bằng `"OK:"`.

### 3.3. Trích xuất Profile (`pprof`) & Tính Thống kê Kiểm định Cặp (`Paired t-test`)
1. Dùng `go tool pprof -symbolize=none` trích xuất đầy đủ bảng Top Cumulative, Top Flat, Peek `runtime.cgocall`, Peek `Explorer/goxapian`, `inuse_space`, `inuse_objects`, `alloc_space`, `alloc_objects` từ các file `cpu30.pb.gz`, `heap.pb.gz`, `allocs.pb.gz` của Cặp 1 (1M và 4M) lưu vào `note/evidence/keccak_blockcache_xapian_batch_20261017/pprof_comparison.txt`.
2. Đối chiếu định lượng 3 cơ chế đã can thiệp từ số liệu thật trong `pprof_comparison.txt`:
   - **Cơ chế 1 (`e26ad843` — `keccakStatePool`, inline tx accessors, bounded `txsCache`, mempool batch allocs):**
     - So sánh `alloc_space` và `alloc_objects` của `golang.org/x/crypto/sha3.NewLegacyKeccak256` / `sigCacheKey` / `TransactionPool.AddTransactions` / `TxValidatorPool.ProcessTransactionsInPoolSub` và CPU của `Transaction.FromAddress` / `Transaction.Hash`.
   - **Cơ chế 2 (Pha 1 — Giới hạn `blockCache` `maxBlockCacheEntries = 64`):**
     - So sánh `inuse_space` và `inuse_objects` của `smart_contract.NewExecuteSCResult` và tổng `inuse_space` / `inuse_objects` trên `heap.pb.gz` ở 4M tx (`t=150s`).
   - **Cơ chế 3 (`cb4a8d56` + Pha 2 — `add_boolean_term` & `IndexDocumentsBatch`):**
     - So sánh CPU (`s` và `%`) của `ExplorerSearchService.doIndexTransaction` / `_Cfunc_database_index_documents_batch` và `runtime.cgocall` trên `cpu30.pb.gz`.
3. Tính thống kê kiểm định cặp (`Mean ± SD`, `Diff Mean ± SD`, `% Change`, `t_stat`, `p_two_tailed` với `df = 3`) từ 4 cặp 1M và 4 cặp 4M, xuất ra `derived_summary.csv` và `MANIFEST.json`.
4. Viết báo cáo chi tiết tại `note/keccak_blockcache_xapian_batch_report_20261017.md` và cập nhật dòng 23 vào `note/CLAIMS_LEDGER.md` (cập nhật luôn mục "Chưa làm" ở cuối `note/CLAIMS_LEDGER.md`).

---

## ✅ Danh sách Kiểm tra Nghiệm thu (Acceptance Criteria Checklist)

- [x] **Pha 1 (`blockCache`):** `go -C execution test -race ./pkg/blockchain/...` PASS 100%, có unit test `TestBlockCache_BoundedCapacity` (chèn 200 block giả lập, xác nhận dung lượng cache `<= 64`, bảo lưu 64 block mới nhất và đẩy các block cũ ra khỏi bộ nhớ).
- [x] **Pha 2 (`IndexDocumentsBatch`):** `go -C execution test -race ./pkg/goxapian/... ./pkg/explorer/...` PASS 100%, có unit test `TestIndexDocumentsBatchAddAndReplace` và `TestExplorerBatchDrainAndMultiShardSearch` (kiểm chứng batch drain gom lô theo shard, giảm CGo overhead và tìm kiếm chính xác 100%).
- [x] **Build Check:** `./consensus/metanode/scripts/build_check.sh` đạt **5/5 PASS (0 errors, 0 warnings) — 55s**.
- [ ] **Pha 3 (Thực nghiệm A/B Số liệu Thật):**
  - [ ] Có `PREREGISTERED.md` với mã SHA256 thật của `bins_prev` (`5004cf0f`) và `bins_next` (`HEAD`).
  - [ ] Cả 16/16 lượt chạy (4 cặp 1M + 4 cặp 4M) đạt `loss == 0` và `parity == OK`.
  - [ ] Có đầy đủ `pprof_comparison.txt`, `derived_summary.csv`, `MANIFEST.json` trong `note/evidence/keccak_blockcache_xapian_batch_20261017/`.
  - [ ] Báo cáo `note/keccak_blockcache_xapian_batch_report_20261017.md` và dòng 23 trong `note/CLAIMS_LEDGER.md` khớp 100% với file dữ liệu thô, không có bất kỳ con số ước lượng/bịa đặt nào.
