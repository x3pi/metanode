#!/bin/bash
cd /home/abc/chain-n/metanode/execution
OUT=/home/abc/chain-n/metanode/note/evidence/tps_20261024
RPC=http://192.168.1.232:10746,http://192.168.1.232:10747,http://192.168.1.232:10748,http://192.168.1.232:10749
TCP=192.168.1.232:6200,192.168.1.232:6201,192.168.1.232:6202,192.168.1.232:6203
/tmp/secp_tps_blast_bin -rpc $RPC -tcp $TCP -batch 500 -duration 60 -rate-limit 14000 -quiet-sec 15 -max-wait-sec 240 -verify-parity -strict -report $OUT/bft_prof_run.json > $OUT/bft_prof_run.log 2>&1 &
sleep 20
curl -s -m 60 "http://127.0.0.1:6060/debug/pprof/profile?seconds=30" -o $OUT/prof/node0_cpu.pb.gz
curl -s -m 10 "http://127.0.0.1:6060/debug/pprof/goroutine?debug=1" -o $OUT/prof/node0_goroutine.txt
wait
echo PROF_DONE > $OUT/prof/done.txt
