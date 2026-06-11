#!/bin/bash
# ═══════════════════════════════════════════════════════════════════
#  RESTORE SNAPSHOT SYSTEMD — SNAPSHOT RESTORE FOR SYSTEMD SERVICES
#  Chạy dưới quyền root (sudo) để quản lý các systemd service.
# ═══════════════════════════════════════════════════════════════════

set -euo pipefail

NODE_ID=""
SNAPSHOT_URL=""
SNAP_NAME=""
AUTO_YES=false

usage() {
    echo "Usage: sudo bash $0 --node <node_id> --snapshot-url <url> [--snapshot-name <name>] [--yes|-y]"
    echo "Ví dụ: sudo bash $0 --node 2 --snapshot-url http://192.168.1.100:8604 -y"
    exit 1
}

while [[ $# -gt 0 ]]; do
  case $1 in
    --node|-n)
      NODE_ID="$2"
      shift 2
      ;;
    --snapshot-url|-u)
      SNAPSHOT_URL="$2"
      shift 2
      ;;
    --snapshot-name|-s)
      SNAP_NAME="$2"
      shift 2
      ;;
    --yes|-y)
      AUTO_YES=true
      shift 1
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "❌ Cờ không hợp lệ: $1"
      usage
      ;;
  esac
done

if [ -z "$NODE_ID" ] || [ -z "$SNAPSHOT_URL" ]; then
    usage
fi

if [[ ! "$NODE_ID" =~ ^[0-4]$ ]]; then
    echo "❌ node_id phải từ 0-4, nhận được: $NODE_ID"
    exit 1
fi

# Loại bỏ dấu / ở cuối URL nếu có
SNAPSHOT_URL="${SNAPSHOT_URL%/}"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Quyền Root
if [ "${EUID:-0}" -ne 0 ]; then
    echo -e "${RED}❌ Lệnh này cần chạy dưới quyền root (sudo). Chạy lại bằng:${NC}"
    echo "sudo bash $0 --node $NODE_ID --snapshot-url $SNAPSHOT_URL"
    exit 1
fi

# ─── Đọc cấu hình Node đích ──────────────────────────────────────
INSTALL_DIR="/opt/metanode/node-${NODE_ID}"

if [ ! -d "$INSTALL_DIR" ]; then
    echo -e "${RED}❌ Không tìm thấy thư mục cài đặt Node $NODE_ID: $INSTALL_DIR${NC}"
    exit 1
fi

METANODE_USER=$(stat -c '%U' "$INSTALL_DIR" 2>/dev/null || echo "abc")

# ─── Cấu hình URL Tải Snapshot ─────────────────────────────────
SNAP_API="${SNAPSHOT_URL}/api/snapshots"
SNAP_FILES_URL="${SNAPSHOT_URL}/files"

# Service names
svc_exec="metanode-execution-${NODE_ID}"
svc_cons="metanode-consensus-${NODE_ID}"
svc_rpc="metanode-rpc-${NODE_ID}"

# Helper get rpc port
get_node_rpc_port() {
    local nid="$1"
    local cfg="/opt/metanode/node-${nid}/config/execution.json"
    if [ -f "$cfg" ]; then
        local port=$(jq -r '.rpc_port // empty' "$cfg" 2>/dev/null | tr -d ':')
        if [ -n "$port" ]; then
            echo "$port"
            return 0
        fi
    fi
    # Default fallback
    if [ "$nid" -eq 0 ]; then echo "8757"; return 0; fi
    echo "$((10746 + nid))"
}

find_reference_node() {
    for i in 0 1 2 3 4; do
        [ "$i" -eq "$NODE_ID" ] && continue
        local port=$(get_node_rpc_port "$i")
        local resp=$(curl -sf -m 1 -X POST -H "Content-Type: application/json" \
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
    return 0
}

# ─── Bắt đầu tiến trình khôi phục ──────────────────────────────────
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  📸 RESTORE Node $NODE_ID từ Snapshot (Systemd Mode)${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo ""
echo -e "${BLUE}📡 Nguồn Snapshot:${NC} $SNAPSHOT_URL"

# 1. Tìm snapshot
if [ -n "$SNAP_NAME" ]; then
    echo -e "${BLUE}📸 Sử dụng snapshot chỉ định:${NC} $SNAP_NAME"
else
    echo -e "${BLUE}🔍 Tự động tìm snapshot mới nhất qua API (đợi tối đa 120s)...${NC}"
    for ((attempt=1; attempt<=30; attempt++)); do
        SNAP_JSON=$(curl -sf -m 5 "$SNAP_API" 2>/dev/null || echo "")
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
        echo -e "${RED}❌ Không lấy được danh sách snapshot từ API sau 120s!${NC}"
        echo "   Vui lòng kiểm tra lại URL: $SNAP_API"
        exit 1
    fi
    echo -e "${GREEN}  ✅ Tìm thấy snapshot mới nhất:${NC} $SNAP_NAME"
fi

DOWNLOAD_URL="${SNAP_FILES_URL}/${SNAP_NAME}/"
echo -e "${BLUE}  📥 Sẽ tải dữ liệu từ:${NC} $DOWNLOAD_URL"

# Cảnh báo thao tác khôi phục snapshot
echo ""
echo -e "${YELLOW}⚠️  CẢNH BÁO:${NC}"
echo "   1. Dừng các service systemd của Node $NODE_ID"
echo "   2. XÓA TOÀN BỘ dữ liệu blockchain hiện tại của Node $NODE_ID"
echo "   3. Khôi phục từ snapshot: $SNAP_NAME"
echo "   4. Khởi động lại các service của Node $NODE_ID"
echo ""
echo "🚀 Tự động chạy khôi phục..."


START_TIME=$(date +%s)

# Step 1: Dừng các service
echo ""
echo -e "${BLUE}[1/7] 🛑 Dừng các service systemd của Node $NODE_ID...${NC}"
systemctl stop "$svc_cons" 2>/dev/null || true
systemctl stop "$svc_exec" 2>/dev/null || true
echo -e "${GREEN}  ✅ Đã dừng: $svc_exec, $svc_cons${NC}"

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
mkdir -p "${INSTALL_DIR}/data/execution/db/history"
mkdir -p "${INSTALL_DIR}/data/execution/db/consensus"
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

TAR_URL="${SNAP_FILES_URL}/${SNAP_NAME}.tar"
TAR_FILE="$TEMP_SNAP/${SNAP_NAME}.tar"

echo -e "   🔄 Thử tải file nén (.tar) trước cho nhanh..."
MAX_WAIT=12
WAIT_COUNT=0
HTTP_CODE="000"

while [ $WAIT_COUNT -lt $MAX_WAIT ]; do
    HTTP_CODE=$(curl -s -I -o /dev/null -w "%{http_code}" "$TAR_URL" 2>/dev/null || echo "000")
    if [ "$HTTP_CODE" = "200" ]; then
        break
    fi
    echo -e "${YELLOW}      ⏳ Chưa thấy file .tar (Server có thể đang nén). Chờ 5s... ($((WAIT_COUNT+1))/$MAX_WAIT)${NC}"
    sleep 5
    WAIT_COUNT=$((WAIT_COUNT + 1))
done

if [ "$HTTP_CODE" = "200" ]; then
    echo -e "${GREEN}  ✅ Tìm thấy file Tarball trên server. Bắt đầu tải...${NC}"
    wget -c -q --show-progress --progress=bar:force:noscroll "$TAR_URL" -O "$TAR_FILE" || {
        echo -e "${RED}  ❌ Tải Tarball thất bại!${NC}"
        rm -f "$TAR_FILE"
        exit 1
    }
    echo -e "${CYAN}  📦 Đang giải nén Tarball trực tiếp...${NC}"
    
    tar -Sxf "$TAR_FILE" -C "$TEMP_SNAP" 2>/dev/null || {
        echo -e "${YELLOW}  ⚠️ Giải nén lỗi, thử giải nén ra thư mục tạm...${NC}"
        mkdir -p "/tmp/extract_$$"
        tar -Sxf "$TAR_FILE" -C "/tmp/extract_$$"
        mv "/tmp/extract_$$/$SNAP_NAME" "$TEMP_SNAP/"
        rm -rf "/tmp/extract_$$"
    }
    rm -f "$TAR_FILE"
else
    echo -e "${YELLOW}  ⚠️ Không tìm thấy file .tar (hoặc chưa tạo xong). Chuyển sang tải đệ quy từng folder...${NC}"
    wget -c -r -np -nH --cut-dirs=2 -q --show-progress --progress=bar:force:noscroll \
        --reject="index.html*" \
        "$DOWNLOAD_URL" \
        -P "$TEMP_SNAP" || {
        echo -e "${RED}❌ Tải snapshot thất bại! Vui lòng kiểm tra lại đường dẫn: $DOWNLOAD_URL${NC}"
        rm -rf "$TEMP_SNAP"
        exit 1
    }
fi

# Xác định thư mục snapshot thực tế tải về
# Do wget --cut-dirs=2, nếu url là http://ip:port/files/snap_name/
# thì thư mục tải về có thể là snap_name nằm trong TEMP_SNAP
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

# 5b. Copy các thư mục LevelDB/Nomt còn lại vào db với đúng mapping history/consensus
for item in "$SNAP_SRC_DIR"/*; do
    name=$(basename "$item")
    [ "$name" = "back_up" ] && continue
    [ "$name" = "db" ] && continue
    [ "$name" = "metadata.json" ] && continue
    [ "$name" = "index.html" ] && continue
    
    if [ -d "$item" ]; then
        if [ "$name" = "blocks" ] || [ "$name" = "receipts" ] || [ "$name" = "transaction_state" ] || [ "$name" = "mapping" ] || [ "$name" = "changelog_db_account" ] || [ "$name" = "changelog_db_stake" ]; then
            echo -e "    📦 Khôi phục history database: ${name} -> history/${name}..."
            cp -a "$item" "${INSTALL_DIR}/data/execution/db/history/"
        elif [ "$name" = "history" ]; then
            echo -e "    📦 Khôi phục history directory directly..."
            cp -a "$item"/* "${INSTALL_DIR}/data/execution/db/history/" 2>/dev/null || true
        elif [ "$name" = "nomt_db" ] || [ "$name" = "smart_contract_code" ] || [ "$name" = "smart_contract_storage" ] || [ "$name" = "backup_device_key_storage" ] || [ "$name" = "xapian" ] || [ "$name" = "account_state" ] || [ "$name" = "stake_db" ] || [ "$name" = "trie_database" ] || [ "$name" = "backup_db" ]; then
            echo -e "    📦 Khôi phục consensus database: ${name} -> consensus/${name}..."
            cp -a "$item" "${INSTALL_DIR}/data/execution/db/consensus/"
        elif [ "$name" = "other" ]; then
            echo -e "    📦 Khôi phục explorer database: other -> other..."
            cp -a "$item" "${INSTALL_DIR}/data/execution/db/"
        else
            echo -e "    📦 Khôi phục other data folder: ${name} -> db/${name}..."
            cp -a "$item" "${INSTALL_DIR}/data/execution/db/"
        fi
    fi
done

# 🚨 CRITICAL: Xóa thư mục rust_consensus từ snapshot (nếu có) để tránh split-brain
rm -rf "${INSTALL_DIR}/data/execution/db/consensus/rust_consensus" 2>/dev/null || true
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
echo -e "${BLUE}[6/7] 📊 Giám sát trạng thái sync (tối đa 120s)...${NC}"
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
    else
        echo ""
    fi
    return 0
}

for t in 5 10 15 20 25 30 35 40 45 50 55 60 65 70 75 80 85 90 95 100 105 110 115 120; do
    sleep 5
    
    # Lấy thông tin block của node vừa khôi phục
    MY_INFO=$(get_block_height "$MY_PORT")
    MY_BLOCK=$(echo "$MY_INFO" | awk '{print $1}')
    MY_GEI=$(echo "$MY_INFO" | awk '{print $2}')
    MY_EPOCH=$(echo "$MY_INFO" | awk '{print $3}')
    
    if [ -n "$MY_BLOCK" ]; then
        DISP="block=$MY_BLOCK, GEI=${MY_GEI:-?}, epoch=${MY_EPOCH:-?}"
        echo -e "  ${GREEN}⏱️ +${t}s: $DISP — ✅ RPC Node $NODE_ID đã sẵn sàng và online${NC}"
        SYNCED=true
        break
    fi
    echo -e "  ${YELLOW}⏱️ +${t}s: Node chưa phản hồi RPC (đang khởi động)...${NC}"
done

if [ "$SYNCED" = true ]; then
    echo -e "  ${GREEN}✅ Node $NODE_ID đã khởi động thành công và online!${NC}"
else
    echo -e "  ${RED}❌ Node $NODE_ID khởi động thất bại hoặc RPC không phản hồi sau 120s.${NC}"
    echo -e "${YELLOW}🔍 --- TRÍCH XUẤT LOG LỖI SYSTEMD (Execution & Consensus) ---${NC}"
    echo -e "${CYAN}=== metanode-execution-${NODE_ID} logs (last 50 lines) ===${NC}"
    journalctl -u "$svc_exec" -n 50 --no-pager || true
    echo -e "${CYAN}=== metanode-consensus-${NODE_ID} logs (last 50 lines) ===${NC}"
    journalctl -u "$svc_cons" -n 50 --no-pager || true
    echo -e "${YELLOW}🔍 ---------------------------------------------------------${NC}"
    exit 1
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
