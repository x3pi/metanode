#!/bin/bash
# Script Tự Động Thiết Lập Ổ Ảo BTRFS và Build Metanode
# Giúp Snapshot Xapian siêu tốc (<100ms) mà không cần can thiệp ổ cứng thật

set -e # Dừng script ngay nếu có lỗi

echo "==========================================================="
echo "🛠️ BẮT ĐẦU THIẾT LẬP MÔI TRƯỜNG SYNCONLY (BTRFS)"
echo "==========================================================="

# 1. Di chuyển vào thư mục dự án
cd /home/abc/chain-n/metanode/execution/cmd/simple_chain

# 2. Tạo ổ ảo BTRFS 100GB (Chỉ chiếm dung lượng thật khi có dữ liệu ghi vào)
echo "📦 Bước 1: Khởi tạo ổ đĩa ảo 100GB..."
if [ ! -f "synconly_btrfs.img" ]; then
    truncate -s 100G synconly_btrfs.img
    mkfs.btrfs -f synconly_btrfs.img
    echo "✅ Đã tạo thành công ổ cứng ảo synconly_btrfs.img"
else
    echo "⚠️ File ổ ảo đã tồn tại, bỏ qua tạo mới để tránh mất dữ liệu."
fi

# 3. Tạo thư mục và Mount ổ đĩa ảo
echo "🗂️ Bước 2: Mount ổ đĩa ảo vào thư mục data..."
mkdir -p ./sample/synconly/data

# Kiểm tra xem đã mount chưa, nếu chưa thì mount
if ! mountpoint -q ./sample/synconly/data; then
    sudo mount -o loop synconly_btrfs.img ./sample/synconly/data
    echo "✅ Đã mount thành công ổ đĩa BTRFS."
else
    echo "⚠️ Ổ đĩa đã được mount sẵn."
fi

# Cấp quyền cho thư mục data vừa mount để tránh lỗi Permission Denied
sudo chown -R $USER:$USER ./sample/synconly/data

# 4. Build mã nguồn Rust (Fix NOMT)
echo "🦀 Bước 3: Đang build lại thư viện Rust (NOMT)..."
cd /home/abc/chain-n/metanode/execution/pkg/nomt_ffi/rust_lib
cargo build --release
echo "✅ Build Rust thành công."

# 5. Build mã nguồn Go
echo "🐹 Bước 4: Đang build lại Metanode (Go)..."
cd /home/abc/chain-n/metanode/execution/cmd/simple_chain
go build
echo "✅ Build Go thành công."

echo "==========================================================="
echo "🎉 HOÀN TẤT TẤT CẢ THIẾT LẬP!"
echo "Bạn có thể khởi động mạng lưới an toàn:"
echo "1. ./mtn-orchestrator.sh --fresh --build-all"
echo "2. ./run-synconly.sh"
echo "==========================================================="
