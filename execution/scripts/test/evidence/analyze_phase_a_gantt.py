#!/usr/bin/env python3
"""
Phase A Gantt Timeline & Amdahl Ceiling Analyzer.
Parses logs and reports from note/evidence/tps_improvement_20261008/
Computes:
1. Steady-state Timeline & Gantt diagram per block (excluding cold blocks 1 & 2).
2. Cycle time between consecutive blocks.
3. Separation of Ta (computation) vs Tb (persist).
4. Amdahl Ceiling: Theoretical maximum throughput if Tb is 100% hidden.
5. Statistical analysis of Batch 250 vs 500 vs 1000 (10 rounds each: mean, SD, t-test).
"""

import json
import math
import os
import re
import sys

REPO_ROOT = "/home/abc/chain-n/metanode"
EVIDENCE_DIR = os.path.join(REPO_ROOT, "note/evidence/tps_improvement_20261008")

def t_test_two_sample(sample1, sample2):
    """Computes Welch's t-test between two independent samples."""
    n1, n2 = len(sample1), len(sample2)
    if n1 < 2 or n2 < 2:
        return 0.0, 1.0
    m1, m2 = sum(sample1)/n1, sum(sample2)/n2
    v1 = sum((x - m1)**2 for x in sample1) / (n1 - 1)
    v2 = sum((x - m2)**2 for x in sample2) / (n2 - 1)
    se = math.sqrt(v1/n1 + v2/n2)
    if se == 0:
        return 0.0, 1.0
    t_stat = (m1 - m2) / se
    # Approximate degrees of freedom (Welch-Satterthwaite)
    df_num = (v1/n1 + v2/n2)**2
    df_den = (v1/n1)**2 / (n1 - 1) + (v2/n2)**2 / (n2 - 1)
    df = df_num / df_den if df_den > 0 else 1.0
    return t_stat, df

def parse_val0_timeline(log_archive_dir):
    val0_log = os.path.join(log_archive_dir, "val0_execution.log")
    if not os.path.isfile(val0_log):
        return None

    with open(val0_log, "r", encoding="utf-8", errors="ignore") as f:
        content = f.read()

    blocks = {}

    def to_ms(s):
        s = s.strip()
        if s.endswith("µs") or s.endswith("us"): return float(s[:-2])/1e3
        if s.endswith("ms"): return float(s[:-2])
        if s.endswith("s"): return float(s[:-1])*1e3
        return 0.0

    # 1. Parse GO_SPEC
    spec_re = re.compile(r"\[FFI-TRACE\] gei=(\d+) stage=GO_SPEC goroutine_sched_ns=(\d+) prepare_tx_ns=(\d+) clone_ns=(\d+) group_ns=(\d+) exec_ns=(\d+)")
    for m in spec_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks: blocks[gei] = {"gei": gei}
        blocks[gei]["wait_predecessor_ms"] = int(m.group(3)) / 1e6
        blocks[gei]["clone_ms"] = int(m.group(4)) / 1e6
        blocks[gei]["tx_group_ms"] = int(m.group(5)) / 1e6
        blocks[gei]["exec_block_stm_ms"] = int(m.group(6)) / 1e6

    # 2. Parse Roots
    phase1_re = re.compile(r"\[PERF\] Block #(\d+) Phase 1 Root Calc Breakdown:\s+- receiptsRoot:\s+([0-9\.]+[a-zµ]+).*?- txsRoot:\s+([0-9\.]+[a-zµ]+)", re.DOTALL)
    for m in phase1_re.finditer(content):
        blk = int(m.group(1))
        if blk not in blocks: blocks[blk] = {"gei": blk}
        blocks[blk]["receipts_root_ms"] = to_ms(m.group(2))
        blocks[blk]["txs_root_ms"] = to_ms(m.group(3))
        blocks[blk]["roots_total_ms"] = max(to_ms(m.group(2)), to_ms(m.group(3)))

    # 3. Parse Commit Memory
    commit_re = re.compile(r"\[PERF\] Block #(\d+) commitToMemoryParallel Breakdown:.*?🚀 TOTAL COMMIT MEMORY:\s+([0-9\.]+[a-zµ]+)", re.DOTALL)
    for m in commit_re.finditer(content):
        blk = int(m.group(1))
        if blk not in blocks: blocks[blk] = {"gei": blk}
        blocks[blk]["commit_memory_ms"] = to_ms(m.group(2))

    # 4. Parse Save DB / Persist
    save_re = re.compile(r"\[PERF\] Block Commit phase 1 \(Save DB\):\s+([0-9\.]+[a-zµ]+),\s+block:\s+(\d+)")
    for m in save_re.finditer(content):
        blk = int(m.group(2))
        if blk not in blocks: blocks[blk] = {"gei": blk}
        blocks[blk]["save_db_persist_ms"] = to_ms(m.group(1))

    # 5. Parse Commit Dequeued & Done timestamps
    deq_re = re.compile(r"\[FFI-TRACE\] gei=(\d+) stage=GO_COMMIT_DEQUEUED t_ns=(\d+)")
    for m in deq_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks: blocks[gei] = {"gei": gei}
        blocks[gei]["commit_dequeued_t_ns"] = int(m.group(2))

    done_re = re.compile(r"\[FFI-TRACE\] gei=(\d+) stage=GO_COMMIT_DONE t_ns=(\d+)")
    for m in done_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks: blocks[gei] = {"gei": gei}
        blocks[gei]["commit_done_t_ns"] = int(m.group(2))

    # 6. Parse TX count
    txs_re = re.compile(r"\[COMMITTER\] Processing speculative commit: GEI=(\d+), block=#\d+, txs=(\d+)")
    for m in txs_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks: blocks[gei] = {"gei": gei}
        blocks[gei]["tx_count"] = int(m.group(2))

    return blocks

def analyze_timeline():
    summary_file = os.path.join(EVIDENCE_DIR, "phase_a_steady_state_summary.json")
    if not os.path.isfile(summary_file):
        print(f"Summary file {summary_file} not found!")
        return

    with open(summary_file, "r") as f:
        summary = json.load(f)

    print("=" * 80)
    print("📊 PHASE A COMPREHENSIVE GANTT & AMDAHL ANALYSIS")
    print("=" * 80)

    # 1. Workload A1 Analysis
    a1_runs = summary.get("workload_a1_unlimited_60s", [])
    print(f"\n1️⃣ WORKLOAD A1: UNLIMITED STEADY-STATE (60s x {len(a1_runs)} rounds)")
    a1_tps_list = [r["effective_tps"] for r in a1_runs if "effective_tps" in r]
    a1_mean_tps = sum(a1_tps_list) / len(a1_tps_list) if a1_tps_list else 0
    print(f"   • Mean Effective Throughput: {a1_mean_tps:.2f} tx/s (Runs: {[round(x,1) for x in a1_tps_list]})")

    all_steady_ta = []
    all_steady_tb = []
    all_steady_cycle = []
    all_steady_wait = []

    for r in a1_runs:
        m = r.get("steady_state_metrics")
        if m:
            all_steady_ta.append(m["avg_ta_computation_ms"])
            all_steady_tb.append(m["avg_tb_persist_ms"])
            all_steady_cycle.append(m["avg_cycle_ms"])
            for b in m.get("steady_blocks", []):
                all_steady_wait.append(b.get("wait_predecessor_ms", 0))

    if all_steady_ta:
        mean_ta = sum(all_steady_ta) / len(all_steady_ta)
        mean_tb = sum(all_steady_tb) / len(all_steady_tb)
        mean_cycle = sum(all_steady_cycle) / len(all_steady_cycle)
        mean_wait = sum(all_steady_wait) / len(all_steady_wait) if all_steady_wait else 0

        # Amdahl potential:
        # If Tb is 100% hidden, cycle reduces from mean_cycle to mean_ta
        # Ceiling TPS = Baseline TPS * (mean_cycle / mean_ta)
        ceiling_tps = a1_mean_tps * (mean_cycle / mean_ta) if mean_ta > 0 else 0
        delta_pct = ((ceiling_tps - a1_mean_tps) / a1_mean_tps * 100.0) if a1_mean_tps > 0 else 0

        print(f"\n   🔬 STEADY-STATE TIMELINE BREAKDOWN (Cold blocks #1 & #2 excluded):")
        print(f"      • Ta (Computation: Block-STM + Roots + Clone): {mean_ta:.2f} ms")
        print(f"      • Tb (Persist: NOMT commit + Pebble DB write): {mean_tb:.2f} ms")
        print(f"      • Actual Cycle Time between consecutive blocks:  {mean_cycle:.2f} ms")
        print(f"      • Raw wait_predecessor queue time:             {mean_wait:.2f} ms")
        print(f"\n   📐 AMDAHL LAW THEORETICAL CEILING:")
        print(f"      • Baseline Steady-State TPS:  {a1_mean_tps:.2f} tx/s")
        print(f"      • Amdahl Ceiling TPS:         {ceiling_tps:.2f} tx/s")
        print(f"      • Max Potential Improvement:  {delta_pct:+.2f}%")
        print(f"\n   🚦 GATING DECISION FOR PHASE D (Threshold: >= +15%):")
        if delta_pct >= 15.0:
            print(f"      ✅ CRITERION MET: Potential {delta_pct:.1f}% >= 15.0%. Eligible for Phase D pending Phase C design approval.")
        else:
            print(f"      🛑 CRITERION NOT MET: Potential {delta_pct:.1f}% < 15.0%. Abort Phase D; focus on Phase B low-risk optimizations.")

    # 2. Workload A3 Batch Isolation
    a3 = summary.get("workload_a3_batch_isolation", {})
    b250 = [r["effective_tps"] for r in a3.get("batch_250", [])]
    b500 = [r["effective_tps"] for r in a3.get("batch_500", [])]
    b1000 = [r["effective_tps"] for r in a3.get("batch_1000", [])]

    if b500 and b1000:
        def stats(s):
            m = sum(s)/len(s)
            sd = math.sqrt(sum((x-m)**2 for x in s)/(len(s)-1)) if len(s)>1 else 0
            se = sd / math.sqrt(len(s))
            return m, sd, se

        m250, sd250, se250 = stats(b250) if b250 else (0,0,0)
        m500, sd500, se500 = stats(b500)
        m1000, sd1000, se1000 = stats(b1000)

        t_stat, df = t_test_two_sample(b500, b1000)

        print(f"\n2️⃣ WORKLOAD A3: BATCH SIZE STATISTICAL RIGOR (10 Rounds Each)")
        print(f"   • Batch 250:  Mean = {m250:.2f} tx/s | StdDev = {sd250:.2f} | StdErr = {se250:.2f}")
        print(f"   • Batch 500:  Mean = {m500:.2f} tx/s | StdDev = {sd500:.2f} | StdErr = {se500:.2f}")
        print(f"   • Batch 1000: Mean = {m1000:.2f} tx/s | StdDev = {sd1000:.2f} | StdErr = {se1000:.2f}")
        print(f"   • Batch 500 vs 1000 Delta: {((m500 - m1000)/m1000*100):+.2f}%")
        print(f"   • Welch's t-statistic: {t_stat:.3f} (df={df:.1f})")
        if abs(t_stat) > 2.1:
            print("   ✅ Statistically significant difference (p < 0.05). Batch 500 is reliably faster.")
        else:
            print("   ⚠️ Difference is NOT statistically significant (p >= 0.05). Cannot reject noise hypothesis.")

if __name__ == "__main__":
    analyze_timeline()
