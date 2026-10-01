#!/usr/bin/env bash
# Reproduces the parent chain restart divergence (note/parent_chain_next_plan.md, G11).
#
# On a RUNNING local 4-node cluster (deploy/cluster/local_parent_chain, ports 18601-18604) it repeats the
# fault-tolerance tests T-I2 (one node down/up) and T-I3 (quorum lost, then restored) and, after every step,
# compares the block hash of every node at every common height. It stops at the first FORK (same height, different
# hash) or STALL (nodes stay at different heights/hashes 40s after a failed step).
#
#   cd execution && go build -o /tmp/ftbin ./cmd/tool/test_cluster_fault_tolerance
#   FT=/tmp/ftbin ./scripts/test/parent_chain_fork_hunt.sh
set -u
FT=${FT:-/tmp/ftbin}
check() { python3 - <<'PY'
import json,urllib.request,sys
PORTS=(18601,18602,18603,18604)
def st(p):
    try: return json.load(urllib.request.urlopen(f"http://127.0.0.1:{p}/status",timeout=3))
    except Exception: return None
ss=[st(p) for p in PORTS]
if not all(ss): print("OFFLINE"); sys.exit(2)
m=min(s["last_block"] for s in ss)
def h(p,n):
    try: return json.load(urllib.request.urlopen(f"http://127.0.0.1:{p}/block?number={n}",timeout=3))["record"]["BlockHash"]
    except Exception: return None
for n in range(1,m+1):
    if len({h(p,n) for p in PORTS})>1:
        print("FORK at block",n); sys.exit(1)
if len({(s["last_block"],s["last_hash"]) for s in ss})>1: print("LAG/STALL",[s["last_block"] for s in ss]); sys.exit(3)
print("OK",m)
PY
}
$FT -test T-I1 2>&1 | grep -E "PASSED|FAILED" | head -1
for i in $(seq 1 12); do
  for t in T-I2 T-I3; do
    r=$($FT -test $t 2>&1 | grep -E "PASSED|FAILED" | head -1 | cut -c1-60)
    sleep 3; c=$(check); rc=$?
    echo "iter $i $t: $r | $c"
    [ $rc -eq 1 ] && { echo "FORK DETECTED"; exit 1; }
    case "$r" in *FAILED*) sleep 40; c=$(check); echo "   after 40s: $c"; echo "$c" | grep -q "^OK" || { echo "STALL"; exit 3; };; esac
  done
done
echo "no fork/stall in 12 iterations"
