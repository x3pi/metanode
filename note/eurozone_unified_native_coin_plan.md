# Kế hoạch: Đồng Tiền Thống Nhất Kiểu "Eurozone" — Giữ Nguyên Chủ Quyền Chain

Viết 2026-08-27, cập nhật 2026-09-04. **Thay thế khuyến nghị của
`note/unified_settlement_layer_migration_plan.md`** (mô hình "Root Anchor = L1, private chain
mất chủ quyền coin") — tài liệu đó vẫn giữ nguyên làm tham khảo lịch sử, nhưng **không còn là
hướng khuyến nghị**. Hướng này nhẹ hơn nhiều, dựa gần như hoàn toàn vào cơ chế đã có sẵn trong
code, không cần bỏ chủ quyền private chain.

## KẾT LUẬN CUỐI CÙNG (2026-09-04) — ĐÃ ĐIỀU TRA XONG, KHÔNG CẦN SỬA CODE

Bản kế hoạch gốc (mục 1-4 dưới đây) đề xuất **khoá/xoá `ProposalAllocateSupply`**, dựa trên giả
định lúc đó rằng đây thuần là "lỗ hổng in tiền qua vote". Điều tra kỹ hơn (mục 2.3, và kiểm tra
`register_chains --fund-genesis`) cho ra 2 phát hiện làm thay đổi kết luận:

1. **`ProposalAllocateSupply` là con đường DUY NHẤT để mint `GenesisTotalSupply` lần đầu cho 1
   Reserve chain mới** — `register_chains --fund-genesis` (`fundGenesis()`,
   `cmd/tool/register_chains/main.go:606-662`) gọi thẳng vào nó (kind=5) để bootstrap. Xoá hẳn
   sẽ làm hỏng khả năng khởi tạo hệ thống mới từ đầu.
2. **Nó đã bị khoá an toàn sẵn rồi (C7 fix)** — chỉ cấp được cho đúng Reserve
   (`g.LocalChainID != g.ReserveChainID` bị chặn), và chỉ đúng 1 lần duy nhất trong lịch sử
   (`ErrGenesisAlreadyMinted` chặn vĩnh viễn sau lần mint đầu). Sau lần bootstrap đó, nó vĩnh
   viễn không gọi lại được nữa — về hiệu lực đã tương đương "khoá", không cần thêm code.
3. **Treasury multisig mà mục 2.2/3 gọi là "cần thiết kế" — ĐÃ TỒN TẠI SẴN**, không cần xây
   mới: `ProposalTransferAllocation` (kind=6, di chuyển allocation ĐÃ CÓ giữa các chain, không
   in tiền mới, có kiểm tra số dư thật) đã đi qua đúng `GovernanceEngine.Propose/Vote/Execute`
   — quorum `ceil(2N/3)` các chain đang active, mỗi phiếu phải có chữ ký BLS-committee hợp lệ
   của chain đó, và timelock 72h trước khi thực thi. Đây chính xác là "multisig ≥2/3 founding
   chains ký cùng" mà mục 2.2 yêu cầu.

**→ Quyết định về `ProposalAllocateSupply`: KHÔNG sửa code.** Giữ nguyên như hiện trạng (đã tự
khoá qua C7). Không cần bước "khoá/xoá `ProposalAllocateSupply` khỏi `ExecuteGovernanceProposal`"
như mục 3 bước 3 (cũ) đề xuất — bước đó dựa trên hiểu nhầm rằng cơ chế đang mở, trong khi thực tế
nó đã đóng theo đúng nghĩa cần thiết (1 lần, chỉ Reserve).

### Cập nhật (2026-09-04, cùng ngày): gộp thẳng tiền cọc đăng ký = allocation lưu thông, bớt bước
Câu hỏi còn lại của mục 2.2/2.4 — chain mới đăng ký xong phải chờ ai đó (treasury multisig) gửi
riêng 1 giao dịch mới có allocation lưu thông — được thu gọn tiếp: **tiền cọc thật mà chain trả để
đăng ký (`MinNativeStakeToRegister`, nạp thật vào `GATEWAY_CONTRACT_ADDRESS`, burn-then-mint từ ví
người đăng ký) giờ ĐƯỢC DÙNG LUÔN làm allocation lưu thông ban đầu của chính chain đó** — không
còn 2 bước tách rời (đăng ký → chờ treasury cấp riêng).

Đã sửa `GatewayEngine.RegisterChainViaStake` (`pkg/cross_chain/gateway.go`): NẾU đang chạy trên bản
sao có hiệu lực thật (`LocalChainID == ReserveChainID`, đúng điều kiện enforcement duy nhất theo
mục 2.3) VÀ `MinNativeStakeToRegister > 0`, thì (TRƯỚC KHI ghi `ChainRegistry`, để fail sạch nếu
không đủ tiền) CHUYỂN đúng số tiền cọc đó từ `SupplyLedger.PerChainAllocation[ReserveChainID]`
sang `[chainID mới]`.

**Sửa lỗi bảo mật cùng ngày (do chính bạn yêu cầu rà lại)**: bản đầu tiên dùng `GrantAllocation`
(cùng hàm `ProposalAllocateSupply` dùng) — hàm đó **TĂNG `GenesisTotalSupply`** mỗi lần gọi. Vì
tiền cọc trả thật nhưng lấy từ ví của operator TRÊN ROOT ANCHOR, mà số dư ví trên Root Anchor lại
**không hề truy nguyên được về `GenesisTotalSupply`** (genesis Root Anchor tự bịa alloc riêng,
độc lập hoàn toàn — xem hội thoại 2026-09-04) → bất kỳ ai có ví trên Root Anchor (kể cả tiền
genesis tự bịa) đăng ký 1 chain là tự động MINT THÊM `GenesisTotalSupply`, không giới hạn số lần,
không qua vote — đúng "in tiền từ hư không", còn tệ hơn cả `ProposalAllocateSupply` gốc (cái đó
còn bị khoá 1 lần + cần vote). **Đã sửa: đổi sang `TransferAllocation(ReserveChainID, chainID mới,
amount)`** — CHUYỂN từ pool Reserve đã có sẵn (bản thân pool đó bị giới hạn bởi đúng 1 lần mint
`GenesisTotalSupply` trước đó), không mint thêm. `GenesisTotalSupply` **không bao giờ đổi** ở bước
này. Không cần vote (đúng yêu cầu) — điều kiện duy nhất là Reserve phải THẬT SỰ CÒN ĐỦ trong pool
của chính nó; hết pool thì đăng ký-kèm-cấp-tiền thất bại thẳng (`ErrInsufficientAllocation`),
không đăng ký nửa vời (registry không ghi nếu bước cấp tiền fail), không bao giờ tự tạo thêm.

**Quy trình onboarding chain mới, sau khi gộp (rút từ 3 bước xuống còn thực chất 1 bước cho trường
hợp thường gặp)**:
- Chain mới (không phải Reserve đầu tiên): gửi 1 giao dịch `registerChainViaStake` kèm đúng
  `MinNativeStakeToRegister` → vừa được đăng ký (`ChainRegistry`+`Governance.ActiveChains`), vừa
  có ngay allocation lưu thông = đúng số tiền cọc đó, không cần vote, không cần chờ treasury gửi
  riêng. (`ProposalTransferAllocation`/`ClaimMessage()` vẫn còn nguyên, dùng khi 1 chain muốn
  nhận THÊM ngoài phần tự cọc — ví dụ nhận viện trợ hoặc giao dịch thật từ chain khác.)
- Reserve/chain đầu tiên của cả hệ thống: vẫn qua `register_chains --fund-genesis` (đúng 1 lần,
  xem "KẾT LUẬN CUỐI CÙNG" ở trên) — trường hợp này không "đăng ký cọc" vào 1 hệ thống đã tồn tại,
  mà chính là tạo ra `GenesisTotalSupply` đầu tiên, nên giữ nguyên cơ chế khác.

Mục 2.4 ("chain nghèo mãi mãi nếu không ai chuyển tiền cho") — **coi như đã giải quyết cho MỌI
chain đăng ký từ nay về sau**: chain tự mang tiền cọc thật của mình vào làm vốn lưu thông ban đầu,
không cần chờ ai "bố thí". Đánh đổi vận hành cũ (mục 2.4) chỉ còn áp dụng cho các chain đã đăng ký
TRƯỚC thay đổi này (2026-09-04) mà chưa từng có allocation — các chain đó vẫn cần
`ProposalTransferAllocation` từ treasury như quy trình cũ.

Test hồi quy: `TestGateway_RegisterChainViaStake_CreditsStakeIntoAllocation`
(`pkg/cross_chain/gateway_test.go`) — phủ cả 3 nhánh: (1) trên bản sao Reserve, cọc được cộng
đúng số + bất biến giữ nguyên; (2) trên bản sao KHÔNG PHẢI Reserve, không cộng gì (đúng vì không
có hiệu lực thật); (3) `MinNativeStakeToRegister` chưa cấu hình, không cộng gì (giữ hành vi cũ
với mọi config trước 2026-09-04). Toàn bộ `pkg/cross_chain/...` + `pkg/blockchain/tx_processor/...`
chạy lại xanh sau thay đổi.

Các mục 1-4 dưới đây **giữ nguyên làm nhật ký điều tra** (lý do dẫn tới kết luận trên), không
còn là danh sách việc-cần-làm nữa — xem mục 3 (đã cập nhật) cho danh sách việc thật sự còn lại.

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

### 2.2 Tập trung rủi ro vào tài khoản Treasury (Reserve) — ĐÃ XÁC MINH RESOLVED (2026-09-04)
Lo ngại ban đầu: nếu allocation sau bootstrap đến từ 1 giao dịch chuyển tiền do Reserve gửi, thì
**ai giữ khoá gửi giao dịch đó** trở thành điểm tập trung quyền lực mới. Yêu cầu đặt ra: khoá gửi
phải là multisig thật (≥2/3 founding chains ký cùng), không phải 1 khoá đơn.

**Kết luận: không cần xây gì mới — multisig này đã tồn tại sẵn.** Cơ chế di chuyển allocation
giữa các chain (`ProposalTransferAllocation`, kind=6) không tự gửi bằng 1 khoá đơn — nó bắt buộc
đi qua `GovernanceEngine.Propose/Vote/Execute`: quorum `ceil(2N/3)` chain đang active phải vote
đồng ý, mỗi phiếu ký bằng đúng BLS-committee key của chain đó (không giả mạo được), và có
timelock 72h trước khi thực thi (đủ thời gian phát hiện + phản ứng nếu có phiếu bất thường). Đây
chính xác là yêu cầu "multisig ≥2/3" đặt ra ở trên — không phải thiết kế mới, chỉ cần dùng đúng
cơ chế đã có. Xem "KẾT LUẬN CUỐI CÙNG" ở đầu file.

### 2.3 ĐÃ XÁC MINH (2026-09-04) — không đồng bộ được, nhưng KHÔNG PHẢI lỗ hổng: chỉ có đúng 1 bản sao có ý nghĩa bảo mật
**Kết luận: kịch bản "chi tiêu 2 lần qua 2 chain đích khác nhau" không thể xảy ra — đã bị chặn
sẵn bởi đúng đoạn code C8 fix, không cần vá thêm gì.**

Chìa khoá nằm ở điều kiện đã có sẵn trong `attestCommitInternal`:
```go
if enforceCeiling && aggregateAmount.Sign() > 0 {
    if g.LocalChainID != g.ReserveChainID {
        return ErrNonReserveCeilingAttestation   // chain gọi hàm KHÔNG PHẢI Reserve → từ chối thẳng
    }
}
```
Bất kỳ chain nào **không phải Reserve** cố gắng `attestCommit()` với `enforceCeiling=true` cho
1 commit giá trị > 0 từ chain khác đều bị từ chối ngay lập tức, bất kể chain đó có nhận được
Merkle proof + BLS quorum cert hợp lệ hay không. Vậy dù chain A có gửi `outbound()` thẳng tới cả
B và C (kỹ thuật `outbound()` không chặn `DestChainID`), khi B hoặc C thử `attestCommit()` số
tiền đó, **cả hai đều bị chặn** trừ khi B/C chính là Reserve.

Kết quả: trong toàn bộ hệ thống, **chỉ có đúng 1 bản sao "chain X còn được gửi bao nhiêu" từng
bị trừ có ý nghĩa bảo mật — bản sao của Reserve**. Các chain khác về mặt lý thuyết vẫn có field
`SupplyLedger.PerChainAllocation` cục bộ riêng (không đồng bộ với nhau thật), nhưng field đó
**không bao giờ được dùng để enforce trần cho 1 commit giá trị>0 từ chain khác** trừ khi chính
chain đó là Reserve — nên việc "không đồng bộ" giữa các bản sao không tạo ra rủi ro, vì chưa từng
có 2 bản sao nào cùng lúc có ý nghĩa bảo mật để mà lệch nhau.

Đã kiểm tra thêm 1 hướng lách khác: liệu có thể gửi giá trị thật nhưng khai `aggregateAmount=0`
để né điều kiện trên không? Không được — `aggregateAmount` bị ràng buộc mật mã học vào đúng cây
Merkle của commit (`VerifyMerkleProof`), và `ClaimMessage()` tự kiểm tra
`ClaimedAmount + message.Value <= FundedAmount` cho từng message — nếu `FundedAmount`
(=aggregateAmount) bị khai gian là 0, bất kỳ message nào bên trong có `Value>0` sẽ bị từ chối
ngay ở bước claim.

**Việc cần làm tiếp** (mục 3, bước 2): thiết kế treasury multisig — không còn bị chặn bởi câu
hỏi mở này nữa.

### 2.4 Chain "nghèo" mãi mãi nếu không ai chịu chuyển tiền cho nó — ĐÃ GIẢI QUYẾT (2026-09-04)
Lo ngại ban đầu: 1 chain hoàn toàn mới, không ai chuyển tiền cho, sẽ **mãi mãi có allocation = 0**,
dù được ĐĂNG KÝ hợp lệ — cần con người quyết định seed ban đầu qua treasury, không tự động.

**Đã giải quyết bằng cách gộp tiền cọc đăng ký = allocation lưu thông** (xem "Cập nhật 2026-09-04"
ở đầu file, ngay dưới mục 1) — mọi chain đăng ký từ nay về sau tự mang tiền cọc thật của mình vào
làm vốn lưu thông ban đầu ngay lúc đăng ký, không còn "nghèo mãi mãi", không cần chờ treasury
multisig quyết định. Treasury multisig (mục 2.2, `ProposalTransferAllocation`) vẫn còn, dùng cho
2 trường hợp còn lại: (a) chain muốn nhận THÊM ngoài phần tự cọc, (b) các chain đã đăng ký TRƯỚC
2026-09-04 mà chưa từng có allocation.

### 2.5 Tương thích ngược — code/tài liệu đang tham chiếu `ProposalAllocateSupply`
`note/dapp_cross_chain_developer_guide.md`, `note/external_security_audit_scope_p5.md`,
`cmd/tool/live_asset_bridge/main.go` đều tham chiếu cơ chế cũ — cần cập nhật đồng bộ nếu triển
khai, tránh tài liệu/tool nói sai cơ chế thật (đúng bài học "PR gì đó" tài liệu bị lag sau code
đã gặp nhiều lần trong phiên này).

## 3. Việc còn lại thật sự (đã thu hẹp sau kết luận 2026-09-04 — không còn sửa code)

Các bước điều tra (mục 2.3) và thiết kế treasury (mục 2.2) đã xong, kết quả: **cơ chế cần thiết
đã tồn tại sẵn, không cần code mới**. Danh sách việc-cần-làm cũ (7 bước, có bước "khoá/xoá
`ProposalAllocateSupply`") bị thay bằng danh sách rút gọn dưới đây — chỉ còn việc tài liệu/vận
hành:

1. ~~Điều tra mục 2.3~~ — DONE, xem "KẾT LUẬN CUỐI CÙNG".
2. ~~Thiết kế treasury multisig~~ — DONE, đã xác nhận là `GovernanceEngine` +
   `ProposalTransferAllocation` có sẵn, không cần xây thêm.
3. ~~Khoá/xoá `ProposalAllocateSupply`~~ — KHÔNG LÀM, vì đây vẫn là cơ chế bootstrap genesis duy
   nhất và đã tự khoá đủ an toàn (Reserve-only + 1 lần duy nhất, C7 fix). Xem "KẾT LUẬN CUỐI
   CÙNG".
4. ~~Viết quy trình vận hành onboarding chain mới~~ — DONE, và đơn giản hơn dự tính ban đầu: xem
   "Cập nhật (2026-09-04)" ngay dưới mục 1 — chain mới tự mang tiền cọc làm allocation ban đầu
   lúc đăng ký (1 bước), Reserve/chain đầu vẫn qua `register_chains --fund-genesis` (1 lần), và
   `ProposalTransferAllocation`/`ClaimMessage()` chỉ còn dùng cho phần bổ sung ngoài tiền cọc.
5. Cập nhật đồng bộ mọi tài liệu/tool còn tham chiếu cách hiểu cũ (mục 2.5) — chỉ cần sửa để mô
   tả đúng hiện trạng (đã an toàn), không phải vì cơ chế thay đổi.
6. Không cần đợt audit độc lập MỚI hay thay đổi code — hiện trạng đã được xác minh an toàn bằng
   phân tích code trực tiếp (C7 fix + C8-adjacent `ErrNonReserveCeilingAttestation`, mục 2.3).
   Vẫn nên đưa các đoạn code liên quan (`ExecuteGovernanceProposal` case
   `ProposalAllocateSupply`/`ProposalTransferAllocation`, `attestCommitInternal`) vào phạm vi
   audit P5 hiện có để có xác nhận độc lập, nhưng đây là việc thường quy, không phải vì nghi ngờ
   có lỗ hổng.

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

## 5. Genesis private chain gắn thẳng với Root Anchor — đã triển khai (2026-09-04)

Yêu cầu bổ sung cùng ngày: private chain phải **dùng chung native coin thật với Root Anchor**
(không tự bịa tiền ở genesis), và khác Root Anchor ở chỗ tổng cung của nó **co dãn** (bơm vào/rút
ra qua cross-chain), trong khi tổng cung Root Anchor **giữ nguyên bất biến** (không đổi gì ở
Root Anchor genesis theo đúng yêu cầu, chỉ đổi cơ chế private chain).

**Thiết kế "genesis xác định" (deterministic genesis)** — genesis của 1 private chain giờ được
**dẫn xuất từ đúng thông tin đã đăng ký công khai trên Root Anchor**, không còn tự bịa:

1. `ChainRegistry` (Go, `pkg/cross_chain/types.go`) có thêm `GenesisWallet` (ví phải giữ toàn bộ
   số dư ban đầu của chain đó) và `GenesisDigest` (keccak256 của đúng file `genesis.json`, publish
   sau khi genesis đã sinh — 2 pha, vì digest chỉ tính được SAU khi có file thật).
   `RegisterChainViaStake` **ép** `GenesisWallet = tx.FromAddress()` (ví thật đã trả cọc), không
   tin bất kỳ giá trị nào trong payload — chặn mạo danh 1 ví nổi tiếng khác.
2. `gen_single_chain.py` thêm chế độ opt-in (`--initial-supply-wallet`/`--initial-supply`): khi
   bật, **đối chiếu lại 2 giá trị này với Root Anchor qua eth_call thật trước khi tin** (dừng cứng
   nếu lệch), rồi sinh genesis với **duy nhất 1 ví có số dư — đúng bằng số đã xác minh**, mọi ví
   khác (validator, dev account, relayer devnet) đăng ký = 0 (chỉ để BLS pubkey có mặt, không có
   tiền). Gas cho hạ tầng (relayer, submitter) xử lý qua `free_fee_addresses` có sẵn, không cần
   phát tiền free.
3. `register_chains` thêm 2 hành động mới: `publish-genesis-digest` (người đã đăng ký publish
   digest thật, đúng 1 lần, gate bằng `GenesisWallet`) và `verify-genesis` (bất kỳ ai tự tính lại
   digest file cục bộ, đối chiếu với bản Root Anchor đã ghi — khớp mới an toàn chạy node). Bỏ luôn
   bước gửi `registerChainViaStake` lần 2 lên chính private chain mới (không còn khả thi — chain
   đó chưa có tiền để trả cọc lần nữa trên chính nó — và cũng không cần nữa, vì `ChainRegistry`
   của chính nó giờ nằm thẳng trong genesis).

**Kết quả**: tổng cung native coin của 1 private chain tại genesis luôn **chứng minh được** bằng
đúng số Root Anchor đã cấp phát thật (qua cọc thật, xem mục "Cập nhật 2026-09-04" ở đầu file) —
không còn khoảng trống "mỗi lần chạy `gen_single_chain.py` là tự in tiền free" đã phát hiện cùng
ngày. Sau genesis, tổng cung co dãn hoàn toàn tự nhiên qua `Outbound()`/`ClaimMessage()` đã có sẵn
(đốt khi gửi ra, cộng khi nhận vào) — không cần cơ chế mới. Root Anchor's genesis + `alloc` riêng
của chính nó **không đổi gì** — đúng phạm vi yêu cầu.

Đã smoke-test: mode cũ (không truyền 2 flag mới) chạy y hệt hành vi trước đây (không hồi quy); mode
mới verify đúng số thật từ 1 Root Anchor giả lập, sinh genesis đúng 1 ví có số dư = số đã xác minh,
và fail cứng khi số truyền vào lệch với Root Anchor. `go build`/`go vet`/`go test ./...` toàn bộ
module Go xanh.
