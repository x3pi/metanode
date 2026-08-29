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
        # 1. Kéo thư mục logs/
        LOCAL_LOGS_DIR="$inst_dir/logs"
        if [ -d "$LOCAL_LOGS_DIR" ]; then
            cp -r "$LOCAL_LOGS_DIR" "$CHAIN_OUT_DIR/"
            echo "   ✅ Đã sao chép thư mục file logs: $LOCAL_LOGS_DIR"
        else
            echo "   ℹ️  Không tìm thấy thư mục file logs tại: $LOCAL_LOGS_DIR"
        fi

        # 2. Lấy log từ systemd journal
        if command -v journalctl &>/dev/null; then
            journalctl -u "$SERVICE_NAME" --no-pager -n "$LINE_COUNT" > "$JOURNAL_FILE" 2>/dev/null || true
            echo "   ✅ Đã xuất systemd journal ($LINE_COUNT dòng) ra: $JOURNAL_FILE"
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

        # 1. Kéo thư mục logs/ từ xa
        eval $SCP_CMD "${user}@${host_ip}:${inst_dir}/logs" "$CHAIN_OUT_DIR/" </dev/null 2>/dev/null && \
            echo "   ✅ Đã tải thư mục logs từ ${host_ip}:${inst_dir}/logs" || \
            echo "   ⚠️  Không thể scp logs từ ${host_ip}:${inst_dir}/logs"

        # 2. Lấy systemd journal qua ssh (tự động dùng sudo với become_pass nếu cần)
        if [ -n "$become_pass" ]; then
            REMOTE_JOURNAL_CMD="echo '$become_pass' | sudo -S journalctl -u $SERVICE_NAME --no-pager -n $LINE_COUNT 2>/dev/null || journalctl -u $SERVICE_NAME --no-pager -n $LINE_COUNT 2>/dev/null"
        else
            REMOTE_JOURNAL_CMD="journalctl -u $SERVICE_NAME --no-pager -n $LINE_COUNT 2>/dev/null"
        fi

        eval $SSH_CMD "${user}@${host_ip}" "\"$REMOTE_JOURNAL_CMD\"" </dev/null > "$JOURNAL_FILE" 2>/dev/null && \
            echo "   ✅ Đã lấy systemd journal ($LINE_COUNT dòng) từ ${host_ip}" || \
            echo "   ⚠️  Không thể lấy journalctl từ ${host_ip}"
    fi
done

echo -e "\n${CYAN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}🎉 HOÀN TẤT KÉO LOGS!${NC}"
echo -e "📂 Toàn bộ logs đã được lưu tại:"
echo -e "👉 ${YELLOW}$OUTPUT_DIR${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
