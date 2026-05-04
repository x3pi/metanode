#!/bin/bash
# Script to parse and display duration of Epoch Transitions and Snapshot creations.

if [ -z "$1" ]; then
    echo "Usage: ./parse_stalls.sh <node_id>"
    echo "Example: ./parse_stalls.sh 0"
    exit 1
fi

NODE_ID=$1
LOG_DIR="$HOME/chain-n/metanode/consensus/metanode/logs/node_${NODE_ID}"

GO_LOG_DIR="${LOG_DIR}/go-master"
RUST_LOG="${LOG_DIR}/go-master-stdout.log"

echo "=========================================================="
echo "📊 Analyzing Stall History for Node ${NODE_ID}"
echo "=========================================================="

echo ""
echo "🕒 [1] Snapshot Creation Times (from Go Execution logs):"
echo "----------------------------------------------------------"
# Search in all epoch directories under go-master
find "$GO_LOG_DIR" -name "*.log" -type f -exec grep -H "\[BACKUP\] Persisted BackUpDb" {} + | sed -E 's/.*App\.log://' | awk -F "took " '{print $1 " | Took: " $2}' | sed 's/)//' | sort
if [ ${PIPESTATUS[0]} -ne 0 ]; then
    echo "No snapshot logs found or logs directory not accessible."
fi

echo ""
echo "🕒 [2] Epoch Transition Times (from Rust Consensus logs):"
echo "----------------------------------------------------------"
if [ -f "$RUST_LOG" ]; then
    grep "\[STATE MANAGER\] Completed Epoch" "$RUST_LOG" | awk -F "Completed " '{print $2}' | awk '{print $1 " " $2 " " $3 " | " $4 " " $5 " | " $6 " " $7}'
else
    echo "Rust log file not found at $RUST_LOG"
fi

echo ""
echo "=========================================================="
echo "💡 The durations shown indicate how long the system was"
echo "   engaged in consensus-critical synchronous operations."
echo "=========================================================="
