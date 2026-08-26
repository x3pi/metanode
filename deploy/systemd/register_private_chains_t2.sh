#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🚀 Đăng ký 4 Private Chains lên Root Anchor Gateway..."

cd "$DIR/../../execution"
if [ ! -f ./register_chains ]; then
    echo "🔨 register_chains binary not found, building from cmd/tool/register_chains ..."
    go build -o register_chains ./cmd/tool/register_chains
fi
# NOTE: the tool's real flag is -root-anchor, not -rpc (go's flag package
# rejects an unrecognized flag outright) — verified by running the binary
# with -h; -rpc was never a valid flag name here.
./register_chains --root-anchor "http://127.0.0.1:9099" --chains "101,102,103,104" --chains-dir "$DIR/private_chains_data"
echo "✅ Hoàn tất!"
