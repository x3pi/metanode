#!/bin/bash
# ============================================================
#  watch_log.sh - Lấy log realtime từ server qua WebSocket
#  Nhấn Ctrl+C để dừng
# ============================================================

# --- Cấu hình mặc định ---
DEFAULT_HOST="139.59.243.85"
DEFAULT_PORT="8747"
DEFAULT_FILE="App.log"
# Lấy thư mục chứa script
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# --- Đọc tham số ---
HOST="${1:-$DEFAULT_HOST}"
PORT="${2:-$DEFAULT_PORT}"
LOG_FILE="${3:-$DEFAULT_FILE}"

# Tên file output với timestamp, lưu cùng cấp với script
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
OUTPUT_FILE="${SCRIPT_DIR}/debug_${TIMESTAMP}.log"

WS_URL="ws://${HOST}:${PORT}/debug/logs/ws?file=${LOG_FILE}"

echo "============================================================"
echo "  📡 REALTIME LOG WATCHER"
echo "============================================================"
echo "  Server:  ${HOST}:${PORT}"
echo "  File:    ${LOG_FILE}"
echo "  Output:  ${OUTPUT_FILE}"
echo "  URL:     ${WS_URL}"
echo "------------------------------------------------------------"
echo "  Nhấn Ctrl+C để dừng"
echo "============================================================"
echo ""

# Bắt tín hiệu Ctrl+C để hiện thông báo khi tắt
trap 'echo ""; echo "------------------------------------------------------------"; echo "  🛑 Đã dừng. Log đã lưu tại: ${OUTPUT_FILE}"; echo "  📊 Tổng số dòng: $(wc -l < "${OUTPUT_FILE}")"; echo "------------------------------------------------------------"; exit 0' INT TERM

# Chạy wscat + tee (foreground, Ctrl+C để dừng)
wscat -c "${WS_URL}" 2>&1 | tee "${OUTPUT_FILE}"
