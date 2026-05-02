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
GO_DATA_DIR=("node0" "node1" "node2" "node3" "node4")
GO_MASTER_SESSION=("go-master-0" "go-master-1" "go-master-2" "go-master-3" "go-master-4")
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

for sess in "go-master-${NODE_ID}"; do
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

# 5a. Clean stale UDS sockets — CRITICAL for Rust FFI consensus reconnection
# Without this, Rust FFI inside Go Master cannot bind/connect to peers
echo -e "${CYAN}  [5a] Dọn sạch stale UDS sockets...${NC}"
rm -f "/tmp/executor${NODE_ID}.sock" 2>/dev/null || true
rm -f "/tmp/rust-go-node${NODE_ID}-master.sock" 2>/dev/null || true
rm -f "/tmp/metanode-tx-${NODE_ID}.sock" 2>/dev/null || true
echo -e "${GREEN}    ✅ Sockets cleaned${NC}"

# 5b. Start Go Master (embeds Rust Consensus via FFI)
echo -e "${CYAN}  [5b] Go Master + Rust FFI...${NC}"
cd "$GO_SIMPLE_ROOT"
DATA="${GO_DATA_DIR[$NODE_ID]}"
XAPIAN_PATH="sample/$DATA/data/data/xapian_node"
mkdir -p "$XAPIAN_PATH"

# Use EXACT same startup command as mtn-orchestrator.sh for consistency
tmux new-session -d -s "${GO_MASTER_SESSION[$NODE_ID]}" -c "$GO_SIMPLE_ROOT" \
    "ulimit -n 100000; export RUST_BACKTRACE=full && export GOTRACEBACK=crash && export GOTOOLCHAIN=go1.23.5 && export GOMEMLIMIT=4GiB && export XAPIAN_BASE_PATH='$XAPIAN_PATH' && export MVM_LOG_DIR='$LOG_DIR/node_$NODE_ID' && exec ./simple_chain -config=${GO_MASTER_CONFIG[$NODE_ID]} >> \"$LOG_DIR/node_$NODE_ID/go-master-stdout.log\" 2>&1"
echo -e "${GREEN}    🚀 Go Master + Rust FFI started (${GO_MASTER_SESSION[$NODE_ID]})${NC}"

# 5c. Wait for Go Master to initialize and Rust FFI to bootstrap
echo -e "${CYAN}  [5c] Đợi Go Master + Rust FFI khởi tạo (15s)...${NC}"
sleep 15

# 5d. Verify process is alive
HAS_PID=$(pgrep -f "simple_chain.*config-master-node${NODE_ID}" 2>/dev/null | head -1 || true)
if [ -n "$HAS_PID" ]; then
    echo -e "${GREEN}    ✅ Go Master alive (PID: $HAS_PID)${NC}"
else
    echo -e "${RED}    ❌ Go Master crashed! Check: tail -50 $LOG_DIR/node_$NODE_ID/go-master-stdout.log${NC}"
fi



# Step 6: Sync Monitor
echo ""
echo -e "${BLUE}[6/7] 📊 Giám sát sync tuần tự (180s)...${NC}"
PREV_BLOCK=""
STUCK_COUNT=0
MAX_STUCK=3

for t in 10 20 30 40 50 60 70 80 90 100 110 120 130 140 150 160 170 180; do
    sleep 10
    RESTORED_PORT=${MASTER_RPC_PORTS[$NODE_ID]}
    RESTORED_RESP=$(curl -sf -m 1 -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
        "http://127.0.0.1:$RESTORED_PORT" 2>/dev/null || echo "")
        
    CURRENT_BLOCK=""
    if [ -n "$RESTORED_RESP" ]; then
        RESTORED_HEX=$(echo "$RESTORED_RESP" | python3 -c "import sys,json; r=json.load(sys.stdin).get('result',None); print(r if r else '')" 2>/dev/null || echo "")
        if [ -n "$RESTORED_HEX" ] && [ "$RESTORED_HEX" != "0x" ]; then CURRENT_BLOCK=$((16#${RESTORED_HEX#0x})); fi
    fi
    
    GO_BATCH_GEI=$(grep -a 'BATCH-DRAIN' "$LOG_DIR/node_$NODE_ID/go-master-stdout.log" 2>/dev/null | tail -1 | grep -oP '\d+→\d+' | awk -F'→' '{print $2}' || echo "")
    RUST_GEI=$(grep -a 'GEI ' "$LOG_DIR/node_$NODE_ID/rust.log" 2>/dev/null | tail -1 | grep -oP '\d+→\d+' | awk -F'→' '{print $2}' || echo "")
    
    CURRENT_GEI=""
    if [ -n "$RUST_GEI" ] && [ -n "$GO_BATCH_GEI" ]; then
        if [ "$RUST_GEI" -gt "$GO_BATCH_GEI" ]; then CURRENT_GEI=$RUST_GEI; else CURRENT_GEI=$GO_BATCH_GEI; fi
    elif [ -n "$RUST_GEI" ]; then CURRENT_GEI=$RUST_GEI
    elif [ -n "$GO_BATCH_GEI" ]; then CURRENT_GEI=$GO_BATCH_GEI
    fi

    DISP_BLOCK=${CURRENT_BLOCK:-"?"}
    DISP_GEI=${CURRENT_GEI:-"?"}

    if [ -z "$CURRENT_BLOCK" ] && [ -z "$CURRENT_GEI" ]; then
        echo -e "  ${YELLOW}⏱️ +${t}s: node chưa khởi chạy xong (chưa có log & RPC)${NC}"
        continue
    fi
    
    CURRENT_PROGRESS=${CURRENT_GEI:-$CURRENT_BLOCK}
    PROG_LABEL=$( [ -n "$CURRENT_GEI" ] && echo "GEI" || echo "block" )
    
    if [ "$CURRENT_PROGRESS" = "$PREV_BLOCK" ]; then
        STUCK_COUNT=$((STUCK_COUNT + 1))
        if [ $STUCK_COUNT -ge $MAX_STUCK ]; then echo -e "  ${RED}⏱️ +${t}s: block=$DISP_BLOCK, GEI=$DISP_GEI — ⚠️ STUCK ${STUCK_COUNT}x!${NC}"
        else echo -e "  ${YELLOW}⏱️ +${t}s: block=$DISP_BLOCK, GEI=$DISP_GEI — (chưa tăng)${NC}"; fi
    else
        STUCK_COUNT=0
        if [ -n "$PREV_BLOCK" ]; then
            JUMP=$((CURRENT_PROGRESS - PREV_BLOCK))
            if [ $JUMP -gt 100 ]; then echo -e "  ${YELLOW}⏱️ +${t}s: block=$DISP_BLOCK, GEI=$DISP_GEI — ⚡ jump +$JUMP $PROG_LABEL${NC}"
            else echo -e "  ${GREEN}⏱️ +${t}s: block=$DISP_BLOCK, GEI=$DISP_GEI — ✅ +$JUMP $PROG_LABEL${NC}"; fi
        else
            echo -e "  ${GREEN}⏱️ +${t}s: block=$DISP_BLOCK, GEI=$DISP_GEI — ✅ syncing${NC}"
        fi
    fi
    PREV_BLOCK="$CURRENT_PROGRESS"
done

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
echo ""
