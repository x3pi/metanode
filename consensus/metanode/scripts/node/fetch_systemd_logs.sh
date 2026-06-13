#!/bin/bash
# ============================================================================
# Fetch Systemd Logs Script 
# Kéo file log từ các node chạy bằng systemd (deploy_systemd_cluster.sh)
# ============================================================================

set -uo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

SSH_OPTS="${SSH_OPTS:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${1:-$SCRIPT_DIR/deploy.env}"

FETCH_RPC=false
ENV_FILE_SET=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --env)
            if [ -z "${2:-}" ]; then
                echo -e "${YELLOW}Thiếu đường dẫn file env sau tham số --env${NC}"
                exit 1
            fi
            ENV_FILE="$2"
            ENV_FILE_SET=1
            shift 2
            ;;
        --rpc)
            FETCH_RPC=true
            shift
            ;;
        *)
            if [[ "$1" != --* ]] && [ "$ENV_FILE_SET" -eq 0 ]; then
                ENV_FILE="$1"
                ENV_FILE_SET=1
            fi
            shift
            ;;
    esac
done

if [[ "$ENV_FILE" != /* ]]; then
    ENV_FILE="$SCRIPT_DIR/$ENV_FILE"
fi

if [ ! -f "$ENV_FILE" ]; then
    echo -e "${YELLOW}Không tìm thấy file config: $ENV_FILE${NC}"
    echo "Usage: ./fetch_systemd_logs.sh --env <env-file>"
    exit 1
fi

source "$ENV_FILE"

# Tạo thư mục chứa log
# Luôn lưu log tương đối theo vị trí script hiện tại thay vì dùng LOCAL_METANODE bị hardcode
LOCAL_SYSTEMD_LOGS_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)/logs_systemd"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
RUN_LOGS_DIR="$LOCAL_SYSTEMD_LOGS_DIR/run_$TIMESTAMP"
mkdir -p "$RUN_LOGS_DIR"

echo -e "${CYAN}📋 Using config: $ENV_FILE${NC}"

if [ "${SSH_AUTH:-key}" == "password" ] && ! command -v sshpass &> /dev/null; then
    echo -e "${YELLOW}❌ Lỗi: Bạn đang cấu hình SSH_AUTH=\"password\" nhưng chưa cài 'sshpass'.${NC}"
    echo "Hãy chạy: sudo apt install sshpass"
    exit 1
fi

echo -e "${GREEN}📥 Bắt đầu lấy systemd log từ các server về: $RUN_LOGS_DIR${NC}"

get_unique_servers() {
    local servers=""
    for ip in "${NODE_SERVER[@]}"; do
        if [[ ! " $servers " =~ " $ip " ]] && [ -n "$ip" ]; then
            servers="$servers $ip"
        fi
    done
    echo "$servers"
}

ssh_cmd() {
    local host="$1"; shift
    if [ "${SSH_AUTH:-key}" == "password" ]; then
        sshpass -p "$SSH_PASSWORD" ssh $SSH_OPTS "${SSH_USER}@${host}" "$@"
    elif [ -n "${SSH_KEY:-}" ]; then
        ssh $SSH_OPTS -i "$SSH_KEY" "${SSH_USER}@${host}" "$@"
    else
        ssh $SSH_OPTS "${SSH_USER}@${host}" "$@"
    fi
}

scp_cmd() {
    local host="$1"; local src="$2"; local dst="$3"
    if [ "${SSH_AUTH:-key}" == "password" ]; then
        sshpass -p "$SSH_PASSWORD" scp $SSH_OPTS "${SSH_USER}@${host}:${src}" "${dst}"
    elif [ -n "${SSH_KEY:-}" ]; then
        scp $SSH_OPTS -i "$SSH_KEY" "${SSH_USER}@${host}:${src}" "${dst}"
    else
        scp $SSH_OPTS "${SSH_USER}@${host}:${src}" "${dst}"
    fi
}

SERVERS=$(get_unique_servers)

for server in $SERVERS; do
    echo -e "\n${CYAN}🌐 Đang lấy log từ server: $server...${NC}"
    
    nodes=""
    for node_id in "${!NODE_SERVER[@]}"; do
        if [ "${NODE_SERVER[$node_id]}" == "$server" ]; then
            nodes="$nodes $node_id"
        fi
    done

    for id in $nodes; do
        echo "   ▶ Node $id"
        
        # Kéo toàn bộ thư mục logs vật lý của Node
        NODE_LOGS_DIR="/opt/metanode/node-${id}/logs"
        TARGET_DIR="$RUN_LOGS_DIR/node_${id}_logs"
        
        if [ "${SSH_AUTH:-key}" == "password" ]; then
            sshpass -p "$SSH_PASSWORD" scp $SSH_OPTS -r "${SSH_USER}@${server}:${NODE_LOGS_DIR}" "${TARGET_DIR}" 2>/dev/null
        elif [ -n "${SSH_KEY:-}" ]; then
            scp $SSH_OPTS -r -i "$SSH_KEY" "${SSH_USER}@${server}:${NODE_LOGS_DIR}" "${TARGET_DIR}" 2>/dev/null
        else
            scp $SSH_OPTS -r "${SSH_USER}@${server}:${NODE_LOGS_DIR}" "${TARGET_DIR}" 2>/dev/null
        fi

        if [ -d "$TARGET_DIR" ]; then
            echo "     - Folder logs: Đã lấy thành công"
        else
            echo "     - Folder logs: Không tìm thấy hoặc trống"
        fi

        if $FETCH_RPC; then
            RPC_LOGS_DIR="/opt/metanode/rpc-proxy/node${id}_data/logs"
            TARGET_RPC_DIR="$RUN_LOGS_DIR/node_${id}_rpc_logs"
            if [ "${SSH_AUTH:-key}" == "password" ]; then
                sshpass -p "$SSH_PASSWORD" scp $SSH_OPTS -r "${SSH_USER}@${server}:${RPC_LOGS_DIR}" "${TARGET_RPC_DIR}" 2>/dev/null || true
            elif [ -n "${SSH_KEY:-}" ]; then
                scp $SSH_OPTS -r -i "$SSH_KEY" "${SSH_USER}@${server}:${RPC_LOGS_DIR}" "${TARGET_RPC_DIR}" 2>/dev/null || true
            else
                scp $SSH_OPTS -r "${SSH_USER}@${server}:${RPC_LOGS_DIR}" "${TARGET_RPC_DIR}" 2>/dev/null || true
            fi

            if [ -d "$TARGET_RPC_DIR" ]; then
                echo "     - Folder RPC logs: Đã lấy thành công"
            else
                echo "     - Folder RPC logs: Không tìm thấy hoặc trống"
            fi
        fi

    done
done

echo -e "\n${GREEN}🎉 Hoàn thành! Systemd log được lưu tại: ${RUN_LOGS_DIR}${NC}"
