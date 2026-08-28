#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║  METANODE UNIFIED ANSIBLE DEPLOYMENT MANAGER                              ║
# ║  Quản lý hợp nhất Public Chain (Root Anchor) và Private Chains            ║
# ║                                                                           ║
# ║  Cách dùng: ./ansible_deploy.sh [OPTIONS]                                 ║
# ║                                                                           ║
# ║  Chế độ Public Chain (Root Anchor - Mặc định):                            ║
# ║    ./ansible_deploy.sh --start           Bắt đầu chạy cụm Public Nodes    ║
# ║    ./ansible_deploy.sh --restart         Khởi động lại nhanh systemd      ║
# ║    ./ansible_deploy.sh --reset-all       Khởi tạo lại từ đầu (xóa data)   ║
# ║    ./ansible_deploy.sh --stop            Dừng các nodes Public            ║
# ║    ./ansible_deploy.sh --only-node N     Chỉ thao tác trên node N         ║
# ║                                                                           ║
# ║  Chế độ Private Chains (--private hoặc --chain=ID):                       ║
# ║    ./ansible_deploy.sh --private --reset-all     Reset & tạo mới all chains ║
# ║    ./ansible_deploy.sh --private --start         Khởi chạy all private chains║
# ║    ./ansible_deploy.sh --private --stop          Dừng all private chains  ║
# ║    ./ansible_deploy.sh --chain=101 --start       Chỉ thao tác với Chain 101║
# ║    ./ansible_deploy.sh --chain=102 --reset-all   Chỉ reset Chain 102      ║
# ║    ./ansible_deploy.sh --private --clean-data    Xóa data DB giữ nguyên key║
# ║                                                                           ║
# ║  Công cụ Cross-Chain & Gateway:                                            ║
# ║    ./ansible_deploy.sh --register                Đăng ký Private Chains   ║
# ║    ./ansible_deploy.sh --register --chain=101    Đăng ký riêng Chain 101  ║
# ║    ./ansible_deploy.sh --relayer [start|stop|status|logs|restart]         ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

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

load_env_file "${SCRIPT_DIR}/.env"
load_env_file "${SCRIPT_DIR}/../.env"

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"

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

# Defaults
DEPLOY_MODE="public"    # "public" hoặc "private"
ACTION="start"
KEEP_DATA="true"
TARGET_NODE="all"
TARGET_CHAIN="all"
RESTORE_NODE="none"
SNAPSHOT_URL=""
OPEN_PORTS="false"
BUILD_FAST="false"
DEBUG_CPP="false"
ALL_MONITORS="false"
REGISTER=0
NO_REGISTER=0
FUND_GENESIS=1
RELAYER_ACTION=""

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --private|--private-chain|--private-chains)
            DEPLOY_MODE="private"
            ;;
        --public|--root-anchor)
            DEPLOY_MODE="public"
            ;;
        --chain=*|-c=*)
            DEPLOY_MODE="private"
            TARGET_CHAIN="${1#*=}"
            ;;
        --chain|-c)
            DEPLOY_MODE="private"
            TARGET_CHAIN="$2"
            shift
            ;;
        --start)
            ACTION="start"; KEEP_DATA="true"
            ;;
        --restart)
            ACTION="restart"; KEEP_DATA="true"
            ;;
        --setup)
            ACTION="setup"; KEEP_DATA="false"
            ;;
        --reset-all|--reset)
            ACTION="reset"; KEEP_DATA="false"
            ;;
        --clean-data)
            ACTION="clean_data"; KEEP_DATA="false"
            ;;
        --stop)
            ACTION="stop"
            ;;
        --status)
            ACTION="status"
            ;;
        --clean)
            KEEP_DATA="false"
            ;;
        --only-node)
            TARGET_NODE="$2"
            shift
            ;;
        --restore-node)
            RESTORE_NODE="$2"
            shift
            ;;
        --snapshot-url)
            SNAPSHOT_URL="$2"
            shift
            ;;
        --open-ports)
            OPEN_PORTS="true"
            ;;
        --fast)
            BUILD_FAST="true"
            ;;
        --debug-cpp)
            DEBUG_CPP="true"
            ;;
        --all-monitors|--monitor-all)
            ALL_MONITORS="true"
            ;;
        --register)
            REGISTER=1
            ;;
        --no-register)
            NO_REGISTER=1
            ;;
        --fund-genesis)
            FUND_GENESIS=1
            ;;
        --no-fund-genesis)
            FUND_GENESIS=0
            ;;
        --relayer)
            if [[ "$#" -gt 1 && ! "$2" =~ ^-- ]]; then
                RELAYER_ACTION="$2"
                shift
            else
                RELAYER_ACTION="start"
            fi
            ;;
        -i|--inventory)
            INVENTORY="$2"
            shift
            ;;
        -h|--help)
            echo "═══════════════════════════════════════════════════════════════"
            echo "🌐 METANODE UNIFIED ANSIBLE DEPLOYMENT MANAGER"
            echo "═══════════════════════════════════════════════════════════════"
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "🎯 Chế độ triển khai (Deployment Mode):"
            echo "  --public             Chế độ Public Chain / Root Anchor (Mặc định)"
            echo "  --private            Chế độ Private Chains"
            echo "  --chain=ID, -c=ID    Chỉ thao tác trên 1 Private Chain (ví dụ: --chain=101)"
            echo ""
            echo "⚡ Các hành động (Actions):"
            echo "  --start              Khởi động hệ thống nodes"
            echo "  --restart            Khởi động lại nhanh systemd services"
            echo "  --setup              Khởi tạo nodes mới"
            echo "  --reset-all          Reset sạch và cài đặt lại toàn bộ (/opt/metanode)"
            echo "  --clean-data         Xóa trắng data/logs, giữ nguyên keys & cấu hình"
            echo "  --stop               Dừng toàn bộ nodes"
            echo "  --status             Kiểm tra trạng thái RPC và block number"
            echo ""
            echo "🌉 Công cụ Cross-Chain & Relayer:"
            echo "  --register           Đăng ký Private Chains lên Root Anchor"
            echo "  --relayer [ACTION]   Quản lý tmux Relayer (start|stop|restart|status|logs|attach)"
            echo ""
            echo "⚙️ Tùy chọn khác:"
            echo "  --open-ports         Tự động mở firewall ports qua UFW"
            echo "  --fast               Biên dịch nhanh"
            echo "  --inventory FILE     Chỉ định file inventory tùy chỉnh"
            exit 0
            ;;
        *)
            echo "❌ Unknown parameter passed: $1"
            echo "👉 Run: $0 --help for usage instructions."
            exit 1
            ;;
    esac
    shift
done

# Xử lý lệnh Relayer độc lập nếu có
if [ -n "$RELAYER_ACTION" ]; then
    if [ -f "${SCRIPT_DIR}/run_relayer_tmux.sh" ]; then
        exec bash "${SCRIPT_DIR}/run_relayer_tmux.sh" "$RELAYER_ACTION"
    else
        echo "❌ ERROR: Không tìm thấy ${SCRIPT_DIR}/run_relayer_tmux.sh"
        exit 1
    fi
fi

# Detect Deployer Server IP dynamically
DEPLOY_IP=$(hostname -I | tr ' ' '\n' | grep -E '^(192\.168\.|10\.|172\.)' | head -n 1 || hostname -I | awk '{print $1}')

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
if [ -z "$TELEGRAM_BOT_TOKEN" ] && [ -f "$INVENTORY" ]; then
    TELEGRAM_BOT_TOKEN=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    d = yaml.safe_load(f) or {}
print(d.get('all', {}).get('vars', {}).get('telegram_bot_token', '') 
   or d.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('vars', {}).get('telegram_bot_token', ''))
" 2>/dev/null || echo "")
    TELEGRAM_CHAT_ID=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    d = yaml.safe_load(f) or {}
print(d.get('all', {}).get('vars', {}).get('telegram_chat_id', '') 
   or d.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('vars', {}).get('telegram_chat_id', ''))
" 2>/dev/null || echo "-1003867050625")
fi

# ==============================================================================
# 1. THỰC THI CHẾ ĐỘ PRIVATE CHAINS
# ==============================================================================
if [ "$DEPLOY_MODE" == "private" ]; then
    PLAYBOOK="${SCRIPT_DIR}/deploy_private.yml"

    # Nếu action là setup/reset và người dùng không cấm register thì tự động đăng ký
    if [ "$ACTION" = "setup" ] || [ "$ACTION" = "reset" ]; then
        if [ "$NO_REGISTER" -eq 0 ]; then
            REGISTER=1
        fi
    fi

    ACTION_LABEL=$(echo "$ACTION" | tr '[:lower:]' '[:upper:]')

    # Lấy tóm tắt danh sách Private Chains mục tiêu
    CHAINS_SUMMARY=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f) or {}
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
target = '$TARGET_CHAIN'
for name, h in hosts.items():
    cid = str(h.get('chain_id', ''))
    if target != 'all' and cid != target:
        continue
    ip = h.get('ansible_host', '127.0.0.1')
    rpc = h.get('rpc_port', 8546)
    offset = h.get('port_offset', 10)
    print(f'• Chain {cid}: http://{ip}:{rpc} (Offset: {offset})')
" 2>/dev/null || echo "Target Chain: $TARGET_CHAIN")

    echo "═══════════════════════════════════════════════════════════════"
    echo "🌐 METANODE PRIVATE CHAINS — ANSIBLE DEPLOYMENT"
    echo "═══════════════════════════════════════════════════════════════"
    echo "📋 Cấu hình thực thi:"
    echo "   - Mode:         Private Chains"
    echo "   - Action:       $ACTION"
    echo "   - Target Chain: $TARGET_CHAIN"
    echo "   - Open Ports:   $OPEN_PORTS"
    echo "   - Auto Register:$REGISTER"
    echo "   - Inventory:    $INVENTORY"
    echo ""

    send_telegram_notification "🚀 <b>[PRIVATE CHAINS - ${ACTION_LABEL}]</b> Bắt đầu quá trình triển khai:
- Deployer Server IP: <code>${DEPLOY_IP}</code>
- Target Chains: <code>${TARGET_CHAIN}</code>
- Source: <code>${DEPLOY_SOURCE}</code>
- Action: <code>${ACTION}</code>
- Auto Register Gateway: <code>${REGISTER}</code>

📋 <b>Danh sách Private Chains:</b>
<pre>
${CHAINS_SUMMARY}
</pre>"

    set +e
    echo "🚀 Đang thực thi Ansible Playbook deploy_private.yml ..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK" \
        -e "deploy_action=$ACTION" \
        -e "target_chain=$TARGET_CHAIN" \
        -e "open_ports=$OPEN_PORTS"
    ansible_exit=$?
    set -e

    if [ $ansible_exit -eq 0 ]; then
        REGISTER_STATUS="Không kích hoạt"

        # Đăng ký Gateway Root Anchor nếu được kích hoạt
        if [ "$REGISTER" -eq 1 ]; then
            echo ""
            echo "═══════════════════════════════════════════════════════════════"
            echo "📝 ĐĂNG KÝ CÁC PRIVATE CHAINS LÊN GATEWAY (ROOT ANCHOR)"
            echo "═══════════════════════════════════════════════════════════════"

            ROOT_ANCHOR_RPC=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
print(data.get('all', {}).get('vars', {}).get('root_anchor_rpc', 'http://127.0.0.1:10746'))
")

            CHAINS_LIST=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
target = '$TARGET_CHAIN'
if target != 'all':
    c_ids = [str(h['chain_id']) for h in hosts.values() if 'chain_id' in h and str(h['chain_id']) == target]
else:
    c_ids = [str(h['chain_id']) for h in hosts.values() if 'chain_id' in h]
print(','.join(c_ids))
")

            TARGET_RPCS=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
target = '$TARGET_CHAIN'
if target != 'all':
    rpcs = [f\"{h['chain_id']}=http://{h.get('ansible_host', '127.0.0.1')}:{h.get('rpc_port', 8546)}\" for h in hosts.values() if 'chain_id' in h and str(h['chain_id']) == target]
else:
    rpcs = [f\"{h['chain_id']}=http://{h.get('ansible_host', '127.0.0.1')}:{h.get('rpc_port', 8546)}\" for h in hosts.values() if 'chain_id' in h]
print(','.join(rpcs))
")

            GENESIS_SUPPLY=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
print(data.get('all', {}).get('vars', {}).get('root_anchor_genesis_supply', '400000000000000000000000000'))
")
            PER_CHAIN_ALLOCATION=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
print(data.get('all', {}).get('vars', {}).get('root_anchor_per_chain_allocation', '100000000000000000000000000'))
")

            echo "   - Root Anchor RPC: $ROOT_ANCHOR_RPC"
            echo "   - Private Chains:  $CHAINS_LIST"
            echo "   - Target RPCs:     $TARGET_RPCS"
            echo "   - Genesis supply:  $GENESIS_SUPPLY (per-chain: $PER_CHAIN_ALLOCATION)"
            echo ""

            # Xuất file json ngắn gọn chứa IP RPC & TCP của toàn bộ private chains
            python3 -c "
import yaml, json
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
root_rpc = data.get('all', {}).get('vars', {}).get('root_anchor_rpc', 'http://127.0.0.1:10746')

out = {
    'root_anchor': root_rpc,
    'nodes': {},
    'tcp_nodes': {}
}
for h in hosts.values():
    if 'chain_id' in h:
        cid = str(h['chain_id'])
        ip = h.get('ansible_host', '127.0.0.1')
        port = h.get('rpc_port', 8546)
        p_offset = int(h.get('port_offset', 10))
        out['nodes'][cid] = f'http://{ip}:{port}'
        out['tcp_nodes'][cid] = f'{ip}:{4200 + p_offset}'

with open('/tmp/private_chains.json', 'w') as f:
    json.dump(out, f, indent=2)
print('📄 Đã xuất cấu hình ngắn gọn ra: /tmp/private_chains.json')
"

            SUBMITTER_KEY=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
print(data.get('all', {}).get('vars', {}).get('root_anchor_submitter_key', ''))
")

            if [ -n "$CHAINS_LIST" ] && [ -n "$SUBMITTER_KEY" ]; then
                EXTRA_REGISTER_FLAGS=""
                if [ "$FUND_GENESIS" -eq 1 ]; then
                    EXTRA_REGISTER_FLAGS="-fund-genesis -genesis-supply $GENESIS_SUPPLY -per-chain-allocation $PER_CHAIN_ALLOCATION"
                fi

                echo "🚀 Đang thực thi tool register_chains..."
                set +e
                (
                    cd "${REPO_ROOT}/execution"
                    go run ./cmd/tool/register_chains \
                        -root-anchor "$ROOT_ANCHOR_RPC" \
                        -chains "$CHAINS_LIST" \
                        -target-rpcs "$TARGET_RPCS" \
                        -chains-dir "${SCRIPT_DIR}/data" \
                        -key "$SUBMITTER_KEY" \
                        $EXTRA_REGISTER_FLAGS
                )
                reg_exit=$?
                set -e
                if [ $reg_exit -eq 0 ]; then
                    REGISTER_STATUS="✅ Đã đăng ký thành công lên Root Anchor Gateway"
                else
                    REGISTER_STATUS="⚠️ Đã đăng ký Gateway (Hoàn tất Bootstrap founding chains)"
                fi
            fi
        fi

        # Lấy thông tin block number và endpoint thực tế của các chuỗi
        RPC_SUMMARY=$(python3 -c "
import yaml, urllib.request, json
with open('$INVENTORY') as f:
    data = yaml.safe_load(f) or {}
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
target = '$TARGET_CHAIN'
for name, h in hosts.items():
    cid = str(h.get('chain_id', ''))
    if target != 'all' and cid != target:
        continue
    ip = h.get('ansible_host', '127.0.0.1')
    rpc = h.get('rpc_port', 8546)
    blk = 'N/A'
    try:
        req = urllib.request.Request(f'http://{ip}:{rpc}', data=b'{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}', headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=3) as resp:
            res = json.loads(resp.read().decode('utf-8'))
            blk = res.get('result', 'N/A')
    except Exception:
        pass
    print(f'• Chain {cid}: http://{ip}:{rpc} | Block: {blk}')
" 2>/dev/null || echo "")

        send_telegram_notification "✅ <b>[PRIVATE CHAINS - ${ACTION_LABEL}]</b> Triển khai Private Chains thành công!
- Target Chains: <code>${TARGET_CHAIN}</code>
- Source: <code>${DEPLOY_SOURCE}</code>

⚙️ <b>Cấu hình RPC & Endpoints các Chain:</b>
<pre>
${RPC_SUMMARY}
</pre>

🌉 <b>Trạng thái Gateway Register:</b>
<code>${REGISTER_STATUS}</code>

🔍 <b>Lệnh kiểm tra & theo dõi:</b>
• <b>Kiểm tra RPC:</b> <code>./ansible_deploy.sh --private --status</code>
• <b>Xem logs:</b> <code>./fetch_node_logs.sh --private</code>
• <b>Bật Relayer:</b> <code>./ansible_deploy.sh --relayer start</code>"

        echo ""
        echo "🎉 Hoàn tất thao tác Private Chains!"
        exit 0
    else
        send_telegram_notification "❌ <b>[PRIVATE CHAINS - ${ACTION_LABEL}]</b> Triển khai Private Chains thất bại với mã lỗi <code>${ansible_exit}</code>!
- Target Chains: <code>${TARGET_CHAIN}</code>
- Source: <code>${DEPLOY_SOURCE}</code>

🔍 <b>Lệnh lấy log kiểm tra lỗi:</b>
<code>./fetch_node_logs.sh --private</code>"

        echo "❌ ERROR: Triển khai Private Chains thất bại!"
        exit $ansible_exit
    fi
fi

# ==============================================================================
# 2. THỰC THI CHẾ ĐỘ PUBLIC CHAIN (ROOT ANCHOR - CHAIN 991)
# ==============================================================================
PLAYBOOK="${SCRIPT_DIR}/deploy.yml"

if [ "$ACTION" == "reset" ]; then
    ACTION="setup"
fi

if pgrep -f "auto_rebuild_deploy.sh" >/dev/null 2>&1; then
    WATCHER_STATUS="Đang hoạt động (Active) 🟢"
else
    WATCHER_STATUS="Đã tắt (Inactive) 🔴"
fi

TARGET_NODES_IPS=""
if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    TARGET_NODES_IPS=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "$TARGET_NODE" || echo "")
    python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" json > "/tmp/rpc_nodes.json" 2>/dev/null || true
fi

ACTION_LABEL=$(echo "$ACTION" | tr '[:lower:]' '[:upper:]')

echo -e "\n🚀 Starting Ansible Public Cluster ${ACTION_LABEL} with:"
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

send_telegram_notification "🚀 <b>[${ACTION_LABEL}]</b> Bắt đầu quá trình Ansible Public Cluster ${ACTION_LABEL}:
- Deployer Server IP: <code>${DEPLOY_IP}</code>
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Source: <code>${DEPLOY_SOURCE}</code>
- Action: <code>${ACTION}</code>
- Target Node: <code>${TARGET_NODE}</code>
- Keep Data: <code>${KEEP_DATA}</code>
- Restore Node: <code>${RESTORE_NODE}</code>
- Open Ports: <code>${OPEN_PORTS}</code>
- All Monitors: <code>${ALL_MONITORS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>"

EXTRA_VARS="ansible_action=${ACTION} target_node=${TARGET_NODE} keep_data=${KEEP_DATA} restore_node=${RESTORE_NODE} open_ports=${OPEN_PORTS} ansible_build_fast=${BUILD_FAST} ansible_debug_cpp=${DEBUG_CPP}"
if [ -n "$SNAPSHOT_URL" ]; then
    EXTRA_VARS="${EXTRA_VARS} snapshot_url='${SNAPSHOT_URL}'"
fi

echo -e "\n⏸ Tạm dừng Health Monitor trên toàn bộ cụm trong quá trình Deploy để tránh cảnh báo sai..."
if [ -f "${SCRIPT_DIR}/monitors/start_monitors.sh" ]; then
    bash "${SCRIPT_DIR}/monitors/start_monitors.sh" --stop-all >/dev/null 2>&1 || true
fi
pkill -9 -f "start_monitors.sh" || true
pkill -9 -f "block_hash_checker" || true
pkill -9 -f "go run main.go.*--no-stop-flag" || true

if [ "$KEEP_DATA" == "false" ]; then
    echo -e "🧹 Dọn dẹp cache và log cũ của Monitors do dữ liệu Node bị xoá..."
    rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/ghost_blocks.log"
    rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/block_checker_daemon.log"
    rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/chain_anomalies.log"
    rm -f "${SCRIPT_DIR}/monitors/block_hash_checker/"*.csv
fi

cd "$SCRIPT_DIR"
set +e
ansible-playbook -i "$INVENTORY" "$PLAYBOOK" -e "$EXTRA_VARS"
ansible_exit=$?
set -e

if [ $ansible_exit -eq 0 ]; then
    if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        git rev-parse HEAD > "${SCRIPT_DIR}/.last_deployed_commit" 2>/dev/null || true
    fi

    RPC_CONFIG=""
    if [ -f "/tmp/rpc_nodes.json" ]; then
        RPC_CONFIG=$(jq . /tmp/rpc_nodes.json 2>/dev/null || cat /tmp/rpc_nodes.json)
    fi

    echo -e "\n⚙️ Cấu hình kết nối client:"
    echo "$RPC_CONFIG"

    echo -e  "\n📋 *Node Roles:*"
    echo "${ROLES_OUTPUT}"
    send_telegram_notification "✅ <b>[${ACTION_LABEL}]</b> Quá trình Ansible Public Cluster ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> hoàn tất thành công!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>

⚙️ <b>Cấu hình kết nối client:</b>
<pre>
${RPC_CONFIG}
</pre>"
else
    send_telegram_notification "❌ <b>[${ACTION_LABEL}]</b> Quá trình Ansible Public Cluster ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> thất bại với mã lỗi <code>${ansible_exit}</code>!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>"
fi

MONITOR_SCRIPT="${SCRIPT_DIR}/monitors/start_monitors.sh"
if [ -f "$MONITOR_SCRIPT" ] && [ "$ACTION" != "stop" ]; then
    if [ "$ALL_MONITORS" == "true" ]; then
        echo -e "\n▶️ Bật Giám Sát Chéo Đa Máy (Mutual Cross-Monitors) trên TẤT CẢ các máy..."
        bash "$MONITOR_SCRIPT" --all-hosts
    else
        echo -e "\n▶️ Bật lại Health Monitor cục bộ sau khi Deploy xong..."
        bash "$MONITOR_SCRIPT"
    fi
elif [ "$ACTION" == "stop" ]; then
    echo -e "\n⏸ Không bật lại Health Monitor vì hệ thống đang ở trạng thái STOP..."
    if [ "$ALL_MONITORS" == "true" ]; then
        ansible metanode_cluster -i "$INVENTORY" -m shell -a "pkill -f 'start_monitors.sh' || true; pkill -f 'block_hash_checker' || true" >/dev/null 2>&1 || true
    fi
fi

exit $ansible_exit
