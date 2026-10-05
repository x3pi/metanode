#!/bin/bash
# End-to-end automated test runner for 4-validator Mysticeti co-attestation suite.
# Drives gen_env.py, run_env.sh, and e2e_coattest_4val with full isolation on port range 31xxx.
# Usage: ./test_coattest_4val.sh [BASE_DIR]
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
EXECUTION_ROOT="$(cd "$HERE/../../.." && pwd)"
BASE="${1:-/tmp/gate_4val_e2e}"
BIN_DIR="/tmp/e2e_bins"

echo "================================================================="
echo "🛠️ 4-Validator Co-Attestation E2E Suite"
echo "   Base Dir: $BASE"
echo "   Bin Dir:  $BIN_DIR"
echo "================================================================="

# Ensure binaries exist
mkdir -p "$BIN_DIR"
if [ ! -f "$BIN_DIR/parent_chain" ] || [ ! -f "$BIN_DIR/simple_chain" ]; then
    echo "📦 Building parent_chain and simple_chain binaries..."
    ( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/parent_chain" ./cmd/parent_chain )
    ( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/simple_chain" ./cmd/simple_chain )
fi

# Build e2e_coattest_4val tool
echo "📦 Building e2e_coattest_4val tool..."
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/e2e_coattest_4val" ./cmd/tool/e2e_coattest_4val )

# Clean up any previous test state
if [ -d "$BASE" ]; then
    echo "🧹 Cleaning up previous test directory $BASE..."
    "$HERE/run_env.sh" "$BASE" stop 2>/dev/null || true
    rm -rf "$BASE"
fi

# 1. Generate 4-validator environment
echo "⚙️ Generating 4-validator Mysticeti environment..."
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

# 3. Run E2E co-attestation test suite
REPORT="$BASE/report_coattest.md"
echo "🧪 Running co-attestation test suite..."
"$BIN_DIR/e2e_coattest_4val" -env "$BASE/env.json" -script "$HERE/run_env.sh" -report "$REPORT"

echo "================================================================="
echo "✅ 4-Validator Co-Attestation E2E Suite Completed Successfully!"
echo "   Report written to: $REPORT"
echo "================================================================="
