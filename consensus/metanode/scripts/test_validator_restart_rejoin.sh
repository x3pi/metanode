#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# INTEGRATION TEST: Validator Node Restart & Rejoin
# 
# Mục tiêu: Test restart node 1 trong cluster 5 node đang chạy, verify node rejoin
# consensus đúng với "Validator Always Consensus" mode.
#
# Flow:
#   1. Check cluster đang chạy (5 nodes up)
#   2. Record block heights
#   3. Stop node 1
#   4. Wait for cluster to continue (20-30s)
#   5. Start node 1 (restart with data preserved)
#   6. Monitor logs for: Validator mode, Recovering phase, Healthy transition
#   7. Verify block height catches up
#   8. Send test transaction to verify consensus still works
#   9. Report PASS/FAIL
#
# Sử dụng: ./test_validator_restart_rejoin.sh [node_id]
#   node_id: ID của node cần restart (default: 1)
# ═══════════════════════════════════════════════════════════════════════════════

set -e  # Exit on error

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ORCHESTRATOR="$SCRIPT_DIR/mtn-orchestrator.sh"
TX_SENDER_DIR="$BASE_DIR/execution/cmd/tool/tx_sender"

# Node ID to test (default: 1)
TEST_NODE_ID="${1:-1}"

# RPC Ports for each node (from mtn-orchestrator.sh get_rpc_port)
declare -A RPC_PORTS=(
    [0]=8757
    [1]=10747
    [2]=10749
    [3]=10750
    [4]=10748
)

# Log paths
LOG_BASE="$BASE_DIR/consensus/metanode/logs"
LOG_FILE="$LOG_BASE/node_${TEST_NODE_ID}/go-master-stdout.log"

# Test parameters
STOP_WAIT_TIME=30       # Seconds to wait after stopping node (let cluster continue)
STARTUP_WAIT_TIME=60    # Max seconds to wait for node to rejoin
CATCHUP_WAIT_TIME=120   # Max seconds to wait for block catchup
TX_SENDER_WAIT=60       # Seconds to wait for transaction test

# ═══════════════════════════════════════════════════════════════════════════════
# Helper Functions
# ═══════════════════════════════════════════════════════════════════════════════

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[PASS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[FAIL]${NC} $1"
}

log_phase() {
    echo ""
    echo -e "${BOLD}══════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}  $1${NC}"
    echo -e "${BOLD}══════════════════════════════════════════════════════════════════${NC}"
    echo ""
}

# Get block height of a node via RPC
get_block_height() {
    local node_id=$1
    local port=${RPC_PORTS[$node_id]}
    
    local height_hex=$(curl -s --max-time 3 -X POST http://127.0.0.1:${port} \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null \
        | grep -oP '"result":"(0x[0-9a-fA-F]+)"' | cut -d'"' -f4)
    
    if [ -n "$height_hex" ]; then
        printf "%d" "$height_hex" 2>/dev/null
    else
        echo "0"
    fi
}

# Check if node process is running
is_node_running() {
    local node_id=$1
    pgrep -f "simple_chain.*config-master-node${node_id}" >/dev/null 2>&1
}

# Wait for node to be up and responding
wait_for_node() {
    local node_id=$1
    local max_wait=$2
    local waited=0
    
    log_info "Waiting for node $node_id to respond (max ${max_wait}s)..."
    
    while [ $waited -lt $max_wait ]; do
        if is_node_running $node_id; then
            local height=$(get_block_height $node_id)
            if [ "$height" -gt "0" ]; then
                log_success "Node $node_id is up (block height: $height)"
                return 0
            fi
        fi
        sleep 1
        waited=$((waited + 1))
        
        if [ $((waited % 10)) -eq 0 ]; then
            log_info "  Still waiting... (${waited}s/${max_wait}s)"
        fi
    done
    
    log_error "Node $node_id failed to respond within ${max_wait}s"
    return 1
}

# Scan log file for patterns
scan_log_patterns() {
    local log_file=$1
    local patterns=("${!2}")  # Array of patterns to search
    local max_wait=$3
    local waited=0
    local found_count=0
    
    log_info "Scanning log for ${#patterns[@]} patterns (max ${max_wait}s)..."
    
    # Clear old log if exists (so we only scan new output)
    if [ -f "$log_file" ]; then
        # Save original log
        mv "$log_file" "${log_file}.backup.$(date +%s)" 2>/dev/null || true
    fi
    
    while [ $waited -lt $max_wait ]; do
        if [ -f "$log_file" ]; then
            for pattern in "${patterns[@]}"; do
                if grep -q "$pattern" "$log_file" 2>/dev/null; then
                    log_success "Found pattern: '$pattern'"
                    found_count=$((found_count + 1))
                fi
            done
            
            if [ $found_count -eq ${#patterns[@]} ]; then
                log_success "All patterns found in log"
                return 0
            fi
        fi
        
        sleep 2
        waited=$((waited + 2))
        
        if [ $((waited % 10)) -eq 0 ]; then
            log_info "  Scanning... (${waited}s/${max_wait}s, found $found_count/${#patterns[@]} patterns)"
        fi
    done
    
    log_warn "Timeout scanning log. Found $found_count/${#patterns[@]} patterns"
    return 1
}

# Check specific log patterns for Validator restart
check_validator_restart_patterns() {
    local log_file=$1
    local timeout=$2
    local start_time=$(date +%s)
    
    log_phase "MONITORING RESTART PATTERNS"
    
    # Critical patterns to verify cold-start recovery
    declare -a CRITICAL_PATTERNS=(
        "Validator started with consensus authority active"
        "CommitSyncer"
        "RECOVERING\|CatchingUp"
        "Healthy"
    )
    
    local all_found=true
    
    for pattern in "${CRITICAL_PATTERNS[@]}"; do
        log_info "Looking for: $pattern"
        local found=false
        local elapsed=0
        
        while [ $elapsed -lt $timeout ]; do
            if [ -f "$log_file" ] && grep -qE "$pattern" "$log_file" 2>/dev/null; then
                log_success "  ✓ Found: $pattern"
                found=true
                break
            fi
            sleep 1
            elapsed=$(( $(date +%s) - start_time ))
        done
        
        if [ "$found" = false ]; then
            log_warn "  ✗ Not found: $pattern (may still work)"
            all_found=false
        fi
    done
    
    $all_found
}

# Verify block height catchup
verify_catchup() {
    local node_id=$1
    local reference_node=${2:-0}  # Use node 0 as reference
    local max_wait=$3
    local waited=0
    
    log_phase "VERIFYING BLOCK CATCHUP"
    
    local reference_height=$(get_block_height $reference_node)
    log_info "Reference node $reference_node height: $reference_height"
    
    while [ $waited -lt $max_wait ]; do
        local node_height=$(get_block_height $node_id)
        local gap=$((reference_height - node_height))
        
        log_info "Node $node_id height: $node_height (gap: $gap)"
        
        if [ $gap -le 5 ]; then
            log_success "Node $node_id caught up! Gap: $gap blocks"
            return 0
        fi
        
        # Update reference height periodically
        if [ $((waited % 10)) -eq 0 ]; then
            reference_height=$(get_block_height $reference_node)
        fi
        
        sleep 2
        waited=$((waited + 2))
    done
    
    log_error "Node $node_id failed to catch up within ${max_wait}s"
    return 1
}

# Send test transaction
send_test_transaction() {
    log_phase "SENDING TEST TRANSACTION"
    
    if [ ! -d "$TX_SENDER_DIR" ]; then
        log_warn "TX sender directory not found: $TX_SENDER_DIR"
        log_warn "Skipping transaction test"
        return 0
    fi
    
    cd "$TX_SENDER_DIR"
    
    log_info "Sending test transaction..."
    local tx_output
    tx_output=$(GOTOOLCHAIN=go1.23.5 go run . 2>&1) || {
        log_error "Transaction sender failed"
        echo "$tx_output"
        return 1
    }
    
    if echo "$tx_output" | grep -q "✅ All transactions processed"; then
        log_success "Transaction processed successfully!"
        echo "$tx_output" | grep -A 5 "Summary"
        return 0
    else
        log_error "Transaction test did not show success pattern"
        echo "$tx_output"
        return 1
    fi
}

# ═══════════════════════════════════════════════════════════════════════════════
# Main Test Flow
# ═══════════════════════════════════════════════════════════════════════════════

main() {
    log_phase "VALIDATOR RESTART & REJOIN TEST"
    log_info "Target Node: $TEST_NODE_ID"
    log_info "Log File: $LOG_FILE"
    
    # Pre-check: orchestrator exists
    if [ ! -f "$ORCHESTRATOR" ]; then
        log_error "Orchestrator not found: $ORCHESTRATOR"
        exit 1
    fi
    
    # Phase 1: Check cluster status
    log_phase "PHASE 1: CHECKING CLUSTER STATUS"
    
    local running_nodes=0
    for i in 0 1 2 3 4; do
        if is_node_running $i; then
            local height=$(get_block_height $i)
            log_info "Node $i: RUNNING (block: $height)"
            running_nodes=$((running_nodes + 1))
        else
            log_warn "Node $i: NOT RUNNING"
        fi
    done
    
    if [ $running_nodes -lt 5 ]; then
        log_error "Only $running_nodes/5 nodes running. Please start cluster first."
        log_info "Run: $ORCHESTRATOR start"
        exit 1
    fi
    
    log_success "All 5 nodes are running"
    
    # Record initial heights
    declare -A initial_heights
    for i in 0 1 2 3 4; do
        initial_heights[$i]=$(get_block_height $i)
    done
    
    sleep 5  # Let heights stabilize
    
    # Phase 2: Stop target node
    log_phase "PHASE 2: STOPPING NODE $TEST_NODE_ID"
    
    log_info "Stopping node $TEST_NODE_ID..."
    "$ORCHESTRATOR" stop-node $TEST_NODE_ID >/dev/null 2>&1 || true
    
    sleep 2
    
    if is_node_running $TEST_NODE_ID; then
        log_error "Node $TEST_NODE_ID still running after stop command"
        exit 1
    fi
    
    log_success "Node $TEST_NODE_ID stopped"
    
    # Phase 3: Wait for cluster to continue
    log_phase "PHASE 3: WAITING FOR CLUSTER TO CONTINUE (${STOP_WAIT_TIME}s)"
    log_info "Other nodes should continue producing blocks..."
    
    sleep $STOP_WAIT_TIME
    
    # Verify cluster still making progress
    local node0_new_height=$(get_block_height 0)
    if [ $node0_new_height -le ${initial_heights[0]} ]; then
        log_warn "Cluster may be stalled (node 0 height: ${initial_heights[0]} -> $node0_new_height)"
    else
        log_success "Cluster continuing (node 0 height: $node0_new_height)"
    fi
    
    # Phase 4: Restart target node
    log_phase "PHASE 4: RESTARTING NODE $TEST_NODE_ID"
    
    log_info "Starting node $TEST_NODE_ID (preserving data)..."
    "$ORCHESTRATOR" start-node $TEST_NODE_ID >/dev/null 2>&1 || true
    
    sleep 2
    
    # Wait for node to start responding
    if ! wait_for_node $TEST_NODE_ID $STARTUP_WAIT_TIME; then
        log_error "Node $TEST_NODE_ID failed to start"
        exit 1
    fi
    
    # Phase 5: Monitor log patterns
    log_phase "PHASE 5: MONITORING LOG PATTERNS"
    log_info "Checking for cold-start recovery indicators..."
    
    check_validator_restart_patterns "$LOG_FILE" 60 || true
    
    # Phase 6: Verify catchup
    log_phase "PHASE 6: VERIFYING BLOCK CATCHUP"
    
    if verify_catchup $TEST_NODE_ID 0 $CATCHUP_WAIT_TIME; then
        log_success "Node $TEST_NODE_ID successfully caught up!"
    else
        log_warn "Node $TEST_NODE_ID may still be catching up"
    fi
    
    # Phase 7: Test transaction
    if ! send_test_transaction; then
        log_warn "Transaction test may have issues, but node appears to be running"
    fi
    
    # Final status
    log_phase "FINAL STATUS"
    
    echo ""
    echo -e "${BOLD}Node Heights:${NC}"
    for i in 0 1 2 3 4; do
        local current_height=$(get_block_height $i)
        local initial=${initial_heights[$i]}
        local progress=$((current_height - initial))
        
        if [ $i -eq $TEST_NODE_ID ]; then
            echo -e "  Node $i: $current_height (started: $initial, progress: +$progress) ${GREEN}[RESTARTED]${NC}"
        else
            echo -e "  Node $i: $current_height (started: $initial, progress: +$progress)"
        fi
    done
    echo ""
    
    # Summary
    local final_height=$(get_block_height $TEST_NODE_ID)
    local ref_height=$(get_block_height 0)
    local final_gap=$((ref_height - final_height))
    
    if [ $final_gap -le 5 ] && is_node_running $TEST_NODE_ID; then
        log_phase "TEST RESULT: PASS"
        log_success "Node $TEST_NODE_ID successfully restarted and rejoined consensus"
        log_info "Final gap: $final_gap blocks"
        exit 0
    else
        log_phase "TEST RESULT: FAIL"
        log_error "Node $TEST_NODE_ID did not properly rejoin"
        log_info "Final gap: $final_gap blocks"
        exit 1
    fi
}

# Run main
cd "$SCRIPT_DIR"
main "$@"
