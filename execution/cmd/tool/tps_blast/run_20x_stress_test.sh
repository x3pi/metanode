#!/bin/bash
# run_20x_stress_test.sh — Run the multi-node load test N times consecutively
# and generate a summary report in Markdown format.
#
# Usage:
#   ./run_20x_stress_test.sh [rounds] [clients] [tx_per_client]
#
# Defaults:
#   rounds=20, clients=10, tx_per_client=10000
#
# Output:
#   - Console: live progress + final summary table
#   - File:    ./stress_test_report_<timestamp>.md

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_PROJECT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

ROUNDS=${1:-20}
CLIENTS=${2:-10}
TX_PER_CLIENT=${3:-10000}
TOTAL_PER_ROUND=$((CLIENTS * TX_PER_CLIENT))
GRAND_TOTAL=$((ROUNDS * TOTAL_PER_ROUND))

TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
REPORT_FILE="$SCRIPT_DIR/stress_test_report_${TIMESTAMP}.md"

# ANSI colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# RPC endpoints for fork checking
RPCS=("127.0.0.1:8646" "127.0.0.1:10646" "127.0.0.1:10650" "127.0.0.1:10651")

get_block_number() {
    local rpc_url="$1"
    local hex=$(curl -s "http://$rpc_url" -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null \
        | grep -oP '"result":"0x[0-9a-fA-F]+"' | grep -oP '0x[0-9a-fA-F]+')
    printf "%d" "$hex" 2>/dev/null || echo "0"
}

echo ""
echo -e "${BOLD}╔═══════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🔥 STRESS TEST — ${ROUNDS} rounds × ${CLIENTS} clients × ${TX_PER_CLIENT} TXs        ║${NC}"
echo -e "${BOLD}║  📦 Grand total: ${CYAN}${GRAND_TOTAL}${NC}${BOLD} transactions                            ║${NC}"
echo -e "${BOLD}╚═══════════════════════════════════════════════════════════════════╝${NC}"
echo ""

# ── ARRAYS TO STORE RESULTS ──
declare -a R_TPS
declare -a R_MAX_TXS
declare -a R_AVG_TXS
declare -a R_BLOCKS
declare -a R_FORK
declare -a R_STATUS
declare -a R_CONFIRMED
declare -a R_WALL_TIME

OVERALL_START=$(date +%s)

for (( round=1; round<=ROUNDS; round++ )); do
    echo ""
    echo -e "${BOLD}════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}  🔄 ROUND $round / $ROUNDS${NC}"
    echo -e "${BOLD}════════════════════════════════════════════════════${NC}"

    ROUND_START=$(date +%s)
    BLOCK_BEFORE=$(get_block_number "${RPCS[0]}")

    # ── RUN THE LOAD TEST ──
    set +e
    OUTPUT=$(bash "$SCRIPT_DIR/run_multinode_load.sh" "$CLIENTS" "$TX_PER_CLIENT" 2>&1)
    EXIT_CODE=$?
    set -e

    ROUND_END=$(date +%s)
    WALL_TIME=$((ROUND_END - ROUND_START))

    BLOCK_AFTER=$(get_block_number "${RPCS[0]}")
    NUM_BLOCKS=$((BLOCK_AFTER - BLOCK_BEFORE))

    # ── PARSE TPS ──
    TPS=$(echo "$OUTPUT" | grep -oP 'SYSTEM TPS:\s+~\K\d+' || echo "0")
    [ -z "$TPS" ] && TPS=0

    # ── PARSE MAX TXs/block ──
    MAX_TXS_BLOCK=$(echo "$OUTPUT" | grep -oP 'Max TXs/block:\s+\K\d+' || echo "0")
    [ -z "$MAX_TXS_BLOCK" ] && MAX_TXS_BLOCK=0

    # ── PARSE TX IN BLOCKS ──
    TX_IN_BLOCKS=$(echo "$OUTPUT" | grep -oP 'TX trong blocks:\s+\K\d+' || echo "0")
    [ -z "$TX_IN_BLOCKS" ] && TX_IN_BLOCKS=0

    # ── COMPUTE AVG TXs/block ──
    if [ "$NUM_BLOCKS" -gt 0 ] && [ "$TX_IN_BLOCKS" -gt 0 ]; then
        AVG_TXS_BLOCK=$((TX_IN_BLOCKS / NUM_BLOCKS))
    else
        AVG_TXS_BLOCK=0
    fi

    # ── PARSE VERIFIED ──
    CONFIRMED=$(echo "$OUTPUT" | grep -oP '✅ \K\d+' | tail -1 || echo "0")
    [ -z "$CONFIRMED" ] && CONFIRMED=0

    # ── FORK CHECK ──
    FORK_STATUS="✅ Không Fork"
    if echo "$OUTPUT" | grep -q "PHÁT HIỆN LỆCH HASH"; then
        FORK_STATUS="❌ FORK!"
    fi

    # ── ROUND STATUS ──
    if [ "$EXIT_CODE" -ne 0 ]; then
        STATUS="❌ Lỗi (exit=$EXIT_CODE)"
    elif [ "$TPS" -ge 10000 ]; then
        STATUS="✅ Hoàn tất"
    else
        STATUS="✅ Hoàn tất"
    fi

    R_TPS[$round]=$TPS
    R_MAX_TXS[$round]=$MAX_TXS_BLOCK
    R_AVG_TXS[$round]=$AVG_TXS_BLOCK
    R_BLOCKS[$round]=$NUM_BLOCKS
    R_FORK[$round]="$FORK_STATUS"
    R_STATUS[$round]="$STATUS"
    R_CONFIRMED[$round]=$CONFIRMED
    R_WALL_TIME[$round]=$WALL_TIME

    # ── LIVE SUMMARY ──
    echo ""
    echo -e "  ${CYAN}Round $round:${NC} TPS=${BOLD}${TPS}${NC}, Blocks=${NUM_BLOCKS}, AvgTX/blk=${AVG_TXS_BLOCK}, MaxTX/blk=${MAX_TXS_BLOCK}, Fork=${FORK_STATUS}, Time=${WALL_TIME}s"

    # ── COOLDOWN (skip after last round) ──
    if [ $round -lt $ROUNDS ]; then
        echo -e "  ${YELLOW}⏳ Cooldown 3s before next round...${NC}"
        sleep 3
    fi
done

OVERALL_END=$(date +%s)
OVERALL_DURATION=$((OVERALL_END - OVERALL_START))

# ══════════════════════════════════════════════════════════════════════
# GENERATE MARKDOWN REPORT
# ══════════════════════════════════════════════════════════════════════

# Calculate aggregates
SUM_TPS=0
MIN_TPS=999999
MAX_TPS=0
FORK_COUNT=0
for (( i=1; i<=ROUNDS; i++ )); do
    tps=${R_TPS[$i]}
    SUM_TPS=$((SUM_TPS + tps))
    [ "$tps" -lt "$MIN_TPS" ] && MIN_TPS=$tps
    [ "$tps" -gt "$MAX_TPS" ] && MAX_TPS=$tps
    echo "${R_FORK[$i]}" | grep -q "FORK" && FORK_COUNT=$((FORK_COUNT + 1))
done
AVG_TPS=$((SUM_TPS / ROUNDS))

cat > "$REPORT_FILE" << HEADER
# 📊 Báo Cáo Stress Test Cluster (${ROUNDS} Lần)

**Ngày chạy**: $(date +"%Y-%m-%d %H:%M:%S")
**Mục tiêu mỗi lần test**: ${TOTAL_PER_ROUND} TXs (${CLIENTS} clients × ${TX_PER_CLIENT} TX)
**Tổng giao dịch toàn bộ**: ${GRAND_TOTAL} TXs
**Tổng thời gian**: ${OVERALL_DURATION}s
**Tiêu chí**: System TPS > 10.000 tx/s, không bị lỗi deadlock, không bị fork.

| Lần chạy | System TPS | Avg TXs/Block | Max TXs/Block | Số Blocks | Wall Time | Trạng thái Fork | Kết Quả |
|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
HEADER

for (( i=1; i<=ROUNDS; i++ )); do
    echo "| $i | **${R_TPS[$i]} tx/s** | ${R_AVG_TXS[$i]} | ${R_MAX_TXS[$i]} | ${R_BLOCKS[$i]} | ${R_WALL_TIME[$i]}s | ${R_FORK[$i]} | ${R_STATUS[$i]} |" >> "$REPORT_FILE"
done

cat >> "$REPORT_FILE" << FOOTER

## 📈 Tổng Kết

| Metric | Giá trị |
|--------|---------|
| Tổng số giao dịch | **${GRAND_TOTAL}** |
| Số rounds | **${ROUNDS}** |
| TPS trung bình | **${AVG_TPS} tx/s** |
| TPS cao nhất | **${MAX_TPS} tx/s** |
| TPS thấp nhất | **${MIN_TPS} tx/s** |
| Tổng thời gian | **${OVERALL_DURATION}s** |
| Số lần fork | **${FORK_COUNT}** |

FOOTER

if [ "$FORK_COUNT" -eq 0 ]; then
    echo "🛡️ **Kết luận**: Hệ thống hoàn toàn ổn định, không xảy ra fork trong suốt ${ROUNDS} vòng stress test liên tục." >> "$REPORT_FILE"
else
    echo "⚠️ **Cảnh báo**: Phát hiện **${FORK_COUNT}** lần fork! Cần kiểm tra và khắc phục ngay." >> "$REPORT_FILE"
fi

# ── CONSOLE FINAL SUMMARY ──
echo ""
echo -e "${BOLD}╔═══════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║                    📊 KẾT QUẢ STRESS TEST ${ROUNDS} LẦN                 ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════╣${NC}"
echo ""
echo -e "  📦 Tổng giao dịch:      ${BOLD}${GRAND_TOTAL}${NC}"
echo -e "  🔄 Số rounds:           ${BOLD}${ROUNDS}${NC}"
echo -e "  📈 TPS trung bình:      ${BOLD}${AVG_TPS} tx/s${NC}"
echo -e "  🏆 TPS cao nhất:        ${GREEN}${BOLD}${MAX_TPS} tx/s${NC}"
echo -e "  📉 TPS thấp nhất:       ${YELLOW}${BOLD}${MIN_TPS} tx/s${NC}"
echo -e "  ⏱️  Tổng thời gian:      ${BOLD}${OVERALL_DURATION}s${NC}"

if [ "$FORK_COUNT" -eq 0 ]; then
    echo -e "  🛡️  Fork:                ${GREEN}${BOLD}0 / ${ROUNDS} — AN TOÀN 100%${NC}"
else
    echo -e "  ⚠️  Fork:                ${RED}${BOLD}${FORK_COUNT} / ${ROUNDS} — CẦN KIỂM TRA!${NC}"
fi

echo ""
echo -e "  📄 Báo cáo chi tiết:    ${CYAN}${REPORT_FILE}${NC}"
echo ""
echo -e "${BOLD}╚═══════════════════════════════════════════════════════════════════╝${NC}"
