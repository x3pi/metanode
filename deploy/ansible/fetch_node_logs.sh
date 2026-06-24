#!/bin/bash
# ============================================================================
# Fetch Systemd Logs Script for Ansible Configuration
# ============================================================================

set -uo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INVENTORY_FILE="$SCRIPT_DIR/inventory.yml"

FETCH_RPC=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --rpc)
            FETCH_RPC=true
            shift
            ;;
        --inventory|-i)
            if [ -z "${2:-}" ]; then
                echo -e "${YELLOW}Thiếu đường dẫn file inventory sau tham số $1${NC}"
                exit 1
            fi
            INVENTORY_FILE="$2"
            shift 2
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
    echo "Usage: ./fetch_node_logs.sh [--inventory <inventory-file>] [--rpc]"
    exit 1
fi

echo -e "${CYAN}📋 Using inventory: $INVENTORY_FILE${NC}"

# Tạo thư mục chứa log
LOCAL_SYSTEMD_LOGS_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/logs_systemd"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
RUN_LOGS_DIR="$LOCAL_SYSTEMD_LOGS_DIR/run_$TIMESTAMP"
mkdir -p "$RUN_LOGS_DIR"

echo -e "${GREEN}📥 Bắt đầu lấy systemd log từ các server về: $RUN_LOGS_DIR${NC}"

# Dùng python để parse output của ansible-inventory
parse_inventory() {
    python3 -c "
import json
import subprocess
import sys

try:
    output = subprocess.check_output(['ansible-inventory', '-i', '$INVENTORY_FILE', '--list'])
    inv = json.loads(output)
except Exception as e:
    print(f'Error reading inventory: {e}', file=sys.stderr)
    sys.exit(1)

hostvars = inv.get('_meta', {}).get('hostvars', {})
for host, vars in hostvars.items():
    ansible_host = vars.get('ansible_host', host)
    ansible_user = vars.get('ansible_user', 'root')
    ansible_connection = vars.get('ansible_connection', 'ssh')
    # ansible_ssh_pass or ansible_password
    ansible_password = vars.get('ansible_ssh_pass', vars.get('ansible_password', ''))
    ansible_private_key = vars.get('ansible_ssh_private_key_file', '')
    
    node_ids = vars.get('node_ids', [])
    if isinstance(node_ids, list):
        node_ids_str = ' '.join(map(str, node_ids))
    else:
        node_ids_str = str(node_ids)
    
    print(f'{host}|{ansible_host}|{ansible_user}|{ansible_connection}|{ansible_password}|{ansible_private_key}|{node_ids_str}')
"
}

# Lặp qua các host
while IFS='|' read -r host ansible_host ansible_user ansible_connection ansible_password ansible_private_key node_ids; do
    if [ -z "$host" ]; then
        continue
    fi
    echo -e "\n${CYAN}🌐 Đang lấy log từ server: $host ($ansible_host)...${NC}"
    
    for id in $node_ids; do
        if [ -z "$id" ]; then
            continue
        fi
        echo "   ▶ Node $id"
        
        NODE_LOGS_DIR="/opt/metanode/node-${id}/logs"
        TARGET_DIR="$RUN_LOGS_DIR/node_${id}_logs"
        
        if [ "$ansible_connection" == "local" ]; then
            if [ -d "$NODE_LOGS_DIR" ]; then
                cp -r "$NODE_LOGS_DIR" "$TARGET_DIR"
                echo "     - Folder logs: Đã lấy thành công (local)"
            else
                echo "     - Folder logs: Không tìm thấy hoặc trống"
            fi
        else
            if [ -n "$ansible_password" ]; then
                if ! command -v sshpass &> /dev/null; then
                    echo -e "${YELLOW}❌ Cần cài 'sshpass' (sudo apt install sshpass) để sử dụng password authentication.${NC}"
                else
                    sshpass -p "$ansible_password" scp -q -r -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${NODE_LOGS_DIR}" "${TARGET_DIR}" || true
                fi
            elif [ -n "$ansible_private_key" ]; then
                scp -q -r -i "$ansible_private_key" -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${NODE_LOGS_DIR}" "${TARGET_DIR}" || true
            else
                scp -q -r -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${NODE_LOGS_DIR}" "${TARGET_DIR}" || true
            fi
            
            if [ -d "$TARGET_DIR" ]; then
                echo "     - Folder logs: Đã lấy thành công"
            else
                echo "     - Folder logs: Không tìm thấy hoặc trống"
            fi
        fi

        if $FETCH_RPC; then
            RPC_LOGS_DIR="/opt/metanode/rpc-proxy/node${id}_data/logs"
            TARGET_RPC_DIR="$RUN_LOGS_DIR/node_${id}_rpc_logs"
            
            if [ "$ansible_connection" == "local" ]; then
                if [ -d "$RPC_LOGS_DIR" ]; then
                    cp -r "$RPC_LOGS_DIR" "$TARGET_RPC_DIR"
                    echo "     - Folder RPC logs: Đã lấy thành công (local)"
                else
                    echo "     - Folder RPC logs: Không tìm thấy hoặc trống"
                fi
            else
                if [ -n "$ansible_password" ]; then
                    if command -v sshpass &> /dev/null; then
                        sshpass -p "$ansible_password" scp -q -r -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${RPC_LOGS_DIR}" "${TARGET_RPC_DIR}" || true
                    fi
                elif [ -n "$ansible_private_key" ]; then
                    scp -q -r -i "$ansible_private_key" -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${RPC_LOGS_DIR}" "${TARGET_RPC_DIR}" || true
                else
                    scp -q -r -o StrictHostKeyChecking=no "${ansible_user}@${ansible_host}:${RPC_LOGS_DIR}" "${TARGET_RPC_DIR}" || true
                fi

                if [ -d "$TARGET_RPC_DIR" ]; then
                    echo "     - Folder RPC logs: Đã lấy thành công"
                else
                    echo "     - Folder RPC logs: Không tìm thấy hoặc trống"
                fi
            fi
        fi
    done
done <<< "$(parse_inventory)"

echo -e "\n${GREEN}🎉 Hoàn thành! Systemd log được lưu tại: ${RUN_LOGS_DIR}${NC}"
