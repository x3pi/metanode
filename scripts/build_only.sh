#!/bin/bash

# ======================================================
#       Script Build Go Đơn Giản
#   Chỉ build mã nguồn Go và đặt file thực thi vào build_app.
#   Yêu cầu thư mục build_app phải tồn tại trước.
#   KHÔNG xóa build_app khi lỗi.
#   KHÔNG sao chép file cấu hình hay tạo run.sh.
# ======================================================
set -e # Thoát ngay lập tức nếu có lệnh nào thất bại.

# --- Cấu hình Người dùng ---
# 1. Thư mục chứa code Go (tương đối so với script này)
CHAIN_DIR="cmd/simple_chain"

# 2. Tên file thực thi sau khi build (Để trống sẽ dùng tên thư mục CHAIN_DIR)
GO_BINARY_NAME="" # Ví dụ: "my_chain_app"

# 3. Tên thư mục ĐÃ TỒN TẠI để chứa file thực thi kết quả
BUILD_DIR="build_app"
# --- Kết thúc Cấu hình Người dùng ---

# --- Logic Script ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR_ABS="$SCRIPT_DIR/$BUILD_DIR"
CHAIN_DIR_ABS="$SCRIPT_DIR/$CHAIN_DIR"

echo "--- 🛠️ Bắt đầu Quá trình Build Đơn Giản ---"
echo "Thư mục gốc dự án (Script Dir): $SCRIPT_DIR"
echo "Thư mục mã nguồn Go: $CHAIN_DIR_ABS"
echo "Thư mục đích (phải tồn tại): $BUILD_DIR_ABS"

# --- 1. Kiểm tra cài đặt Go ---
echo -n "🔍 Kiểm tra lệnh 'go'... "
if ! command -v go &> /dev/null; then
    echo "❌ Lỗi: Không tìm thấy lệnh 'go'. Vui lòng cài đặt Go (https://go.dev/doc/install)."
    exit 1
fi
echo "✅ Đã tìm thấy."

# --- 2. Xác định tên file thực thi Go ---
if [ -z "$GO_BINARY_NAME" ]; then
    # Nếu GO_BINARY_NAME trống, lấy tên từ thư mục CHAIN_DIR
    GO_BINARY_NAME=$(basename "$CHAIN_DIR")
    echo "ℹ️ GO_BINARY_NAME không được đặt, sử dụng tên thư mục: '$GO_BINARY_NAME'"
fi
# Đường dẫn đầy đủ đến file thực thi đích
TARGET_BINARY_FULL_PATH="$BUILD_DIR_ABS/$GO_BINARY_NAME"

# --- 3. Kiểm tra thư mục mã nguồn Go có tồn tại không ---
if [ ! -d "$CHAIN_DIR_ABS" ]; then
    echo "⛔ Lỗi: Không tìm thấy thư mục mã nguồn Go: '$CHAIN_DIR_ABS'" >&2
    exit 1
fi
 echo "✅ Tìm thấy thư mục mã nguồn Go."

# --- 4. Kiểm tra thư mục đích BUILD_DIR có tồn tại không ---
# Script này yêu cầu thư mục đích phải tồn tại trước
if [ ! -d "$BUILD_DIR_ABS" ]; then
    echo "⛔ Lỗi: Thư mục đích '$BUILD_DIR_ABS' không tồn tại." >&2
    echo "   Script này yêu cầu thư mục đích phải được tạo trước khi chạy." >&2
    exit 1
fi
echo "✅ Thư mục đích '$BUILD_DIR_ABS' tồn tại."

# --- 5. Build ứng dụng Go ---
echo "🚀 Build ứng dụng Go từ '$CHAIN_DIR_ABS'..."
echo "   File đích: '$TARGET_BINARY_FULL_PATH'"

# Chuyển vào thư mục mã nguồn để thực hiện build
# Lệnh 'go build' thường hoạt động tốt nhất khi chạy từ bên trong thư mục chứa mã nguồn
pushd "$CHAIN_DIR_ABS" > /dev/null

# Thực hiện build, output trực tiếp vào thư mục đích
# Lưu ý: Lệnh build này sẽ ghi đè file thực thi cũ nếu nó tồn tại trong build_app
if go build -o "$TARGET_BINARY_FULL_PATH" .; then
    # Quay lại thư mục ban đầu
    popd > /dev/null
    echo "✅ Build ứng dụng Go thành công: '$TARGET_BINARY_FULL_PATH'"

    # --- 6. Xác minh file thực thi đã được tạo ---
    # Mặc dù go build báo thành công, kiểm tra lại cho chắc chắn
    if [ ! -f "$TARGET_BINARY_FULL_PATH" ]; then
         echo "⛔ Lỗi: Build báo thành công, nhưng không tìm thấy file đích '$TARGET_BINARY_FULL_PATH'!" >&2
         # KHÔNG xóa build_app
         exit 1
    fi
     echo "✅ Đã xác minh file thực thi tại '$TARGET_BINARY_FULL_PATH'."

else
    # Lấy mã lỗi từ lệnh go build
    build_exit_code=$?
    # Quay lại thư mục ban đầu
    popd > /dev/null
    echo "⛔ Build Go thất bại (mã lỗi: $build_exit_code)." >&2
    # Quan trọng: KHÔNG xóa thư mục build_app ở đây
    exit $build_exit_code # Thoát với mã lỗi của go build
fi

echo -e "\n--- 🎉 Build Đơn Giản Hoàn Tất! ---"
echo "File thực thi '$GO_BINARY_NAME' đã được đặt trong '$BUILD_DIR_ABS'."
echo "Không có file cấu hình nào được sao chép hoặc file run.sh nào được tạo."
echo "Thư mục '$BUILD_DIR_ABS' không bị sửa đổi gì ngoài việc thêm/cập nhật file thực thi."
echo "---------------------------------"
exit 0