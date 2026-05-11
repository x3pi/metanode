#!/bin/bash

# --- 1. TỰ ĐỘNG CHẠY NGẦM VỚI TMUX ---
if [ -z "$TMUX" ]; then
    tmux new-session -d -s monitor_port "$0"
    echo "✅ Script đã được tự động đưa xuống chạy ngầm trong tmux (session: monitor_port)."
    echo "👉 Xem log: tmux attach -t monitor_port"
    exit 0
fi

# --- 2. Cấu hình ---
TELEGRAM_BOT_TOKEN="8230176859:AAGoZ_78xzb1q4rgJJ5SYLxRhZBYBTSz_xo"
TELEGRAM_CHAT_ID="-1003867050625"
SERVER_IP="139.59.243.85"
PORTS=(4200 4201 8545)

SLEEP_TIME=60                 # check port mỗi 60s
BLOCK_REPORT_INTERVAL=28800   # 8 tiếng = 28800 giây

LAST_BLOCK_REPORT=0

# --- 3. Hàm gửi Telegram ---
send_to_telegram() {
    local message=$1

    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d chat_id="${TELEGRAM_CHAT_ID}" \
        -d text="${message}" > /dev/null
}

# --- 4. Lấy last block ETH ---
get_latest_block() {

    RESPONSE=$(curl -s -X POST http://${SERVER_IP}:8545 \
        -H "Content-Type: application/json" \
        --data '{
            "jsonrpc":"2.0",
            "method":"eth_blockNumber",
            "params":[],
            "id":1
        }')

    HEX_BLOCK=$(echo "$RESPONSE" | grep -o '"result":"0x[^"]*"' | cut -d'"' -f4)

    if [ -n "$HEX_BLOCK" ]; then
        DEC_BLOCK=$((HEX_BLOCK))
        echo "$DEC_BLOCK"
    else
        echo "Không lấy được block"
    fi
}

# --- 5. Thông báo khởi động ---
TIME_NOW=$(date "+%H:%M:%S %d/%m/%Y")

send_to_telegram "🚀 [System] Bắt đầu monitor server ${SERVER_IP} lúc ${TIME_NOW}"

# --- 6. Vòng lặp chính ---
while true; do

    # --- Check port ---
    for PORT in "${PORTS[@]}"; do

        if ! nc -z -w 5 $SERVER_IP $PORT; then
            send_to_telegram "⚠️ CẢNH BÁO: Port ${PORT} trên server ${SERVER_IP} KHÔNG HOẠT ĐỘNG!"
        fi

    done

    # --- Gửi last block mỗi 8 tiếng ---
    CURRENT_TIME=$(date +%s)
    ELAPSED=$((CURRENT_TIME - LAST_BLOCK_REPORT))

    if [ $ELAPSED -ge $BLOCK_REPORT_INTERVAL ]; then

        LAST_BLOCK=$(get_latest_block)

        REPORT_TIME=$(date "+%H:%M:%S %d/%m/%Y")

        send_to_telegram "⛓️ Metanode Node Report

🖥 Server: ${SERVER_IP}
📦 Last Block: ${LAST_BLOCK}
⏰ Time: ${REPORT_TIME}"

        LAST_BLOCK_REPORT=$CURRENT_TIME
    fi

    sleep $SLEEP_TIME
done