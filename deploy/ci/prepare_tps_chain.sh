#!/usr/bin/env bash
# ==============================================================================
# 🚀 PREPARE TPS CHAIN & GENESIS SPAM KEYS
# Tự động kiểm tra danh sách ví TPS (tự động sinh mới bằng main.go nếu thiếu),
# nạp ví & BLS key trực tiếp vào genesis.json (giữ nguyên genesis.json.example sạch trên git),
# reset cụm Public Chain và cập nhật IP endpoints.
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METANODE_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SUITE_DIR="$(cd "${METANODE_DIR}/../metanode-suite" 2>/dev/null && pwd || echo "${METANODE_DIR}/metanode-suite")"

GENESIS_EXAMPLE="${METANODE_DIR}/deploy/systemd/genesis.json.example"
GENESIS_JSON="${METANODE_DIR}/deploy/systemd/genesis.json"
KEYS_DIR="${SUITE_DIR}/test_tps/gen_spam_keys"
KEYS_FILE="${TPS_KEYS_FILE:-${KEYS_DIR}/generated_keys.json}"
GEN_KEYS_SCRIPT="${KEYS_DIR}/main.go"
MANAGE_GENESIS="${KEYS_DIR}/manage_genesis.py"

CI_CONFIG="${SCRIPT_DIR}/ci_config.yaml"
YAML_REQUIRED_KEYS=""
if [ -f "$CI_CONFIG" ]; then
    YAML_REQUIRED_KEYS=$(python3 -c "import yaml; c=yaml.safe_load(open('${CI_CONFIG}')); print(c.get('genesis', {}).get('required_min_keys', ''))" 2>/dev/null || echo "")
fi

CHECK_ONLY=false
REQUIRED_KEYS="${TPS_REQUIRED_KEYS:-${YAML_REQUIRED_KEYS:-50000}}"

for arg in "$@"; do
    if [ "$arg" = "--check-keys-only" ] || [ "$arg" = "--dry-run" ]; then
        CHECK_ONLY=true
    elif [[ "$arg" =~ ^[0-9]+$ ]]; then
        REQUIRED_KEYS="$arg"
    fi
done

echo "=========================================================="
echo "⚡ [TPS PREPARATION] KHỞI TẠO MÔI TRƯỜNG CHO BÀI TEST TPS"
echo "   - Yêu cầu số lượng ví: $REQUIRED_KEYS"
echo "   - File danh sách ví:   $KEYS_FILE"
echo "=========================================================="

# 1. Kiểm tra danh sách keys, nếu không tồn tại hoặc không đủ số lượng thì tự sinh mới bằng main.go
NEED_GENERATE=false
if [ ! -f "$KEYS_FILE" ]; then
    echo "⚠️ File keys chưa tồn tại ($KEYS_FILE)."
    NEED_GENERATE=true
else
    CURRENT_KEYS_COUNT=$(python3 -c "import json; data=json.load(open('$KEYS_FILE')); print(len(data))" 2>/dev/null || echo "0")
    echo "📊 Số lượng keys hiện có trong file: $CURRENT_KEYS_COUNT (Yêu cầu tối thiểu: $REQUIRED_KEYS)"
    if [ "$CURRENT_KEYS_COUNT" -lt "$REQUIRED_KEYS" ]; then
        echo "⚠️ Số lượng keys hiện tại ($CURRENT_KEYS_COUNT) không đủ $REQUIRED_KEYS."
        NEED_GENERATE=true
    else
        echo "✅ File keys đã đáp ứng đầy đủ số lượng ($CURRENT_KEYS_COUNT >= $REQUIRED_KEYS)."
    fi
fi

if [ "$NEED_GENERATE" = true ]; then
    echo "⚙️ Đang tự động sinh $REQUIRED_KEYS keys bằng script Go: $GEN_KEYS_SCRIPT..."
    if [ ! -f "$GEN_KEYS_SCRIPT" ]; then
        echo "❌ Lỗi: Không tìm thấy script sinh key tại: $GEN_KEYS_SCRIPT"
        exit 1
    fi
    cd "$KEYS_DIR"
    go run main.go -count "$REQUIRED_KEYS" -keys-output "$KEYS_FILE" -genesis-in "" -genesis-out ""
    NEW_COUNT=$(python3 -c "import json; data=json.load(open('$KEYS_FILE')); print(len(data))" 2>/dev/null || echo "0")
    echo "✅ Đã tự động tạo mới $NEW_COUNT ví spam thành công tại: $KEYS_FILE!"
fi

if [ "$CHECK_ONLY" = true ]; then
    echo "🔍 [CHECK-ONLY] Đã kiểm tra và chuẩn bị xong keys ($KEYS_FILE)."
    echo "   Dừng lại mà không can thiệp reset cluster."
    exit 0
fi

if [ ! -f "$MANAGE_GENESIS" ]; then
    echo "❌ Lỗi: Không tìm thấy script manage_genesis.py tại: $MANAGE_GENESIS"
    exit 1
fi

# 2. Khởi tạo genesis.json từ genesis.json.example (giữ genesis.json.example sạch trên git)
echo "📄 2. Tạo genesis.json từ template genesis.json.example..."
cp "$GENESIS_EXAMPLE" "$GENESIS_JSON"

# 3. Nạp danh sách ví TPS & BLS keys trực tiếp vào genesis.json
echo "🔑 3. Nạp danh sách ví TPS & BLS keys trực tiếp vào genesis.json (chống trùng lặp, không sửa file git)..."
python3 "$MANAGE_GENESIS" add "$GENESIS_JSON" "$KEYS_FILE" "$GENESIS_JSON"

# 4. Triển khai & Reset lại toàn bộ cụm Public Chain
echo "🚀 4. Triển khai & Reset cụm Public Chain với Genesis mới..."
cd "${METANODE_DIR}/deploy/ansible"
./ansible_deploy.sh --reset-all --open-ports

# 5. Cập nhật IP & RPC Endpoints vào metanode-suite
echo "⚙️  5. Cập nhật cấu hình IP & RPC endpoints..."
cd "${SUITE_DIR}/scripts/update-ip"
./update-ip.sh

echo "=========================================================="
echo "✅ HOÀN TẤT CHUẨN BỊ MÔI TRƯỜNG TEST TPS!"
echo "=========================================================="
