#!/bin/bash
set -e

echo "==========================================================="
echo "🚀 BẮT ĐẦU TẠO PHÂN VÙNG BTRFS VẬT LÝ TỪ LVM"
echo "==========================================================="

# Di chuyển đến đúng thư mục
cd /home/abc/chain-n/metanode/execution/cmd/simple_chain

# 1. Kiểm tra Volume Group ubuntu-vg có tồn tại không
if ! sudo vgdisplay ubuntu-vg >/dev/null 2>&1; then
    echo "❌ Lỗi: Không tìm thấy Volume Group 'ubuntu-vg'."
    exit 1
fi

# 2. Tạo Logical Volume mới tên là metanode_data, dung lượng 400GB
echo "📦 Đang trích 400GB dung lượng trống để tạo phân vùng mới..."
if sudo lvs ubuntu-vg/metanode_data >/dev/null 2>&1; then
    echo "⚠️  Phân vùng metanode_data đã tồn tại! Bỏ qua bước tạo mới."
else
    sudo lvcreate -L 400G -n metanode_data ubuntu-vg
    echo "✅ Tạo phân vùng 400GB thành công!"
fi

# 3. Format BTRFS
echo "💽 Đang định dạng phân vùng sang chuẩn BTRFS..."
# Chỉ format nếu chưa format (tránh lỡ tay làm mất data nếu chạy lại script)
if ! sudo blkid /dev/ubuntu-vg/metanode_data | grep -q btrfs; then
    sudo mkfs.btrfs -f /dev/ubuntu-vg/metanode_data
    echo "✅ Format BTRFS thành công!"
else
    echo "⚠️  Phân vùng đã là BTRFS, bỏ qua bước format."
fi

# 4. Sao lưu thư mục sample hiện tại
echo "🔄 Đang sao lưu thư mục data cũ..."
if [ -d "./sample" ] && [ ! -d "./sample_backup" ]; then
    mv ./sample ./sample_backup
    echo "✅ Đã đổi tên 'sample' thành 'sample_backup'."
fi

# 5. Tạo thư mục và Mount
echo "📂 Đang gắn (mount) phân vùng BTRFS vào hệ thống..."
mkdir -p ./sample
if ! mountpoint -q ./sample; then
    sudo mount /dev/ubuntu-vg/metanode_data ./sample
    echo "✅ Đã mount thành công!"
else
    echo "⚠️  Phân vùng đã được mount sẵn vào ./sample."
fi

# 6. Cấp quyền
echo "🔑 Cấp quyền sở hữu thư mục cho user hiện tại..."
sudo chown -R $USER:$USER ./sample

echo ""
echo "==========================================================="
echo "🎉 HOÀN TẤT!"
echo "Tất cả các node Metanode giờ đây sẽ chạy trên phân vùng BTRFS vật lý 400GB!"
echo "Bạn có thể tự tin bật tính năng 'snapshot_enabled: true' cho mọi node!"
echo "==========================================================="
