#!/bin/bash

SESSION="simple_chain"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR" || { echo "Không thể cd vào thư mục script"; exit 1; }

# Xóa session cũ
tmux kill-session -t "$SESSION" 2>/dev/null

# Tạo session mới và 5 pane
tmux new-session -d -s "$SESSION" -n "NODES"
tmux split-window -h -t "$SESSION":0.0    # Pane 1
tmux split-window -v -t "$SESSION":0.0    # Pane 2
tmux split-window -v -t "$SESSION":0.1    # Pane 3
tmux split-window -v -t "$SESSION":0.2    # Pane 4

tmux select-layout -t "$SESSION":0 tiled

# Xapian path được tự động đọc từ Databases.XapianPath trong config.json
# App tự tạo thư mục nếu chưa có — không cần mkdir thủ công

tmux send-keys -t "$SESSION":0.0 "
echo '=== Starting MASTER ==='
go run . -config=config-master.json
" C-m

tmux send-keys -t "$SESSION":0.1 "
sleep 3
echo '=== Starting MASTER READ-ONLY ==='
go run . -config=config-master-read-only.json
" C-m

tmux send-keys -t "$SESSION":0.2 "
sleep 6
echo '=== Starting WRITE NODE ==='
go run . -config=config-sub-write.json
" C-m

tmux send-keys -t "$SESSION":0.3 "
sleep 10
echo '=== Starting WRITE-2 NODE ==='
go run . -config=config-sub-write-2.json
" C-m

tmux send-keys -t "$SESSION":0.4 "
sleep 12
echo '=== Starting SUB READ-ONLY ==='
go run . -config=config-sub-read-only.json
" C-m

tmux attach-session -t "$SESSION"
