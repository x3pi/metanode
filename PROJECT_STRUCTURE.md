# 🗺️ Metanode Project Structure

> Canonical module map. Describes the **current** layout only; history lives in git and `note/`.
> **Rule:** update this file when a package, entrypoint, FFI/proto interface or cross-layer channel is added, removed or renamed.

---

## 1. System Topology

```
                 ┌──────────────────────────────────────────────┐
                 │ Parent Chain (cmd/parent_chain)              │
                 │ Rust BFT + HTTP REST · float accounts ·      │
                 │ chain/account registry · f+1 BLS co-attest   │
                 └───────▲───────────────────────────▲──────────┘
        batch anchor /   │                           │  account gate /
        credit attest    │                           │  credit attest
        ┌────────────────┴─────────┐       ┌─────────┴────────────────┐
        │ Execution cluster        │ relay │ Execution cluster        │
        │ consensus_mode = BFT     │◄─────►│ consensus_mode = "raft"  │
        │ Mysticeti validators     │       │ raftfeed sequencers      │
        └──────────────────────────┘       └──────────────────────────┘
        each box = N × the single-process node described in §2
```

- Parent chain ID comes from genesis (default `991`).
- BFT tolerance: `f = ⌊(n-1)/3⌋`; a cluster needs n ≥ 4 to survive one fault (`note/bft_fault_tolerance_node_count.md`).

## 2. Node Layering (one process: `execution/cmd/simple_chain`)

Go is the host process; Rust consensus is linked as `libmetanode` (CGo). Both directions are in-process FFI.

```
 Clients ── JSON-RPC/WS (eth_*, mtn_*) ──┐     ── Protobuf TCP (batch tx / receipts) ──┐
                                         ▼                                            ▼
┌──────────────────────────── Go Execution (execution/) ───────────────────────────────────┐
│ cmd/simple_chain: main/app/backend, rpc_*, mtn_api, processor/ (block + tx pipelines)    │
│                                                                                          │
│  Ingress ─► transaction_pool ─► tx_batch_forwarder ─► FFI (raft mode: raftfeed.Submit)   │
│  Execute: grouptxns (Union-Find) ─► tx_processor (Block-STM / EVM / MVM / Gateway)       │
│  State:   account_state_db ─► trie (NOMT) ─► PebbleDB / Xapian (goxapian) / mvm (C++)    │
│  Side:    sync, snapshot, pruning, rollup, parentchain, cross_chain, keyvault, metrics   │
│  executor/ffi_bridge.go ◄── execute_block / process_rpc_request / get_state_root ──┐     │
└────────────────────────────────────────────────────┬───────────────────────────────┼─────┘
        metanode_submit_transaction_batch (Go→Rust)  │                               │ GoCallbacks (Rust→Go)
┌────────────────────────────────────────────────────▼───────────────────────────────┴─────┐
│ Rust Consensus (consensus/metanode, libmetanode)                                         │
│  ffi.rs ─► node/ (setup_storage → setup_consensus, epoch_monitor, sync, executor_client) │
│  consensus/commit_processor (ordered commit loop, digest gate, peer attestation, WAL)    │
│  liveness_watchdog.rs · setup_consensus/fork_guard.rs · epoch_transition · checkpoint    │
│  ┌──────────── meta-consensus/core (Mysticeti DAG-BFT) ───────────────┐                  │
│  │ core/proposer · block_manager · synchronizer · linearizer ·        │                  │
│  │ commit_finalizer · commit_syncer · coordination_hub ·              │                  │
│  │ commit_vote_monitor · dag_state · storage (RocksDB) · tonic network│                  │
│  └────────────────────────────────────────────────────────────────────┘                  │
└────────────────────────────────────────────────────┬─────────────────────────────────────┘
                                                     │ tonic gRPC (blocks/votes) + peer_rpc (TCP)
                                                     ▼  other validators
```

## 3. Commit Data Flow (zero-fork path)

```
tx ─► mempool ─► batch ─► Rust propose ─► DAG ─► linearizer (deterministic order)
   ─► commit_vote_monitor: 2f+1 same digest? ──no──► PENDING (never timeout-dispatch)
                   │yes (or TRUE cold-start)              └► Conflict ⇒ discard, wait CertifiedCommit
                   ▼
   commit_processor ─► execute_block (FFI) ─► Go: group ─► Block-STM/EVM ─► NOMT root ─► receipts
                   ▲                                                       │
                   └──── GEI / state-root check (gei_validator, fork_guard) ◄┘
```

Decision matrix (`PeerAttestResult`): no digest data and no votes → `Ok` (true cold start); votes present but < 2f+1 → `Insufficient` (wait); 2f+1 agree → `Ok`; 2f+1 disagree → `Conflict` (discard local). See `AGENTS.md` Part 2.5.

In `consensus_mode="raft"` Rust consensus is not started: `pkg/rollup/raftfeed` builds identical `ExecutableBlock`s on every replica and pushes them to `blockIngestionQueue`.

---

## 4. Repository Layout

```
metanode/
├── README.md, AGENTS.md, DATABASE_STRUCTURE.md, OPERATIONS_GUIDE.md, HOW_TO_TUNE_BLOCK_SIZE.md, ci.md
├── ci.sh                ← symlink to deploy/ci/ci_watcher.sh
├── consensus/           ← Rust workspace
│   ├── metanode/        ← consensus node + FFI library (src/, proto/, scripts/build_check.sh)
│   │   └── meta-consensus/{core,config,types}  ← Mysticeti DAG-BFT engine
│   └── vendor/          ← vendored `nomt`, patched `parking_lot_core`
├── crates/              ← shared Rust crates (§7)
├── execution/           ← Go module (execution engine, parent chain, rollup, tools)
│   ├── cmd/             ← simple_chain, parent_chain, exec_node, tool/*
│   ├── executor/        ← Go side of the FFI bridge + snapshot manager
│   ├── pkg/             ← packages (§5.4)
│   ├── contracts/       ← Solidity system & cross-chain contracts
│   └── debug_nil/       ← standalone nil/slice panic reproductions
├── deploy/
│   ├── ansible/         ← single cluster (4 validators + RPC), monitors/
│   ├── ansible_clusters/← parent chain + multiple execution clusters
│   ├── ansible_private_chains/  ← private-chain inventory
│   ├── ci/              ← self-hosted CI (ci_config.yaml, ci_runner.py, ci_watcher.sh)
│   ├── cluster/local_devnet/    ← local single-chain devnet scripts
│   └── systemd/         ← unit templates, genesis examples, key generators
├── dashboard/           ← cluster/cross-chain monitoring UI (Vite)
├── portal/              ← web onboarding / rollup / explorer portal (Vite)
├── docs/                ← Docusaurus site + BLOCK_PROCESSING_ARCHITECTURE.md, tee_evm_architecture.md
├── note/                ← design docs, reports, CLAIMS_LEDGER.md, evidence/
└── scripts/             ← operational scripts
```

---

## 5. Go Execution Engine

### 5.1 Entrypoints (`execution/cmd/`)
| Path | Role |
|------|------|
| `simple_chain/` | Unified node: Validator / SyncOnly / RPC / Explorer roles |
| `parent_chain/` | Parent settlement & registry chain daemon (own `processor/`, `tx_batcher.go`) |
| `exec_node/` | Standalone execution node |
| `tool/*` | Operator, benchmark and verification tools (key/genesis generators, registration, TPS blasters, parity/chaos/e2e checkers, `rollup_cluster`, `protocol_fingerprint`, `encrypt_secret`, …) |

### 5.2 `cmd/simple_chain/`
| File / dir | Role |
|------------|------|
| `main.go`, `app*.go` | CLI entry, bootstrap, service wiring |
| `backend.go`, `rpc_*.go`, `mtn_api.go`, `mtn_registration_api.go`, `admin_api.go`, `debug_api.go`, `routes/` | JSON-RPC / WebSocket surface |
| `rpc_rate_limiter.go` | Optional global + per-IP limiter (HTTP and WS, per batch item) |
| `startup_integrity_check.go` | Post-crash integrity check |
| `rollup_adapters.go` | Bridges node core to the rollup engine |
| `c0_spike.go` / `c0_spike_stub.go` | Crash/determinism harness (build tag `c0spike`) |
| `processor/` | Block and transaction pipelines (below) |

### 5.3 `cmd/simple_chain/processor/`
| Group | Files |
|-------|-------|
| Block pipeline | `block_processor_core/commit/processing/state/roots/receipt/broadcast/indexing/epoch/attestation/network/sync.go`, `speculative_executor.go`, `state_processor.go`, `gei_authority.go` |
| Tx pipeline | `transaction_processor*.go`, `tx_validator_pool_core.go`, `tx_batch_forwarder_core.go`, `tx_virtual_executor_core.go` (off-chain reads), `receipt_tracker.go`, `processors.go` (load monitor + ingress overload gate) |
| Raft | `raft_state_source.go` (consistent snapshot for new replicas) |
| Sub-packages | `pipeline/` (block trace/stats), `rpcquery/` (filters, conversion), `syncorder/` |

### 5.4 `execution/pkg/`
| Package | Role | Risk |
|---------|------|------|
| `blockchain/` | Chain state, block state commit (`block_state_commit.go`), `vm_processor/` | 🔴 state-root write |
| `blockchain/tx_processor/` | Tx execution: `block_stm.go`, `true_block_stm.go`, `native_fast_path.go`, `mvcc/`, EVM dispatch, `gateway_handler.go` / `parent_chain_gateway_handler.go`, `rollup_system_handler.go`, signature enforcement, fee floor, attestation workers | 🔴 consensus rules |
| `account_state_db/` | Account state + commit pipeline | 🔴 trie mutations |
| `state/`, `state_db/`, `smart_contract_db/`, `transaction_state_db/` | State transitions, stake DB, per-contract storage, tx-scoped state | 🔴 EVM state |
| `trie/`, `trie_database/` | Merkle trie (flat, NOMT, verkle backends) and persistence | 🟡 |
| `nomt_ffi/` | CGo bridge to Rust NOMT (`rust_lib` → `libmtn_nomt`) | 🟡 C boundary |
| `grouptxns/`, `transaction_grouper/` | Union-Find grouping of transactions | 🟡 determinism |
| `transaction/`, `transaction_pool/`, `receipt/`, `block/`, `block_validator/`, `block_signer/` | Types, mempool, receipts, blocks, validation, BLS block signing | 🟡 |
| `mvm/`, `goxapian/` | Embedded C++ Meta VM and Xapian full-text index | 🔴 deterministic / CGo |
| `sync/`, `snapshot/`, `pruning/`, `state_changelog/` | Peer sync data, backup/restore, state pruning, state history | 🔴 sync / 🟡 |
| `network/`, `quic_network/`, `connection_manager/`, `node/`, `txsender/`, `transfer/` | Protobuf TCP ingress with admission/backpressure, QUIC, P2P hosts, tx client, file transfer | 🟡 |
| `rollup/` | Deterministic rollup state machine, cross-node transfer workers, account-registry and registration relay, committee co-attestation | 🟢 isolated |
| `rollup/raftfeed/` | `consensus_mode="raft"`: replicated `hashicorp/raft` sequencer (fsm, forward, attest, membership, state transfer) | 🟡 raft mode only |
| `parentchain/` | Parent chain state, stores, HTTP RPC, `QuorumClient` | 🟢 |
| `cross_chain/` | Root Anchor, gateway, asset registry, ceremony, relayer and `relayer_daemon/` | 🟢 |
| `protocolparams/` | Fingerprint of execution-relevant settings; checked against genesis at startup; RPC `mtn_protocolFingerprint` | 🟢 |
| `keyvault/` | `enc:v1:` encrypted secrets in `config.json` | 🟢 |
| `mining/`, `poh/` | Block production service; stake-weighted leader schedule | 🔴 timing |
| `storage/`, `storagenode/`, `shard_storage/`, `blob_store/` | Storage backends, storage node, sharded block store, EIP-4844 blobs | 🟡 |
| `explorer/`, `filters/`, `stats/`, `error_tx_manager/` | Explorer index, log filters, stats, failed-tx records | 🟢 |
| `config/`, `metrics/`, `logger/`, `loggerfile/`, `tracing/`, `monitor_service/`, `fatal/`, `failpoint/` | Config, Prometheus, logging, tracing, fail-stop, crash injection | 🟢 |
| `proto/` | Protobuf definitions + generated code (+ vtproto) | 🟡 wire format |
| `bls/`, `common/`, `utils/`, `abi_file/`, `script/`, `pathdetector/`, `proxy_tx/`, `shared_memory/`, `remotestorage/`, `important_data_logger/`, `models/` | Helpers | 🟢 |

### 5.5 `execution/executor/` — Go side of the FFI bridge
| File | Role |
|------|------|
| `ffi_bridge.go` | Starts Rust consensus, registers `GoCallbacks`, exposes `SubmitTransactionBatch`, serves `execute_block` / `process_rpc_request` / `get_state_root` |
| `unix_socket_handler*.go` | `RequestHandler`: handles Rust RPC requests (epoch, block, sync, validators). File names are historical; it is reached through FFI, not a socket |
| `listener.go`, `unix_socket.go`, `socket_abstraction.go` | Legacy socket executor/listener (not part of the FFI path) |
| `snapshot_*.go` | Snapshot manager / server / verification |
| `committee_notifier.go`, `interfaces.go`, `mock_executor.go` | Epoch committee notification, interfaces, test double |

---

## 6. Rust Consensus (`consensus/metanode/`)

### 6.1 `src/` (node)
| Path | Role | Risk |
|------|------|------|
| `main.rs`, `lib.rs`, `config.rs` | Binary entry, library root, config | 🟡 |
| `ffi.rs` | C-ABI exports (`metanode_start_consensus`, `_register_callbacks`, `_submit_transaction_batch`, `_pause/_resume_consensus`, `_restore_from_snapshot`, `_attest_payload_loss*`, `_get_*_votes`, `_init_rocksdb`, …) and the `GoCallbacks` struct | 🔴 |
| `liveness_watchdog.rs` | Detects a stalled tokio scheduler from an OS thread and aborts the process (halt rather than guess) | 🟡 |
| `consensus/commit_processor/` | Ordered commit loop (`processor.rs`), FFI execution (`executor.rs`), `gei_validator.rs`, `epoch.rs`, `lag_monitor.rs`, `wal.rs` | 🔴 critical |
| `consensus/` | `epoch_transition.rs`, `tx_recycler.rs`, `checkpoint.rs`, `clock_sync.rs`, `commit_callbacks.rs`, `state_attestation.rs` | 🔴 |
| `node/` | `consensus_node.rs` (orchestrator), `setup_storage/`, `setup_consensus/` (`startup_sync`, `verification`, `fork_guard`, `state_recovery`), `epoch_monitor/`, `epoch_transition_manager.rs`, `epoch_checkpoint.rs`, `committee*.rs`, `sync*.rs`, `recovery.rs`, `catchup.rs`, `rpc_circuit_breaker.rs`, `peer_go_client.rs`, `peer_health.rs`, `tx_submitter.rs`, `block_delivery.rs`, `dual_stream.rs` | 🔴 |
| `node/executor_client/` | Rust→Go client: `block_sending.rs` (FFI `execute_block`), `rpc_queries*.rs` (FFI `process_rpc_request`), `block_store.rs`, `persistence.rs`, `transition_handoff.rs`. `connection_pool.rs` / `socket_stream.rs` are the legacy socket path (`connect()` is a no-op) | 🔴 FFI boundary |
| `node/rust_sync_node/` | Sync-only mode: fetch, block queue, epoch recovery | 🔴 |
| `node/transition/` | Validator ↔ observer mode transitions, demotion, tx recovery | 🔴 |
| `network/` | `tx_socket_server.rs` (receives Go tx batches from the FFI channel; ordered submit gate), `peer_rpc/` (epoch/sync RPC), `peer_discovery.rs`, `rpc.rs`, `codec.rs` | 🟡 |
| `types/` | `transaction`, `tx_hash`, `cross_chain`, `gateway`, `governance`, `root_anchor`, `pop`, `relayer`, `epoch_sync` | 🟢 |

### 6.2 `meta-consensus/core/src/` (Mysticeti DAG-BFT)
| Module | Role | Risk |
|--------|------|------|
| `core/` (`proposer`, `commit_manager`, `block_importer`), `core.rs`, `core_thread.rs` | Block proposal and commit decisions | 🔴 |
| `linearizer/` | DAG → deterministic linear commit order | 🔴 critical |
| `commit_syncer/` (`fetcher`, `cold_start`) | Commit sync / catch-up | 🔴 critical |
| `commit_finalizer/`, `commit_observer.rs`, `commit_consumer.rs`, `commit.rs` | Finalization and delivery of commits | 🔴 |
| `coordination_hub.rs`, `commit_vote_monitor.rs` | Peer attestation and commit-digest vote quorum | 🔴 |
| `synchronizer/`, `block_manager/`, `authority_service/`, `authority_node/`, `subscriber.rs` | Live block sync, suspended-block tracking, RPC service, node assembly | 🔴 |
| `dag_state/`, `storage/` (RocksDB, in-memory) | DAG state and persistence | 🔴 |
| `block.rs`, `block_verifier.rs`, `leader_schedule.rs`, `leader_scoring.rs`, `leader_timeout.rs`, `threshold_clock.rs`, `stake_aggregator.rs`, `round_tracker.rs`, `round_prober.rs`, `adaptive_delay.rs` | Block types/verification, leader election, round progress | 🔴 / 🟡 |
| `recovery_barrier.rs`, `epoch_change*.rs`, `reconfiguration.rs`, `system_transaction*.rs` | Recovery sync, epoch change and system transactions | 🔴 |
| `transaction.rs`, `transaction_certifier.rs`, `tx_group_filter.rs` | Tx batching/certification, Union-Find group limit check | 🟡 |
| `network/` | tonic gRPC transport | 🟡 |

`meta-consensus/config` and `meta-consensus/types` hold shared configuration and types.

---

## 7. Shared Rust Crates (`crates/`)
| Crate | Role |
|-------|------|
| `shared-crypto` | BLS12-381 / Ed25519 primitives |
| `typed-store` (+ `-derive`, `-error`, `-workspace-hack`) | Typed RocksDB wrapper |
| `nomt_db` | NOMT database helper crate |
| `metanode-keytool` | Key generation library + CLI |
| `metanode-tee-revm` | TEE EVM build helpers |
| `meta-protocol-config` (+ `-macros`), `meta-macros`, `meta-proc-macros`, `meta-enum-compat-util` | Protocol config and macros |
| `meta-http`, `meta-tls`, `mysten-network`, `mysten-common` | HTTP, TLS, network types, common utilities |
| `mysten-metrics`, `prometheus-closure-metric`, `telemetry-subscribers` | Metrics and tracing |

---

## 8. Cross-Layer Communication

| Channel | Direction | Protocol | Files |
|---------|-----------|----------|-------|
| Block commit delivery | Rust → Go | FFI callback `execute_block` → `cgo_execute_block` | `block_sending.rs` → `ffi_bridge.go` |
| Generic RPC (epoch, block, GEI, sync) | Rust → Go | FFI callback `process_rpc_request` (Protobuf) → `RequestHandler.ProcessProtobufRequest` | `rpc_queries*.rs` → `unix_socket_handler*.go` |
| State root / tx trace / log | Rust → Go | FFI callbacks `get_state_root`, `update_tx_trace`, `log_message` | `ffi.rs` |
| Tx batch forwarding | Go → Rust | C ABI `metanode_submit_transaction_batch` (`executor.SubmitTransactionBatch`; also used by `cmd/parent_chain/processor/tx_batcher.go`) | `tx_batch_forwarder_core.go` → `ffi.rs` → `tx_socket_server.rs` |
| Lifecycle / control | Go → Rust | C ABI `start_consensus`, `register_callbacks`, `pause/resume_consensus`, `restore_from_snapshot`, `attest_payload_loss*`, `get_*_votes` | `ffi_bridge.go` → `ffi.rs` |
| Trie / proofs | Go → Rust NOMT | C ABI (`libmtn_nomt`) | `pkg/nomt_ffi/` → `pkg/nomt_ffi/rust_lib` |
| Raft block source | Go internal | `hashicorp/raft` → FSM → `blockIngestionQueue` (no Rust consensus); batches go to `raftfeed.Submit` | `pkg/rollup/raftfeed/` |
| Peer sync (block data) | Go ↔ Go | QUIC + custom TCP | `pkg/network/`, `pkg/quic_network/` |
| Consensus blocks / votes | Rust ↔ Rust | tonic gRPC | `meta-consensus/core/src/network/` |
| Peer RPC (epoch boundary, sync) | Rust ↔ Rust | custom TCP | `peer_rpc/server.rs` ↔ `client.rs` |
| Commit digest attestation | Rust ↔ Rust | commit votes embedded in DAG blocks | `coordination_hub.rs` ↔ `commit_vote_monitor.rs` |
| Cross-chain settlement | Execution cluster ↔ Parent chain | HTTP REST + f+1 BLS co-attestation | `pkg/parentchain/`, `pkg/rollup/` |

Protobuf definitions: `execution/pkg/proto/*.proto`, `consensus/metanode/proto/{executor,transaction,validator_rpc}.proto`, `pkg/rollup/raftfeed/proto/`.

---

## 9. High-Risk Change Zones

Always trace callers before modifying (`codegraph_impact`, fallback `grep -rn`).

| Zone | Location | Risk |
|------|----------|------|
| State root commit | `pkg/blockchain/block_state_commit.go` | Fork |
| Account state pipeline | `pkg/account_state_db/account_state_db_commit.go` | Fork |
| Tx execution rules | `pkg/blockchain/tx_processor/` (Block-STM, fast path, fee/signature filters) | Fork — consensus rule change, deploy all validators together |
| Peer sync handler | `processor/block_processor_sync.go` | State divergence |
| FFI Go↔Rust | `pkg/nomt_ffi/`, `executor/ffi_bridge.go`, `consensus/metanode/src/ffi.rs`, `executor_client/` | Crash / memory safety / deadlock |
| Epoch transition | `processor/block_processor_epoch.go`, `src/consensus/epoch_transition.rs` | Data loss |
| Commit processor | `src/consensus/commit_processor/` | Ordering violation |
| Linearizer | `meta-consensus/core/src/linearizer/mod.rs` | Fork (non-deterministic order) |
| Commit syncer | `meta-consensus/core/src/commit_syncer/mod.rs` | Sync failure / stale data |
| Locks in consensus-core | `GLOBAL_TX_CACHE`, `dag_state` (`parking_lot`) | Runtime freeze |
| Tx batch forwarder | `processor/tx_batch_forwarder_core.go` | Tx loss |
| Mining / PoH | `pkg/mining/`, `pkg/poh/` | Timing regression |
| Snapshot manager | `executor/snapshot_manager.go` | Corruption during restore |

---

## 10. Update Protocol

Update this file when:
- a package/module is added to `pkg/` or `src/`, or a file is renamed or moved;
- an entrypoint is added to `cmd/`;
- the FFI interface, a `.proto` definition or a cross-layer channel changes;
- a crate is added to `crates/`.

Do not record implementation details, benchmark numbers, commit hashes or change history here.
