#!/usr/bin/env python3
"""
Diagnostic Script for Investigating RSS Drift across Benchmark Rounds.
Runs 3 fresh cluster rounds with detailed telemetry:
- PIDs alive before start / after stop
- Baseline RSS per node immediately after cluster start
- Periodic RSS samples (every 0.5s) per node during blast
- DB data size on disk
- Final Peak RSS comparison
"""

import json
import os
import re
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from stats_util import mean, sample_sd, spearman_correlation

BENCH_BASE = "/tmp/gate_4val_diagnostic"
TEMPLATE_BASE = "/tmp/gate_4val_clean_template"
BIN_DIR = "/tmp/p06_bins"
REPO_ROOT = "/home/abc/chain-n/metanode"
RUN_ENV = f"{REPO_ROOT}/execution/scripts/test/gate_e2e/run_env.sh"
BLAST_TOOL = "/tmp/b1_bins/secp_tps_blast"
KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
NODES = ["val0", "val1", "val2", "val3"]

def ensure_template():
    if not os.path.isdir(TEMPLATE_BASE):
        print("⚙️ Generating clean cluster template...")
        cmd = [
            sys.executable,
            f"{REPO_ROOT}/execution/scripts/test/gate_e2e/gen_env.py",
            "--base", TEMPLATE_BASE,
            "--bin", BIN_DIR,
            "--validators", "4",
            "--port-base", "31000",
            "--repo", REPO_ROOT
        ]
        res = subprocess.run(cmd, capture_output=True, text=True, check=False)
        if res.returncode != 0:
            print("Failed to generate template:", res.stderr)
            sys.exit(1)

def get_live_bench_pids():
    """Finds any simple_chain or consensus processes associated with BENCH_BASE."""
    pids = []
    try:
        out = subprocess.check_output(["ps", "-eo", "pid,rss,cmd"], text=True)
        for line in out.splitlines():
            if BENCH_BASE in line and ("simple_chain" in line or "metanode" in line):
                parts = line.strip().split()
                pids.append((int(parts[0]), int(parts[1]), parts[2]))
    except Exception:
        pass
    return pids

def stop_and_kill_bench():
    """Stops the cluster and forcefully kills any leftover processes to guarantee zero leakage."""
    if os.path.isdir(BENCH_BASE):
        subprocess.run([RUN_ENV, BENCH_BASE, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        time.sleep(1)

    # Check for lingering processes
    leftovers = get_live_bench_pids()
    if leftovers:
        print(f"⚠️ Found {len(leftovers)} lingering processes for {BENCH_BASE}. Force killing...")
        for pid, rss, cmd in leftovers:
            try:
                os.kill(pid, 9)
            except Exception:
                pass
        time.sleep(0.5)

    if os.path.isdir(BENCH_BASE):
        shutil.rmtree(BENCH_BASE, ignore_errors=True)

def setup_fresh_cluster():
    stop_and_kill_bench()
    shutil.copytree(TEMPLATE_BASE, BENCH_BASE)

def start_cluster():
    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "false"
    res = subprocess.run([RUN_ENV, BENCH_BASE, "start"], env=env, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        print("Failed to start cluster:", res.stderr)
        return False
    return True

def get_pids():
    pids = {}
    for n in NODES:
        pid_file = f"{BENCH_BASE}/pids/{n}.pid"
        if os.path.isfile(pid_file):
            with open(pid_file, "r") as f:
                pids[n] = int(f.read().strip())
    return pids

def read_proc_rss_kb(pid):
    """Reads exact Resident Set Size in KB from /proc/<pid>/status."""
    status_file = f"/proc/{pid}/status"
    if os.path.isfile(status_file):
        try:
            with open(status_file, "r") as f:
                for line in f:
                    if line.startswith("VmRSS:"):
                        parts = line.split()
                        return int(parts[1])  # in KB
        except Exception:
            pass
    return 0

def get_dir_size_mb(path):
    total = 0
    if os.path.isdir(path):
        for root, _, files in os.walk(path):
            for f in files:
                try:
                    total += os.path.getsize(os.path.join(root, f))
                except Exception:
                    pass
    return total / (1024.0 * 1024.0)

def main():
    print("==================================================================")
    print("🔬 RSS DRIFT DIAGNOSTIC: 3 FRESH CLUSTER ROUNDS")
    print("   Goal: Determine whether RSS baseline drifts or Peak RSS measurement accumulates")
    print("==================================================================")

    ensure_template()

    rounds = 3
    baseline_rss_records = []
    peak_rss_records = []
    blast_rss_records = []

    for r in range(1, rounds + 1):
        print(f"\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
        print(f"▶️ [ROUND {r}/{rounds}] Setting up FRESH cluster from template...")
        print(f"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

        # 1. Check live processes before start
        pre_live = get_live_bench_pids()
        print(f"   • Pre-start lingering bench processes: {len(pre_live)}")

        # 2. Setup fresh cluster
        setup_fresh_cluster()
        initial_data_size = get_dir_size_mb(f"{BENCH_BASE}/val0")
        print(f"   • Fresh val0 initial disk size: {initial_data_size:.2f} MB")

        # 3. Start cluster
        started = start_cluster()
        if not started:
            print("❌ Failed to start cluster in round", r)
            continue

        pids = get_pids()
        print(f"   • Cluster started with PIDs: {pids}")

        # 4. Measure Baseline RSS immediately after startup
        time.sleep(2)  # Wait for startup settle
        base_rss_per_node = {n: read_proc_rss_kb(pids[n]) / 1024.0 for n in NODES if n in pids}
        total_base_rss = sum(base_rss_per_node.values())
        baseline_rss_records.append(total_base_rss)
        print(f"   📊 Baseline Total RSS (after start): {total_base_rss:.1f} MB (per node: {base_rss_per_node})")

        # 5. Run Blast with live telemetry
        val_pids_str = ",".join(str(pids[n]) for n in NODES if n in pids)
        nodes_config = f"{BENCH_BASE}/rpc_nodes.json"
        cmd = [
            BLAST_TOOL,
            "-nodes-config", nodes_config,
            "-keys", KEYS_FILE,
            "-count", "25000",
            "-batch", "1000",
            "-mode", "tcp",
            "-type", "1559",
            "-verify-parity=false",
            "-pids", val_pids_str
        ]

        # Background sampler for proc RSS
        samples = []
        stop_sampling = False

        import threading
        def sampler_thread():
            while not stop_sampling:
                cur_sample = {n: read_proc_rss_kb(pids[n]) / 1024.0 for n in NODES if n in pids}
                samples.append((time.time(), sum(cur_sample.values())))
                time.sleep(0.5)

        st = threading.Thread(target=sampler_thread)
        st.start()

        t0 = time.time()
        res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, check=False)
        dur = time.time() - t0
        stop_sampling = True
        st.join()

        # Parse Peak RSS reported by blast tool
        tool_peak_rss = None
        m_rss = re.search(r"Peak RSS \(4 Target Nodes\):\s+(\d+)\s+MB", res.stdout)
        if m_rss:
            tool_peak_rss = int(m_rss.group(1))

        # Sample peak from /proc
        proc_peak_rss = max(s[1] for s in samples) if samples else total_base_rss
        post_data_size = get_dir_size_mb(f"{BENCH_BASE}/val0")

        peak_rss_records.append(tool_peak_rss)
        blast_rss_records.append(proc_peak_rss)

        print(f"   • Blast Duration: {dur:.2f}s")
        print(f"   • Blast Tool Reported Peak RSS: {tool_peak_rss} MB")
        print(f"   • /proc Sampled Peak RSS:       {proc_peak_rss:.1f} MB")
        print(f"   • Post-blast val0 disk size:    {post_data_size:.2f} MB")

        # 6. Stop and Clean
        stop_and_kill_bench()
        post_live = get_live_bench_pids()
        print(f"   • Post-stop lingering bench processes: {len(post_live)}")

    print("\n==================================================================")
    print("📊 DIAGNOSTIC SUMMARY ACROSS 3 ROUNDS:")
    print(f"   • Baseline Total RSS (Round 1..3): {baseline_rss_records}")
    if len(baseline_rss_records) >= 3:
        b1, b3 = baseline_rss_records[0], baseline_rss_records[2]
        b_pct = ((b3 - b1) / b1) * 100 if b1 > 0 else 0
        print(f"   • Baseline Drift (Round 3 vs Round 1): {b_pct:+.2f}% (R1={b1:.1f} MB, R3={b3:.1f} MB)")
    print(f"   • Tool Reported Peak RSS:           {peak_rss_records}")
    print(f"   • /proc Status Sampled Peak RSS:    {blast_rss_records}")
    if len(peak_rss_records) >= 2:
        rounds_idx = list(range(1, len(peak_rss_records) + 1))
        spearman_tool = spearman_correlation(rounds_idx, [float(x) for x in peak_rss_records])
        print(f"   • Spearman Correlation (Tool Peak): {spearman_tool:.4f}")
    print("==================================================================")

if __name__ == "__main__":
    main()
