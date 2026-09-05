# Security Audit Findings: Cross-Chain Flow

> **Cập nhật 2026-09-05: cả 2 phát hiện dưới đây đã được xác nhận là lỗi thật (kiểm chứng trực
> tiếp trên code, không chỉ đọc mô tả) và đã được vá + có test khoá lại hành vi đúng.** Chi tiết
> đầy đủ (root cause sâu hơn mô tả gốc, các file thay đổi, thiết kế pipeline mới) nằm ở
> `note/cross_chain_root_anchor_architecture.md` mục 2.4 (đối chiếu spec gốc) và trong các commit
> message liên quan. Giữ nguyên mô tả gốc bên dưới cho mục đích lịch sử/audit trail.

## 1. Permanent Lock of Funds / Denial of Service on Payload Revert — ✅ ĐÃ VÁ (2026-09-05)
**Location:** `execution/pkg/blockchain/tx_processor/gateway_handler.go` (`claimMessage` and `verifyAndExecute`)

**Description:**
If the inner EVM payload execution (`executeContractCallForGateway`) reverts (e.g., target contract is out of gas, calls `revert()`, or an ERC20 `transfer` fails), the error is bubbled up to `GatewayHandler`, which returns `HandleRevertedTransaction`. This causes the entire barrier transaction to revert, discarding all state changes (including setting the message status). 

As a result:
- The message remains `Pending` indefinitely.
- No `MessageFailureAttestMessage` (failCert) is ever generated because the node itself discarded the transaction.
- The user is permanently unable to claim the funds on the destination chain (it will always revert).
- The user is permanently unable to call `Refund` on the source chain because they cannot obtain a `failCert`.

**Đào sâu hơn khi vá (2026-09-05):** root cause thật sự sâu hơn mô tả trên. Xác nhận qua code:
`MessageStatusFailed` được định nghĩa nhưng **chưa từng có nơi nào trong toàn bộ codebase gán giá
trị này** (grep xác nhận) trước bản vá. `Refund()` xác minh `failCert` đúng và có test, nhưng cert
đó chỉ từng được tạo trực tiếp trong unit test (ký tay) — **không có pipeline production nào**
(kiểu `submitCommitAttestation`/`getCommitAttestationShares` đã có cho commit root) để validator
thật tạo ra cert này. Đối chiếu mục 2.4 điểm 1 của `note/cross_chain_root_anchor_architecture.md`:
đúng ra B phải finalize trạng thái FAILED (không rollback im lặng) — code cũ làm ngược lại hoàn
toàn.

**Đã vá bằng 3 phần:**
1. `GatewayEngine.FinalizeFailedAfterExecutionRevert` (gateway.go) — khi payload đích revert,
   không hard-revert cả transaction nữa mà finalize trạng thái `Failed` thật (hoàn lại ceiling
   `ClaimedAmount`/`PerChainAllocation`/tip đã tạm ghi nhận bởi `ClaimMessage`). Áp dụng cho cả
   nhánh CONTRACT_CALL native lẫn custom-asset vault-unlock/mint.
2. Pipeline tạo failCert production thật: 2 method ABI mới `submitMessageFailureAttestation` /
   `getMessageFailureAttestationShares` (mirror `submitCommitAttestation`/
   `getCommitAttestationShares`), `MessageFailureAttestationWorker` mới (mirror
   `CommitAttestationWorker`), trigger qua `MessageFailedCallback` (mirror
   `CommitFinalizedCallback`) — validator của chain đích tự ký + nộp share lên Root Anchor ngay khi
   local execution của chính nó xác nhận Failed.
3. `RelayerDaemon` (daemon.go): sau khi `claimMessage` trả về thành công nhưng trạng thái là
   `Failed`, tự động gộp QuorumCert thất bại rồi gọi `refund()` trên chain nguồn — có hàng đợi
   retry (`pendingRefunds`) nếu chưa đủ quorum hoặc gửi lỗi tạm thời.

Test: `TestGatewayEngine_FinalizeFailedAfterExecutionRevert_ReversesProvisionalCredits`
(cross_chain), `TestGatewayHandler_CustomAsset_Outbound_ClaimMessage` (cập nhật lại để khớp hành
vi mới).

## 2. Total Supply Deflation via Unrefunded Tip — ✅ ĐÃ VÁ (2026-09-05)
**Location:** `execution/pkg/blockchain/tx_processor/gateway_handler.go` (`refund`)

**Description:**
In `outbound`, `Value`, `GasFee`, and `Tip` are collectively burned from the sender's balance. However, if a message fails and is successfully refunded via the `Refund` function, only `GasFee` and `Value` are restored to `msg.Sender`. The `Tip` is completely ignored in the refund logic and is never credited back to the sender or given to any relayer. This permanently destroys the native tokens allocated to the `Tip`, violating the "Conservation of Balance" (Bảo toàn tổng cung) invariant.

**Đã vá:** thêm bước hoàn `Tip` về đúng `msg.Sender` trong case `"refund"`, giống hệt cách `GasFee`
được hoàn. Test mới: `TestGatewayHandler_Refund_RestoresTip` (outbound Tip=15 → attest → refund,
xác nhận số dư về đúng 100% ban đầu).

## 3. Cross-Chain Ledger Inflation via Missing Reserve Refund — ✅ ĐÃ VÁ (2026-09-05)
**Location:** `execution/pkg/cross_chain/gateway.go` (`CreditReserveAllocation`, `RefundReserveAllocation`)

**Mô tả (do người dùng phát hiện):** trong luồng 2-hop A -> Reserve -> B, `attestCommit` trừ
allocation của A trên Reserve, `creditReserveAllocation` cộng allocation cho B trên Reserve. Nếu
message thất bại trên B và được hoàn qua `Refund()` trên A, không có hàm nào đảo ngược phần credit
đã cộng cho B trên Reserve — allocation của B bị lạm phát vĩnh viễn, có thể dùng để "in" token thật
ở chuỗi khác.

**Kiểm chứng:** đúng là lúc phát hiện, code sửa đã có sẵn trong working tree (chưa commit) nhưng
**chưa chạy được** — thiếu entry ABI cho `refundReserveAllocation`, thiếu trong danh sách dispatch
write-method, và unpack calldata bỏ sót field `epoch` (làm lệch toàn bộ tham số phía sau, đồng thời
mất luôn bước so khớp epoch chống replay). Đào sâu hơn phát hiện gốc rễ còn nặng hơn: **`CreditReserveAllocation`
hoàn toàn không xác minh bằng chứng "message thực sự thành công"** — chỉ cần 1 Merkle proof khớp
1 commit đã attest (chứng minh message được xếp hàng, không chứng minh đã giao thành công) — nghĩa
là **bất kỳ ai** (không cần kiểm soát B) gọi được `creditReserveAllocation` đều có thể cộng khống
allocation cho bất kỳ chain nào.

**Đã vá triệt để (4 phần):**
1. Sửa 3 lỗi wiring của patch gốc (thêm ABI, thêm vào dispatch list, sửa unpack thiếu `epoch` +
   thêm bước so khớp epoch còn thiếu trong `RefundReserveAllocation`).
2. `CreditReserveAllocation` giờ **bắt buộc một QuorumCert thành công thật**, ký bởi đúng committee
   đã đăng ký của `destChainId` (đối xứng với `failCert` mà `Refund()`/`RefundReserveAllocation`
   đã yêu cầu) — domain tag mới `MESSAGE_SUCCESS_ATTEST_V1:`.
3. Pipeline production mới để tạo chứng chỉ thành công: `submitMessageSuccessAttestation`/
   `getMessageSuccessAttestationShares` (ABI mirror của cặp failure), `MessageSuccessAttestationWorker`
   mới (mirror `MessageFailureAttestationWorker`), trigger qua `MessageSucceededCallback` — validator
   của chain đích tự ký ngay khi local execution của chính nó xác nhận Success (Value > 0).
4. `RelayerDaemon` (daemon.go): trước khi gọi `creditReserveAllocation`, tự động gộp QuorumCert
   thành công từ Root Anchor (`pollAndAggregateSuccessCert`) rồi mới gửi kèm.

Test: `TestGateway_CreditReserveAllocation_2HopDestCredit` (cập nhật, thêm case cert giả mạo bị từ
chối), `TestGateway_RefundReserveAllocation_ReversesCreditAndEmitsRefund` (mới — đảo ngược credit,
phát sinh message hoàn tiền thật, chặn epoch cũ/cert giả/hoàn 2 lần),
`TestGatewayHandler_CreditAndRefundReserveAllocation` (mới — xuyên qua đúng dispatch ABI thật, đúng
tầng đã có lỗi wiring ban đầu).

## 4. Native/Custom-Asset Ledger Cross-Contamination — ✅ ĐÃ VÁ (2026-09-05, rà soát chủ động)
**Location:** `execution/pkg/cross_chain/gateway.go` (`attestCommitInternal`, `ClaimMessage`,
`Refund`, `CreditReserveAllocation`, `FinalizeFailedAfterExecutionRevert`)

**Mô tả:** phát hiện khi chủ động rà soát các chỗ khác có cùng dạng lỗ hổng với mục 3.
`SupplyLedger.PerChainAllocation` là sổ cái **native coin**, nhưng `AttestCommit`/`ClaimMessage`/
`Refund`/`CreditReserveAllocation` trước đây trừ/cộng vào đúng sổ này cho **bất kỳ `assetId` nào**
— nghĩa là số lượng thô của 1 custom asset (thường tính theo 10^18, không liên quan gì tới lượng
native coin thật chain đó đang giữ) bị trộn chung vào đúng 1 bộ đếm với native coin.

Hướng nguy hiểm thật sự: `ClaimMessage`'s credit không phân biệt asset → 1 lần claim custom-asset
hợp lệ (không cần tấn công) có thể **thổi phồng ceiling native coin** của chain nhận lên một con số
khổng lồ, sau đó chain đó có thể rút native coin thật vượt xa những gì nó thực sự nên được phép —
"rửa" khối lượng custom-asset bất kỳ thành hạn mức chi tiêu native coin thật. Xác nhận thêm:
`AssetRegistryEngine.LockAndBridgeAsset`/`ReceiveAndSettleAsset`/`VerifyAssetConservationInvariant`
— cơ chế bảo toàn cung riêng, đúng đắn, dành cho custom asset — **chưa từng được gọi ở đâu trong
code production** (chỉ định nghĩa, không ai dùng); bảo toàn thật cho custom asset hiện dựa hoàn
toàn vào các lệnh gọi `transferFrom()`/`transfer()`/`mint()` thật trên hợp đồng token thật.

**Đã vá:** thêm điều kiện `assetId == 0` (native) trước MỌI thao tác đọc/ghi
`PerChainAllocation` ở cả 5 vị trí trên — custom asset không chạm vào sổ cái native nữa (tự bảo toàn
đủ qua hợp đồng token thật, không cần và không nên dùng chung sổ cái này). Không đổi hành vi native
coin (assetId=0) chút nào.

Test: `TestGateway_CustomAssetNeverTouchesNativePerChainAllocation` (mới — commit custom-asset với
giá trị 10^24 vượt xa allocation native nhỏ của chain nguồn, kể cả khi Reserve chưa cấu hình, vẫn
attest+claim thành công và không đụng tới `PerChainAllocation` của bất kỳ chain nào).

## 5. Permanent Lock of Funds via Unregistered Destination Chain in outbound() — ✅ ĐÃ VÁ (2026-09-05, rà soát chủ động)
**Location:** `execution/pkg/blockchain/tx_processor/gateway_handler.go` (case `"outbound"`)

**Mô tả:** phát hiện khi rà soát toàn bộ các luồng cross-chain theo yêu cầu người dùng ("đánh giá
lại tất cả các luồng"). Nhánh relay-onward bên trong `claimMessage` (khi 1 message tiếp tục đi
tiếp sang chain thứ 3) đã kiểm tra `finalDestChainID` phải khác chính chain hiện tại VÀ phải nằm
trong `ChainRegistry` trước khi gọi `engine.Outbound(...)`. Nhưng điểm vào trực tiếp do người dùng
gọi — case `"outbound"` — lại **hoàn toàn không có 2 kiểm tra này**: `engine.Outbound()` tự nó
không có quyền truy cập `ChainRegistry` nên không thể tự kiểm tra, và handler gọi thẳng nó rồi burn
thật Value+Tip+GasFee ngay sau đó.

Hậu quả: gửi `outbound()` tới một `destChainId` gõ nhầm hoặc chưa từng được đăng ký sẽ burn thật
tiền của người gửi, nhét message vào `PendingOutboundMessages[destChainId]` — nơi không ai (không
relayer nào theo dõi cặp chain đó) sẽ bao giờ đóng gói (`batchOutboundCommit`) hay giao đến đích.
Message nằm `Pending` vĩnh viễn, không bao giờ có cách tạo ra failure cert (vì không ai từng thử
thực thi nó để mà revert) → tiền bị khoá vĩnh viễn, không có đường hoàn lại. Cùng nguyên tắc gốc rễ
với mục 1 ("no permanent lock of funds"), khác ở chỗ đây là tự người gửi kích hoạt (không cần kẻ
tấn công) chứ không phải khai thác nhắm vào tiền của người khác.

**Đã vá:** thêm đúng 2 kiểm tra mirror với nhánh relay-onward, chạy TRƯỚC mọi burn/lock: từ chối
nếu `destChainId == LocalChainID` (gửi "cross-chain" về chính mình) hoặc `destChainId` chưa có
trong `ChainRegistry`. Không đổi hành vi với một destChainId hợp lệ đã đăng ký.

Test: `TestGatewayHandler_Outbound_RejectsUnregisteredOrSelfDestChain` (mới — cả 2 trường hợp bị từ
chối trước khi có bất kỳ thay đổi số dư nào); cập nhật setup ChainRegistry cho 4 test hiện có từng
ngầm dựa vào việc gửi tới 1 destChainId chưa đăng ký (`TestGatewayHandler_OutboundPersistsAcrossChainStateReload`,
`TestGatewayHandler_ConsecutiveTransactionsFromSameSenderAdvanceNonce`,
`TestGatewayHandler_CustomAsset_Outbound_ClaimMessage`, `TestGatewayHandler_BatchOutboundCommit_EndToEnd`,
`TestGatewayHandler_CustomAsset_RealTokenTransferSucceeds`).

---

**Kết luận rà soát toàn diện (2026-09-05, theo yêu cầu "đánh giá lại tất cả các luồng"):** đã đi
qua toàn bộ danh sách method export của `GatewayEngine`/`AssetRegistryEngine` (attest/claim/refund/
credit/refundReserve/registerChainViaStake/transferAllocationWithCert/allocateSupplyWithCert/
declareChainDeadWithCert/unregisterChainWithCert/updateCommitteeWithRecoveryCert/
claimDeadChainBalance/withdrawRelayerTip/batchOutboundCommit/outbound/setGenesisDigest/
registerCommitteePop/submitCommitteeAttestation/submitCommitAttestation, cùng toàn bộ dispatch case
tương ứng trong `gateway_handler.go`) theo 3 tiêu chí: (a) đúng bên ký/đúng cert cho mọi hành động
ảnh hưởng tới chain khác, (b) idempotent hoặc có cơ chế chống replay khi hành động không tự nhiên
idempotent, (c) không lẫn đơn vị native/custom-asset. Ngoài finding #5 ở trên, không phát hiện thêm
lỗ hổng nào — toàn bộ các hàm còn lại đã đúng thiết kế (nhiều hàm có sẵn comment ghi lại các lần vá
bảo mật trước đó, ví dụ nonce chống replay của `TransferAllocationWithCert`, epoch-phải-tăng của
`UpdateCommitteeWithRecoveryCert`, merkle-proof-qua-`AccountTreeRoot` đã được chốt bởi chữ ký
committee cũ của `ClaimDeadChainBalance`).

## 6. Total Supply Deflation via Blocked 2-Hop Refund (Value never restored) — ✅ ĐÃ VÁ (2026-09-05)
**Location:** `execution/pkg/cross_chain/gateway.go` (`Refund`), `execution/pkg/blockchain/tx_processor/gateway_handler.go` (case `"refund"`), `execution/pkg/cross_chain/relayer_daemon/daemon.go` (`processFailedClaim`)

**Mô tả (do người dùng đề xuất giải pháp, yêu cầu kiểm tra + xử lý triệt để):** trước bản vá này,
`Refund()` **cấm hoàn toàn** việc gọi trực tiếp trên chain nguồn (A) đối với message 2-hop
(`DestChainID != ReserveChainID`), bắt buộc phải đi qua `RefundReserveAllocation` trên Reserve.
Hậu quả: message gốc kẹt vĩnh viễn ở `Pending` trên A, và vì `Refund()` trả lỗi ngay từ đầu, ngay cả
Tip + GasFee (vốn chỉ là burn cục bộ trên A, không liên quan gì tới Reserve) cũng không bao giờ được
hoàn — một dạng Total Supply Deflation.

**Giải pháp người dùng đề xuất** (đã kiểm chứng đúng qua code, áp dụng gần như nguyên vẹn):
1. Mở lại `Refund()` cho message 2-hop: vẫn xác thực `destFailureCert`, đánh dấu
   `MessageStatusRefunded` trên A, hoàn Tip + GasFee như bình thường.
2. Nhưng **bỏ qua** bước cộng lại `PerChainAllocation` (gateway.go) và bỏ qua việc mint lại `Value`
   (gateway_handler.go) khi là 2-hop — vì `Value` chưa từng bị trừ trên sổ cái CỤC BỘ của A (bước
   trừ ceiling thật luôn xảy ra trên RESERVE, qua `attestCommit`, xem `chooseAttestMethod`/
   `AttestCommit`'s C8 gate) — mint lại ở đây sẽ là mint khống lần 2 khi Reserve cũng hoàn `Value`.
   `Value` cốt lõi tiếp tục được hoàn qua 1 outbound message mới do Reserve tự phát sinh.

**Đào sâu hơn khi vá — 1 lỗ hổng riêng phát hiện thêm khi kiểm tra "Reserve tự phát sinh outbound
message mới" có thật sự xảy ra không:** grep xác nhận `RefundReserveAllocation` (hàm ĐÃ tồn tại từ
fix #3) **chưa từng được gọi ở bất kỳ đâu trong `relayer_daemon/daemon.go`** — `processFailedClaim`
(hàm orchestration thật duy nhất xử lý message Failed) chỉ gọi `refund()` trên chain nguồn, không hề
biết tới `refundReserveAllocation`. Nghĩa là: nếu chỉ áp dụng đúng y nguyên giải pháp người dùng đề
xuất (2 phần ở gateway.go/gateway_handler.go) mà không nối dây thêm phần daemon, kết quả sẽ **tệ hơn
cả lỗi gốc** — message bị đánh dấu `Refunded` (coi như đã xong) nhưng `Value` không bao giờ thực sự
được hoàn ở đâu cả, mất tiền vĩnh viễn thay vì chỉ kẹt `Pending`.

**Đã vá bổ sung:** `processFailedClaim` (daemon.go) giờ, sau khi `refund()` thành công trên A, tính
`is2Hop` (đúng công thức với `attestCommitInternal`/gateway_handler.go's "refund" case) và nếu true,
gọi thêm `refundReserveAllocation()` trên Reserve (dùng lại đúng `destFailureCert` đã gộp) — hàm này
đảo ngược credit đã cấp cho B trên Reserve (nếu có, phòng thủ thêm — về lý thuyết không thể có vì
`CreditReserveAllocation` từ fix #3 đã yêu cầu success cert thật) và phát sinh 1 message outbound
Value-only mới từ Reserve về A, message này sau đó chảy qua đúng pipeline attest/claim bình thường
để mint thật `Value` cho user trên A. Thêm kiểm tra trạng thái trước khi gọi lại `refund()`/
`refundReserveAllocation()` (idempotent re-entry) để một lần retry sau lỗi mạng tạm thời không bị
`refund()` revert (đã Refunded từ lần trước) che mất việc `refundReserveAllocation()` vẫn cần chạy.

Test: `TestGatewayHandler_Refund_TwoHop_SkipsValueRestoresTipAndGasFee` (mới — real ABI dispatch,
real ví thật: xác nhận Tip+GasFee được hoàn, `Value` KHÔNG được mint lại cục bộ khi là 2-hop; đối
chứng với `TestGatewayHandler_Refund_RestoresTip` vốn cố tình đặt `LocalChainID == ReserveChainID`
để rơi vào nhánh không phải 2-hop và xác nhận `Value` VẪN được hoàn ở đó), và
`TestRelayerDaemon_ClaimMessageFails_PursuesRefund_TwoHop` (mới — dựng thật 3 chain A/Reserve/B qua
RPC giả lập, chạy toàn bộ vòng batch → attestCommit (Reserve) + attestReserveIssuedCommit (B) →
claimMessage thất bại trên B → `processFailedClaim` hoàn Tip/GasFee trên A + gọi
`refundReserveAllocation` trên Reserve, xác nhận Reserve thật sự sinh ra đúng 1 outbound message mới
mang đúng `Value` gửi về A).

## 7. MessageID không được bảo tồn qua chặng relay (Reserve -> B) — ✅ ĐÃ VÁ (2026-09-05)
**Location:** `execution/pkg/cross_chain/gateway.go` (`OutboundParams`, `Outbound`),
`execution/pkg/blockchain/tx_processor/gateway_handler.go` (case `"claimMessage"`, nhánh relay-onward)

**Mô tả (do người dùng phát hiện + đề xuất giải pháp):** cơ chế relay-onward trong `claimMessage`
(mục "2-hop A -> Reserve -> B value & CONTRACT_CALL routing", dùng `EncodeRelayPayload`/
`DecodeRelayPayload`, KHÁC với cơ chế dual-attest ở mục 3/6) — khi chặng 1 (A -> Reserve) được
claim thành công và Reserve phát hiện marker relay, Reserve tự sinh 1 message chặng 2 (Reserve ->
B) bằng cách gọi `engine.Outbound()` — nhưng trước đây hàm này luôn tự băm `tx.Hash()` của giao
dịch claim chặng 1 làm MessageID MỚI, không liên hệ gì tới MessageID gốc mà A đã gửi.

**Đối chiếu claim gốc của người dùng:** mô tả gốc nói `RefundReserveAllocation`/
`CreditReserveAllocation` "đòi hỏi MessageID cũ" — kiểm chứng trực tiếp trên code cho thấy điều này
**không chính xác**: 2 hàm đó (xây ở mục 3/6) thuộc về 1 cơ chế 2-hop HOÀN TOÀN KHÁC (dual-attest
trực tiếp A→B qua `RelayBatch`, không tạo message mới) và chưa từng được gọi cho luồng relay-onward
này. Tuy nhiên, đào sâu bằng 1 test thật (chạy `git stash` tạm bỏ patch để so sánh trước/sau) xác
nhận **triệu chứng vẫn có thật, thậm chí nặng hơn mô tả gốc**: vì chặng 2 dùng ID mới không liên hệ
gì tới ID gốc, `MessageStatus` phía Reserve cho ĐÚNG ID mà người gửi trên A từng thấy bị đóng băng
vĩnh viễn ở `Success` (do `ClaimMessage` chặng 1 set) — **kể cả khi chặng 2 sau đó THẤT BẠI thật và
được hoàn tiền** dưới ID mới không ai biết để tra cứu. Đây không chỉ là thiếu quan sát được mà còn
là báo cáo trạng thái SAI (nói "thành công" trong khi tiền đã bị hoàn ngược, không đến B) dưới đúng
ID người dùng có trong tay.

**Giải pháp người dùng đề xuất** (giữ nguyên, chỉ sửa lại comment giải thích cho đúng): thêm
`OriginalID *common.Hash` vào `OutboundParams`; nhánh relay-onward truyền
`OriginalID = &msg.MessageID` (ID của chặng 1); `Outbound()` tái sử dụng ID này thay vì băm mới nếu
được cung cấp. Xác nhận không có tác dụng phụ: `ChannelSequence` (theo cặp chain, không liên quan
MessageID), `LockedTips` (chặng relay luôn Tip=0, không kích hoạt), và không có rủi ro trùng lặp
(ID gốc là hash giao dịch thật của A, không thể trùng ai khác) — 2 điểm gọi `Outbound()` còn lại
(case `"outbound"` thật của user, và `RelayerEngine.SubmitOutbound` — xác nhận dead code, không ai
gọi ngoài chính file định nghĩa nó) không bị ảnh hưởng vì không set `OriginalID`.

**Kiểm chứng bằng test thật (không chỉ đọc code):** viết
`TestComprehensive_TwoHopContractCall_LegTwoFailsAndRefundsOnReserve` (chặng 2 mang cả Value lẫn 1
lệnh gọi ERC-20 thật cố tình revert do người gửi có số dư token = 0 trên B) — chạy `git stash` tạm
gỡ patch: test THẤT BẠI đúng như dự đoán, cho thấy `MessageStatus` phía Reserve của ID chặng 1 vẫn
là `Success` (0x1) dù chặng 2 đã `Refunded`. Áp lại patch: test PASS, `MessageStatus` của ID chặng 1
trên Reserve phản ánh đúng `Refunded`.

Test: `TestComprehensive_TwoHopContractCall_LegTwoFailsAndRefundsOnReserve` (mới, quy trình đầy đủ:
batchOutboundCommit thật trên Reserve → attestReserveIssuedCommit + claimMessage thất bại thật trên
B → refund thật trên Reserve dùng đúng ID chặng 1 → xác nhận trạng thái cuối cùng dưới ID gốc).

## 8. Double Refund of Tip and GasFee on Reverted Executions — ✅ ĐÃ VÁ (2026-09-05)
**Location:** `execution/pkg/cross_chain/gateway.go` (`FinalizeFailedAfterExecutionRevert`),
`execution/pkg/blockchain/tx_processor/gateway_handler.go` (case `"refund"`)

**Mô tả (do người dùng phát hiện, kèm patch đề xuất cho 2 lỗ hổng — chỉ mục này được áp dụng, xem
mục "Đã rà soát và KHÔNG áp dụng" ngay dưới cho lỗ hổng còn lại):** khi `claimMessage` claim thành
công nhưng payload thực thi thật sự REVERT (business-logic revert của dApp đích) — trạng thái được
`FinalizeFailedAfterExecutionRevert` chốt thành `Failed` (mục 2.4 điểm 1 / finding #1) — Tip đã được
`ClaimMessage` cộng vào `RelayerBalances[relayer]` **trước khi** payload được thực thi (không điều
kiện gì), và GasFee đã được `settleGasCappedContractCall` xử lý xong hoàn toàn (phần dùng cho
validator + phần dư hoàn lại `msg.Sender`) **ngay cả khi revert** — cả 2 đã được settle vĩnh viễn
trên chuỗi đích. Nhưng: (a) `FinalizeFailedAfterExecutionRevert` (bản cũ) lại thu hồi ngược Tip khỏi
`RelayerBalances[relayer]` — tước đoạt phần thưởng hợp lệ của relayer dù họ đã làm đúng việc (chỉ
dApp đích lỗi logic, không phải lỗi của relayer); (b) `refund()` (bản cũ, thêm bởi chính fix #2 của
phiên làm việc này) lại hoàn tiếp 100% Tip + GasFee cho `msg.Sender` trên chuỗi nguồn — tức là **in
tiền lần 2** cho đúng số Tip/GasFee đã settle xong ở chuỗi đích.

**Đã vá:** `FinalizeFailedAfterExecutionRevert` không thu hồi Tip khỏi relayer nữa (relayer giữ
nguyên phần thưởng đã kiếm được). `refund()` chỉ còn hoàn `Value` (và chỉ khi không phải 2-hop, xem
mục 6) — bỏ hẳn bước hoàn `Tip`/`GasFee` mà fix #2 từng thêm vào, vì fix #2 đã sai khi lấy đúng logic
hoàn `GasFee` (vốn đã sai từ trước) làm khuôn mẫu để mirror sang `Tip`.

Test: `TestGatewayHandler_Refund_DoesNotRestoreTipOrGasFee` (đổi tên + viết lại từ
`TestGatewayHandler_Refund_RestoresTip` của fix #2 — giờ xác nhận đúng ngược lại: chỉ `Value` được
hoàn, `Tip`+`GasFee` bị giữ nguyên không hoàn), `TestGatewayHandler_Refund_TwoHop_RestoresNothingLocally`
(đổi tên từ `..._SkipsValueRestoresTipAndGasFee` — 2-hop giờ không hoàn gì cục bộ cả), cập nhật
`TestGatewayEngine_FinalizeFailedAfterExecutionRevert_ReversesProvisionalCredits` (đảo ngược assertion
Tip).

### Đã rà soát và KHÔNG áp dụng: "Total Supply Inflation via Unbacked Tip and GasFee Minting"

Người dùng cũng đề xuất 1 patch thứ 2 (đã sửa `relayer.go`'s `BuildCommitTree` để cộng thêm
`Tip`+`GasFee` vào `AggregateAmount`, và `gateway.go`'s `ClaimMessage` để đưa cả 2 vào
`totalClaimAmount` khi kiểm tra hard-cap `FundedAmount` và cộng vào `PerChainAllocation`), với lý do:
"Reserve chỉ trừ Value, nhưng destination lại mint cả Tip/GasFee khi relayer rút tiền → in tiền vô
hạn không qua `PerChainAllocation`."

**Kiểm chứng lại bằng phản chứng cụ thể (không chỉ đọc mô tả):** `Tip`/`GasFee` **đã được** đưa vào
đúng hash của per-message Merkle leaf từ trước (`CanonicalEncodeMessage`, comment gốc của hàm này
ghi rõ: "included in the hash so a relayer can't alter the locked cross-chain gas budget in
transit"). Nghĩa là 1 relayer **không thể** khai khống `Tip`/`GasFee` khi gọi `claimMessage()` —
mọi giá trị khác với giá trị thật trong message gốc (đã được BLS-sign bởi committee chuỗi nguồn) sẽ
làm sai lệch `leafHash`, khiến `VerifyMerkleProof` thất bại ngay lập tức. Số `Tip`/`GasFee` được
mint ở đích luôn **khớp chính xác 1:1** với số đã burn thật ở nguồn — không có "in vô hạn" nào cả.
`PerChainAllocation` không đếm `Tip`/`GasFee` chỉ khiến ceiling của 1 chain **thấp hơn thực tế nó
đang nắm giữ** (bảo thủ hơn mức cần thiết khi chain đó tiếp tục gửi giá trị đi tiếp qua Reserve) —
không phải lỗ hổng khai thác được.

**Áp patch này gây ra 1 regression nghiêm trọng khác, xác nhận bằng test thật:** `AttestCommit`'s
cổng C8 ("chỉ Reserve mới được attest 1 commit có `aggregateAmount > 0`") kích hoạt bất cứ khi nào
`aggregateAmount > 0` — trước patch, 1 message "thuần tuý" (`Value = 0`, chỉ gọi contract, theo đúng
phân loại mục 2.2(a) của `note/cross_chain_root_anchor_architecture.md`, vốn được thiết kế đi thẳng
A→B không cần qua Reserve) luôn có `aggregateAmount = 0`. Sau patch, VÌ hầu như MỌI message CONTRACT_CALL
thật đều có `GasFee > 0` (ngân sách gas cho lệnh gọi), `aggregateAmount` giờ luôn > 0 — khiến MỌI
message thuần tuý cũng bị bắt buộc phải cấu hình Reserve (`ErrReserveChainNotConfigured`) hoặc phải
attest trên đúng Reserve (`ErrNonReserveCeilingAttestation`) mới attest được — phá vỡ hoàn toàn khả
năng gửi message trực tiếp A→B mà kiến trúc gốc đã minh định. Phát hiện qua 8 test thật lập tức FAIL
sau khi áp patch (`TestGatewayHandler_ClaimMessagePayload_ExecutesRealContractCall`,
`TestGatewayHandler_VerifyAndExecutePayload_ExecutesRealContractCall`,
`TestComprehensive_TwoHopContractCall_RealERC20_A_Reserve_B`, và 5 test khác) — tất cả PASS trở lại
sau khi revert riêng phần patch này.

**Kết luận:** đã revert hoàn toàn phần thay đổi ở `relayer.go`/`ClaimMessage`'s `totalClaimAmount`
(giữ nguyên `BuildCommitTree`/hard-cap/`PerChainAllocation` credit ở Value-only như trước), chỉ giữ
lại phần vá double-refund (mục 8 ở trên) — độc lập hoàn toàn với phần này, không phụ thuộc gì vào
nhau.
