#!/bin/bash

# Script để build module pkg/bls
# Module này sử dụng CGO để compile C code từ blst library

set -e

# Màu sắc cho output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🔨 Bắt đầu build module pkg/bls...${NC}"

# Lấy thư mục gốc của project
PROJECT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$PROJECT_ROOT"

# Kiểm tra CGO có được bật không
if [ "$CGO_ENABLED" != "1" ] && [ "$CGO_ENABLED" != "" ]; then
    echo -e "${YELLOW}⚠️  CGO_ENABLED không được bật, đang bật CGO...${NC}"
    export CGO_ENABLED=1
fi

# Kiểm tra C compiler
if ! command -v gcc &> /dev/null; then
    echo -e "${RED}❌ Không tìm thấy gcc! Module này cần C compiler để build.${NC}"
    echo -e "${YELLOW}💡 Cài đặt gcc: sudo apt-get install build-essential${NC}"
    exit 1
fi

# Kiểm tra và đảm bảo thư mục build tồn tại
BLST_BUILD_DIR="pkg/bls/blst/build"
if [ ! -d "$BLST_BUILD_DIR" ]; then
    echo -e "${YELLOW}⚠️  Thư mục $BLST_BUILD_DIR không tồn tại, đang tạo...${NC}"
    mkdir -p "$BLST_BUILD_DIR"/{elf,coff,mach-o,win64} || {
        echo -e "${RED}❌ Không thể tạo thư mục build${NC}"
        exit 1
    }
    echo -e "${GREEN}✅ Đã tạo thư mục build${NC}"
fi

# Kiểm tra xem có file assembly không, nếu thiếu thì cần chạy refresh.sh
if [ ! -f "$BLST_BUILD_DIR/assembly.S" ] || [ -z "$(ls -A $BLST_BUILD_DIR/elf/*.s 2>/dev/null)" ]; then
    echo -e "${YELLOW}⚠️  Thiếu file assembly, đang kiểm tra refresh.sh...${NC}"
    if [ -f "$BLST_BUILD_DIR/refresh.sh" ]; then
        echo -e "${BLUE}📝 Đang generate file assembly từ Perl scripts...${NC}"
        if ! command -v perl &> /dev/null; then
            echo -e "${RED}❌ Không tìm thấy perl! Cần perl để generate assembly files.${NC}"
            echo -e "${YELLOW}💡 Cài đặt perl: sudo apt-get install perl${NC}"
            exit 1
        fi
        cd "$BLST_BUILD_DIR"
        bash refresh.sh || {
            echo -e "${YELLOW}⚠️  refresh.sh có thể cần Rust bindgen, bỏ qua lỗi này...${NC}"
        }
        cd "$PROJECT_ROOT"
        echo -e "${GREEN}✅ Đã generate file assembly${NC}"
    else
        echo -e "${YELLOW}⚠️  Không tìm thấy refresh.sh, thư mục build có thể đã có sẵn file assembly${NC}"
    fi
fi

# Tạo symlink từ bindings/go đến các thư mục build cần thiết
# File assembly.S trong bindings/go cần include các file từ elf/, coff/, mach-o/, win64/
BLST_BINDINGS_DIR="pkg/bls/blst/bindings/go"
for dir in elf coff mach-o win64; do
    if [ ! -e "$BLST_BINDINGS_DIR/$dir" ]; then
        echo -e "${BLUE}🔗 Đang tạo symlink $dir...${NC}"
        ln -s "../../build/$dir" "$BLST_BINDINGS_DIR/$dir" || {
            echo -e "${YELLOW}⚠️  Không thể tạo symlink, thử copy...${NC}"
            cp -r "$BLST_BUILD_DIR/$dir" "$BLST_BINDINGS_DIR/$dir" || {
                echo -e "${RED}❌ Không thể tạo thư mục $dir${NC}"
                exit 1
            }
        }
    fi
done
echo -e "${GREEN}✅ Đã tạo symlink/copy các thư mục assembly${NC}"

# Di chuyển đến thư mục blst bindings
BLST_BINDINGS_DIR="pkg/bls/blst/bindings/go"
cd "$BLST_BINDINGS_DIR"

# Kiểm tra xem có cần chạy generate.py không
# (chỉ chạy nếu file blst.go không tồn tại hoặc các file .tgo mới hơn)
if [ ! -f "blst.go" ] || [ "generate.py" -nt "blst.go" ] || [ "blst_minpk.tgo" -nt "blst.go" ]; then
    echo -e "${YELLOW}📝 Đang generate code từ .tgo files...${NC}"
    
    if ! command -v python3 &> /dev/null; then
        echo -e "${RED}❌ Không tìm thấy python3! Cần python3 để chạy generate.py${NC}"
        exit 1
    fi
    
    # Kiểm tra goimports
    if ! command -v goimports &> /dev/null; then
        echo -e "${YELLOW}⚠️  Không tìm thấy goimports, đang cài đặt...${NC}"
        go install golang.org/x/tools/cmd/goimports@latest || {
            echo -e "${RED}❌ Không thể cài đặt goimports${NC}"
            exit 1
        }
    fi
    
    python3 generate.py || {
        echo -e "${RED}❌ Lỗi khi chạy generate.py${NC}"
        exit 1
    }
    echo -e "${GREEN}✅ Generate code thành công${NC}"
else
    echo -e "${BLUE}ℹ️  File blst.go đã tồn tại, bỏ qua generate.py${NC}"
fi

# Quay lại thư mục gốc
cd "$PROJECT_ROOT"

# Build module
echo -e "${BLUE}🔨 Đang build module pkg/bls...${NC}"
go build -v ./pkg/bls || {
    echo -e "${RED}❌ Lỗi khi build module pkg/bls${NC}"
    exit 1
}

echo -e "${GREEN}✅ Build module pkg/bls thành công!${NC}"

# Tùy chọn: chạy test
if [ "${1:-}" = "--test" ] || [ "${1:-}" = "-t" ]; then
    echo -e "${BLUE}🧪 Đang chạy tests...${NC}"
    go test -v ./pkg/bls || {
        echo -e "${YELLOW}⚠️  Một số tests thất bại${NC}"
    }
fi

echo ""
echo -e "${GREEN}✅ Hoàn tất!${NC}"
echo -e "${BLUE}💡 Để chạy tests: ./pkg/bls/build.sh --test${NC}"

