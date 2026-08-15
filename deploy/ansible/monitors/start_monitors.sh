#!/bin/bash
# start_monitors.sh
# Script to manage background health, resource, and block hash monitors
# Supports running locally or distributed across all cluster nodes.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Helper functions to locate inventory and parser
get_inv_path() {
    if [ -f "${SCRIPT_DIR}/../inventory.yml" ]; then
        echo "${SCRIPT_DIR}/../inventory.yml"
    elif [ -f "${SCRIPT_DIR}/inventory.yml" ]; then
        echo "${SCRIPT_DIR}/inventory.yml"
    fi
}

get_parse_py() {
    if [ -f "${SCRIPT_DIR}/../parse_inventory.py" ]; then
        echo "${SCRIPT_DIR}/../parse_inventory.py"
    elif [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
        echo "${SCRIPT_DIR}/parse_inventory.py"
    fi
}

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

# Tự động lấy token từ inventory.yml nếu có
INV_PATH=$(get_inv_path)
PARSE_PY=$(get_parse_py)

if [ -n "$INV_PATH" ]; then
    BOT_TOKEN=$(grep -E '^\s*telegram_bot_token:' "$INV_PATH" | head -n 1 | awk '{print $2}' | tr -d '"'"'")
    CHAT_ID=$(grep -E '^\s*telegram_chat_id:' "$INV_PATH" | head -n 1 | awk '{print $2}' | tr -d '"'"'")
    if [ -n "$BOT_TOKEN" ]; then TELEGRAM_BOT_TOKEN="$BOT_TOKEN"; fi
    if [ -n "$CHAT_ID" ]; then TELEGRAM_CHAT_ID="$CHAT_ID"; fi
fi
RPC_JSON_PATH="/tmp/rpc_nodes.json"

send_tele() {
    if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
        return
    fi
    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d chat_id="${TELEGRAM_CHAT_ID}" \
        -d parse_mode="HTML" \
        --data-urlencode text="$1" >/dev/null 2>&1 || true
}

# ─── ACTION: STOP LOCAL MONITORS ─────────────────────────────────────────────
if [ "${1:-}" == "stop" ] || [ "${1:-}" == "--stop" ]; then
    echo "🛑 Đang dừng các tiến trình monitor cục bộ..."
    pkill -9 -f "[s]tart_monitors.sh health" || true
    pkill -9 -f "[s]tart_monitors.sh resources" || true
    pkill -9 -f "[b]lock_hash_checker" || true
    pkill -9 -f "go run [m]ain.go.*--no-stop-flag" || true
    echo "✅ Đã dừng toàn bộ monitors cục bộ."
    exit 0
fi

# ─── ACTION: STOP ALL MONITORS ACROSS CLUSTER ────────────────────────────────
if [ "${1:-}" == "stop-all" ] || [ "${1:-}" == "--stop-all" ]; then
    echo "🛑 Đang dừng các tiến trình monitor trên toàn bộ cụm máy..."
    if [ -n "$INV_PATH" ] && command -v ansible >/dev/null 2>&1; then
        ansible metanode_cluster -i "$INV_PATH" -m shell -a "pkill -9 -f '[s]tart_monitors.sh health' || true; pkill -9 -f '[s]tart_monitors.sh resources' || true; pkill -9 -f '[b]lock_hash_checker' || true; pkill -9 -f 'go run [m]ain.go.*--no-stop-flag' || true" >/dev/null 2>&1 || true
    fi
    pkill -9 -f "[s]tart_monitors.sh health" || true
    pkill -9 -f "[s]tart_monitors.sh resources" || true
    pkill -9 -f "[b]lock_hash_checker" || true
    pkill -9 -f "go run [m]ain.go.*--no-stop-flag" || true
    echo "✅ Đã dừng toàn bộ monitors trên tất cả các node."
    exit 0
fi

# ─── ACTION: DISTRIBUTED MULTI-HOST MONITOR LAUNCH ───────────────────────────
if [ "${1:-}" == "--all-hosts" ] || [ "${1:-}" == "--all" ] || [ "${1:-}" == "--multi" ]; then
    echo "🌐 Đang khởi động chế độ Giám Sát Chéo Đa Máy (Mutual Cross-Monitoring)..."
    
    if [ -z "$INV_PATH" ] || ! command -v ansible >/dev/null 2>&1; then
        echo -e "⚠️ Không tìm thấy Ansible hoặc inventory.yml. Chuyển về chế độ giám sát cục bộ."
        exec /bin/bash "${SCRIPT_DIR}/start_monitors.sh"
    fi

    # 1. Compile Block Hash Checker cục bộ nếu cần
    BLOCK_CHECKER_DIR="${SCRIPT_DIR}/block_hash_checker"
    if [ -d "$BLOCK_CHECKER_DIR" ]; then
        echo "🔨 Kiểm tra và biên dịch Block Hash Checker..."
        cd "$BLOCK_CHECKER_DIR"
        if [ ! -f "block_hash_checker" ] || [ "main.go" -nt "block_hash_checker" ]; then
            go build -o block_hash_checker main.go || true
        fi
        if [ -n "$TELEGRAM_BOT_TOKEN" ]; then
            echo "TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN" > .env
            echo "TELEGRAM_CHAT_ID=$TELEGRAM_CHAT_ID" >> .env
        fi
        cd "$SCRIPT_DIR"
    fi

    # 2. Sinh rpc_nodes.json và copy sang block_hash_checker
    if [ -n "$PARSE_PY" ]; then
        python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true
        if [ -f "$RPC_JSON_PATH" ] && [ -d "$BLOCK_CHECKER_DIR" ]; then
            cp "$RPC_JSON_PATH" "$BLOCK_CHECKER_DIR/config-m-nodes.json"
        fi
    fi

    # 3. Chuẩn bị file copy sang các node
    if [ -f "$INV_PATH" ] && [ ! -f "${SCRIPT_DIR}/inventory.yml" ]; then
        cp "$INV_PATH" "${SCRIPT_DIR}/inventory.yml"
    fi
    if [ -f "$PARSE_PY" ] && [ ! -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
        cp "$PARSE_PY" "${SCRIPT_DIR}/parse_inventory.py"
    fi

    echo "📦 Đồng bộ gói Monitor sang tất cả các máy trong cụm..."
    ansible metanode_cluster -i "$INV_PATH" -b -m file -a "path=/opt/metanode/monitors/block_hash_checker state=directory mode=0777 owner=abc group=abc" >/dev/null 2>&1 || true
    ansible metanode_cluster -i "$INV_PATH" -m copy -a "src=${SCRIPT_DIR}/start_monitors.sh dest=/opt/metanode/monitors/start_monitors.sh mode=0755" >/dev/null 2>&1 || true
    if [ -f "${SCRIPT_DIR}/inventory.yml" ]; then
        ansible metanode_cluster -i "$INV_PATH" -m copy -a "src=${SCRIPT_DIR}/inventory.yml dest=/opt/metanode/monitors/inventory.yml mode=0644" >/dev/null 2>&1 || true
    fi
    if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
        ansible metanode_cluster -i "$INV_PATH" -m copy -a "src=${SCRIPT_DIR}/parse_inventory.py dest=/opt/metanode/monitors/parse_inventory.py mode=0755" >/dev/null 2>&1 || true
    fi
    if [ -f "${SCRIPT_DIR}/.env" ]; then
        ansible metanode_cluster -i "$INV_PATH" -m copy -a "src=${SCRIPT_DIR}/.env dest=/opt/metanode/monitors/.env mode=0600" >/dev/null 2>&1 || true
    fi
    if [ -d "$BLOCK_CHECKER_DIR" ]; then
        ansible metanode_cluster -i "$INV_PATH" -m copy -a "src=${BLOCK_CHECKER_DIR}/ dest=/opt/metanode/monitors/block_hash_checker/ mode=preserve" >/dev/null 2>&1 || true
    fi

    echo "🚀 Kích hoạt Monitor trên tất cả các máy trong cụm song song..."
    ansible metanode_cluster -i "$INV_PATH" -m shell -a "nohup /bin/bash /opt/metanode/monitors/start_monitors.sh </dev/null >/dev/null 2>&1 & sleep 1" >/dev/null 2>&1 || true

    echo "🎉 Đã khởi động thành công hệ thống giám sát chéo trên TẤT CẢ các máy!"
    exit 0
fi

# ─── WORKER 1: HEALTH MONITOR (Kiểm tra node sống/chết & Server Health) ──────
if [ "${1:-}" == "health" ]; then
    echo "Starting health monitor loop with Smart Crash/Server-Down Detection..."
    declare -A dead_nodes
    declare -A failure_type
    
    # Lấy IP local của máy monitor hiện tại
    MONITOR_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1)
    if [ -z "$MONITOR_IP" ]; then MONITOR_IP=$(hostname -I | awk '{print $1}'); fi

    while true; do
        INV_PATH=$(get_inv_path)
        PARSE_PY=$(get_parse_py)

        if [ -n "$PARSE_PY" ] && [ -n "$INV_PATH" ]; then
            python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true
            AUTH_JSON=$(python3 "$PARSE_PY" "$INV_PATH" auth 2>/dev/null || echo "{}")
        else
            AUTH_JSON="{}"
        fi
        
        if [ -f "$RPC_JSON_PATH" ]; then
            while read -r node_key node_url; do
                if ! curl -s -m 10 "$node_url" >/dev/null 2>&1 && { sleep 2; ! curl -s -m 10 "$node_url" >/dev/null 2>&1; }; then
                    if [ "${dead_nodes[$node_key]:-0}" == "0" ]; then
                        dead_nodes[$node_key]=1
                        ip=$(echo "$node_url" | awk -F/ '{print $3}' | awk -F: '{print $1}')
                        node_id=${node_key#m}
                        ssh_user=$(echo "$AUTH_JSON" | jq -r ".users[\"$node_key\"] // \"your_user\"" 2>/dev/null)
                        ssh_pass=$(echo "$AUTH_JSON" | jq -r ".passes[\"$node_key\"] // \"your_password\"" 2>/dev/null)
                        
                        crash_time=$(date +%Y%m%d_%H%M%S)
                        crash_dir="${SCRIPT_DIR}/logs_crash/node_${node_id}_crash_${crash_time}"
                        
                        # ─── BƯỚC 1: PHÂN BIỆT SERVER DOWN vs NODE CRASH ──────────────
                        is_local=false
                        if [ "$ip" == "$MONITOR_IP" ] || [ "$ip" == "127.0.0.1" ] || [ "$ip" == "localhost" ]; then
                            is_local=true
                        fi

                        server_alive=false
                        server_rebooted=false
                        uptime_secs=999999

                        if [ "$is_local" == "true" ]; then
                            server_alive=true
                            uptime_secs=$(cat /proc/uptime 2>/dev/null | awk '{print int($1)}' || echo "999999")
                            if [ "$uptime_secs" -lt 120 ]; then server_rebooted=true; fi
                        else
                            # Thử SSH nhanh 3s kiểm tra máy chủ còn sống không
                            ssh_uptime=$(sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=3 "$ssh_user@$ip" "cat /proc/uptime 2>/dev/null | awk '{print int(\$1)}'" 2>/dev/null || echo "FAILED")
                            if [ "$ssh_uptime" != "FAILED" ] && [[ "$ssh_uptime" =~ ^[0-9]+$ ]]; then
                                server_alive=true
                                uptime_secs=$ssh_uptime
                                if [ "$uptime_secs" -lt 120 ]; then server_rebooted=true; fi
                            fi
                        fi

                        # ─── BƯỚC 2: XỬ LÝ THEO TỪNG LOẠI SỰ CỐ ──────────────────────
                        if [ "$server_alive" == "false" ]; then
                            # TRƯỜNG HỢP A: SERVER BỊ TẮT / MẤT NGUỒN / MẤT MẠNG
                            failure_type[$node_key]="SERVER_DOWN"
                            send_tele "🚨 <b>[NGHIÊM TRỌNG: MÁY CHỦ MẤT KẾT NỐI / SẬP NGUỒN]</b> 🚨
────────────────────────
🎯 <b>MÁY CHỦ BỊ SẬP (Target Server):</b>
   • <b>IP:</b> <code>${ip}</code>
   • <b>Node bị ảnh hưởng:</b> <code>${node_key}</code> (${node_url})
   • <b>Tình trạng:</b> Mất kết nối SSH/Ping hoàn toàn

📡 <b>MÁY PHÁT HIỆN & BÁO CÁO (Reporter Server):</b>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Mức độ:</b> Thảm họa (Disaster)
────────────────────────
⚠️ Máy chủ vật lý <code>${ip}</code> đang tắt nguồn, đứt mạng hoặc treo cứng OS."

                        elif [ "$server_rebooted" == "true" ]; then
                            # TRƯỜNG HỢP B: SERVER VỪA BỊ KHỞI ĐỘNG LẠI (REBOOT) — TEST BỊ DỪNG HẾT
                            failure_type[$node_key]="SERVER_REBOOT"
                            reboot_dir="${SCRIPT_DIR}/logs_crash/server_${ip}_reboot_${crash_time}"
                            mkdir -p "$reboot_dir"

                            # Tự động lưu log lần boot trước để phục vụ debug
                            if [ "$is_local" == "true" ]; then
                                journalctl -b -1 -e -n 100 --no-pager > "$reboot_dir/journal_previous_boot.log" 2>/dev/null || true
                                dmesg -T 2>/dev/null | grep -iE 'oom|panic|killed|segfault|error' | tail -n 50 > "$reboot_dir/dmesg_errors.log" 2>/dev/null || true
                            else
                                sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "journalctl -b -1 -e -n 100 --no-pager" > "$reboot_dir/journal_previous_boot.log" 2>/dev/null || true
                                sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "dmesg -T | grep -iE 'oom|panic|killed|segfault|error' | tail -n 50" > "$reboot_dir/dmesg_errors.log" 2>/dev/null || true
                            fi

                            # Xóa bớt backup cũ
                            ls -dt "${SCRIPT_DIR}/logs_crash"/* 2>/dev/null | tail -n +6 | xargs rm -rf 2>/dev/null || true

                            send_tele "🚨 <b>[NGHIÊM TRỌNG: MÁY CHỦ VỪA REBOOT — TEST BỊ DỪNG!]</b> 🚨
────────────────────────
🎯 <b>MÁY CHỦ BỊ REBOOT (Target Server):</b>
   • <b>IP:</b> <code>${ip}</code>
   • <b>Node bị ảnh hưởng:</b> <code>${node_key}</code>
   • <b>Khởi động lại:</b> <code>${uptime_secs}s trước</code>

📡 <b>MÁY PHÁT HIỆN & BÁO CÁO (Reporter Server):</b>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Mức độ:</b> Khẩn cấp (Critical)
────────────────────────
⛔ <b>TOÀN BỘ TIẾN TRÌNH TEST / BENCHMARK ĐÃ BỊ DỪNG!</b>
Máy chủ <code>${ip}</code> bị khởi động lại (khả năng do: Kernel Panic, OOM Killer cạn RAM, Quá tải CPU hoặc Sập nguồn).

🛠 <b>Lệnh kiểm tra nguyên nhân Reboot trực tiếp trên máy ${ip}:</b>
• Xem log lần boot trước:
<code>ssh $ssh_user@$ip \"journalctl -b -1 -e -n 100\"</code>
• Xem log lỗi Kernel / OOM Killer:
<code>ssh $ssh_user@$ip \"dmesg -T | grep -iE 'oom|panic|killed' | tail -n 30\"</code>
• Xem lịch sử reboot:
<code>ssh $ssh_user@$ip \"last reboot | head -n 5\"</code>"

                        else
                            # TRƯỜNG HỢP C: NODE CRASH (Server vẫn sống nhưng Service Node bị lỗi/sập)
                            failure_type[$node_key]="NODE_CRASH"
                            mkdir -p "$crash_dir"
                            
                            exec_status="unknown"
                            cons_status="unknown"

                            if [ "$is_local" == "true" ]; then
                                exec_status=$(systemctl is-active "metanode-execution-$node_id" 2>/dev/null || echo "unknown")
                                cons_status=$(systemctl is-active "metanode-consensus-$node_id" 2>/dev/null || echo "unknown")
                                
                                # Kéo nhật ký journalctl mới nhất
                                journalctl -u "metanode-execution-$node_id" -n 200 --no-pager > "$crash_dir/journal_execution.log" 2>/dev/null || true
                                journalctl -u "metanode-consensus-$node_id" -n 200 --no-pager > "$crash_dir/journal_consensus.log" 2>/dev/null || true
                                
                                # Kéo panic dump nếu có
                                cp /opt/metanode/node-$node_id/logs/execution/panic.log "$crash_dir/" 2>/dev/null || true
                                
                                # Kéo file log execution mới nhất (chỉ lấy 1 file mới nhất trong thư mục ngày)
                                latest_exec=$(ls -t /opt/metanode/node-$node_id/logs/execution/*/*.log /opt/metanode/node-$node_id/logs/execution/*.log 2>/dev/null | head -n 1 || true)
                                if [ -n "$latest_exec" ]; then cp "$latest_exec" "$crash_dir/" 2>/dev/null || true; fi
                                
                                # Kéo file log consensus mới nhất
                                latest_cons=$(ls -t /opt/metanode/node-$node_id/logs/consensus/*/*.log /opt/metanode/node-$node_id/logs/consensus/*.log 2>/dev/null | head -n 1 || true)
                                if [ -n "$latest_cons" ]; then cp "$latest_cons" "$crash_dir/" 2>/dev/null || true; fi
                            else
                                exec_status=$(sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "systemctl is-active metanode-execution-$node_id 2>/dev/null || echo 'unknown'")
                                cons_status=$(sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "systemctl is-active metanode-consensus-$node_id 2>/dev/null || echo 'unknown'")
                                
                                # Kéo nhật ký journalctl mới nhất
                                sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "journalctl -u metanode-execution-$node_id -n 200 --no-pager" > "$crash_dir/journal_execution.log" 2>/dev/null || true
                                sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "journalctl -u metanode-consensus-$node_id -n 200 --no-pager" > "$crash_dir/journal_consensus.log" 2>/dev/null || true
                                
                                # Kéo panic dump nếu có
                                sshpass -p "$ssh_pass" scp -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip:/opt/metanode/node-$node_id/logs/execution/panic.log" "$crash_dir/" 2>/dev/null || true
                                
                                # Kéo file log execution mới nhất
                                latest_exec=$(sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "ls -t /opt/metanode/node-$node_id/logs/execution/*/*.log /opt/metanode/node-$node_id/logs/execution/*.log 2>/dev/null | head -n 1" 2>/dev/null || true)
                                if [ -n "$latest_exec" ]; then
                                    sshpass -p "$ssh_pass" scp -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip:$latest_exec" "$crash_dir/" 2>/dev/null || true
                                fi
                                
                                # Kéo file log consensus mới nhất
                                latest_cons=$(sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "ls -t /opt/metanode/node-$node_id/logs/consensus/*/*.log /opt/metanode/node-$node_id/logs/consensus/*.log 2>/dev/null | head -n 1" 2>/dev/null || true)
                                if [ -n "$latest_cons" ]; then
                                    sshpass -p "$ssh_pass" scp -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip:$latest_cons" "$crash_dir/" 2>/dev/null || true
                                fi
                            fi

                            # Xóa bớt các thư mục backup cũ, chỉ giữ lại 5 bản mới nhất
                            ls -dt "${SCRIPT_DIR}/logs_crash"/* 2>/dev/null | tail -n +6 | xargs rm -rf 2>/dev/null || true

                            send_tele "🚨 <b>[SỰ CỐ: NODE CRASH / SERVICE SẬP]</b> 🚨
────────────────────────
🎯 <b>MÁY CÓ NODE BỊ SẬP (Target Server):</b>
   • <b>IP:</b> <code>${ip}</code>
   • <b>Node bị sập:</b> <code>${node_key}</code>
   • <b>Execution Service:</b> <code>${exec_status}</code>
   • <b>Consensus Service:</b> <code>${cons_status}</code>

📡 <b>MÁY PHÁT HIỆN & BÁO CÁO (Reporter Server):</b>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Mức độ:</b> Khẩn cấp (Critical)
────────────────────────
📦 <b>Đã tự động sao lưu gói Logs mới nhất!</b>
🛠 <b>Lệnh kéo Logs về máy trạm để Debug:</b>
<code>sshpass -p \"$ssh_pass\" scp -r $ssh_user@$MONITOR_IP:$crash_dir ./node_${node_id}_crash_${crash_time}</code>"
                        fi
                    fi
                else
                    if [ "${dead_nodes[$node_key]:-0}" == "1" ]; then
                        dead_nodes[$node_key]=0
                        prev_type=${failure_type[$node_key]:-"NODE_CRASH"}
                        ip=$(echo "$node_url" | awk -F/ '{print $3}' | awk -F: '{print $1}')
                        
                        send_tele "✅ <b>[ĐÃ PHỤC HỒI: NODE HOẠT ĐỘNG TRỞ LẠI]</b> ✅
────────────────────────
🎯 <b>MÁY ĐÃ PHỤC HỒI (Target Server):</b>
   • <b>IP:</b> <code>${ip}</code>
   • <b>Node:</b> <code>${node_key}</code> (${node_url})
   • <b>Sự cố trước đó:</b> <code>${prev_type}</code>

📡 <b>MÁY GHI NHẬN PHỤC HỒI (Reporter Server):</b>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Trạng thái:</b> Đã phản hồi RPC bình thường
────────────────────────"
                    fi
                fi
            done < <(jq -r '.nodes | to_entries[] | "\(.key) \(.value)"' "$RPC_JSON_PATH" 2>/dev/null || true)
        fi
        sleep 10
    done
    exit 0
fi

# ─── WORKER 2: RESOURCE MONITOR (Kiểm tra RAM/CPU/Disk) ──────────────────────
if [ "${1:-}" == "resources" ]; then
    echo "Starting resource monitor loop..."
    declare -A alert_history
    
    MONITOR_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1)
    if [ -z "$MONITOR_IP" ]; then MONITOR_IP=$(hostname -I | awk '{print $1}'); fi

    while true; do
        INV_PATH=$(get_inv_path)
        PARSE_PY=$(get_parse_py)

        if [ -n "$PARSE_PY" ] && [ -n "$INV_PATH" ]; then
            python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true
            AUTH_JSON=$(python3 "$PARSE_PY" "$INV_PATH" auth 2>/dev/null || echo "{}")
        else
            AUTH_JSON="{}"
        fi
        
        if [ -f "$RPC_JSON_PATH" ]; then
            while read -r node_key node_url; do
                ip=$(echo "$node_url" | awk -F/ '{print $3}' | awk -F: '{print $1}')
                ssh_user=$(echo "$AUTH_JSON" | jq -r ".users[\"$node_key\"] // \"your_user\"" 2>/dev/null)
                ssh_pass=$(echo "$AUTH_JSON" | jq -r ".passes[\"$node_key\"] // \"your_password\"" 2>/dev/null)
                
                is_local=false
                if [ "$ip" == "$MONITOR_IP" ] || [ "$ip" == "127.0.0.1" ] || [ "$ip" == "localhost" ]; then
                    is_local=true
                fi

                if [ "$is_local" == "true" ]; then
                    ram_usage=$(free -m 2>/dev/null | awk 'NR==2{printf "%.0f", $3*100/$2 }')
                    cpu_usage=$(top -bn1 2>/dev/null | grep 'Cpu(s)' | awk '{print 100 - $8}' | cut -d. -f1)
                    disk_usage=$(df -h / 2>/dev/null | awk 'NR==2 {print $5}' | sed 's/%//')
                else
                    metrics=$(sshpass -p "$ssh_pass" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$ssh_user@$ip" "ram=\$(free -m | awk 'NR==2{printf \"%.0f\", \$3*100/\$2 }'); cpu=\$(top -bn1 | grep 'Cpu(s)' | awk '{print 100 - \$8}' | cut -d. -f1); disk=\$(df -h / | awk 'NR==2 {print \$5}' | sed 's/%//'); echo \"\$ram \$cpu \$disk\"" 2>/dev/null || true)
                    ram_usage=$(echo "$metrics" | awk '{print $1}')
                    cpu_usage=$(echo "$metrics" | awk '{print $2}')
                    disk_usage=$(echo "$metrics" | awk '{print $3}')
                fi

                RAM_LIMIT=95
                CPU_LIMIT=97
                DISK_LIMIT=95

                if [[ -n "$ram_usage" ]] && [[ -n "$cpu_usage" ]] && [[ -n "$disk_usage" ]]; then
                    if [[ "$ram_usage" -ge "$RAM_LIMIT" ]] || [[ "$cpu_usage" -ge "$CPU_LIMIT" ]] || [[ "$disk_usage" -ge "$DISK_LIMIT" ]]; then
                        current_time=$(date +%s)
                        last_alert=${alert_history[$node_key]:-0}
                        time_diff=$((current_time - last_alert))
                        
                        if [ "$time_diff" -ge 1800 ]; then
                            alert_history[$node_key]=$current_time
                            send_tele "🚨 <b>[CẢNH BÁO: TÀI NGUYÊN QUÁ TẢI]</b> 🚨
────────────────────────
🎯 <b>MÁY BỊ QUÁ TẢI (Target Server):</b>
   • <b>IP:</b> <code>${ip}</code> (Node: <code>${node_key}</code>)
   • <b>RAM:</b> ${ram_usage}% (ngưỡng: ${RAM_LIMIT}%)
   • <b>CPU:</b> ${cpu_usage}% (ngưỡng: ${CPU_LIMIT}%)
   • <b>Ổ đĩa (Disk):</b> ${disk_usage}% (ngưỡng: ${DISK_LIMIT}%)

📡 <b>MÁY PHÁT HIỆN & BÁO CÁO (Reporter Server):</b>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Mức độ:</b> Cảnh báo (Warning)
────────────────────────"
                        fi
                    else
                        alert_history[$node_key]=0
                    fi
                fi
            done < <(jq -r '.nodes | to_entries[] | "\(.key) \(.value)"' "$RPC_JSON_PATH" 2>/dev/null || true)
        fi
        sleep 300 # Check every 5 minutes
    done
    exit 0
fi

# ─── LOCAL MONITOR INITIALIZATION ────────────────────────────────────────────
echo "🔄 Đang khởi động các tiến trình giám sát trên máy này (${MONITOR_IP:-localhost})..."

# 1. Kill old processes
pkill -f "go run main.go.*--no-stop-flag" || true
pkill -f "block_hash_checker.*--daemon" || true
pkill -f "start_monitors.sh health" || true
pkill -f "start_monitors.sh resources" || true

# 2. Start Health Monitor in background
nohup /bin/bash "${SCRIPT_DIR}/start_monitors.sh" health > /dev/null 2>&1 &
echo "✅ Đã bật Health Monitor (kiểm tra node sống/chết)"

# 3. Start Resource Monitor in background
nohup /bin/bash "${SCRIPT_DIR}/start_monitors.sh" resources > /dev/null 2>&1 &
echo "✅ Đã bật Resource Monitor (kiểm tra RAM/CPU quá tải)"

# 4. Start Block Hash Checker in background
BLOCK_CHECKER_DIR="${SCRIPT_DIR}/block_hash_checker"
if [ -d "$BLOCK_CHECKER_DIR" ]; then
    INV_PATH=$(get_inv_path)
    PARSE_PY=$(get_parse_py)
    if [ -n "$PARSE_PY" ] && [ -n "$INV_PATH" ]; then
        python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true
    fi

    if [ -f "$RPC_JSON_PATH" ]; then
        cp "$RPC_JSON_PATH" "$BLOCK_CHECKER_DIR/config-m-nodes.json"
    fi
    cd "$BLOCK_CHECKER_DIR" || exit 1
    
    if [ ! -f "block_hash_checker" ] || [ "main.go" -nt "block_hash_checker" ]; then
        go build -o block_hash_checker main.go || true
    fi
    
    if [ -f "block_hash_checker" ]; then
        nohup ./block_hash_checker --watch --interval 5s --config config-m-nodes.json --daemon > block_checker_daemon.log 2>&1 &
        PID=$!
        sleep 1
        
        if ! kill -0 $PID 2>/dev/null; then
            echo -e "\033[0;31m❌ [ERROR] Block Hash Monitor khởi động thất bại!\033[0m"
            echo -e "\033[0;33mChi tiết lỗi trong block_checker_daemon.log:\033[0m"
            cat block_checker_daemon.log 2>/dev/null || true
        else
            echo "✅ Đã bật Block Hash Monitor (kiểm tra lệch hash)"
        fi
    fi
else
    echo "⚠️ Không tìm thấy thư mục block_hash_checker"
fi

echo "🎉 Hoàn tất khởi động các Monitors ngầm!"
