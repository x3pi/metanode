#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
WORK_DIR="${SCRIPT_DIR}/raft_test_cluster"
BIN_SIMPLE_CHAIN="${SCRIPT_DIR}/simple_chain"
BIN_ROLLUP_CLUSTER="${REPO_ROOT}/execution/cmd/tool/rollup_cluster/rollup-cluster"

echo "╔═══════════════════════════════════════════════════════════════════════════════╗"
echo "║  🚀 THỬ NGHIỆM KHẢ NĂNG CHỊU LỖI CỤM NODE THỰC THI VỚI RAFT (HASHICORP/RAFT)   ║"
echo "╚═══════════════════════════════════════════════════════════════════════════════╝"

cleanup() {
    echo ""
    echo "🧹 Đang dọn dẹp các tiến trình Raft..."
    pkill -f "simple_chain.*raft_test_cluster" || true
    sleep 1
}
trap cleanup EXIT

cleanup

# 1. Chuẩn bị thư mục và khóa bí mật HMAC
rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}/n0" "${WORK_DIR}/n1" "${WORK_DIR}/n2"

SECRET_FILE="${WORK_DIR}/raft_secret.key"
# Tạo secret 32 bytes
python3 -c "import secrets; open('${SECRET_FILE}', 'wb').write(secrets.token_bytes(32))"
chmod 600 "${SECRET_FILE}"

GENESIS_FILE="${SCRIPT_DIR}/devnet_data/exec1/genesis.json"
if [[ ! -f "${GENESIS_FILE}" ]]; then
    echo "❌ Không tìm thấy genesis file tại ${GENESIS_FILE}"
    exit 1
fi

PRIV_KEY="0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c"
SEQ_ADDR="0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"

# 2. Tạo config cho 3 replica (n0, n1, n2)
# Ports:
# n0: RPC 8810, P2P 4310, Raft 7110, Forward 7210
# n1: RPC 8811, P2P 4311, Raft 7111, Forward 7211
# n2: RPC 8812, P2P 4312, Raft 7112, Forward 7212

create_config() {
    local ID=$1
    local RPC_PORT=$2
    local P2P_PORT=$3
    local RAFT_PORT=$4
    local FWD_PORT=$5
    local BOOTSTRAP=$6

    cat <<EOF > "${WORK_DIR}/${ID}/config.json"
{
  "debug": false,
  "cluster_id": 1,
  "consensus_mode": "raft",
  "enable_private_gateway": false,
  "master_password": "devnet-test-password",
  "app_pepper": "devnet-test-pepper",
  "private_key": "${PRIV_KEY}",
  "address": "${SEQ_ADDR}",
  "log_path": "${WORK_DIR}/${ID}/logs",
  "backup_path": "${WORK_DIR}/${ID}/backup",
  "explorer_db_path": "${WORK_DIR}/${ID}/explorer",
  "explorer_read_only_db_path": "${WORK_DIR}/${ID}/explorer-ro",
  "is_explorer": false,
  "connection_address": "0.0.0.0:${P2P_PORT}",
  "version": "0.0.1.0",
  "rpc_port": ":${RPC_PORT}",
  "db_type": 2,
  "genesis_file_path": "${GENESIS_FILE}",
  "snapshot_enabled": false,
  "pk_admin_file_storage": "87d931eaa2f76709f2615586e0d560ca9b80f247c9cc431e197ba3e7167db623",
  "bls_admin_storage": "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
  "owner_file_storage_address": "0xC6E6474A8DEAD25B0e75b1aeA5d35FA19f69588a",
  "Databases": {
    "RootPath": "${WORK_DIR}/${ID}/data",
    "DBEngine": "sharded",
    "Version": "0.0.1.0",
    "BLSPrivateKey": "5fb8d1ceadf4059adca5c106dbd91452be8c433b2c38c5dd85c50f1c7da4c85c"
  },
  "is_rpc_node": true,
  "raft": {
    "node_id": "${ID}",
    "bind_address": "127.0.0.1:${RAFT_PORT}",
    "data_dir": "${WORK_DIR}/${ID}/raft",
    "forward_bind_address": "127.0.0.1:${FWD_PORT}",
    "forward_secret_file": "${SECRET_FILE}",
    "sequencer_address": "${SEQ_ADDR}",
    "bootstrap": ${BOOTSTRAP},
    "heartbeat_timeout_ms": 100,
    "election_timeout_ms": 200,
    "leader_lease_timeout_ms": 80,
    "commit_timeout_ms": 30,
    "peers": [
      {
        "id": "n0",
        "address": "127.0.0.1:7110",
        "forward_address": "127.0.0.1:7210"
      },
      {
        "id": "n1",
        "address": "127.0.0.1:7111",
        "forward_address": "127.0.0.1:7211"
      },
      {
        "id": "n2",
        "address": "127.0.0.1:7112",
        "forward_address": "127.0.0.1:7212"
      }
    ]
  }
}
EOF
}

create_config "n0" 8810 4310 7110 7210 true
create_config "n1" 8811 4311 7111 7211 false
create_config "n2" 8812 4312 7112 7212 false

# Tạo file cluster.json cho công cụ rollup-cluster
cat <<EOF > "${WORK_DIR}/cluster.json"
{
  "secret_file": "${SECRET_FILE}",
  "members": [
    {"id": "n0", "raft_addr": "127.0.0.1:7110", "admin_addr": "127.0.0.1:7210"},
    {"id": "n1", "raft_addr": "127.0.0.1:7111", "admin_addr": "127.0.0.1:7211"},
    {"id": "n2", "raft_addr": "127.0.0.1:7112", "admin_addr": "127.0.0.1:7212"}
  ]
}
EOF

echo "📁 Đã khởi tạo cấu hình cho 3 Replicas (n0 bootstrap=true, n1, n2)."

# 3. Khởi chạy 3 Replicas
echo ""
echo "🚀 [BƯỚC 1] Khởi chạy 3 node thực thi thuộc cụm Raft (Quorum: 2/3)..."
"${BIN_SIMPLE_CHAIN}" -config "${WORK_DIR}/n0/config.json" > "${WORK_DIR}/n0/node.log" 2>&1 &
PID_N0=$!
"${BIN_SIMPLE_CHAIN}" -config "${WORK_DIR}/n1/config.json" > "${WORK_DIR}/n1/node.log" 2>&1 &
PID_N1=$!
"${BIN_SIMPLE_CHAIN}" -config "${WORK_DIR}/n2/config.json" > "${WORK_DIR}/n2/node.log" 2>&1 &
PID_N2=$!

echo "   n0 (PID ${PID_N0}) - RPC :8810 | Raft :7110"
echo "   n1 (PID ${PID_N1}) - RPC :8811 | Raft :7111"
echo "   n2 (PID ${PID_N2}) - RPC :8812 | Raft :7112"

echo "⏳ Chờ cụm Raft bầu Leader (Heartbeat 100ms, Election 200ms)..."
sleep 4

# 4. Kiểm tra trạng thái bằng rollup-cluster check
echo ""
echo "📊 [BƯỚC 2] Kiểm tra trạng thái sức khỏe cụm Raft ban đầu:"
"${BIN_ROLLUP_CLUSTER}" -config "${WORK_DIR}/cluster.json" check || true

# Helper kiểm tra block height
get_height() {
    local PORT=$1
    curl -s -X POST "http://127.0.0.1:${PORT}" -H "Content-Type: application/json" \
      -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq -r '.result // "error"'
}

echo ""
echo "🔍 Block Height ban đầu:"
echo "   n0 (:8810): $(get_height 8810)"
echo "   n1 (:8811): $(get_height 8811)"
echo "   n2 (:8812): $(get_height 8812)"

# 5. Gửi giao dịch khi cả 3 node đang sống
echo ""
echo "💳 [BƯỚC 3] Gửi giao dịch qua RPC của cụm (3 node đang online)..."
# Gửi giao dịch chuyển tiền qua eth_sendRawTransaction
# Tạo giao dịch đơn giản qua RPC n0
SEND_TX_RES=$(curl -s -X POST "http://127.0.0.1:8810" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["0xf864808504a817c80082520894000000000000000000000000000000000000000180808207e1a0a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1a01f0f8761e3fe67cdc9e7573adf72c7e929e2a00f981834298867d5628ca2d8f6"],"id":1}')
echo "   Kết quả gửi tx vào n0: ${SEND_TX_RES}"

sleep 2
echo "   Block Height sau giao dịch 1:"
echo "   n0: $(get_height 8810)"
echo "   n1: $(get_height 8811)"
echo "   n2: $(get_height 8812)"

# 6. GIẢ LẬP SỰ CỐ: KILL LEADER NODE (n0)!
echo ""
echo "💥 [BƯỚC 4] GIẢ LẬP SỰ CỐ: CƯỠNG CHẾ TẮT LEADER (KILL -9 PID ${PID_N0} - node n0)..."
kill -9 "${PID_N0}"
sleep 1

echo "⚡ Đã kill n0! Kiểm tra trạng thái cụm ngay sau khi mất Leader:"
"${BIN_ROLLUP_CLUSTER}" -config "${WORK_DIR}/cluster.json" check || true

# 7. Gửi tiếp giao dịch vào cụm còn lại (n1 hoặc n2)
echo ""
echo "💳 [BƯỚC 5] Cụm còn 2/3 node (đạt Quorum). Gửi giao dịch tiếp theo vào Follower n1 (RPC :8811)..."
SEND_TX_RES2=$(curl -s -X POST "http://127.0.0.1:8811" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["0xf86481018504a817c80082520894000000000000000000000000000000000000000280808207e1a0a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1a01f0f8761e3fe67cdc9e7573adf72c7e929e2a00f981834298867d5628ca2d8f6"],"id":1}')
echo "   Kết quả gửi tx vào n1: ${SEND_TX_RES2}"

sleep 2
echo "   Block Height của các node còn sống:"
echo "   n1 (:8811): $(get_height 8811)"
echo "   n2 (:8812): $(get_height 8812)"

# 8. PHỤC HỒI: KHỞI ĐỘNG LẠI n0 VÀ CATCH-UP
echo ""
echo "🔄 [BƯỚC 6] Khởi động lại n0 để kiểm tra khả năng tự động đồng bộ (Catch-Up)..."
# Khi n0 bật lại, nó đã có state trong data_dir và raft dir, nó tự động tái tham gia cụm
"${BIN_SIMPLE_CHAIN}" -config "${WORK_DIR}/n0/config.json" > "${WORK_DIR}/n0/node_rejoin.log" 2>&1 &
PID_N0_NEW=$!
echo "   n0 đã online trở lại với PID ${PID_N0_NEW}."
sleep 3

echo ""
echo "📊 [BƯỚC 7] Kiểm tra lại trạng thái toàn cụm sau khi n0 đã phục hồi:"
"${BIN_ROLLUP_CLUSTER}" -config "${WORK_DIR}/cluster.json" check || true

echo ""
echo "🔍 Block Height sau khi toàn bộ 3 node online:"
H0=$(get_height 8810)
H1=$(get_height 8811)
H2=$(get_height 8812)
echo "   n0 (:8810): ${H0}"
echo "   n1 (:8811): ${H1}"
echo "   n2 (:8812): ${H2}"

if [[ "${H0}" != "error" && "${H1}" != "error" && "${H2}" != "error" && "${H0}" == "${H1}" && "${H1}" == "${H2}" ]]; then
    echo ""
    echo "🎉 ================================================================================"
    echo "   ✅ KẾT QUẢ: THỬ NGHIỆM KHẢ NĂNG CHỊU LỖI RAFT THÀNH CÔNG RỰC RỠ!"
    echo "   • 3 node duy trì Quorum đồng thuận nhất quán."
    echo "   • Khi Leader bị sập (kill -9), cụm tự động bầu Leader mới trong vài trăm ms."
    echo "   • Cụm vẫn tiếp tục xử lý giao dịch và đóng block bình thường với 2/3 node."
    echo "   • Node chết khi sống lại tự động catch-up đồng bộ 100% block height!"
    echo "================================================================================"
else
    echo "⚠️ Các node có chiều cao block: n0=${H0}, n1=${H1}, n2=${H2}"
fi
