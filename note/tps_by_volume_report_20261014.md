# TPS của node hiện tại theo khối lượng tx (BFT 4 validator, một máy) — 2026-10-14

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/tps_by_volume_20261014/` (manifest `MANIFEST.json`). Tiếp nối `note/node_ab_old_vs_new_report_20261014.md`.

## 1. Câu hỏi
Báo cáo A/B trước cho thấy số TPS của bản mới phụ thuộc mạnh vào khối lượng tx chịu tải (backlog). Cần một bảng TPS **ghi rõ khối lượng tx** để các số công bố không bị so sánh sai.

## 2. Cách làm
Binary đã sửa lỗi mất tx (commit `1cea5f0e`), công cụ đo đếm từ block, cụm mới cho mỗi lượt, BFT 4 validator, cổng 31xxx, một máy. Công cụ có cờ `-max-txs` mới (dừng bơm khi đủ số tx). Quét khối lượng: 4 mức (0,5 / 1 / 2 / 4 triệu tx) × 4 vòng, thứ tự xáo ngẫu nhiên có seed 20261016 (`order.txt`). Burst 25k: 7 lượt. Mọi lượt: gửi = nhận = commit, parity khớp trên 4 validator.

## 3. Kết quả [ĐO]
### 3.1 Sustained, theo khối lượng tx (4 lượt mỗi mức)
| Khối lượng tx | TPS đếm đầu-cuối (trung bình, tx/s) | TPS trong cửa sổ có block (tx/s) | Evidence |
|---|---|---|---|
| 0,5 triệu | 13118,0 | 13583,5 | evidence:tps_vol_sweep |
| 1 triệu | 12190,1 | 12412,3 | evidence:tps_vol_sweep |
| 2 triệu | 11495,4 | 11583,7 | evidence:tps_vol_sweep |
| 4 triệu | 9973,5 | 10010,3 | evidence:tps_vol_sweep |

- Độ lệch chuẩn giữa các lượt cùng mức: 1.843 (0,5 triệu), 272 (1 triệu), 282 (2 triệu), 906 (4 triệu) tx/s [TÍNH từ evidence:tps_vol_sweep].
- Tương quan thứ hạng Spearman giữa khối lượng và TPS đầu-cuối = −0,91 (16 lượt). 4 triệu so với 1 triệu: −2.217 tx/s (Welch p = 0,013); 2 triệu so với 1 triệu: −695 tx/s (p = 0,012); 0,5 triệu so với 1 triệu: không khác có ý nghĩa (p = 0,39), vì lượt đầu của mức 0,5 triệu cao bất thường (15.870) [TÍNH].
- Tốc độ commit giảm khoảng 24% từ 0,5 triệu xuống 4 triệu tx với cùng một binary (evidence:tps_vol_sweep). Điều này ủng hộ giả thuyết "backlog làm chậm commit"; cơ chế bên trong node chưa được đo [GIẢ THUYẾT].

### 3.2 Burst 25.000 tx (7 lượt)
| Chỉ số | Giá trị | Evidence |
|---|---|---|
| TPS đếm đầu-cuối (trung bình ± SD, tx/s) | 7968,06 ± 715,40 | evidence:tps_vol_burst |
| Khoảng giá trị (tx/s) | 7040 đến 9246 | evidence:tps_vol_burst |

- Cùng bậc với các lô burst trước (7.681 ± 902 và 7.774 ± 1.069 trước bản sửa lỗi mất tx; số công bố 7.804) (xem `note/tps_counted_report_20261013.md` và `note/evidence/tps_counted_20261013/`). Bản sửa không làm burst nhỏ đổi đáng kể [TÍNH].
- Burst chỉ có 3–5 block nên TPS ở đây gồm cả độ trễ khởi động của một block đầu, không phản ánh tốc độ ổn định.

## 4. Cách trích dẫn số TPS từ nay [ĐỀ XUẤT]
- Luôn kèm khối lượng tx và chế độ đo; ví dụ "BFT 4 validator, một máy, sustained 1 triệu tx: 12,2k ± 0,3k tx/s đếm từ chuỗi".
- Không dùng số "đầu-cuối" của khối lượng khác nhau để so sánh với nhau, và không dùng số của hai đợt chạy khác ngày (dao động ±12% do tải nền).
- Số công thức cũ (`legacy_effective_tps`) chỉ để đối chiếu, không công bố.

## 5. Giới hạn
- Một máy chung, 4 validator cùng máy; các con số TPS dao động theo giờ (đã thấy ±12% giữa các đợt cùng binary).
- Chỉ 4 lượt mỗi mức; mức 0,5 triệu có SD lớn; mức 4 triệu có một lượt thấp (8.620).
- Chỉ BFT; chưa đo Raft bằng công cụ mới.
- Chưa đo cơ chế bên trong node khiến commit chậm khi backlog lớn (chưa profile).
- Burst: tốc độ ổn định không đo được vì quá ít block.

## 6. Chưa làm
- Profile CPU/bộ nhớ của validator khi xả backlog 4 triệu tx để tìm cơ chế (có thể là chi phí mỗi block theo kích thước mempool hoặc áp lực GC).
- Đo Raft burst/sustained bằng công cụ đếm mới.
- Lặp lại quét khối lượng ở thời điểm máy ít tải hơn.
