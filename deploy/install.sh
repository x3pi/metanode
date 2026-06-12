#!/bin/bash
# ╔══════════════════════════════════════════════════════════════════════╗
# ║  Metanode Node Installer                                             ║
# ║                                                                      ║
# ║  Installs and configures a Metanode validator or sync-only node      ║
# ║  using systemd — similar to how Sui nodes are deployed.              ║
# ║                                                                      ║
# ║  Usage:                                                              ║
# ║    sudo bash install.sh --config-dir ./node-0_keys                   ║
# ║                                                                      ║
# ║  What this script does:                                              ║
# ║    1. Creates a 'metanode' system user                               ║
# ║    2. Creates /opt/metanode directory structure                      ║
# ║    3. Builds Go + Rust binaries from source                          ║
# ║    4. Copies configs and keys into /opt/metanode                     ║
# ║    5. Installs two systemd services:                                 ║
# ║         - metanode-execution.service  (Go layer)                     ║
# ║         - metanode-consensus.service  (Rust layer, starts after Go)  ║
# ║    6. Enables + starts both services                                 ║
# ╚══════════════════════════════════════════════════════════════════════╝

set -eo pipefail

# ─── Colors ────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

log_info()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
log_ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_err()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }
log_step()  { echo -e "\n${BOLD}${CYAN}══════════════════════════════════════${NC}"; echo -e "${BOLD}  $*${NC}"; echo -e "${BOLD}${CYAN}══════════════════════════════════════${NC}"; }

# ─── Root check ────────────────────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
    log_err "Please run as root: sudo bash install.sh --config-dir <your_config_dir>"
    exit 1
fi

# ─── Parse arguments ───────────────────────────────────────────────────────

CONFIG_DIR=""
SKIP_BUILD="false"

ARGS=("$@")
for i in "${!ARGS[@]}"; do
    case "${ARGS[$i]}" in
        --config-dir=*) CONFIG_DIR="${ARGS[$i]#--config-dir=}" ;;
        --config-dir)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && CONFIG_DIR="${ARGS[$next]}"
            ;;
        --skip-build) SKIP_BUILD="true" ;;
        --yes|-y) AUTO_YES="true" ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -z "$CONFIG_DIR" ]; then
    log_err "--config-dir is required. Example: sudo bash install.sh --config-dir ./node-0_keys"
    exit 1
fi

if [ ! -d "$CONFIG_DIR" ]; then
    log_err "Config directory not found: $CONFIG_DIR"
    exit 1
fi

if [ ! -f "$CONFIG_DIR/execution.json" ] || [ ! -f "$CONFIG_DIR/consensus.toml" ]; then
    log_err "execution.json or consensus.toml not found in $CONFIG_DIR"
    exit 1
fi

# Extract NODE_ID from consensus.toml
NODE_ID=$(grep "^node_id " "$CONFIG_DIR/consensus.toml" | cut -d '=' -f 2 | tr -d ' ' | tr -d '"')
if [ -z "$NODE_ID" ]; then
    log_err "Could not determine node_id from $CONFIG_DIR/consensus.toml"
    exit 1
fi

NODE_TYPE="validator" # Default display
if grep -q 'is_explorer = true' "$CONFIG_DIR/execution.json"; then
    NODE_TYPE="synconly"
fi

METANODE_USER="${METANODE_USER:-metanode}"
INSTALL_DIR="/opt/metanode/node-${NODE_ID}"
REPO_URL="https://github.com/x3pi/metanode.git"
REPO_BRANCH="main"
BUILD_DIR="${BUILD_DIR:-/opt/metanode/node-${NODE_ID}/src}"

SVC_EXECUTION="metanode-execution-${NODE_ID}"
SVC_CONSENSUS="metanode-consensus-${NODE_ID}"


# ─── Print summary ─────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}${GREEN}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${GREEN}║  🚀 Metanode Node Installer                              ║${NC}"
echo -e "${BOLD}${GREEN}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  Node Type    : ${CYAN}${NODE_TYPE}${NC}"
echo -e "  Node ID      : ${CYAN}${NODE_ID}${NC}"
echo -e "  Install Dir  : ${CYAN}${INSTALL_DIR}${NC}"
echo -e "  System User  : ${CYAN}${METANODE_USER}${NC}"
echo -e "  Services     : ${CYAN}${SVC_EXECUTION} / ${SVC_CONSENSUS}${NC}"
echo -e "  Repo         : ${CYAN}${REPO_URL}@${REPO_BRANCH}${NC}"
echo ""

if [ "${AUTO_YES:-false}" != "true" ]; then
    read -p "  Proceed with installation? [y/N] " -n 1 -r
    echo ""
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_warn "Installation cancelled."
        exit 0
    fi
fi

# Stop services if they are already running or in a restart loop to prevent "Text file busy"
log_info "Stopping existing services (if any) to prevent file locks..."
systemctl stop ${SVC_CONSENSUS}.service 2>/dev/null || true
systemctl stop ${SVC_EXECUTION}.service 2>/dev/null || true

# ──────────────────────────────────────────────────────────────────────────
# STEP 1: Create system user and directory structure
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 1: Creating system user and directories"

if ! id "$METANODE_USER" &>/dev/null; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$METANODE_USER"
    log_ok "Created system user: $METANODE_USER"
else
    log_info "System user already exists: $METANODE_USER"
fi

mkdir -p \
    "$INSTALL_DIR/bin" \
    "$INSTALL_DIR/config" \
    "$INSTALL_DIR/keys" \
    "$INSTALL_DIR/data/execution" \
    "$INSTALL_DIR/data/consensus" \
    "$INSTALL_DIR/logs/execution" \
    "$INSTALL_DIR/logs/consensus"

chown -R "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR"
chmod 750 "$INSTALL_DIR/keys"
log_ok "Directories created under $INSTALL_DIR"

# ──────────────────────────────────────────────────────────────────────────
# STEP 2: Preparing and building binaries
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 2: Preparing and building binaries"

LOCAL_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_USER="${SUDO_USER:-$(whoami)}"

if [ -d "$LOCAL_ROOT/consensus/metanode" ] && [ -d "$LOCAL_ROOT/execution/cmd/simple_chain" ]; then
    log_info "Detected local repository at: $LOCAL_ROOT"
    log_info "Building directly from local source under user: $BUILD_USER"
    SRC_DIR="$LOCAL_ROOT"
else
    log_info "Local repository not detected. Cloning from $REPO_URL (branch: $REPO_BRANCH)..."
    if [ ! -d "$BUILD_DIR/.git" ]; then
        git clone --branch "$REPO_BRANCH" "$REPO_URL" "$BUILD_DIR"
    else
        log_info "Repository already exists. Pulling latest..."
        git -C "$BUILD_DIR" fetch origin "$REPO_BRANCH"
        git -C "$BUILD_DIR" reset --hard "origin/$REPO_BRANCH"
    fi
    SRC_DIR="$BUILD_DIR"
fi

GENESIS_FILE="$SRC_DIR/execution/cmd/simple_chain/genesis.json"
if [ ! -f "$GENESIS_FILE" ]; then
    log_err "Genesis file not found at: $GENESIS_FILE"
    exit 1
fi
log_info "Using genesis file: $GENESIS_FILE"



# Build Rust consensus engine
if [ "$SKIP_BUILD" != "true" ] && [ -f "$SRC_DIR/consensus/metanode/Cargo.toml" ]; then
    log_info "Building Rust consensus engine (this may take ~10 minutes)..."
    EXT_PATH="/usr/local/go/bin:/home/$BUILD_USER/go/bin:/usr/local/go1.24.3/bin:/home/$BUILD_USER/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    sudo -u "$BUILD_USER" env PATH="$EXT_PATH" bash -c "cd '$SRC_DIR/consensus/metanode' && cargo build --release --bin metanode" 2>&1 | \
        grep -E "^(error|warning: unused|Compiling|Finished|error\[)" || true
else
    log_info "Skipping Rust build (either --skip-build or source code not found, using prebuilt binary)"
fi
RUST_BIN="$SRC_DIR/target/release/metanode"
[ -f "$RUST_BIN" ] || RUST_BIN="$SRC_DIR/consensus/metanode/target/release/metanode"
[ -f "$RUST_BIN" ] || { log_err "Rust build failed — binary not found"; exit 1; }
log_ok "Rust binary verified: $RUST_BIN"

# Build Go execution engine
if [ "$SKIP_BUILD" != "true" ] && [ -f "$SRC_DIR/execution/cmd/simple_chain/go.mod" ]; then
    log_info "Building Go execution engine..."
    EXT_PATH="/usr/local/go/bin:/home/$BUILD_USER/go/bin:/usr/local/go1.24.3/bin:/home/$BUILD_USER/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    sudo -u "$BUILD_USER" env PATH="$EXT_PATH" bash -c "cd '$SRC_DIR/execution/cmd/simple_chain' && CGO_ENABLED=1 go build -o simple_chain ."
else
    log_info "Skipping Go build (either --skip-build or source code not found, using prebuilt binary)"
fi
GO_BIN="$SRC_DIR/execution/cmd/simple_chain/simple_chain"
[ -f "$GO_BIN" ] || { log_err "Go build failed — binary not found"; exit 1; }
log_ok "Go binary verified: $GO_BIN"

# Copy binaries
# Đảm bảo các tiến trình cũ của node này dừng hẳn trước khi ghi đè để tránh lỗi "Text file busy"
log_info "Waiting for node-${NODE_ID} processes to exit..."
for i in {1..30}; do
    if ! pgrep -f "node-${NODE_ID}/bin/simple_chain" >/dev/null && ! pgrep -f "node-${NODE_ID}/bin/metanode" >/dev/null; then
        break
    fi
    sleep 0.5
done

# Force kill nếu vẫn chưa dừng hẳn
if pgrep -f "node-${NODE_ID}/bin/simple_chain" >/dev/null; then
    log_warn "Force killing execution process of node-${NODE_ID}..."
    pkill -9 -f "node-${NODE_ID}/bin/simple_chain" || true
fi
if pgrep -f "node-${NODE_ID}/bin/metanode" >/dev/null; then
    log_warn "Force killing consensus process of node-${NODE_ID}..."
    pkill -9 -f "node-${NODE_ID}/bin/metanode" || true
fi

cp "$RUST_BIN" "$INSTALL_DIR/bin/metanode"
cp "$GO_BIN"   "$INSTALL_DIR/bin/simple_chain"
chmod +x "$INSTALL_DIR/bin/metanode" "$INSTALL_DIR/bin/simple_chain"
chown "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR/bin/metanode" "$INSTALL_DIR/bin/simple_chain"
log_ok "Binaries installed to $INSTALL_DIR/bin/"

# ──────────────────────────────────────────────────────────────────────────
# STEP 3: Generate or Copy configs
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 3: Preparing configuration files & Keys"

# Copy pre-generated configs and keys directly
cp "$CONFIG_DIR/execution.json" "$INSTALL_DIR/config/execution.json"
cp "$CONFIG_DIR/consensus.toml" "$INSTALL_DIR/config/consensus.toml"

if [ -f "$CONFIG_DIR/genesis.json" ]; then
    cp "$CONFIG_DIR/genesis.json" "$INSTALL_DIR/config/genesis.json"
    cp "$CONFIG_DIR/genesis.json" "$INSTALL_DIR/bin/genesis.json"
    log_ok "Copied genesis.json from config dir"
elif [ -f "$GENESIS_FILE" ]; then
    cp "$GENESIS_FILE" "$INSTALL_DIR/config/genesis.json"
    cp "$GENESIS_FILE" "$INSTALL_DIR/bin/genesis.json"
    log_ok "Copied default genesis.json"
fi

if [ -f "$CONFIG_DIR/network_key.json" ]; then
    cp "$CONFIG_DIR/network_key.json" "$INSTALL_DIR/keys/network_key.json"
fi
if [ -f "$CONFIG_DIR/protocol_key.json" ]; then
    cp "$CONFIG_DIR/protocol_key.json" "$INSTALL_DIR/keys/protocol_key.json"
fi

chmod 600 "$INSTALL_DIR/keys/"*.json 2>/dev/null || true
chown -R "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR/config" "$INSTALL_DIR/keys" "$INSTALL_DIR/bin"
log_ok "All configs and keys installed to $INSTALL_DIR"


# ──────────────────────────────────────────────────────────────────────────
# STEP 5: Create required data directories
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 5: Creating data directories"

mkdir -p \
    "$INSTALL_DIR/data/execution/db/data/xapian_node" \
    "$INSTALL_DIR/data/execution/backup" \
    "$INSTALL_DIR/data/execution/snapshots" \
    "$INSTALL_DIR/data/execution/explorer" \
    "$INSTALL_DIR/data/consensus"

chown -R "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR/data"
log_ok "Data directories ready"

# ──────────────────────────────────────────────────────────────────────────
# BTRFS/XFS CHECK: Required when snapshot_enabled=true
# ──────────────────────────────────────────────────────────────────────────
if [ "${SNAPSHOT_ENABLED:-false}" = "true" ]; then
    DATA_DIR="$INSTALL_DIR/data/execution/db"
    FS_TYPE=$(stat -f -c "%T" "$DATA_DIR" 2>/dev/null || echo "unknown")
    if [ "$FS_TYPE" = "btrfs" ] || [ "$FS_TYPE" = "xfs" ]; then
        log_ok "Filesystem check: $DATA_DIR is $FS_TYPE — snapshot reflink supported ✅"
    else
        echo ""
        echo -e "${RED}╔══════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${RED}║  ⚠️  CẢNH BÁO: SNAPSHOT_ENABLED=true nhưng filesystem        ║${NC}"
        echo -e "${RED}║     KHÔNG hỗ trợ reflink (hiện tại: $FS_TYPE)               ║${NC}"
        echo -e "${RED}║                                                              ║${NC}"
        echo -e "${RED}║  Node sẽ CRASH khi khởi động với lỗi:                       ║${NC}"
        echo -e "${RED}║  CRITICAL: Reflink (btrfs/xfs) is required for snapshotting ║${NC}"
        echo -e "${RED}║                                                              ║${NC}"
        echo -e "${RED}║  Để khắc phục, chạy TRƯỚC khi tiếp tục:                    ║${NC}"
        echo -e "${RED}║                                                              ║${NC}"
        echo -e "${RED}║    cd $SRC_DIR/execution/cmd/simple_chain              ║${NC}"
        echo -e "${RED}║    sudo bash migrate-to-btrfs-lvm.sh                        ║${NC}"
        echo -e "${RED}║                                                              ║${NC}"
        echo -e "${RED}║  Hoặc tắt snapshot trong .env:  SNAPSHOT_ENABLED=false      ║${NC}"
        echo -e "${RED}╚══════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        read -p "  Tiếp tục cài đặt dù chưa có BTRFS? [y/N] " -n 1 -r
        echo ""
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_warn "Cài đặt bị huỷ. Vui lòng setup BTRFS trước rồi chạy lại install.sh."
            exit 0
        fi
        log_warn "Tiếp tục — node sẽ crash khi khởi động nếu không có BTRFS/XFS!"
    fi
fi

# ──────────────────────────────────────────────────────────────────────────
# STEP 6: Install systemd service units
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 6: Installing systemd services"

# --- ${SVC_EXECUTION}.service (Go layer) ---
cat > /etc/systemd/system/${SVC_EXECUTION}.service <<EOF
[Unit]
Description=Metanode Execution Layer (Go) — Node ${NODE_ID}
Documentation=https://github.com/x3pi/metanode
After=network-online.target
Wants=network-online.target
# Consensus depends on execution — execution must start first
Before=${SVC_CONSENSUS}.service
#StartLimitIntervalSec=600
#StartLimitBurst=3

[Service]
Type=simple
User=${METANODE_USER}
Group=${METANODE_USER}
WorkingDirectory=${INSTALL_DIR}/bin

ExecStart=${INSTALL_DIR}/bin/simple_chain -config=${INSTALL_DIR}/config/execution.json
ExecStop=/bin/kill -SIGTERM \$MAINPID

# Allow 90s for DB to flush cleanly on shutdown (CRITICAL — prevents DB corruption)
TimeoutStopSec=90
Restart=on-failure
RestartSec=15s

# Environment
Environment=GOTRACEBACK=all
Environment=NODE_TYPE=${NODE_TYPE}
# Removed GOMEMLIMIT=4GiB to let Go use all available physical RAM
LimitNOFILE=100000

# Logging
StandardOutput=append:${INSTALL_DIR}/logs/execution/execution.log
StandardError=append:${INSTALL_DIR}/logs/execution/execution.log
SyslogIdentifier=${SVC_EXECUTION}

[Install]
WantedBy=multi-user.target
EOF

# --- ${SVC_CONSENSUS}.service (Rust layer) ---
cat > /etc/systemd/system/${SVC_CONSENSUS}.service <<EOF
[Unit]
Description=Metanode Consensus Engine (Rust) — Node ${NODE_ID}
Documentation=https://github.com/x3pi/metanode
After=network-online.target ${SVC_EXECUTION}.service
Wants=network-online.target
Requires=${SVC_EXECUTION}.service
#StartLimitIntervalSec=600
#StartLimitBurst=3

[Service]
Type=simple
User=${METANODE_USER}
Group=${METANODE_USER}
WorkingDirectory=${INSTALL_DIR}/bin

ExecStart=${INSTALL_DIR}/bin/metanode start --config ${INSTALL_DIR}/config/consensus.toml
ExecStop=/bin/kill -SIGTERM \$MAINPID

TimeoutStopSec=60
Restart=on-failure
RestartSec=10s

# Environment
Environment=RUST_BACKTRACE=full
LimitNOFILE=100000

# Logging
StandardOutput=append:${INSTALL_DIR}/logs/consensus/consensus.log
StandardError=append:${INSTALL_DIR}/logs/consensus/consensus.log
SyslogIdentifier=${SVC_CONSENSUS}

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable ${SVC_EXECUTION}.service
systemctl enable ${SVC_CONSENSUS}.service
log_ok "systemd services installed and enabled: ${SVC_EXECUTION} / ${SVC_CONSENSUS}"

# ──────────────────────────────────────────────────────────────────────────
# STEP 7: Start services
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 7: Starting services"

log_info "Starting ${SVC_EXECUTION}..."
systemctl start ${SVC_EXECUTION}.service
sleep 5

log_info "Starting ${SVC_CONSENSUS}..."
systemctl start ${SVC_CONSENSUS}.service

sleep 3
echo ""
log_step "Installation complete!"
echo ""
systemctl --no-pager status ${SVC_EXECUTION}.service --lines=5 || true
echo ""
systemctl --no-pager status ${SVC_CONSENSUS}.service --lines=5 || true

echo ""
echo -e "${BOLD}  📋 Management commands (Node ${NODE_ID}):${NC}"
echo -e "  ${CYAN}journalctl -u ${SVC_EXECUTION} -f${NC}   # Follow execution logs"
echo -e "  ${CYAN}journalctl -u ${SVC_CONSENSUS} -f${NC}   # Follow consensus logs"
echo -e "  ${CYAN}sudo systemctl stop ${SVC_CONSENSUS} ${SVC_EXECUTION}${NC}  # Stop"
echo -e "  ${CYAN}sudo systemctl restart ${SVC_EXECUTION} ${SVC_CONSENSUS}${NC}  # Restart"
echo ""
