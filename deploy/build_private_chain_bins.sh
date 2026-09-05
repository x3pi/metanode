#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════════╗
# ║  🛠️  METANODE PRIVATE CHAIN KIT — BINARY BUILDER & SYNC TOOL                 ║
# ║                                                                              ║
# ║  Biên dịch mới nhất từ mã nguồn repo và cập nhật vào thư mục:                ║
# ║  deploy/private_chain_kit/bin/                                               ║
# ║                                                                              ║
# ║  Bao gồm 5 file nhị phân:                                                    ║
# ║  1. metanode            (Consensus Engine - Rust)                           ║
# ║  2. simple_chain        (Execution Node - Go + CGO + Rust FFI + MVM)        ║
# ║  3. cross_chain_relayer (Relayer daemon - Go)                                ║
# ║  4. register_chains     (On-chain Chain & BLS Registration Tool - Go)        ║
# ║  5. bls_pubkey          (BLS G1 Pubkey Derivation Tool - Go)                 ║
# ╚══════════════════════════════════════════════════════════════════════════════╝

set -e

# ─── Paths ────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_ROOT="$REPO_ROOT/execution"
RUST_ROOT="$REPO_ROOT/consensus/metanode"
DEFAULT_DEST_BIN="$SCRIPT_DIR/private_chain_kit/bin"
DEST_BIN="${DEST_BIN:-$DEFAULT_DEST_BIN}"

# ─── Colors ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# ─── Default flags ────────────────────────────────────────────────────────────
BUILD_ALL=true
BUILD_TOOLS_ONLY=false
BUILD_NODE_ONLY=false
CLEAN_CACHE=false

usage() {
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}${MAGENTA}🛠️  METANODE PRIVATE CHAIN KIT — BINARY BUILDER${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --all              (Mặc định) Build toàn bộ 5 binaries (Rust, Go Node, Tools)"
    echo "  --tools-only       Chỉ build các công cụ Go cross-chain (relayer, register_chains, bls_pubkey)"
    echo "  --node-only        Chỉ build 2 node cốt lõi (metanode + simple_chain)"
    echo "  --clean            Xóa sạch go build cache và cargo artifacts trước khi build"
    echo "  --dest <DIR>       Chỉ định thư mục đích (Mặc định: deploy/private_chain_kit/bin)"
    echo "  -h, --help         Hiển thị hướng dẫn này"
    echo ""
    echo "Ví dụ:"
    echo "  $0                 # Build đủ cả 5 file và cập nhật vào private_chain_kit/bin"
    echo "  $0 --tools-only    # Build siêu nhanh chỉ các tool Go cross-chain (5-10s)"
    echo "  $0 --clean         # Build lại từ đầu sạch sẽ hoàn toàn"
    echo ""
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --all)
            BUILD_ALL=true
            BUILD_TOOLS_ONLY=false
            BUILD_NODE_ONLY=false
            shift
            ;;
        --tools-only)
            BUILD_ALL=false
            BUILD_TOOLS_ONLY=true
            BUILD_NODE_ONLY=false
            shift
            ;;
        --node-only)
            BUILD_ALL=false
            BUILD_TOOLS_ONLY=false
            BUILD_NODE_ONLY=true
            shift
            ;;
        --clean)
            CLEAN_CACHE=true
            shift
            ;;
        --dest)
            DEST_BIN="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo -e "${RED}❌ Tham số không hợp lệ: $1${NC}"
            usage
            ;;
    esac
done

mkdir -p "$DEST_BIN"

echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
echo -e "${BOLD}${MAGENTA}🚀 KHỞI ĐỘNG BIÊN DỊCH BINARY CHO PRIVATE CHAIN KIT${NC}"
echo -e "📁 Thư mục nguồn Repo:  ${BLUE}$REPO_ROOT${NC}"
echo -e "🎯 Thư mục đích:        ${GREEN}$DEST_BIN${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"

# Tính toán số luồng biên dịch an toàn theo RAM và CPU
TOTAL_MEM_GB=$(free -g 2>/dev/null | awk '/^Mem:/{print $2}' || echo 8)
if [ -z "$TOTAL_MEM_GB" ] || [ "$TOTAL_MEM_GB" -le 0 ]; then TOTAL_MEM_GB=8; fi
NUM_CORES=$(nproc 2>/dev/null || echo 2)

RUST_JOBS=$(( TOTAL_MEM_GB / 3 ))
if [ $RUST_JOBS -lt 1 ]; then RUST_JOBS=1; fi
if [ $RUST_JOBS -gt $NUM_CORES ]; then RUST_JOBS=$NUM_CORES; fi
if [ $RUST_JOBS -gt 24 ]; then RUST_JOBS=24; fi

GO_JOBS=$(( TOTAL_MEM_GB / 5 ))
if [ $GO_JOBS -lt 1 ]; then GO_JOBS=1; fi
if [ $GO_JOBS -gt $NUM_CORES ]; then GO_JOBS=$NUM_CORES; fi
if [ $GO_JOBS -gt 8 ]; then GO_JOBS=8; fi

START_TOTAL=$(date +%s)

if [ "$CLEAN_CACHE" = true ]; then
    echo -e "${YELLOW}🧹 [Clean] Đang dọn dẹp Go cache...${NC}"
    (cd "$GO_ROOT" && go clean -cache)
fi

# ══════════════════════════════════════════════════════════════════════════════
# 1. BIÊN DỊCH RUST ENGINE & FFI (CHỈ CHẠY NẾU KHÔNG PHẢI TOOLS-ONLY)
# ══════════════════════════════════════════════════════════════════════════════
if [ "$BUILD_TOOLS_ONLY" = false ]; then
    echo -e "\n${BOLD}${YELLOW}▶ [1/4] Biên dịch EVM & NOMT FFI (mvm + mtn-nomt-ffi)...${NC}"
    t_start=$(date +%s)
    
    # C++ MVM Linker
    (cd "$GO_ROOT/pkg/mvm" && chmod +x build.sh && ./build.sh linux >/dev/null 2>&1 || ./build.sh linux)
    
    # Rust NOMT FFI
    (cd "$REPO_ROOT" && cargo build --release --locked -p mtn-nomt-ffi -j $RUST_JOBS)
    
    echo -e "${GREEN}  ✅ EVM & NOMT FFI hoàn tất ($(( $(date +%s) - t_start ))s)${NC}"

    echo -e "\n${BOLD}${YELLOW}▶ [2/4] Biên dịch Consensus Engine (Rust: metanode)...${NC}"
    t_start=$(date +%s)
    
    (cd "$RUST_ROOT" && cargo build --release --locked -j $RUST_JOBS)
    
    # Đồng bộ libmetanode.a để Go cgo link chính xác
    mkdir -p "$RUST_ROOT/target/release"
    mkdir -p "$REPO_ROOT/target/release"
    cp -p "$REPO_ROOT/target/release/libmetanode.a" "$RUST_ROOT/target/release/libmetanode.a" 2>/dev/null || true
    
    # Copy binary metanode sang DEST_BIN
    cp "$REPO_ROOT/target/release/metanode" "$DEST_BIN/metanode"
    chmod +x "$DEST_BIN/metanode"
    echo -e "${GREEN}  ✅ Binary metanode đã cập nhật ➔ $DEST_BIN/metanode ($(( $(date +%s) - t_start ))s)${NC}"

    echo -e "\n${BOLD}${YELLOW}▶ [3/4] Biên dịch Execution Node (Go: simple_chain)...${NC}"
    t_start=$(date +%s)
    
    (cd "$GO_ROOT/cmd/simple_chain" && CGO_ENABLED=1 go build -o "$DEST_BIN/simple_chain" .)
    chmod +x "$DEST_BIN/simple_chain"
    echo -e "${GREEN}  ✅ Binary simple_chain đã cập nhật ➔ $DEST_BIN/simple_chain ($(( $(date +%s) - t_start ))s)${NC}"
fi

# ══════════════════════════════════════════════════════════════════════════════
# 2. BIÊN DỊCH CÁC CÔNG CỤ CROSS-CHAIN GO
# ══════════════════════════════════════════════════════════════════════════════
if [ "$BUILD_NODE_ONLY" = false ]; then
    echo -e "\n${BOLD}${YELLOW}▶ [4/4] Biên dịch Go Cross-Chain Tools...${NC}"
    t_start=$(date +%s)
    
    echo -e "    🔨 Building cross_chain_relayer..."
    (cd "$GO_ROOT" && go build -o "$DEST_BIN/cross_chain_relayer" ./cmd/tool/cross_chain_relayer)
    chmod +x "$DEST_BIN/cross_chain_relayer"
    
    echo -e "    🔨 Building register_chains..."
    (cd "$GO_ROOT" && go build -o "$DEST_BIN/register_chains" ./cmd/tool/register_chains)
    chmod +x "$DEST_BIN/register_chains"
    
    echo -e "    🔨 Building bls_pubkey..."
    (cd "$GO_ROOT" && go build -o "$DEST_BIN/bls_pubkey" ./cmd/tool/bls_pubkey)
    chmod +x "$DEST_BIN/bls_pubkey"
    
    echo -e "${GREEN}  ✅ Bộ công cụ Go Cross-Chain đã cập nhật vào $DEST_BIN ($(( $(date +%s) - t_start ))s)${NC}"
fi

TOTAL_TIME=$(( $(date +%s) - START_TOTAL ))

echo -e "\n${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
echo -e "${BOLD}${GREEN}🎉 HOÀN TẤT BIÊN DỊCH VÀ ĐỒNG BỘ TOÀN BỘ BINARY (${TOTAL_TIME}s)${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
echo -e "Danh sách file nhị phân trong ${BLUE}$DEST_BIN${NC}:"
ls -lh "$DEST_BIN" | awk 'NR>1 {printf "   ├─ %-22s %8s  (%s)\n", $9, $5, $6" "$7" "$8}'
echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Sẵn sàng đóng gói hoặc chạy thử nghiệm!${NC}\n"
