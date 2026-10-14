# Vì sao commit chậm khi backlog lớn (profile), ảnh hưởng của giới hạn bộ nhớ Go, và Raft đo bằng công cụ đếm — 2026-10-14

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/node_profile_backlog_20261014/` (manifest `MANIFEST.json`). Tiếp nối `note/tps_by_volume_report_20261014.md`.

## 1. Câu hỏi
Báo cáo trước cho thấy tốc độ commit của node hiện tại giảm khi backlog tăng (13,1k tx/s ở 0,5 triệu tx xuống 10,0k ở 4 triệu tx, tương quan thứ hạng −0,91). Phần nào của node tiêu tốn thời gian thêm đó? Raft có giữ kết quả tốt khi đo bằng công cụ đếm mới không?

## 2. Cách làm
Binary đã sửa lỗi mất tx; BFT 4 validator (và Raft 3 replica) trên một máy; cụm mới cho mỗi lượt; công cụ đo đếm; cờ `-max-txs` đặt chính xác khối lượng tx. Runner thêm `--profile-at` (chụp profile CPU 30 giây, heap và goroutine của validator 0 giữa pha xả backlog, kèm mẫu CPU/RSS mỗi 10 giây) và `--node-env` (đặt biến môi trường cho node). Profile đọc bằng `/tmp/p06_bins/pprof` (bản `go tool pprof` trên máy lỗi khởi động, không liên quan dữ liệu). Bộ canh bộ nhớ chạy nền, dừng cụm nếu bộ nhớ khả dụng của máy xuống dưới 20 GB. Mọi lượt: gửi = nhận = commit, parity OK trên mọi validator/replica.

## 3. Kết quả [ĐO]
### 3.1 Profile CPU của validator 0 giữa pha xả backlog (2 lượt mỗi mức)
| Khối lượng tx | TPS đầu-cuối | GC (`gcDrain`, % CPU) | Gọi sang Rust/C (`cgocall`, % CPU) | Heap Go đang sống | RSS lớn nhất | Evidence |
|---|---|---|---|---|---|---|
| 1 triệu (lượt 1) | 15111 | 10,8 | 35,5 | 907 MB | 11,9 GB | evidence:nb_profile |
| 1 triệu (lượt 2) | 15398 | 11,3 | 35,4 | 861 MB | 11,8 GB | evidence:nb_profile |
| 4 triệu (lượt 3) | 12603 | 29,2 | 22,4 | 2045 MB | 16,3 GB | evidence:nb_profile |
| 4 triệu (lượt 4) | 12485 | 30,1 | 21,9 | 2340 MB | 17,1 GB | evidence:nb_profile |

- Ở 4 triệu tx, thu gom rác của Go (GC) chiếm khoảng 29–30% CPU, so với khoảng 11% ở 1 triệu tx. Heap Go đang sống tăng khoảng 2,3 lần, và RSS tăng khoảng 40% [TÍNH từ evidence:nb_profile].
- Tổng CPU dùng trong cửa sổ 30 giây tăng (287 và 279 giây CPU ở 1 triệu, so với 374 và 423 giây ở 4 triệu) nhưng commit chậm hơn: phần CPU tăng thêm đi vào GC, không vào xử lý tx (evidence:nb_profile).
- Những đối tượng chiếm nhiều heap nhất ở 4 triệu tx: `smart_contract.NewExecuteSCResult` (khoảng 542 MB, 27% heap ở lượt 3, so với 179 MB ở 1 triệu), `sync.Map.Swap` (khoảng 411 MB), `blockchain.AddTxToCache` (khoảng 137 MB), `tx_processor.storeVerifiedBound` (cum khoảng 451 MB). Chúng tăng theo số tx đang chờ [ĐO trong file heap profile, evidence:nb_profile].

### 3.2 Giới hạn bộ nhớ Go (`META_GOMEMLIMIT`)
**[ĐỌC CODE]** `execution/cmd/simple_chain/main.go` đặt mặc định `GOGC=800` và `GOMEMLIMIT=8 GB`; biến môi trường `GOGC` không có tác dụng; `META_GOMEMLIMIT` (đơn vị GB) hoặc cấu hình mới đổi được giới hạn.

Thí nghiệm ghép cặp, 4 triệu tx, 4 cặp xen kẽ, thứ tự ngẫu nhiên (seed 20261018):
| Biến thể | TPS đầu-cuối (trung bình, tx/s) | Evidence |
|---|---|---|
| Mặc định (8 GB) | 12887,7 | evidence:nb_gomem |
| `META_GOMEMLIMIT=12` | 13348,0 | evidence:nb_gomem |
- Chênh +460,3 tx/s (+3,6%), t = 2,67, df = 3, p = 0,0755, CI95 [−87,8; 1008,4]: 12 GB nhanh hơn ở cả 4 cặp nhưng không đạt mức ý nghĩa 5% với n = 4 [TÍNH từ evidence:nb_gomem].

Profile một lượt mỗi cấu hình, 4 triệu tx (cùng đợt chạy):
| Cấu hình | TPS đầu-cuối | GC (% CPU) | RSS lớn nhất (val0) | Evidence |
|---|---|---|---|---|
| Mặc định (8 GB) | 13190 | 29,0 | 16,0 GB | evidence:nb_profile2 |
| 12 GB | 13492 | 26,6 | 20,3 GB | evidence:nb_profile2 |
| 24 GB | 14055 | 19,5 | 31,8 GB | evidence:nb_profile2 |
- (evidence:nb_profile2) Nâng giới hạn làm GC giảm (29,0% → 19,5% CPU) và tốc độ tăng 6,6% ở 24 GB, nhưng RSS của một validator tăng gần gấp đôi (16 → 32 GB). Ở mức 1 triệu tx (không bị ép), tốc độ là 15,1–15,4k tx/s; vậy ở 4 triệu tx vẫn còn khoảng 7% chưa giải thích sau khi gần như bỏ ràng buộc bộ nhớ [TÍNH].
- Số tuyệt đối của đợt đêm cao hơn hẳn đợt ban ngày (4 triệu tx mặc định: 10,0k trong quét khối lượng, 12,9–13,2k trong đợt này); chỉ so ghép cặp trong cùng đợt chạy là đáng tin (evidence:nb_gomem, evidence:nb_profile2).

### 3.3 Raft 3 replica, đo bằng công cụ đếm
| Chế độ | Số lượt | TPS đếm đầu-cuối (trung bình ± SD, tx/s) | Evidence |
|---|---|---|---|
| Burst 25.000 tx | 5 | 13121,2 ± 310,4 | evidence:nb_raft_burst |
| Sustained 1 triệu tx | 5 | 20337,6 ± 285,6 | evidence:nb_raft_sust |
- Cả 10 lượt: không mất tx, cả 3 replica sống đến cuối, parity OK (evidence:nb_raft_burst, evidence:nb_raft_sust).
- Burst cùng bậc với số đã công bố (13.034 tx/s, `note/tps_full_measurement_report_20261009.md`) [TÍNH].
- Báo cáo cũ ghi Raft sustained dừng tạo block khoảng giây 40–44 và 19–23% tx đã nhận không lên chuỗi. Với binary hiện tại và khối lượng 1 triệu tx, hiện tượng đó **không xuất hiện** ở 5/5 lượt. Chưa biết bản sửa `HandleConnection` có phải nguyên nhân hay không (khối lượng và chế độ đo cũng khác); chưa có thí nghiệm A/B cho Raft [GIẢ THUYẾT].

## 4. Diễn giải
1. **Ở backlog lớn, một phần đáng kể CPU của validator bị GC của Go chiếm** (11% → 29–30%), vì heap Go đang sống tăng theo số tx đang chờ. Đây là cơ chế quan sát được trong profile (evidence:nb_profile).
2. **Nâng giới hạn bộ nhớ lấy lại một phần** (evidence:nb_gomem, evidence:nb_profile2): +3,6% (12 GB, ghép cặp, chưa đạt p < 0,05) và +6,6% (24 GB, một lượt), đổi lại RSS tăng 25–100%. GC chưa phải toàn bộ nguyên nhân: sau khi giảm GC còn khoảng 7% chưa giải thích [GIẢ THUYẾT: chi phí khác tỷ lệ với kích thước hàng đợi, chưa đo].
3. **Hướng cải thiện có thể làm** (chưa làm): (a) giảm số đối tượng Go giữ lại cho mỗi tx đang chờ (`NewExecuteSCResult`, `sync.Map` cache, `AddTxToCache`); (b) chặn kích thước backlog ở lớp nhận tx (hành vi bản cũ có tác dụng này) nếu mục tiêu là TPS ổn định; (c) chỉnh `META_GOMEMLIMIT` theo RAM thực của máy.

## 5. Giới hạn
- Một máy chung; các con số TPS dao động theo giờ; chỉ so ghép cặp trong cùng đợt.
- Profile chỉ cho phần Go; phần Rust/C (`cgocall`, khoảng 22–35% CPU) không phân rã được.
- Profile 4 lượt (2 mỗi mức) và 3 lượt (một mỗi cấu hình bộ nhớ); thí nghiệm ghép cặp bộ nhớ chỉ 4 cặp; nhân quả mới được hỗ trợ ở mức gián tiếp.
- Raft: chỉ 5 lượt mỗi chế độ, không có A/B với bản trước; 3 replica cùng máy.

## 6. Chưa làm
- Chạy A/B Raft giữa binary cũ và mới; chạy Raft ở các khối lượng khác.
- Phân rã chi phí phía Rust; thử giảm đối tượng giữ lại mỗi tx và đo lại.
- Quét `META_GOMEMLIMIT` nhiều mức với số lượt lớn hơn, kèm đo RSS toàn máy.
- Panic NOMT stake_db vẫn mở (xem `note/nomt_crash_hunt_report_20261013.md`); cần bạn đồng ý đổi `eprintln!` để thấy chuỗi nguyên nhân.
