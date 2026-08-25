# Kế hoạch sửa Task 1 (native value-transfer wiring) — 2 lỗi mất-tiền/double-mint mới,
# tìm thấy 2026-08-25 khi review code Task 1 (chưa commit)

> ✅ **RESOLVED 2026-08-25, cùng ngày tìm thấy.** Cả 3 lỗi mô tả dưới đây đã được sửa —
> phần lớn bởi một agent khác chạy song song (đã tự áp dụng đúng hướng sửa đề xuất ở đây:
> validate-trước-mutate-sau cho `outbound`, gate `isContractCall` dựa trên code thật thay vì
> đoán qua field mới, bỏ cơ chế mint-tip-tức-thời giữ lại accrue+withdraw). Session này xác
> minh lại bằng `go build/vet/test` thật (100% pass, không riêng 2 package cũ mà toàn bộ
> `go test ./...`) rồi tìm thêm và tự sửa **3 vấn đề residual** không nằm trong 3 lỗi gốc:
> 1. `outbound()` đường native burn Tip và Value bằng 2 lệnh tách rời — nếu balance đủ Tip
>    nhưng không đủ Tip+Value, Tip bị đốt kẹt. Đã gộp thành 1 lệnh burn duy nhất.
> 2. `outbound()` đường custom asset burn Tip (native) *trước* khi lock asset
>    (`transferFrom`, có thể fail do thiếu allowance) — Tip bị đốt kẹt nếu lock fail. Đã đảo
>    thứ tự: lock asset trước, Tip burn cuối cùng.
> 3. `verifyAndExecute` (pack event) và `withdrawRelayerTip` (pack return data) return lỗi
>    cứng SAU KHI balance đã mutate thật — vi phạm chính nguyên tắc "không còn đường lỗi sau
>    khi đã mutate balance" mà file này đặt ra. Đã sửa thành soft-fail (event) / pack trước khi
>    mutate (return data).
> Cũng xoá 1 đoạn `fmt.Printf` debug bị bỏ sót trong `GatewayEngine.ClaimMessage` (in ra mọi
> lần claim, không gate).
>
> **Còn mở, chưa chặn nhưng cần biết:**
> - **Task 1.2 (custom asset) có code nhưng test còn yếu** — `TestGatewayHandler_CustomAsset_Outbound_ClaimMessage`
>   chỉ chứng minh code "fail gracefully" khi target không phải contract thật (dùng địa chỉ
>   precompile SHA256 làm giả), **chưa có test nào chứng minh `transferFrom`/`mint`/`transfer`
>   thật sự thành công** khi gọi vào một token contract ERC-20 thật đã deploy. Cần thêm trước
>   khi coi Task 1.2 là "xong", không chỉ "có code".
> - **Rủi ro residual đã biết, chấp nhận, chưa giải:** `saveGatewayEngine()` (dòng cuối cùng
>   `handleWrite`) tự nó có thể fail (lỗi ghi storage trie) — nếu fail SAU KHI balance đã
>   mutate thật ở bất kỳ case nào, cùng shape lỗi với 3 lỗi gốc nhưng xác suất thấp hơn nhiều
>   (storage-trie ghi lỗi là lỗi hạ tầng nghiêm trọng, không phải điều kiện business-logic
>   thường gặp). Giải triệt để cần cơ chế snapshot/revert thật ở tầng `AccountStateDB`/
>   `SmartContractDB` cho barrier TX — xác nhận qua code: **không hề tồn tại**
>   (`grep -rn "func.*Snapshot\|RevertToSnapshot" pkg/state pkg/blockchain` chỉ ra 1 hàm
>   `Snapshot` không liên quan trong `gateway_registry_monitor.go`). Không sửa trong lần này —
>   ghi lại làm việc cho vòng audit bảo mật (Phase 3/P5) hoặc một lần refactor riêng nếu muốn
>   loại bỏ hoàn toàn lớp rủi ro này.
>
> `go build ./... && go vet ./... && go test ./...` (toàn bộ module `execution/`) đã chạy
> thật, xanh 100%, không riêng 2 package Gateway.
>
> Nội dung gốc bên dưới giữ nguyên làm hồ sơ đầy đủ của từng lỗi (vị trí, bằng chứng, cách
> khai thác) — vẫn hữu ích để hiểu vì sao nguyên tắc "mutate balance cuối cùng, không còn
> đường lỗi sau nó" tồn tại trong toàn bộ file này.

---

**Dành cho:** một agent/dev mới hoàn toàn, chưa có ngữ cảnh trước.

**Đọc trước, theo đúng thứ tự:**
1. `note/cross_chain_production_readiness_plan.md` — mục **"How to work"** (bắt buộc: Zero-Fork
   invariant, verification bar `go build && go vet && go test` zero regressions, quy trình PR,
   triết lý testing "test có thực sự chứng minh điều nó tuyên bố, hay chỉ tự set giá trị rồi
   check lại chính giá trị đó").
2. Cùng file đó, mục **Phase 0.6** — bối cảnh đầy đủ: vì sao value-transfer chưa từng được nối
   vào balance thật, và kế hoạch sửa gốc (6 bước) mà đoạn code dưới đây đang cố hiện thực.
3. `note/cross_chain_full_implementation_plan.md` Task 1 — vị trí việc này nằm trong lộ trình
   tổng.

## Bối cảnh: code đã tồn tại, chưa commit, có bug

Có một lần thực hiện Task 1 đã được viết (không rõ tác giả — không phải qua PR, không có trong
`git log`) và hiện nằm **chưa commit** trong working tree ở đúng 4 file `git status` liệt kê:
`execution/pkg/cross_chain/gateway.go`,
`execution/pkg/blockchain/tx_processor/gateway_handler.go`,
`execution/pkg/blockchain/tx_processor/gateway_handler_test.go`,
`execution/pkg/blockchain/vm_processor/vm_processor.go`,
`execution/pkg/blockchain/tx_processor/abi_contract/gatewayAbi.go`.

**Nếu các file này không còn ở trạng thái mô tả dưới đây** (đã bị revert, đã commit, hoặc đã bị
sửa tiếp) — đọc kỹ diff hiện tại trước, đối chiếu với mô tả bên dưới, rồi áp dụng đúng phần fix
plan còn liên quan. Đừng giả định code y hệt.

**Xác nhận bằng lệnh thật (đã chạy, kết quả thật, không phải suy đoán), từ `execution/`:**
```
go build ./...        # PASS
go vet ./...           # PASS
go test ./pkg/blockchain/tx_processor/... ./pkg/cross_chain/...
```
→ **FAIL 3 test**: `TestGatewayHandler_VerifyAndExecute_Lifecycle`,
`TestGatewayHandler_AttestCommitThenClaimMessage`,
`TestGatewayHandler_ClaimMessageMintsRealValueAndTip` (test do chính đoạn code này viết ra —
cũng fail). Đây là bằng chứng đầu tiên: code hiện tại vi phạm ngay "verification bar" của chính
dự án — chưa từng được chạy `go test` sạch trước khi để lại trong working tree.

Việc **đã làm đúng** trong lần thực hiện này (không cần sửa): Task 2 —
`GatewayEngine.BootstrapFoundingChainsWithCaller` + field `GenesisCoordinator`
(`gateway.go` quanh dòng 169-182) chặn front-run đúng như readiness plan yêu cầu, có test
`TestGatewayHandler_BootstrapFoundingChains_CoordinatorGuards` pass thật.

---

## Nguyên nhân gốc của mọi lỗi dưới đây

`runBarrierTx` (`execution/pkg/blockchain/tx_processor/true_block_stm.go`, hàm xử lý mọi tx gửi
tới `GATEWAY_CONTRACT_ADDRESS`) tự ghi rõ trong comment: nó chạy **"directly against the global
chainState (no MVCC tracking, no retry-on-abort)"**. Trong khi đó `GatewayHandler.handleWrite`
(`gateway_handler.go`) chỉ gọi `saveGatewayEngine(chainState, engine)` **một lần duy nhất, ở
dòng cuối cùng (dòng 1002)** — nghĩa là mọi thay đổi trong struct `GatewayEngine` (MessageStatus,
ClaimedAmount, RelayerBalances...) chỉ được lưu thật nếu hàm chạy hết, không return lỗi giữa
chừng ở bất kỳ `case` nào.

Hai model persistence này **không tương thích** khi trộn trong cùng một hàm: `AccountStateDB`
ghi ngay lập tức, không rollback được; `GatewayEngine` chỉ ghi khi thành công toàn bộ. Code Task
1 gọi `processNativeMintBurnForGateway`/`executeContractCallForGateway` (đều ghi thẳng vào
`AccountStateDB`) xen giữa các bước có thể fail — đó là gốc của cả 3 lỗi dưới đây.

**Nguyên tắc sửa chung, áp dụng cho mọi case:** trong mỗi nhánh xử lý (`outbound`, `claimMessage`,
`verifyAndExecute`, `refund`, `claimDeadChainBalance`, `withdrawRelayerTip`), phải đảm bảo
**không còn bất kỳ đường lỗi (`return ..., err`) nào sau khi đã gọi
`processNativeMintBurnForGateway`/`executeContractCallForGateway`**. Tức là: validate xong hết,
chạy hết mọi bước có thể fail (kể cả contract-call), **rồi mới** mutate balance thật, ở cuối
cùng. Nếu một bước bắt buộc phải chạy sau khi mint (ví dụ ghi log/event) thì bước đó không được
phép fail theo cách trả lỗi ra ngoài.

---

## Lỗi 1 (CRITICAL, mất tiền vĩnh viễn) — `outbound()` đốt tiền thật trước khi validate

**Vị trí:** `gateway_handler.go` case `"outbound"`, dòng ~420-445. Thứ tự hiện tại:
```go
// dòng ~430: burn NGAY, trước khi biết Outbound() có hợp lệ không
if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 1, totalDeduct, tx.FromAddress(), tx.ToAddress()); err != nil {
    return nil, nil, fmt.Errorf("outbound native burn failed: %w", err)
}
msg, err := engine.Outbound(tx.FromAddress(), params, tx.Hash())
if err != nil {
    return nil, nil, err   // <-- burn đã xảy ra thật, KHÔNG được hoàn lại
}
```
`engine.Outbound()` (`gateway.go` dòng ~382-434) có thể fail (ví dụ
`params.HopCount > MaxHopCount`, dòng 387-389 — người gọi/relayer client set sai HopCount hoàn
toàn hợp lý xảy ra trong vận hành thật). Khi đó: tiền đã bị `SubBalance` thật (ghi trực tiếp, đã
giải thích ở trên, không rollback được), transaction báo lỗi/revert, nhưng **không có
`CrossChainMessage` nào được tạo** để claim lại. Người gọi mất tiền vĩnh viễn dù giao dịch của họ
"thất bại".

**Test hiện có không bắt được lỗi này:** `TestGatewayHandler_OutboundFailsOnInsufficientBalance`
chỉ test nhánh balance không đủ (fail **trước** khi kịp burn, vì burn tự fail closed khi
`SubBalance` báo lỗi — nhánh này đúng, giữ nguyên). Không có test nào cho nhánh "burn thành công,
sau đó `Outbound()` mới fail".

**Fix:** đảo thứ tự — gọi `engine.Outbound()` trước để lấy `msg`/lỗi, xong xuôi và chắc chắn
không còn đường lỗi nào nữa mới burn:
```go
msg, err := engine.Outbound(tx.FromAddress(), params, tx.Hash())
if err != nil {
    return nil, nil, err   // chưa đụng balance, an toàn revert
}
if params.AssetID == nil || params.AssetID.Sign() == 0 {
    totalDeduct := ...
    if totalDeduct.Sign() > 0 {
        if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 1, totalDeduct, tx.FromAddress(), tx.ToAddress()); err != nil {
            return nil, nil, fmt.Errorf("outbound native burn failed: %w", err)
        }
    }
}
```
Kiểm tra kỹ: sau đoạn burn này trong case `"outbound"`, phần code còn lại (đóng gói event log,
`engine.MessageStatus[...] = ...` nếu có) có return lỗi ở nhánh nào không — nếu có, phải dời lên
trước burn, hoặc đảm bảo nhánh đó không thể fail.

**Test bắt buộc phải thêm (chứng minh đúng lỗi này, không phải test khác né được):**
`TestGatewayHandler_OutboundFailsHopCountExceededDoesNotBurn` — seed sender có balance thật đủ,
gọi `outbound()` với `HopCount > MaxHopCount`, assert: (a) transaction fail, (b) balance sender
**không đổi** (giữ nguyên số dư seed ban đầu, không bị trừ dù 1 đồng).

---

## Lỗi 2 (CRITICAL, double-mint qua "claim lại được") — mint chạy trước bước có thể fail

**Vị trí:** cả 2 case `"claimMessage"` (dòng ~465-521) và `"verifyAndExecute"` (dòng ~892-960) có
cùng shape: `processNativeMintBurnForGateway` (mint `msg.Value` cho `msg.Target`, dòng ~492/929;
mint `msg.Tip` cho relayer, dòng ~498/935) chạy **trước** `executeContractCallForGateway` (dòng
~506/942, chỉ chạy nếu `len(msg.Payload) > 0 && msg.Target != 0`). Nếu contract-call fail (trả
lỗi), hàm return lỗi ngay tại chỗ đó — **bỏ qua hoàn toàn `saveGatewayEngine` ở dòng 1002**. Hệ
quả: `engine.MessageStatus[messageID]` (được `ClaimMessage`/`gateway.go` dòng ~641 set
`= MessageStatusSuccess`) và `AttestedCommit.ClaimedAmount` (dòng ~603-604) chỉ tồn tại trong
struct `engine` ở bộ nhớ tạm của lần gọi này — **không bao giờ được ghi xuống**, message coi như
**chưa từng được claim** theo góc nhìn của ledger đã lưu. Nhưng tiền thật (`msg.Value` +
`msg.Tip`) **đã** được cộng vào `AccountStateDB` thật, không rollback được. → cùng một message có
thể `claimMessage` lại nhiều lần, mỗi lần lại mint thật thêm một lượng `msg.Value + msg.Tip`.

**Đây chính là nguyên nhân của 2/3 test đang fail** (`TestGatewayHandler_VerifyAndExecute_Lifecycle`,
`TestGatewayHandler_AttestCommitThenClaimMessage`): cả hai dùng `Target` là một địa chỉ thường
(không phải contract thật) nhưng `Payload` non-empty, nên `executeContractCallForGateway` luôn
fail với `HALTED: NONE` — chạm đúng lỗi 2 này ngay trong CI.

**Vấn đề gộp (không phải bug riêng, nhưng là lỗi thiết kế góp phần):** code hiện tại coi bất kỳ
message nào có `Payload` non-empty **và** `Target` khác zero là "CONTRACT_CALL" và ép chạy qua
MVM (`mvmE.Execute`). Không có field/flag nào trong `CrossChainMessage`/ABI phân biệt "đây thực
sự là lệnh gọi contract" khỏi "message chuyển giá trị thường có kèm data phụ không nhằm thực thi"
— đúng kịch bản 2 test có sẵn ở trên. Đây là điểm cần **quyết định thiết kế**, không tự đoán:
- Phương án A: thêm field mới `IsContractCall bool` (hoặc dùng `AssetID`/một enum
  `MessageKind`) vào `CrossChainMessage`, set tường minh ở `outbound()` khi người gọi thật sự
  muốn CONTRACT_CALL — ABI-breaking, phối hợp với các thay đổi ABI khác đang chờ (xem Phase 0.6
  mục 2 trong readiness plan về field `recipient`).
- Phương án B: chỉ coi là CONTRACT_CALL khi `Target` có code thật đã deploy (kiểm tra
  `SmartContractDB` trước khi thử `Execute`) — không cần đổi ABI, nhưng phải cẩn thận: một target
  address được deploy SAU khi message được tạo (giữa lúc outbound và lúc claim) có thể đổi hành vi
  không mong muốn — cần nêu rõ trade-off này khi chọn.

**Dừng lại và hỏi trước khi chọn A hay B** (đúng tinh thần "How to work" của readiness plan).

**Fix chung cho lỗi 2 sau khi đã có flag/tiêu chí phân biệt CONTRACT_CALL:** đảo thứ tự — chạy
`executeContractCallForGateway` **trước**, kiểm tra lỗi, **rồi mới** mint `msg.Value`/`msg.Tip`.
Áp dụng cho cả `"claimMessage"` và `"verifyAndExecute"`:
```go
if isContractCall(msg) {
    if err := executeContractCallForGateway(ctx, chainState, tx, blockTime, msg.Sender, msg.Target, msg.Payload, big.NewInt(0), tx.MaxGas()); err != nil {
        return nil, nil, fmt.Errorf("... payload execution failed: %v", err)
    }
}
// chỉ tới đây mới mutate balance thật — không còn đường lỗi nào phía sau nữa
if (msg.AssetID == nil || msg.AssetID.Sign() == 0) && msg.Value != nil && msg.Value.Sign() > 0 {
    ...
}
if msg.Tip != nil && msg.Tip.Sign() > 0 {
    ...
}
```
Rồi kiểm tra phần code còn lại phía sau (đóng gói `MessageStatusChanged` event, dòng ~511+/945+)
không còn nhánh nào return lỗi — nếu có (ví dụ `event.Inputs.NonIndexed().Pack` lỗi), phải xử lý
không-fail-closed (log rồi tiếp tục) thay vì `return nil, nil, err`, vì tại điểm đó balance thật
đã đổi và không được phép revert nữa.

**Test bắt buộc phải thêm:** một test dùng `Target` **không phải** contract (như 2 test đang có
sẵn) với `Payload` non-empty nhưng flag/tiêu chí CONTRACT_CALL = false → phải **claim thành công**,
mint đúng 1 lần, `MessageStatus` lưu thành `Success` thật (đọc lại từ `loadGatewayEngine` sau khi
tx commit, không đọc từ struct tạm trong test) — và gọi `claimMessage` lần 2 với cùng message
phải bị reject (`ErrAlreadyClaimed`), **không được mint thêm lần nữa**. Đây là test then chốt
chứng minh lỗi 2 đã hết, không chỉ là test "không crash".

---

## Lỗi 3 (CRITICAL, double-mint không cần lỗi gì — xảy ra ngay ở happy path)

**Vị trí:** `gateway.go` dòng ~643-650, bên trong `ClaimMessage` (code cũ, **không đổi** trong
lần thực hiện Task 1 này):
```go
// Disburse tip to relayer (P2.3 & P4.2)
if message.Tip != nil && message.Tip.Sign() > 0 {
    currBal := g.RelayerBalances[relayer]
    ...
    g.RelayerBalances[relayer] = new(big.Int).Add(currBal, message.Tip)
}
```
Đây là cơ chế ledger cũ: tip **tích luỹ** vào `RelayerBalances`, rút sau bằng
`withdrawRelayerTip`. Code Task 1 mới **thêm** một cơ chế song song: mint `msg.Tip` bằng tiền
thật **ngay lập tức** cho `tx.FromAddress()` tại `gateway_handler.go` dòng ~498/935 (trong cùng
lần gọi `claimMessage`/`verifyAndExecute`) — nhưng **không xoá/bỏ qua** đoạn code cũ ở trên. Kết
quả: relayer nhận tip 2 lần — một lần thật ngay lúc claim, một lần nữa khi gọi
`withdrawRelayerTip()` sau đó (dòng ~982-991 của `gateway_handler.go`, gọi
`engine.WithdrawRelayerTip` → đọc đúng `RelayerBalances` đã bị cộng ở trên → mint thật lần 2).

**Test `TestGatewayHandler_WithdrawRelayerTip` không phát hiện được** vì nó tự gán thẳng
`engine.RelayerBalances[relayer] = big.NewInt(500)` rồi save, thay vì đi qua một `claimMessage`
thật — che mất đúng lỗi này (test tự set giá trị mà production code lẽ ra phải tự derive, đúng
pattern mà `cross_chain_production_readiness_plan.md` mục "How to work" đã cảnh báo).

**Fix — chọn một trong hai, không giữ cả hai (dừng lại, xác nhận trước khi chọn nếu không chắc):**
- **Phương án khuyến nghị:** bỏ đoạn mint tip tức thời mới thêm ở `gateway_handler.go`
  (dòng ~496-501 và ~933-938), giữ nguyên cơ chế tích luỹ + `withdrawRelayerTip` cũ — đây là thiết
  kế gốc, ít thay đổi hơn, và tách bạch rõ "tiền của relayer" khỏi luồng claim (dễ audit hơn).
- **Phương án khác:** giữ mint tức thời, xoá hẳn đoạn cộng `RelayerBalances` trong
  `gateway.go` (~643-650) và xoá method `WithdrawRelayerTip`/case `"withdrawRelayerTip"`/entry ABI
  `withdrawRelayerTip` (vì không còn gì để rút nữa) — nhưng việc này xoá luôn khả năng tích luỹ
  nhiều tip nhỏ rồi rút một lần (tiết kiệm gas cho relayer), cân nhắc trade-off này trước khi
  chọn.

**Test bắt buộc phải thêm:** một test thực hiện **thật** `claimMessage` với tip > 0, rồi gọi
`withdrawRelayerTip` ngay sau đó (không tự set `RelayerBalances` tay), assert tổng số dư relayer
nhận được **đúng bằng đúng 1 lần tip**, không phải 2 lần.

---

## Sau khi sửa cả 3 lỗi trên

1. `go build ./... && go vet ./... && go test ./...` từ `execution/` — **0 fail, 0 regression**,
   bao gồm cả 3 test đang fail hiện tại (`TestGatewayHandler_VerifyAndExecute_Lifecycle`,
   `TestGatewayHandler_AttestCommitThenClaimMessage`, `TestGatewayHandler_ClaimMessageMintsRealValueAndTip`)
   và 3 test mới bắt buộc thêm ở trên phải pass, đúng thứ tự: viết test trước khi sửa code (test
   phải fail đúng vì lý do mô tả, không phải vì lý do khác), sửa code, test pass.
2. `gofmt -l` sạch trên các file đã sửa.
3. Việc còn lại của Task 1 (chưa làm trong lần này, không phải phạm vi sửa lỗi ở trên, xem
   `cross_chain_full_implementation_plan.md` Task 1.2/1.3 để biết chi tiết):
   - Task 1.2: `AssetRegistryEngine.LockAndBridgeAsset`/`ReceiveAndSettleAsset` vẫn chưa nối
     balance thật — `asset_registry.go` không đổi trong lần thực hiện này.
   - Task 1.3: `executeContractCallForGateway` hiện dùng thẳng `tx.MaxGas()` của relayer, chưa có
     cơ chế khoá "cross-chain gas" tại `outbound()` + hoàn phần dư theo đúng kiến trúc mục 2.6.5 —
     việc chọn flag CONTRACT_CALL ở Lỗi 2 phía trên là bước đầu cho việc này, nhưng gas-lock thật
     sự vẫn chưa làm.
4. **Acceptance test cuối cùng** của toàn bộ Task 1 (không chỉ 3 lỗi trên) vẫn là test thật đã mô
   tả ở Phase 0.6 của `cross_chain_production_readiness_plan.md`: khởi động 2 chain thật + Root
   Anchor thật, quan sát `eth_getBalance` thật đổi trên RPC thật — chưa có test này thì Task 1
   coi như chưa xong, dù 3 lỗi trên đã sửa.
5. Commit theo đúng quy trình ở "How to work": branch từ `dev`, PR qua `gh`, không tự merge. Tiêu
   đề PR gợi ý: `fix(cross-chain): native value-transfer wiring — burn-before-validate fund loss,
   claim-replay double-mint, relayer-tip double-mint`.
