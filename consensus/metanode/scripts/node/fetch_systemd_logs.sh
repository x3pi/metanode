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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${1:-$SCRIPT_DIR/deploy.env}"

if [ "$#" -ge 2 ] && [ "$1" == "--env" ]; then
    ENV_FILE="$2"
    if [[ "$ENV_FILE" != /* ]]; then
        ENV_FILE="$SCRIPT_DIR/$ENV_FILE"
    fi
elif [ "$#" -eq 1 ] && [[ "$1" != --* ]]; then
    ENV_FILE="$1"
    if [[ "$ENV_FILE" != /* ]]; then
        ENV_FILE="$SCRIPT_DIR/$ENV_FILE"
    fi
fi

if [ ! -f "$ENV_FILE" ]; then
    echo -e "${YELLOW}Không tìm thấy file config: $ENV_FILE${NC}"
    echo "Usage: ./fetch_systemd_logs.sh --env <env-file>"
    exit 1
fi

source "$ENV_FILE"

# Tạo thư mục chứa log
LOCAL_SYSTEMD_LOGS_DIR="${LOCAL_METANODE:-$SCRIPT_DIR/../../../}/logs_systemd"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
RUN_LOGS_DIR="$LOCAL_SYSTEMD_LOGS_DIR/run_$TIMESTAMP"
mkdir -p "$RUN_LOGS_DIR"

echo -e "${CYAN}📋 Using config: $ENV_FILE${NC}"
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
        
        # 1. Fetch file log vật lý của Execution
        EXEC_LOG="/opt/metanode/node-${id}/logs/execution/execution.log"
        scp_cmd "$server" "$EXEC_LOG" "$RUN_LOGS_DIR/node_${id}_file_execution.log" 2>/dev/null
        if [ -s "$RUN_LOGS_DIR/node_${id}_file_execution.log" ]; then
            echo "     - execution file: Đã lấy"
        else
            rm -f "$RUN_LOGS_DIR/node_${id}_file_execution.log"
        fi

        # 2. Fetch file log vật lý của Consensus
        CONS_LOG="/opt/metanode/node-${id}/logs/consensus/consensus.log"
        scp_cmd "$server" "$CONS_LOG" "$RUN_LOGS_DIR/node_${id}_file_consensus.log" 2>/dev/null
        if [ -s "$RUN_LOGS_DIR/node_${id}_file_consensus.log" ]; then
            echo "     - consensus file: Đã lấy"
        else
            rm -f "$RUN_LOGS_DIR/node_${id}_file_consensus.log"
        fi

        # 3. Fetch file log vật lý của RPC
        RPC_LOG="/opt/metanode/rpc-proxy/node${id}_data/logs/systemd.log"
        scp_cmd "$server" "$RPC_LOG" "$RUN_LOGS_DIR/node_${id}_file_rpc.log" 2>/dev/null
        if [ -s "$RUN_LOGS_DIR/node_${id}_file_rpc.log" ]; then
            echo "     - rpc file: Đã lấy"
        else
            rm -f "$RUN_LOGS_DIR/node_${id}_file_rpc.log"
        fi

        # 4. Backup: Lấy thẳng từ journalctl (phòng khi không ghi ra file) - lấy 10,000 dòng cuối
        ssh_cmd "$server" "sudo journalctl -u metanode-execution-$id -n 10000 --no-pager" > "$RUN_LOGS_DIR/node_${id}_journal_execution.log" 2>/dev/null
        if [ -s "$RUN_LOGS_DIR/node_${id}_journal_execution.log" ]; then
            echo "     - journal execution: Đã lấy"
        else
            rm -f "$RUN_LOGS_DIR/node_${id}_journal_execution.log"
        fi

        ssh_cmd "$server" "sudo journalctl -u metanode-consensus-$id -n 10000 --no-pager" > "$RUN_LOGS_DIR/node_${id}_journal_consensus.log" 2>/dev/null
        if [ -s "$RUN_LOGS_DIR/node_${id}_journal_consensus.log" ]; then
            echo "     - journal consensus: Đã lấy"
        else
            rm -f "$RUN_LOGS_DIR/node_${id}_journal_consensus.log"
        fi

        ssh_cmd "$server" "sudo journalctl -u metanode-rpc-$id -n 10000 --no-pager" > "$RUN_LOGS_DIR/node_${id}_journal_rpc.log" 2>/dev/null
        if [ -s "$RUN_LOGS_DIR/node_${id}_journal_rpc.log" ]; then
            echo "     - journal rpc: Đã lấy"
        else
            rm -f "$RUN_LOGS_DIR/node_${id}_journal_rpc.log"
        fi

    done
done

echo -e "\n${GREEN}🎉 Hoàn thành! Systemd log được lưu tại: ${RUN_LOGS_DIR}${NC}"
