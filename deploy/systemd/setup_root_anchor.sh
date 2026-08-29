#!/usr/bin/env bash
# setup_root_anchor.sh — Automated Setup and Runner for Root Anchor Network (Milestone I)
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="$SCRIPT_DIR/root_anchor_data"

echo "═══════════════════════════════════════════════════════════════"
echo "⚓ SETUP & RUN ROOT ANCHOR NETWORK (CHAIN ID 9099)"
echo "═══════════════════════════════════════════════════════════════"

CLEAN=0
NO_BUILD=0
VALIDATORS=4
FOUNDING_CHAINS="101,102,103,104"

for arg in "$@"; do
    case $arg in
        --clean|-c)
            CLEAN=1
            ;;
        --no-build)
            NO_BUILD=1
            ;;
        --validators=*)
            VALIDATORS="${arg#*=}"
            ;;
        --help|-h)
            echo "Usage: bash setup_root_anchor.sh [--clean] [--no-build] [--validators=N]"
            exit 0
            ;;
    esac
done

if [ "$NO_BUILD" -eq 0 ]; then
    echo "🔨 [BUILD] Checking build targets (Go, Rust, FFI)..."
    bash "$SCRIPT_DIR/../../consensus/metanode/scripts/build_check.sh"
    echo ""
fi

if [ "$CLEAN" -eq 1 ] && [ -d "$DATA_DIR" ]; then
    echo "🛑 Stopping existing Root Anchor processes..."
    if [ -f "$DATA_DIR/stop_all.sh" ]; then
        bash "$DATA_DIR/stop_all.sh" || true
    fi
    for pidfile in "$DATA_DIR"/node_*/node.pid; do
        if [ -f "$pidfile" ]; then
            PID=$(cat "$pidfile")
            kill -9 "$PID" 2>/dev/null || true
            rm -f "$pidfile"
        fi
    done
    fuser -k 9099/tcp 9100/tcp 9101/tcp 9102/tcp 4200/tcp 4201/tcp 4202/tcp 4203/tcp 10200/tcp 10201/tcp 10202/tcp 10203/tcp 19200/tcp 19201/tcp 19202/tcp 19203/tcp 20200/tcp 20201/tcp 20202/tcp 20203/tcp >/dev/null 2>&1 || true
    sleep 1
    echo "🧹 Cleaning up data directory..."
    rm -rf "$DATA_DIR"
fi

if [ ! -f "$DATA_DIR/genesis.json" ]; then
    mkdir -p "$DATA_DIR"
    echo "👉 Initializing Root Anchor cluster with $VALIDATORS validators..."
    python3 "$SCRIPT_DIR/gen_root_anchor_chain.py" \
        --chain-id 9099 \
        --validators "$VALIDATORS" \
        --founding-chains "$FOUNDING_CHAINS" \
        --output-dir "$DATA_DIR"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "✅ Root Anchor Network is configured and ready at: $DATA_DIR"
echo "   - Chain ID: 9099"
echo "   - RPC Endpoint (Node 0): http://127.0.0.1:9099"
echo "   - Start cluster: bash $DATA_DIR/start_all.sh"
echo "   - Stop cluster:  bash $DATA_DIR/stop_all.sh"
echo "═══════════════════════════════════════════════════════════════"
