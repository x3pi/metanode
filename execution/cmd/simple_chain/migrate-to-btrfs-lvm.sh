#!/bin/bash
set -e

echo "==========================================================="
echo "🚀 BẮT ĐẦU TẠO PHÂN VÙNG BTRFS VẬT LÝ TỪ LVM / LOOPBACK"
echo "==========================================================="

# Di chuyển đến thư mục chứa script
cd "$(dirname "$0")"

BTRFS_DEV="/dev/ubuntu-vg/metanode_data"
BTRFS_IMG_PATH="$(pwd)/metanode_btrfs.img"

USE_LOOPBACK=false

# 1. Kiểm tra Volume Group ubuntu-vg có tồn tại không
if ! sudo vgdisplay ubuntu-vg >/dev/null 2>&1; then
    echo "⚠️  Không tìm thấy Volume Group 'ubuntu-vg', chuyển sang chế độ Loopback File."
    USE_LOOPBACK=true
fi

if [ "$USE_LOOPBACK" = false ]; then
    # 2. Tạo Logical Volume mới tên là metanode_data, dung lượng 400GB
    echo "📦 Đang thử trích 400GB dung lượng trống LVM để tạo phân vùng mới..."
    if sudo lvs ubuntu-vg/metanode_data >/dev/null 2>&1; then
        echo "⚠️  Phân vùng metanode_data đã tồn tại! Bỏ qua bước tạo mới."
    else
        if ! sudo lvcreate -L 400G -n metanode_data ubuntu-vg; then
            echo "⚠️  Không đủ không gian LVM, chuyển sang chế độ Loopback File (Sparse File)."
            USE_LOOPBACK=true
        else
            echo "✅ Tạo phân vùng 400GB thành công!"
        fi
    fi
fi

if [ "$USE_LOOPBACK" = true ]; then
    echo "📦 Sử dụng file Sparse 400GB làm phân vùng BTRFS..."
    BTRFS_DEV="$BTRFS_IMG_PATH"
    if [ ! -f "$BTRFS_DEV" ]; then
        truncate -s 400G "$BTRFS_DEV"
        echo "✅ Đã tạo file metanode_btrfs.img dung lượng 400GB."
    else
        echo "⚠️  File metanode_btrfs.img đã tồn tại."
    fi
fi

# 3. Format BTRFS
echo "💽 Đang định dạng phân vùng sang chuẩn BTRFS..."
# Chỉ format nếu chưa format (tránh lỡ tay làm mất data nếu chạy lại script)
if ! sudo blkid "$BTRFS_DEV" | grep -i -q btrfs; then
    sudo mkfs.btrfs -f "$BTRFS_DEV"
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
    sudo mount "$BTRFS_DEV" ./sample
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
echo "Tất cả các node Metanode giờ đây sẽ chạy trên phân vùng BTRFS 400GB!"
echo "Bạn có thể tự tin bật tính năng 'snapshot_enabled: true' cho mọi node!"
echo "==========================================================="
