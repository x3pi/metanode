# 🗺️ Metanode Project Structure
> **Last updated:** 2026-05-17
> **Rule:** This file MUST be updated whenever a new module, package, or significant file is added/removed/renamed.

---

## 📐 High-Level Architecture

```
metanode/
├── execution/          ← Go execution engine (EVM-compatible layer)
└── consensus/          ← Rust consensus engine (BFT/DAG-based)
    └── metanode/       ← Main Rust consensus node
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
│  │  pkg/              ← Shared packages     │   │
│  │  ├── blockchain/   ← Block commit/state  │   │
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
│  │  ├── proto/        ← gRPC protobuf defs  │   │
│  │  ├── models/       ← Shared data models  │   │
│  │  ├── config/       ← Node configuration  │   │
│  │  └── metrics/      ← Prometheus metrics  │   │
│  └──────────────────────────────────────────┘   │
└──────────────────┬──────────────────────────────┘
                   │ FFI (C ABI via nomt_ffi)
                   │ gRPC (consensus sync)
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
│  │  │   ├── consensus_node.rs ← ORCHESTRATOR│   │
│  │  │   ├── epoch_monitor.rs                │   │
│  │  │   ├── epoch_transition_manager.rs     │   │
│  │  │   ├── committee.rs + committee_src.rs │   │
│  │  │   ├── sync.rs + sync_controller.rs    │   │
│  │  │   ├── recovery.rs                     │   │
│  │  │   ├── executor_client/ ← →Go gRPC/FFI │   │
│  │  │   │   ├── mod.rs (main)               │   │
│  │  │   │   ├── block_sending.rs (46KB)      │   │
│  │  │   │   ├── rpc_queries.rs              │   │
│  │  │   │   ├── rpc_queries_epoch.rs        │   │
│  │  │   │   ├── connection_pool.rs          │   │
│  │  │   │   ├── persistence.rs              │   │
│  │  │   │   ├── block_sync.rs               │   │
│  │  │   │   ├── socket_stream.rs            │   │
│  │  │   │   ├── traits.rs                   │   │
│  │  │   │   └── transition_handoff.rs       │   │
│  │  │   ├── rust_sync_node/ ← sync-only mode│   │
│  │  │   │   ├── sync_loop.rs (39KB)         │   │
│  │  │   │   ├── fetch.rs (33KB)             │   │
│  │  │   │   ├── epoch_recovery.rs           │   │
│  │  │   │   └── block_queue.rs              │   │
│  │  │   └── transition/   ← Mode transitions│   │
│  │  │       ├── epoch_transition.rs (27KB)  │   │
│  │  │       ├── mode_transition.rs (20KB)   │   │
│  │  │       ├── consensus_setup.rs          │   │
│  │  │       ├── demotion.rs                 │   │
│  │  │       ├── tx_recovery.rs              │   │
│  │  │       └── verification.rs             │   │
│  │  ├── network/                            │   │
│  │  │   ├── rpc.rs (29KB)                   │   │
│  │  │   ├── tx_socket_server.rs             │   │
│  │  │   ├── peer_discovery.rs               │   │
│  │  │   └── peer_rpc/                       │   │
│  │  │       ├── server.rs (33KB)            │   │
│  │  │       ├── client.rs (19KB)            │   │
│  │  │       └── types.rs                    │   │
│  │  └── types/                              │   │
│  │      ├── transaction.rs                  │   │
│  │      └── tx_hash.rs                      │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```


---

## 📦 Go Execution Engine — Key Modules

### `cmd/simple_chain/` — Main Node Process
| File | Role |
|------|------|
| `main.go` | CLI entrypoint, node startup |
| `app.go` | Application bootstrap, service wiring |
| `backend.go` | Chain backend (EVM state, DB) |
| `app_blockchain.go` | Blockchain app logic |
| `app_network.go` | Network app logic |
| `mtn_api.go` | MTN-specific JSON-RPC API |
| `rpc_block.go` | Block-related RPC handlers |
| `rpc_transaction.go` | Tx-related RPC handlers |
| `rpc_state.go` | State RPC handlers |
| `tx_async_queue.go` | Async tx submission queue |

### `cmd/simple_chain/processor/` — Core Block Processing
| File | Role |
|------|------|
| `block_processor_core.go` | Main block processor loop |
| `block_processor_sync.go` | **Peer sync / state recovery** ⚠️ |
| `block_processor_commit.go` | Block commit pipeline |
| `block_processor_processing.go` | Tx execution pipeline |
| `block_processor_network.go` | Network message handling |
| `block_processor_batch.go` | Batch tx processing |
| `block_processor_attestation.go` | BLS attestation logic |
| `block_processor_epoch.go` | Epoch transition handling |
| `block_processor_state.go` | State root verification |
| `tx_batch_forwarder_core.go` | Tx batch → consensus forwarding |
| `tx_validator_pool_core.go` | Tx validation pool |
| `tx_virtual_executor_core.go` | Virtual/offchain tx execution |
| `transaction_processor.go` | Core tx processing |
| `transaction_virtual_processor.go` | Virtual tx processing |
| `state_processor.go` | State transition processor |
| `vote_recovery.go` | Vote/quorum recovery |

### `pkg/` — Shared Packages (Critical Ones)
| Package | Role | Concurrency Risk |
|---------|------|-----------------|
| `blockchain/` | Block state commit, `block_state_commit.go` | 🔴 HIGH — state root write |
| `sync/` | Peer sync, anti-entropy | 🔴 HIGH — distributed state |
| `nomt_ffi/` | FFI bridge to Rust NOMT trie | 🟡 MED — C boundary |
| `trie/` | Merkle trie operations | 🟡 MED — shared read |
| `trie_database/` | Trie persistence layer | 🟡 MED — DB write |
| `mapping_db/` | Slot→trie key mapping | 🟡 MED — DB write |
| `state/` | Account state transitions | 🔴 HIGH — EVM state |
| `state_db/` | State database layer | 🟡 MED — DB |
| `transaction_pool/` | Mempool management | 🟡 MED — concurrent access |
| `network/` | P2P connection mgmt | 🟡 MED — async I/O |
| `mining/` | Block production | 🔴 HIGH — timing sensitive |
| `poh/` | Proof of History | 🟡 MED — clock sensitive |
| `snapshot/` | State snapshot/restore | 🟡 MED — large I/O |
| `mvm/` | Meta VM execution | 🔴 HIGH — deterministic |
| `proto/` | gRPC proto definitions | 🟢 LOW |

---

## 🦀 Rust Consensus Engine — Full Module Map

### Root: `consensus/metanode/src/`
| File | Role |
|------|------|
| `main.rs` | Binary entrypoint, runtime init |
| `ffi.rs` | **C-ABI exports callable from Go** via `nomt_ffi/` — state commits, trie updates, root queries |
| `config.rs` | Node configuration parsing |
| `lib.rs` | Library root |

### `src/consensus/commit_processor/` — BFT Commit Engine ⚠️ CRITICAL
| File | Size | Role | Risk |
|------|------|------|------|
| `processor.rs` | **89KB** | **Main ordered commit loop** — drives all execution | 🔴 CRITICAL |
| `executor.rs` | 18KB | Calls Go FFI to execute committed blocks | 🔴 HIGH |
| `gei_validator.rs` | 14KB | Validates GEI (Go Execution Interface) responses | 🔴 HIGH |
| `epoch.rs` | 3KB | Epoch boundary detection within commit loop | 🔴 HIGH |
| `lag_monitor.rs` | 5KB | Commit lag monitoring / backpressure | 🟡 MED |
| `wal.rs` | 4KB | Write-ahead log for crash recovery | 🟡 MED |

### `src/consensus/` — Epoch & State Management
| File | Role | Risk |
|------|------|------|
| `epoch_transition.rs` | Epoch boundary trigger + tx drainage | 🔴 HIGH |
| `tx_recycler.rs` | Recycles uncommitted txs post-epoch | 🟡 MED |
| `checkpoint.rs` | Checkpoint save/restore | 🟡 MED |
| `clock_sync.rs` | BFT clock synchronization | 🟡 MED |
| `commit_callbacks.rs` | **Rust→Go** commit notifications | 🔴 HIGH |
| `state_attestation.rs` | State root attestation pre-commit | 🔴 HIGH |

### `src/node/` — Node Orchestration ⚠️ LARGEST MODULE (28+ files)
| File | Size | Role | Risk |
|------|------|------|------|
| `consensus_node.rs` | **206KB** | **Central node orchestrator** — all lifecycle logic | 🔴 CRITICAL |
| `epoch_monitor.rs` | 32KB | Epoch health monitoring + alerts | 🔴 HIGH |
| `epoch_transition_manager.rs` | 18KB | Full epoch handoff sequencing | 🔴 HIGH |
| `epoch_checkpoint.rs` | 10KB | Epoch state persistence at boundaries | 🔴 HIGH |
| `epoch_store.rs` | 7KB | Epoch metadata storage | 🟡 MED |
| `committee.rs` | 10KB | Validator committee management | 🔴 HIGH |
| `committee_source.rs` | 24KB | Committee selection logic | 🔴 HIGH |
| `node_methods.rs` | 20KB | Node API implementation | 🟡 MED |
| `startup.rs` | 13KB | Boot sequence | 🟡 MED |
| `sync.rs` | 8KB | Sync state machine | 🔴 HIGH |
| `sync_controller.rs` | 10KB | Sync session controller | 🔴 HIGH |
| `sync_metrics.rs` | 9KB | Sync performance metrics | 🟢 LOW |
| `recovery.rs` | 7KB | Crash/fork recovery | 🔴 HIGH |
| `rpc_circuit_breaker.rs` | 14KB | Circuit breaker for Go RPC | 🟡 MED |
| `peer_go_client.rs` | 11KB | RPC client to Go execution layer | 🔴 HIGH |
| `peer_health.rs` | 4KB | Peer liveness monitoring | 🟡 MED |
| `health_check.rs` | 8KB | Node health endpoint | 🟢 LOW |
| `queue.rs` | 8KB | Internal task queue | 🟡 MED |
| `coordinator.rs` | 3KB | Cross-module coordinator | 🟡 MED |
| `block_delivery.rs` | 3KB | Block delivery to consumers | 🟡 MED |
| `notification_server.rs` | 5KB | Push notification server | 🟢 LOW |
| `tx_submitter.rs` | 5KB | Submit txs to consensus | 🟡 MED |

### `src/node/executor_client/` — Go Execution Client ⚠️ FFI/RPC BOUNDARY
| File | Size | Role |
|------|------|------|
| `mod.rs` | 28KB | Main client logic — call routing to Go |
| `block_sending.rs` | **46KB** | Send committed blocks to Go execution layer |
| `block_store.rs` | 4KB | Local block cache |
| `block_sync.rs` | 5KB | Block sync coordination with Go |
| `rpc_queries.rs` | 21KB | Query Go execution state via RPC |
| `rpc_queries_epoch.rs` | 14KB | Epoch-specific RPC queries |
| `connection_pool.rs` | 8KB | Connection pool to Go execution |
| `persistence.rs` | 16KB | Persist execution results |
| `socket_stream.rs` | 9KB | Socket stream handling |
| `traits.rs` | 7KB | Abstract executor traits |
| `transition_handoff.rs` | 10KB | Epoch transition handoff to Go |

### `src/node/rust_sync_node/` — Sync-Only Node Mode
| File | Size | Role |
|------|------|------|
| `sync_loop.rs` | **39KB** | Main sync loop — drives block catch-up |
| `fetch.rs` | **33KB** | Block fetch logic from peers |
| `epoch_recovery.rs` | 15KB | Epoch crash recovery during sync |
| `block_queue.rs` | 15KB | Incoming block queue |
| `start.rs` | 4KB | Sync node startup sequence |

### `src/node/transition/` — Mode Transition Logic
| File | Size | Role |
|------|------|------|
| `epoch_transition.rs` | **27KB** | Full epoch transition orchestration |
| `mode_transition.rs` | **20KB** | Node mode changes (validator ↔ observer) |
| `consensus_setup.rs` | 13KB | Consensus re-setup post-transition |
| `demotion.rs` | 10KB | Node demotion logic |
| `tx_recovery.rs` | 8KB | Tx recovery during transition |
| `verification.rs` | 8KB | Post-transition state verification |

### `src/network/` — P2P Consensus Networking
| File | Size | Role | Risk |
|------|------|------|------|
| `rpc.rs` | 29KB | Main RPC server | 🔴 HIGH |
| `tx_socket_server.rs` | 14KB | Tx reception socket | 🟡 MED |
| `peer_discovery.rs` | 14KB | Peer discovery | 🟡 MED |
| `codec.rs` | 1KB | Message encoding | 🟢 LOW |
| `peer_rpc/server.rs` | **33KB** | Peer RPC server | 🔴 HIGH |
| `peer_rpc/client.rs` | 19KB | Peer RPC client | 🔴 HIGH |
| `peer_rpc/types.rs` | 3KB | RPC types | 🟢 LOW |

### `src/types/`
| File | Role |
|------|------|
| `transaction.rs` | Core Tx type |
| `tx_hash.rs` | Tx hash utilities |

---

## 🔗 Cross-Layer Communication

| Channel | Direction | Protocol |
|---------|-----------|----------|
| Block commit notification | Rust → Go | FFI callback (`commit_callbacks.rs`) |
| Tx batch forwarding | Go → Rust | gRPC / shared memory |
| State root verification | Go ↔ Rust | FFI (`nomt_ffi/`) |
| Peer sync (block data) | Go ↔ Go | QUIC + custom P2P |
| Consensus votes | Rust ↔ Rust | P2P libp2p |

---

## ⚠️ High-Risk Change Zones

> These areas have the highest blast radius. Always grep callers before modifying.

| Zone | Location | Risk |
|------|----------|------|
| State root commit | `pkg/blockchain/block_state_commit.go` | Fork risk |
| Peer sync handler | `processor/block_processor_sync.go` | State divergence |
| FFI boundary | `pkg/nomt_ffi/` + `src/ffi.rs` | Crash / memory safety |
| Epoch transition | `processor/block_processor_epoch.go` + `src/consensus/epoch_transition.rs` | Data loss |
| Commit processor | `src/consensus/commit_processor/` | Ordering violation |
| Tx batch forwarder | `processor/tx_batch_forwarder_core.go` | Tx loss |
| Mining/PoH | `pkg/mining/` + `pkg/poh/` | Timing regression |

---

## 📝 Update Protocol

When to update this file:
- ✅ New package/module added to `pkg/` or `src/`
- ✅ New entrypoint or command added to `cmd/`
- ✅ FFI interface changed
- ✅ gRPC proto definitions changed
- ✅ Cross-layer communication channel added/removed
- ✅ File renamed or moved
- ❌ Internal implementation changes (no structural change)


