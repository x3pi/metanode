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

## 0. TPS và độ trễ trong báo cáo này được tính như thế nào

Số liệu do công cụ `execution/cmd/tool/secp_tps_blast` tạo ra. Tải đi qua đường thật: gửi qua TCP đến node → đồng thuận → thực thi → block → receipt đọc qua RPC. Giao dịch được **ký sẵn trước khi bơm**, nên thời gian ký không nằm trong số đo. Cách tính khác nhau theo chế độ, và đây là điều cần biết khi đọc mọi con số:

| Chế độ | Cửa sổ thời gian | Tử số | Điểm cần lưu ý |
|---|---|---|---|
| **Burst Nk** (bơm N tx một lần) | Công cụ báo `commit_duration` bắt đầu **sau khi bơm xong** và kết thúc khi 50 tx mẫu có receipt | N tx, ngoại suy từ 50 mẫu | **Không gồm thời gian bơm.** Nếu bơm chậm thì chain đã xử lý trong lúc bơm và số công cụ báo bị đẩy lên. Báo cáo này dùng **N ÷ (thời gian bơm + `commit_duration`)** làm số chính, tức từ lúc gửi tx đầu tiên đến lúc mẫu cuối có receipt. |
| **Sustained 60 s** (bơm không giới hạn tốc độ) | 60 giây kể từ lúc bắt đầu bơm | Số tx **có trong block** lúc kết thúc, đếm lại bằng `eth_getBlockByNumber` (sau tối đa 20 s drain, không tính vào mẫu số) | Là "tx đã lên chain ÷ 60 s", không phải tx đã gửi. Tx được nhận nhưng không vào chain bị loại khỏi tử số (xem mục 5). |
| **Tốc độ cố định** (5.000 / 8.000 tx/s) | 60 giây, `-rate-limit` | Tx trong block ÷ 60 s | Số đo bằng tốc độ bơm khi chain theo kịp. |

**Độ trễ:** mỗi mẫu là tx đầu của mỗi 5 batch; độ trễ là thời gian từ lúc gửi batch đến lúc `eth_getTransactionReceipt` có kết quả (poll mỗi 50–100 ms). Chỉ **độ trễ ở tốc độ cố định** (mục 2.1) là thời gian "gửi → nhận receipt" đúng nghĩa và không bị cắt. Ở burst, độ trễ tính từ lúc bắt đầu bơm và bị chặn dưới bởi thời gian bơm (poll chỉ bắt đầu sau khi bơm xong). Ở tải không giới hạn, phần lớn mẫu bị cắt ở deadline (mục 2.2).

---

## 1. Kết quả thông lượng (tx/s)

| Cấu hình | n | Trung vị | Trung bình | SD | Khoảng | Công cụ báo (trung vị) | Số block/lượt |
|---|---|---|---|---|---|---|---|
| **Raft burst 20k — A**, gồm thời gian bơm | 8 | **12.956** | 12.952 | 444 | 12.441–13.745 | 19.854 | 11–13 |
| Raft burst 20k — B | 8 | 13.066 | 13.140 | 471 | 12.443–13.900 | 19.602 | 11–14 |
| **Raft burst 25k — A** | 8 | **13.034** | 13.281 | 507 | 12.964–14.305 | 13.719 | 12–16 |
| **Raft burst 30k — A** | 8 | **12.982** | 13.057 | 382 | 12.530–13.699 | 13.606 | 14–16 |
| **BFT burst 25k, cold-start (cụm mới)** | 8 | **7.804** | 7.700 | 411 | 7.135–8.131 | 8.111 | 2–4 |
| BFT burst 25k, warm-up (tải mồi 10k + nghỉ 10 s) | 4 | 8.533 | 8.313 | 799 | 7.166–9.021 | 8.845 | 3 |
| **BFT sustained 60 s** | 8 | **14.072** | 14.042 | 574 | 13.247–14.795 | 14.072 | 154–179 |
| BFT sustained 60 s (bật FFI trace, S5b) | 3 | 14.130 | 14.280 | 350 | 14.030–14.680 | 14.130 | 169 |
| Raft sustained 60 s — binary A (không bộ đếm) ¹ | 8 | 10.321 | 10.321 | 374 | 9.653–11.034 | 10.321 | 328–369 |
| Raft sustained 60 s — binary B (có bộ đếm) ¹ | 8 | 10.147 | 10.252 | 366 | 9.816–10.900 | 10.147 | 332–364 |
| Raft sustained 60 s — B kèm lấy metric (S7) ¹ | 3 | 10.540 | 10.672 | 1.030 | 9.714–11.761 | 10.540 | 332–392 |

¹ **Raft sustained không phải thông lượng ổn định của Raft:** trong mọi lượt Raft ngừng tạo block ở giây ~40–44 và 19–23% tx đã được nhận không bao giờ lên chain (mục 5). Số 10,2k là "tx đã lên chain ÷ 60 s" của một cụm đã dừng hoạt động giữa chừng.

Ghi chú về cách đọc:
- Với burst, số chính là số **gồm thời gian bơm**. Cột "công cụ báo" chỉ để đối chiếu; nó cao hơn khi bơm chậm (Raft 20k: bơm ~0,5 s rồi chờ ~1,0 s).
- Sau khi tính thời gian bơm, Raft burst **phẳng ở ≈13,0k tx/s** ở cả 20k, 25k và 30k (12.956 / 13.034 / 12.982), nên khác biệt giữa các mức tải chỉ là hệ quả của cách tính cũ.
- Raft sustained: A và B khác nhau −1,7% theo trung vị, Welch t = −0,37, **không có khác biệt có ý nghĩa**, nên bộ đếm vi mô không làm giảm thông lượng.
- BFT sustained đã đối chiếu bằng dấu thời gian block (3 lượt trace): ~15,0–15,2k tx/s trong 55–58 giây có block, nên 14,0–14,7k là hợp lý và hơi thấp (mẫu số 60 s).

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

## 5. Raft sustained: cụm ngừng tạo block giữa chừng (S8, S9)

### 5.1 Dữ liệu
| Chỉ số | S8 r1 | S8 r2 | S1 A r1 | BFT sustained r1 |
|---|---|---|---|---|
| Tx client báo đã gửi | 891.830 | 823.473 | 856.183 | 839.000 |
| Tx có trong block lúc kết thúc | 697.783 | 630.842 | 665.754 | 830.000 |
| Tx đã nhận nhưng không lên chain | **194.047 (21,8%)** | **192.631 (23,4%)** | **190.429 (22,2%)** | 9.000 (1,1%) |
| Thời điểm block cuối (dấu thời gian block, từ lúc bắt đầu bơm) | +42 s | +40 s | | |
| Tốc độ commit trong khoảng có block (số tx ÷ khoảng thời gian block) | 16.614 tx/s | 16.175 tx/s | | ~15.100 tx/s |

- Dấu thời gian block có độ phân giải 1 giây. Tx lên chain theo từng cửa sổ 10 giây (S8 r1): 223.207 → 198.193 → 124.000 → 133.703 → 18.680 (giây 0–10, 10–20, 20–30, 30–40, 40–50). Cửa sổ 0–20 giây đạt ~20–22k tx/s, sau đó giảm còn ~12–13k tx/s rồi dừng.
- Client bơm đến giây 60 (885.364 tx tại giây 45 ở r1), nhưng chain không có block nào sau giây ~42.

### 5.2 Điều tra bằng log node (S9: một lượt, binary B, giữ log; chuỗi 2 giây/mẫu)
Số liệu trích trong `raw/s9_raft_investigate_key_log_lines.txt` và `raw/s9_raft_investigate_series.json`:
- **Có bầu lại leader giữa chừng.** Log node n2 lúc 09:01:44.204: `raft: heartbeat timeout reached, starting election: last-leader-addr=127.0.0.1:7110 last-leader-id=n0`; log n0 lúc 09:01:44.638: `rejecting pre-vote request since we have a leader: ... leader=127.0.0.1:7112 leader-id=n2`. Trên leader ban đầu (n0), số proposal đưa vào hàng đợi dừng ở 301, khớp với số entry đã apply tại giây ~30 (301); các entry sau đó (đến 379) đến qua đường forward.
- **Áp lực bộ nhớ ngay trước đó.** n0 lúc 09:01:42: `OOM-GUARD CRITICAL memory pressure ... Forcing GC + FreeOSMemory` và `RESOURCE_MONITOR: Very high memory system usage: 7786 MB (Limit: 8 GB)`; sau đó 8.009 MB ở 09:02:12 và 09:02:42.
- **Cấu hình Raft của phép đo rất nhạy:** `heartbeat_timeout_ms=100`, `election_timeout_ms=200`, `leader_lease_timeout_ms=80`, `commit_timeout_ms=30` (trong harness).
- **Hàng đợi propose không phải nguyên nhân:** độ sâu tối đa 0, `status_full=0`, `split_batch=0`.
- **Sau khi block dừng:** log `ProcessPoolSub` trên n0 lặp lại `allTxs≈40.100–40.300, validTxs=0, futureTxs=allTxs` (các mẫu trích từ 09:02:01 đến hết), tức pool chỉ còn các tx có nonce lớn hơn nonce kỳ vọng, không tx nào thực thi được. Không có cảnh báo `Mempool is full`/eviction (`MaxMempoolSize=200000`). `FutureTxTimeout=5 phút` nên chưa có tx nào bị xóa theo TTL trong thời gian chạy.
- Cả 3 node cuối chạy có cùng số block (379), nên các node vẫn nhất quán (zero-fork PASS).

### 5.3 Diễn giải (đã có bằng chứng so với còn là giả thuyết)
- **Đã có bằng chứng:** (a) Raft dừng tạo block ở giây ~40–44 trong mọi lượt sustained quan sát được (S8 ×2, S9); (b) 19–23% tx được client báo đã gửi không lên chain; (c) có một lần bầu lại leader ở giây ~30 do hết thời gian heartbeat, sau áp lực bộ nhớ gần giới hạn 8 GB; (d) lúc dừng, pool của node nhận chỉ còn tx có nonce ở tương lai.
- **Giả thuyết chưa kiểm chứng:** lần bầu lại leader làm mất các batch đang bay (hoặc tx bị bỏ ở đường forward), tạo khoảng trống nonce cho từng ví; các tx sau khoảng trống nằm mãi ở trạng thái "future" nên không có block mới. Chưa xác định tx nào bị mất và ở bước nào.
- **Hệ quả cho việc đọc số liệu:** ở tải bão hòa không giới hạn, Raft có thể nhận tx rồi không đưa lên chain (mất im lặng, chưa rõ có phải chỉ do cấu hình timeout quá gắt trong phép đo hay không). Con số "Raft sustained" **không phải năng lực ổn định** của Raft. Khi tốc độ bơm cố định ở 5.000–8.000 tx/s thì không có hiện tượng này (confirmed = submitted).


### 5.4 Thử nới timeout Raft (S10: 8 lượt, binary A, xen kẽ ngẫu nhiên, seed 20261010)
So sánh `heartbeat/election/lease` hiện tại **100/200/80 ms** với **500/1000/400 ms**, Raft sustained 60 s, 4 lượt mỗi nhóm:

| Nhóm | Lượt | TPS công cụ báo | Tx đã gửi | Tx lên chain | Không lên chain | Khoảng thời gian có block | Dòng "starting election" |
|---|---|---|---|---|---|---|---|
| 100/200/80 | r1 | 10.581 | 801.543 | 638.484 | 20,3% | 40 s | 0 |
| 100/200/80 | r2 | 10.440 | 786.274 | 629.860 | 19,9% | 40 s | 0 |
| 100/200/80 | r3 | 10.055 | 758.348 | 606.602 | 20,0% | 39 s | 0 |
| 100/200/80 | r4 | 9.825 | 786.814 | 592.434 | 24,7% | 38 s | 0 |
| 500/1000/400 | r1 | 10.133 | 759.015 | 609.801 | 19,7% | 39 s | 0 |
| 500/1000/400 | r2 | **4.132** | **250.000** | 248.000 | 0,8% | **15 s** | 2 |
| 500/1000/400 | r3 | **3.749** | **227.000** | 225.000 | 0,9% | **13 s** | 2 |
| 500/1000/400 | r4 | **3.932** | **239.000** | 236.000 | 1,3% | **15 s** | 2 |

- **Nới timeout không khắc phục hiện tượng dừng.** Nhóm 100/200/80 (4/4 lượt) và một lượt 500/1000/400 vẫn dừng ở giây ~38–40 và mất ~20–25% tx.
- **Ba trong bốn lượt 500/1000/400 rơi vào một chế độ khác:** client chỉ gửi được 227.000–250.000 tx rồi bị chặn (tiến độ bơm không tăng từ giây 15), block chỉ kéo dài 13–15 s, và TPS công cụ báo ~3,7–4,1k. Đây cùng dạng với kết quả "~3,9k" của lần chạy Phase 2 (253.000 và 222.000 tx lên chain, 112–129 block), nên 3,9k là **một chế độ hỏng có thật và tái hiện được**, không phải nhiễu đo, nhưng không phải thông lượng của Raft.
- Chế độ này xuất hiện với cấu hình 500/1000/400 nhiều hơn (3/4), trong khi cấu hình hiện tại không gặp trong 4 lượt của S10 (hay trong 11 lượt Raft sustained trước đó của báo cáo này). Chưa đủ lượt để kết luận tần suất, nhưng không có dấu hiệu nới timeout là cách chữa.
- **Kết luận:** nguyên nhân Raft ngừng nhận/tạo block dưới tải bão hòa chưa được xác định; cần theo dõi từng tx (đường TCP ingress → `Submit` → forward → pool) thay vì chỉnh timeout.

---

## 6. Kết luận

1. **Số đáng dùng (trung vị, 1 máy, cụm sạch):**
   - BFT: sustained **14,1k tx/s** (đối chiếu block-time 15,0–15,2k); burst 25k cold-start **7,8k tx/s** (gồm thời gian bơm).
   - Raft: burst 20k/25k/30k đều **≈13,0k tx/s** (gồm thời gian bơm). Raft sustained **chưa có số ổn định** vì cụm dừng tạo block ở giây ~40–44; trong khoảng còn tạo block, tốc độ commit là ~16,2–16,6k tx/s (≈20–22k tx/s trong 20 giây đầu), dấu thời gian block có độ phân giải 1 giây.
2. **Raft có độ trễ commit thấp hơn BFT khoảng 18 lần** ở cùng tốc độ bơm 5.000–8.000 tx/s (0,10 s so với 1,85–1,89 s), và giữ đủ tốc độ 5.000 và 8.000 tx/s không mất tx.
3. **BFT mất ít tx hơn nhiều ở tải bão hòa** (1,1% so với 22–23% của Raft), tuy vậy cũng không phải 0%.
4. **Cảnh báo về mô hình tin cậy:** BFT chịu lỗi Byzantine (3/4), Raft chỉ chịu lỗi sập (2/3) và cụm Raft thử nghiệm không có parent chain hay Rust FFI. So sánh chỉ mang tính tham khảo.
5. **Nút thắt:**
   - Raft burst: ≈13,0k tx/s phẳng theo cỡ tải; hàng đợi propose luôn rỗng và chi phí mỗi block (~31 ms apply + ~8 ms FSM) chỉ chiếm ~20% thời gian ở tốc độ block quan sát được. Chưa xác định giới hạn ở đâu.
   - Raft sustained: vấn đề ổn định (bầu lại leader, khoảng trống nonce), không phải giới hạn thông lượng.
   - BFT: các khâu Go đo được chiếm ~43% thời gian Rust chờ Go; ~190 ms còn lại chưa gán nguồn. Gate `waitCommitted` ≈ 0.

## 7. Hạn chế
- Một máy chung cho tất cả node; số tuyệt đối không chuyển sang triển khai nhiều máy.
- Số lượt: 3–8 mỗi cấu hình. Warm-up (n=4), trace (n=3), tốc độ cố định (n=3) có khoảng tin cậy rộng.
- Phần điều tra Raft dừng giữa chừng dựa trên 3 lượt có dấu thời gian block/log (S8 ×2, S9 ×1) và 8 lượt thử timeout (S10). Chuỗi nhân quả (bầu lại leader → khoảng trống nonce) chưa được chứng minh.
- Dấu thời gian block có độ phân giải 1 giây, nên tốc độ theo cửa sổ nhỏ chỉ mang tính tham khảo.
- Các lượt BFT dùng binary A (không có bộ đếm Raft, không ảnh hưởng BFT). `parent_chain` dùng binary có sẵn.
- Lượt trace đầu tiên (S5) không lấy được log thời gian nên chỉ dùng S5b (3 lượt) cho phần phân rã.
- Chưa so sánh BFT burst với độ trễ gồm thời gian bơm trong cùng chế độ (BFT bơm ~0,1 s nên chênh lệch nhỏ: 7.804 so với 8.111).

## 8. Việc đề xuất làm tiếp
1. **Tìm nguyên nhân Raft ngừng nhận/tạo block dưới tải bão hòa** (nới timeout đã thử ở mục 5.4 và không chữa được): theo dõi từng tx đi qua TCP ingress → `Submit` → forward → pool; kiểm tra khoảng trống nonce của vài ví trước/sau khi dừng; thử giới hạn bộ nhớ cao hơn (`GOMEMLIMIT`); thêm bộ đếm tx bị `Submit` trả về false.
2. **Sửa công cụ blast:** báo thêm "TPS tính từ lúc gửi đầu tiên đến receipt cuối", số tx được nhận nhưng không lên chain, và số block cuối cùng theo dấu thời gian; ghi rõ định nghĩa mẫu số trong output.
3. Gán nguồn cho ~190 ms/block chưa giải thích của BFT (chuẩn bị tx, FFI giải mã, khóa, hàng đợi) bằng timeline nhiều điểm hơn.
4. Nếu cần so sánh BFT và Raft công bằng: cùng mô hình tin cậy, nhiều máy, tốc độ bơm cố định và độ trễ gửi → receipt.
