# Root Anchor Cross-Chain Bridge — Next-Steps Plan (post Milestone A/B/C/D)

Status as of 2026-08-25: Milestones **A, B, C, D** of the original wiring plan are
implemented, tested, and merged into `dev` (PRs #51, #52, #53, #54). This document is a
handoff plan for whoever (human or agent) picks up the work next. It assumes **no prior
context** from the sessions that built A–D — everything needed to get oriented is either
in this file or in the two files it points to.

Read in this order before touching code:

1. `note/cross_chain_root_anchor_architecture.md` (v15) — the design doc. Section 14 is the
   authoritative P0–P8 task table with required tests; section 7 is the risk matrix (risk
   rows are numbered and referenced below as "risk #N").
2. This file.
3. Whichever milestone section below you're picking up — each links to the exact current
   files/functions so you don't have to re-derive them.

## How this codebase works (read once, applies to every milestone below)

- **Two languages, one invariant.** Go (`execution/`) is the execution layer; Rust
  (`consensus/metanode/`) is consensus. They talk over a synchronous, deliberately-blocking
  cgo FFI. **Zero-Fork invariant: never add a timeout or async path that could let two
  validators mutate state differently.** All 4 milestones so far did their new work
  entirely in Go, reusing Root Anchor itself (via JSON-RPC) as the only cross-validator
  channel — Go has no P2P layer of its own that reaches other organizations' nodes, and
  changing Rust consensus internals is high-risk and was avoided every time it wasn't
  strictly necessary. Keep doing that unless a task below explicitly requires touching
  Rust.
- **GatewayHandler pattern.** All cross-chain logic lives behind one singleton dispatcher:
  `execution/pkg/blockchain/tx_processor/gateway_handler.go`, serving
  `GATEWAY_CONTRACT_ADDRESS` (0x1002) as a barrier-tx precompile. State is a JSON blob in
  `SmartContractDB` (`gatewayStateStorageKey`), loaded/saved via `loadGatewayEngine` /
  `saveGatewayEngine`. New ABI methods go in
  `execution/pkg/blockchain/tx_processor/abi_contract/gatewayAbi.go` **using flattened
  parallel arrays, never Solidity tuples/structs** — follow the existing entries exactly.
  Add the write/view case to `gateway_handler.go`'s dispatch switch, plus any `mustXxx`
  arg-conversion helper needed (see existing `mustBytesSlice`, `mustUint64Slice`, etc).
- **Crypto:** BLS is **min-pk** (blst, `execution/pkg/bls`, G1 48-byte pubkeys, G2 96-byte
  sigs) everywhere in Go. Do not reach for Rust's `pop.rs` (min-sig, fastcrypto) — it's
  orphaned/unused and out of scope; confirmed unnecessary for everything done so far.
  Every validator's durable min-pk pubkey lives at
  `AccountStateDB.AccountState(addr).PublicKeyBls()`.
- **Testing philosophy:** integration tests use real crypto and real production code
  paths end-to-end — real `GatewayHandler`, real ABI encode/decode, real BLS
  sign/aggregate/verify, real signed go-ethereum transactions decoded via
  `ethtypes.Transaction` + `ethtypes.Sender`. No mocks where a real path is feasible. See
  `execution/pkg/blockchain/tx_processor/committee_attestation_worker_test.go` and
  `gateway_committee_update_test.go` as the reference examples (Milestone C).
- **Verification bar before every commit:** `go build ./... && go vet ./... && go test
  ./...` from `execution/`, zero regressions (`grep -E "FAIL|^---"` on test output should
  be empty), `gofmt -l` clean.
- **Git/PR workflow:** branch off `dev`, implement, commit, push, open a PR via `gh`,
  watch CI to green. **Do not self-merge** — the project owner merges PRs personally.
  Update `note/cross_chain_root_anchor_architecture.md` only if the design itself changes;
  otherwise leave it alone and let the PR description carry implementation notes.
- **Plan Mode discipline:** for anything nontrivial, explore first (Explore sub-agents are
  cheap and were used heavily for every milestone so far to avoid building on stale
  assumptions — the design doc's own premises turned out to be factually wrong in at least
  two places, caught only by reading the actual Rust/Go source). Surface any major
  finding/risk to the task owner before implementing, get the approach approved, then
  build with real tests, not mocks.

Persistent memory from the sessions that built A–D lives at
`/home/pi/.claude/projects/-mnt-2d4726e7-046b-47c7-b9a9-d2a9cc0cfc8d-Work-metanode/memory/cross_chain_wiring_plan_progress.md`
— if you're a Claude Code agent with access to that memory store, read it; it has
finer-grained detail (exact gotchas, exact test fixtures) than this document repeats.

---

## Recommended order

The items below are ordered by recommended priority, not by design-doc section number.
Rationale for the order is in each section. **E and F together are what make
`attestCommit()` — wired in Milestone A, still not safely usable end-to-end — actually
production-ready; do those first.**

### Milestone E (do first) — Bind `aggregateAmount` to real state via `AggregateValueLeaf` + Merkle proof

**Why first:** this is a named, already-designed, still-unimplemented **security hole**,
not a feature gap. Design doc risk row #20 (search `note/cross_chain_root_anchor_architecture.md`
for "risk #20" / "`aggregateAmount` trong `attestCommit()`") states plainly:

> `aggregateAmount` trong `attestCommit()` không ràng buộc mật mã → relayer tự khai số để
> né trần `per_chain_allocation` dù message thật vẫn claim đủ giá trị — vi phạm thẳng bất
> biến `Σ per_chain_allocation == genesis_total_supply`... **ĐÃ CHỐT**: bắt buộc
> `AggregateValueLeaf` + Merkle proof trong `attestCommit()`.

Confirmed still open by direct inspection (2026-08-25): `attestCommit`'s ABI
(`gatewayAbi.go`) has inputs `sourceChainId, commitRoot, aggregateAmount, certEpoch,
certAggregateSignature, certSignerBitmap` — **no `aggregateProof` parameter, no
`assetId`, no binding of `aggregateAmount` to anything but the caller's say-so.** A grep
for `AggregateValueLeaf` across `execution/pkg/cross_chain` and
`execution/pkg/blockchain/tx_processor` returns nothing — it does not exist yet anywhere
in Go.

**Scope (design doc sections 2.3.1, 11.2, 11.3, and P2.2's test requirements in section 14):**

1. Define `AggregateValueLeaf` (Go struct, `execution/pkg/cross_chain`) representing "the
   real total value moved for `(sourceChainId, commitRoot, assetId)`" as committed inside
   the source chain's own state root at the epoch the commit belongs to — read section
   11.2/11.6 closely for the exact leaf shape and where it's supposed to be
   constructed/committed on the source-chain side. This is the piece most likely to need
   a genuine design decision (how does the source chain compute and expose this leaf?) —
   don't guess past what the doc specifies; if it's ambiguous, that's worth a check-in
   with the task owner before implementing, not a silent assumption.
2. Add `aggregateProof` (Merkle proof, reuse the existing `MerkleProof`/
   `VerifyAccountMerkleProof`-style primitives in `epoch_sync.go` if the leaf shape allows
   it — don't invent a second proof format if the existing one fits) and `assetId` to
   `attestCommit`'s ABI inputs in `gatewayAbi.go`.
3. In `gateway_handler.go`'s `attestCommitInternal` (or wherever the write case lives),
   verify the proof against the relevant committed state root **before** trusting
   `aggregateAmount`; reject (no ledger mutation, correct revert reason) if the proof is
   missing/invalid/mismatched.
4. Enforce **write-once**: a second `attestCommit()` for the same `(sourceChainId,
   commitRoot, assetId)` with a different `aggregateAmount` must be rejected (design doc
   section 1.3 #11).
5. Enforce the hard cap: `claimedAmount <= fundedAmount` in `claimMessage()` — check
   whether this is already enforced (Milestone A's `claimMessage` may already have some
   ledger-capacity check; verify by reading the current code rather than assuming either
   way) and close the gap if not.

**Required tests (from design doc section 14, P2.2 row — these are not optional, they are
the literal Definition of Done specified by the design doc itself):**

- (a) `aggregateProof` wrong/missing/mismatched against the real `AggregateValueLeaf` →
  must revert, even with an otherwise-valid `QuorumCert`.
- (b) Reproduce the actual attack: relayer under-declares (e.g. real total moved is
  100,000, relayer claims `aggregateAmount=1`) → subsequent `claimMessage()` calls for
  messages in that commit must be blocked once `claimedAmount` would exceed the *declared*
  `fundedAmount=1`; no minting past the funded cap.
- (c) Second `attestCommit()` call for the same `(sourceChainId, commitRoot, assetId)`
  with a different `aggregateAmount` → must revert (write-once).

Existing security-focused test file to extend or take as a pattern:
`execution/pkg/cross_chain/security_audit_test.go` (see
`TestAudit_AdversarialOverdrawAndSupplyCeiling` for the closest existing analogue).

---

### Milestone F — Real quorum-cert production for `attestCommit()` (generalize Milestone C)

**Why second:** Milestone A wired `attestCommit()`'s *verification* side (it checks a
`QuorumCert` if handed one); Milestone C built a real multi-validator BLS quorum-cert
*production* pipeline, but scoped strictly to `CommitteeUpdate` certs. **Nobody in
production can currently produce a real cert for a value-transfer commit** — only
test-seeded certs exist for `attestCommit()` today. This was explicitly flagged as
deferred in the Milestone C PR: "a natural follow-on, but a separate milestone." Do this
after E so the cert you produce is actually safe to trust once bound to real state (E) —
doing F before E would just make it easier to exploit risk #20 at scale.

**Reuse, don't rebuild.** The mechanism is structurally the same as Milestone C's, and
should copy its shape closely:

- Reference implementation to mirror:
  `execution/pkg/blockchain/tx_processor/committee_attestation_worker.go` (the whole
  "attest-share bulletin board on Root Anchor" design — each validator signs a
  domain-separated digest with its own min-pk key, submits a share via a Gateway write
  method, any validator aggregates once ≥2/3 stake of the relevant committee has
  attested).
- What's different this time: the thing being attested is a **commit root** (a batch of
  outbound cross-chain messages from one epoch), not a new committee. The "old committee"
  whose stake counts is the *current* committee at the *current* epoch of the source
  chain (already exactly what `ChainRegistry[chainId].Committee`/`Epoch` hold — no new
  registry needed).
- New domain tag needed, distinct from `CommitteeUpdateDomainTag` — the doc comment on
  `CommitteeUpdateDomainTag` in `execution/pkg/cross_chain/epoch_sync.go` already
  reserves `"COMMIT_ROOT_ATTEST_V1:"` for this purpose (see that file's comment — it was
  written anticipating this exact follow-on). Add a
  `ComputeCommitRootAttestDigest(sourceChainId uint64, commitRoot common.Hash, assetId,
  aggregateAmount *big.Int, aggregateProof ...) common.Hash` alongside
  `ComputeCommitteeUpdateDigest`, matching Milestone E's finalized `AggregateValueLeaf`/
  `aggregateProof` shape so the signed digest actually covers what's being claimed (don't
  let the signature cover a smaller commitment than what E enforces on-chain, or E's cap
  can be bypassed by signing a stale/narrower digest).
- New Gateway write/view methods, same flattened-array convention: something like
  `submitCommitAttestation(...)` / `getCommitAttestationShares(...)`, parallel to
  `submitCommitteeAttestation`/`getCommitteeAttestationShares`.
- The worker needs a trigger analogous to the epoch-advance callback Milestone C added
  (`RequestHandler.SetEpochAdvancedCallback` in `execution/executor/unix_socket_handler.go`)
  — but triggered per-commit (whenever a new batch of outbound messages is finalized into
  a commit root), not per-epoch. Find the actual commit/checkpoint production point in
  the execution layer (likely near wherever `commitRoot` values are currently computed
  for test fixtures — check `epoch_sync.go` and whatever calls
  `BuildAccountMerkleTree`/produces commit roots today) before assuming the trigger
  point; this needs its own short exploration pass, don't guess.

**Tests:** mirror `committee_attestation_worker_test.go`'s structure (real HTTP Root
Anchor stand-in via `httptest.Server` behind the real `GatewayHandler`, real signed
go-ethereum txs, real BLS aggregation) — build a "CommitAttestationWorker full lifecycle"
test analogous to
`TestCommitteeAttestationWorker_SingleValidatorFullLifecycle`, plus negative tests
(insufficient quorum, non-member signer, digest mismatch with Milestone E's binding).

**Known limitation to carry forward, not silently fix:** Milestone C's committee-update
path is strictly sequential (no catch-up if epochs are skipped). Confirm whether the
same limitation is acceptable for commit-root attestation (commits are presumably more
frequent than epochs — check whether missing one is more costly here) and flag it
explicitly in the PR rather than assuming it's fine by analogy.

---

### Milestone G — Wire `GovernanceEngine` and `AssetRegistry` into the live `GatewayHandler`

**Why:** both are fully implemented and unit-tested already
(`execution/pkg/cross_chain/governance.go`, `execution/pkg/cross_chain/asset_registry.go`,
with their own `_test.go` files) but confirmed **completely orphaned** — grep for
`cross_chain.GovernanceEngine`/`cross_chain.AssetRegistry` outside the `cross_chain`
package itself returns nothing. `governance.go`'s own `RegisterActiveChain(chainID)`
comment says it "adds a newly approved chain into the governance voter pool" but nothing
ever calls it from `Execute()` or anywhere in `gateway_handler.go` — new chains can never
actually join `ChainRegistry` through governance today, despite the vote/timelock
machinery existing and passing its own tests in isolation.

**Scope (design doc P0.2 and P6.1/P6.2):**

1. Add Gateway ABI write methods for propose/vote/execute on `GovernanceEngine`
   (propose → vote → ≥2/3 active chains → 72h timelock → executed), backed by a new field
   on `GatewayEngine` (same JSON-blob persistence pattern as
   `PendingCommitteeAttestations`/`RegisteredPops` from Milestone C).
2. Wire a successful governance execution of "onboard chain X" to actually call
   `RegisterActiveChain` **and** insert the new entry into `GatewayEngine.ChainRegistry`
   — this is the actual missing link; today `ChainRegistry` entries only exist because
   test code seeds them directly.
3. Same pattern for `AssetRegistry`: wire propose/vote/execute for registering a new
   `AssetEntry`, and wire the existing lock/mint verification path (`AssetRegistry`
   already has its own tests for the crypto/accounting side — the gap is purely "nothing
   calls it from the live handler").
4. Required tests per section 14: P0.2's (a)-(d) (propose+vote at threshold vs below
   threshold; execute before/after 72h; idempotent second execute); P6.1's fake
   self-registration rejection ("1 chain tự khai 'home_chain' cho asset nó không sở hữu
   phải bị từ chối").

This is lower-risk than E/F (no new cryptography, "just" wiring already-tested engines
into the live dispatch path) but unblocks the whole multi-chain-onboarding story, which
today has no live path at all.

---

### Milestone H — `verifyAndExecute()` and `claimDeadChainBalance()`

**Why:** explicitly deferred since Milestone A's original scope note in `gatewayAbi.go`
(line ~14: "`verifyAndExecute()` and `claimDeadChainBalance()` are deferred — they follow
the exact same pattern and can be added once this foundation is proven"). That foundation
(GatewayHandler dispatch, BLS verify, Merkle proof plumbing) is now proven across A/B/C —
this is comparatively mechanical follow-through, best done after E/F since both new
methods should reuse E's `AggregateValueLeaf`/proof machinery and F's cert-production
worker rather than duplicating them.

- `verifyAndExecute()` (P2.7): the atomic single-message fast path — Merkle-verify +
  execute in one call, no separate `attestCommit`/`claimMessage` split. Test: equivalent
  coverage to P2.2+P2.3 combined, atomic in one transaction.
- `claimDeadChainBalance()` (P2.8): claim against an `AccountLeaf` + Merkle proof once a
  chain has been declared dead via governance (Milestone G must land first — this
  literally depends on the "declare-dead" governance action existing). Tests: correct
  claim; double-claim blocked via a `deadChainClaimed` mapping; proof for a chain **not**
  yet declared dead must be rejected.

---

### Milestone I — Root Anchor network deployment + founding-chain onboarding (P1.1/P1.2)

**Why last among engineering tasks, and flagged separately:** this is infrastructure/ops
work (stand up an actual Root Anchor Metanode network via existing `deploy/ansible`/
`deploy/systemd` tooling, get real health checks green, then get ≥4 founding chains to
actually contribute validators) plus a real **business negotiation** dependency (P1.2)
that the design doc itself calls out as "not a technical task... the biggest variable" in
its own timeline estimate (section 15). An agent can do P1.1 (the deployment mechanics)
but P1.2 cannot be shortened by engineering work at all — flag this clearly to whoever
is coordinating rather than treating it as a normal backlog item with an ETA.

---

### Milestone J — Security audit + staged rollout (P5, section 12's T0–T5)

Once E/F/G/H are done and self-tested, this codebase will have shipped real production
cryptographic/consensus-adjacent logic across 4+ PRs with no external review yet. The
design doc's own section 8/15 timeline treats an **independent external audit** as a hard
gate before mainnet and explicitly as non-agent-shortenable (~4–8 weeks, external to the
dev team). Recommend scheduling this once E–H are merged, not deferring indefinitely —
don't let internal-agent-built cryptographic code go to mainnet without it, per the
design doc's own stated policy in section 5/12.

---

## Explicitly out of scope for "next steps" unless the task owner asks

- Reconciling Rust's unused min-sig `pop.rs`/`root_anchor.rs` mirror code — confirmed
  twice now (Milestone C's investigation) not to be needed for anything on this list;
  don't resurrect it speculatively.
- Rewriting/relaxing the Zero-Fork synchronous-FFI invariant for performance — section 13
  throughput optimizations (relayer batching/fee auction) are explicitly P4-and-later in
  the design doc's own roadmap, not a blocker for anything above.
