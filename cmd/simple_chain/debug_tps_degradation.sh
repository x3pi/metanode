#!/bin/bash
# debug_tps_degradation.sh — TPS Degradation Root Cause Analyzer
#
# Mục tiêu: Phân tích tại sao TPS giảm dần theo thời gian trong pipeline:
#   Client → Go Sub (TX pool) → Go Master → Rust consensus → Go Master (block) → Go Sub
#
# Port conventions (set by deploy_cluster.sh):
#   Go Master node 0: pprof=6060, node 1: 6061, ...
#   Go Sub    node 0: pprof=6070, node 1: 6071, ...
#
# Usage:
#   bash debug_tps_degradation.sh [master_pprof_port] [sub_pprof_port] [interval_sec] [times]
#   bash debug_tps_degradation.sh 6060 6070 30 10
# bash debug_tps_degradation.sh 6060 6070 30 1
# go tool pprof -http=:8083 -base=./tps_debug/20260406_023128/snap_1/master_heap_1.prof ./tps_debug/20260406_023128/snap_8/master_heap_8.prof
MASTER_PORT="${1:-6060}"
SUB_PORT="${2:-6070}"
INTERVAL="${3:-30}"
TIMES="${4:-8}"

MASTER_URL="http://127.0.0.1:${MASTER_PORT}/debug/pprof"
SUB_URL="http://127.0.0.1:${SUB_PORT}/debug/pprof"
OUT_DIR="./tps_debug/$(date +%Y%m%d_%H%M%S)"

mkdir -p "$OUT_DIR"

# ── ANSI Colors ──────────────────────────────────────────────────────────────
RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'
CYAN='\033[0;36m'; BLUE='\033[0;34m'; NC='\033[0m'

echo -e "${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  🔍 TPS DEGRADATION DEBUGGER                                ║${NC}"
echo -e "${BLUE}║  Pipeline: Client -> Sub -> Master -> Rust -> Master -> Sub  ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  Master pprof : ${CYAN}${MASTER_URL}${NC}"
echo -e "  Sub    pprof : ${CYAN}${SUB_URL}${NC}"
echo -e "  Snapshots    : ${TIMES} lần, mỗi ${INTERVAL}s"
echo -e "  Output dir   : ${OUT_DIR}"
echo ""

# ── Check pprof servers ───────────────────────────────────────────────────────
MASTER_ALIVE=true
SUB_ALIVE=true

if ! curl -sf "$MASTER_URL/" > /dev/null 2>&1; then
    echo -e "${RED}❌ Go Master pprof không có tại ${MASTER_URL}${NC}"
    echo -e "   → Restart cluster với --pprof-addr=localhost:${MASTER_PORT}"
    MASTER_ALIVE=false
fi

if ! curl -sf "$SUB_URL/" > /dev/null 2>&1; then
    echo -e "${YELLOW}⚠️  Go Sub pprof không có tại ${SUB_URL}${NC}"
    echo -e "   → Go Sub chưa được cấu hình pprof. Dùng deploy_cluster.sh mới."
    SUB_ALIVE=false
fi

if ! $MASTER_ALIVE && ! $SUB_ALIVE; then
    echo -e "${RED}❌ Không có pprof server nào hoạt động. Thoát.${NC}"
    exit 1
fi

$MASTER_ALIVE && echo -e "${GREEN}✅ Go Master pprof online${NC}"
$SUB_ALIVE   && echo -e "${GREEN}✅ Go Sub pprof online${NC}"

# ── Extract key metric from logs ──────────────────────────────────────────────
# Đọc [PERF] log lines từ master log để track pipeline timing
extract_perf_from_log() {
    local log_file="$1"
    local snap_dir="$2"
    local snap_idx="$3"
    
    if [ ! -f "$log_file" ]; then
        return
    fi
    
    echo "=== PERF LOG SNAPSHOT #${snap_idx} ($(date +%H:%M:%S)) ===" > "${snap_dir}/perf_log_${snap_idx}.txt"
    
    # Extract key performance lines từ log (last 200 lines)
    tail -200 "$log_file" | grep -E '\[PERF\]|\[TPS\]|COMMIT_WORKER|BROADCAST_WORKER|ProcessTransactions|createBlockFromResults' \
        >> "${snap_dir}/perf_log_${snap_idx}.txt" 2>/dev/null || true
    
    # Đếm blocks committed trong 30s gần nhất
    local recent_blocks
    recent_blocks=$(tail -500 "$log_file" | grep -c 'COMMIT_WORKER' 2>/dev/null || echo 0)
    echo "  Recent COMMIT_WORKER events (last 500 lines): $recent_blocks" >> "${snap_dir}/perf_log_${snap_idx}.txt"
    
    # Extract broadcastChannel len warnings (signals backup lag)
    local broadcast_warns
    broadcast_warns=$(tail -500 "$log_file" | grep -c 'broadcastChannel' 2>/dev/null || echo 0)
    echo "  broadcastChannel warnings (last 500 lines): $broadcast_warns" >> "${snap_dir}/perf_log_${snap_idx}.txt"
    
    # Extract persistChannel full warnings (signals DB lag)
    local persist_warns
    persist_warns=$(tail -500 "$log_file" | grep -c 'persistChannel full' 2>/dev/null || echo 0)
    echo "  persistChannel full (last 500 lines): $persist_warns" >> "${snap_dir}/perf_log_${snap_idx}.txt"
    
    # Extract SendBytes timeout warnings (signals Sub lag)
    local send_warns
    send_warns=$(tail -500 "$log_file" | grep -c 'timeout sending BlockData' 2>/dev/null || echo 0)
    echo "  sendBlockData timeouts (last 500 lines): $send_warns" >> "${snap_dir}/perf_log_${snap_idx}.txt"
}

# ── Take snapshot ─────────────────────────────────────────────────────────────
take_snapshot() {
    local idx=$1
    local ts
    ts=$(date +%H:%M:%S)
    local snap_dir="${OUT_DIR}/snap_${idx}"
    mkdir -p "$snap_dir"
    
    echo ""
    echo -e "═══════════════════════════════════════════════════════"
    echo -e "📸 ${CYAN}[${ts}] Snapshot #${idx}${NC}"
    echo -e "═══════════════════════════════════════════════════════"
    
    # ── Go MASTER profiling ───────────────────────────────────────
    if $MASTER_ALIVE; then
        echo -e "  ${BLUE}[MASTER]${NC}"
        
        # Heap profile
        if curl -sf "${MASTER_URL}/heap" > "${snap_dir}/master_heap_${idx}.prof" 2>/dev/null; then
            echo -e "   ✅ master heap profile"
        fi
        
        # Goroutine dump
        curl -sf "${MASTER_URL}/goroutine?debug=2" > "${snap_dir}/master_goroutines_${idx}.txt" 2>/dev/null || true
        local master_goroutines
        master_goroutines=$(grep -c "^goroutine " "${snap_dir}/master_goroutines_${idx}.txt" 2>/dev/null || echo 0)
        echo -e "   📊 Goroutines: ${master_goroutines}"
        
        # Block profile (detects mutex/channel contention)
        curl -sf "${MASTER_URL}/block?debug=1" > "${snap_dir}/master_block_${idx}.txt" 2>/dev/null || true
        echo -e "   ✅ master block profile (mutex contention)"
        
        # Mutex profile
        curl -sf "${MASTER_URL}/mutex?debug=1" > "${snap_dir}/master_mutex_${idx}.txt" 2>/dev/null || true
        echo -e "   ✅ master mutex profile"
        
        # Allocs
        curl -sf "${MASTER_URL}/allocs?debug=1" > "${snap_dir}/master_allocs_${idx}.txt" 2>/dev/null || true
        echo -e "   ✅ master allocs"
        
        # Channel contention check (goroutines blocked on channel ops)
        local chan_blocked
        chan_blocked=$(grep -c "chan send\|chan receive\|select" "${snap_dir}/master_goroutines_${idx}.txt" 2>/dev/null || echo 0)
        echo -e "   📊 Channel-blocked goroutines: ${chan_blocked}"
        
        # Detect broadcast/commit/persist worker stalls
        for worker in "broadcastWorker" "commitWorker" "persistWorker"; do
            local count
            count=$(grep -c "$worker" "${snap_dir}/master_goroutines_${idx}.txt" 2>/dev/null || echo 0)
            echo -e "   📊 ${worker} goroutines: ${count}"
        done
    fi
    
    # ── Go SUB profiling ──────────────────────────────────────────
    if $SUB_ALIVE; then
        echo -e "  ${BLUE}[SUB]${NC}"
        
        # Heap profile
        if curl -sf "${SUB_URL}/heap" > "${snap_dir}/sub_heap_${idx}.prof" 2>/dev/null; then
            echo -e "   ✅ sub heap profile"
        fi
        
        # Goroutine dump
        curl -sf "${SUB_URL}/goroutine?debug=2" > "${snap_dir}/sub_goroutines_${idx}.txt" 2>/dev/null || true
        local sub_goroutines
        sub_goroutines=$(grep -c "^goroutine " "${snap_dir}/sub_goroutines_${idx}.txt" 2>/dev/null || echo 0)
        echo -e "   📊 Sub goroutines: ${sub_goroutines}"
        
        # Sub specific: check ProcessTransactionsFromSub and NetworkSync goroutines
        local sub_tx_workers
        sub_tx_workers=$(grep -c "ProcessTransactionsFromSub\|NumSubTxWorkers\|injectionQueue" "${snap_dir}/sub_goroutines_${idx}.txt" 2>/dev/null || echo 0)
        echo -e "   📊 Sub TX injection workers: ${sub_tx_workers}"
        
        curl -sf "${SUB_URL}/allocs?debug=1" > "${snap_dir}/sub_allocs_${idx}.txt" 2>/dev/null || true
    fi
    
    # ── Tổng hợp snapshot ─────────────────────────────────────────
    {
        echo "=== SNAPSHOT #${idx} === $(date)"
        echo ""
        echo "--- MASTER GOROUTINES: ${master_goroutines:-N/A}"
        echo "--- SUB GOROUTINES:    ${sub_goroutines:-N/A}"
        echo "--- CHANNEL BLOCKED:   ${chan_blocked:-N/A}"
    } >> "${OUT_DIR}/summary.txt"
    
    echo ""
}

# ── Main loop ─────────────────────────────────────────────────────────────────
echo "═══════════════════════════════════════════════════════"
echo -e "${YELLOW}⚡ Bắt đầu thu thập... Hãy chạy TPS test ngay bây giờ!${NC}"
echo -e "${YELLOW}👉 NHẤN ENTER ĐỂ KẾT THÚC SỚM VÀ PHÂN TÍCH NGAY${NC}"
echo "═══════════════════════════════════════════════════════"

ACTUAL_TIMES=0
for i in $(seq 1 "$TIMES"); do
    take_snapshot "$i"
    ACTUAL_TIMES=$i
    if [ "$i" -lt "$TIMES" ]; then
        echo -e "   ⏳ Waiting ${INTERVAL}s for next snapshot... (Nhấn ENTER để kết thúc sớm)"
        # Chờ đợi bằng lệnh read có timeout, nếu nhấn phím Enter sẽ thoát loop sớm
        if read -r -t "$INTERVAL"; then
            echo -e "\n${YELLOW}⏹️ Đã nhận tín hiệu (ENTER). Dừng thu thập và chuyển sang phân tích...${NC}"
            break
        fi
    fi
done

# Cập nhật số TIMES thực tế để các bước phân tích (auto-analysis) tính toán chuẩn xác
TIMES=$ACTUAL_TIMES

# ── Auto Analysis ─────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}📈 AUTO-ANALYSIS: GOROUTINE GROWTH TREND${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"

echo ""
echo "Master goroutines over time:"
for i in $(seq 1 "$TIMES"); do
    f="${OUT_DIR}/snap_${i}/master_goroutines_${i}.txt"
    if [ -f "$f" ]; then
        cnt=$(grep -c "^goroutine " "$f" 2>/dev/null || echo 0)
        bar=$(printf '█%.0s' $(seq 1 $((cnt / 10))))
        printf "  Snap #%-2d: %4d goroutines  %s\n" "$i" "$cnt" "$bar"
    fi
done

if $SUB_ALIVE; then
    echo ""
    echo "Sub goroutines over time:"
    for i in $(seq 1 "$TIMES"); do
        f="${OUT_DIR}/snap_${i}/sub_goroutines_${i}.txt"
        if [ -f "$f" ]; then
            cnt=$(grep -c "^goroutine " "$f" 2>/dev/null || echo 0)
            bar=$(printf '█%.0s' $(seq 1 $((cnt / 10))))
            printf "  Snap #%-2d: %4d goroutines  %s\n" "$i" "$cnt" "$bar"
        fi
    done
fi

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}🔍 TOP BOTTLENECK CANDIDATES IN LAST SNAPSHOT${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"

LAST_SNAP="${OUT_DIR}/snap_${TIMES}"

# Check 1: Channel backpressure (commit/broadcast/persist full)
echo ""
echo -e "${YELLOW}1. Channel Backpressure (blocks broadcastChannel/persistChannel):${NC}"
if [ -f "${LAST_SNAP}/master_goroutines_${TIMES}.txt" ]; then
    grep -A5 "broadcastChannel\|persistChannel\|commitChannel" "${LAST_SNAP}/master_goroutines_${TIMES}.txt" 2>/dev/null | head -30 || echo "  (none found)"
fi

# Check 2: Database write lag (LevelDB/PebbleDB contention)
echo ""
echo -e "${YELLOW}2. Database Write Goroutines (LevelDB/PebbleDB lag):${NC}"
if [ -f "${LAST_SNAP}/master_goroutines_${TIMES}.txt" ]; then
    grep -c "pebble\|leveldb\|BatchPut\|PersistAsync" "${LAST_SNAP}/master_goroutines_${TIMES}.txt" 2>/dev/null || echo "  0 goroutines"
fi

# Check 3: Sub node receive lag (backup serialization)
echo ""
echo -e "${YELLOW}3. Block Broadcast to Sub Lag (SendBytes blocked):${NC}"
if [ -f "${LAST_SNAP}/master_goroutines_${TIMES}.txt" ]; then
    grep -c "SendBytes\|broadcastBlockToNetwork\|sendData" "${LAST_SNAP}/master_goroutines_${TIMES}.txt" 2>/dev/null || echo "  0 goroutines"
fi

# Check 4: Heap growth
echo ""
echo -e "${YELLOW}4. Master Heap Growth:${NC}"
FIRST_ALLOC="${OUT_DIR}/snap_1/master_allocs_1.txt"
LAST_ALLOC="${LAST_SNAP}/master_allocs_${TIMES}.txt"
if [ -f "$FIRST_ALLOC" ] && [ -f "$LAST_ALLOC" ]; then
    f1=$(wc -c < "$FIRST_ALLOC" 2>/dev/null || echo 0)
    fN=$(wc -c < "$LAST_ALLOC" 2>/dev/null || echo 0)
    echo "  First snapshot allocs file: ${f1} bytes"
    echo "  Last  snapshot allocs file: ${fN} bytes"
    if [ "$f1" -gt 0 ]; then
        growth=$(( (fN - f1) * 100 / f1 ))
        echo "  Growth: ${growth}%"
    fi
fi

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}📊 HOW TO ANALYZE VISUALLY${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════${NC}"
echo ""
echo "# A. So sánh heap Master: snap 1 vs snap cuối (tìm memory leak)"
echo "   go tool pprof -http=:8081 ${OUT_DIR}/snap_1/master_heap_1.prof"
echo "   go tool pprof -http=:8082 ${OUT_DIR}/snap_${TIMES}/master_heap_${TIMES}.prof"
echo ""
echo "# B. Diff heap Master (tìm cái GÌ đã tăng):"
echo "   go tool pprof -http=:8083 -base=${OUT_DIR}/snap_1/master_heap_1.prof ${OUT_DIR}/snap_${TIMES}/master_heap_${TIMES}.prof"
echo ""
echo "# C. Phân tích contention (mutex/channel blocking):"
echo "   go tool pprof -http=:8084 ${OUT_DIR}/snap_${TIMES}/master_block_${TIMES}.prof"
echo ""
echo "# D. Nếu có Sub pprof, so sánh Sub heap:"
if $SUB_ALIVE; then
    echo "   go tool pprof -http=:8085 -base=${OUT_DIR}/snap_1/sub_heap_1.prof ${OUT_DIR}/snap_${TIMES}/sub_heap_${TIMES}.prof"
fi
echo ""
echo -e "${GREEN}✅ Xong! Files: ${OUT_DIR}${NC}"
echo ""

# ── Key Diagnostic Summary ────────────────────────────────────────────────────
echo -e "${CYAN}══════════════════════════════════════════════════════════${NC}"
echo -e "${CYAN}💡 COMMON ROOT CAUSES OF TPS DEGRADATION:${NC}"
echo -e "${CYAN}══════════════════════════════════════════════════════════${NC}"
cat << 'EOF'

1. 📦 broadcastChannel FULL
   → broadcastWorker chậm hơn commitWorker
   → Root cause: prepareBackupData() (serialize state) mất nhiều thời gian
   → DB state tích lũy theo block → serialize càng lâu hơn
   → Fix: Enable ZSTD, giảm số fields backup, tăng buffer

2. 💾 persistChannel FULL
   → PersistAsync() (LevelDB write) chậm hơn block creation
   → Root cause: LevelDB compaction chạy nền, chặn write
   → Fix: PebbleDB compaction tuning, giới hạn L0 files

3. 🌐 SendBytes timeout → Sub node lag
   → Master gửi backup data cho Sub nhưng Sub chưa đọc xong block trước
   → Root cause: Sub's subNodeBlockBuffer thêm blocks nhanh hơn nó apply
   → Fix: Tăng broadcastChannel buffer, rate-limit Sub sync

4. 🧠 Memory leak → GC pressure
   → Go GC chạy nhiều hơn khi heap tăng → STW pause → TPS drop
   → Root cause: txHashConnectionMap, pendingReceipts, trieCache không FreeAll
   → Fix: Check cleanupPendingReceipts, cleanupTxHashConnectionMap intervals

5. 🔁 Goroutine leak → Context switch overhead
   → Goroutine count tăng theo thời gian
   → Root cause: BroadCastReceipts spawns goroutine per TX per block
   → Fix: Bounded worker pool (đã fix G-H2)

EOF
