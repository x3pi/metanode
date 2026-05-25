#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  LOCAL TEST SUITE — Test toàn bộ prod scripts trên 1 máy         ║
# ║                                                                   ║
# ║  Script này test từng bước như production nhưng trên localhost.   ║
# ║  Dùng mtn-orchestrator.sh bên dưới (đã được test kỹ).            ║
# ║                                                                   ║
# ║  Usage:                                                           ║
# ║    ./local_test.sh                  # Chạy tất cả tests           ║
# ║    ./local_test.sh --status         # Chỉ xem status              ║
# ║    ./local_test.sh --start          # Start 5 nodes local         ║
# ║    ./local_test.sh --stop           # Stop 5 nodes local          ║
# ║    ./local_test.sh --restart        # Stop + fresh start          ║
# ║    ./local_test.sh --monitor        # Chạy health check 1 lần     ║
# ║    ./local_test.sh --test-systemd   # Test systemd service (cần sudo) ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ORCHESTRATOR="$METANODE_ROOT/consensus/metanode/scripts/mtn-orchestrator.sh"
MONITOR_SCRIPT="$SCRIPT_DIR/prod_monitor.sh"
SETUP_SERVICE_SCRIPT="$SCRIPT_DIR/prod_setup_nodes.sh"

# ─── Colors ──────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_step()  { echo -e "  ${BLUE}►${NC} $*"; }
log_phase() { echo -e "\n${CYAN}${BOLD}═══ $* ═══${NC}"; }
log_pass()  { echo -e "  ${GREEN}✅ PASS${NC} — $*"; }
log_fail()  { echo -e "  ${RED}❌ FAIL${NC} — $*"; }
log_skip()  { echo -e "  ${YELLOW}⏭  SKIP${NC} — $*"; }

PASS_COUNT=0
FAIL_COUNT=0

check() {
    local desc="$1"; shift
    if "$@" > /dev/null 2>&1; then
        log_pass "$desc"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        log_fail "$desc"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

# ─── Test: Node RPC health check ─────────────────────────────────────
test_rpc_health() {
    log_phase "Test 1: RPC Health Check (5 nodes)"

    declare -A PORTS=([0]=8757 [1]=10747 [2]=10749 [3]=10750 [4]=10748)
    local all_ok=true

    for node in 0 1 2 3 4; do
        local port="${PORTS[$node]}"
        local height
        height=$(curl -sf --max-time 3 \
            -X POST "http://localhost:$port" \
            -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" \
            2>/dev/null || echo "")

        if [ -n "$height" ]; then
            log_pass "Node $node (port $port): block #$height"
            PASS_COUNT=$((PASS_COUNT + 1))
        else
            log_fail "Node $node (port $port): không phản hồi"
            FAIL_COUNT=$((FAIL_COUNT + 1))
            all_ok=false
        fi
    done

    if $all_ok; then
        log_info "✅ Tất cả 5 nodes đang online"
    else
        log_warn "⚠️  Một số nodes không phản hồi — chạy --start trước"
    fi
}

# ─── Test: Block tiến triển (không bị stuck) ─────────────────────────
test_block_progress() {
    log_phase "Test 2: Block Progress (10s)"
    log_step "Lấy block height lần 1..."

    declare -A PORTS=([0]=8757 [1]=10747 [2]=10749 [3]=10750 [4]=10748)
    declare -A H1

    for node in 0 1 2 3 4; do
        H1[$node]=$(curl -sf --max-time 3 \
            -X POST "http://localhost:${PORTS[$node]}" \
            -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" \
            2>/dev/null || echo "0")
    done

    log_step "Chờ 10 giây..."
    sleep 10

    log_step "Lấy block height lần 2..."
    local any_progressed=false
    for node in 0 1 2 3 4; do
        local h2
        h2=$(curl -sf --max-time 3 \
            -X POST "http://localhost:${PORTS[$node]}" \
            -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" \
            2>/dev/null || echo "0")

        local h1="${H1[$node]}"
        if [ "$h2" -gt "$h1" ] 2>/dev/null; then
            log_pass "Node $node: $h1 → $h2 (+$((h2 - h1)) blocks)"
            PASS_COUNT=$((PASS_COUNT + 1))
            any_progressed=true
        elif [ "$h2" -eq "$h1" ] && [ "$h2" -gt "0" ] 2>/dev/null; then
            log_warn "Node $node: stuck at block $h1 (bình thường nếu không có tx)"
        else
            log_fail "Node $node: không lấy được height"
            FAIL_COUNT=$((FAIL_COUNT + 1))
        fi
    done

    if ! $any_progressed; then
        log_warn "Không có node nào tăng block — gửi thử 1 tx để kiểm tra"
    fi
}

# ─── Test: Monitor script ─────────────────────────────────────────────
test_monitor() {
    log_phase "Test 3: Monitor Script"
    log_step "Chạy prod_monitor.sh..."

    ENV_FILE="$SCRIPT_DIR/local_test.env" bash "$MONITOR_SCRIPT"
    log_pass "Monitor script chạy không có lỗi"
    PASS_COUNT=$((PASS_COUNT + 1))
}

# ─── Test: prod_deploy.sh --status ───────────────────────────────────
test_deploy_status() {
    log_phase "Test 4: prod_deploy.sh --status"
    log_step "Chạy prod_deploy.sh --status với local_test.env..."

    ENV_FILE="$SCRIPT_DIR/local_test.env" bash "$SCRIPT_DIR/prod_deploy.sh" --status
    log_pass "prod_deploy.sh --status chạy thành công"
    PASS_COUNT=$((PASS_COUNT + 1))
}

# ─── Test: Systemd service (dry-run, cần sudo) ───────────────────────
test_systemd_dryrun() {
    log_phase "Test 5: Systemd Service (dry-run)"

    if ! command -v systemctl &>/dev/null; then
        log_skip "systemctl không có — không phải systemd OS"
        return
    fi

    # Chỉ test --status (không install thật)
    log_step "Kiểm tra service files đã cài chưa..."
    local installed=0
    for node in 0 1 2 3 4; do
        if [ -f "/etc/systemd/system/metanode-node${node}.service" ]; then
            installed=$((installed + 1))
            log_pass "metanode-node${node}.service đã được cài"
            PASS_COUNT=$((PASS_COUNT + 1))
        else
            log_warn "metanode-node${node}.service chưa cài (chạy: ./prod_setup_nodes.sh $node)"
        fi
    done

    if [ $installed -eq 0 ]; then
        log_step "Chưa có service nào. Để cài thử Node 0:"
        echo ""
        echo "    sudo ./prod_setup_nodes.sh 0"
        echo "    sudo systemctl start metanode-node0"
        echo "    sudo systemctl status metanode-node0"
        echo "    sudo systemctl stop metanode-node0"
        echo ""
    fi
}

# ─── Commands ─────────────────────────────────────────────────────────
cmd_status() {
    log_phase "Status — 5 nodes trên localhost"

    declare -A PORTS=([0]=8757 [1]=10747 [2]=10749 [3]=10750 [4]=10748)
    declare -A SESSIONS

    echo ""
    printf "  %-6s %-12s %-8s %-20s\n" "Node" "tmux" "Port" "Block"
    printf "  %-6s %-12s %-8s %-20s\n" "------" "------------" "--------" "--------------------"

    local alive=0
    for node in 0 1 2 3 4; do
        local port="${PORTS[$node]}"
        local session="go-master-${node}"

        local tmux_status="❌ dead"
        if tmux has-session -t "$session" 2>/dev/null; then
            tmux_status="✅ alive"
        fi

        local height
        height=$(curl -sf --max-time 2 \
            -X POST "http://localhost:$port" \
            -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" \
            2>/dev/null || echo "DOWN")

        if [ "$height" != "DOWN" ]; then
            alive=$((alive + 1))
            printf "  %-6s %-12s %-8s ${GREEN}block #%-20s${NC}\n" "$node" "$tmux_status" "$port" "$height"
        else
            printf "  %-6s %-12s %-8s ${RED}%-20s${NC}\n" "$node" "$tmux_status" "$port" "DOWN"
        fi
    done

    echo ""
    if [ $alive -eq 5 ]; then
        echo -e "  ${GREEN}${BOLD}✅ Tất cả 5/5 nodes đang chạy${NC}"
    elif [ $alive -gt 0 ]; then
        echo -e "  ${YELLOW}${BOLD}⚠️  $alive/5 nodes đang chạy${NC}"
    else
        echo -e "  ${RED}${BOLD}❌ Không có node nào chạy — dùng: ./local_test.sh --start${NC}"
    fi
    echo ""

    echo -e "  ${BLUE}Tmux sessions:${NC}"
    echo "    tmux ls                        # Xem tất cả sessions"
    for node in 0 1 2 3 4; do
        echo "    tmux attach -t go-master-$node    # Attach vào Node $node"
    done
    echo ""
}

cmd_start() {
    log_phase "Start 5 nodes trên localhost"
    log_step "Dùng mtn-orchestrator.sh (đã được test kỹ)..."
    echo ""
    bash "$ORCHESTRATOR" start "$@"
}

cmd_stop() {
    log_phase "Stop 5 nodes (graceful)"
    bash "$ORCHESTRATOR" stop
}

cmd_restart() {
    log_phase "Restart cluster (fresh)"
    bash "$ORCHESTRATOR" restart --fresh "$@"
}

cmd_monitor() {
    log_phase "Health check (chạy 1 lần)"
    ENV_FILE="$SCRIPT_DIR/local_test.env" bash "$MONITOR_SCRIPT"
}

cmd_test_systemd() {
    log_phase "Test systemd service cho Node 0 (cần sudo)"

    if ! command -v systemctl &>/dev/null; then
        log_error "systemctl không có trên hệ thống này"
        exit 1
    fi

    log_step "Cài metanode-node0.service..."
    bash "$SETUP_SERVICE_SCRIPT" 0

    echo ""
    log_step "Kiểm tra service đã cài..."
    sudo systemctl status metanode-node0 --no-pager || true

    echo ""
    echo -e "${YELLOW}Lưu ý:${NC} Không start service ngay vì orchestrator đang chạy."
    echo "  Để test systemd start:"
    echo "    1. ./local_test.sh --stop           # Dừng orchestrator trước"
    echo "    2. sudo systemctl start metanode-node0"
    echo "    3. sudo systemctl status metanode-node0"
    echo "    4. journalctl -u metanode-node0 -f"
    echo "    5. sudo systemctl stop metanode-node0"
}

run_all_tests() {
    log_phase "LOCAL TEST SUITE — Metanode Chain Production Scripts"
    echo -e "  Thời gian: $(date '+%Y-%m-%d %H:%M:%S')"
    echo -e "  Machine: $(hostname)"
    echo ""

    test_rpc_health
    test_block_progress
    test_monitor
    test_deploy_status
    test_systemd_dryrun

    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════╗${NC}"
    echo -e "${BOLD}║  TEST RESULTS                        ║${NC}"
    echo -e "${BOLD}╠══════════════════════════════════════╣${NC}"
    echo -e "${BOLD}║  ${GREEN}PASS: $PASS_COUNT${NC}$(printf '%*s' $((28 - ${#PASS_COUNT})) '')${BOLD}║${NC}"
    echo -e "${BOLD}║  ${RED}FAIL: $FAIL_COUNT${NC}$(printf '%*s' $((28 - ${#FAIL_COUNT})) '')${BOLD}║${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════╝${NC}"
    echo ""

    if [ $FAIL_COUNT -eq 0 ]; then
        echo -e "${GREEN}${BOLD}✅ All tests passed! Scripts sẵn sàng cho production.${NC}"
    else
        echo -e "${YELLOW}${BOLD}⚠️  $FAIL_COUNT test(s) failed — kiểm tra logs bên trên.${NC}"
        exit 1
    fi
}

# ─── Main ─────────────────────────────────────────────────────────────
print_usage() {
    echo ""
    echo -e "${BOLD}Usage:${NC} $0 [COMMAND]"
    echo ""
    echo "  (no args)       Chạy toàn bộ test suite"
    echo "  --status        Xem trạng thái 5 nodes"
    echo "  --start         Start tất cả 5 nodes"
    echo "  --start --fresh Fresh start (xóa data cũ)"
    echo "  --stop          Stop tất cả 5 nodes"
    echo "  --restart       Stop + fresh start"
    echo "  --monitor       Chạy health check 1 lần"
    echo "  --test-systemd  Test systemd service setup (cần sudo)"
    echo ""
    echo "Lệnh xem log:"
    echo "  tmux attach -t go-master-0    # Xem live output Node 0"
    echo "  tail -f $METANODE_ROOT/consensus/metanode/logs/node_0/go-master-stdout.log"
    echo ""
}

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🧪 METANODE LOCAL TEST — Production Scripts             ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"

case "${1:-}" in
    "")               run_all_tests ;;
    --status)         cmd_status ;;
    --start)          shift; cmd_start "$@" ;;
    --stop)           cmd_stop ;;
    --restart)        shift; cmd_restart "$@" ;;
    --monitor)        cmd_monitor ;;
    --test-systemd)   cmd_test_systemd ;;
    --help|-h)        print_usage ;;
    *)
        log_error "Unknown command: $1"
        print_usage
        exit 1
        ;;
esac
