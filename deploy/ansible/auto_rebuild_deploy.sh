#!/bin/bash
# ============================================================================
# Git Auto-Rebuild & Deploy Daemon
# Periodically checks for new commits on remote and triggers deploy/restart.
# ============================================================================

set -u

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ANSIBLE_DIR="${PROJECT_ROOT}/deploy/ansible"
PID_FILE="${ANSIBLE_DIR}/auto_deploy.pid"
LOG_FILE="${ANSIBLE_DIR}/auto_deploy.log"
LAST_DEPLOYED_FILE="${ANSIBLE_DIR}/.last_deployed_commit"
CHECK_INTERVAL=5
REMOTE="origin"

is_running() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | xargs)
        if [ -n "$pid" ] && ps -p "$pid" >/dev/null 2>&1; then
            return 0
        fi
    fi
    return 1
}

cmd_stop() {
    if is_running; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | xargs)
        echo "🛑 Đang dừng Auto-Deploy Watcher (PID: $pid)..."
        pkill -P "$pid" 2>/dev/null || true
        kill "$pid" 2>/dev/null || true
        rm -f "$PID_FILE"
        echo "✅ Watcher đã được dừng thành công."
    else
        local stray_pids
        stray_pids=$(pgrep -f "auto_rebuild_deploy.sh" 2>/dev/null | grep -v "$$" || true)
        if [ -n "$stray_pids" ]; then
            echo "🛑 Đang dọn dẹp các tiến trình watcher đang chạy (PID: $stray_pids)..."
            kill $stray_pids 2>/dev/null || true
            rm -f "$PID_FILE"
            echo "✅ Đã dừng các tiến trình watcher."
        else
            echo "ℹ️ Auto-Deploy Watcher hiện không chạy."
            rm -f "$PID_FILE"
        fi
    fi
}

cmd_status() {
    echo "=========================================================="
    echo "📊 TRẠNG THÁI GIT AUTO-DEPLOY WATCHER"
    echo "=========================================================="
    if is_running; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | xargs)
        echo "🟢 Trạng thái : ĐANG CHẠY (PID: $pid)"
    else
        echo "🔴 Trạng thái : ĐÃ DỪNG"
    fi
    local last_commit="(Chưa có)"
    if [ -f "$LAST_DEPLOYED_FILE" ]; then
        last_commit=$(cat "$LAST_DEPLOYED_FILE" 2>/dev/null | xargs)
    fi
    echo "📌 Commit mốc : $last_commit"
    echo "📜 File log   : $LOG_FILE"
    echo "=========================================================="
}

cmd_logs() {
    if [ ! -f "$LOG_FILE" ]; then
        touch "$LOG_FILE"
    fi
    echo "📜 Đang theo dõi log ($LOG_FILE)... (Nhấn Ctrl+C để thoát)"
    tail -n 30 -f "$LOG_FILE"
}

# Check for subcommands first
case "${1:-}" in
    stop)
        cmd_stop
        exit 0
        ;;
    status)
        cmd_status
        exit 0
        ;;
    logs)
        cmd_logs
        exit 0
        ;;
esac

# Check for flags
DAEMON_MODE=false
FORCE_INITIAL_DEPLOY=false
CUSTOM_BRANCH=""
args=()

while [ $# -gt 0 ]; do
    case "$1" in
        -d|--daemon|start)
            DAEMON_MODE=true
            shift
            ;;
        --initial-deploy)
            FORCE_INITIAL_DEPLOY=true
            shift
            ;;
        --branch)
            CUSTOM_BRANCH="$2"
            shift 2
            ;;
        stop)
            cmd_stop
            exit 0
            ;;
        status)
            cmd_status
            exit 0
            ;;
        logs)
            cmd_logs
            exit 0
            ;;
        *)
            args+=("$1")
            shift
            ;;
    esac
done

if [ "$DAEMON_MODE" = true ]; then
    if is_running; then
        local_pid=$(cat "$PID_FILE" 2>/dev/null | xargs)
        echo "⚠️  Watcher daemon đang chạy với PID: $local_pid"
        echo "📜 Xem log: $0 logs"
        echo "🛑 Dừng:    $0 stop"
        exit 0
    fi

    CHILD_ARGS=()
    if [ -n "$CUSTOM_BRANCH" ]; then
        CHILD_ARGS+=(--branch "$CUSTOM_BRANCH")
    fi
    if [ "$FORCE_INITIAL_DEPLOY" = true ]; then
        CHILD_ARGS+=(--initial-deploy)
    fi
    if [ ${#args[@]} -gt 0 ]; then
        CHILD_ARGS+=("${args[@]}")
    fi

    echo "🚀 Starting Git Auto-Deploy Watcher in background daemon mode..."
    nohup "$0" ${CHILD_ARGS[@]+"${CHILD_ARGS[@]}"} > "$LOG_FILE" 2>&1 &
    NEW_PID=$!
    echo "$NEW_PID" > "$PID_FILE"
    echo "✅ Watcher is now running in the background (PID: $NEW_PID)."
    echo "📜 To view logs, run: $0 logs  (or tail -f $LOG_FILE)"
    echo "🛑 To stop it, run:   $0 stop"
    exit 0
fi

cd "$PROJECT_ROOT"

# Auto-detect current active branch if not specified (defaults to main)
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "main")
if [ "$CURRENT_BRANCH" = "HEAD" ] || [ -z "$CURRENT_BRANCH" ]; then
    CURRENT_BRANCH="main"
fi
BRANCH="${CUSTOM_BRANCH:-$CURRENT_BRANCH}"

echo "👀 Starting Git Auto-Deploy Watcher..."
echo "📍 Project Root: $PROJECT_ROOT"
echo "📍 Tracking remote: $REMOTE/$BRANCH"
echo "⏰ Check interval: ${CHECK_INTERVAL}s"

# Ensure we are tracking the branch correctly
git checkout "$BRANCH" 2>/dev/null || true

LOCAL_HASH=$(git rev-parse HEAD 2>/dev/null || echo "")

# Handle initial startup
if [ "$FORCE_INITIAL_DEPLOY" = true ]; then
    echo "🚀 Performing initial deployment as requested via --initial-deploy..."
    cd "$ANSIBLE_DIR"
    export DEPLOY_SOURCE="Auto-Deploy (Initial Run)"
    if ! ./ansible_deploy.sh ${args[@]+"${args[@]}"}; then
        echo "❌ Initial deploy failed! Exiting auto-deploy watcher."
        exit 1
    fi
    echo "✅ Initial deploy successful."
    echo "$LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
else
    # Mặc định: Ghi nhận commit hiện tại ở local làm mốc ban đầu, KHÔNG deploy ngay
    echo "📌 Đã ghi nhận commit hiện tại ở local: ${LOCAL_HASH:0:8}"
    echo "$LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
    echo "💡 Watcher sẽ chờ khi nào có commit mới từ remote (${REMOTE}/${BRANCH}) mới thực hiện pull, build và cập nhật hệ thống."
fi

cd "$PROJECT_ROOT"
echo "👀 Entering Watcher mode. Polling every ${CHECK_INTERVAL}s..."

while true; do
    # Fetch from remote without merging
    if git fetch "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
        REMOTE_HASH=$(git rev-parse "${REMOTE}/${BRANCH}" 2>/dev/null || echo "")
        LOCAL_HASH=$(git rev-parse HEAD 2>/dev/null || echo "")
        
        # Read the last deployed commit hash
        LAST_DEPLOYED=""
        if [ -f "$LAST_DEPLOYED_FILE" ]; then
            LAST_DEPLOYED=$(cat "$LAST_DEPLOYED_FILE" | xargs)
        fi
        
        # Chỉ kích hoạt khi remote có commit mới khác commit đã lưu/đã deploy
        if [ -n "$REMOTE_HASH" ] && [ "$REMOTE_HASH" != "$LAST_DEPLOYED" ]; then
            echo -e "\n🔔 [$(date '+%Y-%m-%d %H:%M:%S')] Phát hiện commit mới trên remote!"
            PREV_COMMIT="${LAST_DEPLOYED:0:8}"
            echo "   Commit đã ghi nhận trước: ${PREV_COMMIT:-"(Chưa có)"}"
            echo "   Commit mới trên remote  : ${REMOTE_HASH:0:8}"
            
            # Nếu local chưa có commit này thì kéo về
            if [ "$LOCAL_HASH" != "$REMOTE_HASH" ]; then
                echo "🔄 Đang kéo mã nguồn mới từ ${REMOTE}/${BRANCH}..."
                git pull "$REMOTE" "$BRANCH"
            else
                echo "ℹ️ Local đã có sẵn commit ${REMOTE_HASH:0:8}."
            fi
            
            # Extract new commit details for notification
            NEW_LOCAL_HASH=$(git rev-parse HEAD)
            COMMIT_MSG=$(git log -1 --pretty=%B | head -n 1)
            COMMIT_AUTHOR=$(git log -1 --pretty=%an)
            export DEPLOY_SOURCE="Auto-Deploy (Branch: ${BRANCH}, Git Commit ${NEW_LOCAL_HASH:0:8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"
            
            echo "🚀 Kích hoạt build & deploy hệ thống cho $DEPLOY_SOURCE..."
            cd "$ANSIBLE_DIR"
            ./ansible_deploy.sh --start --fast ${args[@]+"${args[@]}"}
            
            # CẬP NHẬT COMMIT ĐÃ DEPLOY VÀO FILE ĐỂ KHÔNG DEPLOY LẶP LẠI
            echo "$NEW_LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
            
            # Go back to root
            cd "$PROJECT_ROOT"
            echo "✅ Hoàn tất lượt cập nhật cho commit ${NEW_LOCAL_HASH:0:8}. Tiếp tục theo dõi..."
        fi
    else
        echo "⚠️ [$(date '+%Y-%m-%d %H:%M:%S')] Không thể kết nối fetch từ git remote (${REMOTE}/${BRANCH}). Sẽ thử lại sau..."
    fi
    
    sleep $CHECK_INTERVAL
done
