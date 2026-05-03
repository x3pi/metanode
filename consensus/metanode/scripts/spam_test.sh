#!/bin/bash
set -e

echo "=========================================================================="
echo "🔄 Bắt đầu tự động spam test E2E..."
echo "=========================================================================="

# Cấu hình số vòng test (mặc định 10 lần nếu không truyền tham số)
MAX_RUNS=${1:-10}
TARGET_NODE=1

FAILED=0
PASSED=0

for ((i=1; i<=MAX_RUNS; i++)); do
    echo ""
    echo "▶️ Đang chạy Test vòng $i / $MAX_RUNS mạnh tay trên Node $TARGET_NODE..."
    echo "=========================================================================="
    
    # Kiểm tra xem Node 0 đã có snapshot chưa trước khi chạy test (vì test 4.5 cần snapshot)
    echo "🔍 Đang kiểm tra Node 0 xem đã có snapshot chưa..."
    SNAPSHOTS_JSON=$(curl -sf "http://127.0.0.1:8700/api/snapshots" 2>/dev/null || echo "null")
    if [ "$SNAPSHOTS_JSON" = "null" ] || [ -z "$SNAPSHOTS_JSON" ]; then
        echo -e "⚠️ Node 0 chưa có snapshot (HTTP không phản hồi). Dừng test một cách an toàn."
        exit 0
    fi
    
    SNAPSHOT_COUNT=$(echo "$SNAPSHOTS_JSON" | jq 'length' 2>/dev/null || echo "0")
    if [ "$SNAPSHOT_COUNT" -eq 0 ]; then
        echo -e "⚠️ Node 0 chưa tạo xong snapshot nào. Dừng test một cách an toàn (chưa đủ điều kiện test)."
        exit 0
    fi
    echo -e "✅ Đã phát hiện $SNAPSHOT_COUNT snapshot trên Node 0! Bắt đầu đập phá..."

    
    # Chỉ chạy destructive test (khởi động/wipe) và bỏ qua rác Log 
    # Nếu muốn bỏ qua restart hoàn toàn cả trong test chặn, hãy thêm --skip-destructive
    export DISABLE_REPUTATION_SWAPS=1
    if ./e2e_test_suite.sh --node "$TARGET_NODE"; then
        echo -e "\n✅ Vòng $i PASSED thành công rực rỡ!"
        PASSED=$((PASSED + 1))
    else
        echo -e "\n❌ Vòng $i FAILED!"
        FAILED=$((FAILED + 1))
        
        # Pull log lỗi nổi bật
        echo "🔍 Đang trích xuất log lỗi của Node $TARGET_NODE..."
        tail -n 200 logs/node_${TARGET_NODE}/rust-consensus.log | grep -E "ERROR|WARN|CRITICAL|PANIC" || true
        
        echo "🚨 Dừng spam test vì có lỗi tại vòng $i!"
        exit 1
    fi
    
    # Nghỉ ngơi ngắn giữa các đợt đập/tắt
    echo "[Spam-Bot] Nghỉ 3s trước khi tiếp tục..."
    sleep 3
done

echo ""
echo "🎯 KẾT QUẢ TỔNG QUAN SPAM TEST:"
echo "✅ Passed: $PASSED / $MAX_RUNS"
echo "❌ Failed: $FAILED"

if [ $FAILED -gt 0 ]; then
    exit 1
fi
