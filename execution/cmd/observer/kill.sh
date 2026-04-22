#!/bin/bash

SESSION="observer"

# Kiểm tra xem session có đang tồn tại không
tmux has-session -t $SESSION 2>/dev/null

if [ $? -eq 0 ]; then
    echo "Killing session $SESSION and stopping all processes..."
    # Lệnh này sẽ đóng tmux và kill luôn các tiến trình con bên trong
    tmux kill-session -t $SESSION
    echo "Done."
else
    echo "Session $SESSION is not running."
fi