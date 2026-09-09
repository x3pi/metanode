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

ACTION="${1:-start}"

# ------------------------------------------------------------------------------
# Multi-instance support (2026-09-05, production-readiness pass):
# `run_relayer_tmux.sh start` alone still behaves EXACTLY as before (instance name "default" maps
# to the original tmux session name "relayer", the original gateway_register.json, and the
# original relayer.log path -- zero behavior change for existing single-instance deployments).
#
# A second positional argument names a SEPARATE named instance: `run_relayer_tmux.sh start relayer2`
# runs its own tmux session, its own log file, and REQUIRES its own config file
# gateway_register.<name>.json with a relayer_key that differs from the default instance's --
# running two relayer processes off the SAME key is a real nonce-collision hazard (see
# cross_chain_relayer/main.go's devnetDefaultRelayerKeyHex doc comment for a confirmed live
# instance of exactly this bug from two different processes sharing one key). This is what makes
# "does it support running multiple Relayers" actually true at the deployment-tooling layer, not
# just at the contract layer (permissionless attest/claim was already safe -- see
# note/cross_chain_relayer_production_readiness.md).
INSTANCE_NAME="${2:-default}"
if [ "$INSTANCE_NAME" = "default" ]; then
    SESSION_NAME="relayer"
    GW_CONFIG_JSON="$SCRIPT_DIR/gateway_register.json"
    LOG_FILE="$SCRIPT_DIR/relayer.log"
else
    SESSION_NAME="relayer-$INSTANCE_NAME"
    GW_CONFIG_JSON="$SCRIPT_DIR/gateway_register.$INSTANCE_NAME.json"
    LOG_FILE="$SCRIPT_DIR/relayer.$INSTANCE_NAME.log"
fi

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
        echo "Cách dùng: $0 [ACTION] [INSTANCE_NAME]"
        echo ""
        echo "Các lệnh hỗ trợ:"
        echo "  start,   --start    Khởi động Relayer ngầm trong tmux (Mặc định)"
        echo "  stop,    --stop     Dừng Relayer và đóng tmux session"
        echo "  restart, --restart  Khởi động lại Relayer"
        echo "  status,  --status   Xem trạng thái và 10 dòng log mới nhất"
        echo "  logs,    --logs     Theo dõi log realtime (tail -f)"
        echo "  attach,  --attach   Vào màn hình tương tác tmux"
        echo ""
        echo "INSTANCE_NAME (tùy chọn, mặc định 'default'):"
        echo "  Chạy nhiều Relayer song song trên cùng 1 máy, mỗi instance là 1"
        echo "  session tmux + 1 key + 1 log riêng. Instance 'default' dùng đúng"
        echo "  session/log/config như trước (không đổi hành vi cũ). Một instance"
        echo "  đặt tên khác (vd 'relayer2') BẮT BUỘC phải có file cấu hình riêng"
        echo "  gateway_register.<tên>.json với relayer_key KHÁC với instance mặc"
        echo "  định (2 tiến trình dùng chung 1 key sẽ đụng nonce -- xem"
        echo "  note/cross_chain_relayer_production_readiness.md)."
        echo ""
        echo "Ví dụ:"
        echo "  $0 start                # Bật Relayer instance mặc định"
        echo "  $0 start relayer2       # Bật thêm 1 Relayer instance thứ 2"
        echo "  $0 stop relayer2        # Chỉ tắt instance thứ 2, không đụng instance khác"
        echo "  $0 status               # Xem tình trạng instance mặc định"
        exit 0
        ;;
    *)
        echo "❌ Lựa chọn không hợp lệ: '$ACTION'"
        echo "👉 Dùng: $0 [start | stop | restart | status | logs | attach] [INSTANCE_NAME]"
        exit 1
        ;;
esac

# Hàm dừng triệt để tiến trình Relayer và tmux CỦA RIÊNG INSTANCE NÀY.
#
# FIX (2026-09-05, found while adding multi-instance support): the original pkill pattern was a
# bare "cross_chain_relayer" -- it matches EVERY relayer process on the host regardless of
# instance, so starting/stopping instance B would silently kill instance A too, defeating the
# entire point of running multiple relayers. Scoped to this instance's own --config path instead,
# which is unique per instance (see GW_CONFIG_JSON above).
stop_relayer() {
    local stopped=false
    if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
        tmux kill-session -t "$SESSION_NAME"
        stopped=true
    fi

    # Quét dọn bất kỳ PID cross_chain_relayer NÀO ĐANG DÙNG CHÍNH XÁC config file của instance
    # này -- không đụng tới các instance khác đang chạy song song.
    if pgrep -f "cross_chain_relayer.*--config[ =]'?$GW_CONFIG_JSON'?" > /dev/null 2>&1; then
        pkill -9 -f "cross_chain_relayer.*--config[ =]'?$GW_CONFIG_JSON'?" 2>/dev/null || true
        stopped=true
    fi

    if [ "$stopped" = true ]; then
        echo "🛑 Đã DỪNG hoàn toàn Relayer instance '$INSTANCE_NAME' (tmux session '$SESSION_NAME')."
    else
        echo "ℹ️  Relayer instance '$INSTANCE_NAME' (tmux session '$SESSION_NAME') hiện không chạy."
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
INVENTORY_YML="$SCRIPT_DIR/inventory.yml"

# Auto-generation from inventory.yml only ever applies to the default instance's own
# gateway_register.json -- a named instance's gateway_register.<name>.json is a deliberate,
# hand-authored (or ansible-templated) file with its OWN relayer_key, and must already exist.
if [ "$INSTANCE_NAME" = "default" ] && [ ! -f "$GW_CONFIG_JSON" ] && [ -f "$INVENTORY_YML" ]; then
    echo "⚙️ Đang khởi tạo $GW_CONFIG_JSON từ $INVENTORY_YML ..."
    python3 -c "
import yaml, json
with open('$INVENTORY_YML') as f:
    data = yaml.safe_load(f)
hosts = data.get('all', {}).get('children', {}).get('private_chains', {}).get('hosts', {})
root_rpc = data.get('all', {}).get('vars', {}).get('root_anchor_rpc', 'http://127.0.0.1:10746')
submitter_key = data.get('all', {}).get('vars', {}).get('root_anchor_submitter_key', 'd3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9')
# relayer_key (2026-09-04): deliberately NOT defaulted to submitter_key -- see
# cross_chain_relayer/main.go's devnetDefaultRelayerKeyHex doc comment. Empty here falls through
# to that same devnet-only default in the Go tool itself.
relayer_key = data.get('all', {}).get('vars', {}).get('relayer_key', '')
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
    'submitter_key': submitter_key,
    'relayer_key': relayer_key,
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
    if [ "$INSTANCE_NAME" = "default" ]; then
        echo "👉 Hãy chạy deploy private chains trước: ./deploy_private_chains.sh --setup"
    else
        echo "👉 Instance '$INSTANCE_NAME' cần 1 file cấu hình RIÊNG tại đường dẫn trên,"
        echo "   với relayer_key KHÁC instance mặc định. Copy gateway_register.json rồi"
        echo "   đổi relayer_key là cách nhanh nhất, ví dụ:"
        echo "     cp '$SCRIPT_DIR/gateway_register.json' '$GW_CONFIG_JSON'"
        echo "     # rồi sửa relayer_key trong file vừa copy"
    fi
    exit 1
fi

# Guard chống nonce-collision: 1 instance đặt tên khác KHÔNG được dùng chung relayer_key với
# instance mặc định (2 tiến trình độc lập cùng ký bằng 1 key sẽ tự đua nonce với nhau -- xem
# devnetDefaultRelayerKeyHex's doc comment trong cross_chain_relayer/main.go cho 1 ca thật đã xảy
# ra giữa 2 tiến trình khác nhau dùng chung 1 key).
if [ "$INSTANCE_NAME" != "default" ] && [ -f "$SCRIPT_DIR/gateway_register.json" ]; then
    DUP_KEY_CHECK=$(python3 -c "
import json
def load(p):
    try:
        with open(p) as f:
            return json.load(f).get('relayer_key', '')
    except Exception:
        return ''
a = load('$SCRIPT_DIR/gateway_register.json')
b = load('$GW_CONFIG_JSON')
print('DUP' if a and b and a == b else 'OK')
" 2>/dev/null || echo "OK")
    if [ "$DUP_KEY_CHECK" = "DUP" ]; then
        echo "❌ Lỗi: instance '$INSTANCE_NAME' ($GW_CONFIG_JSON) dùng relayer_key TRÙNG với"
        echo "   instance mặc định ($SCRIPT_DIR/gateway_register.json)."
        echo "👉 Mỗi instance chạy song song BẮT BUỘC phải có relayer_key riêng, nếu không 2"
        echo "   tiến trình sẽ tự đua nonce với nhau và giao dịch của bên thua sẽ bị rớt."
        exit 1
    fi
fi

echo "🔨 Biên dịch binary cross_chain_relayer..."
(cd "$EXECUTION_DIR" && go build -o "$BIN_PATH" ./cmd/tool/cross_chain_relayer)

# Dừng phiên cũ (CHỈ của instance này) trước khi start mới
stop_relayer

# Xóa log cũ để khởi động từ log sạch
rm -f "$LOG_FILE"
touch "$LOG_FILE"

# Metrics/health server (2026-09-05): mỗi instance PHẢI dùng 1 cổng riêng, nếu không instance
# khởi động sau sẽ bind lỗi. Instance mặc định lấy :9090 làm mặc định tiện dụng cho triển khai
# đơn-instance hiện có; mọi instance đặt tên khác mặc định TẮT hẳn endpoint metrics trừ khi được
# set tường minh, để không bao giờ vô tình đụng cổng với instance khác. Đặt biến môi trường
# RELAYER_METRICS_ADDR trước khi gọi script để tuỳ chỉnh (vd RELAYER_METRICS_ADDR=:9091).
if [ "$INSTANCE_NAME" = "default" ]; then
    METRICS_ADDR="${RELAYER_METRICS_ADDR:-:9090}"
else
    METRICS_ADDR="${RELAYER_METRICS_ADDR:-}"
fi
EXTRA_ARGS="${RELAYER_EXTRA_ARGS:-}"

echo "═══════════════════════════════════════════════════════════════"
echo "🚀 KHỞI CHẠY CROSS-CHAIN RELAYER TRONG TMUX"
echo "   - Instance Name:   $INSTANCE_NAME"
echo "   - Session Name:    $SESSION_NAME"
echo "   - Config File:     $GW_CONFIG_JSON"
echo "   - Log File:        $LOG_FILE"
echo "   - Metrics Addr:    ${METRICS_ADDR:-(disabled)}"
echo "═══════════════════════════════════════════════════════════════"

# Tạo lệnh chạy ngầm với tee ghi log mới (sử dụng --config)
CMD="cd '$EXECUTION_DIR' && '$BIN_PATH' --config '$GW_CONFIG_JSON' --metrics-addr '$METRICS_ADDR' $EXTRA_ARGS 2>&1 | tee '$LOG_FILE'"

# Khởi tạo tmux detached session
tmux new-session -d -s "$SESSION_NAME" bash -c "$CMD"

# Chờ 1 giây kiểm tra
sleep 1

if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
    echo "🎉 Relayer đã khởi chạy thành công trong tmux session '$SESSION_NAME'!"
    echo ""
    echo "💡 Các lệnh quản lý tiện lợi:"
    echo "  👉 Tắt Relayer:        $0 stop $INSTANCE_NAME"
    echo "  👉 Khởi động lại:      $0 restart $INSTANCE_NAME"
    echo "  👉 Xem log realtime:   $0 logs $INSTANCE_NAME"
    echo "  👉 Kiểm tra trạng thái: $0 status $INSTANCE_NAME"
    echo "  👉 Vào tmux tương tác: $0 attach $INSTANCE_NAME"
    echo ""
    echo "📋 5 dòng log khởi động đầu tiên:"
    tail -n 5 "$LOG_FILE" 2>/dev/null || true
else
    echo "❌ Lỗi: Tmux session '$SESSION_NAME' không thể khởi động. Hãy kiểm tra lại log."
    exit 1
fi
