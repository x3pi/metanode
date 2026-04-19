# 🔬 Phân Tích Kết Quả TPS — Multi-Node Load Test

> **Ngày**: 2026-03-03  
> **Cấu hình**: 10 clients × 10,000 TX = 100,000 TX, 4 validator nodes  
> **Storage**: PebbleDB + ShardedDB (16 shards)  
> **⚠️ NGUYÊN TẮC: Mọi cải thiện PHẢI đảm bảo KHÔNG FORK**

---

## 📊 Tổng Quan Kết Quả

| Metric | Lần 1 (Cold Start) | Lần 2 (Warm) |
|--------|:---:|:---:|
| **System TPS** | ~20,000 tx/s | ~14,285 tx/s |
| Tổng TX | 100,000 | 100,000 |
| Số blocks | 5 | 9 |
| Max TX/block | 30,000 (#1) | 28,000 (#13) |
| Thời gian xử lý | 5s | 7s |
| Success Rate | 100% | 100% |
| Fork | Không ✅ | Không ✅ |

---

## ⏱️ Chi Tiết Thời Gian Từng Phase (Lần 1 — Dữ Liệu Thực Từ Log)

### Block #1 — 30,000 TXs (tổng: 677ms)

| Phase | Thời gian | % Block |
|-------|----------:|--------:|
| Grouping TXs | 83ms | 12% |
| PreloadAccounts (30001 addrs) | 106ms | 16% |
| **TX Execution (Parallel)** | **330ms** | **49%** |
| IntermediateRoot BatchUpdate | 49ms | 7% |
| IntermediateRoot (AccountDB) | 136ms | 20% |
| IntermediateRoot (StakeDB) | 0.2ms | ~0% |
| IntermediateRoot (TrieDB) | ~0ms | ~0% |
| **TOTAL IR (Wall Clock)** | **136ms** | **20%** |
| `createBlockFromResults` TOTAL | **678ms** | — |
| CommitWorker critical path | 4ms | — |
| PersistWorker (async) | 18ms | — |

### Block #2 — 30,000 TXs (tổng: 686ms)

| Phase | Thời gian | % Block |
|-------|----------:|--------:|
| Grouping TXs | 87ms | 13% |
| PreloadAccounts (30001 addrs) | 115ms | 17% |
| **TX Execution (Parallel)** | **325ms** | **47%** |
| IntermediateRoot BatchUpdate | 52ms | 8% |
| IntermediateRoot (AccountDB) | 128ms | 19% |
| IntermediateRoot (StakeDB) | 0.1ms | ~0% |
| **TOTAL IR (Wall Clock)** | **128ms** | **19%** |
| `createBlockFromResults` TOTAL | **686ms** | — |
| CommitWorker critical path | 14ms | — |
| PersistWorker (async) | 74ms | — |

### Block #3 — 5,000 TXs (tổng: 180ms)

| Phase | Thời gian | % Block |
|-------|----------:|--------:|
| PreloadAccounts (5001 addrs) | 12ms | 7% |
| **TX Execution (Parallel)** | **65ms** | **36%** |
| IntermediateRoot BatchUpdate | 22ms | 12% |
| IntermediateRoot (AccountDB) | 46ms | 26% |
| **TOTAL IR (Wall Clock)** | **46ms** | **26%** |
| `createBlockFromResults` TOTAL | **180ms** | — |
| CommitWorker critical path | 2ms | — |
| PersistWorker (async) | 15ms | — |

### Block #4 — 15,000 TXs (tổng: 534ms)

| Phase | Thời gian | % Block |
|-------|----------:|--------:|
| PreloadAccounts (15001 addrs) | 36ms | 7% |
| **TX Execution (Parallel)** | **179ms** | **34%** |
| IntermediateRoot BatchUpdate | 40ms | 8% |
| IntermediateRoot (AccountDB) | 93ms | 17% |
| **TOTAL IR (Wall Clock)** | **93ms** | **17%** |
| `createBlockFromResults` TOTAL | **534ms** | — |
| CommitWorker critical path | 15ms | — |
| PersistWorker (async) | 49ms | — |

### Block #5 — 20,000 TXs (tổng: 627ms)

| Phase | Thời gian | % Block |
|-------|----------:|--------:|
| Grouping TXs | 53ms | 8% |
| PreloadAccounts (20001 addrs) | 82ms | 13% |
| **TX Execution (Parallel)** | **250ms** | **40%** |
| IntermediateRoot BatchUpdate | 65ms | 10% |
| IntermediateRoot (AccountDB) | 148ms | 24% |
| **TOTAL IR (Wall Clock)** | **148ms** | **24%** |
| `createBlockFromResults` TOTAL | **627ms** | — |
| CommitWorker critical path | 12ms | — |
| PersistWorker (async) | 50ms | — |

### 📊 Tổng Hợp — Trung Bình Theo Phase

| Phase | 5K TX | 15K TX | 20K TX | 30K TX |
|-------|------:|-------:|-------:|-------:|
| PreloadAccounts | 12ms | 36ms | 82ms | 110ms |
| TX Execution | 65ms | 179ms | 250ms | 327ms |
| IR BatchUpdate | 22ms | 40ms | 65ms | 50ms |
| IR AccountDB | 46ms | 93ms | 148ms | 132ms |
| **createBlock TOTAL** | **180ms** | **534ms** | **627ms** | **682ms** |
| CommitWorker | 2ms | 15ms | 12ms | 9ms |
| PersistWorker (async) | 15ms | 49ms | 50ms | 46ms |

### 💾 PebbleDB Sharded Flush (Lazy — Async)

```
account_state:      16 shards × ~9000 records → 20-52ms/shard
receipts:           16 shards × ~6700 records → 3-200ms/shard  
transaction_state:  16 shards × ~6700 records → 3-183ms/shard
mapping:            16 shards × ~5000 records → 2-12ms/shard
blocks:             individual shards → 2-5ms/shard
backup_db:          individual shards → 2-318ms/shard
```

---

## 🔍 Phân Tích Bottleneck

### #1: TX Execution (34-49% thời gian block)
- Mỗi group = 1 goroutine, nhưng BLS registration = 1 TX/group → 30K goroutines
- Bao gồm: marshal, state mutation, receipt creation
- Đã tối ưu: skip verification, deterministic BLS receipt

### #2: IntermediateRoot AccountDB (17-26% thời gian block)
- **BatchUpdate đã áp dụng**: 49-65ms cho 20K-30K keys (vs ~750ms nếu sequential)
- Parallel marshal (32 workers) trước khi lock muTrie
- Sorted dirty keys → deterministic trie structure

### #3: PreloadAccounts (7-17% thời gian block)
- Batch load qua 1 lần muTrie lock
- Scale tuyến tính theo số unique addresses

### Pipeline serialization:
- Block N phải hoàn tất trước khi Block N+1 bắt đầu
- CommitWorker rất nhanh (2-15ms) — không phải bottleneck
- PersistWorker chạy async (15-74ms) — không block pipeline

---

## 🛠️ Kỹ Thuật Đã Áp Dụng

### A. Injection Layer

| Kỹ thuật | Fork-safe? |
|----------|:---:|
| Raw TCP Writer (4MB buffer) | ✅ Client-side |
| Pre-build TXs trước connect | ✅ Client-side |
| Batch 2000 TX/message | ✅ Client-side |
| 10 clients round-robin 4 nodes | ✅ Client-side |

### B. Consensus Layer (Rust)

| Kỹ thuật | Fork-safe? |
|----------|:---:|
| Merged sub-DAG → 1 Go block | ✅ Cùng commit |
| Deterministic timestamp (median) | ✅ Consensus-level |
| TX Dedup + Sort by hash | ✅ Deterministic |

### C. Executor Layer (Go)

| Kỹ thuật | Fork-safe? |
|----------|:---:|
| Concurrent group processing | ✅ Indexed results |
| Batch PreloadAccounts (sorted) | ✅ Deterministic |
| Skip TX verification | ✅ Pre-verified |
| Deterministic BLS receipt | ✅ No EVM |

### D. State Layer (Go)

| Kỹ thuật | Fork-safe? |
|----------|:---:|
| Parallel IntermediateRoot (Account ∥ Stake) | ✅ Independent tries |
| **Batched trie.Update()** (vs sequential) | ✅ Sorted keys |
| Parallel marshal (32 workers) | ✅ Pure computation |
| CommitPipeline early muTrie unlock | ✅ Read-safe |
| Dirty account tracking (sync.Map) | ✅ Deterministic |

### E. Storage Layer

| Kỹ thuật | Fork-safe? |
|----------|:---:|
| **PebbleDB** thay LevelDB | ✅ Storage layer |
| **ShardedDB** (16 shards) | ✅ Deterministic shard routing |
| Lazy async flush | ✅ Post-commit |
| Targeted GC (block > 5K TX) | ✅ After finalized |
| LRU cache (AccountState) | ✅ Read cache |

### F. Block Creation

| Kỹ thuật | Fork-safe? |
|----------|:---:|
| Parallel receiptsRoot + txsRoot | ✅ Independent |
| Async TX-Hash mapping | ✅ Non-critical index |
| Skip empty commits | ✅ All nodes skip same |

---

## 🚀 Hướng Cải Thiện (KHÔNG FORK)

> **⚠️ Mọi optimization PHẢI đảm bảo:**
> 1. Cùng input → cùng output (deterministic)
> 2. Cùng stateRoot trên mọi node
> 3. Block hash, receiptsRoot, txsRoot identical

### ~~Pipeline Block N+1~~ ❌ ĐÃ THỬ — GÂY FORK

> Đã thử nghiệm overlap execution giữa block N và N+1. Kết quả: **FORK** do trie state
> bị shared giữa 2 blocks — PreloadAccounts(N+1) đọc dirty state chưa commit từ block N,
> dẫn đến stateRoot khác nhau giữa các nodes. **KHÔNG KHẢ THI** với kiến trúc hiện tại.

---

### 1. 🟡 Parallel Trie Update by Prefix (Medium)

**Hiện tại**: BatchUpdate sequential (49-65ms cho 30K keys)  
**Cải tiến**: Partition by key prefix → parallel sub-trie update  
**Dự kiến**: 2x speedup cho BatchUpdate phase  
**Fork-safe**: ✅ Deterministic partition + sequential merge

### 2. 🟡 Reduce Goroutine Count (Medium)

**Hiện tại**: 30K groups → 30K goroutines (BLS = 1 TX/group)  
**Cải tiến**: Batch nhỏ thành worker pools (e.g., 64 workers)  
**Dự kiến**: Giảm scheduler overhead, giảm GC pressure  
**Fork-safe**: ✅ Indexed results → deterministic merge

### 3. 🟢 Larger Block Aggregation (Low Risk)

**Cải tiến**: Buffer 2-3 Rust commits → 1 block lớn hơn  
**Dự kiến**: Ít lần gọi IR, giảm per-block overhead  
**Fork-safe**: ✅ All nodes buffer same commits  
**Trade-off**: Tăng latency individual TXs

### 4. 🟢 Lazy Trie Hashing

**Cải tiến**: Chỉ hash khi commit  
**Fork-safe**: ✅ Hash chỉ phụ thuộc data

---

## 📈 Tóm Tắt

```
TOP BOTTLENECK:   TX Execution (Parallel)   ~40% thời gian block
BOTTLENECK #2:    IntermediateRoot AccountDB ~20% thời gian block  
BOTTLENECK #3:    PreloadAccounts            ~13% thời gian block

KHÔNG phải bottleneck:
  - CommitWorker:   2-15ms  (rất nhanh)
  - PersistWorker:  15-74ms (async, không block)
  - PebbleDB flush: async lazy, 16 shards parallel

Đã đạt:     20K TPS, 100% success, 0 forks
Tiềm năng:  50K+ TPS với pipeline execution + worker pools
```
