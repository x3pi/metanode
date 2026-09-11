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
# ║    --all-monitors      Run monitors mutually across ALL machines  ║
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

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-""}"

load_telegram_config() {
    local target_yml="$1"
    local token=""
    local chat_id=""

    # 1. First priority: Read directly from target YAML (inventory.yml or monitors/inventory.yml)
    if [ -f "$target_yml" ]; then
        token=$(grep -E '^\s*telegram_bot_token:' "$target_yml" | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g')
        chat_id=$(grep -E '^\s*telegram_chat_id:' "$target_yml" | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g')
    fi

    # 2. Fallback to .env if not found in YAML
    if [ -z "$token" ]; then
        load_env_file "${SCRIPT_DIR}/.env"
        load_env_file "${SCRIPT_DIR}/../.env"
        [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && token="$TELEGRAM_BOT_TOKEN"
        [ -n "${TELEGRAM_CHAT_ID:-}" ] && chat_id="$TELEGRAM_CHAT_ID"
    fi

    [ -n "$token" ] && TELEGRAM_BOT_TOKEN="$token"
    [ -n "$chat_id" ] && TELEGRAM_CHAT_ID="$chat_id"
    export TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
    export TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"
}

send_telegram_notification() {
    local message="$1"
    if [ -n "$TELEGRAM_BOT_TOKEN" ]; then
        curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            -d "parse_mode=HTML" \
            --data-urlencode "text=${message}" > /dev/null 2>&1 || true
    fi
}

INVENTORY="${SCRIPT_DIR}/inventory.yml"
PLAYBOOK="${SCRIPT_DIR}/deploy.yml"

# Defaults
ACTION=""
EXPLICIT_ACTION="false"
KEEP_DATA="true"
TARGET_NODE="all"
RESTORE_NODE="none"
SNAPSHOT_URL=""
BTRFS_SIZE_VAL=""
OPEN_PORTS="false"
BUILD_FAST="false"
DEBUG_CPP="false"
ALL_MONITORS="false"
USE_PREBUILT="false"
PREBUILT_BIN_DIR=""

DEPLOY_SOURCE="${DEPLOY_SOURCE:-"Manual (Local Machine)"}"
# BUG FIX (2026-09-10): must query the metanode repo (SCRIPT_DIR), not the caller's cwd.
# When invoked by an absolute path from a DIFFERENT repo (e.g. metanode-suite's
# run_restart_test.sh calling "${ANSIBLE_DIR}/ansible_deploy.sh" without cd-ing into
# metanode first), a bare `git rev-parse` here inherited the caller's cwd and reported
# THAT repo's branch/commit instead -- e.g. logged "Branch: master | Commit: 33e70d4"
# (metanode-suite's own state) during every node_chaos_restart rolling-restart step,
# even though the metanode repo actually deploying the binaries was correctly on dev.
# Purely a misleading log/Telegram label, never a wrong-branch deploy -- but confusing
# enough during a live incident investigation to fix outright with `git -C`.
if command -v git >/dev/null 2>&1 && git -C "$SCRIPT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    GIT_BRANCH=$(git -C "$SCRIPT_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
    GIT_COMMIT=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")
    if [[ "$GIT_BRANCH" != "unknown" ]] || [[ "$GIT_COMMIT" != "unknown" ]]; then
        if [[ ! "$DEPLOY_SOURCE" =~ "Branch:" ]] && [[ ! "$DEPLOY_SOURCE" =~ "$GIT_BRANCH" ]]; then
            DEPLOY_SOURCE="$DEPLOY_SOURCE (Branch: $GIT_BRANCH | Commit: $GIT_COMMIT)"
        fi
    fi
fi

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --start) ACTION="start"; KEEP_DATA="true"; EXPLICIT_ACTION="true" ;;
        --restart) ACTION="restart"; KEEP_DATA="true"; EXPLICIT_ACTION="true" ;;
        --reset-all) ACTION="setup"; KEEP_DATA="false"; EXPLICIT_ACTION="true" ;;
        --stop) ACTION="stop"; EXPLICIT_ACTION="true" ;;
        --clean) KEEP_DATA="false" ;;
        --gen-keys) ACTION="gen_keys"; EXPLICIT_ACTION="true" ;;
        --only-node) TARGET_NODE="$2"; shift ;;
        --restore-node) RESTORE_NODE="$2"; shift ;;
        --snapshot-url) SNAPSHOT_URL="$2"; shift ;;
        --btrfs-size) BTRFS_SIZE_VAL="$2"; shift ;;
        --open-ports) OPEN_PORTS="true" ;;
        --fast) BUILD_FAST="true" ;;
        --debug-cpp) DEBUG_CPP="true" ;;
        --all-monitors|--monitor-all) ALL_MONITORS="true" ;;
        --bin-dir)
            USE_PREBUILT="true"
            PREBUILT_BIN_DIR="$2"
            shift
            ;;
        --prebuilt-bin|--use-prebuilt)
            USE_PREBUILT="true"
            if [[ "$#" -gt 1 && ! "$2" =~ ^-- ]]; then
                PREBUILT_BIN_DIR="$2"
                shift
            fi
            ;;
        --skip-build)
            USE_PREBUILT="true"
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo "Options:"
            echo "  --start             Start nodes (re-distribute binaries)"
            echo "  --restart           Fast restart systemd services"
            echo "  --reset-all         Fresh setup (gen keys, clears data)"
            echo "  --gen-keys          Only generate keys & genesis locally (does not touch servers)"
            echo "  --stop              Stop nodes and monitors"
            echo "  --clean             Clear data before starting nodes"
            echo "  --only-node N       Only apply actions to node N"
            echo "  --restore-node N    Restore node N from snapshot url"
            echo "  --snapshot-url U    Snapshot URL to use (e.g. http://ip:8604)"
            echo "  --btrfs-size SIZE   Size of BTRFS partition/image (e.g. 50G, 100G, 400G)"
            echo "  --open-ports        Open firewall ports for the nodes"
            echo "  --all-monitors      Run monitors mutually across ALL machines"
            echo "  --fast              Fast build (skip redundant steps)"
            echo "  --debug-cpp         Enable debug mode for C++ MVM linker"
            echo "  --skip-build        Sử dụng binary có sẵn, bỏ qua toàn bộ bước build code"
            echo "  --prebuilt-bin [D]  Sử dụng binary có sẵn từ thư mục D (Mặc định: deploy/bin)"
            echo "  --bin-dir D         Chỉ định thư mục chứa file binary có sẵn"
            exit 0
            ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

# Resolve Telegram configuration from YAML:
# If --all-monitors flag is active, read from monitors/inventory.yml.
# Otherwise read from standard inventory.yml.
if [ "$ALL_MONITORS" == "true" ]; then
    TG_CONFIG_YML="${SCRIPT_DIR}/monitors/inventory.yml"
else
    TG_CONFIG_YML="${SCRIPT_DIR}/inventory.yml"
fi
load_telegram_config "$TG_CONFIG_YML"

if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
    echo -e "\033[0;33m⚠️ [CẢNH BÁO] Không tìm thấy telegram_bot_token trong ${TG_CONFIG_YML} (hoặc .env). Thông báo Telegram sẽ bị tắt.\033[0m\n"
fi

# Resolve prebuilt binary path if enabled
if [ "$USE_PREBUILT" == "true" ]; then
    if [ -z "$PREBUILT_BIN_DIR" ]; then
        CANDIDATES=(
            "${SCRIPT_DIR}/../bin"
            "${SCRIPT_DIR}/../private_chain_kit/bin"
            "${SCRIPT_DIR}/../../metanode-deploy/bin"
            "${SCRIPT_DIR}/../../../metanode-suite/private-chain-v1/private_chain_kit/bin"
        )
        for cand in "${CANDIDATES[@]}"; do
            if [ -f "$cand/metanode" ] && [ -f "$cand/simple_chain" ]; then
                PREBUILT_BIN_DIR="$cand"
                break
            fi
        done
        if [ -z "$PREBUILT_BIN_DIR" ]; then
            PREBUILT_BIN_DIR="${SCRIPT_DIR}/../bin"
        fi
    fi

    if [ -d "$PREBUILT_BIN_DIR" ]; then
        PREBUILT_BIN_DIR="$(cd "$PREBUILT_BIN_DIR" && pwd)"
    fi

    MISSING_BINS=()
    if [ ! -f "${PREBUILT_BIN_DIR}/metanode" ]; then
        MISSING_BINS+=("metanode")
    fi
    if [ ! -f "${PREBUILT_BIN_DIR}/simple_chain" ]; then
        MISSING_BINS+=("simple_chain")
    fi

    if [ ${#MISSING_BINS[@]} -gt 0 ]; then
        echo -e "\n\033[0;31m❌ [LỖI PREBUILT BINARY] Không tìm thấy file nhị phân (${MISSING_BINS[*]}) tại:\033[0m"
        echo -e "   \033[0;33m${PREBUILT_BIN_DIR}\033[0m"
        echo -e "\033[0;36m   👉 Hãy chạy script build trước để tạo các file nhị phân:\033[0m"
        echo -e "      \033[1;32m./deploy/build_private_chain_bins.sh\033[0m"
        echo -e "\033[0;36m   👉 Hoặc chỉ định đường dẫn chứa file binary đã có sẵn:\033[0m"
        echo -e "      \033[1;32m./ansible_deploy.sh --prebuilt-bin /duong/dan/chua/bin\033[0m\n"
        exit 1
    fi

    chmod +x "${PREBUILT_BIN_DIR}/metanode" "${PREBUILT_BIN_DIR}/simple_chain" 2>/dev/null || true
    for tool in cross_chain_relayer register_chains bls_pubkey gen_recovery_committee; do
        if [ -f "${PREBUILT_BIN_DIR}/${tool}" ]; then
            chmod +x "${PREBUILT_BIN_DIR}/${tool}" 2>/dev/null || true
        fi
    done
fi

# Resolve default action if not explicitly specified
if [ "$EXPLICIT_ACTION" == "false" ]; then
    if [ "$OPEN_PORTS" == "true" ]; then
        ACTION="open_ports"
    else
        ACTION="start"
    fi
fi

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
    if ! TARGET_NODES_IPS=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "$TARGET_NODE"); then
        echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Cấu hình ${INVENTORY} không hợp lệ! Vui lòng sửa cấu hình theo thông báo trên trước khi tiếp tục.\033[0m\n"
        exit 1
    fi
    rm -f "/tmp/rpc_nodes.json" 2>/dev/null || true
    if ! (umask 077 && python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" json > "/tmp/rpc_nodes.json"); then
        echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Không thể xuất thông tin RPC từ ${INVENTORY}!\033[0m\n"
        exit 1
    fi
    chmod 0600 "/tmp/rpc_nodes.json" 2>/dev/null || true
fi

# Safety Check: Node tạo snapshot KHÔNG ĐƯỢC PHÉP tự khôi phục chính nó
#
# Logic phân loại node snapshot/synconly (phải khớp CHÍNH XÁC cách deploy.yml tự phân loại,
# và fail-closed nếu không xác minh được) sống trong check_snapshot_node.py -- tách ra file
# riêng thay vì Python inline trong heredoc để có thể unit-test độc lập, xem
# test_check_snapshot_node.py. Guard an toàn dữ liệu không được phép fail-open.
if [ "$RESTORE_NODE" != "none" ] && [ -f "${INVENTORY}" ]; then
    SNAP_CHECK_OUTPUT=$(python3 "${SCRIPT_DIR}/check_snapshot_node.py" "${INVENTORY}" "${RESTORE_NODE}" 2>&1)
    SNAP_CHECK_RC=$?
    if [ $SNAP_CHECK_RC -ne 0 ]; then
        echo -e "\n\033[0;31m❌ [LỖI AN TOÀN] Không thể xác minh Node ${RESTORE_NODE} có phải Node tạo Snapshot hay không!\033[0m"
        echo -e "\033[0;33m   ${SNAP_CHECK_OUTPUT}\033[0m"
        echo -e "\033[0;36m   👉 Kiểm tra lại ${INVENTORY} và đảm bảo đã cài PyYAML (pip install pyyaml), rồi chạy lại.\033[0m\n"
        exit 1
    fi
    IS_SNAP_NODE="$SNAP_CHECK_OUTPUT"
    if [ "$IS_SNAP_NODE" == "true" ]; then
        echo -e "\n\033[0;31m❌ [LỖI AN TOÀN] Node ${RESTORE_NODE} là Node tạo Snapshot (SyncOnly)!\033[0m"
        echo -e "\033[0;33m   ⚠️ Node tạo snapshot KHÔNG ĐƯỢC PHÉP tự khôi phục chính nó.\033[0m"
        echo -e "\033[0;36m   👉 Chỉ được phép khôi phục dữ liệu snapshot trên các Node Validator (vd: 0, 1, 2, 3).\033[0m\n"
        exit 1
    fi
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
echo "   BTRFS Size:         ${BTRFS_SIZE_VAL:-"(từ inventory.yml)"}"
echo "   Open Ports:         $OPEN_PORTS"
echo "   Build Fast:         $BUILD_FAST"
echo "   Prebuilt Bin:       ${USE_PREBUILT}${PREBUILT_BIN_DIR:+ (Dir: $PREBUILT_BIN_DIR)}"
echo "   Watcher:            $WATCHER_STATUS"

ROLES_OUTPUT=""
if [ -f "${SCRIPT_DIR}/parse_inventory.py" ]; then
    if ! ROLES_OUTPUT=$(python3 "${SCRIPT_DIR}/parse_inventory.py" "$INVENTORY" "roles"); then
        echo -e "\n\033[0;31m❌ [LỖI DỪNG THỰC THI] Không thể đọc vai trò các node từ ${INVENTORY}!\033[0m\n"
        exit 1
    fi
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
- Prebuilt Bin: <code>${USE_PREBUILT}${PREBUILT_BIN_DIR:+ (Dir: ${PREBUILT_BIN_DIR})}</code>
- BTRFS Size: <code>${BTRFS_SIZE_VAL:-"default"}</code>
- Open Ports: <code>${OPEN_PORTS}</code>
- All Monitors: <code>${ALL_MONITORS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>"

# Prepare extra vars
EXTRA_VARS="ansible_action=${ACTION} target_node=${TARGET_NODE} keep_data=${KEEP_DATA} restore_node=${RESTORE_NODE} open_ports=${OPEN_PORTS} ansible_build_fast=${BUILD_FAST} ansible_debug_cpp=${DEBUG_CPP} ansible_use_prebuilt=${USE_PREBUILT} ansible_prebuilt_bin_dir='${PREBUILT_BIN_DIR}'"
if [ -n "$SNAPSHOT_URL" ]; then
    EXTRA_VARS="${EXTRA_VARS} snapshot_url='${SNAPSHOT_URL}'"
fi
if [ -n "$BTRFS_SIZE_VAL" ]; then
    EXTRA_VARS="${EXTRA_VARS} btrfs_size='${BTRFS_SIZE_VAL}'"
fi

# Detect become password from inventory for localhost become tasks
INVENTORY_BECOME_PASS=$(grep -E '^\s*ansible_become_pass:' "$INVENTORY" | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g')
if [ -n "$INVENTORY_BECOME_PASS" ]; then
    EXTRA_VARS="${EXTRA_VARS} ansible_become_pass='${INVENTORY_BECOME_PASS}'"
fi

if [ "$ACTION" == "gen_keys" ]; then
    echo -e "\n🔑 [GEN-KEYS] Bắt đầu sinh bộ Key & Genesis mẫu cục bộ (Không đụng tới server)..."
    cd "$SCRIPT_DIR"
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK" -e "$EXTRA_VARS" --tags gen_keys
    exit_code=$?
    if [ $exit_code -eq 0 ]; then
        echo -e "\n=========================================================="
        echo -e "✅ ĐÃ TẠO XONG KEYS & GENESIS MẪU CỤC BỘ!"
        echo -e "=========================================================="
        echo -e "📁 Vị trí lưu trữ:"
        echo -e "   • Thư mục keys từng node: deploy/systemd/node-X_keys/"
        echo -e "   • File Genesis chung:      deploy/systemd/genesis.json"
        echo -e "\n✏️ BƯỚC TIẾP THEO (NẾU MUỐN SỬA):"
        echo -e "   1. Vào deploy/systemd/node-X_keys thay file key của bạn."
        echo -e "   2. Mở deploy/systemd/genesis.json chỉnh chainId, ví nhận tiền (alloc)..."
        echo -e "\n🚀 KHI ĐÃ SẴN SÀNG KHỞI ĐỘNG CHUỖI TỪ BLOCK 0 VỚI BỘ KEY NÀY:"
        echo -e "   ./ansible_deploy.sh --start --clean --open-ports"
        echo -e "   (⚠️ Không dùng --reset-all để tránh bị đúc đè lại key)"
        echo -e "==========================================================\n"
    fi
    exit $exit_code
fi

if [ "$ACTION" != "open_ports" ]; then
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
fi

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

    # Read and format Node RPC IPs and TCP Nodes from /tmp/rpc_nodes.json
    RPC_NODES_LIST=""
    TCP_NODES_LIST=""
    if [ -f "/tmp/rpc_nodes.json" ]; then
        RPC_NODES_LIST=$(jq -r '.nodes | to_entries[] | "  • \(.key): \(.value)"' /tmp/rpc_nodes.json 2>/dev/null || true)
        TCP_NODES_LIST=$(jq -r '.tcp_nodes | to_entries[] | "  • \(.key): \(.value)"' /tmp/rpc_nodes.json 2>/dev/null || true)
    fi

    echo -e "\n⚙️ Danh sách Node RPC (IP & Port):"
    echo "$RPC_NODES_LIST"

    echo -e "\n🌐 Danh sách Node TCP (Consensus P2P):"
    echo "$TCP_NODES_LIST"

    echo -e  "\n📋 *Node Roles:*"
    echo "${ROLES_OUTPUT}"
    send_telegram_notification "✅ <b>[${ACTION_LABEL}]</b> Quá trình Ansible ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> hoàn tất thành công!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

📋 <b>Node Roles:</b>
<pre>
${ROLES_OUTPUT}
</pre>

⚙️ <b>Danh sách Node RPC:</b>
<pre>
${RPC_NODES_LIST}
</pre>

🌐 <b>Danh sách Node TCP (Consensus P2P):</b>
<pre>
${TCP_NODES_LIST}
</pre>

💡 <b>Xem log nhanh:</b> <code>./fetch_node_logs.sh</code> (thêm <code>--rpc</code> nếu cần log RPC; xem DEPLOY_GUIDE.md)"
else
    send_telegram_notification "❌ <b>[${ACTION_LABEL}]</b> Quá trình Ansible ${ACTION_LABEL} từ <code>${DEPLOY_SOURCE}</code> thất bại với mã lỗi <code>${ansible_exit}</code>!
- Target Node IPs: <code>${TARGET_NODES_IPS}</code>
- Watcher Daemon: <code>${WATCHER_STATUS}</code>

🔍 <b>Lệnh lấy log kiểm tra lỗi:</b>
• <b>Tại từng máy node (thay X bằng ID node, ví dụ 0, 1, 2, 3):</b>
  - <b>Consensus logs:</b>
    <code>sudo journalctl -u \"metanode-consensus-*\" -n 100 --no-pager</code>
  - <b>Execution logs:</b>
    <code>tail -n 100 /opt/metanode/node-X/logs/execution/*/execution.log</code>
• <b>Từ xa tại máy Master (chạy từ thư mục ansible):</b>
  - <b>Consensus logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"sudo journalctl -u 'metanode-consensus-*' -n 100 --no-pager\"</code>
  - <b>Execution logs:</b>
    <code>ansible all -i inventory.yml -m shell -a \"tail -n 100 /opt/metanode/node-*/logs/execution/*/execution.log\"</code>"
fi

MONITOR_SCRIPT="${SCRIPT_DIR}/monitors/start_monitors.sh"
if [ "$ACTION" != "open_ports" ]; then
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
fi

exit $ansible_exit
