#!/bin/bash
# kill -9 durability test for registration requests, on the isolated env (see gen_env.py / run_env.sh).
# A: registered + CONFIRMED, then exec1 is killed -9 -> must still be CONFIRMED after restart.
# B: registered while the Parent Chain is DOWN (request only queued), exec1 killed -9, both restarted -> must reach CONFIRMED.
# Usage: restart_durability.sh <BASE>   (env must be freshly started with run_env.sh start)
set -u
E="$(cd "$1" && pwd)"; HERE="$(cd "$(dirname "$0")" && pwd)"
BIN=$E/bin; PH=$(python3 -c "import json;print(json.load(open('$E/env.json'))['ports']['parent_http'])")
RPC=$(python3 -c "import json;print(json.load(open('$E/env.json'))['ports']['exec1']['rpc'])")
D="$BIN/e2e_account_gate -env $E/env.json"
KA=$(python3 -c "import os;print(os.urandom(32).hex())"); KB=$(python3 -c "import os;print(os.urandom(32).hex())")
fail=0
$D -restart-step register -user-key $KA || exit 1
$D -restart-step verify -user-key $KA | tail -1 || exit 1       # A confirmed before the crash
kill -TERM "$(cat $E/pids/parent.pid)"; sleep 3
$D -restart-step register -user-key $KB || exit 1                # B only queued
kill -9 "$(cat $E/pids/exec1.pid)"; sleep 2
( cd $E/parent && exec $BIN/parent_chain -data-dir $E/parent -http :$PH -rust-config $E/parent/node_parent.toml -genesis $E/parent/parent_genesis.json >>$E/logs/parent.log 2>&1 ) & echo $! > $E/pids/parent.pid
for _ in $(seq 1 90); do curl -s -m2 http://127.0.0.1:$PH/status -o /dev/null && break; sleep 1; done
( cd $E/exec1 && PARENT_CHAIN_URL=http://127.0.0.1:$PH BLS_CONSERVATION_MODE=enforce BLS_CONSERVATION_INTERVAL_SECONDS=300 exec $BIN/simple_chain -config=$E/exec1/config.json --pprof-addr= >>$E/logs/exec1.log 2>&1 ) & echo $! > $E/pids/exec1.pid
for _ in $(seq 1 180); do curl -s -m2 -X POST -H 'content-type: application/json' --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' http://127.0.0.1:$RPC | grep -q result && break; sleep 1; done
echo "--- after restart"
$D -restart-step verify -user-key $KA | tail -1 || { echo "A FAILED"; fail=1; }
$D -restart-step verify -user-key $KB | tail -1 || { echo "B FAILED"; fail=1; }
echo "restart durability: $([ $fail = 0 ] && echo PASS || echo FAIL)"; exit $fail
