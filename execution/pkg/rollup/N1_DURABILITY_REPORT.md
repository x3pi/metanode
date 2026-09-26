# 🛡️ Báo Cáo Rà Soát Bền Vững Các Kho Dữ Liệu (N1 Durability Audit Report)

> **Ngày lập:** 2026-09-26  
> **Phiên bản:** 1.0.0 (Baseline Audit)  
> **Trạng thái:** Đã hoàn tất rà soát mã nguồn; benchmark fsync/TPS và nghiệm thu C0 đầy đủ vẫn đang chờ artifact thực nghiệm  
> **Tài liệu tham chiếu:** [N1_N4_IMPLEMENTATION_PLAN.md](./N1_N4_IMPLEMENTATION_PLAN.md), [NEXT_STEPS_PLAN.md](./NEXT_STEPS_PLAN.md)

---

## 1. Tóm tắt kết quả rà soát (Executive Summary)

Sau sự cố ngày 2026-09-24 (lỗi mất bytecode trong kho `smart_contract_code` do dùng Lazy Pebble đệm 5s dẫn đến replay sai hash sau `kill -9`, đã sửa bằng `SyncDurable`), toàn bộ **16 kho dữ liệu** (logical và physical) nằm trên chu trình commit và phục hồi của node đã được rà soát chi tiết trên mã nguồn.

### 🔴 Các phát hiện rủi ro trọng yếu (Critical Findings) & Tình trạng khắc phục:
1. **Lỗ hổng N1-FIX-01 (Mapping Fire-and-Forget — ĐÃ SỬA):**
   - Trước đây tại `execution/pkg/blockchain/blockchain.go:932-938`, hàm `BlockChain.Commit()` đẩy batch mapping (`number -> hash`, `tx -> block`) sang một goroutine chạy nền bất đồng bộ.
   - **Đã khắc phục:** Chuyển `BatchPut` sang thực thi đồng bộ, bổ sung `storage.SyncDurable(bc.storageManager.GetStorageMapping())` kèm failpoint markers (`before-mapping-barrier`, `after-mapping-barrier`), truyền lỗi trực tiếp về caller. Đã kiểm chứng 100% bằng unit test.
2. **Lỗ hổng N1-FIX-02 (Async Persistence & Error Suppression — ĐÃ SỬA):**
   - Tại `commitToMemoryParallel()`, các goroutine bất đồng bộ của `PersistAsync` (account, stake, receipts) đã được chuyển về thực thi đồng bộ inline, trả lỗi trực tiếp về caller.
   - Khi có bất kỳ lỗi nào trong quá trình commit bộ nhớ song song, `createBlockFromResults()` lập tức kích hoạt `revertDraftBlock()` và trả về `nil`, từ chối xuất bản block với trạng thái dở dang.
   - Trong `commitWorker`, bổ sung `ErrChan chan error`, lưu `lastCommitErr`, loại bỏ false-success (không close `DoneChan` khi lỗi), fence / `WaitForPersistence()` trả lỗi chuẩn xác, chặn triệt để các block tiếp theo khi có lỗi commit trước.
3. **Lỗ hổng N1-FIX-04 (Event Log thiếu Durability Barrier — ĐÃ SỬA):**
   - Tại `execution/pkg/smart_contract_db/smart_contract_db.go`, đã bổ sung `storage.SyncDurable(db.dbSmartContract)` kèm failpoint markers `before-sync-durable-events` và `after-sync-durable-events`.
   - Chỉ kích hoạt fsync khi `len(globalEventLogBatch) > 0`, bảo đảm zero-cost với các block không phát sinh event. Đã có 3 unit tests kiểm chứng hành vi sync và error propagation.
4. **Phân định ranh giới vật lý (Physical Domains):**
   - `simple_chain` khởi tạo các kho thành các thư mục cơ sở dữ liệu ShardelDB **hoàn toàn tách biệt vật lý** (`data/blocks`, `data/mapping`, `data/receipts`, `data/smart_contract`, `data/smart_contract_code`...).
   - Do đó, việc gắn `SyncDurable()` được kiểm soát chặt chẽ chỉ ở các ranh giới cần thiết (code deploy, event logs, mapping, block DB), gom barrier trước khi unblock consensus.

---

## 2. Bảng Ma Trận Bền Vững 16 Kho Dữ Liệu (Durability Matrix)

| # | Logical Store | Physical Path / Domain | Backend | Writer Call Site | Scheduling | Referenced By | Commit Point Hiện Tại | Durability Barrier | Error Propagation | Hậu quả nếu mất ghi gần nhất (Loss Impact) | Khả năng Rebuild (Rebuild Path) |
|:---:|:---|:---|:---|:---|:---|:---|:---|:---|:---|:---|:---|
| **1** | **Block DB & `lastBlockHashKey`** | `data/blocks` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `cs.blockDatabase.SaveLastBlock(blk)` trong `CommitBlockState` | Đồng bộ dưới `commitMutex` | Canonical block, parent hash, block height | `CommitBlockState` (bước 3 & 8b) | **CÓ** (`SyncDurable` tại barrier 8b, commit `634b0d39`) | Trả lỗi (`return blockNum, err`) | **Cực nghiêm trọng**: NOMT đi trước BlockDB -> lệch root với tip header -> node treo thoát mã 78 | Re-sync từ peer P2P |
| **2** | **Block-number → Hash Mapping** | `data/mapping` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `bc.SetBlockNumberToHash` -> `bc.Commit()` | **Đồng bộ** (ĐÃ SỬA N1-FIX-01: loại bỏ goroutine) | `GetBlockHashByNumber`, RPC queries, Sequential Guard | Trong `CommitBlockState` | **CÓ** (`SyncDurable` tại `bc.Commit()`) | **ĐÃ SỬA** (Trả lỗi về caller, có unit test) | Mất tra cứu block theo number; RPC trả null; Sequential Guard nhận diện sai | Rebuild được bằng cách quét lại toàn bộ Block DB |
| **3** | **Tx-hash → Block Mapping** | `data/mapping` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `bc.SetTxHashMapBlockNumberBatch` -> `bc.Commit()` | **Đồng bộ** (ĐÃ SỬA N1-FIX-01: loại bỏ goroutine) | `eth_getTransactionByHash`, `eth_getTransactionReceipt` | Trong `CommitBlockState` | **CÓ** (`SyncDurable` tại `bc.Commit()`) | **ĐÃ SỬA** (Trả lỗi về caller, có unit test) | RPC tra cứu tx/receipt trả về null | Rebuild được bằng cách quét lại transactions trong Block DB |
| **4** | **Transaction State** | `data/transaction_state` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `txDB.Commit` trong commit pipeline | Đồng bộ | `TransactionsRoot` trong block header | Trước block barrier | **CÓ** (`SyncDurable` trong `CommitBlockState`) | Trả lỗi, fail-closed | Mất chi tiết execution của tx; lệch khi truy vấn state tx | Re-execute block từ đầu |
| **5** | **Receipts Store** | `data/receipts` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `receipts.CommitPipeline` / `PersistAsync` | **Đồng bộ inline** | `ReceiptsRoot` trong header, RPC receipt | Trước block barrier | **CÓ** (`SyncDurable` trong `CommitBlockState`) | Trả lỗi, fail-closed | Mất receipt; RPC `getTransactionReceipt` lỗi; không verify được receipt root | Re-execute toàn bộ transactions trong block |
| **6** | **Smart Contract Bytecode** | `data/smart_contract_code` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `SmartContractDB.Commit()` (`smart_contract_db.go:579`) | Đồng bộ | `codeHash` trong Account State | Trong `SmartContractDB.Commit()` | **CÓ** (`SyncDurable(codeStorage)` sau deploy) | Trả lỗi (`return err`) | **ĐÃ KHẮC PHỤC**: Trước đây mất code làm replay gọi contract bị revert -> lệch state root | Không thể rebuild nếu mất transaction deploy gốc |
| **7** | **Smart Contract Event Logs** | `data/smart_contract` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `SmartContractDB.Commit()` (`smart_contract_db.go:659`) | Đồng bộ | `eth_getLogs`, RPC filter, Bridge event watcher | Trong `SmartContractDB.Commit()` | **CÓ** (`SyncDurable` khi có logs - ĐÃ SỬA N1-FIX-04) | **ĐÃ SỬA** (Trả lỗi, unit tests PASS 100%) | **ĐÃ KHẮC PHỤC**: Event logs được đồng bộ bền vững ngay khi commit block có phát sinh event | Re-execute contract calls của block |
| **8** | **Contract Storage Trie** | `nomt_db` (khi dùng NOMT) / `data/smart_contract` (khi dùng MPT) | NOMT (Rust engine) / Lazy Pebble | `CommitAllStorage()` và `LateBindRoots()` | Đồng bộ | `StorageRoot` của từng contract trong Account State | Trước khi commit account state | **CÓ** qua cơ chế fsync của NOMT | Trả lỗi (`return err`) | **FORK NGAY LẬP TỨC**: Sai StorageRoot dẫn tới sai AccountStatesRoot | Re-execute block |
| **9** | **Account State Trie** | `nomt_db/account_state` (NOMT) | NOMT (Bitbox/RocksDB) | FFI C++ / Rust `CommitAsync` | Đồng bộ ở ranh giới block | `header.AccountStatesRoot()` | Ngay sau `CommitBlockState` | **CÓ** (NOMT fsync nội bộ) | Trả lỗi qua FFI | **FORK CONSENSUS**: Sai state root của toàn bộ blockchain | Phải rollback hoặc sync snapshot từ peer |
| **10** | **Stake State Trie** | `nomt_db/stake_state` (NOMT) | NOMT (Bitbox/RocksDB) | FFI C++ / Rust `CommitAsync` | Đồng bộ ở ranh giới block | `header.StakeStatesRoot()` | Ngay sau `CommitBlockState` | **CÓ** (NOMT fsync nội bộ) | Trả lỗi qua FFI | **FORK CONSENSUS**: Sai danh sách validator và voting weight | Rollback hoặc sync snapshot từ peer |
| **11** | **Trie Changelog DB** | `data/changelog` (riêng biệt) | Lazy Pebble (5s flush) + Pebble NoSync WAL | `block_processor_network.go:206` | Đồng bộ trước `CommitBlockState` | Trạng thái phục hồi nhanh / Reorg | Trước `CommitBlockState` | **KHÔNG** có `SyncDurable` | Trả lỗi | Mất changelog làm chậm quá trình rollback/reorganize | Rebuild từ block DB |
| **12** | **Backup DB Block Payload** | `backup/chain_backup` | ShardelDB (Lazy Pebble) | `backupWorker` sau block commit | Asynchronous worker queue | Sub-node peer sync, archive | Sau khi worker queue xử lý | **KHÔNG** có sync đồng bộ block | Ghi log worker | Không ảnh hưởng node cục bộ; sub-node sync có thể bị chậm | Ghi lại từ canonical block DB |
| **13** | **Consensus Progress Keys** | File metadata / socket state (`Rust executor_client`) | File nhị phân uvarint / KV store | `persist_last_block_number`, `persist_last_sent_index` | Đồng bộ sau khi Go xác nhận commit | Rust consensus engine | Sau khi Go trả lời Unix socket | **CÓ** (`fsync` file descriptor phía Rust) | Trả lỗi sang consensus loop | **DEADLOCK / FORK**: Nếu progress key đi trước durable block, sau crash node bỏ qua replay block chưa ghi đĩa | Phục hồi từ block height thực tế của Go |
| **14** | **Backup Device Key** | `data/backup_device_key` | Lazy Pebble (5s flush) + Pebble NoSync WAL | `cs.storageManager.CommitDeviceKey` trong `CommitBlockState` | Đồng bộ | Thiết bị ủy quyền off-chain | `CommitBlockState` (bước 5) | **KHÔNG** có `SyncDurable` | Bỏ qua lỗi (`_ = CommitDeviceKey`) | Mất cache key của thiết bị | Query lại từ node ủy quyền |
| **15** | **Explorer Search Index** | `data/explorer_db` (Xapian) | Xapian full-text index | `ExplorerSearchService.IndexBlock` | Asynchronous worker queue | Explorer UI search | Tách biệt hoàn toàn consensus | Xapian flush định kỳ | Ghi log worker | Mất chỉ mục tìm kiếm trên Web UI; canonical chain 100% không ảnh hưởng | Reindex từ canonical block/receipts |
| **16** | **Snapshot `last_block.dat` & Sidecars** | `data/snapshot` / `last_block.dat` | File nhị phân phẳng (Flat Binary File) | Checkpoint Manager / Snapshot Manager | Đồng bộ tại mốc snapshot | Fast node catch-up & recovery | Tại ranh giới epoch/checkpoint | **CÓ** (`fsync` file descriptor trước khi đổi symlink) | Trả lỗi | Snapshot hỏng buộc node phải tải snapshot khác từ peer | Xóa snapshot hỏng và tạo lại từ state hiện tại |

---

## 3. Thứ Tự Thời Gian Thực Tế Của Chu Trình Commit (Execution Timeline)

Dưới đây là thứ tự thực thi chính xác của một block làm biến đổi trạng thái (state-changing block) theo mã nguồn:

```text
[BẮT ĐẦU COMMIT BLOCK]
  │
  ├── 1. SmartContractDB.CommitAllStorage() (Ghi các slot thay đổi vào bộ đệm Trie)
  │
  ├── 2. SmartContractDB.LateBindRoots() (Tính StorageRoot và bind vào Account State)
  │
  ├── 3. SmartContractDB.Commit()
  │       ├── BatchPut(bytecode)
  │       ├── ⚠️ SyncDurable(codeStorage)       <── ĐÃ BẢO VỆ BỀN VỮNG (Commit bytecode khi deploy)
  │       ├── BatchPut(globalEventLogBatch)
  │       └── ⚠️ SyncDurable(dbSmartContract)   <── ĐÃ BẢO VỆ BỀN VỮNG (Commit event logs khi có events)
  │
  ├── 4. txDB.Commit() & receipts.CommitPipeline()
  │       └── Ghi đồng bộ inline qua PersistAsync, fail-closed khi lỗi
  │
  ├── 5. CommitBlockState() (Dưới commitMutex)
  │       ├── SetcurrentBlockHeader() (Cập nhật con trỏ RAM)
  │       ├── AddBlockToCache() (Cập nhật cache block RAM)
  │       ├── SaveLastBlock(blk) (Ghi block vào bộ đệm BlockDB)
  │       ├── SetBlockNumberToHash() & SetTxHashMapBlockNumberBatch()
  │       │     └── ⚠️ BlockChain.Commit() -> ĐỒNG BỘ + SyncDurable(mappingStorage)
  │       ├── storage.UpdateLastBlockNumber(blockNum) (Tăng biến đếm block RAM)
  │       └── ⚠️ BARRIER 8b: storage.SyncDurable(blockDB) <── ĐÃ BẢO VỆ BỀN VỮNG (Block DB)
  │
  ├── 6. FFI: NOMT CommitAsync() (Rust fsync AccountState & StakeState)
  │
  ├── 7. Phát tín hiệu DoneChan & ErrChan (Fail-closed: chỉ báo success khi commit thành công 100%)
  │
  ├── 8. Consensus Progress Keys: Rust lưu last_sent_index & last_block_number
  │
  └── 9. Background Workers: Backup DB Worker & Explorer Search Indexer (Không chặn commit)
[KẾT THÚC COMMIT BLOCK]
```

---

## 4. Phân Biệt Giữa SIGKILL (Process Crash) và Power Loss (Mất điện phần cứng)

Báo cáo này phân định rạch ròi hai cấp độ rủi ro bền vững:

1. **Khả năng chịu đựng `SIGKILL` (Process Crash / Userspace Buffer):**
   - Khi một tiến trình bị `SIGKILL` (`kill -9`), vùng nhớ RAM của process (bao gồm `memoryCache` của Lazy Pebble với chu kỳ flush 5s) bị giải phóng ngay lập tức mà không kịp đẩy xuống đĩa.
   - Do đó, mọi dữ liệu chưa được gọi `flushToDisk()` sẽ **mất 100%**.
   - Các kho có nguy cơ mất mát cao nhất dưới `SIGKILL` trước khi sửa: **Mapping DB** (trước đây do goroutine chưa kịp ghi, nay đã ghi đồng bộ + `SyncDurable`), **Event Logs** (trước đây nằm trong memoryCache 5s, nay đã có `SyncDurable`), **Receipts** (nay đã inline `PersistAsync`).
2. **Khả năng chịu đựng `Power Loss` (Mất điện / Hard Power Cutoff / Kernel Crash):**
   - Khi mất điện đột ngột, toàn bộ **Linux OS Page Cache** bị xóa sổ.
   - Các kho sử dụng `pebble.NoSync` (đã `Write` vào kernel nhưng chưa gọi `fsync()`) sẽ bị **mất toàn bộ dữ liệu chưa sync**.
   - Do đó, việc vượt qua bài kiểm tra `kill -9` **chưa chứng minh được** an toàn trước mất điện. Hệ thống chỉ an toàn trước mất điện khi các kho quan trọng được bảo vệ bằng barrier `SyncDurable()` (gọi trực tiếp `fsync()` WAL của Pebble).

---

## 5. Quy Tắc Gom Durability Barrier (Tránh Lạm Phát Fsync)

Nhằm bảo vệ hiệu năng và ngăn chặn sụt giảm TPS:
- **Tuyệt đối không:** Thêm lệnh `SyncDurable()` rải rác vào từng hàm `Put()` hoặc từng kho riêng rẽ (gây ra 8–10 lần `fsync()` vật lý trên mỗi block).
- **Quy tắc gom barrier:**
  1. Ghi toàn bộ dữ liệu của block (Block, Receipts, Transactions, Mapping, Event Logs) vào các kho tương ứng trước.
  2. Gom cơ chế barrier bền vững vào **một điểm duy nhất** tại ranh giới kết thúc của `CommitBlockState` (ngay trước khi gọi NOMT commit).
  3. Ở các phase tiếp theo, khi chuyển sang mô hình Shared Database (`InitSharedDatabase`), toàn bộ các prefix domain sẽ được fsync cùng lúc chỉ bằng **1 lần gọi barrier duy nhất**.

---

## 6. Kế Hoạch Khắc Phục Kỹ Thuật (Phù hợp Lộ Trình N1-FIX)

Dựa trên bảng ma trận, các PR khắc phục sẽ được chia tách độc lập theo thứ tự ưu tiên:

1. **PR N1-FIX-01 (Mapping Durability & Order — ĐÃ HOÀN TẤT):**
   - Loại bỏ goroutine fire-and-forget tại `execution/pkg/blockchain/blockchain.go:932`.
   - Đưa thao tác `BatchPut` của mapping về luồng đồng bộ trong `BlockChain.Commit()` và bổ sung `storage.SyncDurable(bc.storageManager.GetStorageMapping())`.
   - Bổ sung failpoint markers (`before-mapping-barrier`, `after-mapping-barrier`) và kiểm chứng error propagation bằng unit test `TestBlockChain_Commit_SynchronousAndErrorPropagation`.
2. **PR N1-FIX-02 (Async Persistence & Error Propagation — ĐÃ HOÀN TẤT):**
   - Loại bỏ các goroutine fire-and-forget của `PersistAsync` trong `commitToMemoryParallel()`, chuyển sang thực thi đồng bộ inline và trả lỗi trực tiếp.
   - Thêm cơ chế fail-closed trong `createBlockFromResults()`: kích hoạt `revertDraftBlock()` và trả `nil` khi có bất kỳ task nào trong chu trình commit bộ nhớ thất bại.
   - Thêm cơ chế bảo vệ trong `commitWorker`: hủy bỏ toàn bộ chu trình lưu GEI / ký BLS / DoneChan khi `CommitBlockState` gặp lỗi đĩa.
   - Đã kiểm chứng bằng unit test `TestPersistAsync_ErrorPropagationAndGateUnblock`, `TestBlockProcessor_RevertDraftBlock`, và `TestCommitToMemoryParallel_ReceiptsErrorPropagation`.
3. **PR N1-FIX-03 (Transaction & Receipt Durability — ĐÃ SỬA, CHỜ BENCHMARK):**
   - C0 chứng minh canonical block có thể sống sót sau `SIGKILL` trong khi Transaction State chưa tồn tại; chỉ ghi inline là chưa đủ.
   - Đã thêm `SyncDurable` cho Receipts và Transaction State trước block barrier, có failpoint riêng cho từng kho và truyền lỗi fail-closed.
   - Chi phí hai barrier bổ sung chưa được đo; xem Mục 7.
4. **PR N1-FIX-04 (Event Log Durability — ĐÃ HOÀN TẤT):**
   - Đã bổ sung `storage.SyncDurable(db.dbSmartContract)` kèm failpoint markers `before-sync-durable-events` và `after-sync-durable-events` trong `SmartContractDB.Commit()`.
   - Chỉ kích hoạt fsync khi `len(globalEventLogBatch) > 0`, đảm bảo zero-cost với block thông thường.
   - Đã kiểm chứng 100% bằng 3 unit tests: `TestCommit_SyncsEventLogStorageAfterWritingLogs`, `TestCommit_PropagatesEventLogStorageSyncError`, và `TestCommit_DoesNotSyncEventLogStorageWhenNoLogs`.
5. **PR N1-FIX-05 (Consensus Progress Keys Invariant — ĐÃ BẢO ĐẢM):**
   - Đảm bảo bất biến tiến trình: `updateAndPersistConsensusState` (GEI, CommitIndex) và tín hiệu `DoneChan` chỉ được thực hiện sau khi `CommitBlockState` thành công trọn vẹn.
   - Khi `CommitBlockState` gặp lỗi đĩa, `commitWorker` kích hoạt fail-closed lập tức (bỏ qua cập nhật GEI, hủy phát DoneChan) ngăn ngừa hoàn toàn tình trạng consensus Rust chạy trước block đĩa Go.

### 6.1. Lỗ hổng còn mở do C0 phát hiện (Bàn giao kỹ thuật - Out of Scope cho PR này)

> ⚠️ **THÔNG BÁO BÀN GIAO (HANDOVER NOTE):** Lỗ hổng này **chưa thể xử lý trong phạm vi PR N1/N4 hiện tại** do đòi hỏi thay đổi kiến trúc quản lý session của NOMT handle hoặc cơ chế two-phase commit / state changelog giữa Go và FFI Rust. Lỗi này đã được lập tài liệu đặc tả bàn giao chi tiết tại [HANDOVER_NOMT_CONTRACT_STORAGE_ATOMICITY.md](./HANDOVER_NOMT_CONTRACT_STORAGE_ATOMICITY.md) để bàn giao cho chuyên gia storage/core blockchain xử lý trong một PR riêng.
>
> **Cam kết nguyên tắc Zero-Fork:** Tuyệt đối không nới lỏng test crash matrix để che giấu lỗi; PR N1 giữ nguyên trạng thái `◐` (Chưa đạt / Incomplete) đối với mục tiêu Crash Matrix trọn vẹn.

Lần chạy thực tế ngày 2026-09-26 với 2 round, 5 block đã đi qua kiểm tra determinism nhưng **chưa qua toàn bộ crash matrix**. Tại failpoint `after-mapping-barrier` của block #2, restart từ block #1 rồi replay block #2 cho hash và `AccountStatesRoot` khác lần chạy sạch. Dữ liệu contract storage của block #2 có thể đã bền trước khi canonical block/NOMT account payload hoàn tất, nên replay áp dụng mutation lần hai lên contract storage đã đi trước.

Đây là lỗi atomicity giữa **Contract Storage NOMT** và canonical **Account State/Block DB**, không được che bằng cách nới điều kiện test. Cần một PR riêng cung cấp rollback/rebuild contract storage về root của canonical tip (hoặc commit journal hai pha có recovery) và test hồi quy tại failpoint này. Vì vậy N1 vẫn ở trạng thái **chưa hoàn tất / chưa sẵn sàng merge**, dù các barrier code, mapping, event, receipts và transaction-state đã được bổ sung.

---

## 7. Chi Phí Fsync & Tác Động Hiệu Năng

> **Trạng thái bằng chứng:** **CHƯA ĐO**. Không có raw output, commit SHA và mô tả thiết bị của một lần benchmark trước/sau có thể kiểm chứng trong repository. Vì vậy báo cáo này không công bố số p50/p95/p99 hoặc TPS ước lượng như thể đó là kết quả thực nghiệm. N1 chỉ được đánh dấu hoàn tất sau khi các artifact này tồn tại.

> **Cập nhật 2026-09-26 (đo bởi reviewer, `ci.sh run-now --reset --only tps_blast`, cluster local 4 validator + 1 sync trên một máy, mỗi lần reset cluster):** TPS end-to-end của bài TPS Blast, hai mẫu xen kẽ cho mỗi bên — `dev` (`a21d68da`): **6616** và **6984** tx/s; nhánh PR sau khi merge `dev`: **6268** và **6496** tx/s. Trung bình PR thấp hơn ~6% và thấp hơn ở cả hai cặp xen kẽ, nhưng mẫu nhỏ (n=2) và các lần chạy cùng một bản dao động ~5%, nên đây là **ước lượng sơ bộ, không phải kết luận**. Một lần `ci.sh run-now` đầy đủ trên `dev` trước đó cho ~7695 tx/s (điều kiện khác: chạy sau chuỗi bài khác) nên **không** dùng làm mốc so sánh. Chưa đo: p50/p95/p99 độ trễ commit, số `fsync()` vật lý mỗi block, và thiết bị lưu trữ chi tiết. Raw output nằm trong log CI của phiên đó, chưa được đưa vào repository.

### 7.1. Barrier theo đường đi mã nguồn (không phải số `fsync()` vật lý đã đo)

| Miền lưu trữ | Barrier trên code path hiện tại | Điều kiện gọi | Ghi chú đo lường |
|---|:---:|---|---|
| **Account State (NOMT)** | Có | Khi commit payload account | NOMT là miền vật lý riêng; cần trace syscall/I/O để xác nhận số flush vật lý |
| **Stake State (NOMT)** | Có | Khi commit payload stake | Không được mặc định gộp với account thành một `fsync` vật lý |
| **Block Database (`blocks`)** | Có | Mọi block canonical | `storage.SyncDurable(blockDatabase)` |
| **Mapping DB (`mapping`)** | Có | Khi batch mapping không rỗng | `storage.SyncDurable(mappingDB)` trong `BlockChain.Commit()` |
| **Smart Contract Code (`code`)** | Có | Khi có bytecode mới | `storage.SyncDurable(codeStorage)` trong `SmartContractDB.Commit()` |
| **Event Logs (`smart_contract`)** | Có | Khi có event logs | `storage.SyncDurable(dbSmartContract)`; đây là DB riêng với code storage |
| **Receipts** | Có | Trước khi block canonical được công bố | `storage.SyncDurable(receiptStorage)`; recovery bypass vẫn đọc được receipt trie |
| **Transaction State** | Có | Trước khi block canonical được công bố | `storage.SyncDurable(transactionStorage)`; recovery bypass vẫn đọc được transaction trie |

Các dòng trên chỉ đếm **lời gọi barrier theo nhánh mã nguồn**. Chúng không chứng minh số syscall `fsync`/`fdatasync`, số lần flush thiết bị, hay latency thực tế. Một block contract có thể chạm cả code và event-log storage; không được gộp hai miền này thành một barrier nếu chưa có trace.

### 7.2. Kết quả benchmark cần nộp

| Chỉ số | Baseline trước barrier | Bản hiện tại | Delta |
|---|:---:|:---:|:---:|
| Commit latency p50 | **CHƯA ĐO** | **CHƯA ĐO** | **CHƯA ĐO** |
| Commit latency p95 | **CHƯA ĐO** | **CHƯA ĐO** | **CHƯA ĐO** |
| Commit latency p99 | **CHƯA ĐO** | **CHƯA ĐO** | **CHƯA ĐO** |
| Sustained TPS | **CHƯA ĐO** | **CHƯA ĐO** | **CHƯA ĐO** |
| Barrier/syscall count mỗi loại block | **CHƯA ĐO** | **CHƯA ĐO** | **CHƯA ĐO** |

Mỗi kết quả phải kèm raw artifact, commit SHA của baseline và candidate, timestamp, số mẫu/warm-up, cấu hình workload, kernel/filesystem/mount options, model ổ đĩa và lệnh chạy chính xác. Nên lưu artifact ngoài file báo cáo sinh tự động theo đường dẫn ổn định như `artifacts/n1/<timestamp>/`.

### 7.3. Lệnh tái lập

1. **C0 crash/recovery (xác minh correctness, không thay thế benchmark latency):**
   ```bash
   cd execution
   go build -tags c0spike -o /tmp/sc ./cmd/simple_chain
   /tmp/sc -config ../deploy/systemd/genesis.json --tool-c0-spike=verify -c0-blocks=5 -c0-rounds=2 -c0-report=stdout
   ```
2. **TPS workload:**
   ```bash
   cd ../metanode-suite/test_tps/tps_blast_cc
   ./run_tps_test.sh --no-reset --count 25000 --rounds 1 --load_balance=true --batch=2000 --amount 1
   ```

Hai bản phải chạy trên cùng host/config, xen kẽ thứ tự chạy để giảm bias. Chỉ điền bảng 7.2 sau khi raw artifact của cả hai bản đã được lưu và liên kết trong báo cáo.
