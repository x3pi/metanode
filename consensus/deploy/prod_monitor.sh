#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  PROD MONITOR — Health check + Telegram/Log alerts               ║
# ║                                                                   ║
# ║  Chạy định kỳ qua cron để giám sát trạng thái nodes.             ║
# ║                                                                   ║
# ║  Setup:                                                           ║
# ║    # Thêm vào crontab (crontab -e):                              ║
# ║    * * * * * /path/to/prod_monitor.sh >> /tmp/metanode_monitor.log 2>&1 ║
# ║                                                                   ║
# ║  Config:                                                          ║
# ║    Chỉnh TELEGRAM_BOT_TOKEN và TELEGRAM_CHAT_ID bên dưới,        ║
# ║    hoặc set trong prod_deploy.env (được source tự động).          ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/prod_deploy.env}"

# Load config if exists (for TELEGRAM credentials, NODE_SERVER, etc.)
if [ -f "$ENV_FILE" ]; then
    # shellcheck source=prod_deploy.env.template
    source "$ENV_FILE"
fi

# ─── Config (override in prod_deploy.env or here directly) ───────────
TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-}"

# Monitoring check interval (used for block-stuck detection)
BLOCK_STUCK_THRESHOLD=120   # Alert nếu block không tăng sau N giây
STALE_BLOCK_FILE_DIR="/tmp/metanode_monitor"

# Nodes trên server CỤC BỘ này (chỉ monitor local ports)
# Tự detect dựa trên NODE_SERVER mapping trong prod_deploy.env
# Nếu không có prod_deploy.env, monitor tất cả 5 nodes (localhost)
declare -A NODE_RPC_PORT
NODE_RPC_PORT[0]="${NODE_RPC_PORT[0]:-8757}"
NODE_RPC_PORT[1]="${NODE_RPC_PORT[1]:-10747}"
NODE_RPC_PORT[2]="${NODE_RPC_PORT[2]:-10749}"
NODE_RPC_PORT[3]="${NODE_RPC_PORT[3]:-10750}"
NODE_RPC_PORT[4]="${NODE_RPC_PORT[4]:-10748}"

# Determine which nodes to monitor on this server
SERVER_A="${SERVER_A:-127.0.0.1}"
THIS_IP="$(hostname -I | awk '{print $1}')"

get_local_nodes() {
    # If no NODE_SERVER mapping, monitor all nodes that have a running process
    local nodes=""
    for node in 0 1 2 3 4; do
        local srv="${NODE_SERVER[$node]:-127.0.0.1}"
        if [ "$srv" = "$THIS_IP" ] || [ "$srv" = "127.0.0.1" ] || [ "$srv" = "localhost" ] || [ "$srv" = "$SERVER_A" ]; then
            nodes="$nodes $node"
        fi
    done
    # Fallback: if no match, check which ports are listening
    if [ -z "$nodes" ]; then
        for node in 0 1 2 3 4; do
            local port="${NODE_RPC_PORT[$node]}"
            if ss -tlnp 2>/dev/null | grep -q ":${port} "; then
                nodes="$nodes $node"
            fi
        done
    fi
    echo "$nodes"
}

mkdir -p "$STALE_BLOCK_FILE_DIR"

# ─── Alert functions ──────────────────────────────────────────────────
send_telegram() {
    local msg="$1"
    if [ -n "$TELEGRAM_BOT_TOKEN" ] && [ -n "$TELEGRAM_CHAT_ID" ]; then
        curl -s "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            --data-urlencode "text=${msg}" \
            > /dev/null 2>&1 || true
    fi
}

alert() {
    local msg="$1"
    local ts
    ts="$(date '+%Y-%m-%d %H:%M:%S')"
    echo "[$ts] 🚨 ALERT: $msg"
    send_telegram "🚨 [$(hostname -s)] $msg"
}

# ─── Get block height from local RPC ─────────────────────────────────
get_block_height() {
    local port="$1"
    curl -sf --max-time 3 \
        -X POST "http://localhost:$port" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
        | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" \
        2>/dev/null || echo ""
}

# ─── Main monitoring loop ─────────────────────────────────────────────
TIMESTAMP="$(date '+%Y-%m-%d %H:%M:%S')"
HOST="$(hostname -s)"
LOCAL_NODES="$(get_local_nodes)"

if [ -z "${LOCAL_NODES// /}" ]; then
    echo "[$TIMESTAMP] No local nodes detected on this server ($THIS_IP). Checking all..."
    LOCAL_NODES="0 1 2 3 4"
fi

echo "[$TIMESTAMP] Monitoring nodes:$LOCAL_NODES on $HOST ($THIS_IP)"

for node in $LOCAL_NODES; do
    PORT="${NODE_RPC_PORT[$node]}"
    HEIGHT="$(get_block_height "$PORT")"

    if [ -z "$HEIGHT" ]; then
        # Node is DOWN
        alert "Node ${node} (port ${PORT}) DOWN on ${HOST}!"
        echo "[$TIMESTAMP] ❌ Node $node: DOWN"
        continue
    fi

    echo "[$TIMESTAMP] ✅ Node $node: block #$HEIGHT"

    # Check for stuck blocks (block not increasing)
    STALE_FILE="$STALE_BLOCK_FILE_DIR/node${node}_last_block"
    if [ -f "$STALE_FILE" ]; then
        LAST_HEIGHT="$(cat "$STALE_FILE" | awk '{print $1}')"
        LAST_TIME="$(cat "$STALE_FILE" | awk '{print $2}')"
        NOW="$(date +%s)"
        ELAPSED=$(( NOW - LAST_TIME ))

        if [ "$HEIGHT" = "$LAST_HEIGHT" ] && [ "$ELAPSED" -gt "$BLOCK_STUCK_THRESHOLD" ]; then
            alert "Node ${node} (port ${PORT}) STUCK at block #${HEIGHT} for ${ELAPSED}s on ${HOST}!"
        elif [ "$HEIGHT" != "$LAST_HEIGHT" ]; then
            # Block advanced — update stale file
            echo "$HEIGHT $(date +%s)" > "$STALE_FILE"
        fi
    else
        # First run — initialize stale file
        echo "$HEIGHT $(date +%s)" > "$STALE_FILE"
    fi
done
