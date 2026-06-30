#!/bin/bash
# ============================================================================
# Standalone Release Builder for Metanode
# Builds Go & Rust binaries and packages them into a standalone tarball
# ============================================================================

set -euo pipefail

# ─── Colors ──────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log_ok()   { echo -e "${GREEN}[OK]${NC}    $*"; }
log_info() { echo -e "${CYAN}[INFO]${NC}  $*"; }
log_step() { echo -e "\n${BOLD}${YELLOW}=== $* ===${NC}"; }
log_err()  { echo -e "\033[0;31m[ERROR]\033[0m $*" >&2; exit 1; }

# ─── Directories ─────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

RELEASE_DIR="$PROJECT_ROOT/metanode-deploy"
TARBALL_NAME="metanode-deploy.tar.gz"

# ─── Parse arguments ─────────────────────────────────────────────────────────
BUILD_FAST=false
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --fast) BUILD_FAST=true ;;
        *) log_err "Unknown parameter: $1" ;;
    esac
    shift
done

CARGO_FLAGS="--release"
TARGET_DIR="release"
if [ "$BUILD_FAST" = true ]; then
    CARGO_FLAGS=""
    TARGET_DIR="debug"
    log_info "Building in FAST (debug) mode!"
fi

log_step "Checking Dependencies"
command -v go &>/dev/null || log_err "Go compiler is not installed."
command -v cargo &>/dev/null || log_err "Rust (cargo) is not installed."
command -v tar &>/dev/null || log_err "tar command is missing."
log_ok "Dependencies met."

log_step "Cleaning old builds"
rm -rf "$RELEASE_DIR"
rm -f "$PROJECT_ROOT/$TARBALL_NAME"
mkdir -p "$RELEASE_DIR/bin"
mkdir -p "$RELEASE_DIR/configs"
mkdir -p "$RELEASE_DIR/cluster"

# ─── 1. Build Rust (Consensus & FFI) ──────────────────────────────────────────
log_step "Building Rust Consensus Engine & FFI"
cd "$PROJECT_ROOT"
# Build the FFI library first so Go execution engine can link against it
cargo build $CARGO_FLAGS -p mtn-nomt-ffi

cd "$PROJECT_ROOT/consensus/metanode"
# Build the consensus engine
cargo build $CARGO_FLAGS

# FIX WORKSPACE TARGET: Cargo places the build output in the workspace root target, but Go expects it in consensus/metanode/target
mkdir -p "$PROJECT_ROOT/consensus/metanode/target/$TARGET_DIR"
cp -p "$PROJECT_ROOT/target/$TARGET_DIR/libmetanode.a" "$PROJECT_ROOT/consensus/metanode/target/$TARGET_DIR/libmetanode.a" 2>/dev/null || true

cp "$PROJECT_ROOT/target/$TARGET_DIR/metanode" "$RELEASE_DIR/bin/"
log_ok "Metanode binary copied to release."


# ─── 2. Build Go (Execution) ────────────────────────────────────────────────
log_step "Building Go Execution Engine"
cd "$PROJECT_ROOT/execution/cmd/simple_chain"

go clean -cache
go build -a -o simple_chain .
cp simple_chain "$RELEASE_DIR/bin/"
log_ok "simple_chain binary copied to release."

# ─── 3. Build RPC Client (Go) ───────────────────────────────────────────────
log_step "Building Go RPC Proxy Client"
cd "$PROJECT_ROOT/execution/cmd/rpc/cmd/rpc-client"
go build -o rpc-client-bin .
if [ ! -f certificate.pem ]; then
    openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout private.key -out certificate.pem -subj "/CN=localhost" 2>/dev/null
fi
cp rpc-client-bin "$RELEASE_DIR/bin/"
cp certificate.pem "$RELEASE_DIR/bin/" 2>/dev/null || true
cp private.key "$RELEASE_DIR/bin/" 2>/dev/null || true
log_ok "rpc-client-bin and TLS certs copied to release."

# ─── 4. Copy Configurations ─────────────────────────────────────────────────
log_step "Collecting Scripts & Configurations"
cd "$SCRIPT_DIR"

# Copy genesis template
if [ -f "genesis.json.example" ]; then
    cp genesis.json.example "$RELEASE_DIR/configs/genesis.json"
else
    log_info "Warning: genesis.json.example not found in deploy/, skipping."
fi

# Copy RPC config templates
mkdir -p "$RELEASE_DIR/configs/rpc"
if [ -d "single-node/rpc" ]; then
    cp single-node/rpc/config-rpc.json "$RELEASE_DIR/configs/rpc/" 2>/dev/null || true
    cp single-node/rpc/config-client-tcp.json "$RELEASE_DIR/configs/rpc/" 2>/dev/null || true
    log_ok "RPC Config templates copied."
fi

# Copy main install script
cp install.sh "$RELEASE_DIR/"
cp gen_validator_entry.py "$RELEASE_DIR/"

# Copy cluster scripts
cp cluster/systemd-cluster.sh "$RELEASE_DIR/cluster/"
cp cluster/install-rpc-systemd.sh "$RELEASE_DIR/cluster/"
cp cluster/restore_snapshot_systemd.sh "$RELEASE_DIR/cluster/"
cp -r cluster/scripts "$RELEASE_DIR/cluster/" 2>/dev/null || true

# Đảm bảo quyền thực thi
chmod +x "$RELEASE_DIR/install.sh"
chmod +x "$RELEASE_DIR/gen_validator_entry.py"
chmod +x "$RELEASE_DIR/cluster/"*.sh

log_ok "Scripts & templates copied successfully."

# ─── 5. Create Tarball ──────────────────────────────────────────────────────
log_step "Packaging Release"
cd "$PROJECT_ROOT"
tar -czvf "$TARBALL_NAME" "$(basename "$RELEASE_DIR")"

log_step "DONE"
log_ok "Successfully created Standalone Release Package: ${PROJECT_ROOT}/${TARBALL_NAME}"
echo "You can now transfer ${TARBALL_NAME} to any server and deploy without source code."
