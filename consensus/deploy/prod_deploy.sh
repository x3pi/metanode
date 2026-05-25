#!/bin/bash
# ╔═══════════════════════════════════════════════════════════════════╗
# ║  PROD DEPLOY — Build + Push + Start cluster lên production        ║
# ║                                                                   ║
# ║  Usage:                                                           ║
# ║    ./prod_deploy.sh --all              # Full deploy              ║
# ║    ./prod_deploy.sh --update-ips       # Chỉ update IPs          ║
# ║    ./prod_deploy.sh --build            # Chỉ build binary         ║
# ║    ./prod_deploy.sh --push             # Chỉ copy sang servers    ║
# ║    ./prod_deploy.sh --start            # Chỉ start nodes          ║
# ║    ./prod_deploy.sh --stop             # Dừng tất cả nodes        ║
# ║    ./prod_deploy.sh --status           # Kiểm tra trạng thái      ║
# ╚═══════════════════════════════════════════════════════════════════╝

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/prod_deploy.env}"

# ─── Load config ─────────────────────────────────────────────────────
if [ ! -f "$ENV_FILE" ]; then
    echo "❌ Config file not found: $ENV_FILE"
    echo "   Copy template first: cp prod_deploy.env.template prod_deploy.env"
    echo "   Then fill in your real server IPs and SSH key path."
    exit 1
fi
# shellcheck source=prod_deploy.env.template
source "$ENV_FILE"

# ─── Colors ──────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_step()  { echo -e "  ${BLUE}►${NC} $*"; }
log_phase() { echo -e "\n${CYAN}${BOLD}═══ $* ═══${NC}"; }

# ─── SSH helper ──────────────────────────────────────────────────────
ssh_cmd() {
    local host="$1"; shift
    if [ "${SSH_AUTH:-key}" = "key" ]; then
        ssh -i "${SSH_KEY:-$HOME/.ssh/id_rsa}" ${SSH_OPTS:-} "$SSH_USER@$host" "$@"
    else
        sshpass -p "$SSH_PASSWORD" ssh ${SSH_OPTS:-} "$SSH_USER@$host" "$@"
    fi
}

scp_to() {
    local src="$1"; local host="$2"; local dst="$3"
    if [ "${SSH_AUTH:-key}" = "key" ]; then
        scp -i "${SSH_KEY:-$HOME/.ssh/id_rsa}" ${SSH_OPTS:-} -r "$src" "$SSH_USER@$host:$dst"
    else
        sshpass -p "$SSH_PASSWORD" scp ${SSH_OPTS:-} -r "$src" "$SSH_USER@$host:$dst"
    fi
}

rsync_to() {
    local src="$1"; local host="$2"; local dst="$3"
    if [ "${SSH_AUTH:-key}" = "key" ]; then
        rsync -az --progress -e "ssh -i ${SSH_KEY:-$HOME/.ssh/id_rsa} ${SSH_OPTS:-}" "$src" "$SSH_USER@$host:$dst"
    else
        sshpass -p "$SSH_PASSWORD" rsync -az --progress -e "ssh ${SSH_OPTS:-}" "$src" "$SSH_USER@$host:$dst"
    fi
}

# ─── Get unique servers (dedup Node → Server mapping) ────────────────
get_remote_servers() {
    local -A seen
    for node in 0 1 2 3 4; do
        local srv="${NODE_SERVER[$node]}"
        if [ "${seen[$srv]:-}" = "" ] && [ "$srv" != "$SERVER_A" ]; then
            seen[$srv]=1
            echo "$srv"
        fi
    done
}

# ─── Step: Update IPs ────────────────────────────────────────────────
step_update_ips() {
    log_phase "Update IPs in config files"
    local ips=()
    for node in 0 1 2 3 4; do
        ips+=("${NODE_SERVER[$node]}")
    done
    log_step "Running update_ips.sh with: ${ips[*]}"
    "$LOCAL_SCRIPTS/node/update_ips.sh" "${ips[@]}"
    log_info "✅ IPs updated in all config files"
}

# ─── Step: Build ─────────────────────────────────────────────────────
step_build() {
    log_phase "Build Binary (Rust + FFI + Go)"
    log_step "Running build_check.sh..."
    "$LOCAL_SCRIPTS/build_check.sh"
    log_info "✅ Build successful"
}

# ─── Step: Push to remote servers ────────────────────────────────────
step_push() {
    log_phase "Push binaries and configs to remote servers"

    local remote_servers
    remote_servers=$(get_remote_servers)

    for server in $remote_servers; do
        log_step "Pushing to $server..."

        # Create remote directories
        ssh_cmd "$server" "mkdir -p $REMOTE_GO_SIMPLE $REMOTE_RUST_DIR/config $REMOTE_RUST_DIR/target/release $REMOTE_SCRIPTS/node"

        # Push Go binary
        log_step "  → Go binary (simple_chain)..."
        rsync_to "$LOCAL_GO_SIMPLE/simple_chain" "$server" "$REMOTE_GO_SIMPLE/"

        # Push Rust binary (embedded as libmetanode.a via FFI — not needed as standalone)
        # The Go binary already has Rust FFI linked in.

        # Push Go configs for nodes on this server
        for node in 0 1 2 3 4; do
            if [ "${NODE_SERVER[$node]}" = "$server" ]; then
                log_step "  → Config for Node $node..."
                rsync_to "$LOCAL_GO_SIMPLE/config-master-node${node}.json" "$server" "$REMOTE_GO_SIMPLE/"
                rsync_to "$LOCAL_RUST_DIR/config/node_${node}.toml" "$server" "$REMOTE_RUST_DIR/config/"
            fi
        done

        # Push genesis
        log_step "  → genesis.json..."
        rsync_to "$LOCAL_GO_SIMPLE/genesis.json" "$server" "$REMOTE_GO_SIMPLE/"

        # Push scripts (orchestrator)
        log_step "  → Scripts..."
        rsync_to "$LOCAL_SCRIPTS/mtn-orchestrator.sh" "$server" "$REMOTE_SCRIPTS/"
        rsync_to "$LOCAL_SCRIPTS/node/" "$server" "$REMOTE_SCRIPTS/node/"

        # Fix permissions
        ssh_cmd "$server" "chmod +x $REMOTE_GO_SIMPLE/simple_chain $REMOTE_SCRIPTS/mtn-orchestrator.sh $REMOTE_SCRIPTS/node/*.sh 2>/dev/null || true"

        log_info "  ✅ $server done"
    done

    log_info "✅ Push complete"
}

# ─── Step: Start nodes ───────────────────────────────────────────────
step_start() {
    log_phase "Start all nodes"

    # Group nodes by server
    declare -A server_nodes
    for node in 0 1 2 3 4; do
        local srv="${NODE_SERVER[$node]}"
        server_nodes[$srv]="${server_nodes[$srv]:-} $node"
    done

    for server in "${!server_nodes[@]}"; do
        local nodes="${server_nodes[$server]}"
        log_step "Starting nodes [$nodes] on $server..."

        if [ "$server" = "$SERVER_A" ]; then
            # Local: start via orchestrator directly
            for node in $nodes; do
                "$LOCAL_SCRIPTS/mtn-orchestrator.sh" start-node "$node"
                sleep 3
            done
        else
            # Remote: start via SSH
            for node in $nodes; do
                ssh_cmd "$server" "cd $REMOTE_SCRIPTS && ./mtn-orchestrator.sh start-node $node"
                sleep 3
            done
        fi
        log_info "  ✅ $server started"
    done
}

# ─── Step: Stop nodes ────────────────────────────────────────────────
step_stop() {
    log_phase "Stop all nodes (graceful)"

    declare -A server_nodes
    for node in 0 1 2 3 4; do
        local srv="${NODE_SERVER[$node]}"
        server_nodes[$srv]="${server_nodes[$srv]:-} $node"
    done

    for server in "${!server_nodes[@]}"; do
        local nodes="${server_nodes[$server]}"
        log_step "Stopping nodes [$nodes] on $server..."

        if [ "$server" = "$SERVER_A" ]; then
            for node in $nodes; do
                "$LOCAL_SCRIPTS/mtn-orchestrator.sh" stop-node "$node" || true
            done
        else
            for node in $nodes; do
                ssh_cmd "$server" "cd $REMOTE_SCRIPTS && ./mtn-orchestrator.sh stop-node $node" || true
            done
        fi
        log_info "  ✅ $server stopped"
    done
}

# ─── Step: Status ────────────────────────────────────────────────────
step_status() {
    log_phase "Cluster Status"
    echo ""
    printf "%-6s %-18s %-8s %-12s\n" "Node" "Server" "Port" "Block"
    printf "%-6s %-18s %-8s %-12s\n" "------" "------------------" "--------" "------------"

    local all_ok=true
    for node in 0 1 2 3 4; do
        local server="${NODE_SERVER[$node]}"
        local port="${NODE_RPC_PORT[$node]}"

        # Query block height (local or remote)
        local height
        if [ "$server" = "$SERVER_A" ]; then
            height=$(curl -sf --max-time 3 -X POST "http://localhost:$port" \
                -H "Content-Type: application/json" \
                -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
                | python3 -c "import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))" \
                2>/dev/null || echo "")
        else
            height=$(ssh_cmd "$server" "curl -sf --max-time 3 -X POST http://localhost:$port \
                -H 'Content-Type: application/json' \
                -d '{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}' \
                | python3 -c \"import sys,json; print(int(json.load(sys.stdin).get('result','0x0'),16))\"" \
                2>/dev/null || echo "")
        fi

        if [ -n "$height" ]; then
            printf "%-6s %-18s %-8s ${GREEN}%-12s${NC}\n" "$node" "$server" "$port" "block #$height"
        else
            printf "%-6s %-18s %-8s ${RED}%-12s${NC}\n" "$node" "$server" "$port" "DOWN"
            all_ok=false
        fi
    done

    echo ""
    if $all_ok; then
        echo -e "  ${GREEN}${BOLD}✅ All 5 nodes are running${NC}"
    else
        echo -e "  ${RED}${BOLD}❌ Some nodes are DOWN${NC}"
    fi
    echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────
print_usage() {
    echo ""
    echo -e "${BOLD}Usage:${NC} $0 [OPTIONS]"
    echo ""
    echo "  --all          Full deploy: update-ips + build + push + start"
    echo "  --update-ips   Update IPs in all config files"
    echo "  --build        Build Rust + Go binary"
    echo "  --push         Push binaries + configs to remote servers"
    echo "  --start        Start all nodes"
    echo "  --stop         Stop all nodes gracefully"
    echo "  --status       Check status of all nodes"
    echo ""
    echo "Environment:"
    echo "  ENV_FILE=path  Path to config file (default: prod_deploy.env)"
    echo ""
    echo "Examples:"
    echo "  ./prod_deploy.sh --all"
    echo "  ./prod_deploy.sh --stop"
    echo "  ENV_FILE=staging.env ./prod_deploy.sh --status"
    echo ""
}

if [ $# -eq 0 ]; then
    print_usage
    exit 0
fi

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🚀 METANODE PRODUCTION DEPLOY                          ║${NC}"
echo -e "${BOLD}║  Config: $(basename "$ENV_FILE")$(printf '%*s' $((44 - ${#ENV_FILE} + ${#ENV_FILE} - ${#ENV_FILE##*/})) '')║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""

case "$1" in
    --all)
        step_update_ips
        step_build
        step_push
        step_start
        echo ""
        sleep 5
        step_status
        ;;
    --update-ips) step_update_ips ;;
    --build)      step_build ;;
    --push)       step_push ;;
    --start)      step_start ;;
    --stop)       step_stop ;;
    --status)     step_status ;;
    --help|-h)    print_usage ;;
    *)
        log_error "Unknown option: $1"
        print_usage
        exit 1
        ;;
esac
