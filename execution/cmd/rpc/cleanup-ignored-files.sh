#!/bin/bash

# Script để xóa các file/folder không được commit (bị bỏ qua bởi .gitignore)
# Sử dụng: ./cleanup-ignored-files.sh
#
# Script này sẽ xóa:
# - Database files (LevelDB)
# - Log files
# - Backup files
# - Binary executables
# - Cache files
# - System files

set -e  # Dừng nếu có lỗi nghiêm trọng

# Màu sắc cho output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🧹 Bắt đầu dọn dẹp các file không được commit...${NC}"

# Kiểm tra xem có phải trong git repository không
if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo -e "${RED}❌ Lỗi: Không phải git repository!${NC}"
    exit 1
fi

# Đếm số file trước khi xóa
BEFORE_COUNT=$(git ls-files --others --ignored --exclude-standard 2>/dev/null | wc -l)
echo -e "${YELLOW}📊 Số file ignored trước khi xóa: ${BEFORE_COUNT}${NC}"

if [ "$BEFORE_COUNT" -eq 0 ]; then
    echo -e "${GREEN}✅ Không có file nào cần xóa!${NC}"
    exit 0
fi

# Xóa các folder được ignore trong cmd/rpc-client
echo -e "${BLUE}🗑️  Xóa database và log files...${NC}"
[ -d "cmd/rpc-client/db" ] && rm -rf cmd/rpc-client/db && echo "  ✓ Đã xóa cmd/rpc-client/db"
[ -d "cmd/rpc-client/logs" ] && rm -rf cmd/rpc-client/logs && echo "  ✓ Đã xóa cmd/rpc-client/logs"
[ -d "cmd/rpc-client/privatekey_db_encrypted" ] && rm -rf cmd/rpc-client/privatekey_db_encrypted && echo "  ✓ Đã xóa cmd/rpc-client/privatekey_db_encrypted"
[ -f "rpc-client" ] && rm -f rpc-client && echo "  ✓ Đã xóa rpc-client binary"

# Xóa các file backup
echo -e "${BLUE}🗑️  Xóa các file backup...${NC}"
BACKUP_COUNT=$(find . -name "*.backup" -type f 2>/dev/null | wc -l)
find . -name "*.backup" -type f -delete 2>/dev/null || true
[ "$BACKUP_COUNT" -gt 0 ] && echo "  ✓ Đã xóa $BACKUP_COUNT file backup"

# Xóa các file output/coverage
echo -e "${BLUE}🗑️  Xóa các file output/coverage...${NC}"
OUT_COUNT=$(find . -name "*.out" -type f 2>/dev/null | wc -l)
find . -name "*.out" -type f -delete 2>/dev/null || true
[ "$OUT_COUNT" -gt 0 ] && echo "  ✓ Đã xóa $OUT_COUNT file output"

# Xóa các file log
echo -e "${BLUE}🗑️  Xóa các file log...${NC}"
LOG_COUNT=$(find . -name "*.log" -type f 2>/dev/null | wc -l)
find . -name "*.log" -type f -delete 2>/dev/null || true
[ "$LOG_COUNT" -gt 0 ] && echo "  ✓ Đã xóa $LOG_COUNT file log"

# Xóa Python cache
echo -e "${BLUE}🗑️  Xóa Python cache...${NC}"
PYCACHE_COUNT=$(find . -name "__pycache__" -type d 2>/dev/null | wc -l)
find . -name "__pycache__" -type d -exec rm -rf {} + 2>/dev/null || true
find . -name "*.pyc" -type f -delete 2>/dev/null || true
[ "$PYCACHE_COUNT" -gt 0 ] && echo "  ✓ Đã xóa $PYCACHE_COUNT thư mục __pycache__"

# Xóa macOS system files
echo -e "${BLUE}🗑️  Xóa macOS system files...${NC}"
DSSTORE_COUNT=$(find . -name ".DS_Store" -type f 2>/dev/null | wc -l)
find . -name ".DS_Store" -type f -delete 2>/dev/null || true
[ "$DSSTORE_COUNT" -gt 0 ] && echo "  ✓ Đã xóa $DSSTORE_COUNT file .DS_Store"

# Xóa các binary files được build
echo -e "${BLUE}🗑️  Xóa các binary files...${NC}"
[ -f "build_app" ] && rm -f build_app && echo "  ✓ Đã xóa build_app"
[ -f "build" ] && rm -f build && echo "  ✓ Đã xóa build"

# Xóa các binary trong cmd (ngoại trừ source files)
BINARY_COUNT=$(find cmd -type f -perm +111 ! -name "*.go" ! -name "*.sh" ! -name "*.json" ! -name "*.md" ! -name "*.txt" ! -name "*.lua" 2>/dev/null | wc -l)
find cmd -type f -perm +111 ! -name "*.go" ! -name "*.sh" ! -name "*.json" ! -name "*.md" ! -name "*.txt" ! -name "*.lua" -delete 2>/dev/null || true
[ "$BINARY_COUNT" -gt 0 ] && echo "  ✓ Đã xóa $BINARY_COUNT binary files"

# Đếm số file sau khi xóa
AFTER_COUNT=$(git ls-files --others --ignored --exclude-standard 2>/dev/null | wc -l)
DELETED=$((BEFORE_COUNT - AFTER_COUNT))

echo ""
echo -e "${GREEN}✅ Hoàn tất!${NC}"
echo -e "${YELLOW}📊 Số file ignored sau khi xóa: ${AFTER_COUNT}${NC}"
echo -e "${GREEN}🗑️  Đã xóa: ${DELETED} file/folder${NC}"
echo ""
echo -e "${BLUE}💡 Lưu ý: Các file này sẽ được tạo lại tự động khi chạy ứng dụng.${NC}"

