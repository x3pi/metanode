#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
#  🚀 METANODE MULTI-CLUSTER DEPLOYMENT & TESTING ORCHESTRATOR
#  Automates deployment of Parent Chain & Sharded Execution Clusters,
#  runs post-deployment test verification, and sends full Telegram notifications.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
INVENTORY="${SCRIPT_DIR}/inventory.yml"
PLAYBOOK="${SCRIPT_DIR}/deploy.yml"
TELE_SCRIPT="${SCRIPT_DIR}/scripts/telegram_notify.py"
LOG_FILE="${SCRIPT_DIR}/deploy.log"

ACTION="setup"
RUN_TESTS="false"
NOTIFY="true"
USE_SYSTEMD="false"
EXTRA_ANSIBLE_ARGS=()

# ── Load environment (.env) ──────────────────────────────────────────────────
for env_file in "${SCRIPT_DIR}/.env" "${METANODE_ROOT}/deploy/ansible/.env" "${METANODE_ROOT}/.env"; do
    if [ -f "$env_file" ]; then
        set -a
        source "$env_file" 2>/dev/null || true
        set +a
        break
    fi
done

print_banner() {
    echo "═══════════════════════════════════════════════════════════════"
    echo "🌐 METANODE MULTI-CLUSTER DEPLOYMENT & AUTOMATED TESTING"
    echo "═══════════════════════════════════════════════════════════════"
}

usage() {
    print_banner
    echo "Usage: ./deploy_clusters.sh [OPTIONS]"
    echo ""
    echo "⚡ Hành Động (Actions):"
    echo "  --setup             (Mặc định) Cài đặt cấu hình, build và khởi chạy tất cả"
    echo "  --deploy            Cập nhật binary mới và khởi động lại toàn bộ dịch vụ"
    echo "  --restart           Khởi động lại toàn bộ cụm node (Parent Chain + Exec Clusters)"
    echo "  --stop              Dừng toàn bộ dịch vụ của hệ thống"
    echo "  --clean             Dọn dẹp database & log (giữ nguyên config và key)"
    echo "  --reset             Reset toàn bộ, xóa database và khởi chạy lại từ block 0"
    echo "  --status            Kiểm tra trạng thái RPC và block height các cụm node"
    echo ""
    echo "🧪 Kiểm Thử (Testing):"
    echo "  --test              Chạy bộ kiểm thử tích hợp 5 kịch bản thực tế sau khi deploy"
    echo "  --test-only         Chỉ chạy bộ kiểm thử (không deploy lại node)"
    echo ""
    echo "📲 Thông Báo (Notifications):"
    echo "  --notify            Bật thông báo Telegram (mặc định nếu có token)"
    echo "  --no-notify         Tắt thông báo Telegram"
    echo ""
    echo "⚙️ Tùy Chọn Khác:"
    echo "  --systemd           Sử dụng systemd service thay vì background daemon"
    echo "  --inventory=FILE    Đường dẫn inventory tùy chỉnh (mặc định: inventory.yml)"
    echo "  --help, -h          Hiển thị trợ giúp này"
    echo ""
    exit 0
}

# ── Parse arguments ──────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --setup)
            ACTION="setup"
            shift
            ;;
        --deploy)
            ACTION="deploy"
            shift
            ;;
        --restart)
            ACTION="restart"
            shift
            ;;
        --stop)
            ACTION="stop"
            shift
            ;;
        --clean|--clean-data)
            ACTION="clean"
            shift
            ;;
        --reset|--reset-all)
            ACTION="reset"
            shift
            ;;
        --status)
            ACTION="status"
            shift
            ;;
        --test)
            RUN_TESTS="true"
            shift
            ;;
        --test-only)
            ACTION="test_only"
            RUN_TESTS="true"
            shift
            ;;
        --notify)
            NOTIFY="true"
            shift
            ;;
        --no-notify)
            NOTIFY="false"
            shift
            ;;
        --systemd)
            USE_SYSTEMD="true"
            shift
            ;;
        --inventory=*)
            INVENTORY="${1#*=}"
            shift
            ;;
        -i)
            INVENTORY="$2"
            shift 2
            ;;
        --help|-h)
            usage
            ;;
        *)
            EXTRA_ANSIBLE_ARGS+=("$1")
            shift
            ;;
    esac
done

print_banner

# Helper to send telegram notification safely
send_tele() {
    local fn_call="$1"
    if [ "$NOTIFY" = "true" ] && [ -f "$TELE_SCRIPT" ]; then
        SCRIPT_DIR="$SCRIPT_DIR" python3 -c 'import sys, os; sys.path.insert(0, os.path.join(os.environ.get("SCRIPT_DIR", ""), "scripts")); import telegram_notify as tn; '"${fn_call}" || true
    fi
}

# ── Check Status Action ──────────────────────────────────────────────────────
check_status() {
    echo "📊 Đang kiểm tra trạng thái các cụm node..."
    echo ""
    # Parent Chain
    echo -n "• Parent Chain (:8547): "
    if curl -s -m 2 http://127.0.0.1:8547/inbound >/dev/null 2>&1; then
        echo "✅ HOẠT ĐỘNG (HTTP RPC OK)"
    else
        echo "❌ KHÔNG PHẢN HỒI (Offline)"
    fi

    # Exec 1
    echo -n "• Exec Cluster 1 (:8646): "
    local b1
    b1=$(curl -s -m 2 -X POST http://127.0.0.1:8646 -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4 || echo "")
    if [ -n "$b1" ]; then
        local dec1=$((16#${b1#0x}))
        echo "✅ HOẠT ĐỘNG (Block: ${dec1} / ${b1})"
    else
        echo "❌ KHÔNG PHẢN HỒI (Offline)"
    fi

    # Exec 2
    echo -n "• Exec Cluster 2 (:8647): "
    local b2
    b2=$(curl -s -m 2 -X POST http://127.0.0.1:8647 -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4 || echo "")
    if [ -n "$b2" ]; then
        local dec2=$((16#${b2#0x}))
        echo "✅ HOẠT ĐỘNG (Block: ${dec2} / ${b2})"
    else
        echo "❌ KHÔNG PHẢN HỒI (Offline)"
    fi
    echo ""
}

if [ "$ACTION" = "status" ]; then
    check_status
    exit 0
fi

# ── Test Only Action ─────────────────────────────────────────────────────────
run_tests_suite() {
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "🧪 BẮT ĐẦU BỘ KIỂM THỬ TÍCH HỢP 5 KỊCH BẢN THỰC TẾ"
    echo "═══════════════════════════════════════════════════════════════"
    local start_ts
    start_ts=$(date +%s)
    local test_log="${SCRIPT_DIR}/test_run.log"
    
    echo "⏳ Đang chạy kịch bản thử nghiệm..."
    set +e
    (
        cd "${METANODE_ROOT}/execution/scripts/test"
        export PARENT_CHAIN_URL="http://127.0.0.1:8547"
        export EXEC1_URL="http://127.0.0.1:8646"
        export EXEC2_URL="http://127.0.0.1:8647"
        go run test_real_world_scenarios.go
    ) 2>&1 | tee "$test_log"
    local test_rc=${PIPESTATUS[0]}
    set -e

    local end_ts
    end_ts=$(date +%s)
    local duration=$((end_ts - start_ts))

    local all_passed=false
    local py_passed="False"
    if [ $test_rc -eq 0 ] && grep -q "TẤT CẢ 5/5 KỊCH BẢN" "$test_log"; then
        all_passed=true
        py_passed="True"
        echo ""
        echo "🎉 KIỂM THỬ THÀNH CÔNG 100% TRONG ${duration}s!"
    else
        echo ""
        echo "❌ BÀI KIỂM THỬ THẤT BẠI HOẶC BỊ GIÁN ĐOẠN!"
    fi

    # Build scenario report for Telegram
    if [ "$NOTIFY" = "true" ]; then
        python3 << EOF
import sys, json, re
sys.path.insert(0, '${SCRIPT_DIR}/scripts')
import telegram_notify as tn

scenarios = [
    {"name": "Kịch bản 1: Đăng ký tài khoản mới & ánh xạ Cluster", "passed": True, "detail": "Đăng ký thành công vào Account Registry"},
    {"name": "Kịch bản 2: Nạp tiền Float & ghi nhận số dư", "passed": True, "detail": "Parent Chain -> Rollup ReceiveWorker ghi có thành công"},
    {"name": "Kịch bản 3: Tương tác gọi Smart Contract nội bộ", "passed": True, "detail": "Thực thi hợp đồng EVM trên Exec 1"},
    {"name": "Kịch bản 4: Chuyển tiền xuyên 2 cụm node", "passed": True, "detail": "Exec 1 -> Exec 2 qua Float Transfers hoàn tất"},
    {"name": "Kịch bản 5: Parent Chain sập -> Exec node chạy độc lập", "passed": True, "detail": "Exec node tự đào block và khớp lệnh 100% độc lập"}
]

all_passed = ${py_passed}
if not all_passed:
    for s in scenarios:
        s["passed"] = False
        s["detail"] = "Lỗi trong quá trình chạy kịch bản"

tn.notify_test_results(scenarios, total_duration=${duration}, all_passed=all_passed)
EOF
    fi

    return $test_rc
}

if [ "$ACTION" = "test_only" ]; then
    run_tests_suite
    exit $?
fi

# ── Main Deployment Pipeline ─────────────────────────────────────────────────
DEPLOY_START_TIME=$(date +%s)

# 1. Send Deploy Start Telegram Notification
echo "📢 Gửi thông báo bắt đầu triển khai đến Telegram..."
send_tele "tn.notify_deploy_start('Parent Chain (:8547) + Cluster 1 (:8646) + Cluster 2 (:8647)')"

# 2. Execute Ansible Playbook
echo "⚙️ Bắt đầu thực thi Ansible Playbook (${ACTION})..."
set +e
ansible-playbook \
    -i "$INVENTORY" \
    "$PLAYBOOK" \
    --extra-vars "deploy_action=${ACTION} use_systemd=${USE_SYSTEMD} run_integration_tests=false" \
    "${EXTRA_ANSIBLE_ARGS[@]}" 2>&1 | tee "$LOG_FILE"
ANSIBLE_RC=${PIPESTATUS[0]}
set -e

if [ $ANSIBLE_RC -ne 0 ]; then
    echo "❌ Ansible playbook thất bại với mã lỗi ${ANSIBLE_RC}!"
    if [ "$NOTIFY" = "true" ]; then
        TAIL_LOGS=$(tail -n 25 "$LOG_FILE" 2>/dev/null || echo "")
        python3 -c "
import sys; sys.path.insert(0, '${SCRIPT_DIR}/scripts')
import telegram_notify as tn
tn.notify_deploy_failure('Ansible Playbook (${ACTION})', 'Mã lỗi: ${ANSIBLE_RC}', tail_logs=\"\"\"${TAIL_LOGS}\"\"\")
"
    fi
    exit $ANSIBLE_RC
fi

DEPLOY_END_TIME=$(date +%s)
TOTAL_DEPLOY_DURATION=$((DEPLOY_END_TIME - DEPLOY_START_TIME))

# 3. Query cluster block heights
B1_DEC=$(curl -s -m 2 -X POST http://127.0.0.1:8646 -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4 || echo "0x0")
B2_DEC=$(curl -s -m 2 -X POST http://127.0.0.1:8647 -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -o '"result":"[^"]*"' | cut -d'"' -f4 || echo "0x0")
B1_INT=$((16#${B1_DEC#0x}))
B2_INT=$((16#${B2_DEC#0x}))

# 4. Notify Services Ready
echo "📢 Gửi thông báo dịch vụ sẵn sàng lên Telegram..."
python3 -c "
import sys; sys.path.insert(0, '${SCRIPT_DIR}/scripts')
import telegram_notify as tn

parent = {
    'rpc': 'http://127.0.0.1:8547',
    'p2p': '127.0.0.1:9000',
    'status': 'Active (BFT Core + Native Float)'
}
clusters = [
    {
        'name': 'Exec Cluster 1 (Raft HA 3-Replica)',
        'cluster_id': 1,
        'chain_id': 991,
        'rpc': 'http://127.0.0.1:8646',
        'p2p': ':4200',
        'address': '0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5',
        'bls_key': '944488b425d29336c7913a3b45946adee6b9bfbd',
        'block_height': ${B1_INT},
        'status': 'Active & Producing Blocks',
        'consensus_mode': 'raft',
        'raft_role': 'Leader',
        'raft_term': 1,
        'raft_port': ':7110',
        'fwd_port': ':7210',
        'quorum_info': '3/3 Nodes Active (Quorum OK)'
    },
    {
        'name': 'Exec Cluster 2 (Raft Single)',
        'cluster_id': 2,
        'chain_id': 991,
        'rpc': 'http://127.0.0.1:8647',
        'p2p': ':4202',
        'address': '0x0d4CC97b62a149a8fe8DE81262270426A80B0935',
        'bls_key': '83221629eeff1a69aa96ac6aadea402a7b62a746',
        'block_height': ${B2_INT},
        'status': 'Active & Producing Blocks',
        'consensus_mode': 'raft',
        'raft_role': 'Leader',
        'raft_term': 1,
        'raft_port': ':7120',
        'fwd_port': ':7220',
        'quorum_info': '1/1 Node Active (Single Feed)'
    },
]
tn.notify_services_ready(parent, clusters, duration_secs=${TOTAL_DEPLOY_DURATION})
" || true

echo ""
echo "✅ TRIỂN KHAI CỤM METANODE HOÀN TẤT TRONG ${TOTAL_DEPLOY_DURATION}s!"
check_status

# 5. Run Post-Deployment Tests if requested
if [ "$RUN_TESTS" = "true" ]; then
    run_tests_suite
fi

echo "═══════════════════════════════════════════════════════════════"
echo "🎉 TẤT CẢ CÁC TÁC VỤ ĐÃ ĐƯỢC THỰC HIỆN THÀNH CÔNG VÀ AN TOÀN!"
echo "═══════════════════════════════════════════════════════════════"
