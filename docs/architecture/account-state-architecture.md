# AccountStateDB — Kiến trúc & Phân tích Chi tiết

> Tài liệu phân tích kiến trúc `AccountStateDB` trong `pkg/account_state_db/account_state_db.go`
> Cập nhật: 2026-03-01

---

## 1. Tổng quan Kiến trúc

Hệ thống quản lý account state sử dụng mô hình **Flat KV + Deferred Trie Update**:

```
 ┌──────────────────────────────────────────────────┐
 │              EXECUTE TRANSACTION                  │
 │  Read/Write trực tiếp trên memory (Flat KV)      │
 │  ❌ KHÔNG đi qua trie                            │
 └──────────────────────────────────────────────────┘
                        │
                        ▼
 ┌──────────────────────────────────────────────────┐
 │               COMMIT BLOCK                        │
 │  Chỉ lúc này mới:                                │
 │  • Update trie theo dirty accounts                │
 │  • Recompute hash (leaf → root)                  │
 │  • Lấy stateRoot cho block header                │
 └──────────────────────────────────────────────────┘
                        │
                        ▼
 ┌──────────────────────────────────────────────────┐
 │                  TRIE                             │
 │  Chỉ dùng để:                                    │
 │  • Tính stateRoot (Merkle Patricia hash)         │
 │  • Generate proof (chưa dùng)                    │
 │  • Snapshot/sync state                           │
 └──────────────────────────────────────────────────┘
```

---

## 2. Execute Transaction — Đọc/Ghi trên Flat KV

### Mô tả

Khi execute transaction, tất cả thao tác đọc/ghi account state đều xảy ra **trên memory**, thông qua `dirtyAccounts` (sync.Map) và `lruCache`. **KHÔNG** có lời gọi `trie.Update()` hay `trie.Hash()` nào trong quá trình execute.

### Flow chi tiết (code thực tế)

```
getOrCreateAccountState(address):
  ├─ 1. dirtyAccounts.Load(addr)     → HIT? return     ~0.1µs, no lock
  ├─ 2. lruCache.Get(addr)           → HIT? return     ~1µs, no lock  
  └─ 3. muTrie.Lock → trie.Get()    → LevelDB I/O     50-500µs (cold read lần đầu)
       └─ lruCache.Add(addr, data)   → cache cho lần sau

Modify balance/nonce:
  └─ Sửa trực tiếp trên in-memory AccountState object

setDirtyAccountState(state):
  └─ dirtyAccounts.Store(addr, state)  → sync.Map, no lock, ~0.1µs
```

### Mapping Code ↔ Flow

| Bước | Function | File | Lock? | I/O? |
|------|----------|------|-------|------|
| Đọc account | `getOrCreateAccountState()` | `account_state_db.go:1179` | Không (dirty/LRU hit) | Không |
| Cold read | `trie.Get()` trong `getOrCreateAccountState` | `account_state_db.go:1237` | `muTrie.Lock` | LevelDB |
| Batch preload | `PreloadAccounts()` | `account_state_db.go:1293` | `muTrie.RLock` 1 lần | LevelDB batch |
| Sửa balance | `AddBalance()`, `SubBalance()` | `account_state_db.go:463,489` | Không | Không |
| Sửa nonce | `PlusOneNonce()` | `account_state_db.go:385` | Không | Không |
| Đánh dấu dirty | `setDirtyAccountState()` | `account_state_db.go:1160` | Không (sync.Map) | Không |

### Tại sao ❌ KHÔNG đi qua trie lúc execute?

1. **`dirtyAccounts` (sync.Map)** — Đây là layer đầu tiên được check. Mọi account đã đọc hoặc sửa trong block hiện tại đều nằm ở đây → hit rate gần 100% cho hot accounts.

2. **`lruCache` (500K entries)** — Layer thứ 2. Account đã đọc ở block trước nhưng chưa dirty ở block này vẫn có trong LRU → không cần trie.

3. **`trie.Get()` (cold read)** — Chỉ xảy ra khi account CHƯA TỪNG được đọc (lần đầu tiên, block đầu tiên). Sau đó được cache vào `lruCache`.

4. **`PreloadAccounts()`** — Batch preload giảm lock contention: 1 lần `muTrie.RLock` cho tất cả, thay vì N lần lock riêng lẻ.

### Hiệu suất thực tế

| Scenario | Trie access? | Tốc độ |
|----------|-------------|--------|
| Hot account (đã dirty) | ❌ | ~0.1µs |
| Warm account (trong LRU) | ❌ | ~1µs |
| Cold account (lần đầu) | ⚠️ Fallback | 50-500µs |
| Sau PreloadAccounts | ❌ | ~0.1µs |

> **Kết luận:** ~95%+ truy cập account KHÔNG đi qua trie. Chỉ cold read lần đầu mới cần trie.

---

## 3. Commit Block — Update Trie & Tính StateRoot

### Mô tả

Lúc commit block là **thời điểm DUY NHẤT** trie được update. Tất cả dirty accounts được flush vào trie, recompute Merkle hash, và lấy stateRoot cho block header.

### Flow chi tiết (code thực tế)

```
ProcessTransactions() — tx_processor.go:41
  │
  ├─ processGroupsConcurrently()          ← Execute TXs (Flat KV only)
  │
  ├─ IntermediateRoot(true)               ← Flush dirty → trie, tính hash
  │   ├─ 1. dirtyAccounts.Range()         → Clone tất cả dirty entries
  │   ├─ 2. slices.SortFunc(keys)         → Sort address (FORK-SAFETY!)
  │   ├─ 3. Parallel marshal (32 workers) → AccountState → []byte
  │   ├─ 4. muTrie.Lock()
  │   ├─ 5. FOR EACH dirty:              
  │   │      trie.Update(addr, bytes)     ← ~25µs × N accounts
  │   │      lruCache.Add(addr, bytes)    ← Update cache
  │   ├─ 6. dirtyAccounts = sync.Map{}    → Clear dirty 
  │   ├─ 7. trie.Hash()                  → Recompute root hash
  │   └─ 8. muTrie.Unlock() (deferred)
  │
  └─ return ProcessResult{Root: stateRoot}

createBlockFromResults() — block_processor_processing.go:144
  │
  ├─ accountRoot = processResults.Root     ← stateRoot từ IntermediateRoot
  ├─ GenerateBlockData(... accountRoot ...) ← Ghi root vào block header
  │
  └─ commitToMemoryParallel()
      └─ AccountStateDB.Commit()           ← Persist trie to LevelDB
          ├─ IntermediateRoot(false)        → Chỉ return cached hash
          ├─ trie.Commit(true)             → Generate nodeSet
          ├─ Verify: intermediateHash == committedHash
          ├─ db.BatchPut(nodeSet)          → LevelDB persist
          └─ Swap trie, update originRootHash

commitWorker() — block_processor_commit.go:27
  └─ SaveLastBlock(block)                  → Block + header persist to DB
```

### Các phase chính khi commit

| Phase | Function | Thời gian | Mô tả |
|-------|----------|-----------|-------|
| **Phase 1: Flush dirty → trie** | `IntermediateRoot(true)` | 200-522ms | Sort → marshal → trie.Update × N |
| **Phase 2: Compute stateRoot** | `trie.Hash()` | ~10ms | Recompute Merkle root |
| **Phase 3: Persist to LevelDB** | `trie.Commit()` + `db.BatchPut()` | 2-4ms | Disk I/O |
| **Phase 4: Backup data** | `prepareBackupData()` | 56-167ms | Snapshot cho sub-nodes |

### FORK-SAFETY trong commit

1. **Sort keys trước khi update trie** — `slices.SortFunc` đảm bảo thứ tự update giống nhau trên tất cả nodes → cùng trie structure → cùng root hash.

2. **Clear dirtyAccounts ngay sau apply** — Tránh entries từ block N+1 (qua `PreloadAccounts`) bị lẫn vào commit block N.

3. **IntermediateRoot(false) trong Commit** — Khi gọi từ `Commit()`, KHÔNG xử lý dirtyAccounts (đã clear). Chỉ return hash đã tính.

4. **`lockedFlag` (atomic)** — Đảm bảo không có 2 IntermediateRoot(true) chạy đồng thời.

---

## 4. Trie — Vai trò và Giới hạn

### Trie chỉ dùng cho

| Mục đích | Cần? | Khi nào? | Code path |
|----------|------|----------|-----------|
| **Tính stateRoot** | ✅ Bắt buộc | Mỗi block commit | `IntermediateRoot(true)` → `trie.Hash()` |
| **Persist state** | ✅ Bắt buộc | Mỗi block commit | `Commit()` → `trie.Commit()` → LevelDB |
| **Cold read (fallback)** | ⚠️ Hiếm | Block đầu hoặc LRU miss | `getOrCreateAccountState()` → `trie.Get()` |
| **Snapshot/GetAll** | ⚠️ Debug only | Khi cần | `GetAll()` → iterate trie |
| **Generate Merkle proof** | ❌ Không dùng | — | Không có code gọi `trie.Prove()` |

### Trie KHÔNG dùng cho

| Thao tác | Thay thế bằng | Lý do |
|----------|---------------|-------|
| Đọc account khi execute | `dirtyAccounts` → `lruCache` | Tránh lock, tránh I/O |
| Ghi account khi execute | `setDirtyAccountState()` | sync.Map, lock-free |
| Balance/nonce modification | In-memory object mutation | Trực tiếp trên pointer |

---

## 5. Cấu trúc Dữ liệu

```go
type AccountStateDB struct {
    // === Flat KV Layer (dùng khi execute) ===
    dirtyAccounts   sync.Map                         // addr → AccountState (in-memory, lock-free)
    lruCache        *lru.Cache[common.Address, []byte] // 500K entries, ~250MB RAM

    // === Trie Layer (dùng khi commit) ===
    trie            *MerklePatriciaTrie               // Merkle Patricia Trie
    muTrie          sync.Mutex                        // Protect trie.Get/Update (not thread-safe)
    originRootHash  common.Hash                       // Root hash sau commit cuối

    // === Concurrency Control ===  
    lockedFlag      atomic.Bool                       // Prevent concurrent IntermediateRoot
    muCommit        sync.Mutex                        // Serialize Commit() calls
    db              storage.Storage                   // LevelDB for trie persistence
}
```

### Thứ tự ưu tiên đọc/ghi

```
 READ:  dirtyAccounts (1st) → lruCache (2nd) → trie.Get (3rd, cold only)
 WRITE: setDirtyAccountState → dirtyAccounts.Store (sync.Map)
 FLUSH: IntermediateRoot(true) → trie.Update × N → trie.Hash → clear dirty
```

---

## 6. LRU Cache — Chi tiết

### Cơ chế

| Thứ tự | Nguồn | Lock? | Tốc độ |
|--------|-------|-------|--------|
| 1 | `dirtyAccounts` (sync.Map) | Không | ~0.1µs |
| 2 | `lruCache` (LRU 500K) | Không cần trie lock | ~1µs |
| 3 | `trie.Get()` → LevelDB | `muTrie.Lock` | 50-500µs |

### Eviction & Invalidation

- **Không có TTL** — eviction thuần theo LRU (capacity 500K)
- **Purge** khi: `ReloadTrie()`, `Discard()`
- **Update** khi: `IntermediateRoot(true)` → `lruCache.Add()` cho mỗi dirty account
- **Tại sao an toàn không có TTL?** Thứ tự đọc `dirtyAccounts → lruCache → trie` đảm bảo: khi account bị modify, nó nằm trong `dirtyAccounts` (ưu tiên cao hơn), nên LRU entry cũ không bao giờ được sử dụng.

### Memory

500K entries × ~500 bytes/entry ≈ **~250MB RAM**

---

## 7. Bottleneck & Hiệu suất

### Dữ liệu thực tế

| Block | TXs | ProcessTransactions | IntermediateRoot | Tỷ lệ IR/Total | Engine tx/s |
|-------|-----|---------------------|-----------------|-----------------|-------------|
| #9 | 10K | 458ms | 304ms | **66%** | 21,802 |
| #10 | 20K | 659ms | 495ms | **75%** | 30,325 |
| #11 | 10K | 314ms | 204ms | **65%** | 31,778 |
| #12 | 20K | 774ms | 522ms | **67%** | 25,817 |

### Bottleneck chính

```
IntermediateRoot(true) — chiếm 65-75% thời gian block

  Parallel marshal (32 workers)    ~30ms    ✅ Đã tối ưu
  Sort keys                        ~2ms     ✅ Nhanh
  trie.Update() × N (sequential)   ~500ms   ❌ BOTTLENECK
  trie.Hash()                      ~10ms    ✅ Nhanh
```

**Nguyên nhân:** `trie.Update()` phải sequential vì Merkle Patricia Trie không thread-safe. 20K accounts × 25µs = **500ms**.

### Tóm tắt

| Component | Vai trò | Performance impact |
|-----------|---------|-------------------|
| `dirtyAccounts` | Flat KV cho execute | ✅ ~0.1µs/op, lock-free |
| `lruCache` | Cache warm accounts | ✅ ~1µs/op, no trie lock |
| `PreloadAccounts` | Batch cold read | ✅ 1 lock cho N accounts |
| `IntermediateRoot` | Flush dirty → trie | ❌ 200-522ms (bottleneck) |
| `trie.Commit` | Persist to disk | ✅ 2-4ms |

---

## 8. Tóm tắt: 3 Phase Chính

### Phase 1: Execute Transaction (❌ Không trie)

```
 TX → Read account (dirtyAccounts/lruCache)
    → Modify balance/nonce (in-memory)
    → setDirtyAccountState (sync.Map.Store)
    → ❌ KHÔNG gọi trie.Update(), trie.Hash(), hay bất kỳ trie operation nào
```

### Phase 2: Commit Block (✅ Trie update)

```
 Batch TXs done → IntermediateRoot(true)
    → Collect dirty accounts
    → Sort keys (fork-safety)  
    → Parallel marshal (32 workers)
    → Sequential trie.Update() × N   ← BOTTLENECK
    → trie.Hash() → stateRoot
    → Clear dirtyAccounts
    
 stateRoot → Block Header → Block Hash
    
 Commit() → trie.Commit() → LevelDB persist
```

### Phase 3: Trie (chỉ phục vụ commit)

```
 Trie dùng cho:
    ✅ stateRoot (Merkle hash) — bắt buộc cho block header
    ✅ Persist state — LevelDB cho crash recovery
    ⚠️ Cold read fallback — chỉ lần đầu
    ❌ Generate proof — không dùng
    ❌ Execute TX — không dùng
```
