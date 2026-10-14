# So sánh node cũ (`f97a2fbf`) với node hiện tại: vì sao số công bố 14.072 tx/s khác 12,7k — 2026-10-14

Nhãn: **[ĐO]** = lệnh chạy trong phiên này, có file thô; **[TÍNH]**; **[ĐỌC CODE]**; **[GIẢ THUYẾT]**; **[CHƯA ĐO]**.
Evidence: `note/evidence/node_ab_old_vs_new_20261014/` (manifest `MANIFEST.json`).

## 1. Câu hỏi
`note/tps_full_measurement_report_20261009.md` công bố BFT sustained 60 s ≈ 14.072 tx/s (binary build từ `f97a2fbf`). Bản hiện tại (đã sửa lỗi mất tx ở `HandleConnection`) đo được khoảng 12,7k tx/s bằng công cụ đếm. Do đo khác nhau, do mã chậm đi, hay do hai bản chịu khối lượng khác nhau?

## 2. Cách làm
- **Bản cũ ("old")** là một bản lai: mã Go của `f97a2fbf`, liên kết với các thư viện Rust hiện tại. Giữa `f97a2fbf` và HEAD, phần Rust chỉ khác `consensus/vendor/nomt/nomt/src/io/mod.rs` (bản vá `recv_timeout`) và `commit_syncer` [ĐỌC CODE: `git diff --stat`]; nên thí nghiệm cô lập các thay đổi phía Go, chủ yếu `pkg/network` (server/connection).
- **Bản mới ("new")** là binary đã sửa lỗi mất tx (commit `1cea5f0e`).
- Cùng công cụ đo đếm, cùng cấu hình, cụm mới cho mỗi lượt, BFT 4 validator, cổng 31xxx, cùng một máy; các lượt old/new **xen kẽ**, thứ tự trong mỗi cặp xáo ngẫu nhiên có seed (`order.txt`).
- Thí nghiệm 1 (tải nặng): 8 cặp, `-duration 60` cho cả hai bản. Thí nghiệm 2 (khối lượng tx tương đương): 5 cặp; bản cũ `-duration 60`, bản mới `-duration 7`.
- Hai thử nghiệm bị loại: lượt burst 850.000 tx chỉ gửi 50.000 tx (công cụ bị giới hạn bởi 50.000 khoá), và lượt bản mới `-duration 20` gửi 2,4 triệu tx (không khớp). Lượt 20 s được giữ làm thông tin (evidence:node_ab_unmatched).

## 3. Kết quả [ĐO]
### 3.1 Thí nghiệm 1: cùng `-duration 60` (8 cặp)
| Chỉ số | Bản cũ (trung bình) | Bản mới (trung bình) | Evidence |
|---|---|---|---|
| TPS đếm đầu-cuối (tx/s) | 13423,3 | 11447,7 | evidence:node_ab1 |
| TPS đếm trong cửa sổ có block (tx/s) | 13716,1 | 11484,1 | evidence:node_ab1 |
| Tx đã gửi mỗi lượt | 863.125 | 4.193.297 | evidence:node_ab1 |
| Thời gian cả lượt (s) | 64,3 | 367,0 | evidence:node_ab1 |

- Chênh ghép cặp (cũ − mới) đầu-cuối = +1.975,6 tx/s, t = 6,76, df = 7, p = 0,0003, CI95 [1284,4; 2666,7]; cửa sổ có block +2.231,9 tx/s, p = 0,0002 [TÍNH].
- Bản cũ làm chậm client: chỉ nhận khoảng 0,86 triệu tx trong 60 s. Bản mới nhận khoảng 4,2 triệu tx (ingress không còn chặn client như trước) rồi xả backlog khoảng 6 phút. Hai bản chịu tải rất khác nhau, nên chênh lệch này chưa thể gán cho mã [TÍNH].
- Bản cũ mất 8.000 tx (một lượt 10.000) giữa client và validator ở 8/8 lượt; bản mới 0/8 (evidence:node_ab1).
- Số của bản cũ ở thí nghiệm này (13,4k đầu-cuối; số công thức cũ 14,2k) cùng bậc với số đã công bố 14.072 tx/s [TÍNH, so với báo cáo 2026-10-09].

### 3.2 Thí nghiệm 2: khối lượng tx tương đương (5 cặp)
| Chỉ số | Bản cũ (trung bình) | Bản mới (trung bình) | Evidence |
|---|---|---|---|
| TPS đếm đầu-cuối (tx/s) | 11727,6 | 12425,7 | evidence:node_ab2 |
| TPS đếm trong cửa sổ có block (tx/s) | 11939,5 | 12728,7 | evidence:node_ab2 |
| Tx đã gửi mỗi lượt | 923.400 | 732.951 | evidence:node_ab2 |
| Thời gian cả lượt (s) | 78,0 | 59,0 | evidence:node_ab2 |

- Chênh ghép cặp (mới − cũ) đầu-cuối = +698,1 tx/s, t = 3,56, df = 4, p = 0,0236, CI95 [153,8; 1242,4]; cửa sổ có block +789,2 tx/s, t = 4,22, p = 0,0135, CI95 [269,6; 1308,8] [TÍNH].
- Ở khối lượng tx gần nhau (0,73 triệu so với 0,92 triệu), **bản mới nhanh hơn bản cũ khoảng 6%** (evidence:node_ab2), không chậm hơn. Chênh lệch về số tx gửi (không khớp hoàn toàn) là một yếu tố nhiễu còn lại.
- Cả 10 lượt: parity khớp trên 4 validator; bản cũ vẫn gửi thừa 8.000 tx so với số nhận ở 5/5 lượt, bản mới 0/5 (evidence:node_ab2).

### 3.3 Lượt thông tin: bản mới với 2,4 triệu tx (1 lượt)
Bản mới `-duration 20`: gửi 2.396.393 tx, commit 10.944 tx/s đầu-cuối và 11.009 tx/s trong cửa sổ có block, tổng 219 s (evidence:node_ab_unmatched). Với backlog lớn hơn thì tốc độ commit thấp hơn, cùng chiều với giả thuyết backlog [GIẢ THUYẾT, chỉ 1 lượt].

## 4. Giải thích [TÍNH + GIẢ THUYẾT]
1. **Số 14.072 tx/s có thể tái lập với mã cũ**, trong điều kiện mã cũ tự giới hạn khối lượng (client bị chặn, ~0,86 triệu tx mỗi lượt 60 s); đo nó cho kết quả 13,4k đầu-cuối ở thí nghiệm 1.
2. **Bản mới không chậm đi ở cùng khối lượng tx** (thí nghiệm 2: +6%, p = 0,02). Chênh lệch 17% ở thí nghiệm 1 đi cùng khác biệt khối lượng gấp 4,9 lần (4,2 triệu so với 0,86 triệu tx); backlog lớn chắc chắn là một phần nguyên nhân [GIẢ THUYẾT, chưa có can thiệp nào loại bỏ nhân tố khác].
3. **Số TPS tuyệt đối dao động theo thời điểm:** bản cũ đo 13,4k ở thí nghiệm 1 nhưng 11,7k ở thí nghiệm 2 (cùng binary, chạy cách nhau vài giờ) (evidence:node_ab1, evidence:node_ab2). Trong thí nghiệm 1, các lượt của bản cũ cũng giảm từ 14,4k–14,7k ở cặp 1–3 xuống 11,8k và 12,8k ở cặp 6–7. Máy dùng chung nên chỉ có so sánh ghép cặp trong cùng một đợt chạy là đáng tin; các số tuyệt đối giữa các ngày không so được [GIẢ THUYẾT: tải của tiến trình khác trên máy].
4. Phần "mất tx" của bản cũ khớp với lỗi đã sửa ở `HandleConnection` (xem `note/tps_ingress_rootcause_report_20261013.md`).

## 5. Giới hạn
- Bản cũ là bản lai (Go cũ + Rust hiện tại), không phải binary nguyên bản của `f97a2fbf`; khác biệt phía Rust (bản vá `recv_timeout` của NOMT, `commit_syncer`) chưa được tách riêng.
- Thí nghiệm 2 chỉ có 5 cặp; khối lượng tx của hai bản chưa bằng nhau (bản cũ nhiều hơn khoảng 26%); khoảng tin cậy rộng.
- Một máy chung, 4 validator cùng máy; tải nền của máy thay đổi theo giờ (xem mục 4.3).
- Chưa có thí nghiệm can thiệp tách riêng "ảnh hưởng của backlog" từ "ảnh hưởng của mã".

## 6. Chưa làm
- Chạy thí nghiệm hai nhánh với khối lượng tx chính xác bằng nhau (ví dụ giới hạn tốc độ), hoặc quét khối lượng (0,5 / 1 / 2 / 4 triệu tx) cho bản mới để thấy đường cong tốc độ commit theo kích thước backlog.
- Build binary nguyên bản của `f97a2fbf` (cả Rust) để loại trừ phần Rust.
- Lặp lại thí nghiệm trên máy ít tải hơn để thu nhỏ độ dao động.
