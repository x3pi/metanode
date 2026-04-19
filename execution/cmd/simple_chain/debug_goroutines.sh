#!/bin/bash

# Script để capture goroutine dumps và phân tích

PPROF_URL="http://127.0.0.1:6061/debug/pprof"
LOG_DIR="sample/simple/data-write/logs"

echo "🔍 Goroutine Debug Script"
echo "================================"

# Function để capture dump
capture_dump() {
    local name=$1
    local desc=$2
    echo -e "\n📸 Capturing: $desc ($name)"
    # Goroutine dump
    curl -s "$PPROF_URL/goroutine?debug=2" > "$LOG_DIR/goroutine_${name}.txt"
    echo "✅ Saved: $LOG_DIR/goroutine_${name}.txt"
    # Heap dump
    curl -s "$PPROF_URL/heap" > "$LOG_DIR/heap_${name}.prof"
    echo "✅ Saved: $LOG_DIR/heap_${name}.prof"
    # Memory stats
    curl -s "$PPROF_URL/allocs?debug=1" > "$LOG_DIR/allocs_${name}.txt"
    echo "✅ Saved: $LOG_DIR/allocs_${name}.txt"
}

# 1. Baseline (trước test)
echo -e "\n⏳ Waiting 2 seconds for app to stabilize..."
sleep 2
capture_dump "baseline_start-11" "Baseline - App started"

# # 2. Đợi user chạy load test
# echo -e "\n⏳ Waiting for load test to start..."
# echo "📌 Please run your 1000 requests NOW and wait for them to complete"
# read -p "Press ENTER when load test is RUNNING: " dummy

# # 3. Capture during test
# sleep 5
# capture_dump "during_test" "During load test"

# # 4. Wait for test to complete
# echo -e "\n⏳ Waiting for test to complete..."
# read -p "Press ENTER when load test is COMPLETED: " dummy

# # 5. Immediate after test
# capture_dump "immediately_after" "Immediately after test"

# # 6. Wait 30 seconds
# echo -e "\n⏳ Waiting 30 seconds for GC..."
# sleep 30
# capture_dump "after_30sec" "After 30 seconds"

# # 7. Wait another 5 minutes
# echo -e "\n⏳ Waiting 5 minutes for memory to be freed..."
# sleep 300
# capture_dump "after_5min" "After 5 minutes"

# # 8. Analysis
# echo -e "\n📊 Analysis"
# echo "================================"

# echo -e "\n🔴 COMPARING BASELINE vs IMMEDIATELY AFTER:"
# echo "Goroutines increase:"
# baseline_goroutines=$(grep "^goroutine " "$LOG_DIR/goroutine_baseline_start.txt" | head -1 | grep -oP '\d+' | head -1)
# after_goroutines=$(grep "^goroutine " "$LOG_DIR/goroutine_immediately_after.txt" | head -1 | grep -oP '\d+' | head -1)

# echo "  Baseline: $baseline_goroutines goroutines"
# echo "  After: $after_goroutines goroutines"
# echo "  Increase: $((after_goroutines - baseline_goroutines)) goroutines"

# if [ $after_goroutines -gt $((baseline_goroutines + 100)) ]; then
#     echo "⚠️  LEAK DETECTED: Goroutines not cleaned up!"
# fi

# echo -e "\n🔎 Top goroutines after test (first 30):"
# head -100 "$LOG_DIR/goroutine_immediately_after.txt" | grep -A 3 "^goroutine"

# echo -e "\n📈 Heap dumps for analysis:"
# echo "  Baseline: $LOG_DIR/heap_baseline_start.prof"
# echo "  After: $LOG_DIR/heap_immediately_after.prof"
# echo ""
# echo "To analyze with pprof:"
# echo "  go tool pprof -http=:8080 $LOG_DIR/heap_baseline_start.prof"
# echo "  go tool pprof -http=:8081 $LOG_DIR/heap_immediately_after.prof"

# echo -e "\n✅ All captures completed!"


