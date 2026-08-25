# Root Anchor Cross-Chain Bridge — Production Readiness Plan

Status as of 2026-08-25: Milestones **A through I** of the wiring plan are implemented.
PR #56 (branch `cross-chain-milestones-e-to-i`) fixes 2 critical bugs found in Milestones
E and G during review and is pending CI/merge — **read that PR before anything else**,
and if it isn't merged yet, either merge it first or make sure whatever you do here
starts from its branch, not from `dev` alone.

This document is a **handoff plan for a fresh agent/session with no prior context** to
take the system from "implemented and unit-tested" to "safe to run in production with
real money." It assumes you've read `note/cross_chain_root_anchor_architecture.md` (the
design doc) and `note/cross_chain_wiring_plan_next_steps.md` (the plan that produced
Milestones E-I) — this document picks up where those leave off.

**Bottom line up front: this system is not production-ready today.** Real cryptographic
verification exists and is unit/integration tested, but (a) at least one more concrete,
same-shape bug is already known and unfixed (Phase 0 below), (b) nothing has run on a
real multi-node network yet, and (c) no external security review has happened. Two
independent review passes over freshly-written code in this project already turned up
3 critical bugs (Milestone E's proof target, Milestone G's missing vote auth, and the
Phase 0 bug below) — treat that as a base rate, not bad luck, and do not skip the gates
in this plan to save time.

## How to work (read once, applies to every phase below)

- **Zero-Fork invariant.** Never let a background worker or async path write to
  consensus-relevant state (anything that becomes part of a signed commit or state
  root) — every past fork-risk in this project was exactly that pattern. If a fix
  requires new state, thread it through the same synchronous request/response paths
  already used by `GatewayHandler`.
- **Verification bar before every commit:** `go build ./... && go vet ./... && go test
  ./...` from `execution/`, zero regressions, `gofmt -l` clean.
- **Testing philosophy:** use real crypto and real production code paths end-to-end
  wherever feasible (real BLS sign/verify/aggregate, real ABI encode/decode, real signed
  transactions) — not mocks. If a test can only pass by having the test itself set a
  state value that no production code path would ever produce for a real caller, that's
  not a passing test, that's the bug hiding (see Phase 0 — this is exactly how the two
  bugs fixed in PR #56 were found).
- **A finding is not "handled" until it has a regression test** proving the specific
  attack/bug is rejected, not just a code review conclusion.
- **Git/PR workflow:** branch off `dev` (or off PR #56's branch if it isn't merged yet),
  commit, push, open a PR via `gh`, watch CI to green. **Do not self-merge** — the
  project owner merges PRs personally.
- Before implementing anything ambiguous, **stop and surface it** rather than guessing —
  Phase 0 below explicitly flags where the design isn't fully pinned down yet.

---

## Phase 0 (do first, blocking) — Fix `claimDeadChainBalance()`'s account-proof binding gap

**This is a live, already-shipped bug, the same shape as the one just fixed in PR #56's
`attestCommit()` fix, in code that has NOT yet been fixed.**

`ClaimDeadChainBalance` (`execution/pkg/cross_chain/gateway.go`) verifies a claimed
account balance like this:

```go
expectedLeafHash := HashAccountLeaf(AccountLeaf{Account: account, Balance: amount})
...
if !VerifyMerkleProof(accountLeafHash, proof, registry.StateRoot) {
    return ErrInvalidMerkleProof
}
```

`registry.StateRoot` is populated from the real production account-state trie root
(`AccountStatesRoot()` off a real block header, wired in Milestone C's
`CommitteeAttestationWorker.stateRootAtBlock`) — a Merkle-Patricia/NOMT trie. The proof
format being verified (`MerkleProof{LeafIndex, Siblings}`) comes from
`BuildAccountMerkleTree`, a completely different, simple binary Merkle tree
(`hashPair`-based) that exists only in `pkg/cross_chain`. **Nothing in this codebase ever
builds a `BuildAccountMerkleTree` proof that validates against a real production
`AccountStatesRoot()`** — confirmed by grep: `execution/pkg/trie` exposes no
`Prove`/`GetProof`/`VerifyProof` at all. Every existing test for this path
(`TestGateway_P2_8_ClaimDeadChainBalanceAndDuplicateGuard`,
`TestRelayer_Scenario10_8_DeadChainRecovery`,
`TestGatewayHandler_ClaimDeadChainBalance_Lifecycle`) only passes because the test
directly sets `ChainRegistry[deadChainID].StateRoot = <BuildAccountMerkleTree's own
root>` instead of a real trie root — the exact pattern that hid the Milestone E bug.

**Why this matters more than most gaps in this plan**: Chain-Death Recovery is the
last-resort mechanism for users to get their funds back when a chain has genuinely died.
If it doesn't actually work against real state, it will fail exactly when it's needed
most, likely discovered only during a real incident.

### What needs deciding (don't guess past this — it's a real design choice)

The design doc (section 11.6, `AccountLeaf`/Chain-Death Recovery) assumes an
account-level Merkle proof exists but — like `AggregateValueLeaf` before the Phase-0 fix
in PR #56 — doesn't fully specify how a real chain produces one against its real trie.
Two candidate approaches; investigate both before committing to one:

1. **Build real inclusion-proof generation for the production trie backend(s)**
   (`pkg/trie`, MPT and/or NOMT). This is the "correct" long-term answer — it removes
   the parallel tree entirely — but it's a nontrivial trie-internals feature and touches
   code well outside `pkg/cross_chain`. Check whether the underlying trie library
   (`pkg/trie/node`, `pkg/nomt_ffi`) already has anything proof-adjacent before assuming
   this is greenfield.
2. **Build a real, periodically-checkpointed account snapshot tree** using the existing
   `BuildAccountMerkleTree` scheme: at some well-defined point (e.g., every epoch
   boundary, alongside the existing `CommitteeUpdate`/`stateRootAtBlock` flow in
   `committee_attestation_worker.go`), walk the chain's full account set for real
   (`AccountStateDB` exposes enumeration — check `GetAllValidators`-equivalent for
   accounts, or trie iteration) and build+commit its `BuildAccountMerkleTree` root as a
   **new, separate field** on `ChainRegistry` (do not overload the existing `StateRoot`
   field, which is legitimately used for something else — the real trie root synced via
   `CommitteeUpdate`). This is more tractable but adds a second per-epoch commitment and
   needs its own strategy for the "how much of a stale snapshot is acceptable for a
   just-declared-dead chain" question.

Whichever you pick, the required tests are the same as Phase 0's own bar: real account
enumeration → real tree → real proof → verified against what a real chain would actually
expose, no test-forged roots. Update the three tests named above to match.

---

## Phase 1 — Close remaining known functional gaps

Lower urgency than Phase 0, but should land before T2 testnet (Phase 2) so the testnet
run exercises the real system, not a system with known-missing pieces.

1. **Epoch catch-up.** `ApplyCommitteeUpdate` only accepts strictly sequential epochs
   (documented limitation from Milestone C). If a chain misses Root Anchor connectivity
   across multiple epochs, it currently has no way to catch up other than replaying every
   missed epoch sequentially (and it may not have the old committee's signatures for
   epochs it missed). Decide and implement a bounded recovery path, or explicitly accept
   the limitation with a monitoring alert (`GatewayRegistryMonitor` already detects the
   drift — wire an alert on it) so operators know when a chain needs manual intervention.
2. **Deployment tooling smoke-test.** `deploy/systemd/gen_root_anchor_chain.py` and
   `setup_root_anchor.sh` (Milestone I) have never been run — confirmed only that
   `metanode keytool generate validator` exists as a subcommand, not that the scripts'
   exact invocation/output format matches what the scripts assume. Run them for real
   against a freshly-built `metanode` binary before Phase 2 needs them.
3. **Second read-through of Milestones F, H, I** at the same depth Phase-0-style review
   gave E and G. F (`CommitAttestationWorker`) and the `RelayerDaemon` (I) were reviewed
   structurally and looked sound, but did not get the same adversarial "what would make
   this test pass without the real thing being true" scrutiny that found the Phase 0 bug.
   In particular: check `RelayerDaemon`'s key handling (`RelayerKeyHex`,
   `RootAnchorSubmitterPrivateKeyHex` are plain config strings — fine for a
   reference/testnet daemon, but flag it explicitly if anyone considers this the
   long-term mainnet key-management story, since it isn't one).
4. **Governance `propose()` has no gating.** Confirm this is intentional (permissionless
   proposal, gated only at the vote/quorum stage) per the design doc rather than an
   oversight, and if intentional, make sure there's a rate-limit or spam consideration
   before mainnet (unbounded free proposals against a map keyed by content hash is a
   minor griefing/storage-growth vector, not a fund-safety issue, but worth a decision).

---

## Phase 2 — T0–T3 per the design doc's own rollout plan (section 12)

The design doc's section 12 table is the authoritative gate sequence; this section maps
current status onto it rather than re-deriving a new one.

| Stage | Design doc requirement | Current status |
|---|---|---|
| **T0 — Unit test** | 100% pass, error-branch coverage, not just happy path | Largely satisfied — `TestAudit_*` (9 tests) and per-function unit tests cover BLS verify, Merkle tamper, replay, hop-count, epoch fail-closed, etc. Re-run after Phase 0/1 land. |
| **T1 — Local devnet, all 8 scenarios automated** | 1 Reserve + 2 private chains, scenarios 10.1–10.8 all pass | **Logically covered** (`TestRelayer_Scenario10_1` through `10_8` all exist and pass) but these run in-process against Go structs directly, not against real running Metanode node processes with real RPC/consensus. Re-run the same 8 scenarios end-to-end against real nodes (see T2) before calling T1 actually satisfied in the doc's sense — in-process tests are necessary but not sufficient, and are exactly the kind of test that hid the Milestone E/Phase-0 bugs. |
| **T2 — Multi-chain testnet, real infra** | ≥4 real chains, real network latency, measure real BLS/commit costs and throughput at 500/2000/4000 msg batches | **Not started.** Use `deploy/systemd/gen_root_anchor_chain.py`/`setup_root_anchor.sh` (Phase 1 item 2) plus existing `deploy/ansible` tooling to stand up Root Anchor + ≥4 private chains on separate machines/VMs. Run the RelayerDaemon (Milestone I) for real between them. This is where Phase 0's fix must be validated for real (a chain actually dies or is simulated dead, real trie state, real proof). |
| **T3 — Adversarial/chaos testing** | Simulate compromising 3/4 validators of a small chain and repeat scenario 10.7; kill Reserve temporarily; full Chain-Death Recovery end-to-end at least once on testnet | **Not started** — depends on T2 infrastructure existing first. This is also where Phase 0's fix gets its real proof: Chain-Death Recovery must succeed end-to-end on the T2 testnet, not just in a unit test. |

Do not proceed to Phase 3 (audit) without real T2 numbers — the audit firm will want them,
and per the design doc's own stated principle, no stage should be skipped by "trusting"
instead of measuring.

---

## Phase 3 — External security audit (design doc P5 / section 8)

Non-negotiable per the design doc's own policy, and reinforced by this project's own
track record: two internal review passes on Milestones E-H already found 3 critical
bugs in freshly-written cryptographic/authorization code. Engage an independent
external audit covering the full verify stack (BLS, Merkle proofs of both kinds once
Phase 0 lands, replay protection, double-mint protection, origin-sender context
integrity, governance authorization) plus the 3 adversarial scenarios from T3. Do not
proceed to Phase 4 with any open Critical/High finding — this is a hardcoded gate in the
design doc, not a suggestion.

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
significant. Scope it explicitly to the new attack surface: BLS/Merkle verification,
`per_chain_allocation` accounting, governance authorization, Chain-Death Recovery.

---

## Explicitly not in scope for this plan

- Rebuilding Rust consensus internals — confirmed multiple times across Milestones B-I
  that everything needed lives in Go using Go's own min-pk BLS keys; don't resurrect
  `consensus/metanode/src/types/{pop,root_anchor}.rs` speculatively.
- Performance/throughput optimizations beyond what's needed to get real T2 numbers
  (design doc section 13's relayer batching/fee-auction ideas are explicitly P4-and-later
  in the doc's own roadmap, not a production-readiness blocker).
- Founding-chain business negotiation (P1.2) — track it, but no engineering effort
  shortens it; it can proceed in parallel with Phases 0-3.
