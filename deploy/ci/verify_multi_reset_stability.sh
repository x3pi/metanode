#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  verify_multi_reset_stability.sh — Regression test for the concurrent
#  multi-node-per-host --reset-all race condition.
#
#  Background: a host running multiple co-located node processes (e.g. this
#  cluster's 192.168.1.230, hosting node-1/node-2/node-4 at once) exposed a
#  class of race conditions that a single --reset-all run only tripped
#  sometimes: a fixed-duration shutdown wait in stop_services letting a still-
#  flushing process write stale data into a freshly-recreated data dir, and
#  root-owned leftovers surviving a non-recursive chown. Both are now fixed
#  (data-driven shutdown wait + recursive chown + mount-hygiene checks), but a
#  single test run passing once doesn't prove a race is gone -- it proves the
#  race didn't fire THIS time. This script runs --reset-all back to back N
#  times and requires every node to come up clean and answer RPC every single
#  round, so a reintroduced race gets caught by repetition instead of luck.
#
#  Usage: ./verify_multi_reset_stability.sh [ROUNDS] [HEIGHT_WAIT_SEC]
#    ROUNDS           Number of consecutive --reset-all cycles (default: 5)
#    HEIGHT_WAIT_SEC  Max seconds to wait per round for every node to answer
#                      eth_blockNumber before declaring that round failed
#                      (default: 180)
# ═══════════════════════════════════════════════════════════════════
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_DIR="$(cd "$SCRIPT_DIR/../ansible" && pwd)"
ROUNDS="${1:-5}"
HEIGHT_WAIT_SEC="${2:-180}"
POLL_INTERVAL_SEC=3
RPC_NODES_JSON="/tmp/rpc_nodes.json"

PASS=0
FAIL=0
FAILED_ROUNDS=()
START_TIME=$(date +%s)

echo "=========================================================="
echo "🔁 MULTI-RESET STABILITY TEST"
echo "   Số vòng lặp: $ROUNDS"
echo "   Timeout chờ đồng bộ mỗi vòng: ${HEIGHT_WAIT_SEC}s"
echo "=========================================================="

for round in $(seq 1 "$ROUNDS"); do
    round_log="/tmp/multi_reset_round_${round}.log"
    echo -e "\n=========================================================="
    echo "🔄 [Vòng $round/$ROUNDS] Chạy ./ansible_deploy.sh --reset-all --open-ports"
    echo "=========================================================="
    cd "$ANSIBLE_DIR"
    if ! ./ansible_deploy.sh --reset-all --open-ports >"$round_log" 2>&1; then
        echo "❌ [Vòng $round] ansible_deploy.sh --reset-all thất bại (mã lỗi khác 0). Log: $round_log"
        FAIL=$((FAIL + 1))
        FAILED_ROUNDS+=("$round:ansible_deploy_failed")
        continue
    fi

    if [ ! -f "$RPC_NODES_JSON" ]; then
        echo "❌ [Vòng $round] Không tìm thấy $RPC_NODES_JSON sau khi deploy xong"
        FAIL=$((FAIL + 1))
        FAILED_ROUNDS+=("$round:no_rpc_nodes_json")
        continue
    fi

    mapfile -t NODE_KEYS < <(python3 -c "
import json
d = json.load(open('$RPC_NODES_JSON'))
print('\n'.join(sorted(d.get('nodes', {}).keys())))
" 2>/dev/null)

    if [ "${#NODE_KEYS[@]}" -eq 0 ]; then
        echo "❌ [Vòng $round] $RPC_NODES_JSON không liệt kê node nào"
        FAIL=$((FAIL + 1))
        FAILED_ROUNDS+=("$round:empty_node_list")
        continue
    fi

    echo "⏳ [Vòng $round] Chờ tất cả ${#NODE_KEYS[@]} node trả lời eth_blockNumber (tối đa ${HEIGHT_WAIT_SEC}s)..."
    elapsed=0
    round_ok=false
    while [ "$elapsed" -lt "$HEIGHT_WAIT_SEC" ]; do
        all_up=true
        line="   [${elapsed}s/${HEIGHT_WAIT_SEC}s]"
        for key in "${NODE_KEYS[@]}"; do
            url=$(python3 -c "import json; print(json.load(open('$RPC_NODES_JSON'))['nodes']['$key'])" 2>/dev/null)
            resp=$(curl -s -m 3 -X POST "$url" -H 'Content-Type: application/json' \
                -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
            hexval=$(python3 -c "
import json, sys
try:
    d = json.loads('''$resp''')
    print(d.get('result', 'DEAD'))
except Exception:
    print('DEAD')
" 2>/dev/null)
            line="$line $key=$hexval"
            if [ "$hexval" = "DEAD" ] || [ -z "$hexval" ]; then
                all_up=false
            fi
        done
        echo "$line"
        if [ "$all_up" = true ]; then
            round_ok=true
            break
        fi
        sleep "$POLL_INTERVAL_SEC"
        elapsed=$((elapsed + POLL_INTERVAL_SEC))
    done

    if [ "$round_ok" = true ]; then
        echo "✅ [Vòng $round] Tất cả node đều sống và trả lời RPC sau ${elapsed}s."
        PASS=$((PASS + 1))
    else
        echo "❌ [Vòng $round] Có node KHÔNG lên được trong ${HEIGHT_WAIT_SEC}s (crash-loop / race condition). Deploy log: $round_log"
        echo "   👉 Kiểm tra: sudo systemctl status metanode-execution-N --no-pager trên từng host trong inventory.yml"
        FAIL=$((FAIL + 1))
        FAILED_ROUNDS+=("$round:node_never_came_up")
    fi
done

DURATION=$(($(date +%s) - START_TIME))
echo -e "\n=========================================================="
echo "📊 KẾT QUẢ MULTI-RESET STABILITY TEST"
echo "   PASS: $PASS/$ROUNDS vòng"
echo "   FAIL: $FAIL/$ROUNDS vòng"
echo "   Tổng thời gian: ${DURATION}s"
if [ "$FAIL" -gt 0 ]; then
    echo "   Các vòng lỗi: ${FAILED_ROUNDS[*]}"
    echo "=========================================================="
    exit 1
fi
echo "=========================================================="
exit 0
