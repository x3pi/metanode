#!/bin/bash
set -e

echo "==========================================================="
echo "🚀 BẮT ĐẦU TẠO PHÂN VÙNG BTRFS DÀNH CHO TOÀN BỘ CLUSTER"
echo "==========================================================="

MOUNT_DIR="/opt/metanode"
BTRFS_DEV="/dev/ubuntu-vg/metanode_data"
BTRFS_IMG_PATH="/opt/metanode_cluster_btrfs.img"
USE_LOOPBACK=false

echo "📂 Thư mục gốc của cluster sẽ là: $MOUNT_DIR"

# 1. Kiểm tra Volume Group
if ! sudo vgdisplay ubuntu-vg >/dev/null 2>&1; then
    echo "⚠️  Không tìm thấy Volume Group 'ubuntu-vg', chuyển sang chế độ Loopback File."
    USE_LOOPBACK=true
fi

if [ "$USE_LOOPBACK" = false ]; then
    echo "📦 Đang thử trích 400GB dung lượng trống LVM..."
    if sudo lvs ubuntu-vg/metanode_data >/dev/null 2>&1; then
        echo "⚠️  Phân vùng metanode_data đã tồn tại!"
    else
        if ! sudo lvcreate -L 400G -n metanode_data ubuntu-vg; then
            echo "⚠️  Không đủ không gian LVM, chuyển sang chế độ Loopback File."
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
        sudo truncate -s 400G "$BTRFS_DEV"
        echo "✅ Đã tạo file $BTRFS_IMG_PATH dung lượng 400GB."
    else
        echo "⚠️  File $BTRFS_IMG_PATH đã tồn tại."
    fi
fi

# 2. Format BTRFS
echo "💽 Đang định dạng phân vùng sang chuẩn BTRFS..."
if ! sudo blkid "$BTRFS_DEV" | grep -i -q btrfs; then
    sudo mkfs.btrfs -f "$BTRFS_DEV"
    echo "✅ Format BTRFS thành công!"
else
    echo "⚠️  Phân vùng đã là BTRFS, bỏ qua format."
fi

# 3. Tạo thư mục & Mount
echo "📂 Đang gắn (mount) phân vùng BTRFS vào $MOUNT_DIR..."
sudo mkdir -p "$MOUNT_DIR"

if ! mountpoint -q "$MOUNT_DIR"; then
    if [ "$USE_LOOPBACK" = true ]; then
        sudo mount -o loop "$BTRFS_DEV" "$MOUNT_DIR"
    else
        sudo mount "$BTRFS_DEV" "$MOUNT_DIR"
    fi
    echo "✅ Đã mount thành công vào $MOUNT_DIR!"
else
    echo "⚠️  Phân vùng đã được mount sẵn vào $MOUNT_DIR."
fi

# 4. Cấp quyền
echo "🔑 Cấp quyền sở hữu thư mục..."
sudo chown -R $USER:$USER "$MOUNT_DIR"

# 5. Thêm vào /etc/fstab để tự động mount khi khởi động lại
if [ "$USE_LOOPBACK" = true ]; then
    FSTAB_ENTRY="$BTRFS_IMG_PATH $MOUNT_DIR btrfs loop,defaults,nofail 0 0"
else
    FSTAB_ENTRY="$BTRFS_DEV $MOUNT_DIR btrfs defaults,nofail 0 0"
fi

if ! grep -q "$MOUNT_DIR" /etc/fstab; then
    echo "⚙️ Đang cấu hình tự động mount vào /etc/fstab..."
    echo "$FSTAB_ENTRY" | sudo tee -a /etc/fstab > /dev/null
    echo "✅ Đã thêm vào fstab thành công!"
fi

echo "==========================================================="
echo "🎉 HOÀN TẤT!"
echo "Tất cả các node nên được cài đặt vào bên trong $MOUNT_DIR"
echo "(VD: /opt/metanode/node-0, /opt/metanode/node-1...)"
echo "==========================================================="
