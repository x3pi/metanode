# Kế hoạch: Đồng Tiền Thống Nhất Kiểu "Eurozone" — Giữ Nguyên Chủ Quyền Chain

Viết 2026-08-27, cập nhật 2026-09-04. **Thay thế khuyến nghị của
`note/unified_settlement_layer_migration_plan.md`** (mô hình "Root Anchor = L1, private chain
mất chủ quyền coin") — tài liệu đó vẫn giữ nguyên làm tham khảo lịch sử, nhưng **không còn là
hướng khuyến nghị**. Hướng này nhẹ hơn nhiều, dựa gần như hoàn toàn vào cơ chế đã có sẵn trong
code, không cần bỏ chủ quyền private chain.

## CẬP NHẬT (2026-09-04, phiên sau) — C6 (Sybil-vote mua governance qua stake) ĐÃ ĐÓNG

Mục 2.6 dưới đây ghi C6 là "PHÁT HIỆN THẬT, CÒN MỞ" — đã đóng trong phiên làm việc sau, theo
quyết định trực tiếp của người dùng: **"ý tôi bỏ hoàn toàn vote này có thể sẽ an toàn hơn đó vì
không có ai thao túng vote cả"**. Không chọn bất kỳ hướng nào trong 3 hướng mục 2.6 từng liệt kê
(tách quyền vote riêng / vote theo trọng số stake / nâng sàn kinh tế) — thay vào đó **xoá hẳn
`GovernanceEngine`** (toàn bộ propose/vote/quorum(≥2/3 active chains)/timelock 72h/execute), vì
không còn vote nào để mà thao túng nữa.

**Thiết kế thay thế** (đã triển khai + security-review + live test xanh 45/45 package,
commit `95cb5fd7`):
- Hành động chỉ ảnh hưởng tài nguyên CỦA CHÍNH actor (chuyển allocation của mình, mint supply
  genesis của mình, đăng ký asset của mình qua `HomeChainID`) → **tự ký (self-signed) bằng đúng
  uỷ ban BLS thật đang sống trên chain đó** — không vote, đúng yêu cầu "chuyển tiền cần mỗi chữ
  ký là được rồi không cần vote". Hàm mới: `AllocateSupplyWithCert`, `TransferAllocationWithCert`.
- Hành động ảnh hưởng BÊN THỨ BA không tự ký được (đổi uỷ ban 1 chain khác, tuyên bố chain khác
  chết, huỷ đăng ký chain khác) → **`RecoveryCommittee`** — 1 uỷ ban nhỏ, CỐ ĐỊNH, cấu hình 1 lần
  qua config (`cross_chain.recovery_committee_json`), KHÔNG lớn lên theo `RegisterChainViaStake`
  (nên không Sybil được) và KHÔNG chỉ nằm ở Reserve (tránh tập trung quyền lực lại, đúng kết luận
  mục 2.2 cũ). Hàm mới: `DeclareChainDeadWithCert`, `UnregisterChainWithCert`,
  `UpdateCommitteeWithRecoveryCert`. Nếu `recovery_committee_json` để trống, cả 3 hàm này LUÔN từ
  chối (`VerifyQuorumCertAgainstRegistry` fail-closed trên committee rỗng) — "cứu hộ" không tự
  bật, phải cấu hình tường minh.

**Hệ quả cho các mục dưới đây**: mục 2.2 (mô tả `ProposalTransferAllocation` qua
`GovernanceEngine.Propose/Vote/Execute` là "multisig ≥2/3 founding chains") **đã LỖI THỜI** —
`TransferAllocationWithCert` giờ chỉ cần đúng chain nguồn tự ký, không còn multisig liên-chain
nữa (đổi hướng theo yêu cầu người dùng "mỗi chữ ký là được"). Mục 2.6 giữ nguyên làm nhật ký điều
tra rủi ro gốc; xem đoạn này để biết hướng xử lý cuối cùng KHÁC với 3 hướng mục 2.6 từng đề xuất.

**Rà soát bảo mật khi xoá `GovernanceEngine`**: `GovernanceProposal.Executed` từng là chốt chống
replay/double-execute duy nhất cho toàn bộ hệ thống propose/vote/execute — xoá nó mà không thay
thế đã tạo ra 2 lỗ hổng thật, phát hiện + vá + verify thực nghiệm (tắt fix, xác nhận đúng lỗi dự
đoán, bật lại, xác nhận test xanh) ngay trong phiên này trước khi commit:
1. `TransferAllocationWithCert` không có gì chặn replay 1 cert hợp lệ (public) → rút cạn
   allocation lặp lại nhiều lần. Vá bằng nonce theo từng `fromChainID`.
2. `UpdateCommitteeWithRecoveryCert` không kiểm tra epoch đơn điệu → replay 1 cert cứu hộ CŨ có
   thể rollback uỷ ban/epoch của chain về trạng thái cũ, kể cả sau khi chain đã tiến hợp lệ qua
   nhiều epoch mới — chiếm quyền uỷ ban. Vá bằng bắt buộc `NewEpoch > epoch hiện tại`.

## CẬP NHẬT (2026-09-04, live-verify qua run_full_pipeline.sh) — bug bootstrap thật, đã vá

Sau khi xoá `GovernanceEngine` (đoạn trên), chạy `run_full_pipeline.sh` thật trên cụm 4-validator
Root Anchor (chain 991) để live-verify thì phát hiện: **Reserve (991) không bao giờ tự đăng ký
được vào ChainRegistry của chính nó** — `RegisterChainViaStake` khi Reserve đăng ký CHÍNH MÌNH gọi
nội bộ `TransferAllocation(991, 991, amount)` (same-chain), bị `TransferAllocation` từ chối thẳng
(`ErrSameChainTransfer`), lỗi này KHÔNG được nuốt (chỉ `ErrInsufficientAllocation` được nuốt) →
toàn bộ self-registration fail. Hệ quả: 1 Root Anchor mới triển khai KHÔNG BAO GIỜ mint được
genesis supply (`AllocateSupplyWithCert`/`TransferAllocationWithCert` đều tự-ký-only, cần
`ChainRegistry[991]` tồn tại trước). Đã vá (commit `ae42cd63`): bỏ qua bước credit khi
`reg.ChainID == g.ReserveChainID` (same-chain credit vốn là no-op) — tiền cọc thật vẫn bị trừ ví
người gọi như mọi chain khác, chỉ không cộng vô allocation vô nghĩa. Test hồi quy verify thực
nghiệm cả lỗi lẫn fix (`TestGateway_RegisterChainViaStake_CreditsStakeIntoAllocation/"Reserve
registering ITSELF..."`).

**Đã hoàn tất tiếp (cùng ngày)**: nối dây tự động — `deploy_private_chains.sh` giờ tự sinh entry
chain 991 (uỷ ban BLS thật của Root Anchor, đối chiếu địa chỉ với genesis sống để loại bỏ khoá cũ/
rác) và `deploy/ansible/roles/local_build/tasks/main.yml` tự sinh + tiêm `recovery_committee_json`
vào mọi node (dùng lại chính uỷ ban validator thật của Root Anchor làm RecoveryCommittee) — xem
commit `54869f43`, `017374e8`. Redeploy live xác nhận cả 2: self-registration + mint genesis supply
thành công thật (không còn "skip"), và cả 3 hàm RecoveryCommittee-authorized
(`updateCommitteeWithRecoveryCert`/`declareChainDeadWithCert`/`unregisterChainWithCert`) chạy thật
trên 1 chain thử nghiệm dùng riêng, có đọc lại state qua `eth_call` xác nhận thay đổi thật (đổi
uỷ ban + epoch, xoá đăng ký). **Toàn bộ 6/6 hàm cert-based mới đều đã live-verify, không chỉ unit
test.**

**Phát hiện vận hành đáng lưu ý (tránh nhầm lẫn sau này)**: khoá BLS validator của Root Anchor
KHÔNG ổn định qua các lần redeploy `Action: setup` — `gen_validator_entry.py` gọi thẳng
`metanode keytool generate validator` mỗi lần chạy, không kiểm tra khoá cũ đã có để tái dùng, nên
MỖI lần setup lại đều sinh khoá HOÀN TOÀN MỚI cho cả 4 validator. Từng nhầm là khoá ổn định (dựa
trên mtime file cũ trùng hợp từ 28/8) — sai, dẫn tới 1 lần chạy live-test dùng nhầm khoá cũ, cert
bị revert vì sai chữ ký (không phải lỗi logic, chỉ là khoá test đã lỗi thời qua lần redeploy kế
tiếp). Bài học: sau MỖI lần `run_full_pipeline.sh`/`setup_root_anchor.sh --clean`, phải đọc lại
`deploy/systemd/node-N_keys/authority_key.json` MỚI trước khi ký bất kỳ cert nào bằng tay, không
được cache khoá từ lần chạy trước.

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

### 2.6 Sybil đăng ký chain để MUA phiếu vote governance qua stake — PHÁT HIỆN THẬT, CÒN MỞ
### (2026-09-04, cùng ngày — khác với rủi ro mục 2.1)

Khác với mục 2.1 (giả định đăng ký vẫn qua vote-gate), `RegisterChainViaStake` (đường đăng ký
KHÔNG qua vote, cố ý theo yêu cầu người dùng 2026-08-28) gọi thẳng
`Governance.RegisterActiveChain(chainID)` (`gateway.go`) — cho chain mới **toàn quyền vote
governance ngay lập tức**, không qua bất kỳ vote-gate nào. Governance là **1-chain-1-vote không
trọng số theo stake** (`QuorumThreshold = ceil(2N/3)` trên SỐ LƯỢNG chain, không phải theo tiền).

**Live-verify trên cụm thật**: `MinNativeStakeToRegister = 1 MTN` (xác nhận qua `eth_call`), 4
chain active lúc đó → quorum = 3. Với ~10-20 MTN (rẻ), kẻ tấn công tự đăng ký đủ Sybil chain để
MỘT MÌNH đạt quorum, thao túng toàn bộ governance (`ProposalUpdateCommittee` — đổi uỷ ban BFT của
BẤT KỲ chain nào khác; `ProposalTransferAllocation` — chuyển allocation đi; tuyên bố chain chết...)
mà không cần ai đồng ý.

**Quyết định của người dùng (2026-09-04)**: KHÔNG thêm lại vote-gate cho voting rights — việc bỏ
toàn bộ logic vote liên quan tới private chain là chủ ý, không phải sơ suất cần vá. Thay vào đó,
người dùng chuyển hướng sang tách "tiền ví" và "tiền lưu thông": `RegisterChainViaStake` giờ nhận
`amount` do người gửi tự chọn (>= `MinNativeStakeToRegister` làm sàn), thay vì luôn lấy đúng 1 mức
cố định — xem mục "5.4" bên dưới. **Rủi ro Sybil-vote ở mục này CHƯA ĐƯỢC ĐÓNG** — amount tự chọn
không nâng chi phí sàn để mua 1 phiếu vote (kẻ tấn công vẫn có thể chọn amount = đúng sàn cho mỗi
Sybil), đây là 1 tính năng khác (định cỡ kinh tế linh hoạt lúc đăng ký), không phải fix cho rủi ro
này. Cần quyết định riêng sau nếu muốn đóng: ví dụ tách quyền vote khỏi `RegisterChainViaStake`
(cần 1 proposal riêng, vẫn vote-gate, để CHUYỂN từ "đã đăng ký kinh tế" sang "có quyền vote"), hoặc
chuyển governance sang trọng số theo stake thay vì 1-chain-1-vote, hoặc nâng sàn đủ cao để Sybil
kinh tế không khả thi — cả 3 đều là thay đổi chính sách lớn, chưa làm.

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
module Go xanh. Đã đóng gói thành script tái dùng được: `deploy/systemd/test_deterministic_genesis.sh`.

### 5.1 `deploy_private_chains.sh` khớp thiết kế mới (2026-09-04, cùng ngày)

Thêm cờ opt-in `--deterministic-genesis` (tự bật `--register`). Vấn đề cốt lõi: pipeline cũ sinh
genesis TRƯỚC khi đăng ký (ansible chạy `gen_single_chain.py` ngay trong bước `--setup`), ngược thứ
tự cần thiết cho thiết kế mới (phải biết số tiền thật đã đăng ký TRƯỚC khi sinh genesis). Giải
quyết bằng tách làm 2 pha, không cần sửa `deploy.yml`:

- **Pha 1** (`ansible-playbook ... --limit localhost`): chỉ chạy Play sinh key + cấu hình cục bộ,
  KHÔNG đụng tới node thật/service.
- **Đăng ký + sinh lại genesis**: `register_chains --config` đăng ký tất cả chain lên Root Anchor
  **và tiếp tục gửi lại y hệt lên chính từng private chain's RPC như cũ** (ĐÃ THỬ bỏ bước này lúc
  thiết kế, hiểu nhầm là dư thừa — thực ra đây là bước ĐỒNG BỘ DANH BẠ CHÉO giữa các private chain
  với nhau, không phải tự đăng ký: thiếu nó thì chain 102 không biết committee của chain 101 trong
  `ChainRegistry` cục bộ của chính nó, relayer sẽ revert "unknown source chain ID" ngay khi thử
  relay thật — phát hiện bằng cách chạy `run_full_pipeline.sh` thật, không phải suy luận, xem mục
  5.2 bên dưới) → với từng chain, đọc lại đúng số đã cấp (`query-alloc-raw`)+ví
  (`query-genesis-wallet-raw`) → gọi lại `gen_single_chain.py` với
  `--initial-supply-wallet`/`--initial-supply` (key giữ nguyên nhờ fix idempotent bên dưới) →
  `publish-genesis-digest` + `verify-genesis` (fail cứng nếu lệch, KHÔNG đẩy lên node thật khi
  chưa xác minh).
- **Pha 2** (ansible-playbook đầy đủ, `deploy_action=setup` — cố ý KHÔNG dùng lại `reset` dù người
  dùng gọi `--reset-all`, xem comment tại chỗ gọi trong script để biết lý do + đánh đổi đã biết):
  đẩy genesis đã xác minh lên node thật + khởi động.

**Fix nền tảng bắt buộc phải có trước**: `gen_single_chain.py`'s `generate_validator_keys()` giờ
**idempotent** — nếu key đã tồn tại (từ Pha 1) thì đọc lại từ đĩa thay vì gọi `metanode keytool
generate` lần nữa (trước đây luôn sinh key MỚI mỗi lần gọi — gọi 2 lần sẽ ra 2 committee khác nhau,
phá vỡ liên kết với committee đã đăng ký). Lưu thêm `_gen_meta.json` (metadata riêng của script,
không phải file node đọc) để khôi phục đúng pubkey gốc của `protocol_key`/`network_key` sau khi
2 file đó đã bị ghi đè thành định dạng base64-kết-hợp (priv+pub) mà node thật cần.

**Lỗi tự tìm ra và tự sửa trước khi commit** (không phải qua live-test đa máy, mà qua đọc kỹ
`deploy.yml`): gọi lại ansible-playbook Pha 2 với `deploy_action=reset` (y hệt hành động gốc) sẽ
khiến Play 1 chạy lại bước `rm -rf $LOCAL_OUT`, xoá sạch genesis vừa đăng ký+xác minh, sinh
committee ngẫu nhiên MỚI không khớp gì với Root Anchor. Đã sửa: Pha 2 luôn dùng `setup`.

**Đã kiểm chứng**: `go build`/`go vet`/`go test ./...` xanh (thêm `register_chains/main_test.go` —
test mới cho `query-alloc-raw`/`query-genesis-wallet-raw`, trước đây pkg này chưa có test nào);
đọc kỹ logic 2 pha bằng cách trích xuất đúng khối Python nhúng trong bash ra chạy dry-run với
`subprocess.run` giả lập, xác nhận đúng thứ tự lệnh cho nhiều chain.

### 5.2 Live-test thật trên cụm m0 (2026-09-04, cùng ngày) — chạy `run_full_pipeline.sh` thật,
### tìm ra và sửa 2 lỗi thật (không phải giả định)

Người dùng yêu cầu "đảm bảo quá trình này hoạt động không lỗi" → chạy thật `run_full_pipeline.sh`
(cả 6 bước, không skip) trên cụm thật (Root Anchor 4 node + private chain 101/102), 2 lượt:

**Lượt 1 — phát hiện lỗi #1 (bootstrap ordering)**: Bước 2/6 (`deploy_private_chains.sh
--reset-all`, KHÔNG bật `--deterministic-genesis` — dùng đường mặc định) fail ngay ở
`registerChainViaStake(chain 101)`:
```
Reserve's allocation pool cannot cover the stake amount: source chain has insufficient
allocation: chain 991 available 0, requested 1000000000000000000
```
Nguyên nhân: bản sửa bảo mật (TransferAllocation thay GrantAllocation, mục "Cập nhật 2026-09-04")
khiến việc đăng ký FAIL HẲN nếu Reserve chưa có pool — đúng khi Reserve ĐÃ có tiền, nhưng lúc mới
khởi tạo hệ thống, Reserve luôn = 0 (chưa mint `GenesisTotalSupply`), mà mint đó (`fundGenesis`)
lại cần quorum từ các chain ĐANG active — mà pipeline cũ đăng ký chain 101/102 TRƯỚC rồi mới dùng
chính chúng để vote mint. Chặn đăng ký khi Reserve=0 tạo vòng lặp: đăng ký cần mint, mint cần
người vote đã đăng ký. **Đã sửa** (`d1762c37`): bước cấp tiền giờ "cố gắng tốt nhất" — thiếu pool
(`ErrInsufficientAllocation`) chỉ bỏ qua bước cấp tiền, KHÔNG chặn đăng ký (đăng ký xong, allocation
tạm = 0, sẽ được `fundGenesis`'s `ProposalTransferAllocation` cấp bù sau — đúng cơ chế cũ, không
đổi gì). Chạy lại: cả 2 chain đăng ký thành công, allocation của 101/102 sau đó đến từ đúng đường
`fundGenesis` vote như trước (bootstrap lần đầu) — không có gì bị in ra từ hư không.

**Lượt 2 — phát hiện lỗi #2 (bỏ nhầm bước đồng bộ danh bạ chéo)**: Bước 2 qua hết, Bước 5/6 (test
Cross-Chain thật) timeout 60s chờ relayer. `relayer.log` cho thấy nguyên nhân thật:
```
attestCommit for chain 101 asset 0 reverted: unknown source chain ID: chain 101
```
Nguyên nhân: bản sửa trước đó (`7fb2189d`) đã BỎ bước gửi `registerChainViaStake` lần 2 lên chính
RPC của từng private chain, với lý do (SAI) là "chỉ để tự đăng ký, dư thừa vì giờ genesis đã có
sẵn ChainRegistry". Thực tế bước đó dùng để ĐĂNG KÝ CHÉO — cho chain 102 biết committee của chain
101 (và ngược lại) trong chính `ChainRegistry` cục bộ của chain 102, thứ mà `attestCommit`/
`attestReserveIssuedCommit` tra cứu tại chỗ đang xử lý, không phải bản ghi trên Root Anchor.
`GatewayRegistryMonitor` có tồn tại để tự đồng bộ dần việc này, nhưng không đủ nhanh/đủ tin cậy để
thay thế bước đồng bộ đồng bộ (synchronous) này ngay lúc mới deploy. **Đã khôi phục** bước gửi lần
2 (không phải revert toàn bộ, giữ nguyên toàn bộ phần `GenesisWallet`/deterministic-genesis khác).

**Bài học chung**: cả 2 lỗi đều là loại "chỉ lộ ra khi chạy thật trên cụm thật", đúng lý do ban đầu
tại sao user yêu cầu chạy `run_full_pipeline.sh` thay vì chỉ tin code review/unit test. Sau khi sửa
lỗi #2 (đang chờ chạy lại lượt 3 để xác nhận toàn bộ 6 bước xanh).

**Lượt 3 — xanh toàn bộ, phát hiện lỗi #3 (đào sâu theo yêu cầu "tiếp tục đào sâu vấn đề relayer
luôn đi")**: lượt 3 pass 6/6 bước, nhưng lượt 2 (trước khi sửa lỗi #2 xong hẳn) từng timeout 60s ở
lượt thử 2/3 của Bước 5 — đào sâu tìm ra nguyên nhân thật khác lỗi #2: `cross_chain_relayer` không
có key riêng, fallback dùng CHUNG `submitter_key` với `register_chains` — cả 2 tiến trình tự track
nonce độc lập cho CÙNG một địa chỉ, đua nhau khi relayer khởi động chỉ ~3s sau khi `register_chains`
xong, làm 1 tx của relayer bị "mồ côi" nonce vĩnh viễn (`eth_getTransactionReceipt` trả `null`,
không có gap nonce — chứng tỏ tx KHÁC đã chiếm đúng slot đó). **Đã sửa** (`dab2f3fb`): cho relayer
danh tính riêng (`devnetDefaultRelayerKeyHex`, cùng địa chỉ devnet đã có sẵn BLS/gas-exempt từ
trước), bỏ hẳn fallback dùng `submitter_key`. Xác nhận lại bằng lượt chạy đầy đủ tiếp theo: xanh
toàn bộ 9/9 lượt cross-chain (kể cả lượt 2/3 từng fail), 102/102 Block-STM.

### 5.3 Lỗ hổng kế toán relay 2-hop A→Reserve→B: credit phía đích không tới được ledger có thẩm
### quyền (2026-09-04, cùng ngày — phát hiện khi user yêu cầu "xem kỹ có vấn đề bảo mật gì không")

Sau khi lượt 3 xanh toàn bộ, kiểm tra bất biến `Σ PerChainAllocation == GenesisTotalSupply` trên
chính ledger của Reserve (qua `eth_call getAllocation()` thật, không suy đoán) phát hiện **thiếu
đúng 2100 MTN** — khớp chính xác tổng giá trị đã relay thật 101→102 trong Bước 5 (500×3 + 200×3).
Chain 101 bị trừ đúng, chain 102 KHÔNG được cộng tương ứng trên bản ghi có thẩm quyền của Reserve.

**Nguyên nhân** (xác nhận qua đọc code, không suy đoán): relay 2-hop A→Reserve→B gồm 3 lệnh gọi
riêng biệt, trên 3 "node"/process riêng biệt (`relayer_daemon.go:494-533`):
1. `attestCommit` gửi tới node Reserve → trừ `PerChainAllocation[101]` trên ledger CỦA RESERVE.
2. `attestReserveIssuedCommit` gửi tới node chain 102 → không đụng `SupplyLedger` (đúng thiết kế,
   xem doc comment `AttestReserveIssuedCommit`).
3. `claimMessage` gửi tới node chain 102 → cộng `PerChainAllocation[102]` — nhưng đây là bản
   `SupplyLedger` CỤC BỘ CỦA NODE CHAIN 102 (`g.LocalChainID=102`), một struct hoàn toàn tách biệt
   với bản của Reserve. Đúng như mục 2.3 đã kết luận trước đó: "chỉ có đúng 1 bản sao có ý nghĩa
   bảo mật" (bản của Reserve) — nhưng bước cộng tiền lại ghi vào bản KHÔNG có thẩm quyền.

Trừ đúng chỗ (Reserve), cộng sai chỗ (bản cục bộ của B) → tổng trên Reserve co lại vĩnh viễn sau
mỗi lần relay 2-hop. Không mất tiền thật của người dùng (B vẫn nhận đủ tiền thật), nhưng là rủi ro
tự-DoS/kẹt quỹ tích lũy dần: allocation của B trên Reserve bị ghi nhận thấp hơn thực tế, nên sau
này B tự gửi tiền đi (tự nó gọi `AttestCommit`) có thể bị `ErrAllocationExceeded` từ chối dù B thực
sự có đủ tiền.

**Đã sửa**: thêm `GatewayEngine.CreditReserveAllocation` (gateway.go) — chặng thứ 3, đối xứng với
`AttestCommit`'s debit, gọi trực tiếp vào node Reserve (tự chặn nếu gọi sai node, giống C8),
idempotent (write-once theo `MessageID` qua `ReserveCreditedMessages`), verify cùng Merkle proof
với `ClaimMessage`. `relayer_daemon.go`'s `RelayBatch` gọi endpoint mới này ngay sau khi
`claimMessage` thành công trên đích, mỗi khi đích khác Reserve — best-effort (lỗi ở bước này không
rollback tiền đã claim thật, chỉ risk ceiling false-reject sau này, tự sửa được bằng cách gọi lại
vì idempotent). ABI mới `creditReserveAllocation` (`gatewayAbi.go`) + case dispatch mới
(`gateway_handler.go`).

**Verify thật trên cụm m0** (không chỉ unit test): rebuild+redeploy cả 4 node Root Anchor
(`ansible_deploy.sh --start --fast`) + relayer, chạy lại `01-client-only-transfer -amount 500`
(101→102). Log relayer: `💰 credited chain 102's allocation on Reserve for message 0x...`. Số liệu
`eth_call` trước/sau khớp chính xác: chain 101 giảm đúng 500 MTN, chain 102 (trên ledger Reserve,
KHÔNG phải bản cục bộ) tăng đúng 500 MTN — bất biến giữ đúng cho giao dịch mới. Khoảng thiếu 2100
MTN cũ (từ trước khi sửa, 9 message Bước 5's lượt trước) vẫn còn tồn đọng — các message đó đã được
relayer đánh dấu "processed" nên không tự retry; đây là devnet nên không backfill, nhưng một hệ
thống thật cần công cụ đối soát/backfill riêng nếu gặp tình huống tương tự trên dữ liệu đã tồn tại
trước bản vá. Unit test mới: `TestGateway_CreditReserveAllocation_2HopDestCredit`
(`gateway_test.go`) — cộng đúng chain đích (không phụ thuộc claim cục bộ), idempotent, chặn sai
node, chặn Merkle proof sai.

### 5.4 `registerChainViaStake` — amount do người gửi tự chọn (2026-09-04, cùng ngày, sau mục 2.6)

Theo yêu cầu người dùng khi thảo luận rủi ro Sybil-vote (mục 2.6): tách "tiền ví" khỏi "tiền lưu
thông", cho người đăng ký TỰ CHỌN số tiền đưa vào lưu thông (>= `MinNativeStakeToRegister` làm
sàn), thay vì luôn lấy đúng 1 mức cố định như trước. `GatewayEngine.RegisterChainViaStake` nhận
thêm tham số `amount`; `MinNativeStakeToRegister` giờ chỉ còn là SÀN (vẫn là chặn Sybil-spam C6),
không còn là số tiền credit cố định. ABI `registerChainViaStake` thêm tham số `amount uint256`;
`register_chains` có field config mới `stake_amount` (per-chain, để trống thì tự query
`getMinNativeStakeToRegister()` làm mặc định — hành vi cũ nguyên vẹn nếu không set).

**Nhắc lại rõ (đã ghi ở mục 2.6)**: tính năng này KHÔNG đóng rủi ro Sybil-vote — chỉ là tính năng
định cỡ kinh tế linh hoạt, tách biệt khỏi câu hỏi "đăng ký có nên tự động có quyền vote không".

**Verify thật trên cụm m0**: đăng ký chain 9202 với `stake_amount=5 MTN` (> sàn 1 MTN) → credit
đúng 5 MTN (không phải 1 MTN sàn). Đăng ký chain 9203 với `stake_amount=0.5 MTN` (< sàn) → revert
sạch, log `amount 500000000000000000 is below the minimum stake 1000000000000000000`, allocation
vẫn = 0 (không có state một phần nào bị ghi).

**Phát hiện phụ, KHÔNG liên quan tới thay đổi này** (tình cờ gặp lúc rebuild+redeploy Root Anchor
lần 2 để verify): restart cả 4 node Root Anchor ngay sau khi cụm đã tích luỹ ~9300 commit (từ đợt
test Bước 5/6 nặng trước đó) khiến node-1/2/3 (không phải node-0) panic 100% xác định ở tầng Rust
consensus-core, tại `leader_scoring.rs:198`
(`self.commit_range.clone().expect("CommitRange should be set...")`). Root cause đầy đủ: nhánh
"SCHEDULE-RECOVERY" trong `commit_manager.rs` (bug tiềm ẩn từ commit tháng 5/2026 "fork-safe subdag
scoring for schedule recovery", lần đầu bị kích hoạt hôm nay) gọi `update_leader_schedule_v2()`
ngay cả khi `scoring_subdag` HOÀN TOÀN RỖNG (đúng nghĩa đen của "DAG lacks 300 commits") — lúc đó
`ScoringSubdag.commit_range` vẫn là `None` (chỉ được set bên trong `add_subdags()`, không được gọi
nếu rỗng), khiến `.expect()` panic thay vì trả về "0 subdags, chưa có gì để tính điểm". Có ít nhất
2 điểm `.expect()` giống hệt nhau sẽ panic (`leader_scoring.rs:198` và
`dag_state/write.rs:660`'s `scoring_subdag_commit_range()`) — sửa 1 chỗ chưa chắc đủ, cần sửa cả
2. Cụm được khôi phục lần đầu bằng cách chạy lại `run_full_pipeline.sh` từ genesis mới (đường phục
hồi đã biết chắc chắn hoạt động), không phải bằng cách sửa bug Rust này.

**ĐÃ SỬA (2026-09-04, cùng ngày, sau khi người dùng xác nhận "sửa ngay")**: cả 2 điểm `.expect()`
được sửa ở tầng wrapper `DagState` (`dag_state/write.rs`) — nơi DUY NHẤT mọi caller thật sự đi qua
(`update_leader_schedule_v2()` luôn gọi qua wrapper, không bao giờ gọi thẳng
`ScoringSubdag::calculate_distributed_vote_scores()`, đã kiểm tra từng call site để xác nhận). Khi
`commit_range` là `None`, cả 2 hàm giờ trả về giá trị suy biến (all-zero scores, neo tại
`last_commit_index()` hiện tại) thay vì panic — an toàn theo đúng lý luận comment gốc tại
`commit_manager.rs`: nhánh sparse-DAG fallback CHỦ Ý không bao giờ gọi `schedule_verified()` hay
gỡ cờ `is_schedule_recovery_pending()`, nên các điểm số suy biến này KHÔNG BAO GIỜ được dùng để ra
quyết định bầu leader thật — bộ đếm giao dịch của chính node đó vẫn bị chặn đề xuất block cho tới
khi có dữ liệu phục hồi thật (baseline scores hoặc đủ 300 commit tự nhiên).

Test hồi quy mới (`dag_state/tests.rs`):
`test_calculate_scoring_subdag_scores_on_genuinely_empty_subdag_does_not_panic` — dựng 1
`DagState` mới hoàn toàn sạch (đúng điều kiện live đã gặp, không phải giả lập), xác nhận không
panic + giá trị trả về hợp lý. Đã verify test này THẬT SỰ bắt được bug: dùng `git stash` bỏ riêng
fix ở `write.rs` → test fail đúng với message panic y hệt log thật đã bắt được
(`CommitRange should be set if calculate_scores is called.` tại `leader_scoring.rs:198`) → `git
stash pop` khôi phục fix, test pass lại.

Verify đầy đủ: toàn bộ test suite `consensus-core` (193 test, build cả debug lẫn full workspace)
pass, 0 regression. Build release đầy đủ (`ansible_deploy.sh --start`, không dùng `--fast` vì đụng
code Rust) + redeploy cả 4 node Root Anchor thật — xác nhận **cả 8/8 cổng P2P** (19200-19203,
9100-9103) đều lên (trước đây chỉ có 2/8 của node-0), 0 panic mới trong log, log
"STARTUP-SYNC Proposals UNLOCKED" xuất hiện trên tất cả các node — cụm hoàn toàn khỏe mạnh sau
restart, đúng kịch bản trước đây từng crash-loop vĩnh viễn.
