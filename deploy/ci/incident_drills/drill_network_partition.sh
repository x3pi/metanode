#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
#  drill_network_partition.sh — ⚠️ INVASIVE DRILL. Deliberately blocks the
#  metanode P2P/consensus/RPC ports BETWEEN one target host and the rest of
#  the cluster for a bounded period, to verify: (a) the remaining nodes keep
#  making progress if they still hold quorum, (b) the partitioned node does
#  NOT fork or falsely commit while isolated (Zero-Fork Invariant --
#  AGENTS.md), and (c) once connectivity is restored, the partitioned node
#  catches back up cleanly with no fork.
#
#  Scoped to ONLY the metanode ports (via iptables rules on those specific
#  ports, not a blanket network-level block) so this does not disrupt any
#  OTHER traffic to/from the target host -- e.g. someone else's SSH session
#  or unrelated work on a shared machine keeps working during the drill.
#
#  ⚠️ RUN THIS ONLY:
#    - During a scheduled maintenance window, nobody else needing the
#      cluster's consensus to be live during the test
#    - You have sudo/iptables access on the target host and can verify the
#      rule was actually removed afterward (see cleanup safety notes below)
#
#  Safety design (defense in depth):
#    1. Pre-flight refuses to run if applying the partition would drop the
#       cluster below BFT quorum (i.e. refuses to partition more than f
#       nodes at once).
#    2. `trap ... EXIT INT TERM` removes the iptables rule on any exit path,
#       not just success.
#    3. An independent background watchdog on the TARGET HOST ITSELF (not
#       just this script's own process) removes the rule after
#       MAX_PARTITION_SECONDS regardless of whether this script is still
#       alive -- protects against the controlling SSH session itself
#       dropping mid-drill, which would otherwise strand a firewall rule
#       silently isolating a production node indefinitely.
#
#  Usage:
#    ./drill_network_partition.sh --node N --confirm [--duration-seconds 60]
#
#    --node N                Node id whose host gets isolated.
#    --confirm                 Required. Without it, prints the plan and exits.
#    --duration-seconds N     How long to hold the partition. Default: 60.
# ═══════════════════════════════════════════════════════════════════════════
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_DIR="$(cd "$SCRIPT_DIR/../../ansible" && pwd)"
RPC_NODES_JSON="/tmp/rpc_nodes.json"

TARGET_NODE=""
CONFIRM=false
DURATION_SECONDS=60
MAX_PARTITION_SECONDS=300   # hard ceiling for the on-host watchdog, regardless of --duration-seconds

while [[ "$#" -gt 0 ]]; do
    case "$1" in
        --node) TARGET_NODE="$2"; shift ;;
        --confirm) CONFIRM=true ;;
        --duration-seconds) DURATION_SECONDS="$2"; shift ;;
        *) echo "Unknown flag: $1"; exit 1 ;;
    esac
    shift
done

if [ -z "$TARGET_NODE" ]; then
    echo "❌ Thiếu --node N. Ví dụ: --node 1"
    exit 1
fi
if [ ! -f "$RPC_NODES_JSON" ]; then
    echo "❌ Không tìm thấy ${RPC_NODES_JSON} -- chạy ansible_deploy.sh ít nhất 1 lần trước."
    exit 1
fi

echo "=========================================================="
echo "🌐 DRILL: Network Partition (⚠️ INVASIVE -- chặn traffic thật)"
echo "=========================================================="

TARGET_IP=$(python3 -c "
import json, urllib.parse
d = json.load(open('$RPC_NODES_JSON'))
url = d.get('nodes', {}).get('m${TARGET_NODE}')
print(urllib.parse.urlparse(url).hostname if url else '')
")
if [ -z "$TARGET_IP" ]; then
    echo "❌ Không xác định được IP của node ${TARGET_NODE} từ ${RPC_NODES_JSON}."
    exit 1
fi

mapfile -t ALL_NODE_KEYS < <(python3 -c "
import json
print('\n'.join(sorted(json.load(open('$RPC_NODES_JSON')).get('nodes', {}).keys())))
")
TOTAL_NODES=${#ALL_NODE_KEYS[@]}
F=$(( (TOTAL_NODES - 1) / 3 ))

echo "📋 Tổng số node: ${TOTAL_NODES} (f=${F} -- chịu được tối đa ${F} node lỗi/bị cô lập cùng lúc)"
echo "🎯 Node bị cô lập: m${TARGET_NODE} (IP: ${TARGET_IP})"
echo "⏱️  Thời gian cô lập: ${DURATION_SECONDS}s (trần cứng watchdog: ${MAX_PARTITION_SECONDS}s)"

if [ "$F" -lt 1 ]; then
    echo "❌ [TỪ CHỐI CHẠY] Cụm chỉ ${TOTAL_NODES} node, f=${F} -- KHÔNG chịu được dù chỉ 1 node"
    echo "   bị cô lập. Drill này cần cụm >= 4 node (f>=1) để có ý nghĩa an toàn."
    exit 1
fi

# Metanode ports (see deploy/ansible/DEPLOY_GUIDE.md port table / setup-cluster-btrfs.sh
# open_ports.sh): Execution RPC, Execution P2P, Meta RPC, Consensus P2P, Consensus Peer RPC,
# Consensus Metrics, Snapshot Server -- block the P2P/consensus/RPC range, not SSH (22) or
# anything else, so unrelated use of the host is unaffected.
PORT_RANGE="6200:6299,9100:9299,10100:10199,10746:10799,19200:19299,8600:8699"

if [ "$CONFIRM" != true ]; then
    echo ""
    echo "ℹ️  [DRY-RUN] Chưa chặn gì cả. Kế hoạch nếu chạy --confirm:"
    echo "   1. Trên host của node ${TARGET_NODE} VÀ trên các host khác: chặn traffic 2 chiều"
    echo "      tới/từ ${TARGET_IP} chỉ trên các port metanode (${PORT_RANGE}) -- SSH và các"
    echo "      dịch vụ khác trên host vẫn hoạt động bình thường."
    echo "   2. Giữ trong ${DURATION_SECONDS}s, theo dõi: cụm còn lại có tiếp tục tăng block"
    echo "      không, node bị cô lập có KHÔNG tự ý commit gì không (Zero-Fork Invariant)."
    echo "   3. Gỡ chặn, theo dõi node bị cô lập tự đồng bộ lại, xác nhận Zero-Fork sau khi hợp nhất."
    echo "   ⚠️  Thêm --confirm để thực sự chạy (chỉ trong cửa sổ bảo trì!)."
    exit 0
fi

echo ""
echo "🚨 XÁC NHẬN: sẽ chặn mạng của node ${TARGET_NODE} trong 5 giây... (Ctrl+C để huỷ)"
sleep 5

cd "$ANSIBLE_DIR"

apply_partition() {
    # Applied on EVERY host: each host blocks traffic to/from the target IP on metanode
    # ports. Also applied ON the target host itself (blocking traffic to every OTHER
    # cluster IP would need each peer IP -- simpler and equally effective to just block
    # the target's own outbound/inbound on these ports host-wide, which is scoped to this
    # host's involvement in the drill).
    ansible metanode_cluster -i inventory.yml -b -m shell -a "
        iptables -I INPUT -s ${TARGET_IP} -p tcp -m multiport --dports ${PORT_RANGE} -j DROP -m comment --comment DRILL_NETWORK_PARTITION;
        iptables -I OUTPUT -d ${TARGET_IP} -p tcp -m multiport --dports ${PORT_RANGE} -j DROP -m comment --comment DRILL_NETWORK_PARTITION;
        ( sleep ${MAX_PARTITION_SECONDS}; iptables -D INPUT -s ${TARGET_IP} -p tcp -m multiport --dports ${PORT_RANGE} -j DROP -m comment --comment DRILL_NETWORK_PARTITION 2>/dev/null; iptables -D OUTPUT -d ${TARGET_IP} -p tcp -m multiport --dports ${PORT_RANGE} -j DROP -m comment --comment DRILL_NETWORK_PARTITION 2>/dev/null ) >/dev/null 2>&1 & disown
    " 2>&1 | tail -10
}

remove_partition() {
    echo "🧹 [CLEANUP] Đang gỡ luật chặn trên toàn cụm..."
    ansible metanode_cluster -i inventory.yml -b -m shell -a "
        iptables -D INPUT -s ${TARGET_IP} -p tcp -m multiport --dports ${PORT_RANGE} -j DROP -m comment --comment DRILL_NETWORK_PARTITION 2>/dev/null || true;
        iptables -D OUTPUT -d ${TARGET_IP} -p tcp -m multiport --dports ${PORT_RANGE} -j DROP -m comment --comment DRILL_NETWORK_PARTITION 2>/dev/null || true
    " 2>&1 | tail -10
    echo "✅ [CLEANUP] Xong. Xác nhận thủ công: 'sudo iptables -L -n | grep DRILL_NETWORK_PARTITION'"
    echo "   trên TỪNG host phải KHÔNG còn dòng nào (watchdog trên host cũng tự gỡ sau"
    echo "   ${MAX_PARTITION_SECONDS}s nếu bước cleanup này vì lý do gì đó không chạy được)."
}
trap remove_partition EXIT INT TERM

echo "1️⃣  Áp luật chặn (chỉ port metanode, không đụng SSH/dịch vụ khác)..."
apply_partition

echo "2️⃣  Đang giữ cô lập trong ${DURATION_SECONDS}s -- theo dõi cụm còn lại..."
elapsed=0
while [ "$elapsed" -lt "$DURATION_SECONDS" ]; do
    line="   [${elapsed}s/${DURATION_SECONDS}s]"
    for key in "${ALL_NODE_KEYS[@]}"; do
        url=$(python3 -c "import json; print(json.load(open('$RPC_NODES_JSON'))['nodes']['$key'])")
        h=$(curl -s -m 2 -X POST "$url" -H 'Content-Type: application/json' \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null | \
            python3 -c "import json,sys
try:
    print(json.load(sys.stdin).get('result','DEAD'))
except Exception:
    print('DEAD')" 2>/dev/null)
        line="$line $key=$h"
    done
    echo "$line"
    sleep 5
    elapsed=$((elapsed + 5))
done

echo "3️⃣  Gỡ chặn (trap sẽ chạy remove_partition ngay khi script kết thúc)..."
echo ""
echo "✅ [HOÀN TẤT] Xem log chiều cao ở trên: cụm còn lại (>= 2f+1 node) phải VẪN tăng block"
echo "   bình thường trong lúc node ${TARGET_NODE} bị cô lập, và node ${TARGET_NODE} phải KHÔNG"
echo "   tự báo tăng block nào (đứng yên) trong lúc bị cô lập -- nếu nó vẫn tăng, đó là dấu"
echo "   hiệu VI PHẠM Zero-Fork Invariant, cần điều tra ngay."
echo "   👉 Sau khi script thoát (cleanup xong), theo dõi thêm: node ${TARGET_NODE} có tự bắt"
echo "      kịp chiều cao cụm không, và Block Hash/StateRoot có khớp cụm không (Zero-Fork)."
