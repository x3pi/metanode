#!/bin/bash
# run_node0_only_load.sh — Measure TPS specifically on Node 0 (Local or Remote)
# Usage: ./run_node0_only_load.sh [clients] [tx_per_client]

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_PROJECT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
MTN_CONSENSUS_ROOT="$(cd "$GO_PROJECT/../mtn-consensus" && pwd)"

CLIENTS=${1:-10}
TX_PER_CLIENT=${2:-20000}
BATCH_SIZE=2000
# Combine Node 0 local port and Node 1 remote ports to balance the load
NODES=("127.0.0.1:4201" "127.0.0.1:6201" "127.0.0.1:6211" "127.0.0.1:6221")
NUM_NODES=${#NODES[@]}

# Note: Report generation (Engine tx/s) requires access to Node 0 logs.
# If running this remotely, the block-by-block detail might fail unless LOG_FILE is accessible.
# Auto-detect latest epoch directory (epoch_0, epoch_1, etc.)
LOG_DIR_BASE="$MTN_CONSENSUS_ROOT/metanode/logs/node_0/go-master"
LATEST_EPOCH_DIR=$(ls -1d "$LOG_DIR_BASE"/epoch_* 2>/dev/null | sort -t_ -k2 -n | tail -1)
if [ -n "$LATEST_EPOCH_DIR" ]; then
    LOG_FILE="$LATEST_EPOCH_DIR/App.log"
else
    LOG_FILE="$LOG_DIR_BASE/epoch_0/App.log"
fi

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

TOTAL_TX=$((CLIENTS * TX_PER_CLIENT))

echo ""
echo -e "${BOLD}╔═══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🚀 DISTRIBUTED SECURE LOAD TEST — $CLIENTS clients × $TX_PER_CLIENT TXs  ║${NC}"
echo -e "${BOLD}║  📍 Target Nodes: Node 0 & Node 1 (Load Balanced)      ║${NC}"
echo -e "${BOLD}║  📦 Total TX target: ${CYAN}${TOTAL_TX}${NC}${BOLD}                              ║${NC}"
echo -e "${BOLD}╚═══════════════════════════════════════════════════════════╝${NC}"
echo ""

BLOCKS_BEFORE=$(grep -c 'createBlockFromResults' "$LOG_FILE" 2>/dev/null) || BLOCKS_BEFORE=0

echo -e "${YELLOW}⏳ Compiling tps_blast tool...${NC}"
cd "$GO_PROJECT"
go build -o /tmp/tps_blast ./cmd/tool/tps_blast

echo -e "${YELLOW}🧹 Cleaning up old cached account files...${NC}"
rm -f /tmp/blast_accounts_*.json

echo -e "${YELLOW}⏳ Launching $CLIENTS clients targeting multiple nodes...${NC}"
for (( i=1; i<=CLIENTS; i++ )); do
    NODE_INDEX=$(( (i - 1) % NUM_NODES ))
    TARGET_NODE=${NODES[$NODE_INDEX]}
    echo "  → Client $i connecting to target $TARGET_NODE"
    /tmp/tps_blast -config ./cmd/tool/tps_blast/config.json -node "$TARGET_NODE" -count "$TX_PER_CLIENT" -batch "$BATCH_SIZE" -sleep 5 -wait 60 -rpc "127.0.0.1:8757" -accounts_file "/tmp/blast_accounts_${i}.json" > "/tmp/node0_blast_${i}.log" 2>&1 &
done

echo -e "${YELLOW}⏳ Waiting for clients to inject TXs...${NC}"
wait

if [ ! -f "$LOG_FILE" ]; then
    echo -e "${RED}⚠️  Warning: Log file not found at $LOG_FILE.${NC}"
    echo -e "${YELLOW}   If Node 0 is on a remote machine, skipping block-by-block TPS report.${NC}"
else
    echo -e "${YELLOW}⏳ Chain is processing... Waiting until idle (no new blocks for 5s)...${NC}"
    LAST_LINE_COUNT=0
    STAGNANT=0
    while [ $STAGNANT -lt 5 ]; do
        CURRENT=$(grep -c 'createBlockFromResults' "$LOG_FILE" 2>/dev/null)
        if [ "$CURRENT" -gt "$LAST_LINE_COUNT" ]; then
            LAST_LINE_COUNT=$CURRENT
            STAGNANT=0
        else
            STAGNANT=$((STAGNANT + 1))
        fi
        sleep 1
    done

    echo -e "${GREEN}✅ Chain idle. Generating report...${NC}"
    # Report generation logic...
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
        ETPS=$(grep "ProcessTransactions.*block #${BNUM}" "$LOG_FILE" 2>/dev/null | tail -1 | grep -oP '\d+ tx/s' | grep -oP '^\d+' || true)
        printf "  %-10s  %10s  %15s  %15s\n" "#$BNUM" "$TXCNT" "$DUR" "${ETPS:+${ETPS} tx/s}"
    done <<< "$NEW_BLOCKS"

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
    echo -e "  📊 ${BOLD}SYSTEM TPS:  ~${SYSTEM_TPS} tx/s${NC}"
    echo -e "  📦 Tổng TX gửi:        ${BOLD}${TOTAL_TX}${NC}"
    echo -e "  📥 TX trong blocks:     ${BOLD}${TOTAL_TXS}${NC}"
    echo -e "  🧊 Số blocks:           ${BOLD}${TOTAL_BLOCKS}${NC}"
    echo -e "  📈 Max TXs/block:       ${BOLD}${MAX_TXS}${NC} (${MAX_BLOCK})"
    echo -e "  ⏱️  Thời gian xử lý:     ${BOLD}${PROC_SEC}s${NC} (${FIRST_TS} → ${LAST_TS})"
fi

echo ""
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║                    🔍 KIỂM TRA ĐĂNG KÝ BLS (XÁC THỰC)               ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
TOTAL_CONFIRMED=0
TOTAL_FAILED=0
TOTAL_ERRORS=0
for (( i=1; i<=CLIENTS; i++ )); do
    LOG_FILE_CLIENT="/tmp/node0_blast_${i}.log"
    VERIFY_LINE=$(grep "Verified:" "$LOG_FILE_CLIENT" | tail -1)
    if [ -n "$VERIFY_LINE" ]; then
        CONFIRMED=$(echo "$VERIFY_LINE" | sed -n 's/.*✅ \([0-9]*\).*/\1/p')
        FAILED=$(echo "$VERIFY_LINE" | sed -n 's/.*❌ \([0-9]*\).*/\1/p')
        ERRORS=$(echo "$VERIFY_LINE" | sed -n 's/.*⚠️ \([0-9]*\).*/\1/p')
        TOTAL_CONFIRMED=$((TOTAL_CONFIRMED + CONFIRMED))
        TOTAL_FAILED=$((TOTAL_FAILED + FAILED))
        TOTAL_ERRORS=$((TOTAL_ERRORS + ERRORS))
    fi
done
SUCCESS_RATE=0
if [ $TOTAL_TX -gt 0 ]; then
    SUCCESS_RATE=$(awk "BEGIN {printf \"%.1f\", ($TOTAL_CONFIRMED/$TOTAL_TX)*100}")
fi
echo -e "  🔍 Verified Summary:     ${TOTAL_TX}/${TOTAL_TX} (✅ ${GREEN}${TOTAL_CONFIRMED}${NC} | ❌ ${RED}${TOTAL_FAILED}${NC} | ⚠️ ${YELLOW}${TOTAL_ERRORS}${NC})"
echo -e "  ✅ Success Rate:         ${BOLD}${SUCCESS_RATE}%${NC}"
echo ""
