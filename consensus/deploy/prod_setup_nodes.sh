#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  PROD SETUP NODES — Cài systemd service cho node trên server hiện tại ║
# ║                                                                   ║
# ║  Chạy script này TRỰC TIẾP trên từng server production.          ║
# ║  KHÔNG chạy remote — cần quyền sudo local.                       ║
# ║                                                                   ║
# ║  Usage:                                                           ║
# ║    ./prod_setup_nodes.sh 0 1        # Cài Node 0 + Node 1        ║
# ║    ./prod_setup_nodes.sh 2          # Chỉ cài Node 2             ║
# ║    ./prod_setup_nodes.sh --all      # Cài tất cả node            ║
# ║    ./prod_setup_nodes.sh --remove 0 # Gỡ service Node 0          ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ─── Paths ───────────────────────────────────────────────────────────
# Tự động suy ra từ vị trí script (consensus/deploy/ → metanode root)
METANODE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_DIR="$METANODE_ROOT/execution/cmd/simple_chain"
LOG_BASE="$METANODE_ROOT/consensus/metanode/logs"
SCRIPTS_DIR="$METANODE_ROOT/consensus/metanode/scripts"

# ─── Colors ──────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_step()  { echo -e "  ${BLUE}►${NC} $*"; }

# ─── RPC ports (must match config-master-nodeN.json) ─────────────────
declare -A RPC_PORTS=([0]=8757 [1]=10747 [2]=10749 [3]=10750 [4]=10748)

# ─── Validate ────────────────────────────────────────────────────────
check_prereqs() {
    if [ ! -f "$GO_DIR/simple_chain" ]; then
        log_error "Binary not found: $GO_DIR/simple_chain"
        log_error "Run prod_deploy.sh --build first, or push binary to this server."
        exit 1
    fi
    if ! command -v systemctl &>/dev/null; then
        log_error "systemctl not found — is this a systemd-based system?"
        exit 1
    fi
    log_step "Binary found: $GO_DIR/simple_chain ($(du -sh "$GO_DIR/simple_chain" | cut -f1))"
}

# ─── Install systemd unit for one node ───────────────────────────────
install_service() {
    local node_id="$1"
    local service_name="metanode-node${node_id}"
    local log_dir="$LOG_BASE/node_$node_id"
    local xapian_path="sample/node${node_id}/data/data/xapian_node"
    local service_file="/etc/systemd/system/${service_name}.service"

    log_step "Installing $service_name.service..."

    # Create log directory
    mkdir -p "$log_dir"

    # Write unit file
    sudo tee "$service_file" > /dev/null << UNIT
[Unit]
Description=Metanode Chain Node ${node_id} (Go Master + Rust FFI)
Documentation=https://github.com/metanode/metanode
After=network-online.target
Wants=network-online.target

# Prevent too many restarts in a short period
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User=${USER}
Group=${USER}
WorkingDirectory=${GO_DIR}

ExecStart=${GO_DIR}/simple_chain -config=config-master-node${node_id}.json

# CRITICAL: Must be > SHUTDOWN_TIMEOUT (45s in mtn-orchestrator.sh)
# Allows Go Master to: StopWait(12s) → FlushAll → CloseAll → exit
# Without sufficient time, SIGKILL may corrupt PebbleDB!
ExecStop=/bin/kill -SIGTERM \$MAINPID
TimeoutStopSec=90

# Restart policy: auto-restart on crash, not on clean exit
Restart=on-failure
RestartSec=15s

# Environment variables (matches mtn-orchestrator.sh)
Environment=RUST_BACKTRACE=full
Environment=GOTRACEBACK=crash
Environment=GOTOOLCHAIN=go1.23.5
Environment=XAPIAN_BASE_PATH=${xapian_path}
Environment=MVM_LOG_DIR=${log_dir}

# Resource limits (replaces 'ulimit -n 100000' in orchestrator)
LimitNOFILE=100000
LimitCORE=infinity

# Logging: stdout/stderr → journald
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${service_name}

[Install]
WantedBy=multi-user.target
UNIT

    # Reload systemd and enable
    sudo systemctl daemon-reload
    sudo systemctl enable "$service_name"

    log_info "  ✅ ${service_name}.service installed"
    echo ""
    echo "    Manage with:"
    echo "      sudo systemctl start   ${service_name}"
    echo "      sudo systemctl stop    ${service_name}    # graceful (waits 90s)"
    echo "      sudo systemctl status  ${service_name}"
    echo "      journalctl -u ${service_name} -f"
    echo ""
}

# ─── Remove systemd unit for one node ────────────────────────────────
remove_service() {
    local node_id="$1"
    local service_name="metanode-node${node_id}"
    local service_file="/etc/systemd/system/${service_name}.service"

    log_step "Removing $service_name..."

    # Stop if running
    if sudo systemctl is-active --quiet "$service_name" 2>/dev/null; then
        log_warn "  Service is running — stopping first..."
        sudo systemctl stop "$service_name" || true
    fi

    sudo systemctl disable "$service_name" 2>/dev/null || true
    sudo rm -f "$service_file"
    sudo systemctl daemon-reload

    log_info "  ✅ ${service_name}.service removed"
}

# ─── Show status of all installed services ───────────────────────────
show_status() {
    echo ""
    echo -e "${BOLD}Installed metanode services:${NC}"
    for node in 0 1 2 3 4; do
        local service="metanode-node${node}"
        if [ -f "/etc/systemd/system/${service}.service" ]; then
            local active
            active=$(sudo systemctl is-active "$service" 2>/dev/null || echo "unknown")
            local enabled
            enabled=$(sudo systemctl is-enabled "$service" 2>/dev/null || echo "unknown")
            if [ "$active" = "active" ]; then
                echo -e "  ${GREEN}✅${NC} $service  ($active / $enabled)"
            else
                echo -e "  ${RED}❌${NC} $service  ($active / $enabled)"
            fi
        else
            echo -e "  ${YELLOW}--${NC} $service  (not installed)"
        fi
    done
    echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────
print_usage() {
    echo ""
    echo -e "${BOLD}Usage:${NC} $0 [OPTIONS] [NODE_IDS...]"
    echo ""
    echo "  NODE_IDS     One or more node IDs to install (0-4)"
    echo "  --all        Install services for all nodes (0-4)"
    echo "  --remove N   Remove service for node N"
    echo "  --status     Show status of all services"
    echo ""
    echo "Examples:"
    echo "  $0 0 1          # Install Node 0 and Node 1 (Server A)"
    echo "  $0 2            # Install Node 2 (Server B)"
    echo "  $0 3 4          # Install Node 3 and Node 4 (Server C)"
    echo "  $0 --all        # Install all 5 nodes"
    echo "  $0 --remove 0   # Remove Node 0 service"
    echo "  $0 --status     # Check all service statuses"
    echo ""
    echo -e "${YELLOW}NOTE:${NC} Run this script DIRECTLY on each production server."
    echo "      Requires sudo for systemctl commands."
    echo ""
}

if [ $# -eq 0 ]; then
    print_usage
    exit 0
fi

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  ⚙️  METANODE SYSTEMD SERVICE SETUP                     ║${NC}"
echo -e "${BOLD}║  Server: $(hostname -f)$(printf '%*s' $((41 - ${#HOSTNAME})) '')║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""

case "$1" in
    --all)
        check_prereqs
        for node in 0 1 2 3 4; do
            install_service "$node"
        done
        show_status
        ;;
    --remove)
        if [ -z "${2:-}" ]; then
            log_error "--remove requires a node ID (0-4)"
            exit 1
        fi
        remove_service "$2"
        ;;
    --status)
        show_status
        ;;
    --help|-h)
        print_usage
        ;;
    *)
        # Treat remaining args as node IDs
        check_prereqs
        for node in "$@"; do
            if [[ ! "$node" =~ ^[0-4]$ ]]; then
                log_error "Invalid node ID: $node (must be 0-4)"
                exit 1
            fi
            install_service "$node"
        done
        show_status
        ;;
esac

echo -e "${GREEN}${BOLD}✅ Done!${NC}"
echo ""
echo -e "${YELLOW}IMPORTANT:${NC} Do NOT run mtn-orchestrator.sh and systemd at the same time!"
echo "  • Dev/test: use mtn-orchestrator.sh"
echo "  • Production: use systemctl"
echo ""
