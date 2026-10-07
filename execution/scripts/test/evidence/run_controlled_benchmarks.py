#!/usr/bin/env python3
"""
Controlled Benchmarks Runner for Phase 4:
1. GOGC Comparison: Default 800 vs GOGC=50 (7 alternating rounds each on FRESH clusters).
2. Debug Flag Comparison: ENABLE_DEBUG_PPROF=true vs false (7 alternating rounds each on FRESH clusters).
Strict Anti-Fabrication Protocol: All metrics logged, fresh state per round, Welch's t-test computed.
"""

import argparse
import csv
import json
import math
import os
import re
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from stats_util import mean, sample_sd, welch_t_test, t_critical, spearman_correlation

BENCH_BASE = "/tmp/gate_4val_controlled_bench"
TEMPLATE_BASE = "/tmp/gate_4val_clean_template"
BIN_DIR = "/tmp/p06_bins"
REPO_ROOT = "/home/abc/chain-n/metanode"
RUN_ENV = f"{REPO_ROOT}/execution/scripts/test/gate_e2e/run_env.sh"
BLAST_TOOL = "/tmp/b1_bins/secp_tps_blast"
KEYS_FILE = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
EVIDENCE_DIR = f"{REPO_ROOT}/note/evidence/perf_rss_20261007"
os.makedirs(EVIDENCE_DIR, exist_ok=True)

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

def stop_and_clean_bench():
    if os.path.isdir(BENCH_BASE):
        subprocess.run([RUN_ENV, BENCH_BASE, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        time.sleep(1)
        shutil.rmtree(BENCH_BASE, ignore_errors=True)

def setup_fresh_cluster(gogc=None):
    stop_and_clean_bench()
    shutil.copytree(TEMPLATE_BASE, BENCH_BASE)
    if gogc is not None:
        for n in NODES:
            cfg_path = f"{BENCH_BASE}/{n}/config.json"
            with open(cfg_path, "r") as f:
                cfg = json.load(f)
            if gogc == 800:
                cfg.pop("go_gc_percent", None)  # default is 800 in code
            else:
                cfg["go_gc_percent"] = gogc
            with open(cfg_path, "w") as f:
                json.dump(cfg, f, indent=2)

def start_cluster(enable_debug=True):
    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "true" if enable_debug else "false"
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

def get_cpu_ticks(pids):
    total_ticks = 0
    for pid in pids.values():
        stat_file = f"/proc/{pid}/stat"
        if os.path.isfile(stat_file):
            try:
                with open(stat_file, "r") as f:
                    parts = f.read().split()
                    # utime is 14th (index 13), stime is 15th (index 14)
                    utime = int(parts[13])
                    stime = int(parts[14])
                    total_ticks += (utime + stime)
            except Exception:
                pass
    return total_ticks

def get_log_size_kb():
    total_bytes = 0
    # 1. Check $BASE/logs (run_env.sh redirects stdout/stderr to $BASE/logs/<name>.log)
    base_log_dir = f"{BENCH_BASE}/logs"
    if os.path.isdir(base_log_dir):
        for fname in os.listdir(base_log_dir):
            fpath = os.path.join(base_log_dir, fname)
            if os.path.isfile(fpath):
                try:
                    total_bytes += os.path.getsize(fpath)
                except Exception:
                    pass
    # 2. Also check node-specific logs if created
    for n in NODES:
        log_dir = f"{BENCH_BASE}/{n}/logs"
        if os.path.isdir(log_dir):
            for root, _, files in os.walk(log_dir):
                for f in files:
                    try:
                        total_bytes += os.path.getsize(os.path.join(root, f))
                    except Exception:
                        pass
    return total_bytes / 1024.0

def run_blast(tx_count=25000, batch=1000):
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
    t0 = time.time()
    cpu_ticks_0 = get_cpu_ticks(pids)
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, check=False)
    duration = time.time() - t0
    cpu_ticks_1 = get_cpu_ticks(pids)
    # Clock ticks per sec is usually 100 on Linux (sysconf(_SC_CLK_TCK))
    clk_tck = os.sysconf(os.sysconf_names.get('SC_CLK_TCK', 100)) if hasattr(os, 'sysconf') else 100
    cpu_seconds = (cpu_ticks_1 - cpu_ticks_0) / clk_tck if cpu_ticks_1 >= cpu_ticks_0 else 0.0
    log_size_kb = get_log_size_kb()

    tps = None
    m = re.search(r"Effective TPS:\s+([\d\.]+)\s+tx/s", res.stdout)
    if m:
        tps = float(m.group(1))

    peak_rss = None
    m_rss = re.search(r"Peak RSS \(4 Target Nodes\):\s+(\d+)\s+MB", res.stdout)
    if m_rss:
        peak_rss = int(m_rss.group(1))

    return tps, peak_rss, duration, cpu_seconds, log_size_kb

def mean_sd(vals):
    return mean(vals), sample_sd(vals)

def main():
    parser = argparse.ArgumentParser(description="Controlled Benchmarks for GOGC and Debug Flag")
    parser.add_argument("--rounds", type=int, default=7, help="Number of interleaved rounds (default: 7)")
    parser.add_argument("--test", choices=["all", "gogc", "debug"], default="all", help="Which benchmark to run")
    parser.add_argument("--output-csv", default=None, help="Custom output CSV path")
    args = parser.parse_args()

    rounds = args.rounds

    print("==================================================================")
    print("🔬 BENCHMARK ĐỐI CHỨNG CÓ KIỂM SOÁT (GOGC & DEBUG FLAG)")
    print("   Nguyên tắc: Mỗi lượt chạy trên CLUSTER MỚI (fresh genesis)")
    print(f"   Lượt đo:    {rounds} lượt xen kẽ mỗi nhánh")
    print("   Workload:   25,000 transactions Secp256k1 EIP-1559, batch 1,000")
    print("==================================================================")

    ensure_template()

    gogc_results = {"Default": [], "GOGC50": []}
    gogc_rss = {"Default": [], "GOGC50": []}
    gogc_cpu = {"Default": [], "GOGC50": []}
    gogc_log = {"Default": [], "GOGC50": []}
    gogc_dur = {"Default": [], "GOGC50": []}

    # ---------------- 4.1. GOGC Benchmark ----------------
    if args.test in ["all", "gogc"]:
        print(f"\n▶️ [1/2] Chạy Thực Nghiệm GOGC: Mặc Định (800) vs GOGC=50 ({rounds} lượt xen kẽ)...")
        for r in range(1, rounds + 1):
            # Default run
            print(f"\n   [Lượt {r}/{rounds}] Cấu hình: GOGC Mặc Định (800)...")
            setup_fresh_cluster(gogc=800)
            if start_cluster(enable_debug=True):
                tps, rss, dur, cpu_s, log_kb = run_blast()
                print(f"      • Default #{r}: TPS={tps:.1f} tx/s, Peak RSS={rss} MB, CPU={cpu_s:.1f}s, Log={log_kb:.1f}KB, Dur={dur:.2f}s")
                if tps and rss:
                    gogc_results["Default"].append(tps)
                    gogc_rss["Default"].append(rss)
                    gogc_cpu["Default"].append(cpu_s)
                    gogc_log["Default"].append(log_kb)
                    gogc_dur["Default"].append(dur)
            stop_and_clean_bench()

            # GOGC=50 run
            print(f"   [Lượt {r}/{rounds}] Cấu hình: GOGC=50...")
            setup_fresh_cluster(gogc=50)
            if start_cluster(enable_debug=True):
                tps, rss, dur, cpu_s, log_kb = run_blast()
                print(f"      • GOGC50 #{r}: TPS={tps:.1f} tx/s, Peak RSS={rss} MB, CPU={cpu_s:.1f}s, Log={log_kb:.1f}KB, Dur={dur:.2f}s")
                if tps and rss:
                    gogc_results["GOGC50"].append(tps)
                    gogc_rss["GOGC50"].append(rss)
                    gogc_cpu["GOGC50"].append(cpu_s)
                    gogc_log["GOGC50"].append(log_kb)
                    gogc_dur["GOGC50"].append(dur)
            stop_and_clean_bench()

        # Stats for GOGC
        def_tps_m, def_tps_sd = mean_sd(gogc_results["Default"])
        g50_tps_m, g50_tps_sd = mean_sd(gogc_results["GOGC50"])
        def_rss_m, def_rss_sd = mean_sd(gogc_rss["Default"])
        g50_rss_m, g50_rss_sd = mean_sd(gogc_rss["GOGC50"])

        t_stat_tps, df_tps, p_tps, ci_tps = welch_t_test(gogc_results["Default"], gogc_results["GOGC50"])
        t_stat_rss, df_rss, p_rss, ci_rss = welch_t_test(gogc_rss["Default"], gogc_rss["GOGC50"])

        pct_diff_tps = ((g50_tps_m - def_tps_m) / def_tps_m) * 100
        pct_diff_rss = ((g50_rss_m - def_rss_m) / def_rss_m) * 100

        print("\n==================================================================")
        print(f"📊 KẾT QUẢ THỐNG KÊ GOGC (Fresh State, n={rounds} mỗi nhánh):")
        print(f"   • Default GOGC: TPS = {def_tps_m:.1f} ± {def_tps_sd:.1f} tx/s | Peak RSS = {def_rss_m:.0f} ± {def_rss_sd:.0f} MB")
        print(f"   • GOGC=50:      TPS = {g50_tps_m:.1f} ± {g50_tps_sd:.1f} tx/s | Peak RSS = {g50_rss_m:.0f} ± {g50_rss_sd:.0f} MB")
        print(f"   • TPS Chênh lệch: {pct_diff_tps:+.2f}% (t={t_stat_tps:.2f}, df={df_tps:.1f}, p={p_tps:.4f}, 95% CI=[{ci_tps[0]:.1f}, {ci_tps[1]:.1f}])")
        print(f"   • RSS Chênh lệch: {pct_diff_rss:+.2f}% (t={t_stat_rss:.2f}, df={df_rss:.1f}, p={p_rss:.4f})")
        print("==================================================================")

    dbg_results = {"DebugOn": [], "DebugOff": []}
    dbg_rss = {"DebugOn": [], "DebugOff": []}
    dbg_cpu = {"DebugOn": [], "DebugOff": []}
    dbg_log = {"DebugOn": [], "DebugOff": []}
    dbg_dur = {"DebugOn": [], "DebugOff": []}

    # ---------------- 4.2. Debug Flag Benchmark ----------------
    if args.test in ["all", "debug"]:
        print(f"\n▶️ [2/2] Chạy Thực Nghiệm Debug Flag: ENABLE_DEBUG_PPROF=true vs false ({rounds} lượt xen kẽ)...")
        for r in range(1, rounds + 1):
            # Debug On
            print(f"\n   [Lượt {r}/{rounds}] Cấu hình: Debug On (ENABLE_DEBUG_PPROF=true)...")
            setup_fresh_cluster(gogc=800)
            if start_cluster(enable_debug=True):
                tps, rss, dur, cpu_s, log_kb = run_blast()
                print(f"      • DebugOn #{r}: TPS={tps:.1f} tx/s, Peak RSS={rss} MB, CPU={cpu_s:.1f}s, Log={log_kb:.1f}KB, Dur={dur:.2f}s")
                if tps and rss:
                    dbg_results["DebugOn"].append(tps)
                    dbg_rss["DebugOn"].append(rss)
                    dbg_cpu["DebugOn"].append(cpu_s)
                    dbg_log["DebugOn"].append(log_kb)
                    dbg_dur["DebugOn"].append(dur)
            stop_and_clean_bench()

            # Debug Off
            print(f"   [Lượt {r}/{rounds}] Cấu hình: Debug Off (ENABLE_DEBUG_PPROF=false)...")
            setup_fresh_cluster(gogc=800)
            if start_cluster(enable_debug=False):
                tps, rss, dur, cpu_s, log_kb = run_blast()
                print(f"      • DebugOff #{r}: TPS={tps:.1f} tx/s, Peak RSS={rss} MB, CPU={cpu_s:.1f}s, Log={log_kb:.1f}KB, Dur={dur:.2f}s")
                if tps and rss:
                    dbg_results["DebugOff"].append(tps)
                    dbg_rss["DebugOff"].append(rss)
                    dbg_cpu["DebugOff"].append(cpu_s)
                    dbg_log["DebugOff"].append(log_kb)
                    dbg_dur["DebugOff"].append(dur)
            stop_and_clean_bench()

        # Stats for Debug Flag
        don_tps_m, don_tps_sd = mean_sd(dbg_results["DebugOn"])
        doff_tps_m, doff_tps_sd = mean_sd(dbg_results["DebugOff"])
        don_cpu_m, don_cpu_sd = mean_sd(dbg_cpu["DebugOn"])
        doff_cpu_m, doff_cpu_sd = mean_sd(dbg_cpu["DebugOff"])
        don_log_m, don_log_sd = mean_sd(dbg_log["DebugOn"])
        doff_log_m, doff_log_sd = mean_sd(dbg_log["DebugOff"])

        t_dbg, df_dbg, p_dbg, ci_dbg = welch_t_test(dbg_results["DebugOn"], dbg_results["DebugOff"])
        t_cpu, df_cpu, p_cpu, ci_cpu = welch_t_test(dbg_cpu["DebugOn"], dbg_cpu["DebugOff"])
        pct_diff_dbg = ((doff_tps_m - don_tps_m) / don_tps_m) * 100
        pct_diff_cpu = ((doff_cpu_m - don_cpu_m) / don_cpu_m) * 100

        print("\n==================================================================")
        print(f"📊 KẾT QUẢ THỐNG KÊ DEBUG FLAG (Fresh State, n={rounds} mỗi nhánh):")
        print(f"   • Debug On (true):   TPS = {don_tps_m:.1f} ± {don_tps_sd:.1f} tx/s | CPU = {don_cpu_m:.1f} ± {don_cpu_sd:.1f} s | Log = {don_log_m:.1f} KB")
        print(f"   • Debug Off (false):  TPS = {doff_tps_m:.1f} ± {doff_tps_sd:.1f} tx/s | CPU = {doff_cpu_m:.1f} ± {doff_cpu_sd:.1f} s | Log = {doff_log_m:.1f} KB")
        print(f"   • Chênh lệch TPS:    {pct_diff_dbg:+.2f}% (t={t_dbg:.2f}, df={df_dbg:.1f}, p={p_dbg:.4f}, 95% CI=[{ci_dbg[0]:.1f}, {ci_dbg[1]:.1f}])")
        print(f"   • Chênh lệch CPU:    {pct_diff_cpu:+.2f}% (t={t_cpu:.2f}, df={df_cpu:.1f}, p={p_cpu:.4f})")
        print("==================================================================")

    # Save CSV
    csv_file = args.output_csv if args.output_csv else f"{EVIDENCE_DIR}/controlled_benchmarks_summary.csv"
    with open(csv_file, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["Experiment", "Round", "Config", "TPS", "PeakRSS_MB", "Duration_s", "CPU_s", "LogSize_KB"])
        for r_idx in range(len(gogc_results["Default"])):
            writer.writerow(["GOGC", r_idx + 1, "Default", f"{gogc_results['Default'][r_idx]:.2f}", gogc_rss["Default"][r_idx], f"{gogc_dur['Default'][r_idx]:.2f}", f"{gogc_cpu['Default'][r_idx]:.2f}", f"{gogc_log['Default'][r_idx]:.2f}"])
            writer.writerow(["GOGC", r_idx + 1, "GOGC50", f"{gogc_results['GOGC50'][r_idx]:.2f}", gogc_rss["GOGC50"][r_idx], f"{gogc_dur['GOGC50'][r_idx]:.2f}", f"{gogc_cpu['GOGC50'][r_idx]:.2f}", f"{gogc_log['GOGC50'][r_idx]:.2f}"])
        for r_idx in range(len(dbg_results["DebugOn"])):
            writer.writerow(["DebugFlag", r_idx + 1, "DebugOn", f"{dbg_results['DebugOn'][r_idx]:.2f}", dbg_rss["DebugOn"][r_idx], f"{dbg_dur['DebugOn'][r_idx]:.2f}", f"{dbg_cpu['DebugOn'][r_idx]:.2f}", f"{dbg_log['DebugOn'][r_idx]:.2f}"])
            writer.writerow(["DebugFlag", r_idx + 1, "DebugOff", f"{dbg_results['DebugOff'][r_idx]:.2f}", dbg_rss["DebugOff"][r_idx], f"{dbg_dur['DebugOff'][r_idx]:.2f}", f"{dbg_cpu['DebugOff'][r_idx]:.2f}", f"{dbg_log['DebugOff'][r_idx]:.2f}"])
    print(f"\n✅ Summary CSV saved to {csv_file}")

if __name__ == "__main__":
    main()
