#!/bin/bash

# Script to run multinode load test 15 times and collect results
OUTPUT_REPORT="15_runs_report.txt"
echo "=== 15 RUNS REPORT ===" > $OUTPUT_REPORT

for i in {1..15}; do
    echo "Starting Run $i/15..."
    
    # Run the test and capture the output to a temporary file
    ./run_multinode_load.sh 10 10000 > "run_output_${i}.log"
    
    # Extract important metrics
    TPS=$(grep "SYSTEM TPS:" "run_output_${i}.log" | awk -F'~' '{print $2}' | awk '{print $1}')
    TOTAL_SENT=$(grep "Tổng TX gửi:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    TOTAL_IN_BLOCKS=$(grep "TX trong blocks:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    MAX_TX=$(grep "Max TXs/block:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    SUCCESS_RATE=$(grep "Success Rate:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    TIME=$(grep "Thời gian xử lý:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    FORK_SAFE=$(grep "HỆ THỐNG KHÔNG FORK" "run_output_${i}.log" | wc -l)
    
    FORK_STATUS="SAFE"
    if [ "$FORK_SAFE" -eq "0" ]; then
        FORK_STATUS="FORKED OR ERROR"
    fi

    # Append to report
    echo "Run $i:" >> $OUTPUT_REPORT
    echo "  - TPS: $TPS" >> $OUTPUT_REPORT
    echo "  - TXs Sent: $TOTAL_SENT" >> $OUTPUT_REPORT
    echo "  - TXs in Blocks: $TOTAL_IN_BLOCKS" >> $OUTPUT_REPORT
    echo "  - Max TXs/block: $MAX_TX" >> $OUTPUT_REPORT
    echo "  - Success Rate: $SUCCESS_RATE" >> $OUTPUT_REPORT
    echo "  - Time: $TIME" >> $OUTPUT_REPORT
    echo "  - Fork Status: $FORK_STATUS" >> $OUTPUT_REPORT
    echo "--------------------------" >> $OUTPUT_REPORT
    
    echo "Finished Run $i/15 -> TPS: $TPS"
    
    # Optional: small sleep between runs
    sleep 5
done

echo "Tất cả 15 lần chạy đã hoàn thành! Xem kết quả tại $OUTPUT_REPORT"
