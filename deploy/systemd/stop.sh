#!/usr/bin/env bash

echo "🛑 Đang dừng chain cũ..."
if [ -f private_chain_data/stop_private_chain.sh ]; then
    bash private_chain_data/stop_private_chain.sh || true
fi
pkill -9 -f simple_chain || true
killall simple_chain 2>/dev/null || true
killall metanode 2>/dev/null || true
echo "✅ Đã dừng chain."
