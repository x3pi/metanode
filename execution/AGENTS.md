# agent.md — MTN-Simple-2025 (Go Execution Engine)

## Project Identity

**Name**: `mtn-simple-2025` (MetaNode Simple Chain)  
**Language**: Go 1.23  
**Module**: `github.com/meta-node-blockchain/meta-node`  
**Purpose**: Full blockchain execution engine — handles state management, smart contract execution, transaction processing, block storage, and network synchronization.  
**Role in System**: This is the **Execution Layer** — it processes ordered transactions received from the Rust Consensus Engine (mtn-consensus) and maintains the world state (account balances, smart contracts, receipts).

---

## Architecture Overview

The Go Execution Engine runs as a **unified process** where the Rust Consensus layer is embedded via CGo FFI per validator:

```
┌──────────────────────────────────────────────────────────────┐
│                       Go Master Node                         │
│  ┌────────────────┐  ┌──────────────┐  ┌─────────────────┐  │
│  │ BlockProcessor │  │  AccountState │  │  SmartContract  │  │
│  │ (orchestrates  │  │  DB (MPT)    │  │  DB + C++ MVM   │  │
│  │  entire block  │  │              │  │                 │  │
│  │  lifecycle)    │  │              │  │                 │  │
│  └───────┬────────┘  └──────────────┘  └─────────────────┘  │
│          │                                                   │
│  ┌───────▼────────┐  ┌──────────────┐  ┌─────────────────┐  │
│  │   FFI Bridge   │  │  Transaction │  │  RPC Server     │  │
│  │ (embedded Rust │  │  Pool        │  │  (JSON-RPC/WS)  │  │
│  │   Consensus)   │  │              │  │                 │  │
│  └────────────────┘  └──────────────┘  └─────────────────┘  │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│                       Go Sub Node                            │
│  ┌────────────────┐  ┌──────────────┐  ┌─────────────────┐  │
│  │ BlockProcessor │  │  Sync from   │  │  Client-facing  │  │
│  │ (receives from │  │  Master      │  │  RPC / WS       │  │
│  │  Master via    │  │  (backup     │  │  (serve users)  │  │
│  │  AccountBatch) │  │  blocks)     │  │                 │  │
│  └────────────────┘  └──────────────┘  └─────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

### Master vs Sub Node
- **Master Node**: Receives ordered transactions from Rust Consensus, executes them, produces state roots, and broadcasts finalized blocks to Sub-nodes.
- **Sub Node**: Receives finalized blocks (with state) from the Master via `AccountBatch` replication. Serves client RPC requests. Does NOT execute transactions locally (applies pre-computed state).

---

## Repository Structure

```
mtn-simple-2025/
├── cmd/                                  # Application entry points
│   ├── simple_chain/                     # Main blockchain node
│   │   ├── main.go                       # Entry point
│   │   ├── app.go                        # Application lifecycle (Start/Stop/StopWait)
│   │   ├── app_blockchain.go             # Blockchain initialization
│   │   ├── app_network.go                # Network setup
│   │   ├── app_storage.go                # Storage initialization
│   │   ├── backend.go                    # Backend API wrapper
│   │   ├── processor/                    # ⭐ CORE: Block processing pipeline
│   │   │   ├── block_processor_core.go   # BlockProcessor struct, main init
│   │   │   ├── block_processor_commit.go # Block commit + async persistence
│   │   │   ├── block_processor_processing.go # GenerateBlock, createBlockFromResults
│   │   │   ├── block_processor_txs.go    # TxsProcessor2: batch TX forwarding to Rust
│   │   │   ├── block_processor_sync.go   # Master→Sub synchronization
│   │   │   ├── block_processor_network.go # Network sync (startup catch-up)
│   │   │   ├── block_processor_broadcast.go # Broadcast finalized blocks to subs
│   │   │   ├── block_processor_batch.go  # AccountBatch packing/unpacking
│   │   │   ├── block_processor_utils.go  # Timestamp enforcement (panic on zero)
│   │   │   ├── block_processor_state.go  # GEI (Global Execution Index) tracking
│   │   │   ├── state_processor.go        # State transition logic
│   │   │   ├── transaction_processor.go  # TX validation & pool management
│   │   │   ├── transaction_processor_pool.go  # AddTransactionToPool, ProcessTransactions
│   │   │   ├── transaction_processor_forward.go # Forward TXs to Rust
│   │   │   ├── transaction_processor_helpers.go # TX processing utilities
│   │   │   ├── transaction_processor_offchain.go # Off-chain TX processing
│   │   │   ├── transaction_virtual_processor.go  # Virtual execution (simulation)
│   │   │   ├── connection_processor.go   # Client TCP/WS connection handler
│   │   │   └── constants.go             # System constants
│   │   ├── rpc_block.go                  # eth_getBlockByNumber, etc.
│   │   ├── rpc_transaction.go            # eth_sendRawTransaction, etc.
│   │   ├── rpc_state.go                  # eth_getBalance, eth_call, etc.
│   │   ├── mtn_api.go                    # MetaNode-specific RPC methods
│   │   ├── debug_api.go                  # Debug/trace APIs
│   │   ├── tx_async_queue.go             # Async TX queue for non-blocking submission
│   │   ├── eth_broadcaster.go            # Ethereum-compatible event broadcasting
│   │   └── tool_register.go             # Validator registration tool
│   ├── exec_node/                        # Standalone execution node
│   ├── consensus/                        # Legacy consensus (pre-Mysticeti)
│   ├── mining/                           # Mining server
│   ├── keygen/                           # BLS key generation tool
│   └── tool/                             # CLI utilities
│
├── executor/                             # ⭐ Go↔Rust bridge layer (FFI)
│   ├── unix_socket.go                    # FFI bridge executor wrapper 
│   ├── unix_socket_handler.go            # RPC handlers (basic requests) routed to Rust
│   ├── unix_socket_handler_epoch.go      # Epoch-related RPC handlers (AdvanceEpoch, etc.)
│   ├── unix_socket_protocol.go           # Protobuf framing protocol
│   ├── socket_abstraction.go             # Legacy network abstraction
│   ├── listener.go                       # Commit listener (receives blocks from Rust callbacks)
│   ├── snapshot_manager.go               # LVM snapshot management
│   ├── snapshot_server.go                # Snapshot serving for new nodes
│   ├── snapshot_init.go                  # Snapshot-based state initialization
│   └── committee_notifier.go             # Committee change notifications
│
├── pkg/                                  # ⭐ Shared packages (58 sub-packages)
│   ├── account_state_db/                 # AccountStateDB — Merkle Patricia Trie
│   ├── state_db/                         # Generic state DB abstraction
│   ├── smart_contract_db/                # Smart contract storage
│   ├── block/                            # Block structure + serialization
│   ├── blockchain/                       # Chain state, tx_processor
│   │   └── tx_processor/                 # Core TX execution engine
│   │       ├── tx_processor.go           # processGroupsConcurrently()
│   │       └── vm_processor.go           # VM/Native contract execution
│   ├── storage/                          # Block storage (LevelDB/PebbleDB)
│   ├── transaction/                      # Transaction types & codec
│   ├── transaction_pool/                 # In-memory transaction pool
│   ├── grouptxns/                        # Union-Find TX grouping for parallelism
│   ├── trie/ & trie_database/            # MPT implementation
│   ├── receipt/                          # Transaction receipts
│   ├── bls/                              # BLS12-381 signatures
│   ├── config/                           # SimpleChainConfig struct
│   ├── proto/                            # Protobuf definitions (matches Rust)
│   ├── mvm/                              # C++ MVM (Meta Virtual Machine) bridge
│   │   ├── c_mvm/                        # C++ source code
│   │   ├── linker/                       # CMake bridge (Go ↔ C++)
│   │   └── build.sh                      # C++ build script
│   ├── network/                          # P2P networking
│   ├── sync/                             # Block synchronization
│   ├── explorer/                         # Block explorer data
│   ├── goxapian/                         # Xapian search integration (C++ FFI)
│   ├── jmt_ffi/                          # Jellyfish Merkle Trie FFI
│   ├── metrics/                          # Prometheus metrics
│   ├── snapshot/                         # Snapshot management
│   └── ...                               # Many more utility packages
│
├── contracts/                            # Smart contract source code
├── web3/                                 # Web3 client library
├── types/                                # Shared type definitions
├── scripts/                              # Utility scripts
├── config/                               # Configuration templates
├── build.sh                              # Main build script (handles C++ MVM)
├── build_app.sh                          # Full application build
├── run.sh                                # Start the cluster (Master + Sub nodes)
├── kill_nodes.sh                         # Stop all running nodes
└── go.mod / go.sum                       # Go module definition
```

---

## Key Concepts

### Block Processing Pipeline
The heart of the system is the `BlockProcessor` in `cmd/simple_chain/processor/`. The lifecycle of a block:

1. **Transaction Injection** → `connection_processor.go` receives TXs from clients
2. **Validation & Pooling** → `transaction_processor_pool.go` validates and pools TXs
3. **Forward to Rust** → `block_processor_txs.go` (`TxsProcessor2`) batches TXs via UDS to Rust
4. **Receive Ordered Block** → `executor/listener.go` receives committed sub-DAG from Rust
5. **Execute TXs** → `transaction_processor_pool.go` + `pkg/blockchain/tx_processor/` runs TXs in parallel groups
6. **Generate Block** → `block_processor_processing.go` creates block with state roots
7. **Commit to DB** → `block_processor_commit.go` persists block + state asynchronously
8. **Broadcast** → `block_processor_broadcast.go` sends to Sub-nodes

### State Databases
| Database | Purpose | Backend |
|----------|---------|---------|
| `AccountStateDB` | Account balances, nonces | MPT → LevelDB |
| `SmartContractDB` | Contract storage | PebbleDB |
| `TransactionStateDB` | TX receipts & logs | LevelDB/PebbleDB |
| Block Storage | Block headers & bodies | LevelDB |

### C++ MVM Bridge
The system includes a C++ Virtual Machine (`pkg/mvm/`) connected via CGo:
- C++ source in `pkg/mvm/c_mvm/`
- CMake linker in `pkg/mvm/linker/`
- **Thread safety**: `db_mutex` protects Xapian database access
- **Memory**: Uses `C.free` with deferred cleanup for high-load scenarios

### Master-Sub Synchronization
- **AccountBatch**: Serialized state diffs sent from Master to Sub-nodes
- Sub-nodes apply pre-computed state without re-executing transactions
- Sub-nodes handle client connections (RPC/WebSocket)

---

## Build

### Prerequisites
- Go 1.23+
- C++ compiler (g++ / clang++) with CMake
- RocksDB (optional, for some storage backends)

### Build Commands
```bash
# Build C++ MVM linker + Go binary
bash build.sh linux

# Or build everything (full application)
bash build_app.sh linux

# Or build Go only (if C++ is already built)
cd cmd/simple_chain && go build -o simple_chain .
```

### Build Output
- `simple_chain` — Main node binary
- `meta-node` — Alternative binary name
- `tps_blast` — Transaction throughput benchmarking tool
- `check_account` — Account state verification tool

---

## Run

### Single Node
```bash
cd cmd/simple_chain
./simple_chain --config config.json
```

### Full Cluster (5 Master + 5 Sub)
```bash
bash run.sh
```

### Configuration
Each node needs a JSON config file (see `cmd/simple_chain/config-master-node0.json`):
```json
{
  "chain_id": 991,
  "port": 8545,
  "data_dir": "data_node0",
  "is_master": true,
  "peer_rpc_port": 9001,
  "master_socket": "/tmp/meta_master_0.sock",
  "sub_socket": "/tmp/meta_sub_0.sock",
  ...
}
```

---

## IPC Protocol (Go ↔ Rust via FFI)

### Socket Executor (`executor/unix_socket.go`)
Handles incoming FFI calls and dispatches them:
- **`GetLastBlockNumber`** — Returns current chain height
- **`GetEpochBoundaryData`** — Returns epoch boundary info (timestamp, committee)
- **`AdvanceEpoch`** — Advances Go's epoch state
- **`GetValidatorsAtBlock`** — Returns validator set at a block height
- **`SetConsensusStartBlock`** / **`SetSyncStartBlock`** — Mode transition barriers

### Listener (`executor/listener.go`)
Receives committed blocks from Rust via FFI callbacks:
- **Blocking send** — Never drops blocks (critical safety invariant)
- Blocks are queued for `BlockProcessor` to execute

### Socket Abstraction (`executor/socket_abstraction.go`)
Legacy auto-detection module replaced by FFI interface.

---

## Key Dependencies
| Package | Purpose |
|---------|---------|
| `go-ethereum` v1.14.12 | EVM, crypto, RLP encoding |
| `pebble` v1.1.5 | High-performance storage (CockroachDB) |
| `goleveldb` | Legacy block storage |
| `protobuf` v1.36.5 | IPC protocol encoding |
| `quic-go` v0.50.0 | QUIC network transport |
| `blst` v0.3.13 | BLS12-381 signatures |
| `go-ethereum/verkle` | Verkle tree support |

---

## Critical Invariants

1. **No `time.Now()` for block timestamps** — Timestamps come from Rust consensus. Zero timestamp → `panic()` (fail-fast safety).
2. **Canonical validator sorting** — Sort by `AuthorityKey` bytes (BLS public key). Must match Rust exactly.
3. **AccountBatch determinism** — Master and Sub nodes must produce identical state roots.
4. **Blocking send on Listener** — The commit listener channel MUST NOT drop blocks.
5. **Deferred `C.free`** — All CGo memory allocations use `defer C.free()` to prevent leaks.

---

## Transaction Types
- **Transfer**: Native token transfer
- **Smart Contract Deploy**: Deploy EVM/MVM contract
- **Smart Contract Call**: Execute contract function
- **Stake/Unstake**: Validator staking operations
- **System TX (EndOfEpoch)**: Epoch boundary marker (from Rust consensus)

---

## Parallel Execution Model
Transactions within a block are grouped using Union-Find (`pkg/grouptxns/`):
- TXs touching the same address → same group (sequential execution)
- Independent groups → parallel execution across worker goroutines
- Produces deterministic state roots regardless of parallelism

---

## Environment & Logging
- Logs go to `master.log` / `sub.log` and node-specific log files
- Key log markers:
  - `🔍 [BLOCK-HASH-DEBUG]` — Block hash ingredients
  - `🔄 [EPOCH BOUNDARY DETECTED]` — Epoch transition
  - `📊 [EPOCH BOUNDARY] === COMMITTEE DATA ===` — Committee info
  - `⚡ [TPS]` — Throughput measurements

---

## Testing
```bash
# Unit tests
go test ./...

# Specific package tests
go test ./cmd/simple_chain/processor/ -v -run TestBlockProcessor

# TPS benchmark
go test ./cmd/simple_chain/processor/ -v -run TestTpsBenchmark -bench .

# Integration tests (Go ↔ Rust)
go test ./executor/ -v -run TestGoRustIntegration
```

---

## Common Patterns

### Async Persistence Pipeline
Block commits use an async pipeline to avoid blocking the consensus thread:
```
CommitWorker → Block Metadata DB → AccountStateDB.Commit()
            → PersistAsync (background PebbleDB write)
            → BroadcastToNetwork
```

### State Mutex Pattern
Virtual execution (eth_call) uses `stateMutex` (RWLock) to safely read state without blocking block production:
```go
bp.stateMutex.RLock()   // Read lock for eth_call
bp.stateMutex.Lock()    // Write lock for block commit
```

### GEI (Global Execution Index)
The `GlobalExecutionIndex` is the universal block height counter:
- Assigned by Rust Consensus (deterministic)
- `GEI = epoch_base_index + local_commit_index`
- Tracked atomically in `block_processor_state.go`
