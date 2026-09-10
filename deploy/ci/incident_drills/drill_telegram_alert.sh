#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
#  drill_telegram_alert.sh — SAFE drill (does NOT touch the cluster, disk, or
#  network). Verifies the exact alert-delivery path a real incident would use
#  actually reaches Telegram, end-to-end -- not just "config exists", but
#  "the message genuinely arrived" (checks the real HTTP response from the
#  Telegram API).
#
#  Why this matters: deploy/ansible/monitors/start_monitors.sh's send_tele()
#  reads TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID from env, falling back to
#  inventory.yml's telegram_bot_token/telegram_chat_id -- a typo'd token, an
#  expired bot, or a chat_id the bot was removed from all produce a SILENT
#  failure (the monitor script itself just does `|| true` and moves on) with
#  no visible symptom until a real incident happens and nobody gets paged.
#
#  Usage:
#    ./drill_telegram_alert.sh [--inventory PATH]
#
#  Safe to run any time, on any machine that can reach api.telegram.org and
#  has ansible/inventory.yml (or ANSIBLE_INVENTORY env) configured.
# ═══════════════════════════════════════════════════════════════════════════
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INVENTORY="${1:-}"
if [ "${1:-}" == "--inventory" ]; then
    INVENTORY="${2:-}"
fi
INVENTORY="${INVENTORY:-${SCRIPT_DIR}/../../ansible/inventory.yml}"

echo "=========================================================="
echo "📟 DRILL: Telegram Alert End-to-End Delivery"
echo "=========================================================="

strip_quotes() {
    # Removes a matching pair of leading/trailing single or double quotes, if present.
    local v="$1"
    v="${v%\"}"; v="${v#\"}"
    v="${v%\'}"; v="${v#\'}"
    echo "$v"
}

if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] && [ -f "$INVENTORY" ]; then
    BOT_TOKEN_RAW=$(grep -E '^\s*telegram_bot_token:' "$INVENTORY" | head -n 1 | awk '{print $2}')
    CHAT_ID_RAW=$(grep -E '^\s*telegram_chat_id:' "$INVENTORY" | head -n 1 | awk '{print $2}')
    BOT_TOKEN=$(strip_quotes "$BOT_TOKEN_RAW")
    CHAT_ID=$(strip_quotes "$CHAT_ID_RAW")
    [ -n "$BOT_TOKEN" ] && export TELEGRAM_BOT_TOKEN="$BOT_TOKEN"
    [ -n "$CHAT_ID" ] && export TELEGRAM_CHAT_ID="$CHAT_ID"
fi

if [ -z "${TELEGRAM_BOT_TOKEN:-}" ]; then
    echo "❌ [KHÔNG ĐẠT] Không tìm thấy TELEGRAM_BOT_TOKEN (env, hoặc trong ${INVENTORY})."
    echo "   👉 Đây CHÍNH LÀ loại lỗi drill này tồn tại để bắt: nếu điều này xảy ra thật"
    echo "      trong production, mọi cảnh báo (node crash, disk full, ...) sẽ bị"
    echo "      nuốt âm thầm -- không ai được báo khi có sự cố thật."
    exit 1
fi
if [ -z "${TELEGRAM_CHAT_ID:-}" ]; then
    echo "❌ [KHÔNG ĐẠT] Không tìm thấy TELEGRAM_CHAT_ID."
    exit 1
fi

echo "ℹ️  Bot token: ${TELEGRAM_BOT_TOKEN:0:8}...(ẩn phần còn lại)"
echo "ℹ️  Chat ID:   ${TELEGRAM_CHAT_ID}"
echo ""
echo "📤 Đang gửi tin nhắn TEST qua đúng API endpoint mà các cảnh báo thật dùng..."

DRILL_TIME="$(date '+%Y-%m-%d %H:%M:%S %Z')"
MSG="🧪 <b>[DRILL] Đây là tin nhắn TEST hệ thống cảnh báo</b> 🧪
Không phải sự cố thật -- chỉ để xác minh đường truyền Telegram còn hoạt động.
Thời gian: ${DRILL_TIME}
Máy gửi: $(hostname) ($(hostname -I 2>/dev/null | awk '{print $1}'))
👉 Nếu bạn nhận được tin nhắn này, đường truyền cảnh báo end-to-end đang hoạt động tốt."

HTTP_CODE=$(curl -s -o /tmp/drill_telegram_response.json -w "%{http_code}" \
    -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
    -d chat_id="${TELEGRAM_CHAT_ID}" \
    -d parse_mode="HTML" \
    --data-urlencode "text=${MSG}")

echo ""
echo "📥 HTTP response code: ${HTTP_CODE}"

if [ "$HTTP_CODE" == "200" ]; then
    MSG_ID=$(python3 -c "import json; print(json.load(open('/tmp/drill_telegram_response.json')).get('result', {}).get('message_id', '?'))" 2>/dev/null || echo "?")
    echo "✅ [PASS] Telegram API xác nhận đã nhận và gửi tin (message_id=${MSG_ID})."
    echo "   👉 Vào group/chat cấu hình (chat_id=${TELEGRAM_CHAT_ID}) xác nhận NGƯỜI TRỰC"
    echo "      thực sự thấy được tin nhắn này (không chỉ API trả 200 -- còn cần xác nhận"
    echo "      bot chưa bị mute/kick khỏi group, và người trực có bật thông báo)."
    rm -f /tmp/drill_telegram_response.json
    exit 0
else
    echo "❌ [KHÔNG ĐẠT] Telegram API từ chối tin nhắn (HTTP ${HTTP_CODE})."
    echo "   Phản hồi chi tiết:"
    cat /tmp/drill_telegram_response.json 2>/dev/null
    echo ""
    echo "   👉 Nguyên nhân thường gặp: bot_token sai/hết hạn, chat_id sai, hoặc bot đã"
    echo "      bị xoá khỏi group. Sửa lại telegram_bot_token/telegram_chat_id trong"
    echo "      inventory.yml rồi chạy lại drill này cho tới khi PASS."
    exit 1
fi
