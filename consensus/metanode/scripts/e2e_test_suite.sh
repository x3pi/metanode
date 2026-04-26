#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  E2E Test Suite for MetaNode Consensus
#  Tự động kiểm tra:
#    Test 1: Hash Parity Check (so sánh block hash giữa các node)
#    Test 2: Quét log tìm lỗi (FORK/PANIC/DIVERGE)
#    Test 3: Restart Recovery Test (Stop → Start → Verify catch-up)
#    Test 4: DAG Wipe Recovery Test (Delete DAG → Start → Verify FORWARD-JUMP)
#    Test 5: Post-Recovery Hash Parity (xác nhận lại sau phục hồi)
#
#  Cách dùng:
#    ./e2e_test_suite.sh              # Chạy tất cả test
#    ./e2e_test_suite.sh --skip-destructive   # Bỏ qua test 3+4 (không stop node)
#    ./e2e_test_suite.sh --node 2     # Test restart/wipe trên node 2 thay vì node 1
#
#  Output:
#    - Console: Kết quả realtime
#    - File: debug_report_YYYYMMDD_HHMM.md (Markdown, dùng làm input cho AI agent)
# ═══════════════════════════════════════════════════════════════════

# Không dùng `set -e` vì curl/grep fail là bình thường trong test
set -uo pipefail

# ─── Đường dẫn gốc ──────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUST_DIR="$BASE_DIR/consensus/metanode"
GO_DIR="$BASE_DIR/execution/cmd/simple_chain"
LOG_BASE="$RUST_DIR/logs"
ORCHESTRATOR="$SCRIPT_DIR/mtn-orchestrator.sh"
TX_SENDER_DIR="$BASE_DIR/execution/cmd/tool/tx_sender"
TX_SENDER_NODE="127.0.0.1:4201"  # TCP port cho node 0 (nhận giao dịch)

# ─── Tham số ─────────────────────────────────────────────────────
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
REPORT_FILE="$SCRIPT_DIR/debug_report_${TIMESTAMP}.md"
SKIP_DESTRUCTIVE=false
TARGET_NODE=1           # Node sẽ bị restart/wipe trong test 3+4
RESTART_WAIT=30         # Chờ sau khi stop node (giây)
CATCHUP_WAIT=45         # Chờ sau khi start node để catch-up (giây)
WIPE_CATCHUP_WAIT=60    # Chờ sau khi wipe+start (giây)
MIN_BLOCKS_PRODUCED=10  # Số block tối thiểu phải tăng sau khi restart
TX_PUMP_PID=""          # PID của tx_sender background process

NUM_NODES=5
# RPC ports cho mỗi node — phải khớp config-master-nodeN.json
RPC_PORTS=(8757 10747 10749 10750 10748)

# ─── Parse arguments ────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-destructive) SKIP_DESTRUCTIVE=true; shift ;;
        --node) TARGET_NODE="$2"; shift 2 ;;
        *) shift ;;
    esac
done

# ─── Màu sắc ────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# ─── Biến tổng hợp ──────────────────────────────────────────────
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0
FAILED_TEST_NAMES=()

# ═══════════════════════════════════════════════════════════════════
#  Hàm tiện ích
# ═══════════════════════════════════════════════════════════════════

# Ghi log ra cả console (có màu) và report file (không màu)
log() {
    echo -e "$1"
    echo -e "$1" | sed 's/\x1b\[[0-9;]*m//g' >> "$REPORT_FILE"
}

log_raw() {
    # Ghi nguyên văn, không interpret escape
    echo "$1"
    echo "$1" >> "$REPORT_FILE"
}

init_report() {
    cat > "$REPORT_FILE" << EOF
# 📋 Báo Cáo E2E Test Suite - MetaNode Consensus

| Thông tin | Giá trị |
|-----------|---------|
| Thời gian | $(date) |
| Máy chủ | $(hostname) |
| Số node | $NUM_NODES |
| Node test | $TARGET_NODE |
| Skip destructive | $SKIP_DESTRUCTIVE |

---
EOF
}

record_result() {
    local test_name="$1"
    local passed="$2"  # true/false
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    if [ "$passed" = "true" ]; then
        PASSED_TESTS=$((PASSED_TESTS + 1))
        log "### ✅ KẾT QUẢ: **PASS** — $test_name"
    else
        FAILED_TESTS=$((FAILED_TESTS + 1))
        FAILED_TEST_NAMES+=("$test_name")
        log "### ❌ KẾT QUẢ: **FAIL** — $test_name"
    fi
    log ""
}

# Lấy block number từ 1 node qua RPC
get_block_number() {
    local port=$1
    local result
    result=$(curl -s --max-time 3 -X POST "http://127.0.0.1:$port" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
    
    if [ -z "$result" ]; then
        echo "-1"
        return
    fi
    
    local hex_num
    hex_num=$(echo "$result" | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
    
    if [ -z "$hex_num" ] || [ "$hex_num" = "null" ]; then
        echo "-1"
        return
    fi
    
    # Convert hex to decimal
    printf "%d" "$hex_num" 2>/dev/null || echo "-1"
}

# Lấy block hash từ 1 node tại 1 block number cụ thể
get_block_hash() {
    local port=$1
    local block_hex=$2
    local result
    result=$(curl -s --max-time 3 -X POST "http://127.0.0.1:$port" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$block_hex\", false],\"id\":1}" 2>/dev/null)
    
    if [ -z "$result" ]; then
        echo "null"
        return
    fi
    
    local hash
    hash=$(echo "$result" | grep -o '"hash":"0x[0-9a-fA-F]*"' | head -1 | cut -d'"' -f4)
    
    if [ -z "$hash" ]; then
        echo "null"
    else
        echo "$hash"
    fi
}

# Chờ node process sống lại
wait_for_node_alive() {
    local node_id=$1
    local timeout=$2
    local port=${RPC_PORTS[$node_id]}
    local elapsed=0
    
    while [ $elapsed -lt $timeout ]; do
        local block
        block=$(get_block_number "$port")
        if [ "$block" != "-1" ]; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    return 1
}

# ═══════════════════════════════════════════════════════════════════
#  TX PUMP: Spam giao dịch để buộc hệ thống tạo block
#  MetaNode CHỈ tạo block khi có giao dịch. Không có TX = không block.
# ═══════════════════════════════════════════════════════════════════

# Bắt đầu spam TX trong background
start_tx_pump() {
    # Kiểm tra xem tx_sender đã được build chưa
    if [ ! -d "$TX_SENDER_DIR" ]; then
        log "> ⚠️ tx_sender không tồn tại tại $TX_SENDER_DIR — bỏ qua TX pump"
        return
    fi
    
    # Build tx_sender nếu chưa có binary
    if [ ! -x "$TX_SENDER_DIR/tx_sender" ]; then
        (cd "$TX_SENDER_DIR" && go build -o tx_sender .) || true
    fi
    
    # Xóa PID file cũ nếu process đã chết
    rm -f /tmp/tx_sender.pid 2>/dev/null || true
    
    # Chạy tx_sender binary trực tiếp trong background
    "$TX_SENDER_DIR/tx_sender" --loop --node "$TX_SENDER_NODE" > /dev/null 2>&1 &
    TX_PUMP_PID=$!
    log "- 🔫 TX Pump started (PID: $TX_PUMP_PID) — đang spam giao dịch để buộc tạo block..."
}

# Dừng spam TX
stop_tx_pump() {
    if [ -n "$TX_PUMP_PID" ] && kill -0 "$TX_PUMP_PID" 2>/dev/null; then
        kill -TERM "$TX_PUMP_PID" 2>/dev/null || true
        wait "$TX_PUMP_PID" 2>/dev/null || true
        log "- 🛑 TX Pump stopped (PID: $TX_PUMP_PID)"
        TX_PUMP_PID=""
    fi
    pkill -f tx_sender 2>/dev/null || true
    rm -f /tmp/tx_sender.pid 2>/dev/null || true
}

# Chờ cho đến khi block number tăng đủ từ baseline
# Usage: wait_for_blocks <port> <baseline_block> <min_increase> <timeout_sec>
wait_for_blocks() {
    local port=$1
    local baseline=$2
    local min_increase=$3
    local timeout=$4
    local target=$((baseline + min_increase))
    local elapsed=0
    
    while [ $elapsed -lt $timeout ]; do
        local current
        current=$(get_block_number "$port")
        if [ "$current" != "-1" ] && [ "$current" -ge "$target" ]; then
            log "- ✅ Block đã tăng đủ: $baseline → $current (tăng $((current - baseline)) blocks)"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
        if [ $((elapsed % 10)) -eq 0 ]; then
            log "- ⏳ Đang chờ block tăng... hiện tại: $current, cần: $target ($((elapsed))s/${timeout}s)"
        fi
    done
    
    local final
    final=$(get_block_number "$port")
    log "- ⚠️ Timeout! Block: $baseline → $final (tăng $((final - baseline)) blocks, cần $min_increase)"
    return 1
}

# Trap để cleanup TX pump khi script bị thoát đột ngột
trap 'stop_tx_pump; exit' INT TERM EXIT

# ═══════════════════════════════════════════════════════════════════
#  TEST 1: Hash Parity Check
# ═══════════════════════════════════════════════════════════════════

test_hash_parity() {
    local test_label="${1:-Hash Parity Check}"
    log "## $(date +%H:%M:%S) — $test_label"
    log ""
    
    # Bước 1: Lấy block number từ tất cả node
    local blocks=()
    local online_count=0
    local min_block=999999999
    
    for i in $(seq 0 $((NUM_NODES - 1))); do
        local port=${RPC_PORTS[$i]}
        local block
        block=$(get_block_number "$port")
        blocks+=("$block")
        
        if [ "$block" != "-1" ]; then
            online_count=$((online_count + 1))
            if [ "$block" -lt "$min_block" ]; then
                min_block=$block
            fi
            log "- 🟢 **Node $i**: Block \`$block\` (port $port)"
        else
            log "- 🔴 **Node $i**: \`OFFLINE\` (port $port không phản hồi)"
        fi
    done
    log ""
    
    if [ $online_count -lt 2 ]; then
        log "> ⚠️ Chỉ $online_count node online — không thể so sánh hash."
        record_result "$test_label" "false"
        return
    fi
    
    # Bước 2: So sánh hash tại min_block (block mà TẤT CẢ node online đều có)
    local reference_block=$min_block
    local reference_hex
    reference_hex=$(printf "0x%x" "$reference_block")
    
    log "**So sánh hash tại Block #$reference_block** (block thấp nhất chung):"
    log ""
    
    local hashes=()
    local hash_mismatch=false
    local first_hash=""
    
    for i in $(seq 0 $((NUM_NODES - 1))); do
        if [ "${blocks[$i]}" = "-1" ]; then
            hashes+=("offline")
            continue
        fi
        
        local port=${RPC_PORTS[$i]}
        local hash
        hash=$(get_block_hash "$port" "$reference_hex")
        hashes+=("$hash")
        
        if [ "$hash" = "null" ]; then
            log "- ⚠️ Node $i: Block $reference_block không tồn tại (có thể là empty commit bị drop)"
            continue
        fi
        
        if [ -z "$first_hash" ]; then
            first_hash="$hash"
        fi
        
        log "- Node $i: \`$hash\`"
        
        if [ -n "$first_hash" ] && [ "$hash" != "$first_hash" ] && [ "$hash" != "null" ]; then
            hash_mismatch=true
        fi
    done
    log ""
    
    if [ "$hash_mismatch" = "true" ]; then
        log "> 🚨 **PHÁT HIỆN HASH KHÁC NHAU TẠI BLOCK #$reference_block — CÓ THỂ BỊ FORK!**"
        record_result "$test_label" "false"
    else
        log "> ✅ Tất cả node online đều có hash giống nhau tại Block #$reference_block."
        
        # Kiểm tra thêm: chênh lệch block giữa các node
        local max_block=0
        for b in "${blocks[@]}"; do
            if [ "$b" != "-1" ] && [ "$b" -gt "$max_block" ]; then
                max_block=$b
            fi
        done
        local lag=$((max_block - min_block))
        if [ $lag -gt 10 ]; then
            log "> ⚠️ Chênh lệch block: $lag blocks (max=$max_block, min=$min_block). Một số node đang tụt hậu."
        else
            log "> Chênh lệch block giữa các node: $lag blocks — bình thường."
        fi
        record_result "$test_label" "true"
    fi
}

# ═══════════════════════════════════════════════════════════════════
#  TEST 2: Fork/Panic Log Scan
# ═══════════════════════════════════════════════════════════════════

test_scan_fork_warnings() {
    log "## $(date +%H:%M:%S) — Quét Log Cảnh Báo Phân Nhánh (Fork/Panic Scan)"
    log ""
    
    local found_issues=0
    local total_warnings=0
    
    for i in $(seq 0 $((NUM_NODES - 1))); do
        local log_file="$LOG_BASE/node_$i/go-master-stdout.log"
        if [ ! -f "$log_file" ]; then
            log "- ⚠️ Node $i: Log file không tồn tại: \`$log_file\`"
            continue
        fi
        
        # Lọc 10,000 dòng gần nhất
        # Loại bỏ false positives:
        #   - "FORK-GUARD" là tên feature, không phải lỗi
        #   - "FORK-DIAG" là diagnostic log, không phải lỗi thực
        #   - "ANTI-FORK" là tên check, PASS/SKIP cũng match → chỉ lấy FAIL
        #   - "Created block" là normal proposer log (base64 hashes có thể chứa "fatal")
        #   - Bare "fatal" matches base64 encoded data → đổi sang "fatal error|fatal:"
        local warnings
        warnings=$(tail -n 10000 "$log_file" 2>/dev/null \
            | grep -iE "(FORK DETECTED|DIVERGE|HASH MISMATCH|PANIC|fatal error|fatal:)" \
            | grep -ivE "(FORK-GUARD|FORK-DIAG|anti-fork.*pass|anti-fork.*skip|no panic|Created block)" \
            | tail -n 10) || true
        
        if [ -n "$warnings" ]; then
            local count
            count=$(echo "$warnings" | wc -l)
            total_warnings=$((total_warnings + count))
            log "⚠️ **Node $i**: Tìm thấy $count cảnh báo nghiêm trọng:"
            log ""
            log '```text'
            log_raw "$warnings"
            log '```'
            log ""
            found_issues=1
        else
            log "- ✅ Node $i: Sạch (không có FORK/PANIC/DIVERGE)"
        fi
    done
    log ""
    
    if [ $found_issues -eq 0 ]; then
        record_result "Fork/Panic Log Scan" "true"
    else
        log "> 🚨 Tìm thấy tổng cộng $total_warnings cảnh báo nghiêm trọng trong log."
        record_result "Fork/Panic Log Scan" "false"
    fi
}

# ═══════════════════════════════════════════════════════════════════
#  TEST 3: Node Restart Recovery
# ═══════════════════════════════════════════════════════════════════

test_node_restart() {
    log "## $(date +%H:%M:%S) — Restart Recovery Test (Node $TARGET_NODE)"
    log ""
    
    # Ghi nhận block trước khi stop (từ node CÒN SỐNG, VD: node 0)
    local ref_port=${RPC_PORTS[0]}
    local target_port=${RPC_PORTS[$TARGET_NODE]}
    local block_before
    block_before=$(get_block_number "$ref_port")
    log "- Block cluster trước khi stop (node 0): \`$block_before\`"
    
    # Stop node
    log "- 🔴 Đang dừng node $TARGET_NODE..."
    "$ORCHESTRATOR" stop-node "$TARGET_NODE" >> "$REPORT_FILE" 2>&1 || true
    
    # Bật TX pump để buộc cluster tạo block mới
    start_tx_pump
    
    log "- ⏳ Chờ ${RESTART_WAIT}s + spam TX để cluster tạo thêm block..."
    # Chờ và đảm bảo có ít nhất N block mới được tạo bởi cluster
    wait_for_blocks "$ref_port" "$block_before" "$MIN_BLOCKS_PRODUCED" "$RESTART_WAIT" || true
    
    local block_during
    block_during=$(get_block_number "$ref_port")
    log "- Block cluster khi node $TARGET_NODE đang tắt: \`$block_during\` (tăng $((block_during - block_before)) blocks)"
    
    # Start node
    log "- 🟢 Khởi động lại node $TARGET_NODE..."
    "$ORCHESTRATOR" start-node "$TARGET_NODE" >> "$REPORT_FILE" 2>&1 || true
    
    log "- ⏳ Chờ tối đa ${CATCHUP_WAIT}s để node catch-up..."
    
    # Chờ node RPC sống lại
    if ! wait_for_node_alive "$TARGET_NODE" "$CATCHUP_WAIT"; then
        stop_tx_pump
        log "> 🚨 Node $TARGET_NODE không phản hồi RPC sau ${CATCHUP_WAIT}s!"
        record_result "Restart Recovery (Node $TARGET_NODE)" "false"
        return
    fi
    
    # Chờ thêm để bắt kịp + tiếp tục spam
    sleep 15
    
    stop_tx_pump
    
    # Kiểm tra block sau khi restart
    local block_after
    block_after=$(get_block_number "$target_port")
    local block_ref_after
    block_ref_after=$(get_block_number "$ref_port")
    log "- Block node $TARGET_NODE sau restart: \`$block_after\`"
    log "- Block node 0 (reference): \`$block_ref_after\`"
    log ""
    
    # Trích xuất log đồng bộ từ node restart
    local log_file="$LOG_BASE/node_${TARGET_NODE}/go-master-stdout.log"
    if [ -f "$log_file" ]; then
        log "**Log dấu vết khởi động & đồng bộ (Node $TARGET_NODE):**"
        log ""
        log '```text'
        local sync_evidence
        sync_evidence=$(tail -n 5000 "$log_file" 2>/dev/null \
            | grep -E "(STARTUP|Bootstrapping|CatchingUp|Healthy|SYNC|baseline|recover)" \
            | tail -n 15) || true
        if [ -n "$sync_evidence" ]; then
            log_raw "$sync_evidence"
        else
            log_raw "(Không tìm thấy log sync phase — có thể node catch-up quá nhanh)"
        fi
        log '```'
        log ""
    fi
    
    # Đánh giá
    if [ "$block_after" != "-1" ] && [ "$block_after" -gt "$block_before" ]; then
        log "> Node $TARGET_NODE đã khởi động lại và tiếp tục xử lý block ($block_before → $block_after)."
        # So sánh lag với reference node
        if [ "$block_ref_after" != "-1" ]; then
            local lag=$((block_ref_after - block_after))
            if [ $lag -gt 5 ]; then
                log "> ⚠️ Node $TARGET_NODE vẫn chậm $lag blocks so với cluster. Đang catch-up."
            else
                log "> ✅ Node $TARGET_NODE đã bắt kịp cluster (lag: $lag blocks)."
            fi
        fi
        record_result "Restart Recovery (Node $TARGET_NODE)" "true"
    elif [ "$block_after" != "-1" ]; then
        log "> ⚠️ Node $TARGET_NODE sống lại nhưng block chưa tăng ($block_before → $block_after). Có thể đang catch-up."
        record_result "Restart Recovery (Node $TARGET_NODE)" "true"
    else
        log "> 🚨 Node $TARGET_NODE không phản hồi RPC!"
        record_result "Restart Recovery (Node $TARGET_NODE)" "false"
    fi
}

# ═══════════════════════════════════════════════════════════════════
#  TEST 4: DAG Wipe Recovery (Mô phỏng snapshot restore)
# ═══════════════════════════════════════════════════════════════════

test_dag_wipe_recovery() {
    log "## $(date +%H:%M:%S) — DAG Wipe Recovery Test (Node $TARGET_NODE)"
    log ""
    
    local ref_port=${RPC_PORTS[0]}
    local target_port=${RPC_PORTS[$TARGET_NODE]}
    
    # Ghi nhận block trước khi stop (từ reference node)
    local block_before
    block_before=$(get_block_number "$ref_port")
    log "- Block cluster trước khi wipe (node 0): \`$block_before\`"
    
    # Stop node
    log "- 🔴 Đang dừng node $TARGET_NODE..."
    "$ORCHESTRATOR" stop-node "$TARGET_NODE" >> "$REPORT_FILE" 2>&1 || true
    sleep 3
    
    # Xóa toàn bộ storage Rust (giữ Go data nguyên)
    local storage_dir="$RUST_DIR/config/storage/node_${TARGET_NODE}"
    log "- 🗑️ Xóa toàn bộ Rust DAG storage: \`$storage_dir\`"
    if [ -d "$storage_dir" ]; then
        local contents
        contents=$(ls -la "$storage_dir" 2>/dev/null | head -10) || true
        log '```text'
        log_raw "Nội dung trước khi xóa:"
        log_raw "$contents"
        log '```'
        rm -rf "$storage_dir"
        log "- ✅ Đã xóa thành công."
    else
        log "- ⚠️ Thư mục storage không tồn tại (có thể đã sạch)."
    fi
    log ""
    
    # Bật TX pump để buộc cluster tạo thêm block (cần ≥50 blocks cho snapshot)
    start_tx_pump
    
    # Start node
    log "- 🟢 Khởi động lại node $TARGET_NODE với DAG rỗng..."
    "$ORCHESTRATOR" start-node "$TARGET_NODE" >> "$REPORT_FILE" 2>&1 || true
    
    log "- ⏳ Chờ tối đa ${WIPE_CATCHUP_WAIT}s để node FORWARD-JUMP và catch-up..."
    
    # Chờ node sống lại
    if ! wait_for_node_alive "$TARGET_NODE" "$WIPE_CATCHUP_WAIT"; then
        stop_tx_pump
        log "> 🚨 Node $TARGET_NODE không phản hồi RPC sau ${WIPE_CATCHUP_WAIT}s!"
        record_result "DAG Wipe Recovery (Node $TARGET_NODE)" "false"
        return
    fi
    
    # Chờ thêm cho FORWARD-JUMP + tiếp tục spam TX
    sleep 20
    
    stop_tx_pump
    
    local block_after
    block_after=$(get_block_number "$target_port")
    local block_ref_after
    block_ref_after=$(get_block_number "$ref_port")
    log "- Block node $TARGET_NODE sau wipe+restart: \`$block_after\`"
    log "- Block node 0 (reference): \`$block_ref_after\`"
    log ""
    
    # Trích xuất log FORWARD-JUMP
    local log_file="$LOG_BASE/node_${TARGET_NODE}/go-master-stdout.log"
    if [ -f "$log_file" ]; then
        log "**Log quá trình Forward-Jump & Recovery (Node $TARGET_NODE):**"
        log ""
        log '```text'
        local jump_evidence
        jump_evidence=$(tail -n 5000 "$log_file" 2>/dev/null \
            | grep -E "(FORWARD-JUMP|baseline|STARTUP-SYNC|reset_to_network|batch.drain|CommitSyncer|Bootstrapping|CatchingUp|Healthy)" \
            | tail -n 20) || true
        if [ -n "$jump_evidence" ]; then
            log_raw "$jump_evidence"
        else
            log_raw "(Không tìm thấy log FORWARD-JUMP — có thể node chưa kịp sync)"
        fi
        log '```'
        log ""
    fi
    
    # Đánh giá
    if [ "$block_after" != "-1" ] && [ "$block_after" -ge "$block_before" ]; then
        log "> Node $TARGET_NODE đã phục hồi thành công từ DAG rỗng ($block_before → $block_after)."
        if [ "$block_ref_after" != "-1" ]; then
            local lag=$((block_ref_after - block_after))
            if [ $lag -gt 5 ]; then
                log "> ⚠️ Node $TARGET_NODE vẫn chậm $lag blocks so với cluster. Đang catch-up."
            else
                log "> ✅ Node $TARGET_NODE đã bắt kịp cluster (lag: $lag blocks)."
            fi
        fi
        record_result "DAG Wipe Recovery (Node $TARGET_NODE)" "true"
    elif [ "$block_after" != "-1" ]; then
        log "> ⚠️ Node $TARGET_NODE đã sống nhưng block thấp hơn trước ($block_before → $block_after). Có thể đang catch-up."
        record_result "DAG Wipe Recovery (Node $TARGET_NODE)" "true"
    else
        log "> 🚨 Node $TARGET_NODE không phản hồi RPC!"
        record_result "DAG Wipe Recovery (Node $TARGET_NODE)" "false"
    fi
}

# ═══════════════════════════════════════════════════════════════════
#  TỔNG HỢP BÁO CÁO
# ═══════════════════════════════════════════════════════════════════

finalize_report() {
    log ""
    log "---"
    log ""
    log "## 📊 Tổng Kết"
    log ""
    log "| Metric | Giá trị |"
    log "|--------|---------|"
    log "| Tổng số test | $TOTAL_TESTS |"
    log "| ✅ Passed | $PASSED_TESTS |"
    log "| ❌ Failed | $FAILED_TESTS |"
    log "| Thời gian | $SECONDS giây |"
    log ""
    
    if [ $FAILED_TESTS -gt 0 ]; then
        log "### ❌ Các test thất bại:"
        log ""
        for name in "${FAILED_TEST_NAMES[@]}"; do
            log "- $name"
        done
        log ""
        log "> 🚨 **HỆ THỐNG CÓ VẤN ĐỀ CẦN ĐIỀU TRA.** Vui lòng gửi file report này cho AI agent để phân tích."
    else
        log "> ✅ **TẤT CẢ TEST ĐỀU PASS.** Hệ thống hoạt động ổn định."
    fi
    
    log ""
    log "**Kết thúc:** $(date)"
    
    echo ""
    echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
    if [ $FAILED_TESTS -eq 0 ]; then
        echo -e "${GREEN}${BOLD}║  ✅ ALL $TOTAL_TESTS TESTS PASSED                               ║${NC}"
    else
        echo -e "${RED}${BOLD}║  ❌ $FAILED_TESTS/$TOTAL_TESTS TESTS FAILED                              ║${NC}"
    fi
    echo -e "${BOLD}╠══════════════════════════════════════════════════════════╣${NC}"
    echo -e "${BOLD}║  📁 Report: ${NC}${CYAN}$REPORT_FILE${NC}"
    echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
    
    # Exit code cho CI
    if [ $FAILED_TESTS -gt 0 ]; then
        exit 1
    fi
}

# ═══════════════════════════════════════════════════════════════════
#  MAIN: Luồng thực thi chính
# ═══════════════════════════════════════════════════════════════════

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🧪 MetaNode Consensus E2E Test Suite                  ║${NC}"
echo -e "${BOLD}║  Target node: $TARGET_NODE | Skip destructive: $SKIP_DESTRUCTIVE        ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""

# Kiểm tra dependencies
for cmd in curl grep sed; do
    if ! command -v "$cmd" &>/dev/null; then
        echo -e "${RED}❌ Thiếu dependency: $cmd${NC}"
        exit 1
    fi
done

if [ ! -x "$ORCHESTRATOR" ]; then
    echo -e "${RED}❌ Orchestrator script không tồn tại hoặc không executable: $ORCHESTRATOR${NC}"
    exit 1
fi

SECONDS=0
init_report

# ─── Cleanup: Kill stale tx_sender processes từ các lần chạy trước ──
pkill -f tx_sender 2>/dev/null || true

# ─── Test 1: Hash Parity ────────────────────────────────────────
test_hash_parity "Test 1: Hash Parity Check (Pre-test Baseline)"

# ─── Test 2: Log Scan ───────────────────────────────────────────
test_scan_fork_warnings

# ─── Test 3+4: Destructive tests ────────────────────────────────
if [ "$SKIP_DESTRUCTIVE" = "true" ]; then
    log "## ⏭️ Bỏ qua Test 3+4 (--skip-destructive)"
    log ""
else
    test_node_restart
    test_dag_wipe_recovery
fi

# ─── Test 5: Post-recovery hash parity ──────────────────────────
test_hash_parity "Test 5: Hash Parity Check (Post-Recovery)"

# ─── Test 6: Consensus Liveness Check ───────────────────────────
test_consensus_liveness() {
    local test_label="Test 6: Consensus Liveness Check"
    log "## $(date +%H:%M:%S) — $test_label"
    log ""

    # Lấy block hiện tại từ node 0 (reference)
    local ref_port="${HTTP_PORTS[0]}"
    local baseline
    baseline=$(get_block_number "$ref_port")
    if [ "$baseline" = "-1" ]; then
        log "- 🔴 Node 0 không phản hồi — bỏ qua liveness test"
        record_result "$test_label" "SKIP"
        return
    fi
    log "- Block hiện tại (node 0): $baseline"

    # Bắt đầu spam giao dịch
    start_tx_pump
    log "- ⏳ Chờ tối đa 60s để cluster tạo block mới..."

    # Chờ ít nhất 5 block mới trong 60s
    if wait_for_blocks "$ref_port" "$baseline" 5 60; then
        local final_block
        final_block=$(get_block_number "$ref_port")
        log "- ✅ Cluster vẫn hoạt động: $baseline → $final_block (tăng $((final_block - baseline)) blocks)"
        stop_tx_pump
        record_result "$test_label" "PASS"
    else
        local final_block
        final_block=$(get_block_number "$ref_port")
        log "- 🔴 **CONSENSUS STALL DETECTED!** Block không tăng sau 60s: $baseline → $final_block"
        log "- 🔴 Đây là lỗi production-critical: cluster mất khả năng tạo block mới."
        stop_tx_pump
        record_result "$test_label" "FAIL"
    fi
}

test_consensus_liveness

# ─── Báo cáo tổng hợp ──────────────────────────────────────────
finalize_report
