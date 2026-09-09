# Metanode

Metanode is a hybrid Go/Rust blockchain platform: a **Rust BFT/DAG consensus engine** paired with a **Go EVM-compatible execution engine**, talking to each other over FFI and a Unix domain socket. It supports a public "Root Anchor" chain plus independent private chains that interoperate through a cross-chain relayer.

```
        Go Execution Engine (execution/)              Rust Consensus Engine (consensus/)
   ┌───────────────────────────────────┐        ┌──────────────────────────────────┐
   │ JSON-RPC (eth_*, mtn_*) / gRPC     │  UDS   │ BFT/DAG engine (meta-consensus/)  │
   │ EVM execution, state trie, mempool │◄──────►│ Linearizer, committer, P2P sync   │
   │ pkg/nomt_ffi ─ FFI ─────────────── │  C ABI │ ffi.rs (exports to Go)            │
   └───────────────────────────────────┘        └──────────────────────────────────┘
```

For the full module-by-module breakdown, see [`PROJECT_STRUCTURE.md`](./PROJECT_STRUCTURE.md) — it is the source of truth for the architecture and is kept up to date on every structural change.

## Repository layout

| Path | What it is |
| :--- | :--- |
| [`execution/`](./execution) | Go execution engine — EVM-compatible layer, RPC, state, mempool, P2P |
| [`consensus/`](./consensus) | Rust consensus engine — BFT/DAG core (`meta-consensus/`), node lifecycle, FFI exports |
| [`crates/`](./crates) | Shared Rust crates (crypto, metrics, storage, macros) used across the workspace |
| [`deploy/`](./deploy) | Deployment: Ansible playbooks (Root Anchor, private chains), systemd units/genesis generators, CI watcher/daemon |
| [`docs/`](./docs) | Docusaurus documentation website |
| [`note/`](./note) | Architecture notes, design docs, known-bug write-ups |
| [`scripts/`](./scripts) | Operational scripts (private chain init, benchmarks, cluster orchestration) |
| [`dashboard/`](./dashboard) | Monitoring dashboard |
| [`OPERATIONS_GUIDE.md`](./OPERATIONS_GUIDE.md) | End-to-end deployment & day-2 operations runbook |
| [`DATABASE_STRUCTURE.md`](./DATABASE_STRUCTURE.md) | On-disk database layout per node role |
| [`PROJECT_STRUCTURE.md`](./PROJECT_STRUCTURE.md) | Full architecture map (kept in sync with the codebase) |

## Prerequisites

- **Go** 1.23.5+ (see [`execution/go.mod`](./execution/go.mod))
- **Rust** stable, with `rustfmt` and `clippy` (see [`rust-toolchain.toml`](./rust-toolchain.toml))
- Python 3 (for deployment/genesis generation scripts under `deploy/systemd/`)

## Build

Build and verify both the Go and Rust sides (plus the FFI boundary) with the shared build-check script:

```bash
cd consensus/metanode/scripts
./build_check.sh            # both Go + Rust, release mode
./build_check.sh --go-only  # Go execution engine only
./build_check.sh --rust-only  # Rust consensus engine only
./build_check.sh --debug    # faster, unoptimized build
```

Or build each side directly:

```bash
# Rust consensus engine (workspace defined in Cargo.toml)
cargo build --release

# Go execution engine
cd execution && go build ./...
```

## Running a local chain

To spin up a private chain for local development, generate its genesis/config first:

```bash
CHAIN_ID=1337 VALIDATORS=1 IP=127.0.0.1 ./scripts/init_private_chain.sh
```

For a full multi-node topology (Root Anchor public chain + private chains + cross-chain relayer) and day-2 operations (upgrades, restarts, log collection, troubleshooting), follow [`OPERATIONS_GUIDE.md`](./OPERATIONS_GUIDE.md).

## Documentation

- Architecture & module map: [`PROJECT_STRUCTURE.md`](./PROJECT_STRUCTURE.md)
- Deployment & operations runbook: [`OPERATIONS_GUIDE.md`](./OPERATIONS_GUIDE.md)
- Database layout: [`DATABASE_STRUCTURE.md`](./DATABASE_STRUCTURE.md)
- Design docs & known issues: [`note/`](./note)
- Consensus engine docs (MkDocs site): [`consensus/README.md`](./consensus/README.md)
- Execution engine docs (MkDocs site): [`execution/README.md`](./execution/README.md)
- Rendered documentation website: [`docs/`](./docs)
