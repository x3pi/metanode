#!/bin/bash
# run_multinode_load.sh — multi-node load testing script
# Usage: ./run_multinode_load.sh [clients] [tx_per_client]
# FIX: Uses RPC eth_blockNumber for reliable block range tracking
#      instead of grep-based log parsing which breaks on log rotation.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_PROJECT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
MTN_CONSENSUS_ROOT="$(cd "$GO_PROJECT/../mtn-consensus" && pwd)"

CLIENTS=${1:-10}
TX_PER_CLIENT=${2:-20000}
BATCH_SIZE=2000
LOG_DIR="$MTN_CONSENSUS_ROOT/metanode/logs/node_0/go-master"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

TOTAL_TX=$((CLIENTS * TX_PER_CLIENT))

# Sub-node TCP ports for TX injection
NODES=("127.0.0.1:4201" "127.0.0.1:6201" "127.0.0.1:6211" "127.0.0.1:6221")
# Use MASTER node RPC ports for verification (must use real IPs for remote nodes)
RPCS=("127.0.0.1:8757" "127.0.0.1:10747" "127.0.0.1:10749" "127.0.0.1:10750")
NUM_NODES=${#NODES[@]}

# Helper: get current block number from RPC
get_block_number() {
    local rpc_url="$1"
    local hex=$(curl -s "http://$rpc_url" -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null \
        | grep -oP '"result":"0x[0-9a-fA-F]+"' | grep -oP '0x[0-9a-fA-F]+')
    printf "%d" "$hex" 2>/dev/null || echo "0"
}

echo ""
echo -e "${BOLD}╔═══════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║  🌐 MULTI-NODE LOAD TEST — $CLIENTS clients × $TX_PER_CLIENT TXs       ║${NC}"
echo -e "${BOLD}║  📦 Total TX target: ${CYAN}${TOTAL_TX}${NC}${BOLD}                              ║${NC}"
echo -e "${BOLD}╚═══════════════════════════════════════════════════════════╝${NC}"
echo ""

# ── SNAPSHOT BLOCK HEIGHT BEFORE TEST ──────────────────────────────
BLOCK_BEFORE=$(get_block_number "${RPCS[0]}")
echo -e "  📊 Block height before test: ${BOLD}#${BLOCK_BEFORE}${NC}"

echo -e "${YELLOW}⏳ Compiling tps_blast tool...${NC}"
cd "$GO_PROJECT" || exit 1
go build -o /tmp/tps_blast ./cmd/tool/tps_blast

echo -e "${YELLOW}🧹 Cleaning up old cached blast logs, and signals... (Keeping accounts for cache hits)${NC}"
rm -f /tmp/blast_accounts_*.json
rm -f /tmp/multinode_blast_*.log
rm -f /tmp/blast_start_signal*
rm -f /tmp/backend_start_ms.log

echo -e "${YELLOW}⏳ Launching $CLIENTS load balancing clients across $NUM_NODES nodes...${NC}"
for (( i=1; i<=CLIENTS; i++ )); do
    NODE_INDEX=$(( (i - 1) % NUM_NODES ))
    TARGET_NODE=${NODES[$NODE_INDEX]}
    TARGET_RPC=${RPCS[$NODE_INDEX]}
    
    echo "  → Client $i connecting to node $TARGET_NODE"
    /tmp/tps_blast -config ./cmd/tool/tps_blast/config.json -node "$TARGET_NODE" -count "$TX_PER_CLIENT" -batch "$BATCH_SIZE" -sleep 3 -wait 60 -rpc "$TARGET_RPC" -wait-file "/tmp/blast_start_signal" -accounts_file "/tmp/blast_accounts_${i}.json" > "/tmp/multinode_blast_${i}.log" 2>&1 &
done

echo -e "${YELLOW}⏳ Waiting for ALL $CLIENTS clients to build TXs and connect...${NC}"
while [ $(ls /tmp/blast_start_signal_ready_* 2>/dev/null | wc -l) -lt $CLIENTS ]; do
    sleep 0.1
done

echo -e "${GREEN}🚀 All clients are fully synced! Broadcasting START signal...${NC}"
START_SEC=$(date +%s)
touch /tmp/blast_start_signal

echo -e "${YELLOW}⏳ Waiting for clients to inject TXs...${NC}"
wait

echo -e "${YELLOW}⏳ Chain is processing... Waiting until idle (no new blocks for 10s)...${NC}"
LAST_BLOCK=$(get_block_number "${RPCS[0]}")
STAGNANT=0
while [ $STAGNANT -lt 10 ]; do
    sleep 1
    CURRENT_BLOCK=$(get_block_number "${RPCS[0]}")
    if [ "$CURRENT_BLOCK" -gt "$LAST_BLOCK" ]; then
        echo -e "  📦 Block #${CURRENT_BLOCK} (${STAGNANT}s idle reset)"
        LAST_BLOCK=$CURRENT_BLOCK
        STAGNANT=0
    else
        STAGNANT=$((STAGNANT + 1))
    fi
done
END_SEC=$(date +%s)

# ── SNAPSHOT BLOCK HEIGHT AFTER TEST ──────────────────────────────
BLOCK_AFTER=$(get_block_number "${RPCS[0]}")
echo -e "${GREEN}✅ Chain idle (10s no new blocks). Block height after test: ${BOLD}#${BLOCK_AFTER}${NC}"


# ── PARSE LOG ─────────────────────────────────────────────────────
# Strategy: grep ALL createBlockFromResults lines from ALL log files,
# filter by block number range, then DEDUPLICATE by taking the LAST
# occurrence of each block number (handles log rotation correctly —
# the latest App.log has the most recent run's data).
echo -e "${GREEN}✅ Generating report...${NC}"

BLOCK_START=$((BLOCK_BEFORE + 1))
BLOCK_END=$BLOCK_AFTER

# Collect ALL matching lines from all log files, in chronological order.
# Using 'ls -rt' ensures older files (e.g., from old epochs) are processed first,
# and the latest App.log is processed last, properly overwriting block stats.
ALL_BLOCK_LINES=""
for logf in $(ls -rt "$LOG_DIR"/App.log* "$LOG_DIR"/epoch_*/App.log* 2>/dev/null); do
    [ -f "$logf" ] || continue
    MATCHES=$(grep 'createBlockFromResults' "$logf" 2>/dev/null || true)
    [ -z "$MATCHES" ] && continue
    ALL_BLOCK_LINES="${ALL_BLOCK_LINES}${MATCHES}"$'\n'
done

# Filter by block number range, then deduplicate by block number.
# Using an associative array to keep the LAST occurrence of each block.
declare -A BLOCK_LINE_MAP

while IFS= read -r line; do
    [ -z "$line" ] && continue
    BNUM=$(echo "$line" | grep -oP 'block #\d+' | grep -oP '\d+')
    [ -z "$BNUM" ] && continue
    # Check block number range only — no date filtering needed
    if [ "$BNUM" -ge "$BLOCK_START" ] 2>/dev/null && [ "$BNUM" -le "$BLOCK_END" ] 2>/dev/null; then
        BLOCK_LINE_MAP[$BNUM]="$line"
    fi
done <<< "$ALL_BLOCK_LINES"

# Convert associative array to sorted lines
FILTERED_LINES=""
for BNUM in $(echo "${!BLOCK_LINE_MAP[@]}" | tr ' ' '\n' | sort -n); do
    FILTERED_LINES="${FILTERED_LINES}${BLOCK_LINE_MAP[$BNUM]}"$'\n'
done
FILTERED_LINES=$(echo "$FILTERED_LINES" | grep -v '^$')

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
    TS=$(echo "$line" | grep -oP '\d{2}:\d{2}:\d{2}(\.\d+)?')
    
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
    
    # Search across ALL log files for engine tx/s
    ETPS=""
    for logf in "$LOG_DIR"/App.log* "$LOG_DIR"/epoch_*/App.log*; do
        [ -f "$logf" ] || continue
        ETPS=$(grep "ProcessTransactions.*block #${BNUM}" "$logf" 2>/dev/null | tail -1 | grep -oP '\d+ tx/s' | grep -oP '^\d+' || true)
        [ -n "$ETPS" ] && break
    done
    printf "  %-10s  %10s  %15s  %15s\n" "#$BNUM" "$TXCNT" "$DUR" "${ETPS:+${ETPS} tx/s}"
done <<< "$FILTERED_LINES"

# ── TIMING: BACKEND CHAIN EXECUTION ──
# Exact ms from when Go Sub node forwards FIRST batch to Rust -> until the very last block is processed
PROC_SEC=0
if [ -n "$LAST_TS" ]; then
    L_SEC=$(date -d "$LAST_TS" +%s.%3N 2>/dev/null || echo "0")
    
    if [ -f /tmp/backend_start_ms.log ]; then
        # Take the EARLIEST backend trigger across all nodes
        BACKEND_START_MS=$(sort -n /tmp/backend_start_ms.log | head -1)
        if [ -n "$BACKEND_START_MS" ]; then
            BACKEND_START_SEC=$(awk -v ms="$BACKEND_START_MS" 'BEGIN {printf "%.3f", ms / 1000.0}')
            if [ $(awk -v l="$L_SEC" 'BEGIN {print (l > 0)}') -eq 1 ]; then
                PROC_SEC=$(awk -v l="$L_SEC" -v s="$BACKEND_START_SEC" 'BEGIN {print (l - s)}')
                BACKEND_START_HUMAN=$(date -d @"$(echo $BACKEND_START_SEC | cut -d. -f1)" +"%H:%M:%S" 2>/dev/null || echo "$BACKEND_START_SEC")
                echo -e "  ⏱️  Khoảnh khắc Go Sub -> Rust (Backend Triggered): ${BACKEND_START_HUMAN} (mất ${PROC_SEC}s)"
            fi
        fi
    fi

    # Fallback if backend logging fails
    if [ $(awk -v p="$PROC_SEC" 'BEGIN {print (p <= 0)}') -eq 1 ]; then
        if [ $(awk -v l="$L_SEC" 'BEGIN {print (l > 0)}') -eq 1 ]; then
            PROC_SEC=$(awk -v l="$L_SEC" -v s="$START_SEC" 'BEGIN {print (l - s)}')
        fi
    fi
fi

if [ $(awk -v p="$PROC_SEC" 'BEGIN {print (p <= 0)}') -eq 1 ]; then
    PROC_SEC=$((END_SEC - START_SEC - 10))
fi

if [ "$TOTAL_TXS" -gt 0 ]; then
    SYSTEM_TPS=$(awk -v txs="$TOTAL_TXS" -v secs="$PROC_SEC" 'BEGIN {printf "%.0f", txs/secs}')
else
    SYSTEM_TPS=0
fi

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
echo -e "  🧊 Số blocks:           ${BOLD}${TOTAL_BLOCKS}${NC}  (range: #${BLOCK_START} → #${BLOCK_END})"
echo -e "  📈 Max TXs/block:       ${BOLD}${MAX_TXS}${NC} (${MAX_BLOCK})"
echo -e "  ⏱️  Thời gian xử lý:     ${BOLD}${PROC_SEC}s${NC} (${FIRST_TS} → ${LAST_TS})"
echo -e "  👥 Số clients:          ${BOLD}${CLIENTS}${NC} (Load balanced over $NUM_NODES nodes)"
echo ""

echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║                    🔍 KIỂM TRA ĐĂNG KÝ BLS (XÁC THỰC)               ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"

TOTAL_CONFIRMED=0
TOTAL_FAILED=0
TOTAL_ERRORS=0

for (( i=1; i<=CLIENTS; i++ )); do
    LOG_FILE_CLIENT="/tmp/multinode_blast_${i}.log"
    VERIFY_LINE=$(grep -a "Verified:" "$LOG_FILE_CLIENT" | tail -1)
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

echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║                    🔍 KIỂM TRA FORK (master vs node3)               ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════╣${NC}"

# Only check blocks from THIS test run (BLOCK_START → BLOCK_END)
# to avoid flagging pre-existing divergences from prior runs.
CHECK_FROM=$BLOCK_START
CHECK_TO=$((BLOCK_AFTER > 0 ? BLOCK_AFTER : LAST_BNUM))
HASH_OUT=$(cd "$GO_PROJECT" && go run ./cmd/tool/block_hash_checker/ \
    --nodes "master=http://127.0.0.1:8757,node3=http://127.0.0.1:10750" \
    --from $CHECK_FROM --to $CHECK_TO 2>&1)

LAST_LINE=$(echo "$HASH_OUT" | tail -1)

if echo "$LAST_LINE" | grep -q "KHỚP"; then
    echo -e "  ${GREEN}${BOLD}$LAST_LINE${NC}"
    echo -e "  ${GREEN}${BOLD}🛡️  HỆ THỐNG KHÔNG FORK — AN TOÀN 100%${NC}"
else
    echo "$HASH_OUT" | grep -v "^$" | tail -10
    echo -e "  ${RED}${BOLD}⚠️  PHÁT HIỆN LỆCH HASH — CẦN KIỂM TRA!${NC}"
fi
