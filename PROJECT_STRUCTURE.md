# 🗺️ Metanode Project Structure
> **Last updated:** 2026-06-03
> **Rule:** This file MUST be updated whenever a new module, package, or significant file is added/removed/renamed.

---

## 📐 High-Level Architecture

```
metanode/
├── execution/          ← Go execution engine (EVM-compatible layer)
├── consensus/          ← Rust consensus engine (BFT/DAG-based)
│   └── metanode/       ← Main Rust consensus node
│       ├── src/        ← Node-level consensus code
│       └── meta-consensus/  ← BFT core engine (DAG, committer, syncer)
│           ├── core/   ← Core consensus algorithm implementation
│           ├── config/ ← Consensus configuration types
│           └── types/  ← Shared consensus types
├── crates/             ← Shared Rust crates (crypto, metrics, storage, macros)
├── deploy/             ← Deployment scripts, key generator, and environment templates
├── docs/               ← Docusaurus-based web documentation site
├── note/               ← Architecture documentation & known bugs (relocated from /docs)
├── scripts/            ← Operational scripts
└── DATABASE_STRUCTURE.md ← Database directory structure and requirements based on node roles
```

### Layer Interaction

```
┌─────────────────────────────────────────────────┐
│              External Clients / RPC              │
│         (eth_*, mtn_*, web3 compatible)          │
└───────────────────────┬─────────────────────────┘
                        │ JSON-RPC / gRPC
┌───────────────────────▼─────────────────────────┐
│         Go Execution Engine (execution/)         │
│  ┌──────────────────────────────────────────┐   │
│  │  cmd/simple_chain  ← Main node process   │   │
│  │  ├── processor/    ← Core block logic    │   │
│  │  ├── main.go       ← Entrypoint          │   │
│  │  ├── app.go        ← App bootstrap       │   │
│  │  ├── backend.go    ← Chain backend       │   │
│  │  └── mtn_api.go    ← MTN RPC API        │   │
│  └──────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────┐   │
│  │  executor/         ← FFI/IPC boundary    │   │
│  │  ├── listener.go   ← Block reception     │   │
│  │  ├── unix_socket*  ← UDS handlers        │   │
│  │  ├── ffi_bridge.go ← FFI→Rust bridge     │   │
│  │  └── snapshot_*    ← Snapshot mgmt       │   │
│  └──────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────┐   │
│  │  pkg/              ← Shared packages     │   │
│  │  ├── blockchain/   ← Block commit/state  │   │
│  │  ├── account_state_db/ ← Account state   │   │
│  │  ├── sync/         ← Peer sync           │   │
│  │  ├── node/         ← Node orchestration  │   │
│  │  ├── network/      ← P2P networking      │   │
│  │  ├── nomt_ffi/     ← FFI → Rust NOMT    │   │
│  │  ├── trie/         ← State trie          │   │
│  │  ├── trie_database/← Trie persistence    │   │
│  │  ├── state/        ← Account state       │   │
│  │  ├── state_db/     ← State DB layer      │   │
│  │  ├── mapping_db/   ← Slot→trie mapping   │   │
│  │  ├── transaction/  ← Tx types            │   │
│  │  ├── transaction_pool/← Mempool          │   │
│  │  ├── transaction_grouper/ ← Tx grouping  │   │
│  │  ├── receipt/      ← Receipt mgmt        │   │
│  │  ├── snapshot/     ← State snapshots     │   │
│  │  ├── mvm/          ← Meta VM             │   │
│  │  ├── smart_contract/← Contract exec      │   │
│  │  ├── mining/       ← Block production    │   │
│  │  ├── poh/          ← Proof of History    │   │
│  │  ├── pruning/      ← State pruning       │   │
│  │  ├── proto/        ← gRPC protobuf defs  │   │
│  │  ├── models/       ← Shared data models  │   │
│  │  ├── config/       ← Node configuration  │   │
│  │  └── metrics/      ← Prometheus metrics  │   │
│  └──────────────────────────────────────────┘   │
└──────────────────┬──────────────────────────────┘
                   │ UDS (Unix Domain Socket)
                   │ FFI (C ABI via nomt_ffi)
┌──────────────────▼──────────────────────────────┐
│      Rust Consensus Engine (consensus/)          │
│  ┌──────────────────────────────────────────┐   │
│  │  consensus/metanode/src/                 │   │
│  │  ├── main.rs          ← Entrypoint       │   │
│  │  ├── ffi.rs           ← FFI exports→Go   │   │
│  │  ├── config.rs        ← Node config      │   │
│  │  ├── lib.rs           ← Lib root         │   │
│  │  ├── consensus/       ← BFT/DAG engine   │   │
│  │  │   ├── commit_processor/               │   │
│  │  │   │   ├── processor.rs ← MAIN LOOP    │   │
│  │  │   │   ├── executor.rs  ← FFI exec     │   │
│  │  │   │   ├── gei_validator.rs← GEI check │   │
│  │  │   │   ├── epoch.rs     ← Epoch detect │   │
│  │  │   │   ├── lag_monitor.rs← Backpressure│   │
│  │  │   │   └── wal.rs       ← WAL recovery │   │
│  │  │   ├── epoch_transition.rs             │   │
│  │  │   ├── tx_recycler.rs                  │   │
│  │  │   ├── checkpoint.rs                   │   │
│  │  │   ├── clock_sync.rs                   │   │
│  │  │   ├── commit_callbacks.rs← Rust→Go    │   │
│  │  │   └── state_attestation.rs            │   │
│  │  ├── node/            ← Node lifecycle   │   │
│  │  ├── network/         ← P2P networking   │   │
│  │  └── types/           ← Transaction types│   │
│  └──────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────┐   │
│  │  meta-consensus/core/src/  ← BFT ENGINE │   │
│  │  ├── authority_node/      ← Authority node module │   │
│  │  │   ├── mod.rs          ← Authority node orchestration │   │
│  │  │   └── tests.rs        ← Authority node unit tests │   │
│  │  ├── authority_service/   ← Authority service module │   │
│  │  │   ├── mod.rs          ← Lifecycle coordinator │   │
│  │  │   ├── handlers.rs     ← RPC server handlers │   │
│  │  │   └── broadcast.rs    ← Block broadcast stream │   │
│  │  ├── linearizer/          ← DAG→linear commit ordering │   │
│  │  │   ├── mod.rs          ← Deterministic commit ordering │   │
│  │  │   └── tests.rs        ← Linearizer unit tests │   │
│  │  ├── commit_syncer/      ← Commit sync module │   │
│  │  │   ├── mod.rs          ← Coord loop         │   │
│  │  │   ├── fetcher.rs      ← P2P network fetch  │   │
│  │  │   └── cold_start.rs   ← Sync transition    │   │
│  │  ├── commit_finalizer/   ← Commit finalization module │   │
│  │  │   ├── mod.rs          ← Finalizer loop     │   │
│  │  │   └── types.rs        ← Finalization state structs │   │
│  │  ├── coordination_hub.rs ← Peer attest   │   │
│  │  ├── commit_vote_monitor.rs← Digest vote │   │
│  │  ├── synchronizer/      ← Block sync module  │   │
│  │  │   ├── mod.rs          ← Event loop         │   │
│  │  │   ├── fetcher.rs      ← Fetch blocks P2P   │   │
│  │  │   └── scheduler.rs    ← Scheduled fetches  │   │
│  │  ├── block_manager/      ← Block manager module │   │
│  │  │   ├── mod.rs          ← Block validation/acceptance │   │
│  │  │   └── types.rs        ← Suspended blocks state │   │
│  │  ├── dag_state/           ← DAG state    │   │
│  │  ├── core/                ← Proposer     │   │
│  │  ├── storage/             ← RocksDB      │   │
│  │  └── network/             ← tonic gRPC   │   │
│  │  └── ...                  │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

---

## 📊 Codebase Statistics

| Layer | Files | Lines of Code |
|-------|-------|---------------|
| Go Execution (`execution/`) | ~835 | ~237K |
| Rust Consensus (`consensus/metanode/src/`) | ~88 | ~28K |
| Rust Core Engine (`meta-consensus/core/src/`) | ~83 | ~45K |
| Shared Crates (`crates/`) | ~81 | ~23K |
| **Total** | **~1087** | **~333K** |

---

## 📦 Go Execution Engine — Key Modules

### `cmd/simple_chain/` — Main Node Process
| File | Lines | Role |
|------|-------|------|
| `main.go` | 94 | CLI entrypoint, node startup |
| `app.go` | 484 | Application bootstrap, service wiring |
| `app_blockchain.go` | 1,054 | Blockchain app logic |
| `app_network.go` | 125 | Network app logic |
| `backend.go` | 660 | Chain backend (EVM state, DB) |
| `mtn_api.go` | 671 | MTN-specific JSON-RPC API |
| `rpc_block.go` | 744 | Block-related RPC handlers |
| `rpc_transaction.go` | 780 | Tx-related RPC handlers |
| `rpc_state.go` | 334 | State RPC handlers |
| `tx_async_queue.go` | 338 | Async tx submission queue |
| `debug_api.go` | 870 | Debug/admin endpoints |
| `startup_integrity_check.go` | 272 | Post-crash integrity verification |

### `cmd/simple_chain/processor/` — Core Block Processing
| File | Lines | Role |
|------|-------|------|
| `block_processor_core.go` | 1,014 | Main block processor loop |
| `block_processor_sync.go` | **1,284** | **Peer sync / state recovery** ⚠️ |
| `explorer_history_sync.go`| [DELETED] | ❌ OBSOLETE  Explorer historical data healing sync |
| `block_processor_commit.go` | 681 | Block commit pipeline |
| `block_processor_processing.go` | 780 | Tx execution pipeline |
| `block_processor_network.go` | 1,123 | Network message handling |
| `block_processor_batch.go` | 401 | Batch tx processing |
| `block_processor_attestation.go` | 488 | BLS attestation logic |
| `block_processor_epoch.go` | 182 | Epoch transition handling |
| `block_processor_state.go` | 173 | State root verification |
| `block_processor_broadcast.go` | 410 | Block broadcasting |
| `block_processor_receipt.go` | 347 | Receipt processing |
| `block_processor_indexing.go` | 166 | Block indexing |
| `block_processor_monitoring.go` | 158 | Health monitoring |
| `block_processor_logs.go` | 279 | Log handling |
| `tx_batch_forwarder_core.go` | 211 | Tx batch → consensus forwarding |
| `tx_validator_pool_core.go` | 845 | Tx validation pool |
| `tx_virtual_executor_core.go` | 237 | Virtual/offchain tx execution |
| `transaction_processor.go` | 651 | Core tx processing |
| `transaction_virtual_processor.go` | 457 | Virtual tx processing |
| `state_processor.go` | 630 | State transition processor |
| `vote_recovery.go` | 257 | Vote/quorum recovery |
| `gei_authority.go` | 234 | Go-authoritative GEI singleton |

### `execution/executor/` — FFI/IPC Boundary ⚠️ CRITICAL
| File | Lines | Role |
|------|-------|------|
| `unix_socket_handler_epoch.go` | **2,364** | Epoch-related IPC handlers (block processing, GEI, commit) |
| `snapshot_manager.go` | 1,206 | State snapshot management |
| `go_rust_integration_test.go` | 645 | Go↔Rust integration tests |
| `epoch_transition_integration_test.go` | 615 | Epoch transition tests |
| `snapshot_server.go` | 604 | Snapshot HTTP server |
| `ffi_bridge.go` | 389 | FFI bridge to Rust (C-ABI) |
| `unix_socket_handler_router.go` | 322 | UDS request routing |
| `no_fork_invariant_test.go` | 329 | Fork-safety invariant tests |
| `listener.go` | 259 | Block commit reception from Rust |
| `unix_socket.go` | 201 | UDS server setup |
| `unix_socket_handler.go` | 190 | Base UDS handler |
| `snapshot_init.go` | 223 | Snapshot initialization |
| `committee_notifier.go` | 188 | Committee change notifications |
| `socket_abstraction.go` | 142 | Socket abstraction layer |

### `execution/pkg/blockchain/tx_processor/` — Transaction Executor Layer
| File | Lines | Role |
|------|-------|------|
| `tx_processor.go` | 1,172 | **Concurrent Executor Engine** using Actor Model (Channel-based routing) by Smart Contract address to eliminate data races. |
| `validation.go` | ~323 | Core transaction verification and sanity checks. |

### `execution/pkg/account_state_db/` — Account State Management
| File | Lines | Role |
|------|-------|------|
| `account_state_db_commit.go` | 1,034 | **CommitPipeline** — parallel state root calculation, trie swap |
| `account_state_db.go` | 1,072 | Account state CRUD operations |

### `execution/pkg/blockchain/vm_processor/` — VM Execution Layer
| File | Lines | Role |
|------|-------|------|
| `vm_processor_state.go` | 1,153 | EVM state transition processing |

### `execution/pkg/mvm/` — Meta Virtual Machine
| File | Lines | Role |
|------|-------|------|
| `mvm_api.go` | 1,331 | Meta VM API layer and C-FFI wrapper |
| `extension.go` | 880 | MVM custom extension precompiles |
| `helpers.go` | 646 | MVM execution helpers |

### `execution/pkg/storage/` — Storage & DB Engines
| File | Lines | Role |
|------|-------|------|
| `pebble_db.go` | 681 | High I/O PebbleDB wrapper with WAL sync |
| `batchstore.go` | 649 | DB batch operations and backups |
| `simpledb.go` | 600 | Simple local KV storage interface |

### `cmd/rpc/` — RPC API Gateway
| Module | Role |
|--------|------|
| `cmd/rpc-client/internal/proxy/` | HTTP/WS Proxy. Intercepts specific RPCs (like `eth_getTransactionCount`) and directly queries Go `AccountStateDB` via TCP to bypass stale C++ caches. |
| `tcp-rpc/client-tcp/` | Go implementation of RPC TCP Client for high-performance direct queries. |

### `pkg/` — Shared Packages (Critical Ones)
| Package | Role | Concurrency Risk |
|---------|------|-----------------| 
| `blockchain/` | Block state commit, `block_state_commit.go` | 🔴 HIGH — state root write |
| `account_state_db/` | Account state management, CommitPipeline | 🔴 HIGH — trie mutations |
| `sync/` | Peer sync, anti-entropy | 🔴 HIGH — distributed state |
| `nomt_ffi/` | FFI bridge to Rust NOMT trie | 🟡 MED — C boundary |
| `trie/` | Merkle trie operations (Flat + NOMT backends) | 🟡 MED — shared read |
| `trie_database/` | Trie persistence layer | 🟡 MED — DB write |
| `mapping_db/` | Slot→trie key mapping | 🟡 MED — DB write |
| `state/` | Account state transitions | 🔴 HIGH — EVM state |
| `state_db/` | State database layer (Stake, Smart Contract) | 🟡 MED — DB |
| `transaction_pool/` | Mempool management (Actor pattern, channel-based) | 🟡 MED — concurrent access |
| `network/` | P2P connection mgmt | 🟡 MED — async I/O |
| `mining/` | Block production | 🔴 HIGH — timing sensitive |
| `poh/` | Proof of History | 🟡 MED — clock sensitive |
| `snapshot/` | State snapshot/restore | 🟡 MED — large I/O |
| `mvm/` | Meta VM execution | 🔴 HIGH — deterministic |
| `pruning/` | State pruning manager | 🟡 MED — async background |
| `proto/` | gRPC proto definitions | 🟢 LOW |

---

## 🦀 Shared Rust Crates (`crates/`)

| Crate | Role |
|-------|------|
| `meta-protocol-config` | [DELETED] | ❌ OBSOLETE 
| `meta-protocol-config-macros` | Procedural macros for protocol config |
| `meta-macros` | [DELETED] | ❌ OBSOLETE 
| `meta-proc-macros` | Procedural macros |
| `meta-http` | Shared HTTP client/server utilities |
| `meta-tls` | TLS configuration |
| `meta-enum-compat-util` | [DELETED] | ❌ OBSOLETE 
| `mysten-common` | Common utilities (origin: Sui/Mysten Labs) |
| `mysten-metrics` | [DELETED] | ❌ OBSOLETE 
| `mysten-network` | Network types (origin: Sui/Mysten Labs) |
| `shared-crypto` | [DELETED] | ❌ OBSOLETE 
| `typed-store` | Type-safe RocksDB wrapper |
| `typed-store-derive` | [DELETED] | ❌ OBSOLETE 
| `typed-store-error` | Error types for typed-store |
| `typed-store-workspace-hack` | [DELETED] | ❌ OBSOLETE 
| `telemetry-subscribers` | Tracing/telemetry subscribers |
| `prometheus-closure-metric` | [DELETED] | ❌ OBSOLETE 
| `metanode-keytool` | **Library & CLI tool** — generate BLS12-381/Ed25519/ETH keys for validators. Also integrated as a subcommand under the main `metanode` CLI. |

---

## 🦀 Rust Consensus Engine — Full Module Map

### Root: `consensus/metanode/src/`
| File | Lines | Role |
|------|-------|------|
| `main.rs` | 86 | Binary entrypoint, runtime init |
| `ffi.rs` | 428 | **C-ABI exports callable from Go** via `nomt_ffi/` — state commits, trie updates, root queries |
| `config.rs` | 117 | Node configuration parsing |
| `lib.rs` | 706 | Library root |

### `src/consensus/commit_processor/` — BFT Commit Engine ⚠️ CRITICAL
| File | Lines | Role | Risk |
|------|-------|------|------|
| `processor.rs` | **1,813** | **Main ordered commit loop** — drives all execution, DIGEST-GATE, ZERO-TIMEOUT peer attestation | 🔴 CRITICAL |
| `executor.rs` | 303 | Calls Go FFI to execute committed blocks | 🔴 HIGH |
| `gei_validator.rs` | 382 | Validates GEI (Go Execution Interface) responses | 🔴 HIGH |
| `epoch.rs` | 66 | Epoch boundary detection within commit loop | 🔴 HIGH |
| `lag_monitor.rs` | 157 | Commit lag monitoring / backpressure | 🟡 MED |
| `wal.rs` | 124 | Write-ahead log for crash recovery | 🟡 MED |

### `src/consensus/` — Epoch & State Management
| File | Lines | Role | Risk |
|------|-------|------|------|
| `epoch_transition.rs` | 759 | Epoch boundary trigger + tx drainage | 🔴 HIGH |
| `tx_recycler.rs` | 363 | Recycles uncommitted txs post-epoch | 🟡 MED |
| `checkpoint.rs` | 71 | Checkpoint save/restore | 🟡 MED |
| `clock_sync.rs` | 225 | BFT clock synchronization | 🟡 MED |
| `commit_callbacks.rs` | 65 | **Rust→Go** commit notifications | 🔴 HIGH |
| `state_attestation.rs` | 144 | State root attestation pre-commit | 🔴 HIGH |

### `src/node/` — Node Orchestration ⚠️ LARGEST MODULE (35 files)
| File | Lines | Role | Risk |
|------|-------|------|------|
| `consensus_node.rs` | **236** | **Central node orchestrator** — delegates setup to sub-modules | 🔴 CRITICAL |
| `setup_storage/mod.rs` | **838** | **Phase 1: Storage setup** — discovers epoch, builds committee, verifies hash | 🔴 HIGH |
| `setup_storage/index_sync.rs` | 115 | Helper to determine Go last global execution index | 🟡 MED |
| `setup_consensus/mod.rs` | **1,066** | **Phase 2: Consensus setup** — orchestrates consensus initialization | 🔴 CRITICAL |
| `setup_consensus/startup_sync.rs` | 651 | Startup block sync loop implementation | 🔴 HIGH |
| `setup_consensus/verification.rs` | 231 | Post-gate and background block hash verification | 🔴 HIGH |
| `setup_consensus/fork_guard.rs` | 149 | Runtime Fork Guard background hash verification | 🔴 HIGH |
| `epoch_monitor/mod.rs` | **221** | **Unified epoch monitor** — coordinates health checks and delegates transitions | 🔴 HIGH |
| `epoch_monitor/stall_recovery.rs` | 134 | Validator block stall recovery via active P2P sync | 🔴 HIGH |
| `epoch_monitor/sync_only_advance.rs` | 113 | SyncOnly sequential epoch advancement | 🔴 HIGH |
| `epoch_monitor/validator_transition.rs` | 268 | Validator multi-epoch catchup transition | 🔴 HIGH |
| `epoch_transition_manager.rs` | 570 | Full epoch handoff sequencing | 🔴 HIGH |
| `epoch_checkpoint.rs` | 321 | Epoch state persistence at boundaries | 🔴 HIGH |
| `epoch_store.rs` | 206 | Epoch metadata storage | 🟡 MED |
| `committee.rs` | 271 | Validator committee management | 🔴 HIGH |
| `committee_source.rs` | 554 | Committee selection logic + hash verification | 🔴 HIGH |
| `node_methods.rs` | 442 | Node API implementation (shutdown, mode switch) | 🟡 MED |
| `startup.rs` | 323 | Boot sequence | 🟡 MED |
| `sync.rs` | 206 | Sync state machine | 🔴 HIGH |
| `sync_controller.rs` | 317 | Sync session controller | 🔴 HIGH |
| `sync_metrics.rs` | 221 | Sync performance metrics | 🟢 LOW |
| `recovery.rs` | 184 | Crash/fork recovery | 🔴 HIGH |
| `rpc_circuit_breaker.rs` | 447 | Circuit breaker for Go RPC | 🟡 MED |
| `peer_go_client.rs` | 297 | RPC client to Go execution layer | 🔴 HIGH |
| `peer_health.rs` | 158 | Peer liveness monitoring | 🟡 MED |
| `health_check.rs` | 175 | Node health endpoint | 🟢 LOW |
| `queue.rs` | 283 | Internal task queue | 🟡 MED |
| `coordinator.rs` | 89 | Cross-module coordinator | 🟡 MED |
| `block_delivery.rs` | 85 | Block delivery to consumers | 🟡 MED |
| `notification_server.rs` | 142 | Push notification server | 🟢 LOW |
| `tx_submitter.rs` | 176 | Submit txs to consensus | 🟡 MED |
| `epoch_transition_tests.rs` | 942 | Epoch transition test suite | 🟢 TEST |

### `src/node/executor_client/` — Go Execution Client ⚠️ FFI/RPC BOUNDARY
| File | Lines | Role |
|------|-------|------|
| `mod.rs` | 139 | Main client logic — call routing to Go |
| `block_sending.rs` | 971 | Send committed blocks to Go execution layer |
| `block_store.rs` | 158 | Local block cache |
| `block_sync.rs` | 151 | Block sync coordination with Go |
| `rpc_queries.rs` | 462 | Query Go execution state via RPC |
| `rpc_queries_epoch.rs` | 352 | Epoch-specific RPC queries |
| `connection_pool.rs` | 251 | Connection pool to Go execution |
| `persistence.rs` | 487 | Persist execution results |
| `socket_stream.rs` | 259 | Socket stream handling |
| `traits.rs` | 233 | Abstract executor traits |
| `transition_handoff.rs` | 272 | Epoch transition handoff to Go |

### `src/node/rust_sync_node/` — Sync-Only Node Mode
| File | Lines | Role |
|------|-------|------|
| `sync_loop.rs` | 767 | Main sync loop — drives block catch-up |
| `fetch.rs` | 824 | Block fetch logic from peers |
| `epoch_recovery.rs` | 349 | Epoch crash recovery during sync |
| `block_queue.rs` | 427 | Incoming block queue |
| `start.rs` | 112 | Sync node startup sequence |
| `mod.rs` | 139 | Module root + RustSyncHandle |
| `sync_loop_tests.rs` | 203 | Sync loop test suite |
| `fetch_tests.rs` | 174 | Fetch test suite |
| `epoch_recovery_tests.rs` | 149 | Epoch recovery test suite |

### `src/node/transition/` — Mode Transition Logic
| File | Lines | Role |
|------|-------|------|
| `epoch_transition.rs` | 759 | Full epoch transition orchestration |
| `mode_transition.rs` | 503 | Node mode changes (validator ↔ observer) |
| `consensus_setup.rs` | 397 | Consensus re-setup post-transition |
| `demotion.rs` | 306 | Node demotion logic |
| `tx_recovery.rs` | 278 | Tx recovery during transition |
| `verification.rs` | 239 | Post-transition state verification |

### `src/network/` — P2P Consensus Networking
| File | Lines | Role | Risk |
|------|-------|------|------|
| `rpc.rs` | 641 | Main RPC server | 🔴 HIGH |
| `tx_socket_server.rs` | 382 | Tx reception socket | 🟡 MED |
| `peer_discovery.rs` | 427 | Peer discovery | 🟡 MED |
| `codec.rs` | 51 | Message encoding | 🟢 LOW |
| `peer_rpc/server.rs` | 819 | Peer RPC server | 🔴 HIGH |
| `peer_rpc/client.rs` | 674 | Peer RPC client | 🔴 HIGH |
| `peer_rpc/types.rs` | 22 | RPC types | 🟢 LOW |

### `src/types/`
| File | Role |
|------|------|
| `transaction.rs` | Core Tx type |
| `tx_hash.rs` | Tx hash utilities |

---

## 🧠 Meta-Consensus Core Engine (`meta-consensus/core/src/`)

> This is the BFT consensus algorithm core — DAG-based Mysticeti variant.

### Key Files (>500 lines)
| File | Lines | Role | Risk |
|------|-------|------|------|
| `commit_syncer/mod.rs` | **2,812** | Commit synchronization main coordination loop | 🔴 CRITICAL |
| `synchronizer/mod.rs` | **1,471** | Live block synchronization main loop and verification | 🔴 HIGH |
| `synchronizer/scheduler.rs` | 522 | Scheduled periodic block and own last block fetching | 🟡 MED |
| `dag_state/tests.rs` | 1,376 | DAG state unit test suite | 🟢 TEST |
| `network/tonic_network.rs` | 1,433 | tonic gRPC network layer | 🟡 MED |
| `authority_service/handlers.rs` | 907 | RPC handlers for block subscription and fetching | 🔴 HIGH |
| `commit_finalizer/mod.rs` | 900 | Commit finalization coordination loop | 🔴 HIGH |
| `block_manager/mod.rs` | 672 | Suspended/missing blocks tracking and acceptance | 🔴 HIGH |
| `authority_node/mod.rs` | 724 | Authority node orchestration | 🔴 HIGH |
| `linearizer/mod.rs` | 571 | DAG → linear commit ordering (deterministic) | 🔴 CRITICAL |
| `linearizer/tests.rs` | 600 | Linearizer unit tests | 🟢 TEST |
| `core_tests/commits.rs` | 955 | Core commit and scheduler tests | 🟢 TEST |
| `core_tests/proposal.rs` | 809 | Core proposal and timeout tests | 🟢 TEST |
| `core_tests/ancestors.rs` | 512 | Core ancestors and round signals tests | 🟢 TEST |
| `metrics.rs` | 1,104 | Prometheus metrics definitions | 🟢 LOW |
| `leader_schedule.rs` | 1,072 | Leader election scheduling (stake-weighted) | 🔴 HIGH |
| `commit.rs` | 1,051 | Commit types + verification | 🔴 HIGH |
| `transaction.rs` | 312 | Transaction types and batching | 🟡 MED |
| `transaction_certifier.rs` | 962 | TX certification pipeline | 🟡 MED |
| `commit_observer.rs` | 937 | Commit observation + notification | 🟡 MED |
| `block.rs` | 840 | Block types + serialization | 🟡 MED |
| `core/proposer.rs` | 826 | Block proposal logic | 🔴 HIGH |
| `block_verifier.rs` | 811 | Block signature + content verification | 🔴 HIGH |
| `tx_group_filter.rs` | 182 | Union-Find transaction grouping & limit check | 🟢 LOW |

### Supporting Files (<500 lines)
| File | Lines | Role |
|------|-------|------|
| `authority_node/tests.rs` | 471 | Authority node unit tests |
| `core_tests/recovery.rs` | 295 | Core consensus crash recovery tests |
| `core_tests/mod.rs` | 190 | Core tests common helpers and setup |
| `coordination_hub.rs` | 593 | **Peer attestation hub** — ZERO-TIMEOUT peer commit verification |
| `core/commit_manager.rs` | 569 | Commit decision management |
| `subscriber.rs` | 446 | Block subscription |
| `round_tracker.rs` | 512 | Round advancement tracking |
| `round_prober.rs` | 471 | Peer round probing |
| `recovery_barrier.rs` | 411 | Recovery synchronization barrier |
| `dag_state/write.rs` | 650 | DAG state write operations |
| `dag_state/read.rs` | 449 | DAG state read operations |
| `dag_state/dag_state_impl.rs` | 498 | DAG state machine implementation |
| `core_thread.rs` | 584 | Core consensus thread |
| `commit_vote_monitor.rs` | 303 | **Digest vote tracking** — commit hash quorum verification |
| `commit_consumer.rs` | 159 | Commit consumption interface |
| `system_transaction_provider.rs` | 545 | System TX (epoch change) provider |
| `adaptive_delay.rs` | 201 | Adaptive round delay |
| `leader_scoring.rs` | 336 | Leader reputation scoring |
| `leader_timeout.rs` | 301 | Leader timeout handling |
| `stake_aggregator.rs` | 154 | Stake aggregation |
| `threshold_clock.rs` | 215 | Threshold clock |
| `context.rs` | 187 | Consensus context |
| `error.rs` | 254 | Error types |
| `storage/rocksdb_store.rs` | 472 | RocksDB persistent store |
| `commit_syncer/fetcher.rs` | 495 | P2P block and commit fetch loop |
| `commit_syncer/cold_start.rs` | 233 | Sync status transition decisions |
| `synchronizer/fetcher.rs` | 275 | P2P block fetch worker and verifier |
| `storage/mem_store.rs` | 275 | In-memory store (testing) |
| `authority_service/mod.rs` | 182 | Authority lifecycle service coordinator |
| `authority_service/broadcast.rs` | 203 | Block broadcast stream and counters |
| `commit_finalizer/types.rs` | 88 | State structs for commit finalization |
| `block_manager/types.rs` | 40 | State structs for block manager |

---

## 🔗 Cross-Layer Communication

| Channel | Direction | Protocol | Files |
|---------|-----------|----------|-------|
| Block commit delivery | Rust → Go | UDS socket (send stream) | `block_sending.rs` → `listener.go` |
| Commit notification | Rust → Go | FFI callback | `commit_callbacks.rs` |
| Tx batch forwarding | Go → Rust | UDS socket | `tx_batch_forwarder_core.go` → `tx_socket_server.rs` |
| RPC queries (epoch, block, GEI) | Rust → Go | UDS connection pool | `rpc_queries.rs` → `unix_socket_handler*.go` |
| State root verification | Go ↔ Rust | FFI (C-ABI) | `nomt_ffi/` ↔ `ffi.rs` |
| Peer sync (block data) | Go ↔ Go | QUIC + custom TCP P2P | `pkg/network/` |
| Consensus votes/blocks | Rust ↔ Rust | tonic gRPC | `network/tonic_network.rs` |
| Peer RPC (epoch boundary, sync) | Rust ↔ Rust | Custom TCP | `peer_rpc/server.rs` ↔ `peer_rpc/client.rs` |
| Commit digest attestation | Rust ↔ Rust | P2P embedded in DAG blocks | `coordination_hub.rs` ↔ `commit_vote_monitor.rs` |

---

## ⚠️ High-Risk Change Zones

> These areas have the highest blast radius. Always grep callers before modifying.

| Zone | Location | Risk |
|------|----------|------|
| State root commit | `pkg/blockchain/block_state_commit.go` | Fork risk |
| Account state pipeline | `pkg/account_state_db/account_state_db_commit.go` | Fork risk |
| Peer sync handler | `processor/block_processor_sync.go` | State divergence |
| FFI boundary (Go→Rust) | `pkg/nomt_ffi/` + `src/ffi.rs` | Crash / memory safety |
| FFI boundary (Rust→Go) | `executor/unix_socket_handler_epoch.go` + `executor_client/` | IPC failure |
| Epoch transition | `processor/block_processor_epoch.go` + `src/consensus/epoch_transition.rs` | Data loss |
| Commit processor | `src/consensus/commit_processor/` | Ordering violation |
| Linearizer | `meta-consensus/core/src/linearizer.rs` | Fork (non-deterministic commit) |
| CommitSyncer | `meta-consensus/core/src/commit_syncer/mod.rs` | Sync failure / stale data |
| Tx batch forwarder | `processor/tx_batch_forwarder_core.go` | Tx loss |
| Mining/PoH | `pkg/mining/` + `pkg/poh/` | Timing regression |
| Snapshot manager | `executor/snapshot_manager.go` | Data corruption during restore |

---

## 📝 Update Protocol

When to update this file:
- ✅ New package/module added to `pkg/` or `src/`
- ✅ New entrypoint or command added to `cmd/`
- ✅ FFI interface changed
- ✅ gRPC proto definitions changed
- ✅ Cross-layer communication channel added/removed
- ✅ File renamed or moved
- ✅ New crate added to `crates/`
- ✅ Significant file size changes (>100 lines growth)
- ❌ Internal implementation changes (no structural change)
