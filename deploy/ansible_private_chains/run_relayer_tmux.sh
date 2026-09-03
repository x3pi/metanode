#!/usr/bin/env bash
# ==============================================================================
# run_relayer_tmux.sh — Quản lý vòng đời Cross-Chain Relayer (Start/Stop/Restart/Status)
# Tự động đọc cấu hình từ gateway_register.json
# ==============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
EXECUTION_DIR="$REPO_ROOT/execution"
BIN_PATH="$EXECUTION_DIR/cross_chain_relayer"
GW_CONFIG_JSON="$SCRIPT_DIR/gateway_register.json"
SESSION_NAME="relayer"
LOG_FILE="$SCRIPT_DIR/relayer.log"

ACTION="${1:-start}"

# Chuẩn hóa tham số (cho phép dùng cả --start, --stop, start, stop)
case "$ACTION" in
    --start|start)
        ACTION="start"
        ;;
    --stop|stop)
        ACTION="stop"
        ;;
    --restart|restart)
        ACTION="restart"
        ;;
    --status|status)
        ACTION="status"
        ;;
    --attach|attach)
        ACTION="attach"
        ;;
    --logs|logs|--log|log)
        ACTION="logs"
        ;;
    --help|-h|help)
        echo "═══════════════════════════════════════════════════════════════"
        echo "🌐 METANODE CROSS-CHAIN RELAYER — TMUX CONTROLLER"
        echo "═══════════════════════════════════════════════════════════════"
        echo "Cách dùng: $0 [ACTION]"
        echo ""
        echo "Các lệnh hỗ trợ:"
        echo "  start,   --start    Khởi động Relayer ngầm trong tmux (Mặc định)"
        echo "  stop,    --stop     Dừng Relayer và đóng tmux session"
        echo "  restart, --restart  Khởi động lại Relayer"
        echo "  status,  --status   Xem trạng thái và 10 dòng log mới nhất"
        echo "  logs,    --logs     Theo dõi log realtime (tail -f)"
        echo "  attach,  --attach   Vào màn hình tương tác tmux"
        echo ""
        echo "Ví dụ:"
        echo "  $0 start    # Bật Relayer"
        echo "  $0 stop     # Tắt Relayer"
        echo "  $0 status   # Xem tình trạng"
        exit 0
        ;;
    *)
        echo "❌ Lựa chọn không hợp lệ: '$ACTION'"
        echo "👉 Dùng: $0 [start | stop | restart | status | logs | attach]"
        exit 1
        ;;
esac

# Hàm dừng triệt để tiến trình Relayer và tmux
stop_relayer() {
    local stopped=false
    if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
        tmux kill-session -t "$SESSION_NAME"
        stopped=true
    fi

    # Quét dọn bất kỳ PID cross_chain_relayer nào còn sót lại
    if pgrep -f "cross_chain_relayer" > /dev/null 2>&1; then
        pkill -9 -f "cross_chain_relayer" 2>/dev/null || true
        stopped=true
    fi

    if [ "$stopped" = true ]; then
        echo "🛑 Đã DỪNG hoàn toàn Relayer và tmux session '$SESSION_NAME'."
    else
        echo "ℹ️  Relayer và tmux session '$SESSION_NAME' hiện không chạy."
    fi
}

# 1. Xử lý lệnh STOP
if [ "$ACTION" = "stop" ]; then
    stop_relayer
    exit 0
fi

# 2. Xử lý lệnh STATUS
if [ "$ACTION" = "status" ]; then
    if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
        echo "✅ Relayer tmux session '$SESSION_NAME' ĐANG CHẠY (Active)."
        if [ -f "$LOG_FILE" ]; then
            echo ""
            echo "📋 10 dòng log mới nhất ($LOG_FILE):"
            echo "──────────────────────────────────────────────────────────────"
            tail -n 10 "$LOG_FILE"
            echo "──────────────────────────────────────────────────────────────"
        fi
    else
        echo "❌ Relayer tmux session '$SESSION_NAME' KHÔNG CHẠY."
    fi
    exit 0
fi

# 3. Xử lý lệnh ATTACH
if [ "$ACTION" = "attach" ]; then
    if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
        exec tmux attach -t "$SESSION_NAME"
    else
        echo "❌ Session '$SESSION_NAME' không tồn tại. Hãy khởi động bằng '$0 start' trước."
        exit 1
    fi
fi

# 4. Xử lý lệnh LOGS
if [ "$ACTION" = "logs" ]; then
    if [ -f "$LOG_FILE" ]; then
        echo "👀 Đang theo dõi log realtime ($LOG_FILE)... (Bấm Ctrl+C để thoát)"
        exec tail -f "$LOG_FILE"
    else
        echo "❌ Chưa có file log tại $LOG_FILE"
        exit 1
    fi
fi

# 5. Xử lý lệnh START / RESTART
GW_CONFIG_JSON="$SCRIPT_DIR/gateway_register.json"
INVENTORY_YML="$SCRIPT_DIR/inventory.yml"

if [ ! -f "$GW_CONFIG_JSON" ] && [ -f "$INVENTORY_YML" ]; then
    echo "⚙️ Đang khởi tạo $GW_CONFIG_JSON từ $INVENTORY_YML ..."
    python3 -c "
import yaml, json
with open('$INVENTORY_YML') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
root_rpc = data.get('all', {}).get('vars', {}).get('root_anchor_rpc', 'http://127.0.0.1:10746')
chains = []
for h in hosts.values():
    if isinstance(h, dict) and 'chain_id' in h:
        cid = int(h['chain_id'])
        ip = h.get('ansible_host', '127.0.0.1')
        rpc_port = int(h.get('rpc_port', 8546))
        chains.append({
            'chain_id': cid,
            'rpc_url': f'http://{ip}:{rpc_port}',
            'quorum_threshold': 6667,
            'validators': []
        })
out = {
    'root_anchor_rpc': root_rpc,
    'submitter_key': 'd3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9',
    'genesis_supply': '400000000000000000000000000',
    'per_chain_allocation': '100000000000000000000000000',
    'fund_genesis': True,
    'chains': chains
}
with open('$GW_CONFIG_JSON', 'w') as f:
    json.dump(out, f, indent=2)
"
fi

if [ ! -f "$GW_CONFIG_JSON" ]; then
    echo "❌ Lỗi: Không tìm thấy file $GW_CONFIG_JSON"
    echo "👉 Hãy chạy deploy private chains trước: ./deploy_private_chains.sh --setup"
    exit 1
fi

echo "🔨 Biên dịch binary cross_chain_relayer..."
(cd "$EXECUTION_DIR" && go build -o "$BIN_PATH" ./cmd/tool/cross_chain_relayer)

# Dừng phiên cũ trước khi start mới
stop_relayer

# Xóa log cũ để khởi động từ log sạch
rm -f "$LOG_FILE"
touch "$LOG_FILE"

echo "═══════════════════════════════════════════════════════════════"
echo "🚀 KHỞI CHẠY CROSS-CHAIN RELAYER TRONG TMUX"
echo "   - Session Name:    $SESSION_NAME"
echo "   - Config File:     $GW_CONFIG_JSON"
echo "   - Log File:        $LOG_FILE"
echo "═══════════════════════════════════════════════════════════════"

# Tạo lệnh chạy ngầm với tee ghi log mới (sử dụng --config)
CMD="cd '$EXECUTION_DIR' && '$BIN_PATH' --config '$GW_CONFIG_JSON' 2>&1 | tee '$LOG_FILE'"

# Khởi tạo tmux detached session
tmux new-session -d -s "$SESSION_NAME" bash -c "$CMD"

# Chờ 1 giây kiểm tra
sleep 1

if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
    echo "🎉 Relayer đã khởi chạy thành công trong tmux session '$SESSION_NAME'!"
    echo ""
    echo "💡 Các lệnh quản lý tiện lợi:"
    echo "  👉 Tắt Relayer:        $0 stop"
    echo "  👉 Khởi động lại:      $0 restart"
    echo "  👉 Xem log realtime:   $0 logs"
    echo "  👉 Kiểm tra trạng thái: $0 status"
    echo "  👉 Vào tmux tương tác: $0 attach"
    echo ""
    echo "📋 5 dòng log khởi động đầu tiên:"
    tail -n 5 "$LOG_FILE" 2>/dev/null || true
else
    echo "❌ Lỗi: Tmux session '$SESSION_NAME' không thể khởi động. Hãy kiểm tra lại log."
    exit 1
fi
