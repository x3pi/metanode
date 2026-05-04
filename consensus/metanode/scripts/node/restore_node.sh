#!/bin/bash
# ═══════════════════════════════════════════════════════════════
#  RESTORE NODE TỪ SNAPSHOT — SEQUENTIAL & FORK-SAFE
#  Usage: ./restore_node.sh <node_id> [snapshot_name]
# ═══════════════════════════════════════════════════════════════
set -e

NODE_ID="${1:?❌ Usage: $0 <node_id> [snapshot_name]}"

if [[ ! "$NODE_ID" =~ ^[0-4]$ ]]; then
    echo "❌ node_id phải từ 0-4, nhận được: $NODE_ID"
    exit 1
fi

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

# Paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_PROJECT_ROOT="$(cd "$METANODE_ROOT/../.." && pwd)/execution"
GO_SIMPLE_ROOT="$GO_PROJECT_ROOT/cmd/simple_chain"
LOG_DIR="$METANODE_ROOT/logs"
BINARY="$METANODE_ROOT/target/release/metanode"

# Snapshot source — HTTP server
SNAP_SERVER="${SNAP_SERVER:-http://localhost:8600}"
SNAP_API="$SNAP_SERVER/api/snapshots"
SNAP_FILES_URL="$SNAP_SERVER/files"

NODE_DATA="$GO_SIMPLE_ROOT/sample/node${NODE_ID}"

# Master RPC ports per node (from config-master-nodeX.json)
MASTER_RPC_PORTS=(8757 10747 10749 10750 10748)

# Config maps
GO_MASTER_CONFIG=("config-master-node0.json" "config-master-node1.json" "config-master-node2.json" "config-master-node3.json" "config-master-node4.json")
GO_SUB_CONFIG=("config-sub-node0.json" "config-sub-node1.json" "config-sub-node2.json" "config-sub-node3.json" "config-sub-node4.json")
GO_DATA_DIR=("node0" "node1" "node2" "node3" "node4")
GO_MASTER_SESSION=("go-master-0" "go-master-1" "go-master-2" "go-master-3" "go-master-4")
GO_SUB_SESSION=("go-sub-0" "go-sub-1" "go-sub-2" "go-sub-3" "go-sub-4")
RUST_SESSION=("metanode-0" "metanode-1" "metanode-2" "metanode-3" "metanode-4")
GO_MASTER_SOCKET=("/tmp/rust-go-node0-master.sock" "/tmp/rust-go-node1-master.sock" "/tmp/rust-go-node2-master.sock" "/tmp/rust-go-node3-master.sock" "/tmp/rust-go-node4-master.sock")
RUST_CONFIG=("config/node_0.toml" "config/node_1.toml" "config/node_2.toml" "config/node_3.toml" "config/node_4.toml")



find_reference_node() {
    for i in 0 1 2 3 4; do
        [ "$i" -eq "$NODE_ID" ] && continue
        local port=${MASTER_RPC_PORTS[$i]}
        local resp=$(curl -sf -X POST -H "Content-Type: application/json" \
            --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            "http://127.0.0.1:$port" 2>/dev/null || echo "")
        if [ -n "$resp" ]; then
            local hex_block=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result',''))" 2>/dev/null || echo "")
            if [ -n "$hex_block" ] && [ "$hex_block" != "None" ] && [ "$hex_block" != "" ]; then
                echo "$i"
                return 0
            fi
        fi
    done
    echo ""
    return 1
}

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  📸 RESTORE Node $NODE_ID — Sequential & Fork-Safe${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo ""

find_snap_dir_local() {
    local snap_name="$1"
    for i in 0 1 2 3 4; do
        local candidate="$GO_SIMPLE_ROOT/snapshot_data_node${i}/$snap_name"
        if [ -d "$candidate" ]; then
            echo "$candidate"
            return 0
        fi
    done
    return 1
}

SNAP_MODE="network"
SNAP_DIR=""

echo -e "${BLUE}📡 Snapshot server: ${NC}$SNAP_SERVER"

if [ -n "$2" ]; then
    SNAP_NAME="$2"
    echo -e "${BLUE}📸 Sử dụng snapshot chỉ định: ${NC}$SNAP_NAME"
else
    echo -e "${BLUE}🔍 Tự động tìm snapshot mới nhất...${NC}"
    SNAP_NAME=$(curl -sf "$SNAP_API" 2>/dev/null \
        | python3 -c "import sys,json; snaps=json.load(sys.stdin); print(max(snaps, key=lambda x: x['block_number'])['snapshot_name'])" 2>/dev/null) || true
    
    if [ -z "$SNAP_NAME" ]; then
        echo -e "${RED}❌ Không lấy được snapshot từ API!${NC}"
        echo "   Kiểm tra: curl $SNAP_API"
        exit 1
    fi
    echo -e "${GREEN}  ✅ API trả về: ${NC}$SNAP_NAME"
fi

LOCAL_DIR=$(find_snap_dir_local "$SNAP_NAME")
if [ -n "$LOCAL_DIR" ]; then
    SNAP_DIR="$LOCAL_DIR"
    SNAP_MODE="local"
    echo -e "${GREEN}  ✅ Tìm thấy local: ${NC}$(basename $(dirname $SNAP_DIR))/$SNAP_NAME"
else
    HTTP_CHECK=$(curl -s -o /dev/null -w "%{http_code}" "$SNAP_FILES_URL/$SNAP_NAME/" 2>/dev/null)
    [ -z "$HTTP_CHECK" ] && HTTP_CHECK="000"

    if [ "$HTTP_CHECK" = "200" ]; then
        echo -e "${GREEN}  ✅ Snapshot có sẵn qua HTTP${NC}"
        SNAP_MODE="network"
    else
        echo -e "${RED}❌ Snapshot $SNAP_NAME không tìm thấy qua local lẫn HTTP!${NC}"
        exit 1
    fi
fi

if [ "$SNAP_MODE" = "local" ]; then
    SNAP_SIZE=$(du -sh "$SNAP_DIR" 2>/dev/null | awk '{print $1}')
    echo -e "${BLUE}  📦 Kích thước: ${NC}$SNAP_SIZE ${CYAN}(local copy)${NC}"
else
    echo -e "${BLUE}  📦 Download từ: ${NC}$SNAP_FILES_URL/$SNAP_NAME/ ${CYAN}(network)${NC}"
fi

echo ""
echo -e "${YELLOW}⚠️  Thao tác này sẽ:${NC}"
echo "   1. Dừng Node $NODE_ID"
echo "   2. Xóa TOÀN BỘ dữ liệu Node $NODE_ID (Go + Rust DAG)"
echo "   3. Khôi phục từ snapshot: $SNAP_NAME"
echo "   4. Validate dữ liệu snapshot"
echo "   5. Khởi động tuần tự (Go Master→Rust)"
echo "   6. Giám sát sync tuần tự 90s"
echo "   7. Kiểm tra hash divergence với mạng"
echo ""
read -p "Tiếp tục? (y/N): " CONFIRM
if [[ "$CONFIRM" != "y" && "$CONFIRM" != "Y" ]]; then
    echo "Đã hủy."
    exit 0
fi

START_TIME=$(date +%s)

# Step 1: Stop Node
echo ""
echo -e "${BLUE}[1/7] 🛑 Dừng Node $NODE_ID...${NC}"
"$SCRIPT_DIR/stop_node.sh" "$NODE_ID" 2>/dev/null || true

for sess in "go-master-${NODE_ID}" "go-sub-${NODE_ID}"; do
    if tmux has-session -t "$sess" 2>/dev/null; then
        tmux send-keys -t "$sess" C-c 2>/dev/null || true
        sleep 2
        tmux kill-session -t "$sess" 2>/dev/null || true
    fi
done
pkill -f "config-master-node${NODE_ID}.json" 2>/dev/null || true
pkill -f "config/node_${NODE_ID}.toml" 2>/dev/null || true

sleep 2
echo -e "${GREEN}  ✅ Node $NODE_ID đã dừng hoàn toàn${NC}"

# Step 2: Xóa data
echo -e "${BLUE}[2/7] 🗑️  Xóa dữ liệu Node $NODE_ID (Go + Rust DAG)...${NC}"
rm -rf "$NODE_DATA/data" 2>/dev/null || true
rm -rf "$NODE_DATA/back_up" 2>/dev/null || true
RUST_STORAGE="$METANODE_ROOT/config/storage/node_$NODE_ID"
rm -rf "$RUST_STORAGE" 2>/dev/null || true
echo -e "${GREEN}  ✅ Rust DAG storage đã xóa: ${NC}$RUST_STORAGE"
rm -f "$LOG_DIR/node_$NODE_ID/go-master-stdout.log" 2>/dev/null || true
rm -f "$LOG_DIR/node_$NODE_ID/go-sub-stdout.log" 2>/dev/null || true
rm -f "$LOG_DIR/node_$NODE_ID/rust.log" 2>/dev/null || true
mkdir -p "$LOG_DIR/node_$NODE_ID"
echo -e "${GREEN}  ✅ Dữ liệu và logs đã xóa sạch${NC}"

# Step 3: Restore
echo -e "${BLUE}[3/7] 📸 Khôi phục từ $SNAP_NAME ($SNAP_MODE mode)...${NC}"
mkdir -p "$NODE_DATA/data/data"
mkdir -p "$NODE_DATA/back_up"

if [ "$SNAP_MODE" = "network" ]; then
    TEMP_SNAP="/tmp/snapshot_restore_$$"
    mkdir -p "$TEMP_SNAP"
    echo -e "${CYAN}  📥 Downloading snapshot via HTTP...${NC}"
    wget -c -r -np -nH --cut-dirs=2 -q --show-progress \
        "$SNAP_FILES_URL/$SNAP_NAME/" \
        -P "$TEMP_SNAP" 2>&1 || {
        echo -e "${RED}  ❌ Download thất bại!${NC}"
        rm -rf "$TEMP_SNAP"
        exit 1
    }
    if [ -d "$TEMP_SNAP/$SNAP_NAME" ]; then SNAP_DL_DIR="$TEMP_SNAP/$SNAP_NAME"; else SNAP_DL_DIR="$TEMP_SNAP"; fi
    DL_SIZE=$(du -sh "$SNAP_DL_DIR" 2>/dev/null | awk '{print $1}')
    echo -e "${GREEN}  ✅ Downloaded: $DL_SIZE${NC}"
    SNAP_DIR="$SNAP_DL_DIR"
fi

echo "  📁 Mapping data dirs..."
for folder in "$SNAP_DIR"/*; do
  folder_name=$(basename "$folder")
  if [ "$folder_name" = "back_up" ]; then
      cp -a "$folder"/* "$NODE_DATA/back_up/" 2>/dev/null || true
  elif [ "$folder_name" = "metadata.json" ] || [ "$folder_name" = "index.html" ]; then
      continue
  elif [ -d "$folder" ]; then
      cp -a "$folder" "$NODE_DATA/data/data/"
  fi
done
# 🚨 CRITICAL: Remove `rust_consensus` imported from the snapshot to avoid split-brain.
# Rust must start from GEI=0 and jump to Go's GEI, rather than inheriting a potentially dirty ahead-of-time DAG.
rm -rf "$NODE_DATA/data/data/rust_consensus" 2>/dev/null || true
echo -e "${GREEN}  ✅ Removed dirty rust_consensus to force clean Phase: Bootstrapping${NC}"

echo -e "${GREEN}  ✅ Data dirs copied${NC}"

if [ -f "$SNAP_DIR/metadata.json" ]; then
    cp -a "$SNAP_DIR/metadata.json" "$NODE_DATA/data/data/metadata.json" 2>/dev/null || true
    cp -a "$SNAP_DIR/metadata.json" "$NODE_DATA/data/metadata.json" 2>/dev/null || true
    cp -a "$SNAP_DIR/metadata.json" "$NODE_DATA/metadata.json" 2>/dev/null || true
    echo -e "${GREEN}  ✅ metadata.json restored${NC}"
fi

EPOCH_RESTORED=false
if [ -f "$SNAP_DIR/back_up/epoch_data_backup.json" ]; then
    cp -a "$SNAP_DIR/back_up/epoch_data_backup.json" "$NODE_DATA/back_up/epoch_data_backup.json"
    EPOCH_RESTORED=true
    EPOCH_INFO=$(python3 -c "import json; d=json.load(open('$SNAP_DIR/back_up/epoch_data_backup.json')); print(f'epoch={d[\"current_epoch\"]}')" 2>/dev/null || echo "epoch=?")
    echo -e "${GREEN}  ✅ Epoch data restored: ${EPOCH_INFO}${NC}"
fi

if [ "$EPOCH_RESTORED" = false ]; then
    echo -e "${RED}  ❌ epoch_data_backup.json NOT found in snapshot! Go sẽ bắt đầu từ epoch 0.${NC}"
fi

find "$NODE_DATA" -name "LOCK" -delete 2>/dev/null
# Also remove NOMT .lock files — these contain the PID of the snapshot source process
# and will prevent nomt_open from working on a different node process.
find "$NODE_DATA" -name ".lock" -path "*/nomt_db/*" -delete 2>/dev/null
echo -e "${GREEN}  ✅ Removed stale PebbleDB LOCK and NOMT .lock files${NC}"
if [ "$SNAP_MODE" = "network" ] && [ -n "$TEMP_SNAP" ]; then rm -rf "$TEMP_SNAP"; fi
RESTORED_SIZE=$(du -sh "$NODE_DATA" 2>/dev/null | awk '{print $1}')
echo -e "${GREEN}  ✅ Đã khôi phục tổng cộng: $RESTORED_SIZE${NC}"

# Step 4: Validate
echo -e "${BLUE}[4/7] 🔍 Kiểm tra tính toàn vẹn dữ liệu snapshot...${NC}"
VALIDATION_OK=true

if [ -f "$NODE_DATA/back_up/epoch_data_backup.json" ]; then
    if python3 -c "import json; json.load(open('$NODE_DATA/back_up/epoch_data_backup.json'))" 2>/dev/null; then
        echo -e "${GREEN}  ✅ epoch_data_backup.json — valid JSON${NC}"
    else
        echo -e "${RED}  ❌ epoch_data_backup.json — INVALID JSON!${NC}"
        VALIDATION_OK=false
    fi
fi

REQUIRED_DIRS=("blocks" "nomt_db" "transaction_state")
for dir in "${REQUIRED_DIRS[@]}"; do
    if [ -d "$NODE_DATA/data/data/$dir" ]; then
        echo -e "${GREEN}  ✅ $dir/ — present in Master${NC}"
    else
        echo -e "${RED}  ❌ $dir/ — MISSING!${NC}"
        VALIDATION_OK=false
    fi
done

PEBBLE_SIZE=$(du -sh "$NODE_DATA/back_up" 2>/dev/null | awk '{print $1}')
if [ -n "$PEBBLE_SIZE" ] && [ "$PEBBLE_SIZE" != "0" ]; then
    echo -e "${GREEN}  ✅ PebbleDB back_up/ — $PEBBLE_SIZE${NC}"
else
    echo -e "${YELLOW}  ⚠️  PebbleDB back_up/ trống hoặc không có data${NC}"
fi

if [ "$VALIDATION_OK" = false ]; then
    echo -e "${RED}  ⚠️  Validation có lỗi! Tiếp tục có thể gây fork.${NC}"
    read -p "  Vẫn tiếp tục? (y/N): " CONTINUE_ANYWAY
    if [[ "$CONTINUE_ANYWAY" != "y" && "$CONTINUE_ANYWAY" != "Y" ]]; then exit 1; fi
fi

# Step 5: Start Node
echo -e "${BLUE}[5/7] 🚀 Khởi động tuần tự Node $NODE_ID...${NC}"
cd "$GO_SIMPLE_ROOT"
DATA="${GO_DATA_DIR[$NODE_ID]}"

echo -e "${CYAN}  [5a] Go Master...${NC}"
XAPIAN_MASTER="sample/$DATA/data/data/xapian_node"
tmux new-session -d -s "${GO_MASTER_SESSION[$NODE_ID]}" -c "$GO_SIMPLE_ROOT" \
    "ulimit -n 100000; export GOTOOLCHAIN=go1.23.5 && export GOMEMLIMIT=4GiB && export XAPIAN_BASE_PATH='$XAPIAN_MASTER' && export MVM_LOG_DIR='$LOG_DIR/node_$NODE_ID' && ./simple_chain -config=${GO_MASTER_CONFIG[$NODE_ID]} >> \"$LOG_DIR/node_$NODE_ID/go-master-stdout.log\" 2>&1"
echo -e "${GREEN}    🚀 Go Master started (${GO_MASTER_SESSION[$NODE_ID]})${NC}"

echo -e "${CYAN}  [5b] Go Sub...${NC}"
XAPIAN_SUB="sample/$DATA/data-write/data/xapian_node"
tmux new-session -d -s "${GO_SUB_SESSION[$NODE_ID]}" -c "$GO_SIMPLE_ROOT" \
    "ulimit -n 100000; export GOTOOLCHAIN=go1.23.5 && export GOMEMLIMIT=4GiB && export XAPIAN_BASE_PATH='$XAPIAN_SUB' && ./simple_chain -config=${GO_SUB_CONFIG[$NODE_ID]} >> \"$LOG_DIR/node_$NODE_ID/go-sub-stdout.log\" 2>&1"
echo -e "${GREEN}    🚀 Go Sub started (${GO_SUB_SESSION[$NODE_ID]})${NC}"

echo -e "${CYAN}  [5c] Đợi Go nhận dữ liệu snapshot (10s)...${NC}"
sleep 10
GO_BLOCK=$(grep -a "last_committed_block=" "$LOG_DIR/node_$NODE_ID/go-master-stdout.log" 2>/dev/null | tail -1 | sed -n 's/.*last_committed_block=\([0-9]*\).*/\1/p') || true
if [ -n "$GO_BLOCK" ]; then echo -e "${GREEN}    ✅ Go Master nhận snapshot — block=$GO_BLOCK${NC}"; fi



# Step 6: Sync Monitor — Compare with reference node
echo ""
echo -e "${BLUE}[6/7] 📊 Giám sát sync (so sánh với node tham chiếu, tối đa 120s)...${NC}"
echo -e "${YELLOW}  ℹ️  Block chỉ tăng khi có giao dịch hoặc chuyển đổi epoch (empty blocks đã bị tắt)${NC}"
PREV_BLOCK=""
SYNCED=false

get_block_height() {
    local port=$1
    local resp=$(curl -sf -m 2 -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":1}' \
        "http://127.0.0.1:$port" 2>/dev/null || echo "")
    if [ -n "$resp" ]; then
        echo "$resp" | python3 -c "
import sys,json
try:
    r=json.load(sys.stdin).get('result',None)
    if r:
        bn=int(r.get('number','0x0'),16)
        gei=int(r.get('globalExecIndex','0x0'),16)
        ep=int(r.get('epoch','0x0'),16)
        print(f'{bn} {gei} {ep}')
    else:
        print('')
except:
    print('')
" 2>/dev/null || echo ""
    fi
}

for t in 10 20 30 40 50 60 70 80 90 100 110 120; do
    sleep 10
    RESTORED_PORT=${MASTER_RPC_PORTS[$NODE_ID]}
    
    # Get restored node info
    RESTORED_INFO=$(get_block_height $RESTORED_PORT)
    RESTORED_BLOCK=$(echo "$RESTORED_INFO" | awk '{print $1}')
    RESTORED_GEI=$(echo "$RESTORED_INFO" | awk '{print $2}')
    RESTORED_EPOCH=$(echo "$RESTORED_INFO" | awk '{print $3}')
    
    if [ -z "$RESTORED_BLOCK" ]; then
        echo -e "  ${YELLOW}⏱️ +${t}s: node chưa khởi chạy xong (RPC chưa sẵn sàng)${NC}"
        continue
    fi
    
    # Get reference node info
    REF_NODE=$(find_reference_node)
    REF_BLOCK=""
    REF_GEI=""
    if [ -n "$REF_NODE" ]; then
        REF_PORT=${MASTER_RPC_PORTS[$REF_NODE]}
        REF_INFO=$(get_block_height $REF_PORT)
        REF_BLOCK=$(echo "$REF_INFO" | awk '{print $1}')
        REF_GEI=$(echo "$REF_INFO" | awk '{print $2}')
    fi
    
    DISP="block=$RESTORED_BLOCK, GEI=${RESTORED_GEI:-?}, epoch=${RESTORED_EPOCH:-?}"
    
    if [ -n "$REF_BLOCK" ] && [ "$RESTORED_BLOCK" -ge "$REF_BLOCK" ] 2>/dev/null; then
        # Node has caught up to or surpassed reference node
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ SYNCED (ref node $REF_NODE: block=$REF_BLOCK)${NC}"
        SYNCED=true
        break
    elif [ -n "$PREV_BLOCK" ] && [ "$RESTORED_BLOCK" -gt "$PREV_BLOCK" ] 2>/dev/null; then
        # Block is increasing — still syncing
        JUMP=$((RESTORED_BLOCK - PREV_BLOCK))
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ +$JUMP blocks (ref: ${REF_BLOCK:-?})${NC}"
    elif [ -n "$PREV_BLOCK" ] && [ "$RESTORED_BLOCK" -eq "$PREV_BLOCK" ] 2>/dev/null; then
        # Block not increasing — check if behind reference or just idle
        if [ -n "$REF_BLOCK" ] && [ "$RESTORED_BLOCK" -lt "$REF_BLOCK" ] 2>/dev/null; then
            BEHIND=$((REF_BLOCK - RESTORED_BLOCK))
            echo -e "  ${YELLOW}⏱️ +${t}s: $DISP — ⏳ đang đợi sync ($BEHIND blocks behind ref node $REF_NODE)${NC}"
        else
            echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ đã bắt kịp mạng (idle, chờ giao dịch mới)${NC}"
            SYNCED=true
            break
        fi
    else
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ syncing${NC}"
    fi
    PREV_BLOCK="$RESTORED_BLOCK"
done

if [ "$SYNCED" = true ]; then
    echo -e "  ${GREEN}✅ Node $NODE_ID đã đồng bộ thành công!${NC}"
else
    echo -e "  ${YELLOW}⚠️  Hết 120s giám sát. Kiểm tra logs để xác nhận trạng thái sync.${NC}"
fi

# Step 7: Hash Check
echo ""
echo -e "${BLUE}[7/7] 🔒 Kiểm tra hash divergence...${NC}"
RESTORED_RESP=$(curl -sf -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "http://127.0.0.1:$RESTORED_PORT" 2>/dev/null || echo "")

if [ -n "$RESTORED_RESP" ]; then
    RESTORED_HEX=$(echo "$RESTORED_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('result','0x0'))" 2>/dev/null || echo "0x0")
    RESTORED_DEC=$((16#${RESTORED_HEX#0x}))
    echo -e "${BLUE}  Node $NODE_ID block hiện tại: $RESTORED_DEC${NC}"
    
    REF_NODE=$(find_reference_node)
    if [ -n "$REF_NODE" ]; then
        REF_PORT=${MASTER_RPC_PORTS[$REF_NODE]}
        
        if [ $RESTORED_DEC -gt 0 ]; then
            CHECK_BLOCK_HEX=$(printf "0x%x" $RESTORED_DEC)
            
            HASH_RESTORED=$(curl -sf -X POST -H "Content-Type: application/json" \
                --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$CHECK_BLOCK_HEX\",false],\"id\":1}" \
                "http://127.0.0.1:$RESTORED_PORT" 2>/dev/null \
                | python3 -c "import sys,json; r=json.load(sys.stdin).get('result',{}); print(r.get('hash','') if r else '')" 2>/dev/null || echo "")
            
            HASH_REFERENCE=$(curl -sf -X POST -H "Content-Type: application/json" \
                --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$CHECK_BLOCK_HEX\",false],\"id\":1}" \
                "http://127.0.0.1:$REF_PORT" 2>/dev/null \
                | python3 -c "import sys,json; r=json.load(sys.stdin).get('result',{}); print(r.get('hash','') if r else '')" 2>/dev/null || echo "")
            
            if [ -n "$HASH_RESTORED" ] && [ -n "$HASH_REFERENCE" ]; then
                if [ "$HASH_RESTORED" = "$HASH_REFERENCE" ]; then
                    echo -e "${GREEN}  ✅ Block $RESTORED_DEC hash KHỚP giữa Node $NODE_ID và Node $REF_NODE${NC}"
                else
                    echo -e "${RED}  ❌ FORK DETECTED! Block $RESTORED_DEC hash KHÁC NHAU!${NC}"
                fi
            fi
        fi
    fi
fi

ELAPSED=$(( $(date +%s) - START_TIME ))
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ✅ RESTORE HOÀN TẤT trong ${ELAPSED}s${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo ""
echo -e "  ${BLUE}tmux sessions:${NC}"
echo "    Go Master: tmux attach -t go-master-${NODE_ID}"
echo "    Go Sub:    tmux attach -t go-sub-${NODE_ID}"
echo ""
