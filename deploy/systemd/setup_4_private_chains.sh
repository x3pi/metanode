#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="$SCRIPT_DIR/private_chains_data"

echo "═══════════════════════════════════════════════════════════════"
echo "🏢 SETUP & RUN 4 PRIVATE CHAINS (CHAIN 101, 102, 103, 104)"
echo "═══════════════════════════════════════════════════════════════"

CLEAN=0
NO_BUILD=0
for arg in "$@"; do
    case $arg in
        --clean|-c)
            CLEAN=1
            ;;
        --no-build)
            NO_BUILD=1
            ;;
        --help|-h)
            echo "Usage: bash setup_4_private_chains.sh [--clean] [--no-build]"
            exit 0
            ;;
    esac
done

if [ "$NO_BUILD" -eq 0 ]; then
    echo "🔨 [BUILD] Đảm bảo biên dịch mã nguồn mới nhất (Go, Rust, FFI)..."
    bash "$SCRIPT_DIR/../../consensus/metanode/scripts/build_check.sh"
    echo ""
fi

if [ "$CLEAN" -eq 1 ] && [ -d "$DATA_DIR" ]; then
    echo "🛑 Đang dừng các Private Chains cũ nếu đang chạy..."
    if [ -f "$DATA_DIR/stop_all.sh" ]; then
        bash "$DATA_DIR/stop_all.sh" || true
    fi
    echo "🧹 Dọn dẹp thư mục dữ liệu cũ..."
    rm -rf "$DATA_DIR"
fi

# Hàm sinh private key ECDSA ngẫu nhiên cho Submitter (Milestone C)
gen_key() {
    python3 -c "import secrets; print(secrets.token_hex(32))"
}

if [ ! -d "$DATA_DIR/chain_101" ]; then
    mkdir -p "$DATA_DIR"
    
    echo ""
    echo "👉 1. Khởi tạo Private Chain A (Chain ID 101, RPC: http://127.0.0.1:8546)..."
    SUBMITTER_1=$(gen_key)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 101 \
        --rpc-port 8546 \
        --port-offset 10 \
        --validators 1 \
        --root-anchor-rpc "http://127.0.0.1:9099" \
        --root-anchor-submitter-key "$SUBMITTER_1" \
        --output-dir "$DATA_DIR/chain_101"

    echo ""
    echo "👉 2. Khởi tạo Private Chain B (Chain ID 102, RPC: http://127.0.0.1:8547)..."
    SUBMITTER_2=$(gen_key)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 102 \
        --rpc-port 8547 \
        --port-offset 20 \
        --validators 1 \
        --root-anchor-rpc "http://127.0.0.1:9099" \
        --root-anchor-submitter-key "$SUBMITTER_2" \
        --output-dir "$DATA_DIR/chain_102"

    echo ""
    echo "👉 3. Khởi tạo Private Chain C (Chain ID 103, RPC: http://127.0.0.1:8548)..."
    SUBMITTER_3=$(gen_key)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 103 \
        --rpc-port 8548 \
        --port-offset 30 \
        --validators 1 \
        --root-anchor-rpc "http://127.0.0.1:9099" \
        --root-anchor-submitter-key "$SUBMITTER_3" \
        --output-dir "$DATA_DIR/chain_103"

    echo ""
    echo "👉 4. Khởi tạo Private Chain D (Chain ID 104, RPC: http://127.0.0.1:8549)..."
    SUBMITTER_4=$(gen_key)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 104 \
        --rpc-port 8549 \
        --port-offset 40 \
        --validators 1 \
        --root-anchor-rpc "http://127.0.0.1:9099" \
        --root-anchor-submitter-key "$SUBMITTER_4" \
        --output-dir "$DATA_DIR/chain_104"

    # Lưu lại Submitter Keys để dễ debug nếu cần thiết
    cat << EOF > "$DATA_DIR/submitter_keys.txt"
Chain 101 Submitter Key: $SUBMITTER_1
Chain 102 Submitter Key: $SUBMITTER_2
Chain 103 Submitter Key: $SUBMITTER_3
Chain 104 Submitter Key: $SUBMITTER_4
EOF

    echo "✅ Sinh dữ liệu 4 chains hoàn tất."
fi

echo "🚀 Bắt đầu chạy các nodes..."
cd "$DATA_DIR"
for chain in chain_101 chain_102 chain_103 chain_104; do
    echo "Starting $chain..."
    cd $chain
    nohup bash start_nodes.sh > node.log 2>&1 &
    cd ..
done

echo "✅ Đã start 4 private chains! Logs tại deploy/systemd/private_chains_data/chain_XXX/node.log"
