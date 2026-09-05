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
