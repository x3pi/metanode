#!/bin/bash
# ============================================================================
# Fetch Systemd & File Logs Script for Ansible Configuration (Public & Private)
# ============================================================================

set -uo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INVENTORY_FILE="$SCRIPT_DIR/inventory.yml"

FETCH_RPC=false
FETCH_PRIVATE=false
TARGET_CHAIN=""
LINE_COUNT="500"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --private|--private-chains)
            FETCH_PRIVATE=true
            shift
            ;;
        --chain=*|-c=*)
            FETCH_PRIVATE=true
            TARGET_CHAIN="${1#*=}"
            shift
            ;;
        --chain|-c)
            FETCH_PRIVATE=true
            TARGET_CHAIN="${2:-}"
            shift 2
            ;;
        --rpc)
            FETCH_RPC=true
            shift
            ;;
        --lines=*|-n=*)
            LINE_COUNT="${1#*=}"
            shift
            ;;
        --lines|-n)
            LINE_COUNT="${2:-500}"
            shift 2
            ;;
        --inventory|-i)
            INVENTORY_FILE="$2"
            shift 2
            ;;
        --help|-h)
            echo "Usage: ./fetch_node_logs.sh [OPTIONS]"
            echo "  --public             Lấy log cụm Public Chain (Mặc định)"
            echo "  --private            Lấy log toàn bộ Private Chains"
            echo "  --chain=ID, -c=ID    Chỉ lấy log của 1 Private Chain (ví dụ: --chain=101)"
            echo "  --rpc                Kéo cả thư mục RPC Proxy logs"
            echo "  --lines=N, -n=N      Số dòng journalctl (mặc định: 500)"
            echo "  --inventory FILE     Đường dẫn file inventory.yml"
            exit 0
            ;;
        *)
            shift
            ;;
    esac
done

if [[ "$INVENTORY_FILE" != /* ]]; then
    INVENTORY_FILE="$SCRIPT_DIR/$INVENTORY_FILE"
fi

if [ ! -f "$INVENTORY_FILE" ]; then
    echo -e "${YELLOW}Không tìm thấy file inventory: $INVENTORY_FILE${NC}"
    echo "Usage: ./fetch_node_logs.sh [--inventory <inventory-file>] [--rpc] [--private] [--chain=ID]"
    exit 1
fi

echo -e "${CYAN}📋 Using inventory: $INVENTORY_FILE${NC}"

# Tạo thư mục chứa log
LOCAL_SYSTEMD_LOGS_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/logs_systemd"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
RUN_LOGS_DIR="$LOCAL_SYSTEMD_LOGS_DIR/run_$TIMESTAMP"
mkdir -p "$RUN_LOGS_DIR"

echo -e "${GREEN}📥 Bắt đầu lấy log từ các server về: $RUN_LOGS_DIR${NC}"

if [ "$FETCH_PRIVATE" = true ]; then
    # =========================================================================
    # LẤY LOG PRIVATE CHAINS
    # =========================================================================
    parse_private_inventory() {
        python3 -c "
import json, subprocess, sys
try:
    output = subprocess.check_output(['ansible-inventory', '-i', '$INVENTORY_FILE', '--list'])
    inv = json.loads(output)
except Exception as e:
    print(f'Error reading inventory: {e}', file=sys.stderr)
    sys.exit(1)

hostvars = inv.get('_meta', {}).get('hostvars', {})
private_hosts = inv.get('private_chains', {}).get('hosts', [])
target = '$TARGET_CHAIN'

for host in private_hosts:
    vars = hostvars.get(host, {})
    chain_id = str(vars.get('chain_id', ''))
    if target and target != 'all' and chain_id != target:
        continue
    ip = vars.get('ansible_host', host)
    user = vars.get('ansible_user', 'abc')
    conn = vars.get('ansible_connection', 'ssh')
    passwd = vars.get('ansible_ssh_pass', vars.get('ansible_password', ''))
    install_dir = vars.get('install_dir', f'/opt/metanode/chain-{chain_id}')
    print(f'{host}|{ip}|{user}|{conn}|{passwd}|{chain_id}|{install_dir}')
"
    }

    while IFS='|' read -r host ip user conn passwd chain_id install_dir; do
        [ -z "$host" ] && continue
        echo -e "\n${CYAN}🌐 [Private Chain $chain_id] Server: $host ($ip)...${NC}"
        TARGET_DIR="$RUN_LOGS_DIR/chain_${chain_id}_logs"
        mkdir -p "$TARGET_DIR"

        if [ "$conn" == "local" ]; then
            if [ -d "$install_dir/logs" ]; then
                cp -r "$install_dir/logs"/* "$TARGET_DIR/" 2>/dev/null || true
                echo "     - Logs directory: Đã copy từ $install_dir/logs"
            fi
            journalctl -u "metanode-private-${chain_id}.service" -n "$LINE_COUNT" --no-pager > "$TARGET_DIR/systemd_journal.log" 2>/dev/null || true
            echo "     - Journalctl log: Đã xuất vào $TARGET_DIR/systemd_journal.log"
        else
            SSH_CMD="ssh -o StrictHostKeyChecking=no"
            SCP_CMD="scp -q -r -o StrictHostKeyChecking=no"
            if [ -n "$passwd" ] && command -v sshpass &>/dev/null; then
                SSH_CMD="sshpass -p '$passwd' $SSH_CMD"
                SCP_CMD="sshpass -p '$passwd' $SCP_CMD"
            fi
            eval "$SCP_CMD '${user}@${ip}:${install_dir}/logs/*' '$TARGET_DIR/'" 2>/dev/null || true
            eval "$SSH_CMD '${user}@${ip}' 'journalctl -u metanode-private-${chain_id}.service -n $LINE_COUNT --no-pager'" > "$TARGET_DIR/systemd_journal.log" 2>/dev/null || true
            echo "     - Remote logs & Journalctl: Đã lấy thành công"
        fi
    done < <(parse_private_inventory)

else
    # =========================================================================
    # LẤY LOG PUBLIC CLUSTER
    # =========================================================================
    parse_inventory() {
        python3 -c "
import json, subprocess, sys
try:
    output = subprocess.check_output(['ansible-inventory', '-i', '$INVENTORY_FILE', '--list'])
    inv = json.loads(output)
except Exception as e:
    print(f'Error reading inventory: {e}', file=sys.stderr)
    sys.exit(1)

hostvars = inv.get('_meta', {}).get('hostvars', {})
pub_hosts = inv.get('metanode_cluster', {}).get('hosts', inv.get('all', {}).get('hosts', []))
for host in pub_hosts:
    vars = hostvars.get(host, {})
    ansible_host = vars.get('ansible_host', host)
    ansible_user = vars.get('ansible_user', 'root')
    ansible_connection = vars.get('ansible_connection', 'ssh')
    ansible_password = vars.get('ansible_ssh_pass', vars.get('ansible_password', ''))
    ansible_private_key = vars.get('ansible_ssh_private_key_file', '')
    node_ids = vars.get('node_ids', [])
    node_ids_str = ' '.join(map(str, node_ids)) if isinstance(node_ids, list) else str(node_ids)
    print(f'{host}|{ansible_host}|{ansible_user}|{ansible_connection}|{ansible_password}|{ansible_private_key}|{node_ids_str}')
"
    }

    while IFS='|' read -r host ansible_host ansible_user ansible_connection ansible_password ansible_private_key node_ids; do
        [ -z "$host" ] && continue
        echo -e "\n${CYAN}🌐 Đang lấy log từ server: $host ($ansible_host)...${NC}"
        
        for id in $node_ids; do
            [ -z "$id" ] && continue
            echo "   ▶ Node $id"
            NODE_LOGS_DIR="/opt/metanode/node-${id}/logs"
            TARGET_DIR="$RUN_LOGS_DIR/node_${id}_logs"
            
            if [ "$ansible_connection" == "local" ]; then
                if [ -d "$NODE_LOGS_DIR" ]; then
                    cp -r "$NODE_LOGS_DIR" "$TARGET_DIR"
                    echo "     - Folder logs: Đã lấy thành công (local)"
                fi
            else
                if [ -n "$ansible_password" ] && command -v sshpass &> /dev/null; then
                    sshpass -p "$ansible_password" scp -q -r -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${NODE_LOGS_DIR}" "${TARGET_DIR}" || true
                elif [ -n "$ansible_private_key" ]; then
                    scp -q -r -i "$ansible_private_key" -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${NODE_LOGS_DIR}" "${TARGET_DIR}" || true
                else
                    scp -q -r -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${NODE_LOGS_DIR}" "${TARGET_DIR}" || true
                fi
            fi
        done
    done < <(parse_inventory)
fi

echo -e "\n${GREEN}🎉 Hoàn tất lấy logs về thư mục:${NC} $RUN_LOGS_DIR"
