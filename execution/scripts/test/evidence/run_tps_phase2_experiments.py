#!/usr/bin/env python3
"""
run_tps_phase2_experiments.py - Phase 2 Experimental Measurement Suite (2026-10-09).
Executes direct micro-measurements and preregistered experiments:
1. Raft Sustained 60s (n=8): micro-metrics breakdown (proposeQ wait, raft apply, fsm build, sink wait, boltdb fsync, exec)
2. Raft Burst Scan (20k n=12, 25k n=8, 30k n=8): statusFull, splitBatch, queue depth, TPS distribution
3. BFT Sustained 60s (n=8): 0-10s vs 10-60s throughput split for DAG cold-start quantification
4. BFT Burst 25k Investigation (Harness variance: cold vs warm, cooldown 4s vs 10s)
5. Decision Gate Evaluation: checks whether bottleneck >= 10% of cycle and Amdahl upper bound >= +5%
"""

import os
import sys
import time
import json
import math
import shutil
import socket
import argparse
import subprocess
import urllib.request
from pathlib import Path

REPO_ROOT = Path("/home/abc/chain-n/metanode")
BINS_DIR = Path("/tmp/tps_root_cause_bins")
BIN_SIMPLE_CHAIN = BINS_DIR / "simple_chain"
BIN_BLAST = BINS_DIR / "secp_tps_blast"
BIN_ROLLUP_CLUSTER = BINS_DIR / "rollup-cluster"
BIN_PARENT = BINS_DIR / "parent_chain"

KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
BFT_TEMPLATE = "/tmp/gate_4val_clean_template"
BFT_BASE = "/tmp/gate_4val_p06"
RUN_ENV = str(REPO_ROOT / "execution/scripts/test/gate_e2e/run_env.sh")

RAFT_DIR = Path("/tmp/raft_phase2_cluster")
EVIDENCE_DIR = REPO_ROOT / "note/evidence/tps_improve_phase2_20261009"
RAW_DIR = EVIDENCE_DIR / "raw"

BFT_RPCS = [31646, 31647, 31648, 31649]
BFT_TCPS = [4246, 4247, 4248, 4249]

RAFT_NODES = [
    {"id": "n0", "rpc": 8810, "tcp": 4310, "raft": 7110, "fwd": 7210, "bootstrap": True},
    {"id": "n1", "rpc": 8811, "tcp": 4311, "raft": 7111, "fwd": 7211, "bootstrap": False},
    {"id": "n2", "rpc": 8812, "tcp": 4312, "raft": 7112, "fwd": 7212, "bootstrap": False},
]
SEQ_PRIV = "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
SEQ_ADDR = "0x0d4CC97b62a149a8fe8DE81262270426A80B0935"
RAFT_GENESIS = Path("/tmp/gate_4val_clean_template/exec2/genesis.json")

def wait_tcp(port, host="127.0.0.1", timeout=12.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            with socket.create_connection((host, port), timeout=0.5):
                return True
        except Exception:
            time.sleep(0.2)
    return False

def rpc_call(url: str, method: str, params: list = None, timeout: float = 3.0):
    if params is None:
        params = []
    payload = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode("utf-8")
    req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            if "error" in data:
                return None, data["error"]
            return data.get("result"), None
    except Exception as e:
        return None, str(e)

def http_get_json(url: str, timeout: float = 3.0):
    req = urllib.request.Request(url)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8")), None
    except Exception as e:
        return None, str(e)

# --- BFT Cluster Management ---
def bft_stop():
    if os.path.isdir(BFT_BASE):
        subprocess.run([RUN_ENV, BFT_BASE, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        time.sleep(1)
    subprocess.run(["pkill", "-9", "-f", "simple_chain.*gate_4val_p06"], stderr=subprocess.DEVNULL)
    subprocess.run(["pkill", "-9", "-f", "parent_chain.*gate_4val_p06"], stderr=subprocess.DEVNULL)
    time.sleep(1)
    if os.path.isdir(BFT_BASE):
        shutil.rmtree(BFT_BASE, ignore_errors=True)

def bft_start(enable_ffi_trace=True):
    bft_stop()
    os.makedirs(BFT_BASE, exist_ok=True)
    subprocess.run(["cp", "-a", "--reflink=auto", f"{BFT_TEMPLATE}/.", f"{BFT_BASE}/"], check=True)
    
    env_json_path = Path(BFT_BASE) / "env.json"
    ej = json.loads(env_json_path.read_text())
    ej["bin"] = str(BINS_DIR)
    env_json_path.write_text(json.dumps(ej, indent=2))

    env = dict(os.environ, ENABLE_DEBUG_PPROF="false")
    if enable_ffi_trace:
        env["METANODE_FFI_TRACE"] = "true"

    res = subprocess.run([RUN_ENV, BFT_BASE, "start"], env=env, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        return False

    t0 = time.time()
    while time.time() - t0 < 90.0:
        if all(rpc_call(f"http://127.0.0.1:{p}", "eth_blockNumber")[0] is not None for p in BFT_RPCS):
            break
        time.sleep(1.0)
    else:
        return False
    time.sleep(2)
    return True

def bft_pids():
    p = Path(BFT_BASE) / "pids"
    return ",".join(f.read_text().strip() for f in p.glob("*.pid")) if p.exists() else ""

# --- Raft Cluster Management ---
def raft_stop():
    subprocess.run(["pkill", "-9", "-f", "simple_chain.*raft_phase2_cluster"], stderr=subprocess.DEVNULL)
    time.sleep(1)
    if RAFT_DIR.exists():
        shutil.rmtree(RAFT_DIR, ignore_errors=True)

def raft_start():
    raft_stop()
    RAFT_DIR.mkdir(parents=True, exist_ok=True)

    secret_file = RAFT_DIR / "raft_secret.key"
    secret_file.write_bytes(os.urandom(32))
    os.chmod(secret_file, 0o600)

    for n in RAFT_NODES:
        ndir = RAFT_DIR / n["id"]
        ndir.mkdir(parents=True, exist_ok=True)
        cfg = {
            "debug": False,
            "cluster_id": 1,
            "consensus_mode": "raft",
            "enable_private_gateway": False,
            "master_password": "devnet-test-password",
            "app_pepper": "devnet-test-pepper",
            "private_key": SEQ_PRIV,
            "address": SEQ_ADDR,
            "log_path": str(ndir / "logs"),
            "backup_path": str(ndir / "backup"),
            "explorer_db_path": str(ndir / "explorer"),
            "explorer_read_only_db_path": str(ndir / "explorer-ro"),
            "is_explorer": False,
            "connection_address": f"0.0.0.0:{n['tcp']}",
            "version": "0.0.1.0",
            "rpc_port": f":{n['rpc']}",
            "db_type": 2,
            "genesis_file_path": str(RAFT_GENESIS),
            "snapshot_enabled": False,
            "tx_signature_mode": "secp",
            "Databases": {
                "RootPath": str(ndir / "data"),
                "DBEngine": "sharded",
                "Version": "0.0.1.0",
                "BLSPrivateKey": SEQ_PRIV,
            },
            "is_rpc_node": True,
            "raft": {
                "node_id": n["id"],
                "bind_address": f"127.0.0.1:{n['raft']}",
                "advertise_address": f"127.0.0.1:{n['raft']}",
                "data_dir": str(ndir / "raft"),
                "forward_bind_address": f"127.0.0.1:{n['fwd']}",
                "forward_secret_file": str(secret_file),
                "sequencer_address": SEQ_ADDR,
                "bootstrap": n["bootstrap"],
                "heartbeat_timeout_ms": 100,
                "election_timeout_ms": 200,
                "leader_lease_timeout_ms": 80,
                "commit_timeout_ms": 30,
                "propose_queue_size": 4096,
                "peers": [
                    {"id": m["id"], "address": f"127.0.0.1:{m['raft']}", "forward_address": f"127.0.0.1:{m['fwd']}"}
                    for m in RAFT_NODES
                ]
            }
        }
        (ndir / "config.json").write_text(json.dumps(cfg, indent=2))

    procs = []
    for n in RAFT_NODES:
        ndir = RAFT_DIR / n["id"]
        cfg_file = ndir / "config.json"
        log_f = open(ndir / "node.log", "w")
        p = subprocess.Popen([str(BIN_SIMPLE_CHAIN), "-config", str(cfg_file)],
                             stdout=log_f, stderr=log_f, cwd=str(ndir))
        procs.append((n["id"], p, log_f))

    t0 = time.time()
    while time.time() - t0 < 30.0:
        if all(rpc_call(f"http://127.0.0.1:{n['rpc']}", "eth_blockNumber")[0] is not None for n in RAFT_NODES):
            break
        time.sleep(0.5)
    else:
        return False, None, procs

    # Leader discovery via cluster check
    cluster_cfg = {
        "secret_file": str(secret_file),
        "members": [{"id": n["id"], "raft_addr": f"127.0.0.1:{n['raft']}", "admin_addr": f"127.0.0.1:{n['fwd']}"} for n in RAFT_NODES]
    }
    cluster_json_path = RAFT_DIR / "cluster.json"
    cluster_json_path.write_text(json.dumps(cluster_cfg, indent=2))
    leader = RAFT_NODES[0]
    t0 = time.time()
    while time.time() - t0 < 15.0:
        res = subprocess.run([str(BIN_ROLLUP_CLUSTER), "-config", str(cluster_json_path), "check"], capture_output=True, text=True)
        if res.returncode == 0:
            try:
                lid = json.loads(res.stdout).get("leader")
                if lid:
                    for n in RAFT_NODES:
                        if n["id"] == lid: leader = n; break
                    break
            except Exception: pass
        time.sleep(0.5)

    # Ensure TCP socket is fully accepting connections
    if not wait_tcp(leader["tcp"]):
        return False, None, procs

    return True, leader, procs

def calc_stats(vals):
    n = len(vals)
    if n == 0:
        return {"n": 0, "median": 0.0, "mean": 0.0, "sd": 0.0, "min": 0.0, "max": 0.0, "ci95": [0.0, 0.0]}
    sorted_v = sorted(vals)
    median = sorted_v[n // 2] if n % 2 == 1 else (sorted_v[n // 2 - 1] + sorted_v[n // 2]) / 2.0
    mean = sum(vals) / float(n)
    sd = math.sqrt(sum((x - mean) ** 2 for x in vals) / float(n - 1)) if n > 1 else 0.0
    t_crit_table = {1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571, 6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262, 10: 2.228, 11: 2.201, 12: 2.179}
    t_crit = t_crit_table.get(n - 1, 2.0)
    se = sd / math.sqrt(n) if n > 0 else 0.0
    ci95 = [round(mean - t_crit * se, 2), round(mean + t_crit * se, 2)]
    return {
        "n": n,
        "median": round(median, 2),
        "mean": round(mean, 2),
        "sd": round(sd, 2),
        "min": round(sorted_v[0], 2),
        "max": round(sorted_v[-1], 2),
        "ci95": ci95
    }

# --- Step 1: Raft Sustained 60s Micro-Measurement ---
def run_raft_sustained(rounds=8):
    print("\n" + "="*70)
    print(f"🚀 STEP 1: RAFT SUSTAINED 60s MICRO-MEASUREMENT (n={rounds})")
    print("="*70)
    
    results = []
    for r in range(1, rounds + 1):
        tag = f"raft_sustained_r{r}"
        rep_path = RAW_DIR / f"{tag}.json"
        log_path = RAW_DIR / f"{tag}.log"
        load_before = os.getloadavg()
        
        print(f"[{r}/{rounds}] Starting clean Raft cluster (Load: {load_before[0]:.2f})...", end="", flush=True)
        ok, leader, procs = raft_start()
        if not ok or leader is None:
            print(" ❌ START FAILED", flush=True)
            raft_stop()
            continue
        print(f" ✅ Online (Leader {leader['id']}). Blasting sustained 60s...", end="", flush=True)
        
        nodes_cfg = {
            "nodes": {n["id"]: f"http://127.0.0.1:{n['rpc']}" for n in RAFT_NODES},
            "tcp_nodes": {n["id"]: f"127.0.0.1:{n['tcp']}" for n in RAFT_NODES},
            "roles": {n["id"]: "leader" if n["id"] == leader["id"] else "follower" for n in RAFT_NODES}
        }
        nodes_json_p = RAFT_DIR / "rpc_nodes.json"
        nodes_json_p.write_text(json.dumps(nodes_cfg, indent=2))
        
        cmd = [
            str(BIN_BLAST),
            "-nodes-config", str(nodes_json_p),
            "-rpc", f"http://127.0.0.1:{leader['rpc']}",
            "-tcp", f"127.0.0.1:{leader['tcp']}",
            "-mode", "tcp",
            "-type", "1559",
            "-keys", KEYS_FILE,
            "-pids", ",".join(str(p.pid) for _, p, _ in procs),
            "-verify-parity",
            "-report", str(rep_path),
            "-duration", "60",
            "-batch", "1000"
        ]
        
        t0 = time.time()
        res = subprocess.run(cmd, capture_output=True, text=True)
        t_dur = time.time() - t0
        log_path.write_text(res.stdout + "\n--STDERR--\n" + res.stderr)
        
        # Query leader micro-metrics
        raft_metrics, _ = http_get_json(f"http://127.0.0.1:{leader['fwd']}/raft/v1/metrics")
        # Query block traces
        last_block, _ = rpc_call(f"http://127.0.0.1:{leader['rpc']}", "eth_blockNumber")
        block_traces = None
        if last_block:
            end_b = int(last_block, 16) if isinstance(last_block, str) else int(last_block)
            start_b = max(1, end_b - 50)
            block_traces, _ = rpc_call(f"http://127.0.0.1:{leader['rpc']}", "meta_getBlockTraces", [start_b, end_b])
            
        if rep_path.exists():
            d = json.load(open(rep_path))
            d["raft_metrics"] = raft_metrics
            d["sample_block_traces"] = block_traces
            d["machine_load_before"] = load_before
            d["test_duration_actual_sec"] = t_dur
            json.dump(d, open(rep_path, "w"), indent=2)
            results.append(d)
            
            tps = d.get("effective_tps", 0.0)
            p50 = d.get("latency_p50", 0) / 1e6
            sample_unconf = d.get("sample_unconfirmed", 0)
            cutoff = d.get("sample_cutoff_ratio", 0.0) * 100
            
            print(f" ⚡ {tps:,.1f} tx/s (P50: {p50:.1f}ms, Unconf: {sample_unconf} [{cutoff:.1f}%]) [dur {t_dur:.1f}s]")
            if raft_metrics:
                print(f"     📊 QueueWait: {raft_metrics.get('avg_propose_q_wait_ms', 0):.2f}ms | Apply: {raft_metrics.get('avg_raft_apply_ms', 0):.2f}ms | FSMBuild: {raft_metrics.get('avg_fsm_build_ms', 0):.2f}ms | SinkWait: {raft_metrics.get('avg_fsm_sink_wait_ms', 0):.2f}ms | BoltFsync: {raft_metrics.get('avg_bolt_store_ms', 0):.2f}ms")
        else:
            print(" ⚠️ NO REPORT")
            
        raft_stop()
        time.sleep(10)
    return results

# --- Step 2: Raft Burst Scan (20k, 25k, 30k) ---
def run_raft_burst_scan():
    print("\n" + "="*70)
    print("🚀 STEP 2: RAFT BURST SCAN (20k n=12, 25k n=8, 30k n=8)")
    print("="*70)
    
    scan_configs = [
        (20000, 12),
        (25000, 8),
        (30000, 8)
    ]
    
    results = {}
    for count, rounds in scan_configs:
        print(f"\n--- Scanning Burst {count//1000}k ({rounds} rounds) ---")
        sub_results = []
        for r in range(1, rounds + 1):
            tag = f"raft_burst_{count//1000}k_r{r}"
            rep_path = RAW_DIR / f"{tag}.json"
            log_path = RAW_DIR / f"{tag}.log"
            load_before = os.getloadavg()
            
            print(f"[{r}/{rounds} {count//1000}k] Clean Raft cluster (Load: {load_before[0]:.2f})...", end="", flush=True)
            ok, leader, procs = raft_start()
            if not ok or leader is None:
                print(" ❌ START FAILED", flush=True)
                raft_stop()
                continue
            print(f" ✅ Online. Blasting {count} txs...", end="", flush=True)
            
            nodes_cfg = {
                "nodes": {n["id"]: f"http://127.0.0.1:{n['rpc']}" for n in RAFT_NODES},
                "tcp_nodes": {n["id"]: f"127.0.0.1:{n['tcp']}" for n in RAFT_NODES},
                "roles": {n["id"]: "leader" if n["id"] == leader["id"] else "follower" for n in RAFT_NODES}
            }
            nodes_json_p = RAFT_DIR / "rpc_nodes.json"
            nodes_json_p.write_text(json.dumps(nodes_cfg, indent=2))
            
            cmd = [
                str(BIN_BLAST),
                "-nodes-config", str(nodes_json_p),
                "-rpc", f"http://127.0.0.1:{leader['rpc']}",
                "-tcp", f"127.0.0.1:{leader['tcp']}",
                "-mode", "tcp",
                "-type", "1559",
                "-keys", KEYS_FILE,
                "-pids", ",".join(str(p.pid) for _, p, _ in procs),
                "-verify-parity",
                "-report", str(rep_path),
                "-count", str(count),
                "-batch", "1000"
            ]
            
            t0 = time.time()
            res = subprocess.run(cmd, capture_output=True, text=True)
            t_dur = time.time() - t0
            log_path.write_text(res.stdout + "\n--STDERR--\n" + res.stderr)
            
            raft_metrics, _ = http_get_json(f"http://127.0.0.1:{leader['fwd']}/raft/v1/metrics")
            if rep_path.exists():
                d = json.load(open(rep_path))
                d["raft_metrics"] = raft_metrics
                d["machine_load_before"] = load_before
                d["test_duration_actual_sec"] = t_dur
                json.dump(d, open(rep_path, "w"), indent=2)
                sub_results.append(d)
                
                tps = d.get("effective_tps", 0.0)
                p50 = d.get("latency_p50", 0) / 1e6
                conf = d.get("total_confirmed", 0)
                full = raft_metrics.get("status_full_count", 0) if raft_metrics else 0
                splits = raft_metrics.get("split_batch_count", 0) if raft_metrics else 0
                print(f" ⚡ {tps:,.1f} tx/s (P50: {p50:.1f}ms, Conf: {conf}, statusFull: {full}, splits: {splits}) [dur {t_dur:.1f}s]")
            else:
                print(" ⚠️ NO REPORT")
                
            raft_stop()
            time.sleep(5)
        results[f"burst_{count//1000}k"] = sub_results
    return results

# --- Step 3: BFT Sustained 60s with 10s vs 50s Split (Q4) ---
def run_bft_sustained(rounds=8):
    print("\n" + "="*70)
    print(f"🚀 STEP 3: BFT SUSTAINED 60s (10s vs 50s SPLIT FOR Q4) (n={rounds})")
    print("="*70)
    
    results = []
    for r in range(1, rounds + 1):
        tag = f"bft_sustained_r{r}"
        rep_path = RAW_DIR / f"{tag}.json"
        log_path = RAW_DIR / f"{tag}.log"
        load_before = os.getloadavg()
        
        print(f"[{r}/{rounds}] Clean BFT cluster (Load: {load_before[0]:.2f})...", end="", flush=True)
        if not bft_start(enable_ffi_trace=True):
            print(" ❌ START FAILED", flush=True)
            bft_stop()
            continue
        print(" ✅ Online. Blasting sustained 60s...", end="", flush=True)
        
        cmd = [
            str(BIN_BLAST),
            "-nodes-config", f"{BFT_BASE}/rpc_nodes.json",
            "-mode", "tcp",
            "-type", "1559",
            "-keys", KEYS_FILE,
            "-pids", bft_pids(),
            "-verify-parity",
            "-report", str(rep_path),
            "-duration", "60",
            "-batch", "1000"
        ]
        
        t0 = time.time()
        res = subprocess.run(cmd, capture_output=True, text=True)
        t_dur = time.time() - t0
        log_path.write_text(res.stdout + "\n--STDERR--\n" + res.stderr)
        
        # Query Block Traces to calculate 0-10s vs 10-60s throughput
        last_block, _ = rpc_call(f"http://127.0.0.1:{BFT_RPCS[0]}", "eth_blockNumber")
        block_traces = None
        tps_10s = None
        tps_50s = None
        if last_block:
            end_b = int(last_block, 16) if isinstance(last_block, str) else int(last_block)
            block_traces, _ = rpc_call(f"http://127.0.0.1:{BFT_RPCS[0]}", "meta_getBlockTraces", [1, min(end_b, 1000)])
            if block_traces and len(block_traces) > 0:
                # Cumulative block durations
                cum_us = 0
                txs_10s = 0
                txs_50s = 0
                t_10s_us = 10 * 1_000_000
                for bt in block_traces:
                    dur = bt.get("total_block_duration_us", 0)
                    txs = bt.get("tx_count", 0)
                    cum_us += dur
                    if cum_us <= t_10s_us:
                        txs_10s += txs
                    else:
                        txs_50s += txs
                actual_dur_50s_sec = max(1.0, (cum_us - t_10s_us) / 1e6)
                tps_10s = txs_10s / 10.0
                tps_50s = txs_50s / actual_dur_50s_sec
                
        if rep_path.exists():
            d = json.load(open(rep_path))
            d["machine_load_before"] = load_before
            d["test_duration_actual_sec"] = t_dur
            d["tps_first_10s"] = tps_10s
            d["tps_next_50s"] = tps_50s
            d["sample_block_traces_count"] = len(block_traces) if block_traces else 0
            json.dump(d, open(rep_path, "w"), indent=2)
            results.append(d)
            
            tps = d.get("effective_tps", 0.0)
            p50 = d.get("latency_p50", 0) / 1e6
            sample_unconf = d.get("sample_unconfirmed", 0)
            cutoff = d.get("sample_cutoff_ratio", 0.0) * 100
            print(f" ⚡ Overall: {tps:,.1f} tx/s (P50: {p50:.1f}ms, Unconf: {sample_unconf} [{cutoff:.1f}%]) [dur {t_dur:.1f}s]")
            if tps_10s is not None and tps_50s is not None:
                print(f"     📈 10s Đầu: {tps_10s:,.1f} tx/s  vs  50s Sau: {tps_50s:,.1f} tx/s (Delta: {((tps_50s-tps_10s)/tps_10s)*100:+.1f}%)")
        else:
            print(" ⚠️ NO REPORT")
            
        bft_stop()
        time.sleep(10)
    return results

# --- Step 4: BFT Burst 25k Variance Investigation (Q3) ---
def run_bft_burst25k_investigation(rounds=4):
    print("\n" + "="*70)
    print(f"🚀 STEP 4: BFT BURST 25k INVESTIGATION (Q3 HARNESS COMPARISON) (n={rounds} each)")
    print("="*70)
    
    modes = [
        ("cold_start", "Wipe clean, direct blast (no warm-up, cooldown 4s)"),
        ("warm_up", "Wipe clean, warm-up 2k blast first, wait 2s, then blast 25k"),
        ("cooldown_10s", "Wipe clean, direct blast with 10s cooldown")
    ]
    
    investigation_results = {}
    for mode_key, mode_desc in modes:
        print(f"\n--- Testing Mode: {mode_key} ({mode_desc}) ---")
        mode_results = []
        for r in range(1, rounds + 1):
            tag = f"bft_burst25k_{mode_key}_r{r}"
            rep_path = RAW_DIR / f"{tag}.json"
            log_path = RAW_DIR / f"{tag}.log"
            load_before = os.getloadavg()
            
            print(f"[{r}/{rounds}] Clean BFT cluster...", end="", flush=True)
            if not bft_start(enable_ffi_trace=True):
                print(" ❌ START FAILED", flush=True)
                bft_stop()
                continue
            
            if mode_key == "warm_up":
                print(" Warming up 2k...", end="", flush=True)
                warm_cmd = [
                    str(BIN_BLAST),
                    "-nodes-config", f"{BFT_BASE}/rpc_nodes.json",
                    "-mode", "tcp",
                    "-type", "1559",
                    "-keys", KEYS_FILE,
                    "-count", "2000",
                    "-batch", "500"
                ]
                subprocess.run(warm_cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                time.sleep(2)
                
            print(" Blasting 25k...", end="", flush=True)
            cmd = [
                str(BIN_BLAST),
                "-nodes-config", f"{BFT_BASE}/rpc_nodes.json",
                "-mode", "tcp",
                "-type", "1559",
                "-keys", KEYS_FILE,
                "-pids", bft_pids(),
                "-verify-parity",
                "-report", str(rep_path),
                "-count", "25000",
                "-batch", "1000"
            ]
            
            t0 = time.time()
            res = subprocess.run(cmd, capture_output=True, text=True)
            t_dur = time.time() - t0
            log_path.write_text(res.stdout + "\n--STDERR--\n" + res.stderr)
            
            if rep_path.exists():
                d = json.load(open(rep_path))
                d["machine_load_before"] = load_before
                d["test_duration_actual_sec"] = t_dur
                json.dump(d, open(rep_path, "w"), indent=2)
                mode_results.append(d)
                
                tps = d.get("effective_tps", 0.0)
                p50 = d.get("latency_p50", 0) / 1e6
                print(f" ⚡ {tps:,.1f} tx/s (P50: {p50:.1f}ms) [dur {t_dur:.1f}s]")
            else:
                print(" ⚠️ NO REPORT")
                
            bft_stop()
            cd = 10 if mode_key == "cooldown_10s" else 4
            time.sleep(cd)
            
        investigation_results[mode_key] = mode_results
    return investigation_results

# --- Step 5: Summary Generation and Amdahl Gate Check ---
def generate_summary():
    print("\n" + "="*70)
    print("📊 GENERATING PHASE 2 MEASUREMENT SUMMARY & DECISION GATE EVALUATION")
    print("="*70)
    
    summary = {
        "timestamp": time.strftime("%Y-%m-%d %H:%M:%S UTC", time.gmtime()),
        "bins_sha256": {
            "simple_chain": subprocess.run(["sha256sum", str(BIN_SIMPLE_CHAIN)], capture_output=True, text=True).stdout.split()[0],
            "secp_tps_blast": subprocess.run(["sha256sum", str(BIN_BLAST)], capture_output=True, text=True).stdout.split()[0],
            "rollup-cluster": subprocess.run(["sha256sum", str(BIN_ROLLUP_CLUSTER)], capture_output=True, text=True).stdout.split()[0],
        },
        "experiments": {}
    }
    
    # Analyze Raft sustained files
    raft_sust_files = sorted(RAW_DIR.glob("raft_sustained_r*.json"))
    if raft_sust_files:
        tps_list = []
        p50_list = []
        q_wait_list = []
        raft_apply_list = []
        fsm_decode_list = []
        fsm_build_list = []
        fsm_sink_list = []
        bolt_fsync_list = []
        evm_exec_list = []
        save_db_list = []
        
        for f in raft_sust_files:
            try:
                d = json.load(open(f))
                tps_list.append(d.get("effective_tps", 0.0))
                p50_list.append(d.get("latency_p50", 0) / 1e6)
                m = d.get("raft_metrics")
                if m:
                    q_wait_list.append(m.get("avg_propose_q_wait_ms", 0.0))
                    raft_apply_list.append(m.get("avg_raft_apply_ms", 0.0))
                    fsm_decode_list.append(m.get("avg_fsm_decode_ms", 0.0))
                    fsm_build_list.append(m.get("avg_fsm_build_ms", 0.0))
                    fsm_sink_list.append(m.get("avg_fsm_sink_wait_ms", 0.0))
                    bolt_fsync_list.append(m.get("avg_bolt_store_ms", 0.0))
                # Check block traces for EVM and DB save times
                traces = d.get("sample_block_traces")
                if traces and isinstance(traces, list):
                    for bt in traces:
                        if bt.get("process_txs_duration_us"):
                            evm_exec_list.append(bt["process_txs_duration_us"] / 1000.0)
                        if bt.get("save_db_duration_us"):
                            save_db_list.append(bt["save_db_duration_us"] / 1000.0)
            except Exception: pass
            
        summary["experiments"]["raft_sustained"] = {
            "tps": calc_stats(tps_list),
            "latency_p50_ms": calc_stats(p50_list),
            "propose_q_wait_ms": calc_stats(q_wait_list),
            "raft_apply_ms": calc_stats(raft_apply_list),
            "fsm_decode_ms": calc_stats(fsm_decode_list),
            "fsm_build_ms": calc_stats(fsm_build_list),
            "fsm_sink_wait_ms": calc_stats(fsm_sink_list),
            "boltdb_fsync_ms": calc_stats(bolt_fsync_list),
            "evm_exec_ms": calc_stats(evm_exec_list) if evm_exec_list else None,
            "pebbledb_save_ms": calc_stats(save_db_list) if save_db_list else None,
        }
        
    # Analyze Raft burst scans
    for b_key in ["20k", "25k", "30k"]:
        files = sorted(RAW_DIR.glob(f"raft_burst_{b_key}_r*.json"))
        if files:
            tps_l = []
            p50_l = []
            full_l = []
            split_l = []
            for f in files:
                try:
                    d = json.load(open(f))
                    tps_l.append(d.get("effective_tps", 0.0))
                    p50_l.append(d.get("latency_p50", 0) / 1e6)
                    m = d.get("raft_metrics")
                    if m:
                        full_l.append(m.get("status_full_count", 0))
                        split_l.append(m.get("split_batch_count", 0))
                except Exception: pass
            summary["experiments"][f"raft_burst_{b_key}"] = {
                "tps": calc_stats(tps_l),
                "latency_p50_ms": calc_stats(p50_l),
                "status_full_count_sum": sum(full_l),
                "split_batch_count_sum": sum(split_l),
            }

    # Analyze BFT sustained
    bft_sust_files = sorted(RAW_DIR.glob("bft_sustained_r*.json"))
    if bft_sust_files:
        bft_tps = []
        bft_p50 = []
        tps_10s_l = []
        tps_50s_l = []
        for f in bft_sust_files:
            try:
                d = json.load(open(f))
                bft_tps.append(d.get("effective_tps", 0.0))
                bft_p50.append(d.get("latency_p50", 0) / 1e6)
                if d.get("tps_first_10s") is not None:
                    tps_10s_l.append(d["tps_first_10s"])
                if d.get("tps_next_50s") is not None:
                    tps_50s_l.append(d["tps_next_50s"])
            except Exception: pass
        summary["experiments"]["bft_sustained"] = {
            "overall_tps": calc_stats(bft_tps),
            "latency_p50_ms": calc_stats(bft_p50),
            "tps_first_10s": calc_stats(tps_10s_l),
            "tps_next_50s": calc_stats(tps_50s_l),
        }
        
    # Analyze BFT burst 25k investigation
    for mode in ["cold_start", "warm_up", "cooldown_10s"]:
        files = sorted(RAW_DIR.glob(f"bft_burst25k_{mode}_r*.json"))
        if files:
            tps_l = []
            p50_l = []
            for f in files:
                try:
                    d = json.load(open(f))
                    tps_l.append(d.get("effective_tps", 0.0))
                    p50_l.append(d.get("latency_p50", 0) / 1e6)
                except Exception: pass
            summary["experiments"][f"bft_burst25k_{mode}"] = {
                "tps": calc_stats(tps_l),
                "latency_p50_ms": calc_stats(p50_l)
            }
            
    # Decision Gate Calculations
    # Calculate Raft cycle breakdown & Amdahl upper bound
    decision_gate = {
        "status": "EVALUATING",
        "details": {}
    }
    if "raft_sustained" in summary["experiments"]:
        rs = summary["experiments"]["raft_sustained"]
        t_apply = rs["raft_apply_ms"]["mean"]
        t_fsm_build = rs["fsm_build_ms"]["mean"]
        t_bolt_fsync = rs["boltdb_fsync_ms"]["mean"]
        
        # Total per-entry cycle time estimate
        total_cycle = t_apply + t_fsm_build
        if total_cycle > 0:
            apply_pct = (t_apply / total_cycle) * 100.0
            fsm_pct = (t_fsm_build / total_cycle) * 100.0
            fsync_pct = (t_bolt_fsync / total_cycle) * 100.0
            
            # If batch coalescing (R1) reduces number of entries/blocks by 2x:
            # Amdahl upper bound on Throughput
            coalesce_gain_pct = (apply_pct / 100.0 * 0.5) * 100.0
            
            decision_gate["details"] = {
                "total_estimated_cycle_ms": round(total_cycle, 2),
                "raft_apply_pct": round(apply_pct, 1),
                "fsm_build_pct": round(fsm_pct, 1),
                "boltdb_fsync_pct": round(fsync_pct, 1),
                "amdahl_upper_bound_coalesce_pct": round(coalesce_gain_pct, 1),
                "passed_gate": apply_pct >= 10.0 and coalesce_gain_pct >= 5.0
            }
            decision_gate["status"] = "PASSED" if decision_gate["details"]["passed_gate"] else "BLOCKED"
            
    summary["decision_gate"] = decision_gate
    
    # Save summary
    sum_path = EVIDENCE_DIR / "phase2_measurement_summary.json"
    sum_path.write_text(json.dumps(summary, indent=2))
    print(f"Saved summary to: {sum_path}")
    
    # Update MANIFEST.json with sha256
    manifest = {}
    for p in EVIDENCE_DIR.glob("**/*"):
        if p.is_file() and p.name != "MANIFEST.json":
            rel = str(p.relative_to(EVIDENCE_DIR))
            h = subprocess.run(["sha256sum", str(p)], capture_output=True, text=True).stdout.split()[0]
            manifest[rel] = h
    (EVIDENCE_DIR / "MANIFEST.json").write_text(json.dumps(manifest, indent=2))
    print(f"Updated MANIFEST.json ({len(manifest)} files indexed).")
    
    return summary

def main():
    parser = argparse.ArgumentParser(description="Phase 2 TPS Measurement Suite")
    parser.add_argument("--step", choices=["all", "raft_sustained", "raft_burst", "bft_sustained", "bft_burst25k", "summary"], default="all")
    parser.add_argument("--rounds", type=int, default=8, help="Number of rounds per configuration")
    args = parser.parse_args()
    
    RAW_DIR.mkdir(parents=True, exist_ok=True)
    
    if args.step in ["all", "raft_sustained"]:
        run_raft_sustained(rounds=args.rounds)
    if args.step in ["all", "raft_burst"]:
        run_raft_burst_scan()
    if args.step in ["all", "bft_sustained"]:
        run_bft_sustained(rounds=args.rounds)
    if args.step in ["all", "bft_burst25k"]:
        run_bft_burst25k_investigation(rounds=4)
    if args.step in ["all", "summary"]:
        generate_summary()

if __name__ == "__main__":
    main()
