# PREREGISTERED: Kế Hoạch Điều Tra Nguyên Nhân & Cải Thiện TPS BFT/Raft

**Ngày đăng ký:** 2026-10-08  
**Trạng thái:** PREREGISTERED (Commit trước khi thực hiện đo đạc và can thiệp)  
**Mục tiêu:** Trả lời 5 câu hỏi trọng tâm (Q1–Q5) về khoảng cách hiệu năng giữa BFT và Raft, xác định nút thắt cổ chai bằng số đo trực tiếp (không suy diễn) và chỉ can thiệp cải tiến nếu thỏa mãn các cổng quyết định định lượng.

---

## 1. Hiện Trạng Căn Cứ (Baseline từ `note/tps_remeasure_bft_vs_raft_report_20261008.md`)

- **BFT 4 Validator (Mysticeti DAG BFT):**
  - Burst 25k (n=5): Trung vị **7.227 tx/s** (TB 7.959, SD 1.596, khoảng 6.542 – 10.612 tx/s).
  - Sustained 60s (n=3): Trung vị **14.826 tx/s** (TB 14.240, SD 1.226, khoảng 12.830 – 15.062 tx/s).
- **Raft 3 Node (HashiCorp Raft Quorum 2/3):**
  - Burst 20k (n=3): Trung vị **19.100 tx/s** (TB 19.422, SD 567, khoảng 19.088 – 20.076 tx/s).
  - Burst 25k (n=5): Trung vị **13.650 tx/s** (TB 13.836, SD 563, khoảng 13.326 – 14.679 tx/s).
  - Sustained 60s (n=3): Trung vị **9.998 tx/s** (TB 10.061, SD 174, khoảng 9.927 – 10.258 tx/s).

---

## 2. Các Câu Hỏi Trọng Tâm & Giả Thuyết Khoa Học (Q1–Q5)

### Q1: Vì sao Raft Sustained (10,0k) thấp hơn BFT Sustained (14,8k) trong khi Raft Burst lại cao hơn?
- **Giả thuyết H1.1 (Block Overhead & Frequency):** Raft sinh ra ~330–360 block trong 60s (~1.700–1.800 tx/block), trong khi BFT chỉ sinh ~150–175 block (~5.000 tx/block). Số lần thực hiện commit state trie (NOMT flush), FSM lock, và Go channel synchronization của Raft cao gấp đôi BFT. Chi phí quản lý cố định trên mỗi block làm suy giảm thông lượng liên tục.
- **Giả thuyết H1.2 (Propose Queue Backpressure):** Hàng đợi `propose_queue` (kích thước mặc định hoặc 4.096) bị đầy khi client bơm tải sustained không giới hạn tốc độ, kích hoạt cơ chế backpressure / retry sleep trong forwarder (`time.Sleep(100ms)`).
- **Tiêu chí kiểm chứng Q1:** Đo breakdown thời gian từng khâu của Raft: (1) `propose_queue` wait time, (2) replication roundtrip, (3) FSM apply + BlockProcessor ingestion, (4) NOMT trie commit. Nếu khâu quản lý block chiếm $\ge 20\%$ tổng thời gian chu kỳ, H1.1 được xác nhận.

### Q2: Vì sao Raft Burst 25k (13,7k) thấp hơn Burst 20k (19,1k)?
- **Giả thuyết H2.1 (Batch Splitting & Ingress Queue Limit):** Giữa 20.000 và 25.000 txs, tổng số batch đạt ngưỡng bão hòa của hàng đợi nhận (hoặc buffer TCP socket), làm phát sinh backpressure retry hoặc làm số block bị xé nhỏ thêm.
- **Tiêu chí kiểm chứng Q2:** Quét vi sai giữa 20k và 25k, theo dõi số lần `propose_queue` từ chối batch (`batchSubmitter() == false`) và số lần split batch.

### Q3: Vì sao BFT Burst 25k dao động lớn giữa các lượt (6.542 – 10.612 tx/s, SD 1.596)?
- **Giả thuyết H3.1 (DAG Round Scheduling & Commit Cadence):** Trong burst ngắn (3 blocks), sự chênh lệch chỉ 1 round DAG hoặc 1 nhịp commit certificate (do phân bố ngẫu nhiên mạng localhost) làm thay đổi `commit_duration` từ ~2,3s lên ~3,8s (chênh 1,5s), gây biến thiên TPS tới 62%.
- **Giả thuyết H3.2 (Cold Cache / Memory Allocator):** Lượt chạy đầu tiên chịu chi phí khởi động bộ nhớ (malloc heap, NOMT trie mmap, CGO thread binding) so với các lượt có cache ấm hơn.
- **Tiêu chí kiểm chứng Q3:** So sánh lượt chạy có Warmup cố định vs Không Warmup; đo thời gian chờ commit DAG `[TIMELINE-RUST]` giữa lượt nhanh (10k) và lượt chậm (6,5k).

### Q4: Tại sao BFT Sustained ổn định hơn nhiều so với Burst?
- **Giả thuyết H4.1 (Amortization over 60s):** Ở chế độ sustained 60s, các chi phí khởi động ban đầu (~0,5–1s) được triệt tiêu hoàn toàn khi chia đều cho 60 giây và ~850.000 txs.
- **Tiêu chí kiểm chứng Q4:** Đo vi sai 10s đầu so với 50s tiếp theo trong chế độ sustained 60s.

### Q5: Giải mã độ trễ P50 báo ~68–70s trong chế độ Sustained 60s
- **Phát hiện từ mã nguồn (`execution/cmd/tool/secp_tps_blast/main.go:1206-1285`):**
  Trong `runSustainedBenchmark`, 100 transaction mẫu được lưu ở giây thứ 0 (`SentAt = t0`). Nhưng công cụ chỉ bắt đầu vòng lặp truy vấn biên lai (`eth_getTransactionReceipt`) **SAU KHI** đã kết thúc 60s sustained và ngủ thêm 10s stabilization! Do đó `time.Since(st.SentAt)` luôn đo khoảng thời gian từ giây 0 đến giây 70.
- **Kết luận xác nhận cho Q5:** Độ trễ ~68–70s **hoàn toàn là do logic hoãn truy vấn receipt của công cụ đo**, không phải thời gian xử lý hay backlog nghẽn của node. Cần sửa logic tracking receipt liên tục trong suốt 60s để có độ trễ thực tế.

---

## 3. Cổng Quyết Định Can Thiệp (Decision Gates)

1. **Ngưỡng Amdahl:** Chỉ đề xuất can thiệp code khi một thành phần được chứng minh bằng số đo chiếm $\ge 10\%$ chu kỳ block và cận trên cải thiện dự kiến đạt $\ge +5\%$ Throughput.
2. **Ngưỡng Chấp Nhận Cải Tiến:**
   - Mỗi cấu hình đo tối thiểu **$\ge 8$ lượt độc lập** (wipe sạch cụm trước mỗi lượt).
   - Mức cải thiện trung vị phải đạt **$\ge +5\%$**.
   - Khoảng tin cậy 95% của hiệu (Confidence Interval via Welch's t-test / Bootstrap) **không được chứa giá trị 0**.
   - Nếu không thỏa mãn: Ghi nhận "Không xác nhận" và giữ nguyên/revert mã nguồn.
3. **Bất Biến Bắt Buộc:**
   - 100% Zero-Fork Parity (`AGENTS.md` Part 2.5): Không dùng timeout/sleep để dispatch; mọi block hash và state root phải khớp tuyệt đối.
   - Vượt qua kiểm tra hỗn loạn `kill -9` tối thiểu 5 chu kỳ.
   - `build_check.sh` 5/5 PASSED không có cảnh báo/lỗi biên dịch.
