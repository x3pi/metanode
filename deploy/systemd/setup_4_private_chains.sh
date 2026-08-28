#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="$SCRIPT_DIR/private_chains_data"

echo "═══════════════════════════════════════════════════════════════"
echo "🏢 SETUP & RUN 4 PRIVATE CHAINS (CHAIN 101, 102, 103, 104)"
echo "═══════════════════════════════════════════════════════════════"

CLEAN=0
NO_BUILD=0
ROOT_ANCHOR_RPC="http://127.0.0.1:10746"

for arg in "$@"; do
    case $arg in
        --clean|-c)
            CLEAN=1
            ;;
        --no-build)
            NO_BUILD=1
            ;;
        --root-anchor-rpc=*)
            ROOT_ANCHOR_RPC="${arg#*=}"
            ;;
        --help|-h)
            echo "Usage: bash setup_4_private_chains.sh [--clean] [--no-build] [--root-anchor-rpc=URL]"
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
    for chain in "$DATA_DIR"/chain_*; do
        if [ -f "$chain/stop_single_chain.sh" ]; then
            bash "$chain/stop_single_chain.sh" || true
        fi
    done
    if [ -f "$DATA_DIR/stop_all.sh" ]; then
        bash "$DATA_DIR/stop_all.sh" || true
    fi
    pkill -f "chain_101|chain_102|chain_103|chain_104" || true
    echo "🧹 Dọn dẹp thư mục dữ liệu cũ..."
    rm -rf "$DATA_DIR"
fi

# Sinh private key ECDSA CHO SUBMITTER (Milestone C / P4) một cách TẤT ĐỊNH
# (deterministic), khoá theo chain ID -- KHÔNG random như trước.
#
# Vì sao: CommitAttestationWorker.submitMyShare() dùng key này để gửi
# submitCommitAttestation() TỚI Root Anchor. Root Anchor generate genesis
# TRƯỚC KHI private chain này (và submitter key ngẫu nhiên của nó) tồn tại
# (setup_root_anchor.sh chạy trước setup_4_private_chains.sh) -- nên một
# submitter key SINH NGẪU NHIÊN sẽ KHÔNG BAO GIỜ có mặt trong genesis alloc
# của Root Anchor, và mọi tx từ nó bị Root Anchor từ chối với lỗi
# "no BLS public key registered on-chain" (chặn đứng việc gửi share thật).
#
# Fix: dẫn xuất key CHỈ từ chain ID (sha256 của một seed cố định) -- công thức
# NÀY PHẢI KHỚP CHÍNH XÁC với derive_devnet_submitter_account() trong
# gen_root_anchor_chain.py, để Root Anchor's genesis (build trước) và private
# chain này (build sau) tính ra ĐÚNG CÙNG một keypair mà không cần truyền dữ
# liệu qua lại giữa 2 script.
#
# CHỈ DÙNG CHO DEVNET -- key này suy ra được từ mã nguồn, không có tính bí mật.
# Production PHẢI sinh submitter key thật (bí mật, ngẫu nhiên) và đăng ký nó
# lên Root Anchor qua một cơ chế đăng ký thật, không phải genesis alloc cứng.
derive_submitter_key() {
    local chain_id="$1"
    echo -n "metanode-devnet-submitter-chain-${chain_id}" | sha256sum | cut -d' ' -f1
}

# SECURITY (C8 fix, PR #84 review, 2026-08-28): every private chain's ReserveChainID must point
# at the SAME single chain -- Root Anchor, which self-configures reserve_chain_id to its own
# chain ID in gen_root_anchor_chain.py -- never at itself. Without this flag, gen_single_chain.py
# defaults reserve_chain_id to --chain-id (i.e. each chain configures itself as its own Reserve),
# which trivially satisfies the C8 check (LocalChainID==ReserveChainID) for EVERY chain and
# defeats it entirely: any private chain could then independently perform a ceiling-enforced
# attestCommit() against its own local ledger copy of another chain's allocation, letting
# multiple destinations overdraw the same source chain in aggregate -- exactly the C8 fix exists
# to prevent (see note/cross_chain_attack_scenario_catalog.md item C8). Resolve Root Anchor's
# REAL on-chain chain ID via eth_chainId rather than assuming/hardcoding it.
echo ""
echo "🔎 Resolving Reserve chain ID from Root Anchor ($ROOT_ANCHOR_RPC) via eth_chainId..."
RESERVE_CHAIN_ID_HEX=$(curl -s -X POST "$ROOT_ANCHOR_RPC" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}' \
    | python3 -c "import json,sys; print(json.load(sys.stdin).get('result',''))")
if [ -z "$RESERVE_CHAIN_ID_HEX" ]; then
    echo "❌ ERROR: could not fetch Root Anchor's chain ID via eth_chainId from $ROOT_ANCHOR_RPC"
    echo "   (Root Anchor must already be running -- setup_root_anchor.sh runs before this script)"
    exit 1
fi
RESERVE_CHAIN_ID=$((RESERVE_CHAIN_ID_HEX))
echo "✅ Reserve chain ID = $RESERVE_CHAIN_ID"

if [ ! -d "$DATA_DIR/chain_101" ]; then
    mkdir -p "$DATA_DIR"
    
    echo ""
    echo "👉 1. Khởi tạo Private Chain A (Chain ID 101, RPC: http://127.0.0.1:8546)..."
    SUBMITTER_1=$(derive_submitter_key 101)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 101 \
        --rpc-port 8546 \
        --port-offset 10 \
        --validators 1 \
        --root-anchor-rpc "$ROOT_ANCHOR_RPC" \
        --root-anchor-submitter-key "$SUBMITTER_1" \
        --reserve-chain-id "$RESERVE_CHAIN_ID" \
        --output-dir "$DATA_DIR/chain_101"

    echo ""
    echo "👉 2. Khởi tạo Private Chain B (Chain ID 102, RPC: http://127.0.0.1:8547)..."
    SUBMITTER_2=$(derive_submitter_key 102)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 102 \
        --rpc-port 8547 \
        --port-offset 20 \
        --validators 1 \
        --root-anchor-rpc "$ROOT_ANCHOR_RPC" \
        --root-anchor-submitter-key "$SUBMITTER_2" \
        --reserve-chain-id "$RESERVE_CHAIN_ID" \
        --output-dir "$DATA_DIR/chain_102"

    echo ""
    echo "👉 3. Khởi tạo Private Chain C (Chain ID 103, RPC: http://127.0.0.1:8548)..."
    SUBMITTER_3=$(derive_submitter_key 103)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 103 \
        --rpc-port 8548 \
        --port-offset 30 \
        --validators 1 \
        --root-anchor-rpc "$ROOT_ANCHOR_RPC" \
        --root-anchor-submitter-key "$SUBMITTER_3" \
        --reserve-chain-id "$RESERVE_CHAIN_ID" \
        --output-dir "$DATA_DIR/chain_103"

    echo ""
    echo "👉 4. Khởi tạo Private Chain D (Chain ID 104, RPC: http://127.0.0.1:8549)..."
    SUBMITTER_4=$(derive_submitter_key 104)
    python3 "$SCRIPT_DIR/gen_single_chain.py" \
        --chain-id 104 \
        --rpc-port 8549 \
        --port-offset 40 \
        --validators 1 \
        --root-anchor-rpc "$ROOT_ANCHOR_RPC" \
        --root-anchor-submitter-key "$SUBMITTER_4" \
        --reserve-chain-id "$RESERVE_CHAIN_ID" \
        --output-dir "$DATA_DIR/chain_104"

    # Lưu lại Submitter Keys để dễ debug nếu cần thiết
    cat << EOF > "$DATA_DIR/submitter_keys.txt"
Chain 101 Submitter Key: $SUBMITTER_1
Chain 102 Submitter Key: $SUBMITTER_2
Chain 103 Submitter Key: $SUBMITTER_3
Chain 104 Submitter Key: $SUBMITTER_4
EOF

    # Tạo start_all.sh và stop_all.sh cho private_chains_data
    cat << 'EOF' > "$DATA_DIR/start_all.sh"
#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for chain in chain_101 chain_102 chain_103 chain_104; do
    if [ -d "$DIR/$chain" ]; then
        echo "Starting $chain..."
        (cd "$DIR/$chain" && nohup bash start_single_chain.sh > node.log 2>&1 & disown)
    fi
done
EOF
    chmod +x "$DATA_DIR/start_all.sh"

    cat << 'EOF' > "$DATA_DIR/stop_all.sh"
#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for chain in chain_101 chain_102 chain_103 chain_104; do
    if [ -d "$DIR/$chain" ] && [ -f "$DIR/$chain/stop_single_chain.sh" ]; then
        echo "Stopping $chain..."
        bash "$DIR/$chain/stop_single_chain.sh" || true
    fi
done
pkill -f "chain_101|chain_102|chain_103|chain_104" || true
EOF
    chmod +x "$DATA_DIR/stop_all.sh"

    echo "✅ Sinh dữ liệu 4 chains hoàn tất."
fi

echo "🚀 Bắt đầu chạy các nodes..."
bash "$DATA_DIR/start_all.sh"

echo "✅ Đã start 4 private chains! Logs tại deploy/systemd/private_chains_data/chain_XXX/node.log"
