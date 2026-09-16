#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════════╗
# ║  📦  METANODE DEPLOY PACKAGE GENERATOR                                        ║
# ║                                                                              ║
# ║  Đóng gói toàn bộ thư mục deploy/ thành file ZIP sạch sẽ:                    ║
# ║  - Giữ nguyên các binaries trong deploy/bin/                                  ║
# ║  - Giữ nguyên toàn bộ scripts Ansible, Systemd, Monitors, Tài liệu           ║
# ║  - Tự động loại trừ logs, cache, PID, keys bí mật, dữ liệu runtime           ║
# ╚══════════════════════════════════════════════════════════════════════════════╝

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXCLUDE_FILE="$SCRIPT_DIR/.package_exclude"
BIN_DIR="$SCRIPT_DIR/bin"
TIMESTAMP="$(date +'%Y%m%d_%H%M%S')"
DEFAULT_OUT_DIR="$SCRIPT_DIR"
OUTPUT_ZIP=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

usage() {
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}📦  METANODE DEPLOY PACKAGE GENERATOR${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "Cách dùng: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --output <PATH>     Đường dẫn file zip xuất ra (Mặc định: deploy/metanode-deploy-<timestamp>.zip)"
    echo "  --no-check-bin      Không kiểm tra 6 file binary trong deploy/bin/"
    echo "  -h, --help          Hiển thị trợ giúp này"
    echo ""
}

CHECK_BIN=true

while [[ $# -gt 0 ]]; do
    case "$1" in
        --output)
            OUTPUT_ZIP="$2"
            shift 2
            ;;
        --no-check-bin)
            CHECK_BIN=false
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo -e "${RED}Tham số không hợp lệ: $1${NC}"
            usage
            exit 1
            ;;
    esac
done

if [ -z "$OUTPUT_ZIP" ]; then
    OUTPUT_ZIP="$DEFAULT_OUT_DIR/metanode-deploy-${TIMESTAMP}.zip"
fi

echo -e "${CYAN}=== 1. Kiểm tra các điều kiện tiên quyết ===${NC}"

# 1. Kiểm tra lệnh zip
if ! command -v zip &> /dev/null; then
    echo -e "${RED}❌ Không tìm thấy lệnh 'zip'. Vui lòng cài đặt: sudo apt update && sudo apt install -y zip${NC}"
    exit 1
fi

# 2. Kiểm tra file exclude
if [ ! -f "$EXCLUDE_FILE" ]; then
    echo -e "${RED}❌ Không tìm thấy file quy tắc loại trừ: $EXCLUDE_FILE${NC}"
    exit 1
fi

# 3. Kiểm tra các file binary trong deploy/bin
if [ "$CHECK_BIN" = true ]; then
    REQUIRED_BINS=("metanode" "simple_chain" "cross_chain_relayer" "register_chains" "bls_pubkey" "gen_recovery_committee")
    MISSING_BINS=()

    for b in "${REQUIRED_BINS[@]}"; do
        if [ ! -f "$BIN_DIR/$b" ]; then
            MISSING_BINS+=("$b")
        fi
    done

    if [ ${#MISSING_BINS[@]} -gt 0 ]; then
        echo -e "${RED}❌ Thiếu các file binary trong deploy/bin/: ${MISSING_BINS[*]}${NC}"
        echo -e "${YELLOW}👉 Hãy chạy trước: ./build_private_chain_bins.sh để build đủ 6 file binary.${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ Đã xác nhận đủ 6 binaries trong $BIN_DIR${NC}"
fi

echo -e "\n${CYAN}=== 2. Tiến hành đóng gói ZIP ===${NC}"
echo -e "Thư mục nguồn: ${BOLD}$SCRIPT_DIR${NC}"
echo -e "File nén đích:  ${BOLD}$OUTPUT_ZIP${NC}"
echo -e "File loại trừ:  ${BOLD}$EXCLUDE_FILE${NC}"

# Di chuyển ra thư mục cha để tạo đường dẫn deploy/ chuẩn trong file zip
PARENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TARGET_NAME="$(basename "$SCRIPT_DIR")"

cd "$PARENT_DIR"

# Tạo danh sách các tham số exclude cho zip từ file .package_exclude
EXCLUDE_ARGS=()
while IFS= read -r line || [ -n "$line" ]; do
    # Bỏ qua dòng trống hoặc bắt đầu bằng #
    [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
    # Strip whitespace
    pattern="$(echo "$line" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    [ -z "$pattern" ] && continue
    # Thêm prefix tên thư mục deploy/
    EXCLUDE_ARGS+=("-x" "$TARGET_NAME/$pattern")
done < "$EXCLUDE_FILE"

# Đồng thời exclude chính các file zip đầu ra nếu lưu trong deploy/
EXCLUDE_ARGS+=("-x" "$TARGET_NAME/*.zip")
EXCLUDE_ARGS+=("-x" "$TARGET_NAME/.*")
EXCLUDE_ARGS+=("-x" "$TARGET_NAME/ansible_private_chains/*")
EXCLUDE_ARGS+=("-x" "$TARGET_NAME/ansible_private_chains")

zip -r "$OUTPUT_ZIP" "$TARGET_NAME" "${EXCLUDE_ARGS[@]}" > /dev/null

echo -e "\n${GREEN}==============================================================================${NC}"
echo -e "${GREEN}🎉 ĐÓNG GÓI THÀNH CÔNG!${NC}"
echo -e "${GREEN}==============================================================================${NC}"
ls -lh "$OUTPUT_ZIP"
echo ""
echo -e "👉 File ZIP này đã bao gồm:"
echo -e "   - Thư mục ${BOLD}deploy/bin/${NC} (chứa đủ cả 6 binaries đã build)"
echo -e "   - Thư mục ${BOLD}deploy/ansible/${NC} (playbooks, roles, inventory mẫu)"
echo -e "   - Thư mục ${BOLD}deploy/systemd/${NC} & hướng dẫn cài đặt"
echo -e "👉 Đã loại trừ: .env, inventory.yml, private_dev_keys.json, logs, pid, cache, ansible_private_chains"
