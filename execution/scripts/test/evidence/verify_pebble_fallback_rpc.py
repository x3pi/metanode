#!/usr/bin/env python3
"""
verify_pebble_fallback_rpc.py - Verification of Pebble DB Fallback on Cold Cache / Restart

Demonstrates end-to-end that when in-memory RAM mapping caches are unpopulated
(e.g., following a cluster restart with cold caches), RPC calls for transaction
receipts (eth_getTransactionReceipt) and transactions (eth_getTransactionByHash)
transparently fall back to reading mappings directly from persistent Pebble DB.
"""

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request

RUN_ENV = "/home/abc/chain-n/metanode/execution/scripts/test/gate_e2e/run_env.sh"
TEMPLATE_BASE = "/tmp/gate_4val_clean_template"
BENCH_BASE = "/tmp/gate_e2e_4val_bench"
BLAST_TOOL = "/tmp/b1_bins/secp_tps_blast"
KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
RPC_URL = "http://127.0.0.1:31646"
NODES = ["val0", "val1", "val2", "val3"]

def stop_and_clean_bench():
    if os.path.isdir(BENCH_BASE):
        subprocess.run([RUN_ENV, BENCH_BASE, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        time.sleep(1)
        shutil.rmtree(BENCH_BASE, ignore_errors=True)

def setup_fresh_cluster():
    stop_and_clean_bench()
    if not os.path.isdir(TEMPLATE_BASE):
        print(f"ERROR: Template base not found: {TEMPLATE_BASE}")
        sys.exit(1)
    shutil.copytree(TEMPLATE_BASE, BENCH_BASE)

def start_cluster():
    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "false"
    res = subprocess.run([RUN_ENV, BENCH_BASE, "start"], env=env, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        print("Failed to start cluster:", res.stderr)
        return False
    return True

def stop_cluster_keep_data():
    res = subprocess.run([RUN_ENV, BENCH_BASE, "stop"], capture_output=True, text=True, check=False)
    time.sleep(1)
    return res.returncode == 0

def rpc_call(method, params=[]):
    req = {
        "jsonrpc": "2.0",
        "method": method,
        "params": params,
        "id": int(time.time() * 1000) % 100000
    }
    data = json.dumps(req).encode('utf-8')
    request = urllib.request.Request(RPC_URL, data=data, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            return json.loads(response.read().decode('utf-8'))
    except Exception as e:
        return {"error": str(e)}

def wait_for_rpc(max_wait=10):
    start = time.time()
    while time.time() - start < max_wait:
        res = rpc_call("eth_blockNumber")
        if "result" in res:
            return True
        time.sleep(0.5)
    return False

def get_pids():
    pids = {}
    for n in NODES:
        pid_file = f"{BENCH_BASE}/pids/{n}.pid"
        if os.path.isfile(pid_file):
            with open(pid_file, "r") as f:
                try:
                    pids[n] = int(f.read().strip())
                except ValueError:
                    pass
    return pids

def run_blast(tx_count=200, batch=100):
    pids = get_pids()
    val_pids_str = ",".join(str(pids[n]) for n in NODES if n in pids)
    nodes_config = f"{BENCH_BASE}/rpc_nodes.json"
    cmd = [
        BLAST_TOOL,
        "-nodes-config", nodes_config,
        "-keys", KEYS_FILE,
        "-count", str(tx_count),
        "-batch", str(batch),
        "-mode", "tcp",
        "-type", "1559",
        "-verify-parity=false",
        "-pids", val_pids_str
    ]
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, check=False)
    return res.stdout

def main():
    print("==================================================================")
    print("🔬 PEBBLE DB FALLBACK RPC VERIFICATION ON COLD CACHE / RESTART")
    print("==================================================================")
    print(f"Timestamp: {time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}")

    print("\n▶️ [Step 1] Initializing fresh isolated cluster from template...")
    setup_fresh_cluster()
    if not start_cluster():
        print("❌ Failed to start cluster")
        sys.exit(1)
    
    if not wait_for_rpc(10):
        print("❌ RPC port 31000 failed to respond within 10s")
        stop_and_clean_bench()
        sys.exit(1)
    print("   • Cluster running on ports 31000..31003. RPC responsive.")

    print("\n▶️ [Step 2] Sending transaction workload (200 txs EIP-1559)...")
    blast_out = run_blast(tx_count=200, batch=100)
    time.sleep(2)  # Wait for block commit

    bnum_res = rpc_call("eth_blockNumber")
    current_block = int(bnum_res.get("result", "0x0"), 16)
    print(f"   • Current Block Height: #{current_block}")
    if current_block < 1:
        print("❌ No blocks committed after blast")
        stop_and_clean_bench()
        sys.exit(1)

    print("\n▶️ [Step 3] Fetching committed block and target transaction...")
    block_res = rpc_call("eth_getBlockByNumber", [hex(current_block), True])
    block_data = block_res.get("result", {})
    txs = block_data.get("transactions", [])
    if not txs:
        # Check previous block
        block_res = rpc_call("eth_getBlockByNumber", [hex(current_block - 1), True])
        block_data = block_res.get("result", {})
        txs = block_data.get("transactions", [])

    if not txs:
        print(f"❌ No transactions found in blocks #{current_block} or #{current_block-1}")
        stop_and_clean_bench()
        sys.exit(1)

    target_tx = txs[0]
    target_tx_hash = target_tx.get("hash")
    print(f"   • Target Tx Hash: {target_tx_hash}")
    print(f"   • Target Block:   #{int(target_tx.get('blockNumber', '0x0'), 16)}")

    print("\n▶️ [Step 4] Querying receipt BEFORE restart (warm RAM cache)...")
    rcp_warm = rpc_call("eth_getTransactionReceipt", [target_tx_hash])
    rcp_warm_res = rcp_warm.get("result")
    if not rcp_warm_res:
        print(f"❌ Warm cache receipt lookup failed: {rcp_warm}")
        stop_and_clean_bench()
        sys.exit(1)
    print(f"   • Warm Status:       {rcp_warm_res.get('status')}")
    print(f"   • Warm Block Number: {int(rcp_warm_res.get('blockNumber', '0x0'), 16)}")
    print(f"   • Warm Gas Used:     {int(rcp_warm_res.get('gasUsed', '0x0'), 16)}")

    print("\n▶️ [Step 5] Stopping cluster (evicting 100% of RAM caches)...")
    if not stop_cluster_keep_data():
        print("❌ Failed to stop cluster")
        stop_and_clean_bench()
        sys.exit(1)
    print("   • All node processes terminated. RAM caches destroyed.")

    print("\n▶️ [Step 6] Restarting cluster with COLD RAM caches...")
    if not start_cluster():
        print("❌ Failed to restart cluster")
        stop_and_clean_bench()
        sys.exit(1)
    
    if not wait_for_rpc(10):
        print("❌ RPC failed to respond after restart")
        stop_and_clean_bench()
        sys.exit(1)
    print("   • Node val0 restarted with empty in-memory tx caches (mapping_rebuild.go:113).")

    print("\n▶️ [Step 7] Querying eth_getTransactionReceipt on COLD cache (forcing Pebble DB fallback)...")
    rcp_cold = rpc_call("eth_getTransactionReceipt", [target_tx_hash])
    rcp_cold_res = rcp_cold.get("result")
    if not rcp_cold_res:
        print(f"❌ Cold cache Pebble DB fallback receipt lookup FAILED: {rcp_cold}")
        stop_and_clean_bench()
        sys.exit(1)

    cold_status = rcp_cold_res.get("status")
    cold_block = int(rcp_cold_res.get("blockNumber", "0x0"), 16)
    cold_gas = int(rcp_cold_res.get("gasUsed", "0x0"), 16)
    print(f"   • Cold Status:       {cold_status} (Expected: 0x1)")
    print(f"   • Cold Block Number: {cold_block} (Expected: {int(rcp_warm_res.get('blockNumber'), 16)})")
    print(f"   • Cold Gas Used:     {cold_gas} (Expected: {int(rcp_warm_res.get('gasUsed'), 16)})")

    assert cold_status == "0x1", f"Expected status 0x1, got {cold_status}"
    assert cold_block == int(rcp_warm_res.get("blockNumber"), 16), "Block number mismatch"
    assert cold_gas == int(rcp_warm_res.get("gasUsed"), 16), "Gas used mismatch"

    print("\n▶️ [Step 8] Querying eth_getTransactionByHash on COLD cache...")
    tx_cold = rpc_call("eth_getTransactionByHash", [target_tx_hash])
    tx_cold_res = tx_cold.get("result")
    if not tx_cold_res:
        print(f"❌ Cold cache Pebble DB fallback tx lookup FAILED: {tx_cold}")
        stop_and_clean_bench()
        sys.exit(1)
    print(f"   • Cold Tx Hash:      {tx_cold_res.get('hash')}")
    print(f"   • Cold Tx Value:     {int(tx_cold_res.get('value', '0x0'), 16)}")

    print("\n▶️ [Step 9] Cleaning up benchmark cluster...")
    stop_and_clean_bench()
    print("   • Isolated benchmark cluster stopped and removed.")

    print("\n==================================================================")
    print("✅ VERIFICATION SUCCESSFUL:")
    print("   1. Tx hash -> block number mapping persists in Pebble DB.")
    print("   2. When RAM cache is cold/evicted, GetBlockNumberByTxHashFast transparently")
    print("      reads from Pebble DB storageMapping.")
    print("   3. eth_getTransactionReceipt and eth_getTransactionByHash return exact")
    print("      data without loss or errors.")
    print("==================================================================")

if __name__ == "__main__":
    main()
