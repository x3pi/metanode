#!/bin/bash
# End-to-end automated test runner for P0-6 Multi-Validator Real Production Evidence Suite.
# Runs 4 isolated Mysticeti validators on port range 31xxx.
# Usage: ./test_p06_evidence.sh [BASE_DIR]
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
EXECUTION_ROOT="$(cd "$HERE/../../.." && pwd)"
REPO_ROOT="$(cd "$EXECUTION_ROOT/.." && pwd)"
BASE="${1:-/tmp/gate_4val_p06}"
BIN_DIR="/tmp/p06_bins"

echo "================================================================="
echo "🚀 P0-6 Multi-Validator Real Production Evidence Suite"
echo "   Base Dir: $BASE"
echo "   Bin Dir:  $BIN_DIR"
echo "================================================================="

mkdir -p "$BIN_DIR"
echo "📦 Building parent_chain, simple_chain, and p06_evidence binaries..."
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/parent_chain" ./cmd/parent_chain )
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/simple_chain" ./cmd/simple_chain )
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/p06_evidence" ./cmd/tool/p06_evidence )

# Clean up any previous test state
if [ -d "$BASE" ]; then
    echo "🧹 Cleaning up previous test directory $BASE..."
    "$HERE/run_env.sh" "$BASE" stop 2>/dev/null || true
    rm -rf "$BASE"
fi

# 1. Generate 4-validator environment
echo "⚙️ Generating 4-validator Mysticeti environment on port range 31xxx..."
python3 "$HERE/gen_env.py" --validators 4 --bin "$BIN_DIR" --base "$BASE"

# 2. Start environment
echo "🚀 Starting 4-validator cluster..."
"$HERE/run_env.sh" "$BASE" start

# Cleanup trap to ensure cluster is always cleanly stopped
cleanup() {
    echo "🛑 Stopping cluster..."
    "$HERE/run_env.sh" "$BASE" stop || true
}
trap cleanup EXIT

# 3. Run P0-6 Evidence Suite
REPORT="$REPO_ROOT/note/p0_6_multivalidator_evidence.md"
echo "🧪 Running P0-6 Evidence Suite..."
"$BIN_DIR/p06_evidence" -env "$BASE/env.json" -script "$HERE/run_env.sh" -report "$REPORT"

echo "================================================================="
echo "✅ P0-6 Evidence Suite Completed Successfully!"
echo "   Report updated: $REPORT"
echo "================================================================="
