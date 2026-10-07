#!/usr/bin/env python3
"""
TTL & Heap Saturation Benchmark Runner (Stage 4).
Runs a controlled sustained transaction injection benchmark over 45 minutes
(or configurable duration) to observe whether HeapAlloc saturates after the
30-minute mappingCacheTTL window or continues to grow indefinitely.

Strict Anti-Fabrication:
- All HeapAlloc/HeapInuse metrics sampled live from /debug/pprof/heap?debug=1&gc=1.
- All RSS/PSS metrics sampled live from /proc/<pid>/status and smaps_rollup.
- Linear regression via stats_util.py to evaluate pre-specified saturation criteria.
"""

import argparse
import csv
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from stats_util import linear_regression, mean

BENCH_BASE = "/tmp/gate_4val_ttl_exp"
TEMPLATE_BASE = "/tmp/gate_4val_clean_template"
BIN_DIR = "/tmp/p06_bins"
REPO_ROOT = "/home/abc/chain-n/metanode"
RUN_ENV = f"{REPO_ROOT}/execution/scripts/test/gate_e2e/run_env.sh"
BLAST_TOOL = "/tmp/b1_bins/secp_tps_blast"
KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
EVIDENCE_DIR = os.path.join(REPO_ROOT, "note/evidence/perf_rss_20261007")
NODES = ["val0", "val1", "val2", "val3"]
RPC_PORTS = {"val0": 31646, "val1": 31647, "val2": 31648, "val3": 31649}
PPROF_PORTS = {n: RPC_PORTS[n] - 200 for n in NODES}

def get_live_bench_pids():
    """Finds any simple_chain processes associated with BENCH_BASE."""
    pids = []
    try:
        out = subprocess.check_output(["ps", "-eo", "pid,rss,cmd"], text=True)
        for line in out.splitlines():
            if BENCH_BASE in line and "simple_chain" in line:
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
    if not os.path.isdir(TEMPLATE_BASE):
        raise RuntimeError(f"Template base not found at {TEMPLATE_BASE}")
    shutil.copytree(TEMPLATE_BASE, BENCH_BASE)

def start_cluster():
    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "true"
    res = subprocess.run([RUN_ENV, BENCH_BASE, "start"], env=env, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        print("Failed to start cluster:", res.stderr, file=sys.stderr)
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

def fetch_pprof_memstats(pprof_port):
    """Fetches memstats with explicit GC forced via debug=1&gc=1."""
    url = f"http://127.0.0.1:{pprof_port}/debug/pprof/heap?debug=1&gc=1"
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=10) as resp:
            content = resp.read().decode("utf-8")
        
        stats = {}
        for line in content.splitlines():
            line = line.strip()
            if line.startswith("#"):
                parts = line[1:].strip().split("=")
                if len(parts) == 2:
                    k, v = parts[0].strip(), parts[1].strip()
                    try:
                        stats[k] = int(v.split()[0])
                    except ValueError:
                        pass
        return stats
    except Exception as e:
        return {}

def fetch_smaps_rollup(pid):
    path = f"/proc/{pid}/smaps_rollup"
    if not os.path.isfile(path):
        return {}
    try:
        with open(path, "r") as f:
            content = f.read()
        stats = {}
        for line in content.splitlines():
            parts = line.split(":")
            if len(parts) == 2:
                k = parts[0].strip()
                v = parts[1].strip().split()[0]
                try:
                    stats[k] = int(v)
                except ValueError:
                    pass
        return stats
    except Exception:
        return {}

def read_proc_rss_kb(pid):
    status_file = f"/proc/{pid}/status"
    if os.path.isfile(status_file):
        try:
            with open(status_file, "r") as f:
                for line in f:
                    if line.startswith("VmRSS:"):
                        parts = line.split()
                        return int(parts[1])
        except Exception:
            pass
    return 0

def run_experiment(duration_sec, sample_interval_sec, rate_limit, output_prefix):
    os.makedirs(EVIDENCE_DIR, exist_ok=True)
    json_path = os.path.join(EVIDENCE_DIR, f"{output_prefix}_timeseries.json")
    csv_path = os.path.join(EVIDENCE_DIR, f"{output_prefix}_summary.csv")
    log_path = os.path.join(EVIDENCE_DIR, f"{output_prefix}.log")

    print(f"🚀 Starting TTL Saturation Experiment:")
    print(f"   Duration: {duration_sec}s ({duration_sec / 60.0:.1f} mins)")
    print(f"   Sample interval: {sample_interval_sec}s")
    print(f"   Rate limit: {rate_limit} tx/s")
    print(f"   Output JSON: {json_path}")
    print(f"   Output CSV:  {csv_path}")

    setup_fresh_cluster()
    if not start_cluster():
        sys.exit(1)

    pids = get_pids()
    print(f"   Cluster active with PIDs: {pids}")

    # Launch background blast tool
    # /tmp/b1_bins/secp_tps_blast -duration <sec> -rate-limit <rate> -mode tcp -batch 100 ...
    tcp_endpoints = [f"127.0.0.1:{31200 + i}" for i in range(4)]
    blast_cmd = [
        BLAST_TOOL,
        "-duration", str(duration_sec),
        "-rate-limit", str(rate_limit),
        "-mode", "tcp",
        "-batch", "100",
        "-tcp", ",".join(tcp_endpoints),
        "-keys", KEYS_FILE,
        "-pids", ",".join(str(pids[n]) for n in NODES if n in pids)
    ]

    print(f"   Starting blast generator...")
    blast_proc = subprocess.Popen(blast_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

    start_time = time.time()
    samples = []
    sample_index = 0

    try:
        while True:
            elapsed = time.time() - start_time
            if elapsed > duration_sec + 5:
                break

            # Poll each node
            sample_record = {
                "sample_idx": sample_index,
                "elapsed_sec": round(elapsed, 2),
                "timestamp": time.time(),
                "nodes": {}
            }

            tot_heap_alloc = 0
            tot_heap_inuse = 0
            tot_rss_kb = 0

            for n in NODES:
                port = PPROF_PORTS[n]
                pid = pids.get(n, 0)
                mstats = fetch_pprof_memstats(port)
                smaps = fetch_smaps_rollup(pid)
                rss_kb = read_proc_rss_kb(pid)

                heap_alloc = mstats.get("HeapAlloc", 0)
                heap_inuse = mstats.get("HeapInuse", 0)
                tot_heap_alloc += heap_alloc
                tot_heap_inuse += heap_inuse
                tot_rss_kb += rss_kb

                sample_record["nodes"][n] = {
                    "HeapAlloc": heap_alloc,
                    "HeapInuse": heap_inuse,
                    "HeapIdle": mstats.get("HeapIdle", 0),
                    "HeapReleased": mstats.get("HeapReleased", 0),
                    "NumGC": mstats.get("NumGC", 0),
                    "VmRSS_KB": rss_kb,
                    "smaps_pss_kb": smaps.get("Pss", 0)
                }

            sample_record["total_heap_alloc_mb"] = round(tot_heap_alloc / (1024.0 * 1024.0), 3)
            sample_record["total_heap_inuse_mb"] = round(tot_heap_inuse / (1024.0 * 1024.0), 3)
            sample_record["total_rss_mb"] = round(tot_rss_kb / 1024.0, 3)

            samples.append(sample_record)
            print(f"   [{elapsed:6.1f}s / {duration_sec}s] Total HeapAlloc: {sample_record['total_heap_alloc_mb']:8.2f} MB | RSS: {sample_record['total_rss_mb']:8.2f} MB")

            sample_index += 1
            time.sleep(sample_interval_sec)

    finally:
        print("   Terminating blast and stopping cluster...")
        if blast_proc.poll() is None:
            blast_proc.terminate()
            try:
                blast_proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                blast_proc.kill()
        stop_and_kill_bench()

    # Save raw timeseries JSON
    with open(json_path, "w") as f:
        json.dump(samples, f, indent=2)

    # Save summary CSV
    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["sample_idx", "elapsed_sec", "total_heap_alloc_mb", "total_heap_inuse_mb", "total_rss_mb"])
        for s in samples:
            writer.writerow([s["sample_idx"], s["elapsed_sec"], s["total_heap_alloc_mb"], s["total_heap_inuse_mb"], s["total_rss_mb"]])

    # Regression analysis
    # Window 1: first 30 minutes (0 <= elapsed <= 1800s)
    # Window 2: last 15 minutes (elapsed >= 1800s)
    # If duration < 1800, split 2/3 and 1/3
    split_point = 1800.0 if duration_sec >= 2700 else (duration_sec * (2.0 / 3.0))

    w1_x = [s["elapsed_sec"] for s in samples if s["elapsed_sec"] <= split_point]
    w1_y = [s["total_heap_alloc_mb"] for s in samples if s["elapsed_sec"] <= split_point]

    w2_x = [s["elapsed_sec"] for s in samples if s["elapsed_sec"] > split_point]
    w2_y = [s["total_heap_alloc_mb"] for s in samples if s["elapsed_sec"] > split_point]

    slope1, _, r2_1, ci1, _ = linear_regression(w1_x, w1_y, scale=60.0)  # MB per minute
    slope2, _, r2_2, ci2, _ = linear_regression(w2_x, w2_y, scale=60.0) if len(w2_x) >= 3 else (0.0, 0.0, 0.0, (0.0, 0.0), 0.0)

    ratio = (slope2 / slope1) if slope1 > 0 else 0.0
    saturated = (slope2 <= 0.05 * slope1) or (slope2 <= 0.5)

    verdict = "PASS (SATURATED)" if saturated else "FAIL (UNBOUNDED)"
    if len(w2_x) < 3:
        verdict = "INCONCLUSIVE (INSUFFICIENT_WINDOW2_SAMPLES)"

    summary_text = (
        f"TTL Saturation Analysis (Split at {split_point}s):\n"
        f"Window 1 (0 -> {split_point}s): slope = {slope1:.4f} MB/min (R2 = {r2_1:.4f})\n"
        f"Window 2 ({split_point}s -> {duration_sec}s): slope = {slope2:.4f} MB/min (R2 = {r2_2:.4f})\n"
        f"Slope ratio (Window 2 / Window 1): {ratio * 100.0:.2f}%\n"
        f"Verdict: {verdict}\n"
    )
    print(summary_text)

    with open(log_path, "w") as f:
        f.write(summary_text)

    return samples, verdict

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--duration", type=int, default=2700, help="Duration in seconds (default: 2700s = 45m)")
    parser.add_argument("--interval", type=int, default=60, help="Sampling interval in seconds (default: 60s)")
    parser.add_argument("--rate", type=int, default=50, help="Blast rate limit in tx/s (default: 50)")
    parser.add_argument("--output", type=str, default="ttl_45min", help="Output prefix")
    args = parser.parse_args()

    run_experiment(args.duration, args.interval, args.rate, args.output)
