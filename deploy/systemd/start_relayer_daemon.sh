#!/usr/bin/env bash
# start_relayer_daemon.sh — Starts the cross-chain RelayerDaemon for private chains (101-104) and Root Anchor (9099)
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_PATH="$SCRIPT_DIR/cross_chain_relayer"

if [ ! -f "$BIN_PATH" ]; then
    echo "🔨 Building cross_chain_relayer binary..."
    (cd "$SCRIPT_DIR/../../execution" && go build -o "$BIN_PATH" ./cmd/tool/cross_chain_relayer)
fi

# RELAYER_KEY must come from the environment for any real deployment — the fallback below is a
# PUBLIC devnet-only key committed to this repo, safe only because nothing of real value is ever
# custodied against it. Never let a real relayer/submitter run with this fallback: whoever
# controls it can submit attestCommit()/claimMessage() as the relayer identity (though not forge
# BLS quorum certs — that still requires real committee signatures). Set RELAYER_KEY in the
# environment (or systemd unit's EnvironmentFile) before running this in anything but a
# throwaway local devnet.
if [ -z "$RELAYER_KEY" ]; then
    echo "⚠️  WARNING: RELAYER_KEY not set in environment — falling back to the PUBLIC devnet key" >&2
    echo "⚠️  committed in this script. This is safe ONLY for a local throwaway devnet. Set" >&2
    echo "⚠️  RELAYER_KEY yourself before running this against any real network." >&2
    RELAYER_KEY="0xd3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8"
fi
ROOT_ANCHOR="http://127.0.0.1:9099"
CHAINS="101=http://127.0.0.1:8546,102=http://127.0.0.1:8547,103=http://127.0.0.1:8548,104=http://127.0.0.1:8549"
POLL_MS=500

echo "═══════════════════════════════════════════════════════════════"
echo "🌐 STARTING CROSS-CHAIN RELAYER DAEMON"
echo "   - Root Anchor: $ROOT_ANCHOR"
echo "   - Private Chains: $CHAINS"
echo "   - Poll Interval: ${POLL_MS}ms"
echo "═══════════════════════════════════════════════════════════════"

exec "$BIN_PATH" \
    -key "$RELAYER_KEY" \
    -root-anchor "$ROOT_ANCHOR" \
    -chains "$CHAINS" \
    -poll-interval-ms "$POLL_MS"
