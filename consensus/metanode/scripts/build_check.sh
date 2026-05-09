#!/bin/bash
# Build script for verifying the Recovery Barrier changes
set -e

echo "🔧 Building consensus-core crate..."
cd /home/abc/chain-n/metanode/consensus/metanode/meta-consensus/core
cargo check 2>&1

echo ""
echo "🔧 Building metanode binary..."
cd /home/abc/chain-n/metanode/consensus/metanode
cargo build 2>&1

echo ""
echo "✅ Build successful!"
