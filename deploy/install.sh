#!/bin/bash
# ╔══════════════════════════════════════════════════════════════════════╗
# ║  Metanode Node Installer                                             ║
# ║                                                                      ║
# ║  Installs and configures a Metanode validator or sync-only node      ║
# ║  using systemd — similar to how Sui nodes are deployed.              ║
# ║                                                                      ║
# ║  Usage:                                                              ║
# ║    sudo bash install.sh --config validator.env                       ║
# ║    sudo bash install.sh --config synconly.env                        ║
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
    log_err "Please run as root: sudo bash install.sh --config <your.env>"
    exit 1
fi

# ─── Parse arguments ───────────────────────────────────────────────────────
JSON_CONFIG=""
TOML_CONFIG=""
PROTOCOL_KEY=""
NETWORK_KEY=""
SKIP_BUILD="false"
EXPLICIT_NODE_ID=""
EXPLICIT_NODE_TYPE="validator"

ARGS=("$@")
for i in "${!ARGS[@]}"; do
    case "${ARGS[$i]}" in
        --config=*) CONFIG_ENV="${ARGS[$i]#--config=}" ;;
        --config)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && CONFIG_ENV="${ARGS[$next]}"
            ;;
        --json-config=*) JSON_CONFIG="${ARGS[$i]#--json-config=}" ;;
        --json-config)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && JSON_CONFIG="${ARGS[$next]}"
            ;;
        --toml-config=*) TOML_CONFIG="${ARGS[$i]#--toml-config=}" ;;
        --toml-config)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && TOML_CONFIG="${ARGS[$next]}"
            ;;
        --protocol-key=*) PROTOCOL_KEY="${ARGS[$i]#--protocol-key=}" ;;
        --protocol-key)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && PROTOCOL_KEY="${ARGS[$next]}"
            ;;
        --network-key=*) NETWORK_KEY="${ARGS[$i]#--network-key=}" ;;
        --network-key)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && NETWORK_KEY="${ARGS[$next]}"
            ;;
        --node-id=*) EXPLICIT_NODE_ID="${ARGS[$i]#--node-id=}" ;;
        --node-id)
            next=$((i+1))
            [ "$next" -lt "${#ARGS[@]}" ] && EXPLICIT_NODE_ID="${ARGS[$next]}"
            ;;
        --skip-build) SKIP_BUILD="true" ;;
        --yes|-y) AUTO_YES="true" ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -n "$JSON_CONFIG" ] && [ -n "$TOML_CONFIG" ]; then
    log_info "Using existing JSON/TOML configurations. Skipping .env loading."
    if [ -z "$EXPLICIT_NODE_ID" ]; then
        log_err "--node-id is required when using --json-config"
        exit 1
    fi
    NODE_ID="$EXPLICIT_NODE_ID"
    NODE_TYPE="$EXPLICIT_NODE_TYPE"
else
    if [ -z "$CONFIG_ENV" ]; then
        CONFIG_ENV="$SCRIPT_DIR/validator.env"
    fi

    [[ "$CONFIG_ENV" != /* ]] && CONFIG_ENV="$SCRIPT_DIR/$CONFIG_ENV"

    if [ ! -f "$CONFIG_ENV" ]; then
        log_err "Config file not found: $CONFIG_ENV"
        exit 1
    fi

    log_info "Loading config from: $CONFIG_ENV"
    source "$CONFIG_ENV"
fi

# ─── Validate required config ──────────────────────────────────────────────
if [ -z "$JSON_CONFIG" ]; then
    required_vars=(
        NODE_TYPE        # "validator" or "synconly"
        NODE_ID          # e.g. 0
        BLS_PRIVATE_KEY  # hex string for Databases.BLSPrivateKey
        ETH_PRIVATE_KEY  # hex string
        ETH_ADDRESS      # hex address without 0x
        RPC_PORT         # e.g. :8757
        P2P_PORT         # e.g. 4000
        PEER_RPC_PORT    # e.g. 19200
        REPO_URL         # e.g. https://github.com/x3pi/metanode.git
    )

    for var in "${required_vars[@]}"; do
        if [ -z "${!var:-}" ]; then
            log_err "Required config variable not set: $var"
            log_err "Check your config file: $CONFIG_ENV"
            exit 1
        fi
    done
fi

# Optional with defaults
METANODE_USER="${METANODE_USER:-metanode}"
# Each node gets its own install dir and service names — supports multi-node on same machine
INSTALL_DIR="${INSTALL_DIR:-/opt/metanode/node-${NODE_ID}}"
REPO_BRANCH="${REPO_BRANCH:-main}"
BUILD_DIR="${BUILD_DIR:-/opt/metanode/node-${NODE_ID}/src}"
PROTOCOL_KEY_FILE="${PROTOCOL_KEY_FILE:-}"
NETWORK_KEY_FILE="${NETWORK_KEY_FILE:-}"

# Systemd service names — unique per NODE_ID so multiple nodes can run on the same machine
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
log_step "Step 3: Preparing configuration files"

if [ -n "$JSON_CONFIG" ] && [ -n "$TOML_CONFIG" ]; then
    log_info "Copying existing JSON config: $JSON_CONFIG"
    cp "$JSON_CONFIG" "$INSTALL_DIR/config/execution.json"
    
    # Đảm bảo ghi đè các đường dẫn runtime để khớp với cấu trúc thư mục của node
    if command -v jq &>/dev/null; then
        jq ".log_path = \"${INSTALL_DIR}/logs/execution/go-master\" |
            .backup_path = \"${INSTALL_DIR}/data/execution/backup\" |
            .Databases.RootPath = \"${INSTALL_DIR}/data/execution/db\" |
            .Databases.SnapshotPath = \"${INSTALL_DIR}/data/execution/snapshot\" |
            .explorer_db_path = \"${INSTALL_DIR}/data/execution/explorer\" |
            .explorer_read_only_db_path = \"${INSTALL_DIR}/data/execution/explorer-read-only\" |
            .snapshot_source_dir = \"${INSTALL_DIR}/data/execution\" |
            .rust_config_path = \"${INSTALL_DIR}/config/consensus.toml\"" \
            "$INSTALL_DIR/config/execution.json" > "$INSTALL_DIR/config/execution.json.tmp"
        mv "$INSTALL_DIR/config/execution.json.tmp" "$INSTALL_DIR/config/execution.json"
        log_info "Patched runtime paths in execution.json using jq"
    else
        log_warn "jq not found! Cannot patch runtime paths in execution.json"
    fi
    
    log_info "Copying existing TOML config: $TOML_CONFIG"
    cp "$TOML_CONFIG" "$INSTALL_DIR/config/consensus.toml"
    
    # Đảm bảo ghi đè các đường dẫn runtime cho Rust (TOML) để khớp với cấu trúc thư mục
    sed -i "s|protocol_key_path = .*|protocol_key_path = \"$INSTALL_DIR/keys/protocol_key.json\"|" "$INSTALL_DIR/config/consensus.toml"
    sed -i "s|network_key_path = .*|network_key_path = \"$INSTALL_DIR/keys/network_key.json\"|" "$INSTALL_DIR/config/consensus.toml"
    sed -i "s|db_path = .*|db_path = \"$INSTALL_DIR/data/consensus\"|" "$INSTALL_DIR/config/consensus.toml"
    sed -i "s|committee_path = .*|committee_path = \"$INSTALL_DIR/config/committee.json\"|" "$INSTALL_DIR/config/consensus.toml"
    log_info "Patched runtime paths in consensus.toml using sed"
    
    log_ok "Copied existing configs to $INSTALL_DIR/config/"
else
    # Generate Go execution config from template
cat > "$INSTALL_DIR/config/execution.json" <<EOF
{
    "debug": false,
    "chainId": ${CHAIN_ID:-991},
    "private_key": "${BLS_PRIVATE_KEY}",
    "address": "${ETH_ADDRESS}",
    "log_path": "${INSTALL_DIR}/logs/execution/go-master",
    "epochs_to_keep": ${EPOCHS_TO_KEEP:-0},
    "backup_path": "${INSTALL_DIR}/data/execution/backup",
    "last_block_save_path": "/last_block.dat",
    "transaction_block_number_last_hash_path": "/transaction_block_number_last_hash",
    "block_hash_to_number_db_root_path": "/block_hash_to_number_db_root_path",
    "free_fee_addresses": [
        "55798165960a62cED34a0d86e36B1758D1303907",
        "0000000000000000000000000000000000000001",
        "Ea004b9aE1F60516210df2fDfcE9342618729d98",
        "5bE00650B306793D1e8Bd5b8151EEe503D82Ff77",
        "Aa3D49d0387e24Be7316EeeE8AC0a98E7c74eB5d",
        "bCaee1A2dCfaDE3E8798ae214E67AcfEFe5605A0",
        "ab85Ac2DdfF24f2159A6FB6d4Bd98c561e0B9FB3",
        "102912742844CB0d6f0854B747dD40644E011C79",
        "7eCeAAD6AD2820C13fDECeE7c0631CF8D8dfFa68",
        "093c721e998cBB05a510C7a9beb892760f61d762",
        "6C4c690F289e656233eFbacd8216D3d8a7350dfC",
        "191C6cEbdA13d395431920863446F7B31Adc0F52",
        "70f7dbF51D465C6BE5c287c106176208d90DEf39",
        "Ce58D332F5FA737bf6e7DDB833eB56E0E63D6394",
        "52908EC68d4CE219EfC9a6fE688364e9ADC8d7e1",
        "c4ddeb869385e35334d17bf1fcdda0fc67da86c2",
        "8E49ad0CfD469a58f957f2DF6009583E6bc6E2eA",
        "faC3cdcdb71eC61f59a4ee650051417e19743386",
        "1b46eb4e4EE5e054f16284bDC59F30C6174c4567",
        "99422404d5537b1BBe6e3BC714bf198474897660",
        "7F5a552500228ab157426Fe275A1D279DAaE15A5",
        "a16379bE5e7f7e95c714fc8Ef48086E6691f6185",
        "7339949caD4cffd3f3EE372b5339acD5f8Bc2cf6",
        "CB1E104F024B234DA0b5d0a158E5254694710AFB",
        "ddff207c0577195f05f86d1422eEAB8d04F5e19D",
        "685ccAac885aeFB3A1eBd27AB92f4e85DCC415Ae",
        "8Cccfb7E27211FE205551021CD70EA65a74aFB4d",
        "0793e5d7F998f3e410b7aAab369dF90463Ae9583",
        "FEd0634FaDf68B159986Ab221074701F71b402c1",
        "0F6d2AC2b303Ff86784917b18DBaE79657393E90",
        "86557654E4af47f43e36837f1418981fd5D06697",
        "54Be0489449F055f759f12EFD368DB49823F672c",
        "004F1552D5Eb2DE0Caf0081C468b1cCE7D6EA2dB",
        "90faC0500f09e7118498168C074032995086E004",
        "c91a5dD69e8Aab9270C78adB5b5E4297d76C76ea",
        "00b31C2932384C58301B3d703e95EfB22058f640",
        "42aee87c6A6BB1C7Ce688e6ce4Fb078826e7E13C",
        "711f91e85ED2b2D8f88Bbf30557Ad7248A19c33d",
        "5f11244dbF556Ce023846B8576063ab3D8505E9c",
        "07787c37ee9328f87973FFA014a50445087F0B18",
        "3166cFfa261619F10Df2c5B7C5E5c8142D8724a0",
        "EE83834DE93b216cEbFC3e2144865c516BFE49d2",
        "7A55548128C56f7Fe662Ee0C6b06947303C7EF08",
        "098d4966188a17120B6D1017FA86DDe947c14B88",
        "5e403EfD6a5BFa86f3AC9E93B72FDadb2Fc4F26A",
        "1d78aC19565cd005A037603702942e30c9AE14e6",
        "fB7b2B5B998474D31487530295e67fE0f784d441",
        "9F3cF229A352cb7960Ea9CF723556A22434e43a8",
        "f9f6551A6B3f524B6040Fe3394bEfBB617AC66Db",
        "397EED67274EE547907e15Fa1dcBB7815681167b",
        "54ec68f6F8331c344Ce23Bd1AbD32B83ab54eC7f",
        "2a7940440a8f77888d33D194759d57F48F2C4479",
        "024DAfCB93ada2e1602c2316C4301D389ce896b5",
        "DC41AE994Ec81eD5F82933B713AE608231A37F72",
        "07EbD6E6aea1C070419aAdAEb28d5Fe108b201F6",
        "0e9c8Da123EB086C3F188978e796A7c1Fe904210",
        "D283CE5548B90fa0a5332F042776BAe5AFb8810a",
        "AFBB3F67042C5AC20Cb9d306083006885745b733",
        "10321aCee1a17360100b5C897f32aD82Cb7E0149",
        "7984a2937822fca7c7f674593a2c78c7cf7f4f17",
        "7aCa77Fb92302D0bC3886b40da0B91318edfAE85",
        "1d78aC19565cd005A037603702942e30c9AE14e6",
        "5e403EfD6a5BFa86f3AC9E93B72FDadb2Fc4F26A",
        "098d4966188a17120B6D1017FA86DDe947c14B88",
        "Ad674E5318fe3a1c1f59e9f65B59Ad2a19398271",
        "aAF2C37492A562BbCB56903dB868B63BF3683Ed2",
        "BE98f2761d6b03493eB9Dc721d294660ffaCe281",
        "0a9011Cd58e111F273EEDd12E42D9F0158B6334E",
        "2342000F17C98A0324528D948EeE5d89d4e67F74",
        "98bd7fc60739895b2f1008F368f5892510db90B4",
        "82Ce9f6F31C7b06925D2c56608ae087E9C578DB6",
        "fF2480EE85B77Ae54191C3fF574d53ec7Bb803dd",
        "E341cec5067dE897d1Bc4dB39443Fcc548d1499B",
        "fF150bb418e9Dd30c431A9683c92616a082068Aaa",
        "8216032EB53c523c4B88d6FC26c4484309b15021",
        "2A3fa8915743D83Eda72f6D7f832e8F6Cb5Dbc6a",
        "a8B00d8fe3DfD2cC6b58eEa6a6b7C0E29460C7b5",
        "6fb275b2e462bdab50870d752d1d93c2844a012f",
        "1c1f05342c14fdF2dE961715EC7F845Cd815196c",
        "C585860DdD340C00F17a06e7A4118162Ed4d05dF",
        "2D02dc65F56D0d58ef15392767e8f82B52a5b1Ea"
    ],
    "cross_chain": {
        "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198"
    },
    "rust_send_socket_path": "/tmp/executor${NODE_ID}.sock",
    "rust_receive_socket_path": "/tmp/rust-go-node${NODE_ID}-master.sock",
    "rust_tx_socket_path": "/tmp/metanode-tx-${NODE_ID}.sock",
    "meta_node_rpc_address": "127.0.0.1:${META_NODE_RPC_PORT:-10100}",
    "connection_address": "0.0.0.0:${P2P_PORT}",
    "dns_server_address": "0.0.0.0:${DNS_PORT:-7081}",
    "version": "0.0.1.0",
    "list_type_service": "SUB-WRITE || MASTER || SUB-READ",
    "service_type": "MASTER",
    "rpc_port": "${RPC_PORT}",
    "peer_rpc_port": ${PEER_RPC_PORT},
    "db_type": 2,
    "genesis_file_path": "genesis.json",
    "snapshot_enabled": ${SNAPSHOT_ENABLED:-false},
    "snapshot_frequency_blocks": ${SNAPSHOT_FREQUENCY:-500},
    "snapshot_block_offset": ${SNAPSHOT_OFFSET:-0},
    "Databases": {
        "RootPath": "${INSTALL_DIR}/data/execution/db",
        "DBEngine": "sharded",
        "Version": "0.0.1.0",
        "BLSPrivateKey": "${BLS_PRIVATE_KEY}",
        "SnapshotPath": "${INSTALL_DIR}/data/execution/snapshots",
        "MaxPartSizeMB": 100,
        "ArchiveBaseName": "snapshot_archive"
    },
    "nodes": {
        "list_sub_address": ["0.0.0.0:${P2P_PORT}"]
    },
    "snapshot_method": "hybrid",
    "snapshot_source_dir": "${INSTALL_DIR}/data/execution",
    "snapshot_server_port": ${SNAPSHOT_SERVER_PORT:-8600},
    "state_backend": "nomt",
    "nomt_commit_concurrency": 32,
    "nomt_page_cache_mb": 1024,
    "nomt_leaf_cache_mb": 1024,
    "rust_config_path": "${INSTALL_DIR}/config/consensus.toml",
    "is_explorer": ${IS_EXPLORER:-false},
    "explorer_db_path": "${INSTALL_DIR}/data/execution/explorer",
    "log": {
        "level": "${LOG_LEVEL:-info}",
        "format": "${LOG_FORMAT:-text}",
        "console_output": ${LOG_CONSOLE_OUTPUT:-true},
        "file_output": ${LOG_FILE_OUTPUT:-false}
    }
}
EOF
log_ok "Generated: $INSTALL_DIR/config/execution.json"

# Generate Rust consensus config from template
IS_VALIDATOR_BOOL="true"
COMMIT_ENABLED="true"
if [ "$NODE_TYPE" = "synconly" ]; then
    IS_VALIDATOR_BOOL="false"
    COMMIT_ENABLED="false"
fi

cat > "$INSTALL_DIR/config/consensus.toml" <<EOF
node_id = ${NODE_ID}
rust_tx_socket_path = "/tmp/metanode-tx-${NODE_ID}.sock"
network_address = "0.0.0.0:${CONSENSUS_PORT:-9000}"
protocol_key_path = "${INSTALL_DIR}/keys/protocol_key.json"
network_key_path = "${INSTALL_DIR}/keys/network_key.json"
storage_path = "${INSTALL_DIR}/data/consensus"
enable_metrics = true
metrics_port = ${METRICS_PORT:-9100}
speed_multiplier = 1.0
time_based_epoch_change = true
max_clock_drift_seconds = 5
enable_ntp_sync = true
ntp_servers = ["pool.ntp.org", "time.google.com"]
ntp_sync_interval_seconds = 300
executor_read_enabled = true
executor_commit_enabled = ${COMMIT_ENABLED}
executor_send_socket_path = "/tmp/executor${NODE_ID}.sock"
executor_receive_socket_path = "/tmp/rust-go-node${NODE_ID}-master.sock"
commit_sync_batch_size = 500
commit_sync_parallel_fetches = 32
commit_sync_batches_ahead = 128
adaptive_catchup_enabled = true
adaptive_delay_enabled = false
epoch_transition_optimization = "fast"
enable_gradual_shutdown = true
gradual_shutdown_user_cert_drain_secs = 2
gradual_shutdown_consensus_cert_drain_secs = 1
gradual_shutdown_final_drain_secs = 1
epoch_monitor_poll_interval_secs = 5
peer_rpc_port = ${PEER_RPC_PORT}
peer_rpc_addresses = [${PEER_RPC_ADDRESSES}]
epochs_to_keep = ${EPOCHS_TO_KEEP:-5}

[log]
level = "${LOG_LEVEL:-info}"
format = "${LOG_FORMAT:-text}"
console_output = ${LOG_CONSOLE_OUTPUT:-true}
file_output = ${LOG_FILE_OUTPUT:-false}
EOF
log_ok "Generated: $INSTALL_DIR/config/consensus.toml"
fi

# Copy genesis.json to config and bin (supporting relative path "genesis.json" starting working directory)
cp "$GENESIS_FILE" "$INSTALL_DIR/config/genesis.json"
cp "$GENESIS_FILE" "$INSTALL_DIR/bin/genesis.json"
chown "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR/config/genesis.json" "$INSTALL_DIR/bin/genesis.json"
log_ok "Copied genesis.json → $INSTALL_DIR/config/ & $INSTALL_DIR/bin/"

# ──────────────────────────────────────────────────────────────────────────
# STEP 4: Install keys
# ──────────────────────────────────────────────────────────────────────────
log_step "Step 4: Installing keys"

if [ -n "$PROTOCOL_KEY" ] && [ -n "$NETWORK_KEY" ]; then
    log_info "Copying existing network keys..."
    cp "$PROTOCOL_KEY" "$INSTALL_DIR/keys/protocol_key.json"
    cp "$NETWORK_KEY"  "$INSTALL_DIR/keys/network_key.json"
else
    if [ -z "$PROTOCOL_KEY_FILE" ] || [ -z "$NETWORK_KEY_FILE" ]; then
        if [ -n "$JSON_CONFIG" ]; then
            log_warn "PROTOCOL_KEY_FILE and NETWORK_KEY_FILE are empty, but --json-config was passed."
            log_warn "Assuming keys are handled internally or not needed."
        else
            log_err "PROTOCOL_KEY_FILE and NETWORK_KEY_FILE are required for all nodes"
            log_err "Generate keys first: ./metanode keytool generate validator --out-dir ./keys"
            exit 1
        fi
    else
        if [ ! -f "$PROTOCOL_KEY_FILE" ] || [ ! -f "$NETWORK_KEY_FILE" ]; then
            log_err "Key file not found: $PROTOCOL_KEY_FILE or $NETWORK_KEY_FILE"
            exit 1
        fi
        cp "$PROTOCOL_KEY_FILE" "$INSTALL_DIR/keys/protocol_key.json"
        cp "$NETWORK_KEY_FILE"  "$INSTALL_DIR/keys/network_key.json"
    fi
fi

chmod 600 "$INSTALL_DIR/keys/"*.json 2>/dev/null || true
chown "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR/keys/"*.json 2>/dev/null || true
log_ok "Network keys installed to $INSTALL_DIR/keys/"

chown -R "$METANODE_USER:$METANODE_USER" "$INSTALL_DIR"

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
#Restart=on-failure
#RestartSec=15s

# Environment
Environment=GOTRACEBACK=all
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
#Restart=on-failure
#RestartSec=10s

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
