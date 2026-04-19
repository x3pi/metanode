#!/bin/bash

# Script to run multinode load test 30 times and collect results in a Markdown report
OUTPUT_REPORT="30_runs_report.md"

echo "# 🚀 Báo Cáo TPS Blast (30 Lần Khởi Chạy)" > $OUTPUT_REPORT
echo "" >> $OUTPUT_REPORT
echo "Tài liệu tự động tổng hợp kết quả của 30 lần chạy tải liên tiếp trên toàn cụm đa node thông qua công cụ \`tps_blast\`." >> $OUTPUT_REPORT
echo "" >> $OUTPUT_REPORT

echo "## Tổng quan" >> $OUTPUT_REPORT
echo "" >> $OUTPUT_REPORT
echo "| Tham Số | Cấu Hình |" >> $OUTPUT_REPORT
echo "|---|---|" >> $OUTPUT_REPORT
echo "| Client Threads | \`10\` |" >> $OUTPUT_REPORT
echo "| Txs/Thread | \`10,000\` |" >> $OUTPUT_REPORT
echo "| Tổng TXs mong muốn | \`100,000\` TXs Mỗi lần chạy |" >> $OUTPUT_REPORT
echo "" >> $OUTPUT_REPORT

echo "## Bảng Kết Quả Chi Tiết" >> $OUTPUT_REPORT
echo "" >> $OUTPUT_REPORT
echo "| Lần Chạy | TPS Hệ Thống | Số TX Đã Gửi | Số TX Trong Block | TX Max/Block | Tỷ Lệ THÀNH CÔNG | TG Xử Lý | Trạng Thái Fork |" >> $OUTPUT_REPORT
echo "| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |" >> $OUTPUT_REPORT

for i in {1..30}; do
    echo "Starting Run $i/30..."
    
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
    
    FORK_STATUS="✅ SAFE"
    if [ "$FORK_SAFE" -eq "0" ]; then
        FORK_STATUS="❌ FORKED/ERR"
    fi
    
    # Fallback to prevent empty fields in Markdown table
    TPS=${TPS:-"N/A"}
    TOTAL_SENT=${TOTAL_SENT:-"N/A"}
    TOTAL_IN_BLOCKS=${TOTAL_IN_BLOCKS:-"N/A"}
    MAX_TX=${MAX_TX:-"N/A"}
    SUCCESS_RATE=${SUCCESS_RATE:-"N/A"}
    TIME=${TIME:-"N/A"}

    # Append row to MD report
    echo "| **$i** | \`$TPS\` | $TOTAL_SENT | $TOTAL_IN_BLOCKS | $MAX_TX | $SUCCESS_RATE | $TIME | $FORK_STATUS |" >> $OUTPUT_REPORT
    
    echo "Finished Run $i/30 -> TPS: $TPS"
    
    # Optional: small sleep between runs to let the cluster breathe
    sleep 5
done

# Basic stats appending
echo "" >> $OUTPUT_REPORT
echo "## Thống Kê" >> $OUTPUT_REPORT
echo "Đang xử lý kết quả..." 

MIN_TPS=$(awk -F'|' 'NR>11 && $3 ~ /[0-9.]+/ {gsub("`", "", $3); gsub(" ", "", $3); print $3}' $OUTPUT_REPORT | sort -n | head -n 1)
MAX_TPS=$(awk -F'|' 'NR>11 && $3 ~ /[0-9.]+/ {gsub("`", "", $3); gsub(" ", "", $3); print $3}' $OUTPUT_REPORT | sort -n | tail -n 1)
AVG_TPS=$(awk -F'|' 'NR>11 && $3 ~ /[0-9.]+/ {gsub("`", "", $3); gsub(" ", "", $3); sum += $3; count++} END { if(count > 0) printf "%.2f", sum/count; else print "N/A" }' $OUTPUT_REPORT)

echo "" >> $OUTPUT_REPORT
echo "- **Thấp Nhất**: \`$MIN_TPS\` TPS" >> $OUTPUT_REPORT
echo "- **Cao Nhất**: \`$MAX_TPS\` TPS" >> $OUTPUT_REPORT
echo "- **Trung Bình**: \`$AVG_TPS\` TPS" >> $OUTPUT_REPORT

echo "Tất cả 30 lần chạy đã hoàn thành! Báo cáo: $OUTPUT_REPORT"
