#!/bin/bash
set -e

# ==============================================================================
# 🚀 METANODE BTRFS CLUSTER STORAGE SETUP SCRIPT
# ==============================================================================
# Single Source of Truth for BTRFS snapshot storage setup & mounting.
# Supports both standalone/systemd manual execution and Ansible automation.
# ==============================================================================

BTRFS_SIZE="${BTRFS_SIZE:-"400G"}"
MOUNT_DIR="${MOUNT_DIR:-"/mnt/metanode_snapshots"}"
BTRFS_IMG_PATH="/opt/metanode_cluster_btrfs.img"
BTRFS_DEV="/dev/ubuntu-vg/metanode_data"
CLEAN_MODE=false
CHECK_MODE=false
UNMOUNT_MODE=false
USE_LOOPBACK=false
FORCE_CLEAN=false

usage() {
    echo "Cách dùng: $0 [OPTIONS]"
    echo "Options:"
    echo "  --size, -s <SIZE>       Dung lượng BTRFS (Mặc định: 400G; ví dụ: 100G, 400G, 1T)"
    echo "  --mount-dir, -m <PATH>  Thư mục mount (Mặc định: /mnt/metanode_snapshots)"
    echo "  --clean, -c             Format và tạo mới lại storage (xóa dữ liệu cũ)"
    echo "  --force, -f             Bắt buộc khi dùng --clean và storage đang chứa dữ liệu node"
    echo "                          (bỏ qua bước kiểm tra an toàn dữ liệu node hiện có)"
    echo "  --check                 Chỉ kiểm tra tài nguyên khả dụng, không thay đổi hệ thống"
    echo "  --unmount, -u           Gỡ mount an toàn khỏi thư mục đích"
    echo "  -h, --help              Hiển thị trợ giúp"
    exit 0
}

while [[ "$#" -gt 0 ]]; do
    case $1 in
        --size|-s) BTRFS_SIZE="$2"; shift 2 ;;
        --mount-dir|-m) MOUNT_DIR="$2"; shift 2 ;;
        --clean|-c) CLEAN_MODE=true; shift ;;
        --force|-f) FORCE_CLEAN=true; shift ;;
        --check) CHECK_MODE=true; shift ;;
        --unmount|-u) UNMOUNT_MODE=true; shift ;;
        -h|--help) usage ;;
        [0-9]*[GMTPgmtp]*) BTRFS_SIZE="$1"; shift ;;
        *) echo "Tham số không hợp lệ: $1" >&2; exit 1 ;;
    esac
done

run_cmd() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

echo "===========================================================" >&2
echo "🚀 QUẢN LÝ PHÂN VÙNG BTRFS SNAPSHOT CLUSTER" >&2
echo "   Dung lượng yêu cầu: $BTRFS_SIZE" >&2
echo "   Thư mục Mount:      $MOUNT_DIR" >&2
echo "   Chế độ Clean:       $CLEAN_MODE" >&2
echo "===========================================================" >&2

# Xử lý chế độ gỡ mount
if [ "$UNMOUNT_MODE" = true ]; then
    if mountpoint -q "$MOUNT_DIR"; then
        echo "📂 Đang gỡ mount khỏi $MOUNT_DIR..." >&2
        run_cmd umount "$MOUNT_DIR" || run_cmd umount -l "$MOUNT_DIR"
        echo "✅ Đã gỡ mount thành công!" >&2
    else
        echo "ℹ️  $MOUNT_DIR chưa được mount." >&2
    fi
    exit 0
fi

# 1. Kiểm tra định dạng và dung lượng đĩa khả dụng
REQ_BYTES=$(numfmt --from=iec "$BTRFS_SIZE" 2>/dev/null || numfmt --from=auto "$BTRFS_SIZE" 2>/dev/null || echo 0)
if [ "$REQ_BYTES" -le 0 ]; then
    echo "❌ Lỗi định dạng dung lượng: '$BTRFS_SIZE' (Ví dụ hợp lệ: 50G, 100G, 400G, 1T, 2T)" >&2
    exit 1
fi

FREE_BYTES=$(df -B1 "$MOUNT_DIR" 2>/dev/null | awk 'NR==2 {print $4}')
if [ -z "$FREE_BYTES" ]; then
    FREE_BYTES=$(df -B1 / 2>/dev/null | awk 'NR==2 {print $4}')
fi

LVM_FREE_BYTES=0
if run_cmd vgs ubuntu-vg >/dev/null 2>&1; then
    LVM_FREE_BYTES=$(run_cmd vgs --noheadings -o vg_free --units b --nosuffix ubuntu-vg 2>/dev/null | awk '{print int($1)}')
fi

IMG_CURR_BYTES=0
if [ -f "$BTRFS_IMG_PATH" ]; then
    IMG_CURR_BYTES=$(stat -c %s "$BTRFS_IMG_PATH" 2>/dev/null || echo 0)
fi

LV_CURR_BYTES=0
if run_cmd lvs ubuntu-vg/metanode_data >/dev/null 2>&1; then
    LV_CURR_BYTES=$(run_cmd lvs --noheadings -o lv_size --units b --nosuffix ubuntu-vg/metanode_data 2>/dev/null | awk '{print int($1)}' || echo 0)
fi

MAX_AVAIL=$FREE_BYTES
if [ "$LVM_FREE_BYTES" -gt "$MAX_AVAIL" ]; then
    MAX_AVAIL=$LVM_FREE_BYTES
fi

NEEDED_BYTES=$REQ_BYTES
if [ "$LV_CURR_BYTES" -ge "$REQ_BYTES" ] || [ "$IMG_CURR_BYTES" -ge "$REQ_BYTES" ]; then
    NEEDED_BYTES=0
elif [ "$LV_CURR_BYTES" -gt 0 ]; then
    NEEDED_BYTES=$(( REQ_BYTES - LV_CURR_BYTES ))
elif [ "$IMG_CURR_BYTES" -gt 0 ]; then
    NEEDED_BYTES=$(( REQ_BYTES - IMG_CURR_BYTES ))
fi

if [ "$NEEDED_BYTES" -gt "$MAX_AVAIL" ]; then
    REQ_HUMAN=$(numfmt --to=iec-i --suffix=B "$NEEDED_BYTES" 2>/dev/null || echo "$NEEDED_BYTES bytes")
    AVAIL_HUMAN=$(numfmt --to=iec-i --suffix=B "$MAX_AVAIL" 2>/dev/null || echo "$MAX_AVAIL bytes")
    echo "❌ [LỖI DUNG LƯỢNG ĐĨA] Cần thêm $REQ_HUMAN nhưng ổ cứng chỉ còn trống $AVAIL_HUMAN!" >&2
    echo "👉 Vui lòng giảm dung lượng cấu hình hoặc giải phóng thêm ổ cứng." >&2
    exit 1
fi
echo "✅ Kiểm tra dung lượng khả dụng: Đạt yêu cầu ($BTRFS_SIZE)" >&2

if [ "$CHECK_MODE" = true ]; then
    echo "✅ [CHECK] Kiểm tra thành công, không có lỗi phát sinh." >&2
    exit 0
fi

# 2. Xác định dùng LVM Partition hay Sparse Loopback Image
if ! run_cmd vgs ubuntu-vg >/dev/null 2>&1; then
    echo "ℹ️  Không tìm thấy Volume Group 'ubuntu-vg', chuyển sang chế độ Loopback Image." >&2
    USE_LOOPBACK=true
fi

if [ "$USE_LOOPBACK" = false ]; then
    if run_cmd lvs ubuntu-vg/metanode_data >/dev/null 2>&1; then
        echo "ℹ️  Phân vùng LV ubuntu-vg/metanode_data đã tồn tại, tái sử dụng." >&2
        BTRFS_DEV="/dev/ubuntu-vg/metanode_data"
    else
        echo "📦 Đang tạo phân vùng LVM ${BTRFS_SIZE}..." >&2
        if ! run_cmd lvcreate -L "${BTRFS_SIZE}" -n metanode_data ubuntu-vg >/dev/null 2>&1; then
            echo "⚠️  Không đủ không gian LVM, tự động chuyển sang chế độ Loopback File." >&2
            USE_LOOPBACK=true
        else
            echo "✅ Tạo phân vùng LVM ${BTRFS_SIZE} thành công!" >&2
            BTRFS_DEV="/dev/ubuntu-vg/metanode_data"
        fi
    fi
fi

if [ "$USE_LOOPBACK" = true ]; then
    BTRFS_DEV="$BTRFS_IMG_PATH"
    run_cmd mkdir -p /opt
    if [ "$CLEAN_MODE" = true ] || [ ! -f "$BTRFS_IMG_PATH" ]; then
        echo "📦 Tạo file Sparse image ${BTRFS_SIZE} tại $BTRFS_IMG_PATH..." >&2
        run_cmd truncate -s 0 "$BTRFS_IMG_PATH" 2>/dev/null || true
        run_cmd truncate -s "${BTRFS_SIZE}" "$BTRFS_IMG_PATH"
        echo "✅ Đã tạo file $BTRFS_IMG_PATH dung lượng ${BTRFS_SIZE}." >&2
    else
        echo "ℹ️  File $BTRFS_IMG_PATH đã tồn tại." >&2
    fi
fi

# 3. Format BTRFS nếu ở chế độ Clean hoặc thiết bị chưa phải chuẩn BTRFS
if [ "$CLEAN_MODE" = true ]; then
    # SAFETY CHECK (this script is manual/standalone-only -- the Ansible-driven path
    # never passes --clean; it uses manage_snapshot_storage.py's own, stricter
    # shared-node bounds check instead). Still worth a guard here: --clean wipes the
    # WHOLE filesystem, so if it currently holds per-node data ("node-N" directories,
    # the same convention manage_snapshot_storage.py uses), refuse unless the operator
    # explicitly acknowledges that with --force. This can't tell WHICH nodes are still
    # "active" the way the Python tool can (this script has no such context), so it
    # cannot be as precise -- it only distinguishes "storage looks empty/fresh" from
    # "storage has node data on it", which is the failure mode that actually matters
    # for a manually-invoked, no-prompt destructive command.
    if mountpoint -q "$MOUNT_DIR"; then
        existing_nodes=$(find "$MOUNT_DIR" -mindepth 1 -maxdepth 1 -type d -name 'node-*' -printf '%f\n' 2>/dev/null | sort)
        if [ -n "$existing_nodes" ] && [ "$FORCE_CLEAN" != true ]; then
            echo "❌ [AN TOÀN DỮ LIỆU] $MOUNT_DIR hiện đang chứa dữ liệu của các node sau:" >&2
            echo "$existing_nodes" | sed 's/^/     - /' >&2
            echo "👉 --clean sẽ XÓA TOÀN BỘ các node trên, không chỉ node bạn đang thao tác." >&2
            echo "   Nếu chắc chắn muốn xóa hết, chạy lại với thêm cờ --force." >&2
            exit 1
        fi
        echo "📂 Đang unmount $MOUNT_DIR để clean..." >&2
        run_cmd umount "$MOUNT_DIR" || run_cmd umount -l "$MOUNT_DIR"
    fi
    if [ "$USE_LOOPBACK" = true ]; then
        # Dọn sạch các loop devices dính dáng tới file ảnh
        for loopdev in $(run_cmd losetup -j "$BTRFS_IMG_PATH" | awk -F: '{print $1}'); do
            run_cmd losetup -d "$loopdev" 2>/dev/null || true
        done
    fi
    echo "💽 Đang format BTRFS (Clean mode)..." >&2
    run_cmd mkfs.btrfs -f "$BTRFS_DEV" >/dev/null
    echo "✅ Format BTRFS thành công!" >&2
else
    if ! run_cmd blkid "$BTRFS_DEV" 2>/dev/null | grep -i -q btrfs; then
        echo "💽 Thiết bị chưa có định dạng BTRFS, tiến hành format..." >&2
        run_cmd mkfs.btrfs -f "$BTRFS_DEV" >/dev/null
        echo "✅ Format BTRFS thành công!" >&2
    else
        echo "ℹ️  Thiết bị đã là chuẩn BTRFS, giữ nguyên dữ liệu." >&2
    fi
fi

# 4. Tạo thư mục & Mount
run_cmd mkdir -p "$MOUNT_DIR"
run_cmd chmod 755 "$MOUNT_DIR"

if ! mountpoint -q "$MOUNT_DIR"; then
    echo "📂 Đang gắn (mount) $BTRFS_DEV vào $MOUNT_DIR..." >&2
    if [ "$USE_LOOPBACK" = true ]; then
        run_cmd mount -t btrfs -o loop,defaults "$BTRFS_DEV" "$MOUNT_DIR"
    else
        run_cmd mount -t btrfs -o defaults "$BTRFS_DEV" "$MOUNT_DIR"
    fi
    echo "✅ Đã mount thành công vào $MOUNT_DIR!" >&2
else
    echo "ℹ️  $MOUNT_DIR đã được mount từ trước." >&2
fi

# Ownership: the Ansible-driven path chowns each per-node subdirectory itself once it
# creates them (see roles/node_setup/tasks/main.yml), so this is a no-op there in
# practice. It matters for the standalone/systemd/manual invocation path, which has no
# such follow-up step -- restores the ownership this script used to set unconditionally
# before this rewrite. Skipped gracefully (not a hard failure) if the metanode user/group
# doesn't exist yet, matching this script's existing "don't block on things a later step
# will fix" philosophy (see the other `|| true` guards above).
if id -u metanode >/dev/null 2>&1; then
    run_cmd chown metanode:metanode "$MOUNT_DIR" 2>/dev/null || true
fi

# 5. Cập nhật /etc/fstab để tự động mount khi khởi động lại
if [ "$USE_LOOPBACK" = true ]; then
    FSTAB_OPTS="loop,defaults,nofail"
else
    FSTAB_OPTS="defaults,nofail"
fi
FSTAB_LINE="$BTRFS_DEV $MOUNT_DIR btrfs $FSTAB_OPTS 0 0"

# Xóa cấu hình fstab cũ cho MOUNT_DIR nếu có để tránh trùng lặp
if [ -f /etc/fstab ]; then
    run_cmd sed -i "\# $MOUNT_DIR #d" /etc/fstab 2>/dev/null || true
    echo "$FSTAB_LINE" | run_cmd tee -a /etc/fstab >/dev/null
    echo "⚙️  Đã cập nhật cấu hình tự động mount vào /etc/fstab." >&2
fi

echo "===========================================================" >&2
echo "🎉 HOÀN TẤT THIẾT LẬP BTRFS SNAPSHOT STORAGE!" >&2
echo "   Thiết bị: $BTRFS_DEV" >&2
echo "   Mount tại: $MOUNT_DIR" >&2
echo "===========================================================" >&2

# In đường dẫn thiết bị ra stdout dòng cuối cùng cho caller (Ansible) sử dụng
echo "$BTRFS_DEV"

