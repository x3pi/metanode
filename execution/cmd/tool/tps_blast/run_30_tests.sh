#!/bin/bash
cd /home/abc/chain-n/metanode/execution/cmd/tool/tps_blast

echo "Starting 30 runs of blast test..." > tps_summary.txt

for i in {1..30}; do
  echo "--- Run $i/30 ---"
  OUTPUT=$(./run_multinode_load.sh 10 10000 2>&1)
  
  TPS=$(echo "$OUTPUT" | grep -oE "SYSTEM TPS:\s+~[0-9]+" | awk '{print $3}')
  MAX_BLOCK=$(echo "$OUTPUT" | grep -oE "Max TXs/block:\s+[0-9]+" | awk '{print $3}')
  
  # Check fork
  if echo "$OUTPUT" | grep -q "HỆ THỐNG KHÔNG FORK"; then
    FORK="SAFE (0 forks)"
  else
    FORK="FORK DETECTED"
  fi
  
  echo "Run $i: TPS=$TPS | MaxBlock=$MAX_BLOCK | ForkStatus=$FORK" | tee -a tps_summary.txt
  sleep 1
done

echo "Done 30 runs."
