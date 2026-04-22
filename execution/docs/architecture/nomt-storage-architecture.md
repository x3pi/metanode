# 🏗️ NOMT Storage Architecture — `state_backend: "nomt"`

> Tài liệu mô tả chi tiết cấu trúc database và trie khi sử dụng NOMT (Nearly Optimal Merkle Trie) backend.  
> Cập nhật: Tháng 4/2026 (sau cleanup PebbleDB genesis fallback + Sub-node cache invalidation).

---

## 1. Tổng quan — Phân chia Storage Engine

Khi `state_backend = "nomt"`, MetaNode phân chia rõ ràng:

- **NOMT (Rust FFI, io_uring)** → Lưu toàn bộ **State Data** cần Merkle proof
- **PebbleDB (Go)** → Lưu **Metadata** (blocks, receipts, bytecode, event logs)

| Engine | Dữ liệu lưu | Vai trò |
|---|---|---|
| **NOMT** | Account state, Stake state, Smart contract slots | Merkle Trie: tính Root Hash, phục vụ `Get()` reads |
| **PebbleDB** | Blocks, TXs, Receipts, Event Logs, SC Bytecode | Flat storage: không cần Merkle proof |

> **Lưu ý (Apr 2026):** PebbleDB `account_state/`, `stake_db/` và `trie_database/` sẽ **không được tạo thư mục** trên disk khi dùng NOMT (bỏ qua init shards hoàn toàn qua DummyStorage). NOMT là nguồn gốc duy nhất cho account/stake reads. Không có PebbleDB fallback.

---

## 2. Cấu trúc thư mục thực tế trên disk

```
sample/node0/data/data/                     ← RootPath (config: Databases.RootPath)
│
├── nomt_db/                                ← 🔶 NOMT Database (Rust native) — STATE DATA
│   ├── account_state/                      ← Namespace: tất cả genesis accounts + runtime updates
│   │   ├── bbn   (32 MB)                   ← Beatree Branch Nodes (internal trie structure)
│   │   ├── ht    (251 MB)                  ← Hash Table (leaf data / values)
│   │   ├── ln    (32 MB)                   ← Leaf Nodes
│   │   ├── meta  (4 KB)                    ← Root hash, metadata
│   │   ├── wal   (0 bytes)                 ← Write-Ahead Log (empty = clean state)
│   │   └── .lock                           ← File lock (1 process per DB)
│   │
│   ├── stake_db/                           ← Namespace: validator info (~5-100 validators)
│   │   └── bbn, ht, ln, meta, wal, .lock   ← Same structure (251 MB pre-allocated)
│   │
│   └── smart_contract_storage/             ← Namespace: EVM storage slots (grows with contracts)
│       └── bbn, ht, ln, meta, wal, .lock   ← Same structure
│
├── account_state/                          ← 🔵 PebbleDB — KHÔNG TẠO THƯ MỤC (DummyStorage)
│   └── (không tồn tại khi dùng NOMT)
│
├── stake_db/                               ← 🔵 PebbleDB — KHÔNG TẠO THƯ MỤC (DummyStorage)
│   └── (không tồn tại khi dùng NOMT)
│
├── smart_contract_storage/                 ← 🔵 PebbleDB — Event logs + non-NOMT entries
│   └── db_shard_0..3/
│
├── smart_contract_code/                    ← 🔵 PebbleDB — Bytecode (CHỈ PebbleDB, không NOMT)
│   └── db_shard_0..3/
│
├── blocks/                                 ← 🔵 PebbleDB — Block headers & bodies
│   └── db_shard_0..3/
│
├── transaction_state/                      ← 🔵 PebbleDB + FlatStateTrie (bypass NOMT)
│   └── db_shard_0..3/
│
├── receipts/                               ← 🔵 PebbleDB + FlatStateTrie (bypass NOMT)
│   └── db_shard_0..3/
│
├── trie_database/                          ← 🔵 PebbleDB — KHÔNG TẠO THƯ MỤC (DummyStorage)
│   └── (không tồn tại khi dùng NOMT)
│
├── mapping/                                ← 🔵 PebbleDB — Block hash → number
│   └── db_shard_0..3/
│
├── backup_device_key_storage/              ← 🔵 PebbleDB — Device key backups
│   └── db_shard_0..3/
│
└── xapian/                                 ← Xapian C++ — Full-text search index
```

---

## 3. NOMT Internal Files

Mỗi NOMT namespace chứa 5 file:

| File | Tên đầy đủ | Vai trò | Kích thước |
|---|---|---|---|
| `ht` | Hash Table | Key-value store chính (Beatree leaf data) | 251 MB (pre-allocated) |
| `bbn` | Beatree Branch Nodes | Internal nodes của Binary Merkle Trie | 32 MB |
| `ln` | Leaf Nodes | Leaf layer của Merkle tree | 32 MB |
| `meta` | Metadata | Root hash hiện tại, version, config | 4 KB |
| `wal` | Write-Ahead Log | Crash recovery — data đang ghi chưa commit | 0 bytes (empty = clean) |
| `.lock` | Lock file | Ngăn 2 process mở cùng DB | — |

### Beatree — Cấu trúc bên trong

```
                    ┌──────────────┐
                    │  Root Hash   │  ← Stored in `meta`
                    │  (32 bytes)  │
                    └──────┬───────┘
                           │
              ┌────────────┴────────────┐
              │                         │
        ┌─────▼─────┐           ┌───────▼─────┐
        │ Branch 0  │           │  Branch 1   │  ← Stored in `bbn`
        │ (internal)│           │  (internal) │
        └─────┬─────┘           └──────┬──────┘
              │                        │
        ┌─────▼─────┐           ┌──────▼──────┐
        │  Leaf 0   │           │   Leaf 1    │  ← Stored in `ln`
        │ hash(k,v) │           │  hash(k,v)  │
        └─────┬─────┘           └──────┬──────┘
              │                        │
        ┌─────▼──────────┐      ┌──────▼──────────┐
        │ KeyPath → Value│      │ KeyPath → Value  │  ← Stored in `ht`
        │ Keccak(key)    │      │ Keccak(key)      │     (Beatree lookup O(1))
        └────────────────┘      └─────────────────┘
```

### KeyPath Generation

NOMT hash key gốc thành **KeyPath** 32 bytes:

```
KeyPath = Keccak256(namespace_bytes + original_key_bytes)

Ví dụ:
  Account 0x824fef8A3cE4b93C...
    namespace = "account_state"
    KeyPath = Keccak256("account_state" + 0x824fef8A...)
            = 0x3a7b1c9f... (32 bytes — phân bố đều trên binary trie)
```

---

## 4. Dữ liệu đi đâu? — Ma trận phân bổ

| Loại dữ liệu | NOMT | PebbleDB | FlatStateTrie | Ghi chú |
|---|:---:|:---:|:---:|---|
| **Account State** (balance, nonce, BLS key) | ✅ **Duy nhất** | ❌ (Không tạo) | ❌ | Mọi reads/writes đều qua NOMT |
| **Stake State** (validator info) | ✅ **Duy nhất** | ❌ (Không tạo) | ❌ | NOMT + knownKeys registry cho `GetAll()` |
| **Smart Contract Storage** (EVM slots) | ✅ Primary | ❌ | ❌ | Per-contract namespace via KeyPath |
| **Smart Contract Code** (bytecode) | ❌ | ✅ | ❌ | Code immutable → PebbleDB đủ |
| **Block Headers** | ❌ | ✅ | ❌ | Sequential data → PebbleDB |
| **Transaction State** | ❌ | ✅ | ✅ Override | NOMT bị bypass (quá chậm cho 50K unique hashes/block) |
| **Receipts** | ❌ | ✅ | ✅ Override | Same as Transaction State |
| **Event Logs** | ❌ | ✅ | ❌ | PebbleDB only |

### Tại sao Transaction/Receipt bypass NOMT?

```go
// trie_factory.go
if namespace == "transaction_state" || namespace == "receipts" {
    // NOMT FFI sync for 50,000 fully unique 256-bit hashes
    // creates massive tree mutation and takes >3.5s per block.
    // Revert to FlatStateTrie which has O(1) commit.
    return NewFlatStateTrie(flatDB, isHash), nil
}
```

TX hash là 100% unique mỗi block (chỉ insert, không update) → FlatStateTrie O(1) nhanh hơn NOMT.

---

## 5. NOMT Handles — Singleton per Namespace

Mỗi namespace có 1 **singleton Handle**, quản lý bởi `globalNomtHandles`:

```go
globalNomtHandles = map[string]*nomt_ffi.Handle{
    "account_state":          handle1,  // pageCacheMB=1024, leafCacheMB=1024
    "stake_db":               handle2,  // pageCacheMB=64,   leafCacheMB=64
    "smart_contract_storage": handle3,  // pageCacheMB=1024, leafCacheMB=1024
}
```

### Cache Memory Allocation

```
Thực tế memory allocation:
  account_state:           1024 MB page + 1024 MB leaf = 2048 MB
  smart_contract_storage:  1024 MB page + 1024 MB leaf = 2048 MB
  stake_db:                  64 MB page +   64 MB leaf =  128 MB
  ─────────────────────────────────────────────────────────────
  TOTAL NOMT cache:                                     ≈ 4.2 GB
```

> ⚠️ Server cần tối thiểu 8GB RAM cho NOMT caches + Go heap + PebbleDB.

---

## 6. NOMT Session Lifecycle

```
                    ┌──────────────┐
                    │ BeginSession │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │  BatchWrite  │  ← Ghi dirty entries (in-memory only)
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │   Finish()   │  ← Tính Merkle root (CPU, fast)
                    │              │     Trả về FinishedSession + root hash
                    └──────┬───────┘
                           │
                    ┌──────▼───────────┐
                    │ CommitPayload()  │  ← Flush data xuống disk (I/O, slow)
                    │  (background)    │     Chạy trong PersistAsync worker
                    └──────────────────┘
```

### 2 Commit Paths

**Path A: `Commit()` — Đồng bộ (Genesis Init)**
```
IntermediateRoot(true) → Commit()
  ├── trie.Commit(true) → pendingFinishedSession
  ├── CommitPayload() ← Flush NGAY (Apr 2026 fix: trước trie swap)
  ├── NewStateTrie() → trie mới
  └── db.trie = newTrie ← Swap
```

**Path B: `CommitPipeline()` + `PersistAsync()` — Pipeline (Block Commit)**
```
Block Thread                    Background Worker
═══════════                     ═══════════════
IntermediateRoot(true)          
CommitPipeline()                
  ├── trie.Commit(true)         
  ├── persistReady = new chan    
  └── muTrie.Unlock() ← EARLY  
                                PersistAsync()
Next block starts!                ├── CommitPayload() (I/O)
  ├── PreloadAccounts            ├── muTrie.Lock → swap trie
  └── IntermediateRoot           └── close(persistReady)
       └── <-persistReady
```

---

## 7. Smart Contract Storage trong NOMT

### Key Isolation per Contract

```
SmartContractDB.loadStorageTrie(contractA_address)
  └── PrefixedStorage(dbSmartContract, contractA)
      └── NomtStateTrie(handle="smart_contract_storage",
                         keyPrefix="smart_contract_storage_<contractA_hex>")

KeyPath = Keccak256("smart_contract_storage_" + hex(contractA) + slot_bytes)
```

Mỗi contract có keyPrefix riêng → cùng slot 0 nhưng **KeyPath khác nhau hoàn toàn**.

### Commit Flow

```
1. LateBindRoots()          ← Tính StorageRoot mỗi contract, late-bind vào AccountState
2. IntermediateRoot(true)   ← Tính AccountRoot (bao gồm StorageRoots)
3. CommitAllStorage()       ← Commit NOMT sessions, tạo replication batch
4. CommitPipeline()         ← AccountStateDB final hash + serialize
5. PersistAsync()           ← Background flush
```

---

## 8. Genesis Init Flow

```
initGenesisBlock()
│
├── 1. Ghi 60K+ accounts → AccountStateDB.SetState() → dirtyAccounts
│
├── 2. IntermediateRoot(true)
│       └── NomtStateTrie.BatchUpdateWithCachedOldValues(60K entries)
│
├── 3. AccountStateDB.Commit()
│       ├── trie.Commit(true) → BeginSession → BatchWrite → Finish()
│       ├── CommitPayload()   ← CRITICAL: Flush TRƯỚC trie swap
│       ├── NewStateTrie()    → trie mới (clean)
│       └── db.trie = newTrie ← Swap (old trie orphaned nhưng data đã on disk)
│
├── 4. StakeStateDB.Commit() → Same flow cho validators
│
└── 5. SaveLastBlock → Genesis block persisted
```

> **Không có PebbleDB fallback.** NOMT CommitPayload đảm bảo genesis data on disk cho cả Master và Sub nodes.

---

## 9. Master → Sub Replication

### 9.1 Master tạo AccountBatch

```
CommitPipeline()
  └── trie.GetCommitBatch() → [["nomt:" + key, value], ...]
      └── SerializeBatch() → accountBatchData ([]byte)
```

Prefix `"nomt:"` đánh dấu entries cần ghi vào NOMT trên Sub node.

### 9.2 Sub node nhận và apply

```
applyBlockBatch(blockBatch)
│
├── 1. Deserialize tất cả batches
│       Account:    [["nomt:addr1", data1], ...]
│       SC Storage: [["addr_A" + "nomt:slot0", value], ...]
│       StakeState: [["nomt:val1", data1], ...]
│
├── 2. ApplyNomtReplicationBatches()
│       for namespace in [Account, SC Storage, StakeState]:
│           ├── Tách "nomt:" prefix → nomtKeys, nomtValues
│           ├── NewNomtStateTrie(handle, namespace)
│           ├── BatchUpdate → Commit → CommitPayload ← Flush to NOMT
│           └── XÓA "nomt:" entries khỏi aggregatedBatches
│
├── 3. PebbleDB BatchPut (PHẦN CÒN LẠI: Block, Code, Receipt, TX...)
│       ← Account/StakeState đã bị xóa ở bước 2, KHÔNG ghi trùng
│
├── 4. Cache Invalidation ← CRITICAL (Apr 2026)
│       ├── mvm.CallClearAllStateInstances()     ← Clear C++ EVM cache
│       ├── AccountStateDB.InvalidateAllCaches() ← Clear loadedAccounts + lruCache
│       └── StakeStateDB.InvalidateAllCaches()   ← Clear dirtyValidators
│
└── 5. Return (Sub node state fully synced)
```

### 9.3 Tại sao Cache Invalidation quan trọng?

`applyBlockBatch()` ghi **trực tiếp** vào NOMT/PebbleDB, bỏ qua AccountStateDB entirely. Nếu không clear caches:

```
Scenario lỗi (TRƯỚC fix):
  1. RPC query account A → loadedAccounts[A] = {nonce:1, balance:100}
  2. Block N arrives, A sends TX → NOMT updated: {nonce:2, balance:90}
  3. RPC query account A → loadedAccounts[A] still = {nonce:1, balance:100} ← STALE!
  4. Sub node appears diverged from Master

Scenario đúng (SAU fix):
  1. RPC query account A → loadedAccounts[A] = {nonce:1, balance:100}
  2. Block N arrives → NOMT updated + InvalidateAllCaches() called
  3. RPC query account A → cache miss → trie.Get(A) → NOMT → {nonce:2, balance:90} ✅
```

---

## 10. knownKeys Registry — `GetAll()` cho NOMT

NOMT là hash-based → không thể enumerate keys. chỉ `stake_db` dùng registry:

| Namespace | Registry | Lý do |
|---|:---:|---|
| `stake_db` | ✅ | Cần `GetAll()` cho validator listing (~5-100 entries) |
| `account_state` | ❌ Skip | 60K+ keys → registry quá lớn |
| `smart_contract_storage` | ❌ Skip | Hàng triệu slots |

Registry data lưu dưới dạng key-value NOMT thông thường:
```
RegistryKeyPath = Keccak256("__nomt_registry__:" + namespace)
Value = [1-byte keyLen][keyBytes][1-byte keyLen][keyBytes]...
```

---

## 11. Luồng đọc RPC Query

```
mtn_getAccountState(address) hoặc eth_getBalance(address)
│
├── AccountStateDB.AccountState(address)
│   ├── 1. Check dirtyAccounts    ← RAM (modified accounts chưa commit)
│   ├── 2. Check loadedAccounts   ← RAM (read cache, cleared mỗi block sync)
│   ├── 3. Check lruCache         ← RAM (200K entries, cleared mỗi block sync)
│   │
│   └── 4. NomtStateTrie.Get(address)
│       ├── Check dirty map       ← RAM
│       ├── Check committing map  ← RAM
│       └── handle.Read(KeyPath)  ← NOMT disk (Beatree O(1))
│
└── Return AccountState {balance, nonce, publicKeyBls, ...}
```

---

## 12. Sơ đồ tổng thể

```
┌─────────────────────────────────────────────────────────────────────┐
│                       MetaNode Go Process                           │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                     BlockProcessor                           │   │
│  │                                                               │   │
│  │  Master:  ExecuteTX → IntermediateRoot → CommitPipeline      │   │
│  │           → PersistAsync (background NOMT CommitPayload)     │   │
│  │                                                               │   │
│  │  Sub:     applyBlockBatch → NOMT replication                 │   │
│  │           → PebbleDB (remaining) → InvalidateAllCaches       │   │
│  └───────────────────────────┬───────────────────────────────────┘   │
│                              │                                       │
│  ┌───────────────────────────▼───────────────────────────────────┐   │
│  │              AccountStateDB / StakeStateDB                     │   │
│  │  dirtyAccounts ← sync.Map (modified, cleared per commit)      │   │
│  │  loadedAccounts ← sync.Map (read cache, cleared per sync)     │   │
│  │  lruCache ← LRU 200K entries (cleared per sync)               │   │
│  └───────────────────────────┬───────────────────────────────────┘   │
│                              │                                       │
│  ┌───────────────────────────▼───────────────────────────────────┐   │
│  │                  NomtStateTrie (Rust FFI)                      │   │
│  │  dirty map ← uncommitted writes                                │   │
│  │  committing map ← post-Commit, pre-CommitPayload               │   │
│  │  handle.Read() → Beatree O(1) lookup                           │   │
│  │  Sessions: BeginSession → BatchWrite → Finish → CommitPayload  │   │
│  └───────────────────────────┬───────────────────────────────────┘   │
│                              │                                       │
│  ┌───────────────────────────▼───────────────────────────────────┐   │
│  │              NOMT Database (Rust native, io_uring)             │   │
│  │                                                                 │   │
│  │  nomt_db/account_state/   ← 265 MB (bbn + ht + ln + meta)     │   │
│  │  nomt_db/stake_db/        ← 251 MB                             │   │
│  │  nomt_db/smart_contract_storage/ ← 251 MB                     │   │
│  └────────────────────────────────────────────────────────────────┘   │
│                                                                       │
│  ┌────────────────────────────────────────────────────────────────┐   │
│  │              PebbleDB (Go, sharded, non-state data only)       │   │
│  │                                                                 │   │
│  │  blocks/           ← Block headers & bodies                    │   │
│  │  receipts/          ← Transaction receipts (FlatStateTrie)     │   │
│  │  transaction_state/ ← TX state (FlatStateTrie)                 │   │
│  │  smart_contract_code/ ← Bytecode                               │   │
│  │  mapping/           ← Block hash → number                      │   │
│  │                                                                 │   │
│  │  account_state/     ← KHÔNG TẠO                                    │   │
│  │  stake_db/          ← KHÔNG TẠO                                    │   │
│  │  trie_database/     ← KHÔNG TẠO                                    │   │
│  └────────────────────────────────────────────────────────────────┘   │
└───────────────────────────────────────────────────────────────────────┘
```

---

## 13. Config

```json
{
  "state_backend": "nomt",
  "nomt_commit_concurrency": 32,
  "nomt_page_cache_mb": 1024,
  "nomt_leaf_cache_mb": 1024,
  "db_type": 2,
  "Databases": {
    "RootPath": "./sample/node0/data/data",
    "DBEngine": "sharded",
    "AccountState":         { "Path": "/account_state/" },
    "SmartContractCode":    { "Path": "/smart_contract_code/" },
    "SmartContractStorage": { "Path": "/smart_contract_storage/" },
    "Stake":                { "Path": "/stake_db/" },
    "Trie":                 { "Path": "/trie_database/" },
    "Backup":               { "Path": "/backup_db/" }
  }
}
```

| Parameter | Khuyến nghị | Mô tả |
|---|---|---|
| `nomt_commit_concurrency` | 32 | Workers song song khi commit NOMT session |
| `nomt_page_cache_mb` | 1024 | Page cache cho primary tries (account, SC) |
| `nomt_leaf_cache_mb` | 1024 | Leaf cache cho primary tries |

> NOMT DB path tự động: `{RootPath}/nomt_db/{namespace}/`

---

## 14. Fork-Safety Checklist

| # | Invariant | Cách đảm bảo |
|---|---|---|
| 1 | Genesis data on disk | `Commit()` gọi `CommitPayload()` sync trước trie swap |
| 2 | Deterministic commit order | `IntermediateRoot` sorts dirty entries by address bytes |
| 3 | SC storage deterministic | `CommitAllStorage` + `LateBindRoots` sort by address |
| 4 | Sub node fresh reads | `InvalidateAllCaches()` sau mỗi `applyBlockBatch()` |
| 5 | No dual-write account data | `ApplyNomtReplicationBatches` xóa `"nomt:"` entries trước PebbleDB |
| 6 | EVM cache consistency | `mvm.CallClearAllStateInstances()` sau block sync |
| 7 | NOMT session ordering | `CommitPipeline` waits `persistReady` trước session mới |
| 8 | No PebbleDB genesis fallback | Dead code đã xóa (Apr 2026) — NOMT là nguồn duy nhất |

---

## 15. Files tham khảo

| File | Mô tả |
|---|---|
| [`trie_factory.go`](../../pkg/trie/trie_factory.go) | Backend selection, NOMT handle management, TX/receipt bypass |
| [`nomt_state_trie.go`](../../pkg/trie/nomt_state_trie.go) | NomtStateTrie: Get, Update, Commit, CommitPayload, ApplyNomtReplicationBatches |
| [`account_state_db.go`](../../pkg/account_state_db/account_state_db.go) | AccountStateDB: caches, InvalidateAllCaches |
| [`account_state_db_commit.go`](../../pkg/account_state_db/account_state_db_commit.go) | Commit (genesis fix), CommitPipeline, PersistAsync |
| [`stake_state_db.go`](../../pkg/state_db/stake_state_db.go) | StakeStateDB: InvalidateAllCaches, CommitPipeline |
| [`smart_contract_db.go`](../../pkg/smart_contract_db/smart_contract_db.go) | SmartContractDB: per-contract NOMT trie management |
| [`block_processor_batch.go`](../../cmd/simple_chain/processor/block_processor_batch.go) | applyBlockBatch: NOMT replication + cache invalidation |
| [`app_blockchain.go`](../../cmd/simple_chain/app_blockchain.go) | Genesis init (no PebbleDB fallback) |
| [`database-and-trie-storage.md`](./database-and-trie-storage.md) | Tổng quan tất cả backends (MPT, FlatStateTrie, NOMT) |
| [`flat_trie_storage.md`](../flat_trie_storage.md) | Chi tiết FlatStateTrie + Bucket Accumulator |
