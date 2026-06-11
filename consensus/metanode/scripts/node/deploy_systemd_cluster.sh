#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  AUTOMATED MULTI-SERVER CLUSTER DEPLOYMENT                        ║
# ║                                                                   ║
# ║  Flow: Build locally → Push binaries+configs → Start remote       ║
# ║                                                                   ║
# ║  Usage: ./deploy_cluster.sh [--build] [--push] [--start] [--all] ║
# ║         ./deploy_cluster.sh --all        # Full deploy            ║
# ║         ./deploy_cluster.sh --push       # Push only (no build)   ║
# ║         ./deploy_cluster.sh --start      # Start only             ║
# ║         ./deploy_cluster.sh --stop       # Stop cluster           ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

# Paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ─── Parse Arguments ────────────────────────────────────────────────
# Support --env <file> to specify a custom env file
# Or set ENV_FILE=deploy-3machines.env before running
DO_BUILD=false
DO_BUILD_EVM=false
DO_PUSH=false
DO_IPS=false
DO_START=false
DO_STOP=false
DO_SETUP=false
KEEP_DATA=false
CUSTOM_ENV=""
ONLY_NODE=""

# Parse args into array for safe indexed access
ARGS=("$@")
for i in "${!ARGS[@]}"; do
    arg="${ARGS[$i]}"
    case "$arg" in
        --env=*) CUSTOM_ENV="${arg#--env=}" ;;
        --env)
            next=$((i+1))
            if [ "$next" -lt "${#ARGS[@]}" ]; then
                CUSTOM_ENV="${ARGS[$next]}"
            fi ;;
        --only-node=*) ONLY_NODE="${arg#--only-node=}" ;;
        --only-node)
            next=$((i+1))
            if [ "$next" -lt "${#ARGS[@]}" ]; then
                ONLY_NODE="${ARGS[$next]}"
            fi ;;
        --restore-node=*) RESTORE_NODE="${arg#--restore-node=}" ;;
        --restore-node)
            next=$((i+1))
            if [ "$next" -lt "${#ARGS[@]}" ]; then
                RESTORE_NODE="${ARGS[$next]}"
            fi ;;
    esac
done

# Resolve env file path
if [ -n "${CUSTOM_ENV:-}" ]; then
    # If relative path, resolve relative to SCRIPT_DIR
    [[ "$CUSTOM_ENV" != /* ]] && CUSTOM_ENV="$SCRIPT_DIR/$CUSTOM_ENV"
    ENV_FILE="$CUSTOM_ENV"
elif [ -n "${ENV_FILE:-}" ] && [[ "${ENV_FILE}" != /* ]]; then
    ENV_FILE="$SCRIPT_DIR/${ENV_FILE}"
else
    ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/deploy.env}"
fi

DO_RESTORE=false

echo -e "${CYAN}  📋 Using config: $ENV_FILE${NC}"

# Load config
if [ ! -f "$ENV_FILE" ]; then
    echo -e "${RED}❌ Config not found: $ENV_FILE${NC}"
    echo -e "${YELLOW}  Usage: ./deploy_cluster.sh --env deploy-3machines.env --all${NC}"
    exit 1
fi
source "$ENV_FILE"

# Fallback for old env files to ensure directory conflict avoidance
PROJECT_ROOT="${PROJECT_ROOT:-${LOCAL_CHAIN_DIR}/metanode}"
REMOTE_PROJECT_ROOT="${REMOTE_PROJECT_ROOT:-${REMOTE_DEPLOY_DIR}/metanode}"

if [ $# -eq 0 ] || [[ "$*" == *"--all"* ]]; then
    DO_BUILD=true; DO_BUILD_EVM=true; DO_PUSH=true; DO_IPS=true; DO_START=true
fi
[[ "$*" == *"--build-all"* ]] && DO_BUILD=true && DO_BUILD_EVM=true
[[ "$*" == *"--build"* ]] && DO_BUILD=true
[[ "$*" == *"--evm"* ]] && DO_BUILD=true && DO_BUILD_EVM=true
[[ "$*" == *"--push"* ]] && DO_PUSH=true
[[ "$*" == *"--ips"* ]] && DO_IPS=true
[[ "$*" == *"--start"* ]] && DO_START=true
[[ "$*" == *"--stop"* ]] && DO_STOP=true
[[ "$*" == *"--restore-node"* ]] && DO_RESTORE=true
[[ "$*" == *"--keep-data"* ]] && KEEP_DATA=true
[[ "$*" == *"--setup"* ]] && DO_SETUP=true

# ─── Helper Functions ───────────────────────────────────────────────

ssh_cmd() {
    local host="$1"; shift
    local ssh_args="$SSH_OPTS"
    if [ "${SSH_AUTH:-key}" == "password" ]; then
        sshpass -p "$SSH_PASSWORD" ssh $ssh_args "${SSH_USER}@${host}" "$@"
    elif [ -n "${SSH_KEY:-}" ]; then
        ssh $ssh_args -i "$SSH_KEY" "${SSH_USER}@${host}" "$@"
    else
        ssh $ssh_args "${SSH_USER}@${host}" "$@"
    fi
}

scp_cmd() {
    local src="$1" dst_host="$2" dst_path="$3"
    local scp_args="$SSH_OPTS -r"
    if [ "${SSH_AUTH:-key}" == "password" ]; then
        sshpass -p "$SSH_PASSWORD" scp $scp_args "$src" "${SSH_USER}@${dst_host}:${dst_path}"
    elif [ -n "${SSH_KEY:-}" ]; then
        scp $scp_args -i "$SSH_KEY" "$src" "${SSH_USER}@${dst_host}:${dst_path}"
    else
        scp $scp_args "$src" "${SSH_USER}@${dst_host}:${dst_path}"
    fi
}

rsync_cmd() {
    local src="$1" dst_host="$2" dst_path="$3"
    local ssh_transport="ssh $SSH_OPTS"
    [ -n "${SSH_KEY:-}" ] && [ "${SSH_AUTH:-key}" != "password" ] && ssh_transport="$ssh_transport -i $SSH_KEY"

    if [ "${SSH_AUTH:-key}" == "password" ]; then
        sshpass -p "$SSH_PASSWORD" rsync -azP -e "$ssh_transport" "$src" "${SSH_USER}@${dst_host}:${dst_path}"
    else
        rsync -azP -e "$ssh_transport" "$src" "${SSH_USER}@${dst_host}:${dst_path}"
    fi
}

get_unique_servers() {
    echo "${NODE_SERVER[@]}" | tr ' ' '\n' | sort -u
}

get_nodes_for_server() {
    local server="$1"
    local nodes=""
    for node_id in "${!NODE_SERVER[@]}"; do
        if [ -n "$ONLY_NODE" ] && [ "$node_id" != "$ONLY_NODE" ]; then
            continue
        fi
        if [ "${NODE_SERVER[$node_id]}" == "$server" ]; then
            nodes="$nodes $node_id"
        fi
    done
    echo "$nodes" | xargs
}

log_step() { echo -e "\n${BLUE}═══════════════════════════════════════════════════════════════${NC}"; echo -e "${BLUE}  $1${NC}"; echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"; }
log_ok()   { echo -e "${GREEN}  ✅ $1${NC}"; }
log_warn() { echo -e "${YELLOW}  ⚠️  $1${NC}"; }
log_info() { echo -e "${CYAN}  ℹ️  $1${NC}"; }
log_err()  { echo -e "${RED}  ❌ $1${NC}"; }

# ═══════════════════════════════════════════════════════════════════
# PHASE --setup: Generate all node configs from genesis-main.json
# ═══════════════════════════════════════════════════════════════════
if $DO_SETUP; then
    log_step "PHASE --setup: Generating node configurations"

    DEPLOY_DIR="$(cd "${PROJECT_ROOT}/deploy" && pwd)"
    GENESIS_MAIN="${DEPLOY_DIR}/genesis-main.json"
    GEN_VALIDATOR_SCRIPT="${DEPLOY_DIR}/gen_validator_entry.py"

    if [ ! -f "$GENESIS_MAIN" ]; then
        log_err "genesis-main.json not found at: $GENESIS_MAIN"
        exit 1
    fi
    if [ ! -f "$GEN_VALIDATOR_SCRIPT" ]; then
        log_err "gen_validator_entry.py not found at: $GEN_VALIDATOR_SCRIPT"
        exit 1
    fi

    # Detect metanode binary (prioritize root target/release)
    METANODE_BIN=""
    for candidate in \
        "$(cd "${PROJECT_ROOT}" && pwd)/target/release/metanode" \
        "${LOCAL_METANODE}/target/release/metanode" \
        "/opt/metanode/bin/metanode"; do
        if [ -f "$candidate" ]; then
            METANODE_BIN="$candidate"
            break
        fi
    done
    if [ -z "$METANODE_BIN" ]; then
        log_err "metanode binary not found — build Rust first (--build) or place binary in target/release/metanode"
        exit 1
    fi
    log_info "Using metanode binary: $METANODE_BIN"

    # Get all node IDs sorted
    ALL_NODE_IDS=($(echo "${!NODE_SERVER[@]}" | tr ' ' '\n' | sort -n))

    # Parse BTRFS_NODES into an associative set for quick lookup
    declare -A BTRFS_SET
    for bn in ${BTRFS_NODES:-}; do
        BTRFS_SET[$bn]=1
    done

    # Build peers map for Python script
    PEERS_MAP=""
    for i in "${ALL_NODE_IDS[@]}"; do
        PEERS_MAP="${PEERS_MAP}${i}=${NODE_SERVER[$i]},"
    done
    PEERS_MAP="${PEERS_MAP%,}" # remove trailing comma

    # ── Step 1: Generate keys + config for each node ─────────────────
    log_info "Generating keys and configs for nodes: ${ALL_NODE_IDS[*]}"
    for nid in "${ALL_NODE_IDS[@]}"; do
        KEYS_DIR="${DEPLOY_DIR}/node-${nid}_keys"
        IS_SYNCONLY=false
        if [ -n "${BTRFS_SET[$nid]+_}" ]; then
            IS_SYNCONLY=true
        fi

        NODE_TYPE_FLAG="validator"
        $IS_SYNCONLY && NODE_TYPE_FLAG="synconly"

        log_info "Node $nid ($NODE_TYPE_FLAG) → $KEYS_DIR"

        python3 "$GEN_VALIDATOR_SCRIPT" \
            --hostname "node-${nid}" \
            --node-id "$nid" \
            --node-type "$NODE_TYPE_FLAG" \
            --ip "${NODE_SERVER[$nid]}" \
            --peers-map "$PEERS_MAP" \
            --keys-dir "$KEYS_DIR" \
            --output "${KEYS_DIR}/node-${nid}_genesis.json" \
            --metanode-bin "$METANODE_BIN" \
            || { log_err "gen_validator_entry.py failed for node $nid"; exit 1; }

        # Apply synconly settings to generated configs
        if $IS_SYNCONLY; then
            # execution.json: is_explorer = true
            if [ -f "$KEYS_DIR/execution.json" ]; then
                jq '.is_explorer = true' "$KEYS_DIR/execution.json" > "$KEYS_DIR/execution.json.tmp" \
                    && mv "$KEYS_DIR/execution.json.tmp" "$KEYS_DIR/execution.json"
                log_info "  Node $nid: is_explorer = true (synconly)"
            fi
            # consensus.toml: executor_commit_enabled = false
            if [ -f "$KEYS_DIR/consensus.toml" ]; then
                sed -i 's/^executor_commit_enabled *= *true/executor_commit_enabled = false/' \
                    "$KEYS_DIR/consensus.toml"
                log_info "  Node $nid: executor_commit_enabled = false (synconly)"
            fi
        fi

        log_ok "Node $nid generated"
    done

    # ── Step 2: Build genesis.json validators array from validator nodes ──
    log_info "Building genesis.json validators from non-synconly nodes..."
    VALIDATOR_ENTRIES_JSON="[]"
    for nid in "${ALL_NODE_IDS[@]}"; do
        # Skip BTRFS (synconly) nodes — they don't participate in consensus
        if [ -n "${BTRFS_SET[$nid]+_}" ]; then
            log_info "  Node $nid: synconly — skipped from validators"
            continue
        fi
        GENESIS_ENTRY="${DEPLOY_DIR}/node-${nid}_keys/node-${nid}_genesis.json"
        if [ ! -f "$GENESIS_ENTRY" ]; then
            log_err "  Genesis entry not found: $GENESIS_ENTRY"
            exit 1
        fi
        VALIDATOR_ENTRIES_JSON=$(echo "$VALIDATOR_ENTRIES_JSON" | \
            jq --slurpfile entry "$GENESIS_ENTRY" '. + [$entry[0]]')
        log_ok "  Node $nid added to validators"
    done

    # ── Step 3: Write genesis.json from genesis-main.json + validators ──
    GENESIS_OUT="${DEPLOY_DIR}/genesis.json"
    jq --argjson validators "$VALIDATOR_ENTRIES_JSON" '.validators = $validators' \
        "$GENESIS_MAIN" > "$GENESIS_OUT"
    log_ok "genesis.json written with $(echo "$VALIDATOR_ENTRIES_JSON" | jq 'length') validators → $GENESIS_OUT"

    # ── Step 4: Copy genesis.json into each node's keys folder ──────────
    log_info "Distributing genesis.json to all node key directories..."
    for nid in "${ALL_NODE_IDS[@]}"; do
        KEYS_DIR="${DEPLOY_DIR}/node-${nid}_keys"
        cp "$GENESIS_OUT" "$KEYS_DIR/genesis.json"
        log_ok "  Copied genesis.json → $KEYS_DIR/"
    done

    log_step "--setup COMPLETE"
    echo -e "${GREEN}  All node configs generated in ${DEPLOY_DIR}/node-*_keys/${NC}"
    echo -e "${GREEN}  genesis.json written: ${GENESIS_OUT}${NC}"
    echo -e "${CYAN}  Next steps:${NC}"
    echo -e "     Push + Start: ${CYAN}./deploy_systemd_cluster.sh --env <your.env> --push --start${NC}"
    echo ""

    # If only --setup, exit here
    if ! $DO_BUILD && ! $DO_PUSH && ! $DO_START; then
        exit 0
    fi
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 0: Validate
# ═══════════════════════════════════════════════════════════════════
echo ""
echo -e "${GREEN}╔═══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  🚀 MULTI-SERVER CLUSTER DEPLOYMENT                          ║${NC}"
echo -e "${GREEN}║  Build local → Push binaries+configs → Start remote          ║${NC}"
echo -e "${GREEN}╚═══════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Validate SSH
log_step "Phase 0: Validating SSH connectivity"
SERVERS=$(get_unique_servers)

for server in $SERVERS; do
    nodes=$(get_nodes_for_server "$server")
    if ssh_cmd "$server" "echo ok" &>/dev/null; then
        log_ok "SSH to $server — OK (nodes: $nodes)"
    else
        log_err "Cannot SSH to $server"
        exit 1
    fi
done

# ═══════════════════════════════════════════════════════════════════
# STOP ONLY (--stop flag)
# ═══════════════════════════════════════════════════════════════════
if $DO_STOP; then
    log_step "Stopping cluster on all servers"
    for server in $SERVERS; do
        nodes=$(get_nodes_for_server "$server")
        if [ -z "$nodes" ]; then continue; fi
        log_info "Stopping nodes [$nodes] on $server..."
        
        if [ -n "$ONLY_NODE" ]; then
            ssh_cmd "$server" "
                export SSH_AUTH='${SSH_AUTH:-}'
                export SSH_PASSWORD='${SSH_PASSWORD:-}'
                _sudo() {
                    if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                        echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                    else
                        sudo \"\$@\"
                    fi
                }
                
                for id in $nodes; do
                    if _sudo systemctl list-unit-files | grep -q metanode-go-\$id; then
                        _sudo systemctl stop metanode-go-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-rpc-\$id; then
                        _sudo systemctl stop metanode-rpc-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-execution-\$id; then
                        _sudo systemctl stop metanode-execution-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-consensus-\$id; then
                        _sudo systemctl stop metanode-consensus-\$id 2>/dev/null || true
                    fi
                    _sudo pkill -f "metanode.*--node-id \$id" 2>/dev/null || true
                    rm -f /tmp/executor*-node\${id}*.sock /tmp/rust-go-node\${id}*.sock /tmp/metanode-tx-node\${id}*.sock 2>/dev/null || true
                done
            " 2>/dev/null || true
        else
            ssh_cmd "$server" "
                export SSH_AUTH='${SSH_AUTH:-}'
                export SSH_PASSWORD='${SSH_PASSWORD:-}'
                _sudo() {
                    if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                        echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                    else
                        sudo \"\$@\"
                    fi
                }
                
                for id in 0 1 2 3 4; do
                    if _sudo systemctl list-unit-files | grep -q metanode-go-\$id; then
                        _sudo systemctl stop metanode-go-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-rpc-\$id; then
                        _sudo systemctl stop metanode-rpc-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-execution-\$id; then
                        _sudo systemctl stop metanode-execution-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-consensus-\$id; then
                        _sudo systemctl stop metanode-consensus-\$id 2>/dev/null || true
                    fi
                done
                _sudo pkill -f 'simple_chain' 2>/dev/null || true
                _sudo pkill -f 'metanode' 2>/dev/null || true
                _sudo pkill -f 'rpc-client-bin' 2>/dev/null || true
                sleep 2
                _sudo pkill -9 -f 'simple_chain' 2>/dev/null || true
                _sudo pkill -9 -f 'metanode' 2>/dev/null || true
                _sudo pkill -9 -f 'rpc-client-bin' 2>/dev/null || true
                _sudo rm -f /tmp/executor*.sock /tmp/rust-go-*.sock /tmp/metanode-tx-*.sock 2>/dev/null || true
            " 2>/dev/null || true
        fi
        log_ok "$server stopped"
    done
    # If only --stop, exit here
    if ! $DO_BUILD && ! $DO_PUSH && ! $DO_START; then
        echo -e "\n${GREEN}  🛑 Cluster stopped.${NC}\n"
        exit 0
    fi
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 1: Build locally
# ═══════════════════════════════════════════════════════════════════
if $DO_BUILD; then
    log_step "Phase 1: Building binaries locally"

    if $DO_BUILD_EVM; then
        log_info "Building EVM (C++ MVM)..."
        (
            cd "${LOCAL_GO_SIMPLE}/../../pkg/mvm" && chmod +x build.sh && ./build.sh
        ) || exit 1
    fi

    # Build Rust metanode library (unified)
    log_info "Building Rust metanode..."
    (
        cd "${LOCAL_METANODE}"
        cargo build --release 2>&1 | tail -3
    )

    log_info "Building NOMT FFI (Rust)..."
    (
        cd "${LOCAL_GO_SIMPLE}/../../pkg/nomt_ffi/rust_lib"
        cargo build --release 2>&1 | tail -3
    ) || exit 1

    log_info "Touching FFI bridge files to ensure Go relinks..."
    touch "${LOCAL_GO_SIMPLE}/../../executor/ffi_bridge.go" "${LOCAL_GO_SIMPLE}/../../pkg/nomt_ffi/bridge.go" 2>/dev/null || true

    # Build Go simple_chain
    log_info "Building Go simple_chain..."
    (
        cd "${LOCAL_GO_SIMPLE}"
        export GOTOOLCHAIN=${GO_TOOLCHAIN}
        export CGO_ENABLED=1
        rm -f simple_chain
        NUM_PROCS=$(nproc)
        if [ "$NUM_PROCS" -gt 4 ]; then
            NUM_PROCS=4
        fi
        go build -p $NUM_PROCS -o simple_chain . 2>&1
    )
    log_ok "Go binary: ${LOCAL_GO_SIMPLE}/simple_chain"

    # Build RPC Proxy
    log_info "Building RPC Proxy..."
    (
        cd "${LOCAL_GO_SIMPLE}/../rpc/cmd/rpc-client"
        rm -f rpc-client-bin
        go build -o rpc-client-bin . 2>&1
        if [ ! -f certificate.pem ]; then
            openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout private.key -out certificate.pem -subj "/CN=localhost" 2>/dev/null
        fi
    )
    log_ok "RPC Proxy binary: ${LOCAL_GO_SIMPLE}/../rpc/cmd/rpc-client/rpc-client-bin"
else
    log_info "Phase 1: Skipped (use --build to enable)"
fi

# Verify binaries exist
GO_BINARY="${LOCAL_GO_SIMPLE}/simple_chain"

if { $DO_PUSH || $DO_START; } && [ ! -f "$GO_BINARY" ]; then
    log_err "Go binary not found: $GO_BINARY (run with --build first)"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 2: Stop remote cluster before push
# ═══════════════════════════════════════════════════════════════════
if $DO_PUSH || $DO_START; then
    log_step "Phase 2: Stopping existing cluster"
    for server in $SERVERS; do
        nodes=$(get_nodes_for_server "$server")
        if [ -z "$nodes" ]; then continue; fi
        log_info "Stopping nodes [$nodes] on $server..."
        
        if [ -n "$ONLY_NODE" ]; then
            ssh_cmd "$server" "
                export SSH_AUTH='${SSH_AUTH:-}'
                export SSH_PASSWORD='${SSH_PASSWORD:-}'
                _sudo() {
                    if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                        echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                    else
                        sudo \"\$@\"
                    fi
                }
                
                for id in $nodes; do
                    if _sudo systemctl list-unit-files | grep -q metanode-go-\$id; then
                        _sudo systemctl stop metanode-go-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-rpc-\$id; then
                        _sudo systemctl stop metanode-rpc-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-execution-\$id; then
                        _sudo systemctl stop metanode-execution-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-consensus-\$id; then
                        _sudo systemctl stop metanode-consensus-\$id 2>/dev/null || true
                    fi
                    _sudo pkill -f "metanode.*--node-id \$id" 2>/dev/null || true
                    rm -f /tmp/executor*-node\${id}*.sock /tmp/rust-go-node\${id}*.sock /tmp/metanode-tx-node\${id}*.sock 2>/dev/null || true
                done
            " 2>/dev/null || true
        else
            ssh_cmd "$server" "
                export SSH_AUTH='${SSH_AUTH:-}'
                export SSH_PASSWORD='${SSH_PASSWORD:-}'
                _sudo() {
                    if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                        echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                    else
                        sudo \"\$@\"
                    fi
                }
                
                for id in 0 1 2 3 4; do
                    if _sudo systemctl list-unit-files | grep -q metanode-go-\$id; then
                        _sudo systemctl stop metanode-go-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-rpc-\$id; then
                        _sudo systemctl stop metanode-rpc-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-execution-\$id; then
                        _sudo systemctl stop metanode-execution-\$id 2>/dev/null || true
                    fi
                    if _sudo systemctl list-unit-files | grep -q metanode-consensus-\$id; then
                        _sudo systemctl stop metanode-consensus-\$id 2>/dev/null || true
                    fi
                done
                _sudo pkill -f 'simple_chain' 2>/dev/null || true
                _sudo pkill -f 'metanode' 2>/dev/null || true
                _sudo pkill -f 'rpc-client-bin' 2>/dev/null || true
                sleep 2
                _sudo pkill -9 -f 'simple_chain' 2>/dev/null || true
                _sudo pkill -9 -f 'metanode' 2>/dev/null || true
                _sudo pkill -9 -f 'rpc-client-bin' 2>/dev/null || true
                _sudo rm -f /tmp/executor*.sock /tmp/rust-go-*.sock /tmp/metanode-tx-*.sock 2>/dev/null || true
            " 2>/dev/null || true
        fi
        log_ok "$server stopped"
    done
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 3: Push binaries + configs to each server
# ═══════════════════════════════════════════════════════════════════
if $DO_PUSH; then
    log_step "Phase 3: Creating remote directories + pushing files"

    GO_MASTER_CONFIGS=("config-master-node0.json" "config-master-node1.json" "config-master-node2.json" "config-master-node3.json" "config-master-node4.json")
    RUST_CONFIGS=("config/node_0.toml" "config/node_1.toml" "config/node_2.toml" "config/node_3.toml" "config/node_4.toml")

    for server in $SERVERS; do
        nodes=$(get_nodes_for_server "$server")
        if [ -z "$nodes" ]; then continue; fi
        log_info "Deploying to $server (nodes: [$nodes])..."

        # Create directory structure on remote
        ssh_cmd "$server" "
            set -euo pipefail;
            mkdir -p '${REMOTE_GO_SIMPLE}'
            mkdir -p '${REMOTE_METANODE}/config'
            mkdir -p '${REMOTE_METANODE}/logs'
            mkdir -p '${REMOTE_SCRIPTS}'
            mkdir -p '${REMOTE_GO_SIMPLE}/../rpc/cmd/rpc-client'
        "

        # Push binaries
        echo -e "    📦 Pushing Go binary..."
        rsync_cmd "$GO_BINARY" "$server" "${REMOTE_GO_SIMPLE}/simple_chain"

        echo -e "    📦 Pushing Rust binary..."
        ssh_cmd "$server" "mkdir -p '${REMOTE_METANODE}/target/release'"
        rsync_cmd "${LOCAL_METANODE}/target/release/metanode" "$server" "${REMOTE_METANODE}/target/release/metanode"

        echo -e "    📦 Pushing RPC Proxy binary & TLS certs..."
        rsync_cmd "${LOCAL_GO_SIMPLE}/../rpc/cmd/rpc-client/rpc-client-bin" "$server" "${REMOTE_GO_SIMPLE}/../rpc/cmd/rpc-client/rpc-client-bin"
        rsync_cmd "${LOCAL_GO_SIMPLE}/../rpc/cmd/rpc-client/certificate.pem" "$server" "${REMOTE_GO_SIMPLE}/../rpc/cmd/rpc-client/certificate.pem"
        rsync_cmd "${LOCAL_GO_SIMPLE}/../rpc/cmd/rpc-client/private.key" "$server" "${REMOTE_GO_SIMPLE}/../rpc/cmd/rpc-client/private.key"

        # Push genesis.json
        echo -e "    📄 Pushing genesis.json..."
        rsync_cmd "${LOCAL_GO_SIMPLE}/genesis.json" "$server" "${REMOTE_GO_SIMPLE}/genesis.json"

        # Push Rust committee.json
        echo -e "    📄 Pushing committee.json..."
        rsync_cmd "${LOCAL_METANODE}/config/committee.json" "$server" "${REMOTE_METANODE}/config/committee.json"

        # Push scripts (update_ips.sh, deploy scripts)
        echo -e "    📄 Pushing scripts..."
        rsync_cmd "${LOCAL_METANODE}/scripts/node/" "$server" "${REMOTE_SCRIPTS}/"

        # Push deploy directory for remote orchestration
        echo -e "    📄 Pushing deploy orchestrator..."
        rsync_cmd "${PROJECT_ROOT}/deploy/" "$server" "${REMOTE_PROJECT_ROOT}/deploy/"

        # Push per-node configs
        for id in $nodes; do
            echo -e "    📄 Node $id configs..."

            # Create node data directories
            # Create node data directories using sudo to bypass root ownership from systemd
            ssh_cmd "$server" "
                set -euo pipefail;
                export SSH_AUTH='${SSH_AUTH:-}'
                export SSH_PASSWORD='${SSH_PASSWORD:-}'
                _sudo() {
                    if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                        echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                    else
                        sudo \"\$@\"
                    fi
                }
                _sudo mkdir -p '${REMOTE_GO_SIMPLE}/sample/node${id}/data/data/xapian_node'
                _sudo mkdir -p '${REMOTE_GO_SIMPLE}/sample/node${id}/data-write/data/xapian_node'
                _sudo mkdir -p '${REMOTE_GO_SIMPLE}/sample/node${id}/back_up'
                _sudo mkdir -p '${REMOTE_GO_SIMPLE}/sample/node${id}/back_up_write'
                _sudo mkdir -p '${REMOTE_METANODE}/config/storage/node_${id}'
                _sudo mkdir -p '${REMOTE_METANODE}/logs/node_${id}'
                _sudo chown -R ${SSH_USER:-abc}:${SSH_USER:-abc} '${REMOTE_GO_SIMPLE}/sample' '${REMOTE_METANODE}/config/storage' '${REMOTE_METANODE}/logs' 2>/dev/null || true
            "

            # Go configs (master)
            rsync_cmd "${LOCAL_GO_SIMPLE}/${GO_MASTER_CONFIGS[$id]}" "$server" "${REMOTE_GO_SIMPLE}/${GO_MASTER_CONFIGS[$id]}"

            # Rust config + keys
            rsync_cmd "${LOCAL_METANODE}/${RUST_CONFIGS[$id]}" "$server" "${REMOTE_METANODE}/${RUST_CONFIGS[$id]}"
            rsync_cmd "${LOCAL_METANODE}/config/node_${id}_network_key.json" "$server" "${REMOTE_METANODE}/config/node_${id}_network_key.json"
            rsync_cmd "${LOCAL_METANODE}/config/node_${id}_protocol_key.json" "$server" "${REMOTE_METANODE}/config/node_${id}_protocol_key.json"
        done

        log_ok "Deployed to $server"
    done
else
    log_info "Phase 3: Skipped (use --push to enable)"
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 4: Update IPs in config files on remote servers (REMOVED)
# ═══════════════════════════════════════════════════════════════════
# IP update logic is now handled locally during --setup.

# ═══════════════════════════════════════════════════════════════════
# PHASE 5: Start nodes on remote servers (via deploy orchestrator)
# ═══════════════════════════════════════════════════════════════════
if $DO_START; then
    if $KEEP_DATA; then
        log_step "Phase 5: Start nodes via systemd-cluster (KEEPING DATA)"
    else
        log_step "Phase 5: Setup nodes via systemd-cluster (CLEAN DATA)"
    fi

    for server in $SERVERS; do
        nodes=$(get_nodes_for_server "$server")
        if [ -z "$nodes" ]; then continue; fi

        log_info "Deploying and starting nodes [$nodes] on $server..."
        
        # Check if any node on this server needs BTRFS
        NEEDS_BTRFS=false
        for id in $nodes; do
            for btrfs_id in ${BTRFS_NODES:-}; do
                if [ "$id" == "$btrfs_id" ]; then
                    NEEDS_BTRFS=true
                    break 2
                fi
            done
        done

        CMD_SEQ=""
        if $NEEDS_BTRFS; then
            CMD_SEQ="
            echo '  ▶ Cấu hình ổ đĩa BTRFS (định dạng ổ cứng tạm thời) cho server do có node cần...'
            if [ \"\${SSH_AUTH:-key}\" == \"password\" ] && [ -n \"\${SSH_PASSWORD:-}\" ]; then
                echo \"\${SSH_PASSWORD}\" | sudo -S bash setup-cluster-btrfs.sh || true
            else
                sudo bash setup-cluster-btrfs.sh || true
            fi
            "
        fi

        for id in $nodes; do
            if [ "$id" == "${SNAPSHOT_SOURCE_NODE:-}" ] && [ -n "${SNAPSHOT_SERVER_PORT:-}" ]; then
                CMD_SEQ="${CMD_SEQ}
                echo '  ▶ Mở cổng tường lửa ufw cho Snapshot API trên Node $id (Port: ${SNAPSHOT_SERVER_PORT})...'
                _sudo ufw allow ${SNAPSHOT_SERVER_PORT}/tcp >/dev/null 2>&1 || true"
            fi

            if $KEEP_DATA; then
                CMD_SEQ="${CMD_SEQ}
                echo '  ▶ Installing Node $id (keeping data)...'
                _sudo bash systemd-cluster.sh install --node $id -y"
            else
                CMD_SEQ="${CMD_SEQ}
                echo '  ▶ Setup Node $id (cleaning data)...'
                _sudo bash systemd-cluster.sh setup --node $id -y"
            fi
        done

        ssh_cmd "$server" "
            set -euo pipefail;
            cd '${REMOTE_PROJECT_ROOT}/deploy/cluster'
            export SSH_AUTH='${SSH_AUTH:-}'
            export SSH_PASSWORD='${SSH_PASSWORD:-}'
            _sudo() {
                if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                    echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                else
                    sudo \"\$@\"
                fi
            }
            $CMD_SEQ
        "
        log_ok "Nodes [$nodes] deployed and started on $server"
    done
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 6: Generate rpc_nodes.json for auto_test.sh
# ═══════════════════════════════════════════════════════════════════
log_step "Phase 6: Generating rpc_nodes.json for Health Checking & Tests"
RPC_JSON_PATH="/tmp/rpc_nodes.json"

declare -A RPC_PORTS=( [0]=8757 [1]=10747 [2]=10749 [3]=10750 [4]=10748 )
JSON_NODES=()
JSON_RPC_PROXIES=()
JSON_TCP_PROXIES=()

for id in "${!NODE_SERVER[@]}"; do
    ip="${NODE_SERVER[$id]}"
    port="${RPC_PORTS[$id]}"
    proxy_http=$((8545 + id))
    proxy_tcp=$((6200 + id)) # TCP connection_address set in config-client-tcp.json
    
    JSON_NODES+=("\"m${id}\": \"http://${ip}:${port}\"")
    JSON_RPC_PROXIES+=("\"m${id}\": \"http://${ip}:${proxy_http}\"")
    JSON_TCP_PROXIES+=("\"m${id}\": \"${ip}:${proxy_tcp}\"")
done

# Nối các string lại bằng dấu phẩy
JOINED_NODES=$(IFS=, ; echo "${JSON_NODES[*]}")
JOINED_RPC=$(IFS=, ; echo "${JSON_RPC_PROXIES[*]}")
JOINED_TCP=$(IFS=, ; echo "${JSON_TCP_PROXIES[*]}")

cat > "$RPC_JSON_PATH" <<EOF
{
  "nodes": {
    $JOINED_NODES
  },
  "rpc_proxies": {
    $JOINED_RPC
  },
  "tcp_proxies": {
    $JOINED_TCP
  }
}
EOF
log_ok "Generated $RPC_JSON_PATH with proxy endpoints"

# ═══════════════════════════════════════════════════════════════════
# PHASE 7: Starting RPC Proxies (via systemd)
# ═══════════════════════════════════════════════════════════════════
if $DO_START; then
    log_step "Phase 7: Starting RPC Proxies (via systemd)"
    for server in $SERVERS; do
        nodes=$(get_nodes_for_server "$server")
        if [ -z "$nodes" ]; then continue; fi
        
        for id in $nodes; do
            if [ -n "$ONLY_NODE" ] && [ "$id" != "$ONLY_NODE" ]; then continue; fi
            echo -e "  ▶ Installing & Starting RPC Proxy for Node $id on $server..."
            
            CMD_RPC="
                set -euo pipefail;
                cd '${REMOTE_PROJECT_ROOT}/deploy/cluster'
                if [ \"${SSH_AUTH:-key}\" == \"password\" ] && [ -n \"${SSH_PASSWORD:-}\" ]; then
                    echo \"${SSH_PASSWORD}\" | sudo -S bash install-rpc-systemd.sh --node ${id} --no-build
                else
                    sudo bash install-rpc-systemd.sh --node ${id} --no-build
                fi
            "
            ssh_cmd "$server" "$CMD_RPC"
        done
    done
    echo -e "  ✅ RPC Proxies started via systemd"
fi

# ═══════════════════════════════════════════════════════════════════
# PHASE 8: Standalone Snapshot Restore
# ═══════════════════════════════════════════════════════════════════
if $DO_RESTORE; then
    log_step "Phase 8: Restore Node(s) via Snapshot"
    
    if [ -n "${RESTORE_NODE:-}" ] && [ -n "${SNAPSHOT_SOURCE_NODE:-}" ]; then
        source_ip="${NODE_SERVER[$SNAPSHOT_SOURCE_NODE]:-127.0.0.1}"
        RESTORE_SNAPSHOT_URL="http://${source_ip}:${SNAPSHOT_SERVER_PORT:-8604}"
        log_info "Snapshot source URL resolved to: ${RESTORE_SNAPSHOT_URL}"
        
        for r_node in $RESTORE_NODE; do
            target_server="${NODE_SERVER[$r_node]:-}"
            
            if [ -n "$target_server" ]; then
                log_info "Restoring Node $r_node on $target_server..."
                CMD_SEQ="
                    set -euo pipefail;
                    cd '${REMOTE_PROJECT_ROOT}/deploy/cluster'
                    export SSH_AUTH='${SSH_AUTH:-}'
                    export SSH_PASSWORD='${SSH_PASSWORD:-}'
                    _sudo() {
                        if [ \"\$SSH_AUTH\" == \"password\" ] && [ -n \"\$SSH_PASSWORD\" ]; then
                            echo \"\$SSH_PASSWORD\" | sudo -S \"\$@\"
                        else
                            sudo \"\$@\"
                        fi
                    }
                    _sudo bash restore_snapshot_systemd.sh --node $r_node --snapshot-url '${RESTORE_SNAPSHOT_URL}' -y
                "
                ssh_cmd "$target_server" "$CMD_SEQ"
                log_ok "Restore complete for Node $r_node on $target_server"
            else
                log_warn "Node $r_node not found in any server configuration."
            fi
        done
    else
        log_warn "SNAPSHOT_SOURCE_NODE not defined or RESTORE_NODE empty, skipping restore."
    fi
else
    log_info "Phase 8: Skipped (use --restore-node to enable)"
fi

# ═══════════════════════════════════════════════════════════════════
# SUMMARY
# ═══════════════════════════════════════════════════════════════════
echo ""
echo -e "${GREEN}╔═══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  🎉 DEPLOYMENT COMPLETE!                                     ║${NC}"
echo -e "${GREEN}╚═══════════════════════════════════════════════════════════════╝${NC}"
echo ""

echo -e "${CYAN}  Actions: build=$DO_BUILD push=$DO_PUSH ips=$DO_IPS start=$DO_START${NC}"
echo ""

for server in $SERVERS; do
    nodes=$(get_nodes_for_server "$server")
    echo -e "${CYAN}  📍 $server — Nodes: [$nodes]${NC}"
    for id in $nodes; do
        echo -e "     tmux: go-master-$id"
    done
done

echo ""
echo -e "    📝 Commands:"
echo -e "       Gen configs:   ${CYAN}./deploy_systemd_cluster.sh --env <your.env> --setup${NC}"
echo -e "       Full deploy:   ${CYAN}./deploy_cluster.sh --all${NC}"
echo -e "       Start keep db: ${CYAN}./deploy_cluster.sh --start --keep-data${NC}"
echo -e "       Stop/Start 1 node: ${CYAN}./deploy_cluster.sh --stop --only-node 4${NC}"
echo -e "       Build only:    ${CYAN}./deploy_cluster.sh --build${NC}"
echo -e "       Push only:     ${CYAN}./deploy_cluster.sh --push --ips${NC}"
echo -e "       Start only:    ${CYAN}./deploy_cluster.sh --start${NC}"
echo -e "       Stop cluster:  ${CYAN}./deploy_cluster.sh --stop${NC}"
echo -e "       Restore node:  ${CYAN}./deploy_cluster.sh --restore-node 3${NC}"
echo -e "       Check status:  ${CYAN}./deploy_status.sh${NC}"
echo ""
