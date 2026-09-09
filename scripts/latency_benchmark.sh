#!/bin/bash
# Latency Benchmark Script for Metanode
# This script compiles and runs the E2E TPS Benchmark tool.

set -e

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$(dirname "$DIR")"
TOOL_DIR="$ROOT_DIR/execution/cmd/tool/tps_benchmark_e2e"

echo "=========================================================="
echo "🚀 Building TPS Benchmark Tool..."
echo "=========================================================="
cd "$TOOL_DIR"
go build -o "$ROOT_DIR/scripts/tps_benchmark_e2e" .
cd "$ROOT_DIR/scripts"

# Default parameters
NUM_TXS=${1:-1000}
ROUNDS=${2:-1}
NODES=${3:-"http://127.0.0.1:8757"}
ACCOUNTS=${4:-1000}
WORKERS=${5:-20}
SETTLE_SECS=${6:-60}

echo "=========================================================="
echo "📊 Running Latency & TPS Benchmark"
echo "=========================================================="
echo "Transactions per round: $NUM_TXS"
echo "Rounds: $ROUNDS"
echo "Nodes: $NODES"
echo "Accounts: $ACCOUNTS"
echo "Workers/node: $WORKERS"
echo "Settle timeout (s): $SETTLE_SECS"
echo "=========================================================="

./tps_benchmark_e2e \
  -nodes="$NODES" \
  -accounts=$ACCOUNTS \
  -txs=$NUM_TXS \
  -workers=$WORKERS \
  -settle=$SETTLE_SECS \
  -rounds=$ROUNDS \
  -verify-forks=false \
  -cooldown=5 \
  -chain-id=1337 

echo "✅ Benchmark complete. Check the JSON report in scripts directory."
