# AGENTS.md — MTN-Consensus (Rust Consensus Engine)

## Project Identity

**Name**: `metanode` / `mtn-consensus`
**Language**: Rust (edition 2021), version `0.9.42`
**Crate type**: `staticlib` + `rlib` (statically linked into Go)
**Purpose**: DAG-based BFT consensus engine, forked from Sui's Mysticeti protocol.
**Role**: **Consensus Layer** — orders transactions, manages epochs, drives the Go Execution Layer via CGo FFI callbacks.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│            MetaNode Unified Process (Go + Rust static lib)       │
│                                                                  │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │              Embedded Rust Library (CGo FFI)              │   │
│  │                                                           │   │
│  │  ffi.rs ──► ConsensusNode ──► AuthorityNode (DAG-BFT)    │   │
│  │               │                    │                      │   │
│  │          EpochMonitor         Linearizer                  │   │
│  │          BlockCoordinator     CommitFinalizer             │   │
│  │          EpochTransitionMgr   CommitProcessor             │   │
│  │          RpcCircuitBreaker    CommitSyncer (SyncOnly)     │   │
│  │               │                    │                      │   │
│  │          executor_client/     ◄── FFI Callbacks ──►       │   │
│  └───────────────┼────────────────────┼──────────────────────┘   │
│                  │ CGo FFI calls       │ CGo callbacks            │
│  ┌───────────────▼────────────────────▼──────────────────────┐   │
│  │          Go Execution Layer (executor/ + processor/)       │   │
│  └───────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

### Key Design Principles
1. **Rust is Authority**: Rust decides transaction ordering and block finality. Go only executes.
2. **Deterministic Transitions**: All epoch boundaries and timestamps come from block headers, never `time::now()`.
3. **Advance-First Protocol (Pillar 47)**: During epoch transitions, Rust calls `AdvanceEpoch` on Go first, then fetches the new committee.
4. **Deferred Commit (Pillar 31.3)**: Non-blocking sub-DAG commitment — missing ancestors defer without blocking the core thread.
5. **Ancestor-Only Linearization (Pillar 31.6)**: Only causally reachable blocks from the leader are committed (bit-perfect determinism).

---

## Repository Structure

```
consensus/
├── AGENTS.md
├── CONTEXT.md
├── metanode/                           # Rust workspace root
│   ├── Cargo.toml                      # version 0.9.42, crate-type = [staticlib, rlib]
│   ├── build.rs                        # Protobuf codegen (prost-build)
│   ├── src/
│   │   ├── main.rs                     # CLI entry: `start` / `generate`
│   │   ├── lib.rs                      # Library root (exposes FFI)
│   │   ├── ffi.rs                      # ⭐ CGo FFI exports & commit callback registration
│   │   ├── config.rs                   # NodeConfig loading (TOML), key management
│   │   │
│   │   ├── consensus/                  # High-level consensus orchestration
│   │   │   ├── commit_processor/       # ⭐ Sends committed SubDAGs to Go (FFI callback)
│   │   │   ├── epoch_transition.rs     # EndOfEpoch system TX handling
│   │   │   ├── tx_recycler.rs          # Recycles uncommitted TXs after leader change
│   │   │   ├── clock_sync.rs           # NTP-based clock synchronization
│   │   │   ├── checkpoint.rs           # Consensus-level checkpointing
│   │   │   ├── commit_callbacks.rs     # Callback registration helpers
│   │   │   └── state_attestation.rs    # Block state attestation (BLS)
│   │   │
│   │   ├── node/                       # Node lifecycle & state machine (largest area)
│   │   │   ├── consensus_node.rs       # ⭐ ConsensusNode struct (~114KB, core orchestrator)
│   │   │   ├── node_methods.rs         # ConsensusNode method implementations
│   │   │   ├── startup.rs              # InitializedNode, bootstrapping
│   │   │   ├── epoch_monitor.rs        # ⭐ Polls Go for epoch changes (~32KB)
│   │   │   ├── epoch_transition_manager.rs  # ⭐ Full epoch transition state machine
│   │   │   ├── epoch_checkpoint.rs     # Crash-recovery checkpoint (atomic write-rename)
│   │   │   ├── epoch_store.rs          # Per-epoch persistent store
│   │   │   ├── block_coordinator.rs    # ⭐ Dual-Stream deduplication (~29KB)
│   │   │   ├── block_delivery.rs       # Block delivery to Go
│   │   │   ├── committee.rs            # Committee construction from Go validator data
│   │   │   ├── committee_source.rs     # ⭐ Peer discovery for committee (~29KB)
│   │   │   ├── coordinator.rs          # Node coordinator
│   │   │   ├── dual_stream.rs          # Manages Consensus + Sync data streams
│   │   │   ├── sync.rs                 # SyncOnly mode logic
│   │   │   ├── sync_controller.rs      # SyncOnly controller
│   │   │   ├── sync_metrics.rs         # Sync-specific metrics
│   │   │   ├── health_check.rs         # Node health monitoring
│   │   │   ├── peer_go_client.rs       # Go peer RPC client
│   │   │   ├── peer_health.rs          # Peer health tracking
│   │   │   ├── queue.rs                # Block/TX queue management
│   │   │   ├── recovery.rs             # Crash recovery logic
│   │   │   ├── rpc_circuit_breaker.rs  # ⭐ Circuit breaker for Go RPC calls (~15KB)
│   │   │   ├── notification_listener.rs # Committee change notification listener
│   │   │   ├── notification_server.rs  # Notification server
│   │   │   ├── tx_submitter.rs         # TX submission to consensus
│   │   │   ├── catchup.rs              # CatchupManager stub
│   │   │   │
│   │   │   ├── executor_client/        # ⭐ FFI client to Go bridge
│   │   │   │   ├── mod.rs              # ExecutorClient main (~29KB)
│   │   │   │   ├── rpc_queries.rs      # Standard RPC calls to Go (~21KB)
│   │   │   │   ├── rpc_queries_epoch.rs # Epoch RPC calls to Go (~14KB)
│   │   │   │   ├── block_sending.rs    # ⭐ SendCommittedSubDag to Go (~46KB)
│   │   │   │   ├── block_store.rs      # Block store abstraction
│   │   │   │   ├── block_sync.rs       # Block sync helpers
│   │   │   │   ├── connection_pool.rs  # Connection pool for Go RPC
│   │   │   │   ├── persistence.rs      # Persistent block storage (~16KB)
│   │   │   │   ├── socket_stream.rs    # Socket stream management
│   │   │   │   ├── traits.rs           # ExecutorClient trait definitions
│   │   │   │   └── transition_handoff.rs # Epoch transition handoff logic
│   │   │   │
│   │   │   ├── transition/             # Epoch transition state machine
│   │   │   │   ├── epoch_transition.rs # Core transition logic (~25KB)
│   │   │   │   ├── mode_transition.rs  # Validator ↔ SyncOnly mode changes (~20KB)
│   │   │   │   ├── consensus_setup.rs  # New ConsensusAuthority setup
│   │   │   │   ├── demotion.rs         # Validator demotion handling
│   │   │   │   ├── tx_recovery.rs      # TX recovery across epoch boundary
│   │   │   │   └── verification.rs     # Committee verification
│   │   │   │
│   │   │   └── rust_sync_node/         # Pure-Rust sync node implementation
│   │   │       ├── sync_loop.rs        # Main sync loop (~39KB)
│   │   │       ├── fetch.rs            # Block fetching from peers (~33KB)
│   │   │       ├── block_queue.rs      # Sync block queue (~16KB)
│   │   │       ├── epoch_recovery.rs   # Epoch recovery for sync node (~16KB)
│   │   │       └── start.rs            # Sync node startup
│   │   │
│   │   ├── network/
│   │   │   └── tx_receiver.rs          # Receives TX batches from Go via FFI
│   │   │
│   │   └── types/                      # Shared type definitions
│   │
│   ├── meta-consensus/                 # Core DAG-BFT consensus library
│   │   ├── core/src/                   # ⭐⭐ The consensus engine (~47 files)
│   │   │   ├── authority_node.rs       # AuthorityNode — consensus participant (~41KB)
│   │   │   ├── authority_service.rs    # RPC service for serving blocks (~70KB)
│   │   │   ├── synchronizer.rs         # Block sync with peers (~87KB)
│   │   │   ├── commit_syncer.rs        # Syncs commits (SyncOnly mode) (~103KB)
│   │   │   ├── commit_finalizer.rs     # Finalizes commit sub-DAGs (~72KB)
│   │   │   ├── commit_observer.rs      # Monitors commits, reputation (~39KB)
│   │   │   ├── linearizer.rs           # ⭐ DAG → linear sequence (~46KB)
│   │   │   ├── block_manager.rs        # DAG block storage (~52KB)
│   │   │   ├── block_verifier.rs       # Validates incoming blocks (~32KB)
│   │   │   ├── transaction.rs          # TX handling (~42KB)
│   │   │   ├── transaction_certifier.rs # TX certification (~39KB)
│   │   │   ├── leader_schedule.rs      # Leader election per round (~44KB)
│   │   │   ├── leader_scoring.rs       # Leader reputation scoring
│   │   │   ├── leader_timeout.rs       # Leader timeout handling
│   │   │   ├── metrics.rs              # Prometheus metrics (~50KB)
│   │   │   ├── core.rs / core_thread.rs # Core consensus loop
│   │   │   ├── round_tracker.rs        # Round state tracking (~19KB)
│   │   │   ├── round_prober.rs         # Round health probing (~18KB)
│   │   │   ├── threshold_clock.rs      # BFT threshold clock
│   │   │   ├── system_transaction.rs   # EndOfEpoch system TX
│   │   │   ├── system_transaction_provider.rs # System TX injection
│   │   │   ├── ancestor.rs             # Ancestor traversal for linearization
│   │   │   ├── universal_committer.rs  # Universal committer
│   │   │   ├── base_committer.rs       # Base committer logic
│   │   │   ├── reconfiguration.rs      # Epoch reconfiguration
│   │   │   ├── subscriber.rs           # Commit subscriber
│   │   │   └── dag_state/, storage/, network/, core/  # Sub-modules
│   │   ├── types/                      # Consensus types (Block, Commit, Round, etc.)
│   │   └── config/                     # Consensus parameters
│   │
│   ├── proto/                          # Protobuf definitions (Go ↔ Rust IPC)
│   ├── config/                         # TOML config files per node (node-0.toml … node-4.toml)
│   ├── scripts/                        # ⭐ Many scripts (27 files)
│   │   ├── mtn-orchestrator.sh         # Main cluster orchestration (~35KB)
│   │   ├── e2e_test_suite.sh           # Full E2E test suite (~46KB)
│   │   ├── test_snapshot_stability_loop.sh # Snapshot stability tests (~23KB)
│   │   ├── test_validator_restart_rejoin.sh # Validator restart testing (~14KB)
│   │   ├── test_restart_loop.sh        # Restart/recovery testing
│   │   ├── test_system.sh              # Full system integration test
│   │   ├── test_snapshot_recovery_e2e.sh # Snapshot recovery E2E
│   │   ├── test_epoch_stress.sh        # Epoch stress testing
│   │   ├── generate_genesis_from_rust_keys.sh # Genesis key generation
│   │   ├── analyze_node_stuck.sh       # Stuck node diagnosis
│   │   ├── trace_transaction.sh        # TX tracing utility
│   │   └── CLUSTER_OPERATIONS.md       # Operations runbook
│   └── deploy/                         # Systemd service files
│
└── crates/                             # Shared utility libraries (workspace siblings)
    ├── shared-crypto/                  # BLS12-381 cryptographic primitives
    ├── typed-store/                    # RocksDB abstraction
    ├── mysten-network/                 # Peer networking (anemo)
    ├── mysten-metrics/                 # Prometheus metrics
    └── meta-protocol-config/           # Protocol version configuration
```

---

## Key Concepts

### Node Modes
| Mode | Description |
|------|-------------|
| **Validator** | Active consensus participant — proposes blocks, votes, commits, serves peers. |
| **SyncOnly** | Passive — fetches committed blocks from peers via `CommitSyncer` or `rust_sync_node`. Awaits committee promotion. |

### Epoch Lifecycle
```
EpochMonitor polls Go for epoch changes (via rpc_queries_epoch.rs)
  → EndOfEpoch system TX committed by consensus
  → AdvanceEpoch RPC → Go advances epoch (Pillar 47)
  → GetEpochBoundaryData RPC → Rust fetches new committee
  → epoch_transition_manager.rs: stops old AuthorityNode
  → transition/consensus_setup.rs: starts new AuthorityNode
  → EpochCheckpoint written to disk (crash-recovery)
```

### Communication Protocol (Rust ↔ Go via FFI)

**Rust calls Go** (via `executor_client/`):

| Call | File | Purpose |
|------|------|---------|
| `GetLastBlockNumber` | `rpc_queries.rs` | Current chain height |
| `GetEpochBoundaryData` | `rpc_queries_epoch.rs` | Epoch committee + timestamp |
| `GetValidatorsAtBlock` | `rpc_queries_epoch.rs` | Validator set at height |
| `AdvanceEpoch` | `rpc_queries_epoch.rs` | Notify Go of epoch change |
| `SetConsensusStartBlock` | `rpc_queries_epoch.rs` | Mode barrier: validator start |
| `SetSyncStartBlock` | `rpc_queries_epoch.rs` | Mode barrier: sync start |
| `WaitForSyncToBlock` | `rpc_queries_epoch.rs` | Wait for Go to sync to N |
| `GetBlocksRange` | `rpc_queries.rs` | Fetch block range for sync |
| `SyncBlocks` | `rpc_queries.rs` | Push bulk blocks to Go |
| `GetLastHandledCommitIndex` | `rpc_queries_epoch.rs` | GEI recovery on restart |
| `SendCommittedSubDag` | `block_sending.rs` | ⭐ Send ordered block to Go |

**Go calls Rust** (via `ffi.rs`):
- `RegisterCommitCallback` — Go registers callback for receiving committed blocks
- `ForwardTransactionBatch` — Go pushes TX batch into Rust consensus

### Block Coordinator (Dual-Stream)
`block_coordinator.rs` deduplicates blocks from two concurrent sources:
1. **Consensus Stream** — Blocks from local DAG consensus (strict sequential).
2. **Sync Stream** — Blocks fetched from peers (gap-filling).

Uses `next_expected_index` + `BTreeMap` buffer for ordering.

### RPC Circuit Breaker
`rpc_circuit_breaker.rs` — Protects Go from being overwhelmed during epoch transitions. Opens automatically on repeated failures, allows recovery retries.

---

## Build & Run

### Build
```bash
# Compiled as static library, linked into Go during full build
cd metanode/consensus/metanode
cargo +nightly build --release
```

### Full Cluster (5 validators)
```bash
cd scripts
bash mtn-orchestrator.sh start
```

### Generate configs for N nodes
```bash
./target/release/metanode generate --nodes 5 --output config/
```

### Standalone (testing only)
```bash
./target/release/metanode start --config config/node-0.toml
```

---

## Storage
- **Consensus DB**: RocksDB per-epoch at `<storage_path>/epochs/epoch_{N}/consensus_db`
- **Epoch Checkpoint**: `<storage_path>/epoch_transition.checkpoint` (atomic write-then-rename)
- **Block Persistence**: `executor_client/persistence.rs` — blocks persisted locally for recovery

---

## Key Dependencies
| Crate | Version | Purpose |
|-------|---------|---------|
| `tokio` | 1.47.1 | Async runtime |
| `prost` / `prost-build` | 0.14.1 | Protobuf serialization |
| `fastcrypto` (Mysten) | git pin | BLS12-381 signatures |
| `typed-store` (local) | — | RocksDB abstraction |
| `mysten-network` (local) | — | anemo P2P networking |
| `anyhow` | 1.0.71 | Error handling |
| `socket2` | 0.5 | TCP keepalive |
| `reqwest` | 0.11 | HTTP client (monitoring) |
| `rayon` | 1.11.0 | Parallel processing |

---

## Critical Invariants (Fork Safety)
1. **No `time::now()` for consensus timestamps** — All timestamps from block headers.
2. **Canonical validator sorting** — Both Go and Rust sort by `AuthorityKey` bytes (BLS pubkey). Must be byte-identical.
3. **Deterministic sub-DAG linearization** — Ancestor-only traversal, no round-wide quorum.
4. **Boundary block determinism** — Epoch boundaries are explicit, consensus-certified values.
5. **Blocking send to Go** — `block_sending.rs` never drops blocks.
6. **Advance-First invariant** — `AdvanceEpoch` MUST be called before `GetEpochBoundaryData`.

---

## Environment Variables
```bash
RUST_LOG=metanode=info,consensus_core=debug   # Logging level
```

---

## Testing
```bash
# Unit tests
cargo test

# Integration: restart recovery
bash scripts/test_restart_loop.sh

# Full system integration
bash scripts/test_system.sh

# E2E test suite (comprehensive)
bash scripts/e2e_test_suite.sh

# Epoch stress test
bash scripts/test_epoch_stress.sh

# Snapshot recovery E2E
bash scripts/test_snapshot_recovery_e2e.sh

# Validator restart & rejoin
bash scripts/test_validator_restart_rejoin.sh
```

---

## Common Patterns

### Error Handling
- Production code: `anyhow::Result` + `.context()` — no `.unwrap()` in hot paths.
- State divergence: `panic!()` deliberately (fail-fast safety).

### Epoch Transition Pattern
```
EpochMonitor detects Go epoch > current
  → AdvanceEpoch RPC to Go (Pillar 47)
  → GetEpochBoundaryData from Go → new committee
  → epoch_transition_manager: stop old AuthorityNode
  → transition/consensus_setup: start new AuthorityNode
  → EpochCheckpoint written to disk
```

### Transaction Flow
```
Go TX pool → FFI call (ForwardTransactionBatch) → tx_receiver.rs
  → pending queue → DAG Block proposal
  → Consensus rounds → Leader elected → linearizer.rs
  → CommittedSubDag → commit_finalizer.rs → commit_processor/
  → block_sending.rs → FFI Callback → Go BlockProcessor
```

### Validator Lifecycle (Committee Changes)
```
[New validator registers on-chain via Go smart contract]
  → EndOfEpoch TX committed
  → Rust fetches updated committee from Go
  → New validator included in next ConsensusAuthority
```
