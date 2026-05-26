#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  RESTORE NODE SYSTEMD — SNAPSHOT RESTORE FOR SYSTEMD SERVICES
#  Chạy dưới quyền root (sudo) để quản lý các systemd service.
#  Usage: sudo bash restore_node_systemd.sh <node_id> [snapshot_name] [source_node_id]
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

NODE_ID="${1:?❌ Usage: sudo bash $0 <node_id> [snapshot_name] [source_node_id]}"

if [[ ! "$NODE_ID" =~ ^[0-4]$ ]]; then
    echo "❌ node_id phải từ 0-4, nhận được: $NODE_ID"
    exit 1
fi

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Quyền Root
if [ "${EUID:-0}" -ne 0 ]; then
    echo -e "${RED}❌ Lệnh này cần chạy dưới quyền root (sudo). Chạy lại bằng:${NC}"
    echo "sudo bash $0 $*"
    exit 1
fi

# ─── Đọc cấu hình Node đích ──────────────────────────────────────
ENV_FILE=""
if [ "$NODE_ID" -eq 4 ]; then
    ENV_FILE="$SCRIPT_DIR/node-4_keys/synconly.env"
else
    ENV_FILE="$SCRIPT_DIR/node-${NODE_ID}_keys/validator.env"
fi

if [ ! -f "$ENV_FILE" ]; then
    echo -e "${RED}❌ Không tìm thấy file cấu hình: $ENV_FILE${NC}"
    exit 1
fi

INSTALL_DIR=$(grep "^INSTALL_DIR=" "$ENV_FILE" | cut -d'=' -f2 | tr -d '"' || echo "")
[ -z "$INSTALL_DIR" ] && INSTALL_DIR="/opt/metanode/node-${NODE_ID}"

METANODE_USER=$(grep "^METANODE_USER=" "$ENV_FILE" | cut -d'=' -f2 | tr -d '"' || echo "")
[ -z "$METANODE_USER" ] && METANODE_USER="metanode"

# ─── Cấu hình Node nguồn snapshot ─────────────────────────────────
SOURCE_NODE="${3:-0}"
if [[ ! "$SOURCE_NODE" =~ ^[0-4]$ ]]; then
    echo "❌ source_node_id phải từ 0-4, nhận được: $SOURCE_NODE"
    exit 1
fi

SRC_ENV_FILE=""
if [ "$SOURCE_NODE" -eq 4 ]; then
    SRC_ENV_FILE="$SCRIPT_DIR/node-4_keys/synconly.env"
else
    SRC_ENV_FILE="$SCRIPT_DIR/node-${SOURCE_NODE}_keys/validator.env"
fi

# Đọc SNAPSHOT_SERVER_PORT từ node nguồn
SNAP_PORT=""
if [ -f "$SRC_ENV_FILE" ]; then
    SNAP_PORT=$(grep "^SNAPSHOT_SERVER_PORT=" "$SRC_ENV_FILE" | cut -d'=' -f2 | tr -d '"' || echo "")
fi
[ -z "$SNAP_PORT" ] && SNAP_PORT=$((8600 + SOURCE_NODE))

SNAP_SERVER="http://localhost:${SNAP_PORT}"
SNAP_API="${SNAP_SERVER}/api/snapshots"
SNAP_FILES_URL="${SNAP_SERVER}/files"

# Service names
svc_exec="metanode-execution-${NODE_ID}"
svc_cons="metanode-consensus-${NODE_ID}"
svc_rpc="metanode-rpc-${NODE_ID}"

# Helper get rpc port
get_node_rpc_port() {
    local nid="$1"
    local cfg=""
    if [ "$nid" -eq 4 ]; then
        cfg="$SCRIPT_DIR/node-4_keys/synconly.env"
    else
        cfg="$SCRIPT_DIR/node-${nid}_keys/validator.env"
    fi
    if [ -f "$cfg" ]; then
        local port=$(grep "^RPC_PORT=" "$cfg" 2>/dev/null | cut -d'=' -f2 | tr -d ':' | tr -d '"' || true)
        if [ -n "$port" ]; then
            echo "$port"
            return 0
        fi
    fi
    echo "$((10746 + nid))"
}

find_reference_node() {
    for i in 0 1 2 3 4; do
        [ "$i" -eq "$NODE_ID" ] && continue
        local port=$(get_node_rpc_port "$i")
        local resp=$(curl -sf -X POST -H "Content-Type: application/json" \
            --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
            "http://127.0.0.1:$port" 2>/dev/null || echo "")
        if [ -n "$resp" ]; then
            local hex_block=$(echo "$resp" | jq -r '.result // empty' 2>/dev/null || echo "")
            if [ -n "$hex_block" ] && [ "$hex_block" != "null" ]; then
                echo "$i"
                return 0
            fi
        fi
    done
    echo ""
    return 1
}

# ─── Bắt đầu tiến trình khôi phục ──────────────────────────────────
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  📸 RESTORE Node $NODE_ID từ Snapshot (Systemd Mode)${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo ""
echo -e "${BLUE}📡 Snapshot Server:${NC} $SNAP_SERVER"

# 1. Tìm snapshot
if [ -n "${2:-}" ]; then
    SNAP_NAME="$2"
    echo -e "${BLUE}📸 Sử dụng snapshot chỉ định:${NC} $SNAP_NAME"
else
    echo -e "${BLUE}🔍 Tự động tìm snapshot mới nhất (đợi tối đa 120s)...${NC}"
    SNAP_NAME=""
    for ((attempt=1; attempt<=30; attempt++)); do
        SNAP_JSON=$(curl -sf "$SNAP_API" 2>/dev/null || echo "")
        if [ -n "$SNAP_JSON" ] && [ "$SNAP_JSON" != "[]" ]; then
            SNAP_NAME=$(echo "$SNAP_JSON" | jq -r 'max_by(.block_number) | .snapshot_name' 2>/dev/null || echo "")
            if [ -n "$SNAP_NAME" ] && [ "$SNAP_NAME" != "null" ]; then
                break
            fi
        fi
        echo -e "   ⏳ Chưa tìm thấy snapshot từ API, đang đợi... (lần thử $attempt/30)"
        sleep 4
    done
    
    if [ -z "$SNAP_NAME" ] || [ "$SNAP_NAME" = "null" ]; then
        echo -e "${RED}❌ Không lấy được snapshot từ API sau 120s!${NC}"
        echo "   Vui lòng kiểm tra xem node nguồn $SOURCE_NODE đang chạy và đã tạo snapshot chưa."
        exit 1
    fi
    echo -e "${GREEN}  ✅ Tìm thấy snapshot mới nhất:${NC} $SNAP_NAME"
fi

DOWNLOAD_URL="${SNAP_FILES_URL}/${SNAP_NAME}/"
echo -e "${BLUE}  📥 Tải từ:${NC} $DOWNLOAD_URL"

# Xác nhận người dùng
echo ""
echo -e "${YELLOW}⚠️  CẢNH BÁO:${NC}"
echo "   1. Dừng các service systemd của Node $NODE_ID"
echo "   2. XÓA TOÀN BỘ dữ liệu blockchain hiện tại của Node $NODE_ID"
echo "   3. Khôi phục từ snapshot: $SNAP_NAME"
echo "   4. Khởi động lại các service của Node $NODE_ID"
echo ""
read -p "   Bạn có chắc chắn muốn tiếp tục? (y/N): " CONFIRM
if [[ "$CONFIRM" != "y" && "$CONFIRM" != "Y" ]]; then
    echo "Đã hủy thao tác."
    exit 0
fi

START_TIME=$(date +%s)

# Step 1: Dừng các service
echo ""
echo -e "${BLUE}[1/7] 🛑 Dừng các service systemd của Node $NODE_ID...${NC}"
systemctl stop "$svc_rpc" 2>/dev/null || true
systemctl stop "$svc_cons" 2>/dev/null || true
systemctl stop "$svc_exec" 2>/dev/null || true
echo -e "${GREEN}  ✅ Đã dừng: $svc_exec, $svc_cons, $svc_rpc${NC}"

# Step 2: Xóa dữ liệu cũ
echo -e "${BLUE}[2/7] 🗑️  Xóa dữ liệu cũ của Node $NODE_ID...${NC}"
rm -rf "${INSTALL_DIR}/data/execution/db" 2>/dev/null || true
rm -rf "${INSTALL_DIR}/data/execution/backup" 2>/dev/null || true
rm -rf "${INSTALL_DIR}/data/consensus" 2>/dev/null || true
# Xóa logs cũ để dễ theo dõi log sync mới
rm -f "${INSTALL_DIR}/logs/execution/execution.log" 2>/dev/null || true
rm -f "${INSTALL_DIR}/logs/consensus/consensus.log" 2>/dev/null || true
echo -e "${GREEN}  ✅ Đã xóa sạch dữ liệu cũ tại ${INSTALL_DIR}/data/${NC}"

# Step 3: Tạo lại cấu trúc thư mục rỗng
mkdir -p "${INSTALL_DIR}/data/execution/db"
mkdir -p "${INSTALL_DIR}/data/execution/backup"
mkdir -p "${INSTALL_DIR}/data/consensus"
mkdir -p "${INSTALL_DIR}/logs/execution"
mkdir -p "${INSTALL_DIR}/logs/consensus"

# Step 4: Tải xuống snapshot
echo -e "${BLUE}[3/7] 📥 Tải xuống snapshot qua HTTP...${NC}"
TEMP_SNAP="/tmp/snapshot_restore_${NODE_ID}_$$"
mkdir -p "$TEMP_SNAP"

if ! command -v wget &>/dev/null; then
    echo -e "${RED}❌ Hệ thống thiếu công cụ 'wget'. Vui lòng cài đặt: apt install wget${NC}"
    rm -rf "$TEMP_SNAP"
    exit 1
fi

wget -c -r -np -nH --cut-dirs=2 -q --show-progress \
    --reject="index.html*" \
    "$DOWNLOAD_URL" \
    -P "$TEMP_SNAP" || {
    echo -e "${RED}❌ Tải snapshot thất bại!${NC}"
    rm -rf "$TEMP_SNAP"
    exit 1
}

# Xác định thư mục snapshot thực tế tải về
SNAP_SRC_DIR="$TEMP_SNAP/$SNAP_NAME"
if [ ! -d "$SNAP_SRC_DIR" ]; then
    SNAP_SRC_DIR="$TEMP_SNAP"
fi

# Step 5: Khôi phục cấu trúc file dữ liệu
echo -e "${BLUE}[4/7] 📂 Ánh xạ dữ liệu snapshot vào thư mục Node $NODE_ID...${NC}"

# 5a. Copy PebbleDB (back_up) sang thư mục backup
if [ -d "$SNAP_SRC_DIR/back_up" ]; then
    cp -a "$SNAP_SRC_DIR/back_up"/* "${INSTALL_DIR}/data/execution/backup/" 2>/dev/null || true
    echo -e "${GREEN}  ✅ Đã khôi phục thư mục backup (PebbleDB)${NC}"
else
    echo -e "${YELLOW}  ⚠️ Không tìm thấy thư mục 'back_up' trong snapshot${NC}"
fi

# 5b. Copy các thư mục LevelDB/Nomt còn lại vào db
for item in "$SNAP_SRC_DIR"/*; do
    name=$(basename "$item")
    [ "$name" = "back_up" ] && continue
    [ "$name" = "metadata.json" ] && continue
    [ "$name" = "index.html" ] && continue
    
    if [ -d "$item" ]; then
        cp -a "$item" "${INSTALL_DIR}/data/execution/db/"
        echo -e "${GREEN}    + Khôi phục: ${name}/${NC}"
    fi
done

# 🚨 CRITICAL: Xóa thư mục rust_consensus từ snapshot (nếu có) để tránh split-brain
rm -rf "${INSTALL_DIR}/data/execution/db/rust_consensus" 2>/dev/null || true
echo -e "${GREEN}  ✅ Đã xóa rust_consensus cũ để ép node chạy Bootstrapping sạch${NC}"

# 5c. Khôi phục metadata.json
if [ -f "$SNAP_SRC_DIR/metadata.json" ]; then
    cp -a "$SNAP_SRC_DIR/metadata.json" "${INSTALL_DIR}/data/execution/db/metadata.json" 2>/dev/null || true
    cp -a "$SNAP_SRC_DIR/metadata.json" "${INSTALL_DIR}/metadata.json" 2>/dev/null || true
    echo -e "${GREEN}  ✅ Đã khôi phục metadata.json${NC}"
fi

# 5d. Dọn dẹp LOCK files
find "${INSTALL_DIR}/data/execution/db" -name "LOCK" -delete 2>/dev/null || true
find "${INSTALL_DIR}/data/execution/db" -name ".lock" -path "*/nomt_db/*" -delete 2>/dev/null || true
echo -e "${GREEN}  ✅ Đã dọn dẹp các file LOCK còn sót lại${NC}"

# 5e. Phân quyền sở hữu cho người dùng metanode
echo -e "${BLUE}  🔑 Cập nhật quyền sở hữu cho user '${METANODE_USER}'...${NC}"
chown -R "${METANODE_USER}:${METANODE_USER}" "${INSTALL_DIR}/data/"
chown -R "${METANODE_USER}:${METANODE_USER}" "${INSTALL_DIR}/logs/"
chown "${METANODE_USER}:${METANODE_USER}" "${INSTALL_DIR}/metadata.json" 2>/dev/null || true

# Dọn dẹp thư mục tạm
rm -rf "$TEMP_SNAP"
echo -e "${GREEN}  ✅ Hoàn tất ánh xạ và phân quyền dữ liệu${NC}"

# Step 6: Khởi động tuần tự
echo -e "${BLUE}[5/7] 🚀 Khởi động các service systemd của Node $NODE_ID...${NC}"

echo -e "${CYAN}  [5a] Khởi động Execution Layer (Go)...${NC}"
systemctl start "$svc_exec"

echo -e "${CYAN}  [5b] Chờ Go nhận dữ liệu và mở database (10s)...${NC}"
sleep 10

GO_LOG="${INSTALL_DIR}/logs/execution/execution.log"
GO_BLOCK=$(grep -a "last_committed_block=" "$GO_LOG" 2>/dev/null | tail -1 | sed -n 's/.*last_committed_block=\([0-9]*\).*/\1/p') || true
if [ -n "$GO_BLOCK" ]; then
    echo -e "${GREEN}    ✅ Go Node đã khởi động thành công — block hiện tại=$GO_BLOCK${NC}"
else
    echo -e "${YELLOW}    ⚠️ Không tìm thấy log block của Go. Kiểm tra logs: journalctl -u $svc_exec -n 50${NC}"
fi

echo -e "${CYAN}  [5c] Khởi động Consensus Layer (Rust)...${NC}"
systemctl start "$svc_cons"

# Khởi động lại RPC Proxy nếu có
if systemctl list-units --full --all 2>/dev/null | grep -q "${svc_rpc}.service"; then
    echo -e "${CYAN}  [5d] Khởi động RPC Proxy...${NC}"
    systemctl start "$svc_rpc"
fi

echo -e "${GREEN}  ✅ Các service đã được khởi động tuần tự${NC}"

# Step 7: Giám sát Block Sync
echo ""
echo -e "${BLUE}[6/7] 📊 Giám sát trạng thái sync (so sánh với node tham chiếu, tối đa 120s)...${NC}"
PREV_BLOCK=""
SYNCED=false
MY_PORT=$(get_node_rpc_port "$NODE_ID")

get_block_height() {
    local port=$1
    local resp=$(curl -sf -m 2 -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest",false],"id":1}' \
        "http://127.0.0.1:$port" 2>/dev/null || echo "")
    if [ -n "$resp" ]; then
        echo "$resp" | python3 -c "
import sys,json
try:
    r=json.load(sys.stdin).get('result',None)
    if r:
        bn=int(r.get('number','0x0'),16)
        gei=int(r.get('globalExecIndex','0x0'),16)
        ep=int(r.get('epoch','0x0'),16)
        print(f'{bn} {gei} {ep}')
    else:
        print('')
except:
    print('')
" 2>/dev/null || echo ""
    fi
}

for t in 10 20 30 40 50 60 70 80 90 100 110 120; do
    sleep 10
    
    # Lấy thông tin block của node vừa khôi phục
    MY_INFO=$(get_block_height "$MY_PORT")
    MY_BLOCK=$(echo "$MY_INFO" | awk '{print $1}')
    MY_GEI=$(echo "$MY_INFO" | awk '{print $2}')
    MY_EPOCH=$(echo "$MY_INFO" | awk '{print $3}')
    
    if [ -z "$MY_BLOCK" ]; then
        echo -e "  ${YELLOW}⏱️ +${t}s: Node chưa phản hồi RPC (đang khởi động)...${NC}"
        continue
    fi
    
    # Lấy thông tin từ node tham chiếu đang chạy ổn định
    REF_NODE=$(find_reference_node)
    REF_BLOCK=""
    REF_GEI=""
    if [ -n "$REF_NODE" ]; then
        REF_PORT=$(get_node_rpc_port "$REF_NODE")
        REF_INFO=$(get_block_height "$REF_PORT")
        REF_BLOCK=$(echo "$REF_INFO" | awk '{print $1}')
        REF_GEI=$(echo "$REF_INFO" | awk '{print $2}')
    fi
    
    DISP="block=$MY_BLOCK, GEI=${MY_GEI:-?}, epoch=${MY_EPOCH:-?}"
    
    if [ -n "$REF_BLOCK" ] && [ "$MY_BLOCK" -ge "$REF_BLOCK" ] 2>/dev/null; then
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ ĐÃ ĐỒNG BỘ (Node tham chiếu $REF_NODE: block=$REF_BLOCK)${NC}"
        SYNCED=true
        break
    elif [ -n "$PREV_BLOCK" ] && [ "$MY_BLOCK" -gt "$PREV_BLOCK" ] 2>/dev/null; then
        JUMP=$((MY_BLOCK - PREV_BLOCK))
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — 🚀 Tăng +$JUMP blocks (Node tham chiếu: ${REF_BLOCK:-?})${NC}"
    elif [ -n "$PREV_BLOCK" ] && [ "$MY_BLOCK" -eq "$PREV_BLOCK" ] 2>/dev/null; then
        if [ -n "$REF_BLOCK" ] && [ "$MY_BLOCK" -lt "$REF_BLOCK" ] 2>/dev/null; then
            BEHIND=$((REF_BLOCK - MY_BLOCK))
            echo -e "  ${YELLOW}⏱️ +${t}s: $DISP — ⏳ Đang đồng bộ tiếp ($BEHIND blocks phía sau node $REF_NODE)${NC}"
        else
            echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ Đã bắt kịp mạng (Idle)${NC}"
            SYNCED=true
            break
        fi
    else
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — 🚀 Đang đồng bộ...${NC}"
    fi
    PREV_BLOCK="$MY_BLOCK"
done

if [ "$SYNCED" = true ]; then
    echo -e "  ${GREEN}✅ Node $NODE_ID đã đồng bộ thành công!${NC}"
else
    echo -e "  ${YELLOW}⚠️ Hết 120s theo dõi. Vui lòng chạy lệnh 'journalctl -u $svc_exec -f' để theo dõi thủ công.${NC}"
fi

# Step 8: Kiểm tra băm Divergence (Fork Check)
echo ""
echo -e "${BLUE}[7/7] 🔒 Kiểm tra khớp mã băm (Block Hash Verification)...${NC}"
MY_BLOCK_HEX=$(printf "0x%x" "${MY_BLOCK:-0}")

if [ "${MY_BLOCK:-0}" -gt 0 ]; then
    REF_NODE=$(find_reference_node)
    if [ -n "$REF_NODE" ]; then
        REF_PORT=$(get_node_rpc_port "$REF_NODE")
        
        HASH_MY=$(curl -sf -X POST -H "Content-Type: application/json" \
            --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$MY_BLOCK_HEX\",false],\"id\":1}" \
            "http://127.0.0.1:$MY_PORT" 2>/dev/null \
            | jq -r '.result.hash // empty' 2>/dev/null || echo "")
            
        HASH_REF=$(curl -sf -X POST -H "Content-Type: application/json" \
            --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$MY_BLOCK_HEX\",false],\"id\":1}" \
            "http://127.0.0.1:$REF_PORT" 2>/dev/null \
            | jq -r '.result.hash // empty' 2>/dev/null || echo "")
            
        if [ -n "$HASH_MY" ] && [ -n "$HASH_REF" ]; then
            if [ "$HASH_MY" = "$HASH_REF" ]; then
                echo -e "${GREEN}  ✅ Block $MY_BLOCK hash KHỚP giữa Node $NODE_ID và Node tham chiếu $REF_NODE${NC}"
                echo "     Hash: $HASH_MY"
            else
                echo -e "${RED}  🚨 CẢNH BÁO: PHÁT HIỆN LỆCH HASH (FORK DETECTED) ở Block $MY_BLOCK!${NC}"
                echo "     - Node $NODE_ID Hash: $HASH_MY"
                echo "     - Node $REF_NODE Hash: $HASH_REF"
            fi
        else
            echo -e "${YELLOW}  ⚠️ Không thể lấy băm block để so sánh.${NC}"
        fi
    fi
fi

ELAPSED=$(( $(date +%s) - START_TIME ))
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ✅ KHÔI PHỤC SNAPSHOT HOÀN TẤT TRONG ${ELAPSED}s${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo ""
echo -e "👉 Kiểm tra log trực tiếp bằng:"
echo "   - execution: journalctl -u $svc_exec -f"
echo "   - consensus: journalctl -u $svc_cons -f"
echo ""
