#!/bin/bash

# ==============================================================================
# TPS BLAST 200-LOOP STRESS TEST SCRIPT
# ==============================================================================
# This script runs the tps_blast tool 200 consecutive times to stress test
# the Go Sub / Rust Consensus pipeline under extreme, sustained load.
# Total Payload: 200 * 10,000 = 2,000,000 Transactions.
# ==============================================================================

REPETITIONS=2
SUCCESS_COUNT=0
FAILED_COUNT=0

# Thư mục chứa log
LOG_DIR="stress_logs"
mkdir -p "$LOG_DIR"
SUMMARY_LOG="$LOG_DIR/run_summary_$(date +%Y%m%d_%H%M%S).log"

echo "🚀 BẮT ĐẦU CHUỖI STRESS TEST $REPETITIONS LẦN" | tee -a "$SUMMARY_LOG"
echo "------------------------------------------------------" | tee -a "$SUMMARY_LOG"

for i in $(seq 1 $REPETITIONS); do
    echo "======================================================"
    echo "▶️  VÒNG LẶP THỨ $i / $REPETITIONS" | tee -a "$SUMMARY_LOG"
    echo "======================================================"
    
    # Chạy lệnh blast, lưu lại kết quả vào biến output thay vì chỉ in ra màn hình
    # Để grep/awk ra tỷ lệ thành công vào file summary cho dễ xem
    
    # Run tps_blast
    go run . --config config.json --count 20000 --batch 2000 --sleep 5 --wait 300
    
    # Ghi nhận mã trả về của script
    EXIT_CODE=$?
    
    if [ $EXIT_CODE -eq 0 ]; then
        echo "✅ Vòng $i HOÀN THÀNH thành công!" | tee -a "$SUMMARY_LOG"
        ((SUCCESS_COUNT++))
    else
        echo "❌ Vòng $i THẤT BẠI (Exit Code: $EXIT_CODE)!" | tee -a "$SUMMARY_LOG"
        ((FAILED_COUNT++))
        
        # Nếu muốn dừng mạch test ngay khi có lỗi thì bỏ command comment bên dưới:
        # echo "🔴 Dừng chuỗi test do gặp lỗi." | tee -a "$SUMMARY_LOG"
        # break
    fi
    
    # Tạm nghỉ 2 giây giữa mỗi vòng lặp để Node "thở" (hoặc xoá nếu muốn xả tải liên hoàn)
    sleep 2
done

echo "======================================================" | tee -a "$SUMMARY_LOG"
echo "🏁 TỔNG KẾT STRESS TEST ($REPETITIONS vòng)" | tee -a "$SUMMARY_LOG"
echo "✅ THÀNH CÔNG: $SUCCESS_COUNT / $REPETITIONS" | tee -a "$SUMMARY_LOG"
echo "❌ THẤT BẠI: $FAILED_COUNT / $REPETITIONS" | tee -a "$SUMMARY_LOG"
echo "======================================================" | tee -a "$SUMMARY_LOG"

echo "Chi tiết xem tại file log: $SUMMARY_LOG"
