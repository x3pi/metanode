# Báo cáo đo lại độc lập TPS end-to-end: BFT (DAG Mysticeti) vs Raft — 2026-10-08

- **Mục đích:** kiểm tra độ chính xác của các báo cáo TPS BFT/Raft do Gemini lập trong ngày
  (`tps_bft_peak_report_20261008.md`, `tps_raft_peak_report_20261008.md`, `tps_raft_vs_bft_comparison_report_20261008.md`).
- **Commit được đo:** `3569c113` (HEAD của `dev` lúc đo). Binary `simple_chain` và `secp_tps_blast` **được build lại từ HEAD**,
  không dùng `/tmp/p06_bins`. `parent_chain` và `rollup-cluster` dùng lại bản có sẵn (không phải đối tượng kiểm tra).
- **Evidence thô:** `note/evidence/tps_remeasure_20261008/` (JSON từng lượt, log blast, 2 script đo, `SHA256SUMS`).
- **Môi trường:** một máy Linux 104 vCPU, mọi node (BFT 4 validator, Raft 3 node) **chạy chung một máy**; hai chế độ chạy tuần tự, không chạy song song.

---

## 1. Phương pháp

| Mục | BFT | Raft |
|---|---|---|
| Cụm | 4 validator (Mysticeti DAG + parent chain), dựng từ `/tmp/gate_4val_clean_template` | 3 node HashiCorp Raft (`consensus_mode: raft`), dựng bằng chính hàm `setup_cluster()` của Gemini |
| Làm sạch | **wipe và dựng lại cụm trước mỗi lượt** | **wipe và dựng lại cụm trước mỗi lượt** |
| Tải | `secp_tps_blast`, TCP, EIP-1559, batch 1.000, bộ khóa 50.000 ví, `-verify-parity` | như BFT, bơm vào leader |
| Giao thức 1 (burst 25k) | 5 lượt | 5 lượt |
| Giao thức 2 (burst 20k) | chưa đo | 3 lượt |
| Giao thức 3 (sustained 60 s, không giới hạn tốc độ) | 3 lượt | 3 lượt |
| Thống kê | tự tính trung vị/trung bình/SD từ file JSON thô | như BFT |

"Effective TPS" là số tx được xác nhận chia cho thời gian commit mà `secp_tps_blast` đo
(`effective_tps`, `commit_duration` trong báo cáo JSON của công cụ). Mọi lượt đều **confirmed đủ số tx** và **zero-fork PASS** (block hash + state root khớp trên toàn bộ node).

---

## 2. Kết quả

### 2.1 BFT (4 validator)

| Giao thức | Các lượt (tx/s) | Trung vị | Trung bình | SD | Min–Max |
|---|---|---|---|---|---|
| Burst 25k (n=5) | 10.612 / 7.227 / 7.219 / 6.542 / 8.197 | **7.227** | 7.959 | 1.596 | 6.542–10.612 |
| Sustained 60 s (n=3) | 14.826 / 12.830 / 15.062 | **14.826** | 14.240 | 1.226 | 12.830–15.062 |

- Burst 25k: 3 block mỗi lượt, 8.333 tx/block. Thời gian commit 2,36–3,82 s.
- Sustained: 154–174 block, khoảng 5.000 tx/block.

### 2.2 Raft (3 node)

| Giao thức | Các lượt (tx/s) | Trung vị | Trung bình | SD | Min–Max |
|---|---|---|---|---|---|
| Burst 25k (n=5) | 14.679 / 13.409 / 13.650 / 14.116 / 13.326 | **13.650** | 13.836 | 563 | 13.326–14.679 |
| Burst 20k (n=3) | 19.100 / 19.088 / 20.076 | **19.100** | 19.422 | 567 | 19.088–20.076 |
| Sustained 60 s (n=3) | 10.258 / 9.998 / 9.927 | **9.998** | 10.061 | 174 | 9.927–10.258 |

- Burst 25k: 12–13 block, ~1.900–2.100 tx/block. Thời gian commit 1,7–1,9 s.
- Sustained: 328–362 block, ~1.700–1.800 tx/block.

### 2.3 So sánh trực tiếp (cùng giao thức, cùng máy)

| Giao thức | BFT (trung vị) | Raft (trung vị) | Tỷ lệ | Ghi chú thống kê |
|---|---|---|---|---|
| Burst 25k | 7.227 | 13.650 | Raft ≈ **1,9×** BFT | Welch t ≈ 7,8 (n=5/5) |
| Sustained 60 s | 14.826 | 9.998 | BFT ≈ **1,48×** Raft (theo trung bình 1,42×) | Welch t ≈ 5,8 (n=3/3), mẫu nhỏ |

---

## 3. Đối chiếu với báo cáo của Gemini

| Khẳng định của Gemini | Kết quả đo lại | Đánh giá |
|---|---|---|
| BFT đỉnh **9.512,77** tx/s tại 25k | trung vị **7.227**, TB 7.959 (5 lượt) | **Không tái lập như một mức điển hình.** 9.512 chỉ gần lượt 1 (10.612); bốn lượt sau 6.542–8.197. Gemini chỉ đo **một lượt** mỗi mức tải. |
| "BFT +52,6% so với 6.231 hôm qua" | Chênh lệch nằm trong biên dao động giữa các lượt BFT (SD 1.596) | **Chưa đủ cơ sở.** So sánh một điểm với một điểm khác. |
| Báo cáo so sánh: BFT **1.393,6** tx/s | Không khớp với bất kỳ số đo nào | **Mâu thuẫn nội bộ**: cùng ngày, báo cáo BFT peak của chính Gemini ghi 9.512. Hai con số cách nhau gần 7 lần. |
| Raft đỉnh **13.811** tx/s tại 20k | trung vị **19.100** tại 20k (3 lượt) | **Không khớp** (số đo lại cao hơn 38%). Chưa rõ nguyên nhân. |
| Raft ở mức 25k–30k–50k ≈ 12.800–13.400 | burst 25k trung vị **13.650** | **Khớp** ở mức 25k. |
| Raft 8k tx: **10.754** tx/s (báo cáo so sánh) | chưa đo mức 8k | Chưa kiểm tra. |
| "Raft cao gấp **7,72×** BFT" | burst 25k: 1,9×; sustained 60 s: **BFT nhanh hơn 1,48×** | **Sai và gây hiểu lầm.** Hệ số 7,72 lấy từ con số BFT 1.393,6 mâu thuẫn. |
| Zero-fork 100% PASS | Mọi lượt đo lại đều PASS | **Khớp.** |

---

## 4. Nhận xét và đánh giá

### 4.1 Điều đáng tin
- Cả hai chế độ hoạt động ổn định ở quy mô này: không mất tx, không fork, parity khớp ở mọi lượt (BFT 8/8, Raft 11/11).
- Thứ tự "Raft burst nhanh hơn BFT burst" là thật và có ý nghĩa thống kê (burst 25k, 5/5 lượt).
- Hai nhóm số liệu Raft burst 25k của Gemini và mình khớp nhau, cho thấy phía Raft ít nhiễu hơn.

### 4.2 Điều không đáng tin trong báo cáo cũ
- **Mỗi mức tải chỉ một lượt, các mức chạy liên tiếp trên cùng một cụm không wipe.** Độ biến thiên của BFT ở burst 25k lên tới 6.542–10.612 tx/s (chênh 62%), nên một lượt không đủ kết luận.
- **Hai báo cáo cùng ngày cho BFT hai con số cách nhau ~7 lần** mà không được giải thích.
- **Kết luận "Raft nhanh hơn 7,72 lần" bỏ qua giao thức đo.** Với burst ngắn Raft hơn BFT khoảng 1,9 lần, nhưng khi tải liên tục thì BFT hơn Raft.

### 4.3 Vì sao burst và sustained cho ra thứ tự ngược nhau
Mình chưa chứng minh được nguyên nhân; đây là giả thuyết cần kiểm tra:
- Burst chỉ kéo dài 1–4 giây, nên TPS bị chi phối bởi **độ trễ khởi động** (cache lạnh, 3 block lớn so với 12–13 block nhỏ) chứ không phải thông lượng ổn định.
- Raft tạo block nhỏ (~2.000 tx), nhiều block (330–360 block/60 s) nên chịu chi phí cố định mỗi block nhiều lần hơn. BFT gom block lớn (~5.000–8.333 tx), có lợi khi tải kéo dài.
- Tải sustained của Raft có thể bị giới hạn bởi hàng đợi propose (`propose_queue_size = 4096`) hoặc đường replicate. **Chưa đo.**
- Độ trễ P50 trong chế độ sustained (~68–70 s, dài hơn cả thời gian chạy 60 s) cho thấy công cụ đang tạo backlog; số này **không** nên đọc là độ trễ mỗi giao dịch.

### 4.4 Hạn chế của chính phép đo này
- BFT sustained và Raft burst 20k chỉ có **3 lượt**; khoảng dao động còn rộng. Chưa đo BFT burst 20k và các mức 8k/30k/50k mà Gemini báo.
- Tất cả node dùng chung một máy 104 vCPU nên có tranh chấp CPU/đĩa; số tuyệt đối sẽ khác khi triển khai nhiều máy.
- Hai chế độ **không cùng mô hình tin cậy**: BFT chịu lỗi Byzantine (3/4), Raft chỉ chịu lỗi sập (2/3) và cụm Raft thử nghiệm không có parent chain / Rust FFI. So sánh TPS trực tiếp chỉ có ý nghĩa tham khảo.
- Raft burst 25k (13,7k) thấp hơn Raft burst 20k (19,1k): chưa giải thích được, có thể do cỡ block hoặc hàng đợi khi tải vượt một ngưỡng.
- `parent_chain` và `rollup-cluster` là binary có sẵn, không build lại.

---

## 5. Kết luận

1. **BFT:** con số đáng dùng là **trung vị ~7,2k tx/s (burst 25k)** và **~14,8k tx/s (sustained 60 s)**, không phải 9.512 hay 1.393,6.
2. **Raft:** **~13,7k tx/s (burst 25k)**, **~19,1k tx/s (burst 20k)**, **~10,0k tx/s (sustained 60 s)**.
3. **Câu "Raft nhanh hơn BFT 7,72 lần" cần được thu hồi.** Thứ tự phụ thuộc giao thức đo: burst ngắn Raft ≈ 1,9× BFT; tải liên tục BFT ≈ 1,48× Raft.
4. Khi báo cáo TPS, luôn ghi rõ giao thức (burst N tx hay sustained T giây), số lượt, trung vị và SD. Không dùng một lượt duy nhất làm "đỉnh".

## 6. Việc đề xuất làm tiếp
- Đính chính ba báo cáo của Gemini (BFT peak, Raft peak, so sánh) theo bảng mục 3, hoặc gắn cảnh báo ở đầu mỗi file.
- Đo thêm: BFT burst 20k và các mức khác, thêm lượt cho sustained (≥ 8 lượt mỗi chế độ), và thử một cỡ block cố định để tách ảnh hưởng của cỡ block.
- Tìm nguyên nhân Raft sustained thấp (profile hàng đợi propose, đường replicate, FSM apply) và vì sao 25k thấp hơn 20k.
- Nếu muốn so sánh công bằng: dùng cùng mô hình tin cậy (hoặc nêu rõ khác biệt) và, nếu có điều kiện, chạy các node trên nhiều máy.
