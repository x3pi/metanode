#!/bin/bash
export GOTOOLCHAIN=go1.23.5

SESSION="simple_chain"
tmux new-session -d -s $SESSION -n "NODES"

# Pane 0: MASTER node (debug mode)
tmux send-keys -t $SESSION:0.0 "
go run . -config=config-master.json --debug --pprof-addr=localhost:6061
" C-m

# Pane 1: WRITE node (debug mode)
tmux split-window -h -t $SESSION:0
tmux send-keys -t $SESSION:0.1 "sleep 1 && \
go run . -config=config-sub-write.json --debug --pprof-addr=localhost:6062
" C-m

# WRITE-2 (uncomment if needed):
# tmux split-window -v -t $SESSION:0.1
# tmux send-keys -t $SESSION:0.2 "sleep 15 && go run . -config=config-sub-write-2.json --debug" C-m

tmux select-layout -t $SESSION:0 tiled
tmux attach-session -t $SESSION
