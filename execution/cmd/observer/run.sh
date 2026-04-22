#!/bin/bash

SESSION="observer"

# Xóa session cũ nếu có
tmux kill-session -t $SESSION 2>/dev/null

# Xóa log cũ
rm -rf logs
echo "Cleared old logs."

# Tạo session mới với pane đầu tiên
tmux new-session -d -s $SESSION -n "Observers" bash

# Chia thành 3 pane
tmux split-window -h -t $SESSION:0 bash
tmux split-window -v -t $SESSION:0.0 bash

# Pane 0: observer 0 - port 4900
tmux send-keys -t $SESSION:0.0 "go run main.go -config config-client-tcp-1.json -log-name Observer-1.log" C-m
# Pane 1: observer 1 - port 4901
tmux send-keys -t $SESSION:0.1 "go run main.go -config config-client-tcp-2.json -log-name Observer-2.log" C-m
# Pane 2: observer 2 - port 4902
tmux send-keys -t $SESSION:0.2 "go run main.go -config config-client-tcp-3.json -log-name Observer-3.log" C-m
tmux attach -t $SESSION
