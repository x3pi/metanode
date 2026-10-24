#!/bin/bash
cd /home/abc/chain-n/metanode/execution
OUT=/home/abc/chain-n/metanode/note/evidence/tps_20261024
RPC=http://192.168.1.232:10746,http://192.168.1.232:10747,http://192.168.1.232:10748,http://192.168.1.232:10749
TCP=192.168.1.232:6200,192.168.1.232:6201,192.168.1.232:6202,192.168.1.232:6203
for r in 10000 12000 14000; do
/tmp/secp_tps_blast_bin -rpc $RPC -tcp $TCP -batch 500 -duration 60 -rate-limit $r -quiet-sec 15 -max-wait-sec 240 -verify-parity -strict -report $OUT/bft_paced_${r}.json > $OUT/bft_paced_${r}.log 2>&1
echo "bft_paced_$r exit=$?" >> $OUT/summary3.txt; sleep 45; done
echo ALL_DONE >> $OUT/summary3.txt
