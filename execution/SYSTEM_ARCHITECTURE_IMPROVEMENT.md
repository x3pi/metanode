# MetaNode System Architecture Improvement Roadmap

> **Comprehensive Design Audit & Strategic Optimization Plan**
> 
> Target: 40,000+ TPS Sustained Throughput with Fork-Safe Determinism
> 
> Author: System Architecture Review  
> Date: 2026-04-04  
> Scope: `mtn-consensus` (Rust) + `mtn-simple-2025` (Go)  
> Current Baseline: ~25,000 TPS (warm chain, 5 validators)

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Current Architecture Assessment](#2-current-architecture-assessment)
3. [Critical Design Weaknesses](#3-critical-design-weaknesses)
   - 3.1 [Go-Rust IPC Layer](#31-go-rust-ipc-layer)
   - 3.2 [Go Execution Engine](#32-go-execution-engine)
   - 3.3 [Rust Consensus Engine](#33-rust-consensus-engine)
   - 3.4 [Cross-Cutting Concerns](#34-cross-cutting-concerns)
4. [Performance Bottleneck Analysis](#4-performance-bottleneck-analysis)
5. [Strategic Improvement Recommendations](#5-strategic-improvement-recommendations)
   - 5.1 [Tier 1: Critical (Fork-Safety & Stability)](#51-tier-1-critical-fork-safety--stability)
   - 5.2 [Tier 2: High Impact (Performance)](#52-tier-2-high-impact-performance)
   - 5.3 [Tier 3: Long-Term (Architecture)](#53-tier-3-long-term-architecture)
6. [Implementation Roadmap](#6-implementation-roadmap)
7. [Risk Assessment](#7-risk-assessment)

---

## 1. Executive Summary

The MetaNode system is a sophisticated two-process blockchain architecture: Rust handles DAG-based BFT consensus (ordering), while Go executes transactions and manages world state. After extensive optimization, the system achieves ~25,000 TPS on warm chains. However, deep code analysis reveals **23 design weaknesses** across 5 categories that must be addressed to reach the 40k+ TPS target and ensure long-term stability.

### Key Findings

| Category | Critical | High | Medium | Total |
|----------|----------|------|--------|-------|
| IPC Communication | 1 | 3 | 2 | 6 |
| Go Execution Engine | 1 | 4 | 4 | 9 |
| Rust Consensus Engine | 1 | 2 | 1 | 4 |
| Cross-Cutting (Observability, Testing) | 0 | 1 | 2 | 3 |
| **Total** | **3** | **10** | **9** | **22** |

### Top 5 Blockers for 40k+ TPS

1. **Request socket serialization during epoch transitions** — RPC queries from Rust contend on sequential Go handler
2. **Serialization overhead in BackupDb** — ~2-5s per block for Sub-node replication serialization
3. **`CommitProcessor` lock contention** — `tokio::sync::Mutex` on `epoch_eth_addresses` acquired per commit
4. **`processGroupsConcurrently` goroutine sprawl** — Unbounded goroutine creation per TX group
5. **Trie commit pipeline backlog** — `persistChannel` capacity (1) causes head-of-line blocking

---

## 2. Current Architecture Assessment

### Data Flow (Critical Path)

```
┌─────────────────── CRITICAL PATH (~25ms per block @ 25k TPS) ──────────────────┐
│                                                                                  │
│  Go Sub → UDS → Rust TxSocketServer → DAG Block → Consensus → Leader Election  │
│                    (1ms)                                       (5-10ms)          │
│                                                                                  │
│  → Linearizer → CommitProcessor → UDS → Go Master Listener → BlockProcessor    │
│     (1ms)          (2ms)           (3ms)      (1ms)              (15-30ms)       │
│                                                                                  │
│  → IntermediateRoot → commitToMemoryParallel → commitWorker → broadcastWorker   │
│        (5-15ms)            (5-10ms)               (2ms)          (async)         │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### Strengths (Preserve These)

- ✅ **Ancestor-Only Linearization** — Deterministic sub-DAG commitment via explicit causal traversal
- ✅ **FlatStateTrie** — O(1) reads with additive bucket accumulators, eliminating MPT traversal overhead
- ✅ **CommitPipeline** — AccountStateDB releases locks early, allowing next block's TX execution to overlap
- ✅ **Deterministic GroupTransactionsDeterministic** — Union-Find grouping with canonical sorting
- ✅ **Cold-Start Guard** — Two-layer GEI-based protection prevents fork after snapshot restore
- ✅ **Deferred Commit** — CommitProcessor defers out-of-order commits without blocking core thread

---

## 3. Critical Design Weaknesses

### 3.1 Go-Rust IPC Layer

#### ~~IPC-1: Single-Socket for Block Sending~~ (✅ Thiết kế hợp lý — Không phải lỗi)

**File**: `executor/listener.go`, `executor_client/block_sending.rs`

**Đánh giá lại**: Blocks từ Rust → Go **bắt buộc phải xử lý tuần tự** theo `global_exec_index`. Go Master xử lý block N xong mới xử lý block N+1. Do đó, single socket cho việc gửi block là thiết kế **hoàn toàn hợp lý**:

1. `ExecutorClient.send_buffer` dùng `next_expected_index` đảm bảo thứ tự chặt chẽ
2. Go `dataChan` tiêu thụ blocks tuần tự theo GEI
3. Song song hóa socket gửi block sẽ **không tăng throughput** mà chỉ thêm phức tạp về ordering
4. UDS write 5-10MB chỉ mất ~2-3ms — không phải nút thắt thực sự so với 15-30ms xử lý block

**Kết luận**: Giữ nguyên thiết kế single socket cho block sending. Tập trung tối ưu vào **request socket** (IPC-2) — nơi thực sự có tranh chấp do nhiều RPC query đồng thời.

---

#### IPC-2: Request Socket Serialization (🔴 Critical)

**File**: `executor_client/rpc_queries.rs`, `executor/unix_socket.go`

The Go-side `SocketExecutor.handleConnection()` processes requests **sequentially** per connection. The Rust `request_connection` is a single `Arc<Mutex<Option<SocketStream>>>`:

```rust
// mod.rs:54 — Single request connection
pub(crate) request_connection: Arc<Mutex<Option<SocketStream>>>,
```

While a `request_pool` (size=4) was added, the Go side `handleConnection` loop is still sequential per connection — multiple concurrent Rust queries will queue in the Go handler.

**Impact**: During epoch transitions, `CommitProcessor` calls `get_epoch_boundary_data()` which contends with `EpochMonitor` calling `get_current_epoch()`, `get_last_block_number()`, and `get_last_global_exec_index()`. Under load, these queries can stall for 200ms+.

**Recommendation**: 
1. Go side: spawn goroutine per request within `handleConnection`, use a semaphore to limit concurrency
2. Rust side: increase pool size to 8 and add request-type priority queuing

---

#### IPC-3: Backpressure Mechanism is Sleep-Based (🟡 High)

**File**: `executor/listener.go:202-209`

```go
if chLen > 9000 {
    time.Sleep(50 * time.Millisecond)  // Crude backpressure
} else if chLen > 5000 {
    time.Sleep(10 * time.Millisecond)
}
```

This sleep-based backpressure has two problems:
1. **Imprecise** — Sleep(50ms) at 40k TPS means ~2000 TXs are delayed, but the exact queue depth fluctuates
2. **Uncoordinated** — Rust has no visibility into Go's processing speed. The `go_lag_handle` exists in `ExecutorClient` but the feedback loop is incomplete

**Recommendation**: Implement IPC-level ACK flow control:
- Go sends periodic heartbeat messages indicating queue depth
- Rust `SystemTransactionProvider` uses `go_lag_handle` (already partially wired) to dynamically adjust `min_round_delay`

---

#### IPC-4: Protobuf Serialization Overhead (🟡 High)

**File**: `executor_client/block_sending.rs`, `executor/listener.go`

Every committed sub-DAG is serialized to `ExecutableBlock` protobuf, sent over UDS, and deserialized on Go side. For a 50k TX block:
- Serialization: ~3-5ms (Rust side)
- Wire transfer: ~2-5ms (UDS)
- Deserialization: ~2-3ms (Go side)
- **Total: ~7-13ms per block just for IPC**

**Recommendation**: 
1. Use **zero-copy serialization** (FlatBuffers or Cap'n Proto) to eliminate decode overhead
2. Consider **shared memory** (mmap) for localhost deployments — transactions are already byte arrays

---

#### IPC-5: Missing Health Check on Send Socket (🟡 High)

**File**: `executor_client/mod.rs:366-427`

The `connect()` method uses exponential backoff to connect, but has **no periodic health check** on established connections. The `writable()` check in line 371 only detects closed connections, not degraded ones.

```rust
match stream.writable().await {
    Ok(_) => { /* seems alive, but could be slow */ }
    Err(e) => { *conn_guard = None; /* reconnect */ }
}
```

**Impact**: A half-open TCP connection (common in distributed deployments) will appear writable but silently drop data.

**Recommendation**: Add periodic ping/pong messages or TCP keepalive probes:
```rust
// In connect(): Configure TCP keepalive
stream.set_keepalive(Some(Duration::from_secs(10)))?;
```

---

#### IPC-6: Listener Semaphore Too Small for Burst Recovery (🟢 Medium)

**File**: `executor/listener.go:68`

```go
sem: make(chan struct{}, 50), // Limit to 50 concurrent connections
```

During epoch transitions or snapshot restores, multiple Rust connection attempts may happen simultaneously. A limit of 50 is reasonable for steady-state but may reject valid connections during recovery bursts.

**Recommendation**: Increase to 100 and add a metric for rejected connections.

---

#### IPC-7: Buffer Pool Memory Retention (🟢 Medium)

**File**: `executor/listener.go:19-43`

```go
var bufferPool = sync.Pool{
    New: func() interface{} {
        b := make([]byte, 1024*1024) // 1MB initial
        return &b
    },
}
```

The pool caps recycled buffers at 10MB but has no upper bound on the number of pooled buffers. Under burst traffic (e.g., 100 connections simultaneously reading 5MB messages), the pool will accumulate ~500MB of buffers that are never returned.

**Recommendation**: Add pool size tracking with periodic drain of excess buffers.

---

### 3.2 Go Execution Engine

#### GO-1: `GenerateBlock` Ticker Race with Consensus-Driven Flow (🔴 Critical)

**File**: `block_processor_processing.go:23-93`

`GenerateBlock()` uses a **time-based ticker** (100ms) combined with a `processResults` channel to decide when to create blocks. This creates a fundamental design tension with the Rust consensus-driven model:

```go
blockTicker := time.NewTicker(100 * time.Millisecond)  // Time-based flush
for {
    select {
    case processResults := <-bp.transactionProcessor.ProcessResultChan:
        // Accumulate results...
        if len(accumulatedResults.Transactions) >= minTxsForImmediateBlock {
            // Create block immediately
        }
    case <-blockTicker.C:
        // Flush accumulated results on timer
    }
}
```

**Problems**:
1. The `select` statement has **non-deterministic priority** — under load, the ticker case may fire before all available results are consumed
2. `minTxsForImmediateBlock = 1000` means blocks with 999 TXs wait up to 100ms unnecessarily
3. The `ProcessResultChan` has buffer size 1, creating head-of-line blocking when `createBlockFromResults` is processing

**Recommendation**: Replace with a pure event-driven model where block creation is triggered by Rust commit arrival:
```go
// Instead of ticker + channel, use direct callback from executor listener
func (bp *BlockProcessor) onRustCommit(execBlock *pb.ExecutableBlock) {
    // Process immediately — no accumulation needed since Rust already batched
}
```

---

#### GO-2: Duplicated Block Iteration in `prepareBackupData` (🟡 High)

**File**: `block_processor_commit.go:454-592`

`prepareBackupData()` iterates over `processResults.Transactions` **twice** (lines 508-533 and 551-561) to handle MVM contract commit/revert and unprotect. This is O(2N) where it could be O(N):

```go
// First pass (line 508): CommitFullDb / RevertFullDb
for _, tx := range processResults.Transactions { ... }

// Second pass (line 551): UnprotectMVMApi  
for _, tx := range processResults.Transactions { ... }
```

**Recommendation**: Merge into a single pass with deferred unprotect.

---

#### GO-3: `commitToMemoryParallel` Creates N Goroutines Despite Fixed Task Count (🟡 High)

**File**: `block_processor_commit.go:598-745`

The function creates 7 goroutines (2 base + 5 state-changing) per block. At 100 blocks/s, this is 700 goroutine creations/s. While individually cheap (~4KB stack), the `sync.WaitGroup` + `chan taskResult` pattern creates GC pressure:

```go
var wg sync.WaitGroup
resultsChan := make(chan taskResult, totalTasks)  // Allocated per block!
```

**Recommendation**: Use a fixed worker pool with pre-allocated result buffers. `errgroup.Group` from `golang.org/x/sync` would be cleaner.

---

#### GO-4: `ProcessorPool` Busy-Wait Spin Loop (🟡 High)

**File**: `block_processor_processing.go:97-133`

```go
func (bp *BlockProcessor) ProcessorPool() {
    for {
        if bp.transactionProcessor.transactionPool.CountTransactions() > 0 || ... {
            select {
            case bp.processingLockChan <- struct{}{}:
                // Process...
            default:
                // Skip
            }
        }
        time.Sleep(100 * time.Microsecond) // Spin loop!
    }
}
```

This is a classic busy-wait anti-pattern. Even with 100µs sleep, at idle this consumes significant CPU cycles polling `CountTransactions()` which itself takes a lock.

**Recommendation**: Replace with a channel-signaled approach:
```go
func (bp *BlockProcessor) ProcessorPool() {
    for range bp.transactionPool.NotifyChannel() { // Block until TXs arrive
        // Process immediately
    }
}
```

---

#### GO-5: `broadcastBlockToNetwork` Goroutine Per Send Attempt (🟡 High)

**File**: `block_processor_commit.go:358-360`

Inside the bounded worker pool (good), each retry still spawns a goroutine for the actual send:

```go
done := make(chan error, 1)
go func() {
    done <- bp.messageSender.SendBytes(c, p_common.BlockDataTopic, backupData)
}()
```

This creates **`maxRetries × connections`** goroutines per block. With 3 retries and 10 connections, that's 30 goroutines per block.

**Recommendation**: Use `context.WithTimeout` on the send call itself rather than goroutine+select:
```go
ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
defer cancel()
err := bp.messageSender.SendBytesCtx(ctx, c, topic, data)
```

---

#### GO-6: `TxsProcessor2` Batch Accumulation Adds Latency (🟢 Medium)

**File**: `block_processor_txs.go:723-755`

```go
const batchAccumulationTimeout = 100 * time.Millisecond
const minBatchSize = 40000
```

The batch accumulation loop waits up to 100ms for the pool to reach 40k TXs. At steady-state 25k TPS, this means every batch has a built-in 100ms delay before TXs are sent to Rust. At 40k TPS, the pool fills faster, but the "stagnant check" (5 cycles × 2ms = 10ms) still adds unnecessary latency.

**Recommendation**: Use an adaptive timeout based on current ingestion rate:
```go
timeout := max(10ms, min(100ms, estimatedFillTime / 2))
```

---

#### GO-7: Global Singleton `blockchain.GetBlockChainInstance()` (🟢 Medium)

**File**: Multiple files across `block_processor_*.go`

The `GetBlockChainInstance()` pattern is used ~30 times across the block processor. This global singleton prevents:
1. Unit testing with isolated state
2. Multiple chain instances for parallel validation
3. Clean dependency injection

**Recommendation**: Pass `BlockChain` as a constructor parameter to `BlockProcessor`.

---

#### GO-8: `postProcessBlock` Opens Two Trie Roots Sequentially (🟢 Medium)

**File**: `block_processor_txs.go:508-566`

```go
txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(...)  // Opens trie from root
rcpDb, err := receipt.NewReceiptsFromRoot(...)                        // Opens another trie
```

Both trie root openings happen sequentially. They are independent and could be parallelized.

**Recommendation**: Open both tries in parallel goroutines.

---

#### GO-9: `time.NewTicker(30ms)` in `TxsProcessor` Main Loop (🟢 Medium)

**File**: `block_processor_txs.go:121`

```go
ticker := time.NewTicker(30 * time.Millisecond)
```

A 30ms ticker for sequential block processing on Sub nodes limits theoretical throughput to ~33 blocks/s. If blocks are small, this is the bottleneck.

**Recommendation**: Use a channel-signaled approach instead of a fixed ticker. Process blocks as fast as they arrive.

---

### 3.3 Rust Consensus Engine

#### RS-1: `CommitProcessor` Lock Contention on `epoch_eth_addresses` (🔴 Critical)

**File**: `commit_processor.rs:596-767`

Every commit acquires `validator_eth_addresses.lock().await` to resolve the leader address. This is a `tokio::sync::Mutex`, which means:
1. If the epoch transition callback holds this lock (it inserts new epoch data), all commits block
2. The retry loop (10 retries × 200ms) can hold the lock for up to 2 seconds

```rust
let resolved_address = loop {
    let epoch_addresses = validator_eth_addresses.lock().await;  // ← CONTENTION
    // ... validation logic ...
    break Some(addr);
};
```

**Impact**: During epoch transitions, this can stall 5-10 commits (100ms each), causing a 500ms-1s gap in block production.

**Recommendation**: 
1. Use `tokio::sync::RwLock` — reads (leader lookup) can be concurrent, only writes (epoch update) need exclusivity
2. Cache the current epoch's committee in a `Arc<ArcSwap<Vec<Vec<u8>>>>` for lock-free reads

---

#### RS-2: `cumulative_fragment_offset` Not Checkpointed (🟡 High)

**File**: `commit_processor.rs:270`

```rust
let mut cumulative_fragment_offset: u64 = 0;
```

This offset tracks extra GEIs consumed by block fragmentation and is critical for GEI calculation. It is initialized to 0 on every restart and never persisted. If the node restarts mid-epoch with fragmented commits, the offset is lost, causing GEI mismatch → fork.

**Recommendation**: Persist `cumulative_fragment_offset` alongside `epoch_base_index` in the crash recovery checkpoint.

---

#### RS-3: Deferred TX Tracking Spawns Unbounded Tasks (🟡 High)

**File**: `commit_processor.rs:922-947`

When `try_lock()` fails during TX tracking, a `tokio::spawn` is used for deferred tracking with a 500ms sleep:

```rust
tokio::spawn(async move {
    tokio::time::sleep(Duration::from_millis(500)).await;
    if let Ok(guard) = node_arc_clone.try_lock() { ... }
});
```

Under rapid epoch transitions, multiple deferred tasks can accumulate. Each task clones the entire transaction data (`subdag_blocks: Vec<Vec<Vec<u8>>>`), potentially consuming significant memory.

**Recommendation**: Use a bounded queue for deferred tracking tasks:
```rust
let (tx, rx) = tokio::sync::mpsc::channel(100);
// Spawn single consumer task
tokio::spawn(async move {
    while let Some(batch) = rx.recv().await { /* track */ }
});
```

---

#### RS-4: `EpochMonitor` Polling Interval Not Adaptive (🟢 Medium)

**File**: `epoch_monitor.rs:47,57`

```rust
let poll_interval_secs = config.epoch_monitor_poll_interval_secs.unwrap_or(10);
tokio::time::sleep(Duration::from_secs(poll_interval_secs)).await;
```

The epoch monitor uses a fixed 10-second poll interval. During epoch transitions, faster polling would detect changes sooner. During steady-state, slower polling would reduce IPC overhead.

**Recommendation**: Implement exponential backoff with fast-path:
- Normal: 10s interval
- After detecting a gap: 1s interval for 30s, then back to 10s

---

### 3.4 Cross-Cutting Concerns

#### CC-1: Observability Gap — No End-to-End Latency Tracing (🟡 High)

**Current State**: Each component logs its own timing (`[PERF]` tags), but there is no way to trace a transaction from Go Sub ingestion → Rust consensus → Go Master execution → Sub-node broadcast.

**Impact**: Performance regressions require manual log correlation across two processes and multiple log files.

**Recommendation**: Implement distributed tracing with a shared `trace_id`:
1. Go Sub assigns a `batch_id` when forwarding TXs to Rust
2. Rust propagates `batch_id` through `CommittedSubDag`
3. Go Master logs `batch_id` at each pipeline stage
4. Use OpenTelemetry spans for structured trace export

---

#### CC-2: Mixed Vietnamese/English Comments (🟢 Medium)

**Files**: `block_processor_txs.go`, `executor/listener.go`, `grouptxns.go`, etc.

Comments alternate between Vietnamese and English, reducing readability for international contributors. Critical invariants are sometimes only documented in one language.

**Recommendation**: Standardize all comments to English, especially safety-critical invariants.

---

#### CC-3: Insufficient Unit Test Coverage for IPC Error Paths (🟢 Medium)

**Current State**: `executor_client/mod.rs` has tests for client creation/defaults but no tests for:
- Connection timeout behavior
- Circuit breaker tripping and recovery
- Buffer overflow handling
- Out-of-order commit processing

**Recommendation**: Add integration tests using mock UDS sockets.

---

## 4. Performance Bottleneck Analysis

### Bottleneck Ranking (Impact on 40k TPS Target)

| Rank | Component | Bottleneck | Current Impact | 40k TPS Impact |
|------|-----------|-----------|----------------|----------------|
| 1 | Go Master | `IntermediateRoot` (FlatStateTrie) | 5-15ms/block | 15-30ms at 2x TXs |
| 2 | Go Master | `prepareBackupData` serialization | 2-5s/block (async) | Backlog buildup |
| 3 | Rust | `CommitProcessor` leader resolution | 1-2ms (steady) | 500ms during epoch |
| 4 | IPC | Request socket contention (RPC queries) | 1-5ms (steady) | 200ms+ during epoch |
| 5 | Go Master | `commitToMemoryParallel` | 5-10ms/block | Mostly parallelizable |
| 6 | Go Sub | `TxsProcessor` 30ms ticker | 33 blocks/s cap | Sub throughput limit |
| 7 | Go Master | `ProcessorPool` busy-wait | CPU waste | Resource contention |

> **Note**: Block sending via single UDS socket (IPC-1) is **NOT a bottleneck** — blocks are inherently sequential by `global_exec_index`, so parallelizing the send socket would not improve throughput.

### Theoretical Maximum

With all optimizations applied:
- IntermediateRoot: 5ms (FlatStateTrie with cached old values) ✅ Already optimized
- IPC send: 2ms (single UDS connection — sequential by design, adequate bandwidth)
- TX execution: 10ms (parallel groups, 16 cores)
- Block creation: 2ms
- Commit pipeline: 5ms (overlapped with next block)
- **Total critical path: ~23ms → ~43 blocks/s → 43,000 TPS at 1000 TX/block**

This confirms **40k+ TPS is achievable** with the optimizations in this document.

---

## 5. Strategic Improvement Recommendations

### 5.1 Tier 1: Critical (Fork-Safety & Stability)

#### T1-1: Persist `cumulative_fragment_offset` (RS-2)
- **Effort**: 2 hours
- **Risk**: Low (additive change to checkpoint logic)
- **Files**: `commit_processor.rs`, `epoch_checkpoint.rs`

#### T1-2: Replace `epoch_eth_addresses` Mutex with RwLock (RS-1)
- **Effort**: 1 hour  
- **Risk**: Low (read paths don't change, only lock type)
- **File**: `commit_processor.rs`

#### T1-3: Add connection health monitoring to IPC sockets (IPC-5)
- **Effort**: 4 hours
- **Risk**: Medium (need to handle reconnection during active sends)
- **Files**: `executor_client/mod.rs`, `executor_client/socket_stream.rs`

#### T1-4: Fix `GenerateBlock` non-deterministic select priority (GO-1)
- **Effort**: 8 hours
- **Risk**: High (core pipeline change, needs extensive testing)
- **File**: `block_processor_processing.go`

---

### 5.2 Tier 2: High Impact (Performance)

#### ~~T2-1: IPC Connection Pool for Block Sending (IPC-1)~~ — REMOVED
- **Status**: Not needed — blocks are sequential by `global_exec_index`, single socket is correct design
- **Rationale**: Pooling would add ordering complexity without throughput benefit

#### T2-2: Replace Sleep-Based Backpressure with ACK Flow Control (IPC-3)
- **Effort**: 8 hours
- **Risk**: Medium (bidirectional IPC changes)
- **Expected Impact**: Smoother throughput under load, fewer stalls

#### T2-3: Eliminate `ProcessorPool` Busy-Wait (GO-4)
- **Effort**: 4 hours
- **Risk**: Low (channel-based replacement is standard pattern)
- **Expected Impact**: 5-10% CPU savings

#### T2-4: Merge Duplicate TX Iteration in `prepareBackupData` (GO-2)
- **Effort**: 2 hours
- **Risk**: Low (logic consolidation)
- **Expected Impact**: ~1ms per block for MVM-heavy blocks

#### T2-5: Bounded Deferred TX Tracking Queue (RS-3)
- **Effort**: 4 hours
- **Risk**: Low (prevents memory leak during rapid transitions)
- **Expected Impact**: Stability during epoch transitions

#### T2-6: End-to-End Distributed Tracing (CC-1)
- **Effort**: 16 hours
- **Risk**: Low (additive, no behavior change)
- **Expected Impact**: 10x faster performance debugging

---

### 5.3 Tier 3: Long-Term (Architecture)

#### T3-1: Zero-Copy IPC with Shared Memory (IPC-4)
- **Effort**: 40 hours
- **Risk**: High (significant IPC refactor)
- **Expected Impact**: 5-10ms reduction per block, enabling 50k+ TPS

#### T3-2: Replace Global Singletons with Dependency Injection (GO-7)
- **Effort**: 24 hours
- **Risk**: Medium (widespread refactor)
- **Expected Impact**: Testability and maintainability improvement

#### T3-3: Event-Driven `GenerateBlock` from Rust Commit Stream (GO-1)
- **Effort**: 24 hours
- **Risk**: High (fundamental pipeline change)
- **Expected Impact**: Eliminates ticker latency, ~5ms savings per block

#### T3-4: Adaptive Epoch Monitor Polling (RS-4)
- **Effort**: 4 hours
- **Risk**: Low
- **Expected Impact**: Faster epoch transitions, reduced IPC during steady-state

---

## 6. Implementation Roadmap

### Phase 1: Quick Wins (1 week)
| Task | ID | Effort | Priority |
|------|----|--------|----------|
| RwLock for `epoch_eth_addresses` | T1-2 | 1h | P0 |
| Persist `cumulative_fragment_offset` | T1-1 | 2h | P0 |
| Merge duplicate TX iteration | T2-4 | 2h | P1 |
| Eliminate `ProcessorPool` busy-wait | T2-3 | 4h | P1 |
| Bounded deferred TX tracking | T2-5 | 4h | P1 |

**Expected Result**: ~5% throughput improvement + stability hardening

### Phase 2: IPC Optimization (2 weeks)
| Task | ID | Effort | Priority |
|------|----|--------|----------|
| ~~IPC connection pool for sends~~ | ~~T2-1~~ | — | REMOVED |
| ACK-based backpressure | T2-2 | 8h | P1 |
| Connection health monitoring | T1-3 | 4h | P1 |
| Listener semaphore increase | IPC-6 | 1h | P2 |

**Expected Result**: ~15% throughput improvement, smoother under load

### Phase 3: Pipeline Overhaul (3 weeks)
| Task | ID | Effort | Priority |
|------|----|--------|----------|
| Event-driven `GenerateBlock` | T3-3 | 24h | P0 |
| Distributed tracing | T2-6 | 16h | P1 |
| `GenerateBlock` select priority fix | T1-4 | 8h | P0 |
| Adaptive epoch monitor | T3-4 | 4h | P2 |

**Expected Result**: 35-40k TPS sustained

### Phase 4: Architecture Evolution (6 weeks)
| Task | ID | Effort | Priority |
|------|----|--------|----------|
| Zero-copy IPC (shared memory) | T3-1 | 40h | P1 |
| DI refactor (remove singletons) | T3-2 | 24h | P2 |
| Comment standardization | CC-2 | 8h | P3 |
| IPC integration tests | CC-3 | 16h | P2 |

**Expected Result**: 45k+ TPS, production-grade observability

---

## 7. Risk Assessment

### Highest Risk Changes

| Change | Risk | Mitigation |
|--------|------|------------|
| Event-driven `GenerateBlock` (T3-3) | Block production flow changes | A/B test alongside existing ticker for 1 week |
| ~~IPC connection pool (T2-1)~~ | REMOVED — blocks are sequential, pooling unnecessary | N/A |
| Zero-copy IPC (T3-1) | Platform-specific mmap behavior | Keep UDS fallback; gate behind feature flag |
| `GenerateBlock` select fix (T1-4) | Changes core commit pipeline | Run 10-round 50K TPS stress test before merge |

### Testing Requirements for Each Phase

1. **Phase 1**: `cargo test` + `go test ./...` + 5-round 50K TPS blast
2. **Phase 2**: Full cluster restart test + block_hash_checker across 5 nodes + epoch transition test
3. **Phase 3**: 10-round 100K TPS blast + epoch transition across 10 epochs + snapshot restore test
4. **Phase 4**: Week-long soak test at 25K sustained TPS + chaos testing (kill random nodes)

---

## Appendix A: File Index

### Key Go Files Analyzed
| File | Lines | Purpose |
|------|-------|---------|
| `cmd/simple_chain/processor/block_processor_processing.go` | 351 | Block creation pipeline |
| `cmd/simple_chain/processor/block_processor_commit.go` | 784 | Commit + broadcast workers |
| `cmd/simple_chain/processor/block_processor_txs.go` | 981 | Sub-node TX processing |
| `cmd/simple_chain/processor/constants.go` | 135 | Tuning constants |
| `executor/unix_socket.go` | 441 | IPC dispatcher |
| `executor/listener.go` | 238 | Commit listener |
| `pkg/blockchain/tx_processor/tx_processor.go` | 863 | Parallel TX execution |
| `pkg/trie/flat_state_trie.go` | 864 | O(1) state trie |
| `pkg/grouptxns/grouptxns.go` | 582 | Union-Find TX grouping |
| `pkg/account_state_db/account_state_db_commit.go` | ~700 | IntermediateRoot + CommitPipeline |

### Key Rust Files Analyzed
| File | Lines | Purpose |
|------|-------|---------|
| `metanode/src/consensus/commit_processor.rs` | 1036 | Commit ordering + leader resolution |
| `metanode/src/node/epoch_monitor.rs` | 502 | Epoch transition monitor |
| `metanode/src/node/executor_client/mod.rs` | 629 | IPC client to Go |
| `meta-consensus/core/src/linearizer.rs` | 1061 | DAG → linear sequence |

---

## Appendix B: Terminology

| Term | Definition |
|------|-----------|
| **GEI** | Global Execution Index — universal block height counter across epochs |
| **FlatStateTrie** | O(1) read/write state storage with additive bucket accumulators |
| **CommitPipeline** | AccountStateDB optimization that releases locks after nodeSet generation |
| **Sub-DAG** | Set of blocks transitively referenced by a committed leader block |
| **Cold-Start** | Node startup from snapshot with empty DAG storage |
| **Fragment Offset** | Extra GEIs consumed when a large commit is split across multiple Go blocks |
| **Pillar 47** | Advance-First protocol: Rust notifies Go *before* fetching new committee |
