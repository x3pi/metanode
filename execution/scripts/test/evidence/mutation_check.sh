#!/usr/bin/env bash
# ==============================================================================
# Fixed Regression Suite & Fuzz Mutation Runner for verify_evidence.py
# 1. Runs deterministic regression mutations on known edge cases.
#    (Note: This fixed set is for regression only and does not represent overall coverage).
# 2. Runs randomized fuzz mutation coverage via fuzz_mutation_check.py.
# ==============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
cd "$REPO_ROOT"

SEED="${1:-42}"
MODE="${2:---all}"

VERIFY_PY="execution/scripts/test/evidence/verify_evidence.py"
FUZZ_PY="execution/scripts/test/evidence/fuzz_mutation_check.py"
TMP_DIR=$(mktemp -d /tmp/mutation_check_XXXXXX)
trap 'rm -rf "$TMP_DIR"' EXIT

DUR_DIR="note/evidence/perf_nomt_durability_20261007"
DUR_REP="note/perf_nomt_durability_20261007.md"

B1_DIR="note/evidence/perf_nomt_b1_20261007"
B1_REP="note/perf_nomt_b1_20261007.md"

RSS_DIR="note/evidence/perf_rss_20261007"
RSS_REP="note/perf_rss_investigation_20261007.md"

TOTAL=0
CAUGHT=0
MISSED=0

run_mutation() {
    local target_dir="$1"
    local original_rep="$2"
    local desc="$3"
    local search_str="$4"
    local replace_str="$5"

    TOTAL=$((TOTAL + 1))
    local tmp_rep="$TMP_DIR/rep_${TOTAL}.md"
    cp "$original_rep" "$tmp_rep"

    # Verify search string exists in original file
    if ! grep -qF "$search_str" "$tmp_rep"; then
        echo "❌ [ERROR] Target string not found in $original_rep: '$search_str'"
        exit 1
    fi

    # Perform single replacement
    python3 -c "
with open('$tmp_rep', 'r', encoding='utf-8') as f:
    c = f.read()
c = c.replace('''$search_str''', '''$replace_str''', 1)
with open('$tmp_rep', 'w', encoding='utf-8') as f:
    f.write(c)
"

    # Run verifier against mutated report
    set +e
    output=$(python3 "$VERIFY_PY" "$target_dir" --report "$tmp_rep" 2>&1)
    exit_code=$?
    set -e

    if [ $exit_code -ne 0 ]; then
        CAUGHT=$((CAUGHT + 1))
        echo "  [$TOTAL] CAUGHT: $desc"
    else
        MISSED=$((MISSED + 1))
        echo "  [$TOTAL] ❌ MISSED: $desc"
        echo "$output"
    fi
}

echo "=================================================================="
echo "🧪 PART 1: FIXED REGRESSION MUTATION SUITE"
echo "=================================================================="

# 0. Baseline Clean Checks (Must all pass / exit 0)
echo "▶️ [BASELINE] Checking unmutated reports..."
python3 "$VERIFY_PY" "$DUR_DIR" --strict >/dev/null && echo "  • Durability baseline: PASS (exit 0)"
python3 "$VERIFY_PY" "$B1_DIR" --strict >/dev/null && echo "  • B1 baseline:         PASS (exit 0)"
python3 "$VERIFY_PY" "$RSS_DIR" --strict >/dev/null && echo "  • RSS baseline:        PASS (exit 0)"

echo ""
echo "▶️ [MUTATIONS] Executing fixed regression mutation cases..."

# --- Durability Mutations ---
run_mutation "$DUR_DIR" "$DUR_REP" \
    "Durability: Mutate CheckBlock #310 -> #999" \
    "#310" "#999"

run_mutation "$DUR_DIR" "$DUR_REP" \
    "Durability: Mutate BlockHash 0xa50c... -> 0xdeadbeef..." \
    "0xa50c39fa15e920b2..." "0xdeadbeef15e920b2..."

run_mutation "$DUR_DIR" "$DUR_REP" \
    "Durability: Mutate QuorumPause PAUSED_AT_#318 -> PAUSED_AT_#999" \
    "PAUSED_AT_#318" "PAUSED_AT_#999"

run_mutation "$DUR_DIR" "$DUR_REP" \
    "Durability: Mutate text claim dual-kill count (5 vòng -> 4 vòng)" \
    "khi mất Quorum (5 vòng" "khi mất Quorum (4 vòng"

run_mutation "$DUR_DIR" "$DUR_REP" \
    "Durability: Mutate text claim leader-kill count (6 vòng -> 5 vòng)" \
    "Leader (\`val0\`) bị hạ gục (6 vòng" "Leader (\`val0\`) bị hạ gục (5 vòng"

run_mutation "$DUR_DIR" "$DUR_REP" \
    "Durability: Mutate table row Verdict PASS -> FAIL" \
    "| **R1** | \`val1\` | 0.05s | unknown_or_between_commits | N/A | 0 | None | #307 | #310 | \`0xa50c39fa15e920b2...\` | \`0x550d69db0b123746...\` | **PASS** |" \
    "| **R1** | \`val1\` | 0.05s | unknown_or_between_commits | N/A | 0 | None | #307 | #310 | \`0xa50c39fa15e920b2...\` | \`0x550d69db0b123746...\` | **FAIL** |"

# --- B1 Microbenchmark Mutations ---
run_mutation "$B1_DIR" "$B1_REP" \
    "B1: Mutate percentage difference +1.62% -> +99.9%" \
    "+1.62%" "+99.9%"

run_mutation "$B1_DIR" "$B1_REP" \
    "B1: Mutate table row verdict PASS -> FAIL" \
    "| **100** | 5.495 ± 0.649 ms | 5.584 ± 0.254 ms | 1.016 | +1.62% | t = 0.34 (df = 7.8, p > 0.05) | B ≤ 6.105 ms | **PASS** |" \
    "| **100** | 5.495 ± 0.649 ms | 5.584 ± 0.254 ms | 1.016 | +1.62% | t = 0.34 (df = 7.8, p > 0.05) | B ≤ 6.105 ms | **FAIL** |"

run_mutation "$B1_DIR" "$B1_REP" \
    "B1: Remove evidence tag from table row 53" \
    "\`evidence:b1_microbenchmark_a_vs_b#B1_100_A_mean,B1_100_B_mean,B1_100_diff_pct,B1_100_threshold,B1_100_t,B1_100_df\`" ""

run_mutation "$B1_DIR" "$B1_REP" \
    "B1: Remove evidence tag from iteration row 61" \
    "| **R1** | 6.556 ms / 5.462 ms (0.833) | 19.920 ms / 18.216 ms (0.914) | 65.820 ms / 69.098 ms (1.050) | \`evidence:b1_microbenchmark_a_vs_b\` |" \
    "| **R1** | 6.556 ms / 5.462 ms (0.833) | 19.920 ms / 18.216 ms (0.914) | 65.820 ms / 69.098 ms (1.050) | |"

# --- RSS Investigation Mutations ---
run_mutation "$RSS_DIR" "$RSS_REP" \
    "RSS: Mutate HeapAlloc slope m = +98.89 -> +12.34" \
    "\$m = +98.89\$" "\$m = +12.34\$"

run_mutation "$RSS_DIR" "$RSS_REP" \
    "RSS: Mutate EthHashMap Wave 8 InUse 47.81 -> 99.99 MB" \
    "Wave 8: 47.81 MB InUse" "Wave 8: 99.99 MB InUse"

run_mutation "$RSS_DIR" "$RSS_REP" \
    "RSS: Mutate TxHash Wave 8 InUse 35.06 -> 88.88 MB" \
    "Wave 8: 35.06 MB InUse" "Wave 8: 88.88 MB InUse"

run_mutation "$RSS_DIR" "$RSS_REP" \
    "RSS: Mutate EthHashMap Wave 1 InUse 5.98 -> 1.23 MB" \
    "Wave 1: 5.98 MB InUse" "Wave 1: 1.23 MB InUse"

echo ""
echo "=================================================================="
echo "📊 FIXED REGRESSION SUMMARY: $CAUGHT / $TOTAL CAUGHT"
echo "=================================================================="

if [ "$MISSED" -ne 0 ]; then
    echo "❌ $MISSED regression mutations were missed!"
    exit 1
fi

echo ""
echo "=================================================================="
echo "🎲 PART 2: RANDOMIZED FUZZ MUTATION AUDIT (Seed: $SEED, Mode: $MODE)"
echo "=================================================================="
if [ "$MODE" = "--all" ]; then
    python3 "$FUZZ_PY" --seed "$SEED" --all
else
    python3 "$FUZZ_PY" --seed "$SEED" --sample "$MODE"
fi

