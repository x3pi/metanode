#!/bin/bash

# --- 1. TỰ ĐỘNG CHẠY NGẦM VỚI TMUX ---
# Kiểm tra nếu chưa chạy trong tmux thì tự động tạo session chạy ngầm
if [ -z "$TMUX" ]; then
    # Lệnh này sẽ tạo một session chạy ngầm (-d) tên là 'monitor_port' và chạy chính file này ("$0")
    tmux new-session -d -s monitor_port "$0"
    echo "✅ Script đã được tự động đưa xuống chạy ngầm trong tmux (session: monitor_port)."
    echo "👉 Để xem log trực tiếp, gõ lệnh: tmux attach -t monitor_port"
    exit 0
fi

# --- 2. Cấu hình ---
TELEGRAM_BOT_TOKEN="8230176859:AAGoZ_78xzb1q4rgJJ5SYLxRhZBYBTSz_xo"
TELEGRAM_CHAT_ID="-1003867050625"
SERVER_IP="139.59.243.85"
PORTS=(4200 4201 8545)
SLEEP_TIME=60 # Thời gian chờ giữa các lần quét (giây)

# --- 3. Hàm gửi tin nhắn chung ---
send_to_telegram() {
    local message=$1
    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d chat_id="${TELEGRAM_CHAT_ID}" \
        -d text="${message}" > /dev/null
}

# Gửi thông báo khi bắt đầu khởi chạy
TIME_NOW=$(date "+%H:%M:%S %d/%m/%Y")
send_to_telegram "🚀 [System] Bắt đầu chạy ngầm giám sát Server ${SERVER_IP} lúc ${TIME_NOW}..."

# --- 4. Vòng lặp kiểm tra liên tục ---
while true; do
    for PORT in "${PORTS[@]}"; do
        # Kiểm tra bằng netcat
        if ! nc -z -w 5 $SERVER_IP $PORT; then
            send_to_telegram "⚠️ CẢNH BÁO: Port ${PORT} trên server ${SERVER_IP} hiện KHÔNG HOẠT ĐỘNG!"
        fi
    done
    # Nghỉ 60 giây rồi quét lại
    sleep $SLEEP_TIME
done