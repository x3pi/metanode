# Performance Optimizations — Pipeline Commit + Parallel Trie

> **Kết quả: System TPS tăng từ ~10K lên ~20K (2x improvement)**
> 100K TX xử lý trong 3 blocks (trước: 6-8 blocks), Engine tx/s peak: 47K

## Tổng quan

3 tối ưu hóa được triển khai để tăng throughput xử lý block:

| # | Tối ưu | Impact | Files |
|:-:|--------|--------|-------|
| 1 | Pipeline Commit | Tiết kiệm ~15-30ms/block | 3 files |
| 2 | Dirty Count Reduction | Giảm trie.Update() calls | 1 file |
| 3 | **Parallel Trie BatchUpdate** | **IntermediateRoot 5-7x nhanh hơn** | 3 files |

---

## 1. Pipeline Commit

**Vấn đề:** `AccountStateDB.Commit()` giữ `muTrie` lock suốt quá trình: `trie.Commit()` → `BatchPut` (disk I/O) → `NewTrie`. Block tiếp theo không thể đọc state.

**Giải pháp:** Tách thành 2 phase:
- `CommitPipeline()` — sync, nhanh: `trie.Commit()` + serialize batch + **release muTrie ngay lập tức**
- `PersistAsync()` — background goroutine: `BatchPut` + `NewTrie` + swap trie

**Fork-safety:** stateRoot vẫn từ `trie.Hash()` (không thay đổi). Trie gốc vẫn valid cho `Get()` sau `trie.Commit()` vì Commit() tạo internal copy.

### Files thay đổi

#### `pkg/account_state_db/account_state_db.go`
- Thêm struct `PipelineCommitResult`
- Thêm method `CommitPipeline()` — fast sync phase
- Thêm method `PersistAsync()` — slow background phase

#### `cmd/simple_chain/processor/block_processor_core.go`
- Thêm struct `PersistJob`
- Thêm field `persistChannel chan PersistJob`
- Khởi tạo `persistChannel` trong `NewBlockProcessor()`
- Khởi chạy `go bp.persistWorker()`

#### `cmd/simple_chain/processor/block_processor_commit.go`
- `commitToMemoryParallel()`: dùng `CommitPipeline()` thay `Commit()` cho AccountStateDB
- Thêm `persistWorker()` goroutine xử lý `PersistJob` background

---

## 2. Dirty Count Reduction

**Vấn đề:** `getOrCreateAccountState()` và `PreloadAccounts()` đưa TẤT CẢ accounts (kể cả chỉ đọc) vào `dirtyAccounts`. `IntermediateRoot` phải `trie.Update()` cho tất cả — kể cả unchanged.

**Giải pháp:** Tách `loadedAccounts` (read-only) khỏi `dirtyAccounts` (modified):
- `getOrCreateAccountState()` → store vào `loadedAccounts`
- `PreloadAccounts()` → store vào `loadedAccounts`
- `setDirtyAccountState()` → store vào `dirtyAccounts` (chỉ khi modify)
- Lookup order: `dirtyAccounts` → `loadedAccounts` → `lruCache` → trie

**Fork-safety:** `IntermediateRoot` chỉ process `dirtyAccounts` (truly modified). Cả 2 maps đều clear tại cùng thời điểm dưới `muTrie` lock.

### File thay đổi

#### `pkg/account_state_db/account_state_db.go`
- Thêm field `loadedAccounts sync.Map`
- Update `getOrCreateAccountState()` — check dirty → loaded → LRU → trie
- Update `PreloadAccounts()` — store vào `loadedAccounts`
- Clear `loadedAccounts` tại 4 reset points: ReloadTrie, Discard, IntermediateRoot, CopyFrom

---

## 3. Parallel Trie BatchUpdate ⚡ (Breakthrough)

**Vấn đề:** `IntermediateRoot(AccountDB)` chiếm **73% thời gian block** — sequential `trie.Update()` loop chạy ~40µs/account.

| Dirty Accounts | IntermediateRoot (trước) |
|:-:|:-:|
| 5K | 238ms |
| 10K | 373ms |
| 20K | 826ms |

**Giải pháp:** `BatchUpdate()` — parallel subtree updates by first nibble:

```
Root FullNode [16 children]
├── nibble 0: goroutine 0 → update keys starting with 0x0...
├── nibble 1: goroutine 1 → update keys starting with 0x1...
├── ...
└── nibble f: goroutine 15 → update keys starting with 0xf...
ALL 16 RUN IN PARALLEL
```

**Thuật toán:**
1. Hash tất cả keys, convert sang hex nibbles
2. Partition theo first nibble → 16 buckets
3. Resolve root thành FullNode (nếu là HashNode)
4. Mỗi bucket → 1 goroutine:
   - Tạo sub-trie với `root = fullNode.Children[nibble]` + tracer riêng
   - Sequential `insert()` cho keys trong bucket (stripped first nibble)
5. Merge results: `fullNode.Children[nibble] = updatedRoot`
6. Merge tracers (oldKeys, inserts, deletes)

**Fork-safety:** MPT structure xác định bởi SET key-values, không phụ thuộc insertion order. Keys với first nibble khác nhau → subtrees hoàn toàn độc lập → kết quả identical với sequential.

### Files thay đổi

#### `pkg/trie/tracer.go`
- Thêm method `merge(other *Tracer)` — gộp tracer data từ parallel subtrees

#### `pkg/trie/trie.go`
- Thêm method `BatchUpdate(keys, values [][]byte)` (+184 lines):
  - Partition by first nibble → 16 buckets
  - Resolve root to FullNode
  - Fallback to sequential nếu root là ShortNode (small tries)
  - 16 goroutines xử lý subtrees song song
  - Merge results + tracers

#### `pkg/account_state_db/account_state_db.go`
- `IntermediateRoot()`: thay sequential `trie.Update()` loop bằng `trie.BatchUpdate()`

---

## Kết quả Benchmark

### Trước tối ưu
```
System TPS:  ~10,000 tx/s
Blocks cho 100K TX: 6-8
IntermediateRoot (10K accounts): 373ms
Engine tx/s: 12-24K
```

### Sau tối ưu
```
System TPS:  ~20,000 tx/s  (2x improvement)
Blocks cho 100K TX: 3
Engine tx/s: 40-47K (2-3x improvement)
Max TXs/block: 40,000 (trước: 20-25K)
```

### Fork Check
- ✅ 100K TX, 100% verified
- ✅ Không fork giữa các nodes
- ✅ All unit tests pass
