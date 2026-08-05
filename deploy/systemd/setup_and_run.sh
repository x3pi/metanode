# clean data
#./setup_and_run.sh --clean


#!/usr/bin/env bash
# Dừng hệ thống cũ
echo "🛑 Đang dừng chain cũ..."
if [ -f single_chain_data/stop_single_chain.sh ]; then
    bash single_chain_data/stop_single_chain.sh || true
fi
pkill -9 -f simple_chain 2>/dev/null || true
pkill -9 -f metanode 2>/dev/null || true
sleep 1

# Parse flags
CLEAN=0
BUILD=1

for arg in "$@"; do
    case $arg in
        --help|-h)
            echo "📖 HƯỚNG DẪN SỬ DỤNG:"
            echo "  bash setup_and_run.sh [options]"
            echo ""
            echo "Options:"
            echo "  --clean, -c      Xóa toàn bộ dữ liệu cũ và khởi tạo chain từ đầu (reset state)."
            echo "  --no-build       Không biên dịch lại mã nguồn (tiết kiệm thời gian nếu không có sửa đổi code)."
            echo "  --build, -b      Bắt buộc biên dịch lại mã nguồn Go & Rust (mặc định)."
            echo "  --help, -h       Hiển thị hướng dẫn này."
            echo ""
            echo "💡 MẸO:"
            echo "  - Để CHẠY TIẾP và GIỮ NGUYÊN DATA cũ: chỉ cần chạy 'bash setup_and_run.sh'"
            echo "  - Để XÓA SẠCH chạy lại từ đầu: chạy 'bash setup_and_run.sh --clean'"
            exit 0
            ;;
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
            echo "Unknown flag: $arg"
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
    rm -rf single_chain_data
fi

if [ ! -f "single_chain_data/start_single_chain.sh" ]; then
    echo "⚠️ Không tìm thấy dữ liệu (hoặc đã bị xóa), tiến hành tạo mới..."
    # Xóa dữ liệu rác cũ nếu có (an toàn)
    rm -rf single_chain_data

    # Khởi tạo cấu hình mới
    echo "⚙️ Đang tạo cấu hình cho 1 node(s) với Chain ID 991..."
    python3 gen_single_chain.py --validators 1 --chain-id 991 --rpc-port 8545 --is-rpc
else
    echo "✅ Đã tìm thấy dữ liệu chain cũ (single_chain_data). Giữ nguyên dữ liệu để tiếp tục..."
fi

# Khởi động mạng
echo "🚀 Đang khởi động mạng..."
bash single_chain_data/start_single_chain.sh

echo "✅ Đã khởi động xong mạng 1 Node!"
