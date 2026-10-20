import json, os, shlex, shutil, subprocess, sys, time, urllib.request
sys.path.insert(0, "/home/abc/chain-n/metanode/execution/scripts/test/evidence")
import run_tps_counted as r
BASE = "/tmp/gate_4val_fsize"; r.BASE = BASE
BINS, NS, OUT = sys.argv[1], sys.argv[2], sys.argv[3]
MODE = {"stake_db": "stake", "smart_contract_storage": "contract"}.get(NS)
MNT, IMG, DM = "/mnt/nomt_eio", "/tmp/claude-1001/nomt_eio.img", "nomt_eio"
SECT = 1048576  # 512 MB
def sudo(cmd, check=False):
    p = subprocess.run(["bash", "-c", "echo '1234@abcd' | sudo -S -p '' bash -c " + shlex.quote(cmd)], capture_output=True, text=True)
    if check and p.returncode != 0: raise RuntimeError(cmd + ": " + p.stderr[-300:])
    return p
def dm_load(table):
    sudo(f"dmsetup suspend {DM} && dmsetup load {DM} --table {shlex.quote(table)} && dmsetup resume {DM}", True)
def rpc(port, m, p):
    q = urllib.request.Request(f"http://127.0.0.1:{port}", json.dumps({"jsonrpc":"2.0","method":m,"params":p,"id":1}).encode(), {"content-type":"application/json"})
    return json.load(urllib.request.urlopen(q, timeout=5)).get("result")
def load(tag, skip=0):
    if MODE:
        cmd = [f"{BINS}/nomt_ns_blast", "-rpc", "http://127.0.0.1:31647", "-keys", r.KEYS, "-mode", MODE, "-n", "100", "-skip", str(skip), "-per", "40", "-wait-sec", "20"]
    else:
        pm = r.pids()
        cmd = [f"{BINS}/secp_tps_blast", "-nodes-config", f"{BASE}/rpc_nodes.json", "-mode", "tcp", "-type", "1559", "-keys", r.KEYS, "-pids", ",".join(str(p) for p in pm.values()),
               "-verify-parity", "-report", f"{OUT}/{tag}.json", "-max-wait-sec", "120", "-quiet-sec", "15", "-count", "50000", "-batch", "500", "-rounds", "1"]
    p = subprocess.run(cmd, capture_output=True, text=True, timeout=900)
    open(f"{OUT}/{tag}.out", "w").write(p.stdout + p.stderr)
    return [l.strip() for l in (p.stdout + p.stderr).splitlines() if "mode=" in l or "parity:" in l or "loss_vs_sent" in l or "error" in l]
def cleanup():
    sudo(f"umount {MNT}; dmsetup remove -f {DM}; for l in $(losetup -j {IMG} | cut -d: -f1); do losetup -d $l; done; rmdir {MNT}")
    try: os.remove(IMG)
    except OSError: pass
os.makedirs(OUT, exist_ok=True)
try:
    r.stop_and_clean(); cleanup()
    left = subprocess.run("ps -eo args | grep -E '[s]imple_chain|[p]arent_chain' | wc -l", shell=True, capture_output=True, text=True).stdout.strip()
    assert left == "0", left
    assert r.sh(["cp", "-a", r.TEMPLATE, BASE]).returncode == 0
    r.rewrite_paths(BASE, "/tmp/gate_4val_p06", BASE); assert r.leftover_old_paths(BASE, "/tmp/gate_4val_p06") == 0
    subprocess.run(["truncate", "-s", "512M", IMG], check=True)
    dev = sudo(f"losetup -f --show {IMG}", True).stdout.strip(); print("loop", dev)
    sudo(f"dmsetup create {DM} --table '0 {SECT} linear {dev} 0' && mkfs.ext4 -q -F /dev/mapper/{DM} && mkdir -p {MNT} && mount /dev/mapper/{DM} {MNT} && chown 1001:1001 {MNT}", True)
    nd = f"{BASE}/val0/data/consensus/nomt_db/{NS}"
    assert r.sh(["cp", "-a", nd + "/.", MNT + "/"]).returncode == 0
    shutil.rmtree(nd); os.symlink(MNT, nd)
    d = json.load(open(f"{BASE}/env.json")); d["bin"] = BINS; json.dump(d, open(f"{BASE}/env.json", "w"), indent=2)
    env = os.environ.copy(); env["ENABLE_DEBUG_PPROF"] = "false"
    print("start rc", r.sh([r.RUN_ENV, BASE, "start"], env=env).returncode, "not ready:", r.wait_tcp_ready())
    pm = r.pids()
    ports = {n: json.load(open(f"{BASE}/env.json"))["ports"][n]["rpc"] for n in ("val0","val1","val2","val3")}
    def heights():
        o = {}
        for n, pt in ports.items():
            try: o[n] = int(rpc(pt, "eth_blockNumber", []), 16)
            except Exception: o[n] = "down"
        return o
    print("before fault:", heights())
    # every write to the device now fails with EIO (reads still work)
    dm_load(f"0 {SECT} flakey {dev} 0 0 1 1 error_writes")
    print("fault armed")
    print("phase1:", load("p1"))
    time.sleep(5)
    print("val0 alive after fault+load:", os.path.exists(f"/proc/{pm['val0']}"), "heights:", heights())
    # recover the device and the filesystem
    assert not os.path.exists(f"/proc/{pm['val0']}"), "val0 must be dead before the device is restored"
    sudo(f"umount {MNT}", False)
    dm_load(f"0 {SECT} linear {dev} 0")
    fs = sudo(f"e2fsck -fy /dev/mapper/{DM}; mount /dev/mapper/{DM} {MNT}", False); print("fsck/mount rc", fs.returncode)
    print("restart:", r.sh([r.RUN_ENV, BASE, "start_node", "val0"]).stdout.strip()[-60:])
    t0 = time.time()
    while time.time() - t0 < 180:
        h = heights()
        if all(isinstance(v, int) for v in h.values()) and h["val0"] >= max(h.values()) - 1: break
        time.sleep(2)
    print("heights after restart:", heights())
    print("phase2:", load("p2", 1000)); time.sleep(10)
    h = heights(); print("heights after phase2:", h)
    common = min(v for v in h.values() if isinstance(v, int)); hs = {}
    for n, pt in ports.items():
        try: b = rpc(pt, "eth_getBlockByNumber", [hex(common), False]); hs[n] = (b["hash"], b["stateRoot"])
        except Exception: hs[n] = "ERR"
    print("common", common, "all equal:", len({str(v) for v in hs.values()}) == 1, hs.get("val0"))
finally:
    for v in ("val0","val1","val2","val3"):
        os.makedirs(f"{OUT}/{v}", exist_ok=True); r.sh(["cp", "-a", f"{BASE}/{v}/logs", f"{OUT}/{v}/"])
    r.stop_and_clean(); time.sleep(3); cleanup()
    print("mounts left:", subprocess.run("mount | grep -c nomt_eio", shell=True, capture_output=True, text=True).stdout.strip())
