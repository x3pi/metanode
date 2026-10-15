#!/usr/bin/env python3
"""Raft (3 replicas, quorum 2/3) measured with the chain-counted secp_tps_blast tool, fresh cluster per run.

Derived from run_raft_benchmark.py (cluster layout, config, leader discovery) with: ports in the 31xxx range, work dir
/tmp/raft_counted_cluster, binaries taken from --bins, processes stopped through their own Popen handles (never by pattern),
and the counted measurement (-verify-parity, per-block counting, validator ingress counters) instead of the legacy estimate.

Usage: run_raft_counted.py --bins DIR --out DIR --tag TAG --mode {burst,sustained} --runs N [--count 25000] [--max-txs 1000000]
"""
import argparse, json, os, secrets, shutil, subprocess, sys, time, urllib.request
from pathlib import Path

KEYS = "/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"
GENESIS = "/tmp/gate_4val_clean_template/exec2/genesis.json"
WORK = Path("/tmp/raft_counted_cluster")
SEQ_PRIV = "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
SEQ_ADDR = "0x0d4CC97b62a149a8fe8DE81262270426A80B0935"
NODES = [
    {"id": "n0", "rpc": 31810, "tcp": 31710, "raft": 31910, "fwd": 31610, "bootstrap": True},
    {"id": "n1", "rpc": 31811, "tcp": 31711, "raft": 31911, "fwd": 31611, "bootstrap": False},
    {"id": "n2", "rpc": 31812, "tcp": 31712, "raft": 31912, "fwd": 31612, "bootstrap": False},
]

def rpc(url, method, params=None, timeout=3.0):
    data = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or []}).encode()
    try:
        with urllib.request.urlopen(urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"}), timeout=timeout) as r:
            d = json.loads(r.read()); return d.get("result"), d.get("error")
    except Exception as e:
        return None, str(e)

def wait_rpc(port, timeout=40.0):
    t0 = time.time()
    while time.time() - t0 < timeout:
        res, err = rpc(f"http://127.0.0.1:{port}", "eth_blockNumber")
        if err is None and res is not None:
            return True
        time.sleep(0.5)
    return False

def stop(procs):
    for _, p, f in procs:
        try:
            p.terminate()
        except Exception:
            pass
    t0 = time.time()
    while time.time() - t0 < 25 and any(p.poll() is None for _, p, _ in procs):
        time.sleep(0.5)
    for _, p, f in procs:
        if p.poll() is None:
            p.kill()
        try:
            f.close()
        except Exception:
            pass

def setup(bins):
    shutil.rmtree(WORK, ignore_errors=True); WORK.mkdir(parents=True)
    secret = WORK / "raft_secret.key"; secret.write_bytes(secrets.token_bytes(32)); os.chmod(secret, 0o600)
    for n in NODES:
        nd = WORK / n["id"]; nd.mkdir()
        cfg = {"debug": False, "cluster_id": 1, "consensus_mode": "raft", "enable_private_gateway": False,
               "master_password": "devnet-test-password", "app_pepper": "devnet-test-pepper", "private_key": SEQ_PRIV, "address": SEQ_ADDR,
               "log_path": str(nd / "logs"), "backup_path": str(nd / "backup"), "explorer_db_path": str(nd / "explorer"),
               "explorer_read_only_db_path": str(nd / "explorer-ro"), "is_explorer": False, "connection_address": f"0.0.0.0:{n['tcp']}",
               "version": "0.0.1.0", "rpc_port": f":{n['rpc']}", "db_type": 2, "genesis_file_path": GENESIS, "snapshot_enabled": False,
               "tx_signature_mode": "secp",
               "Databases": {"RootPath": str(nd / "data"), "DBEngine": "sharded", "Version": "0.0.1.0", "BLSPrivateKey": SEQ_PRIV},
               "is_rpc_node": True,
               "raft": {"node_id": n["id"], "bind_address": f"127.0.0.1:{n['raft']}", "advertise_address": f"127.0.0.1:{n['raft']}",
                        "data_dir": str(nd / "raft"), "forward_bind_address": f"127.0.0.1:{n['fwd']}", "forward_secret_file": str(secret),
                        "sequencer_address": SEQ_ADDR, "bootstrap": n["bootstrap"], "heartbeat_timeout_ms": 100, "election_timeout_ms": 200,
                        "leader_lease_timeout_ms": 80, "commit_timeout_ms": 30, "propose_queue_size": 2048,
                        "peers": [{"id": m["id"], "address": f"127.0.0.1:{m['raft']}", "forward_address": f"127.0.0.1:{m['fwd']}"} for m in NODES]}}
        (nd / "config.json").write_text(json.dumps(cfg, indent=2))
    procs = []
    for n in NODES:
        nd = WORK / n["id"]; lf = open(nd / "node.log", "w")
        env = os.environ.copy(); env["ENABLE_DEBUG_PPROF"] = "false"
        p = subprocess.Popen([f"{bins}/simple_chain", "-config", str(nd / "config.json")], stdout=lf, stderr=lf, cwd=str(nd), env=env)
        procs.append((n["id"], p, lf))
    for n in NODES:
        if not wait_rpc(n["rpc"]):
            stop(procs); return None
    time.sleep(3)
    return procs

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bins", required=True); ap.add_argument("--out", required=True); ap.add_argument("--tag", required=True)
    ap.add_argument("--mode", choices=["burst", "sustained"], required=True); ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--count", type=int, default=25000); ap.add_argument("--max-txs", type=int, default=0)
    ap.add_argument("--duration", type=int, default=300); ap.add_argument("--batch", type=int, default=1000)
    ap.add_argument("--max-wait-sec", type=int, default=600)
    a = ap.parse_args(); out = Path(a.out); out.mkdir(parents=True, exist_ok=True)
    for i in range(1, a.runs + 1):
        print(f"=== RAFT RUN {i}/{a.runs} {a.mode} ===", flush=True)
        procs = setup(a.bins)
        if procs is None:
            print(json.dumps({"run": i, "error": "cluster did not come up"}), flush=True); continue
        rpcnodes = {"nodes": {n["id"]: f"http://127.0.0.1:{n['rpc']}" for n in NODES}, "tcp_nodes": {n["id"]: f"127.0.0.1:{n['tcp']}" for n in NODES}}
        (WORK / "rpc_nodes.json").write_text(json.dumps(rpcnodes, indent=2))
        rep = out / f"{a.tag}_run{i}.json"
        cmd = [f"{a.bins}/secp_tps_blast", "-nodes-config", str(WORK / "rpc_nodes.json"), "-mode", "tcp", "-type", "1559", "-keys", KEYS,
               "-pids", ",".join(str(p.pid) for _, p, _ in procs), "-verify-parity", "-report", str(rep), "-max-wait-sec", str(a.max_wait_sec), "-quiet-sec", "8"]
        if a.mode == "burst":
            cmd += ["-count", str(a.count), "-batch", str(a.batch), "-rounds", "1"]
        else:
            cmd += ["-duration", str(a.duration), "-batch", str(a.batch)]
            if a.max_txs:
                cmd += ["-max-txs", str(a.max_txs)]
        t0 = time.time(); r = subprocess.run(cmd, capture_output=True, text=True, errors="replace")
        (out / f"{a.tag}_run{i}.log").write_text(r.stdout + "\n--STDERR--\n" + r.stderr)
        alive = {nid: (p.poll() is None) for nid, p, _ in procs}
        # keep node-log lines that matter for a post-mortem
        for nid, _, _ in procs:
            lp = WORK / nid / "node.log"
            if lp.exists():
                t = subprocess.run(["bash", "-c", f"grep -a -i -E 'panic|fatal|poison|\\[nomt_ffi\\]|leader|election|stepping down' '{lp}' | tail -n 200 | cut -c1-300"], capture_output=True, text=True, errors="replace").stdout
                (out / f"{a.tag}_run{i}_{nid}_events.txt").write_text(t)
        stop(procs)
        info = {"run": i, "wall_s": round(time.time() - t0, 1), "exit": r.returncode, "alive_at_end": alive}
        if rep.exists():
            j = json.load(open(rep)); m = j.get("measurement") or {}
            info.update({k: m.get(k) for k in ("bytes_sent_txs", "committed_ours", "loss_vs_sent", "tps_end_to_end", "tps_during_injection", "tps_active_commit_window", "inject_seconds", "end_to_end_seconds", "parity_status", "measurement_stop_reason", "ingress_metrics_ok")})
            info["received_sum"] = sum((m.get("received_by_validator") or {}).values())
        print(json.dumps(info), flush=True)
    print("RAFT-ALL-DONE", flush=True)

if __name__ == "__main__":
    main()
