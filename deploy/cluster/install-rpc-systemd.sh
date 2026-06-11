#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Metanode RPC Installer
#  Cài đặt RPC Client thành systemd services.
#  Tự động đọc port từ execution.json để cấu hình đúng.
#
#  Cách dùng:
#    sudo bash install-rpc-systemd.sh              # Cài tất cả 5 node
#    sudo bash install-rpc-systemd.sh --node 4     # Chỉ cài Node 4
#    sudo bash install-rpc-systemd.sh --no-build   # Bỏ qua bước build (dùng binary cũ)
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -d "/opt/metanode/rpc-proxy" ] && [ -f "/opt/metanode/rpc-proxy/rpc-client-bin" ]; then
    RPC_DIR="/opt/metanode/rpc-proxy"
else
    RPC_DIR="$(realpath "$SCRIPT_DIR/../../execution/cmd/rpc/cmd/rpc-client")"
fi


# ─── Màu sắc ─────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

log_ok()   { echo -e "${GREEN}[OK]${NC}    $*"; }
log_info() { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_err()  { echo -e "${RED}[ERROR]${NC} $*" >&2; }
log_step() { echo -e "\n${BOLD}${CYAN}══ $* ══${NC}"; }

# ─── Parse args ──────────────────────────────────────────────────
ONLY_NODE="all"
NO_BUILD=false
STOP_ONLY=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --node)       ONLY_NODE="$2"; shift 2 ;;
        --no-build)   NO_BUILD=true; shift ;;
        --stop)       STOP_ONLY=true; shift ;;
        -h|--help)
            echo "Cách dùng: sudo bash install-rpc-systemd.sh [options]"
            echo ""
            echo "Options:"
            echo "  --node N      Chỉ xử lý Node N (mặc định: all)"
            echo "  --no-build    Bỏ qua bước build (dùng binary cũ)"
            echo "  --stop        Chỉ DỪNG các RPC service (không install)"
            echo ""
            echo "Ví dụ:"
            echo "  sudo bash install-rpc-systemd.sh              # Build + cài tất cả"
            echo "  sudo bash install-rpc-systemd.sh --node 4     # Chỉ cài Node 4"
            echo "  sudo bash install-rpc-systemd.sh --no-build   # Cài lại, bỏ qua build"
            echo "  sudo bash install-rpc-systemd.sh --stop       # Dừng tất cả RPC"
            echo "  sudo bash install-rpc-systemd.sh --stop --node 0  # Dừng chỉ Node 0"
            exit 0
            ;;
        *) shift ;;
    esac
done

# ─── Helper: dừng service ────────────────────────────────────────
stop_rpc() {
    local node_id="$1"
    local svc="metanode-rpc-${node_id}"
    if systemctl is-active "$svc" &>/dev/null; then
        sudo systemctl stop "$svc"
        log_ok "Đã dừng $svc"
    else
        log_info "$svc không chạy, bỏ qua"
    fi
}

# ─── Lệnh STOP ───────────────────────────────────────────────────
if $STOP_ONLY; then
    log_step "DỪNG RPC SERVICES"
    for idx in "${!NODE_IDS[@]}"; do
        i="${NODE_IDS[$idx]}"
        [ "$ONLY_NODE" != "all" ] && [ "$ONLY_NODE" != "$i" ] && continue
        stop_rpc "$i"
    done
    echo ""
    log_ok "Hoàn tất dừng RPC."
    exit 0
fi

# ─── Danh sách node ──────────────────────────────────────────────
NODE_IDS=(0 1 2 3 4)
NODE_TYPES=(validator validator validator validator synconly)
NODE_CONFIGS=(
    "$SCRIPT_DIR/../node-0_keys"
    "$SCRIPT_DIR/../node-1_keys"
    "$SCRIPT_DIR/../node-2_keys"
    "$SCRIPT_DIR/../node-3_keys"
    "$SCRIPT_DIR/../node-4_keys"
)

# ─── Kiểm tra jq ─────────────────────────────────────────────────
if ! command -v jq &>/dev/null; then
    log_err "Thiếu công cụ 'jq'. Cài đặt: sudo apt install jq"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════
# BƯỚC 1: Build binary (nếu không có --no-build)
# ═══════════════════════════════════════════════════════════════════
if ! $NO_BUILD; then
    log_step "BƯỚC 1: Build mã nguồn rpc-client"
    cd "$RPC_DIR"
    export PATH="/usr/local/go/bin:/home/abc/go/bin:/usr/local/go1.24.3/bin:/home/abc/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH"
    rm -f rpc-client-bin
    go build -o rpc-client-bin main.go
    chmod +x rpc-client-bin
    log_ok "Build thành công: $RPC_DIR/rpc-client-bin"
else
    log_warn "Bỏ qua bước build (--no-build). Dùng binary hiện có."
fi

# ═══════════════════════════════════════════════════════════════════
# BƯỚC 2: Cấu hình từng node và tạo systemd service
# ═══════════════════════════════════════════════════════════════════
log_step "BƯỚC 2: Cấu hình RPC và tạo Systemd Service"

for idx in "${!NODE_IDS[@]}"; do
    i="${NODE_IDS[$idx]}"
    NODE_TYPE="${NODE_TYPES[$idx]}"
    CONFIG_DIR="${NODE_CONFIGS[$idx]}"
    CONFIG_RPC="$RPC_DIR/config-rpc-node${i}.json"
    CONFIG_TCP="$RPC_DIR/config-client-tcp-node${i}.json"

    # Bỏ qua nếu không phải node được chỉ định
    [ "$ONLY_NODE" != "all" ] && [ "$ONLY_NODE" != "$i" ] && continue

    echo ""
    log_info "Cấu hình RPC Node ${i} (${NODE_TYPE})..."

    # Kiểm tra thư mục keys tồn tại
    if [ ! -d "$CONFIG_DIR" ]; then
        log_warn "Không tìm thấy thư mục: $CONFIG_DIR. Bỏ qua cập nhật JSON (giả định config đã được setup sẵn)."
    else
        # ─── Đọc port từ execution.json ─────────────────────────────────────────
        rpc_port=""
        if [ -f "$CONFIG_DIR/execution.json" ]; then
            rpc_port=$(jq -r '.rpc_port' "$CONFIG_DIR/execution.json" 2>/dev/null | tr -d ':' || true)
        fi
        
        if [ -n "$rpc_port" ]; then
            # Copy templates if missing
            if [ ! -f "$CONFIG_RPC" ]; then
                cp "$SCRIPT_DIR/../single-node/rpc/config-rpc.json" "$CONFIG_RPC"
            fi
            if [ ! -f "$CONFIG_TCP" ]; then
                cp "$SCRIPT_DIR/../single-node/rpc/config-client-tcp.json" "$CONFIG_TCP"
            fi

            log_info "Cập nhật các cổng kết nối (HTTP/WSS/TCP/P2P) cho rpc-client node $i"
            jq ".rpc_server_url = \"http://127.0.0.1:${rpc_port}\" | .wss_server_url = \"ws://127.0.0.1:${rpc_port}/ws\" | .server_port = \":$((8545 + i))\" | .https_port = \":$((8666 + i))\" | .tcp_server_port = \":$((9545 + i))\"" "$CONFIG_RPC" > "${CONFIG_RPC}.tmp" && mv "${CONFIG_RPC}.tmp" "$CONFIG_RPC"

            jq ".connection_address = \"0.0.0.0:$((6200 + i))\"" "$CONFIG_TCP" > "${CONFIG_TCP}.tmp" && mv "${CONFIG_TCP}.tmp" "$CONFIG_TCP"
        else
            log_warn "Không tìm thấy rpc_port hoặc file config RPC/TCP, bỏ qua cập nhật."
        fi
    fi

    # Determine actual user/group if run under sudo
    ACTUAL_USER="${SUDO_USER:-$(whoami)}"
    ACTUAL_GROUP="$(id -gn "$ACTUAL_USER" 2>/dev/null || echo "$ACTUAL_USER")"

    # ─── Tạo thư mục log ──────────────────────────────────────────
    mkdir -p "$RPC_DIR/node${i}_data/logs"
    chown -R "$ACTUAL_USER:$ACTUAL_GROUP" "$RPC_DIR/node${i}_data"

    # ─── Tạo Systemd Service ──────────────────────────────────────
    SERVICE_FILE="/etc/systemd/system/metanode-rpc-${i}.service"
    cat <<EOF | sudo tee "$SERVICE_FILE" >/dev/null
[Unit]
Description=Metanode RPC Proxy — Node ${i} (${NODE_TYPE})
After=network-online.target metanode-execution-${i}.service
Wants=network-online.target

[Service]
Type=simple
User=$ACTUAL_USER
Group=$ACTUAL_GROUP
WorkingDirectory=$RPC_DIR

ExecStart=$RPC_DIR/rpc-client-bin --config config-rpc-node${i}.json --tcp-config config-client-tcp-node${i}.json
ExecStop=/bin/kill -SIGTERM \$MAINPID

# Restart=on-failure
# RestartSec=5s
LimitNOFILE=100000

StandardOutput=append:$RPC_DIR/node${i}_data/logs/systemd.log
StandardError=append:$RPC_DIR/node${i}_data/logs/systemd.log
SyslogIdentifier=metanode-rpc-${i}

[Install]
WantedBy=multi-user.target
EOF

    log_ok "Đã tạo $SERVICE_FILE"
done

# ═══════════════════════════════════════════════════════════════════
# BƯỚC 3: Reload và khởi động services
# ═══════════════════════════════════════════════════════════════════
log_step "BƯỚC 3: Khởi động RPC Services"

sudo systemctl daemon-reload

for idx in "${!NODE_IDS[@]}"; do
    i="${NODE_IDS[$idx]}"
    [ "$ONLY_NODE" != "all" ] && [ "$ONLY_NODE" != "$i" ] && continue

    # Dừng instance cũ trước (nếu đang chạy) để giải phóng port
    stop_rpc "$i"

    sudo systemctl enable "metanode-rpc-${i}.service" 2>/dev/null || true
    sudo systemctl start "metanode-rpc-${i}.service"
    echo "🚀 Đã khởi động metanode-rpc-${i}.service"
done

echo ""
echo -e "${BOLD}🎉 Hoàn tất!${NC}"
if [ "$ONLY_NODE" = "all" ]; then
    echo "👉 Xem log: journalctl -u metanode-rpc-N -f  (N = 0..4)"
else
    echo "👉 Xem log: journalctl -u metanode-rpc-${ONLY_NODE} -f"
fi
