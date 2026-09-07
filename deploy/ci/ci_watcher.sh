#!/usr/bin/env bash
# ./ci.sh start      # Bật daemon chạy ngầm
# ./ci.sh status     # Xem trạng thái daemon
# ./ci.sh logs       # Xem log realtime
# ./ci.sh stop       # Dừng daemon
# cd /home/abc/nhat/con-chain-v2/metanode
# ./ci.sh run-now --only tps_blast
# ./ci.sh run-now
# ==============================================================================
# 🌐 METANODE CI/CD GIT WATCHER DAEMON
# Lắng nghe commit mới trên remote Git (nhánh main) và tự động kích hoạt CI Runner.
# ==============================================================================

set -u

REAL_SCRIPT_PATH="$(readlink -f "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd "$(dirname "${REAL_SCRIPT_PATH}")" && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/ci_config.yaml"
PID_FILE="${SCRIPT_DIR}/ci_watcher.pid"
LOG_FILE="${SCRIPT_DIR}/ci_watcher.log"
LAST_COMMIT_FILE="${SCRIPT_DIR}/.last_tested_commit"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "❌ Không tìm thấy file cấu hình: ${CONFIG_FILE}"
    if [ -f "${SCRIPT_DIR}/ci_config.yaml.example" ]; then
        echo "👉 Vui lòng sao chép từ file mẫu và cấu hình thông số cần thiết:"
        echo "   cp ${SCRIPT_DIR}/ci_config.yaml.example ${CONFIG_FILE}"
    fi
    exit 1
fi

# Lấy các thông số cơ bản từ file ci_config.yaml bằng python
get_config_val() {
    python3 -c "
import yaml, sys
try:
    with open('${CONFIG_FILE}', 'r') as f:
        c = yaml.safe_load(f)
    keys = '$1'.split('.')
    val = c
    for k in keys:
        val = val.get(k, {})
    print(val if isinstance(val, (str, int, bool)) else '')
except Exception:
    print('')
"
}

DEFAULT_REPO_PATH="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REPO_PATH="$(get_config_val 'git.repo_path')"
if [ -z "$REPO_PATH" ] || [ "$REPO_PATH" = "auto" ] || [ "$REPO_PATH" = "." ]; then
    REPO_PATH="$DEFAULT_REPO_PATH"
elif [[ "$REPO_PATH" != /* ]]; then
    REPO_PATH="$(cd "${DEFAULT_REPO_PATH}/${REPO_PATH}" 2>/dev/null && pwd || echo "${DEFAULT_REPO_PATH}")"
fi
BRANCH="$(get_config_val 'git.branch')"
BRANCH="${BRANCH:-"main"}"
REMOTE="$(get_config_val 'git.remote')"
REMOTE="${REMOTE:-"origin"}"
INTERVAL="$(get_config_val 'git.poll_interval_seconds')"
INTERVAL="${INTERVAL:-30}"

is_running() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        if ps -p "$pid" > /dev/null 2>&1; then
            return 0
        fi
    fi
    return 1
}

cmd_start() {
    if is_running; then
        echo "⚠️  Watcher daemon đang chạy với PID: $(cat "$PID_FILE")"
        echo "📜 Xem log: $0 logs"
        exit 0
    fi

    echo "🚀 Đang khởi động Metanode CI Watcher Daemon..."
    echo "📍 Giám sát: ${REMOTE}/${BRANCH} tại ${REPO_PATH}"
    echo "⏰ Chu kỳ polling: ${INTERVAL}s"
    
    nohup "$0" __internal_loop > "$LOG_FILE" 2>&1 &
    local new_pid=$!
    echo "$new_pid" > "$PID_FILE"
    echo "✅ Watcher đã chạy ngầm thành công! (PID: ${new_pid})"
    echo "📜 Xem log realtime: $0 logs"
}

cmd_stop() {
    if is_running; then
        local pid=$(cat "$PID_FILE")
        echo "🛑 Đang dừng Watcher daemon (PID: ${pid})..."
        kill "$pid" 2>/dev/null || true
        rm -f "$PID_FILE"
        echo "✅ Watcher đã được dừng."
    else
        echo "ℹ️  Watcher daemon hiện không chạy."
        rm -f "$PID_FILE"
    fi
}

cmd_status() {
    echo "=========================================================="
    echo "📊 TRẠNG THÁI METANODE CI WATCHER DAEMON"
    echo "=========================================================="
    echo "📍 Kho mã nguồn : ${REPO_PATH}"
    echo "🌿 Nhánh theo dõi: ${REMOTE}/${BRANCH}"
    echo "⏰ Chu kỳ kiểm tra: ${INTERVAL}s"
    
    if is_running; then
        echo "🟢 Trạng thái   : ĐANG CHẠY (PID: $(cat "$PID_FILE"))"
    else
        echo "🔴 Trạng thái   : ĐÃ DỪNG"
    fi

    if [ -f "$LAST_COMMIT_FILE" ]; then
        echo "📌 Commit đã test gần nhất: $(cat "$LAST_COMMIT_FILE")"
    else
        echo "📌 Commit đã test gần nhất: (Chưa có dữ liệu)"
    fi
    echo "=========================================================="
}

cmd_logs() {
    if [ ! -f "$LOG_FILE" ]; then
        touch "$LOG_FILE"
    fi
    echo "📜 Đang theo dõi log của CI Watcher (${LOG_FILE})... (Nhấn Ctrl+C để thoát)"
    tail -n 30 -f "$LOG_FILE"
}

cmd_run_now() {
    echo "⚡ Kích hoạt CI Test Runner thủ công ngay lập tức..."
    cd "$SCRIPT_DIR"
    python3 "${SCRIPT_DIR}/ci_runner.py" "$@"
}

__internal_loop() {
    echo "👀 [$(date '+%Y-%m-%d %H:%M:%S')] CI Watcher Daemon đã bắt đầu theo dõi: ${REMOTE}/${BRANCH}"
    cd "$REPO_PATH" || exit 1

    while true; do
        # Sử dụng git ls-remote để kiểm tra commit mới nhất trên remote server mà KHÔNG cần đụng chạm working tree
        REMOTE_HASH=$(git ls-remote "$REMOTE" "refs/heads/$BRANCH" 2>/dev/null | awk '{print $1}')
        
        if [ -n "$REMOTE_HASH" ]; then
            LAST_HASH=""
            if [ -f "$LAST_COMMIT_FILE" ]; then
                LAST_HASH=$(cat "$LAST_COMMIT_FILE" | xargs)
            fi

            # Nếu commit trên remote khác với commit đã test trước đó
            if [ "$REMOTE_HASH" != "$LAST_HASH" ]; then
                echo -e "\n🔔 [$(date '+%Y-%m-%d %H:%M:%S')] PHÁT HIỆN COMMIT MỚI TRÊN ${REMOTE}/${BRANCH}!"
                echo "   👉 Commit cũ: ${LAST_HASH:-"(Chưa có)"}"
                echo "   👉 Commit mới: ${REMOTE_HASH}"
                echo "🚀 Bắt đầu khởi chạy toàn bộ Test Pipeline..."

                cd "$SCRIPT_DIR"
                # Chạy CI Runner với cờ --pull để tự động lấy code mới nhất về
                python3 "${SCRIPT_DIR}/ci_runner.py" --pull
                RUNNER_EXIT=$?

                # Ghi nhận hash đã test để không bị lặp lại
                echo "$REMOTE_HASH" > "$LAST_COMMIT_FILE"

                echo "🏁 [$(date '+%Y-%m-%d %H:%M:%S')] Hoàn tất lượt chạy CI cho commit ${REMOTE_HASH:0:8} (Mã thoát: ${RUNNER_EXIT})."
                cd "$REPO_PATH" || true
            fi
        else
            echo "⚠️ [$(date '+%Y-%m-%d %H:%M:%S')] Không thể kết nối git remote để kiểm tra commit. Sẽ thử lại sau ${INTERVAL}s..."
        fi

        sleep "$INTERVAL"
    done
}

case "${1:-status}" in
    start)
        cmd_start
        ;;
    stop)
        cmd_stop
        ;;
    restart)
        cmd_stop
        sleep 1
        cmd_start
        ;;
    status)
        cmd_status
        ;;
    logs)
        cmd_logs
        ;;
    run-now)
        shift
        cmd_run_now "$@"
        ;;
    __internal_loop)
        __internal_loop
        ;;
    -h|--help|help)
        echo "Cách sử dụng: $0 {start|stop|restart|status|logs|run-now [flags]}"
        echo ""
        echo "Lệnh:"
        echo "  start     Khởi động watcher daemon ngầm"
        echo "  stop      Dừng watcher daemon"
        echo "  restart   Khởi động lại watcher daemon"
        echo "  status    Xem trạng thái hoạt động và commit đã test gần nhất"
        echo "  logs      Xem file log realtime của watcher"
        echo "  run-now   Kích hoạt chạy test ngay lập tức (không cần đợi commit)"
        echo ""
        echo "Ví dụ chạy test cụ thể:"
        echo "  $0 run-now --only tps_blast       # Chỉ test bài TPS"
        echo "  $0 run-now --dry-run             # Xem trước kế hoạch chạy"
        ;;
    *)
        echo "❌ Lệnh không hợp lệ: $1"
        echo "Sử dụng '$0 help' để xem hướng dẫn."
        exit 1
        ;;
esac
