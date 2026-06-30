#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Build Check — Chỉ kiểm tra build, không deploy/run
#  Chạy: bash build_check.sh [--go-only | --rust-only | --all]
# ═══════════════════════════════════════════════════════════════════
set -e

# ─── Paths ────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_ROOT="$REPO_ROOT/execution"
RUST_ROOT="$REPO_ROOT/consensus/metanode"
NOMT_FFI_DIR="$GO_ROOT/pkg/nomt_ffi/rust_lib"

# ─── Colors ───────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

PASS=0
FAIL=0
START_TIME=$(date +%s)

# ─── Parse args ───────────────────────────────────────────────────
BUILD_GO=true
BUILD_RUST=true
RELEASE=true

while [[ $# -gt 0 ]]; do
    case "$1" in
        --go-only)
            BUILD_RUST=false
            shift
            ;;
        --rust-only)
            BUILD_GO=false
            shift
            ;;
        --debug|--fast)
            RELEASE=false
            shift
            ;;
        --release)
            RELEASE=true
            shift
            ;;
        --all)
            shift
            ;;
        *)
            echo "Usage: $0 [--go-only | --rust-only | --all] [--debug | --fast | --release]"
            exit 1
            ;;
    esac
done

echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo -e "${CYAN}  🔨 Build Check — $(date '+%Y-%m-%d %H:%M:%S')${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
echo ""

# ─── Helper ───────────────────────────────────────────────────────
run_step() {
    local name="$1"
    shift
    echo -e "${YELLOW}▶ ${name}...${NC}"
    local step_start=$(date +%s)
    
    # Run command and capture exit code
    if "$@"; then
        local step_end=$(date +%s)
        echo -e "${GREEN}  ✅ ${name} — OK ($(( step_end - step_start ))s)${NC}"
        PASS=$((PASS + 1))
    else
        local step_end=$(date +%s)
        echo -e "${RED}  ❌ ${name} — FAILED ($(( step_end - step_start ))s)${NC}"
        FAIL=$((FAIL + 1))
    fi
    echo ""
}

# ═══════════════════════════════════════════════════════════════════

# ═══════════════════════════════════════════════════════════════════
# 2. RUST BUILDS
# ═══════════════════════════════════════════════════════════════════
# Calculate safe parallel compiler jobs based on RAM and CPU
TOTAL_MEM_GB=$(free -g 2>/dev/null | awk '/^Mem:/{print $2}' || echo 8)
if [ -z "$TOTAL_MEM_GB" ] || [ "$TOTAL_MEM_GB" -le 0 ]; then
    TOTAL_MEM_GB=8
fi
NUM_CORES=$(nproc 2>/dev/null || echo 2)

# Rust limits: each job needs ~3GB RAM. Limit to max 24 jobs.
RUST_JOBS=$(( TOTAL_MEM_GB / 3 ))
if [ $RUST_JOBS -lt 1 ]; then RUST_JOBS=1; fi
if [ $RUST_JOBS -gt $NUM_CORES ]; then RUST_JOBS=$NUM_CORES; fi
if [ $RUST_JOBS -gt 24 ]; then RUST_JOBS=24; fi

# Go limits: CGO linking needs ~4-5GB RAM per process. Limit to max 8 jobs.
GO_JOBS=$(( TOTAL_MEM_GB / 5 ))
if [ $GO_JOBS -lt 1 ]; then GO_JOBS=1; fi
if [ $GO_JOBS -gt $NUM_CORES ]; then GO_JOBS=$NUM_CORES; fi
if [ $GO_JOBS -gt 8 ]; then GO_JOBS=8; fi

if [ "$BUILD_RUST" = true ]; then
    echo -e "${CYAN}─── Rust Builds (Dùng ${RUST_JOBS}/${NUM_CORES} cores) ──────────────────${NC}"

    CARGO_FLAGS="--release --locked"
    if [ "$RELEASE" = false ]; then
        CARGO_FLAGS="--locked"
    fi

    # Consensus (metanode binary)
    run_step "Consensus metanode (cargo build $CARGO_FLAGS)" \
        bash -c "cd '$RUST_ROOT' && cargo build $CARGO_FLAGS -j $RUST_JOBS"

    # Rust NOMT FFI
    run_step "Rust NOMT FFI (cargo build $CARGO_FLAGS -p mtn-nomt-ffi)" \
        bash -c "cd '$REPO_ROOT' && cargo build $CARGO_FLAGS -p mtn-nomt-ffi -j $RUST_JOBS"
fi

# ═══════════════════════════════════════════════════════════════════
# 3. GO BUILDS
# ═══════════════════════════════════════════════════════════════════
if [ "$BUILD_GO" = true ]; then
    if [ "$FAIL" -ne 0 ]; then
        echo -e "${YELLOW}⚠️  Skipping Go build because preceding Rust compilation failed.${NC}"
        FAIL=$((FAIL + 1))
    else
        # Ensure Go links against the newly compiled static library by copying it to the sub-package target dir
        mkdir -p "$RUST_ROOT/target/release"
        mkdir -p "$REPO_ROOT/target/release"
        
        if [ "$RELEASE" = false ]; then
            cp "$REPO_ROOT/target/debug/libmetanode.a" "$RUST_ROOT/target/release/libmetanode.a" 2>/dev/null || true
            cp "$REPO_ROOT/target/debug/libmtn_nomt.a" "$REPO_ROOT/target/release/libmtn_nomt.a" 2>/dev/null || true
        else
            cp "$REPO_ROOT/target/release/libmetanode.a" "$RUST_ROOT/target/release/libmetanode.a" 2>/dev/null || true
        fi
        echo -e "${CYAN}─── Go Builds (Dùng ${GO_JOBS}/${NUM_CORES} cores) ────────────────────${NC}"

        # Go simple_chain binary
        run_step "Go simple_chain (go build)" \
            bash -c "cd '$GO_ROOT/cmd/simple_chain' && export CGO_ENABLED=1 && rm -f simple_chain && touch '$GO_ROOT/executor/ffi_bridge.go' '$GO_ROOT/pkg/nomt_ffi/bridge.go' && go build -p $GO_JOBS -o simple_chain ."
    fi
fi

# ═══════════════════════════════════════════════════════════════════
# SUMMARY
# ═══════════════════════════════════════════════════════════════════
END_TIME=$(date +%s)
TOTAL_TIME=$(( END_TIME - START_TIME ))

echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
if [ "$FAIL" -eq 0 ]; then
    echo -e "${GREEN}  ✅ ALL BUILDS PASSED ($PASS/$PASS) — ${TOTAL_TIME}s${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
    exit 0
else
    echo -e "${RED}  ❌ BUILD FAILED ($FAIL failures, $(( PASS + FAIL )) total) — ${TOTAL_TIME}s${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════${NC}"
    exit 1
fi
