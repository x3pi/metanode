import json, os, subprocess, sys, time
sys.path.insert(0, "/home/abc/chain-n/metanode/execution/scripts/test/evidence")
import run_tps_counted as r
BASE = "/tmp/gate_4val_fsize"; r.BASE = BASE
BINS = sys.argv[1]; MNT = sys.argv[2]; OUT = sys.argv[3]; FSIZE = None
os.makedirs(OUT, exist_ok=True)
r.stop_and_clean()
assert r.sh(["cp", "-a", r.TEMPLATE, BASE]).returncode == 0
r.rewrite_paths(BASE, "/tmp/gate_4val_p06", BASE)
assert r.leftover_old_paths(BASE, "/tmp/gate_4val_p06") == 0
nd = f"{BASE}/val0/data/consensus/nomt_db"
assert os.path.ismount(MNT), "mount a small tmpfs at " + MNT
assert r.sh(["cp", "-a", nd + "/.", MNT + "/"]).returncode == 0
import shutil; shutil.rmtree(nd); os.symlink(MNT, nd)
d = json.load(open(f"{BASE}/env.json")); d["bin"] = BINS; json.dump(d, open(f"{BASE}/env.json", "w"), indent=2)
env = os.environ.copy(); env["ENABLE_DEBUG_PPROF"] = "false"
s = r.sh([r.RUN_ENV, BASE, "start"], env=env); print("start rc", s.returncode, s.stderr[-200:])
print("not ready:", r.wait_tcp_ready())
pm = r.pids(); print("pids", pm)
print(r.sh(["df", "-m", MNT]).stdout)
cmd = [f"{BINS}/secp_tps_blast", "-nodes-config", f"{BASE}/rpc_nodes.json", "-mode", "tcp", "-type", "1559",
       "-keys", r.KEYS, "-pids", ",".join(str(p) for p in pm.values()), "-verify-parity", "-report", f"{OUT}/blast.json",
       "-max-wait-sec", "120", "-quiet-sec", "15", "-count", "400000", "-batch", "500", "-rounds", "1"]
p = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
open(f"{OUT}/blast.out", "w").write(p.stdout + p.stderr)

import urllib.request
def rpc(port, m, p):
    q = urllib.request.Request(f"http://127.0.0.1:{port}", json.dumps({"jsonrpc":"2.0","method":m,"params":p,"id":1}).encode(), {"content-type":"application/json"})
    return json.load(urllib.request.urlopen(q, timeout=5)).get("result")
time.sleep(5)
ports = {n: json.load(open(f"{BASE}/env.json"))["ports"][n]["rpc"] for n in ("val0","val1","val2","val3")}
print("val0 alive after load:", os.path.exists(f"/proc/{pm['val0']}"))
subprocess.run(["bash","-c","echo '1234@abcd' | sudo -S -p '' mount -o remount,size=700m "+MNT], check=True)
print(r.sh([r.RUN_ENV, BASE, "start_node", "val0"]).stdout[-300:])
time.sleep(40)
res = {}
for n, pt in ports.items():
    try: res[n] = int(rpc(pt, "eth_blockNumber", []), 16)
    except Exception as e: res[n] = f"ERR {e}"
print("heights after restart+40s:", res)
common = min(v for v in res.values() if isinstance(v, int))
hs = {}
for n, pt in ports.items():
    try:
        b = rpc(pt, "eth_getBlockByNumber", [hex(common), False]); hs[n] = (b["hash"], b["stateRoot"])
    except Exception as e: hs[n] = f"ERR {e}"
print("common height", common, hs)
print("all equal:", len({str(v) for v in hs.values()}) == 1)
r.sh(["bash","-c",f"for v in val0 val1 val2 val3; do mkdir -p {OUT}/$v; cp -a {BASE}/$v/logs {OUT}/$v/ 2>/dev/null; done"])
json.dump({"heights": res, "common": common, "hashes": hs}, open(f"{OUT}/summary.json","w"), default=str)
r.stop_and_clean()
