# PREREGISTERED: Kế Hoạch Đo Lường Trực Tiếp & Cải Thiện TPS Đợt 2 (2026-10-09)

**Ngày đăng ký:** 2026-10-09  
**Trạng thái:** PREREGISTERED (Commit trước khi thực hiện đo đạc vi mô và can thiệp mã nguồn)  
**Tài liệu nền:** `note/plan_tps_improve_phase2_20261009.md`

---

## 1. Mục Tiêu & Phạm Vi Đo Đạc Vi Mô

Đợt 2 tập trung vào việc **đo lường trực tiếp các thành phần thời gian và bộ đếm nội bộ** (không suy diễn định tính) để kiểm chứng 5 câu hỏi trọng tâm:
1. **Raft Sustained (Q1):** Đo trực tiếp từng thành phần thời gian trong chu kỳ block của Raft:
   - $T_{queue}$: Thời gian chờ trong `proposeQ` từ lúc nhận đến lúc được lấy ra.
   - $T_{raft}$: Thời gian `raft.Apply` (ghi log, fsync BoltDB, replicate qua mạng, chờ ACK Quorum 2/3).
   - $T_{fsm}$: Thời gian `FSM.Apply` giải mã batch và đóng gói `ExecutableBlock` (`f.st.build`).
   - $T_{exec}$: Thời gian thực thi giao dịch, tính toán state root và commit trie (NOMT flush) ở Go engine.
   - Đo tỷ lệ thời gian (%) của từng khâu trên tổng chu kỳ block.
2. **Raft Burst (Q2):** Thu thập bộ đếm nội bộ khi quét burst 20k, 25k, 30k (≥ 12 lượt cho 20k):
   - Số lần `statusFull` (từ chối do đầy `proposeQ`).
   - Số lần `splitBatch` tách nhỏ batch.
   - Số block sinh ra và phân bố số tx mỗi block.
3. **BFT Variance (Q3):** Giải thích sự khác biệt giữa hai bộ đo (n=8 gọn vs bản đo độc lập có 6,5k và 10,6k):
   - Thu thập timeline Rust/CGO (`[TIMELINE-RUST]`/`[TIMELINE-CGO]`) của các lượt nhanh và chậm.
   - Kiểm tra ảnh hưởng của: thứ tự xen kẽ, thời gian nghỉ (cooldown), tracker poll, và số round DAG certificate (2 round vs 3 round).
4. **BFT Sustained vs Burst (Q4):**
   - Đo vi sai thông lượng 10s đầu so với 50s tiếp theo trong sustained 60s để định lượng chi phí cold-start DAG.
   - Đo burst 25k có warm-up cố định vs không warm-up.
5. **Độ Trễ Phục Vụ Thực Tế (Q5):**
   - Đo độ trễ ở **tốc độ bơm cố định** (ví dụ: 5.000 tx/s và 8.000 tx/s, tương đương 50% và 75% tải bão hòa).
   - Phân biệt rõ ràng giữa `SampleConfirmed`, `SampleReverted` (thực tế revert), và `SampleUnconfirmed` (chưa xác nhận kịp).

---

## 2. Tiêu Chí Cổng Quyết Định (Decision Gates)

1. **Cổng Chuyển Tiếp Sang Cải Thiện Code (Giai đoạn 1 → Giai đoạn 2):**
   - Một khâu trong chu kỳ xử lý block phải được **chứng minh bằng số đo thực tế chiếm $\ge 10\%$ tổng chu kỳ block**.
   - Cận trên cải thiện lý thuyết theo định luật Amdahl phải đạt **$\ge +5\%$ Throughput**.
   - Nếu không thỏa mãn: Giữ nguyên mã nguồn, không tiến hành can thiệp suy diễn.

2. **Cổng Chấp Nhận Cải Tiến (Giai đoạn 3):**
   - Kiểm thử A/B xen kẽ ngẫu nhiên có seed (`seed=20261009`), cụm sạch mỗi lượt, **$\ge 8$ lượt mỗi cấu hình**.
   - Mức cải thiện trung vị phải đạt **$\ge +5\%$ Throughput**.
   - Khoảng tin cậy 95% của hiệu (Welch's t-test) **không được chứa giá trị 0**.
   - Nếu không thỏa mãn: Ghi nhận "Không xác nhận" và revert mã nguồn.

3. **Bất Biến Bất Khả Xâm Phạm:**
   - **100% Zero-Fork Invariant (`AGENTS.md` Part 2.5):** Không dùng `sleep()`, `timeout()`, hay bất kỳ cơ chế thời gian nào để quyết định dispatch commit; 100% block hash và state root khớp tuyệt đối giữa mọi node.
   - Vượt qua kiểm thử hỗn loạn `kill -9` tối thiểu 5 chu kỳ trong lúc có tải.
   - `build_check.sh` 5/5 PASSED sạch lỗi và warning.
