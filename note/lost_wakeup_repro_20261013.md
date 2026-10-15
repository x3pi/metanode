# Thử tái hiện "lost wake-up" của NOMT `IoHandle::recv()` trong chương trình Rust tối thiểu — 2026-10-13

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/lost_wakeup_repro_20261013/` (manifest `MANIFEST.json`, id `lw_repro`).

## 1. Câu hỏi
Báo cáo `note/nomt_hang_resolution_20261012.md` ghi nhận một lần treo với 11 completion nằm trong channel nhưng bên nhận không được đánh thức (nghi ngờ lost wake-up trên crossbeam). Có tái hiện được hiện tượng đó trong một chương trình tối thiểu mô phỏng cùng mẫu dùng channel không?

## 2. Thiết kế (viết trước khi chạy, trong đầu file `lw_repro_main.rs.txt`) [ĐỌC CODE]
- Mô phỏng mẫu: một luồng gửi 11 lệnh cho các worker I/O qua một channel, rồi gọi `recv()` đúng 11 lần trên channel completion; worker gửi completion với jitter 50 µs.
- Cấu hình: 3 instance × 4 worker, 300 s mỗi cấu hình; crossbeam-channel 0.5.15 với tải CPU giả 0/100/200 luồng, và `std::sync::mpsc` với tải 100.
- Phát hiện treo: không tiến triển trong 5 s; ghi `len()` của hàng đợi nhận (len > 0 khi treo = dấu hiệu lost wake-up).
- Quy ước kết luận: "không treo" nghĩa là *không tái hiện được trong phạm vi này*, không hơn.

## 3. Kết quả [ĐO] (evidence:lw_repro)
Máy: 104 vCPU Xeon Platinum 8272CL, kernel 6.8.0-139, rustc 1.90.0.

| Cấu hình | Thời gian (s) | Số vòng | Số lần `recv` | Số lần treo | Evidence |
|---|---|---|---|---|---|
| crossbeam, tải 0 | 300.08 | 7.970.759 | 87.678.349 | 0 | evidence:lw_repro |
| crossbeam, tải 100 | 300.04 | 7.279.506 | 80.074.566 | 0 | evidence:lw_repro |
| crossbeam, tải 200 | 300.16 | 134.609 | 1.480.699 | 0 | evidence:lw_repro |
| std mpsc, tải 100 | 300.04 | 5.239.770 | 57.637.470 | 0 | evidence:lw_repro |

Tải 200 luồng vượt số CPU (104) nên số vòng giảm mạnh; đó là ảnh hưởng của lập lịch, không phải của channel.

## 4. Kết luận (3 trạng thái)
**Không tái hiện được trong phạm vi này** (không phải "đã bác bỏ", cũng không phải "đã xác nhận"):
- Không có lần treo nào trong ~227 triệu lần `recv` trên 4 cấu hình.
- Không suy ra được nguyên nhân thật của lần treo ở NOMT. Chương trình không dùng NOMT thật, không có luồng ghi đĩa/fsync, không có RwLock đọc song song, và chạy 20 phút tổng cộng.
- Bản vá `recv_timeout(100 ms)` đang dùng vẫn là giải pháp phòng ngừa; cơ chế treo vẫn **chưa có bằng chứng can thiệp** [GIẢ THUYẾT: lost wake-up]; dữ liệu 11 completion trong hàng đợi là trích từ báo cáo gốc, chưa kiểm lại.

## 5. Giới hạn
- Chương trình mô phỏng, không phải NOMT; một máy; 4 cấu hình × 300 s; không thử các phiên bản crossbeam khác.

## 6. Chưa làm
- Chạy trên NOMT thật với hook ép lập lịch, hoặc dùng công cụ như `loom`/`shuttle` cho mô hình channel.
- Thử cấu hình số worker/ kích thước batch giống NOMT thật và các phiên bản crossbeam khác.
