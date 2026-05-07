# AGENTS.md — MTN-Simple-2025 (Go Execution Engine)

## Project Identity

**Name**: `mtn-simple-2025` (MetaNode Simple Chain)
**Language**: Go 1.23.5
**Module**: `github.com/meta-node-blockchain/meta-node`
**Purpose**: Full blockchain **Execution Layer** — handles state management, smart contract execution, transaction processing, block storage, and P2P synchronization.
**Role in System**: Receives **ordered** transaction batches from the Rust Consensus Engine (`mtn-consensus`) via CGo FFI callbacks and maintains the full world state (account balances, smart contracts, receipts, block history).

---

## System Architecture

The entire MetaNode system runs as a **single unified Go process** per validator node. The Rust consensus library is statically linked into this binary via CGo.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                  MetaNode Unified Process (simple_chain)                │
│                                                                         │
│  CLIENT FACING                  CONSENSUS LAYER (Rust via CGo FFI)      │
│  ┌──────────────┐               ┌────────────────────────────────────┐  │
│  │ RPC / WS     │               │   executor/ffi_bridge.go           │  │
│  │ (port 4201)  │               │   (RegisterCommitCallback, etc.)   │  │
│  └──────┬───────┘               └────────────┬───────────────────────┘  │
│         │                                    │ FFI Callback              │
│  ┌──────▼────────────────────────────────────▼───────────────────────┐  │
│  │                    BlockProcessor (cmd/simple_chain/processor/)    │  │
│  │  TxPool → VirtualExec → ForwardToRust → ReceiveOrdered → Execute  │  │
│  └──────┬─────────────────────────────────────┬─────────────────────┘  │
│         │                                     │                         │
│  ┌──────▼───────┐  ┌──────────────┐  ┌────────▼──────────────────────┐  │
│  │ AccountState │  │SmartContract │  │  TransactionStateDB (Receipts)│  │
│  │ DB (MPT)     │  │ DB (Pebble)  │  │                               │  │
│  └──────────────┘  └──────────────┘  └───────────────────────────────┘  │
│                                                                         │
│  P2P LAYER (SyncOnly nodes)                                             │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │   pkg/network/ + pkg/sync/  (QUIC via quic-go)                  │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

### Node Modes
| Mode | Description |
|------|-------------|
| **Validator** | Participates in consensus. Receives TXs from clients, runs virtual execution, forwards to Rust, receives committed blocks via FFI callback, executes and persists. |
| **SyncOnly** | Does NOT participate in consensus. Fetches committed blocks from Validator peers via P2P (anemo/QUIC network), executes them locally to stay in sync. |

---

## Repository Structure

```
execution/                              # Root of the Go Execution Engine
├── cmd/
│   ├── simple_chain/                   # ⭐ MAIN APPLICATION ENTRY POINT
│   │   ├── main.go                     # CLI entry: `start`, `keygen`, `version` commands
│   │   ├── app.go                      # App lifecycle: Start(), StopWait(), goroutine supervisor
│   │   ├── app_blockchain.go           # Blockchain & consensus initialization
│   │   ├── app_network.go              # P2P network setup (QUIC/anemo)
│   │   ├── app_storage.go              # Storage (LevelDB/PebbleDB) initialization
│   │   ├── backend.go                  # Backend API wrapper (eth_* calls bridge)
│   │   ├── bls_key_store.go            # BLS key loading for validator identity
│   │   │
│   │   ├── processor/                  # ⭐⭐ CORE: Block processing pipeline
│   │   │   ├── block_processor_core.go         # BlockProcessor struct, init, Start()
│   │   │   ├── block_processor_commit.go       # Block commit + async persistence pipeline
│   │   │   ├── block_processor_processing.go   # GenerateBlock(), createBlockFromResults()
│   │   │   ├── block_processor_broadcast.go    # Broadcast finalized blocks to SyncOnly peers
│   │   │   ├── block_processor_sync.go         # SyncOnly node: fetch+apply blocks from peers
│   │   │   ├── block_processor_network.go      # Startup catch-up synchronization
│   │   │   ├── block_processor_batch.go        # AccountBatch packing/unpacking (legacy support)
│   │   │   ├── block_processor_utils.go        # Timestamp enforcement: panic on zero timestamp
│   │   │   ├── block_processor_state.go        # GEI (Global Execution Index) atomic tracking
│   │   │   ├── block_processor_epoch.go        # Epoch state management helpers
│   │   │   ├── block_processor_monitoring.go   # Health metrics & alert logging
│   │   │   ├── block_processor_indexing.go     # Block indexing for explorer
│   │   │   ├── block_processor_logs.go         # Structured log markers
│   │   │   ├── block_processor_receipt.go      # Receipt generation & storage
│   │   │   ├── block_processor_db_sync.go      # Cross-DB sync helpers
│   │   │   ├── block_processor_attestation.go  # Block attestation (BLS signatures)
│   │   │   ├── block_buffers.go                # Block buffer management
│   │   │   ├── state_processor.go              # State transition logic (apply TX results)
│   │   │   ├── transaction_processor.go        # TX validation, pool routing, fee checks
│   │   │   ├── transaction_processor_helpers.go # TX processing utilities
│   │   │   ├── transaction_processor_offchain.go # Off-chain TX processing
│   │   │   ├── transaction_pending.go          # Pending TX management
│   │   │   ├── transaction_virtual_processor.go # Virtual execution (simulation for eth_call)
│   │   │   ├── tx_batch_forwarder_core.go      # ⭐ Batch TXs → forward to Rust via FFI
│   │   │   ├── tx_validator_pool_core.go       # Validator-side TX pool management
│   │   │   ├── tx_virtual_executor_core.go     # Virtual executor core logic
│   │   │   ├── tx_interfaces.go                # TX processor interface definitions
│   │   │   ├── connection_processor.go         # Client TCP/WS connection handler
│   │   │   ├── gei_authority.go                # GEI authority validation
│   │   │   ├── vote_recovery.go                # Vote/attestation recovery
│   │   │   ├── cross_chain_sig_accumulator.go  # Cross-chain signature accumulation
│   │   │   ├── subscribe_processor.go          # Event subscription (eth_subscribe)
│   │   │   ├── processors.go                   # Processor registry/coordinator
│   │   │   ├── consensus_context.go            # Consensus context holder
│   │   │   ├── cache_manager.go                # In-memory cache management
│   │   │   ├── peer_discovery_socket.go        # Peer discovery via socket
│   │   │   ├── pipeline_stats.go               # Pipeline throughput statistics
│   │   │   ├── receipt_tracker.go              # Receipt tracking
│   │   │   ├── revertparser.go                 # ABI revert reason parser
│   │   │   ├── constants.go                    # System-wide constants
│   │   │   ├── pipeline/                       # Pipeline stage implementations
│   │   │   ├── syncorder/                      # Block sync ordering logic
│   │   │   └── rpcquery/                       # RPC query helpers
│   │   │
│   │   ├── rpc_block.go                # eth_getBlockByNumber, eth_getBlockByHash
│   │   ├── rpc_transaction.go          # eth_sendRawTransaction, eth_getTransactionByHash
│   │   ├── rpc_state.go                # eth_getBalance, eth_call, eth_getCode
│   │   ├── rpc_subscription.go         # eth_subscribe (WebSocket)
│   │   ├── mtn_api.go                  # MetaNode-specific RPCs (mtn_* namespace)
│   │   ├── debug_api.go                # Debug/trace APIs
│   │   ├── admin_api.go                # Admin APIs
│   │   ├── tx_async_queue.go           # Async TX queue (non-blocking submission)
│   │   ├── eth_broadcaster.go          # Ethereum-compatible event broadcasting
│   │   ├── eth_tx_converter.go         # TX format conversion (Ethereum ↔ internal)
│   │   ├── transaction_args.go         # TX argument parsing
│   │   ├── tool_register.go            # Validator registration tool (on-chain)
│   │   ├── metrics.go                  # Prometheus metrics for node
│   │   └── config-master-node{0..4}.json  # Per-node configuration files
│   │
│   ├── exec_node/                      # Standalone execution node (no consensus)
│   ├── consensus/                      # Legacy consensus (pre-Mysticeti, deprecated)
│   ├── mining/                         # Mining server
│   ├── keygen/                         # BLS key generation tool
│   └── tool/                           # CLI utility tools
│
├── executor/                           # ⭐ Go ↔ Rust FFI Bridge Layer
│   ├── ffi_bridge.go                   # ⭐ MAIN: RegisterCommitCallback, CGo FFI hooks
│   ├── listener.go                     # Commit listener: receives ordered blocks from Rust
│   ├── unix_socket.go                  # RequestHandler wrapper (FFI + legacy socket)
│   ├── unix_socket_handler.go          # Basic RPC handlers routed to Rust
│   ├── unix_socket_handler_epoch.go    # ⭐ ALL epoch-related RPC handlers (large file, ~94KB)
│   ├── unix_socket_handler_router.go   # Request dispatcher (protobuf switch/case)
│   ├── unix_socket_protocol.go         # Protobuf length-prefixed framing protocol
│   ├── socket_abstraction.go           # Legacy auto-detection (now replaced by FFI)
│   ├── interfaces.go                   # Executor interface definitions
│   ├── committee_notifier.go           # Committee change notification to Rust
│   ├── snapshot_manager.go             # LVM snapshot management (large, ~41KB)
│   ├── snapshot_server.go              # Snapshot serving for bootstrapping new nodes
│   ├── snapshot_init.go                # Snapshot-based state initialization
│   └── snapshot_verify.go             # Snapshot integrity verification
│
├── pkg/                                # ⭐ Shared library packages (61 packages)
│   ├── account_state_db/               # AccountStateDB — Merkle Patricia Trie (MPT)
│   ├── state_db/                       # Generic state DB abstraction
│   ├── state/                          # State management helpers
│   ├── state_changelog/                # State change log for rollback support
│   ├── smart_contract/                 # Smart contract types & execution
│   ├── smart_contract_db/              # Contract storage (PebbleDB backend)
│   ├── block/                          # Block structure + RLP serialization
│   ├── blockchain/
│   │   └── tx_processor/              # ⭐ Core TX execution engine
│   │       ├── tx_processor.go         # processGroupsConcurrently() — parallel exec
│   │       └── vm_processor.go         # VM/Native contract dispatching
│   ├── storage/                        # Block storage (LevelDB/PebbleDB)
│   ├── transaction/                    # Transaction types, codec, signing
│   ├── transaction_pool/               # In-memory TX pool
│   ├── transaction_grouper/            # TX dependency grouper
│   ├── transaction_state_db/           # TX receipts & logs storage
│   ├── grouptxns/                      # Union-Find TX grouping for parallel execution
│   ├── trie/                           # MPT (Merkle Patricia Trie) implementation
│   ├── trie_database/                  # MPT database backend
│   ├── receipt/                        # Transaction receipt structures
│   ├── bls/                            # BLS12-381 signature primitives
│   ├── config/                         # SimpleChainConfig struct (JSON loader)
│   ├── proto/                          # Protobuf definitions (must match Rust proto/)
│   ├── mvm/                            # C++ MVM (Meta Virtual Machine) bridge
│   │   ├── c_mvm/                      # C++ source code for MVM
│   │   └── linker/                     # CMake bridge (Go ↔ C++)
│   ├── network/                        # P2P networking abstractions
│   ├── quic_network/                   # QUIC transport implementation
│   ├── sync/                           # Block synchronization manager
│   ├── snapshot/                       # Snapshot data structures
│   ├── metrics/                        # Prometheus metrics registry
│   ├── logger/                         # Structured logger
│   ├── loggerfile/                     # File-based logger
│   ├── explorer/                       # Block explorer data (tx indexing)
│   ├── goxapian/                       # Xapian full-text search (C++ FFI)
│   ├── nomt_ffi/                       # Jellyfish Merkle Trie FFI (Rust)
│   ├── cross_chain_handler/            # Cross-chain event handler
│   ├── models/                         # Shared data model definitions
│   ├── common/                         # Common utilities
│   ├── utils/                          # General utilities
│   ├── filters/                        # Event filter management
│   ├── rpc_client/                     # Internal RPC client
│   ├── poh/                            # Proof of History helpers
│   ├── pruning/                        # State/block pruning
│   ├── shard_storage/                  # Sharded storage support
│   ├── shared_memory/                  # Shared memory IPC
│   ├── proxy_tx/                       # Proxy transaction handling
│   ├── tracing/                        # OpenTelemetry tracing
│   ├── stats/                          # Runtime statistics
│   └── ...                             # Additional utility packages
│
├── contracts/                          # Smart contract source code (Solidity)
├── web3/                               # Web3 client library (JS/Go)
├── types/                              # Shared type definitions
├── scripts/                            # Utility shell scripts
├── go.mod / go.sum                     # Go module (github.com/meta-node-blockchain/meta-node)
├── build.sh                            # Build C++ MVM + Go binary
├── build_app.sh                        # Full application build (all binaries)
├── run.sh                              # Start the full cluster
├── kill_nodes.sh                       # Stop all running nodes
├── auto_test.sh                        # Automated stress test pipeline
└── SYSTEM_ARCHITECTURE_IMPROVEMENT.md # Detailed architecture improvement notes
```

---

## Key Concepts

### 1. Block Processing Pipeline

The **heart of the system** is `BlockProcessor` in `cmd/simple_chain/processor/`. Full lifecycle:

```
[Client] → RPC/WS (port 4201)
  → connection_processor.go: receives raw TX
  → transaction_processor.go: validates nonce, fee, signature
  → tx_validator_pool_core.go: adds to in-memory TX pool
  → tx_batch_forwarder_core.go: batches TXs → FFI call → Rust Consensus
         ↓
[Rust mtn-consensus]: DAG ordering → CommittedSubDag
         ↓  (FFI Callback)
  → executor/listener.go: receives ordered block (blocking channel)
  → block_processor_processing.go: creates block from ordered TXs
  → pkg/blockchain/tx_processor/: executes TXs in parallel groups
  → state_processor.go: applies state changes to AccountStateDB
  → block_processor_commit.go: async persistence pipeline
  → block_processor_broadcast.go: P2P broadcast to SyncOnly nodes
```

### 2. FFI Bridge (Go ↔ Rust)

All communication between Go and the embedded Rust consensus engine uses CGo FFI (no network round trip).

**File**: `executor/ffi_bridge.go` — Registers Go callbacks that Rust calls directly.

**Request Handlers** (Go serves Rust requests via `executor/unix_socket_handler_router.go`):

| Request Type | Handler File | Purpose |
|---|---|---|
| `GetLastBlockNumberRequest` | `unix_socket_handler.go` | Current chain height |
| `GetEpochBoundaryDataRequest` | `unix_socket_handler_epoch.go` | Epoch boundary (committee + timestamp) |
| `GetValidatorsAtBlockRequest` | `unix_socket_handler_epoch.go` | Validator set at block height |
| `GetActiveValidatorsRequest` | `unix_socket_handler_epoch.go` | Active validators for epoch transition |
| `GetCurrentEpochRequest` | `unix_socket_handler_epoch.go` | Current epoch number |
| `GetEpochStartTimestampRequest` | `unix_socket_handler_epoch.go` | Epoch start timestamp |
| `AdvanceEpochRequest` | `unix_socket_handler_epoch.go` | Go advances its epoch state |
| `SetConsensusStartBlockRequest` | `unix_socket_handler_epoch.go` | Mode: Validator starts at block N |
| `SetSyncStartBlockRequest` | `unix_socket_handler_epoch.go` | Mode: SyncOnly starts at block N |
| `WaitForSyncToBlockRequest` | `unix_socket_handler_epoch.go` | Wait until Go syncs to block N |
| `GetBlocksRangeRequest` | `unix_socket_handler.go` | Fetch block range (for peer sync) |
| `SyncBlocksRequest` | `unix_socket_handler.go` | Push block batch to Go (SyncOnly) |
| `GetLastHandledCommitIndexRequest` | `unix_socket_handler_epoch.go` | GEI recovery after restart |
| `ForceCommitRequest` | `unix_socket_handler.go` | Force-commit a block |

**Commit Callback** (Rust → Go, via `executor/listener.go`):
- Rust calls `SendCommittedSubDag` FFI callback
- Go's `listener.go` receives the committed block on a **blocking channel** — **NEVER drops blocks**

### 3. Epoch Management

Epoch transitions are coordinated between Rust and Go:

```
[Rust EpochMonitor detects EndOfEpoch system TX]
  → AdvanceEpochRequest → Go advances epoch state
  → GetEpochBoundaryDataRequest → Go returns new committee
  → Rust restarts ConsensusAuthority with new committee
```

Key invariant: **Rust calls `AdvanceEpoch` BEFORE fetching the new committee** (Pillar 47 — Advance-First Protocol).

Go-side epoch state is managed in `unix_socket_handler_epoch.go` (the largest single file, ~94KB).

### 4. State Databases

| Database | Package | Purpose | Backend |
|---|---|---|---|
| `AccountStateDB` | `pkg/account_state_db/` | Account balances, nonces, code | MPT → LevelDB |
| `SmartContractDB` | `pkg/smart_contract_db/` | Contract storage slots | PebbleDB |
| `TransactionStateDB` | `pkg/transaction_state_db/` | TX receipts & event logs | LevelDB/PebbleDB |
| Block Storage | `pkg/storage/` | Block headers & bodies | LevelDB |

### 5. Parallel Transaction Execution

Transactions within a block are grouped using **Union-Find** (`pkg/grouptxns/`):
- TXs touching the same address → **same group** → executed sequentially
- Independent groups → **executed in parallel** across worker goroutines
- Produces **deterministic state roots** regardless of parallelism order

### 6. GEI (Global Execution Index)

The GEI is the canonical block height counter assigned by Rust consensus:

```
GEI = epoch_base_index + local_commit_index
```

- Tracked atomically in `block_processor_state.go`
- Used as the single source of truth for block ordering
- On restart: Go reports last GEI to Rust via `GetLastHandledCommitIndexRequest`

### 7. C++ MVM (Meta Virtual Machine)

A custom C++ VM for native contract execution, bridged via CGo:
- **Source**: `pkg/mvm/c_mvm/`
- **Bridge**: `pkg/mvm/linker/` (CMake)
- **Thread safety**: `db_mutex` protects Xapian database access
- **Memory**: All CGo allocations use `defer C.free()` to prevent leaks

### 8. Snapshot System

For fast bootstrapping of new nodes:
- `executor/snapshot_manager.go` — LVM-based snapshot creation
- `executor/snapshot_server.go` — Serves snapshots to joining nodes
- `executor/snapshot_init.go` — Initializes state from a snapshot

---

## Build

### Prerequisites
- Go 1.23+
- C++ compiler (`g++` / `clang++`) + CMake
- LevelDB + PebbleDB system libraries

### Build Commands
```bash
# Build C++ MVM linker + Go binary (recommended)
bash build.sh linux

# Build everything (all binaries: simple_chain, tps_blast, check_account)
bash build_app.sh linux

# Build Go only (if C++ already built)
cd cmd/simple_chain && go build -o simple_chain .
```

### Build Outputs
| Binary | Purpose |
|--------|---------|
| `simple_chain` | Main validator/synconly node |
| `tps_blast` | TPS benchmarking tool |
| `check_account` | Account state verification |

---

## Run

### Single Node
```bash
cd cmd/simple_chain
./simple_chain --config config-master-node0.json
```

### Full Cluster (5 Validators)
```bash
bash run.sh
```

### Node Configuration (JSON)
```json
{
  "chain_id": 991,
  "connection_address": "0.0.0.0:4201",
  "data_dir": "data_node0",
  "is_validator": true,
  "service_type": "MASTER",
  "peer_rpc_port": 19200,
  "log_level": "info"
}
```

---

## Key Dependencies

| Package | Version | Purpose |
|---------|---------|---------|
| `go-ethereum` | v1.14.12 | EVM, crypto, RLP encoding |
| `pebble` (CockroachDB) | v1.1.5 | High-performance storage |
| `goleveldb` | v1.0.1 | Legacy block storage |
| `protobuf` | v1.36.11 | Go ↔ Rust IPC encoding |
| `quic-go` | v0.50.0 | QUIC network transport (P2P) |
| `blst` | v0.3.13 | BLS12-381 signatures |
| `go-ethereum/verkle` | — | Verkle tree support |
| `badger` v4 | v4.5.1 | Alternative key-value store |
| `prometheus/client_golang` | v1.21.0 | Metrics export |
| `opentelemetry` | v1.33.0 | Distributed tracing |

---

## Critical Invariants (Fork Safety)

1. **No `time.Now()` for block timestamps** — Timestamps come exclusively from Rust consensus. `block_processor_utils.go` **panics** on zero timestamp.
2. **Canonical validator sorting** — Sort by `AuthorityKey` bytes (BLS public key). **Must be byte-identical to Rust sorting**.
3. **Blocking commit listener** — `executor/listener.go` channel **MUST NEVER drop blocks**.
4. **Deferred `C.free()`** — All CGo memory allocations must use `defer C.free()`.
5. **GEI monotonicity** — GEI never goes backward. Any divergence = fork.
6. **Deterministic parallel execution** — TX grouping ensures state roots are identical across all nodes.

---

## Transaction Types

| Type | Description |
|------|-------------|
| Transfer | Native MTN token transfer |
| Smart Contract Deploy | Deploy EVM/MVM contract |
| Smart Contract Call | Execute contract function |
| Stake | Validator staking operation |
| Unstake | Validator unstaking operation |
| Register Validator | On-chain validator registration |
| System TX (EndOfEpoch) | Epoch boundary marker (injected by Rust consensus) |
| Cross-chain TX | Cross-chain bridge event execution |

---

## Environment & Logging

```bash
# Logging level
LOG_LEVEL=info   # or debug, warn, error
```

### Key Log Markers

| Marker | Source | Meaning |
|--------|--------|---------|
| `🔍 [BLOCK-HASH-DEBUG]` | `block_processor_logs.go` | Block hash ingredients |
| `🔄 [EPOCH BOUNDARY DETECTED]` | `unix_socket_handler_epoch.go` | Epoch transition starting |
| `📊 [EPOCH BOUNDARY] === COMMITTEE DATA ===` | `unix_socket_handler_epoch.go` | New committee info |
| `⚡ [TPS]` | `block_processor_monitoring.go` | Throughput measurement |
| `[Go Server] Received AdvanceEpochRequest` | `unix_socket_handler_router.go` | Epoch advance FFI call |
| `[Go Server] Received SetConsensusStartBlockRequest` | `unix_socket_handler_router.go` | Mode transition barrier |

---

## Testing

```bash
# Unit tests (all packages)
go test ./...

# Specific processor tests
go test ./cmd/simple_chain/processor/ -v -run TestBlockProcessor

# TPS benchmark
go test ./cmd/simple_chain/processor/ -v -run TestTpsBenchmark -bench .

# Integration tests (Go ↔ Rust FFI)
go test ./executor/ -v -run TestGoRustIntegration

# Epoch transition integration
go test ./executor/ -v -run TestEpochTransitionIntegration

# Fork invariant tests
go test ./executor/ -v -run TestNoForkInvariant

# Automated stress test pipeline
bash auto_test.sh
```

---

## Common Patterns

### Async Persistence Pipeline
Block commits are non-blocking to avoid stalling the consensus callback thread:
```
CommitCallback (Rust FFI)
  → listener.go channel (blocking send)
  → BlockProcessor.processBlock()
  → AccountStateDB.Commit()
  → PersistAsync() (background PebbleDB write)
  → BroadcastToNetwork()
```

### State Mutex Pattern
`eth_call` (read-only state simulation) uses `stateMutex` (RWLock):
```go
bp.stateMutex.RLock()  // Read lock — eth_call can proceed concurrently
bp.stateMutex.Lock()   // Write lock — exclusive during block commit
```

### Epoch Transition Sequence
```
[Rust] EndOfEpoch TX committed
  → AdvanceEpochRequest → [Go] advances internal epoch counter
  → GetEpochBoundaryDataRequest → [Go] returns {committee, timestamp, boundary_block}
  → [Rust] restarts ConsensusAuthority with new committee
  → SetConsensusStartBlockRequest → [Go] sets mode barrier for new epoch
```

### TX Forward to Rust
```go
// tx_batch_forwarder_core.go
batch := packTxBatch(txList)
rustFFI.ForwardTransactionBatch(batch)  // CGo call into Rust
```

### Virtual Execution (eth_call / gas estimation)
```go
// transaction_virtual_processor.go
result := virtualExecutor.Execute(tx, readOnlyState)
// Does NOT modify AccountStateDB
// Used for fee estimation and eth_call responses
```

---

## Removed / Deprecated Concepts

| Concept | Status | Replacement |
|---------|--------|-------------|
| Go Sub-nodes / Master-nodes | ❌ Removed | Single unified process |
| Master-Sub IPC | ❌ Removed | Direct CGo FFI calls |
| AccountBatch / UDS (Unix Domain Socket) | ❌ Removed | CGo FFI callbacks |
| `cmd/consensus/` | ⚠️ Legacy | Replaced by Rust mtn-consensus |

---

## Related Projects

| Project | Path | Role |
|---------|------|------|
| `mtn-consensus` | `../consensus/` | Rust DAG-BFT consensus engine |
| MetaNode contracts | `./contracts/` | On-chain validator registry, staking |
| Web3 client | `./web3/` | Client SDK for interacting with the node |
