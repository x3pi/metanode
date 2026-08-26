#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🚀 Đăng ký 4 Private Chains lên Root Anchor Gateway (bootstrapFoundingChains thật)..."

cd "$DIR/../../execution"
if [ ! -f ./register_chains ]; then
    echo "🔨 register_chains binary not found, building from cmd/tool/register_chains ..."
    go build -o register_chains ./cmd/tool/register_chains
fi
# NOTE: the tool's real flag is -root-anchor, not -rpc (go's flag package
# rejects an unrecognized flag outright) — verified by running the binary
# with -h; -rpc was never a valid flag name here.
#
# -chains-dir MUST be absolute: this script cd's into execution/ above, but the tool's own
# default ("deploy/systemd/private_chains_data") is relative to the repo root, not execution/ --
# without this flag, genesis/config loading always fails with "no such file or directory" even
# though the directory genuinely exists (found + fixed 2026-08-26, confirmed live).
# --target-rpcs also seeds every private chain's OWN local ChainRegistry with all 4 founding
# committees, not just Root Anchor's -- ChainRegistry is per-chain state, so without this a real
# attestCommit() on any private chain always reverted with "unknown source chain ID" for a commit
# produced by a sibling chain (found + fixed 2026-08-26, confirmed live).
./register_chains \
    --root-anchor "http://127.0.0.1:9099" \
    --chains "101,102,103,104" \
    --chains-dir "$DIR/private_chains_data" \
    --target-rpcs "101=http://127.0.0.1:8546,102=http://127.0.0.1:8547,103=http://127.0.0.1:8548,104=http://127.0.0.1:8549"
echo "✅ Hoàn tất!"
