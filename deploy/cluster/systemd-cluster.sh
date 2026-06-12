#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Metanode Systemd Orchestrator
#  Quản lý toàn bộ cluster (0-4 nodes) trên 1 máy qua systemd
#
#  Cách dùng:
#    sudo bash systemd-cluster.sh setup              # Xóa data + cài mới (chạy lần đầu hoặc reset)
#    sudo bash systemd-cluster.sh install            # Cập nhật binary/config, GIỮ NGUYÊN data
#    sudo bash systemd-cluster.sh install --node 0   # Cài 1 node cụ thể
#    sudo bash systemd-cluster.sh start              # Khởi động toàn bộ
#    sudo bash systemd-cluster.sh stop               # Dừng toàn bộ
#    sudo bash systemd-cluster.sh restart            # Restart toàn bộ
#    sudo bash systemd-cluster.sh status             # Xem trạng thái
#    sudo bash systemd-cluster.sh check              # Kiểm tra block height
#    sudo bash systemd-cluster.sh logs 0             # Xem log node 0
#    sudo bash systemd-cluster.sh reset-failed       # Gỡ rate-limit
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

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
# Mảng: NODE_CONFIGS — đường dẫn thư mục chứa cấu hình json/toml tương ứng

NODE_IDS=(0 1 2 3 4)
NODE_TYPES=(validator validator validator validator synconly)
NODE_CONFIGS=(
    "$DEPLOY_DIR/node-0_keys"
    "$DEPLOY_DIR/node-1_keys"
    "$DEPLOY_DIR/node-2_keys"
    "$DEPLOY_DIR/node-3_keys"
    "$DEPLOY_DIR/node-4_keys"
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

        # Bỏ qua nếu service chưa được cài đặt trên máy này
        if ! systemctl list-unit-files | grep -q "^${se}.service"; then
            continue
        fi

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

    local install_dir="/opt/metanode/node-${node_id}"
    local exec_log="${install_dir}/logs/execution/execution.log"
    local cons_log="${install_dir}/logs/consensus/consensus.log"

    case "$layer" in
        execution)
            log_info "Theo dõi log execution Node ${node_id} (Ctrl+C để thoát)..."
            tail -f "$exec_log" 2>/dev/null || echo "Chưa có file log $exec_log"
            ;;
        consensus)
            log_info "Theo dõi log consensus Node ${node_id} (Ctrl+C để thoát)..."
            tail -f "$cons_log" 2>/dev/null | grep -i "commit\|epoch\|peer\|error\|block\|sync" || echo "Chưa có file log $cons_log"
            ;;
        both|*)
            log_info "Theo dõi log cả 2 service Node ${node_id} (Ctrl+C để thoát)..."
            tail -f "$exec_log" "$cons_log" 2>/dev/null || echo "Chưa có file log"
            ;;
    esac
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: SETUP — Xóa toàn bộ data cũ rồi cài mới (fresh start)
#  Dùng khi: Khởi tạo mạng lần đầu, reset testnet, thay genesis.json
#  CẢNH BÁO: Toàn bộ blockchain data sẽ bị XÓA vĩnh viễn!
# ═══════════════════════════════════════════════════════════════════

cmd_setup() {
    local only_node="all"
    local auto_yes=""
    local use_env=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --node) only_node="$2"; shift 2 ;;
            -y|--yes) auto_yes="-y"; shift ;;
            *) shift ;;
        esac
    done

    # Xác nhận xóa data (trừ khi có -y)
    if [ -z "$auto_yes" ]; then
        echo ""
        echo -e "${RED}╔══════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${RED}║  ⚠️  CẢNH BÁO: LỆNH SETUP SẼ XÓA TOÀN BỘ BLOCKCHAIN DATA  ║${NC}"
        echo -e "${RED}║                                                              ║${NC}"
        if [ "$only_node" = "all" ]; then
        echo -e "${RED}║  Xóa data của: TẤT CẢ 5 NODE (0, 1, 2, 3, 4)               ║${NC}"
        else
        echo -e "${RED}║  Xóa data của: NODE ${only_node}                                        ║${NC}"
        fi
        echo -e "${RED}║                                                              ║${NC}"
        echo -e "${RED}║  Bao gồm: execution db, consensus storage, snapshots, logs  ║${NC}"
        echo -e "${RED}║  KHÔNG THỂ KHÔI PHỤC sau khi xóa!                          ║${NC}"
        echo -e "${RED}╚══════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        read -p "  Bạn có chắc chắn muốn xóa data và cài lại? [y/N] " -n 1 -r
        echo ""
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_warn "Đã hủy. Data không bị xóa."
            exit 0
        fi
    fi

    log_phase "SETUP CLUSTER METANODE (FRESH — XÓA DATA CŨ)"

    # Dừng tất cả services trước khi xóa data
    log_info "Dừng tất cả services trước khi xóa..."
    cmd_stop "${only_node}" 2>/dev/null || true
    sleep 2

    # Xóa data
    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        [ "$only_node" != "all" ] && [ "$only_node" != "$nid" ] && continue

        local node_dir="/opt/metanode/node-${nid}"
        if [ -d "$node_dir" ]; then
            log_warn "Đang xóa data Node ${nid}: ${node_dir}/data/ và ${node_dir}/logs/..."
            # Xóa execution data
            rm -rf "${node_dir}/data/execution"
            # Xóa consensus storage
            rm -rf "${node_dir}/data/consensus"
            # Xóa logs
            rm -rf "${node_dir}/logs/execution"
            rm -rf "${node_dir}/logs/consensus"
            log_ok "Node ${nid}: data đã xóa sạch"
        else
            log_warn "Node ${nid}: thư mục ${node_dir} chưa tồn tại, bỏ qua"
        fi
    done

    echo ""
    log_info "Bắt đầu cài đặt lại sau khi xóa data..."
    cmd_install --node "${only_node}" $auto_yes
}

# ═══════════════════════════════════════════════════════════════════
#  LỆNH: INSTALL — Cập nhật binary/config, KHÔNG xóa data
#  Dùng khi: Update code, thay đổi cấu hình, cài thêm node mới
# ═══════════════════════════════════════════════════════════════════

cmd_install() {
    local only_node="all"
    local auto_yes=""
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --node) only_node="$2"; shift 2 ;;
            -y|--yes) auto_yes="-y"; shift ;;
            *) shift ;;
        esac
    done

    log_phase "CÀI ĐẶT / CẬP NHẬT CLUSTER METANODE"
    log_info "(Data hiện tại được GIỮ NGUYÊN — dùng 'setup' nếu muốn xóa data)"

    for i in "${!NODE_IDS[@]}"; do
        local nid="${NODE_IDS[$i]}"
        [ "$only_node" != "all" ] && [ "$only_node" != "$nid" ] && continue

        local cfg="${NODE_CONFIGS[$i]}"
        local ntype="${NODE_TYPES[$i]}"

        if [ ! -d "$cfg" ]; then
            log_err "Config directory không tồn tại: $cfg"
            log_err "Tạo keys trước: python3 gen_validator_entry.py --node-id $nid ..."
            continue
        fi
        
        local extra_args="--config-dir $cfg"

        log_info "Đang cài đặt Node ${nid} (${ntype})..."
        bash "$DEPLOY_DIR/install.sh" $extra_args $auto_yes
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
        if [ -f "$cfg/execution.json" ]; then
            rpc_port=$(jq -r '.rpc_port' "$cfg/execution.json" 2>/dev/null | tr -d ':' || true)
        fi

        local se=$(svc_exec $nid)
        # Bỏ qua nếu service chưa được cài đặt trên máy này
        if ! systemctl list-unit-files | grep -q "^${se}.service"; then
            continue
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
    setup)        cmd_setup "$@" ;;
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
        echo "  setup                XÓA DATA + cài mới (lần đầu / reset testnet)"
        echo "  setup --node N       XÓA DATA + cài mới chỉ Node N"
        echo "  setup -y             Không hỏi xác nhận"
        echo "  install              Cập nhật binary/config, GIỮ NGUYÊN data"
        echo "  install --node N     Cập nhật chỉ Node N"
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
        echo "  sudo bash $0 setup               # Lần đầu hoặc reset mạng"
        echo "  sudo bash $0 setup -y            # Reset không hỏi xác nhận"
        echo "  sudo bash $0 install             # Update code, giữ data"
        echo "  sudo bash $0 install --node 0   # Chỉ Node 0"
        echo "  sudo bash $0 setup --node 0 -y  # Setup Node 0"
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
