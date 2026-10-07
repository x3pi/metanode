#!/usr/bin/env python3
"""
Automated benchmark runner for B1: Configuration A (commit ec6560ce) vs Configuration B (HEAD).
Executes 7 alternating rounds (A1, B1, A2, B2, ..., A7, B7) on 100, 1000, and 5000 keys.
Computes mean, standard deviation, Welch's t-test, and tests the acceptance criterion:
  B_time <= A_time * (1 / 0.9)  [B >= 90% throughput of A]
"""

import math
import os
import re
import subprocess
import sys

DIR_A = "/tmp/worktree_A/execution"
DIR_B = "/home/abc/chain-n/metanode/execution"
BENCH_NAME = "BenchmarkNomtCommit_Sync"
BENCH_TIME = "20x"
ROUNDS = 7
KEYS = [100, 1000, 5000]

def run_bench(cwd, keys_target):
    cmd = [
        "go", "test",
        f"-bench={BENCH_NAME}/Keys_{keys_target}$",
        f"-benchtime={BENCH_TIME}",
        "-run=^$",
        "./pkg/trie/"
    ]
    res = subprocess.run(
        cmd,
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        check=False
    )
    # Parse ns/op: look from bottom up for line ending with or containing ns/op
    for line in reversed(res.stdout.splitlines()):
        if "ns/op" in line:
            parts = line.strip().split()
            for i, p in enumerate(parts):
                if p == "ns/op" and i > 0:
                    try:
                        val = int(parts[i - 1].replace(",", ""))
                        return val, res.stdout
                    except ValueError:
                        pass
    return None, res.stdout

def run_strace_fsync(cwd):
    cmd = [
        "strace", "-c", "-f", "-e", "trace=fsync,fdatasync",
        "go", "test",
        f"-bench={BENCH_NAME}/Keys_100$",
        "-benchtime=5x",
        "-run=^$",
        "./pkg/trie/"
    ]
    res = subprocess.run(
        cmd,
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        check=False
    )
    return res.stderr

def compute_stats(data):
    n = len(data)
    if n == 0:
        return 0, 0, 0
    mean = sum(data) / n
    variance = sum((x - mean) ** 2 for x in data) / (n - 1) if n > 1 else 0
    sd = math.sqrt(variance)
    sorted_d = sorted(data)
    median = sorted_d[n // 2] if n % 2 != 0 else (sorted_d[n // 2 - 1] + sorted_d[n // 2]) / 2
    return mean, sd, median

def welch_t_test(data1, data2):
    n1, n2 = len(data1), len(data2)
    if n1 < 2 or n2 < 2:
        return 0, 1.0, 1.0
    m1 = sum(data1) / n1
    m2 = sum(data2) / n2
    v1 = sum((x - m1) ** 2 for x in data1) / (n1 - 1)
    v2 = sum((x - m2) ** 2 for x in data2) / (n2 - 1)
    
    se = math.sqrt((v1 / n1) + (v2 / n2))
    if se == 0:
        return 0, 1.0, 1.0
    t_stat = (m1 - m2) / se
    
    # Welch-Satterthwaite degrees of freedom
    num = ((v1 / n1) + (v2 / n2)) ** 2
    denom = ((v1 / n1) ** 2 / (n1 - 1)) + ((v2 / n2) ** 2 / (n2 - 1))
    df = num / denom if denom > 0 else 1.0
    return t_stat, df

def main():
    print("==================================================================")
    print("🔬 B1 MICROBENCHMARK: Configuration A (ec6560ce) vs B (HEAD)")
    print(f"   Worktree A:      {DIR_A}")
    print(f"   Worktree B:      {DIR_B}")
    print(f"   Benchmark:       {BENCH_NAME}")
    print(f"   Keys tested:     {KEYS}")
    print(f"   Alternating:     {ROUNDS} rounds (A1, B1, A2, B2, ..., A7, B7)")
    print("   Criterion:       B_time <= A_time * (1 / 0.90)  [B >= 90% A]")
    print("==================================================================")

    print("\n🔍 [1/3] Verifying Fsync Syscalls via strace...")
    fsync_a = run_strace_fsync(DIR_A)
    fsync_b = run_strace_fsync(DIR_B)
    print("--- Fsync Summary A (ec6560ce) ---")
    for l in fsync_a.splitlines()[-6:]:
        print(f"   {l}")
    print("--- Fsync Summary B (HEAD) ---")
    for l in fsync_b.splitlines()[-6:]:
        print(f"   {l}")

    raw_results = {k: {"A": [], "B": []} for k in KEYS}

    print("\n🚀 [2/3] Executing 7 Alternating Benchmarks...")
    for r in range(1, ROUNDS + 1):
        print(f"\n▶️ ROUND {r}/{ROUNDS}:")
        for k in KEYS:
            # Run A
            ns_a, log_a = run_bench(DIR_A, k)
            if ns_a is None:
                print(f"   ❌ Config A failed on Keys_{k}: {log_a[-200:]}")
                sys.exit(1)
            raw_results[k]["A"].append(ns_a)
            ms_a = ns_a / 1_000_000.0

            # Run B
            ns_b, log_b = run_bench(DIR_B, k)
            if ns_b is None:
                print(f"   ❌ Config B failed on Keys_{k}: {log_b[-200:]}")
                sys.exit(1)
            raw_results[k]["B"].append(ns_b)
            ms_b = ns_b / 1_000_000.0

            ratio = (ms_b / ms_a) if ms_a > 0 else 0
            pct_diff = ((ms_b - ms_a) / ms_a) * 100.0
            print(f"   • Keys {k:4d} | A: {ms_a:7.3f} ms | B: {ms_b:7.3f} ms | B/A: {ratio:6.3f} ({pct_diff:+5.2f}%)")

    print("\n📊 [3/3] Statistical Summary & Hypothesis Testing:")
    print("==================================================================")
    print("| Keys | Config A Mean ± SD (ms) | Config B Mean ± SD (ms) | B/A Ratio | B/A % Diff | Welch t (df) | Ngưỡng 90% | Kết luận |")
    print("| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |")

    all_pass = True
    for k in KEYS:
        a_ms = [x / 1_000_000.0 for x in raw_results[k]["A"]]
        b_ms = [x / 1_000_000.0 for x in raw_results[k]["B"]]

        m_a, sd_a, _ = compute_stats(a_ms)
        m_b, sd_b, _ = compute_stats(b_ms)

        ratio = m_b / m_a if m_a > 0 else 0
        pct_diff = ((m_b - m_a) / m_a) * 100.0
        t_stat, df = welch_t_test(b_ms, a_ms)

        # Threshold: B_time <= A_time * (1 / 0.90) = 1.1111 * A_time (<= 11.11% slowdown)
        passed = m_b <= m_a * (1.0 / 0.90)
        verdict = "**PASS**" if passed else "**FAIL**"
        if not passed:
            all_pass = False

        print(f"| {k} | {m_a:.3f} ± {sd_a:.3f} ms | {m_b:.3f} ± {sd_b:.3f} ms | {ratio:.3f} | {pct_diff:+.2f}% | t={t_stat:.2f} (df={df:.1f}) | B <= {m_a * (1/0.9):.3f} ms | {verdict} |")

    print("==================================================================")
    if all_pass:
        print("🎉 CRITERION B >= 90% A SATISFIED ACROSS ALL TESTED KEY SIZES!")
    else:
        print("❌ CRITERION B >= 90% A FAILED ON AT LEAST ONE KEY SIZE!")

if __name__ == "__main__":
    main()
