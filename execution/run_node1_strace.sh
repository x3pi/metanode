#!/bin/bash
cd /home/abc/chain-n/metanode/execution/cmd/simple_chain
export GOTOOLCHAIN=go1.23.5
export GOMEMLIMIT=4GiB
export XAPIAN_BASE_PATH='sample/node1/data/data/xapian_node'
export MVM_LOG_DIR='/home/abc/chain-n/metanode/consensus/metanode/logs/node_1'
strace -e trace=process,signal -f -o /home/abc/chain-n/metanode/execution/strace_node1.txt ./simple_chain -config=config-master-node1.json >> /home/abc/chain-n/metanode/execution/node1_strace_out.log 2>&1
