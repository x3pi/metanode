#!/bin/bash
set +e
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
cd "$DIR"

echo "�� Step 1: Blasting 10,000 TXs (Normal state)"
cd ../../../execution/cmd/tool/tps_blast
go build -o tps_blast .
./tps_blast -config ./config.json -node "127.0.0.1:4201" -count 10000 -batch 200 -sleep 1 -rpc "127.0.0.1:8757" -skip-verify
sleep 5

echo "✂️ Step 2: Creating Network Partition (Freezing node 1 and 2)"
cd "$DIR"
NODE1_PID=$(pgrep -f "simple_chain.*node1")
NODE2_PID=$(pgrep -f "simple_chain.*node2")

if [ -z "$NODE1_PID" ] || [ -z "$NODE2_PID" ]; then
    echo "❌ Error: Could not find PIDs for node 1 or node 2!"
    exit 1
fi

echo "Freezing Node 1 (PID: $NODE1_PID) and Node 2 (PID: $NODE2_PID)..."
kill -STOP $NODE1_PID
kill -STOP $NODE2_PID
sleep 5

echo "🔥 Step 3: Blasting 40,000 TXs into the active partition (node 0, 3, 4)"
cd ../../../execution/cmd/tool/tps_blast
# Bắn vào node0
./tps_blast -config ./config.json -node "127.0.0.1:4201" -count 40000 -batch 200 -sleep 1 -rpc "127.0.0.1:8757" -skip-verify -no-wait
sleep 15

echo "🩹 Step 4: Healing Partition (Unfreezing node 1 and 2)"
cd "$DIR"
echo "Unfreezing Node 1 and Node 2..."
kill -CONT $NODE1_PID
kill -CONT $NODE2_PID

echo "⏳ Waiting 30s for nodes to sync and catch up..."
sleep 30

echo "🔍 Step 5: Checking for forks (Comparing Block Hashes across all nodes)"
cd ../../../execution/cmd/tool/block_hash_checker
go build -o block_hash_checker .
./block_hash_checker -from 1 -to 100 -nodes "Node0=http://127.0.0.1:8757,Node1=http://127.0.0.1:10747,Node2=http://127.0.0.1:10749,Node3=http://127.0.0.1:10750,Node4=http://127.0.0.1:10748"

echo "✅ Network Partition Test Complete!"
