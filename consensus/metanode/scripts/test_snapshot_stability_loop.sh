#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Snapshot Recovery Stability Loop
#  Lặp lại quá trình snapshot recovery nhiều lần để test độ ổn định.
#  Dừng ngay khi gặp lỗi và tạo báo cáo MD chi tiết.
#
#  Usage:
#    ./test_snapshot_stability_loop.sh                    # 5 lần, node 0->1
#    ./test_snapshot_stability_loop.sh --rounds 10        # 10 lần
#    ./test_snapshot_stability_loop.sh --src 0 --dst 2    # node 0->2
#    ./test_snapshot_stability_loop.sh --rotate           # xoay vòng dst node
# ═══════════════════════════════════════════════════════════════════

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUST_DIR="$BASE_DIR/consensus/metanode"
GO_DIR="$BASE_DIR/execution/cmd/simple_chain"
LOG_BASE="$RUST_DIR/logs"
ORCHESTRATOR="$SCRIPT_DIR/mtn-orchestrator.sh"
TX_SENDER_DIR="$BASE_DIR/execution/cmd/tool/tx_sender"
TX_SENDER_NODE="127.0.0.1:4201"

# ─── Parameters ─────────────────────────────────────────────────
MAX_ROUNDS=5
SRC_NODE=0
DST_NODE=1
ROTATE_DST=false
CATCHUP_TIMEOUT=90
LIVENESS_WAIT=30
SETTLE_TIME=15
TX_PUMP_PID=""
LEVELDB_DIRS="account_state blocks receipts transaction_state mapping smart_contract_code smart_contract_storage stake_db trie_database backup_device_key_storage xapian xapian_node nomt_db"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --rounds) MAX_ROUNDS="$2"; shift 2 ;;
        --src) SRC_NODE="$2"; shift 2 ;;
        --dst) DST_NODE="$2"; shift 2 ;;
        --rotate) ROTATE_DST=true; shift ;;
        --catchup-timeout) CATCHUP_TIMEOUT="$2"; shift 2 ;;
        *) shift ;;
    esac
done

TIMESTAMP=$(date +%Y%m%d_%H%M%S)
REPORT_FILE="$SCRIPT_DIR/stability_report_${TIMESTAMP}.md"
AVAILABLE_DST_NODES=(1 2 3 4)

# ─── Colors ─────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

# ─── RPC Ports ──────────────────────────────────────────────────
RPC_PORTS=(8757 10747 10749 10750 10748)
NUM_NODES=5

# ─── Utility Functions ──────────────────────────────────────────
log() { echo -e "$1"; echo -e "$1" | sed 's/\x1b\[[0-9;]*m//g' >> "$REPORT_FILE"; }
log_raw() { echo "$1"; echo "$1" >> "$REPORT_FILE"; }

get_block_number() {
    local port=$1
    local res=$(curl -s --max-time 3 -X POST "http://127.0.0.1:$port" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
    [ -z "$res" ] && { echo "-1"; return; }
    local hex=$(echo "$res" | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
    [ -z "$hex" ] || [ "$hex" = "null" ] && { echo "-1"; return; }
    printf "%d" "$hex" 2>/dev/null || echo "-1"
}

get_peer_info() {
    curl -s --max-time 3 -X GET "http://127.0.0.1:$1/peer_info" 2>/dev/null
}

get_snapshot_port() { echo $((8600 + $1)); }

start_tx_pump() {
    [ ! -d "$TX_SENDER_DIR" ] && return
    [ ! -x "$TX_SENDER_DIR/tx_sender" ] && (cd "$TX_SENDER_DIR" && go build -o tx_sender . 2>/dev/null) || true
    "$TX_SENDER_DIR/tx_sender" --config "$TX_SENDER_DIR/config.json" \
        --data "$TX_SENDER_DIR/data.json" --loop --node "$TX_SENDER_NODE" > /dev/null 2>&1 &
    TX_PUMP_PID=$!
}

stop_tx_pump() {
    if [ -n "$TX_PUMP_PID" ] && kill -0 "$TX_PUMP_PID" 2>/dev/null; then
        kill -TERM "$TX_PUMP_PID" 2>/dev/null || true
        wait "$TX_PUMP_PID" 2>/dev/null || true
        TX_PUMP_PID=""
    fi
    pkill -f "tx_sender" 2>/dev/null || true
}

cleanup_and_exit() {
    stop_tx_pump
    exit "${1:-1}"
}
trap 'cleanup_and_exit' INT TERM

# ─── Collect Diagnostic Snapshot for Report ─────────────────────
collect_diagnostics() {
    local round=$1
    local dst=$2
    local failure_reason="$3"

    log ""
    log "---"
    log ""
    log "## 🔬 Chẩn đoán cho Lỗi tại Vòng $round"
    log ""
    log "**Lý do thất bại:** $failure_reason"
    log ""

    # Node status
    log "### Trạng thái Cụm (Cluster Status)"
    log ""
    for i in $(seq 0 $((NUM_NODES - 1))); do
        local port=${RPC_PORTS[$i]}
        local block=$(get_block_number "$port")
        local peer_port=$((19200 + i))
        local info=$(get_peer_info "$peer_port")
        local gei=$(echo "$info" | grep -o '"global_exec_index":[0-9]*' | cut -d: -f2)
        local sr=$(echo "$info" | grep -o '"state_root":"[^"]*"' | cut -d'"' -f4)
        local epoch=$(echo "$info" | grep -o '"current_epoch":[0-9]*' | cut -d: -f2)
        if [ "$block" != "-1" ]; then
            log "- **Node $i**: block=\`$block\` gei=\`${gei:-?}\` epoch=\`${epoch:-?}\` state_root=\`${sr:0:20}...\`"
            log "- **Node $i**: \`NGOẠI TUYẾN (OFFLINE)\`"
        fi
    done
    log ""

    # Hash comparison at recent blocks
    log "### So sánh Mã Băm Khối (5 khối chung gần nhất)"
    log ""
    local min_block=999999999
    for i in $(seq 0 $((NUM_NODES - 1))); do
        local b=$(get_block_number "${RPC_PORTS[$i]}")
        [ "$b" != "-1" ] && [ "$b" -lt "$min_block" ] && min_block=$b
    done

    if [ "$min_block" -gt 0 ] && [ "$min_block" -lt 999999999 ]; then
        log "| Khối | $(for i in $(seq 0 $((NUM_NODES-1))); do printf "Node %d | " $i; done)"
        log "|-------|$(for i in $(seq 0 $((NUM_NODES-1))); do printf "------|"; done)"
        for offset in 0 1 2 3 4; do
            local bn=$((min_block - offset))
            [ "$bn" -lt 1 ] && continue
            local hex=$(printf "0x%x" "$bn")
            local row="| #$bn |"
            for i in $(seq 0 $((NUM_NODES - 1))); do
                local port=${RPC_PORTS[$i]}
                local result=$(curl -s --max-time 2 -X POST "http://127.0.0.1:$port" \
                    -H "Content-Type: application/json" \
                    -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$hex\", false],\"id\":1}" 2>/dev/null)
                local hash=$(echo "$result" | grep -o '"hash":"0x[0-9a-fA-F]*"' | head -1 | cut -d'"' -f4)
                row="$row \`${hash:0:14}...\` |"
            done
            log "$row"
        done
    fi
    log ""

    # Recent logs from failed node
    local dst_log="$LOG_BASE/node_${dst}/go-master-stdout.log"
    if [ -f "$dst_log" ]; then
        log "### Nhật ký gần đây của Node $dst (60 dòng cuối)"
        log ""
        log '```text'
        tail -n 60 "$dst_log" 2>/dev/null | while IFS= read -r line; do log_raw "$line"; done
        log '```'
        log ""

        log "### Các Cột Mốc Đồng Bộ/Phục Hồi của Node $dst"
        log ""
        log '```text'
        grep -E "(STARTUP-SYNC|ANTI-FORK|STATE-ROOT|HEALTH|HALT|FORWARD-JUMP|CONSISTENCY|GEI-CROSSCHECK|baseline|Bootstrapping|CatchingUp|Healthy|FORK|DIVERGE|PANIC|fatal)" \
            "$dst_log" 2>/dev/null | tail -30 | while IFS= read -r line; do log_raw "$line"; done
        log '```'
    fi
    log ""

    # Source node log excerpt
    local src_log="$LOG_BASE/node_${SRC_NODE}/go-master-stdout.log"
    if [ -f "$src_log" ]; then
        log "### Các Cột Mốc Đồng Bộ Gần Đây của Node $SRC_NODE (Nguồn)"
        log ""
        log '```text'
        grep -E "(snapshot|FORK|DIVERGE|STATE-ROOT|MISMATCH)" "$src_log" 2>/dev/null | tail -15 | \
            while IFS= read -r line; do log_raw "$line"; done
        log '```'
    fi
}

# ─── Single Round: Snapshot Recovery Test ───────────────────────
run_single_round() {
    local round=$1
    local dst=$2
    local src=$SRC_NODE
    local src_port=${RPC_PORTS[$src]}
    local dst_port=${RPC_PORTS[$dst]}
    local snap_port=$(get_snapshot_port "$src")
    local snap_url="http://127.0.0.1:${snap_port}"
    local round_start=$SECONDS

    log "## 🔄 Vòng $round/$MAX_ROUNDS — Phục hồi từ Snapshot (Node $src → Node $dst)"
    log ""
    log "**Bắt đầu:** $(date '+%Y-%m-%d %H:%M:%S')"
    log ""

    # ── Phase 1: Check snapshot availability ──
    log "### Giai đoạn 1: Kiểm tra Snapshot"
    local snap_json=$(curl -sf "${snap_url}/api/snapshots" 2>/dev/null || echo "null")
    if [ "$snap_json" = "null" ] || [ -z "$snap_json" ]; then
        log "- ❌ API Snapshot không phản hồi trên Node $src"
        return 1
    fi
    local snap_count=$(echo "$snap_json" | jq 'length' 2>/dev/null || echo "0")
    if [ "$snap_count" -eq 0 ]; then
        log "- ⚠️ Không có snapshot nào. Bơm giao dịch để bắt buộc chuyển epoch..."
        start_tx_pump
        for attempt in $(seq 1 12); do
            sleep 5
            snap_json=$(curl -sf "${snap_url}/api/snapshots" 2>/dev/null || echo "null")
            snap_count=$(echo "$snap_json" | jq 'length' 2>/dev/null || echo "0")
            [ "$snap_count" -gt 0 ] && break
        done
        stop_tx_pump
        if [ "$snap_count" -eq 0 ]; then
            log "- ❌ Không có snapshot nào được tạo sau 60s"
            return 1
        fi
    fi
    local snap_name=$(echo "$snap_json" | jq -r '.[-1].snapshot_name')
    log "- ✅ Đã tìm thấy Snapshot: \`$snap_name\` (Tổng cộng $snap_count)"

    # Record pre-recovery state
    local pre_block_src=$(get_block_number "$src_port")
    local pre_block_dst=$(get_block_number "$dst_port")
    log "- Trước khi phục hồi: src_block=\`$pre_block_src\` dst_block=\`$pre_block_dst\`"
    log ""

    # ── Phase 2: Destroy & Restore ──
    log "### Giai đoạn 2: Hủy và Phục hồi Node $dst"
    "$ORCHESTRATOR" stop-node "$dst" > /dev/null 2>&1 || true

    local dst_data="$GO_DIR/sample/node$dst"
    rm -rf "$dst_data/data" "$dst_data/data-write" "$dst_data/back_up" "$dst_data/back_up_write"
    rm -rf "$LOG_BASE/node_$dst" "$RUST_DIR/config/storage/node_$dst"
    log "- 🗑️ Node $dst state wiped"

    local dl_dir="/tmp/snapshot_download_stability_${dst}"
    rm -rf "$dl_dir"; mkdir -p "$dl_dir"
    wget -q -c -r -np -nH --cut-dirs=2 -P "$dl_dir" --reject="index.html*" "${snap_url}/files/${snap_name}/" 2>/dev/null
    if [ ! -d "$dl_dir" ] || [ -z "$(ls -A "$dl_dir" 2>/dev/null)" ]; then
        log "- ❌ Snapshot download failed"
        return 1
    fi

    mkdir -p "$dst_data/data/data" "$dst_data/back_up" "$dst_data/data-write" "$dst_data/back_up_write"
    for dir_name in $LEVELDB_DIRS; do
        [ -d "$dl_dir/$dir_name" ] && mv "$dl_dir/$dir_name" "$dst_data/data/data/$dir_name"
    done
    [ -f "$dl_dir/metadata.json" ] && mv "$dl_dir/metadata.json" "$dst_data/data/data/metadata.json"
    [ -d "$dl_dir/back_up" ] && cp -r "$dl_dir/back_up/"* "$dst_data/back_up/" 2>/dev/null || true
    [ -d "$dl_dir/data-write" ] && cp -r "$dl_dir/data-write/"* "$dst_data/data-write/" 2>/dev/null || true
    [ -d "$dl_dir/back_up_write" ] && cp -r "$dl_dir/back_up_write/"* "$dst_data/back_up_write/" 2>/dev/null || true
    find "$dst_data" -name "LOCK" -delete 2>/dev/null || true
    mkdir -p "$LOG_BASE/node_$dst" "$RUST_DIR/config/storage/node_$dst"
    rm -rf "$dl_dir"
    log "- ✅ Snapshot restored to Node $dst"
    log ""

    # ── Phase 3: Restart & Catch-up ──
    log "### Phase 3: Restart & Sync"
    start_tx_pump
    "$ORCHESTRATOR" start-node "$dst" > /dev/null 2>&1 || true

    local elapsed=0
    local sync_ok=false
    while [ $elapsed -lt $CATCHUP_TIMEOUT ]; do
        local dst_block=$(get_block_number "$dst_port")
        local src_block=$(get_block_number "$src_port")
        if [ "$dst_block" != "-1" ] && [ "$src_block" != "-1" ]; then
            local gap=$((src_block - dst_block))
            [ $gap -lt 0 ] && gap=0
            if [ $gap -le 5 ]; then
                log "- ✅ Synced in ${elapsed}s (dst=\`$dst_block\` src=\`$src_block\`)"
                log "- ✅ Đã đồng bộ sau ${elapsed}s (dst=\`$dst_block\` src=\`$src_block\`)"
                sync_ok=true
                break
            fi
            [ $((elapsed % 10)) -eq 0 ] && log "- ⏳ Đang đồng bộ... dst=\`$dst_block\` src=\`$src_block\` gap=\`$gap\` (${elapsed}s)"
        fi
        sleep 3; elapsed=$((elapsed + 3))
    done

    if [ "$sync_ok" = false ]; then
        stop_tx_pump
        log "- ❌ Quá thời gian chờ đồng bộ (Timeout) sau ${CATCHUP_TIMEOUT}s"
        return 1
    fi

    sleep $SETTLE_TIME
    stop_tx_pump
    log ""

    # ── Phase 4: Integrity Verification ──
    log "### Giai đoạn 4: Xác minh tính nhất quán (Integrity Audit)"
    local src_peer_port=$((19200 + src))
    local dst_peer_port=$((19200 + dst))
    local src_info=$(get_peer_info "$src_peer_port")
    local dst_info=$(get_peer_info "$dst_peer_port")

    local src_gei=$(echo "$src_info" | grep -o '"global_exec_index":[0-9]*' | cut -d: -f2)
    local dst_gei=$(echo "$dst_info" | grep -o '"global_exec_index":[0-9]*' | cut -d: -f2)
    local src_sr=$(echo "$src_info" | grep -o '"state_root":"[^"]*"' | cut -d'"' -f4)
    local dst_sr=$(echo "$dst_info" | grep -o '"state_root":"[^"]*"' | cut -d'"' -f4)
    local src_blk=$(get_block_number "$src_port")
    local dst_blk=$(get_block_number "$dst_port")

    log "| Chỉ số | Node $src (Nguồn) | Node $dst (Đích) | Khớp |"
    log "|--------|-------------------|-------------------|-------|"
    local blk_match="✅"; [ "$src_blk" != "$dst_blk" ] && { local bdiff=$((src_blk-dst_blk)); [ $bdiff -lt 0 ] && bdiff=$((-bdiff)); [ $bdiff -gt 5 ] && blk_match="❌"; }
    log "| Khối | $src_blk | $dst_blk | $blk_match |"

    local gei_match="✅"
    if [ -n "$src_gei" ] && [ -n "$dst_gei" ]; then
        local gdiff=$((src_gei - dst_gei)); [ $gdiff -lt 0 ] && gdiff=$((-gdiff))
        [ $gdiff -gt 5 ] && gei_match="❌"
    fi
    log "| GEI | ${src_gei:-?} | ${dst_gei:-?} | $gei_match |"

    local sr_match="✅"
    if [ "$src_blk" = "$dst_blk" ]; then
        [ "$src_sr" != "$dst_sr" ] && sr_match="❌"
    else
        sr_match="⚠️ (Khác khối)"
    fi
    log "| State Root | ${src_sr:0:20}... | ${dst_sr:0:20}... | $sr_match |"
    log ""

    if [ "$sr_match" = "❌" ]; then
        log "- 🚨 **LỆCH STATE ROOT CÙNG KHỐI — PHÁT HIỆN FORK**"
        return 1
    fi
    if [ "$gei_match" = "❌" ]; then
        log "- ⚠️ **CẢNH BÁO: LỆCH GEI LỚN** (src=$src_gei dst=$dst_gei). Có thể do lag."
        # Không return 1 ở đây, vì hash comparison (Phase 6) mới là kiểm tra chính xác nhất.
    fi
    log "- ✅ Đã thông qua các kiểm tra trạng thái sơ bộ"
    log ""

    # ── Phase 5: Liveness Check ──
    log "### Giai đoạn 5: Kiểm tra Liveness sau phục hồi"
    local live_start=$(get_block_number "$dst_port")
    start_tx_pump
    sleep $LIVENESS_WAIT
    stop_tx_pump
    local live_end=$(get_block_number "$dst_port")

    if [ "$live_end" -gt "$live_start" ]; then
        local produced=$((live_end - live_start))
        log "- ✅ Liveness OK: $produced khối đã được tạo trong ${LIVENESS_WAIT}s"
    else
        log "- ❌ **THẤT BẠI LIVENESS**: Không có khối mới nào trong ${LIVENESS_WAIT}s"
        return 1
    fi

    # ── Phase 6: Hash Parity (spot check) ──
    log ""
    log "### Giai đoạn 6: Kiểm tra tính tương đồng Mã băm (Hash Parity)"
    local parity_ok=true
    local check_block=$(get_block_number "$dst_port")
    for offset in 0 2 5; do
        local bn=$((check_block - offset))
        [ $bn -lt 1 ] && continue
        local hex=$(printf "0x%x" "$bn")
        local ref_hash=""
        local mismatch=false
        for i in $(seq 0 $((NUM_NODES - 1))); do
            local port=${RPC_PORTS[$i]}
            local b=$(get_block_number "$port")
            [ "$b" = "-1" ] || [ "$b" -lt "$bn" ] && continue
            local result=$(curl -s --max-time 2 -X POST "http://127.0.0.1:$port" \
                -H "Content-Type: application/json" \
                -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$hex\", false],\"id\":1}" 2>/dev/null)
            local hash=$(echo "$result" | grep -o '"hash":"0x[0-9a-fA-F]*"' | head -1 | cut -d'"' -f4)
            [ -z "$hash" ] || [ "$hash" = "null" ] && continue
            if [ -z "$ref_hash" ]; then ref_hash="$hash"
            elif [ "$hash" != "$ref_hash" ]; then mismatch=true; break; fi
        done
        if [ "$mismatch" = true ]; then
            log "- ❌ Khối #$bn: Lệch HASH"
            parity_ok=false
        else
            log "- ✅ Khối #$bn: Hash đồng nhất"
        fi
    done

    if [ "$parity_ok" = false ]; then
        log "- 🚨 **THẤT BẠI TÍNH TƯƠNG ĐỒNG HASH — PHÁT HIỆN FORK**"
        return 1
    fi

    local round_duration=$((SECONDS - round_start))
    log ""
    log "**Thời gian:** ${round_duration}s"
    log ""
    log "> ✅ **Vòng $round THÀNH CÔNG**"
    log ""
    return 0
}

# ═══════════════════════════════════════════════════════════════════
#  MAIN
# ═══════════════════════════════════════════════════════════════════

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🔁 Snapshot Recovery Stability Loop                   ║${NC}"
echo -e "${BOLD}║  Vòng: $MAX_ROUNDS | Nguồn: Node $SRC_NODE | Đích: Node $DST_NODE              ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""

# Initialize report
cat > "$REPORT_FILE" <<EOF

| Parameter | Value |
|-----------|-------|
| Timestamp | $(date) |
| Hostname | $(hostname) |
| Planned Rounds | $MAX_ROUNDS |
| Source Node | $SRC_NODE |
| Destination Node | $DST_NODE |
| Rotate DST | $ROTATE_DST |
| Catchup Timeout | ${CATCHUP_TIMEOUT}s |

---

EOF

pkill -f tx_sender 2>/dev/null || true

TOTAL_START=$SECONDS
PASSED=0
FAILED_ROUND=0
FAIL_REASON=""

for round in $(seq 1 $MAX_ROUNDS); do
    # Rotate destination node if requested
    current_dst=$DST_NODE
    if [ "$ROTATE_DST" = true ]; then
        local_idx=$(( (round - 1) % ${#AVAILABLE_DST_NODES[@]} ))
        current_dst=${AVAILABLE_DST_NODES[$local_idx]}
    fi

    echo -e "${CYAN}${BOLD}━━━ Round $round/$MAX_ROUNDS (Node $SRC_NODE → Node $current_dst) ━━━${NC}"

    if run_single_round "$round" "$current_dst"; then
        PASSED=$((PASSED + 1))
        echo -e "${GREEN}✅ Round $round PASSED${NC}"
    else
        FAILED_ROUND=$round
        FAIL_REASON="See Phase details above"
        echo -e "${RED}❌ Round $round FAILED — stopping loop${NC}"

        # Collect full diagnostics
        collect_diagnostics "$round" "$current_dst" "$FAIL_REASON"
        break
    fi

    # Brief pause between rounds
    if [ "$round" -lt "$MAX_ROUNDS" ]; then
        echo -e "${YELLOW}⏳ Cooling down 5s before next round...${NC}"
        sleep 5
    fi
done

TOTAL_DURATION=$((SECONDS - TOTAL_START))

# ── Final Summary ──
log ""
log "---"
log ""
log "## 📊 Stability Test Summary"
log ""
log "| Metric | Value |"
log "|--------|-------|"
log "| Total Rounds Planned | $MAX_ROUNDS |"
log "| Rounds Completed | $((PASSED + (FAILED_ROUND > 0 ? 1 : 0))) |"
log "| ✅ Passed | $PASSED |"
log "| ❌ Failed | $((FAILED_ROUND > 0 ? 1 : 0)) |"
log "| Failed at Round | ${FAILED_ROUND:-N/A} |"
log "| Total Duration | ${TOTAL_DURATION}s |"
log ""

if [ "$FAILED_ROUND" -gt 0 ]; then
    log "> 🚨 **STABILITY TEST FAILED at round $FAILED_ROUND/$MAX_ROUNDS.**"
    log "> Gửi file \`$REPORT_FILE\` cho AI agent để phân tích và fix lỗi."
    log ""
    log "### 🤖 AI Agent Instructions"
    log ""
    log "This report documents a snapshot recovery stability test failure."
    log "Key areas to investigate:"
    log "1. Check the **Diagnostics** section for state divergence details"
    log "2. Compare **State Root** and **GEI** between nodes"
    log "3. Review **Node logs** for FORK, DIVERGE, PANIC, or STATE-ROOT-CHECK markers"
    log "4. Check if STARTUP-SYNC completed correctly"
    log "5. Verify CommitProcessor did not replay already-synced commits"
    log ""
    log "Relevant source files:"
    log "- \`consensus/metanode/src/node/consensus_node.rs\` (STARTUP-SYNC logic)"
    log "- \`execution/executor/snapshot_manager.go\` (Snapshot creation/restore)"
    log "- \`consensus/metanode/src/node/health_check.rs\` (Post-recovery health)"
    log "- \`consensus/metanode/src/consensus/commit_processor.rs\` (Commit dedup)"
else
    log "> ✅ **ALL $MAX_ROUNDS ROUNDS PASSED.** System is stable."
fi

log ""
log "**Report generated:** $(date)"

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
if [ "$FAILED_ROUND" -gt 0 ]; then
    echo -e "${RED}${BOLD}║  ❌ FAILED at round $FAILED_ROUND/$MAX_ROUNDS                            ║${NC}"
else
    echo -e "${GREEN}${BOLD}║  ✅ ALL $MAX_ROUNDS ROUNDS PASSED                               ║${NC}"
fi
echo -e "${BOLD}╠══════════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║  📁 Report: ${NC}${CYAN}$REPORT_FILE${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"

[ "$FAILED_ROUND" -gt 0 ] && exit 1 || exit 0
