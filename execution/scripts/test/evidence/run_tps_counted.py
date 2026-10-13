#!/usr/bin/env python3
"""
Counted-TPS runner (BFT 4-validator cluster, fresh copy of the template per run).

Runs the chain-counting `secp_tps_blast` (secp_tps_blast/2) N times and, in the SAME run, records the legacy
(estimated) numbers the old tool would have printed, so old and new methods can be compared without any
cross-run noise. Nothing is extrapolated or averaged by hand: every row is read from the tool's JSON report.

Usage:
  run_tps_counted.py --mode burst|sustained --runs N --tag NAME --bins DIR --out DIR
         [--count 25000] [--batch 1000] [--duration 60] [--rate-limit 0] [--keep-logs]
Isolation: cluster in /tmp/gate_4val_tps_counted (ports from the template, 31xxx); never touches /opt/metanode or other PIDs.
"""
import argparse
import csv
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from stats_util import mean, sample_sd, t_two_tailed_p, t_critical

REPO = "/home/abc/chain-n/metanode"
TEMPLATE = "/tmp/gate_4val_clean_template"
BASE = "/tmp/gate_4val_tps_counted"
RUN_ENV = f"{REPO}/execution/scripts/test/gate_e2e/run_env.sh"
KEYS = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
NODES = ["val0", "val1", "val2", "val3"]


def sh(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, check=False, **kw)


def stop_and_clean():
    if os.path.isdir(BASE):
        sh([RUN_ENV, BASE, "stop"])
        time.sleep(1)
        shutil.rmtree(BASE, ignore_errors=True)


def disk_mb(path):
    r = sh(["du", "-sm", path])
    try:
        return int(r.stdout.split()[0])
    except Exception:
        return -1


def pids():
    out = {}
    for n in NODES:
        p = f"{BASE}/pids/{n}.pid"
        if os.path.isfile(p):
            out[n] = int(open(p).read().strip())
    return out


def rss_mb(pidmap):
    tot = 0
    for pid in pidmap.values():
        try:
            for line in open(f"/proc/{pid}/status"):
                if line.startswith("VmRSS:"):
                    tot += int(line.split()[1]) // 1024
        except Exception:
            pass
    return tot


SKIP_DIRS = {"data", "consensus_storage", "parentchain_db", "consensus", "snapshot", "backup", "explorer", "explorer-ro", "logs", "rocksdb_dummy_init"}


def rewrite_paths(root, old, new):
    """Rewrite absolute template paths in small text config files (never touches DB directories). Returns #files changed."""
    changed = 0
    for dp, dns, fns in os.walk(root):
        dns[:] = [d for d in dns if d not in SKIP_DIRS]
        for fn in fns:
            if not fn.endswith((".json", ".toml", ".sh", ".env")):
                continue
            fp = os.path.join(dp, fn)
            try:
                if os.path.getsize(fp) > 4_000_000:
                    continue
                b = open(fp, "rb").read()
            except OSError:
                continue
            if old.encode() in b:
                open(fp, "wb").write(b.replace(old.encode(), new.encode()))
                changed += 1
    return changed


def leftover_old_paths(root, old):
    n = 0
    for dp, dns, fns in os.walk(root):
        dns[:] = [d for d in dns if d not in SKIP_DIRS]
        for fn in fns:
            fp = os.path.join(dp, fn)
            try:
                if os.path.getsize(fp) <= 4_000_000 and old.encode() in open(fp, "rb").read():
                    n += 1
            except OSError:
                pass
    return n


def wait_tcp_ready(timeout=90):
    """Every validator's TCP ingress port must accept connections before the tool starts: the first sustained attempt
    found one validator's port still refusing (connect refused), so that validator received no load at all."""
    nodes = json.load(open(f"{BASE}/rpc_nodes.json")).get("tcp_nodes", {})
    pending = {n: a for n, a in nodes.items()}
    end = time.time() + timeout
    while pending and time.time() < end:
        for n, a in list(pending.items()):
            host, port = a.rsplit(":", 1)
            try:
                socket.create_connection((host, int(port)), timeout=1).close()
                del pending[n]
            except OSError:
                pass
        if pending:
            time.sleep(0.5)
    return sorted(pending), len(nodes)


def one_run(i, args, outdir):
    stop_and_clean()
    # cp -a keeps the sparse NOMT files sparse. shutil.copytree densified them (apparent size ~80 GB per validator,
    # ~320 GB per run) and filled the shared filesystem during the first attempt of this runner.
    cp = sh(["cp", "-a", TEMPLATE, BASE])
    if cp.returncode != 0:
        return {"run": i, "error": f"template copy failed: {cp.stderr[-200:]}"}
    # The template was generated with base=/tmp/gate_4val_p06 and its configs embed that ABSOLUTE path (data, logs,
    # consensus storage, keys). Without rewriting, every "fresh" copy would read/write the SAME /tmp/gate_4val_p06
    # directories and state would silently accumulate across runs. Rewrite them to this run's BASE.
    n_rewritten = rewrite_paths(BASE, "/tmp/gate_4val_p06", BASE)
    left = leftover_old_paths(BASE, "/tmp/gate_4val_p06")
    if left:
        stop_and_clean()
        return {"run": i, "error": f"{left} config files still reference /tmp/gate_4val_p06 after rewrite; refusing to run (would share state)"}
    env_json = f"{BASE}/env.json"
    d = json.load(open(env_json))
    d["bin"] = args.bins
    json.dump(d, open(env_json, "w"), indent=2)
    env = os.environ.copy()
    env["ENABLE_DEBUG_PPROF"] = "false"
    t0 = time.time()
    r = sh([RUN_ENV, BASE, "start"], env=env)
    if r.returncode != 0:
        return {"run": i, "error": f"cluster start failed: {r.stderr[-300:]}"}
    start_s = time.time() - t0
    not_ready, n_tcp = wait_tcp_ready()
    if not_ready:
        stop_and_clean()
        return {"run": i, "error": f"TCP ingress not ready after wait on: {not_ready}"}
    pm = pids()
    info = {"run": i, "paths_rewritten_files": n_rewritten, "cluster_start_seconds": round(start_s, 1), "node_pids": pm, "baseline_rss_mb": rss_mb(pm),
            "template_disk_mb_val0": disk_mb(f"{BASE}/val0"), "uptime_before": open("/proc/loadavg").read().strip()}
    rep = f"{outdir}/{args.tag}_run{i}.json"
    cmd = [f"{args.bins}/secp_tps_blast", "-nodes-config", f"{BASE}/rpc_nodes.json", "-mode", "tcp", "-type", "1559",
           "-keys", KEYS, "-pids", ",".join(str(p) for p in pm.values()), "-verify-parity", "-report", rep,
           "-max-wait-sec", str(args.max_wait_sec), "-quiet-sec", str(args.quiet_sec)]
    if args.mode == "burst":
        cmd += ["-count", str(args.count), "-batch", str(args.batch), "-rounds", "1"]
    else:
        cmd += ["-duration", str(args.duration), "-batch", str(args.batch)]
        if args.rate_limit:
            cmd += ["-rate-limit", str(args.rate_limit)]
    t1 = time.time()
    b = sh(cmd)
    info["blast_wall_seconds"] = round(time.time() - t1, 1)
    open(f"{outdir}/{args.tag}_run{i}.log", "w").write(b.stdout + "\n--STDERR--\n" + b.stderr)
    info["blast_exit"] = b.returncode
    mconn = re.search(r"(\d+) TCP connections", b.stdout)
    info["tcp_connections_used"] = int(mconn.group(1)) if mconn else None
    info["tcp_endpoints"] = n_tcp
    info["uptime_after"] = open("/proc/loadavg").read().strip()
    info["peak_rss_mb_tool"] = None
    if os.path.isfile(rep):
        j = json.load(open(rep))
        m = j.get("measurement") or {}
        leg = j.get("legacy_estimate_do_not_report") or {}
        info.update({
            "tool_version": j.get("tool_version"),
            "bytes_sent": m.get("bytes_sent_txs"),
            "received_sum": sum((m.get("received_by_validator") or {}).values()) if m.get("ingress_metrics_ok") else None,
            "rejected_sum": sum((m.get("rejected_by_validator") or {}).values()) if m.get("ingress_metrics_ok") else None,
            "committed_ours": m.get("committed_ours"),
            "committed_total": m.get("committed_total_txs"),
            "ours_exact": m.get("committed_ours_exact"),
            "ours_upper_bound": m.get("committed_ours_upper_bound"),
            "tps_end_to_end_upper": m.get("tps_end_to_end_upper_bound"),
            "loss": m.get("loss_vs_sent"),
            "tps_end_to_end": m.get("tps_end_to_end"),
            "tps_during_injection": m.get("tps_during_injection"),
            "tps_active_commit": m.get("tps_active_commit_window"),
            "drain_seconds": m.get("drain_seconds"),
            "backlog_at_inject_end": m.get("backlog_at_inject_end"),
            "inject_seconds": m.get("inject_seconds"),
            "end_to_end_seconds": m.get("end_to_end_seconds"),
            "parity": m.get("parity_status"),
            "stop_reason": m.get("measurement_stop_reason"),
            "legacy_effective_tps": leg.get("effective_tps"),
            "legacy_confirmed": leg.get("extrapolated_confirmed"),
            "latency_p50_ms": round(j.get("latency_p50", 0) / 1e6),
            "peak_rss_mb_tool": j.get("max_rss_mb"),
        })
    else:
        info["error"] = "tool produced no JSON report"
    if info.get("tcp_connections_used") is not None and info["tcp_connections_used"] != info["tcp_endpoints"]:
        info["error"] = f"tool used {info['tcp_connections_used']} of {info['tcp_endpoints']} TCP endpoints: some validator got no load"
    # blocks from the chain, independent of the tool, for a second source
    stop_and_clean()
    return info


def stats_line(name, xs):
    xs = [x for x in xs if x is not None]
    if len(xs) < 2:
        return f"{name}: n={len(xs)}"
    return f"{name}: n={len(xs)} mean={mean(xs):.2f} sd={sample_sd(xs):.2f} min={min(xs):.2f} max={max(xs):.2f}"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", choices=["burst", "sustained"], required=True)
    ap.add_argument("--runs", type=int, default=7)
    ap.add_argument("--tag", required=True)
    ap.add_argument("--bins", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--count", type=int, default=25000)
    ap.add_argument("--batch", type=int, default=1000)
    ap.add_argument("--duration", type=int, default=60)
    ap.add_argument("--rate-limit", type=int, default=0)
    ap.add_argument("--max-wait-sec", type=int, default=120)
    ap.add_argument("--quiet-sec", type=int, default=8)
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)
    print(f"== run_tps_counted mode={args.mode} runs={args.runs} tag={args.tag} bins={args.bins} head={sh(['git','-C',REPO,'rev-parse','--short','HEAD']).stdout.strip()}")
    print(f"== uname: {os.uname().release}  cpus={os.cpu_count()}  loadavg(start)={open('/proc/loadavg').read().strip()}", flush=True)
    rows = []
    for i in range(1, args.runs + 1):
        print(f"\n=== RUN {i}/{args.runs} ===", flush=True)
        row = one_run(i, args, args.out)
        rows.append(row)
        print(json.dumps({k: v for k, v in row.items() if k not in ("node_pids", "stop_reason")}), flush=True)
        print(f"    stop_reason: {row.get('stop_reason')}", flush=True)
    keys = ["run", "bytes_sent", "received_sum", "rejected_sum", "committed_ours", "ours_exact", "ours_upper_bound", "committed_total", "loss",
            "tps_end_to_end", "tps_end_to_end_upper", "tps_during_injection", "tps_active_commit", "drain_seconds", "backlog_at_inject_end",
            "inject_seconds", "end_to_end_seconds", "legacy_effective_tps", "legacy_confirmed", "latency_p50_ms",
            "parity", "baseline_rss_mb", "peak_rss_mb_tool", "cluster_start_seconds", "blast_exit", "tcp_connections_used", "tcp_endpoints", "uptime_before", "uptime_after", "error"]
    with open(f"{args.out}/{args.tag}_summary.csv", "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=keys, extrasaction="ignore")
        w.writeheader()
        for r in rows:
            w.writerow(r)
    ok = [r for r in rows if r.get("tps_end_to_end") is not None]
    print("\n=== SUMMARY (all numbers read from tool JSON; paired old-vs-new within the SAME run) ===")
    print(stats_line("tps_end_to_end(new, counted)", [r["tps_end_to_end"] for r in ok]))
    print(stats_line("tps_during_injection(new)", [r["tps_during_injection"] for r in ok]))
    print(stats_line("legacy_effective_tps(old estimate)", [r["legacy_effective_tps"] for r in ok]))
    diffs = [r["legacy_effective_tps"] - r["tps_end_to_end"] for r in ok if r.get("legacy_effective_tps") is not None]
    if len(diffs) >= 3:
        md, sd = mean(diffs), sample_sd(diffs)
        n = len(diffs)
        se = sd / (n ** 0.5) if sd > 0 else 0.0
        t = md / se if se > 0 else float("inf")
        p = t_two_tailed_p(t, n - 1) if se > 0 else 0.0
        tc = t_critical(n - 1)
        print(f"paired (legacy - counted): mean={md:.2f} sd={sd:.2f} n={n} t={t:.2f} df={n-1} p={p:.4f} CI95=[{md-tc*se:.2f},{md+tc*se:.2f}]")
    print(stats_line("loss_vs_sent", [r["loss"] for r in ok]))
    print("parity statuses:", sorted({str(r.get('parity')) for r in rows}))
    errs = [r for r in rows if r.get("error")]
    print(f"runs with errors: {len(errs)}")
    sys.exit(0 if not errs else 2)


if __name__ == "__main__":
    main()
