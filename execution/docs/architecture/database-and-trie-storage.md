# 📦 Kiến trúc Lưu trữ Database & Trie — MetaNode

> Tài liệu tham khảo cho developer. Cập nhật: Tháng 4/2026.

---

## 1. Tổng quan — MetaNode lưu trữ những gì?

MetaNode duy trì nhiều Database phục vụ cho các mục đích khác nhau. Tất cả được quản lý bởi `StorageManager` (`pkg/storage/`).

| Database | Chức năng | Engine | Đường dẫn (mặc định) |
|---|---|---|---|
| **AccountStateDB** | Số dư ví, Nonce, BLS Public Key, Device Key | PebbleDB | `<data>/account_state/` |
| **StakeStateDB** | Validator info, delegation, stake amounts | PebbleDB | `<data>/stake_db/` |
| **SmartContractDB** | EVM/MVM Storage (biến Solidity) | PebbleDB | `<data>/smart_contract/` |
| **Block Storage** | Block headers, bodies, lastBlock pointer | PebbleDB | `<data>/block/` |
| **Transaction State** | Transaction receipts & logs | PebbleDB | `<data>/transaction/` |
| **Backup DB** | Crash-recovery metadata (GEI, lastBlockNumber) | PebbleDB | `<backup>/backup_db/` |
| **NOMT DB** | Merkle trie (khi dùng NOMT backend) | Rust FFI (io_uring) | `<data>/nomt_db/` |
| **Explorer DB** | Xapian full-text search index | Xapian (C++) | `<data>/xapian/` |

### Cấu trúc thư mục thực tế

```
sample/node0/data/data/              ← Master node data (config: databases.root_path)
├── account_state/                   ← PebbleDB (sharded)
│   ├── shard_0/                     ← Mỗi shard là 1 PebbleDB instance
│   ├── shard_1/
│   ├── shard_2/
│   └── shard_3/
├── stake_db/                        ← PebbleDB
├── smart_contract/                  ← PebbleDB (sharded)
├── block/                           ← PebbleDB — lưu block headers
├── transaction/                     ← PebbleDB — lưu receipts
├── nomt_db/                         ← NOMT persistent storage (nếu backend=nomt)
│   ├── account_state/               ← NOMT namespace cho accounts
│   └── stake_db/                    ← NOMT namespace cho validators
└── xapian/                          ← Xapian search DB
```

---

## 2. Trie Backends — 3 loại State Trie

MetaNode hỗ trợ 3 backend cho State Trie, được cấu hình qua `state_backend` trong config JSON:

```json
{
  "state_backend": "nomt"   // "mpt" | "flat" | "nomt"
}
```

### 2.1 So sánh 3 backends

| Tiêu chí | MPT (Ethereum gốc) | FlatStateTrie | NOMT |
|---|---|---|---|
| **Đọc dữ liệu** | O(log N) — 6-7 lần I/O | **O(1) — 1 lần I/O** | **O(1) — Beatree** |
| **Tính Root Hash** | O(N) duyệt cây | O(K) — chỉ entries đổi | O(K) — binary trie |
| **Engine lưu trữ** | PebbleDB (Go) | PebbleDB (Go) | Rust FFI (io_uring) |
| **Thread-safe Get()** | ❌ (cần mutex) | ✅ (internal RWMutex) | ✅ (concurrent readers) |
| **Commit model** | Sync (blocking) | Sync + Pipeline | **Async (Session → Finish → CommitPayload)** |
| **Replication** | NodeSet (hash→blob) | Key→Value pairs | `nomt:` prefix pairs |
| **File path** | PebbleDB dirs | PebbleDB dirs | `nomt_db/<namespace>/` |
| **Phù hợp cho** | Tương thích Ethereum | TPS cao, Go-native | **TPS tối đa, production** |

### 2.2 MPT (Merkle Patricia Trie)

Cấu trúc cây theo Ethereum gốc. Mỗi lần Get() phải traverse 6-7 node cây → 6-7 lần đọc LevelDB/PebbleDB.

```
Root Hash
├── 0x4...
│   ├── 0x8... → Account{balance: 100, nonce: 5}
│   └── 0xA... → Account{balance: 50, nonce: 1}
└── 0xB... → Account{balance: 200, nonce: 3}
```

- **Commit**: Tạo `NodeSet` (danh sách trie node mới) → `BatchPut` vào PebbleDB → tạo trie mới từ root hash.
- **Replication**: Gửi `NodeSet` dưới dạng `[hash, blob]` pairs.
- **Code**: `pkg/trie/mpt_state_trie.go` (wrapper quanh go-ethereum `trie.StateTrie`).

### 2.3 FlatStateTrie

Lưu trực tiếp Key→Value vào PebbleDB, O(1) reads. Root Hash tính bằng thuật toán **Bucket Accumulator** (MuHash modulo prime 2^256 - 189).

```
PebbleDB
"fs:<20-byte address>" → serialized AccountState
"fb:<1-byte bucket_id>" → bucket accumulator hash
```

- **Commit**: `BatchPut` dirty entries → update bucket accumulators → compute root hash.
- **Replication**: Gửi raw `[key, value]` pairs trực tiếp.
- **Code**: `pkg/trie/flat_state_trie.go`.
- **Chi tiết**: Xem `docs/flat_trie_storage.md`.

### 2.4 NOMT (Nearly Optimal Merkle Trie) — Production Backend

Binary Merkle Trie được viết bằng **Rust**, kết nối qua **CGo FFI** (`pkg/nomt_ffi/`). Sử dụng **io_uring** (Linux kernel async I/O) cho throughput tối đa.

```
NOMT DB (Rust native)
KeyPath = Keccak256(namespace + original_key) → [32 bytes]
Value = serialized AccountState
```

**Đặc điểm nổi bật:**
- **Session-based commit**: `BeginSession()` → `Write()/BatchWrite()` → `Finish()` → `CommitPayload()`
- **Async commit**: `Finish()` tính root hash trong RAM (nhanh), `CommitPayload()` flush xuống disk (chậm, chạy background)
- **Namespace isolation**: Mỗi loại data (account_state, stake_db) có namespace riêng, key path được hash bằng `Keccak256(namespace + key)`
- **Known Keys Registry**: Lưu danh sách key đã biết vào NOMT cho `GetAll()` (chỉ dùng cho `stake_db`, không dùng cho `account_state`)

**Code chính**: `pkg/trie/nomt_state_trie.go`, `pkg/nomt_ffi/`.

#### NOMT Session Lifecycle

```
                    ┌──────────────┐
                    │ BeginSession │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │  BatchWrite  │  ← Ghi dirty entries (in-memory)
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

---

## 3. AccountStateDB — Trung tâm quản lý State

`AccountStateDB` (`pkg/account_state_db/`) là lớp quản lý chính cho account state. Nó wrap một `StateTrie` (interface) và thêm các tính năng:

### 3.1 Kiến trúc bộ nhớ

```
AccountStateDB
├── trie: StateTrie              ← MPT / FlatStateTrie / NomtStateTrie
├── dirtyAccounts: sync.Map      ← Accounts ĐÃ THAY ĐỔI (modified) — chưa commit
├── loadedAccounts: sync.Map     ← Accounts ĐÃ ĐỌC (read-only cache) — không trigger trie.Update()
├── lruCache: LRU[Address→[]byte] ← Cache serialized bytes từ trie.Get() (200K entries)
├── accountBatch: []byte          ← Serialized batch cho network replication (Master→Sub)
├── muTrie: sync.RWMutex         ← Bảo vệ trie access (RLock = concurrent reads, Lock = commit)
├── muCommit: sync.Mutex         ← Serialize commit operations
├── lockedFlag: atomic.Bool      ← Track IntermediateRoot lock state
└── persistReady: chan struct{}   ← Gate cho pipeline overlap (PersistAsync ↔ next block)
```

### 3.2 Luồng đọc Account State

```
AccountState(address)
│
├── 1. Check dirtyAccounts (sync.Map) ← Modified accounts (highest priority)
├── 2. Check loadedAccounts (sync.Map) ← Previously loaded accounts
├── 3. Check lruCache (LRU) ← Serialized bytes cache
├── 4. trie.Get(address.Bytes()) ← Read from trie backend (PebbleDB/NOMT)
│       ├── FlatTrie/NOMT: NO lock needed (thread-safe)
│       └── MPT: muTrie.Lock() required (not thread-safe)
├── 5. Store result in loadedAccounts ← Cache for next access
└── 6. Return AccountState
```

### 3.3 Luồng ghi (Commit)

MetaNode có **2 commit paths**:

#### Path A: `Commit()` — Đồng bộ (dùng cho Genesis Init)

```
IntermediateRoot(true)          ← Collect dirty, marshal, BatchUpdate trie, compute hash
       │
Commit()                        ← trie.Commit() → NOMT: CommitPayload() (sync) → swap trie
       │
Return (rootHash)
```

> **Lưu ý quan trọng (Apr 2026 Fix):** Với NOMT backend, `Commit()` gọi `CommitPayload()` **synchronously** trước khi swap trie. Nếu không, `pendingFinishedSession` trên trie cũ bị orphan và genesis data không bao giờ flush xuống disk.

#### Path B: `CommitPipeline()` + `PersistAsync()` — Pipeline (dùng cho Block Commit)

```
Block Processing Thread              Background Persist Worker
═══════════════════                  ═══════════════════════
                                     
IntermediateRoot(true)               
  ├── Parallel Marshal (CPU)         
  ├── BatchUpdate (trie)             
  └── muTrie.Lock()                  
                                     
CommitPipeline()                     
  ├── trie.Commit(true) → nodeSet    
  ├── Serialize AccountBatch         
  ├── SET persistReady (new channel) 
  └── muTrie.Unlock() ← EARLY!      
       │                             
       │ PipelineCommitResult ─────► PersistAsync()
       │                               ├── BatchPut to PebbleDB
       │                               ├── NOMT: CommitPayload() (I/O)
       │                               ├── muTrie.Lock() → swap trie
       │                               └── close(persistReady) ← Signal done
       │                             
Next block starts immediately!       
  ├── PreloadAccounts (can overlap)  
  └── IntermediateRoot(true)         
       └── <-persistReady ← Wait if needed
```

**Ưu điểm pipeline**: Block N+1 có thể bắt đầu xử lý transaction ngay khi Block N vừa compute xong hash, không cần đợi disk I/O hoàn tất.

---

## 4. Genesis Init — Luồng khởi tạo khối đầu tiên

Khi node khởi động lần đầu (fresh start), genesis block được tạo từ `genesis.json`:

```
app.initGenesisBlock(blockDatabase)
│
├── 1. Tạo genesis block header (timestamp từ genesis.json)
│
├── 2. Tạo ChainState (AccountStateDB + StakeStateDB + BlockDatabase)
│
├── 3. Ghi Account State:
│       for account in genesis.Alloc:
│           account.PlusOneNonce()     ← Genesis accounts bắt đầu với nonce=1
│           AccountStateDB.SetState(account)
│       IntermediateRoot(true) → Commit()
│       ├── [NOMT] CommitPayload() ← CRITICAL: Flush synchronously trước swap
│       └── PebbleDB BatchPut (FlatTrie fallback cho Sub-node sync)
│
├── 4. Ghi Stake State:
│       for validator in genesis.Validators:
│           cs.CreateRegisterWithKeys(validator)
│           cs.Delegate(validator, delegator, amount)
│       cs.IntermediateRoot(true) → cs.Commit()
│       ├── [NOMT] CommitPayload() ← Also flushes synchronously
│       └── SaveLastBlock → Flush to disk
│
├── 5. [NOMT] Chuyển genesis accounts vào PebbleDB fallback:
│       BatchPut(address → serialized_data) vào PebbleDB
│       ← Để Sub-node có thể đọc khi chưa sync từ Master
│
└── 6. Return (genesis block saved)
```

---

## 5. Master → Sub Replication (AccountBatch)

Khi Master node commit 1 block, nó tạo **AccountBatch** chứa tất cả state changes và gửi cho Sub nodes qua TCP.

### 5.1 Luồng tạo AccountBatch trên Master

```
CommitPipeline()
│
├── trie.Commit(true)
│     ├── [MPT] → NodeSet{hash→blob pairs}
│     ├── [FlatTrie] → GetCommitBatch() → [key→value pairs]
│     └── [NOMT] → GetCommitBatch() → ["nomt:"+key → value pairs]
│
├── SerializeBatch(networkBatch) → accountBatchData ([]byte)
│
└── SetAccountBatch(accountBatchData)
```

### 5.2 Sub node nhận và apply AccountBatch

```
Sub node nhận block từ Master (qua TCP BlockResponse)
│
├── Deserialize AccountBatch → aggregatedBatches map
│     ├── "Account" → [][key, value]
│     ├── "SC Storage" → [][key, value]
│     └── "StakeState" → [][key, value]
│
├── [NOMT] ApplyNomtReplicationBatches():
│     ├── Tách "nomt:" prefix → nomtKeys, nomtValues
│     ├── BatchUpdate → Commit → CommitPayload vào NOMT
│     └── Giữ lại non-nomt entries cho PebbleDB
│
├── BatchPut remaining entries vào PebbleDB
│
└── Apply block header + update lastBlock
```

> **Lưu ý (Apr 2026):** Sub nodes KHÔNG rebuild full NOMT Merkle tree. Chúng nhận pre-computed state roots từ Master. NOMT chỉ dùng để phục vụ `Get()` reads cho RPC queries.

---

## 6. PebbleDB — Engine Lưu trữ

MetaNode dùng **PebbleDB** (CockroachDB) thay vì LevelDB gốc của Ethereum.

### 6.1 Sharded PebbleDB

Các DB lớn (account_state, smart_contract) được **shard** thành nhiều PebbleDB instances để tăng throughput song song:

```
account_state/
├── shard_0/   ← Keys có byte đầu 0x00-0x3F
├── shard_1/   ← Keys có byte đầu 0x40-0x7F
├── shard_2/   ← Keys có byte đầu 0x80-0xBF
└── shard_3/   ← Keys có byte đầu 0xC0-0xFF
```

Số shard mặc định: 4 (account_state), có thể cấu hình.

### 6.2 Crash Safety — NoSync + Periodic Flush

MetaNode sử dụng chiến lược **NoSync + Periodic Flush**:

1. **Normal writes**: `pebble.NoSync` — không fsync mỗi write → throughput tối đa
2. **Periodic flush**: Mỗi 10 giây, goroutine `periodicStorageFlusher()` gọi `FlushAll()` → memtable → SST
3. **Clean shutdown**: `Stop()` gọi `FlushAll()` → zero data loss
4. **Crash recovery**: Tối đa mất ~10 giây data → recoverable từ peers

### 6.3 lastBlock Crash Safety

`lastBlock` pointer (block hiện tại) là critical nhất — mất nó = node không biết mình đang ở block nào.

```
Bảo vệ lastBlock:
├── SaveLastBlock() → PebbleDB (normal, may not flush)
├── SaveLastBlockSync() → PebbleDB + fsync (shutdown)
├── SaveLastBlockBackup() → file backup mỗi 30 giây
└── GetLastBlock() → try PebbleDB → fallback to backup file
```

Nếu PebbleDB mất lastBlock nhưng có dữ liệu SST → **REFUSE to re-init genesis** (Safety Guard tránh xóa toàn bộ state).

---

## 7. Các bẫy thường gặp (Known Gotchas)

### 7.1 NOMT CommitPayload orphan (Đã fix Apr 2026)

**Vấn đề:** `AccountStateDB.Commit()` (non-pipeline path) gọi `trie.Commit(true)` → set `pendingFinishedSession` trên trie → tạo trie MỚI → swap → trie cũ bị orphan → `CommitPayload()` gọi trên trie mới = noop → genesis data mất.

**Fix:** Gọi `CommitPayload()` synchronously TRƯỚC swap trie trong `Commit()`.

**File:** `pkg/account_state_db/account_state_db_commit.go`

### 7.2 dirtyAccounts vs loadedAccounts

- `dirtyAccounts`: Chỉ chứa accounts **đã thực sự thay đổi** (`IsDirty() = true`). IntermediateRoot chỉ update trie với accounts trong map này.
- `loadedAccounts`: Chứa accounts đã đọc nhưng chưa thay đổi. KHÔNG trigger trie update. Giữ qua nhiều block để tránh re-read.

**Nếu nhầm lẫn** (put vào dirtyAccounts khi chỉ đọc) → trie update thừa → performance drop.

### 7.3 LRU Cache stale reads

`lruCache` chứa serialized bytes từ `trie.Get()`. Sau mỗi block commit, các accounts đã thay đổi có bytes MỚI trong trie nhưng bytes CŨ vẫn còn trong LRU → stale read.

**Fix:** LRU được update ngay trong `IntermediateRoot()` khi marshal dirty accounts (trước khi trie.Commit).

### 7.4 NOMT knownKeys registry skip

`account_state` và `smart_contract_storage` có quá nhiều keys (60K+ accounts, hàng triệu slots). Lưu tất cả vào registry sẽ rất lớn → **skip registry** cho các namespace này.

Chỉ `stake_db` (< 100 validators) dùng registry cho `GetAll()`.

```go
// Trong NomtStateTrie.Update():
if namespace != "account_state" && namespace != "smart_contract_storage" && ... {
    knownKeys[hexKey] = keyCopy   // Chỉ track cho small namespaces
}
```

---

## 8. Cấu hình liên quan

```json
{
  "state_backend": "nomt",           // "mpt" | "flat" | "nomt"
  "nomt_commit_concurrency": 4,      // Số worker cho NOMT commit
  "nomt_page_cache_mb": 512,         // NOMT page cache (MB)
  "nomt_leaf_cache_mb": 512,         // NOMT leaf cache (MB)
  "db_type": 2,                      // 2 = PebbleDB
  "databases": {
    "root_path": "./sample/node0/data/data",
    "account_state": {
      "path": "/account_state",
      "shard_count": 4,
      "write_buffer_count": 2
    },
    "smart_contract_storage": {
      "path": "/smart_contract",
      "shard_count": 4,
      "write_buffer_count": 2
    },
    "block": { "path": "/block" },
    "backup": { "path": "/backup_db" }
  }
}
```

---

## 9. Sơ đồ tổng thể

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        MetaNode Go Process                              │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                      BlockProcessor                              │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐    │   │
│  │  │ IntermediateRoot │  │ CommitPipeline │  │ PersistAsync       │    │   │
│  │  │ (marshal+trie  │  │ (hash+batch  │  │ (disk I/O, swap  │    │   │
│  │  │  update, CPU)  │  │  serialize)  │  │  trie, signal)   │    │   │
│  │  └───────┬────────┘  └──────┬───────┘  └────────┬─────────┘    │   │
│  │          │                  │                    │               │   │
│  └──────────┼──────────────────┼────────────────────┼───────────────┘   │
│             │                  │                    │                    │
│  ┌──────────▼──────────────────▼────────────────────▼───────────────┐   │
│  │                     AccountStateDB                               │   │
│  │  dirtyAccounts ← sync.Map (modified)                             │   │
│  │  loadedAccounts ← sync.Map (read-only cache)                     │   │
│  │  lruCache ← LRU 200K entries (serialized bytes)                  │   │
│  └──────────────────────────┬───────────────────────────────────────┘   │
│                             │                                           │
│  ┌──────────────────────────▼───────────────────────────────────────┐   │
│  │                     StateTrie (interface)                         │   │
│  │  ┌────────────┐  ┌─────────────────┐  ┌──────────────────────┐  │   │
│  │  │    MPT     │  │  FlatStateTrie  │  │   NomtStateTrie      │  │   │
│  │  │ (Ethereum) │  │ (O(1) + Bucket) │  │ (Rust + io_uring)    │  │   │
│  │  │            │  │                 │  │                      │  │   │
│  │  │ Get()=O(logN)│ │ Get()=O(1)     │  │ Get()=O(1)           │  │   │
│  │  │ NOT safe   │  │ Thread-safe     │  │ Thread-safe          │  │   │
│  │  └─────┬──────┘  └───────┬─────────┘  └──────────┬───────────┘  │   │
│  └────────┼─────────────────┼────────────────────────┼──────────────┘   │
│           │                 │                        │                  │
│  ┌────────▼─────────────────▼──────┐  ┌──────────────▼───────────────┐  │
│  │  PebbleDB (Go, sharded)        │  │  NOMT DB (Rust FFI)          │  │
│  │  ┌────┐ ┌────┐ ┌────┐ ┌────┐  │  │  ┌──────────────────────┐   │  │
│  │  │ S0 │ │ S1 │ │ S2 │ │ S3 │  │  │  │ account_state/       │   │  │
│  │  └────┘ └────┘ └────┘ └────┘  │  │  │ stake_db/            │   │  │
│  │  account_state/               │  │  │ (Beatree, io_uring)  │   │  │
│  │  smart_contract/              │  │  └──────────────────────┘   │  │
│  │  block/                       │  │                             │  │
│  │  transaction/                 │  │  Async: Session → Finish   │  │
│  └───────────────────────────────┘  │         → CommitPayload    │  │
│                                      └───────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 10. Files tham khảo

| File | Mô tả |
|---|---|
| `pkg/account_state_db/account_state_db.go` | AccountStateDB core (Get, SetState, cache) |
| `pkg/account_state_db/account_state_db_commit.go` | Commit, CommitPipeline, PersistAsync, IntermediateRoot |
| `pkg/trie/state_trie.go` | StateTrie interface definition |
| `pkg/trie/mpt_state_trie.go` | MPT backend wrapper |
| `pkg/trie/flat_state_trie.go` | FlatStateTrie backend |
| `pkg/trie/nomt_state_trie.go` | NOMT backend (Rust FFI) |
| `pkg/nomt_ffi/` | Low-level Rust FFI bindings |
| `pkg/storage/storage_manager.go` | StorageManager, FlushAll, periodic flusher |
| `pkg/storage/shardel_db.go` | Sharded PebbleDB implementation |
| `cmd/simple_chain/app_blockchain.go` | Genesis init, blockchain startup |
| `cmd/simple_chain/processor/block_processor_commit.go` | Block commit pipeline orchestration |
| `cmd/simple_chain/processor/block_processor_batch.go` | AccountBatch packing cho Sub-node sync |
| `docs/flat_trie_storage.md` | Tài liệu chi tiết FlatStateTrie + Bucket Accumulator |
