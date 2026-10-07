#!/usr/bin/env bash
# ==============================================================================
# 💥 NOMT CRASH-RECOVERY & ZERO-FORK INTEGRITY TEST (B1 Benchmark)
# Kills random cluster nodes with `kill -9` under active SECP256k1/EIP-1559 load
# Verifies:
#   1. Zero node exits with code 78 (Startup Data Integrity Check)
#   2. No /tmp/MTN_INTEGRITY_FAILED sentinel file created
#   3. Replicas restart and catch up to Raft cluster state
#   4. 100% Zero-Fork parity across all online nodes (Block Hash + State Root)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_DIR="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BLAST_TOOL="/tmp/secp_tps_blast"
NODES_CONFIG="/tmp/rpc_nodes.exec1.json"
ROUNDS=10

echo "=================================================================="
echo "💥 NOMT CRASH RECOVERY UNDER LOAD — 10 ROUNDS (KILL -9)"
echo "   Testing Durability after bd71000a (fsync) and d0706e6a (commitWg)"
echo "=================================================================="

# Ensure blast tool is compiled
if [ ! -f "$BLAST_TOOL" ]; then
    echo "⚙️ Compiling secp_tps_blast tool..."
    go build -o "$BLAST_TOOL" "${METANODE_DIR}/execution/cmd/tool/secp_tps_blast"
fi

# Clean up any leftover sentinel file
rm -f /tmp/MTN_INTEGRITY_FAILED

REPLICAS=("exec1_r2" "exec1_r3" "exec1_r2" "exec1_r3" "exec1_r2" "exec1_r3" "exec1_r2" "exec1_r3" "exec1_r2" "exec1_r3")
RPC_PORTS=("8648" "8649" "8648" "8649" "8648" "8649" "8648" "8649" "8648" "8649")
LOG_FILES=("exec1_replica2.log" "exec1_replica3.log" "exec1_replica2.log" "exec1_replica3.log" "exec1_replica2.log" "exec1_replica3.log" "exec1_replica2.log" "exec1_replica3.log" "exec1_replica2.log" "exec1_replica3.log")

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
    TARGET_NODE="${REPLICAS[$idx]}"
    TARGET_PORT="${RPC_PORTS[$idx]}"
    TARGET_LOG="${LOG_FILES[$idx]}"

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "🔥 [ROUND ${i}/${ROUNDS}] Target: ${TARGET_NODE} (Port ${TARGET_PORT})"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Step 1: Start sustained background workload (5,000 txs)
    echo "🚀 [1/6] Launching 5,000 SECP transactions workload in background..."
    "$BLAST_TOOL" -nodes-config "$NODES_CONFIG" -count 5000 -batch 1000 -rounds 1 > /tmp/blast_round_${i}.log 2>&1 &
    BLAST_PID=$!

    # Step 2: Wait until transactions are in flight and actively committing to NOMT
    sleep 0.35

    # Step 3: Find PID and issue kill -9 under load
    TARGET_PID=$(pgrep -f "/opt/metanode/bin/simple_chain -config /opt/metanode/${TARGET_NODE}/config.json" | head -n 1 || true)
    if [ -n "$TARGET_PID" ]; then
        echo "💥 [2/6] Killing ${TARGET_NODE} (PID ${TARGET_PID}) with KILL -9 during active trie commit..."
        kill -9 "$TARGET_PID" 2>/dev/null || true
    else
        echo "⚠️ Target ${TARGET_NODE} process not found, skipping kill..."
    fi

    # Wait for blast process to complete its round
    wait "$BLAST_PID" || true
    echo "✅ [3/6] Workload round finished."

    # Step 4: Restart the killed node
    echo "🔄 [4/6] Restarting ${TARGET_NODE}..."
    (cd "/opt/metanode/${TARGET_NODE}" && nohup env PARENT_CHAIN_URL='http://127.0.0.1:8547' /opt/metanode/bin/simple_chain -config "/opt/metanode/${TARGET_NODE}/config.json" >> "/var/log/metanode/${TARGET_LOG}" 2>&1 &)

    # Step 5: Wait for restart and verify startup integrity
    sleep 3

    # Check for sentinel failure
    if [ -f /tmp/MTN_INTEGRITY_FAILED ]; then
        echo "❌ [CRITICAL INTEGRITY FAILURE] /tmp/MTN_INTEGRITY_FAILED found!"
        cat /tmp/MTN_INTEGRITY_FAILED
        exit 78
    fi

    # Verify process is running
    NEW_PID=$(pgrep -f "/opt/metanode/bin/simple_chain -config /opt/metanode/${TARGET_NODE}/config.json" | head -n 1 || true)
    if [ -z "$NEW_PID" ]; then
        echo "❌ [ERROR] ${TARGET_NODE} failed to start or exited immediately!"
        tail -n 30 "/var/log/metanode/${TARGET_LOG}"
        exit 1
    fi
    echo "✅ [5/6] ${TARGET_NODE} restarted successfully (PID: ${NEW_PID}, no exit 78)."

    # Step 6: Verify Catch-up and Zero-Fork Parity
    echo "🛡️ [6/6] Verifying state catchup and Zero-Fork parity across cluster..."
    # Wait up to 10s for the node to catch up with block sync
    CATCHUP_OK=false
    BLOCK_LEADER=0
    BLOCK_TARGET=0
    for attempt in $(seq 1 20); do
        RESP_LEADER=$(rpc_call "8646" "eth_blockNumber")
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
    COMMON_BLOCK_HEX=$(printf "0x%x" $((BLOCK_LEADER - 1)))
    PARITY_OK=false
    HASH1=""
    HASH2=""
    HASH3=""
    ROOT1=""
    ROOT2=""
    ROOT3=""

    for p_att in $(seq 1 10); do
        B1=$(rpc_call "8646" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")
        B2=$(rpc_call "8648" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")
        B3=$(rpc_call "8649" "eth_getBlockByNumber" "[\"${COMMON_BLOCK_HEX}\", false]")

        PARSE_CODE="import sys, json; d=json.load(sys.stdin).get('result') or {}; print(d.get('hash',''), d.get('stateRoot',''))"
        read -r HASH1 ROOT1 < <(echo "$B1" | python3 -c "$PARSE_CODE")
        read -r HASH2 ROOT2 < <(echo "$B2" | python3 -c "$PARSE_CODE")
        read -r HASH3 ROOT3 < <(echo "$B3" | python3 -c "$PARSE_CODE")

        if [ -n "$HASH1" ] && [ -n "$HASH2" ] && [ -n "$HASH3" ] && [ "$HASH1" = "$HASH2" ] && [ "$HASH2" = "$HASH3" ] && [ "$ROOT1" = "$ROOT2" ] && [ "$ROOT2" = "$ROOT3" ]; then
            PARITY_OK=true
            break
        fi
        sleep 0.5
    done

    if [ "$PARITY_OK" = "true" ]; then
        echo "   • Block ${COMMON_BLOCK_HEX}:"
        echo "     - Hash:      ${HASH1:0:18}..."
        echo "     - StateRoot: ${ROOT1:0:18}..."
        echo "   ✅ [ROUND ${i} PASS] 100% Zero-Fork parity confirmed across all 3 nodes!"
    else
        echo "❌ [FORK DETECTED] Hashes or roots differ!"
        echo "   Node 1 Hash: $HASH1, Root: $ROOT1"
        echo "   Node 2 Hash: $HASH2, Root: $ROOT2"
        echo "   Node 3 Hash: $HASH3, Root: $ROOT3"
        exit 1
    fi
done

echo ""
echo "=================================================================="
echo "🎉 ALL 10 ROUNDS OF CRASH RECOVERY UNDER LOAD PASSED!"
echo "   • 0 Integrity Failures (no exit 78, no sentinel file)"
echo "   • 100% Zero-Fork Compliance across all restarts"
echo "   • NOMT Fsync & commitWg Durability VERIFIED"
echo "=================================================================="
