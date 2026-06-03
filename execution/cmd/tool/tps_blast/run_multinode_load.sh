#!/bin/bash
# run_multinode_load.sh — multi-node load testing script
# Usage: ./run_multinode_load.sh [clients] [tx_per_client]
# FIX: Uses RPC eth_blockNumber for reliable block range tracking
#      instead of grep-based log parsing which breaks on log rotation.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_PROJECT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
MTN_CONSENSUS_ROOT="$(cd "$GO_PROJECT/../consensus" && pwd)"

CLIENTS=${1:-5}
TX_PER_CLIENT=${2:-20000}
BATCH_SIZE=${3:-4000}
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
    if [ -n "$rpc_url" ]; then
        local hex=$(curl -s --max-time 1 "http://$rpc_url" -X POST -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null \
            | grep -oP '"result":"0x[0-9a-fA-F]+"' | grep -oP '0x[0-9a-fA-F]+')
        local dec=$(printf "%d" "$hex" 2>/dev/null)
        if [ -n "$dec" ] && [ "$dec" -gt 0 ]; then
            echo "$dec"
            return
        fi
    fi
    # Fallback: Query all nodes in RPCS in case the primary is busy
    for rpc in "${RPCS[@]}"; do
        local hex=$(curl -s --max-time 1 "http://$rpc" -X POST -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null \
            | grep -oP '"result":"0x[0-9a-fA-F]+"' | grep -oP '0x[0-9a-fA-F]+')
        if [ -n "$hex" ]; then
            local dec=$(printf "%d" "$hex" 2>/dev/null)
            if [ -n "$dec" ] && [ "$dec" -gt 0 ]; then
                echo "$dec"
                return
            fi
        fi
    done
    echo "0"
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
echo -e "${YELLOW}⏳ Compiling block_hash_checker tool...${NC}"
go build -o /tmp/block_hash_checker ./cmd/tool/block_hash_checker

echo -e "${YELLOW}🧹 Cleaning up old cached blast logs, and signals... (Keeping accounts for cache hits)${NC}"
rm -f /tmp/blast_accounts_*.json
rm -f /tmp/multinode_blast_*.log
rm -f /tmp/blast_start_signal*
rm -f /tmp/backend_start_ms.log
rm -f /tmp/perf_accumulation.log

echo -e "${YELLOW}⏳ Launching $CLIENTS load balancing clients across $NUM_NODES nodes...${NC}"
CLIENT_PIDS=()
for (( i=1; i<=CLIENTS; i++ )); do
    NODE_INDEX=$(( (i - 1) % NUM_NODES ))
    TARGET_NODE=${NODES[$NODE_INDEX]}
    TARGET_RPC=${RPCS[$NODE_INDEX]}
    
    echo "  → Client $i connecting to node $TARGET_NODE"
    /tmp/tps_blast -config ./cmd/tool/tps_blast/config.json -node "$TARGET_NODE" -count "$TX_PER_CLIENT" -batch "$BATCH_SIZE" -sleep 3 -wait 60 -rpc "$TARGET_RPC" -wait-file "/tmp/blast_start_signal" -accounts_file "/tmp/blast_accounts_${i}.json" -skip-verify > "/tmp/multinode_blast_${i}.log" 2>&1 &
    CLIENT_PIDS+=($!)
done

echo -e "${YELLOW}⏳ Waiting for ALL $CLIENTS clients to build TXs and connect...${NC}"
while true; do
    READY_COUNT=$(ls /tmp/blast_start_signal_ready_* 2>/dev/null | wc -l)
    if [ "$READY_COUNT" -ge "$CLIENTS" ]; then
        break
    fi
    
    for pid in "${CLIENT_PIDS[@]}"; do
        if ! kill -0 "$pid" 2>/dev/null; then
            echo -e "${RED}❌ Error: One of the blast clients (PID $pid) died before reaching ready state!${NC}"
            echo -e "${YELLOW}Logs from failed client:${NC}"
            for logfile in /tmp/multinode_blast_*.log; do
                if grep -q -E "exiting|failed|refused" "$logfile" 2>/dev/null; then
                    echo "--- $logfile ---"
                    cat "$logfile"
                fi
            done
            exit 1
        fi
    done
    
    sleep 0.1
done

echo -e "${GREEN}🚀 All clients are fully synced! Broadcasting START signal...${NC}"
START_MS=$(date +%s%3N)
START_SEC=$((START_MS / 1000))
touch /tmp/blast_start_signal

echo -e "${YELLOW}⏳ Delaying slightly to allow injection to start...${NC}"
sleep 0.1

echo -e "${YELLOW}⏳ Chain is processing... Waiting until idle (no new blocks for 10s)...${NC}"
LAST_BLOCK=$BLOCK_BEFORE
STAGNANT=0
ACTUAL_END_MS=$START_MS
while [ $STAGNANT -lt 100 ]; do
    sleep 0.1
    CURRENT_BLOCK=$(get_block_number "${RPCS[0]}")
    if [ "$CURRENT_BLOCK" -gt "$LAST_BLOCK" ]; then
        echo -e "  📦 Block #${CURRENT_BLOCK} ($((STAGNANT / 10))s idle reset)"
        LAST_BLOCK=$CURRENT_BLOCK
        STAGNANT=0
        ACTUAL_END_MS=$(date +%s%3N)
    else
        STAGNANT=$((STAGNANT + 1))
    fi
done
echo -e "${YELLOW}⏳ Waiting for clients to cleanly exit...${NC}"
wait
END_SEC=$(date +%s)

# ── SNAPSHOT BLOCK HEIGHT AFTER TEST ──────────────────────────────
BLOCK_AFTER=$(get_block_number "${RPCS[0]}")

# If blocks were produced during wait, the stagnant loop timed out early.
# Update ACTUAL_END_MS to the current timestamp to cover the full duration.
if [ "$BLOCK_AFTER" -gt "$LAST_BLOCK" ]; then
    ACTUAL_END_MS=$(date +%s%3N)
fi
echo -e "${GREEN}✅ Chain idle (10s no new blocks). Block height after test: ${BOLD}#${BLOCK_AFTER}${NC}"


# ── PARSE LOG ─────────────────────────────────────────────────────
# Strategy: grep ALL createBlockFromResults lines from ALL log files,
# filter by block number range, then DEDUPLICATE by taking the LAST
# occurrence of each block number (handles log rotation correctly —
# the latest App.log has the most recent run's data).
echo -e "${GREEN}✅ Generating report...${NC}"

BLOCK_START=$((BLOCK_BEFORE + 1))
BLOCK_END=$BLOCK_AFTER

# Query block data via RPC
TOTAL_BLOCKS=0
TOTAL_TXS=0
MAX_TXS=0
MAX_BLOCK=""
FIRST_TS=""
LAST_TS=""
FIRST_TS_DEC=0
LAST_TS_DEC=0

echo -e "${BOLD}╔═══════════════════════════════════════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║                                     📋 CHI TIẾT TỪNG BLOCK (RPC Query)                                ║${NC}"
echo -e "${BOLD}╠═══════════════════════════════════════════════════════════════════════════════════════════════════════╣${NC}"
printf "${BOLD}  %-8s  %7s  %10s  %12s  %12s  %12s${NC}\n" "Block" "TXs" "Timestamp" "VirtualExec" "Consensus" "RealExec"
echo    "  ────────  ───────  ──────────  ────────────  ────────────  ────────────"

for (( b=BLOCK_START; b<=BLOCK_END; b++ )); do
    BLOCK_HEX=$(printf "0x%x" "$b")
    RESPONSE=$(curl -s "http://${RPCS[0]}" -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["'$BLOCK_HEX'",false],"id":1}' 2>/dev/null)
    
    if [ -n "$RESPONSE" ] && echo "$RESPONSE" | grep -q "result"; then
        TXCNT=$(echo "$RESPONSE" | grep -oP '"transactions":\[.*?\]' | grep -o '"0x[0-9a-fA-F]*"' | wc -l)
        
        TS_HEX=$(echo "$RESPONSE" | grep -oP '"timestamp":"0x[0-9a-fA-F]+"' | grep -oP '0x[0-9a-fA-F]+')
        TS_DEC=$(printf "%d" "$TS_HEX" 2>/dev/null || echo "0")
        
        TS_SEC=$((TS_DEC / 1000))
        TS_HUMAN=$(date -d @"$TS_SEC" +"%H:%M:%S" 2>/dev/null || echo "$TS_DEC")
        
        TOTAL_BLOCKS=$((TOTAL_BLOCKS + 1))
        TOTAL_TXS=$((TOTAL_TXS + TXCNT))
        
        if [ "$TXCNT" -gt "$MAX_TXS" ]; then
            MAX_TXS=$TXCNT
            MAX_BLOCK="#$b"
        fi
        
        if [ "$TXCNT" -gt 0 ]; then
            if [ -z "$FIRST_TS" ]; then
                FIRST_TS="$TS_HUMAN"
                FIRST_TS_DEC="$TS_DEC"
            fi
            LAST_TS="$TS_HUMAN"
            LAST_TS_DEC="$TS_DEC"
        fi
        
        # Get performance stats from master node logs
        PERF_LINE=$(grep -a "\[BLOCK-PERF\] Block #${b}:" "/home/abc/chain-n/metanode/consensus/metanode/logs/node_0/go-master-stdout.log" | tail -1)
        if [ -n "$PERF_LINE" ]; then
            V_EXEC=$(echo "$PERF_LINE" | grep -oP 'VirtualExec=[^|]+' | cut -d= -f2 | xargs)
            CONSENSUS=$(echo "$PERF_LINE" | grep -oP 'Consensus=[^|]+' | cut -d= -f2 | xargs)
            R_EXEC=$(echo "$PERF_LINE" | grep -oP 'RealExec=[^|]+' | cut -d= -f2 | xargs)
            if [ "$TXCNT" -gt 0 ]; then
                echo "$V_EXEC $CONSENSUS $R_EXEC $TXCNT" >> /tmp/perf_accumulation.log
            fi
        else
            V_EXEC="N/A"
            CONSENSUS="N/A"
            R_EXEC="N/A"
        fi
        
        printf "  %-8s  %7s  %10s  %12s  %12s  %12s\n" "#$b" "$TXCNT" "$TS_HUMAN" "$V_EXEC" "$CONSENSUS" "$R_EXEC"
    fi
done

# Calculate Processing time using actual wall-clock duration of the test
PROC_MS=$((ACTUAL_END_MS - START_MS))
if [ "$PROC_MS" -le 0 ]; then
    PROC_MS=1000
fi

PROC_SEC=$(awk -v ms="$PROC_MS" 'BEGIN {printf "%.3f", ms/1000}')
echo -e "  📊 Processing Time (host wall clock duration) = ${PROC_SEC}s"

if [ "$TOTAL_TXS" -gt 0 ]; then
    SYSTEM_TPS=$(awk -v txs="$TOTAL_TXS" -v secs="$PROC_SEC" 'BEGIN {printf "%.0f", txs/secs}')
else
    SYSTEM_TPS=0
fi

# Parse the performance accumulation logs if they exist
TOTAL_V_MS=0; TOTAL_C_MS=0; TOTAL_R_MS=0; AVG_V_MS=0; AVG_C_MS=0; AVG_R_MS=0; REAL_TPS=0; GO_TPS=0; E2E_TPS=0; COUNT_BLOCKS=0
if [ -f /tmp/perf_accumulation.log ]; then
    read TOTAL_V_MS TOTAL_C_MS TOTAL_R_MS AVG_V_MS AVG_C_MS AVG_R_MS REAL_TPS GO_TPS E2E_TPS COUNT_BLOCKS < <(awk -v wall_secs="$PROC_SEC" '
    function to_ms(str) {
        if (str ~ /µs/) { sub(/µs/, "", str); return str / 1000; }
        if (str ~ /us/) { sub(/us/, "", str); return str / 1000; }
        if (str ~ /ms/) { sub(/ms/, "", str); return str; }
        if (str ~ /ns/) { sub(/ns/, "", str); return str / 1000000; }
        if (str ~ /s/)  { sub(/s/, "", str);  return str * 1000; }
        return str;
    }
    BEGIN {
        sum_v = 0; sum_c = 0; sum_r = 0; total_txs = 0; count = 0;
    }
    {
        v = to_ms($1);
        c = to_ms($2);
        r = to_ms($3);
        tx = $4;
        
        sum_v += v;
        sum_c += c;
        sum_r += r;
        total_txs += tx;
        count++;
    }
    END {
        if (count > 0) {
            avg_v = sum_v / count;
            avg_c = sum_c / count;
            avg_r = sum_r / count;
            
            real_tps = (sum_r > 0) ? (total_txs / (sum_r / 1000)) : 0;
            go_tps = ((sum_v + sum_r) > 0) ? (total_txs / ((sum_v + sum_r) / 1000)) : 0;
            e2e_tps = (wall_secs > 0) ? (total_txs / wall_secs) : 0;
            
            printf "%.2f %.2f %.2f %.2f %.2f %.2f %.0f %.0f %.0f %d", 
                sum_v, sum_c, sum_r, avg_v, avg_c, avg_r, real_tps, go_tps, e2e_tps, count;
        } else {
            printf "0 0 0 0 0 0 0 0 0 0";
        }
    }' /tmp/perf_accumulation.log)
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

if [ "$COUNT_BLOCKS" -gt 0 ]; then
    echo -e "  ⏱️  Chi tiết thời gian xử lý tích lũy (qua ${COUNT_BLOCKS} blocks):"
    echo -e "    - Bước Chạy Giả (Virtual Exec):  ${BOLD}$(printf "%.2f" $TOTAL_V_MS) ms${NC} (Avg: $(printf "%.2f" $AVG_V_MS) ms/block)"
    echo -e "    - Bước Đồng Thuận (Consensus):   ${BOLD}$(printf "%.2f" $TOTAL_C_MS) ms${NC} (Avg: $(printf "%.2f" $AVG_C_MS) ms/block)"
    echo -e "    - Bước Thực Thi (Real Exec):     ${BOLD}$(printf "%.2f" $TOTAL_R_MS) ms${NC} (Avg: $(printf "%.2f" $AVG_R_MS) ms/block)"
    echo ""
    echo -e "  🚀 TPS thực sự theo từng giai đoạn:"
    echo -e "    - ${GREEN}${BOLD}TPS Thực Thi (Real Exec TPS):       ~${REAL_TPS} tx/s${NC}"
    echo -e "    - ${CYAN}${BOLD}TPS Xử Lý Go (Virtual + Real):      ~${GO_TPS} tx/s${NC}"
    echo -e "    - ${YELLOW}${BOLD}TPS Toàn Trình (Consensus Included): ~${E2E_TPS} tx/s${NC}"
    echo ""
fi

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
HASH_OUT=$(/tmp/block_hash_checker \
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
