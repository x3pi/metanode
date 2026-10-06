#!/bin/bash
# End-to-end automated test runner for 4-validator Mysticeti cross-chain credit co-attestation suite (P0-2).
# Uses an isolated scratchpad directory on port range 31xxx.
# Usage: ./test_cross_chain_coattest.sh [BASE_DIR]
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
EXECUTION_ROOT="$(cd "$HERE/../../.." && pwd)"

DEFAULT_SCRATCH="${E2E_SCRATCH:-${TMPDIR:-/tmp}/metanode_e2e}"
BASE="${1:-$DEFAULT_SCRATCH/cross_chain_4val_e2e}"
BIN_DIR="$DEFAULT_SCRATCH/e2e_bins"

echo "================================================================="
echo "🛠️ Cross-Chain Credit Co-Attestation E2E Suite (P0-2)"
echo "   Base Dir: $BASE"
echo "   Bin Dir:  $BIN_DIR"
echo "================================================================="

# Pre-cleanup: stop a previous run of THIS base through its recorded PIDs only (never pkill by pattern or port).
if [ -d "$BASE" ]; then
    "$HERE/run_env.sh" "$BASE" stop 2>/dev/null || true
fi

# Always rebuild: ensure binaries match the exact latest source code
mkdir -p "$BIN_DIR"
echo "📦 Building parent_chain, simple_chain, and e2e_cross_chain_coattest binaries..."
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/parent_chain" ./cmd/parent_chain )
touch "$EXECUTION_ROOT/pkg/nomt_ffi/bridge.go"
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/simple_chain" ./cmd/simple_chain )
( cd "$EXECUTION_ROOT" && go build -o "$BIN_DIR/e2e_cross_chain_coattest" ./cmd/tool/e2e_cross_chain_coattest )

# Clean up previous test directory
if [ -d "$BASE" ]; then
    echo "🧹 Cleaning up previous test directory $BASE..."
    "$HERE/run_env.sh" "$BASE" stop 2>/dev/null || true
    rm -rf "$BASE"
fi

# 1. Generate 4-validator environment (includes exec2 and dynamic float conservation accounts)
echo "⚙️ Generating 4-validator Mysticeti + exec2 environment..."
python3 "$HERE/gen_env.py" --validators 4 --bin "$BIN_DIR" --base "$BASE"

# 2. Start environment
echo "🚀 Starting 4-validator cluster + exec2..."
"$HERE/run_env.sh" "$BASE" start

# Cleanup trap to ensure cluster is always cleanly stopped
cleanup() {
    echo "🛑 Stopping cluster..."
    "$HERE/run_env.sh" "$BASE" stop || true
    # Extra check: make sure no leftover process remains
    ps aux | grep "$BIN_DIR" | grep -v grep | awk '{print $2}' | xargs -r kill -9 2>/dev/null || true
}
trap cleanup EXIT

# 3. Run E2E cross-chain credit co-attestation test suite
REPORT="$BASE/report_cross_chain_coattest.md"
echo "🧪 Running cross-chain credit co-attestation test suite..."
"$BIN_DIR/e2e_cross_chain_coattest" -env "$BASE/env.json" -script "$HERE/run_env.sh" -report "$REPORT"

echo "================================================================="
echo "✅ Cross-Chain Credit Co-Attestation E2E Suite Completed Successfully!"
echo "   Report written to: $REPORT"
echo "================================================================="
