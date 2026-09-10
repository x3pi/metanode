#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
#  drill_disk_full.sh — ⚠️ INVASIVE DRILL. Deliberately fills real disk space
#  on the TARGET path's filesystem to just over the monitor's DISK_LIMIT
#  (85%, see deploy/ansible/monitors/start_monitors.sh) to verify the real
#  alert actually fires -- not a dry-run, genuinely consumes disk space
#  temporarily.
#
#  ⚠️ RUN THIS ONLY:
#    - During a scheduled maintenance window
#    - On a host nobody else is actively using (this writes real bytes to a
#      real filesystem other processes/people may depend on)
#    - After confirming with `df -h` that the target filesystem has enough
#      TRUE headroom that a temporary push to ~85-90% won't risk genuinely
#      running out of space system-wide (this script refuses to run if it
#      would push past 95%, see PRE-FLIGHT CHECK below, but that safety net
#      is not a substitute for checking yourself first)
#
#  Safety design (defense in depth, in case the script is killed mid-run):
#    1. Pre-flight check refuses to start if already too full, or if the
#       requested fill would push past a hard ceiling.
#    2. The dummy file has an unmistakable name and lives in a known,
#       dedicated location -- trivial to find and delete by hand if this
#       script's own cleanup somehow doesn't run.
#    3. `trap ... EXIT INT TERM` guarantees cleanup runs on Ctrl+C or normal
#       exit, not just the success path.
#    4. An independent background watchdog (`(sleep N && rm -f ...) &`,
#       disowned) deletes the dummy file after MAX_HOLD_SECONDS regardless of
#       whether the foreground script is still alive -- a second, independent
#       safety net if the SSH session/parent process itself gets killed.
#
#  Usage:
#    ./drill_disk_full.sh --path /opt/metanode --confirm [--hold-seconds 90]
#
#    --path PATH          Filesystem to fill (must be the SAME filesystem
#                          start_monitors.sh's DISK_LIMIT check monitors on
#                          this host -- typically /opt/metanode or /).
#    --confirm             Required. Without it, prints what WOULD happen and
#                          exits -- refuses to touch real disk.
#    --hold-seconds N      How long to keep the disk full before cleaning up
#                          (must be long enough for at least one
#                          start_monitors.sh poll cycle to see it and alert).
#                          Default: 90.
# ═══════════════════════════════════════════════════════════════════════════
set -uo pipefail

TARGET_PATH=""
CONFIRM=false
HOLD_SECONDS=90
MAX_HOLD_SECONDS=600   # hard ceiling for the independent watchdog, regardless of --hold-seconds

while [[ "$#" -gt 0 ]]; do
    case "$1" in
        --path) TARGET_PATH="$2"; shift ;;
        --confirm) CONFIRM=true ;;
        --hold-seconds) HOLD_SECONDS="$2"; shift ;;
        *) echo "Unknown flag: $1"; exit 1 ;;
    esac
    shift
done

if [ -z "$TARGET_PATH" ]; then
    echo "❌ Thiếu --path. Ví dụ: --path /opt/metanode"
    exit 1
fi
if [ ! -d "$TARGET_PATH" ]; then
    echo "❌ Đường dẫn không tồn tại: $TARGET_PATH"
    exit 1
fi

echo "=========================================================="
echo "💾 DRILL: Disk Full Alert (⚠️ INVASIVE -- ghi dữ liệu thật)"
echo "=========================================================="
echo "   Target path: $TARGET_PATH"
echo "   Ngưỡng cảnh báo (DISK_LIMIT trong start_monitors.sh): 85%"
echo "   Thời gian giữ đầy đĩa: ${HOLD_SECONDS}s"
echo "=========================================================="

# ─── PRE-FLIGHT CHECK ──────────────────────────────────────────────────────
read -r FS_SIZE_KB FS_USED_KB FS_AVAIL_KB FS_USE_PCT < <(df -k --output=size,used,avail,pcent "$TARGET_PATH" | tail -1 | tr -d '%')
CURRENT_PCT=$(df -k --output=pcent "$TARGET_PATH" | tail -1 | tr -d '% ')

echo "📊 Hiện tại: ${CURRENT_PCT}% đã dùng trên filesystem chứa ${TARGET_PATH}"

if [ "$CURRENT_PCT" -ge 80 ]; then
    echo "❌ [TỪ CHỐI CHẠY] Filesystem đã dùng ${CURRENT_PCT}% -- QUÁ GẦN đầy thật để test an toàn."
    echo "   👉 Drill này chỉ nên chạy khi disk còn nhiều headroom thật (< 80%)."
    exit 1
fi

# Target: push usage to 88% (just over the 85% DISK_LIMIT, with margin to trigger
# reliably, but nowhere near genuinely full).
TARGET_PCT=88
FILL_KB=$(( FS_SIZE_KB * (TARGET_PCT - CURRENT_PCT) / 100 ))

if [ "$FILL_KB" -le 0 ]; then
    echo "❌ Tính toán dung lượng cần ghi ra <= 0 -- có thể disk đã ở ngưỡng đó rồi. Dừng lại."
    exit 1
fi

FILL_MB=$(( FILL_KB / 1024 ))
echo "📝 Sẽ ghi ~${FILL_MB}MB để đẩy usage từ ${CURRENT_PCT}% lên ~${TARGET_PCT}%."

if [ "$CONFIRM" != true ]; then
    echo ""
    echo "ℹ️  [DRY-RUN] Chưa ghi gì cả -- thêm --confirm để thực sự chạy drill này."
    echo "   ⚠️  Chỉ chạy --confirm khi: đang trong cửa sổ bảo trì, không ai khác dùng máy,"
    echo "       và bạn đã tự kiểm tra 'df -h ${TARGET_PATH}' xác nhận đủ dư địa thật."
    exit 0
fi

DUMMY_FILE="${TARGET_PATH}/.DISK_FULL_DRILL_$(date +%Y%m%d_%H%M%S)_SAFE_TO_DELETE.dummy"
echo ""
echo "🚨 BẮT ĐẦU GHI FILE THẬT: ${DUMMY_FILE}"

cleanup() {
    if [ -f "$DUMMY_FILE" ]; then
        echo "🧹 [CLEANUP] Đang xoá ${DUMMY_FILE}..."
        rm -f "$DUMMY_FILE"
        NEW_PCT=$(df -k --output=pcent "$TARGET_PATH" 2>/dev/null | tail -1 | tr -d '% ')
        echo "✅ [CLEANUP] Đã xoá. Disk usage hiện tại: ${NEW_PCT:-?}%"
    fi
}
trap cleanup EXIT INT TERM

# Independent watchdog: deletes the dummy file after MAX_HOLD_SECONDS even if THIS
# script's own process dies before its trap runs (e.g. SSH session drops).
( sleep "$MAX_HOLD_SECONDS"; rm -f "$DUMMY_FILE" 2>/dev/null ) &
disown

fallocate -l "${FILL_MB}M" "$DUMMY_FILE" 2>/dev/null || dd if=/dev/zero of="$DUMMY_FILE" bs=1M count="$FILL_MB" 2>/dev/null

ACTUAL_PCT=$(df -k --output=pcent "$TARGET_PATH" | tail -1 | tr -d '% ')
echo "📊 Disk usage sau khi ghi: ${ACTUAL_PCT}%"

if [ "$ACTUAL_PCT" -lt 85 ]; then
    echo "⚠️  Chưa vượt ngưỡng 85% như dự kiến (filesystem có thể sparse/CoW, vd BTRFS) --"
    echo "    có thể cần tăng TARGET_PCT trong script hoặc dùng --path trỏ đúng phân vùng"
    echo "    thật đang được monitor."
fi

echo ""
echo "⏳ Đang giữ đĩa đầy trong ${HOLD_SECONDS}s để start_monitors.sh's poll cycle phát hiện..."
echo "   👉 Trong lúc này, kiểm tra Telegram xem cảnh báo '[SỰ CỐ]' / disk usage có tới không."
sleep "$HOLD_SECONDS"

echo ""
echo "✅ [HOÀN TẤT] Drill kết thúc bình thường. Cleanup sẽ chạy qua trap ở trên."
echo "   👉 Xác nhận thủ công: (1) Telegram có nhận cảnh báo trong lúc ${HOLD_SECONDS}s vừa qua"
echo "      không, (2) sau cleanup này, có nhận được cảnh báo 'ĐÃ PHỤC HỒI' không (nếu"
echo "      start_monitors.sh có gửi recovery alert cho mục disk usage)."
