#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Clean Report Files
#  Xóa các file báo cáo stability_report_*.md và debug_report_*.md
# ═══════════════════════════════════════════════════════════════════

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "🧹 Đang dọn dẹp các file báo cáo trong thư mục $SCRIPT_DIR..."
rm -f "$SCRIPT_DIR"/stability_report_*.md
rm -f "$SCRIPT_DIR"/debug_report_*.md

echo "✅ Đã dọn dẹp xong."
