#!/usr/bin/env bash
# test_deterministic_genesis.sh — Regression test for gen_single_chain.py's deterministic-genesis
# mode (--initial-supply-wallet/--initial-supply, 2026-09-04, see
# note/eurozone_unified_native_coin_plan.md mục 5). No real Root Anchor needed -- spins up a
# minimal mock JSON-RPC server that answers exactly the 2 eth_call selectors this mode needs
# (getAllocation, getChainRegistry), so this is safe and fast to re-run anywhere, anytime.
#
# Covers:
#   1. Legacy mode (no new flags) is completely unchanged -- still funds every account arbitrarily.
#   2. Deterministic mode, matching input: verifies against the mock, produces a genesis whose
#      ENTIRE native-coin supply is exactly 1 account == the verified amount, nothing else funded.
#   3. Deterministic mode, mismatched --initial-supply: fails closed (non-zero exit), writes no
#      genesis.json.
#
# Usage: ./test_deterministic_genesis.sh
set -u
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="$(mktemp -d)"
MOCK_PORT=0
MOCK_PID=""
FAILURES=0

cleanup() {
    [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null
    rm -rf "$WORK_DIR"
    rm -f "$SCRIPT_DIR"/genesis-559*.json 2>/dev/null
}
trap cleanup EXIT

pass() { echo "  ✅ $1"; }
fail() { echo "  ❌ $1"; FAILURES=$((FAILURES + 1)); }

# Pick a free port for the mock server.
MOCK_PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()")

CHAIN_ID=559001
AMOUNT=424242000000000000000
WALLET=0x9999999999999999999999999999999999999999

cat > "$WORK_DIR/mock_root_anchor.py" << PYEOF
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
SEL_ALLOC = "5a5804b3"
SEL_REGISTRY = "e4dad163"
AMOUNT = $AMOUNT
WALLET = "${WALLET#0x}"
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(n))
        result = "0x"
        if body.get("method") == "eth_call":
            data = body["params"][0]["data"]
            sel = data[2:10]
            if sel == SEL_ALLOC:
                result = "0x" + format(AMOUNT, "064x")
            elif sel == SEL_REGISTRY:
                words = [format(1, "064x")] + ["0"*64]*10 + [WALLET.rjust(64, "0")] + ["0"*64]
                result = "0x" + "".join(words)
        resp = json.dumps({"jsonrpc": "2.0", "id": body.get("id", 1), "result": result}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)
if __name__ == "__main__":
    import sys
    HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PYEOF

python3 "$WORK_DIR/mock_root_anchor.py" "$MOCK_PORT" &
MOCK_PID=$!
sleep 1

echo "═══════════════════════════════════════════════════════════════"
echo "🧪 TEST 1: legacy mode (no new flags) -- must be unchanged"
echo "═══════════════════════════════════════════════════════════════"
python3 "$SCRIPT_DIR/gen_single_chain.py" --chain-id 559001 --validators 1 \
    --output-dir "$WORK_DIR/legacy" --no-example-alloc --dev-accounts 0 > "$WORK_DIR/legacy.log" 2>&1
if [ $? -eq 0 ] && [ -f "$WORK_DIR/legacy/genesis.json" ]; then
    COUNT=$(python3 -c "import json; print(len(json.load(open('$WORK_DIR/legacy/genesis.json'))['alloc']))")
    NONZERO=$(python3 -c "import json; d=json.load(open('$WORK_DIR/legacy/genesis.json')); print(sum(1 for a in d['alloc'] if int(a['balance'])>0))")
    if [ "$NONZERO" -gt 0 ]; then
        pass "legacy mode: $COUNT alloc accounts, $NONZERO with nonzero balance (as before)"
    else
        fail "legacy mode: expected nonzero-balance accounts, got none -- regression?"
    fi
else
    fail "legacy mode: gen_single_chain.py failed (see $WORK_DIR/legacy.log)"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "🧪 TEST 2: deterministic mode, matching --initial-supply -- must produce exactly 1 funded account"
echo "═══════════════════════════════════════════════════════════════"
python3 "$SCRIPT_DIR/gen_single_chain.py" --chain-id "$CHAIN_ID" --validators 1 \
    --output-dir "$WORK_DIR/det" --no-example-alloc --dev-accounts 0 \
    --root-anchor-rpc "http://127.0.0.1:$MOCK_PORT" \
    --initial-supply-wallet "$WALLET" --initial-supply "$AMOUNT" > "$WORK_DIR/det.log" 2>&1
if [ $? -eq 0 ] && [ -f "$WORK_DIR/det/genesis.json" ]; then
    python3 - "$WORK_DIR/det/genesis.json" "$WALLET" "$AMOUNT" << 'PYCHECK'
import json, sys
d = json.load(open(sys.argv[1]))
wallet, amount = sys.argv[2].lower(), int(sys.argv[3])
funded = [a for a in d["alloc"] if int(a["balance"]) > 0]
total = sum(int(a["balance"]) for a in d["alloc"])
ok = True
if len(funded) != 1:
    print(f"  ❌ expected exactly 1 funded account, got {len(funded)}"); ok = False
elif funded[0]["address"].lower() != wallet:
    print(f"  ❌ funded account is {funded[0]['address']}, want {wallet}"); ok = False
elif int(funded[0]["balance"]) != amount:
    print(f"  ❌ funded balance is {funded[0]['balance']}, want {amount}"); ok = False
if total != amount:
    print(f"  ❌ total genesis supply is {total}, want exactly {amount}"); ok = False
if ok:
    print(f"  ✅ deterministic mode: exactly 1 funded account ({wallet}), total supply == {amount}")
sys.exit(0 if ok else 1)
PYCHECK
    [ $? -ne 0 ] && FAILURES=$((FAILURES + 1))
else
    fail "deterministic mode (matching): gen_single_chain.py failed (see $WORK_DIR/det.log)"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "🧪 TEST 3: deterministic mode, WRONG --initial-supply -- must fail closed, write no genesis"
echo "═══════════════════════════════════════════════════════════════"
python3 "$SCRIPT_DIR/gen_single_chain.py" --chain-id "$CHAIN_ID" --validators 1 \
    --output-dir "$WORK_DIR/det_bad" --no-example-alloc --dev-accounts 0 \
    --root-anchor-rpc "http://127.0.0.1:$MOCK_PORT" \
    --initial-supply-wallet "$WALLET" --initial-supply 999 > "$WORK_DIR/det_bad.log" 2>&1
RC=$?
if [ $RC -ne 0 ] && [ ! -f "$WORK_DIR/det_bad/genesis.json" ]; then
    pass "mismatch correctly rejected (exit $RC), no genesis.json written"
else
    fail "mismatch NOT rejected (exit $RC, genesis.json exists=$([ -f "$WORK_DIR/det_bad/genesis.json" ] && echo yes || echo no)) -- SECURITY REGRESSION"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
if [ "$FAILURES" -eq 0 ]; then
    echo "🎉 ALL TESTS PASSED"
    exit 0
else
    echo "❌ $FAILURES TEST(S) FAILED"
    exit 1
fi
