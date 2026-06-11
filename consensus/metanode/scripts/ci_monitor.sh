#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  CI Monitor Script with Auto-Fix Capabilities
#  - Detects crashed nodes (DOWN status)
#  - Detects and kills zombie processes holding node RPC ports
#  - Restarts down nodes cleanly
#  - Detects execution/consensus stalls and restarts the cluster
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ORCHESTRATOR="$SCRIPT_DIR/mtn-orchestrator.sh"

TYPE="recovery"
NO_LISTEN=0
CLEAN_LOGS=0

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --type)
      TYPE="$2"
      shift 2
      ;;
    --no-listen)
      NO_LISTEN=1
      shift
      ;;
    --clean-logs)
      CLEAN_LOGS=1
      shift
      ;;
    *)
      shift
      ;;
  esac
done

# Clean logs if requested
if [ "$CLEAN_LOGS" -eq 1 ]; then
    echo "🧹 [MONITOR] Cleaning log files..."
    # Clean up stability test temp directories
    rm -rf "$SCRIPT_DIR"/tmp.* || true
fi

get_node_rpc_port() {
    case $1 in
        0) echo "8757" ;;
        1) echo "10747" ;;
        2) echo "10749" ;;
        3) echo "10750" ;;
        4) echo "10748" ;;
    esac
}

echo "🔍 [MONITOR] Checking cluster health (Type: $TYPE)..."

# 1. Check Node Status
NODES_DOWN=()
for i in {0..4}; do
    # Get status from orchestrator
    STATUS_LINE=$("$ORCHESTRATOR" status | grep -E "^[[:space:]]*$i[[:space:]]+│" || true)
    if [[ "$STATUS_LINE" =~ "DOWN" ]] || [ -z "$STATUS_LINE" ]; then
        NODES_DOWN+=($i)
    fi
done

if [ ${#NODES_DOWN[@]} -gt 0 ]; then
    echo "⚠️ [MONITOR] Detected crashed/stopped nodes: ${NODES_DOWN[*]}"
    for node_id in "${NODES_DOWN[@]}"; do
        port=$(get_node_rpc_port "$node_id")
        echo "🛠️ [MONITOR] Attempting to auto-fix Node $node_id..."
        
        # Check if port is held by any zombie process
        zombie_pid=$(ss -tulpn | grep -E ":$port " | grep -o -E "pid=[0-9]+" | head -1 | cut -d= -f2 || true)
        if [ -n "$zombie_pid" ]; then
            echo "💀 [MONITOR] Found zombie process $zombie_pid on port $port, killing it..."
            kill -9 "$zombie_pid" 2>/dev/null || true
            sleep 1
        fi
        
        # Stop session to clean up tmux/socket references
        "$ORCHESTRATOR" stop-node "$node_id" >/dev/null 2>&1 || true
        sleep 2
        
        # Start node again
        echo "🚀 [MONITOR] Restarting Go Master Node $node_id..."
        "$ORCHESTRATOR" start-node "$node_id"
    done
    exit 0
fi

# 2. Check for Consensus Stalls (Only if RPC of Node 0 is available)
BLOCK_1=$(curl -s --max-time 3 -X POST "http://127.0.0.1:8757" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq -r '.result' 2>/dev/null || echo "")
if [ -n "$BLOCK_1" ] && [ "$BLOCK_1" != "null" ]; then
    BLOCK_NUM_1=$(printf "%d" "$BLOCK_1" 2>/dev/null || echo "0")
    echo "📊 [MONITOR] Current block height: $BLOCK_NUM_1. Checking for progress..."
    sleep 10
    BLOCK_2=$(curl -s --max-time 3 -X POST "http://127.0.0.1:8757" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq -r '.result' 2>/dev/null || echo "")
    BLOCK_NUM_2=$(printf "%d" "$BLOCK_2" 2>/dev/null || echo "0")
    
    if [ "$BLOCK_NUM_1" -eq "$BLOCK_NUM_2" ] && [ "$BLOCK_NUM_1" -gt 1 ]; then
        # Check if transaction pump is alive
        TX_SENDER_PID=$(pgrep -f "tx_sender" || true)
        if [ -n "$TX_SENDER_PID" ]; then
            echo "🚨 [MONITOR] Block height stalled at $BLOCK_NUM_1 despite active transaction pump! Auto-fixing by restarting the entire cluster..."
            "$ORCHESTRATOR" stop
            sleep 5
            "$ORCHESTRATOR" start
        else
            echo "ℹ️ [MONITOR] Block height stable at $BLOCK_NUM_1 (No transaction pump running)."
        fi
    else
        echo "✅ [MONITOR] Cluster health is OK. Blocks are advancing ($BLOCK_NUM_1 -> $BLOCK_NUM_2)."
    fi
else
    echo "ℹ️ [MONITOR] Cluster RPC is not responding. (Either stopped or performing snapshot recovery)."
fi
