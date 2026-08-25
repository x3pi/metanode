#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="$SCRIPT_DIR/private_chains_data"

echo "═══════════════════════════════════════════════════════════════"
echo "🏢 SETUP & RUN 2 PRIVATE CHAINS (CHAIN 101 & 102)"
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
            echo "Usage: bash setup_2_private_chains.sh [--clean] [--no-build]"
            exit 0
            ;;
    esac
done

if [ "$NO_BUILD" -eq 0 ]; then
    echo "🔨 [BUILD] Đảm bảo biên dịch mã nguồn mới nhất (Go, Rust, FFI)..."
    bash "$SCRIPT_DIR/../../consensus/metanode/scripts/build_check.sh"
    echo ""
fi

if [ "$CLEAN" -eq 1 ]; then
    echo "🛑 Đang dừng các Private Chains cũ nếu đang chạy..."
    if [ -d "$DATA_DIR" ] && [ -f "$DATA_DIR/stop_all.sh" ]; then
        bash "$DATA_DIR/stop_all.sh" || true
    fi
    fuser -k 8546/tcp 8547/tcp 4210/tcp 4220/tcp 20210/tcp 20220/tcp 10210/tcp 10220/tcp 11110/tcp 11120/tcp >/dev/null 2>&1 || true
    sleep 1
    echo "🧹 Dọn dẹp thư mục dữ liệu cũ..."
    rm -rf "$DATA_DIR"
fi

if [ ! -d "$DATA_DIR/chain_101" ] || [ ! -d "$DATA_DIR/chain_102" ]; then
    mkdir -p "$DATA_DIR"
    echo ""
    echo "👉 1. Khởi tạo Private Chain A (Chain ID 101, RPC: http://127.0.0.1:8546)..."
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 101 \
        --rpc-port 8546 \
        --port-offset 10 \
        --validators 1 \
        --output-dir "$DATA_DIR/chain_101"

    echo ""
    echo "👉 2. Khởi tạo Private Chain B (Chain ID 102, RPC: http://127.0.0.1:8547)..."
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 102 \
        --rpc-port 8547 \
        --port-offset 20 \
        --validators 1 \
        --output-dir "$DATA_DIR/chain_102"

    # Tạo master start_all.sh và stop_all.sh
    cat << 'EOF' > "$DATA_DIR/start_all.sh"
#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🚀 Khởi động 2 Private Chains..."
bash "$DIR/chain_101/start_single_chain.sh"
bash "$DIR/chain_102/start_single_chain.sh"
echo "✅ Cả 2 Private Chains đã khởi động thành công!"
echo "   • Chain A (101): http://127.0.0.1:8546"
echo "   • Chain B (102): http://127.0.0.1:8547"
EOF
    chmod +x "$DATA_DIR/start_all.sh"

    cat << 'EOF' > "$DATA_DIR/stop_all.sh"
#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "🛑 Dừng 2 Private Chains..."
bash "$DIR/chain_101/stop_single_chain.sh"
bash "$DIR/chain_102/stop_single_chain.sh"
echo "✅ Cả 2 Private Chains đã dừng."
EOF
    chmod +x "$DATA_DIR/stop_all.sh"
fi

echo ""
echo "🚀 Đang khởi động 2 Private Chains..."
bash "$DATA_DIR/start_all.sh"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "🎉 2 PRIVATE CHAINS ĐÃ SẴN SÀNG:"
echo "   • Private Chain A (101): http://127.0.0.1:8546"
echo "   • Private Chain B (102): http://127.0.0.1:8547"
echo "👉 Dừng 2 Private Chains: bash $DATA_DIR/stop_all.sh"
echo "═══════════════════════════════════════════════════════════════"
