# clean data
#./setup_and_run.sh --clean


#!/usr/bin/env bash
# Dừng hệ thống cũ
echo "🛑 Đang dừng chain cũ..."
if [ -f private_chain_data/stop_private_chain.sh ]; then
    bash private_chain_data/stop_private_chain.sh || true
fi
killall simple_chain 2>/dev/null || true
killall metanode 2>/dev/null || true

# Parse flags
CLEAN=0
BUILD=1
VALIDATORS=1

for arg in "$@"; do
    case $arg in
        --clean|-c)
            CLEAN=1
            ;;
        --no-build)
            BUILD=0
            ;;
        --build|-b)
            BUILD=1
            ;;
        *)
            # Nếu là số thì lấy làm số lượng validator
            if [[ "$arg" =~ ^[0-9]+$ ]]; then
                VALIDATORS=$arg
            fi
            ;;
    esac
done

if [ "$BUILD" -eq 1 ]; then
    echo "🔨 Flag --build được kích hoạt. Đang biên dịch lại mã nguồn Go & Rust..."
    bash ../../consensus/metanode/scripts/build_check.sh --release
    if [ $? -ne 0 ]; then
        echo "❌ Quá trình build thất bại! Dừng khởi tạo mạng."
        exit 1
    fi
fi

if [ "$CLEAN" -eq 1 ]; then
    echo "🧹 Flag --clean được kích hoạt. Đang dọn dẹp toàn bộ dữ liệu cũ..."
    rm -rf private_chain_data
fi

if [ ! -f "private_chain_data/start_private_chain.sh" ]; then
    echo "⚠️ Không tìm thấy dữ liệu (hoặc đã bị xóa), tiến hành tạo mới..."
    # Xóa dữ liệu rác cũ nếu có (an toàn)
    rm -rf private_chain_data

    # Khởi tạo cấu hình mới
    echo "⚙️ Đang tạo cấu hình cho $VALIDATORS node(s) với Chain ID 991..."
    python3 gen_private_chain.py --validators $VALIDATORS --chain-id 991 --rpc-port 8545
else
    echo "✅ Đã tìm thấy dữ liệu chain cũ (private_chain_data). Giữ nguyên dữ liệu để tiếp tục..."
fi

# Khởi động mạng
echo "🚀 Đang khởi động mạng..."
bash private_chain_data/start_private_chain.sh

echo "✅ Đã khởi động xong mạng $VALIDATORS Node!"
