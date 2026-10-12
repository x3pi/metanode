#!/usr/bin/env python3
"""
Phase A Steady-State Benchmark & Timeline Analyzer Runner.
Implements the experimental protocol specified in:
- note/plan_tps_improvement_impl_20261008.md
- note/evidence/tps_improvement_20261008/PREREGISTERED.md

Measures:
1. Workload A1: 5 rounds of 60-second unlimited sustained blast (batch 1000).
2. Workload A2: 5 rounds of 60-second rate-limited blast (4,500 tx/s, batch 500).
3. Workload A3: 10 rounds each for Batch 250, Batch 500, Batch 1000 (25k txs each).

Computes:
- Absolute timelines per block (discarding cold blocks #1 & #2).
- Steady-state cycle time T_cycle between consecutive blocks.
- Time Ta (computation: Block-STM + Root derivation + clone).
- Time Tb (persist: NOMT commit + Pebble DB write + async GEI).
- Amdahl ceiling: Maximum possible throughput if Tb is 100% overlapped.
- Gating decision: Does delta_TPS_potential >= +15%?
"""

import json
import os
import re
import shutil
import subprocess
import sys
import time

BENCH_BASE = "/tmp/gate_4val_p06"
TEMPLATE_BASE = "/tmp/gate_4val_clean_template"
BIN_DIR = "/tmp/p06_bins"
REPO_ROOT = "/home/abc/chain-n/metanode"
RUN_ENV = f"{REPO_ROOT}/execution/scripts/test/gate_e2e/run_env.sh"
BLAST_TOOL = f"{BIN_DIR}/secp_tps_blast"
EVIDENCE_DIR = f"{REPO_ROOT}/note/evidence/tps_improvement_20261008"
NODES = ["val0", "val1", "val2", "val3"]
RPC_URLS = "http://127.0.0.1:31646,http://127.0.0.1:31647,http://127.0.0.1:31648,http://127.0.0.1:31649"
TCP_ADDRS = "127.0.0.1:31200,127.0.0.1:31201,127.0.0.1:31202,127.0.0.1:31203"

def get_live_bench_pids():
    pids = []
    try:
        out = subprocess.check_output(["ps", "-eo", "pid,rss,cmd"], text=True)
        for line in out.splitlines():
            if BENCH_BASE in line and ("simple_chain" in line or "metanode" in line or "parent_chain" in line):
                parts = line.strip().split()
                pids.append(int(parts[0]))
    except Exception:
        pass
    return pids

def stop_and_kill():
    if os.path.isdir(BENCH_BASE):
        subprocess.run([RUN_ENV, BENCH_BASE, "stop"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        time.sleep(1)

    leftovers = get_live_bench_pids()
    if leftovers:
        for pid in leftovers:
            try:
                os.kill(pid, 9)
            except Exception:
                pass
        time.sleep(0.5)

    if os.path.isdir(BENCH_BASE):
        shutil.rmtree(BENCH_BASE, ignore_errors=True)

def setup_clean_cluster():
    stop_and_kill()
    if not os.path.isdir(TEMPLATE_BASE):
        raise RuntimeError(f"Clean template base not found at {TEMPLATE_BASE}")
    cmd = ["cp", "-a", "--reflink=auto", f"{TEMPLATE_BASE}/.", f"{BENCH_BASE}/"]
    os.makedirs(BENCH_BASE, exist_ok=True)
    subprocess.run(cmd, check=True)

def start_cluster():
    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "false"
    env["METANODE_FFI_TRACE"] = "true"
    res = subprocess.run([RUN_ENV, BENCH_BASE, "start"], env=env, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        print("❌ Failed to start cluster:", res.stderr)
        return False
    return True

def archive_node_logs(dest_dir):
    os.makedirs(dest_dir, exist_ok=True)
    if os.path.isdir(f"{BENCH_BASE}/logs"):
        for f in os.listdir(f"{BENCH_BASE}/logs"):
            src = os.path.join(f"{BENCH_BASE}/logs", f)
            if os.path.isfile(src):
                shutil.copy2(src, dest_dir)
    for n in NODES:
        n_log_dir = f"{BENCH_BASE}/{n}/logs"
        if os.path.isdir(n_log_dir):
            for root, _, files in os.walk(n_log_dir):
                for f in files:
                    if f.endswith(".log"):
                        src = os.path.join(root, f)
                        dst = os.path.join(dest_dir, f"{n}_{f}")
                        shutil.copy2(src, dst)

def run_blast_timed(prefix, duration_sec, batch_size=1000, rate_limit=0, count=0):
    report_file = os.path.join(EVIDENCE_DIR, f"{prefix}_report.json")
    log_file = os.path.join(EVIDENCE_DIR, f"{prefix}_blast.log")

    cmd = [
        BLAST_TOOL,
        "-rpc", RPC_URLS,
        "-tcp", TCP_ADDRS,
        "-batch", str(batch_size),
        "-mode", "tcp",
        "-type", "1559",
        "-verify-parity",
        "-report", report_file
    ]
    if duration_sec > 0:
        cmd.extend(["-duration", str(duration_sec)])
        if count == 0:
            count = 5000
    if rate_limit > 0:
        cmd.extend(["-rate-limit", str(rate_limit)])
    if count > 0:
        cmd.extend(["-count", str(count)])

    print(f"▶️ Executing blast: {' '.join(cmd)}")
    start_t = time.time()
    res = subprocess.run(cmd, capture_output=True, text=True, check=False)
    elapsed = time.time() - start_t

    with open(log_file, "w") as f:
        f.write(res.stdout)
        if res.stderr:
            f.write("\n=== STDERR ===\n" + res.stderr)

    log_archive_dir = os.path.join(EVIDENCE_DIR, f"logs_{prefix}")
    archive_node_logs(log_archive_dir)

    if not os.path.exists(report_file):
        raise RuntimeError(f"Report file {report_file} was not generated! Blast stderr: {res.stderr}")

    with open(report_file, "r") as f:
        rep = json.load(f)

    print(f"   Blast completed in {elapsed:.2f}s: Confirmed={rep.get('total_confirmed')}, TPS={rep.get('effective_tps'):.1f}, ZeroFork={rep.get('zero_fork_verified')}")
    return rep

def parse_val0_timeline(log_archive_dir):
    val0_log = os.path.join(log_archive_dir, "val0_execution.log")
    if not os.path.isfile(val0_log):
        return None

    with open(val0_log, "r", encoding="utf-8", errors="ignore") as f:
        content = f.read()

    blocks = {}

    # 1. Parse GO_SPEC
    spec_re = re.compile(r"\[FFI-TRACE\] gei=(\d+) stage=GO_SPEC goroutine_sched_ns=(\d+) prepare_tx_ns=(\d+) clone_ns=(\d+) group_ns=(\d+) exec_ns=(\d+)")
    for m in spec_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks:
            blocks[gei] = {"gei": gei}
        blocks[gei]["wait_predecessor_ms"] = int(m.group(3)) / 1e6
        blocks[gei]["clone_ms"] = int(m.group(4)) / 1e6
        blocks[gei]["tx_group_ms"] = int(m.group(5)) / 1e6
        blocks[gei]["exec_block_stm_ms"] = int(m.group(6)) / 1e6

    # 2. Parse Roots
    phase1_re = re.compile(r"\[PERF\] Block #(\d+) Phase 1 Root Calc Breakdown:\s+- receiptsRoot:\s+([0-9\.]+[a-zµ]+).*?- txsRoot:\s+([0-9\.]+[a-zµ]+)", re.DOTALL)
    for m in phase1_re.finditer(content):
        blk = int(m.group(1))
        if blk not in blocks:
            blocks[blk] = {"gei": blk}
        def to_ms(s):
            if s.endswith("µs") or s.endswith("us"): return float(s[:-2])/1e3
            if s.endswith("ms"): return float(s[:-2])
            if s.endswith("s"): return float(s[:-1])*1e3
            return 0.0
        blocks[blk]["receipts_root_ms"] = to_ms(m.group(2))
        blocks[blk]["txs_root_ms"] = to_ms(m.group(3))
        blocks[blk]["roots_total_ms"] = max(to_ms(m.group(2)), to_ms(m.group(3)))

    # 3. Parse Commit Memory
    commit_re = re.compile(r"\[PERF\] Block #(\d+) commitToMemoryParallel Breakdown:.*?🚀 TOTAL COMMIT MEMORY:\s+([0-9\.]+[a-zµ]+)", re.DOTALL)
    for m in commit_re.finditer(content):
        blk = int(m.group(1))
        if blk not in blocks:
            blocks[blk] = {"gei": blk}
        s = m.group(2)
        if s.endswith("µs") or s.endswith("us"): val = float(s[:-2])/1e3
        elif s.endswith("ms"): val = float(s[:-2])
        elif s.endswith("s"): val = float(s[:-1])*1e3
        else: val = 0.0
        blocks[blk]["commit_memory_ms"] = val

    # 4. Parse Save DB / Persist
    # [PERF] Block Commit phase 1 (Save DB): 23.456ms, block: 2
    save_re = re.compile(r"\[PERF\] Block Commit phase 1 \(Save DB\):\s+([0-9\.]+[a-zµ]+),\s+block:\s+(\d+)")
    for m in save_re.finditer(content):
        blk = int(m.group(2))
        if blk not in blocks:
            blocks[blk] = {"gei": blk}
        s = m.group(1)
        if s.endswith("µs") or s.endswith("us"): val = float(s[:-2])/1e3
        elif s.endswith("ms"): val = float(s[:-2])
        elif s.endswith("s"): val = float(s[:-1])*1e3
        else: val = 0.0
        blocks[blk]["save_db_persist_ms"] = val

    # 5. Parse Commit Dequeued & Done timestamps
    # ⏱️ [FFI-TRACE] gei=2 stage=GO_COMMIT_DEQUEUED t_ns=172836...
    deq_re = re.compile(r"\[FFI-TRACE\] gei=(\d+) stage=GO_COMMIT_DEQUEUED t_ns=(\d+)")
    for m in deq_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks:
            blocks[gei] = {"gei": gei}
        blocks[gei]["commit_dequeued_t_ns"] = int(m.group(2))

    done_re = re.compile(r"\[FFI-TRACE\] gei=(\d+) stage=GO_COMMIT_DONE t_ns=(\d+)")
    for m in done_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks:
            blocks[gei] = {"gei": gei}
        blocks[gei]["commit_done_t_ns"] = int(m.group(2))

    # 6. Parse TX count per block:
    # 📥 [COMMITTER] Processing speculative commit: GEI=2, block=#2, txs=10000
    txs_re = re.compile(r"\[COMMITTER\] Processing speculative commit: GEI=(\d+), block=#\d+, txs=(\d+)")
    for m in txs_re.finditer(content):
        gei = int(m.group(1))
        if gei not in blocks:
            blocks[gei] = {"gei": gei}
        blocks[gei]["tx_count"] = int(m.group(2))

    return blocks

def compute_steady_state_metrics(timeline_blocks):
    # Discard cold blocks (gei <= 2) and trailing empty/drain blocks (tx_count < 500)
    steady_geis = sorted([g for g in timeline_blocks.keys() if g > 2 and timeline_blocks[g].get("tx_count", 0) >= 500])
    if len(steady_geis) < 2:
        return None

    results = []
    cycle_times = []

    for i in range(len(steady_geis)):
        gei = steady_geis[i]
        b = timeline_blocks[gei]

        # Calculate Ta (Computation): Exec + Roots + Clone + Group
        exec_ms = b.get("exec_block_stm_ms", 0)
        roots_ms = b.get("roots_total_ms", 0)
        clone_ms = b.get("clone_ms", 0)
        group_ms = b.get("tx_group_ms", 0)
        ta_ms = exec_ms + roots_ms + clone_ms + group_ms

        # Calculate Tb (Persist): Commit Memory + Save DB
        commit_mem_ms = b.get("commit_memory_ms", 0)
        save_db_ms = b.get("save_db_persist_ms", 0)
        tb_ms = commit_mem_ms + save_db_ms

        # Cycle time to next block based on COMMIT_DONE timestamps if available
        cycle_ms = 0
        if i > 0:
            prev_gei = steady_geis[i-1]
            prev_b = timeline_blocks[prev_gei]
            if "commit_done_t_ns" in b and "commit_done_t_ns" in prev_b:
                cycle_ms = (b["commit_done_t_ns"] - prev_b["commit_done_t_ns"]) / 1e6
                if cycle_ms > 0:
                    cycle_times.append(cycle_ms)

        txs = b.get("tx_count", 0)
        wait_ms = b.get("wait_predecessor_ms", 0)

        results.append({
            "gei": gei,
            "tx_count": txs,
            "wait_predecessor_ms": wait_ms,
            "exec_block_stm_ms": exec_ms,
            "roots_ms": roots_ms,
            "ta_computation_ms": ta_ms,
            "tb_persist_ms": tb_ms,
            "cycle_ms": cycle_ms
        })

    avg_ta = sum(r["ta_computation_ms"] for r in results) / len(results) if results else 0
    avg_tb = sum(r["tb_persist_ms"] for r in results) / len(results) if results else 0
    avg_cycle = sum(cycle_times) / len(cycle_times) if cycle_times else (avg_ta + avg_tb)
    avg_txs = sum(r["tx_count"] for r in results) / len(results) if results else 0

    return {
        "num_steady_blocks": len(results),
        "steady_blocks": results,
        "avg_ta_computation_ms": avg_ta,
        "avg_tb_persist_ms": avg_tb,
        "avg_cycle_ms": avg_cycle,
        "avg_tx_per_block": avg_txs,
        # Amdahl Ceiling: if Tb is 100% hidden, min cycle = Ta
        # Potential TPS = avg_txs / (Ta in sec)
        "baseline_steady_tps": (avg_txs / (avg_cycle / 1000.0)) if avg_cycle > 0 else 0,
        "amdahl_ceiling_tps": (avg_txs / (avg_ta / 1000.0)) if avg_ta > 0 else 0,
        "delta_tps_potential_pct": (((avg_cycle - avg_ta) / avg_ta) * 100.0) if avg_ta > 0 else 0
    }

def main():
    print("=" * 80)
    print("🚀 PHASE A STEADY-STATE BENCHMARK & AMDAHL CEILING INVESTIGATION")
    print("   Protocol: Workload A1 (60s unlimited), A2 (60s rate-limited), A3 (10 rounds batch size)")
    print("   Evaluating: Ta (computation) vs Tb (persist) & Gating Criterion (>= +15%)")
    print("=" * 80)

    summary = {
        "workload_a1_unlimited_60s": [],
        "workload_a2_ratelimited_60s": [],
        "workload_a3_batch_isolation": {
            "batch_250": [],
            "batch_500": [],
            "batch_1000": []
        }
    }

    # ══════════════════════════════════════════════════════════════════════
    # PART 1: Workload A1 — 5 Rounds of 60s Unlimited Blast (Batch 1000)
    # ══════════════════════════════════════════════════════════════════════
    print("\n" + "=" * 60)
    print("📍 [WORKLOAD A1] 5 Rounds of 60-Second Unlimited Blast")
    print("=" * 60)
    for r in range(1, 6):
        prefix = f"a1_unlimited_60s_run_{r}"
        print(f"\n▶️ Starting Workload A1 Round {r}/5...")
        setup_clean_cluster()
        if not start_cluster():
            raise RuntimeError("Failed to start cluster for Workload A1")

        rep = run_blast_timed(prefix, duration_sec=60, batch_size=1000, rate_limit=0)
        t_blocks = parse_val0_timeline(os.path.join(EVIDENCE_DIR, f"logs_{prefix}"))
        metrics = compute_steady_state_metrics(t_blocks) if t_blocks else None

        run_summary = {
            "round": r,
            "effective_tps": rep.get("effective_tps"),
            "total_confirmed": rep.get("total_confirmed"),
            "blocks_produced": rep.get("blocks_produced"),
            "zero_fork_verified": rep.get("zero_fork_verified"),
            "steady_state_metrics": metrics
        }
        summary["workload_a1_unlimited_60s"].append(run_summary)
        print(f"   Round {r} Summary: Effective TPS={rep.get('effective_tps'):.1f}, Blocks={rep.get('blocks_produced')}")
        if metrics:
            print(f"   Steady State: Ta={metrics['avg_ta_computation_ms']:.1f}ms, Tb={metrics['avg_tb_persist_ms']:.1f}ms, Cycle={metrics['avg_cycle_ms']:.1f}ms, Potential Amdahl Delta={metrics['delta_tps_potential_pct']:.1f}%")

    # ══════════════════════════════════════════════════════════════════════
    # PART 2: Workload A2 — 5 Rounds of 60s Rate-Limited Blast (4500 tx/s, Batch 500)
    # ══════════════════════════════════════════════════════════════════════
    print("\n" + "=" * 60)
    print("📍 [WORKLOAD A2] 5 Rounds of 60-Second Rate-Limited Blast (4,500 tx/s)")
    print("=" * 60)
    for r in range(1, 6):
        prefix = f"a2_ratelimited_60s_run_{r}"
        print(f"\n▶️ Starting Workload A2 Round {r}/5...")
        setup_clean_cluster()
        if not start_cluster():
            raise RuntimeError("Failed to start cluster for Workload A2")

        rep = run_blast_timed(prefix, duration_sec=60, batch_size=500, rate_limit=4500)
        t_blocks = parse_val0_timeline(os.path.join(EVIDENCE_DIR, f"logs_{prefix}"))
        metrics = compute_steady_state_metrics(t_blocks) if t_blocks else None

        run_summary = {
            "round": r,
            "effective_tps": rep.get("effective_tps"),
            "total_confirmed": rep.get("total_confirmed"),
            "blocks_produced": rep.get("blocks_produced"),
            "zero_fork_verified": rep.get("zero_fork_verified"),
            "steady_state_metrics": metrics
        }
        summary["workload_a2_ratelimited_60s"].append(run_summary)
        print(f"   Round {r} Summary: Effective TPS={rep.get('effective_tps'):.1f}, Blocks={rep.get('blocks_produced')}")

    # ══════════════════════════════════════════════════════════════════════
    # PART 3: Workload A3 — Batch Size Statistical Rigor (10 Rounds Each)
    # ══════════════════════════════════════════════════════════════════════
    print("\n" + "=" * 60)
    print("📍 [WORKLOAD A3] Batch Size Statistical Rigor (10 Rounds x Batch 250, 500, 1000)")
    print("=" * 60)
    for b_size in [250, 500, 1000]:
        key = f"batch_{b_size}"
        print(f"\n--- Testing Batch Size = {b_size} (10 rounds) ---")
        for r in range(1, 11):
            prefix = f"a3_batch_{b_size}_run_{r}"
            setup_clean_cluster()
            if not start_cluster():
                raise RuntimeError(f"Failed to start cluster for batch {b_size}")
            rep = run_blast_timed(prefix, duration_sec=0, batch_size=b_size, count=25000)
            summary["workload_a3_batch_isolation"][key].append({
                "round": r,
                "effective_tps": rep.get("effective_tps"),
                "commit_duration": rep.get("commit_duration"),
                "zero_fork_verified": rep.get("zero_fork_verified")
            })

    # Save complete summary
    out_file = os.path.join(EVIDENCE_DIR, "phase_a_steady_state_summary.json")
    with open(out_file, "w") as f:
        json.dump(summary, f, indent=2)
    print(f"\n🎉 ALL PHASE A RUNS FINISHED! Summary saved to {out_file}")

if __name__ == "__main__":
    main()
