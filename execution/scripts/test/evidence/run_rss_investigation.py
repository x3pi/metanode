#!/usr/bin/env python3
"""
RSS & Memory Investigation Benchmark Runner (Stage 3).
Executes:
1. 8 Waves of 50,000 SECP transactions (total 400,000 txs) with raw heap snapshots (.pb.gz),
   memstats, smaps_rollup, linear regression analysis, and pprof diff.
2. Controlled GOGC comparison (default 800 vs GOGC=50) under IDENTICAL 25,000 tx workloads.
Strict Anti-Fabrication: All numbers derived from live kernel and Go runtime stats.
"""

import gzip
import json
import math
import os
import re
import subprocess
import sys
import time
import urllib.request

BASE_DIR = "/tmp/gate_4val_p06"
ENV_JSON = f"{BASE_DIR}/env.json"
BLAST_TOOL = "/tmp/b1_bins/secp_tps_blast"
KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
NODES_CONFIG = f"{BASE_DIR}/rpc_nodes.json"
EVIDENCE_DIR = "note/evidence/perf_rss_20261007"
os.makedirs(EVIDENCE_DIR, exist_ok=True)

with open(ENV_JSON, "r") as f:
    env_data = json.load(f)

NODES = ["val0", "val1", "val2", "val3"]
RPC_PORTS = {n: env_data["ports"][n]["rpc"] for n in NODES}
PPROF_PORTS = {n: RPC_PORTS[n] - 200 for n in NODES}

def get_pids():
    pids = {}
    for n in NODES:
        pid_file = f"{BASE_DIR}/pids/{n}.pid"
        if os.path.isfile(pid_file):
            with open(pid_file, "r") as f:
                pids[n] = int(f.read().strip())
    return pids

def get_memstats(pprof_port):
    url = f"http://127.0.0.1:{pprof_port}/debug/pprof/heap?debug=1&gc=1"
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=5) as resp:
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
        return stats, content
    except Exception as e:
        return {}, str(e)

def download_heap_profile(pprof_port, out_path):
    url = f"http://127.0.0.1:{pprof_port}/debug/pprof/heap?gc=1"
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=5) as resp:
            data = resp.read()
        with open(out_path, "wb") as f:
            f.write(data)
        return True
    except Exception as e:
        print(f"   ❌ Failed to download heap profile: {e}", file=sys.stderr)
        return False

def get_smaps_rollup(pid):
    path = f"/proc/{pid}/smaps_rollup"
    if not os.path.isfile(path):
        return {}, ""
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
        return stats, content
    except Exception as e:
        return {}, str(e)

def run_blast(count, batch=1000):
    pids = get_pids()
    val_pids_str = ",".join(str(pids[n]) for n in NODES if n in pids)
    cmd = [
        BLAST_TOOL,
        "-nodes-config", NODES_CONFIG,
        "-keys", KEYS_FILE,
        "-count", str(count),
        "-batch", str(batch),
        "-mode", "tcp",
        "-type", "1559",
        "-verify-parity=false",
        "-pids", val_pids_str
    ]
    t0 = time.time()
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, check=False)
    duration = time.time() - t0

    # Parse TPS
    tps = None
    m = re.search(r"Effective TPS:\s+([\d\.]+)\s+tx/s", res.stdout)
    if m:
        tps = float(m.group(1))

    # Parse Peak RSS
    peak_rss = None
    m_rss = re.search(r"Peak RSS \(4 Target Nodes\):\s+(\d+)\s+MB", res.stdout)
    if m_rss:
        peak_rss = int(m_rss.group(1))

    return tps, peak_rss, duration, res.stdout

def linear_regression(x, y):
    n = len(x)
    if n < 2:
        return 0, 0, 0, 0
    mean_x = sum(x) / n
    mean_y = sum(y) / n
    ss_xx = sum((xi - mean_x) ** 2 for xi in x)
    ss_xy = sum((xi - mean_x) * (yi - mean_y) for xi, yi in zip(x, y))
    ss_yy = sum((yi - mean_y) ** 2 for yi in y)
    
    if ss_xx == 0:
        return 0, 0, 0, 0
    slope = ss_xy / ss_xx
    intercept = mean_y - slope * mean_x
    r2 = (ss_xy ** 2) / (ss_xx * ss_yy) if ss_yy > 0 else 0
    
    # Standard error of slope
    residuals = [yi - (slope * xi + intercept) for xi, yi in zip(x, y)]
    s_err = math.sqrt(sum(r ** 2 for r in residuals) / (n - 2)) if n > 2 else 0
    se_slope = s_err / math.sqrt(ss_xx) if ss_xx > 0 else 0
    return slope, intercept, r2, se_slope

def set_node_gc_percent(val):
    for n in NODES:
        cfg_file = f"{BASE_DIR}/{n}/config.json"
        with open(cfg_file, "r") as f:
            cfg = json.load(f)
        if val is None or val == 0:
            cfg.pop("go_gc_percent", None)
        else:
            cfg["go_gc_percent"] = val
        with open(cfg_file, "w") as f:
            json.dump(cfg, f, indent=2)

def restart_cluster():
    run_env = "/home/abc/chain-n/metanode/execution/scripts/test/gate_e2e/run_env.sh"
    subprocess.run([run_env, BASE_DIR, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    time.sleep(2)
    subprocess.run([run_env, BASE_DIR, "start"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    time.sleep(3)

def main():
    print("==================================================================")
    print("🔬 GIAI ĐOẠN 3: ĐO ĐẠC RSS & BỘ NHỚ NOMT QUA 8 ĐỢT TẢI (400,000 TXS)")
    print(f"   Base Directory: {BASE_DIR}")
    print(f"   Evidence Dir:   {EVIDENCE_DIR}")
    print("   Acceptance:     Linear regression slope <= 30 MB / 100k txs")
    print("==================================================================")

    pids = get_pids()
    print(f"📌 Active PIDs: {pids}")

    # Baseline measurement (Wave 0)
    print("\n📊 [1/4] Capturing Baseline (0 txs)...")
    baseline_records = []
    for n in NODES:
        port = PPROF_PORTS[n]
        pid = pids[n]
        download_heap_profile(port, f"{EVIDENCE_DIR}/heap_wave_0_{n}.pb.gz")
        mstats, mstats_raw = get_memstats(port)
        smaps, smaps_raw = get_smaps_rollup(pid)
        with open(f"{EVIDENCE_DIR}/smaps_wave_0_{n}.txt", "w") as f:
            f.write(smaps_raw)
        with open(f"{EVIDENCE_DIR}/memstats_wave_0_{n}.txt", "w") as f:
            f.write(mstats_raw)
        
        alloc_mb = mstats.get("HeapAlloc", 0) / (1024 * 1024)
        inuse_mb = mstats.get("HeapInuse", 0) / (1024 * 1024)
        sys_mb = mstats.get("HeapSys", 0) / (1024 * 1024)
        rss_mb = smaps.get("Rss", 0) / 1024.0
        baseline_records.append((n, alloc_mb, inuse_mb, sys_mb, rss_mb))
        print(f"   • {n} (PID {pid}): Alloc={alloc_mb:.1f} MB, Inuse={inuse_mb:.1f} MB, Sys={sys_mb:.1f} MB, RSS={rss_mb:.1f} MB")

    # 8 Waves of 50,000 transactions each
    print("\n🚀 [2/4] Executing 8 Waves of 50,000 SECP Transactions (400k total)...")
    wave_data = [] # list of (wave_num, cum_txs, {node: (alloc, inuse, sys, rss)})

    for w in range(1, 9):
        cum_txs = w * 50_000
        print(f"\n🌊 Wave {w}/8 (+50,000 txs -> Cumulative: {cum_txs:,} txs)...")
        tps, peak_rss, dur, blast_log = run_blast(50000, batch=1000)
        print(f"   • Workload completed in {dur:.2f}s (TPS: {tps:.1f} tx/s, Peak RSS: {peak_rss} MB)")
        time.sleep(2)

        node_stats = {}
        for n in NODES:
            port = PPROF_PORTS[n]
            pid = pids[n]
            download_heap_profile(port, f"{EVIDENCE_DIR}/heap_wave_{w}_{n}.pb.gz")
            mstats, mstats_raw = get_memstats(port)
            smaps, smaps_raw = get_smaps_rollup(pid)
            with open(f"{EVIDENCE_DIR}/smaps_wave_{w}_{n}.txt", "w") as f:
                f.write(smaps_raw)
            with open(f"{EVIDENCE_DIR}/memstats_wave_{w}_{n}.txt", "w") as f:
                f.write(mstats_raw)

            alloc_mb = mstats.get("HeapAlloc", 0) / (1024 * 1024)
            inuse_mb = mstats.get("HeapInuse", 0) / (1024 * 1024)
            sys_mb = mstats.get("HeapSys", 0) / (1024 * 1024)
            rss_mb = smaps.get("Rss", 0) / 1024.0
            node_stats[n] = (alloc_mb, inuse_mb, sys_mb, rss_mb)
            print(f"     - {n}: Alloc={alloc_mb:.1f} MB | Inuse={inuse_mb:.1f} MB | RSS={rss_mb:.1f} MB")
        
        wave_data.append((w, cum_txs, node_stats))

    # Linear Regression Analysis
    print("\n📈 [3/4] Linear Regression & Pprof Diff Analysis:")
    x_txs = [d[1] for d in wave_data]
    mean_allocs = [sum(d[2][n][0] for n in NODES) / len(NODES) for d in wave_data]
    mean_rss = [sum(d[2][n][3] for n in NODES) / len(NODES) for d in wave_data]

    slope_alloc, int_alloc, r2_alloc, se_alloc = linear_regression(x_txs, mean_allocs)
    slope_rss, int_rss, r2_rss, se_rss = linear_regression(x_txs, mean_rss)

    slope_alloc_100k = slope_alloc * 100_000
    se_alloc_100k = se_alloc * 100_000
    slope_rss_100k = slope_rss * 100_000

    print("==================================================================")
    print(f"📊 HeapAlloc Regression: Slope = {slope_alloc_100k:+.2f} ± {se_alloc_100k * 2.447:.2f} MB / 100k txs (R² = {r2_alloc:.3f})")
    print(f"📊 RSS Regression:       Slope = {slope_rss_100k:+.2f} MB / 100k txs (R² = {r2_rss:.3f})")

    # Run pprof -base on wave 1 vs wave 8 for val0
    pprof_cmd = [
        "go", "tool", "pprof", "-top", "-base",
        f"{EVIDENCE_DIR}/heap_wave_1_val0.pb.gz",
        f"{EVIDENCE_DIR}/heap_wave_8_val0.pb.gz"
    ]
    pprof_res = subprocess.run(pprof_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    with open(f"{EVIDENCE_DIR}/pprof_diff_wave1_to_8_val0.txt", "w") as f:
        f.write(pprof_res.stdout)
    
    print("\n🔍 Top InUse Memory Changes from Wave 1 to Wave 8 (pprof -base):")
    for l in pprof_res.stdout.splitlines()[:12]:
        print(f"   {l}")

    # Controlled GOGC Experiment (Default vs GOGC=50 on SAME 25,000 tx workload)
    print("\n⚙️ [4/4] Controlled GOGC Comparison (Default 800 vs GOGC=50 on 25k txs)...")
    gogc_results = {"default": [], "gogc50": []}

    # Measure default GOGC (3 runs)
    print("\n▶️ Running 3 Trials under Default GOGC (800)...")
    for r in range(1, 4):
        tps, peak_rss, dur, _ = run_blast(25000, batch=1000)
        gogc_results["default"].append((tps, peak_rss, dur))
        print(f"   • Default Trial {r}: TPS = {tps:.1f} tx/s | Peak RSS = {peak_rss} MB | Duration = {dur:.2f}s")
        time.sleep(1)

    # Reconfigure nodes to GOGC=50 and restart
    print("\n🔄 Reconfiguring cluster with GOGC=50 and restarting...")
    set_node_gc_percent(50)
    restart_cluster()
    pids_gogc50 = get_pids()
    print(f"📌 Active PIDs after GOGC=50 restart: {pids_gogc50}")

    print("\n▶️ Running 3 Trials under GOGC=50...")
    for r in range(1, 4):
        tps, peak_rss, dur, _ = run_blast(25000, batch=1000)
        gogc_results["gogc50"].append((tps, peak_rss, dur))
        print(f"   • GOGC=50 Trial {r}: TPS = {tps:.1f} tx/s | Peak RSS = {peak_rss} MB | Duration = {dur:.2f}s")
        time.sleep(1)

    # Revert config back to default
    print("\n🔄 Reverting cluster to default GOGC and restarting...")
    set_node_gc_percent(None)
    restart_cluster()

    # Summarize GOGC
    def_tps = [x[0] for x in gogc_results["default"]]
    def_rss = [x[1] for x in gogc_results["default"]]
    g50_tps = [x[0] for x in gogc_results["gogc50"]]
    g50_rss = [x[1] for x in gogc_results["gogc50"]]

    m_def_tps, sd_def_tps = sum(def_tps)/len(def_tps), math.sqrt(sum((x-sum(def_tps)/len(def_tps))**2 for x in def_tps)/2)
    m_g50_tps, sd_g50_tps = sum(g50_tps)/len(g50_tps), math.sqrt(sum((x-sum(g50_tps)/len(g50_tps))**2 for x in g50_tps)/2)
    m_def_rss, sd_def_rss = sum(def_rss)/len(def_rss), math.sqrt(sum((x-sum(def_rss)/len(def_rss))**2 for x in def_rss)/2)
    m_g50_rss, sd_g50_rss = sum(g50_rss)/len(g50_rss), math.sqrt(sum((x-sum(g50_rss)/len(g50_rss))**2 for x in g50_rss)/2)

    tps_drop_pct = ((m_g50_tps - m_def_tps) / m_def_tps) * 100
    rss_drop_pct = ((m_g50_rss - m_def_rss) / m_def_rss) * 100

    print("\n==================================================================")
    print("📊 GOGC Comparison Results (Same 25k TX Workload, n=3):")
    print(f"   • Default GOGC: TPS = {m_def_tps:.1f} ± {sd_def_tps:.1f} tx/s | Peak RSS = {m_def_rss:.0f} ± {sd_def_rss:.0f} MB")
    print(f"   • GOGC=50:      TPS = {m_g50_tps:.1f} ± {sd_g50_tps:.1f} tx/s | Peak RSS = {m_g50_rss:.0f} ± {sd_g50_rss:.0f} MB")
    print(f"   • TPS Impact:   {tps_drop_pct:+.1f}%")
    print(f"   • RSS Impact:   {rss_drop_pct:+.1f}%")
    print("==================================================================")

    # Save summary table CSV
    csv_path = f"{EVIDENCE_DIR}/rss_8waves_summary.csv"
    with open(csv_path, "w") as f:
        f.write("Wave,CumTxs,Node,HeapAllocMB,HeapInuseMB,HeapSysMB,RssMB\n")
        for n, alloc, inuse, sys_mb, rss in baseline_records:
            f.write(f"0,0,{n},{alloc:.2f},{inuse:.2f},{sys_mb:.2f},{rss:.2f}\n")
        for w, cum_txs, nstats in wave_data:
            for n in NODES:
                alloc, inuse, sys_mb, rss = nstats[n]
                f.write(f"{w},{cum_txs},{n},{alloc:.2f},{inuse:.2f},{sys_mb:.2f},{rss:.2f}\n")
    print(f"\n✅ Summary CSV saved to {csv_path}")

if __name__ == "__main__":
    main()
