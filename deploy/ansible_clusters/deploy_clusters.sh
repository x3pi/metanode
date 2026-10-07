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

ACTION=""
OPEN_PORTS_FLAG="false"
RUN_TESTS="false"
NOTIFY="true"
EXEC_ONLY="false"
PARENT_ONLY="false"
TARGET_NODE=""
EXPORT_CONFIG_ONLY="false"
ENABLE_MONITOR="true"
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
    echo "  --start             Bật lại các node (toàn bộ hoặc lọc theo --node/--exec-only)"
    echo "  --restart           Khởi động lại toàn bộ cụm node (Parent Chain + Exec Clusters)"
    echo "  --stop              Dừng dịch vụ (toàn bộ hoặc lọc theo --node/--exec-only)"
    echo "  --clean             Dọn dẹp database & log (giữ nguyên config và key)"
    echo "  --reset             Reset toàn bộ, xóa database và khởi chạy lại từ block 0"
    echo "  --status            Kiểm tra trạng thái RPC và block height các cụm node"
    echo "  --open-ports        Tự động cấu hình mở cổng tường lửa (UFW) trên các server"
    echo "  --export-config     Xuất cấu hình mạng ra /tmp/rpc_nodes.json"
    echo ""
    echo "🎯 Phạm vi áp dụng (Target & Scope):"
    echo "  --exec-only         Chỉ thao tác trên Execution Clusters (các chain con - Chain ID 991)"
    echo "  --parent-only       Chỉ thao tác trên Parent Chain"
    echo "  --node=NAME, -n     Chỉ thao tác trên 1 node cụ thể (vd: exec1_r1, exec1_r3, parent)"
    echo ""
    echo "🧪 Kiểm Thử (Testing):"
    echo "  --test              Chạy bộ kiểm thử tích hợp thực tế sau khi deploy"
    echo "  --test-only         Chỉ chạy bộ kiểm thử; tự đồng bộ token Parent Chain nếu token đã đổi"
    echo ""
    echo "📲 Thông Báo & Giám Sát (Notifications & Monitoring):"
    echo "  --notify            Bật thông báo Telegram (mặc định nếu có token)"
    echo "  --no-notify         Tắt thông báo Telegram"
    echo "  --monitor           (Mặc định) Bật monitor ngầm (Health check & Hash verification)"
    echo "  --no-monitor        Không bật monitor ngầm sau khi deploy"
    echo "  --monitor-status    Kiểm tra trạng thái tiến trình monitor ngầm"
    echo "  --stop-monitor      Dừng tiến trình monitor ngầm"
    echo "  --monitor-only      Chỉ khởi động monitor ngầm mà không deploy"
    echo ""
    echo "⚙️ Tùy Chọn Khác:"
    echo "  --env=ENV           Môi trường triển khai: devnet (mặc định cho test cluster) hoặc production"
    echo "  --systemd           Sử dụng systemd service thay vì background daemon"
    echo "  --inventory=FILE    Đường dẫn inventory tùy chỉnh (mặc định: inventory.yml)"
    echo "  --rpc-nodes-file=FILE Đường dẫn file cấu hình RPC JSON (mặc định: /tmp/rpc_nodes.json)"
    echo "  --help, -h          Hiển thị trợ giúp này"
    echo ""
    echo "🔒 Lưu ý bảo mật (Issue #104):"
    echo "  - Khi --env=production: Mật khẩu trong inventory bắt buộc phải được mã hóa bằng Ansible Vault."
    echo "  - Khi --env=devnet (mặc định): Cho phép inventory chứa thông tin thử nghiệm cục bộ."
    echo ""
    exit 0
}

RPC_NODES_FILE="${RPC_NODES_FILE:-${RPC_NODES_JSON_PATH:-/tmp/rpc_nodes.json}}"

# ── Parse arguments ──────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --setup|setup)
            ACTION="setup"
            shift
            ;;
        --deploy|deploy)
            ACTION="deploy"
            shift
            ;;
        --start|start)
            ACTION="start"
            shift
            ;;
        --restart|restart)
            ACTION="restart"
            shift
            ;;
        --stop|stop)
            ACTION="stop"
            shift
            ;;
        --clean|--clean-data|clean)
            ACTION="clean"
            shift
            ;;
        --reset|--reset-all|reset)
            ACTION="reset"
            shift
            ;;
        --status|status)
            ACTION="status"
            shift
            ;;
        --open-ports|open-ports|open_ports)
            OPEN_PORTS_FLAG="true"
            shift
            ;;
        --export-config|--export-config-only)
            EXPORT_CONFIG_ONLY="true"
            shift
            ;;
        --exec-only|--execution-only|--shards-only|--child-only|--child-chain-only)
            EXEC_ONLY="true"
            shift
            ;;
        --parent-only)
            PARENT_ONLY="true"
            shift
            ;;
        --node=*|--target=*)
            TARGET_NODE="${1#*=}"
            shift
            ;;
        -n|--node)
            TARGET_NODE="$2"
            shift 2
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
        --monitor)
            ENABLE_MONITOR="true"
            shift
            ;;
        --no-monitor)
            ENABLE_MONITOR="false"
            shift
            ;;
        --monitor-status)
            ACTION="monitor_status"
            shift
            ;;
        --stop-monitor)
            ACTION="stop_monitor"
            shift
            ;;
        --monitor-only)
            ACTION="monitor_only"
            shift
            ;;
        --systemd)
            # Systemd is now the default and only mode
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
        --rpc-nodes-file=*|--rpc-json=*)
            RPC_NODES_FILE="${1#*=}"
            shift
            ;;
        --rpc-nodes-file|--rpc-json)
            RPC_NODES_FILE="$2"
            shift 2
            ;;
        --env=*)
            METANODE_ENV="${1#*=}"
            shift
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

if [ -z "$ACTION" ]; then
    if [ "$OPEN_PORTS_FLAG" = "true" ]; then
        ACTION="open_ports"
    else
        ACTION="setup"
    fi
fi

export METANODE_ENV="${METANODE_ENV:-devnet}"
export NODE_ENV="${NODE_ENV:-$METANODE_ENV}"

# inventory.yml is untracked (it holds real hosts / vault-encrypted credentials): on a fresh clone seed it
# from the example so the default invocation works, then remind the operator to edit it.
if [ ! -f "$INVENTORY" ] && [ "$INVENTORY" = "${SCRIPT_DIR}/inventory.yml" ] && [ -f "${SCRIPT_DIR}/inventory.example.yml" ]; then
    cp "${SCRIPT_DIR}/inventory.example.yml" "$INVENTORY"
    echo "⚠️  inventory.yml does not exist: created from inventory.example.yml. Please configure hosts/keys/passwords (use ansible-vault) before deploying." >&2
fi

# Make test-only use the same Vault password discovery as deployment.
VAULT_CLUSTER_ARGS=()
if [ -f "${SCRIPT_DIR}/.vault_pass" ]; then
    VAULT_CLUSTER_ARGS=(--vault-password-file "${SCRIPT_DIR}/.vault_pass")
elif [ -f "${METANODE_ROOT}/deploy/ansible/.vault_pass" ]; then
    VAULT_CLUSTER_ARGS=(--vault-password-file "${METANODE_ROOT}/deploy/ansible/.vault_pass")
elif [ -f "$HOME/.vault_pass" ]; then
    VAULT_CLUSTER_ARGS=(--vault-password-file "$HOME/.vault_pass")
fi

print_banner

# Helper to send telegram notification safely
send_tele() {
    local fn_call="$1"
    if [ "$NOTIFY" = "true" ] && [ -f "$TELE_SCRIPT" ]; then
        SCRIPT_DIR="$SCRIPT_DIR" python3 -c 'import sys, os; sys.path.insert(0, os.path.join(os.environ.get("SCRIPT_DIR", ""), "scripts")); import telegram_notify as tn; '"${fn_call}" || true
    fi
}

resolve_target_host() {
    local target="$1"
    case "$target" in
        exec1_r1|r1|1)
            echo "exec1_replica1"
            ;;
        exec1_r2|r2|2)
            echo "exec1_replica2"
            ;;
        exec1_r3|r3|3)
            echo "exec1_replica3"
            ;;
        exec1|cluster_1|cluster1)
            echo "exec1_replica1,exec1_replica2,exec1_replica3"
            ;;
        exec2_r1|r4)
            echo "exec2_replica1"
            ;;
        exec2_r2|r5)
            echo "exec2_replica2"
            ;;
        exec2_r3|r6)
            echo "exec2_replica3"
            ;;
        exec2|cluster_2|cluster2)
            echo "exec2_replica1,exec2_replica2,exec2_replica3"
            ;;
        parent|parent_chain|parent_node)
            echo "parent_node"
            ;;
        *)
            echo "$target"
            ;;
    esac
}

# ── Early Exit for Export Config ─────────────────────────────────────────────
if [ "$EXPORT_CONFIG_ONLY" = "true" ]; then
    echo "📢 Exporting configuration to /tmp..."
    if [ -f "$INVENTORY" ] && [ -f "${SCRIPT_DIR}/scripts/parse_inventory.py" ]; then
        python3 "${SCRIPT_DIR}/scripts/parse_inventory.py" "$INVENTORY" export
        echo "✅ Successfully exported configuration to:"
        echo "   • /tmp/rpc_nodes.json"
        exit 0
    else
        echo "❌ Inventory ($INVENTORY) or parse_inventory.py not found"
        exit 1
    fi
fi

# ── Monitor Helpers (Tái sử dụng start_monitors.sh & block_hash_checker) ─────
MONITOR_SCRIPT="${METANODE_ROOT}/deploy/ansible/monitors/start_monitors.sh"

get_exec_monitor_names() {
    python3 - "$INVENTORY" "${SCRIPT_DIR}/scripts" <<'PY'
import sys

inventory, scripts_dir = sys.argv[1:]
sys.path.insert(0, scripts_dir)
import parse_inventory as pi

for cluster_id, cluster in pi.parse_inventory(inventory).get("clusters", {}).items():
    if cluster.get("replicas"):
        print(cluster.get("cluster_name", f"exec{cluster_id}"))
PY
}

print_monitor_status() {
    echo "🔍 Trạng thái tiến trình Monitor ngầm:"
    local found=0
    local ns
    while IFS= read -r ns; do
        [ -n "$ns" ] || continue
        if [ -d "/tmp/metanode-monitors-${ns}" ]; then
            local pid_f p_name pid
            for pid_f in "/tmp/metanode-monitors-${ns}"/*.pid; do
                if [ -f "$pid_f" ]; then
                    p_name=$(basename "$pid_f" .pid)
                    pid=$(cat "$pid_f" 2>/dev/null || echo "")
                    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
                        echo "  • [${ns}] ${p_name}: ▶️ RUNNING (PID ${pid})"
                        found=1
                    fi
                fi
            done
        fi
    done < <(get_exec_monitor_names 2>/dev/null || true)

    if pgrep -f "[p]arent_chain_monitor.py" >/dev/null 2>&1; then
        echo "  • [parent] liveness/hash monitor: ▶️ RUNNING"
        found=1
    fi

    if [ "$found" -eq 0 ]; then
        echo "  • Chưa có monitor nào đang chạy."
    fi
}

stop_cluster_monitors() {
    echo "⏸️  Tạm dừng monitor ngầm của tất cả execution cluster và parent chain để tránh báo động giả..."
    if [ -f "$MONITOR_SCRIPT" ]; then
        for pid_dir in /tmp/metanode-monitors-*; do
            if [ -d "$pid_dir" ]; then
                ns="${pid_dir##*-}"
                bash "$MONITOR_SCRIPT" --stop --namespace "$ns" >/dev/null 2>&1 || true
            fi
        done
        while IFS= read -r ns; do
            [ -n "$ns" ] || continue
            bash "$MONITOR_SCRIPT" --stop --namespace "$ns" >/dev/null 2>&1 || true
        done < <(get_exec_monitor_names 2>/dev/null || true)
    fi
    if [ -f "${SCRIPT_DIR}/scripts/parent_chain_monitor.py" ]; then
        python3 "${SCRIPT_DIR}/scripts/parent_chain_monitor.py" --stop >/dev/null 2>&1 || true
    fi
    pkill -f "parent_chain_monitor.py" >/dev/null 2>&1 || true
}

clean_monitor_state() {
    echo "🧹 Dọn sạch cache và log trạng thái cũ của Monitor..."
    rm -f "${METANODE_ROOT}/deploy/ansible/monitors/block_hash_checker/chain_anomalies.log" 2>/dev/null || true
    rm -f "${METANODE_ROOT}/deploy/ansible/monitors/block_hash_checker/ghost_blocks.log" 2>/dev/null || true
    rm -f "${METANODE_ROOT}/deploy/ansible/monitors/block_hash_checker/block_checker_daemon.log" 2>/dev/null || true
    rm -f "${METANODE_ROOT}/deploy/ansible/monitors/block_hash_checker/"*.csv 2>/dev/null || true
}

start_cluster_monitors() {
    if [ "$ENABLE_MONITOR" != "true" ]; then
        return 0
    fi
    echo "▶️  Kích hoạt hệ thống Monitor ngầm..."
    
    # 1. Kích hoạt monitor cho các Exec Clusters trong Inventory (Child Chains chạy Raft Consensus, bỏ qua Validator Vote Monitor)
    if [ "$PARENT_ONLY" != "true" ] && [ -f "$MONITOR_SCRIPT" ]; then
        CLUSTER_NAMES=$(get_exec_monitor_names 2>/dev/null || true)

        for ns in $CLUSTER_NAMES; do
            c_file="/tmp/rpc_nodes.${ns}.json"
            if [ -s "$c_file" ]; then
                echo "   • Kích hoạt Health Monitor & Block Hash Checker cho $ns (Namespace: $ns, Raft - no vote)..."
                bash "$MONITOR_SCRIPT" --stop --namespace "$ns" >/dev/null 2>&1 || true
                MONITOR_NAMESPACE="$ns" MONITOR_INVENTORY="$INVENTORY" \
                    bash "$MONITOR_SCRIPT" --namespace "$ns" --config "$c_file" --no-vote
            elif [ -s "$RPC_NODES_FILE" ]; then
                echo "   • Kích hoạt Health Monitor & Block Hash Checker cho child chain (Config: $RPC_NODES_FILE, Raft - no vote)..."
                bash "$MONITOR_SCRIPT" --stop --namespace "$ns" >/dev/null 2>&1 || true
                MONITOR_NAMESPACE="$ns" MONITOR_INVENTORY="$INVENTORY" \
                    bash "$MONITOR_SCRIPT" --namespace "$ns" --config "$RPC_NODES_FILE" --no-vote
            fi
        done
    fi

    # 2. Kích hoạt Monitor cho Parent Chain (Liveness + Hash Consistency, inventory-driven, không dùng Vote Monitor cũ)
    if [ "$EXEC_ONLY" != "true" ] && [ -f "${SCRIPT_DIR}/scripts/parent_chain_monitor.py" ]; then
        HAS_PARENT=$(python3 -c "
import sys; sys.path.insert(0, '${SCRIPT_DIR}/scripts')
import parse_inventory as pi
info = pi.parse_inventory('${INVENTORY}')
print('true' if info.get('parent_nodes') else 'false')
" 2>/dev/null || echo "false")

        if [ "$HAS_PARENT" = "true" ]; then
            echo "   • Kích hoạt Monitor cho Parent Chain (Liveness + Hash Consistency, config: ${INVENTORY})..."
            python3 "${SCRIPT_DIR}/scripts/parent_chain_monitor.py" --stop >/dev/null 2>&1 || true
            python3 "${SCRIPT_DIR}/scripts/parent_chain_monitor.py" --daemon --inventory="${INVENTORY}" --interval=5
        fi
    fi
}

if [ "$ACTION" = "stop_monitor" ]; then
    stop_cluster_monitors
    echo "✅ Đã dừng các tiến trình monitor ngầm của toàn bộ cluster và parent chain."
    exit 0
fi

if [ "$ACTION" = "monitor_only" ]; then
    python3 "${SCRIPT_DIR}/scripts/parse_inventory.py" "$INVENTORY" export "$RPC_NODES_FILE" >/dev/null 2>&1 || true
    start_cluster_monitors
    exit 0
fi

if [ "$ACTION" = "monitor_status" ]; then
    print_monitor_status
    exit 0
fi

# ── Check Status Action ──────────────────────────────────────────────────────
check_status() {
    echo "📊 Checking node cluster status..."
    echo ""
    if [ -f "$INVENTORY" ] && [ -f "${SCRIPT_DIR}/scripts/parse_inventory.py" ]; then
        python3 "${SCRIPT_DIR}/scripts/parse_inventory.py" "$INVENTORY" status
    fi
    echo ""
    print_monitor_status
    echo ""
}

if [ "$ACTION" = "status" ]; then
    check_status
    exit 0
fi

# ── Test Only Action ─────────────────────────────────────────────────────────
run_tests_suite() {
    # Refresh endpoints for both --test and --test-only before Ansible reads them.
    python3 "${SCRIPT_DIR}/scripts/parse_inventory.py" "$INVENTORY" export || return $?
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "🧪 BẮT ĐẦU BỘ KIỂM THỬ TÍCH HỢP 9 KỊCH BẢN THỰC TẾ"
    echo "═══════════════════════════════════════════════════════════════"
    local start_ts
    start_ts=$(date +%s)
    local test_log="${SCRIPT_DIR}/test_run.log"

    : > "$test_log"
    echo "⏳ Đang chạy kịch bản thử nghiệm..."
    set +e
    ansible-playbook \
        -i "$INVENTORY" \
        "$PLAYBOOK" \
        --tags test \
        --extra-vars "run_integration_tests=true deploy_action=test metanode_env=${METANODE_ENV} node_env=${NODE_ENV}" \
        "${VAULT_CLUSTER_ARGS[@]}" \
        "${EXTRA_ANSIBLE_ARGS[@]}" 2>&1 | tee "${test_log}.ansible"
    local test_rc=${PIPESTATUS[0]}
    set -e

    if [ -s "$test_log" ]; then
        cat "$test_log"
    fi

    local end_ts
    end_ts=$(date +%s)
    local duration=$((end_ts - start_ts))

    local all_passed=false
    local py_passed="False"
    if [ $test_rc -eq 0 ] && grep -q "TẤT CẢ 9/9 KỊCH BẢN" "$test_log"; then
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

log_content = ""
try:
    with open("${test_log}", "r", encoding="utf-8", errors="ignore") as f:
        log_content = f.read()
except Exception:
    pass

scenarios = [
    {
        "name": "Kịch bản 1: Đăng ký tài khoản mới & ánh xạ Cluster",
        "passed": "KỊCH BẢN 1 THÀNH CÔNG" in log_content,
        "detail": "Đăng ký thành công vào Account Registry" if "KỊCH BẢN 1 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 2: Nạp tiền Float & ghi nhận số dư",
        "passed": "KỊCH BẢN 2 THÀNH CÔNG" in log_content,
        "detail": "Parent Chain -> Rollup ReceiveWorker ghi có thành công" if "KỊCH BẢN 2 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 3: Tương tác gọi Smart Contract nội bộ",
        "passed": "KỊCH BẢN 3 THÀNH CÔNG" in log_content,
        "detail": "Thực thi hợp đồng EVM trên Exec 1" if "KỊCH BẢN 3 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 4: Chuyển tiền xuyên 2 cụm node",
        "passed": "KỊCH BẢN 4 THÀNH CÔNG" in log_content,
        "detail": "Exec 1 -> Exec 2 qua Float Transfers hoàn tất" if "KỊCH BẢN 4 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 5: Parent Chain sập -> Exec node chạy độc lập",
        "passed": "KỊCH BẢN 5 THÀNH CÔNG" in log_content,
        "detail": "Exec node tự đào block và khớp lệnh 100% độc lập" if "KỊCH BẢN 5 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 6: Khôi phục Parent Chain -> Tự động tái đồng bộ & chuyển tiền liên cụm",
        "passed": "KỊCH BẢN 6 THÀNH CÔNG" in log_content,
        "detail": "Cầu nối Rollup tự động phục hồi và xử lý giao dịch thành công" if "KỊCH BẢN 6 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 7: Gọi Smart Contract xuyên 2 cụm node",
        "passed": "KỊCH BẢN 7 THÀNH CÔNG" in log_content,
        "detail": "Exec 1 kích hoạt luồng gọi Smart Contract sang Exec 2, Exec 2 xử lý và cập nhật hợp đồng EVM thành công" if "KỊCH BẢN 7 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 8: Mất 1/4 node Parent Chain -> chuyển xuyên cụm vẫn hoàn tất",
        "passed": "KỊCH BẢN 8 THÀNH CÔNG" in log_content,
        "detail": "Quorum 3/4: Exec 2 ghi có đúng số tiền" if "KỊCH BẢN 8 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    },
    {
        "name": "Kịch bản 9: Mất quorum Parent Chain -> pending, bật lại hoàn tất đúng 1 lần",
        "passed": "KỊCH BẢN 9 THÀNH CÔNG" in log_content,
        "detail": "Không tạo tiền khi mất quorum; sau khi bật lại ghi có đúng 1 lần" if "KỊCH BẢN 9 THÀNH CÔNG" in log_content else "Không hoàn tất hoặc bị gián đoạn"
    }
]

all_passed = all(s["passed"] for s in scenarios) and ${py_passed}
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

# 0. Xuất thông tin các cổng vào file RPC JSON
if [ -f "$INVENTORY" ] && [ -f "${SCRIPT_DIR}/scripts/parse_inventory.py" ]; then
    python3 "${SCRIPT_DIR}/scripts/parse_inventory.py" "$INVENTORY" export "$RPC_NODES_FILE"
fi

# 1. Send Deploy Start Telegram Notification
echo "📢 Gửi thông báo bắt đầu triển khai đến Telegram..."
send_tele "tn.notify_deploy_start('Parent Chain BFT Committee (4 Validators) + Exec Cluster 1 (3 Replicas) + Exec Cluster 2')"

CHECK_SEC_SCRIPT="${METANODE_ROOT}/deploy/ansible/check_inventory_security.py"

# Pre-flight Security check for plaintext credentials (Issue #104)
if [ -f "$CHECK_SEC_SCRIPT" ] && [ -f "$INVENTORY" ]; then
    PLAINTEXT_VIOLATIONS=$(python3 "$CHECK_SEC_SCRIPT" "$INVENTORY" 2>/dev/null || true)
    if [ -n "$PLAINTEXT_VIOLATIONS" ]; then
        if [ "$METANODE_ENV" = "production" ] && [ "$NODE_ENV" = "production" ]; then
            echo -e "\033[0;31m❌ [SECURITY ERROR] Plaintext credentials (${PLAINTEXT_VIOLATIONS}) detected in inventory for production environment (Issue #104)!\033[0m"
            echo -e "\033[0;33m   In production, credentials MUST be encrypted using ansible-vault (or referenced via Jinja2 '{{ vault_... }}').\033[0m"
            echo -e "\033[0;36m   👉 For devnet/benchmark, run with: ./deploy_clusters.sh --env=devnet\033[0m"
            exit 1
        else
            echo -e "\033[0;33m⚠️ [SECURITY NOTICE] Plaintext credentials (${PLAINTEXT_VIOLATIONS}) detected in inventory (allowed in devnet/benchmark only).\033[0m"
        fi
    fi
fi

# Detect become password from inventory for localhost become tasks (devnet only)
INVENTORY_BECOME_PASS=$(grep -E '^\s*ansible_become_pass:' "$INVENTORY" 2>/dev/null | head -n 1 | awk '{gsub(/["\047]/, ""); print $2}' || true)
if [ -n "$INVENTORY_BECOME_PASS" ] && [ "$INVENTORY_BECOME_PASS" != "!vault" ] && [[ "$INVENTORY_BECOME_PASS" != \{\{* ]]; then
    export ANSIBLE_BECOME_PASS="${ANSIBLE_BECOME_PASS:-$INVENTORY_BECOME_PASS}"
fi

# Pre-flight Sudo Validation to prevent indefinite hang on wrong become password
SUDO_CHECK_OUTPUT=$(python3 - "$INVENTORY" "${SCRIPT_DIR}/.vault_pass" << 'EOF' 2>/dev/null || echo "CHECK_FAILED"
import sys, os, re, subprocess
from ansible.parsing.vault import VaultLib, VaultSecret
from ansible.constants import DEFAULT_VAULT_ID_MATCH

inv_path = sys.argv[1]
vault_file = sys.argv[2]

if subprocess.run(['sudo', '-n', 'true'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
    print('OK')
    sys.exit(0)

pwd = os.environ.get('ANSIBLE_BECOME_PASS', '')
if not pwd and os.path.exists(vault_file) and os.path.exists(inv_path):
    try:
        with open(vault_file, 'rb') as f:
            vpass = f.read().strip()
        vault = VaultLib([(DEFAULT_VAULT_ID_MATCH, VaultSecret(vpass))])
        with open(inv_path) as f:
            text = f.read()
        m = re.search(r'ansible_become_pass:\s*!vault\s*\|\s*\n([\s\S]+?)(?=\n\s*[a-zA-Z_#]|\Z)', text)
        if m:
            lines = [line.strip() for line in m.group(1).splitlines() if line.strip()]
            pwd = vault.decrypt('\n'.join(lines)).decode('utf-8')
    except Exception as e:
        print(f'VAULT_ERROR:{e}')
        sys.exit(0)

if not pwd and os.path.exists(inv_path):
    with open(inv_path) as f:
        for line in f:
            if 'ansible_become_pass:' in line and '!vault' not in line:
                val = line.split('ansible_become_pass:', 1)[1].strip().strip('"\'')
                if val and not val.startswith('{{'):
                    pwd = val
                    break

if not pwd:
    print('NO_PASS')
    sys.exit(0)

try:
    p = subprocess.run(['sudo', '-S', '-v'], input=(pwd + '\n').encode('utf-8'), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=4)
    if p.returncode == 0:
        print('OK')
    else:
        print('INVALID_PASS')  # never echo the password itself (it would reach the terminal, logs and Telegram)
except Exception:
    print('TIMEOUT')
EOF
)

if [[ "$SUDO_CHECK_OUTPUT" == INVALID_PASS* ]]; then
    ERR_MSG="Mật khẩu 'ansible_become_pass' không chính xác (sudo từ chối mật khẩu đã cấu hình)."
    echo ""
    echo -e "\033[0;31m❌ [LỖI SUDO] ${ERR_MSG}\033[0m"
    echo -e "\033[0;33m   Lệnh sudo trên máy từ chối mật khẩu hiện tại.\033[0m"
    echo -e "\033[0;36m   👉 Vui lòng chạy lệnh sau với MẬT KHẨU ĐĂNG NHẬP MÁY TÍNH THẬT của bạn để mã hóa lại:\033[0m"
    echo -e "      cd ${SCRIPT_DIR}"
    echo -e "      ansible-vault encrypt_string --vault-password-file .vault_pass '<MẬT_KHẨU_SUDO_THẬT>' --name ansible_become_pass"
    echo ""
    if [ "$NOTIFY" = "true" ]; then
        TAIL_ERR="❌ [LỖI SUDO] ${ERR_MSG}
Lệnh sudo trên máy từ chối mật khẩu hiện tại.
Cách khắc phục:
cd ${SCRIPT_DIR}
ansible-vault encrypt_string --vault-password-file .vault_pass '<MẬT_KHẨU_SUDO_THẬT>' --name ansible_become_pass"
        python3 - "${SCRIPT_DIR}" "${ERR_MSG}" "${TAIL_ERR}" << 'EOF' 2>/dev/null || true
import sys, os
sys.path.insert(0, os.path.join(sys.argv[1], 'scripts'))
import telegram_notify as tn
err = sys.argv[2]
tail = sys.argv[3]
tn.notify_deploy_failure('Pre-flight Sudo Validation', err, tail_logs=tail)
EOF
    fi
    exit 1
elif [ "$SUDO_CHECK_OUTPUT" = "TIMEOUT" ]; then
    ERR_MSG="Xác thực quyền sudo bị quá thời gian chờ (timeout)!"
    echo -e "\033[0;31m❌ [LỖI SUDO] ${ERR_MSG}\033[0m"
    if [ "$NOTIFY" = "true" ]; then
        python3 - "${SCRIPT_DIR}" "${ERR_MSG}" << 'EOF' 2>/dev/null || true
import sys, os
sys.path.insert(0, os.path.join(sys.argv[1], 'scripts'))
import telegram_notify as tn
err = sys.argv[2]
tn.notify_deploy_failure('Pre-flight Sudo Validation', err, tail_logs="Xác thực quyền sudo bị quá thời gian chờ (timeout > 4s).")
EOF
    fi
    exit 1
fi

# 2. Scope & Target Node Resolution
if [ -n "$TARGET_NODE" ]; then
    RESOLVED_HOST=$(resolve_target_host "$TARGET_NODE")
    echo "🎯 Targeting specific node: ${RESOLVED_HOST} (from parameter: ${TARGET_NODE})"
    if [[ "$ACTION" =~ ^(setup|deploy|restart|reset)$ ]]; then
        EXTRA_ANSIBLE_ARGS+=(--limit "localhost,${RESOLVED_HOST}")
    else
        EXTRA_ANSIBLE_ARGS+=(--limit "${RESOLVED_HOST}")
    fi
    if [ "$ACTION" = "open_ports" ]; then
        EXTRA_ANSIBLE_ARGS+=(--tags "open_ports")
    fi
elif [ "$ACTION" = "open_ports" ]; then
    echo "🛡️  Targeting Firewall (UFW) port opening..."
    if [ "$EXEC_ONLY" = "true" ]; then
        EXTRA_ANSIBLE_ARGS+=(--tags "exec_clusters,open_ports")
    elif [ "$PARENT_ONLY" = "true" ]; then
        EXTRA_ANSIBLE_ARGS+=(--tags "parent_chain,open_ports")
    else
        EXTRA_ANSIBLE_ARGS+=(--tags "open_ports")
    fi
elif [ "$EXEC_ONLY" = "true" ]; then
    echo "⛓️  Targeting Execution Clusters only (Child Chains - EVM Chain ID 991)"
    if [[ "$ACTION" =~ ^(setup|deploy|restart|reset)$ ]]; then
        EXTRA_ANSIBLE_ARGS+=(--tags "build,exec_clusters")
    else
        EXTRA_ANSIBLE_ARGS+=(--tags "exec_clusters")
    fi
elif [ "$PARENT_ONLY" = "true" ]; then
    echo "🏛️  Targeting Parent Chain only"
    if [[ "$ACTION" =~ ^(setup|deploy|restart|reset)$ ]]; then
        EXTRA_ANSIBLE_ARGS+=(--tags "build,parent_chain")
    else
        EXTRA_ANSIBLE_ARGS+=(--tags "parent_chain")
    fi
fi

# 2.5. Tạm dừng monitors và dọn dẹp state nếu đang thực hiện thao tác làm gián đoạn node
if [[ "$ACTION" =~ ^(setup|deploy|restart|reset|clean|stop)$ ]]; then
    stop_cluster_monitors
    if [[ "$ACTION" =~ ^(reset|clean)$ ]]; then
        clean_monitor_state
    fi
fi

# 3. Execute Ansible Playbook
echo "⚙️ Executing Ansible Playbook (${ACTION}, Environment: ${METANODE_ENV})..."
set +e
ansible-playbook \
    -i "$INVENTORY" \
    "$PLAYBOOK" \
    --extra-vars "deploy_action=${ACTION} open_ports=${OPEN_PORTS_FLAG} run_integration_tests=false metanode_env=${METANODE_ENV} node_env=${NODE_ENV}" \
    "${VAULT_CLUSTER_ARGS[@]}" \
    "${EXTRA_ANSIBLE_ARGS[@]}" 2>&1 | tee "$LOG_FILE"
ANSIBLE_RC=${PIPESTATUS[0]}
set -e

if [ $ANSIBLE_RC -ne 0 ]; then
    echo "❌ Ansible playbook thất bại với mã lỗi ${ANSIBLE_RC}!"
    if [ "$NOTIFY" = "true" ]; then
        TAIL_LOGS=$(tail -n 20 "$LOG_FILE" 2>/dev/null || echo "")
        python3 - "${SCRIPT_DIR}" "${ACTION}" "${ANSIBLE_RC}" "${TAIL_LOGS}" << 'EOF' 2>/dev/null || true
import sys, os
sys.path.insert(0, os.path.join(sys.argv[1], 'scripts'))
import telegram_notify as tn
stage = f"Ansible Playbook ({sys.argv[2]})"
err = f"Exit code: {sys.argv[3]}"
tail = sys.argv[4]
tn.notify_deploy_failure(stage, err, tail_logs=tail)
EOF
    fi
    exit $ANSIBLE_RC
fi

DEPLOY_END_TIME=$(date +%s)
TOTAL_DEPLOY_DURATION=$((DEPLOY_END_TIME - DEPLOY_START_TIME))

# Nếu là action stop hoặc clean: kết thúc ngay mà không cần check RPC status
if [ "$ACTION" = "stop" ]; then
    stop_cluster_monitors
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "🛑 ĐÃ DỪNG TOÀN BỘ TIẾN TRÌNH CỤM METANODE THÀNH CÔNG (${TOTAL_DEPLOY_DURATION}s)!"
    echo "═══════════════════════════════════════════════════════════════"
    exit 0
fi

if [ "$ACTION" = "clean" ]; then
    stop_cluster_monitors
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "🧹 ĐÃ DỌN DẸP DỮ LIỆU CỤM METANODE THÀNH CÔNG (${TOTAL_DEPLOY_DURATION}s)!"
    echo "═══════════════════════════════════════════════════════════════"
    exit 0
fi

if [ "$ACTION" = "open_ports" ]; then
    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "🛡️  ĐÃ MỞ THÔNG TẤT CẢ CÁC CỔNG TƯỜNG LỬA (UFW) TRÊN CỤM SERVER THÀNH CÔNG (${TOTAL_DEPLOY_DURATION}s)!"
    echo "═══════════════════════════════════════════════════════════════"
    if [ "$NOTIFY" = "true" ]; then
        send_tele "tn.send_telegram_message(html_message='🛡️ <b>[METANODE CLUSTER FIREWALL]</b>\nĐã mở thông tất cả các cổng tường lửa (UFW) trên cụm server thành công (<b>${TOTAL_DEPLOY_DURATION}s</b>)!')"
    fi
    exit 0
fi

# 3. Export RPC JSON & Notify Services Ready
echo "📢 Xuất cấu hình cổng vào ${RPC_NODES_FILE} và gửi thông báo dịch vụ sẵn sàng lên Telegram..."
python3 "${SCRIPT_DIR}/scripts/parse_inventory.py" "$INVENTORY" export "$RPC_NODES_FILE"
python3 -c "
import sys; sys.path.insert(0, '${SCRIPT_DIR}/scripts')
import parse_inventory as pi
import telegram_notify as tn

info = pi.parse_inventory('${INVENTORY}')
if '${NOTIFY}' == 'true':
    tn.notify_services_ready(info, duration_secs=${TOTAL_DEPLOY_DURATION}, rpc_nodes_path='${RPC_NODES_FILE}')
" || true

echo ""
echo "✅ TRIỂN KHAI CỤM METANODE HOÀN TẤT TRONG ${TOTAL_DEPLOY_DURATION}s!"
check_status

# 4. Kích hoạt Monitor ngầm (start_monitors.sh & block_hash_checker)
start_cluster_monitors

# Tự động đồng bộ cấu hình sang metanode-suite (update-ip.sh) nếu có
UPDATE_IP_SCRIPT="${METANODE_ROOT}/../metanode-suite/scripts/update-ip/update-ip.sh"
if [ -f "$UPDATE_IP_SCRIPT" ]; then
    echo "🔄 Đang đồng bộ cấu hình sang metanode-suite (update-ip.sh)..."
    bash "$UPDATE_IP_SCRIPT" --chain 991 >/dev/null 2>&1 || true
    echo "✅ Đã tự động cập nhật cấu hình test-chain & configs trong metanode-suite!"
fi

# 5. Run Post-Deployment Tests if requested
if [ "$RUN_TESTS" = "true" ]; then
    run_tests_suite
fi

echo "═══════════════════════════════════════════════════════════════"
echo "🎉 TẤT CẢ CÁC TÁC VỤ ĐÃ ĐƯỢC THỰC HIỆN THÀNH CÔNG VÀ AN TOÀN!"
echo "═══════════════════════════════════════════════════════════════"
