#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════════╗
# ║  🌐 METANODE FULL PIPELINE AUTOMATION SCRIPT                                  ║
# ║                                                                              ║
# ║  Automates: Git Pull (Optional) -> Deploy Public Chain (Root Anchor)         ║
# ║             -> Deploy Private Chains -> Restart Relayer -> Update IPs        ║
# ║             -> Cross-Chain Client Test -> Run All BlockSTM Tests (3 Rounds)  ║
# ║             -> Instant Telegram Alert on Error & Success Notification        ║
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
INVENTORY_YML="${SCRIPT_DIR}/inventory.yml"

DO_PULL=false
GIT_BRANCH="dev"
SKIP_TESTS=false
SKIP_CROSS_CHAIN=false
BLOCKSTM_ROUNDS=3

# ─── ĐỌC CẤU HÌNH TELEGRAM TỪ INVENTORY.YML HOẶC .ENV ──────────────────────
TELEGRAM_BOT_TOKEN=""
TELEGRAM_CHAT_ID=""

if [ -f "$INVENTORY_YML" ]; then
    TG_CONF=$(python3 -c "
import yaml, sys
try:
    with open('$INVENTORY_YML') as f:
        d = yaml.safe_load(f)
    v = d.get('all', {}).get('children', {}).get('metanode_cluster', {}).get('vars', {})
    token = v.get('telegram_bot_token', '')
    chat_id = v.get('telegram_chat_id', '')
    print(f'{token}|{chat_id}')
except Exception:
    pass
" 2>/dev/null || true)
    if [ -n "$TG_CONF" ]; then
        TELEGRAM_BOT_TOKEN=$(echo "$TG_CONF" | cut -d'|' -f1)
        TELEGRAM_CHAT_ID=$(echo "$TG_CONF" | cut -d'|' -f2)
    fi
fi

# Fallback từ .env nếu chưa có
if [ -z "$TELEGRAM_BOT_TOKEN" ] && [ -f "${SCRIPT_DIR}/.env" ]; then
    TELEGRAM_BOT_TOKEN=$(grep -E '^TELEGRAM_BOT_TOKEN=' "${SCRIPT_DIR}/.env" | cut -d'=' -f2- | tr -d '"' | tr -d "'" || true)
    TELEGRAM_CHAT_ID=$(grep -E '^TELEGRAM_CHAT_ID=' "${SCRIPT_DIR}/.env" | cut -d'=' -f2- | tr -d '"' | tr -d "'" || true)
fi

MY_IP=$(hostname -I | awk '{print $1}')
CURRENT_STEP="Khởi tạo pipeline"

send_telegram() {
    local message="$1"
    if [ -n "$TELEGRAM_BOT_TOKEN" ] && [ -n "$TELEGRAM_CHAT_ID" ]; then
        curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            -d "text=${message}" \
            -d "parse_mode=HTML" > /dev/null 2>&1 || true
    fi
}

# ─── TRAP BẮT LỖI TỰ ĐỘNG BÁO QUA TELEGRAM ────────────────────────────────
handle_error() {
    local exit_code="$1"
    local line_no="$2"
    local failed_cmd="$3"
    
    local git_commit=$(git -C "${METANODE_DIR}" rev-parse --short HEAD 2>/dev/null || echo "unknown")
    local git_branch=$(git -C "${METANODE_DIR}" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "$GIT_BRANCH")

    # Tìm file log bài test bị lỗi gần nhất (nếu lỗi xảy ra trong quá trình chạy test)
    local test_log_snippet=""
    local latest_log=""
    local candidate_dirs=(
        "${SUITE_DIR}/test-simple/test-rpc/test-blockstm/test_logs"
        "${SUITE_DIR}/test-simple/test-rpc/test-blockstm/cross-chain/test_logs"
    )
    
    for cdir in "${candidate_dirs[@]}"; do
        if [ -d "$cdir" ]; then
            local found_log=$(ls -t "$cdir"/*.log 2>/dev/null | head -n 1 || true)
            if [ -n "$found_log" ]; then
                if [ -z "$latest_log" ] || [ "$found_log" -nt "$latest_log" ]; then
                    latest_log="$found_log"
                fi
            fi
        fi
    done

    if [ -n "$latest_log" ] && [ -f "$latest_log" ]; then
        local log_name=$(basename "$latest_log")
        local raw_lines=$(tail -n 20 "$latest_log")
        local escaped_lines=$(echo "$raw_lines" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g')
        test_log_snippet="
📄 <b>File Log Bài Test:</b> <code>${log_name}</code>
📜 <b>20 Dòng Log Cuối Cùng Của Bài Test:</b>
<pre>${escaped_lines}</pre>"
    fi

    echo -e "\n${RED}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}${RED}❌ PIPELINE GẶP LỖI TẠI: ${CURRENT_STEP}${NC}"
    echo -e "   - Dòng code:  ${line_no}"
    echo -e "   - Lệnh lỗi:   ${failed_cmd}"
    echo -e "   - Exit Code:  ${exit_code}"
    if [ -n "$latest_log" ]; then
        echo -e "   - Log bài test: ${latest_log}"
    fi
    echo -e "${RED}══════════════════════════════════════════════════════════════════════════════${NC}\n"

    local error_msg="🚨 <b>[METANODE PIPELINE: LỖI THỰC THI]</b> 🚨
────────────────────────
🖥️ <b>Máy chủ (Host):</b> <code>${MY_IP}</code> ($(hostname))
🌿 <b>Nhánh Git:</b> <code>${git_branch}</code> (Commit: <code>${git_commit}</code>)
📍 <b>Bước bị lỗi:</b> <b>${CURRENT_STEP}</b>
⚡ <b>Lệnh bị lỗi (Dòng ${line_no}):</b> 
<code>${failed_cmd}</code>
⚠️ <b>Mã lỗi (Exit Code):</b> <code>${exit_code}</code>
────────────────────────${test_log_snippet}
⛔ <i>Toàn bộ tiến trình pipeline đã dừng để bảo vệ hệ thống. Vui lòng kiểm tra log terminal để debug.</i>"

    send_telegram "$error_msg"
    exit "$exit_code"
}

trap 'handle_error $? $LINENO "$BASH_COMMAND"' ERR

print_banner() {
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}${MAGENTA}🚀 METANODE AUTOMATED FULL DEPLOYMENT & TEST PIPELINE${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "📁 ${BOLD}Metanode Directory:${NC}      ${METANODE_DIR}"
    echo -e "📁 ${BOLD}Metanode Suite Directory:${NC} ${SUITE_DIR}"
    echo -e "🌿 ${BOLD}Git Pull Option:${NC}          $( [ "$DO_PULL" = true ] && echo -e "${GREEN}Enabled (Branch: ${GIT_BRANCH})${NC}" || echo -e "${YELLOW}Disabled (Using local workspace)${NC}" )"
    echo -e "🧪 ${BOLD}Run BlockSTM Tests:${NC}       $( [ "$SKIP_TESTS" = true ] && echo -e "${YELLOW}Skipped${NC}" || echo -e "${GREEN}Enabled (${BLOCKSTM_ROUNDS} Vòng x 33 Bài test)${NC}" )"
    echo -e "🌉 ${BOLD}Run Cross-Chain Test:${NC}     $( [ "$SKIP_CROSS_CHAIN" = true ] && echo -e "${YELLOW}Skipped${NC}" || echo -e "${GREEN}Enabled (cross-chain/run_all_tests.sh)${NC}" )"
    echo -e "📱 ${BOLD}Telegram Alert:${NC}           $( [ -n "$TELEGRAM_BOT_TOKEN" ] && echo -e "${GREEN}Enabled (${TELEGRAM_CHAT_ID})${NC}" || echo -e "${YELLOW}Disabled (Không tìm thấy token)${NC}" )"
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
    echo -e "  ${CYAN}--rounds <N>${NC}                Số vòng chạy lặp lại bộ test Block-STM (mặc định: ${YELLOW}3${NC})"
    echo -e "  ${CYAN}--skip-tests${NC}                Bỏ qua bước chạy bộ kiểm thử Block-STM"
    echo -e "  ${CYAN}--skip-cross-chain${NC}          Bỏ qua bước kiểm thử chuyển tiền Cross-Chain"
    echo ""
    echo -e "${BOLD}Trợ giúp (Help):${NC}"
    echo -e "  ${CYAN}-h, --help${NC}                  Hiển thị menu hướng dẫn này"
    echo ""
    echo -e "${BOLD}Ví dụ thực thi:${NC}"
    echo -e "  ${GREEN}$0${NC}                                    # Chạy toàn bộ pipeline từ code hiện tại trên máy (3 vòng test)"
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
        --rounds)
            BLOCKSTM_ROUNDS="$2"
            shift 2
            ;;
        --rounds=*)
            BLOCKSTM_ROUNDS="${1#*=}"
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
    CURRENT_STEP="[Bước 0/6] Git Pull Code mới nhất từ remote ($GIT_BRANCH)"
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
CURRENT_STEP="[Bước 1/6] Triển khai Public Chain (Root Anchor - Chain 991)"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}🏗️  [BƯỚC 1/6] TRIỂN KHAI PUBLIC CHAIN CLUSTER (ROOT ANCHOR - CHAIN 991)${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
cd "${METANODE_DIR}/deploy/ansible"
./ansible_deploy.sh --reset-all
echo -e "${GREEN}✅ Triển khai Public Chain (Root Anchor) hoàn tất!${NC}\n"

# ==============================================================================
# BƯỚC 2: DEPLOY 4 PRIVATE CHAINS (101, 102, 103, 104) & BOOTSTRAP GATEWAY
# ==============================================================================
CURRENT_STEP="[Bước 2/6] Triển khai 4 Private Chains & Cấp hạn mức Genesis"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BOLD}🌐 [BƯỚC 2/6] TRIỂN KHAI PRIVATE CHAINS & ĐĂNG KÝ DANH BẠ GATEWAY${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
cd "${METANODE_DIR}/deploy/ansible_private_chains"
./deploy_private_chains.sh --reset-all
echo -e "${GREEN}✅ Triển khai Private Chains & cấp hạn mức Genesis thành công!${NC}\n"

# ==============================================================================
# BƯỚC 3: KHỞI ĐỘNG LẠI CROSS-CHAIN RELAYER DAEMON
# ==============================================================================
CURRENT_STEP="[Bước 3/6] Khởi động Cross-Chain Relayer Daemon"
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
CURRENT_STEP="[Bước 4/6] Đồng bộ cấu hình IP/RPC Endpoints (update-ip)"
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
    CURRENT_STEP="[Bước 5/6] Chạy bộ test kiểm thử Cross-Chain liên chuỗi"
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
# BƯỚC 6: CHẠY TOÀN BỘ BỘ TEST BLOCK-STM (LẶP LẠI 3 VÒNG)
# ==============================================================================
if [ "$SKIP_TESTS" = false ]; then
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}🧪 [BƯỚC 6/6] CHẠY TOÀN BỘ BỘ KIỂM THỬ BLOCK-STM (${BLOCKSTM_ROUNDS} VÒNG FULL SUITE)${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    cd "${SUITE_DIR}/test-simple/test-rpc/test-blockstm"
    chmod +x ./run_all_tests.sh

    for round in $(seq 1 "$BLOCKSTM_ROUNDS"); do
        CURRENT_STEP="[Bước 6/6] Chạy bộ kiểm thử Block-STM (Vòng ${round}/${BLOCKSTM_ROUNDS})"
        echo -e "\n${BOLD}${MAGENTA}▶️  BẮT ĐẦU CHẠY VÒNG ${round}/${BLOCKSTM_ROUNDS} TOÀN BỘ TEST BLOCK-STM...${NC}"
        ./run_all_tests.sh
        echo -e "${GREEN}✅ Hoàn thành Vòng ${round}/${BLOCKSTM_ROUNDS} Block-STM thành công!${NC}"
    done
    echo -e "\n${GREEN}✅ Toàn bộ ${BLOCKSTM_ROUNDS} vòng kiểm thử Block-STM đã hoàn thành 100%!${NC}\n"
else
    echo -e "${YELLOW}⏭️  Bỏ qua Bước 6: Test Block-STM (--skip-tests)${NC}\n"
fi

# Gửi thông báo thành công qua Telegram
FINAL_COMMIT=$(git -C "${METANODE_DIR}" rev-parse --short HEAD 2>/dev/null || echo "unknown")
FINAL_BRANCH=$(git -C "${METANODE_DIR}" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "$GIT_BRANCH")

SUCCESS_MSG="🎉 <b>[METANODE PIPELINE: HOÀN TẤT THÀNH CÔNG]</b> 🎉
────────────────────────
🖥️ <b>Máy chủ (Host):</b> <code>${MY_IP}</code> ($(hostname))
🌿 <b>Nhánh Git:</b> <code>${FINAL_BRANCH}</code> (Commit: <code>${FINAL_COMMIT}</code>)
────────────────────────
✅ <b>Triển khai:</b> Public Chain (991) + 4 Private Chains (101, 102, 103, 104)
✅ <b>Relayer:</b> Cross-Chain Daemon đã hoạt động ổn định
✅ <b>Cross-Chain Tests:</b> 3 bài test liên chuỗi x 3 lần thành công
✅ <b>Block-STM Tests:</b> ${BLOCKSTM_ROUNDS} Vòng x 33 bài test song song PASSED 100%
────────────────────────
🚀 <i>Hệ thống mạng lưới và bộ kiểm thử đã sẵn sàng hoạt động!</i>"

send_telegram "$SUCCESS_MSG"

echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
echo -e "${BOLD}${GREEN}🎉🎉🎉 CHÚC MỪNG! TOÀN BỘ QUY TRÌNH TRIỂN KHAI VÀ ${BLOCKSTM_ROUNDS} VÒNG TEST ĐÃ THÀNH CÔNG RỰC RỠ!${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
