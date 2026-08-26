#!/usr/bin/env bash

echo "🛑 Đang dừng chain cũ..."
if [ -f private_chain_data/stop_private_chain.sh ]; then
    bash private_chain_data/stop_private_chain.sh || true
fi
if [ -f private_chains_data/stop_all.sh ]; then
    bash private_chains_data/stop_all.sh || true
fi
if [ -f root_anchor_data/stop_all.sh ]; then
    bash root_anchor_data/stop_all.sh || true
fi
killall simple_chain 2>/dev/null || true
killall metanode 2>/dev/null || true
echo "✅ Đã dừng chain."
