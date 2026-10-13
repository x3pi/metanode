# Đo TPS bằng cách ĐẾM tx trong block (BFT, 4 validator) — so với phương pháp ngoại suy cũ — 2026-10-13

Nhãn: **[ĐO]** = số do lệnh tôi chạy trong phiên này, có file thô; **[TÍNH]** = tính từ các số [ĐO] bằng công thức nêu rõ; **[ĐỌC CODE]** = đọc mã, chưa chạy; **[TRÍCH]** = lấy từ báo cáo khác, chưa chạy lại; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/tps_counted_20261013/` (manifest `MANIFEST.json`). Công cụ: `execution/cmd/tool/secp_tps_blast` (phiên bản `secp_tps_blast/2 (chain-counted)`), runner `execution/scripts/test/evidence/run_tps_counted.py`.

## 1. Vì sao cần đo lại (phương pháp cũ) [ĐỌC CODE]
Bản `secp_tps_blast` trước đây:
- suy ra số tx "confirmed" bằng cách nhân số receipt xác nhận của một mẫu 50 hash với tỉ lệ mẫu, rồi chặn trên bởi số "submitted" (`extrapolatedConfirmed`);
- coi "submitted" là số tx đã ghi vào socket TCP (byte vào buffer kernel), không phải số validator đã nhận;
- chế độ burst chia cho thời gian *sau khi bơm xong* đến khi mẫu cuối có receipt; chế độ sustained chia **mọi tx đã vào block tới hết cửa sổ xả tồn** cho **chỉ thời gian bơm**.
Bản mới đếm tx có `to == địa chỉ đích` và `from ∈ tập ví` trong từng block, kèm thời điểm quan sát block đầu tiên bởi validator tham chiếu; đọc thêm bộ đếm `master_raw_eth_txs_received_total` / `_rejected_total` của từng validator trước và sau để biết validator đã nhận bao nhiêu. Công cụ cũ vẫn in số ước lượng trong cùng lượt chạy (`legacy_estimate_do_not_report`) để so sánh không bị nhiễu giữa các lượt.

## 2. Tiêu chí (ghi rõ thời điểm)
Các tiêu chí dưới đây tôi quyết định trong lúc làm, **không** ghi trước khi thấy dữ liệu của lượt đầu tiên; vì vậy chỉ có giá trị mô tả, không phải kiểm định ghi trước:
- Mỗi lượt phải có cụm mới từ template, đủ cả 4 cổng TCP được dùng, parity block hash + state root khớp trên cả 4 validator tại block cuối của validator tham chiếu.
- So sánh số cũ/mới bằng kiểm định t ghép cặp (cùng lượt chạy), p hai phía tính bằng phân phối t thật (`stats_util.py`).

## 3. Kết quả [ĐO]
Cấu hình: BFT 4 validator (chain id 991), tx EIP-1559 từ 1.000 ví hoạt động (lấy từ 50.000 khoá trong `generated_keys.json`), tải qua raw TCP trên 4 kết nối, batch 1.000, một máy 104 vCPU (Xeon Platinum 8272CL), kernel 6.8.0-139 (không lưu kèm binary/ cấu hình chế độ chữ ký trong evidence; commit mã nguồn công cụ ghi trong manifest).

### 3.1 Burst 25.000 tx (7 lượt mỗi lô, hai lô độc lập)
| Lô | n | TPS đếm từ chuỗi (trung bình ± SD, tx/s) | TPS phương pháp cũ (trung bình, tx/s) | Evidence |
|---|---|---|---|---|
| Lô 1 (chỉ còn log, JSON bị mất khỏi thư mục chưa track) | 7 | 7773.85 ± 1069.35 | 7733.00 | evidence:tps_counted_burst_a |
| Lô 2 (đầy đủ JSON + CSV) | 7 | 7680.93 ± 902.28 | 7868.54 | evidence:tps_counted_burst_b |

- Cả 14 lượt: tx gửi = tx validator nhận = tx đã commit = 25.000; không mất tx; parity khớp trên cả 4 validator (evidence:tps_counted_burst_b).
- Chênh lệch ghép cặp (cũ − đếm) ở lô 2 là +187.61 tx/s với p = 0.3787 và khoảng tin cậy 95% [−295.51, 670.73]: **không phát hiện khác biệt có ý nghĩa**; ở lô 1 là −40.86 tx/s, p = 0.7204 (evidence:tps_counted_burst_a, evidence:tps_counted_burst_b). Với burst 25k, số cũ và số đếm giống nhau trong sai số.
- Số trung vị đã công bố trước đó cho BFT burst 25k cold-start là 7.804 tx/s (n=8) [TRÍCH, `note/tps_full_measurement_report_20261009.md`], cùng bậc với hai lô này.

### 3.2 Sustained (`-duration 60`, không giới hạn tốc độ), 5 lượt, cả 4 cổng TCP được dùng
| Chỉ số (trung bình ± SD qua 5 lượt) | Giá trị | Evidence |
|---|---|---|
| TPS đếm từ chuỗi, đầu-cuối (tx/s) | 12322.88 ± 573.27 | evidence:tps_counted_sustained_v2 |
| TPS đếm trong lúc bơm (tx/s) | 12439.81 ± 1493.05 | evidence:tps_counted_sustained_v2 |
| TPS phương pháp cũ (tx/s) | 12449.84 ± 1471.49 | evidence:tps_counted_sustained_v2 |

- Chênh lệch ghép cặp (cũ − đếm): +126.96 tx/s, p = 0.8205: không phát hiện khác biệt có ý nghĩa ở lô này (evidence:tps_counted_sustained_v2).
- Mỗi lượt đếm chính xác từng block (`committed_ours_exact = true`); tx validator nhận = tx đã commit trong cả 5 lượt, nên mọi tx đã vào ingress đều lên chuỗi (evidence:tps_counted_sustained_v2).
- **Thời gian bơm thực tế dài hơn mục tiêu 60 giây:** từ 79.7 đến 161.2 giây (CSV, cột `inject_seconds`), vì client bị chặn khi phía validator giảm nhận; thời gian đầu-cuối 327.0–399.9 giây. Tên "sustained 60 s" chỉ là tham số tạo tải, không phải thời lượng thực của lượt đo (evidence:tps_counted_sustained_v2).
- **Phát hiện ở tầng ingress:** ở 2 trong 5 lượt (lượt 1 và 3), số tx tool coi là đã gửi lớn hơn số validator nhận: 4616838 so với 4458000 (thiếu 158838) và 4436419 so với 4272000 (thiếu 164419). Trong cả 5 lượt, số nhận bằng số commit. Tức là phần "mất" nằm giữa client và ingress của validator, không nằm ở đồng thuận (evidence:tps_counted_sustained_v2). Nguyên nhân chưa xác định [GIẢ THUYẾT: kết nối TCP bị đóng/reset khi validator quá tải trong lúc client đã tính batch là đã gửi].
- Số trung vị đã công bố cho BFT sustained 60 s là 14.072 tx/s (n=8) [TRÍCH, `note/tps_full_measurement_report_20261009.md`]. Với binary hiện tại, 5 lượt này cho TPS đếm đầu-cuối 12.3k tx/s: **không tái lập được 14.1k** dưới cấu hình này. Chưa xác định nguyên nhân (binary khác, thời gian bơm thực tế khác, hoặc tải khác nhau): [CHƯA ĐO].

### 3.3 RSS ngay sau khởi động cụm mới
- Burst lô 2: RSS tổng 4 validator ngay sau khi khởi động dao động trong khoảng 1475–1528 MB qua 7 lượt, hệ số Spearman theo thứ tự lượt là 0.14: không có xu hướng tăng (evidence:tps_counted_burst_b).
- Sustained: 1452 MB ở lượt 1 rồi 1637–1658 MB ở lượt 2–5 (một bước nhảy sau lượt 1, không tăng dần tiếp); nguyên nhân chưa rõ [CHƯA ĐO] (evidence:tps_counted_sustained_v2).
- Hai lô trên đều dùng cụm sinh từ template qua `cp -a` và viết lại đường dẫn tuyệt đối `/tmp/gate_4val_p06` trong cấu hình (xem mục 5). Các lượt bắt đầu từ genesis: block bắt đầu là 1 ở mọi lượt burst lô 2 (evidence:tps_counted_burst_b).

## 4. Hai lỗi của công cụ đo mà lượt sustained đầu tiên đã lộ ra [ĐO]
Bản chạy đầu (còn lưu làm bằng chứng, **không dùng làm kết quả**) ở `sustained_attempt1_flawed/`:
- Cổng TCP của validator thứ tư từ chối kết nối khi tool khởi động (`connection refused`), (log của cả 5 lượt có dòng `TCP connect failed to 127.0.0.1:31203`), nên chỉ 3/4 endpoint được dùng; đã sửa bằng cách runner chờ mọi cổng TCP sẵn sàng và từ chối lượt có ít endpoint hơn cụm (evidence:tps_counted_sustained_v1_flawed).
- Các block 20–28k tx hết thời gian chờ 5 giây khi lấy toàn bộ tx; `committed_ours` có thể thấp hơn thực tế vì các block đó không đọc được. Đã sửa: timeout 180 giây và công cụ in cận trên (`committed_ours_upper_bound`) khi vẫn không lấy được block (evidence:tps_counted_sustained_v1_flawed).

## 5. Tự phản biện
Ba cách các con số này có thể sai, và những gì đã làm:
1. **Đo nhầm đối tượng (đếm tx của người khác hoặc bỏ sót tx của mình).** Đếm theo `to` và `from`; test dùng chuỗi giả có tx lạ gửi tới cùng địa chỉ đích và tx của ví mình gửi đi nơi khác; các đột biến thủ công đã thử trên `measure.go` (bỏ lọc ví, bỏ qua validator chậm, bỏ qua block lỡ...) đều làm test thất bại sau khi bổ sung ca kiểm thử (`measure_test.go`) [ĐO].
2. **Trạng thái tích luỹ giữa các lượt.** Cấu hình trong template nhúng đường dẫn tuyệt đối `/tmp/gate_4val_p06` cho dữ liệu, log và khoá [ĐỌC CODE]; bản `run_tps_counted.py` viết lại các đường dẫn đó và từ chối chạy nếu còn sót. Block bắt đầu bằng 1 ở mọi lượt lô 2 cho thấy chuỗi bắt đầu từ genesis (evidence:tps_counted_burst_b). Các JSON cũ của `tps_root_cause_20261008` cũng có `start_block = 1` [ĐO, đọc 8 file], nên giả thuyết "mọi lượt 'sạch' trước đây dùng chung state" **không** được hỗ trợ cho các lượt TPS đó; các thí nghiệm khác (GOGC, drift, fallback Pebble với block #8681) không ghi block bắt đầu nên không xác định được [CHƯA ĐO].
3. **Công cụ bơm làm hỏng số đo (client chặn, ghi sai).** Nguồn thứ hai: bộ đếm `master_raw_eth_txs_received_total` của từng validator và tổng tx đếm trực tiếp trong block; hai nguồn này bằng nhau ở mọi lượt, còn số "đã gửi" của client lệch ở 2 lượt (mục 3.2).

Lỗi vận hành xảy ra trong phiên (đã xử lý, ghi để rút kinh nghiệm): lần chạy đầu tiên của runner dùng `shutil.copytree`, làm các file NOMT thưa (apparent ~80 GB mỗi validator) thành file đặc và đầy ổ đĩa dùng chung; guard dung lượng đã dừng lần chạy thứ hai và đã đổi sang `cp -a`. Cùng mẫu `shutil.copytree` có ở 4 script benchmark khác đã được vá trong commit `d700e57d`.

## 6. Giới hạn
- Một máy (4 validator cùng máy, không đo tải của tiến trình khác trên máy); các con số TPS không áp dụng cho triển khai nhiều máy.
- Chỉ BFT 4 validator; chưa chạy Raft bằng công cụ mới.
- Burst lô 1 mất file JSON (chỉ còn log và stdout của runner); lô 2 và sustained đầy đủ.
- Sustained: n = 5, thời gian bơm thực tế khác nhau giữa các lượt (79.7–161.2 giây), nên SD lớn.
- TPS "trong lúc bơm" với burst bằng 0 theo định nghĩa (không tx nào được commit trước khi bơm xong 25.000 tx trong 0.1 giây).
- `tps_active_commit_window` chỉ có ý nghĩa khi có nhiều block; với burst chỉ 3–5 block nên không dùng.

## 7. Chưa làm
- Raft burst/sustained bằng công cụ mới; so sánh A/B với binary khác.
- Điều tra vì sao client "gửi" nhiều hơn số validator nhận ở 2/5 lượt sustained.
- Lý giải vì sao sustained hiện tại thấp hơn 14.1k đã công bố.
- Lặp lại với số lượt lớn hơn (n ≥ 7) cho sustained.
