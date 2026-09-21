# Cross-Chain Architecture Audit Report: 2-Hop Routing & Reliability

> ## ⚠️ VERIFICATION ADDENDUM (2026-09-21) — read this before acting on sections 2–6
>
> Every claim below was re-checked against the code and against a live-state trace of the real
> `claimMessage`/`refund` handlers. The headline finding does **not** hold; a different, smaller
> defect does. Sections 2–6 are kept unedited for the record.
>
> **Refuted — "permanent lock of funds on the Reserve chain".** When leg 2 fails on B, the relayer
> calls `refund()` on **Reserve** (leg 2's source chain is Reserve). The handler mints the Value to
> the original sender (`leg2.Sender` keeps the original sender) **on Reserve** — verified: sender
> balance on Reserve = V after the refund. Funds are refunded on Reserve rather than returned to A;
> they are not trapped. (`RefundReserveAllocation` is a separate path for direct A→B messages that
> Reserve only ceiling-attests; it is not what a relayed leg-2 failure uses.)
>
> **Refuted — "Option A (immutable `SourceChainID`) is mandatory".** An uncommitted attempt to
> carry A in leg 2's `SourceChainID` broke `CreditReserveAllocation`/`RefundReserveAllocation`
> tests and, more importantly, would make B look up the commit attestation and committee of the
> wrong chain: leg 2's batch is produced and signed by **Reserve**, so `SourceChainID` must stay
> Reserve for the verification chain to be sound. Not adopted.
>
> **Confirmed and fixed — Reserve's `PerChainAllocation` ledger drifts on every relayed transfer**
> (fail-closed on success, **inflating** on failure). Traced with V=500, ledger A=Reserve=50_000:
>
> | step | before fix | after fix |
> |---|---|---|
> | leg 1 attested + claimed on Reserve | A 49_500, Reserve 50_500 | A 49_500, Reserve **50_000** (value released: it is in flight) |
> | leg 2 succeeds, `creditReserveAllocation` | **rejected** (`ErrCommitNotAttested`: Reserve never attests its own commits) → B never credited, Reserve stuck at +V | B **+V**, Σ conserved |
> | leg 2 fails, `refund()` on Reserve | Reserve 51_000 (**+2V**; only V was minted) → Σ inflated by V | Reserve 50_500, Σ conserved |
>
> Fix: `GatewayEngine.ReleaseRelayedValue` (called by the claimMessage relay branch, Reserve-only,
> idempotent, recorded in `RelayedInFlight`) + `CreditReserveAllocation` accepts Reserve's own
> `CommittedBatches` **only** for a message recorded as relayed with the identical Value (so an
> ordinary Reserve-issued transfer can never be credited without a matching release). Tests:
> `pkg/cross_chain/gateway_relay_ledger_test.go`, plus ledger/balance assertions added to
> `TestComprehensive_TwoHopValueTransfer_*` / `*_LegTwoFailsAndRefundsOnReserve`.
>
> **Deployment note:** this changes state-machine semantics (ledger arithmetic on Reserve), so it
> must be rolled out to all validators of a chain together, like any consensus-affecting change. The
> new state field is `omitempty`, so a chain that never relayed serializes byte-identically. A relayed
> message already in flight at upgrade time has no `RelayedInFlight` record: its success-path credit
> stays rejected and its failure-path refund keeps the old +V double count (no regression, not fixed).
>
> **Adjacent, NOT fixed (separate flow, needs its own decision):** an ordinary Reserve-issued native
> transfer (not relayed) never debits Reserve's ledger on issue (`attestReserveIssuedCommit` skips
> the ceiling), yet `refund()` on Reserve credits it back on failure — the same +V inflation class.
> Also `refund()` returns the value on Reserve, not on A; routing it back to A would need an
> explicit design (original-source record on Reserve), not an overwritten `SourceChainID`.


## 1. Executive Summary
This audit focuses on the logical and security soundness of the cross-chain architecture, particularly the 2-hop routing mechanism (Chain A -> Reserve -> Chain B) introduced for `Native` assets and ceiling-enforced commits.

**Finding:** The architecture suffers from a **Critical Logic/Security Flaw** in the 2-hop routing lifecycle that results in a **permanent lock of user funds** on the Reserve chain when a Leg 2 message fails (e.g., business-logic revert on the destination chain), and similarly permanently breaks the Reserve's ledger allocation synchronization for successful transfers.

## 2. Vulnerability Details: The Lost SourceChainID in 2-Hop Routing

The vulnerability stems from an impedance mismatch between the `CrossChainMessage` data structure, the Relayer Daemon's context, and the Gateway Engine's verification logic.

### 2.1 Context & Message Lifecycle
1. **Leg 1 (Chain A -> Reserve):** User initiates a cross-chain transfer on Chain A destined for Chain B. A `CrossChainMessage` is emitted with `SourceChainID = A` and `DestChainID = B`.
2. **Reserve Routing:** The Relayer for (A <-> Reserve) claims this message on the Reserve chain. The `claimMessage` logic on the Reserve detects this is a 2-hop message and immediately queues a new `Outbound` message (Leg 2) to Chain B.
3. **Leg 2 (Reserve -> Chain B):** The new `CrossChainMessage` is queued. While it correctly preserves the original `MessageID` (thanks to the `OriginalID` fix), its `SourceChainID` is overwritten to `Reserve` (g.LocalChainID), and `DestChainID` remains `B`. **The original `SourceChainID` (A) is not stored anywhere in state or in the new message.**

### 2.2 The Relayer Daemon Failure
If the Leg 2 message fails on Chain B (e.g., payload reverted), Chain B issues a Failure Quorum Certificate over `(Original MessageID, DestChainID=B)`.
The Relayer Daemon (monitoring B) catches this failure and attempts to call `RefundReserveAllocation` on the Reserve chain.
However, the Relayer only has access to the **Leg 2 message** and fetches the **Leg 2 proof** from the Reserve's own `BatchOutboundCommit` tree.

### 2.3 The Contract Logic Failure
When the Relayer submits the Leg 2 message and proof to `RefundReserveAllocation` (or `CreditReserveAllocation` for success), the transaction will unconditionally fail, trapping the funds indefinitely.

There are two fatal errors in the contract logic:

1. **Incorrect State Verification (`ErrCommitNotAttested`):**
   `RefundReserveAllocation` verifies the proof against `g.AttestedCommits`.
   ```go
   key := fmt.Sprintf("%d:%s:%s", message.SourceChainID, commitRoot.Hex(), assetIdStr)
   if _, exists := g.AttestedCommits[key]; !exists { ... }
   ```
   Since the Relayer submits the Leg 2 message, `message.SourceChainID = Reserve` and the `commitRoot` is from Reserve's own `BatchOutboundCommit`. Commits produced by the chain itself are stored in `g.CommittedBatches`, not `g.AttestedCommits`. The check will always fail.

2. **Destructive Refund Routing:**
   Even if the verification logic was patched to check `g.CommittedBatches`, `RefundReserveAllocation` emits the refund outbound message back to `message.SourceChainID`:
   ```go
   refundMsg := CrossChainMessage{
       SourceChainID: g.LocalChainID, // Reserve
       DestChainID:   message.SourceChainID, // Reserve!
       ...
   }
   ```
   Because the Leg 2 message lost the original SourceChainID, the refund is queued for `DestChainID = Reserve` itself. It completely loses track of Chain A. The user's funds are permanently trapped on the Reserve chain with no possible mechanism to route them back.

## 3. Impact
- **Permanent Lock of Funds:** Any 2-hop transfer that fails on the destination chain will permanently lock the native value on the Reserve chain. It can never be refunded to the user on Chain A.
- **Ledger Desynchronization (Denial of Service):** `CreditReserveAllocation` suffers from the exact same `ErrCommitNotAttested` verification bug. When a 2-hop transfer succeeds, the Reserve chain's ledger is never updated to credit the destination chain. Over time, chains will hit their `PerChainAllocation` ceilings, halting the entire cross-chain network.

## 4. Codebase Verification (Confirmed)

A deep code analysis on the current implementation confirms these vulnerabilities:

1. **Loss of SourceChainID (Leg 2 Creation):**
   In `execution/pkg/cross_chain/gateway.go`, function `Outbound` (line 898):
   ```go
   msg := &CrossChainMessage{
       MessageID:     messageID,
       SourceChainID: g.LocalChainID, // Overwritten to Reserve
       DestChainID:   params.DestChainID,
   ```
2. **Incorrect Proof Verification:**
   In `execution/pkg/cross_chain/gateway.go`, function `RefundReserveAllocation` (line 1793):
   ```go
   key := fmt.Sprintf("%d:%s:%s", message.SourceChainID, commitRoot.Hex(), assetIdStr)
   if _, exists := g.AttestedCommits[key]; !exists {
       return fmt.Errorf("%w: commit %s on chain %d", ErrCommitNotAttested, commitRoot.Hex(), message.SourceChainID)
   }
   ```
   This strictly checks `g.AttestedCommits` using the `Reserve` ID, which will definitively fail.

3. **Destructive Refund Routing (Trapped Funds):**
   In `execution/pkg/cross_chain/gateway.go`, function `RefundReserveAllocation` (line 1853):
   ```go
   refundMsg := CrossChainMessage{
       MessageID:     txHash,
       SourceChainID: g.LocalChainID,
       DestChainID:   message.SourceChainID, // Routes back to Reserve itself
   ```

## 5. Recommended Architectural Fixes

This requires an architectural adjustment to preserve the original source chain context across the entire 2-hop lifecycle.

**Option A: Add `OriginalSourceChainID` to `CrossChainMessage`**
- Modify the `CrossChainMessage` struct to explicitly include `OriginalSourceChainID`.
- During `Outbound` on the Reserve chain (Leg 2), set this field to Chain A.
- Update `RefundReserveAllocation` to route the refund to `message.OriginalSourceChainID` and verify the Leg 2 proof against `g.CommittedBatches`.
- *Pros:* Fully stateless, Relayer friendly.
- *Cons:* Breaks ABI compatibility across the entire cross-chain protocol.

**Option B: Stateful Original Source Tracking on Reserve**
- When `claimMessage` on the Reserve chain processes Leg 1 and queues Leg 2, it should record a mapping in state: `g.OriginalSourceChains[MessageID] = Leg1.SourceChainID`.
- Update `RefundReserveAllocation` and `CreditReserveAllocation` to verify the Leg 2 proof against `g.CommittedBatches`.
- For `RefundReserveAllocation`, look up the original source chain via `g.OriginalSourceChains[message.MessageID]` to set the correct `DestChainID` for the refund message.
- *Pros:* Maintains `CrossChainMessage` ABI compatibility.
- *Cons:* Increases state bloat on the Reserve chain.

## 6. Bài học kiến trúc từ TON (The Open Network)

Vì kiến trúc phân mảnh của MetaNode được lấy cảm hứng trực tiếp từ TON (như đặc tả trong `shard_design_ton_real.md`), việc học hỏi cơ chế xử lý multi-hop và hoàn tiền (refund) của TON là hướng đi tự nhiên và tương thích nhất. 

Trong hệ sinh thái TON, giao tiếp chéo shard (thông qua Masterchain) giải quyết triệt để lỗi "mất dấu nguồn gốc" bằng 2 nguyên tắc cốt lõi (chính là bản chất của **Option A**):

### 1. Tính Bất Biến Của Định Danh Nguồn (Immutable Source Address)
Cấu trúc gói tin (`Message`) trong TON luôn mang theo trường `src` (Source) và `dest` (Destination). Định danh này bao gồm `workchain_id` (tương đương `ChainID`) và địa chỉ tài khoản.
- **Giải quyết Context Loss:** Khi một tin nhắn từ Shard A đi qua Masterchain (đóng vai trò tương tự Reserve 991 của MetaNode) để đến Shard B, Masterchain **KHÔNG BAO GIỜ** ghi đè trường `src`. Gói tin giữ nguyên `src = A` và `dest = B` xuyên suốt hành trình định tuyến. Masterchain chỉ đóng vai trò là "người đưa thư" (Transport/Router Layer), không phải là người gửi mới.
- **Áp dụng cho MetaNode:** Cấu trúc `CrossChainMessage` bắt buộc phải coi `SourceChainID` và `DestChainID` là các trường **BẤT BIẾN** (immutable) đại diện cho người gửi ban đầu và đích đến cuối cùng. Việc Reserve (ở Chặng 2) ghi đè `SourceChainID = Reserve` (g.LocalChainID) là hành động vi phạm nguyên lý định tuyến. MetaNode cần triển khai thiết kế bất biến này, biến **Option A** thành giải pháp bắt buộc.

### 2. Cơ chế Hoàn Tiền Tự Nhiên (Bounced Messages)
Thay vì tạo ra các hàm xử lý hoàn tiền phức tạp và dễ lỗi như `RefundReserveAllocation` hiện tại, TON xử lý giao dịch thất bại thông qua khái niệm **Bounced Messages** (Tin nhắn dội lại).
- **Cơ chế hoạt động:** Nếu Shard B từ chối tin nhắn (do lỗi logic contract hoặc sai định dạng), nó sẽ kích hoạt cờ `bounced = true` và sinh ra một tin nhắn trả về. Tin nhắn này đơn giản chỉ đảo ngược địa chỉ: `new_dest = original_src` và `new_src = original_dest`. 
- **Tại sao nó hoàn hảo cho MetaNode?** Nếu MetaNode sửa được lỗi ghi đè `SourceChainID` (áp dụng nguyên tắc #1 ở trên), thì hàm `RefundReserveAllocation` trên Reserve sẽ hoạt động tự nhiên như một Bounced Message. Khi đó, `message.SourceChainID` vẫn đang giữ giá trị là Chain A. Việc lệnh `DestChainID = message.SourceChainID` (như code hiện tại đang viết) sẽ tự động sinh ra tin nhắn đẩy tiền thẳng về Chain A. Toàn bộ quá trình diễn ra stateless, hoàn toàn không cần lưu trữ Mapping (Option B) trên bộ nhớ của Reserve, giúp kiến trúc giữ được sự thanh thoát tối đa.
