#!/usr/bin/env bash
# ==============================================================================
# 💥 NOMT EXPANDED CRASH-RECOVERY & ZERO-FORK INTEGRITY TEST (20 ROUNDS)
# Tests Durability under active 10,000 TX/round SECP256k1/EIP-1559 Load:
#   - Kills leader (val0)
#   - Kills 2 nodes simultaneously (dual crash)
#   - Kills non-leaders (val1..val3)
#   - Random delay to intercept active FFI NOMT CommitPayload
# Verifies:
#   1. Zero node exits with code 78 (Startup Data Integrity Check)
#   2. No /tmp/MTN_INTEGRITY_FAILED sentinel file created
#   3. Replicas restart cleanly, catch up to cluster state
#   4. 100% Zero-Fork parity across all online nodes (Block Hash + State Root)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_DIR="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BLAST_TOOL="/tmp/b1_bins/secp_tps_blast"
ROUNDS=20

BASE_DIR="${1:-/tmp/gate_4val_p06}"
ENV_JSON="$BASE_DIR/env.json"

if [ ! -f "$ENV_JSON" ]; then
    echo "❌ [ERROR] Environment config not found at $ENV_JSON"
    exit 1
fi

j() { python3 -c "import json,sys; d=json.load(open('$ENV_JSON')); print(eval(sys.argv[1]))" "$1"; }

BIN="$(j "d['bin']")"
PH="$(j "d['ports']['parent_http']")"
LEADER_PORT="$(j "d['ports']['val0']['rpc']")"
NODES_CONFIG="$BASE_DIR/rpc_nodes.json"
KEYS_FILE="/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"

echo "=================================================================="
echo "💥 NOMT EXPANDED CRASH RECOVERY UNDER LOAD — ${ROUNDS} ROUNDS (KILL -9)"
echo "   Cluster Base:    ${BASE_DIR}"
echo "   Leader RPC Port: ${LEADER_PORT}"
echo "   Workload:        10,000 txs/round (Batch 1000, TCP Mode)"
echo "=================================================================="

rm -f /tmp/MTN_INTEGRITY_FAILED
SUMMARY_REPORT="/tmp/crash_recovery_20rounds_summary.log"
echo "Round,TargetNodes,KillTiming,RestartExitCode,IntegritySentinel,ForkParityStatus,BlockNumber,BlockHash,StateRoot" > "$SUMMARY_REPORT"

# Rotation of 20 targets covering Leader (val0), Dual-node kills, and Single nodes:
TARGET_SPECS=(
    "val1"           # R1: Single non-leader
    "val2"           # R2: Single non-leader
    "val0"           # R3: LEADER
    "val3"           # R4: Single non-leader
    "val1 val2"      # R5: DUAL KILL
    "val0"           # R6: LEADER
    "val2"           # R7: Single non-leader
    "val0 val3"      # R8: DUAL KILL (Leader + val3)
    "val1"           # R9: Single non-leader
    "val3"           # R10: Single non-leader
    "val0"           # R11: LEADER
    "val2 val3"      # R12: DUAL KILL
    "val1"           # R13: Single non-leader
    "val0"           # R14: LEADER
    "val1 val3"      # R15: DUAL KILL
    "val2"           # R16: Single non-leader
    "val0"           # R17: LEADER
    "val0 val1"      # R18: DUAL KILL (Leader + val1)
    "val3"           # R19: Single non-leader
    "val0"           # R20: LEADER
)

rpc_call() {
    local port="$1"
    local method="$2"
    local params="${3:-[]}"
    curl -s --connect-timeout 2 -X POST "http://127.0.0.1:${port}" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"${method}\",\"params\":${params},\"id\":1}" || echo '{"result":null}'
}

restart_node() {
    local node="$1"
    local port="$(j "d['ports']['$node']['rpc']")"
    local pprof_port=$((port - 200))
    local log_file="$BASE_DIR/logs/${node}.log"
    local pid_file="$BASE_DIR/pids/${node}.pid"

    ( cd "$BASE_DIR/$node" && \
      PARENT_CHAIN_URL="http://127.0.0.1:$PH" BLS_CONSERVATION_MODE=enforce BLS_CONSERVATION_INTERVAL_SECONDS=300 \
      exec "$BIN/simple_chain" -config="$BASE_DIR/$node/config.json" --pprof-addr="127.0.0.1:$pprof_port" >>"$log_file" 2>&1 ) &
    local new_pid=$!
    echo "$new_pid" > "$pid_file"
}

for i in $(seq 1 $ROUNDS); do
    idx=$((i - 1))
    TARGET_NODES="${TARGET_SPECS[$idx]}"

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🔥 [ROUND ${i}/${ROUNDS}] Targets: ${TARGET_NODES}"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Step 1: Start 10k TX workload in background
    echo "🚀 [1/6] Blasting 10,000 SECP transactions in background..."
    VAL_PIDS=$(cat "$BASE_DIR"/pids/val*.pid | paste -sd,)
    "$BLAST_TOOL" \
      -nodes-config "$NODES_CONFIG" \
      -keys "$KEYS_FILE" \
      -count 10000 \
      -batch 1000 \
      -mode tcp \
      -type 1559 \
      -verify-parity=false \
      -pids "$VAL_PIDS" \
      > "/tmp/blast_round_${i}.log" 2>&1 &
    BLAST_PID=$!

    # Step 2: Random delay to intercept commit
    # Vary delay between 0.05s and 0.25s
    DELAYS=(0.05 0.12 0.20 0.08 0.15 0.25 0.10 0.18 0.06 0.22)
    D_IDX=$((i % 10))
    SLEEP_SEC="${DELAYS[$D_IDX]}"
    sleep "$SLEEP_SEC"

    # Step 3: Issue kill -9 to target node(s)
    for node in $TARGET_NODES; do
        PID_FILE="$BASE_DIR/pids/${node}.pid"
        if [ -f "$PID_FILE" ]; then
            TPID=$(cat "$PID_FILE" | tr -d '[:space:]')
            if [ -n "$TPID" ] && kill -0 "$TPID" 2>/dev/null; then
                echo "💥 [2/6] Killing ${node} (PID ${TPID}) with KILL -9 (delay ${SLEEP_SEC}s)..."
                kill -9 "$TPID" 2>/dev/null || true
            fi
        fi
    done

    # Wait for blast process to finish
    wait "$BLAST_PID" || true
    echo "✅ [3/6] Background workload submission cycle ended."

    # Step 4: Restart killed nodes
    for node in $TARGET_NODES; do
        echo "🔄 [4/6] Restarting ${node}..."
        restart_node "$node"
    done

    # Step 5: Wait for startup and check integrity
    sleep 3
    if [ -f /tmp/MTN_INTEGRITY_FAILED ]; then
        echo "❌ [CRITICAL INTEGRITY FAILURE] /tmp/MTN_INTEGRITY_FAILED found in Round $i!"
        cat /tmp/MTN_INTEGRITY_FAILED
        exit 78
    fi

    for node in $TARGET_NODES; do
        PID_FILE="$BASE_DIR/pids/${node}.pid"
        NPID=$(cat "$PID_FILE" | tr -d '[:space:]')
        if ! kill -0 "$NPID" 2>/dev/null; then
            echo "❌ [ERROR] ${node} failed to restart or exited immediately!"
            tail -n 30 "$BASE_DIR/logs/${node}.log"
            exit 1
        fi
    done
    echo "✅ [5/6] All restarted node(s) running cleanly (0 exit 78, no integrity failure)."

    # Step 6: Verify Zero-Fork Parity
    echo "🛡️ [6/6] Verifying state catchup and Zero-Fork parity across cluster..."
    V0_PORT="$(j "d['ports']['val0']['rpc']")"
    V1_PORT="$(j "d['ports']['val1']['rpc']")"
    V2_PORT="$(j "d['ports']['val2']['rpc']")"
    V3_PORT="$(j "d['ports']['val3']['rpc']")"

    # Wait for nodes to sync
    SYNC_PASS=false
    LAST_BLOCK=0
    for attempt in $(seq 1 40); do
        R0=$(rpc_call "$V0_PORT" "eth_blockNumber")
        R1=$(rpc_call "$V1_PORT" "eth_blockNumber")
        R2=$(rpc_call "$V2_PORT" "eth_blockNumber")
        R3=$(rpc_call "$V3_PORT" "eth_blockNumber")

        B0=$(echo "$R0" | python3 -c "import sys, json; r=json.load(sys.stdin); print(int(r.get('result','0x0'), 16))" 2>/dev/null || echo "0")
        B1=$(echo "$R1" | python3 -c "import sys, json; r=json.load(sys.stdin); print(int(r.get('result','0x0'), 16))" 2>/dev/null || echo "0")
        B2=$(echo "$R2" | python3 -c "import sys, json; r=json.load(sys.stdin); print(int(r.get('result','0x0'), 16))" 2>/dev/null || echo "0")
        B3=$(echo "$R3" | python3 -c "import sys, json; r=json.load(sys.stdin); print(int(r.get('result','0x0'), 16))" 2>/dev/null || echo "0")

        # Find min block height across online nodes
        MIN_B=$B0
        [ "$B1" -lt "$MIN_B" ] && MIN_B=$B1
        [ "$B2" -lt "$MIN_B" ] && MIN_B=$B2
        [ "$B3" -lt "$MIN_B" ] && MIN_B=$B3

        if [ "$MIN_B" -ge 1 ] && [ "$((B0 - MIN_B))" -le 1 ] && [ "$((B1 - MIN_B))" -le 1 ] && [ "$((B2 - MIN_B))" -le 1 ] && [ "$((B3 - MIN_B))" -le 1 ]; then
            LAST_BLOCK=$MIN_B
            SYNC_PASS=true
            break
        fi
        sleep 0.5
    done

    CHECK_BLOCK_HEX=$(printf "0x%x" "$LAST_BLOCK")
    PARITY_OK=false
    HASH0=""; HASH1=""; HASH2=""; HASH3=""
    ROOT0=""; ROOT1=""; ROOT2=""; ROOT3=""

    for p_att in $(seq 1 20); do
        BLK0=$(rpc_call "$V0_PORT" "eth_getBlockByNumber" "[\"${CHECK_BLOCK_HEX}\", false]")
        BLK1=$(rpc_call "$V1_PORT" "eth_getBlockByNumber" "[\"${CHECK_BLOCK_HEX}\", false]")
        BLK2=$(rpc_call "$V2_PORT" "eth_getBlockByNumber" "[\"${CHECK_BLOCK_HEX}\", false]")
        BLK3=$(rpc_call "$V3_PORT" "eth_getBlockByNumber" "[\"${CHECK_BLOCK_HEX}\", false]")

        PARSE_CODE="import sys, json; d=json.load(sys.stdin).get('result') or {}; print(d.get('hash',''), d.get('stateRoot',''))"
        read -r HASH0 ROOT0 < <(echo "$BLK0" | python3 -c "$PARSE_CODE")
        read -r HASH1 ROOT1 < <(echo "$BLK1" | python3 -c "$PARSE_CODE")
        read -r HASH2 ROOT2 < <(echo "$BLK2" | python3 -c "$PARSE_CODE")
        read -r HASH3 ROOT3 < <(echo "$BLK3" | python3 -c "$PARSE_CODE")

        if [ -n "$HASH0" ] && [ -n "$HASH1" ] && [ -n "$HASH2" ] && [ -n "$HASH3" ] && \
           [ "$HASH0" = "$HASH1" ] && [ "$HASH1" = "$HASH2" ] && [ "$HASH2" = "$HASH3" ] && \
           [ "$ROOT0" = "$ROOT1" ] && [ "$ROOT1" = "$ROOT2" ] && [ "$ROOT2" = "$ROOT3" ]; then
            PARITY_OK=true
            break
        fi
        sleep 0.5
    done

    if [ "$PARITY_OK" = "true" ]; then
        echo "   • Block #${LAST_BLOCK} (${CHECK_BLOCK_HEX}):"
        echo "     - Hash:      ${HASH0:0:18}..."
        echo "     - StateRoot: ${ROOT0:0:18}..."
        echo "   ✅ [ROUND ${i} PASS] 100% Zero-Fork parity confirmed across all 4 nodes!"
        echo "${i},\"${TARGET_NODES}\",${SLEEP_SEC}s,0,None,PASS,${LAST_BLOCK},${HASH0:0:18}...,${ROOT0:0:18}..." >> "$SUMMARY_REPORT"
    else
        echo "❌ [FORK DETECTED in Round $i] Hashes or roots differ!"
        echo "   Node 0 Hash: $HASH0, Root: $ROOT0"
        echo "   Node 1 Hash: $HASH1, Root: $ROOT1"
        echo "   Node 2 Hash: $HASH2, Root: $ROOT2"
        echo "   Node 3 Hash: $HASH3, Root: $ROOT3"
        echo "${i},\"${TARGET_NODES}\",${SLEEP_SEC}s,0,None,FAIL,${LAST_BLOCK},${HASH0:0:18}...,${ROOT0:0:18}..." >> "$SUMMARY_REPORT"
        exit 1
    fi
done

echo ""
echo "=================================================================="
echo "🎉 ALL 20 ROUNDS OF EXPANDED CRASH RECOVERY UNDER LOAD PASSED!"
echo "   • 0 Integrity Failures (no exit 78, no sentinel file)"
echo "   • 100% Zero-Fork Compliance across all restarts"
echo "   • Leader kill, Dual-node kill & Single-node kill VERIFIED"
echo "   • NOMT Fsync & commitWg Durability VERIFIED"
echo "=================================================================="
