#!/bin/bash
# Usage: ./run_all_validator.sh
# Fresh start VALIDATOR nodes only (0-3), NO SyncOnly node 4
# - Cleans all Go/Rust data (keeps keys/configs)
# - Starts Go Masters → Go Subs → Rust Nodes in correct order

set -e
set -o pipefail
ulimit -n 100000 || true

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

# Paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_PROJECT_ROOT="$(cd "$METANODE_ROOT/../.." && pwd)/execution"
GO_SIMPLE_ROOT="$GO_PROJECT_ROOT/cmd/simple_chain"
LOG_DIR="$METANODE_ROOT/logs"
BINARY="$METANODE_ROOT/target/release/metanode"

# Only validator nodes 0-3 (NO node 4 SyncOnly)
NODES=(0 1 2 3)

# Config Maps
GO_MASTER_CONFIG=("config-master-node0.json" "config-master-node1.json" "config-master-node2.json" "config-master-node3.json")
GO_SUB_CONFIG=("config-sub-node0.json" "config-sub-node1.json" "config-sub-node2.json" "config-sub-node3.json")
GO_DATA_DIR=("node0" "node1" "node2" "node3")
GO_MASTER_SESSION=("go-master-0" "go-master-1" "go-master-2" "go-master-3")
GO_SUB_SESSION=("go-sub-0" "go-sub-1" "go-sub-2" "go-sub-3")
RUST_SESSION=("metanode-0" "metanode-1" "metanode-2" "metanode-3")
GO_MASTER_SOCKET=("/tmp/rust-go-node0-master.sock" "/tmp/rust-go-node1-master.sock" "/tmp/rust-go-node2-master.sock" "/tmp/rust-go-node3-master.sock")
RUST_CONFIG=("config/node_0.toml" "config/node_1.toml" "config/node_2.toml" "config/node_3.toml")



echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  🚀 FRESH START VALIDATORS (0-3) — NO SyncOnly node 4${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""

# ==============================================================================
# Step 1: Stop everything
# ==============================================================================
echo -e "${BLUE}📋 Step 1: Stop all processes...${NC}"
"$SCRIPT_DIR/stop_all.sh"
sleep 2

# ==============================================================================
# Step 2: Build binaries
# ==============================================================================
echo -e "${BLUE}📋 Step 2: Build Rust and Go binaries...${NC}"
echo "  🔄 Building Rust metanode..."
export PATH="/home/abc/protoc3/bin:$PATH"
cd "$METANODE_ROOT" && cargo build --release --bin metanode
echo "  🔄 Building Go simple_chain..."
cd "$GO_SIMPLE_ROOT" && go build -o simple_chain .
echo -e "${GREEN}  ✅ Binaries ready${NC}"

# ==============================================================================
# Step 3: Clean ALL data (keep keys/configs)
# ==============================================================================
echo -e "${BLUE}📋 Step 3: Clean all data...${NC}"

for i in "${!NODES[@]}"; do
    id=${NODES[$i]}
    DATA="${GO_DATA_DIR[$i]}"
    rm -rf "$GO_SIMPLE_ROOT/sample/$DATA/data" 2>/dev/null || true
    rm -rf "$GO_SIMPLE_ROOT/sample/$DATA/data-write" 2>/dev/null || true
    rm -rf "$GO_SIMPLE_ROOT/sample/$DATA/back_up" 2>/dev/null || true
    rm -rf "$GO_SIMPLE_ROOT/sample/$DATA/back_up_write" 2>/dev/null || true
done

rm -rf "$GO_SIMPLE_ROOT/snapshot_data"* 2>/dev/null || true
rm -rf "$METANODE_ROOT/config/storage" 2>/dev/null || true
rm -rf "$LOG_DIR" 2>/dev/null || true

for i in "${!NODES[@]}"; do
    id=${NODES[$i]}
    DATA="${GO_DATA_DIR[$i]}"
    mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/data/data/xapian_node"
    mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/data-write/data/xapian_node"
    mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/back_up"
    mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/back_up_write"
    mkdir -p "$LOG_DIR/node_$id"
done
mkdir -p "$METANODE_ROOT/config/storage"

rm -f /tmp/executor*.sock /tmp/rust-go-*.sock /tmp/metanode-tx-*.sock 2>/dev/null || true
rm -f /tmp/epoch_data_backup.json /tmp/epoch_data_backup_*.json 2>/dev/null || true

echo -e "${GREEN}  ✅ All data cleaned, keys/configs preserved${NC}"

# ==============================================================================
# Step 5: Start Go Masters
# ==============================================================================
for i in "${!NODES[@]}"; do
    id=${NODES[$i]}
    DATA="${GO_DATA_DIR[$i]}"
    XAPIAN="sample/$DATA/data/data/xapian_node"
    SESSION="${GO_MASTER_SESSION[$i]}"
    LOG_FILE="$LOG_DIR/node_$id/go-master-stdout.log"

    echo -e "${GREEN}  🚀 Starting Go Master $id ($SESSION)...${NC}"
    PPROF_ARG=""
    if [ "$id" -eq "0" ]; then
        PPROF_ARG="--pprof-addr=localhost:6060"
    fi

    # Use tee to show output on terminal AND write to log file
    # Add post-mortem diagnostics so crash info stays visible
    tmux new-session -d -s "$SESSION" -c "$GO_SIMPLE_ROOT" \
        "ulimit -n 100000; export RUST_BACKTRACE=full && export GOTRACEBACK=crash && export GOTOOLCHAIN=go1.23.5 && export XAPIAN_BASE_PATH='$XAPIAN' && export MVM_LOG_DIR='$LOG_DIR/node_$id' && echo \"═══ [NODE $id] PID=\$\$ Started at \$(date '+%Y-%m-%d %H:%M:%S') ═══\" && ./simple_chain -config=${GO_MASTER_CONFIG[$i]} $PPROF_ARG 2>&1 | tee -a \"$LOG_FILE\"; EXIT_CODE=\$?; echo ''; echo '╔═══════════════════════════════════════════════════════════╗'; echo '║  🚨 NODE $id PROCESS EXITED                              ║'; echo '╚═══════════════════════════════════════════════════════════╝'; echo \"  ⏰ Time: \$(date '+%Y-%m-%d %H:%M:%S')\"; echo \"  📊 Exit code: \$EXIT_CODE\"; if [ \$EXIT_CODE -gt 128 ]; then SIG=\$((EXIT_CODE - 128)); echo \"  ⚡ Killed by signal: \$SIG (\$(kill -l \$SIG 2>/dev/null || echo UNKNOWN))\"; fi; echo \"  📁 Log: $LOG_FILE\"; echo '  💡 Use: tmux attach -t $SESSION  to reconnect'"
    # Keep tmux pane alive after process exits for crash analysis
    tmux set-option -t "$SESSION" remain-on-exit on

    sleep 2
done



# ==============================================================================
# Step 7: Start Go Subs
# ==============================================================================
echo -e "${BLUE}📋 Step 7: Start all Go Subs...${NC}"
cd "$GO_SIMPLE_ROOT"

for i in "${!NODES[@]}"; do
    id=${NODES[$i]}
    DATA="${GO_DATA_DIR[$i]}"
    XAPIAN="sample/$DATA/data-write/data/xapian_node"
    SESSION="${GO_SUB_SESSION[$i]}"
    LOG_FILE="$LOG_DIR/node_$id/go-sub-stdout.log"

    echo -e "${GREEN}  🚀 Starting Go Sub $id ($SESSION)...${NC}"
    tmux new-session -d -s "$SESSION" -c "$GO_SIMPLE_ROOT" \
        "ulimit -n 100000; export RUST_BACKTRACE=full && export GOTRACEBACK=crash && export GOTOOLCHAIN=go1.23.5 && export XAPIAN_BASE_PATH='$XAPIAN' && export MVM_LOG_DIR='$LOG_DIR/node_$id' && echo \"═══ [SUB $id] PID=\$\$ Started at \$(date '+%Y-%m-%d %H:%M:%S') ═══\" && ./simple_chain -config=${GO_SUB_CONFIG[$i]} 2>&1 | tee -a \"$LOG_FILE\"; EXIT_CODE=\$?; echo ''; echo '╔═══════════════════════════════════════════════════════════╗'; echo '║  🚨 SUB $id PROCESS EXITED                               ║'; echo '╚═══════════════════════════════════════════════════════════╝'; echo \"  ⏰ Time: \$(date '+%Y-%m-%d %H:%M:%S')\"; echo \"  📊 Exit code: \$EXIT_CODE\"; if [ \$EXIT_CODE -gt 128 ]; then SIG=\$((EXIT_CODE - 128)); echo \"  ⚡ Killed by signal: \$SIG (\$(kill -l \$SIG 2>/dev/null || echo UNKNOWN))\"; fi; echo '  💡 Use: tmux attach -t $SESSION  to reconnect'"
    tmux set-option -t "$SESSION" remain-on-exit on

    sleep 1
done

echo "  ⏳ Waiting 5s for Go Subs to initialize..."
sleep 5



# ==============================================================================
# Summary
# ==============================================================================
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  🎉 ALL VALIDATORS STARTED (0-3)! No SyncOnly node.${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""
for i in "${!NODES[@]}"; do
    id=${NODES[$i]}
    echo -e "${GREEN}  Node $id:${NC} tmux attach -t go-master-$id | go-sub-$id"
done
echo ""
echo -e "${GREEN}  📁 Logs: $LOG_DIR/node_N/${NC}"
echo -e "${GREEN}  🔍 Check: tmux ls${NC}"
echo ""
