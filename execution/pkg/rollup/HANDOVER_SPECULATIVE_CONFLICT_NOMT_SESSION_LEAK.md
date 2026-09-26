# Handover: speculative-executor conflict path leaves a NOMT session pending (pipeline deadlock)

> Found 2026-09-26 while running the C2 Raft cluster live. **Not fixed in the executor** (hot path shared with the Rust consensus mode; the right way to discard a speculative state needs the module owner). `raftfeed` avoids it with a delivery gate (block n is handed to the pipeline only when block n-1 is durable), see `NEXT_STEPS_PLAN.md` N3b.

## Symptom
Under a fast stream of small blocks (raft mode / C1 feeder, no gate) the block pipeline stops for good: `WATCHDOG-STATE ... Block #N đang thực thi IntermediateRoot`, `eth_blockNumber` frozen, later speculative blocks report `FAST-PATH-NONCE-REJECT` (they run on a stale base). Nothing recovers without a restart.

## Reproduction (before the gate; git `07ac9c45` = C1 without the gate)
1. Config: `consensus_mode="raft"`, no `raft{}` block (C1 single node), `private_key` = devnet BLS key `2b3aa0f6…203b` (see memory note on the live-probe recipe), start with `-debug -pprof-addr localhost:PORT`.
2. Send 1500 native transfers from each of 2 different funded genesis keys concurrently (same node RPC).
3. Within ~1 minute: watchdog message above, height stops (seen at blocks 115, 120 on two runs; on a 3-node cluster it hit two of three nodes at different blocks).
4. `curl localhost:PORT/debug/pprof/goroutine?debug=2`.

## Evidence
- Exactly one `COMMITTER-CONFLICT: specParentHash=… ≠ actualParentHash=… for GEI=114. Re-executing sequentially.` right before the freeze (`speculative_executor.go`, `commitSpeculativeResult`).
- Goroutine dump: two goroutines parked in `sync.Cond.Wait` at `pkg/nomt_ffi/bridge.go:524` (`BeginSession`, loop `for h.closing || h.activeCount > 0`), called from `nomt_state_trie.go:1504` (`Commit`) ← `account_state_db_commit.go:998` (`IntermediateRoot` inline NOMT commit) ← `tx_processor.go:182`. **No goroutine holds the session**: `activeCount` was left > 0.

## Explanation (consistent with every observation; not proven by a patch)
`IntermediateRoot` runs the real NOMT `Commit` (`session.Finish`), which stages a `pendingFinishedSession` on the trie **and keeps `activeCount` incremented until `CommitPayload`/`Abort`** (`bridge.go` ~861: "we DO NOT decrement activeCount here"). Speculative execution of block n runs on a `ChainState.CloneSpeculative` trie copy sharing the same NOMT handle. If block n-1 committed after that clone was taken, `commitSpeculativeResult` sees `specParentHash ≠ actualParentHash`, clones the real tip and re-executes — but never releases the discarded `res.ClonedState`'s pending finished session first. The re-execution's own `IntermediateRoot` → `BeginSession` waits for `activeCount == 0` forever.

## Why the Rust mode does not hit it
Rust consensus is effectively lock-step (it waits for the execution response of one commit before sending the next), so speculation on a stale parent is rare. A feed that pushes blocks as fast as they arrive (raft) hits it immediately.

## Why I did not patch it
- `CloseSpeculative()` calls `AccountStateDB.Close()` → `NomtStateTrie.Close()` → **`CommitPayload()`**, i.e. it would *persist* the discarded speculative state into NOMT. A conflict-path fix needs an *abort* (drop the finished session, release `activeCount`, drop pending changelog) for account, stake and contract tries, and the callers that already use `CloseSpeculative` for "obsolete" states (`CleanGEI`, already-committed discard, aborted sessions) look suspect for the same reason.
- Shared hot path + consensus-critical state: needs the owner's decision and a test that forces the conflict (e.g. delay the commit of n-1 while n is speculated).

## Suggested fix outline
1. Add `Abort/DiscardPending` to the trie interface (NOMT: `pendingFinishedSession.Abort()` under `LockCommitPayload`, clear `pendingChangelog`, republish read view without the committing map).
2. Call it for `res.ClonedState` in the conflict branch before re-execution (and audit the other `CloseSpeculative` call sites).
3. Regression test: force parent-hash conflict with a non-empty pending session and assert `BeginSession` proceeds; then remove the need for `raftfeed`'s delivery gate only if throughput requires it (gate ceiling ≈ 1/commit latency; measured ~9.2k tx/s end to end with 2000 senders, so it is not the bottleneck today).
