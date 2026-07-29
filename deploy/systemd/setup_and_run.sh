#!/usr/bin/env bash

# Dừng hệ thống cũ
echo "🛑 Đang dừng chain cũ..."
if [ -f private_chain_data/stop_private_chain.sh ]; then
    bash private_chain_data/stop_private_chain.sh || true
fi
killall simple_chain 2>/dev/null || true
killall metanode 2>/dev/null || true

# Xóa dữ liệu cũ
echo "🗑️ Đang xóa dữ liệu cũ..."
rm -rf private_chain_data

# Số lượng node cần tạo (mặc định là 1 nếu không truyền)
VALIDATORS=${1:-1}

# Khởi tạo cấu hình mới
echo "⚙️ Đang tạo cấu hình cho $VALIDATORS node(s) với Chain ID 991..."
python3 gen_private_chain.py --validators $VALIDATORS --chain-id 991

# Khởi động mạng mới
echo "🚀 Đang khởi động mạng..."
bash private_chain_data/start_private_chain.sh

echo "✅ Đã khởi động xong mạng $VALIDATORS Node!"
