# Tiêu Chí Đăng Ký Trước (Pre-registered Criteria): Kiểm Chứng Bộ Nhớ Bounded Mapping Cache

**Ngày lập:** 2026-10-08  
**Tài liệu tham chiếu:** `note/plan_bounded_cache_followup_gemini_20261008.md`, `note/design_bounded_memory_indexes_20261007.md`  
**Cam kết:** Tiêu chí này được ghi nhận và commit vào Git TRƯỚC KHI chạy thực nghiệm đo đạc. Tuyệt đối không thay đổi tiêu chí sau khi quan sát kết quả thực tế.

---

## 1. Thiết Lập Thực Nghiệm (Experiment Setup)

- **Cụm thử nghiệm:** Cụm 4 validator (`val0`..`val3`) cô lập, tái tạo từ template sạch `/tmp/gate_4val_clean_template` trước mỗi lượt chạy (wipe sạch dữ liệu đĩa).
- **Cấu hình môi trường:**
  * `ENABLE_DEBUG_PPROF=true` (kích hoạt HTTP listener debug trên pprof ports 31446..31449).
  * Binary thực thi: `/tmp/p06_bins/simple_chain` đã biên dịch logic Two-Generation Map (commit `4221d60a` / `5fb5f641`).
- **Tải tiêm (Workload):**
  * Công cụ tiêm: `/tmp/b1_bins/secp_tps_blast`.
  * Tốc độ: Cố định 50 tx/s liên tục trong suốt thời gian đo.
  * Chế độ: TCP stream, batch 100 txs, EIP-1559 type 2 transactions.
- **Thời lượng đo:** 45 phút (2,700 giây) cho mỗi lượt chạy.
- **Tần suất thu thập mẫu:** Mỗi 60 giây (tổng cộng ~45 mẫu/lượt).
- **Nguồn dữ liệu:**
  * HeapAlloc / HeapInuse: Đọc trực tiếp từ `/debug/pprof/heap?debug=1&gc=1` (ép GC trước khi đo để loại trừ rác chưa thu hồi).
  * VmRSS: Đọc trực tiếp từ `/proc/<pid>/status`.
- **Số lượt chạy tối thiểu:** Ít nhất 2 lượt chạy độc lập (Run 1 và Run 2) để kiểm tra độ biến thiên.
- **Heap Profile:** Thu thập snapshot `pprof heap` vào phút thứ 10, phút thứ 30 và phút thứ 45 ở mỗi lượt chạy.

---

## 2. Tiêu Chí Đánh Giá Bão Hoà Định Lượng (Quantitative Acceptance Criteria)

Dữ liệu chuỗi thời gian của tổng `HeapAlloc` toàn cụm được phân chia làm hai cửa sổ:
- **Cửa sổ 1 ($W_1$):** 0 đến 30 phút (0s đến 1,800s).
- **Cửa sổ 2 ($W_2$):** 30 đến 45 phút (1,800s đến 2,700s).

Hệ số góc hồi quy tuyến tính (slope) được tính bằng hàm hồi quy chuẩn `stats_util.py` theo đơn vị **MB/phút**, kèm theo hệ số tương quan $R^2$.

### Tiêu chí kết luận:
1. **PASS (BÃO HOÀ / BOUNDED):**
   - Hệ số góc Cửa sổ 2: $W_2 \le 0.5 \text{ MB/phút}$
   - HOẶC tỷ lệ tăng trưởng: $W_2 \le 5\% \times W_1$.
2. **FAIL (CHƯA BÃO HOÀ / UNBOUNDED):**
   - $W_2 > 0.5 \text{ MB/phút}$ VÀ $W_2 > 5\% \times W_1$.
3. **Tiêu chí Heap Profile (`go tool pprof -top`):**
   - `ethHashMapBlsHashMap.Store*` và `txHashToBlockNumberMap.Store*` KHÔNG còn nằm trong danh sách các vị trí cấp phát tích lũy hàng đầu (top allocators) tại các mốc phút 10, 30, 45.

---

## 3. Quy Trình Xử Lý Kết Quả
- Kết quả đo đạc thật sẽ được báo cáo trung thực, kể cả khi FAIL.
- Nếu FAIL: Giữ nguyên tiêu chí, phân tích thành phần nào khác trên heap đang chiếm dụng bộ nhớ (ví dụ `txsCache`, `mheap arenas`, v.v.).
- Nếu chưa hoàn tất đủ 45 phút × 2 lượt: Giữ trạng thái trong tài liệu thiết kế là `"Implemented — memory verification pending"`.
