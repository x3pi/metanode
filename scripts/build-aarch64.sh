#!/usr/bin/env bash
# Cross-compiles metanode's cmd/simple_chain for aarch64-linux-gnu (the real
# board's arch — Orange Pi 5 Max / RK3588) on an x86_64 dev machine. See
# note/tee_dual_mode_execution_plan.md's cross-compile section (§9.37/§9.38)
# for full context and the investigation behind every step below.
#
# ─────────────────────── One-time host setup (not automated here) ─────────
#   1. dpkg --add-architecture arm64
#   2. Add a ports.ubuntu.com source restricted to [arch=arm64] (Ubuntu's
#      default amd64 mirror does not carry arm64 packages), and restrict the
#      existing amd64/i386 sources to Architectures: amd64 i386 so apt
#      doesn't try (and fail) to fetch arm64 Release files from them.
#   3. apt update
#   4. apt install libgmp-dev:arm64 libmpfr-dev:arm64 libtbb-dev:arm64 \
#        libleveldb-dev:arm64 uuid-dev:arm64
#      (these 5 are Multi-Arch: same and coexist fine with the amd64 copies
#      already installed for the normal x86 build.)
#   5. libxapian-dev is NOT Multi-Arch: same — `apt install
#      libxapian-dev:arm64` WILL SILENTLY REMOVE libxapian-dev:amd64 and
#      break the default x86 build (confirmed the hard way, 2026-08-21).
#      Never apt-install the arm64 variant. Instead extract it manually,
#      without touching dpkg's database at all:
#        cd /tmp && apt-get download libxapian-dev:arm64 libxapian30:arm64
#        mkdir -p <XAPIAN_ARM64_ROOT> && cd <XAPIAN_ARM64_ROOT>
#        dpkg -x /tmp/libxapian-dev_*_arm64.deb .
#        dpkg -x /tmp/libxapian30_*_arm64.deb .
#      Point XAPIAN_ARM64_ROOT (env var, see below) at that directory.
#   6. rustup target add aarch64-unknown-linux-gnu
#   7. .cargo/config.toml at repo root (already committed) sets the
#      target's linker/CC/CXX/AR — nothing to do here.
# ────────────────────────────────────────────────────────────────────────────
#
# What this script does:
#   1. Cross-builds pkg/mvm's c_mvm + linker (the C++ EVM interpreter +
#      Xapian core) into out-of-tree build-aarch64/ dirs — never touches the
#      real x86 build/ dirs build_check.sh depends on.
#   2. Cross-builds the 2 Rust staticlibs (mtn-nomt-ffi, metanode — the
#      latter pulls in RocksDB via crates/typed-store, built from source for
#      aarch64 by cargo/cc automatically) via `cargo build --target
#      aarch64-unknown-linux-gnu`.
#   3. cmd/simple_chain's cgo LDFLAGS hardcode paths to the 4 build
#      artifacts above (both C++ .a's under pkg/mvm, plus the 2 Rust .a's) —
#      there is no GOARCH-aware path variant. To link them for aarch64
#      without permanently changing those paths (which would break the
#      default x86 build for everyone), this TEMPORARILY swaps the aarch64
#      artifacts into the real x86 paths, cross-builds cmd/simple_chain, and
#      ALWAYS restores the x86 originals afterward — via a trap, so
#      restoration happens even on error or interrupt (Ctrl-C).
#
# Usage: ./scripts/build-aarch64.sh [output path, default: ./simple_chain-aarch64]
# Env:   XAPIAN_ARM64_ROOT — dir from step 5 above (default: repo root's
#          .aarch64-xapian-extract/, gitignored, not created by this script)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$REPO_ROOT/simple_chain-aarch64}"
XAPIAN_ARM64_ROOT="${XAPIAN_ARM64_ROOT:-$REPO_ROOT/.aarch64-xapian-extract}"

if [ ! -f "$XAPIAN_ARM64_ROOT/usr/include/xapian.h" ]; then
  echo "FATAL: arm64 xapian headers not found at $XAPIAN_ARM64_ROOT" >&2
  echo "  See this script's own header comment (one-time host setup, step 5)" >&2
  echo "  for how to extract them without breaking the amd64 build." >&2
  exit 1
fi
if ! command -v aarch64-linux-gnu-gcc >/dev/null; then
  echo "FATAL: aarch64-linux-gnu-gcc not found (apt install gcc-aarch64-linux-gnu g++-aarch64-linux-gnu)" >&2
  exit 1
fi

echo "=== [1/3] Cross-building pkg/mvm (c_mvm + linker) for aarch64 ==="
MVM_DIR="$REPO_ROOT/execution/pkg/mvm"
TOOLCHAIN="$MVM_DIR/cmake/aarch64-linux-gnu.cmake"

cd "$MVM_DIR/c_mvm"
cmake -S . -B build-aarch64 \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" \
  -DMVM_INSTALL_PREFIX="$MVM_DIR/c_mvm/build-aarch64/install" \
  >/dev/null
cmake --build build-aarch64 --target install -j"$(nproc)" >/dev/null

cd "$MVM_DIR/linker"
cmake -S . -B build-aarch64 \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN" \
  -DMVM_C_MVM_BUILD_DIR="$MVM_DIR/c_mvm/build-aarch64/install" \
  >/dev/null
# Intentionally `cmake --build` (= make), NEVER `--target install`:
# linker/CMakeLists.txt's install() hardcodes the real x86 build/ dir
# (confirmed 2026-08-21, see note/tee_dual_mode_execution_plan.md §9.37 —
# it has no MVM_INSTALL_PREFIX override the way c_mvm's does). The .a is
# read straight out of build-aarch64/ in step 3 instead.
cmake --build build-aarch64 -j"$(nproc)" >/dev/null

echo "=== [2/3] Cross-building Rust staticlibs for aarch64 (RocksDB included, ~2-3 min) ==="
cd "$REPO_ROOT"
cargo build --release --locked --target aarch64-unknown-linux-gnu -p mtn-nomt-ffi
cd "$REPO_ROOT/consensus/metanode"
cargo build --release --locked --target aarch64-unknown-linux-gnu

echo "=== [3/3] Swapping in aarch64 artifacts, building cmd/simple_chain, restoring x86 ==="
X86_NOMT="$REPO_ROOT/target/release/libmtn_nomt.a"
X86_METANODE="$REPO_ROOT/consensus/metanode/target/release/libmetanode.a"
X86_LINKER="$MVM_DIR/linker/build/lib/static/libmvm_linker.a"
X86_MVM="$MVM_DIR/c_mvm/build/lib/static/libmvm.a"

for f in "$X86_NOMT" "$X86_METANODE" "$X86_LINKER" "$X86_MVM"; do
  if [ ! -f "$f" ]; then
    echo "FATAL: expected real x86 build artifact missing: $f" >&2
    echo "  Run consensus/metanode/scripts/build_check.sh once first." >&2
    exit 1
  fi
done

BACKUP_DIR="$(mktemp -d)"
restore() {
  echo "--- restoring x86 build artifacts (from $BACKUP_DIR) ---"
  cp "$BACKUP_DIR/libmtn_nomt.a" "$X86_NOMT"
  cp "$BACKUP_DIR/libmetanode.a" "$X86_METANODE"
  cp "$BACKUP_DIR/libmvm_linker.a" "$X86_LINKER"
  cp "$BACKUP_DIR/libmvm.a" "$X86_MVM"
  rm -rf "$BACKUP_DIR"
}
trap restore EXIT

cp "$X86_NOMT" "$BACKUP_DIR/libmtn_nomt.a"
cp "$X86_METANODE" "$BACKUP_DIR/libmetanode.a"
cp "$X86_LINKER" "$BACKUP_DIR/libmvm_linker.a"
cp "$X86_MVM" "$BACKUP_DIR/libmvm.a"

cp "$REPO_ROOT/target/aarch64-unknown-linux-gnu/release/libmtn_nomt.a" "$X86_NOMT"
cp "$REPO_ROOT/target/aarch64-unknown-linux-gnu/release/libmetanode.a" "$X86_METANODE"
cp "$MVM_DIR/linker/build-aarch64/libmvm_linker.a" "$X86_LINKER"
cp "$MVM_DIR/c_mvm/build-aarch64/install/lib/static/libmvm.a" "$X86_MVM"

cd "$REPO_ROOT/execution/cmd/simple_chain"
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
  CC=aarch64-linux-gnu-gcc CXX=aarch64-linux-gnu-g++ \
  CGO_LDFLAGS="-L$XAPIAN_ARM64_ROOT/usr/lib/aarch64-linux-gnu" \
  go build -o "$OUT" .
# `restore` (trap on EXIT) runs now, whether the build above succeeded or not.

echo "=== Done: $OUT ==="
file "$OUT"
