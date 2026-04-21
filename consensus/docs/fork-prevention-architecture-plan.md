# Fork Prevention Architecture Plan

> **Created**: 2026-04-20  
> **Context**: Hash mismatch detected at blocks 204-205 across 5 nodes (m0-m4).  
> m1 diverged completely (different parentHash chain), m0 transiently diverged.  
> Root cause: non-deterministic state during snapshot restore / slow restart catch-up.

---

## 1. Problem Analysis

### 1.1 Current Fork Vectors (Ordered by Severity)

| # | Vector | Severity | Description |
|---|--------|----------|-------------|
| F1 | **Stale stateRoot after sync** | CRITICAL | `applyBackupDbBatches` writes to LevelDB/PebbleDB but AccountStateDB in-memory cache retains old data. `IntermediateRoot()` computes hash from stale cache → different stateRoot → different block hash. `InvalidateAllCaches()` was added but only for execute-mode; store-only path still exposed. |
| F2 | **parentHash race (bp.lastBlock)** | CRITICAL | Two writers update `bp.lastBlock`: consensus commitWorker and sync handler's `updateLastBlockCallback`. If sync updates after consensus reads `GetLastBlock()` but before block creation → wrong parentHash. |
| F3 | **GEI calculation divergence** | HIGH | `epoch_base_index` + `cumulative_fragment_offset` must be identical on all nodes. After cold-start, if `epoch_base_index` differs (stale `epoch_data_backup.json`), all subsequent GEIs diverge → Go creates blocks at wrong positions. |
| F4 | **Dual block-creation paths** | HIGH | Blocks are created via: (a) normal consensus `createBlockFromResults`, (b) sync `handleSyncBlocksExecuteMode`. Path (b) applies state from BackupDb which may differ from path (a)'s live execution if BackupDb is stale or incomplete. |
| F5 | **Timestamp fallback to `time.Now()`** | MEDIUM | `GenerateBlockData` falls back to `time.Now()` when `timestampSec == 0`. During restart, if Rust sends commits before timestamp is synced, each node uses its local clock → different hash. |
| F6 | **Store-only mode still reachable** | MEDIUM | `HandleSyncBlocksRequest` without `execute_mode=true` stores blocks without executing state. If this path runs during catch-up, NOMT state falls behind stored blocks → stateRoot freeze → fork. |

### 1.2 Fork Scenario (What Happened at Block 204-205)

```
Timeline:
  T0: m1 restarts from snapshot (block ~190, epoch N)
  T1: m1 enters cold-start, Phase 1 peer sync begins
  T2: m1 syncs blocks 191-203 via P2P (store-only OR execute mode with stale BackupDb)
  T3: m1 Phase 1 complete, joins consensus
  T4: Consensus commit for block 204 arrives
  T5: m1 creates block 204 BUT:
      - bp.lastBlock points to locally-synced block 203 (potentially different hash than network's block 203)
      - OR: AccountStateDB cache has stale state from sync → different stateRoot
      → Block 204 hash ≠ network's block 204 hash
  T6: m1's block 205 inherits the wrong parentHash → cascading fork
  
  m0 had a similar transient issue but self-healed (possibly via CHAIN REPAIR logic)
```

---

## 2. Architecture Redesign: "Sync-Execute-Verify" (SEV) Protocol

### 2.1 Core Principle

**A node MUST NOT create new blocks until its local state is provably identical to the network's state at the catch-up point.**

This requires three guarantees:
1. **State Integrity**: Every synced block must be fully executed (not just stored)
2. **State Verification**: After catch-up, local stateRoot must match network's stateRoot
3. **Atomic Transition**: Switch from sync to consensus must be atomic (no window for stale state)

### 2.2 Phase Design

```
┌─────────────────────────────────────────────────────────────┐
│                    NODE RESTART / RESTORE                     │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Phase 0: FREEZE                                             │
│  ├── Stop consensus authority (if running)                   │
│  ├── Flush all pending writes to disk                        │
│  └── Capture snapshot_gei, snapshot_block, snapshot_stateRoot│
│                                                              │
│  Phase 1: SYNC-EXECUTE (current execute_mode, improved)      │
│  ├── Fetch blocks from peers in batches                      │
│  ├── Execute each block through NOMT (CommitBlockState)      │
│  ├── Verify stateRoot after EACH block matches peer's block  │ ← NEW
│  └── On mismatch: HALT and alert (do not continue)           │ ← NEW
│                                                              │
│  Phase 2: STATE VERIFICATION GATE                            │ ← NEW
│  ├── Query 2f+1 peers for block N's stateRoot                │
│  ├── Compare local stateRoot at block N                      │
│  ├── If match: proceed to Phase 3                            │
│  ├── If mismatch: re-sync from last known good block         │
│  └── Max retries: 3, then require manual intervention        │
│                                                              │
│  Phase 3: ATOMIC TRANSITION                                  │
│  ├── Lock bp.lastBlock (prevent any writes)                  │
│  ├── Set bp.lastBlock = verified block N                     │
│  ├── Set chainState.currentBlockHeader = block N header      │
│  ├── Set GEI = block N's GEI                                 │
│  ├── Invalidate ALL caches (Account, Stake, SC, MVM, Trie)   │
│  ├── Rebuild tries from block N's roots                      │
│  ├── Unlock bp.lastBlock                                     │
│  └── Start consensus authority                               │
│                                                              │
│  Phase 4: DUAL-STREAM (existing, improved)                   │
│  ├── Consensus produces blocks from N+1                      │
│  ├── Sync stream disabled (consensus is authoritative)        │
│  └── If consensus block's parentHash ≠ block N hash: PANIC   │ ← NEW
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. Implementation Plan

### 3.1 Eliminate Store-Only Sync Path [F6] — Priority: CRITICAL

**Goal**: Remove the non-execute path from `HandleSyncBlocksRequest` for Master nodes.

**Changes**:
- File: `executor/unix_socket_handler_epoch.go`
- When `ServiceType == "MASTER"`, always force `execute_mode = true` regardless of the request flag
- The store-only path should only remain for Sub nodes (read-only replicas)

```
Impact: Prevents state divergence from blocks stored without execution
Risk: Low — execute mode already works, this just makes it mandatory
```

### 3.2 State Verification Gate [F1, F4] — Priority: CRITICAL

**Goal**: After sync-execute catch-up, verify local state matches network before joining consensus.

**New component**: `StateVerifier` (Go side)
- After Phase 1 completes, Rust calls `VerifyStateRoot(block_number)` on Go
- Go returns `{block_number, state_root, stake_root, block_hash}`
- Rust queries 2f+1 peers for same data
- Proceeds only if quorum agrees with local state

**Changes**:
- File: `consensus/metanode/src/node/startup.rs` — Add Phase 2 between sync and authority start
- File: `executor/unix_socket_handler_epoch.go` — New `HandleVerifyStateRequest` handler
- File: `consensus/metanode/src/node/catchup.rs` — New `verify_state_with_peers` method

```
Impact: Catches ANY state divergence before it can produce forked blocks
Risk: Medium — adds latency to restart (one extra RPC round), but prevents all fork types
```

### 3.3 Single-Writer bp.lastBlock [F2] — Priority: CRITICAL

**Goal**: Only ONE goroutine/path may update `bp.lastBlock`.

**Current problem**:
- `createBlockFromResults` calls `bp.SetLastBlock(bl)` (consensus path)
- `updateLastBlockCallback` calls `bp.SetLastBlock(bl)` (sync path)
- These can race

**Solution**: Introduce a `blockPointerLock sync.Mutex` in BlockProcessor
- `SetLastBlock` acquires lock
- `GetLastBlock` acquires read lock (or use `atomic.Pointer` with CAS)
- During sync: `SetLastBlock` is called, but consensus is paused → no race
- During consensus: sync handler MUST NOT call `SetLastBlock`

**Changes**:
- File: `cmd/simple_chain/processor/block_processor_core.go` — Add mutex to `SetLastBlock`/`GetLastBlock`
- File: `executor/unix_socket_handler_epoch.go` — Remove `updateLastBlockCallback` during consensus mode

```
Impact: Eliminates parentHash race
Risk: Low — consensus goroutine is paused during sync, so lock contention is minimal
```

### 3.4 Mandatory Timestamp from Rust [F5] — Priority: HIGH

**Goal**: Never fall back to `time.Now()` for block creation.

**Changes**:
- File: `cmd/simple_chain/processor/block_processor_utils.go`
- Change `GenerateBlockData`: if `timestampSec == 0`, **panic** instead of using `time.Now()`
- Rust must always provide `commit_timestamp_ms` in every commit

```
Impact: Eliminates non-deterministic timestamps
Risk: Medium — requires verifying ALL commit paths send timestamp
```

### 3.5 Per-Block StateRoot Verification During Sync [F1] — Priority: HIGH

**Goal**: During execute-mode sync, verify each block's stateRoot after execution.

**Changes**:
- File: `executor/unix_socket_handler_epoch.go` in `handleSyncBlocksExecuteMode`
- After `CommitBlockState`, compare local trie root with block header's `AccountStatesRoot`
- If mismatch: log error with full debug info and **STOP** sync (do not continue with wrong state)

```go
// After CommitBlockState:
localRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
expectedRoot := header.AccountStatesRoot()
if localRoot != expectedRoot && expectedRoot != (common.Hash{}) {
    logger.Error("🚨 [STATE VERIFY] Block #%d stateRoot MISMATCH! local=%s expected=%s. HALTING sync.",
        blockNum, localRoot.Hex(), expectedRoot.Hex())
    return &pb.SyncBlocksResponse{Error: "stateRoot mismatch"}, fmt.Errorf("stateRoot mismatch at block %d", blockNum)
}
```

```
Impact: Catches state divergence at the earliest possible point
Risk: Low — verification is a single hash comparison per block
```

### 3.6 Cache Invalidation Hardening [F1] — Priority: HIGH

**Goal**: Ensure ALL caches are invalidated in ALL sync paths, not just execute-mode.

**Changes**:
- Create `chainState.InvalidateAllState()` method that calls:
  - `GetAccountStateDB().InvalidateAllCaches()`
  - `GetStakeStateDB().InvalidateAllCaches()`
  - `GetSmartContractDB().InvalidateAllCaches()`
  - `mvm.ClearAllMVMApi()`
  - `mvm.CallClearAllStateInstances()`
  - `trie_database.GetTrieDatabaseManager().ClearAllCaches()` (if exists)
- Call `InvalidateAllState()` in store-only sync path too (defensive)
- Call `InvalidateAllState()` in Phase 3 atomic transition

```
Impact: Prevents stale cache reads in any path
Risk: Low — cache invalidation is safe (just slower for next read)
```

### 3.7 Peer StateRoot Attestation Protocol — Priority: MEDIUM

**Goal**: Continuous fork detection during normal operation (not just at restart).

**Design**:
- Every N blocks (configurable, default=10), each node broadcasts:
  `{node_id, block_number, block_hash, state_root, timestamp}`
- Nodes compare incoming attestations with local state
- If 2f+1 nodes have different stateRoot at same block: log ALERT
- If local node diverges from 2f+1: auto-pause consensus and enter re-sync

**Changes**:
- New file: `consensus/metanode/src/consensus/state_attestation.rs`
- New file: `executor/state_attestation_handler.go`
- Protocol: Piggybacked on existing peer RPC (minimal overhead)

```
Impact: Detects forks within seconds instead of external monitoring
Risk: Medium — new protocol, needs careful testing
```

### 3.8 Remove Redundant State Mutation Paths — Priority: MEDIUM

**Goal**: `CommitBlockState` should be the ONLY path for updating chain state.

**Current redundant paths**:
1. `HandleSyncBlocksRequest` (store-only): directly calls `SetcurrentBlockHeader`, `UpdateLastBlockNumber`, `SaveLastBlock`
2. `handleSyncBlocksExecuteMode`: calls some via `CommitBlockState`, but also calls `InvalidateAllCaches` separately

**Changes**:
- Extend `CommitBlockState` with:
  - `WithInvalidateCaches()` option
  - `WithUpdateLastBlock(callback)` option  
- Remove all direct state mutation calls outside `CommitBlockState` and `createBlockFromResults`
- Audit all callers with `grep -rn "SetcurrentBlockHeader\|UpdateLastBlockNumber\|SaveLastBlock"` 

```
Impact: Single code path for state changes = easier to reason about correctness
Risk: Medium — refactoring with many callers
```

---

## 4. Execution Priority & Timeline

### Sprint 1 (Immediate — fixes current fork)
| Task | Priority | Est. Effort | Files |
|------|----------|-------------|-------|
| 3.1 Force execute-mode for Master | CRITICAL | 1h | `unix_socket_handler_epoch.go` |
| 3.5 Per-block stateRoot verify | HIGH | 2h | `unix_socket_handler_epoch.go` |
| 3.6 Cache invalidation hardening | HIGH | 2h | `chain_state.go`, `unix_socket_handler_epoch.go` |
| 3.3 Single-writer bp.lastBlock | CRITICAL | 3h | `block_processor_core.go`, `unix_socket_handler_epoch.go` |

### Sprint 2 (This week — prevents recurrence)
| Task | Priority | Est. Effort | Files |
|------|----------|-------------|-------|
| 3.2 State verification gate | CRITICAL | 8h | `startup.rs`, `catchup.rs`, `unix_socket_handler_epoch.go` |
| 3.4 Mandatory timestamp | HIGH | 2h | `block_processor_utils.go`, `executor.rs` |
| 3.8 Consolidate state mutation | MEDIUM | 4h | Multiple Go files |

### Sprint 3 (Next week — operational safety net)
| Task | Priority | Est. Effort | Files |
|------|----------|-------------|-------|
| 3.7 Peer attestation protocol | MEDIUM | 16h | New Rust + Go files |

---

## 5. Verification Plan

### 5.1 Unit Tests
- `TestStateRootVerificationDuringSyncExecute` — inject wrong BackupDb, verify sync halts
- `TestSingleWriterLastBlock` — concurrent SetLastBlock/GetLastBlock, verify no race
- `TestTimestampNeverZero` — verify panic if timestampSec == 0

### 5.2 Integration Tests  
- **Restart test**: Stop node, restore from snapshot, restart, verify no fork for 100 blocks
- **Slow catch-up test**: Start node 200 blocks behind, verify all blocks match after catch-up
- **Partition test**: Network partition during restart, verify fork detection triggers

### 5.3 Monitoring
- Extend existing `hash_mismatch_alert.log` monitoring
- Add Prometheus metrics: `state_root_mismatch_total`, `sync_verify_gate_pass/fail`
- Grafana alert: if any `stateRoot` mismatch in last 5 minutes

---

## 6. Risk Assessment

| Change | Risk | Mitigation |
|--------|------|------------|
| Force execute-mode | Node may fail if BackupDb unavailable | Fallback: fetch BackupDb from peers before execute |
| State verification gate | Adds ~2s to restart | Acceptable tradeoff vs fork |
| Single-writer lock | Deadlock if lock not released | Use `defer unlock()` pattern, add timeout |
| Mandatory timestamp | Breaks backward compat | Feature flag for rollout period |
| Peer attestation | Network overhead | Configurable interval, piggyback on existing RPC |

---

## 7. Long-term Architecture Notes

### Why forks keep recurring
The fundamental issue is **two independent state machines** (Rust consensus + Go execution) that must stay in sync but communicate via async RPC. Any race, stale cache, or ordering violation causes divergence.

### Ideal future state
1. **Single state machine**: Move state execution into Rust (or vice versa) to eliminate cross-process sync
2. **Deterministic replay log**: All blocks are deterministic functions of (parent_state, transactions, metadata). If replay ever diverges, the node has a bug — not a sync issue
3. **State commitment in consensus**: Include stateRoot in consensus protocol (like Ethereum). Blocks with wrong stateRoot are rejected by consensus, making forks impossible at the protocol level

These are longer-term architectural changes but would make the system fundamentally fork-resistant rather than relying on patches.
