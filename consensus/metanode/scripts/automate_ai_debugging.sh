#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Automated AI Debugging Wrapper
#  - Build và khởi động lại toàn bộ cluster (fresh)
#  - Bơm giao dịch cho đến khi block > 500 để tạo snapshot
#  - Chạy stability loop
#  - Tự động thu thập data khi có lỗi (Deadlock/Fork)
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ROUNDS=${1:-100}

echo "=========================================================="
echo "🤖 KÍCH HOẠT QUÁ TRÌNH PHÂN TÍCH LỖI TỰ ĐỘNG BỞI AI"
echo "Vòng lặp tối đa: $ROUNDS"
echo "=========================================================="

# 1. Khởi động lại toàn bộ cluster với dữ liệu mới và build lại code
echo "[1/4] Khởi động lại cụm với dữ liệu sạch và build lại mã nguồn mới..."
bash "$SCRIPT_DIR/mtn-orchestrator.sh" start --fresh --build-all
sleep 15 # Đợi cluster ổn định

# 2. Bơm giao dịch để sinh ra block > 500
echo "[2/4] Bắt đầu bơm giao dịch (để đạt block > 500 và sinh snapshot)..."
TX_SENDER_DIR="$BASE_DIR/execution/cmd/tool/tx_sender"
if [ ! -x "$TX_SENDER_DIR/tx_sender" ]; then
    (cd "$TX_SENDER_DIR" && go build -o tx_sender .)
fi
"$TX_SENDER_DIR/tx_sender" --config "$TX_SENDER_DIR/config.json" \
    --data "$TX_SENDER_DIR/data.json" --loop --node "127.0.0.1:4201" > /dev/null 2>&1 &
TX_PUMP_PID=$!

echo "- Đang chờ hệ thống đạt block > 500..."
while true; do
    # Lấy block number từ node 0
    BLOCK_RES=$(curl -s --max-time 3 -X POST "http://127.0.0.1:8757" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null || echo "")
    if [ -n "$BLOCK_RES" ]; then
        HEX=$(echo "$BLOCK_RES" | grep -o '"result":"[^"]*"' | cut -d'"' -f4 || echo "0x0")
        BLOCK_NUM=$(printf "%d" "$HEX" 2>/dev/null || echo "0")
        
        echo -ne "\r  => Block hiện tại: $BLOCK_NUM/500 "
        if [ "$BLOCK_NUM" -ge 500 ]; then
            echo -e "\n- Đã đạt block > 500. Dừng bơm giao dịch."
            kill -TERM "$TX_PUMP_PID" 2>/dev/null || true
            break
        fi
    fi
    sleep 5
done

# 3. Đợi Snapshot xuất hiện
echo "[3/4] Đợi hệ thống sinh Snapshot..."
while true; do
    SNAP_JSON=$(curl -sf "http://127.0.0.1:8600/api/snapshots" 2>/dev/null || echo "[]")
    SNAP_COUNT=$(echo "$SNAP_JSON" | jq 'length' 2>/dev/null || echo "0")
    if [ "$SNAP_COUNT" -gt 0 ]; then
        SNAP_NAME=$(echo "$SNAP_JSON" | jq -r '.[-1].snapshot_name' 2>/dev/null || echo "unknown")
        echo "- Đã có snapshot: $SNAP_NAME"
        break
    fi
    echo -ne "\r  => Chưa có snapshot, đang chờ..."
    sleep 5
done

# 4. Chạy Stability Test Loop
echo "[4/4] Bắt đầu Test Snapshot Loop..."
if bash "$SCRIPT_DIR/test_snapshot_stability_loop.sh" --rounds "$ROUNDS" --rotate; then
    echo "=========================================================="
    echo "✅ HỆ THỐNG ỔN ĐỊNH - Đã pass $ROUNDS vòng liên tiếp!"
    echo "Không phát hiện lỗi Deadlock/Fork."
    echo "=========================================================="
    exit 0
fi

echo "=========================================================="
echo "🚨 PHÁT HIỆN LỖI! ĐANG THU THẬP DỮ LIỆU CHO AI PHÂN TÍCH..."
echo "=========================================================="

TIMESTAMP=$(date +%Y%m%d_%H%M%S)
PACKAGE_DIR="/tmp/ai_debug_package_$TIMESTAMP"
mkdir -p "$PACKAGE_DIR"

# Copy Stability Report mới nhất
LATEST_REPORT=$(ls -t "$SCRIPT_DIR"/stability_report_*.md 2>/dev/null | head -n 1)
if [ -n "$LATEST_REPORT" ]; then
    cp "$LATEST_REPORT" "$PACKAGE_DIR/"
    echo "- Đã sao chép Stability Report: $(basename "$LATEST_REPORT")"
fi

# Copy 500 dòng cuối cùng của Node Logs
LOG_DIR="$PACKAGE_DIR/node_logs"
mkdir -p "$LOG_DIR"
for i in 0 1 2 3 4; do
    NODE_LOG="$BASE_DIR/consensus/metanode/logs/node_$i/go-master-stdout.log"
    if [ -f "$NODE_LOG" ]; then
        tail -n 1000 "$NODE_LOG" > "$LOG_DIR/node_${i}_tail1000.log"
    fi
done
echo "- Đã trích xuất Node Logs."

# Copy thông tin commit mới nhất để biết codebase
(cd "$BASE_DIR" && git log -1 > "$PACKAGE_DIR/git_commit.log")

# Đóng gói
ZIP_FILE="$SCRIPT_DIR/ai_debug_data_$TIMESTAMP.zip"
(cd "/tmp" && zip -r -q "$ZIP_FILE" "ai_debug_package_$TIMESTAMP")

rm -rf "$PACKAGE_DIR"

echo "=========================================================="
echo "📦 ĐÃ ĐÓNG GÓI DỮ LIỆU THÀNH CÔNG!"
echo "File: $ZIP_FILE"
echo "Vui lòng đính kèm file này (hoặc nội dung report) và báo lại cho AI."
echo "=========================================================="
exit 1

