#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════════╗
# ║  🌐 METANODE FULL PIPELINE AUTOMATION SCRIPT                                  ║
# ║                                                                              ║
# ║  Automates: Git Pull (Optional) -> Deploy Public Chain (Root Anchor)         ║
# ║             -> Deploy Private Chains -> Restart Relayer -> Update IPs        ║
# ║             -> Cross-Chain Client Test -> Run All BlockSTM Tests             ║
# ╚══════════════════════════════════════════════════════════════════════════════╝

set -e

# Màu sắc hiển thị
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# Resolve absolute path even when run via symlink
REAL_SCRIPT_PATH="$(readlink -f "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd "$(dirname "${REAL_SCRIPT_PATH}")" && pwd)"
METANODE_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SUITE_DIR="$(cd "${METANODE_DIR}/../metanode-suite" 2>/dev/null && pwd || echo "${METANODE_DIR}/metanode-suite")"

DO_PULL=false
GIT_BRANCH="dev"
SKIP_TESTS=false
SKIP_CROSS_CHAIN=false

print_banner() {
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}${MAGENTA}🚀 METANODE AUTOMATED FULL DEPLOYMENT & TEST PIPELINE${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "📁 ${BOLD}Metanode Directory:${NC}      ${METANODE_DIR}"
    echo -e "📁 ${BOLD}Metanode Suite Directory:${NC} ${SUITE_DIR}"
    echo -e "🌿 ${BOLD}Git Pull Option:${NC}          $( [ "$DO_PULL" = true ] && echo -e "${GREEN}Enabled (Branch: ${GIT_BRANCH})${NC}" || echo -e "${YELLOW}Disabled (Using local workspace)${NC}" )"
    echo -e "🧪 ${BOLD}Run BlockSTM Tests:${NC}       $( [ "$SKIP_TESTS" = true ] && echo -e "${YELLOW}Skipped${NC}" || echo -e "${GREEN}Enabled (run_all_tests.sh)${NC}" )"
    echo -e "🌉 ${BOLD}Run Cross-Chain Test:${NC}     $( [ "$SKIP_CROSS_CHAIN" = true ] && echo -e "${YELLOW}Skipped${NC}" || echo -e "${GREEN}Enabled (cross-chain/run_all_tests.sh)${NC}" )"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}\n"
}

usage() {
    echo -e "${BOLD}Cách sử dụng:${NC} $0 [OPTIONS]"
    echo ""
    echo -e "${BOLD}Tùy chọn Git (Git Options):${NC}"
    echo -e "  ${CYAN}-p, --pull${NC}                  Kéo mã nguồn mới nhất từ remote Git trước khi chạy"
    echo -e "  ${CYAN}-b, --branch <name>${NC}         Chỉ định nhánh git để pull (mặc định: ${YELLOW}dev${NC})"
    echo ""
    echo -e "${BOLD}Tùy chọn kiểm thử (Test Options):${NC}"
    echo -e "  ${CYAN}--skip-tests${NC}                Bỏ qua bước chạy bộ kiểm thử Block-STM (run_all_tests.sh)"
    echo -e "  ${CYAN}--skip-cross-chain${NC}          Bỏ qua bước kiểm thử chuyển tiền Cross-Chain"
    echo ""
    echo -e "${BOLD}Trợ giúp (Help):${NC}"
    echo -e "  ${CYAN}-h, --help${NC}                  Hiển thị menu hướng dẫn này"
    echo ""
    echo -e "${BOLD}Ví dụ thực thi:${NC}"
    echo -e "  ${GREEN}$0${NC}                                    # Chạy toàn bộ pipeline từ code hiện tại trên máy"
    echo -e "  ${GREEN}$0 --pull${NC}                             # Pull từ origin/dev về rồi deploy & chạy toàn bộ test"
    echo -e "  ${GREEN}$0 --pull --branch=cross-chain-registry${NC} # Pull từ origin/cross-chain-registry về rồi chạy"
    echo -e "  ${GREEN}$0 --skip-tests${NC}                       # Deploy toàn bộ, bật relayer & test cross-chain (bỏ qua BlockSTM)"
    exit 0
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        -p|--pull)
            DO_PULL=true
            shift
            ;;
        -b|--branch)
            GIT_BRANCH="$2"
            DO_PULL=true
            shift 2
            ;;
        --branch=*)
            GIT_BRANCH="${1#*=}"
            DO_PULL=true
            shift
            ;;
        --skip-tests)
            SKIP_TESTS=true
            shift
            ;;
        --skip-cross-chain)
            SKIP_CROSS_CHAIN=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo -e "${RED}❌ Tùy chọn không hợp lệ: $1${NC}"
            usage
            ;;
    esac
done

print_banner

# ==============================================================================
# BƯỚC 0: PULL CODE TỪ GIT NẾU CÓ OPTION --pull
# ==============================================================================
if [ "$DO_PULL" = true ]; then
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}📥 [BƯỚC 0/6] PULL CODE MỚI NHẤT TỪ GIT (Nhánh: ${GIT_BRANCH})${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    
    echo -e "🔄 Đang cập nhật repository Metanode (${METANODE_DIR})..."
    cd "${METANODE_DIR}"
    git checkout "${GIT_BRANCH}" || git checkout -b "${GIT_BRANCH}" "origin/${GIT_BRANCH}" || true
    git pull origin "${GIT_BRANCH}"
    echo -e "${GREEN}✅ Đã cập nhật Metanode thành công! Commit hiện tại: $(git rev-parse --short HEAD)${NC}\n"

    if [ -d "${SUITE_DIR}/.git" ]; then
        echo -e "🔄 Đang cập nhật repository Metanode Suite (${SUITE_DIR})..."
        cd "${SUITE_DIR}"
        git pull || true
        echo -e "${GREEN}✅ Đã cập nhật Metanode Suite thành công!${NC}\n"
    fi
fi

# ==============================================================================
# BƯỚC 1: DEPLOY PUBLIC CHAIN CLUSTER (ROOT ANCHOR - CHAIN 991)
# ==============================================================================
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}🏗️  [BƯỚC 1/6] TRIỂN KHAI PUBLIC CHAIN CLUSTER (ROOT ANCHOR - CHAIN 991)${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
cd "${METANODE_DIR}/deploy/ansible"
./ansible_deploy.sh --reset-all
echo -e "${GREEN}✅ Triển khai Public Chain (Root Anchor) hoàn tất!${NC}\n"

# ==============================================================================
# BƯỚC 2: DEPLOY 4 PRIVATE CHAINS (101, 102, 103, 104) & BOOTSTRAP GATEWAY
# ==============================================================================
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}🌐 [BƯỚC 2/6] TRIỂN KHAI PRIVATE CHAINS & ĐĂNG KÝ DANH BẠ GATEWAY${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
cd "${METANODE_DIR}/deploy/ansible_private_chains"
./deploy_private_chains.sh --reset-all
echo -e "${GREEN}✅ Triển khai Private Chains & cấp hạn mức Genesis thành công!${NC}\n"

# ==============================================================================
# BƯỚC 3: KHỞI ĐỘNG LẠI CROSS-CHAIN RELAYER DAEMON
# ==============================================================================
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}🔄 [BƯỚC 3/6] KHỞI ĐỘNG LẠI CROSS-CHAIN RELAYER DAEMON TRONG TMUX${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
cd "${METANODE_DIR}/deploy/ansible_private_chains"
./run_relayer_tmux.sh restart
echo -e "⏳ Đợi 3 giây để Relayer kết nối WebSocket và sẵn sàng..."
sleep 3
echo -e "${GREEN}✅ Relayer Daemon đã hoạt động ổn định!${NC}\n"

# ==============================================================================
# BƯỚC 4: CẬP NHẬT CẤU HÌNH IP / RPC ENDPOINTS TRONG METANODE-SUITE
# ==============================================================================
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}⚙️  [BƯỚC 4/6] CẬP NHẬT IP & RPC ENDPOINTS CHO BỘ TEST (UPDATE-IP)${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
cd "${SUITE_DIR}/scripts/update-ip"
./update-ip.sh
echo -e "${GREEN}✅ Đã đồng bộ toàn bộ file cấu hình test!${NC}\n"

# ==============================================================================
# BƯỚC 5: CHẠY FULL BỘ TEST CROSS-CHAIN (CROSS-CHAIN RUN_ALL_TESTS.SH)
# ==============================================================================
if [ "$SKIP_CROSS_CHAIN" = false ]; then
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}🌉 [BƯỚC 5/6] CHẠY FULL BỘ TEST CROSS-CHAIN (RUN_ALL_TESTS.SH)${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    CC_TEST_DIR="${SUITE_DIR}/test-simple/test-rpc/test-blockstm/cross-chain"
    if [ -f "${CC_TEST_DIR}/run_all_tests.sh" ]; then
        cd "${CC_TEST_DIR}"
        chmod +x ./run_all_tests.sh
        ./run_all_tests.sh
    else
        cd "${CC_TEST_DIR}/01-client-only-transfer"
        go run main.go
    fi
    echo -e "${GREEN}✅ Toàn bộ bộ kiểm thử Cross-Chain đã hoàn tất xuất sắc!${NC}\n"
else
    echo -e "${YELLOW}⏭️  Bỏ qua Bước 5: Test Cross-Chain (--skip-cross-chain)${NC}\n"
fi

# ==============================================================================
# BƯỚC 6: CHẠY TOÀN BỘ BỘ TEST BLOCK-STM
# ==============================================================================
if [ "$SKIP_TESTS" = false ]; then
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}🧪 [BƯỚC 6/6] CHẠY TOÀN BỘ BỘ KIỂM THỬ BLOCK-STM (RUN_ALL_TESTS.SH)${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    cd "${SUITE_DIR}/test-simple/test-rpc/test-blockstm"
    ./run_all_tests.sh
    echo -e "${GREEN}✅ Toàn bộ bài test Block-STM đã hoàn thành!${NC}\n"
else
    echo -e "${YELLOW}⏭️  Bỏ qua Bước 6: Test Block-STM (--skip-tests)${NC}\n"
fi

echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
echo -e "${BOLD}${GREEN}🎉🎉🎉 CHÚC MỪNG! TOÀN BỘ QUY TRÌNH TRIỂN KHAI VÀ KIỂM THỬ ĐÃ THÀNH CÔNG RỰC RỠ!${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
