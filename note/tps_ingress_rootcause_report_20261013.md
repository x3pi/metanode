# Mất tx giữa client và validator: nguyên nhân, bản sửa và kiểm chứng — 2026-10-13

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/tps_ingress_rootcause_20261013/` (manifest `MANIFEST.json`). Tiếp nối `note/tps_ingress_gap_report_20261013.md`.

## 1. Hiện tượng
Trong các lượt sustained BFT 4 validator, số tx client ghi vào socket lớn hơn số tx validator đếm là đã nhận (`master_raw_eth_txs_received_total`); mọi tx đã nhận đều lên chuỗi. Chênh lệch 34k–191k tx, không kèm cảnh báo/lỗi ở log stdout của node.

## 2. Cách tìm nguyên nhân [ĐO]
1. Lấy mẫu bộ đếm nhận và block head 120 s sau khi công cụ kết thúc: bộ đếm không đổi, nên tx không phải đến muộn.
2. Lấy mẫu `ss -tn` mỗi 10 s: hàng đợi gửi/nhận kernel của các kết nối ingress rỗng trước khi công cụ kết thúc, nên dữ liệu không nằm trong buffer kernel.
3. Dựng một binary node chẩn đoán tạm (chỉ trong binary, không nằm trong repo; patch lưu ở `tooling/`) đếm số message `SendRawTransactions` ở từng tầng mạng. Ở một lượt mất 66.494 tx (evidence:tps_ingress_rootcause_hunt4_run2): tổng toàn cụm đọc/enqueue 4.582 message nhưng chỉ 4.513 message được `HandleConnection` lấy ra; phần chênh nằm ở validator 2 (1.156 enqueue so với 1.087 dispatch). Từ `dispatch` trở đi các tầng khớp nhau và khớp bộ đếm nhận.
4. Thêm log lý do thoát và độ dài hàng đợi lúc thoát của `HandleConnection`. Ở lượt mất 191.007 tx, kết nối của validator 0 thoát với `err=read tcp ...: i/o timeout pending_in_requestChan=192`; ba validator còn lại thoát với hàng đợi rỗng (evidence:tps_ingress_rootcause_hunt5_run2). 192 batch ≈ 191 batch đầy + 1 batch dở, khớp 191.007 tx thiếu [TÍNH].

## 3. Nguyên nhân [ĐỌC CODE + ĐO]
`SocketServer.HandleConnection` (`pkg/network/server.go`) chỉ xả hàng đợi request của kết nối khi reader báo EOF hoặc kết nối đã đóng; với mọi lỗi khác nó `return err` ngay, bỏ mọi batch đã được decode và xếp hàng. Đồng hồ idle (90 s) đếm từ byte cuối của client, trong khi server có thể còn backlog hàng phút (cổng admission/mempool): khi client đã bơm xong, hết 90 s thì deadline đọc kích hoạt, reader báo `i/o timeout`, handler thoát và vứt phần còn lại của hàng đợi.

## 4. Bản sửa (commit `1cea5f0e`)
Mọi lỗi của reader giờ đều đi qua cùng đường xả hàng đợi trước khi đóng kết nối; lỗi không phải EOF vẫn được log. Test mới `TestIngest_IdleTimeoutWithBacklog_NoBatchLost`: trước bản sửa chỉ 7/30 batch được giao, sau bản sửa 30/30 [ĐO]. Cả package `pkg/network` qua với `-race`; `build_check.sh` qua 5/5.

## 5. Kiểm chứng trên cụm thật [ĐO]
| Nhóm | Số lượt hợp lệ | Số lượt mất tx | Evidence |
|---|---|---|---|
| Trước bản sửa, 5 lô chẩn đoán (hunt…hunt5) | 14 | 5 (142.151; 154.870; 33.883; 66.494; 191.007 tx) | evidence:tps_ingress_prefix |
| Sau bản sửa, binary chỉ có bản sửa | 13 | 0 | evidence:tps_ingress_fix_validation |

- Cả 13 lượt hợp lệ sau bản sửa: số gửi = số nhận = số commit (evidence:tps_ingress_fix_validation).
- Gộp với các lô trước đã nêu trong `note/tps_ingress_gap_report_20261013.md` (5 lượt có chênh lệch trên 19), khoảng 30% lượt trước bản sửa bị mất tx; xác suất 13 lượt liên tiếp không mất nếu bản sửa không có tác dụng và tỉ lệ vẫn là 30% là khoảng 1% (0,7^13) [TÍNH, ước lượng thô].
- Lượt 11 trong lô sau bản sửa **không hợp lệ**: validator 3 panic (mục 6), nên tx định tuyến tới validator đó bị mất và bộ đếm nhận không đọc được (evidence:tps_ingress_node_crash).
- TPS đếm từ chuỗi sau bản sửa (13 lượt hợp lệ, trừ lượt 11): trung bình 12.800,77 ± 433,27 tx/s; số công cụ cũ 13.291,73; chênh ghép cặp +490,96 tx/s (t = 3,27, df = 12, p = 0,0067, CI95 [163,9; 818,0]) [TÍNH từ evidence:tps_ingress_fix_validation]. Số cũ cao hơn khoảng 3,8%, cùng chiều với lô 10 lượt trước (+619).

## 6. Phát hiện riêng: validator panic khi commit NOMT stake_db [ĐO]
Ở lượt 11, validator 3 chết giữa lượt: `panic: FATAL: StakeStateDB async commit failed: nomt_ffi: failed to commit payload` (block 45, namespace stake_db), ba validator còn lại tiếp tục (evidence:tps_ingress_node_crash). Đĩa còn ~170 GB, log node không có lỗi I/O trước panic. Nguyên nhân chưa biết; lỗi gốc của Rust `nomt_ffi` không được in ra. Các lần "node chết" trước đó trong phiên là lỗi harness của tôi (đã sửa), không phải lỗi node; đây là lần node tự thoát duy nhất tôi quan sát trên cụm khởi động sạch hợp lệ, nhưng tôi không có số tổng lượt để tính tần suất. Không loại trừ liên quan tới treo NOMT đã biết, nhưng chưa có bằng chứng.

## 7. Giới hạn
- Một máy chung, 4 validator cùng máy; không điều khiển tải của tiến trình khác.
- Tỉ lệ 30% trước bản sửa là ước lượng từ các lô chạy với phiên bản công cụ/chẩn đoán khác nhau; phép tính xác suất chỉ là thô.
- Bản sửa mới chạy 13 lượt hợp lệ; chưa chạy Raft; chưa chạy lại ở tải nhiều máy.
- Binary chẩn đoán chứa patch tạm không được commit; chỉ binary sửa-thuần đã dùng cho 14 lượt kiểm chứng.
- Chưa tìm nguyên nhân panic NOMT (mục 6).

## 8. Chưa làm
- Tìm nguyên nhân `nomt_ffi: failed to commit payload` (cần log lỗi Rust, thử tái hiện với nhiều lượt, kiểm tra RSS/đĩa).
- Chạy lại bộ đo burst/sustained với binary đã sửa để cập nhật số TPS trong ledger.
- Xem lại cấu hình idle timeout (90 s) và kích thước `ConnRequestChanSize` cho khách hàng bơm tải lớn.
