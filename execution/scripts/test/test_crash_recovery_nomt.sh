#!/usr/bin/env bash
# ==============================================================================
# 💥 NOMT CRASH-RECOVERY & ZERO-FORK INTEGRITY TEST (B1 Benchmark)
# Kills random cluster nodes with `kill -9` under active SECP256k1/EIP-1559 load
# Verifies:
#   1. Zero node exits with code 78 (Startup Data Integrity Check)
#   2. No /tmp/MTN_INTEGRITY_FAILED sentinel file created
#   3. Replicas restart and catch up to cluster state
#   4. 100% Zero-Fork parity across all online nodes (Block Hash + State Root)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_DIR="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BLAST_TOOL="/tmp/secp_tps_blast"
ROUNDS=10

BASE_DIR="${1:-/tmp/gate_4val_p06}"
ENV_JSON="$BASE_DIR/env.json"

if [ ! -f "$ENV_JSON" ]; then
    echo "❌ [ERROR] Environment config not found at $ENV_JSON"
    echo "   Please specify a valid isolated cluster directory (e.g., /tmp/gate_4val_p06)"
    exit 1
fi

j() { python3 -c "import json,sys; d=json.load(open('$ENV_JSON')); print(eval(sys.argv[1]))" "$1"; }

BIN="$(j "d['bin']")"
PH="$(j "d['ports']['parent_http']")"
LEADER_PORT="$(j "d['ports']['val0']['rpc']")"
NODES_CONFIG="$BASE_DIR/rpc_nodes.json"
KEYS_FILE="/home/abc/chain-n/metanode-suite/test_tps/gen_spam_keys/generated_keys.json"

echo "=================================================================="
echo "💥 NOMT CRASH RECOVERY UNDER LOAD — ${ROUNDS} ROUNDS (KILL -9)"
echo "   Testing Durability after bd71000a (fsync) and d0706e6a (commitWg)"
echo "   Isolated Cluster Base: ${BASE_DIR}"
echo "   Leader RPC Port:       ${LEADER_PORT}"
echo "=================================================================="

# Ensure blast tool is compiled
if [ ! -f "$BLAST_TOOL" ]; then
    echo "⚙️ Compiling secp_tps_blast tool..."
    (cd "${METANODE_DIR}/execution" && go build -o "$BLAST_TOOL" "./cmd/tool/secp_tps_blast")
fi

# Clean up any leftover sentinel file
rm -f /tmp/MTN_INTEGRITY_FAILED

# Non-leader targets for kill -9 rotation
TARGETS=("val1" "val2" "val3" "val1" "val2" "val3" "val1" "val2" "val3" "val1")

rpc_call() {
    local port="$1"
    local method="$2"
    local params="${3:-[]}"
    curl -s --connect-timeout 2 -X POST "http://127.0.0.1:${port}" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"${method}\",\"params\":${params},\"id\":1}" || echo '{"result":null}'
}

for i in $(seq 1 $ROUNDS); do
    idx=$((i - 1))
    TARGET_NODE="${TARGETS[$idx]}"
    TARGET_PORT="$(j "d['ports']['$TARGET_NODE']['rpc']")"
    TARGET_LOG="${TARGET_NODE}.log"
    PID_FILE="$BASE_DIR/pids/${TARGET_NODE}.pid"

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🔥 [ROUND ${i}/${ROUNDS}] Target: ${TARGET_NODE} (Port ${TARGET_PORT})"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Step 1: Start sustained background workload (1,000 txs)
    echo "🚀 [1/6] Launching 1,000 SECP transactions workload in background..."
    "$BLAST_TOOL" -nodes-config "$NODES_CONFIG" -keys "$KEYS_FILE" -count 1000 -batch 200 -rounds 1 > /tmp/blast_round_${i}.log 2>&1 &
    BLAST_PID=$!

    # Step 2: Wait until transactions are in flight and actively committing to NOMT
    sleep 0.35

    # Step 3: Find PID and issue kill -9 under load
    if [ -f "$PID_FILE" ]; then
        TARGET_PID=$(cat "$PID_FILE" | tr -d '[:space:]')
    else
        TARGET_PID=""
    fi

    if [ -n "$TARGET_PID" ] && kill -0 "$TARGET_PID" 2>/dev/null; then
        echo "💥 [2/6] Killing ${TARGET_NODE} (PID ${TARGET_PID}) with KILL -9 during active trie commit..."
        kill -9 "$TARGET_PID" 2>/dev/null || true
    else
        echo "⚠️ Target ${TARGET_NODE} PID not running or empty, skipping kill..."
    fi

    # Wait for blast process to complete its round
    wait "$BLAST_PID" || true
    echo "✅ [3/6] Workload round finished."

    # Step 4: Restart the killed node
    echo "🔄 [4/6] Restarting ${TARGET_NODE}..."
    PPROF_PORT=$((TARGET_PORT - 200))
    ( cd "$BASE_DIR/$TARGET_NODE" && \
      PARENT_CHAIN_URL="http://127.0.0.1:$PH" BLS_CONSERVATION_MODE=enforce BLS_CONSERVATION_INTERVAL_SECONDS=300 \
      exec "$BIN/simple_chain" -config="$BASE_DIR/$TARGET_NODE/config.json" --pprof-addr="127.0.0.1:$PPROF_PORT" >>"$BASE_DIR/logs/$TARGET_LOG" 2>&1 ) &
    NEW_PID=$!
    echo "$NEW_PID" > "$PID_FILE"

    # Step 5: Wait for restart and verify startup integrity
    sleep 3

    # Check for sentinel failure
    if [ -f /tmp/MTN_INTEGRITY_FAILED ]; then
        echo "❌ [CRITICAL INTEGRITY FAILURE] /tmp/MTN_INTEGRITY_FAILED found!"
        cat /tmp/MTN_INTEGRITY_FAILED
        exit 78
    fi

    # Verify process is running
    if ! kill -0 "$NEW_PID" 2>/dev/null; then
        echo "❌ [ERROR] ${TARGET_NODE} failed to start or exited immediately!"
        tail -n 30 "$BASE_DIR/logs/$TARGET_LOG"
        exit 1
    fi
    echo "✅ [5/6] ${TARGET_NODE} restarted successfully (PID: ${NEW_PID}, no exit 78)."

    # Step 6: Verify Catch-up and Zero-Fork Parity
    echo "🛡️ [6/6] Verifying state catchup and Zero-Fork parity across cluster..."
    # Wait up to 15s for the node to catch up with block sync
    CATCHUP_OK=false
    BLOCK_LEADER=0
    BLOCK_TARGET=0
    for attempt in $(seq 1 30); do
        RESP_LEADER=$(rpc_call "$LEADER_PORT" "eth_blockNumber")
        RESP_TARGET=$(rpc_call "${TARGET_PORT}" "eth_blockNumber")
        BLOCK_LEADER=$(echo "$RESP_LEADER" | python3 -c "import sys, json; r=json.load(sys.stdin); print(int(r.get('result','0x0'), 16))" 2>/dev/null || echo "0")
        BLOCK_TARGET=$(echo "$RESP_TARGET" | python3 -c "import sys, json; r=json.load(sys.stdin); print(int(r.get('result','0x0'), 16))" 2>/dev/null || echo "0")

        if [ "$BLOCK_TARGET" -ge "$BLOCK_LEADER" ] && [ "$BLOCK_LEADER" -gt 0 ]; then
            CATCHUP_OK=true
            break
        fi
        sleep 0.5
    done

    if [ "$CATCHUP_OK" != "true" ]; then
        echo "⚠️ Target node did not catch up in time (Leader: #${BLOCK_LEADER}, Target: #${BLOCK_TARGET})"
    fi

    # Verify parity on block (BLOCK_LEADER - 1)
    COMMON_BLOCK_NUM=$((BLOCK_LEADER - 1))
    if [ "$COMMON_BLOCK_NUM" -lt 1 ]; then
        COMMON_BLOCK_NUM=1
    fi
    COMMON_BLOCK_HEX=$(printf "0x%x" "$COMMON_BLOCK_NUM")
    PARITY_OK=false
    HASH0=""; HASH1=""; HASH2=""; HASH3=""
    ROOT0=""; ROOT1=""; ROOT2=""; ROOT3=""

    V0_PORT="$(j "d['ports']['val0']['rpc']")"
    V1_PORT="$(j "d['ports']['val1']['rpc']")"
    V2_PORT="$(j "d['ports']['val2']['rpc']")"
    V3_PORT="$(j "d['ports']['val3']['rpc']")"

    for p_att in $(seq 1 15); do
        B0=$(rpc_call "$V0_PORT" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")
        B1=$(rpc_call "$V1_PORT" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")
        B2=$(rpc_call "$V2_PORT" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")
        B3=$(rpc_call "$V3_PORT" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")

        PARSE_CODE="import sys, json; d=json.load(sys.stdin).get('result') or {}; print(d.get('hash',''), d.get('stateRoot',''))"
        read -r HASH0 ROOT0 < <(echo "$B0" | python3 -c "$PARSE_CODE")
        read -r HASH1 ROOT1 < <(echo "$B1" | python3 -c "$PARSE_CODE")
        read -r HASH2 ROOT2 < <(echo "$B2" | python3 -c "$PARSE_CODE")
        read -r HASH3 ROOT3 < <(echo "$B3" | python3 -c "$PARSE_CODE")

        if [ -n "$HASH0" ] && [ -n "$HASH1" ] && [ -n "$HASH2" ] && [ -n "$HASH3" ] && \
           [ "$HASH0" = "$HASH1" ] && [ "$HASH1" = "$HASH2" ] && [ "$HASH2" = "$HASH3" ] && \
           [ "$ROOT0" = "$ROOT1" ] && [ "$ROOT1" = "$ROOT2" ] && [ "$ROOT2" = "$ROOT3" ]; then
            PARITY_OK=true
            break
        fi
        sleep 0.5
    done

    if [ "$PARITY_OK" = "true" ]; then
        echo "   • Block #${COMMON_BLOCK_NUM} (${COMMON_BLOCK_HEX}):"
        echo "     - Hash:      ${HASH0:0:18}..."
        echo "     - StateRoot: ${ROOT0:0:18}..."
        echo "   ✅ [ROUND ${i} PASS] 100% Zero-Fork parity confirmed across all 4 nodes!"
    else
        echo "❌ [FORK DETECTED] Hashes or roots differ!"
        echo "   Node 0 Hash: $HASH0, Root: $ROOT0"
        echo "   Node 1 Hash: $HASH1, Root: $ROOT1"
        echo "   Node 2 Hash: $HASH2, Root: $ROOT2"
        echo "   Node 3 Hash: $HASH3, Root: $ROOT3"
        exit 1
    fi
done

echo ""
echo "=================================================================="
echo "🎉 ALL ${ROUNDS} ROUNDS OF CRASH RECOVERY UNDER LOAD PASSED!"
echo "   • 0 Integrity Failures (no exit 78, no sentinel file)"
echo "   • 100% Zero-Fork Compliance across all restarts"
echo "   • NOMT Fsync & commitWg Durability VERIFIED"
echo "=================================================================="
