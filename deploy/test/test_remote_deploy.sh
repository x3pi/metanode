#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════════════════╗
# ║  🚀  AUTOMATED REMOTE DEPLOY & RPC TEST RUNNER                                ║
# ║                                                                              ║
# ║  Tự động hoá kiểm thử triển khai chuỗi Metanode sang máy từ xa:              ║
# ║  1. Đọc và xác thực cấu hình inventory.yml                                   ║
# ║  2. Dọn sạch dữ liệu cũ (/opt/metanode/node-X) trên các máy trong cụm        ║
# ║  3. Đóng gói hoặc lấy gói ZIP deploy mới nhất                                ║
# ║  4. Chuyển ZIP + inventory.yml sang máy kiểm thử đích                        ║
# ║  5. Thực thi tự động:                                                        ║
# ║     - ./ansible_deploy.sh --gen-keys --prebuilt-bin                          ║
# ║     - ./ansible_deploy.sh --start --clean --open-ports --prebuilt-bin        ║
# ║  6. Thăm dò RPC và gửi giao dịch test để xác nhận chain hoạt động            ║
# ╚══════════════════════════════════════════════════════════════════════════════╝

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
WORKSPACE_ROOT="$(cd "$DEPLOY_DIR/.." && pwd)"

# Màu sắc hiển thị
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# Giá trị mặc định
INVENTORY_FILE="${SCRIPT_DIR}/inventory.yml"
[ ! -f "$INVENTORY_FILE" ] && INVENTORY_FILE="${DEPLOY_DIR}/inventory.yml"
[ ! -f "$INVENTORY_FILE" ] && INVENTORY_FILE="${DEPLOY_DIR}/ansible/inventory.yml"
ZIP_FILE=""
BUILD_ZIP=false
BUILD_BINS=false
TARGET_HOST=""
TARGET_USER=""
TARGET_PASS_ARG=""
TARGET_KEY_ARG=""
TARGET_DIR="~/nhat/test-chain"
SKIP_CLEAN=false
SKIP_TX=false
RPC_URL=""
TEST_RPC_DIR="${SCRIPT_DIR}/test_tx"
[ ! -d "$TEST_RPC_DIR" ] && TEST_RPC_DIR="${SCRIPT_DIR}/test_rpc"
[ ! -d "$TEST_RPC_DIR" ] && TEST_RPC_DIR="${SCRIPT_DIR}"
[ ! -f "${TEST_RPC_DIR}/main.go" ] && TEST_RPC_DIR="${WORKSPACE_ROOT}/metanode-suite/test-simple/test-rpc"

usage() {
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}🚀 AUTOMATED REMOTE DEPLOY & RPC TEST RUNNER${NC}"
    echo -e "${CYAN}══════════════════════════════════════════════════════════════════════════════${NC}"
    echo -e "Cách dùng: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --inventory <PATH>      Đường dẫn file inventory.yml (Mặc định: deploy/test/inventory.yml)"
    echo "  --build, --build-bins   Tự động chạy build_chain_bins.sh rồi đóng gói package_deploy.sh"
    echo "  --build-zip             Bắt buộc tạo mới file zip bằng package_deploy.sh (không build lại bin)"
    echo "  --zip <PATH>            Đường dẫn file zip deploy kit (Mặc định: tự tìm file mới nhất)"
    echo "  --target-host <IP>      Chỉ định IP máy kiểm thử deploy (Mặc định: host đầu tiên trong inventory)"
    echo "  --target-user <USER>    User SSH máy kiểm thử (Mặc định: lấy từ inventory.yml)"
    echo "  --target-pass <PASS>    Mật khẩu SSH máy kiểm thử (Mặc định: lấy từ inventory.yml)"
    echo "  --target-key <PATH>     Đường dẫn SSH Private Key (Mặc định: lấy từ inventory.yml)"
    echo "  --target-dir <PATH>     Thư mục giải nén trên máy kiểm thử (Mặc định: ~/nhat/test-chain)"
    echo "  --skip-clean            Bỏ qua bước dọn dẹp /opt/metanode trên các máy"
    echo "  --skip-tx               Bỏ qua bước gửi giao dịch RPC kiểm chứng"
    echo "  --rpc-url <URL>         Chỉ định RPC URL kiểm chứng (Mặc định: tự phát hiện theo inventory)"
    echo "  -h, --help              Hiển thị trợ giúp này"
    echo ""
}

# Phân tích tham số dòng lệnh
while [[ $# -gt 0 ]]; do
    case "$1" in
        --inventory)
            INVENTORY_FILE="$2"
            shift 2
            ;;
        --build|--build-bin|--build-bins|--build-all)
            BUILD_BINS=true
            BUILD_ZIP=true
            shift
            ;;
        --zip)
            ZIP_FILE="$2"
            shift 2
            ;;
        --build-zip)
            BUILD_ZIP=true
            shift
            ;;
        --target-host)
            TARGET_HOST="$2"
            shift 2
            ;;
        --target-user)
            TARGET_USER="$2"
            shift 2
            ;;
        --target-pass|--password|--pass|password)
            TARGET_PASS_ARG="$2"
            shift 2
            ;;
        --target-key|--key|--ssh-key)
            TARGET_KEY_ARG="$2"
            shift 2
            ;;
        --target-dir)
            TARGET_DIR="$2"
            shift 2
            ;;
        --skip-clean)
            SKIP_CLEAN=true
            shift
            ;;
        --skip-tx)
            SKIP_TX=true
            shift
            ;;
        --rpc-url)
            RPC_URL="$2"
            shift 2
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

echo -e "${CYAN}==============================================================================${NC}"
echo -e "${BOLD}🚀 BẮT ĐẦU QUY TRÌNH KIỂM THỬ DEPLOY TỰ ĐỘNG SANG MÁY TEST${NC}"
echo -e "${CYAN}==============================================================================${NC}"

# ==============================================================================
# 1. KIỂM TRA ĐIỀU KIỆN TIÊN QUYẾT & FILE INVENTORY
# ==============================================================================
echo -e "\n${BOLD}[BƯỚC 1/6] Kiểm tra cấu hình và điều kiện môi trường...${NC}"

if [ ! -f "$INVENTORY_FILE" ]; then
    echo -e "${RED}❌ [LỖI NGHIÊM TRỌNG] Không tìm thấy file inventory: ${INVENTORY_FILE}${NC}"
    if [ -f "${SCRIPT_DIR}/inventory.example.yml" ]; then
        echo -e "${YELLOW}👉 Hãy tạo file cấu hình từ file mẫu có sẵn:${NC}"
        echo -e "   ${BOLD}cp ${SCRIPT_DIR}/inventory.example.yml ${SCRIPT_DIR}/inventory.yml${NC}"
        echo -e "   Sau đó mở file và điền thông tin máy chủ, mật khẩu SSH, và Telegram token (nếu có)."
    else
        echo -e "${YELLOW}👉 Hãy đảm bảo file tồn tại hoặc chỉ định đúng đường dẫn qua cờ: --inventory <path>${NC}"
    fi
    exit 1
fi
INVENTORY_FILE="$(cd "$(dirname "$INVENTORY_FILE")" && pwd)/$(basename "$INVENTORY_FILE")"
echo -e "${GREEN}✓ Đã tìm thấy inventory: ${BOLD}${INVENTORY_FILE}${NC}"

if ! command -v python3 &>/dev/null; then
    echo -e "${RED}❌ Không tìm thấy Python 3 trên hệ thống!${NC}"
    exit 1
fi

if ! python3 -c "import yaml" 2>/dev/null; then
    echo -e "${RED}❌ Thiếu thư viện PyYAML (python3 -m pip install pyyaml hoặc sudo apt install python3-yaml)${NC}"
    exit 1
fi

if ! command -v sshpass &>/dev/null; then
    echo -e "${YELLOW}⚠️ Không tìm thấy lệnh 'sshpass'. Nếu remote host dùng mật khẩu, vui lòng cài: sudo apt install -y sshpass${NC}"
fi

# ==============================================================================
# 2. PHÂN TÍCH FILE INVENTORY BẰNG PYTHON
# ==============================================================================
echo -e "\n${BOLD}[BƯỚC 2/6] Đọc thông tin các máy chủ từ inventory.yml...${NC}"

PARSED_YAML=$(python3 - <<EOF
import yaml, json, sys

try:
    with open("${INVENTORY_FILE}", "r") as f:
        data = yaml.safe_load(f)
except Exception as e:
    sys.stderr.write(f"Error loading YAML: {e}\n")
    sys.exit(1)

hosts_data = {}
if isinstance(data, dict):
    hosts_data = (data.get("all", {}).get("children", {}).get("metanode_cluster", {}).get("hosts", {})
                  or data.get("all", {}).get("hosts", {})
                  or data.get("hosts", {}))

global_vars = data.get("all", {}).get("vars", {}) if isinstance(data, dict) else {}
if not global_vars and isinstance(data, dict):
    global_vars = data.get("all", {}).get("children", {}).get("metanode_cluster", {}).get("vars", {}) or {}

tg_token = str(global_vars.get("telegram_bot_token", "")).strip()
tg_chat = str(global_vars.get("telegram_chat_id", "")).strip()

result = []
for hname, hvars in hosts_data.items():
    if not isinstance(hvars, dict):
        hvars = {}
    ip = str(hvars.get("ansible_host", hname)).strip()
    user = str(hvars.get("ansible_user", global_vars.get("ansible_user", "abc"))).strip()
    pwd = str(hvars.get("ansible_ssh_pass", global_vars.get("ansible_ssh_pass", ""))).strip()
    bpwd = str(hvars.get("ansible_become_pass", global_vars.get("ansible_become_pass", pwd))).strip()
    key = str(hvars.get("ansible_ssh_private_key_file", global_vars.get("ansible_ssh_private_key_file", ""))).strip()
    node_ids = hvars.get("node_ids", [])
    rpc_nodes = hvars.get("rpc_nodes", [])
    
    result.append({
        "host_name": hname,
        "ip": ip,
        "user": user,
        "pass": pwd,
        "become_pass": bpwd,
        "key": key,
        "node_ids": node_ids,
        "rpc_nodes": rpc_nodes
    })

print(json.dumps({
    "hosts": result,
    "telegram_bot_token": tg_token,
    "telegram_chat_id": tg_chat
}))
EOF
)

HOSTS_JSON=$(echo "$PARSED_YAML" | jq -c '.hosts // []')
TG_BOT_TOKEN=$(echo "$PARSED_YAML" | jq -r '.telegram_bot_token // empty')
TG_CHAT_ID=$(echo "$PARSED_YAML" | jq -r '.telegram_chat_id // empty')

if [ -z "$HOSTS_JSON" ] || [ "$HOSTS_JSON" == "[]" ]; then
    echo -e "${RED}❌ Không tìm thấy danh sách máy chủ nào trong ${INVENTORY_FILE}!${NC}"
    exit 1
fi

# Kiểm tra cấu hình thông báo Telegram
if [ -z "$TG_BOT_TOKEN" ] || [ "$TG_BOT_TOKEN" == "YOUR_TELEGRAM_BOT_TOKEN_HERE" ] || [ -z "$TG_CHAT_ID" ] || [ "$TG_CHAT_ID" == "YOUR_TELEGRAM_CHAT_ID_HERE" ]; then
    echo -e "${YELLOW}⚠️  [LƯU Ý THÔNG BÁO] Telegram bot token hoặc chat ID chưa được cấu hình trong inventory.yml.${NC}"
    echo -e "${YELLOW}   👉 Hệ thống vẫn tiếp tục deploy, nhưng thông báo monitor/cảnh báo sẽ không gửi tới Telegram.${NC}"
    echo -e "${YELLOW}   👉 Nếu muốn kích hoạt, hãy mở ${BOLD}${INVENTORY_FILE}${NC}${YELLOW} và điền 'telegram_bot_token' và 'telegram_chat_id'.${NC}"
else
    echo -e "${GREEN}✓ Đã phát hiện cấu hình thông báo Telegram: Chat ID = ${BOLD}${TG_CHAT_ID}${NC}"
fi

# Hàm thực thi SSH
exec_remote() {
    local ip="$1"
    local user="$2"
    local pass="$3"
    local key="$4"
    local cmd="$5"

    local exp_key="${key/#\~/$HOME}"
    local ssh_opts=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=15)
    if [ -n "$exp_key" ] && [ -f "$exp_key" ]; then
        ssh "${ssh_opts[@]}" -i "$exp_key" "${user}@${ip}" "$cmd"
    elif [ -n "$pass" ]; then
        sshpass -p "$pass" ssh "${ssh_opts[@]}" "${user}@${ip}" "$cmd"
    else
        ssh "${ssh_opts[@]}" "${user}@${ip}" "$cmd"
    fi
}

# Hàm thực thi lệnh sudo remote an toàn qua base64
exec_remote_sudo() {
    local ip="$1"
    local user="$2"
    local pass="$3"
    local become_pass="$4"
    local key="$5"
    local script_content="$6"

    local b64_script
    b64_script=$(printf '%s' "$script_content" | base64 -w 0)

    local wrapped_cmd
    if [ -n "$become_pass" ]; then
        wrapped_cmd="printf '%s\n' '$become_pass' | sudo -S bash -c \"echo '$b64_script' | base64 -d | bash\""
    else
        wrapped_cmd="sudo bash -c \"echo '$b64_script' | base64 -d | bash\""
    fi
    exec_remote "$ip" "$user" "$pass" "$key" "$wrapped_cmd"
}

# Hàm SCP file sang remote
exec_scp() {
    local ip="$1"
    local user="$2"
    local pass="$3"
    local key="$4"
    local src="$5"
    local dest="$6"

    local exp_key="${key/#\~/$HOME}"
    local scp_opts=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=15)
    if [ -n "$exp_key" ] && [ -f "$exp_key" ]; then
        scp "${scp_opts[@]}" -i "$exp_key" "$src" "${user}@${ip}:${dest}"
    elif [ -n "$pass" ]; then
        sshpass -p "$pass" scp "${scp_opts[@]}" "$src" "${user}@${ip}:${dest}"
    else
        scp "${scp_opts[@]}" "$src" "${user}@${ip}:${dest}"
    fi
}

# Xác định Target Test Host (máy sẽ chạy Ansible)
FIRST_IP=$(echo "$HOSTS_JSON" | jq -r '.[0].ip')
FIRST_USER=$(echo "$HOSTS_JSON" | jq -r '.[0].user')
FIRST_PASS=$(echo "$HOSTS_JSON" | jq -r '.[0].pass')
FIRST_BPASS=$(echo "$HOSTS_JSON" | jq -r '.[0].become_pass')
FIRST_KEY=$(echo "$HOSTS_JSON" | jq -r '.[0].key')

if [ -z "$TARGET_HOST" ]; then
    TARGET_HOST="$FIRST_IP"
fi

# Tìm thông tin xác thực cho TARGET_HOST
TARGET_INFO=$(echo "$HOSTS_JSON" | jq -c --arg tip "$TARGET_HOST" '.[] | select(.ip == $tip)' | head -n 1)
if [ -n "$TARGET_INFO" ] && [ "$TARGET_INFO" != "null" ]; then
    [ -z "$TARGET_USER" ] && TARGET_USER=$(echo "$TARGET_INFO" | jq -r '.user')
    TARGET_PASS=$(echo "$TARGET_INFO" | jq -r '.pass')
    TARGET_BPASS=$(echo "$TARGET_INFO" | jq -r '.become_pass')
    TARGET_KEY=$(echo "$TARGET_INFO" | jq -r '.key')
else
    [ -z "$TARGET_USER" ] && TARGET_USER="$FIRST_USER"
    TARGET_PASS="$FIRST_PASS"
    TARGET_BPASS="$FIRST_BPASS"
    TARGET_KEY="$FIRST_KEY"
fi

if [ -n "$TARGET_PASS_ARG" ]; then
    TARGET_PASS="$TARGET_PASS_ARG"
    TARGET_BPASS="$TARGET_PASS_ARG"
fi

if [ -n "$TARGET_KEY_ARG" ]; then
    TARGET_KEY="$TARGET_KEY_ARG"
fi

if [ -z "$TARGET_DIR" ] || [[ "$TARGET_DIR" == "~"* ]]; then
    TARGET_DIR="/home/${TARGET_USER}/nhat/test-chain"
fi

echo -e "   • Máy kiểm thử Deploy (Deployer): ${BOLD}${TARGET_USER}@${TARGET_HOST}${NC}"
if [ -n "$TARGET_KEY" ]; then
    EXP_TKEY="${TARGET_KEY/#\~/$HOME}"
    if [ -f "$EXP_TKEY" ]; then
        echo -e "   • Xác thực SSH:                   ${GREEN}SSH Key (${TARGET_KEY})${NC}"
    else
        echo -e "   • Xác thực SSH:                   ${YELLOW}SSH Key (chú ý: file '${TARGET_KEY}' chưa thấy trên máy này)${NC}"
    fi
elif [ -n "$TARGET_PASS" ]; then
    echo -e "   • Xác thực SSH:                   ${CYAN}Mật khẩu SSH${NC}"
else
    echo -e "   • Xác thực SSH:                   ${CYAN}Mặc định hệ thống (ssh-agent / default keys)${NC}"
fi
echo -e "   • Thư mục đích:                    ${BOLD}${TARGET_DIR}${NC}"

# Tìm RPC URL để test giao dịch sau khi deploy
if [ -z "$RPC_URL" ]; then
    RPC_NODE_INFO=$(echo "$HOSTS_JSON" | jq -c '[.[] | select((.rpc_nodes | length) > 0)][0] // empty')
    if [ -n "$RPC_NODE_INFO" ] && [ "$RPC_NODE_INFO" != "null" ]; then
        RPC_IP=$(echo "$RPC_NODE_INFO" | jq -r '.ip')
        FIRST_RPC_ID=$(echo "$RPC_NODE_INFO" | jq -r '.rpc_nodes[0]')
        RPC_PORT=$(( 10746 + FIRST_RPC_ID ))
        RPC_URL="http://${RPC_IP}:${RPC_PORT}"
    else
        RPC_URL="http://${TARGET_HOST}:10746"
    fi
fi
echo -e "   • RPC Endpoint kiểm chứng:        ${BOLD}${RPC_URL}${NC}"

# ==============================================================================
# 3. DỌN DẸP DỮ LIỆU CŨ TRÊN CÁC MÁY CHỦ (/opt/metanode/node-X)
# ==============================================================================
if [ "$SKIP_CLEAN" = false ]; then
    echo -e "\n${BOLD}[BƯỚC 3/6] Dọn dẹp sạch sẽ dữ liệu cũ (/opt/metanode/node-X)...${NC}"
    
    TOTAL_HOSTS=$(echo "$HOSTS_JSON" | jq -r 'length')
    for (( i=0; i<$TOTAL_HOSTS; i++ )); do
        H_IP=$(echo "$HOSTS_JSON" | jq -r ".[$i].ip")
        H_USER=$(echo "$HOSTS_JSON" | jq -r ".[$i].user")
        H_PASS=$(echo "$HOSTS_JSON" | jq -r ".[$i].pass")
        H_BPASS=$(echo "$HOSTS_JSON" | jq -r ".[$i].become_pass")
        H_KEY=$(echo "$HOSTS_JSON" | jq -r ".[$i].key")
        N_IDS=$(echo "$HOSTS_JSON" | jq -r ".[$i].node_ids | join(\" \")")

        echo -e "   👉 Đang dừng dịch vụ và dọn dẹp tại ${BOLD}${H_USER}@${H_IP}${NC} (Node IDs: [${N_IDS}])..."
        
        # Script dọn dẹp từ xa an toàn (xử lý cả mountpoint btrfs và tránh pkill tự match chính nó)
        CLEAN_CMD=$(cat <<'EOF'
pkill -9 -f '[s]tart_monitors.sh' 2>/dev/null || true
pkill -9 -f '[b]lock_hash_checker' 2>/dev/null || true
systemctl stop 'metanode-*' 2>/dev/null || true
pkill -9 -x metanode 2>/dev/null || true
pkill -9 -x simple_chain 2>/dev/null || true
EOF
)
        for nid in $N_IDS; do
            CLEAN_CMD="${CLEAN_CMD}
if mountpoint -q \"/opt/metanode/node-${nid}/data\" 2>/dev/null; then
    umount -l \"/opt/metanode/node-${nid}/data\" 2>/dev/null || true
fi
if mountpoint -q \"/opt/metanode/node-${nid}\" 2>/dev/null; then
    umount -l \"/opt/metanode/node-${nid}\" 2>/dev/null || true
fi
rm -rf \"/opt/metanode/node-${nid}\"
rm -rf \"/opt/metanode-deploy/node-${nid}_keys\"
rm -rf \"/mnt/metanode_snapshots/node-${nid}\"/* \"/mnt/metanode_snapshots/node-${nid}\"/.[!.]* 2>/dev/null || true
rm -rf \"/mnt/metanode_snapshots/node-${nid}\" 2>/dev/null || true
"
        done

        if exec_remote_sudo "$H_IP" "$H_USER" "$H_PASS" "$H_BPASS" "$H_KEY" "$CLEAN_CMD"; then
            echo -e "      ${GREEN}✓ Đã làm sạch trắng dữ liệu nodes [${N_IDS}] trên ${H_IP}${NC}"
        else
            echo -e "      ${RED}❌ LỖI: Không thể dọn dẹp dữ liệu cũ tại ${H_IP}! Vui lòng kiểm tra quyền sudo hoặc tiến trình đang bận.${NC}"
            exit 1
        fi
    done
else
    echo -e "\n${YELLOW}[BƯỚC 3/6] Bỏ qua bước dọn dẹp dữ liệu (--skip-clean).${NC}"
fi

# ==============================================================================
# 4. CHUẨN BỊ GÓI ZIP DEPLOY
# ==============================================================================
echo -e "\n${BOLD}[BƯỚC 4/6] Chuẩn bị gói cài đặt metanode deploy kit...${NC}"

# 1. Biên dịch mới binaries nếu có yêu cầu
if [ "$BUILD_BINS" = true ]; then
    echo -e "   🔨 Đang biên dịch 6 binaries mới bằng build_chain_bins.sh..."
    if [ -f "${DEPLOY_DIR}/build_chain_bins.sh" ]; then
        "${DEPLOY_DIR}/build_chain_bins.sh" --all
        echo -e "   ${GREEN}✓ Đã biên dịch xong toàn bộ binaries vào deploy/bin!${NC}"
    else
        echo -e "${RED}❌ Không tìm thấy script build: ${DEPLOY_DIR}/build_chain_bins.sh${NC}"
        exit 1
    fi
fi

# 2. Đóng gói ZIP deploy kit
if [ "$BUILD_ZIP" = true ] || [ -z "$ZIP_FILE" ]; then
    if [ "$BUILD_ZIP" = false ]; then
        # Tìm file zip mới nhất trong thư mục deploy
        LATEST_ZIP=$(ls -t "${DEPLOY_DIR}"/metanode-deploy-*.zip 2>/dev/null | head -n 1 || true)
        if [ -n "$LATEST_ZIP" ] && [ -f "$LATEST_ZIP" ]; then
            ZIP_FILE="$LATEST_ZIP"
            echo -e "   ✓ Tìm thấy gói ZIP có sẵn: ${BOLD}${ZIP_FILE}${NC}"
        fi
    fi

    if [ -z "$ZIP_FILE" ] || [ ! -f "$ZIP_FILE" ] || [ "$BUILD_ZIP" = true ]; then
        echo -e "   📦 Đang tự động đóng gói ZIP mới nhất bằng package_deploy.sh..."
        "${DEPLOY_DIR}/package_deploy.sh"
        ZIP_FILE=$(ls -t "${DEPLOY_DIR}"/metanode-deploy-*.zip | head -n 1)
    fi
fi

if [ ! -f "$ZIP_FILE" ]; then
    echo -e "${RED}❌ Không tìm thấy file ZIP: ${ZIP_FILE}${NC}"
    exit 1
fi

ZIP_BASENAME=$(basename "$ZIP_FILE")
echo -e "   ✓ Gói ZIP sẵn sàng: ${BOLD}${ZIP_FILE}${NC} ($(ls -lh "$ZIP_FILE" | awk '{print $5}'))"

# ==============================================================================
# 5. CHUYỂN FILE SANG MÁY TEST & THỰC THI ANSIBLE DEPLOY
# ==============================================================================
echo -e "\n${BOLD}[BƯỚC 5/6] Chuyển gói sang ${TARGET_USER}@${TARGET_HOST} và bắt đầu Deploy...${NC}"

# Nếu dùng SSH Private Key, đồng bộ key sang máy deployer để Ansible có key kết nối tới các node
if [ -n "$TARGET_KEY" ]; then
    EXP_TARGET_KEY="${TARGET_KEY/#\~/$HOME}"
    if [ -f "$EXP_TARGET_KEY" ]; then
        KEY_NAME=$(basename "$EXP_TARGET_KEY")
        echo -e "   🔑 Đảm bảo SSH Key (${KEY_NAME}) có sẵn trên máy deployer (${TARGET_HOST})..."
        exec_remote "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "mkdir -p ~/.ssh && chmod 700 ~/.ssh"
        exec_scp "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "$EXP_TARGET_KEY" "~/.ssh/${KEY_NAME}"
        exec_remote "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "chmod 600 ~/.ssh/${KEY_NAME}"
    fi
fi

echo -e "   📤 1. Đang tạo thư mục đích trên máy kiểm thử: ${TARGET_DIR}..."
exec_remote "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "mkdir -p ${TARGET_DIR}"

echo -e "   📤 2. Đang gửi gói ZIP: ${ZIP_BASENAME} sang ${TARGET_HOST}..."
exec_scp "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "$ZIP_FILE" "${TARGET_DIR}/${ZIP_BASENAME}"

echo -e "   📤 3. Đang gửi cấu hình: inventory.yml sang ${TARGET_HOST}..."
exec_scp "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "$INVENTORY_FILE" "${TARGET_DIR}/inventory.yml"

echo -e "   📦 4. Đang giải nén và cấu hình trên máy kiểm thử..."
EXTRACT_SCRIPT=$(cat <<EOF
set -e
cd "${TARGET_DIR}"
unzip -o -q "${ZIP_BASENAME}"
mkdir -p "${TARGET_DIR}/deploy/ansible"
cp -f "${TARGET_DIR}/inventory.yml" "${TARGET_DIR}/deploy/ansible/inventory.yml"
cp -f "${TARGET_DIR}/inventory.yml" "${TARGET_DIR}/deploy/inventory.yml"
chmod +x "${TARGET_DIR}/deploy/bin/"* 2>/dev/null || true
chmod +x "${TARGET_DIR}/deploy/ansible/"*.sh 2>/dev/null || true
chmod +x "${TARGET_DIR}/deploy/ansible/monitors/"*.sh 2>/dev/null || true
EOF
)
exec_remote "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "$EXTRACT_SCRIPT"
echo -e "   ${GREEN}✓ Đã giải nén và cập nhật inventory.yml thành công!${NC}"

echo -e "\n   🚀 5. Thực thi lệnh 1: ${BOLD}./ansible_deploy.sh --gen-keys --prebuilt-bin${NC}..."
CMD1="cd ${TARGET_DIR}/deploy/ansible && ./ansible_deploy.sh --gen-keys --prebuilt-bin"
if ! exec_remote "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "$CMD1"; then
    echo -e "\n${RED}❌ [THẤT BẠI] Lệnh sinh khóa (--gen-keys) bị lỗi trên ${TARGET_HOST}!${NC}"
    exit 1
fi
echo -e "   ${GREEN}✓ Sinh khóa và Genesis thành công!${NC}"

echo -e "\n   🚀 6. Thực thi lệnh 2: ${BOLD}./ansible_deploy.sh --start --clean --open-ports --prebuilt-bin${NC}..."
CMD2="cd ${TARGET_DIR}/deploy/ansible && ./ansible_deploy.sh --start --clean --open-ports --prebuilt-bin"
if ! exec_remote "$TARGET_HOST" "$TARGET_USER" "$TARGET_PASS" "$TARGET_KEY" "$CMD2"; then
    echo -e "\n${RED}❌ [THẤT BẠI] Quá trình triển khai cụm node bị lỗi trên ${TARGET_HOST}!${NC}"
    exit 1
fi
echo -e "   ${GREEN}✓ Triển khai cụm node hoàn tất thành công!${NC}"

# ==============================================================================
# 6. GỬI GIAO DỊCH RPC KIỂM CHỨNG CHUỖI
# ==============================================================================
if [ "$SKIP_TX" = false ]; then
    echo -e "\n${BOLD}[BƯỚC 6/6] Thăm dò mạng và gửi giao dịch RPC kiểm chứng...${NC}"
    echo -e "   🔍 Đang kiểm tra RPC Endpoint: ${BOLD}${RPC_URL}${NC}..."

    MAX_RETRIES=20
    RETRY_COUNT=0
    BLOCK_HEX=""

    while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
        RESP=$(curl -s -X POST -H "Content-Type: application/json" \
            --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            --connect-timeout 3 "$RPC_URL" 2>/dev/null || true)
        
        BLOCK_HEX=$(echo "$RESP" | jq -r '.result // empty' 2>/dev/null || true)
        if [ -n "$BLOCK_HEX" ] && [ "$BLOCK_HEX" != "null" ]; then
            BLOCK_DEC=$(( BLOCK_HEX ))
            if [ "$BLOCK_DEC" -ge 1 ]; then
                echo -e "   ${GREEN}✓ Chuỗi đã hoạt động và tạo block! Block hiện tại: #${BLOCK_DEC} (${BLOCK_HEX})${NC}"
                break
            fi
        fi

        RETRY_COUNT=$(( RETRY_COUNT + 1 ))
        echo -e "   ⏳ Đang chờ chain sinh block (Lần $RETRY_COUNT/$MAX_RETRIES, thử lại sau 3s)..."
        sleep 3
    done

    if [ -z "$BLOCK_HEX" ] || [ "$BLOCK_HEX" == "null" ]; then
        echo -e "${YELLOW}⚠️ Cảnh báo: Chưa thể kết nối RPC hoặc chưa có block mới tại ${RPC_URL}.${NC}"
        echo -e "${YELLOW}👉 Tiếp tục thử gửi giao dịch trực tiếp...${NC}"
    fi

    # Kiểm tra thư mục test giao dịch test_tx
    if [ -d "$TEST_RPC_DIR" ] && ([ -f "$TEST_RPC_DIR/main.go" ] || [ -f "$TEST_RPC_DIR/test_rpc" ]); then
        echo -e "\n   📝 Tiến hành gửi giao dịch test qua test-rpc:"
        echo -e "      Thư mục: ${TEST_RPC_DIR}"

        # Tự tạo config-local.json từ config-local.example.json nếu chưa có
        if [ ! -f "$TEST_RPC_DIR/config-local.json" ]; then
            if [ -f "$TEST_RPC_DIR/config-local.example.json" ]; then
                echo -e "   ⚠️ Chưa tìm thấy config-local.json, tự động copy từ config-local.example.json..."
                cp "$TEST_RPC_DIR/config-local.example.json" "$TEST_RPC_DIR/config-local.json"
            fi
        fi

        cd "$TEST_RPC_DIR"
        TEST_CMD_EXEC=""
        if [ -f "./test_rpc" ]; then
            echo -e "      Lệnh:    ${BOLD}./test_rpc -config=config-local.json -data=data.json -url=${RPC_URL}${NC}\n"
            TEST_CMD_EXEC="./test_rpc -config=config-local.json -data=data.json -url=${RPC_URL}"
        else
            echo -e "      Lệnh:    ${BOLD}go run main.go -config=config-local.json -data=data.json -url=${RPC_URL}${NC}\n"
            TEST_CMD_EXEC="go run main.go -config=config-local.json -data=data.json -url=${RPC_URL}"
        fi

        if eval "$TEST_CMD_EXEC"; then
            echo -e "\n${GREEN}==============================================================================${NC}"
            echo -e "${GREEN}🎉 KIỂM THỬ THÀNH CÔNG RỰC RỠ!${NC}"
            echo -e "${GREEN}Giao dịch đã được ghi nhận và smart contract phản hồi chính xác trên chain!${NC}"
            echo -e "${GREEN}==============================================================================${NC}"
        else
            echo -e "\n${RED}❌ Gửi giao dịch RPC thất bại! Vui lòng kiểm tra log node trên máy kiểm thử.${NC}"
            exit 1
        fi
    else
        echo -e "${YELLOW}⚠️ Không tìm thấy thư mục ${TEST_RPC_DIR}. Gửi curl eth_blockNumber kiểm chứng thành công.${NC}"
    fi
else
    echo -e "\n${YELLOW}[BƯỚC 6/6] Bỏ qua bước gửi giao dịch kiểm chứng (--skip-tx).${NC}"
fi

echo -e "\n${GREEN}==============================================================================${NC}"
echo -e "${GREEN}✅ HOÀN TẤT TOÀN BỘ QUY TRÌNH KIỂM THỬ DEPLOY TỰ ĐỘNG!${NC}"
echo -e "${GREEN}==============================================================================${NC}"
