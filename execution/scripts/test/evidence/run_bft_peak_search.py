#!/usr/bin/env python3
"""
run_bft_peak_search.py - Measure Peak Saturation Throughput for 4-Validator DAG BFT Cluster.

Tests scaling saturation workloads (10k -> 20k -> 25k -> 30k txs) on the 4-Validator BFT
cluster after Phase 2b PrepareTransactions parallelization optimization.
Verifies Zero-Fork Parity (Part 2.5) across all 4 nodes, measures block dynamics,
and compares directly against yesterday's 25k baseline (6,231 TPS).
"""

import os
import sys
import time
import json
import shutil
import subprocess
import urllib.request
from pathlib import Path

REPO_ROOT = Path("/home/abc/chain-n/metanode")
TEMPLATE_BASE = "/tmp/gate_4val_clean_template"
BENCH_BASE = "/tmp/gate_4val_p06"
RUN_ENV = str(REPO_ROOT / "execution/scripts/test/gate_e2e/run_env.sh")
BIN_BLAST = "/tmp/p06_bins/secp_tps_blast"
KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
EVIDENCE_DIR = REPO_ROOT / "note/evidence/tps_prepare_tx_opt_20261008"

VAL_NODES = [
    {"id": "val0", "rpc": 31646, "tcp": 31200},
    {"id": "val1", "rpc": 31647, "tcp": 31201},
    {"id": "val2", "rpc": 31648, "tcp": 31202},
    {"id": "val3", "rpc": 31649, "tcp": 31203},
]

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

def wait_for_all_rpc(timeout: float = 60.0) -> bool:
    t0 = time.time()
    while time.time() - t0 < timeout:
        all_ok = True
        for v in VAL_NODES:
            res, err = rpc_call(f"http://127.0.0.1:{v['rpc']}", "eth_blockNumber")
            if err is not None or res is None:
                all_ok = False
                break
        if all_ok:
            return True
        time.sleep(1.0)
    return False

def stop_and_kill():
    if os.path.isdir(BENCH_BASE):
        subprocess.run([RUN_ENV, BENCH_BASE, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        time.sleep(1)
    subprocess.run(["pkill", "-9", "-f", "simple_chain.*gate_4val_p06"], stderr=subprocess.DEVNULL)
    subprocess.run(["pkill", "-9", "-f", "parent_chain.*gate_4val_p06"], stderr=subprocess.DEVNULL)
    time.sleep(1)
    if os.path.isdir(BENCH_BASE):
        shutil.rmtree(BENCH_BASE, ignore_errors=True)

def setup_and_start_cluster():
    print("🧹 Cleaning up old cluster processes...")
    stop_and_kill()
    if not os.path.isdir(TEMPLATE_BASE):
        raise RuntimeError(f"Clean template base not found at {TEMPLATE_BASE}")
    print(f"📁 Replicating clean cluster from {TEMPLATE_BASE} to {BENCH_BASE}...")
    os.makedirs(BENCH_BASE, exist_ok=True)
    subprocess.run(["cp", "-a", "--reflink=auto", f"{TEMPLATE_BASE}/.", f"{BENCH_BASE}/"], check=True)

    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "false"
    print("🚀 Starting 4-validator DAG BFT cluster...")
    res = subprocess.run([RUN_ENV, BENCH_BASE, "start"], env=env, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        print(f"❌ Failed to start cluster:\n{res.stderr}\n{res.stdout}")
        return False

    print("⏳ Waiting for all 4 validators RPC endpoints to be ready...")
    if not wait_for_all_rpc(timeout=90.0):
        print("❌ Timeout waiting for 4 validators RPC")
        return False
    print("✅ All 4 validators DAG BFT online and synced!")
    time.sleep(3)
    return True

def get_cluster_pids():
    pids = []
    pid_dir = Path(BENCH_BASE) / "pids"
    if pid_dir.exists():
        for pf in pid_dir.glob("*.pid"):
            try:
                pids.append(pf.read_text().strip())
            except Exception:
                pass
    return ",".join(pids)

def main():
    print("=" * 80)
    print("🎯 KHẢO SÁT ĐỈNH THÔNG LƯỢNG (PEAK TPS) CỤM 4-VALIDATOR BFT (10K -> 30K TXS)")
    print("   Bản build: simple_chain HEAD (đã tối ưu song song hóa PrepareTransactions)")
    print("=" * 80)

    EVIDENCE_DIR.mkdir(parents=True, exist_ok=True)
    if not setup_and_start_cluster():
        print("❌ Cannot start BFT cluster.")
        sys.exit(1)

    rpc_nodes_file = Path(BENCH_BASE) / "rpc_nodes.json"
    pids_arg = get_cluster_pids()

    stages = [
        {"name": "Stage_10k", "count": 10000, "batch": 1000},
        {"name": "Stage_20k", "count": 20000, "batch": 1000},
        {"name": "Stage_25k", "count": 25000, "batch": 1000}, # Benchmark tương đương bài test hôm qua
        {"name": "Stage_30k", "count": 30000, "batch": 1000},
    ]

    results = []

    try:
        for idx, s in enumerate(stages, start=1):
            print(f"\n" + "-" * 70)
            print(f"🔥 [NẤC {idx}/{len(stages)}] Bơm {s['count']:,} txs EIP-1559 qua raw TCP vào 4 Validator (batch {s['batch']})...")
            print("-" * 70)
            report_file = EVIDENCE_DIR / f"bft_peak_{s['name']}.json"

            cmd = [
                BIN_BLAST,
                "-nodes-config", str(rpc_nodes_file),
                "-mode", "tcp",
                "-type", "1559",
                "-count", str(s["count"]),
                "-batch", str(s["batch"]),
                "-keys", KEYS_FILE,
                "-pids", pids_arg,
                "-verify-parity",
                "-report", str(report_file),
            ]

            t0 = time.time()
            res = subprocess.run(cmd, capture_output=True, text=True)
            t_cmd = time.time() - t0

            if res.returncode != 0:
                print(f"⚠️ Blast warning/error:\n{res.stderr}\n{res.stdout}")
            else:
                print(f"   Lệnh hoàn tất sau {t_cmd:.2f}s")

            if report_file.exists():
                with open(report_file, "r") as rf:
                    d = json.load(rf)
                eff_tps = d.get("effective_tps", 0.0)
                inj_tps = d.get("injection_tps", 0.0)
                dur = d.get("commit_duration", "N/A")
                blks = d.get("blocks_produced", 0)
                p50 = d.get("latency_p50", 0) / 1e6
                p99 = d.get("latency_p99", 0) / 1e6
                cpu = d.get("max_cpu", "N/A")
                rss = d.get("max_rss_mb", 0)
                zero_fork = d.get("zero_fork_verified", False)
                roots_match = d.get("node_roots_consistent", False)

                print(f"   ⚡ Effective TPS:    {eff_tps:,.2f} tx/s")
                print(f"   ⏱️ Commit Duration:  {dur}")
                print(f"   📦 Blocks Produced:  {blks} blocks (Trung bình ~{int(s['count']/max(1, blks)):,} txs/block)")
                print(f"   ⏳ Latency:          P50 = {p50:.1f}ms | P99 = {p99:.1f}ms")
                print(f"   💻 Peak Resource:    CPU = {cpu} | RSS = {rss:,} MB")
                print(f"   🛡️ Zero-Fork Parity: {zero_fork} (Roots Consistent: {roots_match})")

                results.append({
                    "stage": s["name"],
                    "count": s["count"],
                    "effective_tps": eff_tps,
                    "injection_tps": inj_tps,
                    "commit_duration": dur,
                    "blocks": blks,
                    "avg_txs_per_block": int(s['count'] / max(1, blks)),
                    "latency_p50_ms": p50,
                    "latency_p99_ms": p99,
                    "peak_cpu": cpu,
                    "peak_rss_mb": rss,
                    "zero_fork_verified": zero_fork and roots_match
                })
            else:
                print(f"⚠️ Report file not generated for {s['name']}")

            time.sleep(3)

        summary_file = EVIDENCE_DIR / "bft_peak_search_summary.json"
        summary_data = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "cluster": "4-Validator Mysticeti DAG BFT Cluster (Quorum 3/4)",
            "binary": "/tmp/p06_bins/simple_chain",
            "optimization": "Phase 2b Parallelized PrepareTransactions (Worker pool <= 16)",
            "stages": results
        }
        summary_file.write_text(json.dumps(summary_data, indent=2))
        print(f"\n🎉 Hoàn tất bài đo đỉnh BFT! Đã lưu: {summary_file}")

    finally:
        print("🧹 Cleaning up BFT cluster...")
        stop_and_kill()

if __name__ == "__main__":
    main()
