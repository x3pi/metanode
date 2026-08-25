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

**Bottom line up front: this system is still not production-ready.** Real cryptographic
verification exists and is unit/integration tested, and the two known proof-binding bugs
are now fixed — but nothing has run on a real multi-node network yet, and no external
security review has happened. **3 critical bugs found across 2 internal review passes on
freshly-written code is a base rate, not bad luck** — do not skip the gates below to save
time, and stay suspicious of any new cryptographic/authorization code the same way.

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
2. **Deployment tooling smoke-test.** `deploy/systemd/gen_root_anchor_chain.py` and
   `setup_root_anchor.sh` (Milestone I) have never been run end-to-end — only confirmed
   that `metanode keytool generate validator` exists as a CLI subcommand, not that the
   scripts' exact invocation/output format matches what they assume (flag names, output
   file paths/structure, etc.). Build `metanode` for real (see
   `note/metanode-build-environment.md` if using this session's memory, or the repo's own
   build docs) and run both scripts for real before Phase 2 needs them. Fix whatever
   breaks; this is expected to surface small mismatches, not a design problem.
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
