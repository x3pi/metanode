#!/bin/bash
# ============================================================================
# Git Auto-Rebuild & Deploy Daemon
# Periodically checks for new commits on remote, runs Build Verification,
# and triggers deploy/restart (immediately or at scheduled time e.g. 21:00).
# ============================================================================

set -u

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ANSIBLE_DIR="${PROJECT_ROOT}/deploy/ansible"
PID_FILE="${ANSIBLE_DIR}/auto_deploy.pid"
LOCK_FILE="${ANSIBLE_DIR}/auto_deploy.lock"
LOG_FILE="${ANSIBLE_DIR}/auto_deploy.log"
LAST_DEPLOYED_FILE="${ANSIBLE_DIR}/.last_deployed_commit"
CHECK_INTERVAL=5
REMOTE="origin"

TELEGRAM_BOT_TOKEN=""
TELEGRAM_CHAT_ID=""

load_telegram_config() {
    local env_file="${ANSIBLE_DIR}/.env"
    local token=""
    local chat_id=""
    if [ -f "$env_file" ]; then
        token=$(grep -E '^\s*TELEGRAM_BOT_TOKEN=' "$env_file" 2>/dev/null | head -n 1 | cut -d'=' -f2- | tr -d '"\r' || true)
        chat_id=$(grep -E '^\s*TELEGRAM_CHAT_ID=' "$env_file" 2>/dev/null | head -n 1 | cut -d'=' -f2- | tr -d '"\r' || true)
    fi
    if [ -z "$token" ] && [ -f "${ANSIBLE_DIR}/inventory.yml" ]; then
        token=$(grep -E '^\s*telegram_bot_token:' "${ANSIBLE_DIR}/inventory.yml" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
        chat_id=$(grep -E '^\s*telegram_chat_id:' "${ANSIBLE_DIR}/inventory.yml" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
    fi
    [ -n "$token" ] && TELEGRAM_BOT_TOKEN="$token"
    [ -n "$chat_id" ] && TELEGRAM_CHAT_ID="$chat_id"
    export TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
    export TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-"-1003867050625"}"
}

send_telegram_notification() {
    local message="$1"
    if [ -n "${TELEGRAM_BOT_TOKEN:-}" ]; then
        curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
            -d "chat_id=${TELEGRAM_CHAT_ID}" \
            -d "parse_mode=HTML" \
            --data-urlencode "text=${message}" > /dev/null 2>&1 || true
    fi
}

is_running() {
    # 1. Kiểm tra qua PID file nếu tồn tại
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | xargs || echo "")
        if [ -n "$pid" ] && [ "$pid" != "$$" ] && ps -p "$pid" >/dev/null 2>&1; then
            return 0
        fi
    fi
    # 2. Kiểm tra qua file lock kernel (nếu file lock đang bị giữ bởi tiến trình khác)
    if [ -f "$LOCK_FILE" ]; then
        if ! flock -n "$LOCK_FILE" true >/dev/null 2>&1; then
            return 0
        fi
    fi
    return 1
}

cmd_stop() {
    local stopped=false
    local target_pids=""
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | xargs || echo "")
        [ -n "$pid" ] && target_pids="$pid"
    fi

    local stray_pids
    stray_pids=$(pgrep -f "auto_rebuild_deploy.sh" 2>/dev/null | grep -v "$$" || true)
    target_pids=$(echo "$target_pids $stray_pids" | xargs -n1 | sort -u | xargs || echo "")

    if [ -n "$target_pids" ]; then
        echo "🛑 Đang dừng Auto-Deploy Watcher (PID: $target_pids)..."
        for p in $target_pids; do
            pkill -9 -P "$p" 2>/dev/null || true
            kill -TERM "$p" 2>/dev/null || true
        done
        sleep 0.2
        for p in $target_pids; do
            if ps -p "$p" >/dev/null 2>&1; then
                kill -9 "$p" 2>/dev/null || true
            fi
        done
        stopped=true
    fi
    rm -f "$PID_FILE" "$LOCK_FILE"
    if [ "$stopped" = true ]; then
        echo "✅ Watcher đã được dừng thành công."
    else
        echo "ℹ️ Auto-Deploy Watcher hiện không chạy."
    fi
}

cmd_help() {
    cat << 'EOF'
🚀 Git Auto-Rebuild & Deploy Daemon for Metanode

CÚ PHÁP:
    ./auto_rebuild_deploy.sh [LỆNH | TÙY CHỌN]

LỆNH ĐIỀU KHIỂN:
    start                  Khởi động watcher chạy ngầm (daemon)
    stop                   Dừng watcher và dọn dẹp sạch sẽ
    status                 Xem trạng thái hoạt động của watcher
    logs                   Xem log thời gian thực (tail -f)
    help, -h, --help       Hiển thị hướng dẫn này

TÙY CHỌN KHỞI ĐỘNG:
    --immediate, --now     ⚡ Kích hoạt deploy ngay lập tức khi kéo code mới về và build check pass (không hẹn giờ)
    --at <HH:MM>           🕒 Hẹn giờ deploy (mặc định: 21:00 giờ Việt Nam Asia/Ho_Chi_Minh)
    --branch <tên_nhánh>   Chỉ định nhánh git cần theo dõi (mặc định: main)
    --initial-deploy       Kích hoạt deploy ngay 1 lần lúc vừa bật watcher
    -d, --daemon           Chạy dưới dạng tiến trình ngầm (tương đương lệnh 'start')

VÍ DỤ SỬ DỤNG:
    # 1. Chế độ deploy ngay lập tức (kéo commit về -> build check pass -> deploy restart ngay):
    ./auto_rebuild_deploy.sh start --immediate
    (hoặc: ./auto_rebuild_deploy.sh start --now)

    # 2. Chạy hẹn giờ mặc định 21:00 tối:
    ./auto_rebuild_deploy.sh start

    # 3. Hẹn giờ lúc 23:30:
    ./auto_rebuild_deploy.sh start --at 23:30

    # 4. Kiểm tra trạng thái:
    ./auto_rebuild_deploy.sh status
EOF
}

cmd_status() {
    echo "=========================================================="
    echo "📊 TRẠNG THÁI GIT AUTO-DEPLOY WATCHER"
    echo "=========================================================="
    if is_running; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | xargs)
        echo "🟢 Trạng thái       : ĐANG CHẠY (PID: $pid)"
        local cmdline
        cmdline=$(ps -p "$pid" -o args= 2>/dev/null || echo "")
        if echo "$cmdline" | grep -q -- "--immediate"; then
            echo "⚡ Chế độ deploy    : NGAY LẬP TỨC (--immediate)"
        elif echo "$cmdline" | grep -oE -- "--at [0-9:]+" >/dev/null 2>&1; then
            local at_val
            at_val=$(echo "$cmdline" | grep -oE -- "--at [0-9:]+" | awk '{print $2}')
            echo "⏰ Lịch hẹn deploy  : 🕒 ${at_val} (Asia/Ho_Chi_Minh)"
        fi
    else
        echo "🔴 Trạng thái       : ĐÃ DỪNG"
    fi
    local last_commit="(Chưa có)"
    if [ -f "$LAST_DEPLOYED_FILE" ]; then
        last_commit=$(cat "$LAST_DEPLOYED_FILE" 2>/dev/null | xargs)
    fi
    echo "📌 Commit đã deploy : ${last_commit:0:8}"
    echo "📍 Commit hiện tại  : $(git rev-parse --short HEAD 2>/dev/null || echo "Unknown")"
    echo "📜 File log         : $LOG_FILE"
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
    help|-h|--help)
        cmd_help
        exit 0
        ;;
esac

# Check for flags
DAEMON_MODE=false
FORCE_INITIAL_DEPLOY=false
CUSTOM_BRANCH=""
IS_IMMEDIATE=false
SCHEDULE_AT="21:00"
args=()

while [ $# -gt 0 ]; do
    case "$1" in
        -d|--daemon|start)
            DAEMON_MODE=true
            shift
            ;;
        --immediate|--now|--no-schedule)
            IS_IMMEDIATE=true
            SCHEDULE_AT=""
            shift
            ;;
        --initial-deploy)
            FORCE_INITIAL_DEPLOY=true
            shift
            ;;
        --at|--schedule)
            if [ -z "${2:-}" ] || [ "${2:-}" = "none" ] || [ "${2:-}" = "false" ]; then
                IS_IMMEDIATE=true
                SCHEDULE_AT=""
            else
                SCHEDULE_AT="$2"
                IS_IMMEDIATE=false
            fi
            shift 2
            ;;
        --branch)
            CUSTOM_BRANCH="$2"
            shift 2
            ;;
        -h|--help|help)
            cmd_help
            exit 0
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

# Kiểm tra chặn chạy trùng lặp: CHỈ CHO PHÉP DUY NHẤT 1 TIẾN TRÌNH CHẠY (Áp dụng cho CẢ daemon và foreground)
if is_running; then
    running_pid=$(cat "$PID_FILE" 2>/dev/null | xargs || true)
    if [ -z "$running_pid" ]; then
        running_pid=$(pgrep -f "auto_rebuild_deploy.sh" 2>/dev/null | grep -v "$$" | head -n 1 || true)
    fi
    echo "⚠️  Watcher đang chạy với PID: ${running_pid:-"Không rõ"}"
    echo "📜 Xem log: $0 logs"
    echo "🛑 Dừng:    $0 stop"
    exit 0
fi

if [ "$DAEMON_MODE" = true ]; then
    CHILD_ARGS=()
    if [ -n "$CUSTOM_BRANCH" ]; then
        CHILD_ARGS+=(--branch "$CUSTOM_BRANCH")
    fi
    if [ "$IS_IMMEDIATE" = true ] || [ -z "$SCHEDULE_AT" ]; then
        CHILD_ARGS+=(--immediate)
    else
        CHILD_ARGS+=(--at "$SCHEDULE_AT")
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

# Thiết lập File Lock cấp Linux Kernel độc quyền (flock) để loại trừ 100% race condition
exec 200>"$LOCK_FILE"
if ! flock -n 200; then
    echo "❌ [LỖI] Đã có một tiến trình Watcher khác đang giữ lock!"
    echo "🛑 Dừng: $0 stop"
    exit 1
fi

# Ghi nhận PID cho tiến trình đang chạy và dọn dẹp file khi thoát
echo "$$" > "$PID_FILE"
trap 'rm -f "$PID_FILE" "$LOCK_FILE"' EXIT INT TERM

cd "$PROJECT_ROOT"

# Auto-detect current active branch if not specified (defaults to main)
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "main")
if [ "$CURRENT_BRANCH" = "HEAD" ] || [ -z "$CURRENT_BRANCH" ]; then
    CURRENT_BRANCH="main"
fi
BRANCH="${CUSTOM_BRANCH:-$CURRENT_BRANCH}"

load_telegram_config

echo "👀 Starting Git Auto-Deploy Watcher..."
echo "📍 Project Root   : $PROJECT_ROOT"
echo "📍 Tracking remote: $REMOTE/$BRANCH"
echo "⏰ Check interval : ${CHECK_INTERVAL}s"

# Ensure we are tracking the branch correctly
git checkout "$BRANCH" 2>/dev/null || true

LOCAL_HASH=$(git rev-parse HEAD 2>/dev/null || echo "")
BUILD_VERIFIED=false
LAST_VERIFIED_HASH=""

# Helper function to run Build Check and notify Telegram
verify_local_commit() {
    local target_hash="$1"
    local build_script="${PROJECT_ROOT}/consensus/metanode/scripts/build_check.sh"
    local build_log="/tmp/build_check_${target_hash:0:8}.log"
    local build_start
    build_start=$(date +%s)
    
    echo "🔨 [$(TZ='Asia/Ho_Chi_Minh' date '+%Y-%m-%d %H:%M:%S %Z')] Bắt đầu chạy Build Check (Go + Rust + FFI)..."
    if bash "$build_script" --fast > "$build_log" 2>&1; then
        local build_end
        build_end=$(date +%s)
        local build_dur=$((build_end - build_start))
        BUILD_VERIFIED=true
        LAST_VERIFIED_HASH="$target_hash"
        echo "✅ Build Check THÀNH CÔNG trong ${build_dur}s!"
        
        local sched_label="ngay sau đây"
        if [ -n "$SCHEDULE_AT" ]; then
            sched_label="lúc <b>${SCHEDULE_AT} (Asia/Ho_Chi_Minh)</b>"
        fi
        
        send_telegram_notification "✅ <b>[Build Check THÀNH CÔNG]</b>
• <b>Commit:</b> <code>${target_hash:0:8}</code>
• <b>Thời gian kiểm tra:</b> ${build_dur}s
• <b>Trạng thái:</b> Đã biên dịch sạch (Go + Rust + FFI)!
⏰ <b>Lịch deploy:</b> Đã sẵn sàng deploy vào ${sched_label}. Các node hiện tại vẫn hoạt động bình thường."
        return 0
    else
        local build_end
        build_end=$(date +%s)
        local build_dur=$((build_end - build_start))
        BUILD_VERIFIED=false
        LAST_VERIFIED_HASH=""
        local err_snippet
        err_snippet=$(tail -n 12 "$build_log" 2>/dev/null | tr '<>' '[]' | head -c 1200)
        echo "❌ Build Check THẤT BẠI sau ${build_dur}s!"
        
        local sched_notice=""
        if [ -n "$SCHEDULE_AT" ]; then
            sched_notice="Lịch deploy lúc <b>${SCHEDULE_AT}</b> sẽ <b>BỊ TẠM HOÃN</b> để bảo vệ mạng lưới!"
        else
            sched_notice="Hệ thống <b>TỪ CHỐI DEPLOY</b> bản code này để bảo vệ mạng lưới!"
        fi
        
        send_telegram_notification "❌ <b>[CẢNH BÁO: Build Check THẤT BẠI]</b>
• <b>Commit:</b> <code>${target_hash:0:8}</code>
• <b>Thời gian kiểm tra:</b> ${build_dur}s
• <b>Chi tiết lỗi:</b>
<pre>
${err_snippet}
</pre>
⚠️ <b>Cảnh báo:</b> ${sched_notice}"
        return 1
    fi
}

TARGET_DEPLOY_EPOCH=""
if [ -n "$SCHEDULE_AT" ]; then
    TARGET_DEPLOY_EPOCH=$(TZ="Asia/Ho_Chi_Minh" date -d "$SCHEDULE_AT" +%s 2>/dev/null || echo "")
    if [ -n "$TARGET_DEPLOY_EPOCH" ]; then
        NOW_EPOCH=$(date +%s)
        if [ "$TARGET_DEPLOY_EPOCH" -le "$NOW_EPOCH" ]; then
            TARGET_DEPLOY_EPOCH=$(TZ="Asia/Ho_Chi_Minh" date -d "tomorrow $SCHEDULE_AT" +%s 2>/dev/null || echo "")
        fi
        TARGET_HUMAN=$(TZ="Asia/Ho_Chi_Minh" date -d "@$TARGET_DEPLOY_EPOCH" '+%Y-%m-%d %H:%M:%S %Z')
        WAIT_SECONDS=$((TARGET_DEPLOY_EPOCH - NOW_EPOCH))
        HOURS=$((WAIT_SECONDS / 3600))
        MINUTES=$(((WAIT_SECONDS % 3600) / 60))
        echo "⏰ Đã lên lịch hẹn: Sẽ deploy vào lúc $TARGET_HUMAN (còn khoảng ${HOURS}h ${MINUTES}m)"
        echo "💡 Watcher sẽ kiểm tra remote liên tục mỗi ${CHECK_INTERVAL}s. Khi có commit mới sẽ tự động kéo về và chạy Build Check kiểm tra trước."
    else
        echo "⚠️ Định dạng thời gian --at không hợp lệ: $SCHEDULE_AT (Ví dụ: --at 21:00)"
    fi
else
    echo "⚡ Chế độ: KÍCH HOẠT DEPLOY NGAY LẬP TỨC (--immediate)"
    echo "💡 Watcher sẽ kiểm tra remote liên tục mỗi ${CHECK_INTERVAL}s. Khi có commit mới -> Kéo về -> Build Check -> Deploy & restart chain ngay nếu pass."
fi

if [ "$FORCE_INITIAL_DEPLOY" = true ] && [ -z "$SCHEDULE_AT" ]; then
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
    if [ ! -f "$LAST_DEPLOYED_FILE" ]; then
        echo "$LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
    fi
    LAST_DEP_INIT=$(cat "$LAST_DEPLOYED_FILE" 2>/dev/null | xargs || echo "")
    echo "📌 Đã ghi nhận commit đã deploy gần nhất: ${LAST_DEP_INIT:0:8}"
    
    # Nếu local đang có commit mới hơn commit đã deploy gần nhất mà chưa verify
    if [ "$LOCAL_HASH" != "$LAST_DEP_INIT" ] && [ "$BUILD_VERIFIED" = false ]; then
        echo "🔍 Phát hiện local có commit mới (${LOCAL_HASH:0:8}) chưa deploy. Chạy Build Check kiểm tra trước..."
        verify_local_commit "$LOCAL_HASH"
    fi
fi

cd "$PROJECT_ROOT"
echo "👀 Entering Watcher mode. Polling every ${CHECK_INTERVAL}s..."

while true; do
    NOW_EPOCH=$(date +%s)
    
    # ─── 1. KIỂM TRA ĐẾN GIỜ HẸN DEPLOY ────────────────────────────
    if [ -n "$TARGET_DEPLOY_EPOCH" ] && [ "$NOW_EPOCH" -ge "$TARGET_DEPLOY_EPOCH" ]; then
        CURRENT_LOCAL=$(git rev-parse HEAD 2>/dev/null || echo "")
        LAST_DEP=""
        if [ -f "$LAST_DEPLOYED_FILE" ]; then
            LAST_DEP=$(cat "$LAST_DEPLOYED_FILE" 2>/dev/null | xargs || echo "")
        fi
        
        echo -e "\n🔔 [$(TZ='Asia/Ho_Chi_Minh' date '+%Y-%m-%d %H:%M:%S %Z')] ĐÃ ĐẾN GIỜ HẸN DEPLOY (${SCHEDULE_AT})!"
        
        if [ "$CURRENT_LOCAL" != "$LAST_DEP" ]; then
            if [ "$BUILD_VERIFIED" = true ] && [ "$LAST_VERIFIED_HASH" = "$CURRENT_LOCAL" ]; then
                COMMIT_MSG=$(git log -1 --pretty=%B | head -n 1)
                COMMIT_AUTHOR=$(git log -1 --pretty=%an)
                export DEPLOY_SOURCE="Auto-Deploy (Scheduled ${SCHEDULE_AT}, Branch: ${BRANCH}, Git Commit ${CURRENT_LOCAL:0:8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"
                
                send_telegram_notification "🚀 <b>[Đến Giờ Hẹn Deploy ${SCHEDULE_AT}]</b>
Đã đến lịch hẹn! Tiến hành triển khai commit <code>${CURRENT_LOCAL:0:8}</code> (đã vượt qua Build Check) lên toàn bộ cụm node...
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>"
                
                echo "🚀 Kích hoạt build & deploy hệ thống cho $DEPLOY_SOURCE..."
                cd "$ANSIBLE_DIR"
                ./ansible_deploy.sh --start --fast ${args[@]+"${args[@]}"}
                echo "$CURRENT_LOCAL" > "$LAST_DEPLOYED_FILE"
                cd "$PROJECT_ROOT"
                echo "✅ Hoàn tất deploy theo lịch hẹn ${SCHEDULE_AT}!"
            else
                echo "🛑 Đã đến giờ hẹn nhưng commit ${CURRENT_LOCAL:0:8} chưa vượt qua bài kiểm tra Build Check! Hủy đợt deploy này."
                send_telegram_notification "🛑 <b>[HỦY DEPLOY ${SCHEDULE_AT}]</b>
Hệ thống <b>KHÔNG</b> khởi động lại các node vì commit <code>${CURRENT_LOCAL:0:8}</code> chưa vượt qua kiểm tra biên dịch (Build Check).
Các node tiếp tục chạy phiên bản ổn định trước đó."
            fi
        else
            echo "ℹ️ Đến giờ hẹn nhưng không có commit mới nào cần deploy (HEAD vẫn là ${CURRENT_LOCAL:0:8})."
        fi
        
        # Đặt lịch hẹn tiếp theo sang ngày mai
        TARGET_DEPLOY_EPOCH=$(TZ="Asia/Ho_Chi_Minh" date -d "tomorrow $SCHEDULE_AT" +%s 2>/dev/null || echo "")
        TARGET_HUMAN=$(TZ="Asia/Ho_Chi_Minh" date -d "@$TARGET_DEPLOY_EPOCH" '+%Y-%m-%d %H:%M:%S %Z')
        echo "⏰ Lịch hẹn tiếp theo đã được đặt cho: $TARGET_HUMAN"
    fi

    # ─── 2. KIỂM TRA COMMIT MỚI TRÊN REMOTE ─────────────────────────
    if git fetch "$REMOTE" "$BRANCH" >/dev/null 2>&1; then
        REMOTE_HASH=$(git rev-parse "${REMOTE}/${BRANCH}" 2>/dev/null || echo "")
        
        # Chỉ kích hoạt khi REMOTE_HASH là commit mới mà local HEAD CHƯA CÓ
        if [ -n "$REMOTE_HASH" ] && ! git merge-base --is-ancestor "$REMOTE_HASH" HEAD 2>/dev/null; then
            COMMIT_MSG=$(git log -1 --pretty=%B "${REMOTE}/${BRANCH}" 2>/dev/null | head -n 1)
            COMMIT_AUTHOR=$(git log -1 --pretty=%an "${REMOTE}/${BRANCH}" 2>/dev/null || echo "Unknown")
            
            echo -e "\n🔔 [$(TZ='Asia/Ho_Chi_Minh' date '+%Y-%m-%d %H:%M:%S %Z')] Phát hiện commit mới trên remote (${REMOTE}/${BRANCH})!"
            echo "   Commit mới : ${REMOTE_HASH:0:8} by $COMMIT_AUTHOR: $COMMIT_MSG"
            
            # GỬI TELEGRAM BÁO PHÁT HIỆN VÀ ĐANG PULL
            sched_txt="ngay sau khi build thành công"
            if [ -n "$SCHEDULE_AT" ]; then
                sched_txt="lúc <b>${SCHEDULE_AT} (Asia/Ho_Chi_Minh)</b>"
            fi
            send_telegram_notification "🔔 <b>[Phát hiện Commit mới trên Remote]</b>
Hệ thống phát hiện commit mới trên nhánh <code>${BRANCH}</code>:
• <b>Commit:</b> <code>${REMOTE_HASH:0:8}</code>
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>

🔄 <b>Hành động:</b> Đang tự động kéo mã nguồn về và chạy <b>Build Check</b>...
⏰ <i>Lưu ý: Hệ thống sẽ tự động deploy vào ${sched_txt}.</i>"
            
            # Tự động stash nếu working tree có file unstaged để tránh xung đột khi rebase
            HAS_UNSTAGED=false
            if ! git diff-index --quiet HEAD -- 2>/dev/null; then
                HAS_UNSTAGED=true
                echo "📦 Tạm lưu các file chưa commit ở local vào git stash..."
                git stash push -u -m "auto-deploy-stash-$(date +%s)" >/dev/null 2>&1 || true
            fi

            echo "🔄 Đang kéo mã nguồn mới từ ${REMOTE}/${BRANCH}..."
            if git pull --rebase "$REMOTE" "$BRANCH"; then
                if [ "$HAS_UNSTAGED" = true ]; then
                    echo "📦 Phục hồi lại các thay đổi local từ stash..."
                    git stash pop >/dev/null 2>&1 || true
                fi
                NEW_LOCAL_HASH=$(git rev-parse HEAD)
                echo "✅ Đã kéo mã nguồn về thành công (HEAD: ${NEW_LOCAL_HASH:0:8})."
                
                # Chạy Build Check
                verify_local_commit "$NEW_LOCAL_HASH"
                
                # Nếu không đặt lịch hẹn (--at rỗng) và build pass -> Deploy ngay
                if [ -z "$SCHEDULE_AT" ] && [ "$BUILD_VERIFIED" = true ]; then
                    export DEPLOY_SOURCE="Auto-Deploy Immediate (Branch: ${BRANCH}, Git Commit ${NEW_LOCAL_HASH:0:8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"
                    echo "🚀 Kích hoạt build & deploy hệ thống ngay lập tức..."
                    send_telegram_notification "🚀 <b>[Kích Hoạt Deploy Ngay Lập Tức]</b>
Commit <code>${NEW_LOCAL_HASH:0:8}</code> đã vượt qua Build Check!
Đang tiến hành biên dịch và restart toàn bộ cụm node...
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>"
                    cd "$ANSIBLE_DIR"
                    ./ansible_deploy.sh --start --fast ${args[@]+"${args[@]}"}
                    echo "$NEW_LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
                    cd "$PROJECT_ROOT"
                    send_telegram_notification "✅ <b>[Deploy Hoàn Tất]</b>
Cụm node đã được cập nhật thành công lên commit <code>${NEW_LOCAL_HASH:0:8}</code>!"
                fi
            else
                if [ "$HAS_UNSTAGED" = true ]; then
                    git stash pop >/dev/null 2>&1 || true
                fi
                echo "❌ Lỗi kéo mã nguồn (git pull --rebase) từ ${REMOTE}/${BRANCH}!"
                send_telegram_notification "❌ <b>[LỖI GIT PULL]</b> Không thể kéo commit <code>${REMOTE_HASH:0:8}</code> về local do conflict hoặc lỗi git!"
            fi
        fi
    else
        echo "⚠️ [$(date '+%Y-%m-%d %H:%M:%S')] Không thể kết nối fetch từ git remote (${REMOTE}/${BRANCH}). Sẽ thử lại sau..."
    fi
    
    sleep $CHECK_INTERVAL
done
