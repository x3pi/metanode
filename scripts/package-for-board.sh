#!/usr/bin/env bash
# Packages the aarch64 cross-compiled cmd/simple_chain binary (see
# scripts/build-aarch64.sh) together with its runtime shared libraries into
# a self-contained bundle, and optionally pushes + runs it on the real
# board over `hdc`. See note/tee_dual_mode_execution_plan.md §9.39 for full
# context and the investigation behind this.
#
# ─────────────────────── Why this exists ───────────────────────────────────
# The board (Orange Pi 5 Max) runs a customized OpenHarmony userspace, NOT
# glibc Linux (confirmed 2026-08-21: no /lib/aarch64-linux-gnu/, no
# /lib64/ld-linux-aarch64.so.1 — its real libc is /system/lib64/libc.so, an
# OpenHarmony-specific one). A normal `aarch64-linux-gnu` (glibc) binary —
# what scripts/build-aarch64.sh produces — has no dynamic linker or libc to
# resolve against on that system as-is. This script works around that WITHOUT
# needing a full static rebuild (tbb/xapian don't ship static .a from apt,
# see note/tee_dual_mode_execution_plan.md §9.37): bundle the binary with
# its own glibc + libstdc++ + the 6 project libs + their own dynamic linker
# (all pulled from THIS dev machine's aarch64 multiarch packages — the same
# ones scripts/build-aarch64.sh's cross-compile used), then invoke it via
# that bundled linker explicitly:
#   <bundled ld-linux-aarch64.so.1> --library-path <bundled lib64/> <binary>
# This is the standard "run a foreign-libc binary via its own bundled
# loader" trick (same idea as AppImage/Nix-style portable Linux binaries) —
# the board's Linux 5.10 kernel itself is ABI-compatible with any aarch64
# ELF; only userspace (libc) differs, and that's exactly what's bundled.
#
# Confirmed working 2026-08-21: `simple_chain -tool-get-address <hex key>`
# ran on the real board and printed the correct derived address — a real
# secp256k1+keccak computation, not just "didn't crash".
#
# Usage:
#   ./scripts/package-for-board.sh [binary path, default: ./simple_chain-aarch64]
#   Then, to push + smoke-test on the board (requires `hdc`, board already
#   connected — see tz-llm-trustzone's CLAUDE.md for hdcd/UART setup, this
#   script does NOT do that):
#   ./scripts/package-for-board.sh --push [remote dir, default: /data/ssd/metanode]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PUSH=0
REMOTE_DIR="/data/ssd/metanode"
BIN="$REPO_ROOT/simple_chain-aarch64"

if [ "${1:-}" = "--push" ]; then
  PUSH=1
  shift
  [ -n "${1:-}" ] && REMOTE_DIR="$1"
else
  [ -n "${1:-}" ] && BIN="$1"
fi

if [ ! -f "$BIN" ]; then
  echo "FATAL: binary not found at $BIN (run scripts/build-aarch64.sh first)" >&2
  exit 1
fi

BUNDLE_DIR="$REPO_ROOT/.board_bundle"
rm -rf "$BUNDLE_DIR"
mkdir -p "$BUNDLE_DIR/lib64"
cp "$BIN" "$BUNDLE_DIR/simple_chain"

# Exact list confirmed by trial-and-error against the real binary's NEEDED
# entries (`aarch64-linux-gnu-readelf -d`) plus one transitive dependency
# (libz, pulled in by libxapian) that only showed up as a runtime error on
# the board, not in NEEDED itself (dlopen'd, not directly linked) — see
# §9.39 for the exact discovery order. If a future build adds a new C/C++
# dependency, re-run readelf -d on the new binary and diff against this list.
LIBS=(
  libxapian.so.30
  libstdc++.so.6
  libgmp.so.10
  libmpfr.so.6
  libtbb.so.12
  libuuid.so.1
  libm.so.6
  libgcc_s.so.1
  libc.so.6
  libz.so.1
)
for lib in "${LIBS[@]}"; do
  found=""
  for dir in /usr/lib/aarch64-linux-gnu /lib/aarch64-linux-gnu; do
    if [ -f "$dir/$lib" ]; then
      found="$dir/$lib"
      break
    fi
  done
  if [ -z "$found" ]; then
    echo "FATAL: $lib not found in /usr/lib/aarch64-linux-gnu or /lib/aarch64-linux-gnu" >&2
    echo "  (apt install the relevant :arm64 -dev/runtime package first — see" >&2
    echo "  scripts/build-aarch64.sh's header comment for the host setup steps)" >&2
    exit 1
  fi
  cp -L "$found" "$BUNDLE_DIR/lib64/"
done
cp -L /lib/aarch64-linux-gnu/ld-linux-aarch64.so.1 "$BUNDLE_DIR/lib64/"

echo "=== Bundle ready: $BUNDLE_DIR ($(du -sh "$BUNDLE_DIR" | cut -f1)) ==="

if [ "$PUSH" = "0" ]; then
  echo "Not pushing (pass --push [remote dir] to push + smoke-test on the board over hdc)."
  exit 0
fi

if ! command -v hdc >/dev/null; then
  echo "FATAL: hdc not found on PATH" >&2
  exit 1
fi

echo "=== Pushing to board:$REMOTE_DIR (requires hdc already connected) ==="
hdc shell "mkdir -p $REMOTE_DIR/lib64"
hdc file send "$BUNDLE_DIR/simple_chain" "$REMOTE_DIR/simple_chain"
for f in "$BUNDLE_DIR/lib64/"*; do
  hdc file send "$f" "$REMOTE_DIR/lib64/$(basename "$f")"
done
hdc shell "chmod +x $REMOTE_DIR/simple_chain $REMOTE_DIR/lib64/*"

echo "=== Smoke test: -tool-get-address (pure computation, no config/genesis needed) ==="
hdc shell "cd $REMOTE_DIR && ./lib64/ld-linux-aarch64.so.1 --library-path ./lib64 ./simple_chain -tool-get-address 2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"
echo ""
echo "Expected: 0x97126B71376F7e55fBA904FdaA9dF0dBd396612f"
echo ""
echo "To run the real node, push ALL FOUR of these into $REMOTE_DIR (confirmed"
echo "2026-08-21, §9.41: a node.toml/keys/ omission looks EXACTLY like a hung"
echo "Rust consensus thread -- it isn't, it's a 5s retry loop logging to"
echo "logs/execution/<date>/execution.log, NOT the file this script's own"
echo "stdout redirect points at -- see that file for real Rust-side logs):"
echo "  - config.json   (Go node config)"
echo "  - genesis.json  (referenced by config.json's genesis_file_path)"
echo "  - node.toml     (Rust consensus config -- easy to forget, no error"
echo "                   surfaces in the file you're probably tailing)"
echo "  - keys/         (protocol_key.json/network_key.json, referenced by node.toml)"
echo "Then invoke:"
echo "  ./lib64/ld-linux-aarch64.so.1 --library-path ./lib64 ./simple_chain -config <path>"
