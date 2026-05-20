#!/bin/bash

# --- 1. Cấu hình ---
TELEGRAM_BOT_TOKEN="8230176859:AAGoZ_78xzb1q4rgJJ5SYLxRhZBYBTSz_xo"
TELEGRAM_CHAT_ID="-1003867050625"
SERVER_IP="139.59.243.85"
PORTS=(4200 4201 8545)

SLEEP_TIME=600                 # check port mỗi 10 phút (600s)
BLOCK_REPORT_INTERVAL=18000    # 5 tiếng = 18000 giây

LAST_BLOCK_REPORT=0

# --- 2. Hàm gửi Telegram ---
send_to_telegram() {
    local message=$1

    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
        -d chat_id="${TELEGRAM_CHAT_ID}" \
        -d text="${message}" > /dev/null
}

# --- 3. Lấy last block ETH ---
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

# --- 4. Thông báo khởi động ---
# Dùng giờ Việt Nam (Asia/Ho_Chi_Minh) cho toàn bộ log thời gian
TIME_NOW=$(TZ="Asia/Ho_Chi_Minh" date "+%H:%M:%S %d/%m/%Y")

send_to_telegram "🚀 [System] Bắt đầu monitor server ${SERVER_IP} lúc ${TIME_NOW}"

# --- 5. Vòng lặp chính ---
while true; do

    # --- Check port (Vẫn chạy 24/24 để cảnh báo rớt mạng) ---
    for PORT in "${PORTS[@]}"; do
        if ! nc -z -w 5 $SERVER_IP $PORT; then
            send_to_telegram "⚠️ CẢNH BÁO: Port ${PORT} trên server ${SERVER_IP} KHÔNG HOẠT ĐỘNG!"
        fi
    done

    # --- Gửi last block (Chỉ từ 8h -> 17h VN, mỗi 5 tiếng) ---
    CURRENT_TIME=$(date +%s)
    ELAPSED=$((CURRENT_TIME - LAST_BLOCK_REPORT))
    
    # Lấy giờ hiện tại theo múi giờ Việt Nam (định dạng 24h, từ 00 đến 23)
    CURRENT_HOUR=$(TZ="Asia/Ho_Chi_Minh" date +"%H")

    # Kiểm tra xem giờ hiện tại có nằm trong khoảng từ 08:xx đến 17:xx không
    if [ "$CURRENT_HOUR" -ge 8 ] && [ "$CURRENT_HOUR" -le 17 ]; then
        
        # Nếu đã qua 5 tiếng kể từ lần báo cáo cuối
        if [ $ELAPSED -ge $BLOCK_REPORT_INTERVAL ]; then

            LAST_BLOCK=$(get_latest_block)
            REPORT_TIME=$(TZ="Asia/Ho_Chi_Minh" date "+%H:%M:%S %d/%m/%Y")

            send_to_telegram "⛓️ Metanode Node Report

🖥 Server: ${SERVER_IP}
📦 Last Block: ${LAST_BLOCK}
⏰ Time: ${REPORT_TIME}"

            LAST_BLOCK_REPORT=$CURRENT_TIME
        fi
    fi

    sleep $SLEEP_TIME
done