#!/bin/bash
# Usage: ./resume_node_ai.sh <node_id>
# Resume a single node keeping all data (AI-friendly, uses nohup instead of tmux)

set -e
set -o pipefail

NODE_ID="${1:?Usage: $0 <node_id> (0-4)}"

if [[ ! "$NODE_ID" =~ ^[0-4]$ ]]; then
    echo "❌ Invalid node_id: $NODE_ID (must be 0-4)"
    exit 1
fi

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_PROJECT_ROOT="$(cd "$METANODE_ROOT/../.." && pwd)/execution"
GO_SIMPLE_ROOT="$GO_PROJECT_ROOT/cmd/simple_chain"
LOG_DIR="$METANODE_ROOT/logs"
BINARY="$METANODE_ROOT/target/release/metanode"

# Configs
GO_CONFIG="config-master-node${NODE_ID}.json"
RUST_CONFIG="config/node_${NODE_ID}.toml"
DATA="node${NODE_ID}"

GO_SOCKET="/tmp/rust-go-node${NODE_ID}-master.sock"



echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  🔄 RESUME Node $NODE_ID (keep data, AI nohup mode)${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo ""

echo -e "${BLUE}📋 Step 1: Stop node $NODE_ID if running...${NC}"
"$SCRIPT_DIR/stop_node_ai.sh" "$NODE_ID" 2>/dev/null || true
sleep 2

echo -e "${BLUE}📋 Step 2: Verify configs...${NC}"
if [ ! -f "$BINARY" ] || [ -n "$(find "$METANODE_ROOT/src" -name '*.rs' -newer "$BINARY" 2>/dev/null | head -1)" ]; then
    echo "  🔄 Building Rust Metanode..."
    cd "$METANODE_ROOT" && cargo build --release --bin metanode
    echo -e "${GREEN}  ✅ Binary rebuilt${NC}"
fi

mkdir -p "$LOG_DIR/node_$NODE_ID"
mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/data/data/xapian_node"
mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/data-write/data/xapian_node"
mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/back_up"
mkdir -p "$GO_SIMPLE_ROOT/sample/$DATA/back_up_write"

echo -e "${BLUE}📋 Step 3: Start Go Node...${NC}"
cd "$GO_SIMPLE_ROOT"
XAPIAN_NODE="sample/$DATA/data/data/xapian_node"
export GOTOOLCHAIN=go1.23.5
export GOMEMLIMIT=4GiB
export XAPIAN_BASE_PATH="$XAPIAN_NODE"
nohup ./simple_chain -config="$GO_CONFIG" > "$LOG_DIR/node_$NODE_ID/go-master-stdout.log" 2>&1 &
echo -e "${GREEN}  🚀 Go Node started (nohup)${NC}"



sleep 3
echo ""
echo -e "${GREEN}✅ Node $NODE_ID RESUMED${NC}"
echo -e "  📁 Logs: $LOG_DIR/node_$NODE_ID/"
echo ""
