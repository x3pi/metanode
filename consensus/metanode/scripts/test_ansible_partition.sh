#!/bin/bash
# test_ansible_partition.sh
# Tests network partition (STALL behavior) on an Ansible-deployed 4-node cluster

set -e

RPC_URL="http://192.168.1.232:10746"
BLAST_DIR="/home/abc/chain-n/metanode/execution/cmd/tool/tps_blast"
# Note: In ansible deployments, Node 0 Execution P2P is usually 4200
EXECUTION_P2P="192.168.1.232:6200"

echo "🔫 Step 1: Blasting 2,000 TXs (Normal state - 4 nodes alive)"
cd "$BLAST_DIR"
go build -o tps_blast .
./tps_blast -config ./config.json -node "$EXECUTION_P2P" -count 2000 -batch 500 -sleep 1 -rpc "$RPC_URL" -skip-verify -no-wait

sleep 10

echo "✂️ Step 2: Creating Network Partition (Freezing consensus on node-1 and node-2)"
# Dùng pgrep để lấy PID của consensus engines
NODE1_PID=$(pgrep -f "simple_chain.*node-1")
NODE2_PID=$(pgrep -f "simple_chain.*node-2")

if [ -z "$NODE1_PID" ] || [ -z "$NODE2_PID" ]; then
    echo "❌ Error: Could not find PIDs for consensus node-1 or node-2!"
    exit 1
fi

echo "Freezing Node 1 (PID: $NODE1_PID) and Node 2 (PID: $NODE2_PID)..."
echo "abc" | sudo -S kill -STOP $NODE1_PID
echo "abc" | sudo -S kill -STOP $NODE2_PID

sleep 5

echo "🔥 Step 3: Blasting another 2,000 TXs into the active partition (only 2 nodes alive)"
echo "   (Wait: with 4 nodes total, 2f+1 = 3. Since only 2 are alive, the network should STALL and NOT produce blocks!)"
./tps_blast -config ./config.json -node "$EXECUTION_P2P" -count 2000 -batch 500 -sleep 1 -rpc "$RPC_URL" -skip-verify -no-wait

echo "⏳ Waiting 30s to observe the STALL... (Check Telegram for Monitor alerts!)"
sleep 30

echo "🩹 Step 4: Healing Partition (Unfreezing node-1 and node-2)"
echo "Unfreezing Node 1 and Node 2..."
echo "abc" | sudo -S kill -CONT $NODE1_PID
echo "abc" | sudo -S kill -CONT $NODE2_PID

echo "⏳ Waiting 20s for nodes to sync and catch up..."
sleep 20

echo "✅ Network Partition Test Complete!"
