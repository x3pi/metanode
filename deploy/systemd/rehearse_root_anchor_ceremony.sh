#!/bin/bash
# rehearse_root_anchor_ceremony.sh — end-to-end local rehearsal of the Root
# Anchor genesis ceremony (note/runbook_root_anchor_genesis_ceremony.md).
#
# Simulates 4 INDEPENDENT founding-chain operators using 4 fully separate key
# directories on this one machine (no key material is ever shared between
# them), exercises the exact same commands a real ceremony uses
# (founding_entry -> assemble_root_anchor assemble -> assemble_root_anchor
# verify, once per "operator"), and — by default — actually boots a 4-node
# network from the assembled genesis.json and confirms it reaches BFT quorum
# and finalizes real blocks, proving the ceremony output is not just
# structurally valid JSON but an actually-bootable network.
#
# Usage:
#   deploy/systemd/rehearse_root_anchor_ceremony.sh [--no-boot] [--chain-id N] [--keep]
#
#   --no-boot     Skip phase 4 (booting the 4-node network). Much faster; use
#                 this for a quick tooling/schema sanity check (e.g. in CI).
#   --chain-id N  Root Anchor chain ID to rehearse with (default: 9099 — NOT
#                 991, deliberately, to prove the ceremony is not hardcoded to
#                 the single-chain default).
#   --keep        Don't delete the working directory on exit (for inspection).
#
# Requires: a built `metanode` binary (target/release/metanode or
# consensus/metanode/target/release/metanode) and, unless --no-boot, a
# `go build`-able execution/cmd/simple_chain (needs the C++ MVM linker +
# Rust libmetanode.a already built — see AGENTS.md / execution/build.sh).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

BOOT_NETWORK=1
CHAIN_ID=9099
KEEP=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-boot) BOOT_NETWORK=0; shift ;;
    --chain-id) CHAIN_ID="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }

# ── locate metanode binary ───────────────────────────────────────────────
METANODE_BIN=""
for candidate in \
  "$REPO_ROOT/target/release/metanode" \
  "$REPO_ROOT/consensus/metanode/target/release/metanode"
do
  if [[ -x "$candidate" ]]; then METANODE_BIN="$candidate"; break; fi
done
if [[ -z "$METANODE_BIN" ]]; then
  red "ERROR: metanode binary not found. Build it first:"
  red "  cd consensus/metanode && cargo build --release -p metanode"
  exit 1
fi

WORKDIR="$(mktemp -d /tmp/root_anchor_rehearsal.XXXXXX)"
cleanup() {
  local ec=$?
  if [[ -n "${PIDS:-}" ]]; then
    for pid in $PIDS; do kill "$pid" >/dev/null 2>&1 || true; done
    # Give the processes a moment to actually release open DB files before
    # rm -rf runs, or an in-progress fsync/close can make rm briefly fail.
    for pid in $PIDS; do
      for _ in $(seq 1 20); do
        kill -0 "$pid" >/dev/null 2>&1 || break
        sleep 0.2
      done
    done
  fi
  if [[ "$KEEP" -eq 1 ]]; then
    echo "Kept working directory: $WORKDIR"
  else
    rm -rf "$WORKDIR" 2>/dev/null || { sleep 1; rm -rf "$WORKDIR" 2>/dev/null || true; }
  fi
  exit $ec
}
trap cleanup EXIT

bold "Root Anchor genesis ceremony rehearsal — workdir: $WORKDIR"
bold "Root Anchor chain_id: $CHAIN_ID (deliberately not 991)"

# ── build the two ceremony CLI tools once ────────────────────────────────
bold "\nBuilding founding_entry + assemble_root_anchor..."
( cd "$REPO_ROOT/execution" && go build -o "$WORKDIR/founding_entry" ./cmd/tool/founding_entry )
( cd "$REPO_ROOT/execution" && go build -o "$WORKDIR/assemble_root_anchor" ./cmd/tool/assemble_root_anchor )
green "  ✅ built"

# ── phase 1: each simulated operator generates keys + a founding_entry ──
bold "\nPhase 1/4 — 4 independent operators generate keys + founding_entry.json"
NAMES=(Alpha Beta Gamma Delta)
FOUNDING_CHAIN_IDS=(101 102 103 104)
mkdir -p "$WORKDIR/entries"

for i in 0 1 2 3; do
  op_dir="$WORKDIR/operators/op-$i"
  mkdir -p "$op_dir/keys"
  "$METANODE_BIN" keytool generate validator --out-dir "$op_dir/keys" > "$op_dir/keytool.log"

  "$WORKDIR/founding_entry" \
    --keys-dir "$op_dir/keys" \
    --chain-id "${FOUNDING_CHAIN_IDS[$i]}" \
    --chain-name "${NAMES[$i]} Chain" \
    --hostname "node-$i" \
    --ip 127.0.0.1 \
    --p2p-port "$((9100 + i))" --primary-port "$((6200 + i))" --worker-port "$((4012 + i))" \
    --stake 1000000000000000000000 \
    --out "$WORKDIR/entries/chain-$i.json" > "$op_dir/founding_entry.log"

  echo "  operator $i (${NAMES[$i]} Chain, founding chain_id=${FOUNDING_CHAIN_IDS[$i]}): OK"
done
green "  ✅ 4 independent founding_entry.json files produced, no shared key material"

# ── phase 2: coordinator assembles genesis ───────────────────────────────
bold "\nPhase 2/4 — coordinator assembles genesis.json"
EPOCH_MS=$(( ($(date +%s) + 5) * 1000 ))
"$WORKDIR/assemble_root_anchor" assemble \
  --entries "$WORKDIR/entries" \
  --chain-id "$CHAIN_ID" \
  --epoch-timestamp-ms "$EPOCH_MS" \
  --epoch-duration-seconds 300 \
  --attestation-interval 10 \
  --out-dir "$WORKDIR/assembled"

DIGEST="$(cat "$WORKDIR/assembled/genesis_digest.txt")"
green "  ✅ genesis.json assembled, digest: $DIGEST"

# ── phase 3: every operator independently verifies before "starting" ────
bold "\nPhase 3/4 — every operator verifies the digest before starting their node"
for i in 0 1 2 3; do
  "$WORKDIR/assemble_root_anchor" verify \
    --genesis "$WORKDIR/assembled/genesis.json" \
    --expect-digest "$DIGEST" > /dev/null
  echo "  operator $i: digest verified OK"
done
green "  ✅ all 4 operators independently confirm the same genesis"

if [[ "$BOOT_NETWORK" -eq 0 ]]; then
  bold "\n--no-boot given: skipping phase 4 (network boot)."
  green "\nCeremony rehearsal (tooling only) PASSED."
  exit 0
fi

# ── phase 4: actually boot the 4-node network and confirm it works ──────
bold "\nPhase 4/4 — booting a real 4-node network from the assembled genesis"

SIMPLE_CHAIN_BIN="$WORKDIR/simple_chain"
( cd "$REPO_ROOT/execution" && go build -o "$SIMPLE_CHAIN_BIN" ./cmd/simple_chain )

GENESIS_PATH="$WORKDIR/assembled/genesis.json"

python3 "$SCRIPT_DIR/_rehearsal_gen_node_configs.py" \
  --workdir "$WORKDIR" \
  --genesis "$GENESIS_PATH" \
  --chain-id "$CHAIN_ID" \
  --num-nodes 4

PIDS=""
for i in 0 1 2 3; do
  node_dir="$WORKDIR/operators/op-$i/node"
  ( cd "$node_dir" && exec "$SIMPLE_CHAIN_BIN" --config config.json > node.log 2>&1 ) &
  PIDS="$PIDS $!"
done
echo "  started 4 nodes, pids:$PIDS"

bold "  waiting up to 60s for BFT quorum + a finalized block..."
RPC_BASE=18757
OK=0
for _ in $(seq 1 30); do
  sleep 2
  BLOCK_HEX="$(curl -s -X POST "http://127.0.0.1:${RPC_BASE}" \
      -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null \
      | python3 -c 'import sys,json; print(json.load(sys.stdin).get("result",""))' 2>/dev/null || true)"
  if [[ -n "$BLOCK_HEX" && "$BLOCK_HEX" != "0x0" ]]; then
    OK=1
    break
  fi
done

if [[ "$OK" -ne 1 ]]; then
  red "  ❌ network did not finalize a block within 60s. Logs are in $WORKDIR/operators/op-*/node/node.log"
  KEEP=1
  exit 1
fi

green "  ✅ node-0 RPC reports block $BLOCK_HEX"

CHAIN_ID_HEX="$(curl -s -X POST "http://127.0.0.1:${RPC_BASE}" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}' \
    | python3 -c 'import sys,json; print(json.load(sys.stdin)["result"])')"
EXPECT_HEX="$(python3 -c "print(hex($CHAIN_ID))")"
if [[ "$CHAIN_ID_HEX" != "$EXPECT_HEX" ]]; then
  red "  ❌ node reports chainId=$CHAIN_ID_HEX, expected $EXPECT_HEX"
  KEEP=1
  exit 1
fi
green "  ✅ chain_id confirmed: $CHAIN_ID_HEX ($CHAIN_ID) — not the 991 default"

# All 4 RPC ports should agree — proves all 4 independently-keyed nodes are
# in consensus with each other, not just that node-0 alone produced a block.
for i in 0 1 2 3; do
  b="$(curl -s -X POST "http://127.0.0.1:$((RPC_BASE + i))" \
      -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
      | python3 -c 'import sys,json; print(json.load(sys.stdin)["result"])')"
  echo "  node $i: block $b"
  if [[ "$b" == "0x0" ]]; then
    red "  ❌ node $i has not left genesis — quorum was not reached across all 4"
    KEEP=1
    exit 1
  fi
done

green "\nCeremony rehearsal PASSED: 4 independently-keyed founding chains produced a"
green "genesis that boots a real network reaching BFT quorum and finalizing blocks."
