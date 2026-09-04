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
DETERMINISTIC_GENESIS=0

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
        --deterministic-genesis)
            DETERMINISTIC_GENESIS=1
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
            echo "  --deterministic-genesis"
            echo "                      (2026-09-04) Genesis native-coin của private chain = ĐÚNG số tiền cọc thật"
            echo "                      đã được Root Anchor cấp khi đăng ký (không còn tự bịa alloc). Tự bật --register."
            echo "                      Tách deploy thành: sinh key cục bộ -> đăng ký lên Root Anchor -> sinh lại genesis"
            echo "                      đúng số thật -> publish+verify digest -> MỚI đẩy lên node thật và khởi động."
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
# Chế độ --deterministic-genesis (2026-09-04): tách làm 2 lần chạy ansible-playbook thay vì 1,
# xen giữa là bước đăng ký+sinh lại genesis đúng số thật (khối "Đăng ký Gateway" bên dưới) --
# KHÔNG được đẩy genesis lên node thật / khởi động node trước khi biết số tiền cọc thật đã đăng
# ký, nếu không node sẽ chạy với genesis sai (tự bịa số), không thể sửa lại sau khi đã có block 0.
#   Pha 1 (--limit localhost): chỉ chạy đúng Play "Local build and config generation" trong
#   deploy.yml (sinh key + file cấu hình cục bộ tại $SCRIPT_DIR/data/chain_<id>/) -- Play "Deploy
#   and Manage Private Chains across Nodes" nhắm vào group private_chains nên tự động bị bỏ qua
#   hoàn toàn khi limit=localhost, không cần sửa gì trong deploy.yml.
#   Pha 2 (sau khi đăng ký+sinh lại genesis xong, full playbook không giới hạn): chạy lại y hệt --
#   Play 1 tự bỏ qua gen_single_chain.py vì node-0/config.json đã tồn tại (idempotent sẵn), Play 2
#   mới thật sự copy genesis ĐÃ ĐÚNG lên node và khởi động.
if [ "$ACTION" != "none" ]; then
    if [ "$DETERMINISTIC_GENESIS" -eq 1 ] && { [ "$ACTION" = "setup" ] || [ "$ACTION" = "reset" ]; }; then
        echo "🚀 [Pha 1/2 -- deterministic-genesis] Sinh key + cấu hình cục bộ (CHƯA đẩy lên node/khởi động) ..."
        ansible-playbook -i "$INVENTORY" "$PLAYBOOK" \
            -e "deploy_action=$ACTION" \
            -e "target_chain=$TARGET_CHAIN" \
            -e "open_ports=$OPEN_PORTS" \
            --limit localhost
    else
        echo "🚀 Đang thực thi Ansible Playbook ..."
        ansible-playbook -i "$INVENTORY" "$PLAYBOOK" \
            -e "deploy_action=$ACTION" \
            -e "target_chain=$TARGET_CHAIN" \
            -e "open_ports=$OPEN_PORTS"
    fi
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

    # Xuất file json cấu hình tường minh đăng ký Gateway & cấu hình mạng Relayer
    python3 -c "
import os, yaml, json

with open('$INVENTORY') as f:
    data = yaml.safe_load(f)

global_vars = data.get('all', {}).get('vars', {}) or {}
root_rpc = global_vars.get('root_anchor_rpc', 'http://127.0.0.1:10746')
submitter_key = global_vars.get('root_anchor_submitter_key', '')
# relayer_key (2026-09-04): the relayer daemon's OWN signing key, deliberately SEPARATE from
# submitter_key -- see cross_chain_relayer/main.go's devnetDefaultRelayerKeyHex doc comment for
# why sharing one account between register_chains and the relayer daemon is a real nonce-collision
# hazard (found live). Override via inventory.yml's relayer_key if you need a specific identity;
# left empty here falls through to that same devnet-only default in the Go tool itself.
relayer_key = global_vars.get('relayer_key', '')
gen_supply = str(global_vars.get('genesis_supply_to_mint', '$GENESIS_SUPPLY'))
per_chain = str(global_vars.get('per_chain_allocation', '$PER_CHAIN_ALLOCATION'))

hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {}) or {}

chains_config = []
out_simple = {
    'root_anchor': root_rpc,
    'nodes': {},
    'tcp_nodes': {},
    'chain_nodes': {}
}

for host_key, h in sorted(hosts.items()):
    if not isinstance(h, dict) or 'chain_id' not in h:
        continue
    cid = int(h['chain_id'])
    cid_str = str(cid)
    ip = h.get('ansible_host', '127.0.0.1')
    rpc_port = int(h.get('rpc_port', 8546))
    p_offset = int(h.get('port_offset', 10))
    num_vals = int(h.get('validators', 1))

    out_simple['nodes'][cid_str] = f'http://{ip}:{rpc_port}'
    out_simple['tcp_nodes'][cid_str] = f'{ip}:{4200 + p_offset}'

    c_rpc_nodes = {}
    c_tcp_nodes = {}
    validators_list = []

    for v in range(num_vals):
        c_rpc_nodes[f'm{v}'] = f'http://{ip}:{rpc_port + v}'
        c_tcp_nodes[f'm{v}'] = f'{ip}:{4200 + p_offset + v}'

        # Đọc validator BLS private key từ thư mục data cục bộ
        bls_priv = ''
        cfg_candidates = [
            os.path.join('$SCRIPT_DIR', 'data', f'chain_{cid}', f'node-{v}', 'config.json'),
            os.path.join('$SCRIPT_DIR', 'data', f'chain_{cid}', f'node-{v}', 'config', 'execution.json'),
            os.path.join('/opt/metanode', f'chain-{cid}', f'node-{v}', 'config.json'),
        ]
        for cfg_p in cfg_candidates:
            if os.path.exists(cfg_p):
                try:
                    with open(cfg_p) as cf:
                        cd = json.load(cf)
                        bls_priv = cd.get('Databases', {}).get('BLSPrivateKey', '')
                        if bls_priv:
                            break
                except Exception:
                    pass

        validators_list.append({
            'name': f'node-{v}',
            'node_id': v,
            'bls_private_key': bls_priv,
            'stake': 1000
        })

    out_simple['chain_nodes'][cid_str] = {
        'validators': num_vals,
        'rpc_url': f'http://{ip}:{rpc_port}',
        'rpc_nodes': c_rpc_nodes,
        'tcp_nodes': c_tcp_nodes
    }

    chains_config.append({
        'chain_id': cid,
        'rpc_url': f'http://{ip}:{rpc_port}',
        'quorum_threshold': 6667,
        'validators': validators_list
    })

gateway_register_data = {
    'root_anchor_rpc': root_rpc,
    'submitter_key': submitter_key,
    'relayer_key': relayer_key,
    'genesis_supply': gen_supply,
    'per_chain_allocation': per_chain,
    'fund_genesis': True,
    'chains': chains_config
}

with open('$SCRIPT_DIR/gateway_register.json', 'w') as f:
    json.dump(gateway_register_data, f, indent=2)
print('📄 Đã xuất cấu hình Gateway & Relayer ra: $SCRIPT_DIR/gateway_register.json')

with open('/tmp/private_chains.json', 'w') as f:
    json.dump(out_simple, f, indent=2)
print('📄 Đã xuất cấu hình mạng ra: /tmp/private_chains.json')
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
    ./register_chains --config "$SCRIPT_DIR/gateway_register.json"

    # ─── Deterministic-genesis (2026-09-04): sinh lại genesis đúng số tiền THẬT đã đăng ký ───
    # Tại đây mỗi chain đã có PerChainAllocation thật trên Root Anchor (RegisterChainViaStake ->
    # TransferAllocation từ pool Reserve, xem note/eurozone_unified_native_coin_plan.md mục 5) --
    # đọc lại đúng số đó (không tự bịa), sinh lại genesis.json cục bộ (key giữ nguyên nhờ
    # gen_single_chain.py đã idempotent), rồi publish+verify digest TRƯỚC KHI Pha 2 (bên dưới) đẩy
    # nó lên node thật. Dừng cứng (set -e) nếu bất kỳ bước nào lệch -- không đẩy genesis sai lên
    # node thật.
    if [ "$DETERMINISTIC_GENESIS" -eq 1 ]; then
        echo ""
        echo "═══════════════════════════════════════════════════════════════"
        echo "🔒 SINH LẠI GENESIS THEO ĐÚNG SỐ TIỀN ĐÃ ĐĂNG KÝ TRÊN ROOT ANCHOR"
        echo "═══════════════════════════════════════════════════════════════"

        python3 -c "
import subprocess, sys, yaml

with open('$INVENTORY') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {}) or {}
target = '$TARGET_CHAIN'
root_rpc = '$ROOT_ANCHOR_RPC'
submitter_key = '$SUBMITTER_KEY'
register_chains_bin = '$SCRIPT_DIR/../../execution/register_chains'
gen_script = '$SCRIPT_DIR/../systemd/gen_single_chain.py'

def run(cmd, capture=False):
    print('   $ ' + ' '.join(cmd))
    if capture:
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode != 0:
            print(r.stdout, file=sys.stderr); print(r.stderr, file=sys.stderr)
            sys.exit(1)
        return r.stdout.strip()
    else:
        r = subprocess.run(cmd)
        if r.returncode != 0:
            sys.exit(1)

for host_key, h in sorted(hosts.items()):
    if not isinstance(h, dict) or 'chain_id' not in h:
        continue
    cid = h['chain_id']
    if target != 'all' and str(target) != str(cid):
        continue

    print(f'\n➡️  Chain {cid} ({host_key}):')
    amount = run([register_chains_bin, '-action', 'query-alloc-raw', '-root-anchor', root_rpc, '-chains', str(cid)], capture=True)
    wallet = run([register_chains_bin, '-action', 'query-genesis-wallet-raw', '-root-anchor', root_rpc, '-chains', str(cid)], capture=True)
    if amount in ('', '0') or wallet.lower() in ('', '0x0000000000000000000000000000000000000000'):
        print(f'❌ Chain {cid}: chưa có allocation thật trên Root Anchor (amount={amount!r}, wallet={wallet!r}).')
        print('   Kiểm tra: cross_chain.min_native_stake_to_register_wei đã cấu hình trên Root Anchor chưa,')
        print('   và ví submitter có đủ số dư thật để trả cọc không (xem log registerChainViaStake ở trên).')
        sys.exit(1)
    print(f'   ✅ Root Anchor xác nhận: allocation={amount} wei, genesis_wallet={wallet}')

    ip = h.get('ansible_host', '127.0.0.1')
    rpc_port = h.get('rpc_port', 8546)
    port_offset = h.get('port_offset', 0)
    validators = h.get('validators', 1)
    submitter = h.get('node_submitter_key', submitter_key)
    local_out = f'$SCRIPT_DIR/data/chain_{cid}'

    reserve_chain_id_hex = run(['curl', '-s', '-X', 'POST', root_rpc,
                                 '-H', 'Content-Type: application/json',
                                 '-d', '{\"jsonrpc\":\"2.0\",\"method\":\"eth_chainId\",\"params\":[],\"id\":1}'], capture=True)
    import json as _json
    reserve_chain_id = int(_json.loads(reserve_chain_id_hex).get('result', '0x0'), 16)

    run(['python3', gen_script,
         '--chain-id', str(cid), '--ip', str(ip), '--rpc-port', str(rpc_port),
         '--port-offset', str(port_offset), '--validators', str(validators),
         '--root-anchor-rpc', root_rpc, '--root-anchor-submitter-key', submitter,
         '--reserve-chain-id', str(reserve_chain_id), '--output-dir', local_out,
         '--dev-keys-file', '$SCRIPT_DIR/private_dev_keys.json',
         '--initial-supply-wallet', wallet, '--initial-supply', amount])

    genesis_file = f'{local_out}/genesis.json'
    run([register_chains_bin, '-action', 'publish-genesis-digest', '-root-anchor', root_rpc,
         '-chains', str(cid), '-genesis-file', genesis_file, '-key', submitter_key])
    run([register_chains_bin, '-action', 'verify-genesis', '-root-anchor', root_rpc,
         '-chains', str(cid), '-genesis-file', genesis_file])
    print(f'   ✅ Chain {cid}: genesis đã xác minh khớp với Root Anchor -- an toàn để đẩy lên node thật.')
"
    fi
fi

# Pha 2 của --deterministic-genesis: giờ mới thật sự đẩy genesis (đã đúng) lên node và khởi động.
#
# CỐ Ý dùng deploy_action=setup ở đây, KHÔNG dùng lại $ACTION gốc (dù người dùng gọi --reset-all):
# Play 1 trong deploy.yml có bước "rm -rf $LOCAL_OUT" khi ansible_action=='reset' -- nếu Pha 2
# cũng truyền "reset", Play 1 sẽ chạy lại lần nữa (dù đã --limit localhost ở Pha 1, playbook đầy
# đủ ở Pha 2 KHÔNG giới hạn host nên Play 1 vẫn chạy) và XOÁ SẠCH genesis.json/keys vừa đăng ký +
# sinh lại đúng số ở trên -- sinh committee MỚI ngẫu nhiên, không khớp với cái vừa đăng ký trên
# Root Anchor. "setup" không có bước rm -rf này, và vẫn kích hoạt đúng toàn bộ khối "Deploy
# directories and files" (chạy cho cả setup lẫn reset như nhau) nên vẫn đẩy đủ genesis/config/keys
# lên node thật và khởi động bình thường.
#
# Đánh đổi đã biết: nếu chain này TỪNG deploy trước đó (dữ liệu cũ còn trên node thật) và người
# dùng gọi --reset-all --deterministic-genesis muốn xoá sạch dữ liệu cũ trên node thật, bước xoá
# đó sẽ KHÔNG tự động chạy nữa (thuộc "Clean entire installation directory on reset" trong Play 2,
# chỉ chạy khi ansible_action=='reset'). Trường hợp này cần tự xoá thủ công
# /opt/metanode/chain-<id> trên node thật trước khi chạy lại, hoặc chạy --reset-all thường (không
# kèm --deterministic-genesis) 1 lần trước để dọn sạch, rồi mới bật --deterministic-genesis.
if [ "$DETERMINISTIC_GENESIS" -eq 1 ] && { [ "$ACTION" = "setup" ] || [ "$ACTION" = "reset" ]; }; then
    echo ""
    echo "🚀 [Pha 2/2 -- deterministic-genesis] Đẩy genesis đã xác minh lên node thật và khởi động ..."
    ansible-playbook -i "$INVENTORY" "$PLAYBOOK" \
        -e "deploy_action=setup" \
        -e "target_chain=$TARGET_CHAIN" \
        -e "open_ports=$OPEN_PORTS"
fi

echo ""
echo "🎉 Hoàn tất thao tác thành công!"
