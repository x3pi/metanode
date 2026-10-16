# MetaNode Blockchain Monorepo

MetaNode is a high-performance, deterministic Layer-1 / Layer-2 blockchain platform combining a **Rust Mysticeti DAG-BFT consensus engine** (`consensus/`) and a **Go parallel EVM execution engine** (`execution/`) in a **unified single-process architecture** connected via in-memory CGo FFI (`libmetanode`, `libmtn_nomt`, `libmvm` / `libmvm_linker`, system `libxapian`).

---

## Architecture Overview

```text
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                      PARENT SETTLEMENT & REGISTRY CHAIN (ChainID from genesis)           │
│  cmd/parent_chain • Rust BFT consensus • HTTP REST API (/tx /block /receipt /status)   │
│  • Float Accounts & Credit Attestation   • Rollup & Chain Registry (f+1 BLS Quorum)   │
│  • Global Account Registration Gate (pkg/rollup/account_registry_handler.go)              │
└───────────────────▲────────────────────────────────────────────────▲───────────────────┘
                    │ Batch Anchor / Co-Attestation / Account Gate   │
        ┌───────────┴───────────────────────────┐        ┌───────────┴───────────────────┐
        │   EXECUTION CLUSTER A (BFT Mode)      │        │  EXECUTION CLUSTER B (Raft)   │
        │   • 4 Validators (Mysticeti DAG-BFT)  │        │  • 3 Sequencer Nodes (Raft)   │
        │   • Zero-Fork Peer Attestation        │        │  • Deterministic Batch Feed   │
        └───────────────────────────────────────┘        └───────────────────────────────┘

┌────────────────────────────────────────────────────────────────────────────────────────┐
│                   UNIFIED VALIDATOR / EXECUTION NODE (cmd/simple_chain)                │
│                                                                                        │
│  ┌───────────────────────────────────────┐    ┌─────────────────────────────────────┐  │
│  │   Go Execution Layer (execution/)     │    │  Rust Consensus Layer (consensus/)  │  │
│  │ • Block-STM Speculative Parallel VM   │◄──►│ • Mysticeti DAG-BFT Ordering        │  │
│  │ • Union-Find Deterministic Grouping   │FFI │ • CommitVoteMonitor (Zero-Fork)     │  │
│  │ • NOMT Merkle Trie (libmtn_nomt FFI)  │    │ • Epoch transitions                 │  │
│  │ • PebbleDB + C++ MVM + Xapian Search  │    │ • Quorum CommitSyncer & Catchup     │  │
│  │ • Dual Ingress: JSON-RPC/WS + ProtoTCP│    └─────────────────────────────────────┘  │
│  └───────────────────────────────────────┘                                             │
└────────────────────────────────────────────────────────────────────────────────────────┘
```

### Core Capabilities

1. **Unified Single-Process FFI Architecture:**
   - **Rust Consensus (`consensus/metanode`):** Orders transactions via Mysticeti DAG-based BFT and delivers committed sub-DAGs directly to Go over zero-copy CGo FFI callbacks (`libmetanode`).
   - **Zero-Fork Invariant:** Enforced via data-driven peer attestation (`CommitVoteMonitor` / `PeerAttestResult`). Nodes never use wall-clock timeouts to force commit dispatch; unverified commits remain `PENDING` until $2f+1$ peer digest quorum is reached.
   - **Dual Consensus Modes:** Execution clusters can run in **`bft` mode** (Rust Mysticeti DAG-BFT) or **`raft` mode** (`pkg/rollup/raftfeed` replicated sequencer).

2. **Parallel Execution & Authenticated State (`execution/`):**
   - **Block-STM (`pkg/blockchain/tx_processor`) & Union-Find Grouping (`pkg/grouptxns`):** Speculative parallel execution with multi-version memory and deterministic Union-Find transaction grouping.
   - **NOMT + PebbleDB State Engine (`pkg/nomt_ffi`, Rust crate `pkg/nomt_ffi/rust_lib`, vendored NOMT in `consensus/vendor/nomt`):** Nearly Optimal Merkle Trie (`libmtn_nomt`) backed by PebbleDB for authenticated state roots and cryptographic Merkle proofs (`eth_getProof`).
   - **C++ MVM & Full-Text Search (`pkg/mvm`, `pkg/goxapian` + system `libxapian`):** Embedded C++ Meta Virtual Machine and single-call CGo Xapian full-text indexing (`database_index_document`).
   - **TCP Ingress Backpressure & Bounded Heap:** Watermark admission control (`ReceiptTracker.InFlightTxCount()`, `HighWatermark = 300,000`, `LowWatermark = 150,000`) prevent OOM and GC thrashing under multi-million TX bursts.

3. **Hierarchical Parent Chain & Cross-Chain Ecosystem:**
   - **Parent Chain (`cmd/parent_chain`, `pkg/parentchain`):** Dedicated settlement and coordination chain driven by the Rust BFT consensus engine (`--rust-config`), exposing an HTTP REST API (`pkg/parentchain/http_rpc.go`). ChainID comes from genesis (default `991`, see `DefaultParentChainID`).
   - **Account Registration Gate (`AccountGateParentRegistered`):** Optional execution-cluster gate requiring users to be registered (`AccountActive`) on the Parent Chain before transacting on execution clusters(`pkg/rollup/account_registry_handler.go`, `registration_relay.go`).
   - **$f+1$ BLS Committee Co-Attestation:** Execution validators co-sign rollup state batches (`SubmitBatch`) and float credit settlements (`SubmitCreditAttestation`) verified on the Parent Chain.

4. **Dual-Stack Client Ingress:**
   - **Ethereum JSON-RPC & WebSocket (`cmd/simple_chain/rpc_*.go`):** Full support for `eth_sendRawTransaction` (Legacy, EIP-2930, EIP-1559), `eth_call`, `eth_estimateGas`, `eth_getBlockByNumber`, `eth_getTransactionReceipt`, `eth_getLogs`, `eth_getProof`, and `eth_subscribe`.
   - **High-Throughput Protobuf TCP Ingress (`pkg/network`):** Persistent multiplexed TCP socket server for high-TPS batch transaction injection and receipt streaming.

---

## Repository Layout

```text
metanode/
├── PROJECT_STRUCTURE.md         # Canonical architectural & module map (Source of Truth)
├── AGENTS.md                    # Core engineering guardrails & Zero-Fork Invariant rules
├── DATABASE_STRUCTURE.md        # On-disk storage layout (Validator vs RPC vs Explorer)
├── OPERATIONS_GUIDE.md          # Cluster deployment & Day-2 operations runbook
├── HOW_TO_TUNE_BLOCK_SIZE.md    # Throughput, DAG batch size & gas limit tuning guide
├── ci.md                        # Usage guide for the self-hosted CI system (`ci.sh`)
│
├── consensus/                   # Rust Workspace: Mysticeti DAG-BFT & NOMT FFI
│   ├── metanode/                # Main consensus node & CGo FFI library (libmetanode)
│   │   └── scripts/             # build_check.sh & local cluster orchestration scripts
│   ├── vendor/nomt/             # Vendored NOMT (Merkle Trie); FFI wrapper lives in execution/pkg/nomt_ffi/rust_lib
│   └── crates/                  # Shared crypto (BLS12-381), RocksDB typed-store, anemo P2P
│
├── execution/                   # Go Module: Execution Engine, Parent Chain & Tooling
│   ├── cmd/
│   │   ├── simple_chain/        # Unified Validator / SyncOnly / RPC / Explorer node
│   │   ├── parent_chain/        # Parent Settlement & Registry Chain daemon
│   │   └── tool/                # cross_chain_relayer, register_chains, TPS generators (tps_blast, secp_tps_blast), keygen & test tools
│   ├── executor/                # Go ↔ Rust CGo FFI bridge & LVM snapshot manager
│   ├── pkg/                     # Core packages (blockchain/tx_processor, nomt_ffi, rollup, parentchain, ...)
│   └── contracts/               # Solidity system & cross-chain contracts
│
├── deploy/                      # Ansible Deployment & CI Automation
│   ├── ansible/                 # Single 4-validator + RPC cluster deployment (ansible_deploy.sh)
│   ├── ansible_clusters/        # Multi-cluster topology: Parent chain + Execution clusters
│   └── ci/                      # Self-hosted CI runner (ci_runner.py; entrypoint ./ci.sh)
│
├── dashboard/                   # Cross-chain monitoring & cluster dashboard (Node.js)
├── portal/                      # Web3 Onboarding, Rollup & Explorer Portal (Express + Ethers v6)
├── docs/                        # Technical deep-dives (Block Processing & TEE-EVM architecture)
└── note/                        # Engineering designs, A/B benchmark reports & CLAIMS_LEDGER.md
```

---

## Building & Verification

### Prerequisites
- **Go:** `1.23+` (with `CGO_ENABLED=1`)
- **Rust:** `stable` toolchain (`cargo`, `rustc`)
- **C/C++ Toolchain:** `gcc`, `g++`, `cmake`, `make`, `libxapian-dev`, `protobuf-compiler`

### Unified Build Check (Go + Rust + CGo FFI)
Always verify that Rust FFI libraries (`libmetanode`, `libmtn_nomt`), C++ MVM/Xapian libraries, and Go binaries compile cleanly without warnings:

```bash
cd consensus/metanode/scripts
./build_check.sh
```

### Deploying Clusters Locally

1. **Multi-Cluster Topology (Parent Chain + Execution Clusters):**
   ```bash
   cd deploy/ansible_clusters
   ./deploy_clusters.sh --reset
   ```
2. **Single 4-Validator BFT Cluster (Chain `991`):**
   ```bash
   cd deploy/ansible
   ./ansible_deploy.sh --reset-all --fast
   ```

See [`OPERATIONS_GUIDE.md`](./OPERATIONS_GUIDE.md) for full deployment flags, health checks, and Day-2 operational commands.

### Running Tests & Evidence Verification

```bash
# Run Go unit & race tests on core packages
cd execution && go test -race ./pkg/parentchain/... ./pkg/rollup/... ./pkg/blockchain/tx_processor/...

# Run the CI verification suite
./ci.sh run-now --dry-run   # list CI stages; ./ci.sh run-now to run

# Verify benchmark & engineering evidence bundles in note/CLAIMS_LEDGER.md
python3 execution/scripts/test/evidence/claims_lint.py note/CLAIMS_LEDGER.md
python3 execution/scripts/test/evidence/verify_evidence.py --strict note/evidence/<bundle_dir>
```

---

## Documentation Index

- **[`PROJECT_STRUCTURE.md`](./PROJECT_STRUCTURE.md):** Complete directory map, FFI boundary reference, and cross-layer data flows.
- **[`AGENTS.md`](./AGENTS.md):** Engineering protocol, anti-over-engineering guardrails, and the mandatory **Zero-Fork Invariant**.
- **[`DATABASE_STRUCTURE.md`](./DATABASE_STRUCTURE.md):** On-disk PebbleDB, NOMT, RocksDB, and Xapian directory layout across Validator, RPC, and Explorer roles.
- **[`OPERATIONS_GUIDE.md`](./OPERATIONS_GUIDE.md):** End-to-end cluster deployment, systemd management, and troubleshooting runbook.
- **[`HOW_TO_TUNE_BLOCK_SIZE.md`](./HOW_TO_TUNE_BLOCK_SIZE.md):** Configuring batch tolerances, gas limits, and Rust DAG max block size.
- **[`docs/BLOCK_PROCESSING_ARCHITECTURE.md`](./docs/BLOCK_PROCESSING_ARCHITECTURE.md):** Post-consensus block processing, Union-Find grouping, and parallel execution safety rules.
- **[`note/CLAIMS_LEDGER.md`](./note/CLAIMS_LEDGER.md):** Evidence-backed engineering ledger and reproducible A/B benchmark claims.
