#!/usr/bin/env bash
# Regression test for the parent chain restart divergence (note/parent_chain_next_plan.md, G11).
#
# On a RUNNING local 4-node cluster (deploy/cluster/local_parent_chain, ports 18601-18604) it repeatedly runs the
# fault-tolerance steps T-I2 (one node down/up) and T-I3 (quorum lost, then restored). After EVERY step it
#   1. restarts any node left down (a failed step may leave one stopped),
#   2. waits (bounded, polling) until all four nodes agree on height and hash,
#   3. compares the block hash of every node at every common height.
# It fails on the first FORK (same height, different hash) or if the nodes do not converge. Individual step
# failures of the tool itself are only reported: they can be timing artifacts of the test, they are not the invariant.
#
#   cd execution && go build -o /tmp/ftbin ./cmd/tool/test_cluster_fault_tolerance
#   FT=/tmp/ftbin ITER=12 ./scripts/test/parent_chain_fork_hunt.sh
set -u
FT=${FT:-/tmp/ftbin}
ITER=${ITER:-12}
CLUSTER=${CLUSTER:-$(cd "$(dirname "$0")/../../.." && pwd)/deploy/cluster/local_parent_chain}
HUNT_LOG=${HUNT_LOG:-/tmp/hunt}

check() { python3 - <<'PY'
import json,urllib.request,sys
PORTS=(18601,18602,18603,18604)
def st(p):
    try: return json.load(urllib.request.urlopen(f"http://127.0.0.1:{p}/status",timeout=3))
    except Exception: return None
ss=[st(p) for p in PORTS]
off=[i for i,s in enumerate(ss) if not s]
if off: print("OFFLINE",off); sys.exit(2)
m=min(s["last_block"] for s in ss)
def h(p,n):
    try: return json.load(urllib.request.urlopen(f"http://127.0.0.1:{p}/block?number={n}",timeout=3))["record"]["BlockHash"]
    except Exception: return None
import time
for n in range(1,m+1):
    hs={x for x in (h(p,n) for p in PORTS) if x}   # a failed HTTP read (None) is not evidence of a fork
    if len(hs)>1:
        time.sleep(1)                               # re-read once to rule out a transient read
        hs={x for x in (h(p,n) for p in PORTS) if x}
        if len(hs)>1:
            print("FORK at block",n,sorted(x[:10] for x in hs)); sys.exit(1)
if len({(s["last_block"],s["last_hash"]) for s in ss})>1: print("LAG",[s["last_block"] for s in ss]); sys.exit(3)
print("OK",m)
PY
}

# Restart nodes that are down, then wait (bounded polling, no sleeps inside the system under test) for convergence.
settle() {
  local i c rc
  for n in 0 1 2 3; do
    curl -s -m2 localhost:$((18601+n))/status >/dev/null 2>&1 || "$CLUSTER/run.sh" start-node $n >/dev/null 2>&1
  done
  for i in $(seq 1 150); do
    c=$(check); rc=$?
    [ $rc -eq 1 ] && { echo "$c"; return 1; }
    [ $rc -eq 0 ] && { echo "$c"; return 0; }
    sleep 2
  done
  echo "$c (no convergence after 300s)"; return 3
}

$FT -test T-I1 2>&1 | grep -E "PASSED|FAILED" | head -1
settle >/dev/null
for i in $(seq 1 "$ITER"); do
  for t in T-I2 T-I3; do
    out=$($FT -test $t 2>&1); echo "$out" > "$HUNT_LOG.$t.$i.log"
    r=$(echo "$out" | grep -E "PASSED|FAILED" | head -1 | cut -c1-70)
    c=$(settle); rc=$?
    echo "iter $i $t: $r | $c"
    [ $rc -eq 1 ] && { echo "FORK DETECTED"; exit 1; }
    [ $rc -ne 0 ] && { echo "NO CONVERGENCE"; exit 3; }
  done
done
echo "no fork and always converged in $ITER iterations"
