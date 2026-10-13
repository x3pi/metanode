# Chênh lệch giữa tx client "gửi" và tx validator "nhận" (sustained BFT) + độ lệch của số cũ — 2026-10-13

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/tps_ingress_gap_20261013/` (manifest `MANIFEST.json`). Tiếp nối `note/tps_counted_report_20261013.md` (mục 3.2).

## 1. Câu hỏi
Ở báo cáo trước, 2 trong 5 lượt sustained có số tx client coi là đã gửi lớn hơn số validator nhận (mọi tx đã nhận đều lên chuỗi). Tx "mất" ở đâu, và có tái hiện được không? Đồng thời: số TPS đếm có thấp hơn số công cụ cũ không, khi n lớn hơn?

## 2. Cách làm
- Công cụ `secp_tps_blast/2` thêm bộ đếm chẩn đoán `SendBatch` lỗi và tx bị bỏ sau thử lại (`main.go`). Runner `run_tps_counted.py` thêm: (a) lấy mẫu bộ đếm nhận của 4 validator và block head mỗi 15 s trong 120 s sau khi công cụ kết thúc (`*_post.json`); (b) khi nhận < gửi, gửi SIGQUIT tới validator để lấy goroutine dump (chưa được kích hoạt vì không lượt nào ở lô cuối mất tx); (c) dọn cây thư mục có thử lại.
- Cụm mới từ template cho mỗi lượt, BFT 4 validator, cổng 31xxx, `-duration 60 -batch 1000`, 4 kết nối TCP.
- Một số lượt của các lô trước bị loại vì lỗi harness của chính tôi (symlink binary làm `run_env.sh stop` bỏ qua node cũ, cụm lượt trước tranh cổng, validator "chết"/cụm không commit). Các lượt đó không được dùng làm dữ liệu; bài học: kiểm tra cụm lượt trước đã dừng trước khi đo.

## 3. Kết quả [ĐO]
### 3.1 Chênh lệch gửi/nhận qua các lượt hợp lệ
| Lô (thư mục evidence) | Số lượt hợp lệ | Lượt có nhận < gửi | Chênh lệch (tx) | Evidence |
|---|---|---|---|---|
| Lô sustained v2 (báo cáo trước) | 5 | 2 | 158.838 và 164.419 | evidence:tps_ingress_gap_v2ref |
| b2_run1_lossy | 1 | 1 | 98.783 (4.365.783 gửi, 4.267.000 nhận) | evidence:tps_ingress_gap_b2 |
| b3_run1_lossy | 1 | 1 | 97.657 (4.417.657 gửi, 4.320.000 nhận) | evidence:tps_ingress_gap_b3 |
| b4 | 2 | 1 | 44.597 (4.283.597 gửi, 4.239.000 nhận) | evidence:tps_ingress_gap_b4 |
| b5_clean10 | 10 | 0 | 0 | evidence:tps_ingress_gap_b5 |

- Tổng 19 lượt hợp lệ có số nhận: 5 lượt có chênh lệch, 14 lượt không [TÍNH]. Lô cuối 10/10 không mất, nên **không tái hiện được trong lô cuối**; chưa tìm ra yếu tố kích hoạt.
- Ở các lượt có chênh lệch: `SendBatch` không lỗi lần nào (0 lần lỗi, 0 tx bị bỏ), 4 validator đều sống, log node không có cảnh báo/lỗi nào ngày hôm nay (evidence:tps_ingress_gap_b4).
- Chênh lệch không phải số nguyên của 1.000 (có phần dư như 597): phù hợp với batch cuối bị cắt ở deadline [GIẢ THUYẾT, chưa kiểm chứng].
- **Không phải tx đến muộn:** ở các lượt có lấy mẫu sau khi kết thúc (b3 run 1, b4 run 2), bộ đếm nhận giữ nguyên trong 120 s và block head không đổi (evidence:tps_ingress_gap_b3, evidence:tps_ingress_gap_b4).
- **Không phải dữ liệu còn nằm trong buffer kernel:** lấy mẫu `ss -tn` mỗi 10 s ở lô 4 cho thấy hàng đợi gửi/nhận của các kết nối ingress rỗng từ rất lâu trước khi công cụ kết thúc; chỉ có dữ liệu trong hàng đợi trong lúc bơm (evidence:tps_ingress_gap_b4).
- Vị trí mất tx chưa xác định. Đã đọc `pkg/network/{handler,server,connection}.go` [ĐỌC CODE]: đường tx không có nhánh bỏ im lặng rõ ràng (khác với đường lệnh thường); không thể loại trừ nguyên nhân phía ứng dụng nếu không có dump goroutine của một lượt mất tx.

### 3.2 Lô 10 lượt (b5): TPS đếm so với số cũ
| Chỉ số (trung bình ± SD qua 10 lượt) | Giá trị | Evidence |
|---|---|---|
| TPS đếm từ chuỗi, đầu-cuối (tx/s) | 12566.08 ± 510.37 | evidence:tps_ingress_gap_b5 |
| TPS đếm trong lúc bơm (tx/s) | 13125.86 ± 416.96 | evidence:tps_ingress_gap_b5 |
| TPS phương pháp cũ (tx/s) | 13184.68 ± 436.25 | evidence:tps_ingress_gap_b5 |

- Chênh lệch ghép cặp (cũ − đếm) = +618.60 tx/s, t = 6.64, df = 9, p = 0.0001, CI95 = [407.80, 829.40] (evidence:tps_ingress_gap_b5). Với n = 10, số cũ **cao hơn** số đếm đầu-cuối khoảng 4,9% [TÍNH]. Ở lô 5 lượt trước (n = 5) không phát hiện khác biệt (p = 0,82), nhưng độ lệch chuẩn của hiệu khi đó là 1.171 tx/s nên kiểm định yếu; kết quả n = 10 hợp lý với cơ chế đã biết (mẫu số cũ chỉ là thời gian bơm).
- Tất cả 10 lượt: parity OK trên 4 validator (evidence:tps_ingress_gap_b5).
- Thời gian bơm thực tế 78–169 giây so với mục tiêu 60 giây (evidence:tps_ingress_gap_b5).
- Số trung vị đã công bố 14.072 tx/s vẫn không tái lập: 10 lượt cho đầu-cuối 12.566 ± 510 và cao nhất 12.873 (evidence:tps_ingress_gap_b5).

## 4. Lỗi công cụ tìm thấy khi làm
- **Điều kiện dừng "yên lặng 8 s" có thể dừng sớm:** ở lượt 8 của lô b5, validator đã nhận 5.002.293 tx nhưng công cụ dừng khi mới đếm được 4.983.285 trong block; block head sau đó tăng từ 1093 lên 1123, tức chuỗi tạm dừng rồi chạy tiếp (evidence:tps_ingress_gap_b5). Số TPS đếm của lượt này (11.151) bị thấp hơn thực tế vì thế. Đã sửa trong `measure.go`: quy tắc yên lặng không kết thúc phép đo khi bộ đếm validator cho thấy còn tx đã nhận chưa lên block (vẫn bị chặn bởi `max-wait`); test `TestWaitQuiesce_DoesNotStopWhileReceivedTxsArePending` thất bại khi tắt bản sửa (đã thử) và qua khi bật. Lượt 8 vẫn nằm trong bảng trên với giá trị chưa sửa; bản sửa chưa được chạy lại trên cụm thật [CHƯA ĐO].

## 5. Giới hạn
- Máy chung, 4 validator cùng máy; không đo tải của tiến trình khác.
- Chênh lệch gửi/nhận quan sát ở 5/19 lượt hợp lệ, nhưng 0/10 ở lô cuối; chưa có goroutine dump, nên vị trí mất tx và điều kiện kích hoạt chưa biết.
- Các lô b2–b4 có ít lượt hợp lệ và chạy với các phiên bản runner khác nhau (thêm chẩn đoán dần).
- Số liệu lô v2 nằm trong thư mục evidence của báo cáo trước, không sao chép lại ở đây.

## 6. Chưa làm
- Tái hiện chênh lệch có chủ đích (thử tải khác, nhiều lượt hơn) để kích hoạt goroutine dump; xác định tx nằm ở đâu.
- Chạy lại cụm thật với bản sửa điều kiện dừng và đo lại lượt giống lượt 8.
- Điều tra vì sao thời gian bơm giãn từ 60 s lên 78–169 s (có thể do backpressure có chủ đích của cổng admission) và vì sao 14.072 tx/s đã công bố không tái lập.
