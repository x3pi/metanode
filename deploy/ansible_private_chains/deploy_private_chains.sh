#!/usr/bin/env bash
# deploy_private_chains.sh — Automated Ansible Deployment for Metanode Private Chains
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INVENTORY="$SCRIPT_DIR/inventory.yml"
PLAYBOOK="$SCRIPT_DIR/deploy.yml"

echo "═══════════════════════════════════════════════════════════════"
echo "🌐 METANODE PRIVATE CHAINS — ANSIBLE DEPLOYMENT MANAGER"
echo "═══════════════════════════════════════════════════════════════"

ACTION=""
TARGET_CHAIN="all"
OPEN_PORTS="false"
REGISTER=0
START_RELAYER=0

for arg in "$@"; do
    case $arg in
        --setup)
            ACTION="setup"
            ;;
        --clean-data|--clean)
            ACTION="clean_data"
            ;;
        --reset-all|--reset)
            ACTION="reset"
            REGISTER=1
            ;;
        --start)
            ACTION="start"
            ;;
        --restart)
            ACTION="restart"
            ;;
        --stop)
            ACTION="stop"
            ;;
        --status)
            ACTION="status"
            ;;
        --open-ports)
            OPEN_PORTS="true"
            ;;
        --chain=*|-c=*)
            TARGET_CHAIN="${arg#*=}"
            ;;
        --register)
            REGISTER=1
            ;;
        --inventory=*)
            INVENTORY="${arg#*=}"
            ;;
        --help|-h)
            echo "Usage: ./deploy_private_chains.sh [OPTIONS]"
            echo ""
            echo "🎯 Mục tiêu (Target):"
            echo "  --chain=ID, -c=ID   Chỉ áp dụng lệnh cho 1 Chain cụ thể (ví dụ: --chain=101). Mặc định: all"
            echo ""
            echo "⚡ Các Hành Động (Actions):"
            echo "  --setup             (Mặc định) Khởi tạo cấu hình, copy binary & bật các Private Chains"
            echo "  --start             Khởi động service của các Private Chain được chọn"
            echo "  --restart           Khởi động lại service của các Private Chain được chọn"
            echo "  --stop              Dừng service của các Private Chain được chọn (hoặc dừng toàn bộ)"
            echo "  --clean-data        Xóa trắng database & logs (giữ nguyên keys, config & genesis)"
            echo "  --reset-all         Xóa toàn bộ, sinh mới genesis & keys và chạy lại từ block 0"
            echo "  --status            Kiểm tra trạng thái & eth_blockNumber của các Private Chains"
            echo ""
            echo "🛡️ Mạng & Bảo Mật (Network):"
            echo "  --open-ports        Tự động mở các cổng tường lửa (UFW) cho RPC, Peer, Consensus"
            echo ""
            echo "🌉 Gateway:"
            echo "  --register          Đăng ký toàn bộ danh bạ các Private Chains lên Gateway (Root Anchor)"
            echo "  --inventory=F       Đường dẫn file inventory tùy chỉnh (mặc định: inventory.yml)"
            echo ""
            echo "💡 Ví dụ:"
            echo "  ./deploy_private_chains.sh --stop --chain=101       # Dừng chỉ riêng Chain 101"
            echo "  ./deploy_private_chains.sh --stop                  # Dừng tất cả các Private Chains"
            echo "  ./deploy_private_chains.sh --start --chain=101      # Khởi động lại riêng Chain 101"
            echo "  ./deploy_private_chains.sh --clean-data --chain=102 # Xóa database riêng Chain 102"
            echo "  ./deploy_private_chains.sh --setup --open-ports    # Setup và mở port tường lửa"
            exit 0
            ;;
    esac
done

if [ -z "$ACTION" ]; then
    if [ "$REGISTER" -eq 1 ]; then
        ACTION="none"
    else
        ACTION="setup"
    fi
fi

if [ ! -f "$INVENTORY" ]; then
    echo "❌ ERROR: Inventory file not found: $INVENTORY"
    exit 1
fi

echo "📋 Cấu hình thực thi:"
echo "   - Action:       $ACTION"
echo "   - Target Chain: $TARGET_CHAIN"
echo "   - Open Ports:   $OPEN_PORTS"
echo "   - Inventory:    $INVENTORY"
echo ""

# Kiểm tra tính hợp lệ của inventory trước khi thực thi
python3 -c "
import yaml, sys, subprocess

local_ips = {'127.0.0.1', 'localhost', '::1'}
try:
    out = subprocess.check_output(['hostname', '-I'], text=True).strip()
    for ip in out.split():
        local_ips.add(ip.strip())
except Exception:
    pass

with open('$INVENTORY') as f:
    data = yaml.safe_load(f)

global_vars = data.get('all', {}).get('vars', {}) or {}
global_conn = str(global_vars.get('ansible_connection', '')).strip().lower()

hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {}) or {}
seen_chains = {}
for host_key, hvars in hosts.items():
    if not isinstance(hvars, dict):
        continue
    ip = str(hvars.get('ansible_host', host_key)).strip()
    ansible_conn = str(hvars.get('ansible_connection', global_conn)).strip().lower()
    cid = hvars.get('chain_id')

    if ansible_conn == 'local' and ip not in local_ips:
        print(f'\n\033[0;31m❌ [LỖI CẤU HÌNH INVENTORY] Host \'{host_key}\' ({ip}) bị gán \'ansible_connection: local\' nhưng KHÔNG PHẢI máy local!\033[0m', file=sys.stderr)
        if global_conn == 'local' and 'ansible_connection' not in hvars:
            print(f'\033[0;33m   ⚠️ NGUYÊN NHÂN: Bạn đang để \'ansible_connection: local\' trong phần global \'all.vars\', khiến tất cả các host (kể cả máy remote {ip}) đều bị chạy trên máy local hiện tại!\033[0m', file=sys.stderr)
            print(f'\033[0;36m   👉 KHẮC PHỤC: Hãy XÓA \'ansible_connection: local\' khỏi \'all.vars\'. Chỉ đặt \'ansible_connection: local\' cho riêng từng host là máy local ({ \", \".join(sorted(local_ips)) }).\033[0m\n', file=sys.stderr)
        else:
            print(f'\033[0;36m   👉 KHẮC PHỤC: Hãy XÓA dòng \'ansible_connection: local\' trong host \'{host_key}\' để Ansible kết nối qua SSH tới {ip}.\033[0m\n', file=sys.stderr)
        sys.exit(1)

    if cid is not None:
        if cid in seen_chains:
            prev_host = seen_chains[cid]
            print(f'\n\033[0;31m❌ [LỖI CẤU HÌNH INVENTORY] Trùng lặp chain_id [{cid}]!\033[0m', file=sys.stderr)
            print(f'\033[0;33m   - Chain {cid} được khai báo ở cả \'{host_key}\' và \'{prev_host}\'.\033[0m', file=sys.stderr)
            sys.exit(1)
        seen_chains[cid] = host_key

target_chain_str = '$TARGET_CHAIN'
if target_chain_str != 'all':
    try:
        t_cid = int(target_chain_str)
        if t_cid not in seen_chains:
            print(f'\n\033[0;31m❌ [LỖI] Chain ID [{t_cid}] KHÔNG TỒN TẠI hoặc đang bị comment (#) trong file $INVENTORY!\033[0m', file=sys.stderr)
            print(f'\033[0;33m   👉 Các Chain ID hiện có sẵn: {sorted(list(seen_chains.keys()))}\033[0m', file=sys.stderr)
            print(f'\033[0;36m   👉 Hãy mở comment hoặc khai báo thêm chain_id: {t_cid} vào $INVENTORY trước khi chạy.\033[0m\n', file=sys.stderr)
            sys.exit(1)
    except ValueError:
        pass
"

# Chạy Ansible Playbook
if [ "$ACTION" != "none" ]; then
    echo "🚀 Đang thực thi Ansible Playbook ..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK" \
        -e "deploy_action=$ACTION" \
        -e "target_chain=$TARGET_CHAIN" \
        -e "open_ports=$OPEN_PORTS"
fi

# Đăng ký Gateway nếu được yêu cầu
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
c_ids = [str(h['chain_id']) for h in hosts.values() if 'chain_id' in h]
print(','.join(c_ids))
")

    TARGET_RPCS=$(python3 -c "
import yaml
with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
rpcs = [f\"{h['chain_id']}=http://{h.get('ansible_host', '127.0.0.1')}:{h.get('rpc_port', 8546)}\" for h in hosts.values() if 'chain_id' in h]
print(','.join(rpcs))
")

    # Ceremony/devnet supply values -- see register_chains -fund-genesis's own flag docs for why
    # these are config, not a hardcoded protocol constant (PR #84 review, C7 fix). Overridable via
    # inventory.yml's root_anchor_genesis_supply/root_anchor_per_chain_allocation; falls back to a
    # devnet-only default (400,000,000 tokens total genesis supply, split evenly across the
    # founding chains) so the guide's own end-to-end flow (bootstrap -> fund -> transfer test)
    # works out of the box without requiring a manual extra step.
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
    if [ "$TARGET_CHAIN" != "all" ]; then
        echo ""
        echo "   ℹ️  Mục tiêu: Đăng ký Chain $TARGET_CHAIN mới."
        echo "   ℹ️  Các chain cũ ($CHAINS_LIST) sẽ được kiểm tra và tự động bỏ qua (already registered) để đồng bộ danh bạ chéo."
    fi
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
    if [ -z "$SUBMITTER_KEY" ]; then
        echo "❌ Lỗi: root_anchor_submitter_key chưa được khai báo trong inventory.yml"
        exit 1
    fi

    cd "$SCRIPT_DIR/../../execution"
    go build -o register_chains ./cmd/tool/register_chains
    ./register_chains \
        --key "$SUBMITTER_KEY" \
        --root-anchor "$ROOT_ANCHOR_RPC" \
        --chains "$CHAINS_LIST" \
        --chains-dir "$SCRIPT_DIR/data" \
        --target-rpcs "$TARGET_RPCS" \
        --fund-genesis \
        --genesis-supply "$GENESIS_SUPPLY" \
        --per-chain-allocation "$PER_CHAIN_ALLOCATION"
fi

echo ""
echo "🎉 Hoàn tất thao tác thành công!"
