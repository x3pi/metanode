#!/bin/bash

# Dừng pipeline nếu có bất kỳ lệnh nào bị lỗi
# set -e

# Tự động lấy thư mục gốc của project (thư mục chứa file auto_test.sh)
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Suy ra thư mục script mtn-consensus nằm cùng cấp
METANODE_SCRIPT_DIR="$(cd "$PROJECT_ROOT/../consensus/metanode/scripts/node" && pwd)"

# Cấu hình bước bắt đầu chạy (mặc định là 1)
START_STEP=1
# Cấu hình chế độ deploy (mặc định là single)
DEPLOY_MODE="single"

# Nhận tham số truyền vào từ command line (VD: ./auto_test.sh --step 3 --mode multi)
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --step) START_STEP="$2"; shift ;;
        --mode) DEPLOY_MODE="$2"; shift ;;
    esac
    shift
done

echo "=================================================="
if [ $START_STEP -gt 1 ]; then
    echo "🚀 BẮT ĐẦU AUTO TEST PIPELINE (RESUME TỪ BƯỚC $START_STEP)"
else
    echo "🚀 BẮT ĐẦU AUTO TEST PIPELINE TỪ ĐẦU..."
fi
echo "💡 Parameter hiện tại: MODE=$DEPLOY_MODE | START_STEP=$START_STEP"
echo "💡 Usage: ./auto_test.sh [--step <số>] [--mode single|multi]"
echo "=================================================="


# ----------------------------------------------------
# BƯỚC 1: Xóa genesis cũ và tạo file genesis mới
# ----------------------------------------------------
if [ $START_STEP -le 1 ]; then
    echo ""
    echo "📌 BƯỚC 1: Prepare Genesis & Gen Spam Keys..."
    cd "$PROJECT_ROOT/cmd/simple_chain"
    echo "  -> Xóa genesis.json và copy từ genesis-main.json..."
    rm -f genesis.json
    cp genesis-main.json genesis.json

    cd "$PROJECT_ROOT/cmd/tool/test_tps/gen_spam_keys"
    echo "  -> Chạy Gen Spam Keys (count 50000)..."
    go run main.go --count 50000
    if [ $? -ne 0 ]; then echo "❌ Lỗi ở Bước 1"; exit 1; fi
fi

# ----------------------------------------------------
# BƯỚC 2: Triển khai Cụm
# ----------------------------------------------------
if [ $START_STEP -le 2 ]; then
    echo ""
    echo "📌 BƯỚC 2: Triển khai cụm Cluster (deploy_cluster.sh)..."
    if [ "$DEPLOY_MODE" == "single" ]; then
        cd "$METANODE_SCRIPT_DIR/.."
        ./mtn-orchestrator.sh restart --fresh --build-all
        if [ $? -ne 0 ]; then echo "❌ Lỗi khi Deploy Cluster Mạng Lớn ở Bước 2"; exit 1; fi
    else
        cd "$METANODE_SCRIPT_DIR"
        ./deploy_cluster.sh --env deploy-3machines.env --all
        if [ $? -ne 0 ]; then echo "❌ Lỗi khi Deploy Cluster Single ở Bước 2"; exit 1; fi
    fi

    # Đợi 1 chút để các HTTP server start up hoàn toàn
    sleep 5
fi

# ----------------------------------------------------
# BƯỚC 2.5: Bật RPC Proxy
# ----------------------------------------------------
if [ $START_STEP -le 2 ]; then
    echo ""
    echo "📌 BƯỚC 2.5: Kiểm tra và bật RPC Proxy..."
    if ! curl -s http://127.0.0.1:8545 > /dev/null; then
        echo "  -> RPC Proxy chưa bật, đang tiến hành khởi động qua tmux session 'rpc-proxy'..."
        cd "$PROJECT_ROOT/cmd/rpc/cmd/rpc-client"
        # Nếu session đã tồn tại thì tắt đi trước khi tạo mới
        tmux kill-session -t rpc-proxy 2>/dev/null || true
        tmux new-session -d -s rpc-proxy 'go run main.go'
        sleep 5
    else
        echo "  ✅ RPC Proxy đã hoạt động ở port 8545."
    fi
fi

# ----------------------------------------------------
# BƯỚC 3: Test TCP Caller
# ----------------------------------------------------
if [ $START_STEP -le 3 ]; then
    echo ""
    echo "📌 BƯỚC 3: Test TCP RPC (main-no-none.go)..."
    cd "$PROJECT_ROOT/cmd/tool/tool-test-chain/test-tcp/caller-tcp"
    go run main-no-none.go -config=config-local.json -data=data.json
    if [ $? -ne 0 ]; then echo "❌ Lỗi ở Test TCP (Bước 3)"; exit 1; fi
fi

# ----------------------------------------------------
# BƯỚC 4: Test HTTP RPC - Xapian V0
# ----------------------------------------------------
if [ $START_STEP -le 4 ]; then
    echo ""
    echo "📌 BƯỚC 4: Test Xapian V0..."
    cd "$PROJECT_ROOT/cmd/tool/tool-test-chain/test-rpc"
    go run main.go -config=./config-local.json -data=./test_read_wire_xapian/data-xapian-v0.json
    if [ $? -ne 0 ]; then echo "❌ Lỗi ở Test Xapian V0 (Bước 4)"; exit 1; fi
fi

# ----------------------------------------------------
# BƯỚC 5: Test HTTP RPC - Xapian V2
# ----------------------------------------------------
if [ $START_STEP -le 5 ]; then
    echo ""
    echo "📌 BƯỚC 5: Test Xapian V2..."
    cd "$PROJECT_ROOT/cmd/tool/tool-test-chain/test-rpc"
    go run main.go -config=./config-local.json -data=./test_read_wire_xapian/data-xapian-v2.json
    if [ $? -ne 0 ]; then echo "❌ Lỗi ở Test Xapian V2 (Bước 5)"; exit 1; fi
fi

# ----------------------------------------------------
# BƯỚC 6: Load Test TPS
# ----------------------------------------------------
if [ $START_STEP -le 6 ]; then
    echo ""
    echo "📌 BƯỚC 6: Load Test TPS (20,000 txs)..."
    cd "$PROJECT_ROOT/cmd/tool/test_tps/tps_blast_cc"
    if [ "$DEPLOY_MODE" == "single" ]; then
        go run main.go --count 20000 --parallel_native=true --rounds 10 --load_balance=false --batch=500
    else
        go run main.go --count 20000 --parallel_native=true --rounds 1 --load_balance=true --batch=500
    fi
    if [ $? -ne 0 ]; then echo "❌ Lỗi ở Load Test TPS (Bước 6)"; exit 1; fi
fi

echo ""
echo "=================================================="
echo "🎉 AUTO TEST PIPELINE COMPLETED SUCCESSFULLY!"
echo "=================================================="
