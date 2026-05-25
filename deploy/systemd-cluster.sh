#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Metanode Systemd Orchestrator
#  Quản lý toàn bộ cluster (0-4 nodes) trên 1 máy qua systemd
#
#  Cách dùng:
#    sudo bash systemd-cluster.sh start              # Khởi động toàn bộ
#    sudo bash systemd-cluster.sh stop               # Dừng toàn bộ
#    sudo bash systemd-cluster.sh restart            # Restart toàn bộ
#    sudo bash systemd-cluster.sh status             # Xem trạng thái
#    sudo bash systemd-cluster.sh install            # Cài đặt lần đầu
#    sudo bash systemd-cluster.sh install --node 0   # Cài 1 node cụ thể
#    sudo bash systemd-cluster.sh logs 0             # Xem log node 0
#    sudo bash systemd-cluster.sh reset-failed       # Gỡ rate-limit
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR"

# ─── Màu sắc ─────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_err()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }
log_phase() { echo -e "\n${BOLD}${CYAN}═══ $* ═══${NC}"; }

# ─── Cấu hình mỗi node ──────────────────────────────────────────
# Mảng: NODE_IDS — danh sách node ID cần quản lý
# Mảng: NODE_TYPES — loại node tương ứng (validator/synconly)
# Mảng: NODE_CONFIGS — đường dẫn file .env tương ứng

NODE_IDS=(0 1 2 3 4)
NODE_TYPES=(validator validator validator validator synconly)
NODE_CONFIGS=(
    "$DEPLOY_DIR/node-0_keys/validator.env"
    "$DEPLOY_DIR/node-1_keys/validator.env"
    "$DEPLOY_DIR/node-2_keys/validator.env"
    "$DEPLOY_DIR/node-3_keys/validator.env"
    "$DEPLOY_DIR/node-4_keys/synconly.env"
)

# Delay giữa các node khi khởi động (giây)
NODE_START_DELAY=3

# ─── Helper: tên service theo NODE_ID ────────────────────────────
svc_exec()      { echo "metanode-execution-${1}"; }
svc_consensus() { echo "metanode-consensus-${1}"; }

# ─── Helper: lấy index của NODE_ID trong NODE_IDS ────────────────
node_index() {
    local target=$1
    for i in "${!NODE_IDS[@]}"; do
        [ "${NODE_IDS[$i]}" = "$target" ] && echo "$i" && return
    done
    echo "-1"
}

# ─── Helper: kiểm tra service có đang chạy không ─────────────────
is_active() { systemctl is-active "$1" >/dev/null 2>&1; }

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: STATUS
# ═══════════════════════════════════════════════════════════════════

cmd_status() {
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║  📊 TRẠNG THÁI CLUSTER METANODE                             ║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""

    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        local ntype="${NODE_TYPES[$i]}"
        local se=$(svc_exec $nid)
        local sc=$(svc_consensus $nid)

        local exec_status=$(systemctl is-active "$se" 2>/dev/null || echo "inactive")
        local cons_status=$(systemctl is-active "$sc" 2>/dev/null || echo "inactive")

        local exec_color=$RED; local cons_color=$RED
        [ "$exec_status" = "active" ] && exec_color=$GREEN
        [ "$cons_status" = "active" ] && cons_color=$GREEN

        printf "  Node %-2s %-11s │ execution: ${exec_color}%-8s${NC} │ consensus: ${cons_color}%-8s${NC}\n" \
            "$nid" "($ntype)" "$exec_status" "$cons_status"
    done
    echo ""
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: START
# ═══════════════════════════════════════════════════════════════════

cmd_start() {
    local only_node="${1:-all}"

    log_phase "KHỞI ĐỘNG CLUSTER METANODE"

    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        [ "$only_node" != "all" ] && [ "$only_node" != "$nid" ] && continue

        local se=$(svc_exec $nid)
        local sc=$(svc_consensus $nid)
        log_info "Node ${nid}: starting ${se}..."

        if ! systemctl list-units --full --all 2>/dev/null | grep -q "$se.service"; then
            log_warn "  Service ${se} chưa được cài (chưa chạy install?). Bỏ qua."
            continue
        fi

        systemctl start "$se" || log_warn "  Không thể start $se"
        sleep 2
        systemctl start "$sc" || log_warn "  Không thể start $sc"

        log_ok "  Node ${nid} started"

        if [ "$only_node" = "all" ] && [ "$i" -lt $(( ${#NODE_IDS[@]} - 1 )) ]; then
            log_info "  Chờ ${NODE_START_DELAY}s trước node tiếp theo..."
            sleep "$NODE_START_DELAY"
        fi
    done

    echo ""
    cmd_status
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: STOP
# ═══════════════════════════════════════════════════════════════════

cmd_stop() {
    local only_node="${1:-all}"

    log_phase "DỪNG CLUSTER METANODE"

    # Dừng ngược thứ tự: consensus trước, execution sau
    for ((i=${#NODE_IDS[@]}-1; i>=0; i--)); do
        local nid="${NODE_IDS[$i]}"
        [ "$only_node" != "all" ] && [ "$only_node" != "$nid" ] && continue

        local se=$(svc_exec $nid)
        local sc=$(svc_consensus $nid)
        log_info "Node ${nid}: stopping..."

        systemctl stop "$sc" 2>/dev/null || true
        systemctl stop "$se" 2>/dev/null || true
        log_ok "  Node ${nid} stopped"
    done

    echo ""
    cmd_status
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: RESTART
# ═══════════════════════════════════════════════════════════════════

cmd_restart() {
    local only_node="${1:-all}"
    cmd_stop "$only_node"
    sleep 2
    cmd_start "$only_node"
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: RESET-FAILED
# ═══════════════════════════════════════════════════════════════════

cmd_reset_failed() {
    log_phase "GỠ BỎ RATE-LIMIT SYSTEMD"
    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        local se=$(svc_exec $nid)
        local sc=$(svc_consensus $nid)
        systemctl reset-failed "$se" 2>/dev/null && log_ok "$se: reset" || true
        systemctl reset-failed "$sc" 2>/dev/null && log_ok "$sc: reset" || true
    done
    log_info "Xong. Giờ bạn có thể start lại."
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: LOGS
# ═══════════════════════════════════════════════════════════════════

cmd_logs() {
    local node_id="${1:-}"
    local layer="${2:-execution}"   # execution | consensus | both

    if [ -z "$node_id" ]; then
        log_err "Dùng: $0 logs <NODE_ID> [execution|consensus|both]"
        exit 1
    fi

    local se=$(svc_exec $node_id)
    local sc=$(svc_consensus $node_id)

    case "$layer" in
        execution)
            log_info "Theo dõi log execution Node ${node_id} (Ctrl+C để thoát)..."
            journalctl -u "$se" -f
            ;;
        consensus)
            log_info "Theo dõi log consensus Node ${node_id} (Ctrl+C để thoát)..."
            journalctl -u "$sc" -f | grep -i "commit\|epoch\|peer\|error\|block" || true
            ;;
        both|*)
            log_info "Theo dõi log cả 2 service Node ${node_id} (Ctrl+C để thoát)..."
            journalctl -u "$se" -u "$sc" -f
            ;;
    esac
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: INSTALL — Cài đặt toàn bộ hoặc 1 node
# ═══════════════════════════════════════════════════════════════════

cmd_install() {
    local only_node="all"
    local fresh=false

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --node) only_node="$2"; shift 2 ;;
            --fresh) fresh=true; shift ;;
            *) shift ;;
        esac
    done

    log_phase "CÀI ĐẶT CLUSTER METANODE"

    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        [ "$only_node" != "all" ] && [ "$only_node" != "$nid" ] && continue

        local cfg="${NODE_CONFIGS[$i]}"
        local ntype="${NODE_TYPES[$i]}"

        if [ ! -f "$cfg" ]; then
            log_err "Config không tồn tại: $cfg"
            log_err "Tạo keys trước: python3 gen_validator_entry.py --node-id $nid ..."
            continue
        fi

        log_info "Đang cài đặt Node ${nid} (${ntype})..."
        bash "$DEPLOY_DIR/install.sh" --config "$cfg"
        log_ok "Node ${nid} đã cài xong"

        if [ "$only_node" = "all" ] && [ "$i" -lt $(( ${#NODE_IDS[@]} - 1 )) ]; then
            echo ""
        fi
    done

    echo ""
    cmd_status
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: CHECK — Kiểm tra block height toàn bộ cluster
# ═══════════════════════════════════════════════════════════════════

cmd_check() {
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║  🔍 KIỂM TRA BLOCK HEIGHT CLUSTER                           ║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════════════════════════════╝${NC}"
    echo ""

    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        local ntype="${NODE_TYPES[$i]}"
        local cfg="${NODE_CONFIGS[$i]}"

        # Lấy RPC port từ config file
        local rpc_port=""
        if [ -f "$cfg" ]; then
            rpc_port=$(grep "^RPC_PORT=" "$cfg" 2>/dev/null | cut -d'=' -f2 | tr -d ':' | tr -d '"' || true)
        fi

        if [ -z "$rpc_port" ]; then
            printf "  Node %-2s (%s): ${YELLOW}RPC port không xác định${NC}\n" "$nid" "$ntype"
            continue
        fi

        local result
        result=$(curl -s --max-time 2 -X POST "http://localhost:${rpc_port}" \
            -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            2>/dev/null || echo "")

        local block=""
        if [ -n "$result" ]; then
            local hex=$(echo "$result" | grep -o '"result":"[^"]*"' | cut -d'"' -f4 || true)
            if [ -n "$hex" ]; then
                block=$(printf "%d" "$hex" 2>/dev/null || echo "$hex")
            fi
        fi

        if [ -n "$block" ]; then
            printf "  Node %-2s (%s) port:%-5s │ ${GREEN}Block #%s${NC}\n" \
                "$nid" "$ntype" "$rpc_port" "$block"
        else
            printf "  Node %-2s (%s) port:%-5s │ ${RED}OFFLINE / không phản hồi${NC}\n" \
                "$nid" "$ntype" "$rpc_port"
        fi
    done
    echo ""
}

# ═══════════════════════════════════════════════════════════════════
#  MAIN — Parse lệnh
# ═══════════════════════════════════════════════════════════════════

if [ "${EUID:-0}" -ne 0 ] && [[ "${1:-}" != "logs" ]] && [[ "${1:-}" != "status" ]] && [[ "${1:-}" != "check" ]]; then
    log_err "Lệnh này cần quyền root (trừ: status, logs, check)"
    log_err "Chạy lại với: sudo bash $0 $*"
    exit 1
fi

CMD="${1:-help}"
shift || true

case "$CMD" in
    start)        cmd_start "$@" ;;
    stop)         cmd_stop "$@" ;;
    restart)      cmd_restart "$@" ;;
    status)       cmd_status ;;
    install)      cmd_install "$@" ;;
    logs)         cmd_logs "$@" ;;
    check)        cmd_check ;;
    reset-failed) cmd_reset_failed ;;
    help|--help|-h)
        echo ""
        echo -e "${BOLD}Metanode Systemd Orchestrator${NC}"
        echo ""
        echo "Cách dùng:"
        echo "  sudo bash $0 <command> [options]"
        echo ""
        echo "Commands:"
        echo "  install              Cài đặt toàn bộ cluster (lần đầu)"
        echo "  install --node N     Cài đặt chỉ Node N"
        echo "  start                Khởi động toàn bộ cluster"
        echo "  start N              Khởi động chỉ Node N"
        echo "  stop                 Dừng toàn bộ cluster"
        echo "  stop N               Dừng chỉ Node N"
        echo "  restart              Restart toàn bộ"
        echo "  restart N            Restart chỉ Node N"
        echo "  status               Xem trạng thái tất cả services"
        echo "  check                Kiểm tra block height qua RPC"
        echo "  logs N               Xem log execution Node N"
        echo "  logs N consensus     Xem log consensus Node N"
        echo "  logs N both          Xem cả 2 service Node N"
        echo "  reset-failed         Gỡ bỏ rate-limit systemd (sau crash loop)"
        echo ""
        echo "Ví dụ:"
        echo "  sudo bash $0 install             # Cài lần đầu"
        echo "  sudo bash $0 start               # Khởi động tất cả"
        echo "  sudo bash $0 status              # Kiểm tra trạng thái"
        echo "  sudo bash $0 check               # Kiểm tra block height"
        echo "  bash $0 logs 0                   # Xem log Node 0"
        echo "  sudo bash $0 restart 4           # Restart Node 4"
        echo ""
        ;;
    *)
        log_err "Lệnh không hợp lệ: $CMD"
        bash "$0" help
        exit 1
        ;;
esac
