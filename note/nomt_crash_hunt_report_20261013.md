# Tìm lại lần panic NOMT `failed to commit payload`: 20 lượt sustained không tái hiện — 2026-10-13

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/nomt_crash_hunt_20261013/` (manifest `MANIFEST.json`). Tiếp nối `note/tps_ingress_rootcause_report_20261013.md` mục 6.

## 1. Lần panic gốc
Ở lượt 11 của lô kiểm chứng bản sửa, validator 3 chết (`panic: FATAL: StakeStateDB async commit failed: nomt_ffi: failed to commit payload`, block 45, namespace `stake_db`). Dòng Rust còn sót lại trong log stdout là `[nomt_ffi] nomt_commit_payload failed: Store is poisoned due to prior error` (`note/evidence/tps_ingress_rootcause_20261013/node_crash_run11/`) [ĐO].

## 2. Phân tích mã [ĐỌC CODE]
- `consensus/vendor/nomt/nomt/src/store/mod.rs`: khi `sync.sync(...)` trả lỗi ở một lần commit, store bị đánh dấu `poisoned`, và mọi commit sau đó thất bại với đúng thông điệp "Store is poisoned due to prior error".
- Vậy lỗi thấy được ở panic chỉ là hậu quả; **lỗi đầu tiên** làm store bị nhiễm mới là nguyên nhân, và nó nằm ngoài cửa sổ 150 dòng cuối của log mà runner lưu lúc đó. Cụm đã bị xoá, nên không thể khôi phục.
- `execution/pkg/nomt_ffi/rust_lib/src/lib.rs` in lỗi bằng `eprintln!("... {}", e)` (chỉ thông điệp ngoài cùng, không in chuỗi nguyên nhân).

## 3. Thí nghiệm
Runner nay giữ toàn bộ các dòng `[nomt_ffi]`, `panic`, `fatal error`, `SIGSEGV`, `poison`, `ENOSPC`, `No space`, `Input/output`, `io_uring` trong log stdout của từng node, và nén cả log nếu node không còn sống (`*_DEAD_stdout.log.gz`). Chạy sustained 60 s, BFT 4 validator, binary đã sửa lỗi mạng, cụm mới cho mỗi lượt; dừng sau 20 lượt theo yêu cầu.

## 4. Kết quả [ĐO]
| Chỉ số | Giá trị | Evidence |
|---|---|---|
| Số lượt hoàn tất | 20 | evidence:nomt_crash_hunt |
| Số lượt gửi = nhận = commit | 20 trên 20 | evidence:nomt_crash_hunt |
| Số node chết (file `DEAD_stdout`) | 0 | evidence:nomt_crash_hunt |
| Số file `*_nomt_errors.txt` khác rỗng | 0 trên 80 (20 lượt × 4 node) | evidence:nomt_crash_hunt |
| TPS đếm từ chuỗi, đầu-cuối (trung bình ± SD, tx/s) | 12738.66 ± 412.66 | evidence:nomt_crash_hunt |
| TPS phương pháp cũ (trung bình, tx/s) | 13236.89 | evidence:nomt_crash_hunt |

- Chênh ghép cặp (cũ − đếm) = +498,23 tx/s, t = 7,35, df = 19, p < 0,0001, CI95 [356,3; 640,1] [TÍNH từ evidence:nomt_crash_hunt]: số cũ cao hơn khoảng 3,9%, cùng chiều với các lô trước.
- Gộp với lô kiểm chứng trước (13 lượt hợp lệ, `note/evidence/tps_ingress_rootcause_20261013/fix_validation/`): 33 lượt liên tiếp với binary đã sửa không mất tx ở tầng ingress [TÍNH].

## 5. Kết luận (3 trạng thái)
**Không tái hiện được** lỗi `failed to commit payload` trong 20 lượt mới (cộng khoảng 13 lượt của lô trước chỉ có một lần xảy ra). Đây không phải bằng chứng lỗi đã hết hay không liên quan đến bản sửa mạng: một lần xảy ra trong ~33–34 lượt cho ước lượng tần suất rất thấp, và 20 lượt không chạm lỗi là kết quả bình thường nếu tần suất khoảng 3% (xác suất không gặp trong 20 lượt khoảng 54%) [TÍNH, thô]. Nguyên nhân gốc vẫn **chưa biết**.

## 6. Giới hạn
- Một máy chung, 4 validator cùng máy; không đo tải của tiến trình khác.
- Chỉ chạy tải sustained 60 s của công cụ này; chưa thử tải khác, đĩa gần đầy, hay nhiều node cùng lúc ghi mạnh.
- Cửa sổ ghi lỗi mới có hiệu lực: nếu lỗi xảy ra mà node vẫn sống thì chỉ có file `nomt_errors`; chưa có chuỗi nguyên nhân Rust đầy đủ vì `{}` chỉ in lỗi ngoài cùng.

## 7. Chưa làm
- Đổi `eprintln!` trong `nomt_ffi` sang in cả chuỗi nguyên nhân (`{:#}`) và build lại thư viện Rust dùng chung; chưa làm vì động vào `target/release` dùng chung, cần bạn đồng ý.
- Chạy kéo dài hơn hoặc ép điều kiện (đĩa gần đầy, tải I/O nền) để tìm lỗi đầu tiên làm store bị nhiễm.
