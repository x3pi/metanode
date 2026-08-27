# Kế hoạch: Đồng Tiền Thống Nhất Kiểu "Eurozone" — Bỏ Cấp Phát Qua Vote, Giữ Nguyên Chủ Quyền Chain

Viết 2026-08-27. **Thay thế khuyến nghị của `note/unified_settlement_layer_migration_plan.md`**
(mô hình "Root Anchor = L1, private chain mất chủ quyền coin") — tài liệu đó vẫn giữ nguyên làm
tham khảo lịch sử, nhưng **không còn là hướng khuyến nghị**. Hướng này nhẹ hơn nhiều, dựa gần
như hoàn toàn vào cơ chế đã có sẵn trong code, không cần bỏ chủ quyền private chain.

## 0. Ý tưởng cốt lõi — đúng tinh thần Eurozone

Các nước dùng chung đồng Euro **không cần Ngân hàng Trung ương Châu Âu (ECB) duyệt từng giao
dịch nội địa** giữa 2 công dân Pháp — ECB chỉ quan tâm **tổng cung tiền lưu thông**. Mỗi nước
vẫn giữ chính phủ, ngân hàng, chính sách riêng của mình — chỉ dùng chung 1 đơn vị tiền tệ.

Áp đúng mô hình này vào hệ thống: **mỗi private chain giữ nguyên 100% chủ quyền** (validator
riêng, đồng thuận riêng, giao dịch nội bộ riêng, không đổi gì) — chỉ riêng việc **"chain đó
được phép có bao nhiêu đồng coin thống nhất"** không còn quyết định bằng vote, mà quyết định
bằng **đã thực sự NHẬN được bao nhiêu coin thật từ chain khác** (đúng cơ chế cấp phát mà bạn
đề xuất, hoá ra **đã tồn tại sẵn 90% trong code**, xem mục 1).

## 1. Phát hiện quan trọng: cơ chế cần thiết ĐÃ CÓ SẴN, chỉ cần bỏ 1 con đường "vote ra tiền"

Đọc thẳng `pkg/cross_chain/gateway.go`, xác nhận có **2 con đường** dẫn tới
`per_chain_allocation` của 1 chain:

| Con đường | Cơ chế | Có "in tiền mới" không? |
| :--- | :--- | :--- |
| `ClaimMessage()` — nhận tiền thật từ chain khác | Tự động cộng đúng số nhận được vào allocation của chain nhận — **không qua vote**, đã chạy sống, có test | **Không** — chỉ chuyển tiền đã tồn tại từ nơi khác |
| `ProposalAllocateSupply`/`GrantAllocation` — cấp qua vote | Governance vote xong thì **cộng thẳng vào `GenesisTotalSupply`** (xác nhận trong `types.go`: `g.GenesisTotalSupply = new(big.Int).Add(g.GenesisTotalSupply, amount)`) | **CÓ** — đây thực chất là cơ chế IN TIỀN MỚI qua vote, không phải chuyển tiền có sẵn |

**→ `ProposalAllocateSupply` không phải "cấp phát", mà là "in tiền mới bằng vote"** — đây
chính xác là lỗ hổng Sybil đã phân tích ở các lượt trước (kiểm soát đủ phiếu = tự in tiền cho
mình). Cơ chế `ClaimMessage()` (chuyển tiền thật, không in mới) mới là thứ an toàn, và nó **đã
chạy sống, có test, không cần xây thêm gì.**

**Việc cần làm thu hẹp lại chỉ còn đúng 1 việc**: bỏ (hoặc khoá chặt) con đường
`ProposalAllocateSupply`, thay bằng: chain mới muốn có allocation khởi đầu thì **phải nhận 1
giao dịch chuyển tiền thật** từ 1 chain đã có coin (thường là Reserve, dùng đúng
`genesis_total_supply` đã mint sẵn lúc khởi tạo — không đổi gì ở bước genesis) — qua đúng
`outbound()`/`ClaimMessage()` sẵn có, không phải cơ chế mới.

**Việc KHÔNG đổi (quan trọng, giữ nguyên toàn bộ)**:
- `ProposalRegisterChain`/`MinFoundingChains`/`bootstrapFoundingChains` — vẫn giữ vote-gate như
  cũ. Lý do: governance còn dùng cho việc KHÁC ngoài tiền (đổi uỷ ban, tham số hệ thống...) —
  Sybil đăng ký nhiều chain giả vẫn có thể lạm dụng những việc NÀY nếu bỏ luôn vote đăng ký, dù
  không còn "in tiền" được nữa. Chỉ bỏ đúng `ProposalAllocateSupply`, không đụng gì khác.
- Chủ quyền private chain (validator, đồng thuận, giao dịch nội bộ) — không đổi.
- Cơ chế trần `per_chain_allocation` chặn thiệt hại khi 1 chain bị chiếm — giữ nguyên, chỉ đổi
  NGUỒN GỐC con số trần (từ vote sang từ tiền thật đã nhận).

## 2. Đánh giá rủi ro bảo mật chi tiết

### 2.1 Sybil đăng ký chain giả để thao túng GOVERNANCE (không phải tiền) — vẫn còn, cần xử lý riêng
Bỏ `ProposalAllocateSupply` chỉ bịt lỗ "Sybil để in tiền" — **không** bịt lỗ "Sybil để thao
túng các quyết định governance khác" (đổi uỷ ban 1 chain, đổi tham số hệ thống, các loại
proposal tương lai). Vì `ProposalRegisterChain` vẫn giữ vote-gate (mục 1), rủi ro này **không
tệ hơn hiện tại** — nhưng cần nói rõ: bỏ vote cấp tiền **không đồng nghĩa** hệ thống governance
đã an toàn tuyệt đối, chỉ đóng đúng 1 lỗ hổng cụ thể.

### 2.2 Tập trung rủi ro vào tài khoản Treasury (Reserve) — rủi ro MỚI cần thiết kế cẩn thận
Nếu allocation khởi đầu giờ đến từ 1 giao dịch chuyển tiền thật do Reserve gửi, thì **ai giữ
khoá gửi giao dịch đó** trở thành điểm tập trung quyền lực mới: người/nhóm đó quyết định chain
nào được "bơm" bao nhiêu tiền khởi đầu — về bản chất thay "vote nhiều chain" bằng "1 khoá đơn
lẻ (hoặc multisig) quyết định". **Bắt buộc**: khoá gửi treasury này phải là multisig thật (≥2/3
founding chains ký cùng, không phải 1 khoá đơn) — nếu không, đây là bước THỤT LÙI so với vote
đa chain hiện tại (từ "cần đồng thuận nhiều bên" xuống "1 khoá tự quyết").

### 2.3 Câu hỏi CHƯA XÁC MINH — đồng bộ trần allocation giữa nhiều chain đích khác nhau
Đây là điểm tôi **chưa verify được đầy đủ trong phiên này, cần điều tra kỹ trước khi triển
khai bất kỳ thay đổi nào**: `SupplyLedger` là state **cục bộ theo từng chain** (mỗi
`GatewayEngine` có bản sao riêng) — khi chain B `attestCommit()` 1 commit từ chain A, B chỉ trừ
đúng bản sao CỦA B về "A còn được phép gửi bao nhiêu". Câu hỏi mở: nếu chain C **cũng**
`attestCommit()` trực tiếp từ A (message loại (a) value=0 đi thẳng, mục 2.2 kiến trúc doc, hoặc
nếu tương lai value>0 cũng được set đi thẳng), C có bản sao RIÊNG của "A còn được gửi bao
nhiêu" — **2 bản sao này có đồng bộ với nhau không, hay mỗi chain đích tự trừ theo view riêng,
dẫn tới A có thể bị "chi tiêu 2 lần" từ góc nhìn của 2 chain đích khác nhau cộng lại vượt quá
allocation thật của A?** Cần trả lời dứt khoát câu này (đọc kỹ `attestCommitInternal`/luồng
đồng bộ `ChainRegistry` giữa các chain) trước khi thực hiện bất kỳ thay đổi nào ở mục 1 — đây
là rủi ro có thể đã tồn tại độc lập với đề xuất này, không phải rủi ro MỚI do đề xuất tạo ra,
nhưng đáng được điều tra riêng vì ảnh hưởng trực tiếp tới đúng bất biến `Σ per_chain_allocation
== genesis_total_supply` mà toàn bộ mô hình an toàn dựa vào.

### 2.4 Chain "nghèo" mãi mãi nếu không ai chịu chuyển tiền cho nó
Mô hình vote cũ có ưu điểm: cộng đồng CÓ THỂ quyết định "cấp thêm" cho 1 chain đang cần, dù nó
chưa từng nhận gì (linh hoạt). Mô hình mới: 1 chain hoàn toàn mới, không ai chuyển tiền cho, sẽ
**mãi mãi có allocation = 0**, dù được ĐĂNG KÝ hợp lệ. Đây không phải lỗ hổng bảo mật, nhưng là
đánh đổi vận hành cần lường trước: cần 1 quy trình rõ ràng (ai, khi nào, quyết định số tiền
seed ban đầu qua treasury multisig ở mục 2.2) — không tự động, cần con người quyết định.

### 2.5 Tương thích ngược — code/tài liệu đang tham chiếu `ProposalAllocateSupply`
`note/dapp_cross_chain_developer_guide.md`, `note/external_security_audit_scope_p5.md`,
`cmd/tool/live_asset_bridge/main.go` đều tham chiếu cơ chế cũ — cần cập nhật đồng bộ nếu triển
khai, tránh tài liệu/tool nói sai cơ chế thật (đúng bài học "PR gì đó" tài liệu bị lag sau code
đã gặp nhiều lần trong phiên này).

## 3. Kế hoạch triển khai theo giai đoạn (nhẹ hơn hẳn bản "sổ cái thống nhất" cũ)

1. **Điều tra dứt điểm mục 2.3** (đồng bộ trần giữa nhiều chain đích) — làm TRƯỚC TIÊN, có thể
   phát hiện đây đã là việc cần vá độc lập, không phụ thuộc quyết định có làm phần còn lại
   không.
2. Thiết kế cơ chế treasury multisig (mục 2.2) — bao nhiêu chữ ký, ai được ký, quy trình quyết
   định số tiền seed.
3. Khoá/xoá `ProposalAllocateSupply` khỏi `ExecuteGovernanceProposal` (hoặc giữ code nhưng
   chặn ở tầng chính sách vận hành — quyết định kỹ thuật cụ thể cần bàn thêm).
4. Viết quy trình vận hành onboarding chain mới: đăng ký (`ProposalRegisterChain`, không đổi)
   → chờ treasury multisig gửi giao dịch seed thật → chain tự nhận qua `ClaimMessage()` có sẵn.
5. Cập nhật đồng bộ mọi tài liệu/tool ở mục 2.5.
6. Test hồi quy: xác nhận chain mới KHÔNG còn cách nào tự cấp allocation cho mình ngoài nhận
   tiền thật; xác nhận treasury multisig hoạt động đúng ngưỡng ký.
7. Không cần đợt audit độc lập MỚI riêng biệt như bản "sổ cái thống nhất" cũ đòi hỏi — thay đổi
   này thu hẹp bề mặt tấn công (bớt 1 cách in tiền) chứ không mở bề mặt mới, nhưng vẫn nên đưa
   vào phạm vi P5 hiện có trước khi coi là xong (không tự ý coi là "an toàn hơn nên không cần
   audit lại").

## 4. So sánh nhanh với bản "sổ cái thống nhất" (`unified_settlement_layer_migration_plan.md`)

| | Sổ cái thống nhất (cũ) | Eurozone (mới, khuyến nghị) |
| :--- | :--- | :--- |
| Chủ quyền private chain | Mất (với riêng đồng coin) | **Giữ nguyên 100%** |
| Cần xây fraud-proof mới | Có (nếu chọn optimistic) | **Không** |
| Cần audit độc lập mới riêng | Có, bắt buộc | Nên có, nhưng không phải hệ thống hoàn toàn mới |
| Độ trễ/thông lượng | Giảm (mọi giao dịch coin qua Root Anchor) | **Không đổi** — chỉ đổi cách set allocation, không đổi luồng giao dịch |
| Khối lượng code cần viết mới | Rất lớn (sổ cái mới, API mới, di trú dữ liệu) | **Nhỏ** — chủ yếu là bỏ 1 hàm + thêm quy trình vận hành treasury |
| Rủi ro chưa xác minh | Nhiều, hầu hết thiết kế mới | Có 1 câu hỏi cụ thể cần điều tra (mục 2.3), phạm vi rõ ràng |

**Khuyến nghị dứt khoát**: đi theo hướng Eurozone này, không phải hướng sổ cái thống nhất.
