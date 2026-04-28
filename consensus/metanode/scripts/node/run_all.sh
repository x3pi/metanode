#!/bin/bash
# Usage: ./run_all.sh
# Fresh start ALL nodes (0-4) with clean data, keep keys
# - Cleans all Go/Rust data (keeps keys/configs)
# - Resets genesis timestamp ONCE
# - Syncs committee to genesis
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

# Config Maps
GO_MASTER_CONFIG=("config-master-node0.json" "config-master-node1.json" "config-master-node2.json" "config-master-node3.json" "config-master-node4.json")
GO_DATA_DIR=("node0" "node1" "node2" "node3" "node4")
GO_MASTER_SESSION=("go-master-0" "go-master-1" "go-master-2" "go-master-3" "go-master-4")
RUST_SESSION=("metanode-0" "metanode-1" "metanode-2" "metanode-3" "metanode-4")
GO_MASTER_SOCKET=("/tmp/rust-go-node0-master.sock" "/tmp/rust-go-node1-master.sock" "/tmp/rust-go-node2-master.sock" "/tmp/rust-go-node3-master.sock" "/tmp/rust-go-node4-master.sock")
RUST_CONFIG=("config/node_0.toml" "config/node_1.toml" "config/node_2.toml" "config/node_3.toml" "config/node_4.toml")



echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  🚀 FRESH START ALL NODES (0-4) — keep keys, clean data${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""

# ==============================================================================
# Step 1: Stop everything
# ==============================================================================
echo -e "${BLUE}📋 Step 1: Stop all processes...${NC}"
# "$SCRIPT_DIR/stop_all.sh"
sleep 2

# ==============================================================================
# Step 2: Check binary
# ==============================================================================
echo -e "${BLUE}📋 Step 2: Build Rust and Go binaries...${NC}"
echo "  🔄 Building Rust metanode..."
export PATH="/home/abc/protoc3/bin:$PATH"
cd "$METANODE_ROOT" && cargo +nightly build --release --bin metanode
echo "  🔄 Building Go simple_chain..."
cd "$GO_SIMPLE_ROOT" && go build -o simple_chain .
echo -e "${GREEN}  ✅ Binaries ready${NC}"

# ==============================================================================
# Step 3: Clean ALL data (keep keys/configs)
# ==============================================================================
echo -e "${BLUE}📋 Step 3: Clean all data...${NC}"

# Clean Go data for ALL nodes
for id in 0 1 2 3 4; do
    DATA="${GO_DATA_DIR[$id]}"
    rm -rf "$GO_SIMPLE_ROOT/sample/$DATA/data" 2>/dev/null || true
    rm -rf "$GO_SIMPLE_ROOT/sample/$DATA/back_up" 2>/dev/null || true
done

# Clean snapshot data
rm -rf "$GO_SIMPLE_ROOT/snapshot_data"* 2>/dev/null || true

# Clean Rust storage (not keys)
rm -rf "$METANODE_ROOT/config/storage" 2>/dev/null || true

# Clean logs
rm -rf "$LOG_DIR" 2>/dev/null || true

# Clean old MVM log files
# rm -f "$LOG_DIR/mvm_debug.log" 2>/dev/null || true
# rm -f "$LOG_DIR/mvm_cpp_"*.log 2>/dev/null || true

# Recreate directories
for id in 0 1 2 3 4; do
    DATA="${GO_DATA_DIR[$id]}"
    mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/data/data/xapian_node"
    mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/back_up"
    mkdir -p "$LOG_DIR/node_$id"
done
mkdir -p "$METANODE_ROOT/config/storage"

# Clean old sockets and epoch data
rm -f /tmp/executor*.sock /tmp/rust-go-*.sock /tmp/metanode-tx-*.sock 2>/dev/null || true
rm -f /tmp/epoch_data_backup.json /tmp/epoch_data_backup_*.json 2>/dev/null || true

echo -e "${GREEN}  ✅ All data cleaned, keys/configs preserved${NC}"

# ==============================================================================

for id in 0 1 2 3 4; do
    DATA="${GO_DATA_DIR[$id]}"
    XAPIAN="sample/$DATA/data/data/xapian_node"
    
    echo -e "${GREEN}  🚀 Starting Go Master $id (${GO_MASTER_SESSION[$id]})...${NC}"
    PPROF_ARG=""
    if [ "$id" -eq "0" ]; then
        PPROF_ARG="--pprof-addr=localhost:6060"
    fi
    tmux new-session -d -s "${GO_MASTER_SESSION[$id]}" -c "$GO_SIMPLE_ROOT" \
        "ulimit -n 100000; export GOTOOLCHAIN=go1.23.5 && export GOMEMLIMIT=4GiB && export XAPIAN_BASE_PATH='$XAPIAN' && export MVM_LOG_DIR='$LOG_DIR/node_$id' && ./simple_chain -config=${GO_MASTER_CONFIG[$id]} $PPROF_ARG >> \"$LOG_DIR/node_$id/go-master-stdout.log\" 2>&1"
    
    sleep 2  # Brief pause between Go Masters
done





# ==============================================================================
# Summary
# ==============================================================================
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  🎉 ALL NODES STARTED (0-4)!${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""
for id in 0 1 2 3 4; do
    echo -e "${GREEN}  Node $id:${NC} tmux attach -t go-master-$id"
done
echo ""
echo -e "${GREEN}  📁 Logs: $LOG_DIR/node_N/${NC}"
echo -e "${GREEN}  📝 MVM debug log: tail -f $LOG_DIR/node_N/mvm_cpp_*.log${NC}"
echo -e "${GREEN}  🔍 Check: tmux ls${NC}"
echo ""

# ==============================================================================
# Step 9: Run SetGet test
# ==============================================================================
echo -e "${BLUE}📋 Step 9: Running SetGet test...${NC}"
CLIENT_DIR="$HOME/nhat/client/cmd/client/call_tool_example_new"
if [ -d "$CLIENT_DIR" ]; then
    cd "$CLIENT_DIR"
    echo -e "${GREEN}  🚀 Running: go run . -data=SetGet.json -config=config-local-genis.json${NC}"
    # Run the command and pipe 3 enters to it
    (sleep 2; echo ""; sleep 2; echo ""; sleep 2; echo "") | go run . -data=SetGet.json -config=config-local-genis.json
    echo -e "${GREEN}  ✅ SetGet test completed${NC}"
else
    echo -e "${YELLOW}  ⚠️ Client directory not found: $CLIENT_DIR${NC}"
fi
echo ""
