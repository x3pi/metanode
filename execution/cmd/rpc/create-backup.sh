#!/bin/bash

# Script để tạo backup nén (.tar.gz) của toàn bộ project
# Bỏ qua .git, các file/folder ẩn, và các file được ignore bởi .gitignore
# Sử dụng: ./create-backup.sh [tên-file-backup]

set -e

# Màu sắc cho output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Tên file backup (mặc định: MetaCoSign-YYYYMMDD-HHMMSS.tar.gz)
if [ -z "${1:-}" ]; then
    BACKUP_NAME="MetaCoSign-$(date +%Y%m%d-%H%M%S).tar.gz"
else
    BACKUP_NAME="$1"
    # Đảm bảo có extension .tar.gz
    if [[ ! "$BACKUP_NAME" =~ \.tar\.gz$ ]]; then
        BACKUP_NAME="${BACKUP_NAME}.tar.gz"
    fi
fi

echo -e "${BLUE}📦 Bắt đầu tạo backup...${NC}"
echo -e "${YELLOW}📁 Tên file backup: ${BACKUP_NAME}${NC}"

# Lấy thư mục gốc của project
PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$PROJECT_ROOT"

# Tạo danh sách các file/folder cần loại trừ
EXCLUDE_PATTERNS=(
    # Git
    "--exclude=.git"
    "--exclude=.gitignore"
    "--exclude=.gitattributes"
    
    # File/folder ẩn cụ thể (không dùng .* để tránh loại bỏ quá nhiều)
    "--exclude=.DS_Store"
    "--exclude=.vscode"
    "--exclude=.idea"
    "--exclude=.cursor"
    "--exclude=.env"
    "--exclude=.env.local"
    
    # Database và log files
    "--exclude=cmd/rpc-client/db"
    "--exclude=cmd/rpc-client/logs"
    "--exclude=cmd/rpc-client/privatekey_db_encrypted"
    
    # Binary files (chỉ loại trừ file binary ở thư mục gốc, không phải thư mục con)
    # Sử dụng ./rpc-client để chỉ loại trừ file binary ở root, không ảnh hưởng cmd/rpc-client
    "--exclude=./rpc-client"
    "--exclude=build_app"
    # KHÔNG exclude "build" vì nó sẽ loại trừ pkg/bls/blst/build (source code đã generate)
    # Chỉ exclude thư mục build ở root nếu cần
    "--exclude=./build"
    
    # Các file binary phổ biến (loại trừ ở mọi nơi)
    "--exclude=*.out"
    "--exclude=*.exe"
    "--exclude=*.bin"
    "--exclude=*.so"
    "--exclude=*.dylib"
    "--exclude=*.dll"
    "--exclude=*.a"  # Static library files
    
    # Backup files
    "--exclude=*.backup"
    "--exclude=*.log"
    
    # Python cache
    "--exclude=__pycache__"
    "--exclude=*.pyc"
    "--exclude=*.pyo"
    
    # Node modules
    "--exclude=node_modules"
    
    # IDE files
    "--exclude=*.swp"
    "--exclude=*.swo"
    "--exclude=*~"
    
    # Temporary files
    "--exclude=*.tmp"
    "--exclude=*.temp"
    
    # Test và demo files (theo .gitignore)
    "--exclude=cmd/test"
    "--exclude=cmd/demo"
    "--exclude=cmd/demoSocket"
    "--exclude=demo"
    
    # Build artifacts
    "--exclude=cmd/simple_chain/leveldb"
    "--exclude=cmd/simple_chain/xapian"
    "--exclude=cmd/simple_chain/xapian_database"
    "--exclude=cmd/storage/db_data"
    "--exclude=cmd/storage/running_instances"
    "--exclude=cmd/storage/storage_server"
    
    # Backup files đã tạo trước đó
    "--exclude=*.tar.gz"
    "--exclude=*.tar"
    "--exclude=*.zip"
)

# Nhưng giữ lại một số file ẩn quan trọng
# (tar sẽ tự động bỏ qua .* nhưng chúng ta có thể thêm lại nếu cần)

echo -e "${BLUE}🗜️  Đang nén project...${NC}"

# Tạo file tar.gz với các exclude patterns
# Sử dụng --exclude-vcs để tự động bỏ qua .git, .svn, etc.
# KHÔNG dùng --exclude-vcs-ignores vì nó có thể loại trừ nhầm các thư mục quan trọng
tar -czf "$BACKUP_NAME" \
    --exclude-vcs \
    "${EXCLUDE_PATTERNS[@]}" \
    --exclude="$BACKUP_NAME" \
    --exclude="*.tar.gz" \
    --exclude="*.tar" \
    --exclude="*.zip" \
    . 2>&1 | grep -v -E "Removing leading|file changed as we read it" || true

# Kiểm tra xem file có được tạo thành công không
if [ ! -f "$BACKUP_NAME" ]; then
    echo -e "${RED}❌ Lỗi: Không thể tạo file backup!${NC}"
    exit 1
fi

# Kiểm tra kích thước file
FILE_SIZE=$(du -h "$BACKUP_NAME" | cut -f1)
FILE_SIZE_BYTES=$(stat -f%z "$BACKUP_NAME" 2>/dev/null || stat -c%s "$BACKUP_NAME" 2>/dev/null)

echo ""
echo -e "${GREEN}✅ Backup đã được tạo thành công!${NC}"
echo -e "${YELLOW}📦 File: ${BACKUP_NAME}${NC}"
echo -e "${YELLOW}📊 Kích thước: ${FILE_SIZE} (${FILE_SIZE_BYTES} bytes)${NC}"
echo -e "${YELLOW}📍 Vị trí: $(pwd)/${BACKUP_NAME}${NC}"
echo ""
echo -e "${BLUE}💡 Để giải nén: tar -xzf ${BACKUP_NAME}${NC}"

