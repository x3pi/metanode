#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  E2E Test Suite for MetaNode Consensus
#  Tự động kiểm tra: 
#    1. Hash Parity Check
#    2. Quét log tìm lỗi (FORK/PANIC)
#    3. Restart Recovery Test (Stop -> Start)
#    4. Snapshot Restore Test (Delete DAG -> Start)
# ═══════════════════════════════════════════════════════════════════

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
LOG_BASE="$BASE_DIR/consensus/metanode/logs"

# Lấy timestamp cho report
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
REPORT_FILE="$SCRIPT_DIR/debug_report_${TIMESTAMP}.md"

NUM_NODES=5
RPC_PORTS=(8757 10747 10749 10750 10748)

log() {
    echo -e "$1"
    # Lọc bỏ mã màu (ANSI color codes) trước khi ghi vào markdown
    echo -e "$1" | sed 's/\x1b\[[0-9;]*m//g' >> "$REPORT_FILE"
}

init_report() {
    echo "# Báo Cáo E2E Test Suite - MetaNode Consensus" > "$REPORT_FILE"
    echo "**Thời gian:** $(date)" >> "$REPORT_FILE"
    echo "---" >> "$REPORT_FILE"
}

check_hash_parity() {
    log "\n## $(date +%H:%M:%S) - 1. Kiểm tra tính nhất quán (Hash Parity)"
    log "Gửi RPC request đến các node để đối chiếu block hiện tại..."
    
    local target_blocks=()
    local target_hashes=()
    local all_nodes_online=true

    for i in $(seq 0 $((NUM_NODES - 1))); do
        local port=${RPC_PORTS[$i]}
        
        # Get latest block number
        local hex_num=$(curl -s --max-time 2 -X POST http://127.0.0.1:$port \
                 -H "Content-Type: application/json" \
                 -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq -r '.result' 2>/dev/null || echo "null")
        
        if [ "$hex_num" == "null" ] || [ -z "$hex_num" ]; then
            log "- 🔴 **Node $i**: \`OFFLINE\` (Không kết nối được cổng RPC $port)"
            target_blocks+=(-1)
            target_hashes+=("null")
            all_nodes_online=false
            continue
        fi
        
        # Parse logic if hex is weird
        local dec_num=$((16#${hex_num#0x}))
        target_blocks+=($dec_num)
        
        # Get block hash via eth_getBlockByNumber
        local block_info=$(curl -s --max-time 2 -X POST http://127.0.0.1:$port \
                 -H "Content-Type: application/json" \
                 -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["'"$hex_num"'", false],"id":1}' 2>/dev/null)
        
        local hash=$(echo "$block_info" | grep -o '"hash":"[^"]*"' | cut -d'"' -f4 | head -1)
        if [ -z "$hash" ]; then hash="null"; fi
        target_hashes+=("$hash")
        
        log "- 🟢 **Node $i**: Block \`$dec_num\` - Hash: \`$hash\`"
    done
    
    log "\n*Ghi chú: Nếu hệ thống đang tải block liên tục, sự chênh lệch (lag) từ 1-5 blocks là bình thường, nhưng nếu số Block bằng nhau thì Hash BẮT BUỘC phải giống nhau.*"
}

scan_fork_warnings() {
    log "\n## $(date +%H:%M:%S) - 2. Quét Log Cảnh Báo Phân Nhánh (Fork/Panic Scan)"
    local found_issues=0
    for i in $(seq 0 $((NUM_NODES - 1))); do
        local log_file="$LOG_BASE/node_$i/go-master-stdout.log"
        if [ -f "$log_file" ]; then
            # Kiểm tra lỗi ở 10,000 dòng log gần nhất
            local warnings=$(tail -n 10000 "$log_file" | grep -iE "FORK|DIVERGE|MISMATCH|PANIC" | tail -n 10)
            if [ -n "$warnings" ]; then
                log "⚠️ **Node $i**: Tìm thấy Warning/Panic trong log:"
                log "\`\`\`text\n$warnings\n\`\`\`"
                found_issues=1
            fi
        fi
    done
    
    if [ $found_issues -eq 0 ]; then
        log "- ✅ **PASS**: KHÔNG tìm thấy bất kỳ cảnh báo Fork / Diverge / Panic nào."
    fi
}

test_node_restart() {
    log "\n## $(date +%H:%M:%S) - 3. Thử Thách Khởi Động Lại & Tự Phục Hồi (Restart Recovery)"
    log "- Dừng node 1 (mô phỏng rớt mạng)..."
    "$SCRIPT_DIR/mtn-orchestrator.sh" stop-node 1 >> /dev/null 2>&1
    
    log "- ⏳ Chờ 10s để cluster tiếp tục sinh block thiếu vắng Node 1..."
    sleep 10
    
    log "- Khởi động lại node 1..."
    "$SCRIPT_DIR/mtn-orchestrator.sh" start-node 1 >> /dev/null 2>&1
    
    log "- ⏳ Chờ 15s để node bắt kịp mạng (catch-up)..."
    sleep 15
    
    local log_file="$LOG_BASE/node_1/go-master-stdout.log"
    local sync_logs=$(tail -n 3000 "$log_file" | grep -E "(STARTUP|BOOTSTRAP|CatchingUp|Healthy)" | tail -n 5)
    log "- **Log dấu vết đồng bộ Node 1:**"
    log "\`\`\`text\n$sync_logs\n\`\`\`"
}

test_snapshot_restore() {
    log "\n## $(date +%H:%M:%S) - 4. Mô Phỏng Phục Hồi Baseline (Xóa DAG + Phục Hồi)"
    log "- Dừng node 1..."
    "$SCRIPT_DIR/mtn-orchestrator.sh" stop-node 1 >> /dev/null 2>&1
    
    local storage_epochs="$BASE_DIR/consensus/metanode/config/storage/node_1/epochs"
    local storage_recent="$BASE_DIR/consensus/metanode/config/storage/node_1/recent"
    log "- Đang xóa dữ liệu DAG cục bộ (Xóa History, giữ lại Go Master Data)..."
    rm -rf "$storage_epochs" "$storage_recent" 2>/dev/null || true
    
    log "- Khởi động lại node 1 với DAG rỗng..."
    "$SCRIPT_DIR/mtn-orchestrator.sh" start-node 1 >> /dev/null 2>&1
    
    log "- ⏳ Chờ 20s để node xả FORWARD-JUMP và batch block drain..."
    sleep 20
    
    local log_file="$LOG_BASE/node_1/go-master-stdout.log"
    local jump_logs=$(tail -n 3000 "$log_file" | grep -E "(FORWARD-JUMP|baseline|Skipping)" | tail -n 5)
    log "- **Log quá trình nhảy bục (Forward-Jump) Node 1:**"
    log "\`\`\`text\n$jump_logs\n\`\`\`"
}

finalize_report() {
    log "\n---\n**🏁 Hoàn Tất Test Suite.** Thời gian kết thúc: $(date)"
    echo -e "\n📁 Báo cáo đã được lưu tại: \033[1;32m$REPORT_FILE\033[0m"
}

# --- Luồng thực thi chính ---
echo -e "\033[1;36m Bắt đầu chạy E2E Test Suite (Hãy đảm bảo jq và curl có sẵn hệ thống)\033[0m"
init_report
check_hash_parity
scan_fork_warnings
test_node_restart
test_snapshot_restore
log "\n## $(date +%H:%M:%S) - 5. Xác Minh Tính Nhất Quán Lần Cuối (Post-Recovery Hash Parity)"
check_hash_parity
finalize_report
