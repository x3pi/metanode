#!/usr/bin/env bash
# ==============================================================================
# fetch_node_logs.sh — Kéo logs của các Private Chains về máy quản trị
# ==============================================================================
set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INVENTORY_FILE="$SCRIPT_DIR/inventory.yml"
TARGET_CHAIN=""
LINE_COUNT="500"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --chain=*|-c=*)
            TARGET_CHAIN="${1#*=}"
            shift
            ;;
        --chain|-c)
            TARGET_CHAIN="${2:-}"
            shift 2
            ;;
        --lines=*|-n=*)
            LINE_COUNT="${1#*=}"
            shift
            ;;
        --lines|-n)
            LINE_COUNT="${2:-500}"
            shift 2
            ;;
        --inventory=*|-i=*)
            INVENTORY_FILE="${1#*=}"
            shift
            ;;
        --inventory|-i)
            INVENTORY_FILE="${2:-}"
            shift 2
            ;;
        --help|-h)
            echo "Usage: ./fetch_node_logs.sh [OPTIONS]"
            echo "  --chain=ID, -c=ID    Chỉ lấy log của 1 chain cụ thể (ví dụ: --chain=101)"
            echo "  --lines=N,  -n=N     Số dòng journalctl cần lấy (mặc định: 500)"
            echo "  --inventory=F, -i=F  Đường dẫn file inventory (mặc định: inventory.yml)"
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
    echo -e "${YELLOW}❌ Không tìm thấy file inventory: $INVENTORY_FILE${NC}"
    exit 1
fi

TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
OUTPUT_DIR="$SCRIPT_DIR/logs/run_$TIMESTAMP"
mkdir -p "$OUTPUT_DIR"

echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${CYAN}📥 KÉO LOGS CÁC PRIVATE CHAINS VỀ MÁY CỤC BỘ${NC}"
echo -e "   - Inventory:  $INVENTORY_FILE"
echo -e "   - Mục tiêu:   ${TARGET_CHAIN:-Tất cả chains}"
echo -e "   - Thư mục lưu: $OUTPUT_DIR"
echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"

# Trích xuất danh sách hosts từ inventory bằng Python
python3 -c "
import yaml, json, sys

try:
    with open('$INVENTORY_FILE') as f:
        inv = yaml.safe_load(f)
except Exception as e:
    print(f'Error reading inventory: {e}', file=sys.stderr)
    sys.exit(1)

all_vars = inv.get('all', {}).get('vars', {})
default_user = all_vars.get('ansible_user', 'root')
default_pass = all_vars.get('ansible_ssh_pass', all_vars.get('ansible_password', ''))
default_become_pass = all_vars.get('ansible_become_pass', all_vars.get('ansible_sudo_pass', default_pass))
default_conn = all_vars.get('ansible_connection', 'ssh')

hosts = inv.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
for name, h in hosts.items():
    if not isinstance(h, dict):
        continue
    cid = str(h.get('chain_id', ''))
    if not cid:
        continue
    
    target_c = '$TARGET_CHAIN'.strip()
    if target_c and target_c != 'all' and cid != target_c:
        continue

    host_ip = h.get('ansible_host', '127.0.0.1')
    user = h.get('ansible_user', default_user)
    conn = h.get('ansible_connection', default_conn)
    password = h.get('ansible_ssh_pass', h.get('ansible_password', default_pass))
    become_pass = h.get('ansible_become_pass', default_become_pass)
    key_file = h.get('ansible_ssh_private_key_file', '')
    inst_dir = h.get('install_dir', f'/opt/metanode/chain-{cid}')

    print(f'{name}|{cid}|{host_ip}|{user}|{conn}|{password}|{become_pass}|{key_file}|{inst_dir}')
" | while IFS='|' read -r host_name chain_id host_ip user conn password become_pass key_file inst_dir; do
    if [ -z "$chain_id" ]; then
        continue
    fi

    CHAIN_OUT_DIR="$OUTPUT_DIR/chain_${chain_id}_${host_ip}"
    mkdir -p "$CHAIN_OUT_DIR"

    echo -e "\n${GREEN}🌐 [Chain $chain_id] Host: $host_ip ($host_name)...${NC}"

    SERVICE_NAME="metanode-private-${chain_id}.service"
    JOURNAL_FILE="$CHAIN_OUT_DIR/systemd_${SERVICE_NAME}.log"

    if [ "$conn" == "local" ] || [ "$host_ip" == "127.0.0.1" ] || [ "$host_ip" == "localhost" ]; then
        # 1. Kéo toàn bộ file logs (cả logs/ chung và node-*/logs)
        shopt -s nullglob
        for ldir in "$inst_dir"/logs "$inst_dir"/node-*/logs; do
            if [ -d "$ldir" ]; then
                rel_name=$(echo "$ldir" | sed "s|^$inst_dir/||")
                target_sub="$CHAIN_OUT_DIR/$rel_name"
                mkdir -p "$(dirname "$target_sub")"
                cp -r "$ldir" "$target_sub"
                echo "   ✅ Đã sao chép thư mục file logs: $ldir"
            fi
        done

        # 2. Lấy log từ systemd journal (cho tất cả nodes)
        if command -v journalctl &>/dev/null; then
            for unit in $(systemctl list-unit-files "metanode-private-${chain_id}*.service" --no-legend 2>/dev/null | awk '{print $1}'); do
                j_out="$CHAIN_OUT_DIR/systemd_${unit}.log"
                journalctl -u "$unit" --no-pager -n "$LINE_COUNT" > "$j_out" 2>/dev/null || true
                echo "   ✅ Đã xuất systemd journal ($LINE_COUNT dòng) ra: $j_out"
            done
        fi
    else
        # Kết nối Remote SSH
        SSH_CMD="ssh -n -o StrictHostKeyChecking=no -o ConnectTimeout=8"
        SCP_CMD="scp -o StrictHostKeyChecking=no -o ConnectTimeout=8 -r"

        if [ -n "$key_file" ]; then
            SSH_CMD="$SSH_CMD -i $key_file"
            SCP_CMD="$SCP_CMD -i $key_file"
        fi

        if [ -n "$password" ] && command -v sshpass &>/dev/null; then
            SSH_CMD="sshpass -p '$password' $SSH_CMD"
            SCP_CMD="sshpass -p '$password' $SCP_CMD"
        fi

        # 1. Kéo thư mục logs từ xa
        eval $SCP_CMD "${user}@${host_ip}:${inst_dir}/logs" "$CHAIN_OUT_DIR/" </dev/null 2>/dev/null || true
        eval $SCP_CMD "${user}@${host_ip}:${inst_dir}/node-*" "$CHAIN_OUT_DIR/" </dev/null 2>/dev/null || true

        # 2. Lấy log systemd journal từ xa
        REMOTE_UNITS=$(eval $SSH_CMD "${user}@${host_ip}" "systemctl list-unit-files 'metanode-private-${chain_id}*.service' --no-legend 2>/dev/null | awk '{print \$1}'" </dev/null 2>/dev/null || echo "$SERVICE_NAME")
        for unit in $REMOTE_UNITS; do
            j_out="$CHAIN_OUT_DIR/systemd_${unit}.log"
            eval $SSH_CMD "${user}@${host_ip}" "journalctl -u '$unit' --no-pager -n $LINE_COUNT" </dev/null > "$j_out" 2>/dev/null || true
            echo "   ✅ Đã xuất remote systemd journal ra: $j_out"
        done
    fi
done

echo ""
echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}🎉 HOÀN TẤT KÉO LOGS!${NC}"
echo -e "📂 Toàn bộ logs đã được lưu tại:"
echo -e "👉 ${YELLOW}$OUTPUT_DIR${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
