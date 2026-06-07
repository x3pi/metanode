#!/bin/bash
cd /home/abc/chain-n/metanode/consensus/metanode/scripts

echo "Starting fresh restart..."
./mtn-orchestrator.sh restart --fresh > /dev/null 2>&1

echo "Waiting for epoch 1 (110 blocks -> ~220 seconds)..."
sleep 230

echo "Waiting for snapshot to be created automatically..."
for i in {1..30}; do
    SNAPSHOTS=$(curl -sf http://localhost:8600/api/snapshots 2>/dev/null || echo "[]")
    COUNT=$(echo "$SNAPSHOTS" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
    if [ "$COUNT" -gt 0 ]; then
        echo "✅ Snapshot created successfully!"
        break
    fi
    sleep 5
done

echo "Waiting for more blocks..."
sleep 40

echo "Stopping Node 2..."
./mtn-orchestrator.sh stop-node 2

echo "Restoring Node 2..."
./node/restore_node.sh 2

echo "Done! Check logs for InvalidGenesisAncestor"
