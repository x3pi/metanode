#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  Clean Report Files
#  Xóa các file báo cáo, log hệ thống, log test và file rác
# ═══════════════════════════════════════════════════════════════════

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

echo "🧹 Đang dọn dẹp các file báo cáo trong thư mục $SCRIPT_DIR..."
rm -f "$SCRIPT_DIR"/stability_report_*.md
rm -f "$SCRIPT_DIR"/debug_report_*.md

echo "🧹 Đang dọn dẹp log của tool test trong execution/cmd/tool..."
find "$PROJECT_ROOT/execution/cmd/tool" -type f -name "*.log" -delete
find "$PROJECT_ROOT/execution/cmd/tool" -type f -name "*_runs_report.txt" -delete

echo "🧹 Đang dọn dẹp log node execution (node_logs)..."
rm -f "$PROJECT_ROOT/execution/node_logs"/*.log

echo "🧹 Đang dọn dẹp các file rác trong thư mục /tmp..."
rm -f /tmp/blast_*.log
rm -f /tmp/multinode_blast_*.log
rm -f /tmp/blast_accounts_*.json
rm -f /tmp/blast_start_signal*
rm -f /tmp/backend_start_ms.log
rm -f /tmp/perf_accumulation.log

echo "✅ Đã dọn dẹp xong toàn bộ."
