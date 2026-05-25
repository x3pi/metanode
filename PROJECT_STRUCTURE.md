# 🗺️ Metanode Project Structure
> **Last updated:** 2026-05-25
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
├── docs/               ← Docusaurus-based web documentation site
├── note/               ← Architecture documentation & known bugs (relocated from /docs)
└── scripts/            ← Operational scripts
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
│  │  ├── authority_node.rs    ← Authority    │   │
│  │  ├── authority_service.rs ← Lifecycle    │   │
│  │  ├── linearizer.rs       ← DAG→linear   │   │
│  │  ├── commit_syncer.rs    ← Commit sync   │   │
│  │  ├── commit_finalizer.rs ← Finalization  │   │
│  │  ├── coordination_hub.rs ← Peer attest   │   │
│  │  ├── commit_vote_monitor.rs← Digest vote │   │
│  │  ├── synchronizer.rs     ← Block sync    │   │
│  │  ├── dag_state/           ← DAG state    │   │
│  │  ├── core/                ← Proposer     │   │
│  │  ├── storage/             ← RocksDB      │   │
│  │  └── network/             ← tonic gRPC   │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

---

## 📊 Codebase Statistics

| Layer | Files | Lines of Code |
|-------|-------|---------------|
| Go Execution (`execution/`) | ~700+ | ~122K |
| Rust Consensus (`consensus/metanode/src/`) | ~65 | ~29K |
| Rust Core Engine (`meta-consensus/core/src/`) | ~55 | ~45K |
| Shared Crates (`crates/`) | ~200+ | ~140K |
| **Total** | **~1,100** | **~337K** |

---

## 📦 Go Execution Engine — Key Modules

### `cmd/simple_chain/` — Main Node Process
| File | Lines | Role |
|------|-------|------|
| `main.go` | 823 | CLI entrypoint, node startup |
| `app.go` | 691 | Application bootstrap, service wiring |
| `app_blockchain.go` | 1,036 | Blockchain app logic |
| `app_network.go` | 125 | Network app logic |
| `backend.go` | 660 | Chain backend (EVM state, DB) |
| `mtn_api.go` | 658 | MTN-specific JSON-RPC API |
| `rpc_block.go` | 634 | Block-related RPC handlers |
| `rpc_transaction.go` | 733 | Tx-related RPC handlers |
| `rpc_state.go` | 320 | State RPC handlers |
| `tx_async_queue.go` | 280 | Async tx submission queue |
| `debug_api.go` | 690 | Debug/admin endpoints |
| `startup_integrity_check.go` | 430 | Post-crash integrity verification |

### `cmd/simple_chain/processor/` — Core Block Processing
| File | Lines | Role |
|------|-------|------|
| `block_processor_core.go` | 1,007 | Main block processor loop |
| `block_processor_sync.go` | **1,252** | **Peer sync / state recovery** ⚠️ |
| `block_processor_commit.go` | 642 | Block commit pipeline |
| `block_processor_processing.go` | 764 | Tx execution pipeline |
| `block_processor_network.go` | 1,089 | Network message handling |
| `block_processor_batch.go` | 394 | Batch tx processing |
| `block_processor_attestation.go` | 488 | BLS attestation logic |
| `block_processor_epoch.go` | 170 | Epoch transition handling |
| `block_processor_state.go` | 152 | State root verification |
| `block_processor_broadcast.go` | 410 | Block broadcasting |
| `block_processor_receipt.go` | 347 | Receipt processing |
| `block_processor_indexing.go` | 153 | Block indexing |
| `block_processor_monitoring.go` | 175 | Health monitoring |
| `block_processor_logs.go` | 222 | Log handling |
| `tx_batch_forwarder_core.go` | 352 | Tx batch → consensus forwarding |
| `tx_validator_pool_core.go` | 729 | Tx validation pool |
| `tx_virtual_executor_core.go` | 209 | Virtual/offchain tx execution |
| `transaction_processor.go` | 609 | Core tx processing |
| `transaction_virtual_processor.go` | 432 | Virtual tx processing |
| `state_processor.go` | 596 | State transition processor |
| `vote_recovery.go` | 240 | Vote/quorum recovery |
| `gei_authority.go` | 250 | Go-authoritative GEI singleton |

### `execution/executor/` — FFI/IPC Boundary ⚠️ CRITICAL
| File | Lines | Role |
|------|-------|------|
| `unix_socket_handler_epoch.go` | **2,301** | Epoch-related IPC handlers (block processing, GEI, commit) |
| `snapshot_manager.go` | 1,152 | State snapshot management |
| `go_rust_integration_test.go` | 645 | Go↔Rust integration tests |
| `epoch_transition_integration_test.go` | 615 | Epoch transition tests |
| `snapshot_server.go` | 604 | Snapshot HTTP server |
| `ffi_bridge.go` | 356 | FFI bridge to Rust (C-ABI) |
| `unix_socket_handler_router.go` | 322 | UDS request routing |
| `no_fork_invariant_test.go` | 329 | Fork-safety invariant tests |
| `listener.go` | 259 | Block commit reception from Rust |
| `unix_socket.go` | 201 | UDS server setup |
| `unix_socket_handler.go` | 190 | Base UDS handler |
| `snapshot_init.go` | 201 | Snapshot initialization |
| `committee_notifier.go` | 188 | Committee change notifications |
| `socket_abstraction.go` | 106 | Socket abstraction layer |

### `execution/pkg/blockchain/tx_processor/` — Transaction Executor Layer
| File | Lines | Role |
|------|-------|------|
| `tx_processor.go` | 1,071 | **Concurrent Executor Engine** using Actor Model (Channel-based routing) by Smart Contract address to eliminate data races. |
| `validation.go` | ~200 | Core transaction verification and sanity checks. |

### `execution/pkg/account_state_db/` — Account State Management
| File | Lines | Role |
|------|-------|------|
| `account_state_db_commit.go` | 1,084 | **CommitPipeline** — parallel state root calculation, trie swap |
| `account_state_db.go` | 1,079 | Account state CRUD operations |

### `execution/pkg/blockchain/vm_processor/` — VM Execution Layer
| File | Lines | Role |
|------|-------|------|
| `vm_processor_state.go` | 984 | EVM state transition processing |

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
| `meta-protocol-config` | Protocol configuration types and versioning |
| `meta-protocol-config-macros` | Procedural macros for protocol config |
| `meta-macros` | General utility macros |
| `meta-proc-macros` | Procedural macros |
| `meta-http` | HTTP utilities |
| `meta-tls` | TLS configuration |
| `meta-enum-compat-util` | Enum compatibility utilities |
| `mysten-common` | Common utilities (origin: Sui/Mysten Labs) |
| `mysten-metrics` | Prometheus metrics (origin: Sui/Mysten Labs) |
| `mysten-network` | Network types (origin: Sui/Mysten Labs) |
| `shared-crypto` | Cryptographic primitives |
| `typed-store` | Type-safe RocksDB wrapper |
| `typed-store-derive` | Derive macros for typed-store |
| `typed-store-error` | Error types for typed-store |
| `typed-store-workspace-hack` | Workspace dependency hack |
| `telemetry-subscribers` | Tracing/telemetry subscribers |
| `prometheus-closure-metric` | Prometheus metric helpers |

---

## 🦀 Rust Consensus Engine — Full Module Map

### Root: `consensus/metanode/src/`
| File | Lines | Role |
|------|-------|------|
| `main.rs` | 65 | Binary entrypoint, runtime init |
| `ffi.rs` | 403 | **C-ABI exports callable from Go** via `nomt_ffi/` — state commits, trie updates, root queries |
| `config.rs` | 478 | Node configuration parsing |
| `lib.rs` | 10 | Library root |

### `src/consensus/commit_processor/` — BFT Commit Engine ⚠️ CRITICAL
| File | Lines | Role | Risk |
|------|-------|------|------|
| `processor.rs` | **1,893** | **Main ordered commit loop** — drives all execution, DIGEST-GATE, ZERO-TIMEOUT peer attestation | 🔴 CRITICAL |
| `executor.rs` | 470 | Calls Go FFI to execute committed blocks | 🔴 HIGH |
| `gei_validator.rs` | 380 | Validates GEI (Go Execution Interface) responses | 🔴 HIGH |
| `epoch.rs` | 80 | Epoch boundary detection within commit loop | 🔴 HIGH |
| `lag_monitor.rs` | 150 | Commit lag monitoring / backpressure | 🟡 MED |
| `wal.rs` | 105 | Write-ahead log for crash recovery | 🟡 MED |

### `src/consensus/` — Epoch & State Management
| File | Lines | Role | Risk |
|------|-------|------|------|
| `epoch_transition.rs` | 200 | Epoch boundary trigger + tx drainage | 🔴 HIGH |
| `tx_recycler.rs` | 360 | Recycles uncommitted txs post-epoch | 🟡 MED |
| `checkpoint.rs` | 90 | Checkpoint save/restore | 🟡 MED |
| `clock_sync.rs` | 210 | BFT clock synchronization | 🟡 MED |
| `commit_callbacks.rs` | 70 | **Rust→Go** commit notifications | 🔴 HIGH |
| `state_attestation.rs` | 160 | State root attestation pre-commit | 🔴 HIGH |

### `src/node/` — Node Orchestration ⚠️ LARGEST MODULE (33 files)
| File | Lines | Role | Risk |
|------|-------|------|------|
| `consensus_node.rs` | **300** | **Central node orchestrator** — delegates setup to sub-modules | 🔴 CRITICAL |
| `setup_storage.rs` | **960** | **Phase 1: Storage setup** — discovers epoch, builds committee, verifies hash | 🔴 HIGH |
| `setup_consensus.rs` | **2,400** | **Phase 2: Consensus setup** — startup state sync, runtime fork guard | 🔴 CRITICAL |
| `epoch_monitor.rs` | 576 | Epoch health monitoring + alerts | 🔴 HIGH |
| `epoch_transition_manager.rs` | 570 | Full epoch handoff sequencing | 🔴 HIGH |
| `epoch_checkpoint.rs` | 270 | Epoch state persistence at boundaries | 🔴 HIGH |
| `epoch_store.rs` | 190 | Epoch metadata storage | 🟡 MED |
| `committee.rs` | 270 | Validator committee management | 🔴 HIGH |
| `committee_source.rs` | 554 | Committee selection logic + hash verification | 🔴 HIGH |
| `node_methods.rs` | 442 | Node API implementation (shutdown, mode switch) | 🟡 MED |
| `startup.rs` | 370 | Boot sequence | 🟡 MED |
| `sync.rs` | 230 | Sync state machine | 🔴 HIGH |
| `sync_controller.rs` | 270 | Sync session controller | 🔴 HIGH |
| `sync_metrics.rs` | 250 | Sync performance metrics | 🟢 LOW |
| `recovery.rs` | 210 | Crash/fork recovery | 🔴 HIGH |
| `rpc_circuit_breaker.rs` | 447 | Circuit breaker for Go RPC | 🟡 MED |
| `peer_go_client.rs` | 290 | RPC client to Go execution layer | 🔴 HIGH |
| `peer_health.rs` | 130 | Peer liveness monitoring | 🟡 MED |
| `health_check.rs` | 220 | Node health endpoint | 🟢 LOW |
| `queue.rs` | 260 | Internal task queue | 🟡 MED |
| `coordinator.rs` | 80 | Cross-module coordinator | 🟡 MED |
| `block_delivery.rs` | 82 | Block delivery to consumers | 🟡 MED |
| `notification_server.rs` | 145 | Push notification server | 🟢 LOW |
| `tx_submitter.rs` | 140 | Submit txs to consensus | 🟡 MED |
| `epoch_transition_tests.rs` | 942 | Epoch transition test suite | 🟢 TEST |

### `src/node/executor_client/` — Go Execution Client ⚠️ FFI/RPC BOUNDARY
| File | Lines | Role |
|------|-------|------|
| `mod.rs` | 706 | Main client logic — call routing to Go |
| `block_sending.rs` | 962 | Send committed blocks to Go execution layer |
| `block_store.rs` | 130 | Local block cache |
| `block_sync.rs` | 150 | Block sync coordination with Go |
| `rpc_queries.rs` | 462 | Query Go execution state via RPC |
| `rpc_queries_epoch.rs` | 380 | Epoch-specific RPC queries |
| `connection_pool.rs` | 230 | Connection pool to Go execution |
| `persistence.rs` | 487 | Persist execution results |
| `socket_stream.rs` | 250 | Socket stream handling |
| `traits.rs` | 195 | Abstract executor traits |
| `transition_handoff.rs` | 275 | Epoch transition handoff to Go |

### `src/node/rust_sync_node/` — Sync-Only Node Mode
| File | Lines | Role |
|------|-------|------|
| `sync_loop.rs` | 767 | Main sync loop — drives block catch-up |
| `fetch.rs` | 824 | Block fetch logic from peers |
| `epoch_recovery.rs` | 425 | Epoch crash recovery during sync |
| `block_queue.rs` | 427 | Incoming block queue |
| `start.rs` | 110 | Sync node startup sequence |
| `mod.rs` | 200 | Module root + RustSyncHandle |
| `sync_loop_tests.rs` | 190 | Sync loop test suite |
| `fetch_tests.rs` | 155 | Fetch test suite |
| `epoch_recovery_tests.rs` | 130 | Epoch recovery test suite |

### `src/node/transition/` — Mode Transition Logic
| File | Lines | Role |
|------|-------|------|
| `epoch_transition.rs` | 741 | Full epoch transition orchestration |
| `mode_transition.rs` | 503 | Node mode changes (validator ↔ observer) |
| `consensus_setup.rs` | 397 | Consensus re-setup post-transition |
| `demotion.rs` | 290 | Node demotion logic |
| `tx_recovery.rs` | 235 | Tx recovery during transition |
| `verification.rs` | 230 | Post-transition state verification |

### `src/network/` — P2P Consensus Networking
| File | Lines | Role | Risk |
|------|-------|------|------|
| `rpc.rs` | 641 | Main RPC server | 🔴 HIGH |
| `tx_socket_server.rs` | 480 | Tx reception socket | 🟡 MED |
| `peer_discovery.rs` | 427 | Peer discovery | 🟡 MED |
| `codec.rs` | 48 | Message encoding | 🟢 LOW |
| `peer_rpc/server.rs` | 815 | Peer RPC server | 🔴 HIGH |
| `peer_rpc/client.rs` | 649 | Peer RPC client | 🔴 HIGH |
| `peer_rpc/types.rs` | 80 | RPC types | 🟢 LOW |

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
| `commit_syncer.rs` | **3,370** | Commit synchronization + peer fetching + cold-start | 🔴 CRITICAL |
| `core_tests.rs` | 2,738 | Comprehensive consensus tests | 🟢 TEST |
| `synchronizer.rs` | 2,175 | DAG block synchronization | 🔴 HIGH |
| `dag_state/dag_state_impl.rs` | 1,867 | DAG state machine implementation | 🔴 HIGH |
| `authority_service.rs` | 1,828 | Authority lifecycle service | 🔴 HIGH |
| `commit_finalizer.rs` | 1,605 | Commit finalization logic | 🔴 HIGH |
| `network/tonic_network.rs` | 1,433 | tonic gRPC network layer | 🟡 MED |
| `block_manager.rs` | 1,300 | Block validation + storage | 🔴 HIGH |
| `authority_node.rs` | 1,193 | Authority node orchestration | 🔴 HIGH |
| `linearizer.rs` | 1,154 | DAG → linear commit ordering (deterministic) | 🔴 CRITICAL |
| `metrics.rs` | 1,104 | Prometheus metrics definitions | 🟢 LOW |
| `leader_schedule.rs` | 1,072 | Leader election scheduling (stake-weighted) | 🔴 HIGH |
| `commit.rs` | 1,051 | Commit types + verification | 🔴 HIGH |
| `transaction.rs` | 1,000 | Transaction types and batching | 🟡 MED |
| `transaction_certifier.rs` | 962 | TX certification pipeline | 🟡 MED |
| `commit_observer.rs` | 937 | Commit observation + notification | 🟡 MED |
| `block.rs` | 840 | Block types + serialization | 🟡 MED |
| `core/proposer.rs` | 826 | Block proposal logic | 🔴 HIGH |
| `block_verifier.rs` | 802 | Block signature + content verification | 🔴 HIGH |

### Supporting Files (<500 lines)
| File | Lines | Role |
|------|-------|------|
| `coordination_hub.rs` | 593 | **Peer attestation hub** — ZERO-TIMEOUT peer commit verification |
| `core/commit_manager.rs` | 530 | Commit decision management |
| `subscriber.rs` | 470 | Block subscription |
| `round_tracker.rs` | 430 | Round advancement tracking |
| `round_prober.rs` | 410 | Peer round probing |
| `recovery_barrier.rs` | 400 | Recovery synchronization barrier |
| `dag_state/write.rs` | 625 | DAG state write operations |
| `dag_state/read.rs` | 450 | DAG state read operations |
| `core_thread.rs` | 550 | Core consensus thread |
| `commit_vote_monitor.rs` | 303 | **Digest vote tracking** — commit hash quorum verification |
| `commit_consumer.rs` | 175 | Commit consumption interface |
| `system_transaction_provider.rs` | 650 | System TX (epoch change) provider |
| `adaptive_delay.rs` | 200 | Adaptive round delay |
| `leader_scoring.rs` | 340 | Leader reputation scoring |
| `leader_timeout.rs` | 290 | Leader timeout handling |
| `stake_aggregator.rs` | 140 | Stake aggregation |
| `threshold_clock.rs` | 210 | Threshold clock |
| `context.rs` | 170 | Consensus context |
| `error.rs` | 200 | Error types |
| `storage/rocksdb_store.rs` | 450 | RocksDB persistent store |
| `storage/mem_store.rs` | 220 | In-memory store (testing) |

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
| CommitSyncer | `meta-consensus/core/src/commit_syncer.rs` | Sync failure / stale data |
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
