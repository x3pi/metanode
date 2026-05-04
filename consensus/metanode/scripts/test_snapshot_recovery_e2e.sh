#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
# Automated Snapshot Recovery E2E Test Suite
# Automates: Wiping a node -> Restoring from Snapshot -> Verifying Data Parity (State Root) -> Verifying Consensus Liveness
# ═══════════════════════════════════════════════════════════════════

set -uo pipefail

# ─── Configuration & Paths ──────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RUST_DIR="$BASE_DIR/consensus/metanode"
GO_DIR="$BASE_DIR/execution/cmd/simple_chain"
LOG_BASE="$RUST_DIR/logs"
ORCHESTRATOR="$SCRIPT_DIR/mtn-orchestrator.sh"
TX_SENDER_DIR="$BASE_DIR/execution/cmd/tool/tx_sender"

SRC_NODE=${1:-0}
DST_NODE=${2:-1}
SRC_IP=${3:-127.0.0.1}

SRC_RPC_PORT=$((8757 + (SRC_NODE == 0 ? 0 : SRC_NODE == 1 ? 1990 : SRC_NODE == 2 ? 1992 : SRC_NODE == 3 ? 1993 : 1991))) # Map for Node 0-4
DST_RPC_PORT=$((8757 + (DST_NODE == 0 ? 0 : DST_NODE == 1 ? 1990 : DST_NODE == 2 ? 1992 : DST_NODE == 3 ? 1993 : 1991)))

SNAPSHOT_PORT=$((8700 + SRC_NODE))
SNAPSHOT_URL="http://${SRC_IP}:${SNAPSHOT_PORT}"
LEVELDB_DIRS="account_state blocks receipts transaction_state mapping smart_contract_code smart_contract_storage stake_db trie_database backup_device_key_storage xapian xapian_node"

TX_PUMP_PID=""

# ─── Colors ─────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# ─── Utilities ──────────────────────────────────────────────────────────
log() { echo -e "$1"; }

get_block_number() {
    local port=$1
    local res
    res=$(curl -s --max-time 3 -X POST "http://127.0.0.1:$port" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null)
    if [ -z "$res" ]; then echo "-1"; return; fi
    local hex=$(echo "$res" | grep -o '"result":"[^"]*"' | cut -d'"' -f4)
    if [ -z "$hex" ] || [ "$hex" = "null" ]; then echo "-1"; return; fi
    printf "%d" "$hex" 2>/dev/null || echo "-1"
}

get_peer_info() {
    local port=$1
    curl -s --max-time 3 -X POST "http://127.0.0.1:$port" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"peer_info","params":[],"id":1}' 2>/dev/null
}

start_tx_pump() {
    if [ ! -d "$TX_SENDER_DIR" ]; then return; fi
    if [ ! -x "$TX_SENDER_DIR/tx_sender" ]; then (cd "$TX_SENDER_DIR" && go build -o tx_sender .) || true; fi
    "$TX_SENDER_DIR/tx_sender" --config "$TX_SENDER_DIR/config.json" --data "$TX_SENDER_DIR/data.json" --loop --node "127.0.0.1:4201" > /dev/null 2>&1 &
    TX_PUMP_PID=$!
    log "  🔫 TX Spammer started (PID: $TX_PUMP_PID) to force block production."
}

stop_tx_pump() {
    if [ -n "$TX_PUMP_PID" ] && kill -0 "$TX_PUMP_PID" 2>/dev/null; then
        kill -TERM "$TX_PUMP_PID" 2>/dev/null || true
        wait "$TX_PUMP_PID" 2>/dev/null || true
        log "  🛑 TX Spammer stopped."
        TX_PUMP_PID=""
    fi
    pkill -f "tx_sender" 2>/dev/null || true
}

trap 'stop_tx_pump; exit' INT TERM EXIT

log "${BOLD}========================================================================${NC}"
log "${BOLD}  🚀 METANODE SNAPSHOT RECOVERY & INTEGRITY AUTOMATED TEST SUITE${NC}"
log "${BOLD}========================================================================${NC}"

# ═══════════════════════════════════════════════════════════════════
# Phase 1: Preparation & Snapshot Verification
# ═══════════════════════════════════════════════════════════════════
log "\n${CYAN}[Phase 1] Preparation - Checking Source Node $SRC_NODE${NC}"

SNAPSHOTS_JSON=$(curl -sf "${SNAPSHOT_URL}/api/snapshots" 2>/dev/null || echo "null")
if [ "$SNAPSHOTS_JSON" = "null" ] || [ -z "$SNAPSHOTS_JSON" ]; then
    log "${RED}❌ Node $SRC_NODE snapshot API is unresponsive. Is it running?${NC}"
    exit 1
fi

SNAPSHOT_COUNT=$(echo "$SNAPSHOTS_JSON" | jq 'length' 2>/dev/null || echo "0")
if [ "$SNAPSHOT_COUNT" -eq 0 ]; then
    log "${YELLOW}⚠️ Node $SRC_NODE has no snapshots yet. Running TX Spammer to force an epoch boundary...${NC}"
    start_tx_pump
    for i in {1..12}; do
        sleep 5
        SNAPSHOTS_JSON=$(curl -sf "${SNAPSHOT_URL}/api/snapshots" 2>/dev/null || echo "null")
        SNAPSHOT_COUNT=$(echo "$SNAPSHOTS_JSON" | jq 'length' 2>/dev/null || echo "0")
        if [ "$SNAPSHOT_COUNT" -gt 0 ]; then break; fi
    done
    stop_tx_pump
    if [ "$SNAPSHOT_COUNT" -eq 0 ]; then
        log "${RED}❌ Still no snapshots created. Aborting.${NC}"
        exit 1
    fi
fi

LATEST=$(echo "$SNAPSHOTS_JSON" | jq -r '.[-1]')
SNAP_NAME=$(echo "$LATEST" | jq -r '.snapshot_name')
log "${GREEN}✅ Found $SNAPSHOT_COUNT snapshot(s) on Node $SRC_NODE. Target snapshot: $SNAP_NAME${NC}"

# ═══════════════════════════════════════════════════════════════════
# Phase 2: Destruction & Restoration
# ═══════════════════════════════════════════════════════════════════
log "\n${CYAN}[Phase 2] Destruction & Restoration - Target Node $DST_NODE${NC}"

log "  🔴 Stopping Node $DST_NODE..."
"$ORCHESTRATOR" stop-node "$DST_NODE" > /dev/null 2>&1 || true

DST="$GO_DIR/sample/node$DST_NODE"
log "  🗑️ Wiping Node $DST_NODE state..."
rm -rf "$DST/data" "$DST/data-write" "$DST/back_up" "$DST/back_up_write"
rm -rf "$LOG_BASE/node_$DST_NODE" "$RUST_DIR/config/storage/node_$DST_NODE"
rm -rf "$GO_DIR/sample/node${DST_NODE}/data/data/rust_consensus" 2>/dev/null || true

log "  📥 Downloading Snapshot from Node $SRC_NODE..."
DOWNLOAD_URL="${SNAPSHOT_URL}/files/${SNAP_NAME}/"
DOWNLOAD_DIR="/tmp/snapshot_download_node${DST_NODE}"
rm -rf "$DOWNLOAD_DIR"
mkdir -p "$DOWNLOAD_DIR"
wget -q -c -r -np -nH --cut-dirs=2 -P "$DOWNLOAD_DIR" --reject="index.html*" "$DOWNLOAD_URL"
if [ ! -d "$DOWNLOAD_DIR" ] || [ -z "$(ls -A "$DOWNLOAD_DIR")" ]; then
    log "${RED}❌ Download failed!${NC}"
    exit 1
fi

log "  📂 Restoring state..."
mkdir -p "$DST/data/data" "$DST/back_up" "$DST/data-write" "$DST/back_up_write"
for dir_name in $LEVELDB_DIRS; do
    if [ -d "$DOWNLOAD_DIR/$dir_name" ]; then
        mv "$DOWNLOAD_DIR/$dir_name" "$DST/data/data/$dir_name"
    fi
done

if [ -d "$DOWNLOAD_DIR/back_up" ]; then cp -r "$DOWNLOAD_DIR/back_up/"* "$DST/back_up/" 2>/dev/null || true; fi
if [ -d "$DOWNLOAD_DIR/data-write" ]; then cp -r "$DOWNLOAD_DIR/data-write/"* "$DST/data-write/" 2>/dev/null || true; fi
if [ -d "$DOWNLOAD_DIR/back_up_write" ]; then cp -r "$DOWNLOAD_DIR/back_up_write/"* "$DST/back_up_write/" 2>/dev/null || true; fi
find "$DST" -name "LOCK" -delete 2>/dev/null || true
mkdir -p "$LOG_BASE/node_$DST_NODE" "$RUST_DIR/config/storage/node_$DST_NODE"
rm -rf "$DOWNLOAD_DIR"
log "${GREEN}✅ Data successfully restored onto Node $DST_NODE!${NC}"

# ═══════════════════════════════════════════════════════════════════
# Phase 3: Restart & Catch-up
# ═══════════════════════════════════════════════════════════════════
log "\n${CYAN}[Phase 3] Restarting Node $DST_NODE & Awaiting Sync${NC}"

"$ORCHESTRATOR" start-node "$DST_NODE" > /dev/null 2>&1 || true

log "  ⏳ Waiting for Node $DST_NODE to boot up and catch up (Timeout: 60s)..."
catchup_timeout=60
elapsed=0
sync_success=false

while [ $elapsed -lt $catchup_timeout ]; do
    DST_BLOCK=$(get_block_number "$DST_RPC_PORT")
    SRC_BLOCK=$(get_block_number "$SRC_RPC_PORT")
    
    if [ "$DST_BLOCK" != "-1" ] && [ "$SRC_BLOCK" != "-1" ]; then
        if [ "$DST_BLOCK" -ge "$SRC_BLOCK" ] || [ $((SRC_BLOCK - DST_BLOCK)) -le 5 ]; then
            log "${GREEN}✅ Node $DST_NODE has caught up to the network! (Block: $DST_BLOCK, Target: ~$SRC_BLOCK)${NC}"
            sync_success=true
            break
        fi
    fi
    
    sleep 3
    elapsed=$((elapsed + 3))
done

if [ "$sync_success" = false ]; then
    log "${RED}❌ Timeout waiting for Node $DST_NODE to catch up! (Current: $DST_BLOCK, Target: $SRC_BLOCK)${NC}"
    exit 1
fi

sleep 10 # Let the system settle and allow health checks to run

# ═══════════════════════════════════════════════════════════════════
# Phase 4: Integrity & Fork Verification
# ═══════════════════════════════════════════════════════════════════
log "\n${CYAN}[Phase 4] Post-Recovery Integrity & Parity Audit${NC}"

SRC_INFO=$(get_peer_info "$SRC_RPC_PORT")
DST_INFO=$(get_peer_info "$DST_RPC_PORT")

if [ -z "$SRC_INFO" ] || [ -z "$DST_INFO" ] || [[ "$SRC_INFO" == *"error"* ]] || [[ "$DST_INFO" == *"error"* ]]; then
    log "${RED}❌ Failed to query peer_info from nodes.${NC}"
    exit 1
fi

SRC_GEI=$(echo "$SRC_INFO" | jq -r '.result.global_exec_index')
DST_GEI=$(echo "$DST_INFO" | jq -r '.result.global_exec_index')

SRC_STATE=$(echo "$SRC_INFO" | jq -r '.result.state_root')
DST_STATE=$(echo "$DST_INFO" | jq -r '.result.state_root')

log "  📊 ${BOLD}State Metrics Comparison:${NC}"
log "    Node $SRC_NODE (Source) -> GEI: $SRC_GEI | State Root: ${SRC_STATE:0:18}..."
log "    Node $DST_NODE (Target) -> GEI: $DST_GEI | State Root: ${DST_STATE:0:18}..."

if [ "$SRC_STATE" != "$DST_STATE" ]; then
    log "${RED}🚨 FATAL INTEGRITY FAILURE: State Roots do not match! Node $DST_NODE has forked!${NC}"
    exit 1
else
    log "${GREEN}✅ INTEGRITY PASSED: State Roots match flawlessly (Bit-Perfect Parity!).${NC}"
fi

# ═══════════════════════════════════════════════════════════════════
# Phase 5: Consensus Liveness Check
# ═══════════════════════════════════════════════════════════════════
log "\n${CYAN}[Phase 5] Consensus Liveness & Block Production Validation${NC}"

START_BLOCK=$(get_block_number "$DST_RPC_PORT")
log "  📈 Initial Block Height on Node $DST_NODE: $START_BLOCK"

start_tx_pump
log "  ⏳ Pumping transactions for 20 seconds to force new blocks..."
sleep 20
stop_tx_pump

END_BLOCK=$(get_block_number "$DST_RPC_PORT")
log "  📈 Final Block Height on Node $DST_NODE: $END_BLOCK"

if [ "$END_BLOCK" -gt "$START_BLOCK" ]; then
    diff=$((END_BLOCK - START_BLOCK))
    log "${GREEN}✅ LIVENESS PASSED: Node $DST_NODE successfully committed $diff new blocks after recovery!${NC}"
else
    log "${RED}❌ LIVENESS FAILURE: Node $DST_NODE failed to increase its block height. It might be stalled!${NC}"
    exit 1
fi

log "\n${BOLD}🎉 AUTOMATED RECOVERY TEST COMPLETED SUCCESSFULLY! 🎉${NC}"
exit 0
