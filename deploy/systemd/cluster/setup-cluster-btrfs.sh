#!/bin/bash
set -e

# Parse BTRFS partition/file size (Default: 400G, e.g. 50G, 100G, 200G, 400G)
BTRFS_SIZE="${BTRFS_SIZE:-"400G"}"
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --size|-s) BTRFS_SIZE="$2"; shift ;;
        [0-9]*[GMTPgmtp]*) BTRFS_SIZE="$1" ;;
    esac
    shift
done

echo "==========================================================="
echo "🚀 BẮT ĐẦU TẠO PHÂN VÙNG BTRFS DÀNH CHO TOÀN BỘ CLUSTER"
echo "   Dung lượng chỉ định: $BTRFS_SIZE"
echo "==========================================================="

MOUNT_DIR="/opt/metanode"
BTRFS_DEV="/dev/ubuntu-vg/metanode_data"
BTRFS_IMG_PATH="/opt/metanode_cluster_btrfs.img"
USE_LOOPBACK=false

echo "📂 Thư mục gốc của cluster sẽ là: $MOUNT_DIR"

# 1. Kiểm tra dung lượng ổ đĩa khả dụng
REQ_BYTES=$(numfmt --from=iec "$BTRFS_SIZE" 2>/dev/null || numfmt --from=auto "$BTRFS_SIZE" 2>/dev/null || echo 0)
if [ "$REQ_BYTES" -le 0 ]; then
    echo "❌ Lỗi định dạng dung lượng: '$BTRFS_SIZE' (Ví dụ hợp lệ: 50G, 100G, 1T, 2T)"
    exit 1
fi

FREE_BYTES=$(df -B1 "$MOUNT_DIR" 2>/dev/null | awk 'NR==2 {print $4}')
if [ -z "$FREE_BYTES" ]; then
    FREE_BYTES=$(df -B1 / 2>/dev/null | awk 'NR==2 {print $4}')
fi

# Kiểm tra thêm không gian trống LVM nếu có
LVM_FREE_BYTES=0
if sudo vgdisplay ubuntu-vg >/dev/null 2>&1; then
    LVM_FREE_BYTES=$(sudo vgs --noheadings -o vg_free --units b --nosuffix ubuntu-vg 2>/dev/null | awk '{print int($1)}')
fi

MAX_AVAIL=$FREE_BYTES
if [ "$LVM_FREE_BYTES" -gt "$MAX_AVAIL" ]; then
    MAX_AVAIL=$LVM_FREE_BYTES
fi

if [ "$REQ_BYTES" -gt "$MAX_AVAIL" ]; then
    REQ_HUMAN=$(numfmt --to=iec-i --suffix=B "$REQ_BYTES" 2>/dev/null || echo "$BTRFS_SIZE")
    AVAIL_HUMAN=$(numfmt --to=iec-i --suffix=B "$MAX_AVAIL" 2>/dev/null || echo "$MAX_AVAIL bytes")
    echo -e "\033[0;31m❌ [LỖI DUNG LƯỢNG ĐĨA] Yêu cầu $REQ_HUMAN ($BTRFS_SIZE) nhưng ổ cứng chỉ còn trống $AVAIL_HUMAN!\033[0m"
    echo -e "\033[0;33m👉 Vui lòng giảm dung lượng cấu hình hoặc giải phóng thêm bộ nhớ ổ đĩa.\033[0m"
    exit 1
fi

echo "✅ Kiểm tra dung lượng khả dụng: Đạt yêu cầu ($BTRFS_SIZE)"

# 2. Kiểm tra Volume Group
if ! sudo vgdisplay ubuntu-vg >/dev/null 2>&1; then
    echo "⚠️  Không tìm thấy Volume Group 'ubuntu-vg', chuyển sang chế độ Loopback File."
    USE_LOOPBACK=true
fi

if [ "$USE_LOOPBACK" = false ]; then
    echo "📦 Đang thử trích ${BTRFS_SIZE} dung lượng trống LVM..."
    if sudo lvs ubuntu-vg/metanode_data >/dev/null 2>&1; then
        echo "⚠️  Phân vùng metanode_data đã tồn tại!"
    else
        if ! sudo lvcreate -L "${BTRFS_SIZE}" -n metanode_data ubuntu-vg; then
            echo "⚠️  Không đủ không gian LVM, chuyển sang chế độ Loopback File."
            USE_LOOPBACK=true
        else
            echo "✅ Tạo phân vùng ${BTRFS_SIZE} thành công!"
        fi
    fi
fi

if [ "$USE_LOOPBACK" = true ]; then
    echo "📦 Sử dụng file Sparse ${BTRFS_SIZE} làm phân vùng BTRFS..."
    BTRFS_DEV="$BTRFS_IMG_PATH"
    if [ ! -f "$BTRFS_DEV" ]; then
        sudo truncate -s "${BTRFS_SIZE}" "$BTRFS_DEV"
        echo "✅ Đã tạo file $BTRFS_IMG_PATH dung lượng ${BTRFS_SIZE}."
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
