#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  ANSIBLE MULTI-SERVER CLUSTER DEPLOYMENT WRAPPER                  ║
# ║                                                                   ║
# ║  Usage: ./ansible_deploy.sh [OPTIONS]                             ║
# ║  Options:                                                         ║
# ║    --start             Start nodes (re-distribute binaries)       ║
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
load_env_file "${SCRIPT_DIR}/../../metanode-suite/scripts/.env"

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"

if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    echo -e "\033[0;31m❌ [ERROR] TELEGRAM_BOT_TOKEN is not set! Telegram notifications will not be sent.\033[0m"
    echo -e "   To enable notifications, please create a \`.env\` file in one of these locations:"
    echo -e "     📍 \033[0;36m${SCRIPT_DIR}/.env\033[0m"
    echo -e "     📍 \033[0;36m$(realpath "${SCRIPT_DIR}/..")/.env\033[0m"
    echo -e "     📍 \033[0;36m$(realpath "${SCRIPT_DIR}/../../metanode-suite/scripts" 2>/dev/null || echo "../../metanode-suite/scripts")/.env\033[0m"
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
            -d "parse_mode=Markdown" > /dev/null 2>&1 || true
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

DEPLOY_SOURCE="${DEPLOY_SOURCE:-"Manual (Local Machine)"}"

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --start) ACTION="start"; KEEP_DATA="true" ;;
        --reset-all) ACTION="setup"; KEEP_DATA="false" ;;
        --stop) ACTION="stop" ;;
        --clean) KEEP_DATA="false" ;;
        --only-node) TARGET_NODE="$2"; shift ;;
        --restore-node) RESTORE_NODE="$2"; shift ;;
        --snapshot-url) SNAPSHOT_URL="$2"; shift ;;
        --open-ports) OPEN_PORTS="true" ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            exit 0
            ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

echo -e "\n🚀 Starting Ansible Deployment with:"
echo "   Source:        $DEPLOY_SOURCE"
echo "   Action:        $ACTION"
echo "   Target Node:   $TARGET_NODE"
echo "   Keep Data:     $KEEP_DATA"
echo "   Restore Node:  $RESTORE_NODE"
echo "   Open Ports:    $OPEN_PORTS"

send_telegram_notification "🚀 *[DEPLOY]* Bắt đầu quá trình Ansible Deploy:
- Source: \`${DEPLOY_SOURCE}\`
- Action: \`${ACTION}\`
- Target Node: \`${TARGET_NODE}\`
- Keep Data: \`${KEEP_DATA}\`
- Restore Node: \`${RESTORE_NODE}\`
- Open Ports: \`${OPEN_PORTS}\`"

# Prepare extra vars
EXTRA_VARS="ansible_action=${ACTION} target_node=${TARGET_NODE} keep_data=${KEEP_DATA} restore_node=${RESTORE_NODE} open_ports=${OPEN_PORTS}"
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
    # Update last deployed commit file
    if git rev-parse HEAD >/dev/null 2>&1; then
        git rev-parse HEAD > "${SCRIPT_DIR}/.last_deployed_commit" || true
    fi
    send_telegram_notification "✅ *[DEPLOY]* Quá trình Ansible Deploy (\`${ACTION}\`) từ \`${DEPLOY_SOURCE}\` hoàn tất thành công!

🔍 *Lệnh lấy log hữu ích:*
• *Tại từng máy node (thay X bằng ID node, ví dụ 0, 1, 2, 3):*
  - *Consensus logs:*
    \`sudo journalctl -u metanode-consensus-X.service -n 100 --no-pager\`
  - *Execution logs:*
    \`tail -n 100 /opt/metanode/node-X/logs/execution/execution.log\`
• *Từ xa tại máy Master (chạy từ thư mục ansible):*
  - *Consensus logs:*
    \`ansible all -i inventory.yml -m shell -a \"sudo journalctl -u 'metanode-consensus-*' -n 100 --no-pager\"\`
  - *Execution logs:*
    \`ansible all -i inventory.yml -m shell -a \"tail -n 100 /opt/metanode/node-*/logs/execution/execution.log\"\`"
else
    send_telegram_notification "❌ *[DEPLOY]* Quá trình Ansible Deploy (\`${ACTION}\`) từ \`${DEPLOY_SOURCE}\` thất bại với mã lỗi \`${ansible_exit}\`!

🔍 *Lệnh lấy log kiểm tra lỗi:*
• *Tại từng máy node (thay X bằng ID node, ví dụ 0, 1, 2, 3):*
  - *Consensus logs:*
    \`sudo journalctl -u \"metanode-consensus-*\" -n 100 --no-pager\`
  - *Execution logs:*
    \`tail -n 100 /opt/metanode/node-X/logs/execution/execution.log\`
• *Từ xa tại máy Master (chạy từ thư mục ansible):*
  - *Consensus logs:*
    \`ansible all -i inventory.yml -m shell -a \"sudo journalctl -u 'metanode-consensus-*' -n 100 --no-pager\"\`
  - *Execution logs:*
    \`ansible all -i inventory.yml -m shell -a \"tail -n 100 /opt/metanode/node-*/logs/execution/execution.log\"\`"
fi

echo -e "\n▶️ Bật lại Health Monitor sau khi Deploy xong..."
MONITOR_SCRIPT="$(realpath "$SCRIPT_DIR/../../metanode-suite/scripts/start_monitors.sh")"
if [ -f "$MONITOR_SCRIPT" ]; then
    bash "$MONITOR_SCRIPT"
fi

exit $ansible_exit
