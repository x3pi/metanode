import re
import collections
import statistics

# --- Cấu hình ---
# Đường dẫn đến file log của bạn
LOG_FILE_NAME = "fileTimeLogger.log"
# Số lượng kết quả top cần hiển thị
TOP_N = 5
# --- Hết Cấu hình ---


def to_ms(value, unit):
    """Chuyển đổi các đơn vị thời gian sang miligiây (ms)"""
    if unit == 's': # <<< THAY ĐỔI: Thêm xử lý cho giây
        return value * 1000.0
    if unit == 'ms':
        return value
    if unit == 'µs':
        return value / 1000.0
    if unit == 'ns':
        return value / 1000000.0
    return value  # Mặc định

# Các metric cần tìm
target_metrics = [
    "Xác thực Merkle Proof (OK)",
    "Tổng thời gian xử lý",
    "Cập nhật counter và kiểm tra hoàn thành",
    "Gửi chunk (sendChunk)"
]

# Cấu trúc regex để trích xuất
# Group 1: Key
# Group 2: Metric
# Group 3: Value (số)
# Group 4: Unit (đơn vị)
# <<< THAY ĐỔI: Regex đã được sửa để xử lý đúng trường hợp không có khoảng trắng
pattern = re.compile(
    r'-k ([a-f0-9]+)\].*?(' + '|'.join(re.escape(m) for m in target_metrics) + r'): ([\d\.]+)\s*([a-zA-Zµs]+)'
)

# Dùng defaultdict để lưu trữ tất cả các lần thực thi cho mỗi metric
# Cấu trúc: all_times[metric_name] = [(key, duration_in_ms), ...]
all_times = collections.defaultdict(list)

try:
    with open(LOG_FILE_NAME, "r", encoding="utf-8") as f:
        for line in f:
            match = pattern.search(line)
            if match:
                key, metric, value_str, unit = match.groups()
                value = float(value_str)
                duration_ms = to_ms(value, unit)
                all_times[metric].append((key, duration_ms))

    # In kết quả
    print(f"--- 📊 Phân tích thời gian từ file '{LOG_FILE_NAME}' ---")

    for metric in target_metrics:
        print(f"\n## Metric: '{metric}'")

        stats = all_times.get(metric)

        if not stats:
            print("  Không tìm thấy dữ liệu cho metric này.")
            continue

        # Sắp xếp danh sách các lần thực thi theo thời gian
        stats.sort(key=lambda x: x[1])

        # Tính toán thời gian trung bình
        total_duration = sum(duration for key, duration in stats)
        average_time = total_duration / len(stats)
        print(f"  📈 Thời gian trung bình: {average_time:,.6f} ms (trên tổng số {len(stats)} lần thực thi)")

        # In ra top 5 thời gian nhanh nhất
        print(f"\n  --- 🚀 Top {TOP_N} nhanh nhất ---")
        # Đảm bảo không in nhiều hơn số lượng phần tử có trong list
        for key, duration in stats[:TOP_N]:
            print(f"    - Key: {key}, Thời gian: {duration:,.6f} ms")

        # In ra top 5 thời gian chậm nhất
        print(f"\n  --- 🐢 Top {TOP_N} chậm nhất ---")
        # Đảm bảo không in nhiều hơn số lượng phần tử có trong list
        for key, duration in reversed(stats[-TOP_N:]): # Dùng reversed để in từ chậm nhất -> chậm thứ N
            print(f"    - Key: {key}, Thời gian: {duration:,.6f} ms")


except FileNotFoundError:
    print(f"Lỗi: Không tìm thấy file '{LOG_FILE_NAME}'")
    print("Vui lòng kiểm tra lại đường dẫn và đảm bảo file tồn tại.")
except Exception as e:
    print(f"Đã xảy ra lỗi không mong muốn: {e}")