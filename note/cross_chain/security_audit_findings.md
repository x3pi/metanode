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
