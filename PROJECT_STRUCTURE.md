# 🗺️ Metanode Project Structure
> **Last updated:** 2026-10-07 (PR #161: removed `deploy/ansible_private_chains/` and the Parent Chain orchestration from `ansible_deploy.sh`; cluster deploys are systemd-only via `deploy/ansible_clusters/`; consensus log levels demoted except quorum-bypass audit and equivocation warnings).
> **Rule:** This file MUST be updated whenever a new module, package, or significant file is added/removed/renamed.
---

## 📐 High-Level Architecture

```
metanode/
├── ci.sh                   ← Convenient project-root symlink to deploy/ci/ci_watcher.sh
├── deploy/                 ← Deployment configurations and scripts parent folder
│   ├── cluster/
│   │   └── local_parent_chain/ ← 4-node Parent Chain cluster setup (run.sh, toml configs, test_cluster.go)
│   ├── ci/                 ← Automated CI/CD Testing Daemon, Git Watcher & Telegram Alerts
│   │   ├── ci_config.yaml  ← Test matrix configuration (Block-STM, Cross-Chain, Spam, TPS)
│   │   ├── ci_watcher.sh   ← Remote Git commit listener daemon
│   │   ├── ci_runner.py    ← Config-driven test runner with pre-actions & timeout control
│   │   ├── telegram_notify.py ← Telegram notification module
│   │   └── README.md       ← Comprehensive operations & test guide
│   ├── ansible/            ← Ansible deployment scripts for Public Chain (Root Anchor)
│   │   ├── inventory.chain2.yml ← Local ignored profile for isolated second Chain ID 991 network (nodes 5-8, snapshot disabled)
│   │   ├── parse_inventory.py ← Public endpoint export preserves cluster entries; public-only JSON feeds block hash monitor
│   │   ├── roles/          ← Modular Ansible roles (node_setup, local_build, systemd_services, start_services, stop_services, restart_services, snapshot_restore, firewall)
│   │   ├── scripts/manage_snapshot_storage.py ← Managed BTRFS grow/recreate, shared-node checks and size verification
│   │   ├── scripts/test_manage_snapshot_storage.py ← Non-destructive storage command tests
│   │   ├── monitors/       ← Decoupled health, consensus vote, and block hash monitors
│   │   │   ├── vote_monitor/        ← Real-time BFT consensus vote & quorum audit monitor
│   │   │   ├── block_hash_checker/  ← Real-time multi-node block hash synchronization monitor
│   │   │   └── start_monitors.sh    ← Background daemon manager for all monitors
│   │   └── stop_all.sh     ← Script to stop all background deployment processes
│   ├── ansible_clusters/   ← Ansible automation for Parent Chain & Sharded Execution Clusters with Telegram alerts
│   │   └── scripts/parse_inventory.py ← Merges public + cluster endpoints into /tmp/rpc_nodes.json; roles/testing reads root_anchor + private_chains URLs
│   └── systemd/            ← Systemd deployment scripts, key generators (gen_validator_entry.py, gen_private_chain.py), and env templates
├── execution/          ← Go execution engine (EVM-compatible layer)
│   └── debug_nil/      ← Go standalone tests for nil/slice panic debugging
├── consensus/          ← Rust consensus engine (BFT/DAG-based)
│   └── metanode/       ← Main Rust consensus node
│       ├── src/        ← Node-level consensus code
│       └── meta-consensus/  ← BFT core engine (DAG, committer, syncer)
│           ├── core/   ← Core consensus algorithm implementation
│           ├── config/ ← Consensus configuration types
│           └── types/  ← Shared consensus types
├── crates/             ← Shared Rust crates (crypto, metrics, storage, macros)
├── dashboard/          ← Real-time telemetry & explorer web app
├── portal/             ← Web3 Portal: Parent Chain Account Gate Onboarding, Rollup transfers, and zero-fork telemetry
├── docs/               ← Docusaurus-based web documentation site
├── note/               ← Architecture documentation & known bugs (relocated from /docs)
├── scripts/            ← Operational scripts
├── DATABASE_STRUCTURE.md ← Database directory structure and requirements based on node roles
└── OPERATIONS_GUIDE.md  ← Complete End-to-End deployment & Day-2 Operations Runbook
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
│  │  ├── rollup/       ← Pure Rollup FSM (cross-node state machine) │   │
│  │  ├── failpoint/    ← Zero-overhead crash injection harness (c0spike) │   │
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
| `c0_spike.go` | ~1300 | C0 spike harness (`//go:build c0spike`): multi-round determinism, EVM+Gateway workload, per-store durability failpoints with run-scoped markers (`C0_RUN_ID`), `kill -9` recovery, measured Rust-consensus isolation |
| `c0_spike_stub.go` | 25 | Stub for production binary (`//go:build !c0spike`), exits cleanly if invoked without build tag |

*CLI Flags & Config:*
- `--tool-c0-spike=verify|worker`: Tool mode for C0 multi-process spike verification harness.
- `-c0-data-dir`, `-c0-out`, `-c0-blocks`, `-c0-rounds` (>= 2), `-c0-restart`, `-c0-report`: Parameters for C0 spike execution (`-c0-report` defaults to a temp file; the spike never writes into the repo; a failed `verify` keeps its temp data directory). Workers read `C0_LARGE_BLOCK`, `C0_RANDOMIZE_TX` and `C0_TX_SHUFFLE_SEED` from the environment.
- `pkg/blockchain/tx_processor/gateway_harness_hooks.go` (no-op) / `gateway_handler_c0spike.go` (`//go:build c0spike`): test-harness hooks for the Gateway handler; the production build is unchanged.
- `consensus_mode`: "raft" (Rollup Raft ingestion queue mode) or empty (default Rust BFT consensus).

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
| `transaction_processor.go` | Core tx processing (SendRawTransaction/SendRawTransactions, EIP-2718 ingress) |
| `raw_eth_ingress_test.go` | EIP-2718 TCP ingress & mode gating tests |
| `transaction_virtual_processor.go` | Virtual tx processing |
| `state_processor.go` | State transition processor |
| `vote_recovery.go` | Vote/quorum recovery |

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
| `cross_chain/` | Cross-chain types, Root Anchor ledger, GatewayEngine (per-action self-signed cert model; GovernanceEngine propose/vote/execute removed 2026-09-04, RecoveryCommittee + DeclareChainDeadWithCert + UpdateCommitteeWithRecoveryCert removed 2026-09-24 — `UnregisterChainWithCert` is now self-authorized by the leaving chain's own committee with an `UnregisterNonce` replay guard, and `DeadChains` is set only by `SlashOnEquivocation`), AssetRegistryEngine, Ceremony, Root Anchor RPC client, Relayer reference engine, and `relayer_daemon/` automated service (Milestones A-I) | 🟢 LOW |
| `blockchain/tx_processor/` | Transaction processor, VM dispatch, `GatewayHandler` native bridge contract dispatcher, `CommitteeAttestationWorker`, `CommitAttestationWorker`, signature enforcement and admission gate (`VerifyTransaction`, `checkTxSignature`, `verifySignatures`) | 🔴 HIGH — EVM state |
| `rollup/` | Pure deterministic Rollup State Machine (Phase B1: `EventRPCSubmitted` shared by both roles gives every RPC call a submit-then-poll-confirm step — "accepted into the queue" is never treated as "applied"), `Store` with indexed `activeIDs` for backpressure + a per-cluster `FloatSeq` counter (Phase B2), `CrossNodeHandler` (user-facing cross-node tx entrypoint) + background `SendWorker`/`ReceiveWorker`/`ReclaimWorker` driving transfers against `pkg/parentchain` over its real HTTP RPC (Phase B4-B8), including the compensating refund Transfer's own confirmation (`ReceiveWorker.processRefundSent`), gated on Raft leadership via `raftfeed.GetNode().IsLeader()`. Standardized all rollup system event payloads, inner envelopes, and committee co-attestations to 100% deterministic Protobuf binary format (`rollup.proto`, `MarshalRollupSystemPayload`, `MarshalRollupSystemAttestedPayload`, `AccountRegistrationPayload.MarshalProto`), completely removing legacy JSON wire formatting and JSON fallbacks. Includes `AccountRegistryHandler` (with $f+1$ committee BLS co-attestation verification on `ACCT_REG_ATTEST_V1` digest), `RegistrationRelay` (durable registration queue with `RegistrationRelayStore` and `KVStore` recovering PENDING requests across restarts), `RegistrationWorker` for polling Parent Chain with $f+1$ QuorumClient and proposing `account_registered` barrier events with stateDB-gated cursor, and `RollupSystemHandler` with $f+1$ committee BLS co-attestation envelope `{inner, attestations}` for cross-chain credit/lock/refund events with tombstone idempotency preventing double-credit. | 🟢 LOW — pure logic, isolated workers |
| `parentchain/` | Parent Chain native state module (ChainID: `990`, Float Account, Chain Registry, Account Registry, Claimed Messages) for Raft Sequencers — `MemoryStore` (tests) and `DBStore` (LevelDB-backed, production, with a per-destination inbound-transfer index). Standardized consensus data (wire formats, Header, Receipt, BlockRecord, BlockProgress, CallData, and NOMT state values) to Protobuf (`parent_chain.proto`) with deterministic serialization. Includes `QuorumClient` (verifies Byzantine quorum $\ge f+1$ and `nomt_ffi.VerifyProof`). The legacy JSON transaction path is DELETED (no `ParentChainTx`, no `POST /tx`, no unsigned/alternate-digest modes): every state change is a `pb.Transaction` signed (BLS) over its hash with a sequential nonce, submitted as raw proto bytes to `/send_raw_transaction`; `GET /tx?hash=` is lookup only, `GET /nonce?address=` returns the committed sender nonce. `DepositToFloat` always needs a registered source cluster's certificate (no zero-source bypass); who may register as a cluster is set by the genesis (`cluster_policy.go`: `open_cluster_registration` for devnet, `clusters` allow-list for production). Float model: genesis `float_accounts` applied in block 1 (`TreeBlockCommitter.SetGenesisInit`), `METHOD_TRANSFER_BALANCE` for plain account transfers, balance-based cluster registration and `authorized` certifier flag (`cluster_policy.go`, `state.go`); live verification tool `cmd/tool/test_account_model`. Admission filter on `/send_raw_transaction` (unsigned/forged/unknown-sender/stale-nonce txs are rejected with HTTP 400 before reaching consensus) and `GET /float?pubkey=` read RPC. Integrated Prometheus `/metrics` (`parent_chain_last_block`, `parent_chain_state_root`, `parent_chain_fork_detected`, `parent_chain_txs_total`, `parent_chain_blocks_total`). Eliminated `LastDeviceKey` BLS smuggling in favor of `AccountRegistry` lookups. | 🟢 LOW — pure state & client logic |
| `cmd/parent_chain/` | Parent Chain node binary: wires `pkg/parentchain.DBStore` + `http_rpc.HTTPServer` + Rust consensus-core FFI bridge together. `BlockProcessor.processBlock` decodes and executes transactions deterministically sorted by `(FromAddress ASC, Nonce ASC, TxHash ASC)`; `tx_batcher.go` batches submitted txs to Rust consensus core. Enforced mandatory `-genesis` flag without hard-coded validator fallbacks. Local devnet requires data wipe (`run.sh clean`) between format upgrades due to Protobuf migration. Real-time cluster health and parity monitor in `cmd/tool/parent_chain_monitor`. Live end-to-end full lifecycle verification in `cmd/tool/test_live_e2e`. Genesis float account generator `cmd/tool/gen_float_accounts` for aligning execution alloc with Parent Chain genesis. Automated chaos burn-in test `execution/scripts/chaos_burnin.sh`. Automated multi-node fault tolerance test suite in `cmd/tool/test_cluster_fault_tolerance` verifying T-I1..T-I8 with full report in `note/parent_chain_cluster_test_report.md`. Operational runbook documented in `note/runbook_parent_chain_multinode.md`. | 🟡 MED — cluster-verified, standalone & multi-node BFT |
| `cmd/tool/security_test_suite/` | Comprehensive Security Test Suite for Metanode Core: automated attack verification across 4 core vectors (RPC Ingress fuzzing & recursion/DOS bombs SEC-RPC-001..010; Cryptographic validation, tamper detection & replay attacks SEC-CRYPTO-001..008; Smart contract & EVM resource exhaustion SEC-EVM-001..005; Multi-node zero-fork determinism & height divergence SEC-NODE-001..004) with audit reports exported to `note/security_test_suite_report.md`. Accompanying unit test in `pkg/blockchain/tx_processor/security_unit_test.go`. | 🟢 LOW — standalone testing & audit tool |
| `cmd/tool/e2e_cross_chain_coattest/` | E2E cross-chain credit co-attestation test runner (P0-2): automates 6 mandatory validation scenarios (A..F, 26 steps total) against a $\ge 4$-validator Mysticeti destination cluster + Raft source cluster `exec2` on isolated ports 31xxx. Verifies conservation of value, tolerance with 1 node offline, quarantine with 2 nodes offline (zero-fork invariant), Byzantine fake credit event rejection ($f+1$ enforcement), mid-traffic `kill -9` crash recovery with exhaustive block-by-block state parity audit, and stale-state retried dispatch idempotency. | 🟢 LOW — standalone testing & verification tool |
| `rollup/raftfeed/` | `consensus_mode="raft"` switch (`Enabled()`), `ValidateConfig`, and the block source. **C1** (no `raft{}` config block): single-node `Feeder`. **C2** (`raft{}` block present): replicated `hashicorp/raft` cluster — `node.go` (bolt log/stable stores with fsync, TCP transport, bounded propose queue, re-route on lost leadership), `fsm.go` (`Apply` builds the same `ExecutableBlock` on every replica; `Snapshot` waits for DB durability before Raft may compact; `Restore` fail-closed), `forward.go` (follower→leader HTTP + HMAC), `attest.go` (periodic cross-replica block-hash check, fail-closed on majority mismatch), `admin.go`/`adminclient.go`/`membership.go` (C4: status, leader transfer, add/remove replica over the internal HMAC channel, dynamic membership via `forward_port_offset`), `state_transfer.go` + `processor/raft_state_source.go` (consistent snapshot of a RUNNING node served to a new replica: paused-execution atomic checkpoints, sparse-aware verified transfer), operator tool `cmd/tool/rollup_cluster` (`check`, `transfer-leader`, `add-replica`, `remove-replica`, `prepare-replica`, `fetch-state`, `hold-snapshots`), `stamper.go` (shared block stamping), `proto/batch.proto` → `pb/` (`BatchRecord`, `FsmSnapshotMeta`). `Submit` acknowledges only COMMITTED batches; `attest_fault.go` (tag `rollup_faults`, test binaries only) injects a wrong hash for the live divergence test. Config: `pkg/config/raft_config.go`. Started by the raft branch of `block_processor_network.go`; `tx_batch_forwarder` sends batches here (`batchSubmitter`); default-off guards on Rust-consensus entry points | 🟡 MED — guards on RPC/forwarder paths; only active when `consensus_mode="raft"`; adds direct deps `hashicorp/raft`, `raft-boltdb/v2` |
| `keyvault/` + `config/secrets.go` | Secrets of `config.json` may be `enc:v1:…` envelopes (scrypt + AES-256-GCM); decrypted once at startup with a password from `META_KEY_PASSWORD` or a 0600 file; wrong/missing password or `require_encrypted_keys` with a clear-text secret ⇒ node refuses to start. Encrypt with `cmd/tool/encrypt_secret` | 🟢 LOW — plain configs load exactly as before |
| `cmd/tool/test_raw_eth_tcp_live/` | Live cluster end-to-end test suite for Native Ethereum EIP-2718 / EIP-1559 TCP Ingress: connects to actual live cluster over TCP (`SendRawTransaction` and `SendRawTransactions`), executes single and batch EIP-1559 transactions, verifies `TransactionSuccess` with `ethTx.Hash()`, verifies consensus block execution, queries receipts by `ethTx.Hash()` over HTTP JSON-RPC (`eth_getTransactionReceipt`), verifies on-chain balances, and validates rejection security rules (pre-EIP-155, chain ID mismatch, malleable signature `s > N/2`). | 🟢 LOW — standalone testing & verification tool |
| `cmd/tool/secp_tps_blast/` | High-throughput benchmark tool for pure Secp256k1 / EIP-1559 transactions over TCP (`SendRawTransactions`): generates concurrent transactions signed with genesis private keys, injects high-rate batches into cluster nodes, measures effective TPS, end-to-end latency percentiles (P50/P90/P95/P99), peak CPU/RSS resource utilization, and performs multi-node zero-fork consistency audits (verifying identical block hashes and state roots across all replicas). Supports sustained duration blasting with rate limiting (`-duration`, `-rate-limit`). Integrated into `deploy/ci/ci_config.yaml` (`secp_tps`). | 🟢 LOW — standalone benchmark & CI verification tool |
| `scripts/test/test_crash_recovery_nomt.sh` | Chaos crash recovery test suite for NOMT fsync & commitWg durability under continuous EIP-1559 load: executes $\ge 10$ rounds of random mid-traffic `kill -9` against replicas while thousands of transactions are committing, audits recovery restart, verifies no exit 78 ([INTEGRITY]), no `/tmp/MTN_INTEGRITY_FAILED` sentinel, and audits zero-fork state root & block hash parity across all cluster nodes. | 🟢 LOW — test & audit script |

---

## 🦀 Shared Rust Crates (`crates/`)

| Crate | Role |
|-------|------|
| `meta-protocol-config` | Protocol version configuration |
| `meta-protocol-config-macros` | Procedural macros for protocol config |
| `meta-macros` | Shared macro utilities |
| `meta-proc-macros` | Procedural macros |
| `meta-http` | Shared HTTP client/server utilities |
| `meta-tls` | TLS configuration |
| `meta-enum-compat-util` | Compatibility utilities for enums |
| `mysten-common` | Common utilities (origin: Sui/Mysten Labs) |
| `mysten-metrics` | Prometheus metrics integration |
| `mysten-network` | Network types (origin: Sui/Mysten Labs) |
| `shared-crypto` | Cryptographic primitives (BLS12-381/Ed25519) |
| `typed-store` | Type-safe RocksDB wrapper |
| `typed-store-derive` | Derive macros for typed-store |
| `typed-store-error` | Error types for typed-store |
| `typed-store-workspace-hack` | Cargo workspace hack utility crate |
| `telemetry-subscribers` | Tracing/telemetry subscribers |
| `prometheus-closure-metric` | Prometheus closure helper metrics library |
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
| `cross_chain.rs` | Root Anchor & Cross-Chain schema types |
| `governance.rs` | On-chain governance lifecycle & 72h timelock |
| `pop.rs` | BLS12-381 Proof-of-Possession & rogue key guard |
| `root_anchor.rs` | Root Anchor founding committee & BFT stake quorum |
| `gateway.rs` | GatewayPrecompile & cross-chain execution state machine |
| `epoch_sync.rs` | Epoch transition committee sync & account-level state root checkpoints |


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
