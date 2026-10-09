# Báo cáo đo TPS đầy đủ (BFT và Raft) — đo độc lập, 2026-10-09

- **Mục đích:** báo cáo đo TPS với **mọi con số đều do một lượt đo thật, có file thô**. Không có số nào được suy ra từ báo cáo khác.
- **Commit được đo:** `f97a2fbf` (HEAD của `dev` lúc đo). Binary build từ HEAD:
  - `simple_chain_B` = HEAD nguyên trạng (có bộ đếm vi mô Raft).
  - `simple_chain_A` = HEAD nhưng thay `raftfeed/{admin,fsm,node}.go` bằng bản của `1c1b0c53` và bỏ `metrics.go` (không có bộ đếm vi mô), build bằng `go build -overlay`.
  - `secp_tps_blast` build từ HEAD (có bộ theo dõi độ trễ theo thời gian thực). Dùng một bản duy nhất cho mọi lượt.
  - `parent_chain` dùng lại bản có sẵn của `/tmp/p06_bins` (không phải đối tượng so sánh).
- **Môi trường:** một máy Intel Xeon Platinum 8272CL, 104 vCPU, 188 GB RAM. **Tất cả node (BFT 4 validator, Raft 3 node) chạy chung một máy**, các bài đo chạy tuần tự, không song song. Số tuyệt đối sẽ khác khi triển khai nhiều máy.
- **Quy tắc đo:** mỗi lượt dựng **cụm sạch mới** (BFT từ `/tmp/gate_4val_clean_template`, Raft tạo mới), nghỉ 10 giây giữa các lượt, `-verify-parity`, tải EIP-1559 từ bộ khóa 50.000 ví. A/B của Raft sustained chạy **xen kẽ ngẫu nhiên** (seed 20261009, ghép cặp A/B ngẫu nhiên thứ tự).
- **Quy mô:** 93 lượt đo, **93/93 zero-fork PASS và root nhất quán trên mọi node**.
- **Evidence:** `note/evidence/tps_full_measure_20261009/` (`raw/` JSON và log blast từng lượt, `summary.json`, `timeline/` các dòng trace đã lọc, 3 script đo, `SHA256SUMS`).

---

## 1. Kết quả thông lượng (tx/s, "effective TPS" = tx xác nhận ÷ thời gian commit do công cụ blast đo)

| Cấu hình | n | Trung vị | Trung bình | SD | Khoảng | Số block/lượt |
|---|---|---|---|---|---|---|
| **Raft sustained 60 s — binary A (không bộ đếm)** | 8 | **10.321** | 10.321 | 374 | 9.653–11.034 | 328–369 |
| **Raft sustained 60 s — binary B (có bộ đếm)** | 8 | **10.147** | 10.252 | 366 | 9.816–10.900 | 332–364 |
| Raft sustained 60 s — B kèm lấy metric (S7) | 3 | 10.540 | 10.672 | 1.030 | 9.714–11.761 | 332–392 |
| **Raft burst 20k — A** | 8 | **19.854** | 19.085 | 2.315 | 13.729–21.111 | 11–13 |
| Raft burst 20k — B | 8 | 19.602 | 19.546 | 2.929 | 13.328–23.371 | 11–14 |
| **Raft burst 25k — A** | 8 | **13.719** | 15.211 | 3.931 | 13.652–24.910 | 12–16 |
| **Raft burst 30k — A** | 8 | **13.606** | 13.696 | 411 | 13.160–14.450 | 14–16 |
| **BFT burst 25k, cold-start (cụm mới)** | 8 | **8.111** | 7.974 | 441 | 7.345–8.405 | 2–4 |
| BFT burst 25k, warm-up (tải mồi 10k + nghỉ 10 s) | 4 | 8.845 | 8.619 | 877 | 7.368–9.419 | 3 |
| **BFT sustained 60 s** | 8 | **14.072** | 14.042 | 574 | 13.247–14.795 | 154–179 |
| BFT sustained 60 s (bật FFI trace, S5b) | 3 | 14.130 | 14.280 | 350 | 14.030–14.680 | 169 |

Ghi chú về cách đọc:
- "Sustained" là bơm không giới hạn tốc độ trong 60 giây. "Burst Nk" là bơm N tx một lần rồi chờ commit hết.
- Raft sustained: A và B khác nhau −1,7% theo trung vị, Welch t = −0,37. **Không có khác biệt có ý nghĩa**, nên bộ đếm vi mô không làm giảm thông lượng.

---

## 2. Độ trễ

### 2.1 Độ trễ ở tốc độ bơm cố định (đáng tin, so sánh được)
Bơm đúng 5.000 và 8.000 tx/s trong 60 giây, 3 lượt mỗi ô; mọi mẫu độ trễ đều xác nhận kịp (tỷ lệ mẫu bị cắt = 0).

| Chế độ | Tốc độ bơm | TPS đo được | Độ trễ P50 | Độ trễ P99 | Block/60 s |
|---|---|---|---|---|---|
| BFT | 5.000 | 4.999 | **1.889 ms** | 2.013 ms | 44–47 |
| BFT | 8.000 | 7.998 | **1.853 ms** | 2.270 ms | 68–74 |
| Raft | 5.000 | 4.999 | **103 ms** | 107 ms | 322–348 |
| Raft | 8.000 | 7.997 | **103 ms** | 117 ms | 518–537 |

Cả hai chế độ giữ đủ tốc độ 5.000 và 8.000 tx/s. Raft có độ trễ commit thấp hơn BFT khoảng 18 lần (≈0,1 s so với ≈1,9 s).

### 2.2 Độ trễ ở tải không giới hạn (không dùng được để so sánh)
- BFT sustained: P50 ≈ 44,9 s, **94% mẫu không xác nhận kịp trước deadline 30 s của công cụ** (tỷ lệ cắt 0,94).
- Raft sustained: P50 ≈ 1,9 s nhưng **~68% mẫu bị cắt** (P99 ≈ 24 s).
- Vì phần lớn mẫu bị cắt, các con số này chỉ phản ánh nhóm mẫu đầu, và còn phụ thuộc tốc độ bơm vào hàng đợi. **Không được dùng để so sánh BFT với Raft.**

---

## 3. Phân rã thời gian bên trong (BFT sustained, bật FFI trace, 3 lượt, 504 block ổn định)

Trung bình mỗi block (bỏ 2 block đầu), trung bình / trung vị, ms:

| Khâu | Trung bình | Trung vị |
|---|---|---|
| Số tx mỗi block | 5.079 | |
| Chu kỳ giữa hai lần Rust nhận commit | 338 | 288 |
| Rust chờ Go phản hồi (`rust_wait_go`) | 328 | 277 |
| Rust rảnh chờ đồng thuận (`rust_idle_consensus`) | 8,4 | 3,2 |
| Go chờ gate `waitCommitted` | **0,0** | 0,0 |
| Go: thực thi Block-STM (`exec`) | **88,6** | 73,0 |
| Go: chờ Merkle roots | 19,9 | 12,9 |
| Go: tạo block + commit RAM | 31,8 | 22,1 |
| Go: tổng thời gian committer | 51,7 | 34,7 |
| CGO tổng (`cgo_total`) | 235 | 204 |

- Thực thi EVM trên cluster thật ở trạng thái sustained: ~5.079 tx / 88,6 ms ≈ **57k tx/s** (trung vị 73 ms → ~70k tx/s). Con số này là tốc độ thực thi EVM trung bình theo block trong lúc tải liên tục, không phải tốc độ tối đa của một block đơn lẻ.
- Tổng các khâu Go đo được (thực thi + chờ roots + tạo block ≈ 140 ms) chỉ chiếm khoảng 43% thời gian Rust chờ Go (328 ms). Phần ~190 ms còn lại (chuẩn bị tx, nhận/giải mã FFI, khóa, hàng đợi...) **chưa được gán nguồn** trong phép đo này.
- Chu kỳ ~338 ms với ~5.079 tx/block suy ra ≈ 15,0k tx/s, khớp với TPS đo được (≈14,1k).
- Gate `waitCommitted` chờ ~0 ms: không phải nút thắt.

## 4. Phân rã bên trong Raft (leader, binary B, sustained, 3 lượt)

| Chỉ số | r1 | r2 | r3 |
|---|---|---|---|
| Số entry được apply (≈ số block) | 392 | 351 | 332 |
| Số tx tổng | 709.411 | 636.268 | 585.589 |
| Số tx trung bình mỗi block | ~1.810 | ~1.813 | ~1.764 |
| `status_full` / `split_batch` | 0 / 0 | 0 / 0 | 0 / 0 |
| Độ sâu hàng đợi `proposeQ` (tối đa / TB) | 0 / 0 | 0 / 0 | 0 / 0 |
| Chờ trong hàng đợi (TB / tối đa) | 0,01 / 0,03 ms | 0,01 / 0,03 ms | 0,01 / 0,04 ms |
| `raft.Apply` (replicate + quorum + fsync) TB / tối đa | 30,5 / 227,9 ms | 31,5 / 283,7 ms | 31,4 / 288,6 ms |
| FSM decode / FSM build TB | 3,9 / 7,9 ms | 4,1 / 7,9 ms | 4,0 / 7,6 ms |
| Ghi BoltDB TB / tối đa | 8,0 / 184 ms | 8,1 / 155 ms | 8,4 / 135 ms |

- Hàng đợi `proposeQ` **không bao giờ có phần tử chờ** (độ sâu tối đa 0, chờ 0,01 ms, không có `status_full`, không tách batch). Nghĩa là mọi proposal được xử lý ngay khi đến.
- Mỗi block tốn ~31 ms `raft.Apply` + ~8 ms FSM build. Ở ~5,5–6,5 block/giây (tương đương ~1.800 tx/block), chi phí này chiếm khoảng 20% thời gian, còn xa mức bão hòa 22,7 block/giây.

---

## 5. Quan sát về burst của Raft (S2, 32 lượt)

Tương quan giữa tốc độ bơm vào (`injection_tps`) và TPS hiệu dụng là **−0,94** (n=32): lượt nào bơm chậm (dưới 100k tx/s) thì TPS hiệu dụng trung vị 19.897 (15 lượt), lượt nào bơm rất nhanh (≥100k tx/s) thì 13.718 (17 lượt).
- Raft 20k cho kết quả ≈20k tx/s ở hầu hết lượt vì client bơm chậm lại (~33–40k tx/s), còn lượt 25k và 30k phần lớn bơm cực nhanh (~235–310k tx/s) và dừng ở ~13,7k tx/s.
- Lượt 25k có một ngoại lệ 24.910 tx/s với tốc độ bơm chậm (33.603 tx/s), còn lượt 20k có một ngoại lệ 13.729 với tốc độ bơm nhanh (263.516 tx/s). Hai ngoại lệ cùng chiều với quy luật trên.
- **Kết luận thận trọng:** TPS burst của Raft phụ thuộc mạnh vào cách client bơm tải (đã dùng một tham số `-batch 1000` nhưng tốc độ bơm thực tế dao động), không phản ánh năng lực xử lý ổn định. Con số đáng tin để so sánh là sustained và tốc độ cố định.

---

## 6. Kết luận

1. **Số đáng dùng (trung vị, 1 máy, cụm sạch):**
   - BFT: sustained **14,1k tx/s**; burst 25k cold-start **8,1k tx/s**.
   - Raft: sustained **10,2k tx/s**; burst 20k/25k/30k lần lượt **19,9k / 13,7k / 13,6k tx/s** (phụ thuộc cách bơm).
2. **BFT thông lượng cao hơn Raft khi tải kéo dài (≈ +36%: 14,1k so với 10,2k),** vì gom block lớn (~5.080 tx/block, ~170 block/60 s) so với Raft (~1.800 tx/block, ~340 block/60 s).
3. **Raft có độ trễ commit thấp hơn BFT khoảng 18 lần** ở cùng tốc độ bơm 5.000–8.000 tx/s (0,10 s so với 1,85–1,89 s).
4. **Cảnh báo về mô hình tin cậy:** BFT chịu lỗi Byzantine (3/4), Raft chỉ chịu lỗi sập (2/3) và cụm Raft thử nghiệm không có parent chain hay Rust FFI. So sánh chỉ mang tính tham khảo.
5. **Nút thắt chưa được xác định chắc chắn:**
   - Raft: hàng đợi propose luôn rỗng và chi phí mỗi block chỉ ~20% thời gian, nên nút thắt nằm **trước hàng đợi** (đường nhận/ack của client-ingress) hoặc ở phía client; chưa được đo trực tiếp.
   - BFT: các khâu Go đo được chiếm ~43% thời gian Rust chờ Go; ~190 ms còn lại chưa gán nguồn. Gate `waitCommitted` ≈ 0.

## 7. Hạn chế
- Một máy chung cho tất cả node; số tuyệt đối không chuyển sang triển khai nhiều máy.
- Số lượt: 3–8 mỗi cấu hình. Một vài cấu hình (warm-up n=4; trace n=3; fixed-rate n=3) có khoảng tin cậy rộng.
- Raft burst phụ thuộc tốc độ bơm thực của client; kết quả burst không đại diện cho thông lượng ổn định.
- Chưa phân rã được ~190 ms/block của BFT và nút thắt phía ingress của Raft.
- `parent_chain` dùng lại binary có sẵn; BFT chỉ dùng binary A (không có bộ đếm Raft, không ảnh hưởng BFT).
- Lượt đo S5 (đầu tiên) không lấy được log thời gian nên chỉ dùng S5b (3 lượt) cho phần phân rã.

## 8. Việc đề xuất làm tiếp
1. Đo nút thắt phía ingress của Raft: thêm bộ đếm số kết nối/độ đồng thời của `Submit`, thời gian xử lý nhận/ack mỗi batch, và thử đổi số kết nối/độ đồng thời của client.
2. Gán nguồn cho ~190 ms/block chưa giải thích của BFT (chuẩn bị tx, FFI giải mã, khóa, hàng đợi) bằng timeline nhiều điểm hơn.
3. Nếu cần so sánh BFT và Raft công bằng: đo cùng mô hình tin cậy, nhiều máy, và độ trễ ở tốc độ bơm cố định.
