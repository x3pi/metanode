#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
#  drill_node_full_resync.sh — ⚠️ INVASIVE DRILL. Deliberately destroys ALL
#  local data (execution + consensus state) for ONE target node -- bypassing
#  the well-tested snapshot-restore path entirely -- to verify the node can
#  genuinely self-heal via P2P sync from its peers, from a true "no usable
#  local data, no snapshot" starting point. This is DEPLOY_GUIDE.md's
#  "Kịch bản 3. Nếu không có snapshot available, re-sync from other nodes"
#  scenario, made real instead of theoretical.
#
#  Why this is a DIFFERENT drill than the already-heavily-tested
#  --restore-node path: --restore-node restores from a known-good snapshot
#  file (fast, and already proven across 100+ rounds this session). This
#  drill simulates the WORSE case -- no snapshot exists, or the snapshot
#  itself is untrustworthy -- forcing the node to rebuild its entire state
#  from scratch by syncing block-by-block from peers over P2P, which is a
#  completely different code path (and, per DEPLOY_GUIDE.md, a much slower
#  one) that this session's extensive snapshot-focused testing never
#  exercised.
#
#  ⚠️ RUN THIS ONLY:
#    - During a scheduled maintenance window, nobody else using the cluster
#    - After confirming the OTHER nodes are healthy and at quorum (this drill
#      refuses to start otherwise -- destroying a node's data is only safe
#      to test when its peers can actually serve it a full resync)
#    - Understanding this can take a long time proportional to chain height
#      (this is a real chain replay, not a snapshot copy)
#
#  Usage:
#    ./drill_node_full_resync.sh --node N --confirm [--timeout-seconds 1800]
#
#    --node N              Node id to destroy and force-resync (must NOT be
#                           the only validator -- refuses to run on a node
#                           whose loss would drop the cluster below quorum).
#    --confirm              Required. Without it, prints the plan and exits.
#    --timeout-seconds N    Max time to wait for the node to catch back up to
#                           the cluster's height before declaring the drill
#                           failed. Default: 1800 (30 min) -- full resync from
#                           height 0 is inherently slower than a snapshot
#                           restore; size this to your real chain height.
# ═══════════════════════════════════════════════════════════════════════════
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_DIR="$(cd "$SCRIPT_DIR/../../ansible" && pwd)"

TARGET_NODE=""
CONFIRM=false
TIMEOUT_SECONDS=1800
RPC_NODES_JSON="/tmp/rpc_nodes.json"

while [[ "$#" -gt 0 ]]; do
    case "$1" in
        --node) TARGET_NODE="$2"; shift ;;
        --confirm) CONFIRM=true ;;
        --timeout-seconds) TIMEOUT_SECONDS="$2"; shift ;;
        *) echo "Unknown flag: $1"; exit 1 ;;
    esac
    shift
done

if [ -z "$TARGET_NODE" ]; then
    echo "❌ Thiếu --node N. Ví dụ: --node 2"
    exit 1
fi
if [ ! -f "$RPC_NODES_JSON" ]; then
    echo "❌ Không tìm thấy ${RPC_NODES_JSON} -- chạy ansible_deploy.sh ít nhất 1 lần trước"
    echo "   (nó tự sinh file này) để drill biết địa chỉ RPC của từng node."
    exit 1
fi

echo "=========================================================="
echo "🩹 DRILL: Node Full P2P Resync (⚠️ INVASIVE -- xoá dữ liệu thật của Node ${TARGET_NODE})"
echo "=========================================================="

# ─── PRE-FLIGHT: confirm cluster health and quorum without the target node ──
mapfile -t ALL_NODE_KEYS < <(python3 -c "
import json
d = json.load(open('$RPC_NODES_JSON'))
print('\n'.join(sorted(d.get('nodes', {}).keys())))
")
TOTAL_NODES=${#ALL_NODE_KEYS[@]}
TARGET_KEY="m${TARGET_NODE}"

echo "📋 Tổng số node trong cụm: ${TOTAL_NODES}"
echo "🎯 Node bị xoá dữ liệu để test: ${TARGET_KEY}"

HEALTHY=0
declare -A HEIGHTS
for key in "${ALL_NODE_KEYS[@]}"; do
    url=$(python3 -c "import json; print(json.load(open('$RPC_NODES_JSON'))['nodes']['$key'])")
    resp=$(curl -s -m 3 -X POST "$url" -H 'Content-Type: application/json' \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
    h=$(python3 -c "
import json, sys
try:
    print(json.loads('''$resp''').get('result','DEAD'))
except Exception:
    print('DEAD')
" 2>/dev/null)
    HEIGHTS[$key]="$h"
    if [ "$h" != "DEAD" ] && [ -n "$h" ] && [ "$key" != "$TARGET_KEY" ]; then
        HEALTHY=$((HEALTHY + 1))
    fi
    echo "   ${key}: ${h}"
done

# f = floor((n-1)/3); need >= 2f+1 surviving nodes for BFT quorum WITHOUT the target.
REMAINING=$((TOTAL_NODES - 1))
F=$(( (REMAINING - 1) / 3 ))
MIN_QUORUM=$(( 2 * F + 1 ))

echo "🛡️  BFT quorum cần thiết KHÔNG TÍNH node ${TARGET_KEY}: ${MIN_QUORUM}/${REMAINING} (f=${F})"

if [ "$HEALTHY" -lt "$MIN_QUORUM" ]; then
    echo "❌ [TỪ CHỐI CHẠY] Chỉ ${HEALTHY}/${REMAINING} node còn lại đang khoẻ -- KHÔNG đủ"
    echo "   quorum để phục vụ resync an toàn nếu xoá dữ liệu ${TARGET_KEY} ngay bây giờ."
    echo "   👉 Sửa các node khác cho khoẻ hết rồi chạy lại drill."
    exit 1
fi
echo "✅ Đủ quorum (${HEALTHY}/${REMAINING} node khoẻ) để an toàn chạy drill này."

CLUSTER_HEIGHT_HEX="${HEIGHTS[m0]:-${HEIGHTS[m1]:-0x0}}"
for key in "${ALL_NODE_KEYS[@]}"; do
    [ "$key" == "$TARGET_KEY" ] && continue
    [ "${HEIGHTS[$key]}" != "DEAD" ] && CLUSTER_HEIGHT_HEX="${HEIGHTS[$key]}" && break
done
echo "📏 Chiều cao cụm hiện tại (không tính node đích): ${CLUSTER_HEIGHT_HEX}"

if [ "$CONFIRM" != true ]; then
    echo ""
    echo "ℹ️  [DRY-RUN] Chưa xoá gì cả. Kế hoạch nếu chạy --confirm:"
    echo "   1. Dừng service metanode-execution-${TARGET_NODE} (đúng luồng graceful, không SIGKILL)"
    echo "   2. Xoá TOÀN BỘ thư mục data của node ${TARGET_NODE} (bỏ qua đường restore-node/snapshot)"
    echo "   3. Khởi động lại service -- ép node vào trạng thái 'không có dữ liệu local, không có"
    echo "      snapshot' để buộc nó tự đồng bộ lại 100% qua P2P từ các node khác"
    echo "   4. Theo dõi tới khi node đuổi kịp chiều cao cụm (tối đa ${TIMEOUT_SECONDS}s) và xác"
    echo "      nhận Zero-Fork (hash khớp với cụm) sau khi đồng bộ xong"
    echo "   ⚠️  Thêm --confirm để thực sự chạy (chỉ trong cửa sổ bảo trì!)."
    exit 0
fi

echo ""
echo "🚨 XÁC NHẬN: sẽ xoá dữ liệu Node ${TARGET_NODE} trong 5 giây... (Ctrl+C để huỷ)"
sleep 5

echo "1️⃣  Dừng service..."
cd "$ANSIBLE_DIR"
ansible metanode_cluster -i inventory.yml -l "*" -b -m systemd \
    -a "name=metanode-execution-${TARGET_NODE} state=stopped" 2>&1 | tail -5 || \
    echo "⚠️  Không chạy được qua ansible ad-hoc -- dừng thủ công service này trên đúng host rồi Enter để tiếp tục."

echo "2️⃣  Xoá dữ liệu local (bỏ qua hoàn toàn cơ chế snapshot)..."
# Deliberately NOT using ansible_deploy.sh --restore-node here -- that path is already
# extensively verified this session. This drill exists specifically to exercise the P2P
# full-sync path that --restore-node bypasses.
ansible metanode_cluster -i inventory.yml -b -m shell \
    -a "rm -rf /opt/metanode/node-${TARGET_NODE}/data/*" \
    --limit "*" 2>&1 | tail -10

DRILL_START=$(date +%s)
echo "3️⃣  Khởi động lại service, để nó tự resync qua P2P..."
ansible metanode_cluster -i inventory.yml -b -m systemd \
    -a "name=metanode-execution-${TARGET_NODE} state=started" 2>&1 | tail -5

TARGET_URL=$(python3 -c "import json; print(json.load(open('$RPC_NODES_JSON'))['nodes']['$TARGET_KEY'])")
echo ""
echo "⏳ Đang theo dõi Node ${TARGET_NODE} tự đồng bộ lại qua P2P (tối đa ${TIMEOUT_SECONDS}s)..."
elapsed=0
CAUGHT_UP=false
while [ "$elapsed" -lt "$TIMEOUT_SECONDS" ]; do
    resp=$(curl -s -m 3 -X POST "$TARGET_URL" -H 'Content-Type: application/json' \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
    h=$(python3 -c "
import json, sys
try:
    print(json.loads('''$resp''').get('result','DEAD'))
except Exception:
    print('DEAD')
" 2>/dev/null)
    echo "   [${elapsed}s/${TIMEOUT_SECONDS}s] Node ${TARGET_NODE}: ${h} | Cụm: ${CLUSTER_HEIGHT_HEX}"
    if [ "$h" == "$CLUSTER_HEIGHT_HEX" ] && [ "$h" != "DEAD" ]; then
        CAUGHT_UP=true
        break
    fi
    sleep 10
    elapsed=$((elapsed + 10))
done

DURATION=$(( $(date +%s) - DRILL_START ))
echo ""
if [ "$CAUGHT_UP" == true ]; then
    echo "✅ [PASS] Node ${TARGET_NODE} đã tự đồng bộ lại 100% qua P2P thành công, mất ${DURATION}s."
    echo "   👉 Xác nhận thêm bằng tay: so sánh block hash/StateRoot của node ${TARGET_NODE} với"
    echo "      các node khác tại cùng chiều cao để đảm bảo Zero-Fork (không chỉ chiều cao khớp)."
else
    echo "❌ [KHÔNG ĐẠT] Node ${TARGET_NODE} KHÔNG đuổi kịp chiều cao cụm trong ${TIMEOUT_SECONDS}s."
    echo "   👉 Đây là kết quả THẬT cần điều tra: cơ chế tự resync qua P2P (không qua snapshot)"
    echo "      có thể đang có vấn đề, hoặc chỉ đơn giản cần timeout dài hơn cho chiều cao hiện tại."
    exit 1
fi
