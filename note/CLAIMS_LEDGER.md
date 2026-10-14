# Sổ kê khai các tuyên bố hiệu năng / độ tin cậy (claims ledger) — cập nhật 2026-10-13

Trạng thái: **RE-VERIFIED** = tôi chạy/tính lại độc lập trong các phiên 2026-10-13 và kết quả khớp trong sai số nêu rõ; **NOT REPRODUCED** = chạy lại nhưng không khớp; **NARROWED** = đúng nhưng phạm vi hẹp hơn cách diễn đạt gốc; **NOT RE-VERIFIED** = chưa kiểm lại, chỉ trích; **OPEN** = giả thuyết chưa có bằng chứng can thiệp.
Quy tắc: không thêm dòng "RE-VERIFIED" nếu chưa có file thô trong `note/evidence/` hoặc test chạy lại được.

| # | Tuyên bố (nguồn) | Trạng thái | Cơ sở | Ghi chú / giới hạn |
|---|---|---|---|---|
| 1 | BFT burst 25k cold-start ≈ 7.804 tx/s (n=8) (`tps_full_measurement_report_20261009.md`) | RE-VERIFIED (cùng bậc, cả trước và sau bản sửa) | counted: 7.680,93 ± 902,28 (n=7) và 7.773,85 ± 1.069,35 (n=7) trước sửa — evidence:tps_counted_burst_b; 7.968,06 ± 715,40 (n=7) sau sửa — evidence:tps_vol_burst | Một máy; burst chỉ 3–5 block nên không phản ánh tốc độ ổn định |
| 2 | BFT sustained 60 s ≈ 14.072 tx/s (n=8) (cùng báo cáo) | EXPLAINED (tái lập được với mã cũ; không phải hồi quy) | mã Go cũ `f97a2fbf` đo 13,4k đầu-cuối ở cùng điều kiện (8 cặp) — evidence:node_ab1; ở khối lượng tx tương đương bản mới nhanh hơn ~6% (5 cặp, p = 0,02) — evidence:node_ab2 | Bản cũ tự chặn client (~0,86M tx/lượt), bản mới nhận ~4,2M tx nên số đầu-cuối 11,4k; số tuyệt đối giữa các ngày dao động ±12% (máy dùng chung); binary cũ là bản lai (Go cũ + Rust hiện tại) |
| 3 | Công cụ cũ ngoại suy từ 50 mẫu / "submitted" = byte ghi socket (đọc code) | RE-VERIFIED | đọc code + test `measure_test.go`; sustained n=10: số cũ cao hơn số đếm +618,6 tx/s (p = 0,0001) — evidence:tps_ingress_gap_b5 | Burst: số cũ ≈ số đếm. Sustained: số cũ cao hơn ~4,9% |
| 4 | 129.345 tx/s (Block-STM) và 74.067 tx/s (Go pipeline) trong `tps_summary_results_20261009.md` | NARROWED | đọc code/báo cáo | Là đo từng pha trong micro-benchmark, không phải TPS hệ thống đầu-cuối; không dùng làm số TPS chuỗi |
| 5 | Mọi lượt đo "chạy trên cụm mới" | NARROWED | `start_block=1` ở 8 JSON của `tps_root_cause_20261008`; block bắt đầu = 1 ở các lượt mới | Không xác định được cho các thí nghiệm GOGC / drift / Pebble fallback (không ghi block bắt đầu) |
| 6 | Watchdog `SIGABRT` được supervisor khởi động lại | RE-VERIFIED (mẫu deploy) | evidence:wdexp_systemd_user (E1, E2) | Probe liên kết mã watchdog thật nhưng không phải node thật; `run_env.sh` không có supervisor |
| 7 | Watchdog tắt khi `METANODE_EXEC_STUCK_ABORT_SECS=0` | RE-VERIFIED | evidence:wdexp_systemd_user (E3) | Probe |
| 8 | Nguyên nhân treo NOMT là lost wake-up trên crossbeam (`nomt_hang_resolution_20261012.md`) | OPEN | repro tối thiểu không treo trong 4 × 300 s — evidence:lw_repro | Không tái hiện ≠ bác bỏ; chưa có thí nghiệm can thiệp trên NOMT thật |
| 9 | `recv_timeout(100 ms)` chặn được treo ở `IoHandle::recv()` | NOT RE-VERIFIED | — | Chỉ là bản vá phòng ngừa, chưa có bằng chứng can thiệp |
| 10 | Fallback Pebble khi NOMT lỗi hoạt động (test Go) | RE-VERIFIED (test) | test Go chạy lại trong phiên trước | Phạm vi test, chưa chạy trên cụm nhiều máy |
| 11 | p-value / hệ số hồi quy RSS / B1 trong báo cáo 2026-10-07..12 | RE-VERIFIED (tính lại) | tính lại bằng `stats_util.py` | Chỉ tính lại từ số liệu có sẵn, không đo lại |
| 12 | Raft burst ≈ 13k tx/s (20k/25k/30k) | NOT RE-VERIFIED | — | Công cụ đo mới chưa chạy trên Raft |
| 13 | Số liệu GOGC, drift RSS, TTL, fsync | NOT RE-VERIFIED | — | Chưa chạy lại |

| 14 | Tx client "gửi" nhưng validator không nhận (sustained, ~30% lượt trước bản sửa, 34k–191k tx) | RESOLVED (nguyên nhân + sửa + kiểm chứng) | nguyên nhân: `HandleConnection` bỏ hàng đợi khi idle timeout — evidence:tps_ingress_rootcause_hunt5_run2; sau sửa 13/13 lượt hợp lệ không mất — evidence:tps_ingress_fix_validation | Chạy trên 1 máy; chưa Raft/nhiều máy |
| 15 | Validator tự thoát với `nomt_ffi: failed to commit payload` (stake_db, block 45) | OPEN | 1 lần quan sát; thông điệp Rust: "Store is poisoned due to prior error" (lỗi đầu tiên bị mất); không tái hiện trong 20 lượt sau — evidence:nomt_crash_hunt | Nguyên nhân gốc chưa biết; "không tái hiện" ≠ "đã hết" |
| 16 | TPS sustained của node hiện tại giảm khi backlog tăng: 13,1k (0,5 triệu tx) → 12,2k (1) → 11,5k (2) → 10,0k (4), Spearman −0,91 | RE-VERIFIED (quan hệ); cơ chế OPEN | 4 mức × 4 vòng, xáo ngẫu nhiên — evidence:tps_vol_sweep | Một máy; chưa profile node khi xả backlog; phải ghi kèm khối lượng tx khi công bố |

## Giới hạn
- Chỉ các dòng RE-VERIFIED / NOT REPRODUCED có file thô ở `note/evidence/`; mọi dòng khác chỉ là trích.
- Một máy, 4 validator cùng máy.

## Chưa làm
- Dòng 9, 12, 13; đo Raft bằng công cụ đếm mới; kiểm chứng thủ công dòng 10–11 lần nữa từ file thô nếu cần dùng làm căn cứ ngoài.
