#!/bin/bash
# Real TPS measurement on the local 4-validator + 1 sync cluster (all on one host). Raw reports are kept next to this script.
cd /home/abc/chain-n/metanode/execution
RPC=http://192.168.1.232:10746,http://192.168.1.232:10747,http://192.168.1.232:10748,http://192.168.1.232:10749
TCP=192.168.1.232:6200,192.168.1.232:6201,192.168.1.232:6202,192.168.1.232:6203
OUT=/home/abc/chain-n/metanode/note/evidence/tps_20261024
go build -o /tmp/secp_tps_blast_bin ./cmd/tool/secp_tps_blast || exit 3
sha256sum /tmp/secp_tps_blast_bin > $OUT/binary.sha256
for i in 1 2 3; do
  /tmp/secp_tps_blast_bin -rpc $RPC -tcp $TCP -count 25000 -batch 1000 -rounds 1 -verify-parity -strict -report $OUT/burst_$i.json > $OUT/burst_$i.log 2>&1
  echo "burst_$i exit=$?" >> $OUT/summary.txt
  sleep 20
done
/tmp/secp_tps_blast_bin -rpc $RPC -tcp $TCP -batch 1000 -duration 60 -verify-parity -strict -report $OUT/sustained_60s.json > $OUT/sustained_60s.log 2>&1
echo "sustained_60s exit=$?" >> $OUT/summary.txt
echo ALL_DONE >> $OUT/summary.txt
