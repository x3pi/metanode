#!/usr/bin/env python3
"""
run_raft_benchmark.py - Automated Benchmarking & Zero-Fork Verification for Raft Consensus Mode.

Sets up a clean 3-node HashiCorp Raft replicated cluster (n0, n1, n2) with Quorum 2/3,
deploys the latest simple_chain binary with parallelized PrepareTransactions (Phase 2b),
injects real EIP-1559 signed transactions via secp_tps_blast, verifies cross-node parity,
measures block cycle duration and PrepareTransactions latency, and outputs structured results.
"""

import os
import sys
import time
import json
import secrets
import signal
import subprocess
import urllib.request
import urllib.error
from pathlib import Path

REPO_ROOT = Path("/home/abc/chain-n/metanode")
BIN_SIMPLE_CHAIN = Path("/tmp/p06_bins/simple_chain")
BIN_BLAST = Path("/tmp/p06_bins/secp_tps_blast")
BIN_ROLLUP_CLUSTER = REPO_ROOT / "execution/cmd/tool/rollup_cluster/rollup-cluster"
KEYS_FILE = Path("/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json")
GENESIS_FILE = Path("/tmp/gate_4val_clean_template/exec2/genesis.json")
WORK_DIR = Path("/tmp/raft_bench_cluster")
EVIDENCE_DIR = REPO_ROOT / "note/evidence/tps_prepare_tx_opt_20261008"

SEQ_PRIV = "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
SEQ_ADDR = "0x0d4CC97b62a149a8fe8DE81262270426A80B0935"

NODES = [
    {"id": "n0", "rpc": 8810, "tcp": 4310, "raft": 7110, "fwd": 7210, "bootstrap": True},
    {"id": "n1", "rpc": 8811, "tcp": 4311, "raft": 7111, "fwd": 7211, "bootstrap": False},
    {"id": "n2", "rpc": 8812, "tcp": 4312, "raft": 7112, "fwd": 7212, "bootstrap": False},
]

def kill_existing():
    print("🧹 Cleaning up old raft / simple_chain processes...")
    subprocess.run(["pkill", "-9", "-f", "simple_chain.*raft_bench_cluster"], stderr=subprocess.DEVNULL)
    time.sleep(1)

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

def wait_for_rpc(node_rpc: int, timeout: float = 30.0) -> bool:
    url = f"http://127.0.0.1:{node_rpc}"
    t0 = time.time()
    while time.time() - t0 < timeout:
        res, err = rpc_call(url, "eth_blockNumber")
        if err is None and res is not None:
            return True
        time.sleep(0.5)
    return False

def discover_leader(secret_file: Path) -> dict:
    cluster_cfg = {
        "secret_file": str(secret_file),
        "members": [
            {"id": n["id"], "raft_addr": f"127.0.0.1:{n['raft']}", "admin_addr": f"127.0.0.1:{n['fwd']}"}
            for n in NODES
        ]
    }
    cluster_json_path = WORK_DIR / "cluster.json"
    cluster_json_path.write_text(json.dumps(cluster_cfg, indent=2))

    t0 = time.time()
    while time.time() - t0 < 20.0:
        res = subprocess.run([str(BIN_ROLLUP_CLUSTER), "-config", str(cluster_json_path), "check"],
                             capture_output=True, text=True)
        if res.returncode == 0:
            try:
                status = json.loads(res.stdout)
                leader_id = status.get("leader")
                if leader_id:
                    for n in NODES:
                        if n["id"] == leader_id:
                            return n
            except Exception:
                pass
        time.sleep(0.5)
    return NODES[0]

def setup_cluster():
    kill_existing()
    if WORK_DIR.exists():
        import shutil
        shutil.rmtree(WORK_DIR, ignore_errors=True)
    WORK_DIR.mkdir(parents=True, exist_ok=True)

    secret_file = WORK_DIR / "raft_secret.key"
    secret_bytes = secrets.token_bytes(32)
    secret_file.write_bytes(secret_bytes)
    os.chmod(secret_file, 0o600)

    for n in NODES:
        ndir = WORK_DIR / n["id"]
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
            "genesis_file_path": str(GENESIS_FILE),
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
                "propose_queue_size": 2048,
                "peers": [
                    {"id": m["id"], "address": f"127.0.0.1:{m['raft']}", "forward_address": f"127.0.0.1:{m['fwd']}"}
                    for m in NODES
                ]
            }
        }
        (ndir / "config.json").write_text(json.dumps(cfg, indent=2))

    processes = []
    print("🚀 Launching 3 Raft Replicas (n0, n1, n2)...")
    for n in NODES:
        ndir = WORK_DIR / n["id"]
        cfg_file = ndir / "config.json"
        log_file = open(ndir / "node.log", "w")
        p = subprocess.Popen([str(BIN_SIMPLE_CHAIN), "-config", str(cfg_file)],
                             stdout=log_file, stderr=log_file, cwd=str(ndir))
        processes.append((n["id"], p, log_file))
        print(f"   • Replica {n['id']} launched (PID {p.pid}, RPC :{n['rpc']}, Raft :{n['raft']})")

    print("⏳ Waiting for RPC and Raft quorum election...")
    for n in NODES:
        if not wait_for_rpc(n["rpc"], timeout=25.0):
            print(f"❌ Failed to reach RPC for node {n['id']}")
            return None, secret_file, processes
        print(f"   ✅ Node {n['id']} RPC ready at :{n['rpc']}")

    leader = discover_leader(secret_file)
    print(f"👑 Cluster Leader identified: {leader['id']} (RPC :{leader['rpc']}, TCP :{leader['tcp']})")
    time.sleep(2)
    return leader, secret_file, processes

def extract_log_latencies(log_path: Path):
    """Parses PrepareTransactions and Block execution metrics from node.log"""
    prep_times = []
    cycle_times = []
    if not log_path.exists():
        return prep_times, cycle_times

    with open(log_path, "r", errors="ignore") as f:
        for line in f:
            if "PrepTotal=" in line:
                try:
                    part = line.split("PrepTotal=")[1].split()[0]
                    if part.endswith("ms"):
                        prep_times.append(float(part[:-2]))
                    elif part.endswith("µs") or part.endswith("us"):
                        prep_times.append(float(part[:-2]) / 1000.0)
                except Exception:
                    pass
            if "Completed execution of block" in line and "in " in line:
                try:
                    part = line.split("in ")[1].split()[0]
                    if part.endswith("ms"):
                        cycle_times.append(float(part[:-2]))
                    elif part.endswith("µs") or part.endswith("us"):
                        cycle_times.append(float(part[:-2]) / 1000.0)
                except Exception:
                    pass
    return prep_times, cycle_times

def main():
    print("=" * 80)
    print("🚀 BENCHMARK: HIỆU NĂNG ĐỒNG THUẬN RAFT (HASHICORP/RAFT 3-NODE QUORUM 2/3)")
    print("=" * 80)

    EVIDENCE_DIR.mkdir(parents=True, exist_ok=True)
    leader, secret_file, processes = setup_cluster()
    if leader is None:
        print("❌ Cannot proceed: cluster setup failed.")
        sys.exit(1)

    rpc_nodes_cfg = {
        "nodes": {n["id"]: f"http://127.0.0.1:{n['rpc']}" for n in NODES},
        "tcp_nodes": {n["id"]: f"127.0.0.1:{n['tcp']}" for n in NODES},
        "roles": {n["id"]: "leader" if n["id"] == leader["id"] else "follower" for n in NODES}
    }
    rpc_nodes_path = WORK_DIR / "rpc_nodes.json"
    rpc_nodes_path.write_text(json.dumps(rpc_nodes_cfg, indent=2))

    pids_arg = ",".join(str(p.pid) for _, p, _ in processes)

    workloads = [
        {"name": "Warmup_1000", "count": 1000, "batch": 500},
        {"name": "Medium_2000", "count": 2000, "batch": 500},
        {"name": "High_4000",   "count": 4000, "batch": 1000},
        {"name": "Extreme_8000","count": 8000, "batch": 1000},
    ]

    all_results = []

    try:
        for idx, w in enumerate(workloads, start=1):
            print(f"\n▶️ [WORKLOAD {idx}/{len(workloads)}] {w['name']}: {w['count']} txs (batch size {w['batch']})...")
            report_file = EVIDENCE_DIR / f"raft_bench_{w['name']}.json"

            cmd = [
                str(BIN_BLAST),
                "-nodes-config", str(rpc_nodes_path),
                "-rpc", f"http://127.0.0.1:{leader['rpc']}",
                "-tcp", f"127.0.0.1:{leader['tcp']}",
                "-mode", "tcp",
                "-type", "1559",
                "-count", str(w["count"]),
                "-batch", str(w["batch"]),
                "-keys", str(KEYS_FILE),
                "-pids", pids_arg,
                "-verify-parity",
                "-report", str(report_file),
            ]

            t_start = time.time()
            res = subprocess.run(cmd, capture_output=True, text=True)
            t_total = time.time() - t_start

            print(f"   Command completed in {t_total:.2f}s (exit code {res.returncode})")
            if res.returncode != 0:
                print(f"⚠️ Blast error output:\n{res.stderr}\n{res.stdout}")

            if report_file.exists():
                with open(report_file, "r") as rf:
                    bench_data = json.load(rf)
                eff_tps = bench_data.get("effective_tps", 0.0)
                inj_tps = bench_data.get("injection_tps", 0.0)
                zero_fork = bench_data.get("zero_fork_verified", False)
                p50 = bench_data.get("latency_p50", 0) / 1e6
                p99 = bench_data.get("latency_p99", 0) / 1e6
                blocks = bench_data.get("blocks_produced", 0)
                print(f"   📊 Effective TPS: {eff_tps:,.1f} | Injection: {inj_tps:,.1f} | Blocks: {blocks} | Latency P50: {p50:.1f}ms, P99: {p99:.1f}ms | Zero-Fork: {zero_fork}")
                all_results.append({
                    "workload": w["name"],
                    "count": w["count"],
                    "batch": w["batch"],
                    "effective_tps": eff_tps,
                    "injection_tps": inj_tps,
                    "blocks_produced": blocks,
                    "latency_p50_ms": p50,
                    "latency_p99_ms": p99,
                    "zero_fork_verified": zero_fork,
                    "raw_report": str(report_file.name)
                })
            else:
                print(f"⚠️ Report file not generated for {w['name']}")

            time.sleep(2)

        # Trích xuất số liệu latency từ log của Leader và Follower
        leader_log = WORK_DIR / leader["id"] / "node.log"
        prep_times, exec_times = extract_log_latencies(leader_log)
        avg_prep = sum(prep_times) / len(prep_times) if prep_times else 0.0
        avg_exec = sum(exec_times) / len(exec_times) if exec_times else 0.0

        summary = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "consensus_mode": "raft",
            "architecture": "3-Node Replicated HashiCorp Raft Cluster (Quorum 2/3)",
            "nodes": [n["id"] for n in NODES],
            "leader": leader["id"],
            "binary": str(BIN_SIMPLE_CHAIN),
            "prepare_transactions_parallel": True,
            "avg_prepare_tx_ms": avg_prep,
            "avg_block_exec_ms": avg_exec,
            "results": all_results
        }

        summary_file = EVIDENCE_DIR / "raft_consensus_benchmark_summary.json"
        summary_file.write_text(json.dumps(summary, indent=2))
        print(f"\n✅ Đã lưu kết quả tổng hợp Raft vào: {summary_file}")

    finally:
        kill_existing()
        for _, _, f in processes:
            try:
                f.close()
            except Exception:
                pass

if __name__ == "__main__":
    main()
