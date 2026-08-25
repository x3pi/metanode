#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🚀 Đăng ký 4 Private Chains lên Root Anchor Gateway..."

cd "$DIR/../../execution"
./register_chains --rpc "http://127.0.0.1:9099" --chains "101,102,103,104"
echo "✅ Hoàn tất!"
