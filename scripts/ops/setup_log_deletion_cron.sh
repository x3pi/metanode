#!/bin/bash

echo "======================================================"
echo " Script làm rỗng log định kỳ (chạy trực tiếp)        "
echo " Script sẽ xóa nội dung các file *.log trong thư mục:"
echo "   /mnt/Data/NNNNNNN/core/mtn-simple-6.1/logs"
echo "        mỗi 10 phút. Nhấn Ctrl+C để dừng.           "
echo "======================================================"
echo

# --- Phần cấu hình ---
log_dir_path="/mnt/Data/NNNNNNN/core/mtn-simple-6.1/logs"

echo "Sử dụng đường dẫn log cố định: $log_dir_path"
echo

# Kiểm tra tồn tại và quyền truy cập
if [[ ! -e "$log_dir_path" ]]; then
  echo "Lỗi: Đường dẫn '$log_dir_path' không tồn tại."
  exit 1
elif [[ ! -d "$log_dir_path" ]]; then
  echo "Lỗi: '$log_dir_path' không phải là thư mục."
  exit 1
fi

if [[ ! -w "$log_dir_path" || ! -x "$log_dir_path" ]]; then
  echo "Lỗi: Không đủ quyền ghi và/hoặc thực thi trên thư mục '$log_dir_path'."
  exit 1
else
  echo "Đã xác nhận quyền truy cập thư mục."
fi
echo

# --- Hàm làm rỗng log ---
empty_log_command() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] Đang làm rỗng các file *.log trong '$log_dir_path'..."

  /usr/bin/find "$log_dir_path" -name '*.log' -type f -exec sh -c ': > "$1"' _ {} \;
  local exit_code=$?

  if [ $exit_code -ne 0 ]; then
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] CẢNH BÁO: Có lỗi xảy ra khi làm rỗng log (mã lỗi $exit_code)."
  else
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] Đã làm rỗng log thành công."
  fi
}

# --- Vòng lặp chính ---
# echo "Bắt đầu vòng lặp làm rỗng log mỗi 10 phút."
# echo "Nhấn Ctrl+C để dừng script bất cứ lúc nào."
# echo "------------------------------------------------------"

# while true; do
#   empty_log_command
#   echo "[$(date '+%Y-%m-%d %H:%M:%S')] Chờ 10 phút (600 giây) trước lần tiếp theo..."
#   echo "------------------------------------------------------"
#   sleep 600
# done

# echo "Script đã dừng."
# exit 0
