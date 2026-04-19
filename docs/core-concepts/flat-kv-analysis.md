# Flat KV — Phân tích Fork-Safety & Lộ trình Tối ưu

> Cập nhật: 2026-03-01 — Xem xét lại thiết kế đảm bảo không fork

---

## 1. Hiện trạng hiệu suất (dữ liệu thực)

Từ log thực tế `tps_blast 10×10000` trên localhost:

| Block | TXs | ProcessTransactions | IntermediateRoot | Tỷ lệ IR/Total | Engine tx/s |
|-------|-----|---------------------|-----------------|-----------------|-------------|
| #9 | 10K | 458ms | 304ms | **66%** | 21,802 |
| #10 | 20K | 659ms | 495ms | **75%** | 30,325 |
| #11 | 10K | 314ms | 204ms | **65%** | 31,778 |
| #12 | 20K | 774ms | 522ms | **67%** | 25,817 |

> **Phát hiện:** `IntermediateRoot` (trie update) chiếm **65-75% thời gian** xử lý block.

### Phân tích chi tiết

| Phase | Thời gian | Mô tả |
|-------|-----------|-------|
| IntermediateRoot | 200-522ms | `trie.Update()` × N ← **BOTTLENECK** |
| Commit phase 1 (Save DB) | 2-4ms | Disk I/O |
| Commit phase 2 (Backup) | 56-167ms | Serialize + network |

### Vì sao IntermediateRoot chậm?

```
IntermediateRoot(true):
  1. Range dirtyAccounts → clone       ~5ms
  2. Sort keys (fork-safety)           ~2ms
  3. Parallel marshal (32 workers)     ~30ms
  4. muTrie.Lock()
  5. FOR EACH dirty account:           ← BOTTLENECK
       trie.Update(addr, bytes)        ~25µs × 20K = 500ms
  6. Clear dirtyAccounts               ~0ms
  7. trie.Hash()                       ~10ms
```

**Bottleneck:** `trie.Update()` phải sequential — 20K accounts × 25µs = **500ms**.

---

## 2. Phân tích Fork-Safety cho từng phương án tối ưu

### ⚠️ Nguyên tắc Fork-Safety cốt lõi

Hệ thống hiện tại đảm bảo không fork nhờ 3 invariant:

1. **Deterministic stateRoot:** `stateRoot = trie.Hash()` sau khi apply dirty accounts theo thứ tự sorted → tất cả nodes tính cùng hash
2. **Deterministic trie update order:** `slices.SortFunc(keysToProcess)` trước `trie.Update()` → cùng trie structure
3. **Atomic dirty→trie boundary:** Clear `dirtyAccounts` ngay sau apply, trong cùng `muTrie.Lock` → không lẫn entries block N+1

**BẤT KỲ thay đổi nào vi phạm 3 invariant trên → FORK.**

---

### Phase 1: Pipeline Commit — ✅ AN TOÀN

#### Ý tưởng

Tách `trie.Commit()` + `DB.BatchPut()` sang goroutine. `ProcessTransactions` vẫn đợi `IntermediateRoot` nhưng KHÔNG đợi persist.

```
Block N:
  ProcessTransactions → IntermediateRoot(true) → stateRoot (từ TRIE) ✅
  createBlock(stateRoot)
  commitToMemory → AccountStateDB.Commit() → goroutine {
      trie.Commit() → BatchPut → LevelDB    // background, non-blocking
  }
  → Block N+1 bắt đầu NGAY (không đợi persist)
```

#### Fork-Safety Analysis

| Invariant | Ảnh hưởng? | Lý do |
|-----------|-----------|-------|
| Deterministic stateRoot | ✅ Không ảnh hưởng | stateRoot vẫn = `trie.Hash()`, KHÔNG thay đổi |
| Deterministic trie order | ✅ Không ảnh hưởng | Sort + sequential update vẫn giữ nguyên |
| Atomic dirty boundary | ✅ Không ảnh hưởng | Clear dirty vẫn trong `muTrie.Lock` |

#### Rủi ro & Giải pháp

| Rủi ro | Mức độ | Giải pháp |
|--------|--------|-----------|
| Crash trước khi persist xong | Trung bình | Trie nodes vẫn trong memory; block đã broadcast. Node restart sẽ replay từ last persisted. |
| Block N+1 read trie mà N chưa persist | Thấp | `trie.Update()` trong `IntermediateRoot` update **in-memory** trie. Persist chỉ ảnh hưởng LevelDB, không ảnh hưởng memory reads. |
| commitWorker queue full | Thấp | Dùng bounded channel + backpressure. Đợi nếu queue full. |

#### Hiệu quả

| Metric | Trước | Sau | Cải thiện |
|--------|-------|-----|-----------|
| Thời gian block bị block | IntermediateRoot + Commit | IntermediateRoot only | **-56 ~ -167ms/block** |
| TPS impact | — | — | **+15-20%** |
| Fork risk | — | — | **KHÔNG** |

**Kết luận: Phase 1 CÓ THỂ triển khai NGAY. Hash vẫn từ trie, chỉ persist async → KHÔNG CÓ RỦI RO FORK.**

---

### Phase 2: Parallel Trie Update — ⚠️ RỦI RO TRUNG BÌNH

#### Ý tưởng

Partition accounts theo address prefix → update sub-tries song song.

```
IntermediateRoot_Parallel(true):
  1. Partition dirty by addr[0] → 256 buckets
  2. Sort keys within each bucket
  3. Parallel: 256 goroutines × trie.Update(bucket_i)
  4. trie.Hash() → stateRoot
```

#### Fork-Safety Analysis

| Invariant | Ảnh hưởng? | Lý do |
|-----------|-----------|-------|
| Deterministic stateRoot | ⚠️ **CÓ THỂ bị ảnh hưởng** | Merkle Patricia Trie KHÔNG thread-safe. Parallel update vào cùng 1 trie → **DATA RACE** |
| Deterministic trie order | ❌ **BỊ PHẠM** | Thứ tự update giữa các partition không deterministic |
| Atomic dirty boundary | ✅ Không ảnh hưởng | Vẫn clear sau apply |

#### ❌ Vấn đề nghiêm trọng

```
Merkle Patricia Trie:
  - trie.Update() KHÔNG thread-safe (mutates internal nodes)
  - Concurrent write → panic("concurrent map write") hoặc silent corruption
  - Ethereum's trie implementation (go-ethereum) REQUIRES sequential updates
```

**Parallel update trên cùng 1 trie là KHÔNG THỂ với implementation hiện tại.**

#### Giải pháp thay thế: Flatten trie thành 256 independent sub-tries

```
AccountStateDB {
    subTries [256]*MerklePatriciaTrie  // mỗi prefix 1 trie riêng
    metaTrie *MerklePatriciaTrie       // trie of sub-trie roots
}

IntermediateRoot_Parallel:
  1. Parallel: update subTries[i] song song (mỗi trie riêng → no race)
  2. Sequential: update metaTrie with 256 sub-roots
  3. stateRoot = metaTrie.Hash()
```

**Nhưng:**
- Thay đổi trie structure → **KHÔNG TƯƠNG THÍCH** với state hiện tại
- Cần migration toàn bộ trie data
- Sub-nodes sync cần hiểu structure mới
- stateRoot khác hoàn toàn so với hiện tại → **BẮT BUỘC phải upgrade tất cả nodes cùng lúc**

#### Kết luận Phase 2

| Đánh giá | |
|----------|---|
| Fork risk | ⚠️ **Cao nếu implement sai** |
| Complexity | Cao — re-design trie structure |
| Benefit | IntermediateRoot: 500ms → ~50ms |
| Recommendation | **CHƯA NÊN làm**. Ưu tiên Phase 1 + parallel EVM trước |

---

### Phase 3: Flat KV + Deferred Trie + Hash tạm — ❌ NGUY HIỂM CAO

#### Ý tưởng gốc (TÀI LIỆU CŨ)

```
EXECUTE PATH:
  Block N → IntermediateRoot_FAST:
    ├─ Flush dirty → flatKV (LevelDB #2)
    ├─ Hash = Keccak256(sorted dirty entries)   ← "hash tạm"
    └─ return hash tạm
  → createBlock(hash tạm)
  → Block N+1 NGAY

BACKGROUND:
  goroutine → trie.Update() × N → trieHash = trie.Hash()
  → VERIFY: trieHash == hash tạm?
```

#### Fork-Safety Analysis — ❌ CRITICAL

| Invariant | Ảnh hưởng? | Lý do |
|-----------|-----------|-------|
| Deterministic stateRoot | ❌ **BỊ PHẠM NGHIÊM TRỌNG** | Hash tạm ≠ trie hash. Hai thuật toán hoàn toàn khác nhau |
| Deterministic trie order | ✅ Không ảnh hưởng | Background trie vẫn sort |
| Atomic dirty boundary | ⚠️ Phức tạp hơn | Cần quản lý 2 boundary: dirty→flatKV và dirty→trie |

#### ❌ Vấn đề 1: Hash tạm KHÔNG BAO GIỜ bằng trie hash

```
Hash tạm:  Keccak256(sorted(addr1||data1||addr2||data2||...))
Trie hash: MerklePatriciaTrie.Hash()  ← hash qua nhiều tầng internal nodes
```

Hai thuật toán hash **CẤU TRÚC KHÁC NHAU HOÀN TOÀN**:
- Flat hash: hash tất cả entries concatenated
- Trie hash: hash qua cây Merkle (branch nodes, extension nodes, leaf nodes)

**→ Hash tạm LUÔN ≠ trie hash. Verify LUÔN fail.**

#### ❌ Vấn đề 2: Nếu dùng hash tạm thay stateRoot

Nếu block header dùng hash tạm thay vì trie hash:
- **Mất Merkle proof compatibility** — không thể generate proof cho bất kỳ account nào
- **Light client verification** — impossible
- **State sync** — nodes mới không thể verify state nhận được
- **Consensus mismatch** — nếu 1 node restart và rebuild trie, hash tạm nó tính sẽ khác nếu có bất kỳ sai khác nhỏ nào trong serialization

#### ❌ Vấn đề 3: Race condition khi background trie update

```
Block N:    hash_tạm_N → block header
Block N+1:  Bắt đầu execute, đọc từ dirtyAccounts/flatKV
            NHƯNG: background trie N CHƯA xong
            Cold read → trie.Get() → trie CŨ (block N-1) → SAI STATE
                                                            → FORK!
```

Nếu block N+1 có cold read (account chưa trong dirty/LRU), nó đọc từ trie chưa update xong → state cũ → kết quả execute khác → **FORK**.

#### ❌ Vấn đề 4: Crash recovery

```
Block N committed với hash tạm → persisted
Background trie update → CRASH trước khi xong
Node restart → trie ở state N-K → KHÔNG THỂ reproduce hash tạm N
                                → KHÔNG THỂ sync state
```

#### Kết luận Phase 3

| Đánh giá | |
|----------|---|
| Fork risk | ❌ **CỰC CAO — CHẮC CHẮN FORK** |
| Complexity | Rất cao — hash mới, flatDB, WAL, crash recovery |
| Fundamental flaw | Hash tạm ≠ trie hash → **KHÔNG THỂ verify** |
| Recommendation | ❌ **KHÔNG NÊN TRIỂN KHAI** |

---

## 3. Phương án thay thế: Tối ưu IntermediateRoot mà không fork

### Option A: Batch trie.Update() (rủi ro thấp, cải thiện ~2x)

```go
// Hiện tại: N lần trie.Update()
for _, res := range marshalResults {
    trie.Update(addr, bytes)  // 25µs × N
}

// Cải tiến: 1 lần BatchUpdate()
trie.BatchUpdate(sortedEntries)  // Internal optimization trong trie
```

- Nếu trie implementation hỗ trợ batch → giảm overhead per-update
- Fork-safe: vẫn cùng sorted order, cùng trie structure
- Cần modify `MerklePatriciaTrie` thêm `BatchUpdate()`

### Option B: Trie node caching (rủi ro thấp, cải thiện ~1.5x)

```go
// Hiện tại: mỗi trie.Update() resolve nodes từ LevelDB
// Cải tiến: pre-warm trie nodes trong memory trước update
trie.PreWarm(sortedKeys)    // Load tất cả nodes cần thiết vào cache
trie.Update(addr, bytes)    // Không cần disk I/O
```

- Fork-safe: không thay đổi trie structure hay hash algorithm
- Giảm disk I/O trong quá trình update

### Option C: Giảm số dirty accounts (rủi ro thấp, cải thiện tùy workload)

Hiện tại `PreloadAccounts` và `getOrCreateAccountState` đều đưa account vào `dirtyAccounts` ngay cả khi **KHÔNG bị modify**. Điều này làm `IntermediateRoot` phải update trie cho accounts không thay đổi.

```go
// Cải tiến: Chỉ đánh dấu dirty khi THỰC SỰ modify
// Track "clean loaded" accounts riêng, chỉ sync.Map.Store khi modify
```

- Fork-safe: vẫn cùng dirty set, chỉ nhỏ hơn
- Nếu 20K accounts loaded nhưng chỉ 5K modify → IntermediateRoot giảm 4x

---

## 4. Lộ trình khuyến nghị (fork-safe)

| Thứ tự | Phương án | Fork risk | Cải thiện | Effort |
|--------|-----------|-----------|-----------|--------|
| **1** | **Phase 1: Pipeline commit** | ✅ Không | +15-20% TPS | Thấp |
| **2** | **Option C: Giảm dirty count** | ✅ Không | Tùy workload, có thể 2-4x | Trung bình |
| **3** | **Parallel EVM execution** | ✅ Không (đã có) | +200-300% TPS | — |
| **4** | **Option A: Batch trie update** | ✅ Không | ~2x cho IntermediateRoot | Trung bình |
| **5** | **Option B: Trie node caching** | ✅ Không | ~1.5x cho IntermediateRoot | Trung bình |
| ~~6~~ | ~~Phase 2: Parallel trie~~ | ⚠️ Cao | ~~10x IntermediateRoot~~ | ~~Rất cao~~ |
| ~~7~~ | ~~Phase 3: Hash tạm~~ | ❌ Fork | ~~100x IntermediateRoot~~ | ~~Extreme~~ |

### Ưu tiên tức thì

1. **Phase 1 (Pipeline commit)** — Tiết kiệm 56-167ms/block, không rủi ro, implement trong 1 ngày
2. **Option C (Giảm dirty count)** — Nếu hit rate dirty-but-not-modified > 50%, cải thiện rất lớn
3. Sau đó đánh giá lại bottleneck trước khi xem xét Option A/B

---

## 5. Tóm tắt Fork-Safety

```
✅ FORK-SAFE (dùng được ngay):
   • Pipeline commit (async persist)
   • Giảm dirty accounts (chỉ mark dirty khi thực sự modify)
   • Batch trie update (nếu trie support)
   • Trie node pre-warming

⚠️ CẦN CẨN THẬN (phải re-design):
   • Parallel sub-tries (256 independent tries)
   • Cần migration + coordinated upgrade

❌ KHÔNG NÊN LÀM (chắc chắn fork):
   • Hash tạm thay stateRoot
   • Deferred trie với cold reads
   • Bất kỳ thay đổi nào làm stateRoot ≠ trie.Hash()
```

> **Nguyên tắc vàng:** `stateRoot` PHẢI luôn = `MerklePatriciaTrie.Hash()` sau khi apply tất cả dirty accounts theo sorted order. Bất kỳ shortcut nào bypass nguyên tắc này → **FORK**.
