#!/bin/bash
# start_monitors.sh
# Script to manage background health and block hash monitors

# Configuration
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

# Auto load configuration
load_env_file "${SCRIPT_DIR}/.env"
load_env_file "${SCRIPT_DIR}/../.env"
load_env_file "${SCRIPT_DIR}/../../.env"

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"
RPC_JSON_PATH="/tmp/rpc_nodes.json"

send_tele() {
    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d chat_id="${TELEGRAM_CHAT_ID}" \
        -d parse_mode="HTML" \
        --data-urlencode text="$1" >/dev/null
}

# If arg is "health", run the health loop (Background worker)
if [ "${1:-}" == "health" ]; then
    echo "Starting health monitor loop..."
    declare -A dead_nodes
    while true; do
        # Luôn luôn tạo lại rpc_nodes.json trực tiếp từ inventory.yml để thống nhất cấu hình
        if [ -f "${SCRIPT_DIR}/../parse_inventory.py" ] && [ -f "${SCRIPT_DIR}/../inventory.yml" ]; then
            python3 "${SCRIPT_DIR}/../parse_inventory.py" "${SCRIPT_DIR}/../inventory.yml" json > "$RPC_JSON_PATH" 2>/dev/null || true
        fi
        
        if [ -f "$RPC_JSON_PATH" ]; then
            while read -r node_key node_url; do
                if ! curl -s -m 2 "$node_url" >/dev/null 2>&1; then
                    if [ "${dead_nodes[$node_key]:-0}" == "0" ]; then
                        dead_nodes[$node_key]=1
                        ip=$(echo "$node_url" | awk -F/ '{print $3}' | awk -F: '{print $1}')
                        node_id=${node_key#m}
                        ssh_user="abc"
                        ssh_pass="1234@abcd"
                        
                        crash_time=$(date +%Y%m%d_%H%M%S)
                        crash_dir="${SCRIPT_DIR}/logs_crash/node_${node_id}_crash_${crash_time}"
                        
                        # Tự động kéo log từ node sập về máy monitor (234)
                        mkdir -p "${SCRIPT_DIR}/logs_crash"
                        sshpass -p "$ssh_pass" scp -o StrictHostKeyChecking=no -r "$ssh_user@$ip:/opt/metanode/node-$node_id/logs" "$crash_dir" >/dev/null 2>&1 || true

                        # Xóa các thư mục cũ, chỉ giữ lại 4 thư mục mới nhất
                        ls -dt "${SCRIPT_DIR}/logs_crash"/* 2>/dev/null | tail -n +5 | xargs rm -rf

                        # Lấy IP local của máy monitor
                        MONITOR_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1)
                        if [ -z "$MONITOR_IP" ]; then
                            MONITOR_IP=$(hostname -I | awk '{print $1}')
                        fi

                        send_tele "🚨 <b>[FIRING: Node Crash] Metanode Execution Engine</b> 🚨
<b>Node:</b> <code>$node_key</code>
<b>IP:</b> <code>$ip</code>
<b>URL:</b> $node_url
<b>Severity:</b> Critical

Hệ thống đã tự động backup Crash Logs thành công!
🛠 <b>Lệnh kéo Logs về máy trạm để Debug:</b>
<code>sshpass -p \"1234@abcd\" scp -r abc@$MONITOR_IP:$crash_dir ./node_${node_id}_crash_${crash_time}</code>"
                    fi
                else
                    if [ "${dead_nodes[$node_key]:-0}" == "1" ]; then
                        dead_nodes[$node_key]=0
                        send_tele "✅ <b>[RESOLVED: Node Crash] Metanode Execution Engine</b> ✅
<b>Node:</b> <code>$node_key</code> ($node_url) đã phản hồi RPC trở lại bình thường."
                    fi
                fi
            done < <(jq -r '.nodes | to_entries[] | "\(.key) \(.value)"' "$RPC_JSON_PATH" 2>/dev/null || true)
        fi
        sleep 10
    done
    exit 0
fi

echo "🔄 Đang khởi động lại các tiến trình giám sát (Monitors)..."

# 1. Kill old processes
pkill -f "go run main.go.*--no-stop-flag" || true
pkill -f "block_hash_checker.*--daemon" || true
pkill -f "start_monitors.sh health" || true

# 2. Start Health Monitor in background
nohup /bin/bash "${SCRIPT_DIR}/start_monitors.sh" health > /dev/null 2>&1 &
echo "✅ Đã bật Health Monitor (kiểm tra node sống/chết)"

# 3. Start Block Hash Checker in background
BLOCK_CHECKER_DIR="${SCRIPT_DIR}/block_hash_checker"
if [ -d "$BLOCK_CHECKER_DIR" ]; then
    # Auto-copy dynamic RPC endpoints config to config-m-nodes.json
    if [ -f "$RPC_JSON_PATH" ]; then
        cp "$RPC_JSON_PATH" "$BLOCK_CHECKER_DIR/config-m-nodes.json"
    fi
    cd "$BLOCK_CHECKER_DIR" || exit 1
    
    # Compile the binary
    go build -o block_hash_checker main.go
    
    nohup ./block_hash_checker --watch --interval 5s --config config-m-nodes.json --daemon > block_checker_daemon.log 2>&1 &
    PID=$!
    sleep 1
    
    # Kiểm tra xem tiến trình còn sống sau 1 giây không
    if ! kill -0 $PID 2>/dev/null; then
        echo -e "\033[0;31m❌ [ERROR] Block Hash Monitor khởi động thất bại!\033[0m"
        echo -e "\033[0;33mChi tiết lỗi trong block_checker_daemon.log:\033[0m"
        cat block_checker_daemon.log
        exit 1
    fi
    
    echo "✅ Đã bật Block Hash Monitor (kiểm tra lệch hash)"
else
    echo "⚠️ Không tìm thấy thư mục block_hash_checker"
fi

echo "🎉 Hoàn tất khởi động các Monitors ngầm!"
