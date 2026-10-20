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
time.sleep(5)
alive = {n: os.path.exists(f"/proc/{pid}") for n, pid in pm.items()}
print("alive", alive)
r.sh(["bash", "-c", f"cp -a {BASE}/logs {OUT}/logs; for v in val0 val1 val2 val3; do mkdir -p {OUT}/$v; cp -a {BASE}/$v/logs {OUT}/$v/ 2>/dev/null; done"])
json.dump({"alive": alive, "fsize": FSIZE}, open(f"{OUT}/summary.json", "w"))
r.stop_and_clean()
