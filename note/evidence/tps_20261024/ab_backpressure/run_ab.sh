#!/bin/bash
cd /home/abc/chain-n/metanode/execution/scripts/test/evidence
OUT=/home/abc/chain-n/metanode/note/evidence/tps_20261024/ab_backpressure
for i in 1 2 3; do
 for v in A B; do
  python3 run_tps_counted.py --mode sustained --runs 1 --tag ${v}_$i --bins /tmp/ab_$v --out $OUT/$v --max-txs 840000 --duration 120 --max-wait-sec 240 > $OUT/${v}_$i.out 2>&1
  echo "$v$i exit=$?" >> $OUT/progress.txt
 done
done
echo AB_DONE >> $OUT/progress.txt
