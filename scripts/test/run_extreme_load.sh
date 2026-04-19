#!/bin/bash
# run_extreme_load.sh — Stress test TPS with multiple concurrent clients
# Usage: ./run_extreme_load.sh [clients] [tx_per_client]
# Example: ./run_extreme_load.sh 10 10000

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_PROJECT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MTN_CONSENSUS_ROOT="$(cd "$GO_PROJECT/../mtn-consensus" && pwd)"

CLIENTS=${1:-10}
TX_PER_CLIENT=${2:-10000}
BATCH_SIZE=500
LOG_FILE="$MTN_CONSENSUS_ROOT/metanode/logs/node_0/go-master/epoch_0/App.log"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

TOTAL_TX=$((CLIENTS * TX_PER_CLIENT))

echo ""
echo -e "${BOLD}╔═══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🔥 EXTREME LOAD TEST — $CLIENTS clients × $TX_PER_CLIENT TXs          ║${NC}"
echo -e "${BOLD}║  📦 Total TX target: ${CYAN}${TOTAL_TX}${NC}${BOLD}                              ║${NC}"
echo -e "${BOLD}╚═══════════════════════════════════════════════════════════╝${NC}"
echo ""

# Count existing blocks before blast
BLOCKS_BEFORE=$(grep -c 'createBlockFromResults' "$LOG_FILE" 2>/dev/null) || BLOCKS_BEFORE=0

# Launch all clients
echo -e "${YELLOW}⏳ Launching $CLIENTS concurrent tps_blast clients...${NC}"
cd "$GO_PROJECT"

for i in $(seq 1 $CLIENTS); do
    go run ./cmd/tool/tps_blast/ -config ./cmd/tool/tps_blast/config.json -count $TX_PER_CLIENT -batch $BATCH_SIZE -sleep 500 -skip-verify -wait 30 > "/tmp/extreme_blast_${i}.log" 2>&1 &
done

echo -e "${YELLOW}⏳ Waiting for all clients to finish...${NC}"
wait

# Wait a moment for final blocks to be committed
sleep 3

echo ""
echo -e "${GREEN}✅ All $CLIENTS clients finished.${NC}"
echo ""

# ═══════════════════════════════════════════════════════════════
# Parse Go PERF logs — only NEW blocks (after our blast started)
# ═══════════════════════════════════════════════════════════════

NEW_BLOCKS=$(grep 'createBlockFromResults' "$LOG_FILE" | tail -n +$((BLOCKS_BEFORE + 1)))

TOTAL_BLOCKS=0
TOTAL_TXS=0
MAX_TXS=0
MAX_BLOCK=""
FIRST_TS=""
LAST_TS=""
LAST_BNUM=0

echo -e "${BOLD}╔═══════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║                    📋 CHI TIẾT TỪNG BLOCK                            ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
printf "${BOLD}  %-10s  %10s  %15s  %15s${NC}\n" "Block" "TXs" "createBlock" "Engine tx/s"
echo    "  ──────────  ──────────  ───────────────  ───────────────"

while IFS= read -r line; do
    [ -z "$line" ] && continue
    
    # Extract fields
    TXCNT=$(echo "$line" | grep -oP '\d+ txs' | grep -oP '^\d+')
    DUR=$(echo "$line" | grep -oP 'in [0-9.]+[a-zµ]+' | head -1)
    BNUM=$(echo "$line" | grep -oP 'block #\d+' | grep -oP '\d+')
    TS=$(echo "$line" | grep -oP '\d{2}:\d{2}:\d{2}')
    
    [ -z "$TXCNT" ] && continue
    
    TOTAL_BLOCKS=$((TOTAL_BLOCKS + 1))
    TOTAL_TXS=$((TOTAL_TXS + TXCNT))
    
    if [ "$TXCNT" -gt "$MAX_TXS" ]; then
        MAX_TXS=$TXCNT
        MAX_BLOCK="#$BNUM"
    fi
    
    [ -z "$FIRST_TS" ] && FIRST_TS="$TS"
    LAST_TS="$TS"
    [ -n "$BNUM" ] && LAST_BNUM=$BNUM
    
    # Get engine execution speed for this block
    ETPS=$(grep "ProcessTransactions.*block #${BNUM}" "$LOG_FILE" 2>/dev/null | tail -1 | grep -oP '\d+ tx/s' | grep -oP '^\d+' || true)
    
    printf "  %-10s  %10s  %15s  %15s\n" "#${BNUM}" "${TXCNT}" "${DUR}" "${ETPS:+${ETPS} tx/s}"
    
done <<< "$NEW_BLOCKS"

echo ""

# Calculate processing time
if [ -n "$FIRST_TS" ] && [ -n "$LAST_TS" ]; then
    F_SEC=$(date -d "2026-01-01 $FIRST_TS" +%s 2>/dev/null || echo "0")
    L_SEC=$(date -d "2026-01-01 $LAST_TS" +%s 2>/dev/null || echo "0")
    PROC_SEC=$((L_SEC - F_SEC))
    [ "$PROC_SEC" -le 0 ] && PROC_SEC=1
else
    PROC_SEC=1
fi

SYSTEM_TPS=$((TOTAL_TXS / PROC_SEC))

echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║                    📊 TỔNG KẾT                                      ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo ""

if [ "$SYSTEM_TPS" -ge 10000 ]; then
    echo -e "  🏆 ${GREEN}${BOLD}SYSTEM TPS:  ~${SYSTEM_TPS} tx/s   ✅ VƯỢT MỤC TIÊU 10K!${NC}"
else
    echo -e "  📊 ${YELLOW}${BOLD}SYSTEM TPS:  ~${SYSTEM_TPS} tx/s   (mục tiêu: 10,000)${NC}"
fi

echo ""
echo -e "  📦 Tổng TX gửi:        ${BOLD}${TOTAL_TX}${NC}"
echo -e "  📥 TX trong blocks:     ${BOLD}${TOTAL_TXS}${NC}"
echo -e "  🧊 Số blocks:           ${BOLD}${TOTAL_BLOCKS}${NC}"
echo -e "  📈 Max TXs/block:       ${BOLD}${MAX_TXS}${NC} (${MAX_BLOCK})"
echo -e "  ⏱️  Thời gian xử lý:     ${BOLD}${PROC_SEC}s${NC} (${FIRST_TS} → ${LAST_TS})"
echo -e "  👥 Số clients:          ${BOLD}${CLIENTS}${NC}"
echo ""

# ═══════════════════════════════════════════════════════════════
# Hash check — check ALL blocks from 1 to LAST_BNUM
# ═══════════════════════════════════════════════════════════════
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║                    🔍 KIỂM TRA FORK (master vs node4)               ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo ""

CHECK_TO=$((LAST_BNUM > 0 ? LAST_BNUM : TOTAL_BLOCKS + 2))
HASH_OUT=$(cd "$GO_PROJECT" && go run ./cmd/tool/block_hash_checker/ \
    -nodes "master=http://localhost:8747,node4=http://localhost:10748" \
    -from 1 -to $CHECK_TO 2>&1)

LAST_LINE=$(echo "$HASH_OUT" | tail -1)

if echo "$LAST_LINE" | grep -q "KHỚP"; then
    echo -e "  ${GREEN}${BOLD}$LAST_LINE${NC}"
    echo -e "  ${GREEN}${BOLD}🛡️  HỆ THỐNG KHÔNG FORK — AN TOÀN 100%${NC}"
else
    echo "$HASH_OUT" | grep -v "^$" | tail -5
    echo -e "  ${RED}${BOLD}⚠️  PHÁT HIỆN LỆCH HASH — CẦN KIỂM TRA!${NC}"
fi

echo ""
echo -e "${BOLD}╚═══════════════════════════════════════════════════════════════════════╝${NC}"
