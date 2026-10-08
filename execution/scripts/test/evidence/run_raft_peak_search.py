#!/usr/bin/env python3
"""
run_raft_peak_search.py - Find Peak Saturation Throughput for 3-Node Raft Consensus.

Tests scaling workloads: 10,000 -> 20,000 -> 30,000 -> 50,000 transactions
using real EIP-1559 signed txs from 50,000 distinct pre-funded accounts.
Measures Peak Effective TPS, block size dynamics, latency percentiles, and verifies Zero-Fork.
"""

import os
import sys
import time
import json
import secrets
import subprocess
import urllib.request
from pathlib import Path

REPO_ROOT = Path("/home/abc/chain-n/metanode")
BIN_SIMPLE_CHAIN = Path("/tmp/p06_bins/simple_chain")
BIN_BLAST = Path("/tmp/p06_bins/secp_tps_blast")
BIN_ROLLUP_CLUSTER = Path("/tmp/p06_bins/rollup-cluster")
KEYS_FILE = Path("/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json")
GENESIS_FILE = Path("/tmp/gate_4val_clean_template/exec2/genesis.json")
WORK_DIR = Path("/tmp/raft_peak_cluster")
EVIDENCE_DIR = REPO_ROOT / "note/evidence/tps_prepare_tx_opt_20261008"

SEQ_PRIV = "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
SEQ_ADDR = "0x0d4CC97b62a149a8fe8DE81262270426A80B0935"

NODES = [
    {"id": "n0", "rpc": 8810, "tcp": 4310, "raft": 7110, "fwd": 7210, "bootstrap": True},
    {"id": "n1", "rpc": 8811, "tcp": 4311, "raft": 7111, "fwd": 7211, "bootstrap": False},
    {"id": "n2", "rpc": 8812, "tcp": 4312, "raft": 7112, "fwd": 7212, "bootstrap": False},
]

def kill_existing():
    subprocess.run(["pkill", "-9", "-f", "simple_chain.*raft_peak_cluster"], stderr=subprocess.DEVNULL)
    time.sleep(1)

def rpc_call(url: str, method: str, params: list = None, timeout: float = 3.0):
    if params is None:
        params = []
    payload = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode("utf-8")
    req = urllib.request.Request(url, data=payload, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            if "error" in data:
                return None, data["error"]
            return data.get("result"), None
    except Exception as e:
        return None, str(e)

def wait_for_rpc(node_rpc: int, timeout: float = 30.0) -> bool:
    url = f"http://127.0.0.1:{node_rpc}"
    t0 = time.time()
    while time.time() - t0 < timeout:
        res, err = rpc_call(url, "eth_blockNumber")
        if err is None and res is not None:
            return True
        time.sleep(0.5)
    return False

def discover_leader(secret_file: Path) -> dict:
    cluster_cfg = {
        "secret_file": str(secret_file),
        "members": [
            {"id": n["id"], "raft_addr": f"127.0.0.1:{n['raft']}", "admin_addr": f"127.0.0.1:{n['fwd']}"}
            for n in NODES
        ]
    }
    cluster_json_path = WORK_DIR / "cluster.json"
    cluster_json_path.write_text(json.dumps(cluster_cfg, indent=2))

    t0 = time.time()
    while time.time() - t0 < 20.0:
        res = subprocess.run([str(BIN_ROLLUP_CLUSTER), "-config", str(cluster_json_path), "check"],
                             capture_output=True, text=True)
        if res.returncode == 0:
            try:
                status = json.loads(res.stdout)
                leader_id = status.get("leader")
                if leader_id:
                    for n in NODES:
                        if n["id"] == leader_id:
                            return n
            except Exception:
                pass
        time.sleep(0.5)
    return NODES[0]

def setup_cluster():
    kill_existing()
    if WORK_DIR.exists():
        import shutil
        shutil.rmtree(WORK_DIR, ignore_errors=True)
    WORK_DIR.mkdir(parents=True, exist_ok=True)

    secret_file = WORK_DIR / "raft_secret.key"
    secret_bytes = secrets.token_bytes(32)
    secret_file.write_bytes(secret_bytes)
    os.chmod(secret_file, 0o600)

    for n in NODES:
        ndir = WORK_DIR / n["id"]
        ndir.mkdir(parents=True, exist_ok=True)
        cfg = {
            "debug": False,
            "cluster_id": 1,
            "consensus_mode": "raft",
            "enable_private_gateway": False,
            "master_password": "devnet-test-password",
            "app_pepper": "devnet-test-pepper",
            "private_key": SEQ_PRIV,
            "address": SEQ_ADDR,
            "log_path": str(ndir / "logs"),
            "backup_path": str(ndir / "backup"),
            "explorer_db_path": str(ndir / "explorer"),
            "explorer_read_only_db_path": str(ndir / "explorer-ro"),
            "is_explorer": False,
            "connection_address": f"0.0.0.0:{n['tcp']}",
            "version": "0.0.1.0",
            "rpc_port": f":{n['rpc']}",
            "db_type": 2,
            "genesis_file_path": str(GENESIS_FILE),
            "snapshot_enabled": False,
            "tx_signature_mode": "secp",
            "Databases": {
                "RootPath": str(ndir / "data"),
                "DBEngine": "sharded",
                "Version": "0.0.1.0",
                "BLSPrivateKey": SEQ_PRIV,
            },
            "is_rpc_node": True,
            "raft": {
                "node_id": n["id"],
                "bind_address": f"127.0.0.1:{n['raft']}",
                "advertise_address": f"127.0.0.1:{n['raft']}",
                "data_dir": str(ndir / "raft"),
                "forward_bind_address": f"127.0.0.1:{n['fwd']}",
                "forward_secret_file": str(secret_file),
                "sequencer_address": SEQ_ADDR,
                "bootstrap": n["bootstrap"],
                "heartbeat_timeout_ms": 100,
                "election_timeout_ms": 200,
                "leader_lease_timeout_ms": 80,
                "commit_timeout_ms": 30,
                "propose_queue_size": 4096,
                "peers": [
                    {"id": m["id"], "address": f"127.0.0.1:{m['raft']}", "forward_address": f"127.0.0.1:{m['fwd']}"}
                    for m in NODES
                ]
            }
        }
        (ndir / "config.json").write_text(json.dumps(cfg, indent=2))

    processes = []
    print("🚀 Khởi chạy Cụm 3 Node Raft n0, n1, n2 (Propose Queue: 4096)...")
    for n in NODES:
        ndir = WORK_DIR / n["id"]
        cfg_file = ndir / "config.json"
        log_file = open(ndir / "node.log", "w")
        p = subprocess.Popen([str(BIN_SIMPLE_CHAIN), "-config", str(cfg_file)],
                             stdout=log_file, stderr=log_file, cwd=str(ndir))
        processes.append((n["id"], p, log_file))

    print("⏳ Chờ bầu Leader và RPC sẵn sàng...")
    for n in NODES:
        if not wait_for_rpc(n["rpc"], timeout=25.0):
            print(f"❌ Không kết nối được RPC {n['id']}")
            return None, secret_file, processes
        print(f"   ✅ Node {n['id']} RPC sẵn sàng tại port :{n['rpc']}")

    leader = discover_leader(secret_file)
    print(f"👑 Leader cụm: Node {leader['id']} (RPC :{leader['rpc']}, TCP :{leader['tcp']})")
    time.sleep(2)
    return leader, secret_file, processes

def main():
    print("=" * 80)
    print("🎯 KHẢO SÁT ĐỈNH THÔNG LƯỢNG TỐI ĐA (PEAK TPS) CỤM RAFT (10K -> 50K TXS)")
    print("=" * 80)

    EVIDENCE_DIR.mkdir(parents=True, exist_ok=True)
    leader, secret_file, processes = setup_cluster()
    if leader is None:
        print("❌ Lỗi khởi tạo cụm.")
        sys.exit(1)

    rpc_nodes_cfg = {
        "nodes": {n["id"]: f"http://127.0.0.1:{n['rpc']}" for n in NODES},
        "tcp_nodes": {n["id"]: f"127.0.0.1:{n['tcp']}" for n in NODES},
        "roles": {n["id"]: "leader" if n["id"] == leader["id"] else "follower" for n in NODES}
    }
    rpc_nodes_path = WORK_DIR / "rpc_nodes.json"
    rpc_nodes_path.write_text(json.dumps(rpc_nodes_cfg, indent=2))

    pids_arg = ",".join(str(p.pid) for _, p, _ in processes)

    stages = [
        {"name": "Stage_10k", "count": 10000, "batch": 1000},
        {"name": "Stage_20k", "count": 20000, "batch": 1000},
        {"name": "Stage_30k", "count": 30000, "batch": 1000},
        {"name": "Stage_50k", "count": 50000, "batch": 1000},
    ]

    results = []

    try:
        for idx, s in enumerate(stages, start=1):
            print(f"\n" + "-" * 70)
            print(f"🔥 [NẤC {idx}/{len(stages)}] Bơm {s['count']:,} txs EIP-1559 (batch {s['batch']})...")
            print("-" * 70)
            report_file = EVIDENCE_DIR / f"raft_peak_{s['name']}.json"

            cmd = [
                str(BIN_BLAST),
                "-nodes-config", str(rpc_nodes_path),
                "-rpc", f"http://127.0.0.1:{leader['rpc']}",
                "-tcp", f"127.0.0.1:{leader['tcp']}",
                "-mode", "tcp",
                "-type", "1559",
                "-count", str(s["count"]),
                "-batch", str(s["batch"]),
                "-keys", str(KEYS_FILE),
                "-pids", pids_arg,
                "-verify-parity",
                "-report", str(report_file),
            ]

            t0 = time.time()
            res = subprocess.run(cmd, capture_output=True, text=True)
            t_cmd = time.time() - t0

            if res.returncode != 0:
                print(f"⚠️ Lỗi chạy blast: {res.stderr}\n{res.stdout}")
            else:
                print(f"   Lệnh hoàn tất sau {t_cmd:.2f}s")

            if report_file.exists():
                with open(report_file, "r") as rf:
                    d = json.load(rf)
                eff_tps = d.get("effective_tps", 0.0)
                inj_tps = d.get("injection_tps", 0.0)
                dur = d.get("commit_duration", "N/A")
                blks = d.get("blocks_produced", 0)
                p50 = d.get("latency_p50", 0) / 1e6
                p99 = d.get("latency_p99", 0) / 1e6
                cpu = d.get("max_cpu", "N/A")
                rss = d.get("max_rss_mb", 0)
                zero_fork = d.get("zero_fork_verified", False)
                print(f"   ⚡ Effective TPS:    {eff_tps:,.2f} tx/s")
                print(f"   ⏱️ Commit Duration:  {dur}")
                print(f"   📦 Blocks Produced:  {blks} blocks (Trung bình ~{int(s['count']/max(1, blks)):,} txs/block)")
                print(f"   ⏳ Latency:          P50 = {p50:.1f}ms | P99 = {p99:.1f}ms")
                print(f"   💻 Peak Resource:    CPU = {cpu} | RSS = {rss:,} MB")
                print(f"   🛡️ Zero-Fork Parity: {zero_fork}")
                results.append({
                    "stage": s["name"],
                    "count": s["count"],
                    "effective_tps": eff_tps,
                    "injection_tps": inj_tps,
                    "commit_duration": dur,
                    "blocks": blks,
                    "avg_txs_per_block": int(s['count'] / max(1, blks)),
                    "latency_p50_ms": p50,
                    "latency_p99_ms": p99,
                    "peak_cpu": cpu,
                    "peak_rss_mb": rss,
                    "zero_fork_verified": zero_fork
                })
            time.sleep(3)

        summary_file = EVIDENCE_DIR / "raft_peak_search_summary.json"
        summary_data = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "cluster": "3-Node Replicated HashiCorp Raft (Quorum 2/3)",
            "binary": str(BIN_SIMPLE_CHAIN),
            "stages": results
        }
        summary_file.write_text(json.dumps(summary_data, indent=2))
        print(f"\n🎉 Hoàn tất toàn bộ bài kiểm tra đỉnh TPS! Đã lưu: {summary_file}")

    finally:
        kill_existing()
        for _, _, f in processes:
            try:
                f.close()
            except Exception:
                pass

if __name__ == "__main__":
    main()
