#!/usr/bin/env bash
set -e

# ==============================================================================
# Reset MetaNode Multi-Cluster an toàn theo cấu hình inventory.yml
# Khởi chạy bằng systemd, chỉ dừng và dọn đúng các node trong cấu hình.
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "🔄 Bắt đầu Reset toàn bộ cụm theo inventory.yml qua deploy_clusters.sh..."
exec "${SCRIPT_DIR}/deploy_clusters.sh" reset "$@"
