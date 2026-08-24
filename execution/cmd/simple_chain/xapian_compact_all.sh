#!/bin/bash
# xapian_compact_all.sh — offline compaction cho toàn bộ Xapian DB dưới 1 node.
#
# Dùng khi: muốn nén ngay lập tức (không chờ live-compact tự chạy nền, xem
# XapianManager::compactInPlace() trong pkg/mvm/linker/src/xapian/xapian_manager.cpp),
# hoặc cho môi trường/DB mà node chưa kịp chạy live-compact tới.
#
# BẮT BUỘC dừng node trước khi chạy (khác live-compact — cái đó chạy được
# ngay trong tiến trình đang sống). Script tự từ chối chạy nếu phát hiện
# process simple_chain đang sống, để không compact vào 1 DB đang mở (glass
# backend không an toàn khi có tiến trình khác đồng thời ghi/đọc trong lúc
# xapian-compact chạy).
#
# Cách dùng:
#   ./xapian_compact_all.sh <xapian_base_path> [min_size_bytes]
#
# <xapian_base_path>: thư mục gốc Xapian của node — chính là fullXapianPath
#   trong app.go (Databases.RootPath + PathXapian, mặc định "/consensus/xapian"),
#   VD: ./sample/node0/data-write/data/consensus/xapian
# [min_size_bytes]: bỏ qua DB nhỏ hơn ngưỡng này (mặc định 1MB, giống
#   compactInPlace() để nhất quán).

set -euo pipefail

BASE_PATH="${1:-}"
MIN_SIZE_BYTES="${2:-1048576}"

if [ -z "$BASE_PATH" ]; then
    echo "Usage: $0 <xapian_base_path> [min_size_bytes]"
    exit 1
fi

if [ ! -d "$BASE_PATH" ]; then
    echo "[xapian_compact_all] Lỗi: thư mục '$BASE_PATH' không tồn tại."
    exit 1
fi

# --- An toàn: từ chối chạy nếu node đang sống ---
# Khớp đúng pattern process mà kill.sh/run.sh dùng cho node này.
if pgrep -f "go run . -config=config-.*\.json" >/dev/null 2>&1 || \
   pgrep -x "simple_chain" >/dev/null 2>&1; then
    echo "[xapian_compact_all] TỪ CHỐI: phát hiện process simple_chain đang chạy."
    echo "  Compact ngoài (offline) không an toàn khi node đang mở DB này."
    echo "  Dừng node trước (kill.sh), hoặc dùng live-compact tự động trong tiến trình"
    echo "  (XapianManager::compactInPlace(), đã chạy nền mặc định, không cần thao tác gì)."
    exit 1
fi

if ! command -v xapian-compact >/dev/null 2>&1; then
    echo "[xapian_compact_all] Lỗi: không tìm thấy 'xapian-compact' trong PATH."
    echo "  Cài đặt: apt-get install xapian-tools (Debian/Ubuntu)."
    exit 1
fi

TOTAL_BEFORE=0
TOTAL_AFTER=0
COMPACTED_COUNT=0
SKIPPED_COUNT=0

# Layout thật (xem execution/pkg/mvm/linker/src/my_extension/utils.cpp::createFullPath):
#   {BASE_PATH}/{address_hex}/{keccak256(dbname)}/  <- 1 thư mục Xapian glass DB
# => quét đúng 2 cấp thư mục con.
while IFS= read -r -d '' db_dir; do
    # Chỉ xử lý nếu trông giống 1 Xapian glass DB thật (có ít nhất 1 file bên trong).
    if [ -z "$(ls -A "$db_dir" 2>/dev/null)" ]; then
        continue
    fi

    size_bytes=$(du -sb "$db_dir" 2>/dev/null | cut -f1)
    if [ -z "$size_bytes" ] || [ "$size_bytes" -lt "$MIN_SIZE_BYTES" ]; then
        SKIPPED_COUNT=$((SKIPPED_COUNT + 1))
        continue
    fi

    tmp_dir="${db_dir}.compact_tmp"
    backup_dir="${db_dir}.pre_compact_backup"
    rm -rf "$tmp_dir" "$backup_dir"

    echo "[xapian_compact_all] Compacting: $db_dir ($(numfmt --to=iec "$size_bytes" 2>/dev/null || echo "${size_bytes}B"))"
    if ! xapian-compact --no-renumber "$db_dir" "$tmp_dir" 2>&1; then
        echo "[xapian_compact_all] LỖI compact '$db_dir' — bỏ qua, giữ nguyên bản gốc."
        rm -rf "$tmp_dir"
        continue
    fi

    new_size_bytes=$(du -sb "$tmp_dir" 2>/dev/null | cut -f1)
    mv "$db_dir" "$backup_dir"
    mv "$tmp_dir" "$db_dir"
    rm -rf "$backup_dir"

    TOTAL_BEFORE=$((TOTAL_BEFORE + size_bytes))
    TOTAL_AFTER=$((TOTAL_AFTER + new_size_bytes))
    COMPACTED_COUNT=$((COMPACTED_COUNT + 1))
    echo "[xapian_compact_all]   -> $(numfmt --to=iec "$new_size_bytes" 2>/dev/null || echo "${new_size_bytes}B") xong."
done < <(find "$BASE_PATH" -mindepth 2 -maxdepth 2 -type d -print0)

echo ""
echo "[xapian_compact_all] Hoàn tất: đã compact ${COMPACTED_COUNT} DB, bỏ qua ${SKIPPED_COUNT} DB nhỏ."
if [ "$TOTAL_BEFORE" -gt 0 ]; then
    saved=$((TOTAL_BEFORE - TOTAL_AFTER))
    pct=$((saved * 100 / TOTAL_BEFORE))
    echo "[xapian_compact_all] Tổng dung lượng: $(numfmt --to=iec "$TOTAL_BEFORE") -> $(numfmt --to=iec "$TOTAL_AFTER") (giảm ${pct}%)"
fi
