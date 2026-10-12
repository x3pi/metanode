# Đăng Ký Tiêu Chí Thực Nghiệm Trước Khi Đo Đạc (PREREGISTERED) — Giai Đoạn A

> **Tài liệu tham chiếu:** [note/plan_tps_improvement_impl_20261008.md](file:///home/abc/chain-n/metanode/note/plan_tps_improvement_impl_20261008.md)  
> **Ngày đăng ký:** 2026-10-08  
> **Nguyên tắc bất biến (AGENTS.md Part 2.5):** Zero-Fork Invariant (100% không fork, thà pending chứ không fork, không dùng timeout/sleep để quyết định dispatch).  
> **Cam kết:** Tiêu chí và giả thuyết được commit vào Git TRƯỚC KHI chạy benchmark; không thay đổi tiêu chí sau khi thấy kết quả.

---

## 1. Bối Cảnh & Vấn Đề Cần Giải Quyết

Báo cáo điều tra trước ([note/tps_investigation_report_20261008.md](file:///home/abc/chain-n/metanode/note/tps_investigation_report_20261008.md)) kết luận nút thắt #1 là `wait_predecessor` chiếm 44,7% thời gian. Tuy nhiên, phân tích phản biện trong [note/plan_tps_improvement_impl_20261008.md](file:///home/abc/chain-n/metanode/note/plan_tps_improvement_impl_20261008.md) chỉ ra:
1. Con số 44,7% bị **thổi phồng do hiệu ứng hàng đợi (queueing delay)** trong các đợt burst ngắn (25k txs dồn vào 3–4 block). Rust dispatch các block sang Go gần như cùng lúc, khiến block N phải chờ tổng thời gian xử lý của các block trước đó.
2. Block #1 và #2 là **khối lạnh (cold-start)** với `[SIG-ENFORCE]` tốn ~397 ms, kéo lệch giá trị trung bình.
3. Kỳ vọng tăng tốc "+40% đến +55%" là phỏng đoán, chưa có số đo cận trên Amdahl.
4. Rào cản `waitCommitted(gei-1)` là chốt chặn Zero-fork cốt lõi, không được gỡ bỏ nếu tiềm năng tăng tốc thực tế không đủ lớn để bù đắp rủi ro phức tạp hóa hệ thống.

---

## 2. Các Giả Thuyết Nghiên Cứu (Pre-registered Hypotheses)

- **H1 (Steady-state Cycle Time):** Khi chạy workload dài (≥60 s) ở trạng thái ổn định (loại bỏ block #1 và #2), thời gian chu kỳ giữa hai khối liên tiếp ($T_{\text{cycle}}$) sẽ phản ánh thông lượng thực tế ổn định, không bị méo mó bởi hàng đợi burst.
- **H2 (Phân rã thời gian Predecessor):** Thời gian xử lý của block tiền nhiệm $N-1$ gồm hai phần tách biệt:
  - $(a)$ **Phần tính toán CPU bắt buộc trước khi $N$ có thể clone state:** $T_a = T_{\text{block\_stm}} + T_{\text{root\_derivation}} + T_{\text{state\_clone}}$. Khối $N$ bắt buộc phải chờ phần này xong để có state delta đầu vào.
  - $(b)$ **Phần Persist chạy sau:** $T_b = T_{\text{nomt\_commit}} + T_{\text{pebble\_write}} + T_{\text{gei\_async}}$. Đây là **phần duy nhất có thể chồng lấn (overlap)** với việc thực thi của $N$ nếu áp dụng Speculative Pipelining.
- **H3 (Amdahl Ceiling):** Nếu phần $(b)$ được ẩn hoàn toàn vào nền (chạy song song với execution của $N$), chu kỳ tối thiểu của một khối ở trạng thái ổn định sẽ là:
  $$T_{\text{cycle\_min}} = \max(T_a, T_b)$$
  Thông lượng trần lý thuyết đạt được là:
  $$\text{TPS}_{\text{ceiling}} = \frac{\text{Txs\_per\_block}}{T_{\text{cycle\_min}}}$$
  Mức tăng thông lượng tối đa:
  $$\Delta \text{TPS}_{\text{potential}} = \frac{\text{TPS}_{\text{ceiling}} - \text{TPS}_{\text{baseline}}}{\text{TPS}_{\text{baseline}}} \times 100\%$$
- **H4 (Ý nghĩa thống kê của Batch Size):** Với mẫu đủ lớn (≥10 lượt lặp độc lập mỗi mức batch: 250, 500, 1000), xác định rõ liệu Batch 500 có cải thiện thông lượng thực sự vượt qua độ lệch chuẩn (SD) hay chỉ là nhiễu ngẫu nhiên.

---

## 3. Tiêu Chí Quyết Định (Gating Criteria)

> 🚨 **Ngưỡng quyết định bắt buộc cho Giai đoạn D:**
> 
> - **ĐIỀU KIỆN TIẾN HÀNH GIAI ĐOẠN D:** Chỉ khi mức tăng thông lượng cận trên Amdahl đạt:
>   $$\Delta \text{TPS}_{\text{potential}} \ge +15\%$$
>   VÀ thiết kế chi tiết tại Giai đoạn C được User phê duyệt bằng văn bản, thì mới được phép lập nhánh riêng để triển khai Giai đoạn D.
> - **ĐIỀU KIỆN DỪNG LẠI (ABORT GIAI ĐOẠN D):** Nếu $\Delta \text{TPS}_{\text{potential}} < +15\%$, **tuyệt đối không can thiệp vào gate `waitCommitted`**. Toàn bộ nỗ lực tối ưu sẽ chuyển sang **Giai đoạn B** (các cải tiến rủi ro thấp độc lập consensus: parallel root derivation, tuning batch size, mempool pre-warming).

---

## 4. Kế Hoạch Thực Nghiệm Chi Tiết

### 4.1. Môi trường thực nghiệm
- Cụm 4 validator cổng 31xxx (`val0`..`val3`), dữ liệu sạch nhân bản từ `/tmp/gate_4val_clean_template` trước mỗi lượt chạy.
- Máy chủ: Intel Xeon Platinum 8272CL (104 vCPUs, 188 GB RAM), ext4 SSD.
- Công cụ tiêm tải: [secp_tps_blast](file:///home/abc/chain-n/metanode/execution/cmd/tool/secp_tps_blast/main.go) chế độ TCP stream EIP-1559.
- Thu thập log chi tiết với `METANODE_FFI_TRACE=true` để bóc tách timestamp nano-giây từng giai đoạn.

### 4.2. Các bộ thực nghiệm cần đo
1. **Workload A1 — Unlimited Steady-State (60 giây, 5 lượt lặp):**
   - Bơm tải liên tục không giới hạn tốc độ trong 60 giây (`-duration 60 -batch 1000`).
   - Đo: TPS ổn định, số block sản sinh, cycle time giữa các block (bỏ block 1-2).
   - Tách thời gian $(a)$ và $(b)$ cho từng block ổn định, lập biểu đồ Gantt đường găng.
2. **Workload A2 — Rate-Limited Steady-State (60 giây, 4.500 tx/s, 5 lượt lặp):**
   - Bơm tải tốc độ cố định 4.500 tx/s trong 60 giây (`-duration 60 -rate-limit 4500 -batch 500`).
   - Kiểm tra hành vi hàng đợi và cycle time khi hệ thống hoạt động dưới ngưỡng bão hòa.
3. **Workload A3 — Batch Size Statistical Rigor (10 lượt lặp mỗi mức):**
   - Lặp 10 lượt độc lập cho Batch 250 (25k txs/lượt).
   - Lặp 10 lượt độc lập cho Batch 500 (25k txs/lượt).
   - Lặp 10 lượt độc lập cho Batch 1000 (25k txs/lượt).
   - Tính Mean, StdDev, Standard Error và t-test p-value để kết luận Batch 500 có thực sự tốt hơn không.

---

## 5. Ràng Buộc Bất Biến Zero-Fork
- Toàn bộ các lượt chạy benchmark trên bắt buộc phải bật cờ `-verify-parity`.
- Bất kỳ lượt chạy nào có Block Hash hoặc State Root phân kỳ giữa 4 node $\rightarrow$ Kết luận là **FAIL ngay lập tức** và dừng thực nghiệm để điều tra lỗi fork.
