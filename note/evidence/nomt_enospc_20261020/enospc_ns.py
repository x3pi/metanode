import json, os, subprocess, sys, time, shutil, urllib.request
sys.path.insert(0, "/home/abc/chain-n/metanode/execution/scripts/test/evidence")
import run_tps_counted as r
BASE = "/tmp/gate_4val_fsize"; r.BASE = BASE
BINS, NS, OUT = sys.argv[1], sys.argv[2], sys.argv[3]
EXTRA = int(sys.argv[4]) if len(sys.argv) > 4 else 3
MNT = "/mnt/nomt_small"
PW = "echo '1234@abcd' | sudo -S -p '' "
import shlex
def sudo(cmd): return subprocess.run(["bash", "-c", PW + "bash -c " + shlex.quote(cmd)], capture_output=True, text=True)
os.makedirs(OUT, exist_ok=True)
def rpc(port, m, p):
    q = urllib.request.Request(f"http://127.0.0.1:{port}", json.dumps({"jsonrpc":"2.0","method":m,"params":p,"id":1}).encode(), {"content-type":"application/json"})
    return json.load(urllib.request.urlopen(q, timeout=5)).get("result")
def blast(tag, count):
    pm = r.pids()
    cmd = [f"{BINS}/secp_tps_blast", "-nodes-config", f"{BASE}/rpc_nodes.json", "-mode", "tcp", "-type", "1559", "-keys", r.KEYS,
           "-pids", ",".join(str(p) for p in pm.values()), "-verify-parity", "-report", f"{OUT}/{tag}.json",
           "-max-wait-sec", "120", "-quiet-sec", "15", "-count", str(count), "-batch", "500", "-rounds", "1"]
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
    open(f"{OUT}/{tag}.out", "w").write(p.stdout + p.stderr)
    return [l.strip() for l in (p.stdout + p.stderr).splitlines() if "parity:" in l or "loss_vs_sent" in l]
try:
    r.stop_and_clean(); sudo(f"umount {MNT}")
    left = subprocess.run("ps -eo args | grep -E '[s]imple_chain|[p]arent_chain' | wc -l", shell=True, capture_output=True, text=True).stdout.strip()
    assert left == "0", "leftover chain processes: " + left
    assert r.sh(["cp", "-a", r.TEMPLATE, BASE]).returncode == 0
    r.rewrite_paths(BASE, "/tmp/gate_4val_p06", BASE)
    assert r.leftover_old_paths(BASE, "/tmp/gate_4val_p06") == 0
    nd = f"{BASE}/val0/data/consensus/nomt_db/{NS}"
    used = r.disk_mb(nd); size = used + EXTRA
    assert sudo(f"mkdir -p {MNT} && mount -t tmpfs -o size={size}m,uid=1001 tmpfs {MNT}").returncode == 0
    assert r.sh(["cp", "-a", nd + "/.", MNT + "/"]).returncode == 0
    shutil.rmtree(nd); os.symlink(MNT, nd)
    d = json.load(open(f"{BASE}/env.json")); d["bin"] = BINS; json.dump(d, open(f"{BASE}/env.json", "w"), indent=2)
    env = os.environ.copy(); env["ENABLE_DEBUG_PPROF"] = "false"
    print("start rc", r.sh([r.RUN_ENV, BASE, "start"], env=env).returncode)
    print("not ready:", r.wait_tcp_ready())
    pm = r.pids(); print("ns", NS, "used_mb", used, "tmpfs_mb", size, r.sh(["df", "-m", MNT]).stdout.splitlines()[-1])
    print("phase1:", blast("p1", 400000))
    time.sleep(5)
    print("val0 alive after phase1:", os.path.exists(f"/proc/{pm['val0']}"), r.sh(["df", "-m", MNT]).stdout.splitlines()[-1])
    ports = {n: json.load(open(f"{BASE}/env.json"))["ports"][n]["rpc"] for n in ("val0","val1","val2","val3")}
    def heights():
        o = {}
        for n, pt in ports.items():
            try: o[n] = int(rpc(pt, "eth_blockNumber", []), 16)
            except Exception as e: o[n] = "down"
        return o
    print("heights:", heights())
    assert not os.path.exists(f"/proc/{pm['val0']}"), "val0 must be dead before restart"
    sudo(f"mount -o remount,size=900m {MNT}")
    print("restart:", r.sh([r.RUN_ENV, BASE, "start_node", "val0"]).stdout.strip()[-80:])
    t0 = time.time()
    while time.time() - t0 < 180:
        h0 = heights()
        if all(isinstance(v, int) for v in h0.values()) and h0["val0"] >= max(h0.values()) - 1 and h0["val0"] > 2: break
        time.sleep(2)
    print("heights after restart (caught up in %.0fs):" % (time.time() - t0), heights())
    print("phase2 (new load, all 4 up):", blast("p2", 50000))
    time.sleep(10)
    h = heights(); print("heights after phase2:", h)
    ints = [v for v in h.values() if isinstance(v, int)]
    common = min(ints); hs = {}
    for n, pt in ports.items():
        try: b = rpc(pt, "eth_getBlockByNumber", [hex(common), False]); hs[n] = (b["hash"], b["stateRoot"])
        except Exception as e: hs[n] = "ERR"
    print("common", common, "all equal:", len({str(v) for v in hs.values()}) == 1, hs)
    for v in ("val0","val1","val2","val3"):
        os.makedirs(f"{OUT}/{v}", exist_ok=True); r.sh(["cp", "-a", f"{BASE}/{v}/logs", f"{OUT}/{v}/"])
    r.sh(["bash","-c",f"cp -a {BASE}/logs {OUT}/logs"])
finally:
    r.stop_and_clean(); time.sleep(3); print("umount:", sudo(f"umount {MNT}; rmdir {MNT}").returncode)
