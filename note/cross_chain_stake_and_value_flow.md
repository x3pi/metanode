# Cọc đăng ký (Stake) và Luân chuyển Coin Cross-Chain — Tài liệu Tham chiếu

Viết 2026-08-28, cùng lúc với PR thêm `RegisterChainViaStake` (đăng ký chain không cần vote).
Mục đích: trả lời dứt điểm 3 câu hỏi — **cọc dùng để làm gì**, **tiền luân chuyển giữa các chain
như thế nào** (từ lúc chưa có đồng nào tới lúc 1 giao dịch cross-chain thật hoàn tất), và **vì
sao thiết kế này an toàn** — dựa trên đọc trực tiếp code, không suy đoán. Không lặp lại nội dung
`cross_chain_attack_scenario_catalog.md` (danh mục tấn công/trạng thái vá) — tài liệu này là bức
tranh cơ chế, tài liệu kia là bức tranh phòng thủ.

---

## 1. Cọc (`MinRegistrationStake`) dùng để làm gì

**Vấn đề gốc (C6)**: trước khi có cọc, đăng ký 1 chain mới vào `ChainRegistry` hoàn toàn tách
rời khỏi coin — chain đăng ký xong có `PerChainAllocation = 0`, không mất gì để có ngay 1 phiếu
governance ngang hàng các chain khác. Một nhóm đủ kiên nhẫn có thể đăng ký nhiều chain "rỗng"
qua nhiều lần riêng lẻ, dần dần thao túng các quyết định KHÔNG liên quan tiền (đổi uỷ ban chain
khác, tham số hệ thống...).

**Cọc giải quyết bằng cách**: bắt buộc 1 chain phải **đã thực sự nắm giữ** 1 lượng coin tối
thiểu (`PerChainAllocation[chainID] >= MinRegistrationStake`) *trước khi* được ghi vào
`ChainRegistry` — biến "1 phiếu governance" từ miễn phí thành có giá thật.

**2 đường dùng cọc, khác nhau ở việc có cần vote hay không**:

| Đường | Hàm | Cần vote? | Khi nào dùng |
|---|---|---|---|
| A | `ExecuteGovernanceProposal` case `ProposalRegisterChain` | **Có** — vẫn phải qua `propose→vote(≥2/3)→timelock→execute`, cọc chỉ là điều kiện CỘNG THÊM | Khi muốn cả cộng đồng chain hiện tại đồng thuận rõ ràng việc admit 1 chain mới, không chỉ dựa vào cọc |
| B | `RegisterChainViaStake` (`registerChainViaStake()`, PR mới) | **Không** — chỉ cần đủ cọc là ghi thẳng vào `ChainRegistry`, không có bước propose/vote/timelock nào | Khi cọc đã đủ mạnh để tự nó là bằng chứng đủ tin cậy, muốn đăng ký nhanh không chờ 72h timelock |

Cả 2 đường đều **vẫn giữ nguyên** 2 điều kiện mật mã bắt buộc: PoP thật cho mọi validator trong
uỷ ban (`PopVerify`), và `QuorumThreshold` hợp lệ (≥2/3 BFT). Cọc/vote chỉ là lớp điều kiện
**thêm vào**, không thay thế 2 điều kiện mật mã này ở bất kỳ đường nào.

Mặc định (`MinRegistrationStake` chưa cấu hình, giá trị 0): chỉ có Đường A tồn tại, hành vi y hệt
trước khi có cọc — đây là lựa chọn opt-in, không phải mặc định bật.

---

## 2. Tiền luân chuyển giữa các chain như thế nào — toàn bộ vòng đời

### Bước 0 — Điểm khởi đầu: không có đồng nào

`GenesisTotalSupply = 0`, `PerChainAllocation` rỗng khi hệ thống mới khởi tạo (`loadGatewayEngine`
trong `gateway_handler.go` luôn dựng ledger rỗng cho lần ghi đầu tiên). Không chain nào, kể cả
Root Anchor, có coin sẵn.

### Bước 1 — Mint đúng 1 lần, chỉ cho Reserve

`ProposalAllocateSupply` (kind=5) — bắt buộc qua vote đầy đủ, và bị khoá cứng:
- Chỉ chain đúng bằng `ReserveChainID` đã cấu hình mới nhận được (`ErrOnlyReserveMayMint`).
- Chỉ chạy được **1 lần duy nhất** cho toàn hệ thống (`ErrGenesisAlreadyMinted` — kiểm tra
  `GenesisTotalSupply.Sign() == 0` trước khi cho phép).

Đây là **con đường DUY NHẤT** trong toàn bộ hệ thống thực sự tạo ra coin mới (tăng
`GenesisTotalSupply`). Mọi bước sau đây chỉ **di chuyển** coin đã có, không bao giờ tạo thêm.

### Bước 2 — Reserve phân phối cho các chain khác

`ProposalTransferAllocation` (kind=6) — cũng bắt buộc qua vote đầy đủ (`≥2/3` chain đang hoạt
động). Di chuyển `PerChainAllocation[from] → PerChainAllocation[to]`, tổng không đổi
(`sum(PerChainAllocation) == GenesisTotalSupply` — `VerifyInvariant()` kiểm tra sau MỌI thao tác,
`panic()` nếu sai lệch — xem mục 3).

Đây chính là bước **cấp cọc** cho 1 chain muốn đăng ký (mục 1) — chain ứng viên nhận allocation
qua bước này trước, rồi mới dùng nó để đăng ký (Đường A hoặc B ở mục 1).

### Bước 3 — Gửi giá trị cross-chain thật, đúng như code THẬT đang chạy (không phải lý thuyết)

**Cập nhật 2026-08-28: A → B (2 private chain, không chain nào là Reserve) giờ đã nối dây thật,
tự động, đúng theo khoảng trống tính năng tìm thấy lúc viết bản đầu tài liệu này.** Chi tiết bên
dưới.

#### Trường hợp 1 — gửi thẳng tới/từ Reserve (1 chặng, đã hoạt động từ trước)

```
Chain nguồn X:  Outbound()            → khoá/burn coin thật từ tài khoản người gửi (EVM balance),
                                          xếp vào PendingOutboundMessages[ReserveChainID]
Chain nguồn X:  BatchOutboundCommit() → gom các message đang chờ thành 1 cây Merkle, ra commitRoot
Reserve:        attestCommit(X, ...)  → chạy trên Reserve (LocalChainID==ReserveChainID, C8) —
                                          trừ (debit) PerChainAllocation[X] trên bản ghi CỦA RESERVE
Reserve:        claimMessage()        → verify Merkle proof, cộng (credit) PerChainAllocation
                                          [ReserveChainID], thực thi payload/chuyển coin thật
```

#### Trường hợp 2 — A → B qua Reserve (2 chặng, tự động, không cần A/B tự thao tác lần 2)

Người gửi trên chain A gọi `Outbound()` **y hệt Trường hợp 1** nhưng với 2 điểm khác:
`DestChainID = ReserveChainID` (chặng đi trước mắt, bắt buộc để qua được check C8) và
`Payload = EncodeRelayPayload(finalDestChainID = B)` (đánh dấu "chuyển tiếp, không phải nhận
thật").

```
Chặng 1 (A → Reserve, y hệt Trường hợp 1):
  A:        Outbound(DestChainID=Reserve, Target=recipientOnB, Payload=EncodeRelayPayload(B))
  A:        BatchOutboundCommit()
  Reserve:  attestCommit(A, ...)   → C8 pass vì LocalChainID==ReserveChainID
  Reserve:  claimMessage()         → engine.ClaimMessage() verify + credit allocation như thường,
                                      NHƯNG gateway_handler.go phát hiện Payload là relay marker
                                      hợp lệ (chain B đã đăng ký, khác chính Reserve) →
                                      KHÔNG cộng thẳng số dư thật cho Target trên Reserve, thay
                                      vào đó gọi engine.Outbound() NGAY TRONG CÙNG giao dịch, xếp
                                      1 message MỚI vào PendingOutboundMessages[B] — HopCount+1

Chặng 2 (Reserve → B, tự động, RelayerDaemon watch cả cặp (Reserve,B) như mọi cặp khác):
  Reserve:  BatchOutboundCommit()   → gom message vừa xếp ở chặng 1
  B:        attestReserveIssuedCommit(Reserve, ...) → enforceCeiling=false (Reserve là
                                      unconditional issuer, không cần B tự là Reserve mới attest
                                      được — đây chính là mảnh còn thiếu trước đây)
  B:        claimMessage()          → không có relay marker (chặng 2 không đánh dấu tiếp) →
                                      cộng thẳng số dư thật cho recipientOnB, xong
```

**Vì sao an toàn (không double-spend)**: `gateway_handler.go`'s `claimMessage` case ở Chặng 1
KHÔNG BAO GIỜ vừa cộng số dư thật cho Target vừa xếp message chặng 2 — 2 nhánh loại trừ nhau
(`relayedOnward` guard). Value chỉ tồn tại ở đúng 1 nơi tại 1 thời điểm: hoặc đang "đi trên
đường" (nằm trong `PendingOutboundMessages`/`AttestedCommits`, chưa claim), hoặc đã claim thật ở
đích cuối cùng — không bao giờ cả hai.

**Rào chắn fail-closed**: relay marker trỏ về chính chain đang xử lý (self-loop) hoặc trỏ tới 1
chain chưa đăng ký đều bị từ chối thẳng (transaction revert), không âm thầm bỏ qua hay credit
nhầm chỗ.

**Cách 1 relayer thật biết dùng đúng hàm attest nào**: `RelayerDaemon` giờ nhận thêm
`DaemonConfig.ReserveChainID` — khi `sourceChainID` của 1 batch đúng bằng giá trị này, daemon tự
đổi sang gọi `attestReserveIssuedCommit` thay vì `attestCommit` (`chooseAttestMethod`,
`relayer_daemon/daemon.go`). CLI `cross_chain_relayer` có cờ `-reserve-chain-id` tương ứng. Việc
watch cặp (Reserve, B) **không cần code mới** — `cross_chain_relayer/main.go` vốn đã watch MỌI
cặp (nguồn, đích) trong danh sách `-chains` từ trước, chỉ cần Reserve nằm trong danh sách đó.

**Điểm mấu chốt về ý nghĩa của cọc/allocation**: `PerChainAllocation[chainID]` **không phải** số
dư ví người dùng — nó là **trần tín nhiệm cấp chain** (chain-level trust ceiling): "chain đích
tin chain X đã thực sự gửi ra tối đa bao nhiêu, dựa trên số đã cấp". Số dư ví người dùng (EVM
balance) là chuyện hoàn toàn khác, bị khoá/mở ở tầng `Outbound()`/thực thi payload, không đụng
tới `SupplyLedger`.

**Điểm mấu chốt về ý nghĩa của cọc/allocation**: `PerChainAllocation[chainID]` **không phải** số
dư ví người dùng — nó là **trần tín nhiệm cấp chain** (chain-level trust ceiling): "chain đích
tin chain X đã thực sự gửi ra tối đa bao nhiêu, dựa trên số đã cấp". Số dư ví người dùng (EVM
balance) là chuyện hoàn toàn khác, bị khoá/mở ở tầng `Outbound()`/thực thi payload, không đụng
tới `SupplyLedger`.

### Vòng lặp

`ClaimMessage()`'s bước cộng allocation (mục Bước 3, chain đích) chính là cách 1 chain **tự
nhiên có thêm allocation** mà không cần vote — nhưng đây KHÔNG phải in tiền: số cộng vào chain
đích đúng bằng số đã trừ khỏi chain nguồn ở bước `attestCommit`, tổng hệ thống không đổi. Chain
đích giờ có thể dùng allocation mới này để tiếp tục gửi đi (Bước 3 lặp lại, đóng vai "chain
nguồn" cho lượt tiếp theo) — với đúng giới hạn đã nêu ở trên: tự động hoàn toàn nếu lượt tiếp
theo đó lại đi tới/từ Reserve.

---

## 3. Vì sao thiết kế này an toàn — điểm qua từng bất biến

| Bất biến | Được đảm bảo bằng gì | Vi phạm thì sao |
|---|---|---|
| **Không in tiền ngoài kiểm soát** | `ProposalAllocateSupply` là đường DUY NHẤT tăng `GenesisTotalSupply`, khoá Reserve-only + one-time (`ErrOnlyReserveMayMint`/`ErrGenesisAlreadyMinted`) | Đây chính là C7 — đã tìm thấy và vá thật (PR #80) sau khi 1 PR khác (#84) từng vô tình tái tạo lại lỗ hổng này qua đường khác (gọi thẳng `GrantAllocation` từ `BootstrapFoundingChains`, đã bị chặn lại) |
| **Tổng cung luôn khớp** | `VerifyInvariant()` (`sum(PerChainAllocation) == GenesisTotalSupply`) được gọi cuối MỌI hàm sửa ledger (`GrantAllocation`, `TransferAllocation`) — `panic()` ngay nếu sai lệch, không âm thầm tiếp tục | Fail cứng (crash node) thay vì fail âm thầm — đúng nguyên tắc Zero-Fork: thà dừng còn hơn để trạng thái sai lan ra |
| **Chỉ 1 điểm kiểm tra trần duy nhất trên toàn hệ thống** | `attestCommitInternal`: bất kỳ `attestCommit` nào có giá trị >0 đều bắt buộc `LocalChainID == ReserveChainID` (C8 fix). Không chain nào khác được phép tự ý trừ allocation của 1 chain khác vào bản ghi CỤC BỘ của riêng mình | Nếu bỏ qua: nhiều chain đích độc lập có thể cùng attest và cộng dồn rút vượt trần thật của 1 nguồn — chính là C8, đã tìm thấy 2 lần (code gốc, rồi lại ở cấu hình deploy PR #84 tự-tham-chiếu Reserve) |
| **Cọc không bỏ qua rào cản đồng thuận** | Muốn có cọc, vẫn phải qua `ProposalTransferAllocation`/`ProposalAllocateSupply` — CẢ HAI đều bắt buộc `≥2/3` vote. `RegisterChainViaStake` bỏ vote ở BƯỚC ĐĂNG KÝ, không bỏ vote ở BƯỚC CẤP CỌC | 1 chain đơn lẻ (kể cả Reserve) không thể tự ý cấp cọc cho chain Sybil do mình dựng lên rồi tự đăng ký — vẫn cần đa số chain đang hoạt động đồng ý cấp cọc trước |
| **Không rogue-key ở bất kỳ đường đăng ký nào** | Cả `BootstrapFoundingChains`, `ProposalRegisterChain` (uỷ ban không rỗng), và `RegisterChainViaStake` đều bắt buộc `PopVerify` thật cho mọi validator | Không có đường tắt nào bỏ qua PoP — đã có test hồi quy riêng cho từng đường |
| **Claim không vượt quá đã attest** | `ClaimMessage` enforce `ClaimedAmount + Value <= FundedAmount` — không thể claim vượt số Reserve đã attest cho đúng commit đó | Chặn double-spend/over-claim trên cùng 1 commit đã attest |
| **Relay 2 chặng không double-credit** | `gateway_handler.go`'s `claimMessage` case: `relayedOnward` là 2 nhánh loại trừ nhau — hoặc cộng thẳng số dư thật cho Target, hoặc xếp message chặng 2, không bao giờ cả hai | Nếu bỏ qua: 1 giao dịch relay có thể vừa credit thật trên Reserve vừa credit thật (lần nữa) trên B — tạo tiền từ không khí |

---

## 4. Câu hỏi thường gặp

**Hỏi: Chain chưa từng nhận cọc thì làm sao đăng ký?**
Không thể qua Đường B (`RegisterChainViaStake` cần cọc có sẵn). Vẫn có thể qua Đường A
(`ProposalRegisterChain`, cần vote nhưng KHÔNG cần cọc nếu `MinRegistrationStake` chưa cấu hình
hoặc registry chỉ đăng ký metadata routing rỗng, chờ cấp cọc/uỷ ban thật sau).

**Hỏi: Founding chain (4 chain đầu, lúc genesis) có cần cọc không?**
Không — `BootstrapFoundingChains` hoàn toàn tách biệt khỏi `SupplyLedger` (xác nhận trực tiếp
trong code, xem comment tại `GrantAllocation`). Founding chain được cấp coin SAU KHI đăng ký,
qua đúng Bước 1–2 ở mục 2, không phải điều kiện TRƯỚC khi đăng ký.

**Hỏi: 1 chain có thể tự cấp cọc cho chính mình không?**
Không trực tiếp — `TransferAllocation(from, to, amount)` yêu cầu `from != to`
(`ErrSameChainTransfer`), và bản thân proposal `ProposalTransferAllocation` vẫn cần vote từ tập
chain đang hoạt động, không phải 1 chain tự quyết.

**Hỏi: A → B (2 private chain, không chain nào là Reserve) chuyển giá trị thật được chưa?**
**Đã có (2026-08-28)** — xem mục Bước 3, Trường hợp 2: người gửi trên A gọi `Outbound()` với
`Payload = EncodeRelayPayload(B)`, phần còn lại (chặng Reserve→B) tự động, không cần A hay người
dùng thao tác lần 2. Cần `RelayerDaemon` chạy với `-reserve-chain-id` trỏ đúng Reserve, và Reserve
phải nằm trong danh sách `-chains` của daemon (để daemon watch được cặp Reserve→B).

**Hỏi: Nếu Reserve bị chiếm (≥2/3 uỷ ban của chính Reserve bị compromise) thì sao?**
Đây là kịch bản A3 trong `cross_chain_attack_scenario_catalog.md` (weakest-link) — phòng thủ DUY
NHẤT là trần `PerChainAllocation` giới hạn đúng thiệt hại bằng số đã cấp phát hợp lệ trước đó,
không phải toàn bộ `GenesisTotalSupply`. Xem mục A3/A4 của tài liệu đó để biết khuyến nghị vận
hành (n≥4 validator mọi chain tham gia, đặc biệt Reserve).
