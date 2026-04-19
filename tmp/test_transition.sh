#!/bin/bash
pkill -f simple_chain
pkill -f metanode
rm -rf data_*
echo "Starting 4 nodes initially..."
for i in 0 1 3 4; do
  ./cmd/simple_chain/simple_chain -config cmd/simple_chain/config-master-node$i.json > run_master$i.log 2>&1 &
  sleep 1
  RUST_LOG=info ../mtn-consensus/metanode/target/debug/metanode run cmd/simple_chain/config-sub-node$i.json > run_meta$i.log 2>&1 &
  sleep 1
done
echo "Waiting for 4 nodes to produce some blocks..."
sleep 20
echo "Starting Node 2..."
./cmd/simple_chain/simple_chain -config cmd/simple_chain/config-master-node2.json > run_master2.log 2>&1 &
sleep 1
RUST_LOG=info ../mtn-consensus/metanode/target/debug/metanode run cmd/simple_chain/config-sub-node2.json > run_meta2.log 2>&1 &
echo "Done."

