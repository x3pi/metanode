#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  ANSIBLE MULTI-SERVER CLUSTER DEPLOYMENT WRAPPER                  ║
# ║                                                                   ║
# ║  Usage: ./ansible_deploy.sh [OPTIONS]                             ║
# ║  Options:                                                         ║
# ║    --start             Start nodes (re-distribute binaries)       ║
# ║    --restart           Fast restart systemd services              ║
# ║    --setup             Fresh setup (gen keys, clears data)        ║
# ║    --stop              Stop nodes                                 ║
# ║    --clean             Clear data before starting nodes           ║
# ║    --only-node N       Only apply actions to node N               ║
# ║    --restore-node N    Restore node N from snapshot url           ║
# ║    --snapshot-url U    Snapshot URL to use (e.g. http://ip:8604)  ║
# ║    --open-ports        Open firewall ports for the nodes          ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Load environment variables from .env if exists
load_env_file() {
    local env_file="$1"
    if [ -f "$env_file" ]; then
        while IFS= read -r line || [ -n "$line" ]; do
            if [[ "$line" =~ ^[[:space:]]*# ]] || [[ -z "$line" ]]; then
                continue
            fi
            if [[ "$line" =~ = ]]; then
                local key=$(echo "${line%%=*}" | xargs)
                local val=$(echo "${line#*=}" | xargs)
                val="${val%\"}"
                val="${val#\"}"
                val="${val%\'}"
                val="${val#\'}"
                export "$key"="$val"
            fi
        done < "$env_file"
    fi
}

# Auto load configuration from different possible locations
load_env_file "${SCRIPT_DIR}/.env"
load_env_file "${SCRIPT_DIR}/../.env"

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"

if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    echo -e "\033[0;31m❌ [ERROR] TELEGRAM_BOT_TOKEN is not set! Telegram notifications will not be sent.\033[0m"
    echo -e "   To enable notifications, please create a \`.env\` file in one of these locations:"
    echo -e "     📍 \033[0;36m${SCRIPT_DIR}/.env\033[0m"
    echo -e "     📍 \033[0;36m$(realpath "${SCRIPT_DIR}/..")/.env\033[0m"
    echo -e "   With the following structure:"
    echo -e "     \033[0;33mTELEGRAM_BOT_TOKEN=your_bot_token_here\033[0m"
    echo -e "     \033[0;33mTELEGRAM_CHAT_ID=your_chat_id_here\033[0m"
    echo -e "   Or export them directly to your environment.\n"
fi

send_telegram_notification() {
    local message="$1"
    if [ -n "$TELEGRAM_BOT_TOKEN" ]; then
        curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            -d "text=${message}" \
            -d "parse_mode=HTML" > /dev/null 2>&1 || true
    fi
}

INVENTORY="${SCRIPT_DIR}/inventory.yml"
PLAYBOOK="${SCRIPT_DIR}/deploy.yml"

# Defaults
ACTION="start"
KEEP_DATA="true"
TARGET_NODE="all"
RESTORE_NODE="none"
SNAPSHOT_URL=""
OPEN_PORTS="false"
BUILD_FAST="false"
DEBUG_CPP="false"

DEPLOY_SOURCE="${DEPLOY_SOURCE:-"Manual (Local Machine)"}"
if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
    GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
    if [[ "$GIT_BRANCH" != "unknown" ]] || [[ "$GIT_COMMIT" != "unknown" ]]; then
        if [[ ! "$DEPLOY_SOURCE" =~ "Branch:" ]] && [[ ! "$DEPLOY_SOURCE" =~ "$GIT_BRANCH" ]]; then
            DEPLOY_SOURCE="$DEPLOY_SOURCE (Branch: $GIT_BRANCH | Commit: $GIT_COMMIT)"
        fi
    fi
fi

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --start) ACTION="start"; KEEP_DATA="true" ;;
        --restart) ACTION="restart"; KEEP_DATA="true" ;;
        --reset-all) ACTION="setup"; KEEP_DATA="false" ;;
        --stop) ACTION="stop" ;;
        --clean) KEEP_DATA="false" ;;
        --only-node) TARGET_NODE="$2"; shift ;;
        --restore-node) RESTORE_NODE="$2"; shift ;;
        --snapshot-url) SNAPSHOT_URL="$2"; shift ;;
        --open-ports) OPEN_PORTS="true" ;;
        --fast) BUILD_FAST="true" ;;
        --debug-cpp) DEBUG_CPP="true" ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            exit 0
            ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

# Detect Deployer Server IP dynamically
DEPLOY_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1)
if [ -z "$DEPLOY_IP" ]; then
    DEPLOY_IP=$(hostname -I | awk '{print $1}')
fi

# Check if Git Auto-Deploy Watcher daemon is running
if pgrep -f "auto_rebuild_deploy.sh" >/dev/null 2>&1; then
    WATCHER_STATUS="Đang hoạt động (Active) 🟢"
else
    WATCHER_STATUS="Đã tắt (Inactive) 🔴"
fi

# Resolve Target Node IPs dynamically from inventory.yml
TARGET_NODES_IPS=""
if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    TARGET_NODES_IPS=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "$TARGET_NODE" || echo "")
    python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" json > "/tmp/rpc_nodes.json" 2>/dev/null || true
fi

ACTION_LABEL=$(echo "$ACTION" | tr '[:lower:]' '[:upper:]')

echo -e "\n🚀 Starting Ansible ${ACTION_LABEL} with:"
echo "   Deployer Server IP: $DEPLOY_IP"
echo "   Target Node IPs:    $TARGET_NODES_IPS"
echo "   Source:             $DEPLOY_SOURCE"
echo "   Action:             $ACTION"
echo "   Target Node:        $TARGET_NODE"
echo "   Keep Data:          $KEEP_DATA"
echo "   Restore Node:       $RESTORE_NODE"
echo "   Open Ports:         $OPEN_PORTS"
echo "   Build Fast:         $BUILD_FAST"
echo "   Watcher:            $WATCHER_STATUS"

ROLES_OUTPUT=""
if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    ROLES_OUTPUT=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "roles" || true)
    echo -e "\n📋 Node Roles:"
    echo "$ROLES_OUTPUT"
fi

send_telegram_notification "🚀 <b>[${ACTION_LABEL}]</b> Bắt đầu quá trình Ansible ${ACTION_LABEL}:
- Deployer Server IP: <code>${DEPLOY_IP}</code>
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Source: <code>${DEPLOY_SOURCE}</code>
- Action: <code>${ACTION}</code>
- Target Node: <code>${TARGET_NODE}</code>
- Keep Data: <code>${KEEP_DATA}</code>
- Restore Node: <code>${RESTORE_NODE}</code>
- Open Ports: <code>${OPEN_PORTS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>"

# Prepare extra vars
EXTRA_VARS="ansible_action=${ACTION} target_node=${TARGET_NODE} keep_data=${KEEP_DATA} restore_node=${RESTORE_NODE} open_ports=${OPEN_PORTS} ansible_build_fast=${BUILD_FAST} ansible_debug_cpp=${DEBUG_CPP}"
if [ -n "$SNAPSHOT_URL" ]; then
    EXTRA_VARS="${EXTRA_VARS} snapshot_url='${SNAPSHOT_URL}'"
fi

echo -e "\n⏸ Tạm dừng Health Monitor trong quá trình Deploy để tránh cảnh báo sai..."
pkill -f "start_monitors.sh health" || true
pkill -f "block_hash_checker" || true

cd "$SCRIPT_DIR"
set +e
ansible-playbook -i "$INVENTORY" "$PLAYBOOK" -e "$EXTRA_VARS"
ansible_exit=$?
set -e

if [ $ansible_exit -eq 0 ]; then
    # Update last deployed commit file if it's a git repo
    if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        git rev-parse HEAD > "${SCRIPT_DIR}/.last_deployed_commit" 2>/dev/null || true
    fi

    # Read and pretty-print /tmp/rpc_nodes.json
    RPC_CONFIG=""
    if [ -f "/tmp/rpc_nodes.json" ]; then
        RPC_CONFIG=$(jq . /tmp/rpc_nodes.json 2>/dev/null || cat /tmp/rpc_nodes.json)
    fi

    echo -e "\n⚙️ Cấu hình kết nối client:"
    echo "$RPC_CONFIG"

    echo -e  "\n📋 *Node Roles:*"
    echo "${ROLES_OUTPUT}"
    send_telegram_notification "✅ <b>[${ACTION_LABEL}]</b> Quá trình Ansible ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> hoàn tất thành công!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>

⚙️ <b>Cấu hình kết nối client:</b>
<pre>
${RPC_CONFIG}
</pre>

🔍 <b>Lệnh lấy log hữu ích:</b>
• <b>Tại từng máy node (thay X bằng ID node, ví dụ 0, 1, 2, 3):</b>
  - <b>Consensus logs:</b>
    <code>sudo journalctl -u metanode-consensus-X.service -n 100 --no-pager</code>
  - <b>Execution logs:</b>
    <code>tail -n 100 /opt/metanode/node-X/logs/execution/execution.log</code>
• <b>Từ xa tại máy Master (chạy từ thư mục ansible):</b>
  - <b>Consensus logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"sudo journalctl -u 'metanode-consensus-*' -n 100 --no-pager\"</code>
  - <b>Execution logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"tail -n 100 /opt/metanode/node-*/logs/execution/execution.log\"</code>"
else
    send_telegram_notification "❌ <b>[${ACTION_LABEL}]</b> Quá trình Ansible ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> thất bại với mã lỗi <code>${ansible_exit}</code>!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

🔍 <b>Lệnh lấy log kiểm tra lỗi:</b>
• <b>Tại từng máy node (thay X bằng ID node, ví dụ 0, 1, 2, 3):</b>
  - <b>Consensus logs:</b>
    <code>sudo journalctl -u \"metanode-consensus-*\" -n 100 --no-pager</code>
  - <b>Execution logs:</b>
    <code>tail -n 100 /opt/metanode/node-X/logs/execution/execution.log</code>
• <b>Từ xa tại máy Master (chạy từ thư mục ansible):</b>
  - <b>Consensus logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"sudo journalctl -u 'metanode-consensus-*' -n 100 --no-pager\"</code>
  - <b>Execution logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"tail -n 100 /opt/metanode/node-*/logs/execution/execution.log\"</code>"
fi

MONITOR_SCRIPT="${SCRIPT_DIR}/monitors/start_monitors.sh"
if [ -f "$MONITOR_SCRIPT" ] && [ "$ACTION" != "stop" ]; then
    echo -e "\n▶️ Bật lại Health Monitor sau khi Deploy xong..."
    bash "$MONITOR_SCRIPT"
elif [ "$ACTION" == "stop" ]; then
    echo -e "\n⏸ Không bật lại Health Monitor vì hệ thống đang ở trạng thái STOP..."
fi

exit $ansible_exit
