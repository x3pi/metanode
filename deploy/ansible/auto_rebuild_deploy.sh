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
OP_LOCK_FILE="${ANSIBLE_DIR}/.deploy_operation.lock"
LOG_FILE="${ANSIBLE_DIR}/auto_deploy.log"
LAST_DEPLOYED_FILE="${ANSIBLE_DIR}/.last_deployed_commit"
CHECK_INTERVAL=5
REMOTE="origin"

TELEGRAM_BOT_TOKEN=""
TELEGRAM_CHAT_ID=""
LAST_NOTIFIED_REMOTE_HASH=""
SCHEDULE_AT=""

load_telegram_config() {
    local env_file="${ANSIBLE_DIR}/.env"
    local token=""
    local chat_id=""
    if [ -f "$env_file" ]; then
        token=$(grep -E '^\s*TELEGRAM_BOT_TOKEN=' "$env_file" 2>/dev/null | head -n 1 | cut -d'=' -f2- | tr -d '"\r' || true)
        chat_id=$(grep -E '^\s*TELEGRAM_CHAT_ID=' "$env_file" 2>/dev/null | head -n 1 | cut -d'=' -f2- | tr -d '"\r' || true)
    fi
    if [ -z "$token" ] && [ -f "${ANSIBLE_DIR}/inventory.yml" ]; then
        token=$(grep -E '^\s*(telegram_bot_token|bot_token):' "${ANSIBLE_DIR}/inventory.yml" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
        chat_id=$(grep -E '^\s*(telegram_chat_id|chat_id):' "${ANSIBLE_DIR}/inventory.yml" 2>/dev/null | head -n 1 | awk '{print $2}' | sed 's/["\x27]//g' || true)
    fi
    [ -n "$token" ] && TELEGRAM_BOT_TOKEN="$token"
    [ -n "$chat_id" ] && TELEGRAM_CHAT_ID="$chat_id"
    export TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-""}"
    export TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-""}"
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

# A deploy must only build from the exact Git HEAD being recorded.  Include
# untracked files here because `git diff-index` reports only tracked changes.
has_worktree_changes() {
    [ -n "$(git status --porcelain --untracked-files=all 2>/dev/null)" ]
}

run_post_deploy_tests() {
    local target_commit="${1:-$(git rev-parse --short HEAD 2>/dev/null || echo "Unknown")}"
    local suite_script="${PROJECT_ROOT}/../metanode-suite/scripts/rpc-tcp-simple.sh"

    if [ ! -f "$suite_script" ]; then
        echo "⚠️ Không tìm thấy script test giao dịch: $suite_script"
        return 0
    fi

    echo ""
    echo "=========================================================="
    echo "🧪 KIỂM THỬ GIAO DỊCH SAU KHI DEPLOY (POST-DEPLOY TESTS)"
    echo "=========================================================="
    echo "⏳ Đang chờ 10s để cụm node ổn định mạng lưới và mở cổng kết nối..."
    sleep 10

    local test_dir
    test_dir=$(dirname "$suite_script")
    local test_log="${ANSIBLE_DIR}/post_deploy_test.log"
    : > "$test_log"

    local node1_ok=false
    local node0_ok=false
    local failed_summary=""

    echo "🚀 [1/2] Đang chạy kiểm thử giao dịch trên Node 1 (./rpc-tcp-simple.sh --node 1)..."
    echo "=== BẮT ĐẦU TEST NODE 1 ===" >> "$test_log"
    if (cd "$test_dir" && ./rpc-tcp-simple.sh --node 1) >> "$test_log" 2>&1; then
        echo "✅ Node 1: Kiểm thử giao dịch THÀNH CÔNG!"
        node1_ok=true
    else
        echo "❌ Node 1: Kiểm thử giao dịch THẤT BẠI!"
        failed_summary="Node 1"
    fi

    echo "🚀 [2/2] Đang chạy kiểm thử giao dịch trên Node 0 (./rpc-tcp-simple.sh --node 0)..."
    echo "=== BẮT ĐẦU TEST NODE 0 ===" >> "$test_log"
    if (cd "$test_dir" && ./rpc-tcp-simple.sh --node 0) >> "$test_log" 2>&1; then
        echo "✅ Node 0: Kiểm thử giao dịch THÀNH CÔNG!"
        node0_ok=true
    else
        echo "❌ Node 0: Kiểm thử giao dịch THẤT BẠI!"
        if [ -n "$failed_summary" ]; then
            failed_summary="$failed_summary & Node 0"
        else
            failed_summary="Node 0"
        fi
    fi

    if [ "$node1_ok" = true ] && [ "$node0_ok" = true ]; then
        echo "🎉 Cả 2 node (Node 1 & Node 0) đã kiểm thử giao dịch THÀNH CÔNG!"
        send_telegram_notification "✅ <b>[Test Giao Dịch Sau Deploy Thành Công]</b>
Cụm node đã hoàn tất deploy và kiểm thử giao dịch thành công trên commit <code>${target_commit:0:8}</code>:
• <b>Node 1:</b> ✅ PASS (RPC & TCP)
• <b>Node 0:</b> ✅ PASS (RPC & TCP)
✨ Toàn bộ cụm node đang hoạt động ổn định và xử lý giao dịch bình thường!"
    else
        echo "❌ Phát hiện lỗi trong quá trình kiểm thử giao dịch tại: $failed_summary!"
        local last_20_logs
        last_20_logs=$(tail -n 20 "$test_log" | sed -E 's/\x1b\[[0-9;]*m//g' | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g')
        last_20_logs="${last_20_logs:-"(Không có log chi tiết)"}"

        local status_node1="✅ PASS"
        [ "$node1_ok" = false ] && status_node1="❌ FAIL"
        local status_node0="✅ PASS"
        [ "$node0_ok" = false ] && status_node0="❌ FAIL"

        send_telegram_notification "❌ <b>[LỖI TEST GIAO DỊCH SAU DEPLOY]</b>
Đã deploy commit <code>${target_commit:0:8}</code> nhưng kiểm thử giao dịch <b>THẤT BẠI</b> tại <b>${failed_summary}</b>!
• <b>Node 1:</b> ${status_node1}
• <b>Node 0:</b> ${status_node0}

📋 <b>20 dòng log cuối cùng:</b>
<pre>${last_20_logs}</pre>
⚠️ Vui lòng kiểm tra lại dịch vụ của node!"
    fi
    echo "=========================================================="
}

is_running() {
    # 1. Kiểm tra qua PID file nếu tồn tại
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(xargs < "$PID_FILE" 2>/dev/null || echo "")
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
        pid=$(xargs < "$PID_FILE" 2>/dev/null || echo "")
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
    rm -f "$PID_FILE" "$LOCK_FILE" "$OP_LOCK_FILE"
    if [ "$stopped" = true ]; then
        echo "✅ Watcher đã được dừng thành công."
    else
        echo "ℹ️ Auto-Deploy Watcher hiện không chạy."
    fi
}

cmd_run_now() {
    echo "=========================================================="
    echo "🚀 KÍCH HOẠT DEPLOY NGAY LẬP TỨC (RUN-NOW)"
    echo "=========================================================="
    cd "$PROJECT_ROOT" || exit 1
    load_telegram_config

    # Kiểm tra Mutex Lock: Ngăn chặn chạy đồng thời với Watcher hoặc một lệnh deploy khác
    exec 201>"$OP_LOCK_FILE"
    if ! flock -n 201; then
        echo "❌ [TỪ CHỐI] Hiện đang có một tiến trình deploy khác (Watcher hoặc lệnh deploy khác) đang thực thi!"
        echo "💡 Hệ thống khóa Mutex ($OP_LOCK_FILE) đang hoạt động để tránh xung đột build/deploy đồng thời."
        echo "   Vui lòng đợi tiến trình hiện tại hoàn tất hoặc theo dõi: $0 logs"
        exit 1
    fi
    
    local target_branch
    target_branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "main")
    if [ "$target_branch" = "HEAD" ] || [ -z "$target_branch" ]; then
        target_branch="main"
    fi
    
    local force_flag=false
    local pass_args=()
    for arg in "$@"; do
        if [ "$arg" = "--force" ]; then
            force_flag=true
        else
            pass_args+=("$arg")
        fi
    done

    # Kiểm tra và tạm lưu thay đổi chưa commit vào stash để đảm bảo working tree sạch
    local has_unstaged=false
    if has_worktree_changes; then
        echo "📦 Tạm lưu các file chưa commit ở local vào git stash..."
        if ! git stash push -u -m "auto-deploy-run-now-stash-$(date +%s)" >/dev/null 2>&1; then
            echo "❌ [LỖI] Không thể lưu git stash cho các thay đổi local! HỦY BỎ DEPLOY để bảo vệ mã nguồn."
            flock -u 201 2>/dev/null || true
            exit 1
        fi
        if has_worktree_changes; then
            echo "❌ [LỖI] Working tree vẫn còn thay đổi sau git stash! HỦY BỎ DEPLOY để bảo đảm build đúng Git HEAD."
            flock -u 201 2>/dev/null || true
            exit 1
        fi
        has_unstaged=true
    fi

    # 1. Kéo code mới nhất từ remote về nếu có (kiểm tra lỗi fetch nghiêm ngặt)
    echo "🔄 Đang kiểm tra remote (${REMOTE}/${target_branch})..."
    local pull_ok=true
    if ! git fetch "$REMOTE" "$target_branch"; then
        echo "❌ [LỖI] Không thể fetch mã nguồn từ ${REMOTE}/${target_branch}!"
        if [ "$force_flag" = false ]; then
            echo "💡 Đã hủy deploy để tránh triển khai nhầm commit cũ do lỗi mạng. Dùng '--force' nếu muốn ép buộc deploy HEAD local."
            if [ "$has_unstaged" = true ]; then
                git stash pop >/dev/null 2>&1 || true
            fi
            flock -u 201 2>/dev/null || true
            exit 1
        else
            echo "⚠️ Cảnh báo: Fetch thất bại nhưng tiếp tục do có cờ --force..."
        fi
    fi

    if ! git merge-base --is-ancestor "${REMOTE}/${target_branch}" HEAD 2>/dev/null; then
        echo "📦 Phát hiện commit mới trên remote. Đang kéo về (git pull --rebase)..."
        if ! git pull --rebase "$REMOTE" "$target_branch"; then
            echo "❌ [LỖI] Xung đột git khi pull rebase! Đang abort..."
            git rebase --abort >/dev/null 2>&1 || true
            pull_ok=false
        fi
    fi

    if [ "$pull_ok" = false ]; then
        echo "⛔ Hủy bỏ deploy do xung đột git khi pull rebase!"
        if [ "$has_unstaged" = true ]; then
            git stash pop >/dev/null 2>&1 || true
        fi
        flock -u 201 2>/dev/null || true
        exit 1
    fi

    local current_hash
    current_hash=$(git rev-parse HEAD 2>/dev/null || echo "")
    local last_dep=""
    if [ -f "$LAST_DEPLOYED_FILE" ]; then
        last_dep=$(xargs < "$LAST_DEPLOYED_FILE" 2>/dev/null || echo "")
    fi

    if [ "$current_hash" = "$last_dep" ] && [ "$force_flag" = false ]; then
        echo "ℹ️ Commit hiện tại (${current_hash:0:8}) đã được deploy lên cluster trước đó."
        echo "💡 Gợi ý: Dùng '$0 run-now --force' nếu bạn muốn ép buộc deploy lại."
        if [ "$has_unstaged" = true ]; then
            echo "📦 Phục hồi lại các thay đổi local từ stash..."
            git stash pop >/dev/null 2>&1 || true
        fi
        flock -u 201 2>/dev/null || true
        exit 0
    fi

    local commit_msg
    commit_msg=$(git log -1 --pretty=%B | head -n 1)
    local commit_author
    commit_author=$(git log -1 --pretty=%an)
    export DEPLOY_SOURCE="Auto-Deploy Manual Run-Now (Branch: ${target_branch}, Git Commit ${current_hash:0:8} by ${commit_author}: \"${commit_msg}\")"

    echo "🚀 Kích hoạt build & deploy hệ thống ngay lập tức (từ clean HEAD: ${current_hash:0:8})..."
    send_telegram_notification "🚀 <b>[Kích Hoạt Deploy Thủ Công (Run-Now)]</b>
Đang tiến hành biên dịch và restart toàn bộ cụm node lên commit <code>${current_hash:0:8}</code>...
• <b>Tác giả:</b> ${commit_author}
• <b>Nội dung:</b> <i>${commit_msg}</i>"

    cd "$ANSIBLE_DIR" || exit 1
    local deploy_status=0
    if ./ansible_deploy.sh deploy --all --fast ${pass_args[@]+"${pass_args[@]}"}; then
        echo "$current_hash" > "$LAST_DEPLOYED_FILE"
        cd "$PROJECT_ROOT" || exit 1
        echo "✅ Deploy hoàn tất thành công!"
        send_telegram_notification "✅ <b>[Deploy Thủ Công Hoàn Tất]</b>
Cụm node đã được cập nhật thành công lên commit <code>${current_hash:0:8}</code>!"
        run_post_deploy_tests "$current_hash"
    else
        cd "$PROJECT_ROOT" || exit 1
        echo "❌ Lỗi xảy ra trong quá trình deploy!"
        send_telegram_notification "❌ <b>[LỖI DEPLOY THỰC TẾ]</b>
Tiến trình cập nhật lên commit <code>${current_hash:0:8}</code> ĐÃ THẤT BẠI ở bước chạy ansible_deploy (biên dịch hoặc triển khai lỗi)!"
        deploy_status=1
    fi

    # Phục hồi stash sau khi hoàn tất toàn bộ quá trình build & deploy
    if [ "$has_unstaged" = true ]; then
        echo "📦 Phục hồi lại các thay đổi local từ stash..."
        if ! git stash pop >/dev/null 2>&1; then
            echo "⚠️ [CẢNH BÁO] Deploy đã hoàn tất từ clean HEAD, nhưng phục hồi stash local bị xung đột (conflict markers)!"
            send_telegram_notification "⚠️ <b>[CẢNH BÁO STASH POP]</b> Deploy thành công commit <code>${current_hash:0:8}</code>, nhưng phục hồi thay đổi local bị xung đột. Cần kiểm tra file conflict thủ công!"
        fi
    fi

    flock -u 201 2>/dev/null || true
    if [ "$deploy_status" -ne 0 ]; then
        exit 1
    fi
}

cmd_help() {
    cat << 'EOF'
🚀 Git Auto-Rebuild & Deploy Daemon for Metanode

CÚ PHÁP:
    ./auto_rebuild_deploy.sh [LỆNH | TÙY CHỌN]

LỆNH ĐIỀU KHIỂN:
    start                  Khởi động watcher chạy ngầm (daemon)
    run-now, deploy-now    Kích hoạt deploy ngay lập tức (không cần đợi commit mới hay giờ hẹn)
    stop                   Dừng watcher và dọn dẹp sạch sẽ
    status                 Xem trạng thái hoạt động của watcher
    logs                   Xem log thời gian thực (tail -f)
    help, -h, --help       Hiển thị hướng dẫn này

TÙY CHỌN KHỞI ĐỘNG:
    --at <HH:MM>           🕒 Hẹn giờ deploy (ví dụ: --at 21:00 giờ Việt Nam Asia/Ho_Chi_Minh)
                           (Khi hẹn giờ: KHÔNG kéo code về trước, giữ nguyên local repo,
                            đúng 21:00 mới tự động kéo về, build check và deploy)
                           (Mặc định nếu không có --at: Tự động kéo về và deploy ngay khi có commit mới)
    --branch <tên_nhánh>   Chỉ định nhánh git cần theo dõi (mặc định: main)
    --initial-deploy       Kích hoạt deploy ngay 1 lần lúc vừa bật watcher
    -d, --daemon           Chạy dưới dạng tiến trình ngầm (tương đương lệnh 'start')

VÍ DỤ SỬ DỤNG:
    # 1. Chạy mặc định (kéo commit về -> build check pass -> deploy & restart chain ngay):
    ./auto_rebuild_deploy.sh start

    # 2. Chạy có hẹn giờ deploy (ví dụ: 21:00 tối, không đụng working tree ban ngày):
    ./auto_rebuild_deploy.sh start --at 21:00

    # 3. Kích hoạt deploy ngay lập tức (bất kể đang hẹn giờ hay vừa dừng watcher):
    ./auto_rebuild_deploy.sh run-now

    # 4. Ép buộc deploy lại commit hiện tại:
    ./auto_rebuild_deploy.sh run-now --force

    # 5. Kiểm tra trạng thái:
    ./auto_rebuild_deploy.sh status
EOF
}

cmd_status() {
    echo "=========================================================="
    echo "📊 TRẠNG THÁI GIT AUTO-DEPLOY WATCHER"
    echo "=========================================================="
    if is_running; then
        local pid
        pid=$(xargs < "$PID_FILE" 2>/dev/null || echo "")
        echo "🟢 Trạng thái       : ĐANG CHẠY (PID: $pid)"
        local cmdline
        cmdline=$(ps -p "$pid" -o args= 2>/dev/null || echo "")
        if echo "$cmdline" | grep -oE -- "--at [0-9:]+" >/dev/null 2>&1; then
            local at_val
            at_val=$(echo "$cmdline" | grep -oE -- "--at [0-9:]+" | awk '{print $2}')
            echo "⏰ Lịch hẹn deploy  : 🕒 ${at_val} (Asia/Ho_Chi_Minh)"
        else
            echo "⚡ Chế độ deploy    : NGAY LẬP TỨC (Không hẹn giờ)"
        fi
    else
        echo "🔴 Trạng thái       : ĐÃ DỪNG"
    fi
    local last_commit="(Chưa có)"
    if [ -f "$LAST_DEPLOYED_FILE" ]; then
        last_commit=$(xargs < "$LAST_DEPLOYED_FILE" 2>/dev/null || echo "")
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
    run-now|deploy-now)
        shift
        cmd_run_now "$@"
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
SCHEDULE_AT=""
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
        --at|--schedule)
            if [ $# -lt 2 ] || [[ "$2" =~ ^-- ]]; then
                echo "❌ [LỖI] Cờ $1 yêu cầu giá trị thời gian (ví dụ: --at 21:00)" >&2
                exit 1
            fi
            SCHEDULE_AT="$2"
            shift 2
            ;;
        --branch)
            if [ $# -lt 2 ] || [[ "$2" =~ ^-- ]]; then
                echo "❌ [LỖI] Cờ $1 yêu cầu tên nhánh git (ví dụ: --branch dev)" >&2
                exit 1
            fi
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
    running_pid=$(xargs < "$PID_FILE" 2>/dev/null || true)
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
    if [ -n "$SCHEDULE_AT" ]; then
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
trap 'rm -f "$PID_FILE" "$LOCK_FILE" "$OP_LOCK_FILE"' EXIT INT TERM

cd "$PROJECT_ROOT" || exit 1

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
        echo "💡 Chế độ hẹn giờ: Mã nguồn local được giữ nguyên vẹn. Đến đúng giờ hẹn sẽ tự động kéo về, biên dịch và deploy."
    else
        echo "⚠️ Định dạng thời gian --at không hợp lệ: $SCHEDULE_AT (Ví dụ: --at 21:00)"
    fi
else
    echo "⚡ Chế độ: KÍCH HOẠT DEPLOY NGAY LẬP TỨC (Mặc định không hẹn giờ)"
    echo "💡 Watcher sẽ kiểm tra remote liên tục mỗi ${CHECK_INTERVAL}s. Khi có commit mới -> Kéo về -> Build Check -> Deploy & restart chain ngay nếu pass."
fi

if [ "$FORCE_INITIAL_DEPLOY" = true ] && [ -z "$SCHEDULE_AT" ]; then
    echo "🚀 Performing initial deployment as requested via --initial-deploy..."
    exec 201>"$OP_LOCK_FILE"
    if ! flock -n 201; then
        echo "❌ [LỖI] Đang có tiến trình deploy khác nắm giữ lock!"
        exit 1
    fi
    cd "$ANSIBLE_DIR" || exit 1
    export DEPLOY_SOURCE="Auto-Deploy (Initial Run)"
    if ! ./ansible_deploy.sh deploy --all ${args[@]+"${args[@]}"}; then
        echo "❌ Initial deploy failed! Exiting auto-deploy watcher."
        flock -u 201 2>/dev/null || true
        exit 1
    fi
    echo "✅ Initial deploy successful."
    echo "$LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
    run_post_deploy_tests "$LOCAL_HASH"
    flock -u 201 2>/dev/null || true
else
    if [ ! -f "$LAST_DEPLOYED_FILE" ]; then
        echo "$LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
    fi
    LAST_DEP_INIT=$(xargs < "$LAST_DEPLOYED_FILE" 2>/dev/null || echo "")
    echo "📌 Đã ghi nhận commit đã deploy gần nhất: ${LAST_DEP_INIT:0:8}"
fi

cd "$PROJECT_ROOT" || exit 1
echo "👀 Entering Watcher mode. Polling every ${CHECK_INTERVAL}s..."

while true; do
    NOW_EPOCH=$(date +%s)
    
    # ─── 1. KIỂM TRA ĐẾN GIỜ HẸN DEPLOY ────────────────────────────
    if [ -n "$TARGET_DEPLOY_EPOCH" ] && [ "$NOW_EPOCH" -ge "$TARGET_DEPLOY_EPOCH" ]; then
        echo -e "\n🔔 [$(TZ='Asia/Ho_Chi_Minh' date '+%Y-%m-%d %H:%M:%S %Z')] ĐÃ ĐẾN GIỜ HẸN DEPLOY (${SCHEDULE_AT})!"
        
        # Kiểm tra Mutex Lock: Ngăn ngừa đụng độ với run-now hoặc lệnh deploy khác
        exec 201>"$OP_LOCK_FILE"
        if ! flock -n 201; then
            echo "⚠️ [TRÌ HOÃN] Có tiến trình deploy khác đang nắm giữ lock. Sẽ thử lại ở vòng lặp kế tiếp..."
            sleep 5
            continue
        fi

        LAST_DEP=""
        if [ -f "$LAST_DEPLOYED_FILE" ]; then
            LAST_DEP=$(xargs < "$LAST_DEPLOYED_FILE" 2>/dev/null || echo "")
        fi
        
        # Đến giờ hẹn mới thực hiện kéo code mới nhất từ remote về working tree
        echo "🔄 Đang kiểm tra và kéo mã nguồn mới nhất từ ${REMOTE}/${BRANCH}..."
        HAS_UNSTAGED=false
        if has_worktree_changes; then
            echo "📦 Tạm lưu các file chưa commit ở local vào git stash..."
            if ! git stash push -u -m "auto-deploy-stash-$(date +%s)" >/dev/null 2>&1; then
                echo "❌ [LỖI] Không thể lưu git stash cho các thay đổi local! HỦY BỎ DEPLOY lịch hẹn để bảo vệ mã nguồn."
                send_telegram_notification "❌ <b>[HỦY DEPLOY LỊCH HẸN]</b> Không thể lưu git stash thay đổi local! Đã hủy đợt deploy này."
                flock -u 201 2>/dev/null || true
                TARGET_DEPLOY_EPOCH=$(TZ="Asia/Ho_Chi_Minh" date -d "tomorrow $SCHEDULE_AT" +%s 2>/dev/null || echo "")
                TARGET_HUMAN=$(TZ="Asia/Ho_Chi_Minh" date -d "@$TARGET_DEPLOY_EPOCH" '+%Y-%m-%d %H:%M:%S %Z')
                echo "⏰ Lịch hẹn tiếp theo đã được đặt cho: $TARGET_HUMAN"
                continue
            fi
            if has_worktree_changes; then
                echo "❌ [LỖI] Working tree vẫn còn thay đổi sau git stash! HỦY BỎ DEPLOY lịch hẹn để bảo đảm build đúng Git HEAD."
                send_telegram_notification "❌ <b>[HỦY DEPLOY LỊCH HẸN]</b> Working tree vẫn còn thay đổi sau git stash. Đã hủy đợt deploy để bảo đảm binary đúng commit SHA."
                flock -u 201 2>/dev/null || true
                TARGET_DEPLOY_EPOCH=$(TZ="Asia/Ho_Chi_Minh" date -d "tomorrow $SCHEDULE_AT" +%s 2>/dev/null || echo "")
                TARGET_HUMAN=$(TZ="Asia/Ho_Chi_Minh" date -d "@$TARGET_DEPLOY_EPOCH" '+%Y-%m-%d %H:%M:%S %Z')
                echo "⏰ Lịch hẹn tiếp theo đã được đặt cho: $TARGET_HUMAN"
                continue
            fi
            HAS_UNSTAGED=true
        fi

        PULL_OK=true
        if ! git pull --rebase "$REMOTE" "$BRANCH"; then
            PULL_OK=false
            git rebase --abort >/dev/null 2>&1 || true
            echo "❌ Lỗi kéo mã nguồn (git pull --rebase) từ ${REMOTE}/${BRANCH}!"
            send_telegram_notification "❌ <b>[LỖI GIT PULL ĐẾN GIỜ HẸN]</b>
Đã đến giờ hẹn (${SCHEDULE_AT}) nhưng không thể kéo mã nguồn mới từ remote do xung đột git! Đã tạm hoãn đợt deploy này."
            if [ "$HAS_UNSTAGED" = true ]; then
                git stash pop >/dev/null 2>&1 || true
            fi
        fi

        CURRENT_LOCAL=$(git rev-parse HEAD 2>/dev/null || echo "")

        if [ "$PULL_OK" = true ] && [ "$CURRENT_LOCAL" != "$LAST_DEP" ]; then
            COMMIT_MSG=$(git log -1 --pretty=%B | head -n 1)
            COMMIT_AUTHOR=$(git log -1 --pretty=%an)
            export DEPLOY_SOURCE="Auto-Deploy (Scheduled ${SCHEDULE_AT}, Branch: ${BRANCH}, Git Commit ${CURRENT_LOCAL:0:8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"

            send_telegram_notification "🚀 <b>[Đến Giờ Hẹn Deploy ${SCHEDULE_AT}]</b>
Đã đến lịch hẹn! Tiến hành triển khai commit <code>${CURRENT_LOCAL:0:8}</code> lên toàn bộ cụm node...
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>"

            echo "🚀 Kích hoạt build & deploy hệ thống cho $DEPLOY_SOURCE (từ clean HEAD: ${CURRENT_LOCAL:0:8})..."
            cd "$ANSIBLE_DIR" || exit 1
            if ./ansible_deploy.sh deploy --all --fast ${args[@]+"${args[@]}"}; then
                echo "$CURRENT_LOCAL" > "$LAST_DEPLOYED_FILE"
                cd "$PROJECT_ROOT" || exit 1
                echo "✅ Hoàn tất deploy theo lịch hẹn ${SCHEDULE_AT}!"
                send_telegram_notification "✅ <b>[Deploy Lịch Hẹn Hoàn Tất]</b>
Cụm node đã được cập nhật thành công lên commit <code>${CURRENT_LOCAL:0:8}</code>!"
                run_post_deploy_tests "$CURRENT_LOCAL"
            else
                cd "$PROJECT_ROOT" || exit 1
                echo "❌ Lỗi xảy ra trong quá trình deploy theo lịch hẹn!"
                send_telegram_notification "❌ <b>[LỖI DEPLOY THỰC TẾ]</b>
Tiến trình cập nhật lên commit <code>${CURRENT_LOCAL:0:8}</code> ĐÃ THẤT BẠI ở bước chạy ansible_deploy (biên dịch hoặc triển khai lỗi)!"
            fi

            # Phục hồi stash sau khi hoàn tất toàn bộ quá trình build & deploy
            if [ "$HAS_UNSTAGED" = true ]; then
                echo "📦 Phục hồi lại các thay đổi local từ stash..."
                if ! git stash pop >/dev/null 2>&1; then
                    echo "⚠️ [CẢNH BÁO] Deploy lịch hẹn thành công từ clean HEAD, nhưng phục hồi stash local bị xung đột (conflict markers)!"
                    send_telegram_notification "⚠️ <b>[CẢNH BÁO STASH POP]</b> Deploy thành công commit <code>${CURRENT_LOCAL:0:8}</code>, nhưng phục hồi thay đổi local bị xung đột. Cần kiểm tra file conflict thủ công!"
                fi
            fi
        elif [ "$PULL_OK" = true ]; then
            echo "ℹ️ Đến giờ hẹn nhưng không có commit mới nào cần deploy (HEAD vẫn là ${CURRENT_LOCAL:0:8}, trùng với commit đã deploy)."
            if [ "$HAS_UNSTAGED" = true ]; then
                git stash pop >/dev/null 2>&1 || true
            fi
        fi

        flock -u 201 2>/dev/null || true

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
            if [ -n "$SCHEDULE_AT" ]; then
                # TRƯỜNG HỢP CÓ HẸN GIỜ: KHÔNG kéo code về trước, giữ nguyên working tree local!
                if [ "$REMOTE_HASH" != "${LAST_NOTIFIED_REMOTE_HASH:-}" ]; then
                    LAST_NOTIFIED_REMOTE_HASH="$REMOTE_HASH"
                    COMMIT_MSG=$(git log -1 --pretty=%B "${REMOTE}/${BRANCH}" 2>/dev/null | head -n 1)
                    COMMIT_AUTHOR=$(git log -1 --pretty=%an "${REMOTE}/${BRANCH}" 2>/dev/null || echo "Unknown")
                    
                    echo -e "\n🔔 [$(TZ='Asia/Ho_Chi_Minh' date '+%Y-%m-%d %H:%M:%S %Z')] Phát hiện commit mới trên remote (${REMOTE}/${BRANCH})!"
                    echo "   Commit mới : ${REMOTE_HASH:0:8} by $COMMIT_AUTHOR: $COMMIT_MSG"
                    echo "   ⏰ Chế độ hẹn giờ (${SCHEDULE_AT}): KHÔNG kéo về trước. Mã nguồn local được giữ nguyên 100%."
                    echo "   💡 Hệ thống sẽ tự động kéo về và biên dịch deploy đúng lúc ${SCHEDULE_AT}."
                    
                    send_telegram_notification "🔔 <b>[Phát hiện Commit mới trên Remote]</b>
Hệ thống phát hiện commit mới trên nhánh <code>${BRANCH}</code>:
• <b>Commit:</b> <code>${REMOTE_HASH:0:8}</code>
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>

⏰ <b>Lịch hẹn:</b> Hệ thống sẽ <b>tự động kéo về, biên dịch & deploy vào lúc ${SCHEDULE_AT} (Asia/Ho_Chi_Minh)</b>.
💡 <i>Mã nguồn trên máy chủ local được giữ nguyên vẹn để không làm gián đoạn công việc của bạn.</i>"
                fi
            else
                # TRƯỜNG HỢP KHÔNG HẸN GIỜ: Deploy ngay lập tức
                exec 201>"$OP_LOCK_FILE"
                if ! flock -n 201; then
                    echo "⚠️ [TRÌ HOÃN] Có tiến trình deploy khác đang nắm giữ lock. Sẽ thử lại ở vòng lặp kế tiếp..."
                    sleep 5
                    continue
                fi

                COMMIT_MSG=$(git log -1 --pretty=%B "${REMOTE}/${BRANCH}" 2>/dev/null | head -n 1)
                COMMIT_AUTHOR=$(git log -1 --pretty=%an "${REMOTE}/${BRANCH}" 2>/dev/null || echo "Unknown")
                
                echo -e "\n🔔 [$(TZ='Asia/Ho_Chi_Minh' date '+%Y-%m-%d %H:%M:%S %Z')] Phát hiện commit mới trên remote (${REMOTE}/${BRANCH})!"
                echo "   Commit mới : ${REMOTE_HASH:0:8} by $COMMIT_AUTHOR: $COMMIT_MSG"
                
                send_telegram_notification "🔔 <b>[Phát hiện Commit mới trên Remote]</b>
Hệ thống phát hiện commit mới trên nhánh <code>${BRANCH}</code>:
• <b>Commit:</b> <code>${REMOTE_HASH:0:8}</code>
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>

🔄 <b>Hành động:</b> Đang tự động kéo mã nguồn về và tiến hành deploy ngay..."
                
                HAS_UNSTAGED=false
                if has_worktree_changes; then
                    echo "📦 Tạm lưu các file chưa commit ở local vào git stash..."
                    if ! git stash push -u -m "auto-deploy-stash-$(date +%s)" >/dev/null 2>&1; then
                        echo "❌ [LỖI] Không thể lưu git stash cho các thay đổi local! HỦY BỎ DEPLOY để bảo vệ mã nguồn."
                        send_telegram_notification "❌ <b>[HỦY DEPLOY NGAY]</b> Không thể lưu git stash thay đổi local! Đã hủy đợt deploy này."
                        flock -u 201 2>/dev/null || true
                        continue
                    fi
                    if has_worktree_changes; then
                        echo "❌ [LỖI] Working tree vẫn còn thay đổi sau git stash! HỦY BỎ DEPLOY để bảo đảm build đúng Git HEAD."
                        send_telegram_notification "❌ <b>[HỦY DEPLOY NGAY]</b> Working tree vẫn còn thay đổi sau git stash. Đã hủy deploy để bảo đảm binary đúng commit SHA."
                        flock -u 201 2>/dev/null || true
                        continue
                    fi
                    HAS_UNSTAGED=true
                fi

                echo "🔄 Đang kéo mã nguồn mới từ ${REMOTE}/${BRANCH}..."
                if git pull --rebase "$REMOTE" "$BRANCH"; then
                    NEW_LOCAL_HASH=$(git rev-parse HEAD)
                    echo "✅ Đã kéo mã nguồn về thành công (HEAD: ${NEW_LOCAL_HASH:0:8})."

                    export DEPLOY_SOURCE="Auto-Deploy Immediate (Branch: ${BRANCH}, Git Commit ${NEW_LOCAL_HASH:0:8} by ${COMMIT_AUTHOR}: \"${COMMIT_MSG}\")"
                    echo "🚀 Kích hoạt build & deploy hệ thống ngay lập tức (từ clean HEAD: ${NEW_LOCAL_HASH:0:8})..."
                    send_telegram_notification "🚀 <b>[Kích Hoạt Deploy Ngay Lập Tức]</b>
Đang tiến hành biên dịch và restart toàn bộ cụm node lên commit <code>${NEW_LOCAL_HASH:0:8}</code>...
• <b>Tác giả:</b> ${COMMIT_AUTHOR}
• <b>Nội dung:</b> <i>${COMMIT_MSG}</i>"
                    cd "$ANSIBLE_DIR" || exit 1
                    if ./ansible_deploy.sh deploy --all --fast ${args[@]+"${args[@]}"}; then
                        echo "$NEW_LOCAL_HASH" > "$LAST_DEPLOYED_FILE"
                        cd "$PROJECT_ROOT" || exit 1
                        send_telegram_notification "✅ <b>[Deploy Hoàn Tất]</b>
Cụm node đã được cập nhật thành công lên commit <code>${NEW_LOCAL_HASH:0:8}</code>!"
                        run_post_deploy_tests "$NEW_LOCAL_HASH"
                    else
                        cd "$PROJECT_ROOT" || exit 1
                        send_telegram_notification "❌ <b>[LỖI DEPLOY THỰC TẾ]</b>
Tiến trình cập nhật lên commit <code>${NEW_LOCAL_HASH:0:8}</code> ĐÃ THẤT BẠI ở bước chạy ansible_deploy (biên dịch hoặc triển khai lỗi)!"
                    fi

                    # Phục hồi stash sau khi hoàn tất toàn bộ quá trình build & deploy
                    if [ "$HAS_UNSTAGED" = true ]; then
                        echo "📦 Phục hồi lại các thay đổi local từ stash..."
                        if ! git stash pop >/dev/null 2>&1; then
                            echo "⚠️ [CẢNH BÁO] Deploy thành công từ clean HEAD, nhưng phục hồi stash local bị xung đột (conflict markers)!"
                            send_telegram_notification "⚠️ <b>[CẢNH BÁO STASH POP]</b> Deploy thành công commit <code>${NEW_LOCAL_HASH:0:8}</code>, nhưng phục hồi thay đổi local bị xung đột. Cần kiểm tra file conflict thủ công!"
                        fi
                    fi
                else
                    git rebase --abort >/dev/null 2>&1 || true
                    if [ "$HAS_UNSTAGED" = true ]; then
                        git stash pop >/dev/null 2>&1 || true
                    fi
                    echo "❌ Lỗi kéo mã nguồn (git pull --rebase) từ ${REMOTE}/${BRANCH}!"
                    send_telegram_notification "❌ <b>[LỖI GIT PULL]</b> Không thể kéo commit <code>${REMOTE_HASH:0:8}</code> về do conflict! Đã tự abort rebase."
                fi
                flock -u 201 2>/dev/null || true
            fi
        fi
    else
        echo "⚠️ [$(date '+%Y-%m-%d %H:%M:%S')] Không thể kết nối fetch từ git remote (${REMOTE}/${BRANCH}). Sẽ thử lại sau..."
    fi
    
    sleep $CHECK_INTERVAL
done
