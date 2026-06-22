#!/bin/bash

# Script to run multinode load test N times and collect results

RUNS=15
INVENTORY=""
NO_REBUILD=0
RAMDISK_OPTION=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --inventory|-i)
      INVENTORY="$2"
      shift 2
      ;;
    --no-rebuild|no-build|no-rebuild)
      NO_REBUILD=1
      shift
      ;;
    --ramdisk)
      RAMDISK_OPTION="--ramdisk"
      shift
      ;;
    *)
      if [[ "$1" =~ ^[0-9]+$ ]]; then
         RUNS="$1"
      fi
      shift
      ;;
  esac
done

OUTPUT_REPORT="${RUNS}_runs_report.txt"
echo "=== ${RUNS} RUNS REPORT ===" > $OUTPUT_REPORT


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MTN_CONSENSUS_ROOT="$(cd "$SCRIPT_DIR/../../../../consensus" && pwd)"

# ─── CLEANUP CONCURRENT RUNS ──────────────────────────────────────────
echo "🧹 Cleaning up any conflicting load test processes..."
pkill -f "run_multinode_load.sh" 2>/dev/null
pkill -f "tps_blast" 2>/dev/null
pkill -f "block_hash_checker" 2>/dev/null
for pid in $(pgrep -f "run_15_times.sh"); do
    if [ "$pid" -ne "$$" ]; then
        echo "Killing conflicting run_15_times.sh process: $pid"
        kill -9 "$pid" 2>/dev/null
    fi
done
# ──────────────────────────────────────────────────────────────────────

for ((i=1; i<=RUNS; i++)); do
    echo "Starting Run $i/$RUNS..."
    
if [ "$i" -eq 1 ]; then
        if [ -n "$INVENTORY" ]; then
            echo "▶️ Run 1: Restarting REMOTE nodes using Ansible inventory $INVENTORY..."
            if [ "$NO_REBUILD" -eq 0 ]; then
                echo "  (Rebuilding and wiping data)"
                ansible-playbook -i "$INVENTORY" "$MTN_CONSENSUS_ROOT/../deploy/ansible/deploy.yml" -e "ansible_action=start keep_data=false"
            else
                echo "  (Restarting without wiping data)"
                ansible-playbook -i "$INVENTORY" "$MTN_CONSENSUS_ROOT/../deploy/ansible/deploy.yml" -e "ansible_action=start keep_data=true"
            fi
            echo "⏳ Waiting 15s for remote nodes to stabilize..."
            sleep 15
        else
            BUILD_OPTION="--build-all"
            if [ "$NO_REBUILD" -eq 1 ]; then
                echo "▶️ Run 1: Restarting nodes without rebuilding..."
                BUILD_OPTION=""
            else
                echo "▶️ Run 1: Rebuilding and fresh restarting nodes..."
            fi
            if [ -n "$RAMDISK_OPTION" ]; then
                echo "▶️ Sử dụng RAM Disk cho Database..."
            fi
            "$MTN_CONSENSUS_ROOT/metanode/scripts/mtn-orchestrator.sh" restart --fresh $BUILD_OPTION $RAMDISK_OPTION
            echo "⏳ Waiting 10s for nodes to stabilize..."
            sleep 10
        fi
    else
        echo "▶️ Run $i: Running load test without restarting nodes..."
        sleep 1
    fi
    
    # Run the spam test
    if [ -n "$INVENTORY" ]; then
        ./tps_spam.sh 10 10000 4000 --inventory "$INVENTORY" > "run_output_raw_${i}.log"
    else
        ./tps_spam.sh 10 10000 4000 > "run_output_raw_${i}.log"
    fi
    
    # Wait for chain to become idle and analyze
    if [ -n "$INVENTORY" ]; then
        ./tps_analyze.sh --wait-idle --inventory "$INVENTORY" >> "run_output_raw_${i}.log"
    else
        ./tps_analyze.sh --wait-idle >> "run_output_raw_${i}.log"
    fi

    sed -E 's/\x1b\[[0-9;]*[a-zA-Z]//g' "run_output_raw_${i}.log" > "run_output_${i}.log"
    rm -f "run_output_raw_${i}.log"
    
    # Extract important metrics
    TPS=$(grep "SYSTEM TPS:" "run_output_${i}.log" | awk -F'~' '{print $2}' | awk '{print $1}')
    TOTAL_SENT=$(grep "Injected" "run_output_${i}.log" | awk '{print $4}' | xargs)
    TOTAL_IN_BLOCKS=$(grep "TX trong blocks:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    MAX_TX=$(grep "Max TXs/block:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    SUCCESS_RATE="N/A (No Verify Mode)"
    TIME=$(grep "Thời gian xử lý:" "run_output_${i}.log" | awk -F':' '{print $2}' | xargs)
    FORK_SAFE=$(grep "HỆ THỐNG KHÔNG FORK" "run_output_${i}.log" | wc -l)
    
    # Detailed stage timings and TPS
    REAL_TPS=$(grep "TPS Thực Thi (Real Exec):" "run_output_${i}.log" | awk -F'~' '{print $2}' | awk '{print $1}')
    GO_TPS=$(grep "TPS Xử Lý Go (Virtual + Real):" "run_output_${i}.log" | awk -F'~' '{print $2}' | awk '{print $1}')
    PIPELINE_TPS=$(grep "TPS Pipeline (V+C+R toàn bộ):" "run_output_${i}.log" | awk -F'~' '{print $2}' | awk '{print $1}')
    
    V_EXEC=$(grep "Bước Chạy Giả (Virtual Exec):" "run_output_${i}.log" | cut -d':' -f2- | xargs)
    CONSENSUS=$(grep "Bước Đồng Thuận (Consensus):" "run_output_${i}.log" | cut -d':' -f2- | xargs)
    R_EXEC=$(grep "Bước Thực Thi (Real Exec):" "run_output_${i}.log" | cut -d':' -f2- | xargs)

    FORK_STATUS="SAFE"
    if [ "$FORK_SAFE" -eq "0" ]; then
        FORK_STATUS="FORKED OR ERROR"
    fi

    # Append to report
    echo "Run $i:" >> $OUTPUT_REPORT
    echo "  - TPS (Classic/Block TS): $TPS" >> $OUTPUT_REPORT
    echo "  - TPS (Real Exec Only):  $REAL_TPS" >> $OUTPUT_REPORT
    echo "  - TPS (Virtual+Real Go): $GO_TPS" >> $OUTPUT_REPORT
    echo "  - TPS (Pipeline V+C+R):  $PIPELINE_TPS" >> $OUTPUT_REPORT
    echo "  - Virtual Exec Duration: $V_EXEC" >> $OUTPUT_REPORT
    echo "  - Consensus Duration:    $CONSENSUS" >> $OUTPUT_REPORT
    echo "  - Real Exec Duration:     $R_EXEC" >> $OUTPUT_REPORT
    echo "  - TXs Sent: $TOTAL_SENT" >> $OUTPUT_REPORT
    echo "  - TXs in Blocks: $TOTAL_IN_BLOCKS" >> $OUTPUT_REPORT
    echo "  - Max TXs/block: $MAX_TX" >> $OUTPUT_REPORT
    echo "  - Success Rate: $SUCCESS_RATE" >> $OUTPUT_REPORT
    echo "  - Time (Wall Clock): $TIME" >> $OUTPUT_REPORT
    echo "  - Fork Status: $FORK_STATUS" >> $OUTPUT_REPORT
    echo "  - Detailed Block Commits:" >> $OUTPUT_REPORT
    sed -n '/CHI TIẾT TỪNG BLOCK/,/TỔNG KẾT/p' "run_output_${i}.log" | grep -v -E 'CHI TIẾT TỪNG BLOCK|TỔNG KẾT|╠════|║' | sed 's/^/    /' >> $OUTPUT_REPORT
    echo "--------------------------" >> $OUTPUT_REPORT
    
    echo "Finished Run $i/$RUNS -> TPS (Real Exec): $REAL_TPS | TPS (Pipeline): $PIPELINE_TPS | TPS (Wall): $TPS"
    
    # Optional: small sleep between runs
    sleep 5
done

echo "Tất cả $RUNS lần chạy đã hoàn thành! Xem kết quả tại $OUTPUT_REPORT"
