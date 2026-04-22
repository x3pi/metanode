#!/bin/bash
set -e

# ==========================================
# Cấu hình Mặc định
# ==========================================
TARGET_NODE=${1:-4}
TARGET_RPC_PORT=10748
NODE0_RPC_PORT=8757
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

GO_DIR="$BASE_DIR/execution/cmd/simple_chain"
RUST_DIR="$BASE_DIR/consensus/metanode"

# Mapping Node -> RPC Port
if [ "$TARGET_NODE" == "1" ]; then TARGET_RPC_PORT=10747; fi
if [ "$TARGET_NODE" == "2" ]; then TARGET_RPC_PORT=10749; fi
if [ "$TARGET_NODE" == "3" ]; then TARGET_RPC_PORT=10750; fi
if [ "$TARGET_NODE" == "4" ]; then TARGET_RPC_PORT=10748; fi

echo "=============================================="
echo " 🧪 AUTO TEST SNAPSHOT TỰ ĐỘNG - NODE $TARGET_NODE"
echo "=============================================="
echo ""

cd "$SCRIPT_DIR"

# 0. Chạy giao dịch (tx_sender) để tạo block mới & snapshot mới
echo "⏳ [0/6] Bơm giao dịch (tx_sender) để tạo block và snapshot mới..."
rm -f /tmp/tx_sender.pid
killall tx_sender 2>/dev/null || true
pushd "$BASE_DIR/execution/cmd/tool/tx_sender" > /dev/null
go run . -loop > /dev/null 2>&1 &
TX_PID=$!
popd > /dev/null

echo "   -> Đang đợi 20 giây để giao dịch sinh ra block mới..."
sleep 20
kill $TX_PID 2>/dev/null || true
pkill -f "tx_sender" 2>/dev/null || true
killall tx_sender 2>/dev/null || true
rm -f /tmp/tx_sender.pid
echo "   -> Đã dừng bơm giao dịch."

# 1. Tìm bản Snapshot mới nhất
echo "⏳ [1/6] Tìm Snapshot mới nhất từ Node 0..."
MAX_WAIT=60
ELAPSED=0
LATEST_SNAPSHOT=""

while [ $ELAPSED -lt $MAX_WAIT ]; do
    LATEST_SNAPSHOT=$(ls -1 "$GO_DIR/snapshot_data_node0" 2>/dev/null | sort -V | tail -n 1 || true)
    if [ -n "$LATEST_SNAPSHOT" ] && [ "$LATEST_SNAPSHOT" != "null" ]; then
        echo "   -> Đã tìm thấy snapshot mục tiêu, chờ 2s để đảm bảo ghi hoàn tất..."
        sleep 2
        break
    fi
    echo "   -> Chưa thấy snapshot (Go có thể đang tính toán/flush)... đợi thêm ($ELAPSED/$MAX_WAIT s)"
    sleep 3
    ELAPSED=$((ELAPSED + 3))
done

if [ -z "$LATEST_SNAPSHOT" ]; then
    echo "❌ Không tìm thấy Snapshot nào trên Node 0 sau quá trình kiểm tra!"
    exit 1
fi
echo "✅ Dùng Snapshot: $LATEST_SNAPSHOT"

# 2. Stop Node
echo "⏳ [2/6] Dừng tiến trình Node $TARGET_NODE..."
./mtn-orchestrator.sh stop-node "$TARGET_NODE" > /dev/null
sleep 2

# 3. Phá hủy dữ liệu Node
echo "⏳ [3/6] Phá hủy dữ liệu (Wipe Storage) của Node $TARGET_NODE..."
rm -rf "$GO_DIR/sample/node${TARGET_NODE}/data"
rm -rf "$GO_DIR/sample/node${TARGET_NODE}/back_up"
rm -rf "$RUST_DIR/config/storage/node_${TARGET_NODE}"

# 4. Copy dữ liệu (Restore)
echo "⏳ [4/6] Copy/Download dự liệu $LATEST_SNAPSHOT vào Node $TARGET_NODE..."
mkdir -p "$GO_DIR/sample/node${TARGET_NODE}/data/data"
# Sử dụng local CP cho nhanh trong môi trường test (hoặc bạn có thể sửa thành wget/rsync khi chạy 2 server khác nhau)
cp -r "$GO_DIR/snapshot_data_node0/$LATEST_SNAPSHOT/"* "$GO_DIR/sample/node${TARGET_NODE}/data/data/"

# 5. Khởi động lại
echo "⏳ [5/6] Khởi động lại Node $TARGET_NODE..."
./mtn-orchestrator.sh start-node "$TARGET_NODE" > /dev/null
echo "✅ Node $TARGET_NODE đã được bật. Đang chờ 5s để module khởi tạo DB..."
sleep 5

# 6. Polling giám sát đồng thuận (RPC)
echo "⏳ [6/6] Khảo sát đồng thuận RPC (Tối đa 30s)..."

MAX_ATTEMPTS=40
ATTEMPT=0
SYNCED=false

# Helper function để gọi RPC lấy thông tin block mới nhất
fetch_block_info() {
    local port=$1
    # Dùng jq để parse JSON ra '{hash} {stateroot} {blockNumber}'
    curl -s -X POST -H 'Content-Type: application/json' \
        --data '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", false],"id":1}' \
        "http://127.0.0.1:${port}" | jq -r 'if .result != null then "\(.result.hash) \(.result.stateRoot) \(.result.number)" else "null null null" end' 2>/dev/null || echo "null null null"
}

while [ $ATTEMPT -lt $MAX_ATTEMPTS ]; do
    ATTEMPT=$((ATTEMPT+1))
    
    # Đọc Node 0 Mốc chuẩn
    INFO_0=$(fetch_block_info $NODE0_RPC_PORT)
    HASH_0=$(echo "$INFO_0" | awk '{print $1}')
    ROOT_0=$(echo "$INFO_0" | awk '{print $2}')
    NUM_0=$(echo "$INFO_0" | awk '{print $3}')

    # Đọc Node Target 
    INFO_T=$(fetch_block_info $TARGET_RPC_PORT)
    HASH_T=$(echo "$INFO_T" | awk '{print $1}')
    ROOT_T=$(echo "$INFO_T" | awk '{print $2}')
    NUM_T=$(echo "$INFO_T" | awk '{print $3}')

    if [ "$HASH_0" == "null" ] || [ "$HASH_T" == "null" ] || [ "$HASH_T" == "" ]; then
        echo "   -> [Retry $ATTEMPT/$MAX_ATTEMPTS] Đang đợi RPC Server khởi động phản hồi..."
        sleep 1
        continue
    fi

    # Hiển thị tiến trình
    echo "   -> Block theo dõi:"
    echo "      Node 0: Block [$NUM_0] - Hash: ${HASH_0:0:10}... - Root: ${ROOT_0:0:10}..."
    echo "      Node $TARGET_NODE: Block [$NUM_T] - Hash: ${HASH_T:0:10}... - Root: ${ROOT_T:0:10}..."

    if [ "$NUM_T" == "$NUM_0" ] && [ "$HASH_T" == "$HASH_0" ] && [ "$ROOT_T" == "$ROOT_0" ]; then
        SYNCED=true
        echo ""
        echo "🎉 [THÀNH CÔNG] Node $TARGET_NODE đã hoàn toàn catch up và bám sát đồng thuận với hệ thống!"
        echo "   Dữ liệu Snapshot cực chuẩn, khớp từ StateRoot tới DAG Hash tại block $NUM_0."
        break
    fi

    echo "   Đang đợi Node $TARGET_NODE đồng bộ (Block $NUM_T -> $NUM_0)..."
    sleep 2
done

if [ "$SYNCED" = false ]; then
    echo "❌ [THẤT BẠI] Node $TARGET_NODE chưa thể đồng thuận sau $MAX_ATTEMPTS chu kỳ!"
    echo "   - Node 0: Block $NUM_0 | Root: $ROOT_0"
    echo "   - Node $TARGET_NODE: Block $NUM_T | Root: $ROOT_T"
    exit 1
fi
echo "=============================================="
echo " ✅ TEST HOÀN TẤT!"
echo "=============================================="
