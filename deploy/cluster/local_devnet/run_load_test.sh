#!/usr/bin/env bash
# run_load_test.sh — one-command, repeatable TPS/latency load test recipe.
#
# Rebuilds simple_chain from the current source tree, resets this fixture's
# 4-node local devnet (chain 1337) to a clean genesis, funds the exact
# deterministic accounts tps_benchmark_e2e signs from, then runs the E2E
# throughput benchmark (settlement-verified, multi-node fork-checked) and
# a per-tx latency probe. Exists so re-verifying "does the mempool/consensus
# pipeline still deliver 100% of submitted transactions at scale" (the same
# question e.g. 1M/4M-tx runs answered live on 2026-09-02/03 while chasing
# the TxPayloadCache/localNonceFloor bug classes -- see git history and
# note/report/ for that history) is a single command, not a bespoke manual
# session every time.
#
# Usage:
#   ./run_load_test.sh [TXS] [ACCOUNTS] [ROUNDS]
#
# Defaults to TXS=1000000 ACCOUNTS=200 ROUNDS=1. Examples:
#   ./run_load_test.sh                  # 1M txs, the historical benchmark scale
#   ./run_load_test.sh 4000000 300 1    # 4M txs (the other historically-verified scale)
#   ./run_load_test.sh 20000 50 1       # quick smoke run (~seconds)
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXEC_DIR="$(cd "$DIR/../../../execution" && pwd)"

TXS="${1:-1000000}"
ACCOUNTS="${2:-200}"
ROUNDS="${3:-1}"

RPC="http://127.0.0.1:18545"
# All 4 nodes' RPCs (node-1..3 confirmed at 18546-18548 in their own
# config.json) -- passed to tps_benchmark_e2e's -nodes so -verify-forks
# actually compares real state across independent nodes instead of only
# ever talking to node-0 alone.
ALL_RPCS="http://127.0.0.1:18545,http://127.0.0.1:18546,http://127.0.0.1:18547,http://127.0.0.1:18548"
CHAIN_ID=1337
# "Sender (A0)" from this fixture's own dev_accounts.json -- pre-funded in
# genesis.json, used only to seed the deterministic tps-bench-e2e accounts.
FUNDER_KEY="9f61a687fbeac9e11d5cfce0fe2dcec035cb2b21eb9c584d8cf90696ce2fc370"

echo "═══════════════════════════════════════════════════════════════"
echo "🚀 Load test: ${TXS} txs, ${ACCOUNTS} accounts, ${ROUNDS} round(s)"
echo "═══════════════════════════════════════════════════════════════"

PROJECT_ROOT="$(cd "$DIR/../../.." && pwd)"

# The Go binary links the Rust consensus engine statically via cgo, whose
# LDFLAGS point at consensus/metanode/target/release/libmetanode.a --
# NOT the workspace-root target/release/ that `cargo build` from anywhere
# in this repo actually writes to (consensus/metanode is a workspace
# *member*, sharing the root target/ dir; its own target/ subdirectory is
# a stale leftover, last touched 2026-07/08). Only build_release.sh (the
# ansible-driven production path) knows to copy the fresh artifact into
# that expected location; a bare `go build` here silently links against
# whatever old snapshot happens to already be sitting there. Found live
# 2026-09-03 chasing a consensus-timing change that a first `go build`
# alone did not pick up. Rebuild + copy explicitly so this recipe never
# silently tests stale Rust code again.
echo "🔨 Building Rust consensus engine (release)..."
(cd "$PROJECT_ROOT" && cargo build --release -p metanode)
mkdir -p "$PROJECT_ROOT/consensus/metanode/target/release"
cp -p "$PROJECT_ROOT/target/release/libmetanode.a" "$PROJECT_ROOT/consensus/metanode/target/release/libmetanode.a"

echo "🔨 Building simple_chain, tps_benchmark_e2e, fund_tps_bench_accounts from current source..."
(cd "$EXEC_DIR/cmd/simple_chain" && go build -o simple_chain .)
(cd "$EXEC_DIR/cmd/tool/tps_benchmark_e2e" && go build -o tps_benchmark_e2e .)
(cd "$EXEC_DIR/cmd/tool/fund_tps_bench_accounts" && go build -o fund_tps_bench_accounts .)
(cd "$EXEC_DIR/cmd/tool/count_onchain_txs" && go build -o count_onchain_txs .)
(cd "$EXEC_DIR/cmd/tool/tps_latency_probe" && go build -o tps_latency_probe . 2>/dev/null || true)

echo "🛑 Stopping any existing local_devnet processes..."
bash "$DIR/stop_single_chain.sh" || true
# Belt-and-suspenders: stop_single_chain.sh only kills what its own pid
# files point to. A pid file can go stale (overwritten by a later start
# attempt while an even older process from a prior manual run still holds
# the port) -- found live 2026-09-03 chasing exactly this. Match on this
# fixture's own pprof port range (see start_single_chain.sh's
# PPROF_BASE_PORT comment for why it's 7060-7063, not 6060-6063 -- that
# range belongs to a completely different, separately-deployed cluster
# that can legitimately be running at the same time) rather than the
# config path: each node actually launches with a bare relative
# "--config config.json" (it cd's into its own directory first), which
# never matches a pattern built from an absolute $DIR path.
pkill -9 -f "simple_chain --config config.json --debug --pprof-addr=127.0.0.1:706" 2>/dev/null || true
sleep 1

echo "🧹 Wiping node data directories for a clean genesis..."
for i in 0 1 2 3; do
    rm -rf "$DIR/node-$i/data"
    mkdir -p "$DIR/node-$i/data"
    rm -f "$DIR/node-$i/node-$i.pid"
done

# Pre-flight port check: simple_chain's RPC server calls fatal.Exit() (Go
# panic, crash.log written) if its pprof listener can't bind -- and
# crucially, that kills ONLY that one node's process, silently, while the
# other 3 keep running (or the run just ends up 1-of-4 alive), which is
# consensus-broken but produces no obvious error anywhere the RPC client
# side would see: the chain just sits frozen at block 0 forever, which
# used to look identical to "still starting up" or "code regression".
# Fail loudly here instead, before wasting a full funding+benchmark cycle
# discovering it after the fact (found live 2026-09-03, chasing exactly
# that false trail).
PPROF_BASE_PORT="${PPROF_BASE_PORT:-7060}"
for port in 18545 18546 18547 18548 20000 20001 20002 20003 \
    $((PPROF_BASE_PORT+0)) $((PPROF_BASE_PORT+1)) $((PPROF_BASE_PORT+2)) $((PPROF_BASE_PORT+3)); do
    if ss -ltn 2>/dev/null | grep -q ":$port "; then
        echo "❌ Port $port is already in use by something else -- refusing to start (would silently crash 1+ nodes)."
        echo "   Check: ss -ltnp | grep :$port"
        exit 1
    fi
done

echo "▶️  Starting fresh 4-node devnet..."
# The deterministic tps-bench-e2e accounts fund_tps_bench_accounts just
# funded are brand new -- never registered a BLS pubkey on-chain, which
# cmd/simple_chain/eth_tx_converter.go's buildMetaTxFromEthTx normally
# requires of every sender on the EnablePrivateGateway speculative path
# ("account ... has no BLS public key registered on-chain", found live
# 2026-09-03 running this exact recipe). SKIP_MEMPOOL_SIG_VERIFY is that
# file's own built-in escape hatch for precisely this devnet/benchmark
# case -- exported here (inherited by the node processes start_single_
# chain.sh launches), never meant to reach a real deployment.
export SKIP_MEMPOOL_SIG_VERIFY=true
bash "$DIR/start_single_chain.sh"

echo "⏳ Waiting for RPC to come up..."
for i in $(seq 1 60); do
    RESP=$(curl -s -X POST "$RPC" -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null || true)
    if echo "$RESP" | grep -q '"result"'; then
        echo "✅ RPC is up: $RESP"
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo "❌ RPC never came up after 60s -- check $DIR/node-0/logs/node-0.log"
        exit 1
    fi
    sleep 1
done

# RPC responding is NOT the same as the 4-node cluster actually reaching
# consensus: a lone node-0 (its 3 peers each crashed on a port conflict,
# see the pre-flight check above) answers eth_blockNumber fine forever
# while stuck at block 0, since it alone can never finalize a block --
# found live 2026-09-03, looked identical to "still starting up" until
# traced to each peer's own crash.log. Confirm the chain is actually
# advancing (not just alive) before trusting it with a funding tx, let
# alone a million-tx benchmark.
echo "⏳ Confirming the chain is actually producing blocks (not just node-0 alone)..."
START_BLOCK_HEX=$(echo "$RESP" | grep -oP '"result":"\K[^"]+')
START_BLOCK=$((START_BLOCK_HEX))
for i in $(seq 1 30); do
    RESP2=$(curl -s -X POST "$RPC" -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null || true)
    CUR_HEX=$(echo "$RESP2" | grep -oP '"result":"\K[^"]+' || true)
    if [ -n "$CUR_HEX" ] && [ $((CUR_HEX)) -gt "$START_BLOCK" ]; then
        echo "✅ Chain is producing blocks (now at $CUR_HEX)"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "❌ Block number never advanced past $START_BLOCK_HEX in 30s -- the 4 nodes likely aren't reaching consensus."
        echo "   Check each node's crash.log (a pprof/RPC port bind conflict crashes that node silently):"
        for n in 0 1 2 3; do
            if [ -f "$DIR/node-$n/crash.log" ]; then
                echo "   --- node-$n/crash.log ---"
                head -8 "$DIR/node-$n/crash.log"
            fi
        done
        exit 1
    fi
    sleep 1
done

echo "💰 Funding ${ACCOUNTS} deterministic tps-bench-e2e accounts..."
"$EXEC_DIR/cmd/tool/fund_tps_bench_accounts/fund_tps_bench_accounts" \
    -node "$RPC" -chain-id "$CHAIN_ID" -key "$FUNDER_KEY" \
    -n "$ACCOUNTS" -amount "100000000000000000000" -wait -wait-timeout 60s

echo "🌉 Running latency probe (50 sequential txs, one account)..."
"$EXEC_DIR/cmd/tool/tps_latency_probe/tps_latency_probe" \
    -node "$RPC" -chain-id "$CHAIN_ID" -key "$FUNDER_KEY" -n 50 || true

# +1: eth_blockNumber returns the current (already-committed) tip -- which
# can still contain the latency probe's own last transaction (confirmed
# live 2026-09-03: it does, every time, since the probe's final tx and this
# query race to land in/after the same block). Counting strictly AFTER the
# tip avoids ever attributing a pre-benchmark setup tx to the benchmark.
BENCH_START_BLOCK_HEX=$(curl -s -X POST "$RPC" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | grep -oP '"result":"\K[^"]+')
BENCH_START_BLOCK=$(( $((BENCH_START_BLOCK_HEX)) + 1 ))

echo "🚀 Running E2E throughput benchmark..."
OUT="$DIR/load_test_report_$(date +%Y%m%d_%H%M%S).json"
"$EXEC_DIR/cmd/tool/tps_benchmark_e2e/tps_benchmark_e2e" \
    -nodes "$ALL_RPCS" -chain-id "$CHAIN_ID" \
    -accounts "$ACCOUNTS" -txs "$TXS" -rounds "$ROUNDS" \
    -workers 20 -settle 120 -verify-forks -out "$OUT"

# tps_benchmark_e2e's own txs_confirmed is bounded by its fixed -settle
# window (120s above) -- at large scale the chain can still be actively
# draining well past that deadline, silently under-reporting real
# delivery (found live 2026-09-03: a 4M-tx run reported 3,312,294
# confirmed at the 120s mark, chain kept going in the background, all
# 4,000,000 had genuinely landed a couple minutes later). Don't guess a
# bigger timeout for whatever scale gets run next -- independently wait
# for the chain to actually go quiet and count for real.
echo ""
echo "🔍 Independently verifying on-chain delivery (waiting for the chain to drain)..."
"$EXEC_DIR/cmd/tool/count_onchain_txs/count_onchain_txs" \
    -node "$RPC" -start "$BENCH_START_BLOCK" -expect "$TXS" \
    -wait-drain -empty-blocks 15 -poll-interval 3s -max-wait 15m

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "📄 Report: $OUT"
echo "═══════════════════════════════════════════════════════════════"
