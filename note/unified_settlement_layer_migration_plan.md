# Kế hoạch chuyển sang mô hình "1 sổ cái thống nhất" (Root Anchor = Settlement Layer)

Viết 2026-08-27, theo yêu cầu đánh giá việc chuyển từ mô hình hiện tại (mỗi chain giữ
`per_chain_allocation` riêng, xem `note/cross_chain_root_anchor_architecture.md` mục 2.3)
sang mô hình **1 đồng coin, 1 sổ cái số dư thật duy nhất**, dùng chung cho mọi private chain.

## 0. Đính chính trước: đây KHÔNG phải mô hình Superchain (OP Stack) thật

Superchain (Optimism) — mô hình nổi tiếng nhất mang tên này — **không** dùng 1 sổ cái số dư
chung: mỗi L2 vẫn giữ trạng thái riêng, tài sản bridge qua xác nhận ở L1 (fraud proof/validity
proof), kể cả chuẩn "interop" mới nhất (2024+) vẫn là 2 L2 nhắn tin trực tiếp cho nhau, không
gộp chung 1 sổ cái. Cái bạn mô tả ("1 đồng, 1 sổ cái duy nhất cho tất cả chain") gần với mô
hình **Ethereum L1 + Rollup** hơn: L1 giữ trạng thái GỐC thật, chain con chỉ thực thi rồi cam
kết ngược về L1. Tài liệu này viết kế hoạch cho đúng mô hình L1+Rollup đó, áp cho Root Anchor
đóng vai trò L1.

## 1. So sánh 2 mô hình

| | Hiện tại (Custodial/Reserve, mục 2.3) | Đề xuất (Root Anchor = Settlement Layer) |
| :--- | :--- | :--- |
| Số dư "thật" của đồng coin thống nhất nằm ở đâu | Mỗi chain tự giữ, Root Anchor chỉ giữ **trần** (`per_chain_allocation`) | **Chỉ 1 nơi duy nhất: Root Anchor** — private chain không giữ số dư thật nữa |
| Private chain có chủ quyền với đồng coin đó không | Có — tự do mint/burn trong giới hạn trần | **Không** — chỉ thực thi/hiển thị, số dư thật luôn đối chiếu về Root Anchor |
| Root Anchor có cần biết TRẠNG THÁI THẬT bên trong 1 private chain không | Không — chỉ verify chữ ký BLS + trần | **Có** — phải verify/lưu đúng số dư account-level, không chỉ tổng hợp theo chain |
| Độ trễ giao dịch liên quan đồng coin này | Bình thường (đồng thuận nội bộ 1 chain) | **Tăng** — mọi giao dịch cần Root Anchor xác nhận (đồng bộ) hoặc cam kết + chờ challenge window (optimistic) |
| Thông lượng | Không đổi so với hiện tại | Bị giới hạn thêm bởi băng thông xử lý của riêng Root Anchor — ngược mục tiêu >30K TPS |
| An toàn khi 1 chain bị chiếm | Giới hạn ở đúng trần chain đó (đã có) | Tốt hơn về lý thuyết (Root Anchor tự verify account-level) — nhưng cần cơ chế mới hoàn toàn để làm đúng (mục 3) |

**Đánh đổi cốt lõi, cần người phụ trách kiến trúc/kinh doanh xác nhận trước khi làm bất cứ gì
khác**: mô hình mới **xoá bỏ chủ quyền của private chain với riêng đồng coin thống nhất** — đây
ngược hẳn nguyên tắc thiết kế đã ghi rõ trong `cross_chain_root_anchor_architecture.md` mục 1.2
("mỗi private chain có chủ quyền riêng"). Private chain vẫn giữ chủ quyền với MỌI THỨ KHÁC
(logic riêng, token riêng không thuộc đồng coin thống nhất, validator riêng) — chỉ mất chủ
quyền với đúng 1 đồng coin này.

## 2. Kế hoạch di trú theo giai đoạn

### Phase 0 — Quyết định kiến trúc (bắt buộc trước khi viết code)
- Xác nhận đánh đổi chủ quyền ở mục 1 là chấp nhận được — đây là quyết định kinh doanh, không
  phải kỹ thuật.
- Phạm vi: chỉ đồng coin native, hay cả custom asset (`AssetRegistry`) cũng theo mô hình mới?
- **Quyết định lớn nhất, ảnh hưởng toàn bộ phần còn lại**: chọn 1 trong 2 hướng thực thi —
  - **(A) Đồng bộ**: mọi giao dịch động tới đồng coin này phải CHỜ Root Anchor xác nhận mới
    coi là final. Đơn giản, an toàn ngay, nhưng độ trễ = round-trip tới Root Anchor +
    đồng thuận ở đó cho MỌI giao dịch — ảnh hưởng thông lượng nghiêm trọng.
  - **(B) Optimistic**: private chain thực thi cục bộ trước (nhanh), cam kết trạng thái lên
    Root Anchor sau, có "cửa sổ thách thức" (challenge window) cho bên thứ 3 chứng minh gian
    lận trước khi trạng thái coi là final. Giữ được thông lượng, nhưng cần xây **cơ chế chứng
    minh gian lận (fraud proof)** — thứ các dự án Optimistic Rollup thật (Optimism, Arbitrum)
    tốn nhiều năm để làm đúng, và **hiện chưa tồn tại trong code base này ở bất kỳ dạng nào**.

### Phase 1 — Đặc tả API "Settlement Layer" trên Root Anchor
- Thiết kế lại `GatewayEngine.SupplyLedger`: từ state cục bộ MỖI CHAIN (hiện tại, xác nhận qua
  code — mỗi `GatewayEngine` có `SupplyLedger` riêng) thành 1 sổ cái DUY NHẤT sống trên Root
  Anchor, lưu số dư theo TỪNG ĐỊA CHỈ (account-level), không chỉ tổng theo `per_chain_allocation`.
- Thiết kế API cho private chain đọc/ghi vào sổ cái này (tuỳ theo lựa chọn Phase 0.A/B).

### Phase 2 — Cơ chế thực thi
- Nếu chọn (B) optimistic: xây fraud-proof cho đúng phạm vi cần (tối thiểu: chứng minh 1 private
  chain khai sai số dư/thực thi sai) — đây là khối lượng công việc lớn nhất của toàn kế hoạch,
  tương đương xây mới 1 hệ thống con hoàn toàn khác BLS-quorum-cert hiện có.
- Nếu chọn (A) đồng bộ: đơn giản hơn nhiều về bảo mật, nhưng cần đo lại toàn bộ mục tiêu thông
  lượng (`HOW_TO_TUNE_BLOCK_SIZE.md`, mục 13 kiến trúc doc) vì giả định "mỗi chain tự xử lý
  song song độc lập" không còn đúng cho riêng luồng đồng coin này.

### Phase 3 — Di trú dữ liệu hiện có (bước nhạy cảm nhất về giá trị thật)
- Mọi chain đang chạy đã có số dư thật cục bộ + `per_chain_allocation` riêng — cần snapshot,
  đối chiếu đúng bất biến `Σ per_chain_allocation == genesis_total_supply` (mục 2.1), rồi
  "import" đúng 1 lần vào sổ cái mới trên Root Anchor.
- Sai ở bước này = mất hoặc nhân đôi giá trị thật vĩnh viễn — cần môi trường đóng băng tạm
  thời (tương tự 1 đợt hard-fork thật) + kiểm thử lặp lại nhiều lần trên testnet trước khi làm
  thật.

### Phase 4 — Kiểm thử & audit riêng
- Đây là thay đổi kiến trúc lõi ảnh hưởng trực tiếp an toàn giá trị — **không dùng lại phạm vi
  P5 cũ** (`external_security_audit_scope_p5.md` được viết cho mô hình custodial hiện tại),
  cần 1 đợt audit độc lập MỚI, phạm vi riêng cho toàn bộ settlement layer + fraud proof (nếu có).

### Phase 5 — Rollout dần
- Giữ đúng tinh thần T4 hiện tại (mục 12 kiến trúc doc): giá trị nhỏ trước, tăng dần theo thời
  gian quan sát không sự cố — có thể chạy song song cả 2 mô hình (cũ giữ nguyên cho chain chưa
  migrate, mới cho chain đã migrate) một thời gian trước khi khoá hẳn.

## 3. Khuyến nghị

Đây là **thay đổi kiến trúc lớn nhất có thể làm cho hệ thống này** — lớn hơn hẳn mọi việc đã
bàn trước đó (cọc thay vote, giảm validator...). Trước khi đầu tư công sức thiết kế chi tiết
hơn, cần trả lời dứt khoát 2 câu hỏi ở Phase 0: **(1)** đánh đổi mất chủ quyền coin có chấp
nhận được với mục tiêu kinh doanh không, và **(2)** chọn (A) đồng bộ hay (B) optimistic —
2 câu trả lời này quyết định toàn bộ khối lượng công việc thật sự (chênh nhau rất nhiều: (A)
là redesign lớn, (B) là redesign lớn + xây mới 1 hệ thống fraud-proof từ đầu).

Với hiện trạng dự án (P5 audit độc lập cho mô hình HIỆN TẠI còn chưa làm, T2 nhiều máy thật còn
chưa xong) — khuyến nghị: **hoàn thành mô hình hiện tại qua đủ P5+T2+T3 trước**, sau đó coi kế
hoạch này là 1 sáng kiến v2 riêng biệt, không làm song song để tránh nhân đôi bề mặt cần audit
trong khi cả 2 mô hình đều chưa được xác minh độc lập lần nào.
