# MetaNode Automation Test Pipeline (`auto_test.sh`)

This document provides a comprehensive guide on how to use the automated testing script `auto_test.sh` included in the `mtn-simple-2025` repository.

## Overview

The `auto_test.sh` script is an end-to-end automation pipeline designed to facilitate testing for the MetaNode blockchain ecosystem. It orchestrates the entire lifecycle of a test network:
1. Cleaning up and regenerating the genesis configuration.
2. Generating a massive set of spam keys for simulated loads.
3. Deploying the local MetaNode cluster (supporting both single-machine local testnets and multi-machine setups).
4. Running validation tests for basic TCP RPC functionality.
5. Testing advanced MVM capabilities including the Xapian V0 and V2 precompiles.
6. Triggering a massive TPS (Transactions Per Second) load test via parallel native transfers.

## Prerequisites

- You must execute this script from within a bash terminal.
- Ensure your `$PROJECT_ROOT` and paths (`/home/abc/nhat/con-chain-v2/...`) are correct and accessible.
- Dependencies such as `go`, binary compilation toolchains, and necessary shell scripts (e.g., `mtn-orchestrator.sh` or `deploy_cluster.sh`) must be executable.

## Usage & Arguments

You can run the script normally to execute all steps sequentially from the beginning:

```bash
./auto_test.sh
```

### Supported Arguments

The script supports overriding the starting step and the deployment topology mode.

| Argument | Value Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `--step` | `Integer (1-6)` | `1` | The pipeline step at which to start or resume. Useful if a previous run failed at an intermediate step and you don't want to restart the cluster from scratch. |
| `--mode` | `single` \| `multi`| `single`| The topology used in Step 2. `single` uses `mtn-orchestrator.sh` for a 1-machine cluster. `multi` uses `deploy_cluster.sh` with `deploy-3machines.env` for a multi-machine testing layout. |

### Examples

**1. Run a fresh multi-machine test from the beginning:**
```bash
./auto_test.sh --step 1 --mode multi
```

**2. The cluster is already deployed. Resume load testing (starts at Step 6):**
```bash
./auto_test.sh --step 6 --mode multi
```

**3. Run specific Xapian tests and then TPS load tests (skipping cluster deploy):**
```bash
./auto_test.sh --step 4 --mode multi
```

## Pipeline Steps Explained

### Bước 1: Prepare Genesis & Gen Spam Keys
- **Location:** `cmd/simple_chain` and `cmd/tool/test_tps/gen_spam_keys`
- **Action:** Refreshes `genesis.json` from `genesis-main.json` to ensure clean state and generates 50,000 unique key pairs (`generated_keys.json`) used subsequently by the load tester.

### Bước 2: Deploy Cluster
- **Location:** `mtn-consensus/metanode/scripts/...`
- **Action:** 
  - If `--mode single`: Runs `mtn-orchestrator.sh restart --fresh --build-all`.
  - If `--mode multi`: Runs `deploy_cluster.sh --env deploy-3machines.env --all`.
- **Note:** Includes a brief 5-second sleep to ensure HTTP/RPC servers settle before pushing queries.

### Bước 3: Test TCP RPC
- **Location:** `cmd/tool/tool-test-chain/test-tcp/caller-tcp`
- **Action:** Runs `main-no-none.go` to test legacy raw TCP transaction injection.

### Bước 4: Test HTTP RPC - Xapian V0
- **Location:** `cmd/tool/tool-test-chain/test-rpc`
- **Action:** Runs a targeted script implementing the initial tests (read/write data) aimed at validating Xapian V0's integration within the C++ MVM engine.

### Bước 5: Test HTTP RPC - Xapian V2
- **Location:** `cmd/tool/tool-test-chain/test-rpc`
- **Action:** Validates Xapian V2 integration updates over the JSON-RPC interface.

### Bước 6: Load Test TPS (Load Balancer & Parallel Execution)
- **Location:** `cmd/tool/test_tps/tps_blast_cc`
- **Action:** Triggers the TPS load tester with a 20,000 TX spray across 5 rounds. Uses parallel native transfers (`--parallel_native=true`) and round-robin connection pools (`--load_balance=true`).
- **Recent Updates for Debugging:** The tool is now equipped to automatically detect `invalid nonce` errors thrown by lagging consensus nodes. If an invalid nonce occurs, it triggers a cross-check array (Mismatch/Divergence Check) scanning all RPC targets in the pool concurrently and cleanly identifies which node represents stale network state. You can inspect the divergence table in your terminal during this step.

## Troubleshooting

- **"Lỗi ở Bước X" (Error at Step X):** Look at the lines directly above the error message to view the output of the Go program. Fix the compilation or logic error, and use `./auto_test.sh --step X` to resume from the failure point without resetting everything.
- **Node 3 Deadlocks / "Missing Receipt":** If Step 6 hangs or states timeout waiting for receipts, one of the MVM Go/C++ consensus instances (potentially Master Node 3) has deadlocked. Check the `logs/node_X/go-master-stdout.log` logs to identify where GEI (Global Exec Index) stalled.
- **"invalid nonce":** This is expected under massive load testing when fetching state from weakly-synchronised sub-nodes. Step 6's TPS tool will print out a table diagnosing which node yielded the stale nonce.
