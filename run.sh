#!/bin/bash
# --- ====================================================== ---
# --- Cấu hình Người dùng                                     ---
# --- Script này CHUẨN BỊ môi trường, BUILD code Go và      ---
# --- RUN các node xử lý snapshot                          ---
# --- ====================================================== ---
# !!! QUAN TRỌNG: Chỉnh sửa các biến dưới đây cho phù hợp !!!

# 1. Thư mục chứa code Go chain (tương đối so với vị trí script)
CHAIN_DIR="cmd/simple_chain"

# 2. Tên file thực thi sau khi build (Để trống sẽ dùng tên thư mục CHAIN_DIR)
GO_BINARY_NAME="" # Ví dụ: "my_chain_app"

# 3. Cấu hình Rsync (Nguồn dữ liệu gốc cho snapshot)
RSYNC_MASTER_DATA_SRC="sample/simple/data/data/" # Thư mục nguồn tương đối

# 5. Tên và điểm gắn kết snapshot (Base name và Mount point)
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
SNAPSHOT_NAME_BASE="chain_snap_${TIMESTAMP}"        # Go sẽ dùng làm tiền tố
SNAPSHOT_MOUNT_POINT_BASE="sample/simple/data/data_snap" # Tên thư mục tương đối cho mount point

# 6. Đường dẫn log
LOG_DIR="./node_logs"


# --- ========================== ---
# --- Kết thúc Cấu hình Người dùng ---
# --- ========================== ---

# --- Khai báo biến và hàm ---
PIDS=()
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR_ABS="$SCRIPT_DIR/$LOG_DIR"
SNAPSHOT_MOUNT_POINT_ABS="$SCRIPT_DIR/$SNAPSHOT_MOUNT_POINT_BASE" # Đường dẫn tuyệt đối cho mount point
RSYNC_MASTER_DATA_SRC_ABS="$SCRIPT_DIR/$RSYNC_MASTER_DATA_SRC" # Đường dẫn tuyệt đối cho nguồn rsync
GO_BINARY_PATH="" # Sẽ được đặt sau khi build thành công

# --- Các hàm Helper (check_and_install_tools, cleanup, handle_exit_signals) ---
check_and_install_tools() {
    echo "🔍 Kiểm tra các công cụ cần thiết..."
    tools=("rsync" "go")
    missing_tools=()
    for tool in "${tools[@]}"; do
        if ! command -v "$tool" &> /dev/null; then
             if ! dpkg-query -W -f='${Status}' "$tool" 2>/dev/null | grep -q "ok installed"; then
                 if [[ "$tool" == "go" ]] && dpkg-query -W -f='${Status}' "golang-go" 2>/dev/null | grep -q "ok installed"; then
                     continue
                 fi
                 missing_tools+=("$tool")
             fi
        fi
    done

    if [ ${#missing_tools[@]} -gt 0 ]; then
        echo "📦 Cần cài đặt: ${missing_tools[*]}"
        if ! command -v apt-get &> /dev/null; then
            echo "⛔ Lỗi: Không tìm thấy apt-get. Vui lòng cài đặt thủ công: ${missing_tools[*]}" >&2
            exit 1
        fi
        echo "🔄 Đang cập nhật danh sách gói..."
        sudo apt-get update -qq
        for tool in "${missing_tools[@]}"; do
            install_package=$tool
            if [[ "$tool" == "go" ]]; then
                 echo "---"
                 echo "⚠️ Go chưa được cài đặt. Phiên bản trong apt có thể cũ."
                 echo "   Bạn nên cài đặt Go theo hướng dẫn chính thức tại: https://go.dev/doc/install"
                 echo "   Hoặc thử cài bằng apt (có thể không phải bản mới nhất):"
                 echo "   sudo apt-get install -y golang-go"
                 echo "---"
                 read -p "Bạn có muốn thử cài Go bằng apt không? (y/N): " confirm_go
                 if [[ "$confirm_go" =~ ^[Yy]$ ]]; then
                     install_package="golang-go"
                 else
                     echo "⛔ Vui lòng cài đặt Go thủ công và chạy lại script." >&2
                     exit 1
                 fi
            fi
            echo "📥 Đang cài đặt $install_package..."
            sudo apt-get install -y "$install_package"
            if [ $? -ne 0 ]; then
                echo "⛔ Lỗi: Không thể cài đặt $install_package. Vui lòng cài đặt thủ công." >&2
                exit 1
            fi
        done
        if command -v go &> /dev/null; then
             echo "✅ Go đã được cài đặt."
        else
             echo "⛔ Không tìm thấy lệnh 'go' sau khi cài đặt. Vui lòng kiểm tra lại." >&2
             exit 1
        fi
    fi
    echo "✅ Tất cả công cụ cần thiết đã sẵn sàng."
}



cleanup() {
    echo -e "\n🧹 Bắt đầu dọn dẹp (Chỉ dừng tiến trình Go)..."
    if [ ${#PIDS[@]} -gt 0 ]; then
        echo "🛑 Đang gửi tín hiệu dừng (SIGTERM) tới các node Go..."
        kill -TERM "${PIDS[@]}" 2>/dev/null # Gửi SIGTERM

        echo "⏳ Chờ các node dừng (tối đa 5 giây)..."
        local wait_time=5
        local end_time=$((SECONDS + wait_time))
        local pids_running=("${PIDS[@]}") # Copy mảng PIDS

        # Vòng lặp chờ và kiểm tra
        while [ $SECONDS -lt $end_time ] && [ ${#pids_running[@]} -gt 0 ]; do
            local still_running=()
            for pid in "${pids_running[@]}"; do
                # Kiểm tra xem PID có còn chạy không
                if ps -p "$pid" > /dev/null; then
                    still_running+=("$pid")
                fi
            done
            pids_running=("${still_running[@]}") # Cập nhật danh sách PID còn chạy
            if [ ${#pids_running[@]} -gt 0 ]; then
                 sleep 0.5;
            fi # Đợi một chút trước khi kiểm tra lại
        done

        # Nếu sau thời gian chờ vẫn còn PID chạy
        if [ ${#pids_running[@]} -gt 0 ]; then
            echo "   ⚠️ Các PID sau chưa dừng sau ${wait_time} giây, gửi SIGKILL: ${pids_running[*]}"
            kill -KILL "${pids_running[@]}" 2>/dev/null # Gửi SIGKILL nếu cần
        else
            echo "   ✅ Tất cả các node Go đã dừng (hoặc đã dừng trước đó)."
        fi
        PIDS=() # Xóa danh sách PID sau khi đã dừng
    else
        echo "ℹ️ Không có PID của node Go nào được ghi nhận để dừng."
    fi

    echo "ℹ️ Việc dọn dẹp snapshot (nếu có) sẽ do tiến trình Go xử lý."
    # Cân nhắc: Thêm tùy chọn xóa file binary đã build ở đây nếu muốn
    # if [ -n "$GO_BINARY_PATH" ] && [ -f "$GO_BINARY_PATH" ]; then
    #     echo "   🗑️ Xóa file thực thi Go: $GO_BINARY_PATH"
    #     rm -f "$GO_BINARY_PATH"
    # fi
    echo "✅ Dọn dẹp (chỉ dừng process) hoàn tất."
}

handle_exit_signals() {
    local signal_type=$1 # INT or TERM or EXIT
    echo -e "\n🚨 Nhận tín hiệu $signal_type..."
    if [[ -z "$CLEANUP_CALLED" ]]; then
        export CLEANUP_CALLED=true
        cleanup
    fi
    echo "✅ Script đã dừng."
    # Chỉ exit nếu là INT hoặc TERM để tránh exit 0 từ trap EXIT khi đã bị ngắt
    if [[ "$signal_type" == "INT" ]] || [[ "$signal_type" == "TERM" ]]; then
        # Thoát với mã lỗi chuẩn cho Ctrl+C
        exit 130
    fi
    # Nếu là EXIT và chưa bị ngắt, trap sẽ kết thúc tự nhiên
}


# --- Logic chính của Script ---
trap 'handle_exit_signals INT' INT
trap 'handle_exit_signals TERM' TERM
trap 'handle_exit_signals EXIT' EXIT

cd "$SCRIPT_DIR"
echo "🏠 Đang chạy từ thư mục: $SCRIPT_DIR"

check_and_install_tools

mkdir -p "$LOG_DIR_ABS" || { echo "⛔ Lỗi tạo thư mục log '$LOG_DIR_ABS'."; exit 1; }
echo "   Thư mục log '$LOG_DIR_ABS' đã sẵn sàng."

CHAIN_DIR_ABS="$SCRIPT_DIR/$CHAIN_DIR" # Đường dẫn tuyệt đối tới thư mục chain
if [ ! -d "$CHAIN_DIR_ABS" ]; then
    echo "⛔ Lỗi: Thư mục mã nguồn '$CHAIN_DIR' ('$CHAIN_DIR_ABS') không tồn tại!" >&2
    exit 1
fi

# --- 1. Cấu hình và Chuẩn bị Môi trường Snapshot (Rsync) ---
echo -e "\n--- ⚙️ Chuẩn bị Môi trường Snapshot (Rsync) cho Go ---"

FINAL_COMPRESS_ENABLE_SNAPSHOT="false"
FINAL_COMPRESS_SNAPSHOT_CREATE_CMD=""
FINAL_COMPRESS_SNAPSHOT_MOUNT_CMD=""
FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT=""
FINAL_COMPRESS_SNAPSHOT_CLEANUP_CMD=""
FINAL_SNAPSHOT_NAME_BASE=""
FINAL_RSYNC_MASTER_DATA_SRC_ABS=""

echo "📦 Cấu hình rsync snapshot cho Go."
echo "   Nguồn Rsync (Tuyệt đối): '$RSYNC_MASTER_DATA_SRC_ABS'"
echo "   Đích Rsync (Tuyệt đối = Mount Point): '$SNAPSHOT_MOUNT_POINT_ABS'"

if [ ! -d "$RSYNC_MASTER_DATA_SRC_ABS" ]; then
    echo "⚠️ Thư mục nguồn rsync '$RSYNC_MASTER_DATA_SRC_ABS' không tồn tại. Sẽ không bật snapshot." >&2
    FINAL_COMPRESS_ENABLE_SNAPSHOT="false"
else
    CREATE_CMD_STR="echo '⏳ (Go-executing) Creating rsync snapshot at $SNAPSHOT_MOUNT_POINT_ABS...' && rm -rf '$SNAPSHOT_MOUNT_POINT_ABS' && mkdir -p '$SNAPSHOT_MOUNT_POINT_ABS' && rsync -a --delete '$RSYNC_MASTER_DATA_SRC_ABS/' '$SNAPSHOT_MOUNT_POINT_ABS/' || { echo '⛔ (Go-executed) FATAL: Rsync failed!' >&2; exit 1; }"
    MOUNT_CMD_STR="echo 'ℹ️ (Go-executed) Rsync snapshot creation finished for $SNAPSHOT_MOUNT_POINT_ABS.'"
    CLEANUP_CMD_STR="echo '🧹 (Go-executing) Cleaning up rsync snapshot at $SNAPSHOT_MOUNT_POINT_ABS...' && rm -rf '$SNAPSHOT_MOUNT_POINT_ABS' && echo '✅ (Go-executed) Rsync cleanup finished for $SNAPSHOT_MOUNT_POINT_ABS.'"

    FINAL_COMPRESS_ENABLE_SNAPSHOT="true"
    FINAL_COMPRESS_SNAPSHOT_CREATE_CMD="$CREATE_CMD_STR"
    FINAL_COMPRESS_SNAPSHOT_MOUNT_CMD="$MOUNT_CMD_STR"
    FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT="$SNAPSHOT_MOUNT_POINT_ABS"
    FINAL_COMPRESS_SNAPSHOT_CLEANUP_CMD="$CLEANUP_CMD_STR"
    FINAL_SNAPSHOT_NAME_BASE="$SNAPSHOT_NAME_BASE"
    FINAL_RSYNC_MASTER_DATA_SRC_ABS="$RSYNC_MASTER_DATA_SRC_ABS"

    echo "   Đã chuẩn bị rsync snapshot TEMPLATES."
fi

# --- 1.5. Build Ứng dụng Go ---
echo -e "\n--- 🔨 Build Ứng dụng Go ---"

# Xác định tên file thực thi
if [ -z "$GO_BINARY_NAME" ]; then
    GO_BINARY_NAME=$(basename "$CHAIN_DIR")
    echo "ℹ️ Tên file thực thi không được cấu hình, sử dụng tên thư mục: '$GO_BINARY_NAME'"
fi
GO_BINARY_PATH="$CHAIN_DIR_ABS/$GO_BINARY_NAME" # Đường dẫn tuyệt đối tới file thực thi

echo "   Thư mục mã nguồn Go: '$CHAIN_DIR_ABS'"
echo "   Tên file thực thi đích: '$GO_BINARY_NAME'"
echo "   Đường dẫn file thực thi đích: '$GO_BINARY_PATH'"

# Lưu thư mục hiện tại và cd vào thư mục chain
pushd "$CHAIN_DIR_ABS" > /dev/null || { echo "⛔ Lỗi cd vào '$CHAIN_DIR_ABS'."; exit 1; }
echo "   🚀 Bắt đầu build trong thư mục: $(pwd)"

# Thực hiện build
go build -o "$GO_BINARY_NAME" .
build_exit_code=$?

# Quay lại thư mục gốc
popd > /dev/null

# Kiểm tra kết quả build
if [ $build_exit_code -ne 0 ]; then
    echo "⛔ Lỗi build ứng dụng Go (exit code: $build_exit_code). Xem lại output phía trên." >&2
    exit 1
fi

if [ ! -f "$GO_BINARY_PATH" ]; then
    echo "⛔ Lỗi: Build có vẻ thành công nhưng không tìm thấy file thực thi tại '$GO_BINARY_PATH'." >&2
    exit 1
fi

echo "✅ Build thành công! File thực thi: '$GO_BINARY_PATH'"


# --- 2. Export biến môi trường cho Go và Khởi động Nodes ---
echo -e "\n--- 🚀 Khởi động các Node Go trong nền (từ file đã build) ---"

# *** EXPORT TRỰC TIẾP CÁC BIẾN FINAL_ TRƯỚC KHI CHẠY NODE ***
export COMPRESS_ENABLE_SNAPSHOT="$FINAL_COMPRESS_ENABLE_SNAPSHOT"
export COMPRESS_SNAPSHOT_CREATE_CMD="$FINAL_COMPRESS_SNAPSHOT_CREATE_CMD"
export COMPRESS_SNAPSHOT_MOUNT_CMD="$FINAL_COMPRESS_SNAPSHOT_MOUNT_CMD"
export COMPRESS_SNAPSHOT_MOUNT_POINT="$FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT"
export COMPRESS_SNAPSHOT_CLEANUP_CMD="$FINAL_COMPRESS_SNAPSHOT_CLEANUP_CMD"
export SNAPSHOT_NAME_BASE="$FINAL_SNAPSHOT_NAME_BASE"

if [ "$FINAL_COMPRESS_ENABLE_SNAPSHOT" = "true" ]; then
    export RSYNC_MASTER_DATA_SRC_ABS="$FINAL_RSYNC_MASTER_DATA_SRC_ABS"
    echo "   Exported Rsync snapshot variables for Go."
else
    echo "   Snapshot không được bật, không export biến Rsync."
fi
# echo "DEBUG: CREATE_CMD exported: $COMPRESS_SNAPSHOT_CREATE_CMD" # Debug nếu cần

echo "   Sử dụng file thực thi Go: $GO_BINARY_PATH"

# Hàm khởi động node
start_node() {
    local node_name=$1
    local config_file=$2
    local log_file=$3

    echo "   🚀 Khởi động $node_name Node (Log: $log_file)..."
    touch "$log_file" || { echo "⛔ Lỗi chạm vào file log '$log_file'!" >&2; return 1; }
    chmod u+w "$log_file" || { echo "⛔ Lỗi chmod cho file log '$log_file'!" >&2; return 1; }

    (
        cd "$CHAIN_DIR_ABS" || {
            echo "$(date +%Y-%m-%d_%H%M%S) --- $node_name Node Startup Error ---" >> "$log_file"
            echo "$(date +%Y-%m-%d_%H%M%S): ⛔ Lỗi cd vào thư mục '$CHAIN_DIR_ABS'" >> "$log_file"
            exit 1
         }

        echo "$(date +%Y-%m-%d_%H%M%S): --- $node_name Node Log ---" > "$log_file"
        echo "$(date +%Y-%m-%d_%H%M%S): Chạy từ thư mục: $(pwd)" >> "$log_file"
        echo "$(date +%Y-%m-%d_%H%M%S): File thực thi: $GO_BINARY_PATH" >> "$log_file"
        # Xapian path tự động đọc từ Databases.XapianPath trong config.json
        # App tự tạo thư mục, không cần export XAPIAN_BASE_PATH

        echo "$(date +%Y-%m-%d_%H%M%S): Inherited COMPRESS_ENABLE_SNAPSHOT=$COMPRESS_ENABLE_SNAPSHOT" >> "$log_file"
        echo "$(date +%Y-%m-%d_%H%M%S): Inherited COMPRESS_SNAPSHOT_MOUNT_POINT=$COMPRESS_SNAPSHOT_MOUNT_POINT" >> "$log_file"
        echo "$(date +%Y-%m-%d_%H%M%S): Inherited SNAPSHOT_NAME_BASE=$SNAPSHOT_NAME_BASE" >> "$log_file"

        local cmd_to_run="./$GO_BINARY_NAME -config=$config_file"
        echo "$(date +%Y-%m-%d_%H%M%S): Thực thi: $cmd_to_run" >> "$log_file"
        exec $cmd_to_run >> "$log_file" 2>&1
        echo "$(date +%Y-%m-%d_%H%M%S): ⛔ Lỗi exec: $cmd_to_run" >> "$log_file"
        exit 1
    ) &
    PIDS+=($!)
    echo "   PID cho $node_name: ${PIDS[${#PIDS[@]}-1]}"
}

# Khởi động các node (Xapian path tự đọc từ Databases.XapianPath trong config)
start_node "MASTER"  "config-master.json"       "$LOG_DIR_ABS/master.log"
sleep 1
start_node "WRITE 1" "config-sub-write.json"   "$LOG_DIR_ABS/write1.log"
sleep 1
start_node "WRITE 2" "config-sub-write-2.json" "$LOG_DIR_ABS/write2.log"


# --- 3. Giữ Script Chính Chạy & Thông báo ---
echo -e "\n--- ✅ Script Chính Đang Chạy ---"
echo "   File thực thi Go đã được build tại: $GO_BINARY_PATH"
echo "   Các node Go đã được khởi động trong nền từ file build."
echo "   PIDs của các node: ${PIDS[*]}"

# Sử dụng giá trị FINAL_ để hiển thị thông tin đúng
if [ "$FINAL_COMPRESS_ENABLE_SNAPSHOT" = "true" ]; then
    echo "   ✅ Rsync snapshot TEMPLATES đã được export cho Go."
    echo "      Đường dẫn mount point snapshot: $FINAL_COMPRESS_SNAPSHOT_MOUNT_POINT"
else
    echo "   ℹ️ Snapshot không được bật (COMPRESS_ENABLE_SNAPSHOT=false)."
fi

echo -e "\n👀 Để theo dõi output của các node, sử dụng lệnh 'tail -f' trong terminal khác:"
echo "   tail -f $LOG_DIR_ABS/master.log"
echo "   tail -f $LOG_DIR_ABS/write1.log"
echo "   tail -f $LOG_DIR_ABS/write2.log"
echo -e "\n👉 Nhấn Ctrl+C để dừng script chính và gửi tín hiệu dừng tới các node Go."

# Vòng lặp giữ script chạy và kiểm tra trạng thái node
while true; do
    all_running=true
    pids_to_check=("${PIDS[@]}") # Tạo bản sao để tránh race condition nếu PIDS thay đổi
    if [ ${#pids_to_check[@]} -eq 0 ]; then
         echo -e "\n--- ℹ️ Thông báo ($(date +"%Y-%m-%d %H:%M:%S")) ---"
         echo "   Không có PID nào được theo dõi. Script sẽ thoát."
         exit 0 # Thoát bình thường khi không còn node nào
    fi

    for pid_index in "${!pids_to_check[@]}"; do
        pid=${pids_to_check[$pid_index]}
        if ! ps -p "$pid" > /dev/null; then
            node_name="Node (PID $pid)" # Tên mặc định
            # Cố gắng xác định tên node từ index PID (chỉ là ước lượng)
            if [ "$pid_index" -eq 0 ]; then node_name="MASTER Node (PID $pid)";
            elif [ "$pid_index" -eq 1 ]; then node_name="WRITE 1 Node (PID $pid)";
            elif [ "$pid_index" -eq 2 ]; then node_name="WRITE 2 Node (PID $pid)";
            fi

            echo -e "\n--- ⚠️ Cảnh báo ($(date +"%Y-%m-%d %H:%M:%S")) ---"
            echo "   ❌ $node_name đã dừng!"
            # Xóa PID đã dừng khỏi danh sách gốc PIDS để không kiểm tra lại
            # Cần tìm index của PID trong mảng PIDS gốc
            original_indices_to_remove=()
            for i in "${!PIDS[@]}"; do
                if [[ "${PIDS[$i]}" == "$pid" ]]; then
                    original_indices_to_remove+=("$i")
                fi
            done
            # Xóa từ cuối lên để tránh thay đổi index
             for (( idx=${#original_indices_to_remove[@]}-1 ; idx>=0 ; idx-- )) ; do
                 unset "PIDS[${original_indices_to_remove[$idx]}]"
            done
             PIDS=("${PIDS[@]}") # Sắp xếp lại mảng PIDS

            all_running=false
            # Không thoát script chính ở đây, chỉ thông báo
        fi
    done

    if $all_running; then
        # echo -n "." # Bỏ comment nếu muốn thấy dấu chấm định kỳ
        sleep 60
    else
        echo "   Một hoặc nhiều node đã dừng. Script chính sẽ tiếp tục chạy và theo dõi các node còn lại."
        echo "   Các node còn lại đang chạy (nếu có): ${PIDS[*]}"
        echo "   Nhấn Ctrl+C để dừng hoàn toàn."
        sleep 60 # Kiểm tra thường xuyên hơn khi có node dừng
    fi
done