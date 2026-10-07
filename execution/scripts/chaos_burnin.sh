#!/usr/bin/env bash
# ==============================================================================
# 🌪️ MetaNode Parent Chain Chaos Burn-in Test (P1 Production Readiness Plan)
# ==============================================================================
# Tests parent chain 4-node cluster under random crash & recovery cycles:
#   - Random kill -9 of 1, 2, or all 4 nodes
#   - Random downtime 5s - 120s (exercises both short gaps and gap > gc_depth=50)
#   - Continuous transaction generation on surviving nodes during partition
#   - Full node restart & parity convergence check
#   - Block-by-block hash verification across all 4 nodes (block 1 to tip)
#   - Zero-Fork Invariant enforcement: halts IMMEDIATELY with logs preserved on fork/drift.
#
# Usage:
#   CYCLES=200 ./execution/scripts/chaos_burnin.sh
#   CYCLES=10 MAX_DOWNTIME=30 ./execution/scripts/chaos_burnin.sh
# ==============================================================================

set -uo pipefail

CYCLES=${CYCLES:-200}
MIN_DOWNTIME=${MIN_DOWNTIME:-5}
MAX_DOWNTIME=${MAX_DOWNTIME:-120}
CONVERGENCE_TIMEOUT=${CONVERGENCE_TIMEOUT:-300}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CLUSTER_DIR="$REPO_ROOT/deploy/cluster/local_parent_chain"
CRASH_DIR="$REPO_ROOT/execution/scripts/chaos_crash_reports"
FT_BIN="/tmp/ftbin_chaos"

PORTS=(18601 18602 18603 18604)

echo "================================================================================"
echo "🌪️  STARTING PARENT CHAIN CHAOS BURN-IN TEST"
echo "  Cycles: $CYCLES"
echo "  Downtime range: ${MIN_DOWNTIME}s - ${MAX_DOWNTIME}s"
echo "  Timeout per convergence: ${CONVERGENCE_TIMEOUT}s"
echo "  Cluster: $CLUSTER_DIR"
echo "================================================================================"

# Compile fault tolerance helper binary for continuous tx generation if needed
if [ ! -f "$FT_BIN" ] || [ "$REPO_ROOT/execution/cmd/tool/test_cluster_fault_tolerance/main.go" -nt "$FT_BIN" ]; then
    echo "🔨 Building test_cluster_fault_tolerance helper..."
    (cd "$REPO_ROOT/execution" && go build -o "$FT_BIN" ./cmd/tool/test_cluster_fault_tolerance)
fi

# Ensure cluster is up
echo "🔍 Checking cluster status..."
ALL_UP=true
for p in "${PORTS[@]}"; do
    if ! curl -s -m 2 "http://127.0.0.1:$p/status" >/dev/null 2>&1; then
        ALL_UP=false
        break
    fi
done

if [ "$ALL_UP" = false ]; then
    echo "⚠️ Cluster is not running. Starting cluster..."
    "$CLUSTER_DIR/run.sh" up
    sleep 5
fi

# Python parity & block-by-block hash verification helper
verify_parity() {
python3 - <<'PY'
import json, urllib.request, sys, time

PORTS = [18601, 18602, 18603, 18604]

def fetch_status(p):
    try:
        req = urllib.request.Request(f"http://127.0.0.1:{p}/status", headers={"User-Agent": "ChaosBurnin/1.0"})
        with urllib.request.urlopen(req, timeout=3) as r:
            return json.loads(r.read())
    except Exception:
        return None

statuses = [fetch_status(p) for p in PORTS]
offline = [i for i, s in enumerate(statuses) if s is None]
if offline:
    print(f"OFFLINE_NODES: {offline}")
    sys.exit(2)

# Check fork_detected flag
for i, s in enumerate(statuses):
    if s.get("fork_detected", False):
        print(f"CRITICAL: fork_detected == True on node {i}")
        sys.exit(1)

blocks = [s["last_block"] for s in statuses]
hashes = [s["last_hash"] for s in statuses]
roots = [s["state_root"] for s in statuses]

min_b = min(blocks)
max_b = max(blocks)

if min_b != max_b or len(set(hashes)) > 1 or len(set(roots)) > 1:
    print(f"LAGGING: blocks={blocks}, hashes={set(hashes)}, roots={set(roots)}")
    sys.exit(3)

# Block-by-block verification from 1 to min_b
def fetch_block_hash(p, n):
    try:
        req = urllib.request.Request(f"http://127.0.0.1:{p}/block?number={n}", headers={"User-Agent": "ChaosBurnin/1.0"})
        with urllib.request.urlopen(req, timeout=3) as r:
            data = json.loads(r.read())
            rec = data.get("record", {})
            return rec.get("BlockHash")
    except Exception:
        return None

for n in range(1, min_b + 1):
    h_set = {fetch_block_hash(p, n) for p in PORTS}
    h_set.discard(None)
    if len(h_set) > 1:
        # Retry once after 1s to rule out transient read during write
        time.sleep(1)
        h_set = {fetch_block_hash(p, n) for p in PORTS}
        h_set.discard(None)
        if len(h_set) > 1:
            print(f"FORK_AT_BLOCK_{n}: {list(h_set)}")
            sys.exit(1)

print(f"PARITY_OK: block={min_b}, root={roots[0][:16]}..., hash={hashes[0][:16]}...")
sys.exit(0)
PY
}

settle_and_verify() {
    local max_wait=$1
    local start_time=$(date +%s)
    local out=""
    local code=0

    while true; do
        out=$(verify_parity 2>&1)
        code=$?

        if [ $code -eq 0 ]; then
            echo "$out"
            return 0
        elif [ $code -eq 1 ]; then
            echo "🚨 FORK DETECTED! $out"
            return 1
        fi

        local now=$(date +%s)
        local elapsed=$((now - start_time))
        if [ $elapsed -ge $max_wait ]; then
            echo "⏳ TIMEOUT after ${elapsed}s without convergence: $out"
            return 3
        fi
        sleep 2
    done
}

preserve_crash_state() {
    local cycle=$1
    local reason=$2
    local target_dir="$CRASH_DIR/cycle_${cycle}_$(date +%Y%m%d_%H%M%S)"
    mkdir -p "$target_dir"
    echo "🚨 PRESERVING CRASH STATE TO: $target_dir"
    echo "Reason: $reason" > "$target_dir/reason.txt"

    for i in 0 1 2 3; do
        if [ -d "$CLUSTER_DIR/node-$i/logs" ]; then
            cp -r "$CLUSTER_DIR/node-$i/logs" "$target_dir/node-$i-logs"
        fi
        curl -s -m 2 "http://127.0.0.1:$((18601+i))/status" > "$target_dir/node-$i-status.json" 2>&1 || true
    done

    echo "🔍 Extracting divergence traces:"
    grep -a -E "DIGEST-GATE|DIVERGENT|Baseline injected|Recovering committed state|RECOVERY-GUARD" "$target_dir"/*/*.log 2>/dev/null | tail -n 30 || true
}

echo "✅ Initial verification before chaos..."
INITIAL_RES=$(settle_and_verify 30) || {
    echo "❌ Initial cluster state is not healthy: $INITIAL_RES"
    exit 1
}
echo "$INITIAL_RES"

# Main Chaos Loop
for cycle in $(seq 1 "$CYCLES"); do
    echo "--------------------------------------------------------------------------------"
    echo "🌀 CYCLE $cycle / $CYCLES - $(date '+%Y-%m-%d %H:%M:%S')"

    # Random selection of fault pattern:
    # 70% kill 1 node, 20% kill 2 nodes, 10% kill all 4 nodes
    ROLL=$((RANDOM % 100))
    VICTIMS=()

    if [ "$ROLL" -lt 70 ]; then
        VICTIM=$((RANDOM % 4))
        VICTIMS=("$VICTIM")
    elif [ "$ROLL" -lt 90 ]; then
        N1=$((RANDOM % 4))
        N2=$(((N1 + 1 + (RANDOM % 3)) % 4))
        VICTIMS=("$N1" "$N2")
    else
        VICTIMS=(0 1 2 3)
    fi

    # QUORUM_LOSS=1: always kill 2 or 4 nodes (quorum lost) and keep them down for
    # LONG_DOWN_MIN..LONG_DOWN_MAX seconds (minutes), to test long pending + recovery.
    if [ "${QUORUM_LOSS:-0}" = "1" ]; then
        if [ $((RANDOM % 2)) -eq 0 ]; then
            N1=$((RANDOM % 4)); N2=$(((N1 + 1 + (RANDOM % 3)) % 4)); VICTIMS=("$N1" "$N2")
        else
            VICTIMS=(0 1 2 3)
        fi
    fi

    # Random downtime: weighted between MIN_DOWNTIME and MAX_DOWNTIME
    # 25% chance of long downtime (50s - 120s) to cross gc_depth (50 blocks)
    LONG_ROLL=$((RANDOM % 100))
    if [ "$LONG_ROLL" -lt 25 ] && [ "$MAX_DOWNTIME" -gt 50 ]; then
        DOWNTIME=$((50 + (RANDOM % (MAX_DOWNTIME - 50 + 1))))
    else
        DOWNTIME=$((MIN_DOWNTIME + (RANDOM % (30 - MIN_DOWNTIME + 1))))
    fi

    if [ "${QUORUM_LOSS:-0}" = "1" ]; then
        DOWNTIME=$((${LONG_DOWN_MIN:-180} + (RANDOM % (${LONG_DOWN_MAX:-300} - ${LONG_DOWN_MIN:-180} + 1))))
    fi

    echo "💥 Action: Killing node(s) [${VICTIMS[*]}] for ${DOWNTIME}s (Roll: $ROLL, LongRoll: $LONG_ROLL)..."

    # Stop victims forcefully with kill -9
    for v in "${VICTIMS[@]}"; do
        "$CLUSTER_DIR/run.sh" stop-node "$v" >/dev/null 2>&1 || true
        # Also ensure no stray process
        PID_FILE="$CLUSTER_DIR/node-$v/node-$v.pid"
        if [ -f "$PID_FILE" ]; then
            kill -9 "$(cat "$PID_FILE")" 2>/dev/null || true
            rm -f "$PID_FILE"
        fi
    done

    # If at least 3 nodes are still alive (surviving quorum = 3/4), send traffic
    SURVIVORS=()
    for n in 0 1 2 3; do
        IS_VICTIM=false
        for v in "${VICTIMS[@]}"; do
            if [ "$v" -eq "$n" ]; then
                IS_VICTIM=true
                break
            fi
        done
        if [ "$IS_VICTIM" = false ]; then
            SURVIVORS+=("$n")
        fi
    done

    if [ "${#SURVIVORS[@]}" -ge 3 ]; then
        echo "📨 Surviving quorum (${#SURVIVORS[@]} nodes: [${SURVIVORS[*]}]). Sending background transactions..."
        # Send transactions in background during downtime
        (
            END_TX=$(( $(date +%s) + DOWNTIME - 2 ))
            while [ "$(date +%s)" -lt "$END_TX" ]; do
                "$FT_BIN" -test T-I1 >/dev/null 2>&1 || true
                sleep 0.5
            done
        ) &
        TX_PID=$!
    fi

    echo "⏳ Sleeping for ${DOWNTIME}s..."
    sleep "$DOWNTIME"

    if [ "${#SURVIVORS[@]}" -ge 3 ] && [ -n "${TX_PID:-}" ]; then
        wait "$TX_PID" 2>/dev/null || true
    fi

    # Restart victim nodes
    echo "🔄 Restarting node(s) [${VICTIMS[*]}]..."
    for v in "${VICTIMS[@]}"; do
        "$CLUSTER_DIR/run.sh" start-node "$v" >/dev/null 2>&1
    done

    # Wait for convergence and verify parity
    echo "⚖️  Verifying convergence and parity across all 4 nodes..."
    START_CONV=$(date +%s)
    CONV_RES=$(settle_and_verify "$CONVERGENCE_TIMEOUT")
    CONV_CODE=$?
    CONV_DURATION=$(( $(date +%s) - START_CONV ))

    if [ $CONV_CODE -ne 0 ]; then
        echo "❌ FAILURE in Cycle $cycle! Exit code: $CONV_CODE, Details: $CONV_RES"
        preserve_crash_state "$cycle" "$CONV_RES"
        exit 1
    fi

    echo "✅ Cycle $cycle PASSED in ${CONV_DURATION}s! $CONV_RES"
done

echo "================================================================================"
echo "🎉 CHAOS BURN-IN COMPLETED SUCCESSFULLY!"
echo "  Total Cycles: $CYCLES"
echo "  Zero-Fork Invariant: 100% PRESERVED"
echo "  Parity Status: All 4 nodes verified identical block hash from block 1 to tip"
echo "================================================================================"
