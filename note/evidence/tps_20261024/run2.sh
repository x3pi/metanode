#!/bin/bash
cd /home/abc/chain-n/metanode/execution
OUT=/home/abc/chain-n/metanode/note/evidence/tps_20261024
RPC=http://192.168.1.232:10746,http://192.168.1.232:10747,http://192.168.1.232:10748,http://192.168.1.232:10749
TCP=192.168.1.232:6200,192.168.1.232:6201,192.168.1.232:6202,192.168.1.232:6203
for r in 5000 8000; do
/tmp/secp_tps_blast_bin -rpc $RPC -tcp $TCP -batch 500 -duration 60 -rate-limit $r -quiet-sec 15 -max-wait-sec 180 -verify-parity -strict -report $OUT/paced_${r}.json > $OUT/paced_${r}.log 2>&1
echo "paced_$r exit=$?" >> $OUT/summary2.txt; sleep 30; done
echo ALL_DONE >> $OUT/summary2.txt
