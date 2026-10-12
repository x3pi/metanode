#!/usr/bin/env python3
"""
Benchmark Runner for Improvement B1 (Parallel Speculative Root Derivation).
Measures:
- 5 rounds of 60-second unlimited blast (batch 1000), identical to Workload A1 protocol.
- Strict Zero-Fork Invariant verification (-verify-parity).
- Calculates timeline breakdown (T_cycle, Ta, Tb, Phase 1 duration).
- Compares against Phase A baseline (Mean = 10,898 tx/s).
- Computes SHA256 hashes and updates MANIFEST.json.
"""

import hashlib
import json
import math
import os
import shutil
import subprocess
import sys
import time

sys.path.insert(0, "/home/abc/chain-n/metanode/execution/scripts/test/evidence")
from run_phase_a_steady_state import (
    setup_clean_cluster,
    start_cluster,
    run_blast_timed,
    parse_val0_timeline,
    compute_steady_state_metrics,
    stop_and_kill,
    BENCH_BASE,
    EVIDENCE_DIR,
    NODES
)

MANIFEST_FILE = os.path.join(EVIDENCE_DIR, "MANIFEST.json")
BASELINE_A1_TPS = [10373.1, 11415.2, 10839.2, 11110.8, 10751.7] # Workload A1 5 runs

def compute_file_sha256(filepath):
    h = hashlib.sha256()
    with open(filepath, "rb") as f:
        while True:
            chunk = f.read(65536)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()

def welch_t_test(sample1, sample2):
    n1, n2 = len(sample1), len(sample2)
    m1 = sum(sample1) / n1
    m2 = sum(sample2) / n2
    v1 = sum((x - m1) ** 2 for x in sample1) / (n1 - 1)
    v2 = sum((x - m2) ** 2 for x in sample2) / (n2 - 1)
    se = math.sqrt(v1 / n1 + v2 / n2)
    if se == 0:
        return 0, 1.0
    t_stat = (m1 - m2) / se
    df = (v1 / n1 + v2 / n2) ** 2 / ((v1 / n1) ** 2 / (n1 - 1) + (v2 / n2) ** 2 / (n2 - 1))
    return t_stat, df

def run_b1_benchmark_series():
    print("=" * 80)
    print("🚀 STARTING B1 (PARALLEL SPECULATIVE ROOT DERIVATION) BENCHMARK SERIES")
    print("=" * 80)

    b1_results = []
    new_files_for_manifest = []

    for run_idx in range(1, 6):
        prefix = f"b1_unlimited_60s_run_{run_idx}"
        print(f"\n[{run_idx}/5] Starting clean cluster for {prefix}...")
        setup_clean_cluster()

        if not start_cluster():
            raise RuntimeError(f"Failed to start cluster for {prefix}!")

        print(f"[{run_idx}/5] Cluster started. Waiting 5s warm-up...")
        time.sleep(5)

        print(f"[{run_idx}/5] Blasting {prefix} (60s unlimited, batch 1000)...")
        rep = run_blast_timed(prefix, duration_sec=60, batch_size=1000, rate_limit=0)

        # Confirm Zero-Fork
        if not rep.get("zero_fork_verified"):
            stop_and_kill()
            raise RuntimeError(f"🚨 ZERO-FORK VIOLATION DETECTED in {prefix}!")

        log_archive = os.path.join(EVIDENCE_DIR, f"logs_{prefix}")
        t_blocks = parse_val0_timeline(log_archive)
        metrics = compute_steady_state_metrics(t_blocks) if t_blocks else None

        tps = rep.get("effective_tps", 0.0)
        confirmed = rep.get("total_confirmed", 0)
        blocks = rep.get("blocks_produced", 0)

        run_summary = {
            "run": run_idx,
            "prefix": prefix,
            "tps": tps,
            "confirmed_txs": confirmed,
            "blocks_produced": blocks,
            "zero_fork_verified": rep.get("zero_fork_verified"),
            "node_roots_consistent": rep.get("node_roots_consistent"),
            "timeline_metrics": metrics
        }
        b1_results.append(run_summary)

        print(f"✅ [{run_idx}/5] {prefix} COMPLETE: TPS={tps:.1f}, Confirmed={confirmed}, ZeroFork={rep.get('zero_fork_verified')}")
        if metrics:
            print(f"   Timeline: Avg Ta={metrics.get('avg_ta_computation_ms', 0):.1f}ms, Avg Tb={metrics.get('avg_tb_persist_ms', 0):.1f}ms, Cycle={metrics.get('avg_cycle_ms', 0):.1f}ms")

        # Collect files for manifest
        report_file = os.path.join(EVIDENCE_DIR, f"{prefix}_report.json")
        blast_log = os.path.join(EVIDENCE_DIR, f"{prefix}_blast.log")
        if os.path.exists(report_file):
            new_files_for_manifest.append(report_file)
        if os.path.exists(blast_log):
            new_files_for_manifest.append(blast_log)

        if os.path.isdir(log_archive):
            for root, _, files in os.walk(log_archive):
                for f in files:
                    new_files_for_manifest.append(os.path.join(root, f))

        stop_and_kill()
        time.sleep(2)

    # Statistical Summary
    tps_list = [r["tps"] for r in b1_results]
    mean_b1 = sum(tps_list) / len(tps_list)
    sd_b1 = math.sqrt(sum((x - mean_b1) ** 2 for x in tps_list) / (len(tps_list) - 1))

    mean_a1 = sum(BASELINE_A1_TPS) / len(BASELINE_A1_TPS)
    sd_a1 = math.sqrt(sum((x - mean_a1) ** 2 for x in BASELINE_A1_TPS) / (len(BASELINE_A1_TPS) - 1))

    delta_tps = mean_b1 - mean_a1
    pct_gain = (delta_tps / mean_a1) * 100.0

    t_stat, df = welch_t_test(tps_list, BASELINE_A1_TPS)

    summary = {
        "workload": "B1 (Parallel Speculative Root Derivation)",
        "protocol": "60s sustained unlimited blast, batch 1000, 4-node cluster",
        "num_runs": len(b1_results),
        "b1_tps_runs": tps_list,
        "mean_tps_b1": mean_b1,
        "sd_tps_b1": sd_b1,
        "mean_tps_a1_baseline": mean_a1,
        "sd_tps_a1_baseline": sd_a1,
        "delta_tps": delta_tps,
        "pct_improvement": pct_gain,
        "welch_t_stat": t_stat,
        "welch_df": df,
        "zero_fork_all_runs_verified": all(r["zero_fork_verified"] for r in b1_results),
        "runs_detail": b1_results
    }

    summary_file = os.path.join(EVIDENCE_DIR, "b1_speculative_roots_summary.json")
    with open(summary_file, "w") as f:
        json.dump(summary, f, indent=2)
    new_files_for_manifest.append(summary_file)

    print("\n" + "=" * 80)
    print("📊 B1 BENCHMARK STATISTICAL SUMMARY")
    print("=" * 80)
    print(f"Phase A1 Baseline Mean: {mean_a1:.1f} tx/s (SD = {sd_a1:.1f})")
    print(f"Phase B1 Measured Mean: {mean_b1:.1f} tx/s (SD = {sd_b1:.1f})")
    print(f"Absolute Gain:         {delta_tps:+.1f} tx/s")
    print(f"Relative Improvement:  {pct_gain:+.2f}%")
    print(f"Welch's t-statistic:   t = {t_stat:.3f} (df = {df:.1f})")
    print(f"Zero-Fork Invariant:   100% PASS (All 5 runs verified)")
    print("=" * 80)

    # Update MANIFEST.json
    print(f"\nUpdating MANIFEST.json with {len(new_files_for_manifest)} files...")
    manifest = {}
    if os.path.exists(MANIFEST_FILE):
        try:
            with open(MANIFEST_FILE, "r") as f:
                manifest = json.load(f)
        except Exception:
            manifest = {}

    for fpath in new_files_for_manifest:
        if os.path.isfile(fpath):
            rel_path = os.path.relpath(fpath, EVIDENCE_DIR)
            manifest[rel_path] = {
                "sha256": compute_file_sha256(fpath),
                "size_bytes": os.path.getsize(fpath),
                "recorded_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(os.path.getmtime(fpath)))
            }

    with open(MANIFEST_FILE, "w") as f:
        json.dump(manifest, f, indent=2)

    print(f"✅ MANIFEST.json updated (total tracked entries: {len(manifest)}).")
    return summary

if __name__ == "__main__":
    run_b1_benchmark_series()
