# Báo cáo TPS cuối cùng: Raft và BFT, độ trễ, và vì sao thông lượng thấp hơn khả năng thực thi — 2026-10-10

**Phạm vi.** Tổng hợp mọi số đo độc lập đã làm ngày 2026-10-08 → 10-10, sau khi sửa lỗi mất giao dịch ở đường nhận tx qua TCP (`note/tps_stall_root_cause_fix_20261009.md`). Mọi con số bên dưới đều từ lượt đo thật, có file thô trong `note/evidence/tps_root_cause_fix_20261009/` và `note/evidence/tps_full_measure_20261009/`. Môi trường: **một máy** Intel Xeon Platinum 8272CL (104 vCPU, 188 GB RAM), toàn bộ node (Raft 3 node, BFT 4 validator) chạy chung máy, tải EIP-1559 từ 1.000 ví, zero-fork PASS ở mọi lượt.

**Cách đo thông lượng.** "End-to-end" = số giao dịch đã gửi chia cho thời gian từ lúc gửi tx đầu tiên đến lúc tx cuối cùng lên chain (đã chờ chain xử lý hết backlog, và đếm lại tx trong block của chính chain, không dùng số công cụ tự báo). Với burst ngắn dùng số "gồm thời gian bơm" (N ÷ (thời gian bơm + thời gian đến khi mẫu cuối có receipt)). Độ trễ lấy ở tốc độ bơm cố định (mọi tx lên chain, không bị cắt).

---

## 1. Báo cáo TPS cuối cùng

| Chỉ số | Raft (3 node) | BFT (4 validator) |
|---|---|---|
| Thông lượng end-to-end, tải bão hòa, **sau khi sửa** | **13.164 tx/s** trung vị (12.053–13.980; n=8, sustained 60 s) | **13.490 tx/s** trung vị (13.419–14.283; n=3, sustained 20 s) |
| Mất giao dịch ở tải bão hòa | **0** (8/8 lượt hoàn tất 100%) | **0** (3/3 lượt hoàn tất 100%) |
| Binary gốc (trước khi sửa), cùng giao thức | **0/3 hoàn tất**, dừng ở 210.000–250.000 tx | **0/3 hoàn tất**, dừng ở 799.000–885.000 tx |
| Burst, gồm thời gian bơm (trung vị) | 20k: 12.486 · 25k: 13.446 · 30k: 13.827 tx/s (n=5 mỗi mức) | 25k: 7.426 tx/s (n=5) |
| Độ trễ commit ở 5.000 tx/s (P50 / P99) | 413 ms / 530 ms (n=3) | 1.856 ms / 2.028 ms (n=3) |
| Độ trễ commit ở 8.000 tx/s (P50 / P99) | 412 ms / 434–621 ms (n=3) | 1.871 ms / 2.162 ms (n=3) |

Hai ghi chú để tránh hiểu sai:
- **Độ trễ Raft hôm nay (~330–410 ms) cao hơn lần đo sáng 2026-10-09 (~103 ms)** cho cùng binary gốc; binary gốc và binary đã sửa cho kết quả như nhau (326–417 ms với binary gốc, 408–413 ms với binary đã sửa), nên không do bản sửa. Nguyên nhân chưa được giải thích (xem mục 4).
- Độ trễ đo ở tải không giới hạn (P50 tới hàng chục giây) là **backlog**, không phải độ trễ của chain, và không dùng để so sánh hai chế độ.

---

## 2. Trả lời các câu hỏi

### 2.1 Vì sao độ trễ của Raft và BFT khác nhau?

Raft nhanh hơn BFT khoảng 4–5 lần (0,33–0,41 s so với 1,86 s) ở cùng tốc độ bơm. Các số đo cho thấy:
- **Raft:** một giao dịch chỉ cần qua một lần nhân bản log tới đa số (`raft.Apply` trung bình ~31 ms/block, trong đó dựng block ở FSM ~8 ms và ghi BoltDB ~8 ms), rồi đi vào pipeline xử lý block của Go ngay trong cùng tiến trình. Không có vòng đồng thuận DAG và không qua cầu FFI. Chu kỳ block ~138 ms (7,26 block/giây).
- **BFT:** giao dịch phải đi qua các vòng đồng thuận DAG của Rust rồi mới được giao cho Go qua FFI. Đo trực tiếp (3 lượt trace, 504 block): chu kỳ giữa hai lần Rust nhận commit **~338 ms**, thời gian Rust chờ Go phản hồi ~328 ms, tổng thời gian của một lệnh gọi CGO (gồm cả xử lý phía Go) ~235 ms/block.
- Độ trễ BFT ≈ 1,86 s tương đương ~5–6 chu kỳ block (338 ms); độ trễ Raft ≈ 0,4 s tương đương ~3 chu kỳ block (138 ms). Đây là quan sát về tỷ lệ, **chưa phải bằng chứng nhân quả**.

Mô hình tin cậy cũng khác: BFT chịu lỗi Byzantine (3/4), Raft chỉ chịu lỗi sập (2/3), và cụm Raft thử nghiệm không có parent chain hay Rust FFI. So sánh độ trễ chỉ mang tính tham khảo.

### 2.2 Vì sao thông lượng Raft và BFT gần như bằng nhau?

Vì cả hai chế độ **dùng chung cùng một pipeline xử lý block của Go**, và chính pipeline đó là nút thắt, không phải lớp đồng thuận.
- Chi phí tuần tự trên mỗi giao dịch gần như giống hệt: **Raft ~69 µs/tx** (14.529 tx/s ở đoạn ổn định, 7,26 block/giây × 2.000 tx) và **BFT ~67 µs/tx** (338 ms cho ~5.079 tx/block).
- Hai chế độ hình thành block khác nhau (Raft ~2.000 tx/block × 7,3 block/giây; BFT ~5.000 tx/block × 2,9 block/giây) nhưng cùng ra ~13–15k tx/s, tức cùng một chi phí mỗi tx.
- Lớp đồng thuận không phải điểm nghẽn ở Raft: hàng đợi propose luôn rỗng, `raft.Apply` chiếm ~22% thời gian và hàng đợi sink phía sau FSM gần như không chờ (~0,004 ms).

Lưu ý khi diễn giải: mình đã **thử và bác bỏ** giả thuyết nút thắt nằm ở forwarder (khâu đưa tx từ pool vào consensus) bằng thí nghiệm đối chứng:

| Cấu hình forwarder (Raft sustained 60 s, 3 lượt mỗi cấu hình) | End-to-end (trung vị) |
|---|---|
| Hiện tại (trần 8.000 tx/nhịp, rút ×5) | 13.503 tx/s |
| Trần 20.000, rút ×5 | 13.498 tx/s |
| Trần 40.000, rút ×5 | 13.335 tx/s |
| Trần 8.000, rút ×1 | 11.628 tx/s |
| Trần 20.000, rút ×1 | 11.814 tx/s |

Đổi trần hoặc lượng rút không làm thông lượng tăng, nên forwarder không phải nút thắt. Riêng con số "trần 8.000 tx/nhịp × 1,8 nhịp/giây ≈ 14,6k" tình cờ gần với thông lượng đo được nhưng **không phải nguyên nhân** (mình từng đưa ra giải thích này và đã bác bỏ).

### 2.3 Riêng phần thực thi Block-STM xử lý được hàng trăm nghìn tps không?

**Chưa đến "hàng trăm nghìn", nhưng ở mức hàng chục nghìn đến hơn một trăm nghìn tx/s cho riêng khâu thực thi.** Số đo thật:

| Bối cảnh | Block-STM | Quy ra |
|---|---|---|
| Raft sustained, block 2.000 tx, 755 block | 21,5 ms/block trung bình (20,1 ms trung vị) | ~**93k tx/s** |
| BFT sustained, block ~5.079 tx, 504 block | 88,6 ms/block trung bình (73,0 ms trung vị) | ~**57k tx/s** (~70k theo trung vị) |
| Một lượt burst block 8.000 tx (timeline Phase B của agent khác, `note/tps_phase_b_validation_report_20261008.md`) | 61,85 ms/block | ~**129k tx/s** |

Mức cao nhất (129k) là chuyển khoản gốc từ nhiều ví (gần như không xung đột) trong một lượt burst. Với tải sustained, con số là 57–93k tx/s. Đây là tốc độ của **một khâu** chứ không phải thông lượng của hệ thống. Chưa có bài đo Block-STM với giao dịch gọi hợp đồng hoặc nhiều xung đột.

### 2.4 Vì sao thông lượng tổng thể vẫn thấp so với khả năng xử lý?

Vì thông lượng bằng **số tx trong block chia cho chu kỳ block**, và chu kỳ block là **tổng nối tiếp** của nhiều khâu, trong đó Block-STM chỉ là một phần nhỏ.

**Raft** (mỗi block 2.000 tx; trung bình trên 755 block của một lượt khỏe):

| Khâu | ms/block |
|---|---|
| `PrepareTransactions` (giải mã, kiểm tra ràng buộc chữ ký; xem 2.5) | 30,6 |
| Block-STM | 21,5 |
| Root tx / root receipt (chạy song song) | 8,5 / 7,4 |
| IntermediateRoot (tài khoản) | 3,9 |
| Commit vào bộ nhớ | 13,3 |
| **Tổng các khâu đã đo (không tính Raft)** | **~78** |
| **Chu kỳ block thực tế** | **~138** |

Các khâu đã đo cộng lại chỉ ~78 ms; nếu chỉ có chừng đó thì sẽ đạt ~25,7k tx/s, trong khi thực tế là 14,5k. Còn **~60 ms/block (khoảng 45%) chưa gán nguồn** (có thể gồm `raft.Apply` nếu nó nối tiếp, kiểm tra chữ ký, gom nhóm tx, tạo block và ghi DB, phát biên nhận, lập chỉ mục). Khâu tốn nhất đã đo là `PrepareTransactions` (30,6 ms), lớn hơn Block-STM.

**BFT** (block ~5.079 tx, chu kỳ ~338 ms): thực thi 88,6 + chờ root 19,9 + tạo block và commit RAM 31,8 ≈ 140 ms (41% chu kỳ); phần còn lại (~190 ms, ~59%) chưa gán nguồn.

Để đạt 100k tx/s với block 5.000 tx, chu kỳ block phải ≤ 50 ms, trong khi chỉ riêng các khâu đã đo đã ~78–140 ms. Vì vậy muốn tăng rõ rệt phải giảm chi phí các khâu nối tiếp (nhất là `PrepareTransactions` và phần chưa gán nguồn) hoặc chồng lấn chúng giữa các block liên tiếp, thay vì tăng tốc Block-STM.

Một lý do khác khiến số tổng thấp hơn số "trăm nghìn" thường được nhắc: các số lớn đó thường là thực thi riêng, không gồm ingress, kiểm tra chữ ký, root, commit, ghi DB và phát biên nhận; còn con số 13k ở đây gồm toàn bộ đường đi từ lúc gửi đến khi tx nằm trong block.

### 2.5 Khâu chậm nhất đã đo: `PrepareTransactions` là gì

**Vị trí.** `execution/cmd/simple_chain/processor/speculative_executor.go`, hàm `PrepareTransactions`. Mỗi validator gọi hàm này **cho từng block**, ngay đầu quá trình xử lý block (sau cổng chờ block trước, trước khi sao chép trạng thái và thực thi). Đầu vào là block thô nhận từ Raft hoặc Rust (`ExecutableBlock`, giao dịch còn ở dạng protobuf chưa giải mã); đầu ra là danh sách giao dịch **đã giải mã, đã lọc, đã loại trùng và đã sắp xếp theo thứ tự xác định**, để mọi node dựng ra cùng một block và cùng state root.

**Nó làm gì (theo đúng mã hiện tại):**
1. **Giải mã song song** các giao dịch (`ParallelUnmarshalTransactions`, tối đa 16 worker; bỏ qua digest rỗng hoặc toàn 0).
2. **Kiểm tra ràng buộc phong bì** (`ValidateEnvelopeBindingsParallel`, tối đa 16 worker): với giao dịch Ethereum có `RawEnvelope`, đối chiếu từng trường thực thi của protobuf với phong bì gốc (giải mã RLP, dùng signer để khôi phục người gửi, tức ecrecover, có memoize) và **loại** giao dịch không khớp, để một biến thể giả mạo không chiếm chỗ hash trước giao dịch thật.
3. **Loại trùng** theo hash giao dịch (giữ bản hợp lệ đầu tiên).
4. **Sắp xếp theo hash** (so sánh byte). Thứ tự này là một phần của kết quả xác định (nó quyết định thứ tự thực thi và do đó state root), nên không được bỏ hay đổi nếu không có kế hoạch chuyển đổi cho toàn cụm.

**Vì sao đây là khâu chậm nhất trong các khâu đã đo.** Ở Raft, một block 2.000 tx tốn trung bình **30,6 ms** (trung vị 29,4 ms, 755 block), lớn hơn Block-STM (21,5 ms) và lớn hơn các khâu root, commit. Phần đắt nhất bên trong nó là bước 2, vì mỗi giao dịch EIP-1559 phải giải mã RLP và khôi phục chữ ký secp256k1. Số đo cũ trước khi song song hóa (block 8.000 tx, nguồn `note/tps_prepare_tx_opt_report_20261008.md`):

| Bước | Trước khi song song hóa | Ghi chú |
|---|---|---|
| Kiểm tra ràng buộc phong bì (ecrecover) | **332,3 ms** (~58% chu kỳ block lúc đó) | chạy tuần tự đơn luồng |
| Giải mã | ~2,5 ms | |
| Loại trùng | ~6,8 ms | |
| Sắp xếp theo hash | ~1,5 ms | không phải phần đắt |
| **Tổng** | **~345 ms** | benchmark: 486,9 ms → **93,3 ms** sau khi song song hóa (nhanh gấp 5,22 lần) |

Sau khi song song hóa, còn ~30,6 ms cho block 2.000 tx (Raft). Đo ở mức vi mô (`BenchmarkPrepareTransactions_*`), chưa đo riêng từng bước trên cụm đang chạy tải bão hòa.

**Lưu ý quan trọng.** Đây là khâu chậm nhất **trong số các khâu đã đo**, không phải khâu chậm nhất tuyệt đối: ~60 ms/block của Raft (và ~190 ms/block của BFT) vẫn chưa gán nguồn. Khâu này đáng chú ý vì (a) mọi validator tự chạy lại nó dù giao dịch đã được xác minh ở ingress, nên có khả năng xác minh chữ ký lặp nhiều lần (ở ingress, ở đây, và lúc thực thi) và (b) nó nằm trên đường tuần tự của chu kỳ block. Cả hai ý trên mới là giả thuyết, chưa kiểm chứng bằng profile.

---

## 3. Điều đã được sửa trong đợt này (để các số trên đáng tin)

Trước khi sửa, đường nhận giao dịch qua TCP bỏ giao dịch khi quá tải (ngắt kết nối, evict/reject, bỏ khi hàng đợi đầy hoặc khi client đóng kết nối) trong khi client không đọc phản hồi; mỗi giao dịch bị bỏ để lại khoảng trống nonce và pool mắc kẹt, chain dừng ở 210.000–885.000 tx. Sau sửa: backpressure lossless, không mất giao dịch, không dừng (8/8 Raft và 3/3 BFT hoàn tất 100%). Chi tiết và danh sách thay đổi mã ở `note/tps_stall_root_cause_fix_20261009.md`.

---

## 4. Hạn chế và những điều chưa biết

- **~60 ms/block của Raft và ~190 ms/block của BFT chưa gán nguồn**; chưa có profile CPU của pipeline Go nên chưa biết khâu nào đáng tối ưu trước. Số từng khâu của Raft lấy từ một lượt duy nhất (755 block).
- **Độ trễ Raft ở tốc độ cố định đổi từ ~103 ms lên ~330–410 ms** giữa hai buổi đo, cho cả binary gốc lẫn binary đã sửa, kể cả khi dùng lại binary blast cũ; máy không có tải nền đáng kể. Chưa giải thích được.
- **Mọi số đo trên một máy chung nhiều node**; số tuyệt đối sẽ khác khi triển khai nhiều máy. Chưa thử với SDK client thật.
- **Block-STM chỉ đo với chuyển khoản gốc, ví phân tán**; chưa đo giao dịch hợp đồng hay tải nhiều xung đột.
- **Head-of-line blocking** của lệnh đọc trên server TCP lúc bão hòa chưa đo (RPC HTTP không bị ảnh hưởng).
- Burst ngắn (2–5 giây): số end-to-end nhiễu vì nhịp poll nên dùng số gồm thời gian bơm.
- Hai chế độ khác mô hình tin cậy (BFT và CFT) nên so sánh TPS chỉ mang tính tham khảo.

## 5. Việc tiếp theo đề xuất

1. Lấy **CPU profile** một validator Raft lúc bão hòa để gán nguồn cho ~60 ms/block, đặc biệt kiểm tra chữ ký bị xác minh lặp (ingress, `PrepareTransactions`, lúc thực thi).
2. Cân nhắc chạy `PrepareTransactions` của block N+1 song song với thực thi block N (chồng lấn khâu), chỉ sau khi profile xác nhận và với kiểm tra zero-fork.
3. Giải thích sự lệch độ trễ Raft ~103 ms ↔ ~330–410 ms bằng cách đo lại với đúng cấu hình và trạng thái đĩa của buổi sáng.
4. Đo Block-STM với giao dịch hợp đồng và tải xung đột; đo head-of-line blocking của lệnh đọc.
5. Review kỹ thay đổi ingress trước khi merge vào `main`.

## 6. Evidence

- `note/evidence/tps_root_cause_fix_20261009/raw/`: JSON và log của mọi lượt (ma trận `m_*`, đối chiếu `m2_*`, `m3_*`, thí nghiệm forwarder `m4_*`, `m5_*`, kiểm chứng rút cạn `s31`–`s35`, kịch bản khoảng trống nonce `s40`), script đo ở `scripts/`.
- `note/evidence/tps_full_measure_20261009/`: ma trận đo ban đầu, trace thời gian từng giai đoạn của BFT, bộ đếm vi mô Raft.
