#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🚀 Starting Metanode Single Chain (Chain ID 1337)..."

METANODE_BIN="/home/abc/chain-n/metanode/target/release/metanode"
SIMPLE_CHAIN_BIN="/home/abc/chain-n/metanode/execution/cmd/simple_chain/simple_chain"

if [ ! -f "$SIMPLE_CHAIN_BIN" ]; then
    echo "🔨 Building simple_chain Go binary..."
    (cd "/home/abc/chain-n/metanode/execution/cmd/simple_chain" && go build -o simple_chain .)
fi

# PPROF_BASE_PORT: pprof ports default to 7060-7063, NOT 6060-6063. The
# ansible-deployed Root Anchor / private-chain cluster (deploy/ansible,
# deploy/ansible_private_chains -- a completely separate cluster, chain
# 991/101/102, not this fixture's chain 1337) hardcodes 6060-6063 for its
# own 4 nodes' pprof ports. Both clusters can legitimately be running at
# once on the same machine (found live 2026-09-03: they were), and a pprof
# bind conflict makes simple_chain's RPC server call fatal.Exit() --
# 3 of 4 nodes silently crashed on startup with only node-0 left alive,
# which can never reach consensus alone, so the chain just sat frozen at
# block 0 with no error visible from the RPC side. Override with
# PPROF_BASE_PORT=6060 (or whatever) only if you know nothing else on the
# machine is using it.
PPROF_BASE_PORT="${PPROF_BASE_PORT:-7060}"

echo "  → Starting Node-0 (RPC: http://127.0.0.1:20000)..."
(cd "$DIR/node-0" && "$SIMPLE_CHAIN_BIN" --config config.json --debug --pprof-addr=127.0.0.1:$((PPROF_BASE_PORT+0)) > logs/node-0.log 2>&1 & echo $! > node-0.pid)

echo "  → Starting Node-1 (RPC: http://127.0.0.1:20001)..."
(cd "$DIR/node-1" && "$SIMPLE_CHAIN_BIN" --config config.json --debug --pprof-addr=127.0.0.1:$((PPROF_BASE_PORT+1)) > logs/node-1.log 2>&1 & echo $! > node-1.pid)

echo "  → Starting Node-2 (RPC: http://127.0.0.1:20002)..."
(cd "$DIR/node-2" && "$SIMPLE_CHAIN_BIN" --config config.json --debug --pprof-addr=127.0.0.1:$((PPROF_BASE_PORT+2)) > logs/node-2.log 2>&1 & echo $! > node-2.pid)

echo "  → Starting Node-3 (RPC: http://127.0.0.1:20003)..."
(cd "$DIR/node-3" && "$SIMPLE_CHAIN_BIN" --config config.json --debug --pprof-addr=127.0.0.1:$((PPROF_BASE_PORT+3)) > logs/node-3.log 2>&1 & echo $! > node-3.pid)

echo "✅ Single Chain 1337 started successfully!"
echo "   Node-0 RPC URL: http://127.0.0.1:18545"
echo "   Chain ID: 1337"
echo "   Check logs in $DIR/node-0/logs/node-0.log"
