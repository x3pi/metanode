# Săn lại lần panic NOMT với log lỗi đầy đủ chuỗi nguyên nhân: 17 lượt không tái hiện — 2026-10-15

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/nomt_errlog_hunt_20261015/` (manifest `MANIFEST.json`). Tiếp nối `note/nomt_crash_hunt_report_20261013.md`.

## 1. Việc đã làm
- Theo đồng ý của bạn, đổi 5 dòng `eprintln!` trong `execution/pkg/nomt_ffi/rust_lib/src/lib.rs` (`nomt_session_commit` hai chỗ, `nomt_session_finish`, `nomt_commit_payload`, `state_db_commit`) từ `{}` sang `{:#}`, để in cả chuỗi nguyên nhân của lỗi `anyhow`. Chỉ đổi nội dung log (commit `84de9ab3`). Thư viện cũ được sao lưu vào scratchpad trước khi build lại `target/release/libmtn_nomt.a`.
- `build_check.sh` qua 5/5, không cảnh báo; test `pkg/nomt_ffi` qua [ĐO].
- Dựng lại binary node với thư viện mới và chạy sustained 2 triệu tx trên cụm mới mỗi lượt, BFT 4 validator, để bắt lại lỗi. Runner giữ mọi dòng `[nomt_ffi]`, `panic`, `poison`, `ENOSPC`, `io_uring` của log node và nén cả log nếu một node chết.

## 2. Kết quả [ĐO]
| Chỉ số | Giá trị | Evidence |
|---|---|---|
| Số lượt hoàn tất (dừng sau lượt 17 theo yêu cầu) | 17 | evidence:nomt_errlog |
| Số lượt gửi = nhận = commit | 17 trên 17 | evidence:nomt_errlog |
| Số node chết | 0 | evidence:nomt_errlog |
| Số tệp lỗi NOMT/Rust khác rỗng | 0 trên 68 (17 lượt × 4 node) | evidence:nomt_errlog |
| TPS đếm đầu-cuối (trung bình ± SD, tx/s) | 14500,19 ± 257,15 | evidence:nomt_errlog |

- Parity khớp trên cả 4 validator ở mọi lượt (evidence:nomt_errlog).
- Cộng với 20 lượt trước và 13 lượt kiểm chứng bản sửa mạng, lỗi `failed to commit payload` đã xuất hiện **1 lần** trong khoảng 50 lượt BFT 4 validator hợp lệ [TÍNH, ước lượng thô].

## 3. Kết luận
**Không tái hiện được** trong 17 lượt mới. Log mới chưa được thấy hoạt động trên một lỗi thật, nên chưa xác nhận được định dạng `{:#}` in đúng chuỗi nguyên nhân. Nguyên nhân gốc vẫn **chưa biết**: một lần xảy ra trên khoảng 50 lượt cho tần suất khoảng 2%; xác suất không gặp trong 17 lượt nếu tần suất đó là thật khoảng 71% (0,98^17) [TÍNH, thô], nên kết quả này bình thường và không cho thấy lỗi đã hết.

## 4. Giới hạn
- Một máy chung; số TPS đo ban đêm cao hơn ban ngày (14,5k ở 2 triệu tx so với khoảng 11,5k ở đợt quét ban ngày), nên không so TPS giữa các đợt.
- Binary dùng thư viện NOMT mới đúng cho lô này; `bins_new` của các lô khác vẫn dùng thư viện cũ.
- Chỉ một cấu hình tải (sustained 2 triệu tx).

## 5. Chưa làm
- Chờ lần xảy ra tiếp theo để đọc chuỗi nguyên nhân thật; có thể chạy trong CI/nền khi máy rảnh.
- Thử ép điều kiện (đĩa gần đầy, tải I/O nền, nhiều node ghi mạnh) để tăng tần suất.
