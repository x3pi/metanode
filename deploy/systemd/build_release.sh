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
DEPLOY_BIN_DIR="$PROJECT_ROOT/deploy/bin"
# Clean up any leftover staging directories from previous interrupted runs
rm -rf "${PROJECT_ROOT}/deploy/.bin_staging."* 2>/dev/null || true
STAGING_BIN_DIR=$(mktemp -d "${PROJECT_ROOT}/deploy/.bin_staging.XXXXXX")

# Ensure cleanup of STAGING_BIN_DIR on normal exit or Ctrl+C / SIGINT / SIGTERM
cleanup_staging() {
    if [ -n "${STAGING_BIN_DIR:-}" ] && [ -d "$STAGING_BIN_DIR" ]; then
        rm -rf "$STAGING_BIN_DIR"
    fi
}
trap cleanup_staging EXIT INT TERM HUP

# ─── Parse arguments ─────────────────────────────────────────────────────────
BUILD_FAST=false
PREBUILT_DIR=""
DO_BUILD=false
DO_PACKAGE=false
export ENABLE_DEBUG_CPP=false

while [[ "$#" -gt 0 ]]; do
    case $1 in
        --fast) BUILD_FAST=true ;;
        --debug-cpp) export ENABLE_DEBUG_CPP=true ;;
        --prebuilt-dir|--bin-dir) PREBUILT_DIR="$2"; shift ;;
        --build-only) DO_BUILD=true ;;
        --package) DO_PACKAGE=true ;;
        *) log_err "Unknown parameter: $1" ;;
    esac
    shift
done

# Backward compatibility: if neither --build-only nor --package is specified, do both.
if [ "$DO_BUILD" = false ] && [ "$DO_PACKAGE" = false ]; then
    DO_BUILD=true
    DO_PACKAGE=true
fi

CARGO_FLAGS="--release"
TARGET_DIR="release"
BUILD_PROFILE="release"
if [ "$BUILD_FAST" = true ]; then
    CARGO_FLAGS=""
    TARGET_DIR="debug"
    BUILD_PROFILE="fast"
    log_info "Building in FAST (debug) mode!"
fi

log_step "Checking Dependencies"
if [ "$DO_PACKAGE" = true ]; then
    command -v tar &>/dev/null || log_err "tar command is missing."
fi
if [ "$DO_BUILD" = true ] && [ -z "$PREBUILT_DIR" ]; then
    command -v go &>/dev/null || log_err "Go compiler is not installed."
    command -v cargo &>/dev/null || log_err "Rust (cargo) is not installed."
fi
log_ok "Dependencies met."

build_binaries() {
    log_step "Building Binaries to Staging ($STAGING_BIN_DIR)"

    if [ -n "$PREBUILT_DIR" ]; then
        if [ -d "$PREBUILT_DIR" ]; then
            PREBUILT_DIR="$(cd "$PREBUILT_DIR" && pwd)"
        fi
        log_info "Copying Pre-built Binaries from $PREBUILT_DIR"
        [ -f "$PREBUILT_DIR/metanode" ] || log_err "Pre-built binary missing: $PREBUILT_DIR/metanode"
        [ -f "$PREBUILT_DIR/simple_chain" ] || log_err "Pre-built binary missing: $PREBUILT_DIR/simple_chain"

        cp "$PREBUILT_DIR/metanode" "$STAGING_BIN_DIR/"
        cp "$PREBUILT_DIR/simple_chain" "$STAGING_BIN_DIR/"
        chmod +x "$STAGING_BIN_DIR/metanode" "$STAGING_BIN_DIR/simple_chain"

        for tool in cross_chain_relayer register_chains bls_pubkey gen_recovery_committee; do
            if [ -f "$PREBUILT_DIR/$tool" ]; then
                cp "$PREBUILT_DIR/$tool" "$STAGING_BIN_DIR/"
                chmod +x "$STAGING_BIN_DIR/$tool"
                log_ok "Copied tool: $tool"
            fi
        done
        log_ok "All pre-built binaries copied to staging."
    else
        # 1. Build Rust (Consensus & FFI)
        log_step "Building Rust Consensus Engine & FFI"
        cd "$PROJECT_ROOT"
        cargo build $CARGO_FLAGS -p mtn-nomt-ffi
        cd "$PROJECT_ROOT/consensus/metanode"
        cargo build $CARGO_FLAGS

        mkdir -p "$PROJECT_ROOT/consensus/metanode/target/$TARGET_DIR"
        cp -p "$PROJECT_ROOT/target/$TARGET_DIR/libmetanode.a" "$PROJECT_ROOT/consensus/metanode/target/$TARGET_DIR/libmetanode.a" 2>/dev/null || true

        cp "$PROJECT_ROOT/target/$TARGET_DIR/metanode" "$STAGING_BIN_DIR/"
        log_ok "Metanode binary compiled."

        # 1.5. Build EVM Linker (C++)
        log_step "Building EVM Linker (C++)"
        cd "$PROJECT_ROOT/execution/pkg/mvm"
        bash build.sh
        log_ok "EVM Linker built successfully."

        # 2. Build Go (Execution & Tools)
        log_step "Building Go Execution Engine & Tools"
        cd "$PROJECT_ROOT/execution/cmd/simple_chain"
        go clean -cache
        go build -a -o simple_chain .
        cp simple_chain "$STAGING_BIN_DIR/"
        log_ok "simple_chain binary compiled."

        cd "$PROJECT_ROOT/execution"
        if [ -d "cmd/tool/gen_recovery_committee" ]; then
            go build -o gen_recovery_committee ./cmd/tool/gen_recovery_committee
            cp gen_recovery_committee "$STAGING_BIN_DIR/"
            log_ok "gen_recovery_committee compiled."
        fi

        # Tools build (optional but good to have)
        for tool in cross_chain_relayer register_chains bls_pubkey; do
            if [ -d "cmd/tool/$tool" ]; then
                go build -o "$tool" "./cmd/tool/$tool"
                cp "$tool" "$STAGING_BIN_DIR/"
            fi
        done
    fi

    # Create build metadata
    cd "$PROJECT_ROOT"
    GIT_COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "unknown")
    TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    cat <<EOF > "$STAGING_BIN_DIR/.build_metadata"
{
  "commit": "$GIT_COMMIT",
  "profile": "$BUILD_PROFILE",
  "timestamp": "$TIMESTAMP",
  "prebuilt_dir": "$PREBUILT_DIR"
}
EOF

    # Atomic move
    log_step "Publishing Binaries to $DEPLOY_BIN_DIR"
    exec 200> "$PROJECT_ROOT/deploy/.bin.lock"
    flock -x 200
    rm -rf "${DEPLOY_BIN_DIR}_old"
    if [ -d "$DEPLOY_BIN_DIR" ]; then
        mv "$DEPLOY_BIN_DIR" "${DEPLOY_BIN_DIR}_old"
    fi
    mv "$STAGING_BIN_DIR" "$DEPLOY_BIN_DIR"
    rm -rf "${DEPLOY_BIN_DIR}_old"
    flock -u 200

    log_ok "Build published successfully to $DEPLOY_BIN_DIR."
}

package_release() {
    log_step "Packaging Release Tarball"
    rm -rf "$RELEASE_DIR"
    rm -f "$PROJECT_ROOT/$TARBALL_NAME"
    mkdir -p "$RELEASE_DIR/bin"
    mkdir -p "$RELEASE_DIR/configs"
    mkdir -p "$RELEASE_DIR/cluster"

    # Ensure deploy/bin exists (either from build step or previous run)
    if [ ! -d "$DEPLOY_BIN_DIR" ]; then
        log_err "Binaries not found at $DEPLOY_BIN_DIR. Run with --build-only first."
    fi

    log_info "Copying binaries from $DEPLOY_BIN_DIR..."
    # Read from DEPLOY_BIN_DIR under lock to avoid partial reads during a concurrent build publish
    exec 200> "$PROJECT_ROOT/deploy/.bin.lock"
    flock -s 200
    cp -r "$DEPLOY_BIN_DIR"/* "$RELEASE_DIR/bin/"
    cp "$DEPLOY_BIN_DIR/.build_metadata" "$RELEASE_DIR/bin/" 2>/dev/null || true
    flock -u 200

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
    cp cluster/setup-cluster-btrfs.sh "$RELEASE_DIR/cluster/"
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
}

if [ "$DO_BUILD" = true ]; then
    build_binaries
fi

if [ "$DO_PACKAGE" = true ]; then
    package_release
fi
