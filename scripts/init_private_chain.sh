#!/usr/bin/env bash
# init_private_chain.sh — Wrapper script to initialize a Metanode Private Chain

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Default parameters
CHAIN_ID=${CHAIN_ID:-1337}
VALIDATORS=${VALIDATORS:-1}
IP=${IP:-"127.0.0.1"}
OUTPUT_DIR=${OUTPUT_DIR:-"$REPO_ROOT/private_chain_data"}
ALLOC_BALANCE=${ALLOC_BALANCE:-1000000}
DEV_ACCOUNTS=${DEV_ACCOUNTS:-5}

python3 "$REPO_ROOT/deploy/systemd/gen_private_chain.py" \
  --chain-id "$CHAIN_ID" \
  --validators "$VALIDATORS" \
  --ip "$IP" \
  --output-dir "$OUTPUT_DIR" \
  --alloc-balance "$ALLOC_BALANCE" \
  --dev-accounts "$DEV_ACCOUNTS" "$@"
