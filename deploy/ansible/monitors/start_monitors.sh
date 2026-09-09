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
    if [ -n "$BOT_TOKEN" ]; then export TELEGRAM_BOT_TOKEN="$BOT_TOKEN"; fi
    if [ -n "$CHAT_ID" ]; then export TELEGRAM_CHAT_ID="$CHAT_ID"; fi
fi
export TELEGRAM_BOT_TOKEN
export TELEGRAM_CHAT_ID
RPC_JSON_PATH="/tmp/rpc_nodes.json"

# Chain-stall probe transaction key (2026-09-08, see send_stall_probe_tx() below). Same
# fallback pattern as deploy/systemd/start_relayer_daemon.sh's RELAYER_KEY: env var first, then
# an inventory.yml override, then... see below.
#
# 2026-09-08 FOLLOW-UP: the original last-resort fallback here (dev_accounts.json's "Sender A0")
# turned out to have NO BLS public key registered on a real CI cluster's genesis -- every probe
# attempt failed at submission ("failed to build MetaTx: account ... has no BLS public key
# registered on-chain"), not at confirmation, so this monitor confidently declared a real stall
# ("không phải do rảnh") on a cluster that was actually completely healthy and idle. Confirmed by
# hand: the exact same probe against the exact same node, using metanode-suite's own
# test-chain/config.json private_keys[0] (a key the CI test suite's own transactions already
# prove is funded and BLS-registered) confirmed in 813ms. So: prefer that known-working key when
# the sibling metanode-suite checkout is present (the common case for anyone running the CI
# tooling this alert is meant to complement) before falling back to the old devnet key, which is
# kept only as a last resort for a bare local_devnet with no metanode-suite checkout at all.
PROBE_TX_KEY="${PROBE_TX_KEY:-}"
if [ -z "$PROBE_TX_KEY" ] && [ -n "$INV_PATH" ]; then
    PROBE_TX_KEY=$(grep -E '^\s*probe_tx_key:' "$INV_PATH" | head -n 1 | awk '{print $2}' | tr -d '"'"'")
fi
PROBE_SUITE_CONFIG="${SCRIPT_DIR}/../../../../metanode-suite/test-simple/test-rpc/test-chain/config.json"
if [ -z "$PROBE_TX_KEY" ] && [ -f "$PROBE_SUITE_CONFIG" ]; then
    PROBE_TX_KEY=$(python3 -c "
import json
try:
    keys = json.load(open('$PROBE_SUITE_CONFIG')).get('private_keys', [])
    print(keys[0] if keys else '')
except Exception:
    print('')
" 2>/dev/null)
fi
if [ -z "$PROBE_TX_KEY" ]; then
    PROBE_TX_KEY="0x9f61a687fbeac9e11d5cfce0fe2dcec035cb2b21eb9c584d8cf90696ce2fc370"
fi
export PROBE_TX_KEY
PROBE_TOOL_SRC="${SCRIPT_DIR}/../../../execution/cmd/tool/tps_latency_probe"
PROBE_TOOL_BIN="${SCRIPT_DIR}/stall_probe_tool"

# Code version this monitor script (and, by extension, whatever's currently deployed alongside
# it) is running from -- 2026-09-09, requested explicitly after a night of alerts where it was
# hard to tell from Telegram alone whether an alert was about the code fix already in place or
# an older deploy still running it. Computed once, included in every alert below. Short hash +
# "-dirty" suffix if this checkout has uncommitted changes (matches `git describe`-style
# convention already used by ansible_deploy.sh's own "Commit: <hash>" banner).
CODE_VERSION="N/A"
if command -v git >/dev/null 2>&1 && git -C "$SCRIPT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    CODE_VERSION=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo "N/A")
    if [ "$CODE_VERSION" != "N/A" ] && [ -n "$(git -C "$SCRIPT_DIR" status --porcelain 2>/dev/null)" ]; then
        CODE_VERSION="${CODE_VERSION}-dirty"
    fi
fi

send_tele() {
    if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
        return
    fi
    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d chat_id="${TELEGRAM_CHAT_ID}" \
        -d parse_mode="HTML" \
        --data-urlencode text="$1" >/dev/null 2>&1 || true
}

# Helper: Kiểm tra node có nằm trong danh sách bỏ qua giám sát (do test tắt bật node hoặc bảo trì)
is_node_ignored() {
    local node_key="$1"
    local node_id="$2"
    for ign_file in "/tmp/monitors_ignore_nodes" "/tmp/metanode_ignore_nodes" "${SCRIPT_DIR}/ignore_nodes" "/opt/metanode/monitors/ignore_nodes"; do
        if [ -f "$ign_file" ]; then
            local content
            content=$(cat "$ign_file" 2>/dev/null || echo "")
            if echo "$content" | grep -qwE "(all|${node_id}|${node_key}|m${node_id})"; then
                return 0
            fi
        fi
    done
    return 1
}

# Trước khi báo "chain stall" (mục BƯỚC 3 dưới), thử gửi 1 giao dịch thăm dò (probe tx) tới 1
# node còn sống. Nhiều chain (kể cả chain này) KHÔNG tự tạo block rỗng khi không có giao dịch --
# block đứng yên vì đang RẢNH, không phải vì bị treo thật. 1 tx thăm dò sẽ được đưa vào block
# bình thường nếu consensus vẫn khỏe, và ta tránh được cảnh báo giả (2026-09-08, sau khi gặp
# đúng trường hợp này trên cụm thật: chain rảnh vẫn bị báo NGHIÊM TRỌNG).
#
# Trả mã thoát PHÂN BIỆT rõ 3 tình huống khác hẳn nhau (2026-09-08 follow-up: từng gộp chung
# "gửi thất bại" và "gửi được nhưng không xác nhận" làm một -- khiến 1 lần PROBE_TX_KEY sai/thiếu
# đăng ký BLS bị hiểu nhầm thành "đã thử, không phải do rảnh" dù thực ra tool còn chưa gửi được gì):
#   0 = tx thăm dò được xác nhận vào block -- chain khỏe, chỉ đang rảnh.
#   1 = thiếu công cụ/không dựng được binary/không lấy được chain-id -- KHÔNG kết luận được gì.
#   2 = gửi tx bị RPC từ chối ngay (vd sai khóa, tài khoản chưa đăng ký BLS) -- lỗi cấu hình của
#       chính probe, KHÔNG phải bằng chứng chain bị treo.
#   3 = tx được RPC chấp nhận nhưng hết giờ chờ không thấy receipt -- tín hiệu thật đáng ngờ nhất.
send_stall_probe_tx() {
    local node_url="$1"
    [ -z "$node_url" ] && return 1

    # Build tps_latency_probe đúng 1 lần rồi cache lại binary -- cùng kiểu với cách
    # block_hash_checker được build bên dưới (LOCAL MONITOR INITIALIZATION).
    if [ ! -f "$PROBE_TOOL_BIN" ] || [ "${PROBE_TOOL_SRC}/main.go" -nt "$PROBE_TOOL_BIN" ]; then
        if [ -d "$PROBE_TOOL_SRC" ] && command -v go >/dev/null 2>&1; then
            (cd "$PROBE_TOOL_SRC" && go build -o "$PROBE_TOOL_BIN" .) 2>/dev/null || true
        fi
    fi
    [ -x "$PROBE_TOOL_BIN" ] || return 1

    local chain_id_hex chain_id
    chain_id_hex=$(curl -s -m 5 -X POST "$node_url" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}' 2>/dev/null | jq -r .result 2>/dev/null || echo "")
    [[ "$chain_id_hex" =~ ^0x[0-9a-fA-F]+$ ]] || return 1
    chain_id=$((16#${chain_id_hex#0x}))

    local out
    out=$("$PROBE_TOOL_BIN" -node "$node_url" -chain-id "$chain_id" -n 1 -key "$PROBE_TX_KEY" -max-wait 15s 2>&1)
    LAST_PROBE_OUTPUT="$out"
    if echo "$out" | grep -q "latency="; then
        return 0
    elif echo "$out" | grep -q "send error:"; then
        return 2
    else
        return 3
    fi
}
# Resolve SSH auth for a node: prefers the SSH key (ansible_ssh_private_key_file, tracked in
# rpc_nodes.json's "ssh" section). Falls back to reading ansible_ssh_pass ON DEMAND straight
# from inventory.yml -- for the still-supported devnet-only plaintext-password inventories
# (see inventory.example.yml "Cách 2") -- for use with sshpass. The password is held only in
# the SSH_PASS shell variable for the immediate ssh_remote/scp_remote call below; it is never
# written into rpc_nodes.json/config-m-nodes.json (that was the actual issue #105 fix: those
# files are copied to every cluster node and world-readable-by-default on shared /tmp).
resolve_ssh_auth() {
    local node_key="$1" node_id="$2" rpc_data="$3"
    SSH_USER=$(echo "$rpc_data" | jq -r ".ssh[\"$node_key\"].user // \"abc\"" 2>/dev/null)
    local key
    key=$(echo "$rpc_data" | jq -r ".ssh[\"$node_key\"].key // empty" 2>/dev/null)
    key="${key/#\~/$HOME}"
    SSH_OPTS="-o StrictHostKeyChecking=no"
    SSH_PASS=""
    if [ -n "$key" ] && [ -f "$key" ]; then
        SSH_OPTS="-i $key $SSH_OPTS"
    elif [ -n "$INV_PATH" ] && command -v sshpass >/dev/null 2>&1; then
        SSH_PASS=$(python3 -c "
import yaml
try:
    with open('$INV_PATH') as f:
        d = yaml.safe_load(f) or {}
    mc = d.get('all', {}).get('children', {}).get('metanode_cluster', {})
    hosts = mc.get('hosts', {}) or d.get('all', {}).get('hosts', {}) or {}
    gv = mc.get('vars', {}) or d.get('all', {}).get('vars', {}) or {}
    for h in hosts.values():
        if isinstance(h, dict) and $node_id in (h.get('node_ids') or []):
            print(h.get('ansible_ssh_pass', gv.get('ansible_ssh_pass', '')))
            break
except Exception:
    pass
" 2>/dev/null)
    fi
}

ssh_remote() {
    if [ -n "$SSH_PASS" ]; then
        sshpass -p "$SSH_PASS" ssh $SSH_OPTS "$@"
    else
        ssh $SSH_OPTS "$@"
    fi
}

scp_remote() {
    if [ -n "$SSH_PASS" ]; then
        sshpass -p "$SSH_PASS" scp $SSH_OPTS "$@"
    else
        scp $SSH_OPTS "$@"
    fi
}


# ─── ACTION: IGNORE / UNIGNORE NODES FROM MONITORING ────────────────────────
if [ "${1:-}" == "ignore" ] || [ "${1:-}" == "--ignore" ]; then
    node_to_ignore="${2:-all}"
    mkdir -p /tmp
    echo "$node_to_ignore" >> /tmp/monitors_ignore_nodes
    echo "✅ Đã thêm '$node_to_ignore' vào danh sách bỏ qua giám sát (/tmp/monitors_ignore_nodes)."
    exit 0
fi

if [ "${1:-}" == "unignore" ] || [ "${1:-}" == "--unignore" ]; then
    node_to_unignore="${2:-}"
    if [ -z "$node_to_unignore" ] || [ "$node_to_unignore" == "all" ]; then
        rm -f /tmp/monitors_ignore_nodes 2>/dev/null || true
        echo "✅ Đã xóa toàn bộ danh sách bỏ qua giám sát."
    else
        sed -i "/\b${node_to_unignore}\b/d" /tmp/monitors_ignore_nodes 2>/dev/null || true
        echo "✅ Đã xóa '$node_to_unignore' khỏi danh sách bỏ qua giám sát."
    fi
    exit 0
fi

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
        rm -f "$RPC_JSON_PATH" 2>/dev/null || true
        (umask 077 && python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true)
        chmod 0600 "$RPC_JSON_PATH" 2>/dev/null || true
        if [ -f "$RPC_JSON_PATH" ] && [ -d "$BLOCK_CHECKER_DIR" ]; then
            cp "$RPC_JSON_PATH" "$BLOCK_CHECKER_DIR/config-m-nodes.json"
            chmod 0600 "$BLOCK_CHECKER_DIR/config-m-nodes.json" 2>/dev/null || true
        fi
    fi

    # 3. Chuẩn bị file copy sang các node (luôn luôn sync file mới nhất từ root, không để file cũ bị lệch IP)
    if [ -f "$INV_PATH" ]; then
        if [ "$(readlink -f "$INV_PATH" 2>/dev/null)" != "$(readlink -f "${SCRIPT_DIR}/inventory.yml" 2>/dev/null)" ]; then
            cp -f "$INV_PATH" "${SCRIPT_DIR}/inventory.yml"
        fi
    fi
    if [ -f "$PARSE_PY" ]; then
        if [ "$(readlink -f "$PARSE_PY" 2>/dev/null)" != "$(readlink -f "${SCRIPT_DIR}/parse_inventory.py" 2>/dev/null)" ]; then
            cp -f "$PARSE_PY" "${SCRIPT_DIR}/parse_inventory.py"
        fi
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
    
    # Chain Stall Detector tracking
    last_seen_block=0
    last_block_progress_ts=$(date +%s)
    last_stall_alert_ts=0
    is_chain_stalled=false
    # 2026-09-08: was a hardcoded 120s -- raised default and made overridable
    # (CHAIN_STALL_THRESHOLD_SEC in .env or the environment) after a real false alarm on an idle
    # chain (no pending txs -> no new block -> looked identical to a real stall from block height
    # alone). The bigger fix for the false-positive itself is send_stall_probe_tx() above, called
    # right before alerting below; this threshold mainly controls how often that probe fires.
    STALL_THRESHOLD_SEC="${CHAIN_STALL_THRESHOLD_SEC:-300}"

    # Lấy IP local của máy monitor hiện tại
    MONITOR_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1)
    if [ -z "$MONITOR_IP" ]; then MONITOR_IP=$(hostname -I | awk '{print $1}'); fi

    while true; do
        INV_PATH=$(get_inv_path)
        PARSE_PY=$(get_parse_py)

        if [ -n "$PARSE_PY" ] && [ -n "$INV_PATH" ]; then
            (umask 077 && python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true)
            chmod 0600 "$RPC_JSON_PATH" 2>/dev/null || true
        fi
        
        if [ -f "$RPC_JSON_PATH" ]; then
            RPC_CONFIG_DATA=$(cat "$RPC_JSON_PATH" 2>/dev/null || echo "{}")
            while read -r node_key node_url; do
                node_id=${node_key#m}
                # Kiểm tra nếu node nằm trong danh sách bỏ qua (do test tắt bật node hoặc bảo trì)
                if is_node_ignored "$node_key" "$node_id"; then
                    continue
                fi

                if ! curl -s -m 10 "$node_url" >/dev/null 2>&1 && { sleep 2; ! curl -s -m 10 "$node_url" >/dev/null 2>&1; }; then
                    if is_node_ignored "$node_key" "$node_id"; then
                        continue
                    fi

                    if [ "${dead_nodes[$node_key]:-0}" == "0" ]; then
                        dead_nodes[$node_key]=1
                        ip=$(echo "$node_url" | awk -F/ '{print $3}' | awk -F: '{print $1}')
                        resolve_ssh_auth "$node_key" "$node_id" "$RPC_CONFIG_DATA"
                        ssh_user="$SSH_USER"
                        
                        crash_time=$(date +%Y%m%d_%H%M%S)
                        crash_dir="${SCRIPT_DIR}/logs_crash/node_${node_id}_crash_${crash_time}"
                        
                        # ─── BƯỚC 1: PHÂN BIỆT SERVER DOWN vs REBOOT vs MAINTENANCE vs CRASH ──────────────
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
                            # Thử SSH nhanh 3s kiểm tra máy chủ còn sống không (qua SSH Key)
                            ssh_uptime=$(ssh_remote -o ConnectTimeout=3 "$ssh_user@$ip" "cat /proc/uptime 2>/dev/null | awk '{print int(\$1)}'" 2>/dev/null || echo "FAILED")
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
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
   • <b>Mức độ:</b> Thảm họa (Disaster)
────────────────────────
⚠️ Máy chủ vật lý <code>${ip}</code> đang tắt nguồn, đứt mạng hoặc treo cứng OS.
────────────────────────
👉 <b>HƯỚNG DẪN XỬ LÝ CHO DEV:</b>
1. Kiểm tra nguồn điện & kết nối mạng của máy chủ <code>${ip}</code>.
2. Sau khi máy chủ online trở lại, bật lại riêng node <code>${node_key}</code> (giữ nguyên Data):
<code>./ansible_deploy.sh --start --only-node ${node_id}</code>"

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
                                ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "journalctl -b -1 -e -n 100 --no-pager" > "$reboot_dir/journal_previous_boot.log" 2>/dev/null || true
                                ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "dmesg -T | grep -iE 'oom|panic|killed|segfault|error' | tail -n 50" > "$reboot_dir/dmesg_errors.log" 2>/dev/null || true
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
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
   • <b>Mức độ:</b> Khẩn cấp (Critical)
────────────────────────
⛔ <b>TOÀN BỘ TIẾN TRÌNH TEST / BENCHMARK ĐÃ BỊ DỪNG!</b>
Máy chủ <code>${ip}</code> bị khởi động lại (khả năng do: Kernel Panic, OOM Killer cạn RAM, Quá tải CPU hoặc Sập nguồn).

👉 <b>HƯỚNG DẪN XỬ LÝ CHO DEV:</b>
Khởi động lại riêng node <code>${node_key}</code> (giữ nguyên Data):
<code>./ansible_deploy.sh --start --only-node ${node_id}</code>

🛠 <b>Lệnh kiểm tra nguyên nhân Reboot trực tiếp trên máy ${ip}:</b>
• Xem log lần boot trước:
<code>ssh $ssh_user@$ip \"journalctl -b -1 -e -n 100\"</code>
• Xem log lỗi Kernel / OOM Killer:
<code>ssh $ssh_user@$ip \"dmesg -T | grep -iE 'oom|panic|killed' | tail -n 30\"</code>
• Xem lịch sử reboot:
<code>ssh $ssh_user@$ip \"last reboot | head -n 5\"</code>"

                        else
                            # Kiểm tra xem service có bị dừng chủ động (inactive/deactivating do test hoặc bảo trì) không
                            exec_status="unknown"
                            cons_status="unknown"

                            if [ "$is_local" == "true" ]; then
                                exec_status=$(systemctl is-active "metanode-execution-$node_id" 2>/dev/null || echo "unknown")
                                cons_status=$(systemctl is-active "metanode-consensus-$node_id" 2>/dev/null || echo "unknown")
                            else
                                exec_status=$(ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "systemctl is-active metanode-execution-$node_id 2>/dev/null || echo 'unknown'")
                                cons_status=$(ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "systemctl is-active metanode-consensus-$node_id 2>/dev/null || echo 'unknown'")
                            fi

                            if [ "$exec_status" == "inactive" ] || [ "$exec_status" == "deactivating" ]; then
                                failure_type[$node_key]="MAINTENANCE"
                                dead_nodes[$node_key]=2 # 2 = dừng chủ động (không coi là crash và không alert recovery khi bật lại)
                                echo "ℹ️ Node $node_key ($ip) đang ở trạng thái dừng chủ động ($exec_status). Bỏ qua cảnh báo crash."
                                continue
                            fi

                            # TRƯỜNG HỢP C: NODE CRASH (Server vẫn sống nhưng Service Node bị lỗi/sập)
                            failure_type[$node_key]="NODE_CRASH"
                            mkdir -p "$crash_dir"

                            if [ "$is_local" == "true" ]; then
                                # Kéo nhật ký journalctl mới nhất
                                journalctl -u "metanode-execution-$node_id" -n 500 --no-pager > "$crash_dir/journal_execution.log" 2>/dev/null || true
                                journalctl -u "metanode-consensus-$node_id" -n 500 --no-pager > "$crash_dir/journal_consensus.log" 2>/dev/null || true
                                
                                # Kéo panic dump nếu có
                                cp /opt/metanode/node-$node_id/logs/execution/panic.log "$crash_dir/" 2>/dev/null || true
                                
                                # Kéo thư mục log execution ngày mới nhất (chứa execution.log, App.log, IntermediateRoot.log...)
                                mkdir -p "$crash_dir/execution"
                                latest_exec_date_dir=$(ls -dt /opt/metanode/node-$node_id/logs/execution/20* 2>/dev/null | head -n 1 || true)
                                if [ -n "$latest_exec_date_dir" ]; then
                                    cp -r "$latest_exec_date_dir" "$crash_dir/execution/" 2>/dev/null || true
                                fi
                                cp /opt/metanode/node-$node_id/logs/execution/*.log "$crash_dir/execution/" 2>/dev/null || true
                                
                                # Kéo thư mục log consensus ngày mới nhất
                                mkdir -p "$crash_dir/consensus"
                                latest_cons_date_dir=$(ls -dt /opt/metanode/node-$node_id/logs/consensus/20* 2>/dev/null | head -n 1 || true)
                                if [ -n "$latest_cons_date_dir" ]; then
                                    cp -r "$latest_cons_date_dir" "$crash_dir/consensus/" 2>/dev/null || true
                                fi
                                cp /opt/metanode/node-$node_id/logs/consensus/*.log "$crash_dir/consensus/" 2>/dev/null || true
                            else
                                # Kéo nhật ký journalctl mới nhất
                                ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "journalctl -u metanode-execution-$node_id -n 500 --no-pager" > "$crash_dir/journal_execution.log" 2>/dev/null || true
                                ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "journalctl -u metanode-consensus-$node_id -n 500 --no-pager" > "$crash_dir/journal_consensus.log" 2>/dev/null || true
                                
                                # Kéo panic dump nếu có
                                scp_remote -o ConnectTimeout=5 "$ssh_user@$ip:/opt/metanode/node-$node_id/logs/execution/panic.log" "$crash_dir/" 2>/dev/null || true
                                
                                # Kéo thư mục log execution ngày mới nhất
                                mkdir -p "$crash_dir/execution"
                                latest_exec_date_dir=$(ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "ls -dt /opt/metanode/node-$node_id/logs/execution/20* 2>/dev/null | head -n 1" 2>/dev/null || true)
                                if [ -n "$latest_exec_date_dir" ]; then
                                    scp_remote -r -o ConnectTimeout=5 "$ssh_user@$ip:$latest_exec_date_dir" "$crash_dir/execution/" 2>/dev/null || true
                                fi
                                scp_remote -o ConnectTimeout=5 "$ssh_user@$ip:/opt/metanode/node-$node_id/logs/execution/*.log" "$crash_dir/execution/" 2>/dev/null || true
                                
                                # Kéo thư mục log consensus ngày mới nhất
                                mkdir -p "$crash_dir/consensus"
                                latest_cons_date_dir=$(ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "ls -dt /opt/metanode/node-$node_id/logs/consensus/20* 2>/dev/null | head -n 1" 2>/dev/null || true)
                                if [ -n "$latest_cons_date_dir" ]; then
                                    scp_remote -r -o ConnectTimeout=5 "$ssh_user@$ip:$latest_cons_date_dir" "$crash_dir/consensus/" 2>/dev/null || true
                                fi
                                scp_remote -o ConnectTimeout=5 "$ssh_user@$ip:/opt/metanode/node-$node_id/logs/consensus/*.log" "$crash_dir/consensus/" 2>/dev/null || true
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
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
   • <b>Mức độ:</b> Khẩn cấp (Critical)
────────────────────────
👉 <b>HƯỚNG DẪN XỬ LÝ (RUNBOOK CHO DEV):</b>
• <b>Bước 1:</b> Khởi động lại riêng node <code>${node_key}</code> (Giữ nguyên Data):
  <code>./ansible_deploy.sh --start --only-node ${node_id}</code>
  <i>(hoặc fast restart: <code>./ansible_deploy.sh --restart --only-node ${node_id}</code>)</i>

• <b>Bước 2:</b> Nếu restart vẫn sập (hỏng DB hoặc tụt quá xa > 5 epoch): Khôi phục từ Snapshot:
  <code>./ansible_deploy.sh --reset-all --only-node ${node_id} --restore-node ${node_id} --snapshot-url <URL_SNAPSHOT></code>
  ⚠️ <i>LƯU Ý: Tuyệt đối KHÔNG bỏ cờ <code>--only-node ${node_id}</code>!</i>

────────────────────────
📦 <b>Đã tự động sao lưu gói Logs mới nhất!</b>
🛠 <b>Lệnh kéo Logs về máy trạm để Debug:</b>
<code>scp -r $ssh_user@$MONITOR_IP:$crash_dir ./node_${node_id}_crash_${crash_time}</code>"
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
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
   • <b>Trạng thái:</b> Đã phản hồi RPC bình thường
────────────────────────"
                    elif [ "${dead_nodes[$node_key]:-0}" == "2" ]; then
                        # Node tắt chủ động nay bật lại bình thường, reset cờ êm đềm
                        dead_nodes[$node_key]=0
                    fi
                fi
            done < <(jq -r '.nodes | to_entries[] | "\(.key) \(.value)"' "$RPC_JSON_PATH" 2>/dev/null || true)

            # ─── BƯỚC 3: PHÁT HIỆN CHUỖI ĐỨNG IM (CHAIN STALL DETECTOR) ───────────────
            curr_max_block=0
            probe_target_url=""
            while read -r chk_key node_url; do
                chk_id=${chk_key#m}
                if is_node_ignored "$chk_key" "$chk_id"; then
                    continue
                fi
                hex_b=$(curl -s -m 3 -X POST "$node_url" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null | jq -r .result 2>/dev/null || echo "")
                if [[ "$hex_b" =~ ^0x[0-9a-fA-F]+$ ]]; then
                    dec_b=$((16#${hex_b#0x}))
                    if [ "$dec_b" -gt "$curr_max_block" ]; then curr_max_block=$dec_b; fi
                    # Nhớ lại 1 node còn phản hồi được để dùng làm đích gửi tx thăm dò bên dưới,
                    # nếu cần -- không cần là node cao nhất, chỉ cần còn sống.
                    if [ -z "$probe_target_url" ]; then probe_target_url="$node_url"; fi
                fi
            done < <(jq -r '.nodes | to_entries[] | "\(.key) \(.value)"' "$RPC_JSON_PATH" 2>/dev/null || true)

            now_ts=$(date +%s)
            if [ "$curr_max_block" -gt 0 ]; then
                if [ "$curr_max_block" -gt "$last_seen_block" ]; then
                    if [ "$is_chain_stalled" == "true" ]; then
                        is_chain_stalled=false
                        send_tele "✅ <b>[ĐÃ PHỤC HỒI: MẠNG TIẾP TỤC SINH BLOCK]</b> ✅
────────────────────────
📡 <b>MÁY PHÁT HIỆN & BÁO CÁO (Reporter Server):</b>
   • <b>Hostname:</b> <code>$(hostname)</code>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
🎯 <b>Độ cao Block mới nhất:</b> <code>#${curr_max_block}</code>
📡 <b>Trạng thái:</b> Chuỗi đã thoát khỏi tình trạng treo và tiếp tục tạo block bình thường.
────────────────────────"
                    fi
                    last_seen_block=$curr_max_block
                    last_block_progress_ts=$now_ts
                else
                    stall_duration=$((now_ts - last_block_progress_ts))
                    # Nếu block không tăng sau STALL_THRESHOLD_SEC, cảnh báo lặp lại mỗi 15 phút
                    if [ "$stall_duration" -ge "$STALL_THRESHOLD_SEC" ]; then
                        if [ $((now_ts - last_stall_alert_ts)) -ge 900 ]; then
                            # Trước khi báo: thử 1 tx thăm dò. Nếu chain chỉ đang RẢNH (không có
                            # giao dịch nên không tạo block mới -- không phải bị treo thật), tx
                            # này sẽ được đưa vào block và ta bỏ qua cảnh báo giả (2026-09-08).
                            confirmed_real_stall=true
                            probe_status_line="Chưa thử được (không có node nào để gửi)"
                            echo "🔎 [STALL PROBE] Nghi ngờ chain treo tại block #${last_seen_block} (đứng yên ${stall_duration}s) -- thử gửi 1 tx thăm dò tới ${probe_target_url:-<không có node nào>}..."
                            if [ -n "$probe_target_url" ]; then
                                LAST_PROBE_OUTPUT=""
                                send_stall_probe_tx "$probe_target_url"
                                probe_rc=$?
                                case "$probe_rc" in
                                    0)
                                        sleep 3
                                        probe_hex=$(curl -s -m 3 -X POST "$probe_target_url" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null | jq -r .result 2>/dev/null || echo "")
                                        if [[ "$probe_hex" =~ ^0x[0-9a-fA-F]+$ ]] && [ $((16#${probe_hex#0x})) -gt "$last_seen_block" ]; then
                                            last_seen_block=$((16#${probe_hex#0x}))
                                            last_block_progress_ts=$now_ts
                                            confirmed_real_stall=false
                                            echo "✅ [STALL PROBE] Tx thăm dò đã vào block #${last_seen_block} -- chain chỉ đang rảnh (không có giao dịch), KHÔNG phải bị treo thật. Bỏ qua cảnh báo."
                                        else
                                            probe_status_line="Tx thăm dò báo đã xác nhận nhưng block vẫn chưa nhích -- bất thường, cần xem log."
                                        fi
                                        ;;
                                    2)
                                        # 2026-09-08: gặp thật trên cụm CI -- PROBE_TX_KEY sai/chưa đăng ký BLS
                                        # khiến RPC từ chối NGAY LÚC GỬI, không liên quan gì tới chain có treo
                                        # hay không. Đừng khẳng định "không phải do rảnh" trong tình huống này.
                                        probe_err_snippet=$(echo "$LAST_PROBE_OUTPUT" | grep "send error:" | head -1 | sed 's/^ *//')
                                        probe_status_line="Bị RPC từ chối ngay khi gửi (lỗi cấu hình PROBE_TX_KEY, KHÔNG phải bằng chứng chain treo): ${probe_err_snippet:-không rõ lỗi}"
                                        echo "⚠️ [STALL PROBE] $probe_status_line"
                                        ;;
                                    3)
                                        probe_status_line="Đã gửi được nhưng hết giờ chờ (15s) không thấy receipt -- tín hiệu treo thật."
                                        echo "⚠️ [STALL PROBE] $probe_status_line"
                                        ;;
                                    *)
                                        probe_status_line="Không chạy được (thiếu công cụ hoặc không lấy được chain-id) -- không loại trừ được khả năng rảnh."
                                        echo "⚠️ [STALL PROBE] $probe_status_line"
                                        ;;
                                esac
                            fi

                            if [ "$confirmed_real_stall" == "true" ]; then
                                last_stall_alert_ts=$now_ts
                                is_chain_stalled=true
                                send_tele "🚨 <b>[NGHIÊM TRỌNG: CHUỖI BỊ ĐỨNG IM / CHAIN STALL]</b> 🚨
────────────────────────
📡 <b>MÁY PHÁT HIỆN & BÁO CÁO (Reporter Server):</b>
   • <b>Hostname:</b> <code>$(hostname)</code>
   • <b>IP:</b> <code>${MONITOR_IP}</code>
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
🎯 <b>TÌNH TRẠNG CONSENSUS / EXECUTION BỊ TREO:</b>
   • <b>Node được kiểm tra (tx thăm dò):</b> <code>${probe_target_url:-không có}</code>
   • <b>Block hiện tại:</b> <code>#${last_seen_block}</code>
   • <b>Thời gian không tăng block:</b> <code>${stall_duration}s</code> (ngưỡng: ${STALL_THRESHOLD_SEC}s)
   • <b>Kết quả tx thăm dò:</b> ${probe_status_line}
   • <b>Nguyên nhân khả dĩ:</b> Mất kết nối P2P quá f node, deadlock consensus, hoặc stall round.
────────────────────────
👉 <b>HƯỚNG DẪN XỬ LÝ (RUNBOOK CHO DEV):</b>
Consensus bị kẹt vòng lặp. Chạy Fast Restart toàn cụm trong 2 giây để bầu lại Leader:
<code>./ansible_deploy.sh --restart</code>
🟢 <i>An toàn: Giữ nguyên 100% dữ liệu, không tốn thời gian build lại.</i>"
                            fi
                        fi
                    fi
                fi
            fi
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
            (umask 077 && python3 "$PARSE_PY" "$INV_PATH" json > "$RPC_JSON_PATH" 2>/dev/null || true)
            chmod 0600 "$RPC_JSON_PATH" 2>/dev/null || true
        fi
        
        if [ -f "$RPC_JSON_PATH" ]; then
            RPC_CONFIG_DATA=$(cat "$RPC_JSON_PATH" 2>/dev/null || echo "{}")
            while read -r node_key node_url; do
                node_id=${node_key#m}
                if is_node_ignored "$node_key" "$node_id"; then
                    continue
                fi
                ip=$(echo "$node_url" | awk -F/ '{print $3}' | awk -F: '{print $1}')
                resolve_ssh_auth "$node_key" "$node_id" "$RPC_CONFIG_DATA"
                ssh_user="$SSH_USER"
                
                is_local=false
                if [ "$ip" == "$MONITOR_IP" ] || [ "$ip" == "127.0.0.1" ] || [ "$ip" == "localhost" ]; then
                    is_local=true
                fi

                if [ "$is_local" == "true" ]; then
                    ram_usage=$(free -m 2>/dev/null | awk 'NR==2{printf "%.0f", $3*100/$2 }')
                    cpu_usage=$(top -bn1 2>/dev/null | grep 'Cpu(s)' | awk '{print 100 - $8}' | cut -d. -f1)
                    disk_usage=$(df -h / 2>/dev/null | awk 'NR==2 {print $5}' | sed 's/%//')
                else
                    metrics=$(ssh_remote -o ConnectTimeout=5 "$ssh_user@$ip" "ram=\$(free -m | awk 'NR==2{printf \"%.0f\", \$3*100/\$2 }'); cpu=\$(top -bn1 | grep 'Cpu(s)' | awk '{print 100 - \$8}' | cut -d. -f1); disk=\$(df -h / | awk 'NR==2 {print \$5}' | sed 's/%//'); echo \"\$ram \$cpu \$disk\"" 2>/dev/null || true)
                    ram_usage=$(echo "$metrics" | awk '{print $1}')
                    cpu_usage=$(echo "$metrics" | awk '{print $2}')
                    disk_usage=$(echo "$metrics" | awk '{print $3}')
                fi

                RAM_LIMIT=95
                CPU_LIMIT=97
                DISK_LIMIT=85

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
   • <b>Code version:</b> <code>${CODE_VERSION}</code>
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

    if [ -s "$RPC_JSON_PATH" ]; then
        cp -f "$RPC_JSON_PATH" "$BLOCK_CHECKER_DIR/config-m-nodes.json"
        if [ -d "/opt/metanode/monitors/block_hash_checker" ]; then
            cp -f "$RPC_JSON_PATH" "/opt/metanode/monitors/block_hash_checker/config-m-nodes.json" 2>/dev/null || true
        fi
    fi
    cd "$BLOCK_CHECKER_DIR" || exit 1
    
    if { [ ! -f "block_hash_checker" ] || [ "main.go" -nt "block_hash_checker" ]; } && command -v go >/dev/null 2>&1; then
        go build -o block_hash_checker main.go || true
    fi
    
    if [ -f "block_hash_checker" ]; then
        chmod +x "block_hash_checker"
        nohup ./block_hash_checker --watch --interval 5s --config config-m-nodes.json --daemon > block_checker_daemon.log 2>&1 &
        PID=$!
        sleep 2
        
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
