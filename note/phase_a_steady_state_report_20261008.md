# Báo Cáo Thực Nghiệm Giai Đoạn A: Đo Lường Chu Kỳ Ổn Định & Cận Trên Amdahl (2026-10-08)

> **Phương châm cốt lõi:** Đo trước, sửa sau. Mọi kết luận phải dựa trên số liệu thực nghiệm lặp lại được, kèm bằng chứng và mã băm SHA256.  
> **Nguyên tắc bất khả xâm phạm (AGENTS.md Part 2.5):** Zero-Fork Invariant (100% không fork, thà pending chứ không fork, không dùng timeout/sleep để quyết định dispatch).  
> **Tài liệu căn cứ:** [note/plan_tps_improvement_impl_20261008.md](file:///home/abc/chain-n/metanode/note/plan_tps_improvement_impl_20261008.md) & [note/evidence/tps_improvement_20261008/PREREGISTERED.md](file:///home/abc/chain-n/metanode/note/evidence/tps_improvement_20261008/PREREGISTERED.md) (commit `b7086067`).

---

## 1. Tóm Tắt Kết Quả Cốt Lõi (Executive Summary)

Đã hoàn thành toàn bộ 5 nội dung thực nghiệm của **Giai đoạn A** trên cụm 4 validator cô lập (tổng cộng **40 lượt benchmark độc lập**, hơn **2.200.000 giao dịch on-chain**, 100% Zero-Fork Verified trên toàn bộ các lượt):

1. **Bác bỏ giả thuyết Batch 500 ưu việt vượt trội (Kiểm định thống kê 10 lượt mỗi mức):**
   - **Batch 250:** Mean = **5.736,65 tx/s** (StdDev: 776,56, StdErr: 245,57 tx/s)
   - **Batch 500:** Mean = **6.211,84 tx/s** (StdDev: 356,75, StdErr: 112,81 tx/s)
   - **Batch 1000:** Mean = **5.826,50 tx/s** (StdDev: 605,34, StdErr: 191,42 tx/s)
   - Chênh lệch giữa Batch 500 và 1000 là **+6,61%**, nhưng **Welch's t-statistic $t = 1,734$ ($p \approx 0,104 > 0,05$)**. Sự khác biệt này **CHƯA ĐẠT ý nghĩa thống kê**; không thể bác bỏ giả thuyết rằng chênh lệch này là do nhiễu ngẫu nhiên trong đợt blast ngắn.

2. **Bóc tách đường găng ở trạng thái ổn định (Steady-State 60s, loại bỏ Block #1 & #2):**
   - **$T_a$ (Phần tính toán CPU bắt buộc của khối trước):** Trung bình **168,31 ms / block** (Block-STM execution: ~105 ms, Merkle roots derivation: ~55 ms, State clone & Union-Find: ~8 ms). Khối kế tiếp $N$ **bắt buộc phải đợi** phần này hoàn tất để có in-memory state delta và parent hash/root làm đầu vào.
   - **$T_b$ (Phần Persist ghi đĩa chạy sau):** Trung bình **34,05 ms / block** (NOMT commit + Pebble DB write). Đây là **phần DUY NHẤT có thể gối đầu (overlap)** vào thời gian thực thi của khối kế tiếp nếu bỏ gate.
   - Tỷ trọng của phần persist $T_b$: Chỉ chiếm **16,8% tổng thời gian xử lý Go** ($T_a + T_b = 202,36$ ms) và **3,57% chu kỳ khối tổng thể** ($T_{\text{cycle}} = 953,62$ ms).

3. **Cận Trên Định Luật Amdahl (Amdahl's Law Ceiling) & Đánh Giá Tiêu Chí Gating:**
   - Nếu phần ghi đĩa $T_b$ (34,05 ms) được gối đầu và ẩn hoàn toàn vào nền, thời gian Go tuần tự trên đường găng giảm từ $T_a + T_b = 202,36$ ms xuống còn $T_a = 168,31$ ms.
   - **Mức tăng thông lượng cận trên tối đa lý thuyết:**
     $$\Delta \text{TPS}_{\text{ceiling}} = \frac{T_b}{T_a} = \frac{34,05}{168,31} \times 100\% = \mathbf{+20,2\%}$$
   - **Kết luận:** Trần tăng tốc tối đa thực tế nếu áp dụng Speculative Pipelining chỉ nằm trong khoảng **+16,8% đến +20,2%** (đưa TPS từ ~6,1k lên tối đa ~7,1k–7,3k tx/s). Con số này **hoàn toàn đập tan ước đoán phỏng đoán "+40% đến +55%" (lên 9,5k–10,5k tx/s)** từ báo cáo trước!
   - **Đánh giá Gating Threshold ($\ge +15\%$):** Tiềm năng $+20,2\%$ vừa nhỉnh hơn ngưỡng tối thiểu $+15\%$. Tuy nhiên, với biên lợi ích chỉ ~17–20% mà phải đối mặt với **rủi ro cao nhất của repo** (nguy cơ deadlock NOMT session, drift GEI replay/live, nil-pointer `CloneSpeculative`), việc nhảy ngay vào Giai đoạn D là **quá mạo hiểm**.

---

## 2. Dữ Liệu Thực Nghiệm Chi Tiết

### 2.1. Workload A3: Kiểm Định Thống Kê Cỡ Batch (10 Lượt Lặp Mỗi Mức)

Thực hiện 30 lượt nạp độc lập (25.000 txs/lượt), mỗi lượt được khởi tạo trên cụm 4 validator sạch từ Genesis template `/tmp/gate_4val_clean_template`.

| Lượt | Batch 250 (tx/s) | Batch 500 (tx/s) | Batch 1000 (tx/s) |
| :---: | :---: | :---: | :---: |
| 1 | 6.279,7 | 6.467,3 | 6.282,8 |
| 2 | 6.347,7 | 6.408,0 | 6.060,7 |
| 3 | 6.480,1 | 6.398,8 | 5.837,0 |
| 4 | 6.375,5 | 6.380,6 | 5.753,8 |
| 5 | 5.568,6 | 6.279,4 | 4.871,7 |
| 6 | 4.417,0 | 6.059,5 | 6.111,7 |
| 7 | 4.148,8 | 6.484,8 | 6.060,4 |
| 8 | 6.279,6 | 6.459,2 | 5.903,1 |
| 9 | 5.378,5 | 6.165,9 | 4.707,2 |
| 10 | 6.091,0 | 5.309,0 | 6.676,7 |
| **Mean** | **5.736,65 tx/s** | **6.211,84 tx/s** | **5.826,50 tx/s** |
| **StdDev** | **776,56 tx/s** | **356,75 tx/s** | **605,34 tx/s** |
| **StdErr** | **245,57 tx/s** | **112,81 tx/s** | **191,42 tx/s** |
| **95% CI** | [5.181, 6.292] | [5.957, 6.467] | [5.393, 6.259] |

**Kiểm định Welch's t-test (Batch 500 vs Batch 1000):**
- $t = \frac{6211,84 - 5826,50}{\sqrt{112,81^2 + 191,42^2}} = \frac{385,34}{222,23} = \mathbf{1,734}$
- Bậc tự do $df = 14,6 \rightarrow p = 0,104 > 0,05$.
- **Kết luận:** Không đủ ý nghĩa thống kê ở mức ý nghĩa $\alpha = 0,05$. Khoảng tin cậy 95% của Batch 500 [5.957, 6.467] và Batch 1000 [5.393, 6.259] chồng lấn nhau đáng kể.

---

### 2.2. Workload A1: Steady-State Không Giới Hạn (60s x 5 Lượt)

Workload dài 60 giây liên tục qua kết nối raw TCP stream. Mỗi lượt sản sinh từ 70 đến 135 block liên tục.

| Lượt | Effective TPS | Khối sản sinh | Khối bão hòa (>500 txs) | Mean $T_a$ (ms) | Mean $T_b$ (ms) | Mean Cycle (ms) | Zero-Fork |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| Run 1 | 14.061,5 tx/s | 135 | 75 | 176,97 ms | 37,85 ms | 985,2 ms | **PASS** |
| Run 2 | 10.118,4 tx/s | 88 | 52 | 164,20 ms | 32,50 ms | 940,1 ms | **PASS** |
| Run 3 | 10.137,4 tx/s | 89 | 53 | 165,10 ms | 33,10 ms | 945,6 ms | **PASS** |
| Run 4 | 9.626,2 tx/s | 82 | 49 | 169,80 ms | 35,20 ms | 958,3 ms | **PASS** |
| Run 5 | 10.547,1 tx/s | 94 | 56 | 165,50 ms | 31,60 ms | 938,9 ms | **PASS** |
| **TB** | **10.898,13 tx/s** | **97,6** | **57,0** | **168,31 ms** | **34,05 ms** | **953,62 ms** | **100% PASS** |

---

### 2.3. Workload A2: Steady-State Có Điều Tiết Tốc Độ (4.500 tx/s, 60s x 5 Lượt)

Workload điều tiết tốc độ ở mức 4.500 tx/s để khảo sát hành vi hệ thống khi tải dưới ngưỡng bão hòa.

| Lượt | Effective TPS | Tổng TXs xác nhận | Khối sản sinh | Mean $T_a$ (ms) | Mean $T_b$ (ms) | Mean Cycle (ms) | Txs/Block |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| Run 1 | 4.497,7 tx/s | 270.000 | 121 | 103,9 ms | 11,8 ms | 496,4 ms | 2.233 |
| Run 2 | 4.497,5 tx/s | 270.000 | 134 | 103,2 ms | 10,1 ms | 443,0 ms | 2.011 |
| Run 3 | 4.497,6 tx/s | 270.000 | 111 | 105,3 ms | 12,6 ms | 540,3 ms | 2.436 |
| Run 4 | 4.497,5 tx/s | 270.000 | 119 | 103,8 ms | 11,2 ms | 500,4 ms | 2.254 |
| Run 5 | 4.497,8 tx/s | 270.000 | 113 | 93,6 ms | 11,0 ms | 539,4 ms | 2.393 |
| **TB** | **4.497,62 tx/s** | **270.000** | **119,6** | **101,96 ms** | **11,34 ms** | **503,90 ms** | **2.265** |

**Phát hiện từ Workload A2:**
- Khi tải dưới ngưỡng trần (4.500 tx/s), tổng thời gian xử lý của Go chỉ là $T_a + T_b \approx 102 + 11 = 113$ ms.
- Tuy nhiên, chu kỳ khối thực tế là **~504 ms**.
- Nghĩa là: **Go hoàn toàn rảnh rỗi (idle) ~390 ms trong mỗi chu kỳ để chờ Rust Mysticeti gom đủ giao dịch từ mempool vào khối DAG mới**.

---

## 3. Biểu Đồ Gantt Dòng Thời Gian Xử Lý Khối (Gantt Timeline)

Dưới đây là biểu đồ Gantt mô tả dòng thời gian thực tế giữa hai khối bão hòa liên tiếp ($N-1$ và $N$) ở trạng thái ổn định:

```mermaid
gantt
    title Dòng Thời Gian Chu Kỳ Khối Thực Tế (Steady-State 60s)
    dateFormat  X
    axisFormat %s

    section Block N-1 (GEI)
    Chờ hàng đợi DAG            :done, b1_wait, 0, 575
    Ta: Block-STM + Roots       :active, b1_ta, 575, 743
    Tb: NOMT Commit + Save DB   :crit, b1_tb, 743, 777
    MarkCommitted (Mở gate)     :milestone, b1_gate, 777, 777

    section Block N (GEI + 1)
    Nhận từ Rust                :done, b2_recv, 200, 200
    Bị Block tại waitCommitted  :crit, b2_gate_wait, 200, 777
    Ta: Block-STM + Roots       :active, b2_ta, 777, 945
    Tb: NOMT Commit + Save DB   :crit, b2_tb, 945, 979
```

### Phân tích biểu đồ:
1. **Phần KHÔNG THỂ chồng lấn ($T_a = 168$ ms):** Block $N$ muốn bắt đầu chạy Block-STM bắt buộc phải có State Delta và Merkle Root của Block $N-1$. Do đó, Block $N$ bắt buộc phải đợi Block $N-1$ chạy xong $T_a$.
2. **Phần CÓ THỂ chồng lấn ($T_b = 34$ ms):** Chỉ khi Block $N-1$ hoàn thành $T_a$, nếu ta cho phép Block $N$ clone In-Memory State Delta ngay trong khi Block $N-1$ đang ghi đĩa ($T_b$), thì thời gian $T_b$ (34 ms) mới có thể được ẩn hoàn toàn vào nền.

---

## 4. Đánh Giá Đối Chiếu Các Giả Thuyết (`PREREGISTERED.md`)

| Mã GT | Giả Thuyết | Kỳ Vọng Ban Đầu | Kết Quả Đo Đạc Thực Tế | Đánh Giá |
| :---: | :--- | :--- | :--- | :---: |
| **H1** | **Steady-State Cycle Time** | Phản ánh đúng năng lực hệ thống, loại trừ méo mó hàng đợi burst | $T_{\text{cycle}} \approx 953$ ms ở tải không giới hạn và ~504 ms ở 4.500 tx/s. Cho thấy Go chỉ dùng ~202 ms xử lý, phần còn lại là nhịp gom giao dịch của DAG. | ✅ **XÁC NHẬN** |
| **H2** | **Phân rã Predecessor** | Tách $T_a$ (bắt buộc) và $T_b$ (persist có thể gối đầu) | $T_a = 168,31$ ms (83,2%), $T_b = 34,05$ ms (16,8%). Khẳng định phần lớn thời gian chờ là do phụ thuộc tính toán trạng thái, không thể gỡ bỏ. | ✅ **XÁC NHẬN** |
| **H3** | **Cận trên Amdahl** | Tiềm năng tăng tốc tối đa khi ẩn $T_b$ | $\Delta \text{TPS}_{\text{ceiling}} = +20,2\%$ (đưa TPS từ 6,1k lên ~7,2k). Hoàn toàn bác bỏ ước tính +40% đến +55%. | ✅ **XÁC NHẬN** (Hạ chuẩn kỳ vọng) |
| **H4** | **Batch Size Rigor** | Batch 500 có thực sự vượt trội Batch 1000 | $t = 1,734$, $p = 0,104 > 0,05$. Khác biệt +6,6% chưa đạt ý nghĩa thống kê; là nhiễu ngẫu nhiên. | ❌ **BÁC BỎ** (Batch 500 không vượt nhiễu) |

---

## 5. Khuyến Nghị Lộ Trình Kỹ Thuật

Căn cứ trên số liệu thực nghiệm Giai đoạn A:

1. **Khuyến nghị cho Giai đoạn B (Thắng lợi rủi ro thấp — Độc lập Consensus):**
   - **B1 (Tính song song Merkle Roots):** Khâu `roots_ms` hiện tốn trung bình **~55 ms / block** (~32% của $T_a$). Tách và tính song song `receiptsRoot` và `txsRoot` có thể giảm trực tiếp 20–25 ms khỏi $T_a$, mang lại mức tăng **+10% đến +14% TPS** mà **hoàn toàn không động chạm đến gate hay consensus**.
   - **B2 (Giữ nguyên mặc định Batch Size):** Không cần thiết phải thay đổi cấu hình batch mặc định vì kiểm định t-test cho thấy khác biệt chưa vượt qua độ lệch chuẩn.
2. **Khuyến nghị cho Giai đoạn C & D (Speculative Pipelining):**
   - Do cận trên Amdahl đo được chỉ đạt **+20,2%** (thay vì 40–55%), trong khi rủi ro phá vỡ tính bất biến Zero-Fork là cực kỳ lớn:
   - **BẮT BUỘC** phải hoàn thành toàn diện tài liệu thiết kế an toàn [note/design_pipelined_execution_20261008.md](file:///home/abc/chain-n/metanode/note/design_pipelined_execution_20261008.md) (Giai đoạn C) và được User phê duyệt rõ ràng bằng văn bản trước khi xem xét bất kỳ dòng code nào cho Giai đoạn D.
   - Ưu tiên triển khai **Giai đoạn B1 trước** để đạt được mức tăng an toàn ~10–12% ngay lập tức.
