#!/bin/bash

# Tên session tmux cần kill
SESSION="simple_chain"

# Kiểm tra xem session có tồn tại không
if tmux has-session -t $SESSION 2>/dev/null; then
    echo "Đang dừng session tmux: $SESSION"
    
    # Kill tất cả các process trong session
    tmux kill-session -t $SESSION
    
    echo "Session $SESSION đã được dừng thành công"
else
    echo "Session $SESSION không tồn tại hoặc đã được dừng"
fi

# Tùy chọn: Kill tất cả các process go run có thể còn sót lại
echo "Đang kiểm tra và dừng các process go run còn sót..."
pkill -f "go run . -config=config-.*\.json" 2>/dev/null || echo "Không có process go run nào cần dừng"

echo "Hoàn tất!"