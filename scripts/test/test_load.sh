#!/bin/bash
echo "Killing nodes..."
cd /home/abc/chain-n/mtn-simple-2025
bash kill_nodes.sh
cd /home/abc/chain-n/mtn-consensus/metanode/scripts
bash mtn-orchestrator.sh stop
sleep 3
bash clean_all.sh
rm -rf /home/abc/chain-n/mtn-simple-2025/data_master_*
rm -rf /home/abc/chain-n/mtn-simple-2025/data_sub_*
rm -rf /home/abc/chain-n/mtn-simple-2025/node_logs/*
rm -rf /home/abc/chain-n/mtn-consensus/metanode/logs/*

echo "Starting Orchestrator..."
bash mtn-orchestrator.sh start
sleep 3
cd /home/abc/chain-n/mtn-simple-2025
echo "Starting Execution Cluster..."
bash run.sh
sleep 15
echo "Running Load Test..."
cd cmd/tool/tps_blast
./run_multinode_load.sh 30 30000
