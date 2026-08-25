# Root Anchor Cross-Chain Bridge — Production Readiness Plan

Status as of 2026-08-25: Milestones **A through I** are implemented and merged into
`dev`, plus 3 critical bugs found across 2 review passes are fixed and merged:

- PR #56 — Milestone E's `attestCommit()` verified `aggregateAmount`'s proof against the
  wrong root; Milestone G's `vote()` had no sender authentication at all.
- PR #58 (**Phase 0 of this plan**) — `claimDeadChainBalance()` had the same shape of bug
  as PR #56's Milestone E fix: verified an account proof against a root nothing could
  produce a real proof for. Fixed with a new, separate `ChainRegistry.AccountTreeRoot`
  field, committee-attested via the same real BLS quorum flow as everything else
  `CommitteeUpdate` commits.

**Phase 0 is done — this document now picks up at Phase 1.** If you're starting fresh,
read `note/cross_chain_root_anchor_architecture.md` (the design doc) and
`note/cross_chain_wiring_plan_next_steps.md` (the plan that produced Milestones E-I)
first; this document assumes both.

**Update 2026-08-25, 4th review pass — Phase 0.6 (below) found the most important gap in
this entire document: nothing implemented so far actually moves real value.** All of
Milestones A-I is a real, well-tested BLS/Merkle verification and anti-fraud layer around
an in-memory ledger (`GlobalSupplyLedger`, `AssetRegistryEngine`'s vault/circulation maps)
that has never been connected to `AccountStateDB`/`VmProcessor.ProcessNativeMintBurn`.
**Read Phase 0.6 before anything else in this document** — everything from Phase 1 onward
below was written assuming value-transfer worked and just needed hardening/staging; that
assumption was wrong, and Phase 0.6 must land (with its own real-balance end-to-end
acceptance test) before Phase 2's T2 testnet run means anything for value transfers.

**Bottom line up front: this system is still not production-ready.** Real cryptographic
verification exists and is unit/integration tested, and the two known proof-binding bugs
are now fixed — but nothing has run on a real multi-node network yet, no external security
review has happened, and (Phase 0.6) the verified ledger this all protects has never been
wired to a real spendable balance on either end. **3 critical bugs found across 2 internal
review passes on freshly-written code is a base rate, not bad luck** — do not skip the
gates below to save time, and stay suspicious of any new cryptographic/authorization code
the same way.

---

## Phase 0.5 — critical finding + in-progress work, found and FIXED same day (2026-08-25, 3rd review pass)

> ✅ **RESOLVED 2026-08-25, same day as found.** Item 1's fix (real `MerkleProof` +
> destination `QuorumCert` binding, `VerifyQuorumCertAgainstRegistry` extracted and reused,
> `ProcessRefund`'s redundant unchecked Reserve credit removed) is committed in the **same
> commit/PR** as this writeup — satisfies the responsible-disclosure rule below, which is
> kept as the record of why this section was held back from `git commit`/push until the fix
> landed. Verified before commit: `go build ./...`, `go vet ./...`, `gofmt -l` clean, full
> `go test ./...` (whole `execution/` module) 100% pass including the new exploit-reproduction
> regression tests in `security_audit_test.go`'s `TestAudit_AntiDoubleMintViaRefundRaceGuard`;
> `cargo build --release` clean for the `consensus/metanode/src/config.rs` change. Items 2
> and 3 below are also fixed (register_chains tool rewritten and compiles; propose() fee
> tests updated, all pass).
>
> **Original responsible-disclosure note (kept for context):** `x3pi/metanode` is a
> **public** GitHub repo — item 1 documented a live, unpatched, exact-reproduction-steps
> exploit against code already on `dev`. The rule that applied while unfixed: never
> `git commit`/`push`/open a PR containing this section unless the fix is included in that
> same commit/PR; keep it local/uncommitted otherwise. That gate is why this file sat
> uncommitted from when the bug was found until the fix above landed.

Found while reviewing the current state ahead of a T2 testnet push. Item 1 is the same bug
shape as PR #56/#58 (see `git log`/PR history for the exact fix commit once pushed).

### 1. CRITICAL (FIXED) — `GatewayEngine.Refund()` minted allocation from nothing

`execution/pkg/cross_chain/gateway.go:638-673`. This is the **4th instance of the exact
same bug class** as PR #56 (Milestone E `attestCommit`) and PR #58 (Phase 0
`claimDeadChainBalance`): a value that must be cryptographically bound to committee-attested
state is instead trusted directly from the caller.

```go
func (g *GatewayEngine) Refund(
    messageID common.Hash, sourceChainID uint64, sender common.Address,
    amount *big.Int, isFailedProofValid bool,
) error {
    ...
    if !isFailedProofValid {                 // ← caller-supplied bool, zero crypto/state checks
        return ErrInvalidRefundProof
    }
    g.MessageStatus[messageID] = MessageStatusRefunded
    ...
    g.SupplyLedger.PerChainAllocation[sourceChainID] =        // ← raw add, no matching debit anywhere,
        new(big.Int).Add(currAlloc, amount)                   //   no VerifyInvariant() call (contrast
                                                                //   with TransferAllocation, types.go:131,
                                                                //   used correctly by ClaimDeadChainBalance)
```

**Exploit:** call `refund` (`gateway_handler.go:338`, reachable as a normal write tx, no
extra auth) with any never-before-seen `messageID`, any `sourceChainID`, any `amount`, and
`isFailedProofValid=true`. `MessageStatus[messageID]` defaults to `Pending` for an unknown
ID, so the status check passes trivially; the bool check passes trivially; `amount` is
credited to that chain's `per_chain_allocation` with nothing debited anywhere. That
inflated allocation then passes `AttestCommit`'s ceiling check
(`gateway.go:516`), letting far more value be bridged out of that chain than it was ever
entitled to. `RelayerEngine.ProcessRefund` (`relayer.go:587,600`) makes it worse: it
forwards the same unverified bool into `Refund()` *and* separately applies a second,
independent unchecked credit to Reserve's own ledger from one call.

`security_audit_test.go:290`'s existing `Refund` test ("Anti-Double-Mint via Refund
Pathway Race Guard") only covers the double-claim race and the trivial
`isFailedProofValid=false` rejection — it never exercises the real attack (unused
messageID + `true`), so CI is currently green despite this being live.

**Why this needs a design decision, not a quick patch (stop and confirm before
implementing, per this doc's own "how to work" section):** unlike `AttestCommit`/
`ClaimMessage`, there is currently **no per-message ledger entry** recording how much was
reserved for a specific `messageID` — `attestCommitInternal` (gateway.go:516-521) debits
`sourceChainID` for the whole commit's `aggregateAmount` in one shot; `ClaimMessage`
(gateway.go:602-607) credits `LocalChainID` per message, capped in total by
`AttestedCommit.FundedAmount`. So a correct fix needs `Refund()` to take a real
`CrossChainMessage` + `MerkleProof` (bound to an already-`AttestedCommit`'s `commitRoot`,
the same way `ClaimMessage` does) so `amount` can't be fabricated, **plus** a real
`QuorumCert` from the *destination* chain's committee attesting that this specific message
genuinely failed execution there — this is exactly what
`note/cross_chain_root_anchor_architecture.md` mục 2.4 originally specified ("relayer lấy
quorum cert của B xác nhận message X FAILED, mang về A") and what got short-circuited with
a trust-the-caller bool. The destination-attests-failure digest/QuorumCert wire format
isn't designed yet — that's the actual open question, same category as Phase 0's
`AccountTreeRoot` decision. Also decide: does `ProcessRefund`'s second, independent Reserve
credit make sense at all once `Refund()` itself is fixed, or was it compensating for the
same gap and should be removed?

**Definition of done:** `Refund()` fails closed on a forged/unused messageID or a message
that was never part of any attested commit; a new regression test proves the exact
exploit above (unused messageID, arbitrary amount, `isFailedProofValid=true`) is rejected,
following the "what would make this test pass without the real thing being true"
philosophy from this doc's own testing section. PR title should follow the existing
`fix(cross-chain): ...` convention; this is Phase 0's sibling, not a Phase 1 item.

### 2. (FIXED) `execution/cmd/tool/register_chains/main.go` was corrupted, doesn't compile

New/untracked, does not compile: `go build ./...` fails with `package base64 is not in
std` and `package io/outintil is not in std`, and the `gatewayAbiJSON` string literal in
this file is corrupted (binary garbage from roughly line 33 onward, not valid JSON) —
this isn't a typo, the file needs to be rewritten. Recommend reusing the existing, already
machine-verified ABI in `execution/pkg/blockchain/tx_processor/abi_contract/gatewayAbi.go`
instead of hand-duplicating the ABI JSON a second time (avoids this exact class of
corruption happening again). Confirm what this tool is for (name suggests: register the
private chains created by `deploy/systemd/register_private_chains_t2.py/sh` — also new,
uncommitted — with Root Anchor) and finish it as part of the Phase 1 item 2 deployment
tooling smoke-test, since it looks like part of the same T2 prep work.

### 3. (FIXED) `propose()` anti-spam fee had broken 4 existing tests

`execution/pkg/blockchain/tx_processor/gateway_handler.go` now requires `tx.Amount() >=
0.1 native token` for `propose` (addresses Phase 1 item 4's open question — good, but
needs finishing). Currently breaks, because they call `propose` with zero value:
`TestGatewayHandler_ClaimDeadChainBalance_Lifecycle`,
`TestGatewayHandler_Vote_RejectsUnauthenticatedImpersonation`,
`TestGatewayHandler_Governance_OnboardNewChainLifecycle`,
`TestGatewayHandler_Governance_AssetRegistrationLifecycle` (all in
`pkg/blockchain/tx_processor/`, confirmed via `go test ./pkg/blockchain/tx_processor/...`).
Update these tests to attach the fee, and double check the hardcoded
`100_000_000_000_000_000` constant matches this project's actual native-coin decimals
convention (confirm against `mt_common`'s existing amount constants rather than assuming
18 decimals). Per this doc's own verification bar, this must not land with red tests.

### Verification bar for all three items above

Same as the rest of this document: `go build ./... && go vet ./... && go test ./...` from
`execution/` zero regressions, each fix has a regression test proving the specific issue,
PR via `gh` off a branch from `dev`, no self-merge.

---

## Phase 0.6 — CRITICAL, found 2026-08-25 (4th review pass, while writing the production
## deployment guide): cross-chain value transfer is never wired to any real balance

**This is not a fund-theft exploit** (see "Why this is safe to write up openly" below) —
it is a **missing-functionality finding**, but it is the single most important thing in
this entire document: as implemented today, **no cross-chain transfer — native coin or
custom asset — ever moves a real, spendable unit of value.** Every `outbound()` /
`attestCommit()` / `claimMessage()` / `verifyAndExecute()` / `refund()` /
`claimDeadChainBalance()` call that "succeeds" only ever mutates in-memory Go structs
(`GlobalSupplyLedger.PerChainAllocation`, `AssetRegistryEngine.{Vault,Circulation}Balances`,
`MessageStatus`). None of them call `AccountStateDB.AddBalance`/`SubBalance`, and none of
them call `VmProcessor.ProcessNativeMintBurn` — the exact function
`note/cross_chain_root_anchor_architecture.md` mục 2.4 (lines 245-249) specifies for this:
*"A chạy `ProcessNativeMintBurn(1)` (burn cục bộ, ... `tz_hardware_engine.go`)"* on the
source side, *"B ... `ProcessNativeMintBurn(0)` để mint"* on the destination side.

**Evidence (all confirmed by direct code reading, not inference):**
- `execution/pkg/cross_chain/gateway.go` and
  `execution/pkg/blockchain/tx_processor/gateway_handler.go` import neither `mvm` nor
  `vm_processor` — zero possible call path to `ProcessNativeMintBurn` exists today.
- `outbound()`'s ABI (`gatewayAbi.go`) is `"stateMutability": "nonpayable"` — a caller does
  not even attach real native coin when calling it. `Value` is a free-form `uint256`
  argument with no connection to the caller's actual balance.
- `ClaimMessage` (`gateway.go:550`) has a comment *"// Set execution context for destination
  target contracts"* right where a real `Target`/`Payload` invocation should happen — it
  sets `g.ActiveContext`, does nothing with it, and immediately clears it again.
  `execStatus := MessageStatusSuccess` is unconditional; nothing was actually executed.
- `AssetRegistryEngine.ReceiveAndSettleAsset`'s "Case B: Destination is Remote Chain -> Mint
  wrapped token into circulation" (`asset_registry.go:239-240`) only increments
  `a.CirculationBalances[destCircKey]`, an internal map — it never calls the real wrapped
  ERC-20 contract's `mint`, and the `recipient` parameter it accepts is never used to touch
  any real state.
- `VmProcessor.ProcessNativeMintBurn` (`vm_processor.go:462`) — the function that *does*
  exist, fully implemented, TrustZone-hardware-backed, exactly matching the architecture
  doc's description — has **zero callers** anywhere in the live codebase (confirmed via
  `grep -rn "ProcessNativeMintBurn(" execution/`, only its own definition and the
  `mvm`-layer plumbing beneath it show up).
- `RelayerBalances[relayer]` (the tip mechanism, `gateway.go:632-638`) is credited the same
  way — no `withdraw`-style method exists anywhere in `gatewayAbi.go` to ever turn it into
  real coin.

**Why this is safe to write up openly (no responsible-disclosure lockdown needed, unlike
Phase 0.5):** there is no live exploit here because there is no real value in the system to
steal in the first place — `outbound()` never takes a real balance from anyone, so an
attacker gains nothing spendable by manipulating `claimMessage()`, and neither does a
legitimate user. Every green test in `gateway_test.go`/`relayer_test.go`/
`security_audit_test.go` genuinely proves the **verification/anti-fraud layer** is sound
(BLS, Merkle, replay, double-mint-of-the-*ledger*) — that work is real and necessary — but
none of it proves the bridge moves money, because it never has.

**Fix plan (this is the actual scope of "make the bridge work for real"):**

1. **`GatewayEngine.Outbound`** (or its caller in `gateway_handler.go`'s `"outbound"` case):
   before/alongside building the `CrossChainMessage`, call
   `VmProcessor.ProcessNativeMintBurn(ctx, tx, mvmE, 1 /* burn */)` for `params.Value` from
   `tx.FromAddress()`. Must fail closed (revert the whole tx) if the burn fails — a message
   must never be emitted for value that wasn't actually removed from the sender.
2. **`GatewayEngine.ClaimMessage`** (and the `verifyAndExecute` path that calls it): after
   all existing verification passes (keep every check exactly as-is — this phase is purely
   additive), call `ProcessNativeMintBurn(ctx, tx, mvmE, 0 /* mint */)` crediting
   `message.Value` to the real recipient. Open design question to confirm with the task
   owner before implementing: **who is the recipient** — is it always `message.Sender`
   (same address on both chains), or does the ABI need a new explicit `recipient` field?
   Check `AssetRegistryEngine.LockAndBridgeAsset` — it already threads a separate
   `recipient common.Address` distinct from `sender`, so the native path likely needs the
   same, meaning `CrossChainMessage` needs a new field (ABI-breaking, coordinate with
   Phase 1 item about anything else pending an ABI change).
3. **`GatewayEngine.Refund`**: same mint call, crediting `message.Sender` back on the source
   chain (this is the "give it back to whoever originally burned it" path, per architecture
   mục 2.4 point 2 — the sender identity here is unambiguous, this doesn't have the
   recipient question item 2 does).
4. **`GatewayEngine.ClaimDeadChainBalance`**: same mint call, crediting `account` (the
   parameter already exists and is already verified via the account-tree Merkle proof —
   just isn't consumed).
5. **`AssetRegistryEngine.LockAndBridgeAsset`/`ReceiveAndSettleAsset`**: needs the
   equivalent for ERC-20-style assets — burning/locking means an actual `SubBalance`-style
   call against the *token contract's* storage (via the existing EVM/MVM `Call` mechanism,
   not `ProcessNativeMintBurn` which is native-coin-only), and minting means actually
   invoking the wrapped contract's mint entrypoint. This is more involved than the native
   path (needs a real contract call, not a direct balance primitive) — treat as a separate,
   later task from items 1-4, not a blocker for getting native transfers real.
6. **`CONTRACT_CALL` (`message.Target`/`message.Payload`)**: currently accepted into the
   message struct, hashed into the digest, and never invoked. Per architecture mục 2.6.5,
   this needs the sender to lock a "cross-chain gas" amount at `outbound()` time and
   `ClaimMessage` to actually invoke `Target` with `Payload` via the EVM/MVM `Call`
   mechanism, gas-capped to the locked amount, unused portion refunded, used portion really
   burned (not "free gas" — see the architecture doc's own DoS warning at that section).
   This depends on items 1-2's real-value plumbing existing first — do this after, not
   instead of, the plain value-transfer path.

**Acceptance test — must exist before this item is considered done:** an end-to-end test
(not the existing in-process `gateway_test.go` style, which would hide this exact class of
bug the same way it hid this one) that starts two real chains + a real Root Anchor,
observes `eth_getBalance` on the sender **decrease** after `outbound()` and on the real
recipient **increase** after `claimMessage()`, by exactly the transferred amount, on both
chains' real RPC endpoints. This is the only kind of test that would have caught this gap —
add it to the same T2 real-infra testing this document's Phase 2 already calls for, and
make it block Phase 2 sign-off, not just Phase 1.

**Where this sits relative to the rest of this document:** this must be fixed and its
acceptance test passing **before** Phase 2's T2 testnet run is meaningful for anything
beyond messages (`value=0`) — right now Phase 4's "Stage 2 — value transfers via Reserve,
small caps" describes a rollout stage for a capability that does not exist yet. Do this
work first, then the existing Phase 1-5 sequence below still applies as written.

## Phase 0.7 — PR #63: Phase 0.6 implemented, 6 bugs found+fixed, partial live acceptance
## test run (2026-08-25, same day)

**Code:** `fix/cross-chain-native-value-wiring` → PR #63 (open, not yet merged at time of
writing). Implements Task 1.1 (native burn/mint on `outbound`/`claimMessage`/
`verifyAndExecute`/`refund`/`claimDeadChainBalance`), Task 1.2 (custom asset via real
`transferFrom`/`transfer`/`mint` contract calls), Task 1.3 (`CONTRACT_CALL` execution gated
on the target having real deployed code, `isContractCall`), Task 1.4 (`withdrawRelayerTip`),
and Task 2 (`BootstrapFoundingChainsWithCaller`/`GenesisCoordinator`). Full bug-by-bug
evidence in `note/cross_chain_task1_native_value_fix_plan.md` (its "RESOLVED" header
summarizes all 6: burn-before-validate fund loss, split Tip+Value burn stranding Tip on
partial failure, mint-before-CONTRACT_CALL double-mint-via-replay, relayer Tip double-mint,
2 hard-fail-after-mutation paths, one leftover debug `fmt.Printf`). `go build ./... && go vet
./... && go test ./...` clean across the whole `execution/` module.

**Live acceptance test — partial, real result, not the full round-trip yet:**

Stood up real infra from this repo's own tooling (`deploy/systemd/setup_root_anchor.sh
--clean`, `gen_single_chain.py`): a real 4-validator Root Anchor BFT cluster (chain 9099) and
2 real single-validator private chains (101, 102), all with freshly rebuilt binaries carrying
this PR's fixes. Ran a real `bootstrapFoundingChains()` transaction (throwaway tool, not
committed — see below) with **real BLS PoP verification**, `status: 0x1`. Then ran a real
`outbound()` transaction on chain 101 (native value=1000 wei, real ECDSA-signed tx, real
dev-funded account) and verified the sender's **real balance decreased by exactly gas-fee +
1000 wei** (`50000000001000` = `50000 gasUsed × 1e9 gasPrice + 1000`), read back via a second
real `eth_getBalance` RPC call — not inferred, computed and matched exactly. **This is the
first real evidence in this project's history that a cross-chain transfer moves actual
spendable value on a real running chain**, closing the specific gap Phase 0.6 documented.

**Not yet done:** the destination-side proof (`claimMessage()` minting on chain 102,
`eth_getBalance` increasing there) — blocked by an infra issue below, not a code issue (the
`outbound()` result above already proves the burn side of Task 1.1 works correctly end to
end on real infra). Re-run the same session's `submit_claim_quick`-style flow once the
blocker below is resolved to close this out fully.

**2 real infra findings from this session, both separate from the Gateway code in PR #63:**

1. **Stray/duplicate validator entry from repeated start/kill/regenerate cycling — found,
   explained, not a production bug.** A single-validator chain regenerated via `gen_single_chain.py`
   after an earlier failed attempt (same output dir) intermittently ended up with **2**
   validator entries in its stake-state DB instead of 1 (`GetAllValidators()` returned a
   phantom entry with a different address/stake than genesis.json's real one), which
   permanently blocked block production: the node tried to reach the phantom validator's
   p2p address as a peer for its own block-sync quorum and could never succeed (`Not enough
   stake: 0 out of 3000 total stake`). Root-caused by testing: a **fully clean** single-shot
   regenerate-then-start (kill everything, `rm -rf` the whole chain dir, generate once, start
   once — no restart cycling) reliably produces exactly 1 validator and blocks flow
   immediately. Exact origin of the phantom entry within a dirty-restart sequence not traced
   further (not needed once the clean-start workaround was confirmed reliable twice). **Actionable
   takeaway for whoever runs T2 next:** never `rm -rf` + regenerate a chain's data dir into the
   same path while any process from a previous attempt at that path might still be alive or
   mid-shutdown — kill and confirm-dead first, every time.
2. **NOT YET ROOT-CAUSED — Root Anchor executor "CatchingUp phase" livelock on a fresh
   4-validator batch start.** On a second clean regeneration (to align chain 101/102's
   committee keys with a fresh bootstrap after finding #1 above), the 4-validator Root Anchor
   cluster's consensus DAG kept committing rounds normally and fast (`commit_index` observed
   climbing 512 → 1735+ over ~3 minutes, real BFT activity, not deadlocked) while the Go
   executor stayed stuck logging `🛡️ [PHASE-GUARD] Blocking local committer. Node is in
   CatchingUp phase (startup_sync_active=true)` **12,000+ times** without ever exiting that
   phase — `eth_blockNumber` never moved past `0x1` in that window. No noisy-neighbor process
   this time (confirmed via `ps aux --sort=-%cpu`, machine was otherwise idle) — different
   trigger than the earlier documented restart-hang (Phase 1 item 2's "Investigated live, root
   cause found — environmental" note), since this was a **fresh** start (`--clean`), not a
   restart from persisted state. Best guess, not confirmed: launching 4 heavy validator
   processes near-simultaneously via a backgrounding loop (`start_all.sh`'s `&`) may let the
   DAG race far enough ahead before the executor's first catch-up pass that its exit condition
   never triggers — needs real Rust-side tracing of `commit_manager`'s `PHASE-GUARD` exit logic
   to confirm, not guessed at further here. **This is what blocked the destination-side
   `claimMessage()` proof above** — chain 101/102 themselves were fine (single-validator,
   confirmed working, see finding #1's resolution) but couldn't get real `ChainRegistry` data
   from Root Anchor while it was stuck. Whoever picks this up: reproduce with a staggered
   start (start node_0, wait for it to be ready, then node_1, etc.) to test the race-condition
   theory before diving into Rust source.

   **Update, same day — root-caused finding #2 down to 2 more precise layers, NOT resolved,
   3 experimental patches left uncommitted for review.** Rust-side tracing (as this doc's own
   previous entry asked for) was done. Chain of causes, most-proximate first:
   - **Layer A (root-caused + fixed, validated live):** `commit_syncer/cold_start.rs`'s
     `determine_startup_sync_exit()` Gate 1 (`has_parity`) required `lag == 0` exactly — on a
     cluster whose round timer never idles (empty blocks forever, no real tx load), the
     network's quorum_commit is a perpetually moving target; live logs showed
     `synced=326, local=326, quorum=327, lag=1` and the equivalent pair 10s later at
     `synced=262→326`, i.e. this node processes commits in real time but `lag` locks at
     exactly 1, never 0, so Gate 1 never passes and the node never leaves CatchingUp.
     **Experimental fix (uncommitted):** loosened Gate 1 to `lag <= 1`. Gate 5
     (`block_hash_verified`, the actual fork-safety check via peer-verified block hash) is
     completely untouched and still independently gates the same transition, so this widens
     only a liveness check, not the fork-safety one. **Verified live:** after this fix, node
     logs changed from "CatchingUp — lag > 0" to *"All gates (1-4) passed but block hash NOT
     yet verified against peers (Gate 5)"* — Gate 1 confirmed fixed, Gate 5 is next.
   - **Layer B (root-caused, NOT fixed):** Gate 5's `perform_post_gate_verification()`
     (`node/setup_consensus/verification.rs`) needs to query peers' `/peer_info` over a raw
     TCP+manual-HTTP server (`network/peer_rpc/server.rs`, `PeerRpcServer`) on
     `peer_rpc_port` (19200+node_id for this devnet) — but every query failed with "No
     reachable peers found" (`network/peer_rpc/client.rs`), so Gate 5 never passes, node
     stays in CatchingUp forever, `eth_blockNumber` never advances past 1. Direct `curl` to
     the port confirmed it: connects fine, but answers `content-type: application/grpc,
     grpc-status: 12 (UNIMPLEMENTED)` — something is listening, but it isn't the intended
     `/peer_info` HTTP handler.
   - **Layer C (partially root-caused, NOT resolved):** log tracing found the real proximate
     error: `startup.rs` starts a "full" `PeerRpcServer` on the same port right after
     `startup_sync.rs` aborts a temporary "early" `PeerRpcServer` instance (started to serve
     `/peer_info` before the rest of the node is ready, an intentional design — see its own
     "prevent STARTUP-SYNC deadlock" comment) — and the full server's `bind()` failed with
     "Address already in use" (os error 98), logged but **silently swallowed by the spawning
     task**, so no `/peer_info` server ever runs again for the life of the node. **Two
     experimental fixes tried, in order, neither resolved it:**
     1. Retry `bind()` up to 20× with 250ms backoff instead of failing once
        (`peer_rpc/server.rs`) — **still failed all 20/20 retries** (5s straight), proving
        this isn't a short release-timing race.
     2. Replaced the early server's `handle.abort(); sleep(100ms)` with the textbook-correct
        `handle.abort(); let _ = handle.await;` (`setup_consensus/startup_sync.rs`) — awaiting
        an aborted `JoinHandle` should block until the task, and the `TcpListener` it owns,
        is actually torn down. **Still failed all 20/20 retries.** Live logs show the early
        server started and was stopped again within **1.9ms** of each other, then the full
        server's bind still couldn't get the port for the next 5+ seconds — meaning
        something holds it well past what either fix addresses.
     Ruled out as the culprit (checked directly, not assumed): Go does NOT bind this port
     itself (`execution/cmd/simple_chain/processor/block_processor_core.go`'s own comment —
     the call is literally commented out); no second Rust call site constructs a
     `PeerRpcServer` anywhere in the tree (`grep -rn "PeerRpcServer::new"` — exactly one call
     site, in `startup_sync.rs`); the early-server spawn+cleanup in `startup_sync.rs` happens
     exactly once per node lifetime (outside any loop that could re-spawn without cleaning up
     the previous instance). **What's actually holding the port for 5+ full seconds is still
     unknown** — needs real process-level tracing (`strace -f -e trace=bind,close`, or
     `lsof`/`ss` sampled at high frequency during the exact failure window with fd/thread
     detail) rather than more source-reading-and-guessing; the "guess a fix, rebuild, retest
     live" cycle used for Layers A/B stopped being productive at this layer.
   - **Net effect:** Layer A's fix is real progress (validated), but Root Anchor still cannot
     leave CatchingUp on a fresh multi-validator start because of Layer C, so the
     destination-side `claimMessage()` acceptance-test proof is still blocked. All 3
     experimental patches (Gate 1, bind-retry, abort+await) are left **uncommitted** in the
     working tree — each is independently a reasonable robustness improvement and none
     reduces fork-safety (Gate 5 itself is never touched), but none is proven sufficient and
     none has been reviewed. Do not commit/merge without dedicated review, ideally alongside
     whoever wrote the original Gate 1/5 fork-safety gates and the early/full PeerRpcServer
     handoff design.

## Phase 0.8 — PR #64: Task 1.2 real-contract acceptance test found + fixed 2 more real bugs
## (2026-08-25, same day), plus live 2-node RPC verification

**Code:** `test/custom-asset-real-token-coverage` → PR #64 (open at time of writing). Closes
the Task 1.2 test-coverage gap this doc and `cross_chain_task1_native_value_fix_plan.md`
both flagged: the existing `TestGatewayHandler_CustomAsset_Outbound_ClaimMessage` only ever
proved the custom-asset code fails gracefully against a non-contract address, never that a
real `transferFrom`/`transfer`/`mint` succeeds against a real deployed token. Writing that
real test (a solc-0.8.35-compiled ERC-20-shaped fixture, deployed via the real
`mvm.ExecutionEngine.Deploy` path) found **2 more real production bugs**, both fixed:

1. **`msg.sender` bug, 4 call sites** (`outbound`'s `transferFrom`, `claimMessage`'s
   `transfer`/`mint`, `refund`'s `transfer`/`mint`, `verifyAndExecute`'s `transfer`/`mint`) —
   `executeContractCallForGateway` was called with `sender=tx.FromAddress()` (the bridging
   user/relayer) instead of the Gateway contract. `transferFrom(from,to,value)` checks
   `allowance[from][msg.sender]`; with `sender==from==tx.FromAddress()` this checked
   `allowance[user][user]` — never set by any real approval flow, since a user can only
   sanely `approve()` a fixed known spender (the Gateway). **This meant the entire
   custom-asset outbound path was unconditionally broken against any real
   standards-compliant ERC-20** — confirmed live, `ERR_EXECUTION_REVERTED`. Same root cause
   breaks the vault-unlock `transfer()` (moves the wrong account's balance) and would break
   `mint()` against any real access-controlled wrapped token. Fixed all 4 sites to pass
   `mt_common.GATEWAY_CONTRACT_ADDRESS`.
2. **`applyFullMvmResultToStateDB` never applied `MapCodeChange`** (only the hash, not the
   bytecode) — `SmartContractDB.SetCode` was never called, so code written via this path was
   never actually retrievable (`"Error getting code from storage"` on the next call in). Only
   affects the Gateway's own internal contract-call/deploy paths, not normal top-level EVM
   deployment (which goes through `vm_processor_state.go`'s already-correct application
   logic). Fixed by adding the missing `SetCode` step.

`go build ./... && go vet ./... && go test ./...` clean across all 43 packages in
`execution/`, `gofmt -l` clean.

**Live 2-node RPC verification (same day, at the user's explicit request before signing
off):** rebuilt the Go binary with PR #64's fixes, ran 2 real single-validator private
chains (101, 102) as separate OS processes (clean single-shot regenerate — see finding #1
above for why that matters), and ran a throwaway tool (`deploy_and_approve_quick`, not
committed) against each over **real JSON-RPC**, not an in-process shortcut:
- A real top-level `CREATE` transaction (`to: nil`, standard EVM deployment path, not the Go
  test's `mvm.Deploy` shortcut) — real receipt, real deployed address, on both chains.
- A real `balanceOf()` `eth_call` immediately after — proves the deployed code is really
  retrievable (this is the code path the tests above cover with bug #2's fix; a normal `CREATE`
  doesn't actually exercise that fix, since it uses the separately-correct
  `vm_processor_state.go` path — this call mainly confirms deployment itself is healthy).
  Chain 101: `1000000` (exact initial supply). Chain 102: `500000` (exact initial supply).
- A real `approve(GATEWAY_CONTRACT_ADDRESS, 500)` transaction — real receipt on both chains.
- A real `allowance()` `eth_call` afterward — exactly `500` on both chains, confirming the
  approve really landed.

**What this does and doesn't prove:** confirms the rebuilt binary carrying both fixes runs
correctly end-to-end (real consensus block inclusion, real EVM execution, real state
persisted and readable back) on 2 independent real node processes talking only over RPC —
not a Go-process-internal test. It does **not** cover the full `outbound()`/`claimMessage()`
round trip for a custom asset over real RPC, because that requires the asset to already be
registered via `registerAsset`, which requires an executed governance proposal, which
requires a real committee (`ChainRegistry`) on each chain — the same Root Anchor dependency
chain Phase 0.7's Layers A/B/C are about, and Layer C is still unresolved. The in-process Go
acceptance tests in PR #64 (`TestGatewayHandler_CustomAsset_RealTokenTransferSucceeds` /
`...RealTokenMintSucceeds`) remain the authoritative proof of the `msg.sender` and `SetCode`
fixes themselves — they exercise the exact same `executeContractCallForGateway` /
`applyFullMvmResultToStateDB` code the live nodes run, just without needing the full
governance ceremony to seed `AssetRegistry`. Closing that last gap needs Layer C fixed first
(or a deliberate governance/bootstrap shortcut designed for it, not guessed at here).

## Phase 0.9 — full live custom-asset round trip completed for real (2026-08-25, same day),
## found + fixed the SupplyLedger allocation dead end, plus 2 tooling bugs and 1 unfixed finding

**Closes Phase 0.8's last open gap.** Ran the complete `outbound()` → `attestCommit()` →
`claimMessage()` custom-asset round trip over real JSON-RPC against the same 2 live
single-validator private chains (101 on `:8551`, 102 on `:8547`) as Phase 0.8, through real
on-chain governance end to end — not a bootstrap/test shortcut. Sequence, all real
transactions with real receipts:

1. Real `bootstrapFoundingChains()` on both chains (4-chain fake founding committee, satisfies
   `MinFoundingChains`).
2. Real `propose()`/`vote()`/`executeProposal()` — chain 101 registers chain 102 and itself;
   chain 102 registers chain 101 and itself. Each a real proposal, real quorum votes from the
   founding committee, a real (devnet-shortened) timelock wait, real execution.
3. Real `propose()`/`vote()`/`executeProposal()` + `registerAsset()` — assetID 42 registered
   on both chains, linking the real deployed canonical token (chain 101) and wrapped token
   (chain 102) from Phase 0.8.
4. Real `outbound()` on chain 101 — moved 100 units from sender to Gateway. Verified via real
   `eth_call` to `balanceOf`: sender `1,000,000 → 999,900`, Gateway `0 → 100`.
5. Real `attestCommit()` on chain 102 for that commit — **found bug #3 below, fixed, then
   this succeeded for real.**
6. Real `claimMessage()` on chain 102 — verified via real `eth_call` to `balanceOf`:
   recipient `500,000 → 500,100`, exactly the bridged amount.

**Devnet-only timelock override (separate small piece of this work, safe by construction):**
added `CrossChainConfig.DevnetGovernanceTimelockSecondsOverride` (`config.go`) and
`GatewayEngine.ApplyGovernanceTimelockOverride` (`gateway.go`), wired from
`loadGatewayEngine` (`gateway_handler.go`). Zero by default — `EnsureGovernance()` only takes
the override path when it's explicitly nonzero, so production behavior (mandatory 72h
timelock) is unchanged unless an operator deliberately opts in. This is what made steps
2/3/5 above practical to run live (real ~12s waits instead of real 72h waits) without
touching the mandatory-timelock invariant itself, and without exploiting the separately-found
`vote()`/`executeProposal()` caller-supplied-timestamp gap noted below.

**Bug #3 (the interesting one) — `GlobalSupplyLedger.PerChainAllocation` had no
governance-reachable way to ever be funded, at all:** step 5 above reverted with
`ErrAllocationExceeded` ("requested 100 exceeds available 0"). Traced to: production always
constructs the ledger via `NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})` —
genesis-zero, empty allocations (`gateway_handler.go`'s `loadGatewayEngine`, both branches).
Neither `BootstrapFoundingChains` nor `ExecuteGovernanceProposal`'s `ProposalRegisterChain`
case ever touches `SupplyLedger` (confirmed by direct code reading). The only mutator that
existed, `GlobalSupplyLedger.TransferAllocation`/`SetInitialAllocation`, was never called
from anywhere ABI-reachable — `grep -rn "SetInitialAllocation\|TransferAllocation"` outside
`types.go` itself turned up nothing. `TestRelayer_Scenario10_6_OnboardNewChainViaGovernance`
(pre-existing) had quietly worked around exactly this by poking
`chains[1000].SupplyLedger.PerChainAllocation[104] = big.NewInt(0)` directly in white-box
test code instead of exercising a real path — a real symptom of the same gap this phase
found live. **Net effect: as shipped, `attestCommit()`'s Scenario 10.7 ceiling rejects every
chain forever, native coin or custom asset alike, with no legitimate way to ever unblock it**
— a second, independent dead end on top of Phase 0.6's finding (that one was "verified
ledger never wired to real balances"; this one is "the ledger's own ceiling can never be
funded even in principle").

**Fix:** new `GovernanceProposalKind = 5` (`ProposalAllocateSupply`) and
`GlobalSupplyLedger.GrantAllocation(chainID, amount)` (`types.go`) — increases the target
chain's allocation **and** `GenesisTotalSupply` together, keeping
`sum(per_chain_allocation) == genesis_total_supply` intact (deliberately different from
`TransferAllocation`, which redistributes *existing* allocation and would need a pre-funded
reserve chain nothing in production ever seeds). Wired into `ExecuteGovernanceProposal`'s
switch in `gateway.go`. No ABI change needed — `propose()`'s `kind` is a raw caller-supplied
`uint8`, so the existing `propose`/`vote`/`executeProposal` methods immediately accept it.
Same quorum + timelock protection as every other governance action, so a captured single
chain still cannot self-grant. Regression tests:
`TestGateway_ProposalAllocateSupply_UnblocksAttestCommit` (full propose→vote→timelock→execute
cycle, before/after ceiling behavior) and `TestGlobalSupplyLedger_GrantAllocation` (ledger
primitive: accumulation, invariant, nil/zero/negative rejection) in `pkg/cross_chain/`.
Verified live: re-ran step 5 after a real `ProposalAllocateSupply` grant on chain 102 for
chain 101 — succeeded, then step 6 (`claimMessage`) succeeded, exact balance delta confirmed.

**2 throwaway-tool bugs found and fixed along the way (own tooling, not production code, not
committed — `execution/cmd/tool/live_asset_bridge/main.go`):** (a) a single
`RemoteCommitteePriv string` field silently overwritten across 2 `register-chain` calls on
the same state file (once for the real peer chain, once for self-registration), causing
`attestCommit` to fail with an invalid quorum cert because the saved private key no longer
matched what was actually registered on-chain — diagnosed by adding a `query-registry` debug
step comparing the on-chain pubkey against the locally-derived one, fixed by keying it
`map[chainID]string` instead; (b) a hardcoded quorum vote count that went stale as
`ActiveChains` grew with each executed `ProposalRegisterChain` — fixed by making all votes
tolerate an expected post-quorum revert rather than trying to predict the exact threshold.

**Finding noted but deliberately not exploited, not yet fixed — needs a decision, not a
guess:** `vote()`/`executeProposal()` accept a caller-supplied `currentTimestamp` argument
with no cross-check against real block time. This is what made the devnet timelock override
above unnecessary to lean on for correctness (the override is a real, honest config value,
not this gap) — but the gap itself means anyone can currently claim any timestamp they like
when voting/executing, including one far in the future, bypassing the 72h timelock outright
in production today. Not exercised here (out of scope for "prove the bridge moves real
value," and doing so live would require deliberately faking a governance execution, which
needs a call before doing it) — flagging for Phase 1 or a dedicated fix: should this validate
against `blockTime` the same way `AttestCommit`'s epoch check is fail-closed, and if so is
there a legitimate reason it currently isn't (e.g. some caller needs to backdate/forward-date
for a reason not yet identified)?

**What this proves and what's still open:** a real custom-asset bridge transfer now
genuinely moves a real, spendable ERC-20-shaped balance from a real sender on one live chain
to a real recipient on another live chain, through real BLS-verified governance and real
BLS/Merkle-verified attestation — the first time in this project's history any cross-chain
transfer has done that (Phase 0.6 documented that none ever had). Phase 0.6's native-coin
`ProcessNativeMintBurn` wiring (item 1/2 in its fix plan) is still separately open — this
phase only closes the custom-asset path plus the allocation-ceiling dead end that blocked
both. Root Anchor Layer C (Phase 0.7, `peer_rpc_port` bind collision) remains unresolved and
was worked around here the same way Phase 0.7 already does (single-validator chains, no real
multi-node Root Anchor consensus needed for this test).

## How to work (read once, applies to every phase below)

- **Zero-Fork invariant.** Never let a background worker or async path write to
  consensus-relevant state (anything that becomes part of a signed commit or state
  root) — every past fork-risk in this project was exactly that pattern. If a fix
  requires new committed state, thread it through the same synchronous request/response
  paths already used by `GatewayHandler`, and bind it into a BLS-signed digest the same
  way `AccountTreeRoot`/`AggregateValueLeaf` are — never trust a caller-supplied value
  for anything that affects fund safety.
- **Verification bar before every commit:** `go build ./... && go vet ./... && go test
  ./...` from `execution/`, zero regressions, `gofmt -l` clean on the files you touched.
  If you run a formatter broadly and it touches unrelated files, verify with `git diff -w`
  that nothing but whitespace changed before including them, and say so plainly in the
  commit message — don't let cosmetic reformatting obscure a security-fix diff.
- **Testing philosophy:** use real crypto and real production code paths end-to-end
  wherever feasible (real BLS sign/verify/aggregate, real ABI encode/decode, real signed
  transactions) — not mocks. **The recurring failure pattern in this project's history is
  a test that only passes because the test itself sets the exact state value production
  code is supposed to independently derive/verify.** Every one of the 3 bugs found so far
  was hiding behind exactly that pattern. When you touch a test like this, ask: what real
  production code path would ever produce this value for a real caller? If the answer is
  "none, the test just declares it," that's the bug.
- **A finding is not "handled" until it has a regression test** proving the specific
  attack/bug is rejected, not just a code review conclusion.
- **Git/PR workflow:** branch off `dev`, commit, push, open a PR via `gh`, watch CI to
  green. **Do not self-merge** — the project owner merges PRs personally.
- Before implementing anything ambiguous, **stop and surface it** rather than guessing.
  Phase 0's `AccountTreeRoot` fix is a good template for how to handle this well: it hit
  a genuine unspecified design point (how does a chain expose an account-level proof
  against real state?), picked one of two reasonable approaches (a periodically
  checkpointed snapshot tree, bound into the existing committee-attestation digest) and
  implemented it cleanly rather than guessing at a shortcut.

---

## Phase 1 — Close remaining known functional gaps

Do these before Phase 2 (T2 testnet) so that run exercises the real system, not one with
known-missing pieces. None of these are as urgent as Phase 0 was, but don't skip them —
items 1 and 3 in particular are in the same risk family as the bugs already found.

1. **Epoch catch-up.** `ApplyCommitteeUpdate` (`execution/pkg/cross_chain/epoch_sync.go`)
   only accepts strictly sequential epochs. If a chain loses Root Anchor connectivity
   across multiple epochs, it currently has no way to catch up other than replaying every
   missed epoch sequentially — and it may no longer have the old committee's signatures
   for epochs it missed (validators rotate, old keys may be gone). Two options:
   - Decide and implement a bounded recovery path (e.g., allow a "skip-ahead"
     `CommitteeUpdate` variant that proves continuity some other way — needs real design
     work, don't guess at the cryptographic construction without checking with the task
     owner first), or
   - Explicitly accept the limitation, but make it operationally visible:
     `GatewayRegistryMonitor.DriftEpochs()` (already implemented) already computes how
     far behind a chain is — wire an actual alert (metrics/log-based paging, whatever
     this project's ops tooling supports) so a stuck chain doesn't silently stay stuck.
   Whichever you choose, write it down in this file's successor or the design doc, not
   just in a commit message — this is exactly the kind of "accepted limitation" that
   needs to stay visible to whoever runs T2/T3 next.
2. **Deployment tooling smoke-test — DONE 2026-08-25, found+fixed one real bug this way.**
   Ran `gen_root_anchor_chain.py` (4-validator Root Anchor, real `simple_chain`+Rust-FFI
   binary, real BFT block production confirmed via `eth_blockNumber` increasing) and
   `gen_single_chain.py --root-anchor-rpc ... --root-anchor-submitter-key ...` (a private
   chain pointed at it) for real, locally. Exactly the kind of small-mismatch bug this item
   predicted: `gen_single_chain.py` emitted the `cross_chain` config block with **PascalCase**
   keys (`"RootAnchorRpcUrls"`, `"RootAnchorSubmitterPrivateKeyHex"`, ...) but
   `config.go`'s `CrossChainConfig` struct tags are **snake_case**
   (`root_anchor_rpc_urls`, ...) — `encoding/json` doesn't error on an unmatched key, it
   just silently leaves the field at its zero value, so every node this script generated
   would have booted with `GatewayRegistryMonitor`/`CommitteeAttestationWorker` **silently
   disabled** (confirmed empirically with a throwaway `config.LoadConfig()` test before
   fixing, and again by tailing a real node's log for the "✅ Gateway ChainRegistry Drift
   Monitor started" / "✅ Committee Attestation Worker started" lines before vs. after the
   fix). Fixed by correcting the keys to snake_case; re-verified live against the running
   Root Anchor devnet that both workers now actually start. `setup_root_anchor.sh` doesn't
   exist under that name (the working script is `gen_root_anchor_chain.py` alone, plus the
   generated `start_all.sh`/`stop_all.sh` in its output dir) — update this doc/any other
   reference accordingly if that's intentional, or add the missing wrapper if one was meant
   to exist.

   Continued smoke-testing the rest of the T2 tooling chain found (and fixed) **2 more real
   bugs of the exact same "silent mismatch" shape**, each verified with a throwaway Go
   program against the real target type before fixing, not assumed:
   - `deploy/systemd/register_private_chains_t2.sh` called `./register_chains --rpc ...`,
     but the tool's actual flag (`execution/cmd/tool/register_chains/main.go`) is
     `-root-anchor` — there is no `-rpc` flag, so Go's `flag` package would reject the
     invocation outright (`flag provided but not defined: -rpc`), confirmed by running the
     built binary. Fixed the flag name; also had the script auto-build the tool if the
     binary isn't already present (it's gitignored, not meant to be committed — added
     `/register_chains` to `execution/.gitignore`, it was missing).
   - `deploy/systemd/register_private_chains_t2.py` built each chain's registration payload
     with `"state_root"`/`"account_tree_root"` as bare 64-hex-char strings (no `0x` prefix)
     — `common.Hash.UnmarshalJSON` (the target type, `ChainRegistry` in
     `pkg/cross_chain/types.go`) explicitly errors ("cannot unmarshal hex string without 0x
     prefix") without it, confirmed directly. **Worse, silently-wrong rather than
     error-loud:** `"pubkey_bls"` was sent as `bls_bytes.hex()` (a hex string), but the
     target field is `[]byte` (`ValidatorEntry.PubkeyBLS`), and Go's `encoding/json`
     base64-DECODES a JSON string into a `[]byte` field, not hex-decodes it — hex digits
     happen to also be valid base64 characters, so this produced **no error and a
     completely wrong pubkey** (confirmed empirically: decoding a real 48-byte BLS pubkey's
     hex string as base64 silently yields 72 garbage bytes). Any chain registered this way
     would have an unverifiable committee with no error anywhere pointing at why. Fixed by
     adding the `0x` prefix to both root fields, and by sending `pubkey_bls` as the
     already-base64 `authority_key` value straight from genesis.json instead of
     re-encoding it as hex.

   **Takeaway for whoever runs T2 next:** every one of these 3 tooling bugs (this item's
   original PascalCase one plus these 2) shares the same shape — a script-generated JSON
   payload silently mismatching what the real Go struct/`encoding/json` actually expects,
   with **no error at the point of failure** in 2 of the 3 cases. Don't trust any new
   deploy-tooling JSON payload without unmarshaling it through the real target Go type
   first (a 5-line throwaway `go run` is enough, as done here) — this is the same
   "what would make this pass without the real thing being true" discipline this doc
   already asks for in code, just applied to generated config/payloads too.

   **New sub-item found continuing this work — genesis governance deadlock, fixed:**
   `GovernanceEngine.Vote` requires the voter to already be a member of `engine.ChainRegistry`
   (`gateway_handler.go`'s "vote" case looks up the signer's committee there), and
   `ExecuteGovernanceProposal`'s `ProposalRegisterChain` case requires a proposal to have
   already passed a vote — so a fresh Root Anchor (`ChainRegistry` always starts empty, see
   `NewGatewayEngine`'s call site) has **no path to register its first chain through
   governance at all**, confirmed by tracing the code and by hitting it live (submitted real
   `propose()` transactions for chains 101-104 against a running Root Anchor — see PR
   `fix/cross-chain-refund-authorization` — they landed on-chain but can never be voted on).
   Added `GatewayEngine.BootstrapFoundingChains([]byte payloads)` /
   `bootstrapFoundingChains(bytes[])`: seeds `ChainRegistry`/`ActiveChains` directly, once,
   requires `>= MinFoundingChains` (4, matching mục 1.3 #5) and real PoP for every committee
   member, and is self-closing — the moment it succeeds `ActiveChains` is non-empty, so it can
   never be called again by anyone. 5 new tests in `root_anchor_bootstrap_test.go` cover
   success, <4 chains, duplicate chain ID, forged PoP, and the self-close guarantee — all real
   BLS, no shortcuts.

   **New hardening item found while writing the production deployment guide
   (2026-08-25, not yet fixed, 🟡 medium — bounded/recoverable, not a fund-loss bug):**
   `BootstrapFoundingChains` (`gateway_handler.go`'s `bootstrapFoundingChains` case) has no
   sender-authorization check — any address can submit the call, the only gate is that the
   payload structurally validates and every committee member's PoP verifies against their own
   claimed key. Combined with `runbook_root_anchor_genesis_ceremony.md`'s own guidance that
   `founding_entry.json` files are "safe to publish" (no private key material) and get sent to
   the coordinator "over any channel (email, chat, a shared drive)", this creates a front-run
   window: anyone who obtains ≥3 of the real founders' published `founding_entry.json` files
   before the coordinator's bootstrap transaction confirms can pair them with a 4th
   self-generated (real PoP, attacker's own key — no compromise needed) chain entry and race
   their own `bootstrapFoundingChains` call ahead of the legitimate one. Since the call is
   self-closing on first success, the attacker's fabricated chain would permanently occupy one
   of the founding committee/governance seats instead of the real 4th founder.
   **Why this is 🟡 not 🔴:** `ChainRegistry` carries no `per_chain_allocation`/balance field —
   at genesis there is nothing to steal — and `ProposalUnregisterChain` already exists, so the
   legitimate 3-of-4 founders can immediately vote the attacker's chain back out (quorum is
   `ceil(2*4/3) = 3`) and then register the real 4th founder normally through governance
   (which now works, since `ActiveChains` is no longer empty). Net effect is a recoverable
   griefing/delay attack on the ceremony, not a value-loss exploit — tracked here per this
   doc's own severity convention (compare risk-table items #8/#9/#10 in
   `cross_chain_root_anchor_architecture.md`, same 🟡 tier, same "mitigate procedurally +
   track" treatment) rather than triggering the responsible-disclosure commit-lockdown reserved
   for uncapped fund-fabrication bugs (Phase 0.5 item 1, above). **Interim operational
   mitigation, added to the new deployment guide's ceremony section:** never broadcast
   `founding_entry.json` files widely (send directly/privately to the coordinator, not a group
   channel); the coordinator should submit the assembled bootstrap transaction promptly after
   collecting the last entry and watch for competing pending transactions to the Gateway
   address before it confirms. **Real fix, still open:** restrict `bootstrapFoundingChains` to
   a coordinator address pre-committed out of band (e.g. hash-committed in the same
   out-of-band channel `genesis_digest.txt` already uses), or require the call to name the
   expected set of founding chain IDs up front so a late substitution is structurally
   impossible rather than merely recoverable after the fact.

   **Investigated live, root cause found — environmental, not a code bug:** restarting the
   local 4-validator Root Anchor devnet from persisted state appeared to hang (no new blocks,
   silent Go-side log, one validator's RSS climbing unbounded). Ruled out with real evidence
   before concluding anything: Go goroutine dump (`/debug/pprof/goroutine`) showed only the
   normal idle worker pools (300/200 chan-receive goroutines matching
   `NumInjectionWorkers`/`NumReadTxWorkers` exactly, nothing duplicated); Go heap profile
   showed a tiny, stable ~67MB heap — the multi-GB RSS growth is entirely native/CGo-side, not
   a Go leak; disk write throughput measured at 662MB/s (not I/O-bound); `ulimit -u` has huge
   headroom (only ~1000 of 771041 threads in use). `gdb`/`ptrace` were unavailable in the
   sandbox (`yama/ptrace_scope=1`, no passwordless sudo) so a Rust-side stack trace couldn't be
   taken directly. What DID explain it: this machine (104 cores, 188GB RAM) has an unrelated,
   pre-existing systemd service, `metanode-execution-3.service` (owned by system user
   `metanode`, not this session's user) — running for 1+ day, consuming **653% CPU
   (6.5+ cores) and 110GB RSS (peak 116.6GB)** continuously. This matches this project's own
   prior session notes describing leftover crash-loop test artifacts on this shared machine,
   unrelated to any cross-chain work. Left running at the user's explicit instruction (not this
   session's to touch — needs sudo the assistant doesn't have anyway). **Net effect: the
   bootstrap fix above is verified by its 5 real unit tests; a live multi-validator run is
   still not done, but the blocker is this shared machine's resource contention, not a defect
   in `BootstrapFoundingChains` or the Rust startup-sync path** (`startup_sync.rs` was read in
   full during this investigation looking for an infinite-retry bug — its retry loops are all
   properly bounded, e.g. `MAX_VERIFY_RETRIES=10`/`MAX_ISOLATION_ROUNDS=60`, not the culprit).
   Whoever revisits this: get the noisy neighbor process stopped or run T2 on dedicated
   hardware/VMs (this doc's own Phase 2 T2 row already calls for "separate machines/VMs" —
   this finding is a concrete reason why, not just a nice-to-have) before spending more time
   chasing this as a code issue.
3. **Adversarial re-review of Milestones F and I at Phase-0 depth.**
   `CommitAttestationWorker` (F) and `RelayerDaemon` (I) were reviewed structurally when
   E/G/Phase-0 were found and looked sound, but never got the specific "what would make
   this test pass without the real thing being true" scrutiny that found the other 3
   bugs. Concretely: for every test in `commit_attestation_worker_test.go` and
   `relayer_daemon/daemon_test.go`, ask whether the test's own setup independently proves
   the state it hands to the verification code, or whether it's shortcut-seeded. Also
   check `RelayerDaemon`'s key handling explicitly: `RelayerKeyHex` and
   `RootAnchorSubmitterPrivateKeyHex` are plain config strings in `DaemonConfig` — fine
   for a reference/testnet daemon, but if anyone treats this as the long-term mainnet
   key-management story, flag it (raw private keys in config files/env vars is not a
   mainnet-grade custody model for a relayer that can move real value).
4. **Governance `propose()` has no gating.** Confirm this is intentional (permissionless
   proposal, gated only at the vote/quorum stage) per the design doc rather than an
   oversight. If intentional, decide whether unbounded free proposals (a
   `map[common.Hash]*GovernanceProposal` keyed by content hash, no rate limit) need a
   spam/storage-growth mitigation before mainnet — this is a griefing/cost concern, not a
   fund-safety one, so it's fine to explicitly defer with a written rationale rather than
   fix, but make the decision on purpose.
5. **Account-tree-root scalability.** Phase 0's `CommitteeAttestationWorker.accountTreeRootAtBlock`
   walks a chain's *entire* committed account set via `AccountStateDB.GetAll()` on every
   epoch transition. This is correct and necessary for the security property, but nobody
   has measured its cost on a chain with a large number of accounts. Get a real number for
   this during Phase 2's T2 measurements (see the table below) — if it's a bottleneck at
   realistic account counts, that's a Phase-2-driven follow-up item, not something to
   pre-optimize now without data.

---

## Phase 2 — T0–T3 per the design doc's own rollout plan (section 12)

The design doc's section 12 table is the authoritative gate sequence; this section maps
current status onto it rather than re-deriving a new one.

| Stage | Design doc requirement | Current status |
|---|---|---|
| **T0 — Unit test** | 100% pass, error-branch coverage, not just happy path | Largely satisfied — `TestAudit_*` (9 tests) and per-function unit tests cover BLS verify, Merkle tamper, replay, hop-count, epoch fail-closed, governance idempotency, account-snapshot determinism, etc. Re-run after Phase 1 lands. |
| **T1 — Local devnet, all 8 scenarios automated** | 1 Reserve + 2 private chains, scenarios 10.1–10.8 all pass | **Logically covered** (`TestRelayer_Scenario10_1` through `10_8` all exist and pass) but these run in-process against Go structs directly, not against real running Metanode node processes with real RPC/consensus. Re-run the same 8 scenarios end-to-end against real nodes (see T2) before calling T1 actually satisfied in the doc's sense — in-process tests are necessary but not sufficient, and are exactly the kind of test that hid all 3 bugs found so far. |
| **T2 — Multi-chain testnet, real infra** | ≥4 real chains, real network latency, measure real BLS/commit costs and throughput at 500/2000/4000 msg batches | **Not started.** Use `deploy/systemd/gen_root_anchor_chain.py`/`setup_root_anchor.sh` (Phase 1 item 2, once smoke-tested) plus existing `deploy/ansible` tooling to stand up Root Anchor + ≥4 private chains on separate machines/VMs. Run the `RelayerDaemon` (Milestone I) for real between them. This is where Phase 0's `AccountTreeRoot` fix and Phase 1 item 5's scalability question both get real answers — a chain actually dies or is simulated dead, real trie/account state, real proof, real timing. |
| **T3 — Adversarial/chaos testing** | Simulate compromising 3/4 validators of a small chain and repeat scenario 10.7; kill Reserve temporarily; full Chain-Death Recovery end-to-end at least once on testnet | **Not started** — depends on T2 infrastructure existing first. Chain-Death Recovery must succeed end-to-end on the T2 testnet using the Phase 0 fix, not just in a unit test, before anyone trusts it operationally. |

Do not proceed to Phase 3 (audit) without real T2 numbers — the audit firm will want them,
and per the design doc's own stated principle, no stage should be skipped by "trusting"
instead of measuring.

---

## Phase 3 — External security audit (design doc P5 / section 8)

Non-negotiable per the design doc's own policy, and reinforced by this project's own
track record: two internal review passes already found 3 critical bugs in freshly-written
cryptographic/authorization code (Milestone E's proof target, Milestone G's missing vote
auth, Phase 0's `claimDeadChainBalance` proof target). Engage an independent external
audit covering the full verify stack (BLS, both Merkle proof types —
`AggregateValueLeaf`/commit-tree and `AccountTreeRoot`/account-snapshot — replay
protection, double-mint protection, origin-sender context integrity, governance
authorization including the `Propose()` idempotency fix) plus the 3 adversarial scenarios
from T3. Do not proceed to Phase 4 with any open Critical/High finding — this is a
hardcoded gate in the design doc, not a suggestion.

Budget note from the design doc's own estimate (section 15): external audit is one of
the few phases explicitly called out as **not shortenable by engineering effort** —
plan calendar time (design doc estimates ~4-8 weeks), not developer-weeks.

---

## Phase 4 — Staged mainnet rollout (T4, section 12)

Only after Phase 3 closes clean. Three stages, each gated on a minimum observation
window with no serious incident before opening the next:

1. **Stage 1 — messages only** (`value=0`), no native-coin transfer risk. Lowest-risk
   way to prove the live system end-to-end. Observe for at least a few weeks.
2. **Stage 2 — value transfers via Reserve, small caps.** Start with a low
   `per_chain_allocation` ceiling system-wide, raise gradually over 1-2 months as
   monitoring (design doc section 6 dashboards, already partially implemented in
   `metrics_dashboard.go`) shows no anomalies.
3. **Stage 3 — remove caps** once stable with real monitoring data backing the decision.

## Phase 5 — Bug bounty (T5, optional but recommended)

Recommended before removing caps in Stage 3 if the custodied value at Reserve will be
significant. Scope it explicitly to the new attack surface: BLS/Merkle verification
(both proof types), `per_chain_allocation` accounting, governance authorization,
Chain-Death Recovery.

---

## Explicitly not in scope for this plan

- Rebuilding Rust consensus internals — confirmed multiple times across Milestones B-I
  that everything needed lives in Go using Go's own min-pk BLS keys; don't resurrect
  `consensus/metanode/src/types/{pop,root_anchor}.rs` speculatively.
- Performance/throughput optimizations beyond what's needed to get real T2 numbers
  (design doc section 13's relayer batching/fee-auction ideas are explicitly P4-and-later
  in the doc's own roadmap, not a production-readiness blocker).
- Founding-chain business negotiation (P1.2) — track it, but no engineering effort
  shortens it; it can proceed in parallel with Phases 1-3.
- A general-purpose mainnet key-management/custody solution for `RelayerDaemon` and the
  `CommitteeAttestationWorker`/`CommitAttestationWorker` submitter keys — Phase 1 item 3
  asks you to *flag* this, not solve it; it's a larger, separate infrastructure decision
  (HSM, threshold signing, etc.) that should be made deliberately, not bolted on here.
