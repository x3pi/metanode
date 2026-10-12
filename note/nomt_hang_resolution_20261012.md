# NOMT/Block-STM validator hang — root cause, fix, evidence, and `dev` vs `main` review (2026-10-12)

## 1. Symptom
A BFT validator stops executing blocks forever (Go `WATCHDOG-EVM ... ProcessTransactionsOptimistic` repeating; the other three validators keep going).
Observed on this cluster roughly 1 run in 10 under saturated load (5 hangs seen on the unfixed code during this investigation).

## 2. Root cause (what is proven and what is not)
**Proven (live captures, `note/evidence/nomt_hang_20261012/`):**
- The stuck thread is inside `nomt_commit_payload -> Store::commit -> SyncController::post_meta -> writeout::write_ht -> IoHandle::recv()`, parked on a crossbeam channel. ~107–110 reader threads are blocked behind it in `Nomt::read -> RawRwLock::lock_shared_slow` (a consequence, not the cause), and the Go speculative executor waits on them.
- With per-call diagnostics (`write_ht` guard + `Receiver::len()` + io_uring `fdinfo`): `expected=11 sent=11 received=0 completion_channel_len=11 channel_is_empty=false`; global I/O counters balanced (36297 packets sent/received/reaped/completed), `workers_idle=9`, no panic, no I/O error in dmesg.
  => the 11 completions **were queued in the channel, but the receiver was never woken**: a lost wake-up on the completion channel.
- A spurious futex wake of the parked thread (state set to NOTIFIED) made it re-park: no sender had selected it.

**Not identified:** which layer loses the wake-up (crossbeam-channel 0.5.15, std futex parker, or the kernel). The workaround below does not depend on that.

## 3. Fix
`consensus/vendor/nomt` `IoHandle::recv()` now polls `recv_timeout(100 ms)` in a loop (documented in `consensus/vendor/NOMT_PATCHES.md` #2).
It waits for exactly the same completions, indefinitely, and decides nothing by elapsed time, so it cannot change any committed state (Zero-Fork safe).
Safety net for any other hang inside block execution: `StartWatchdogFatal` (EVM/STATE phases) dumps all goroutines and aborts after `METANODE_EXEC_STUCK_ABORT_SECS` (default 300 s, 0 disables) so systemd restarts the node, which re-syncs from its peers (fail-stop, not a dispatch decision).

## 4. Evidence the fix works (statistical — mechanism not proven)
| Binary | Saturated BFT runs | Hangs |
| :-- | :--: | :--: |
| without the fix (HEAD code, several builds) | ~10 per hang | 5 hangs seen (m19_FINAL_2, b_orig_head_3, h2_7, h3_14, ab_HEAD_1) |
| with the fix (`FIX` 43 runs, `FIX2` 8 runs) | **51** | **0** |
If the fix did nothing and the hang rate were ~10 %, seeing 0 in 43 runs has probability ≈ 1 %.
Throughput with the fix is not lower than without it (BFT 10–11.7k tx/s vs 6.8–10.6k on the unfixed binary in the same session; machine noise dominates).
Caveat: the marker log line `[NOMT-IO] recv waited ...` cannot tell slow I/O from a recovered wake-up (crossbeam returns the recovered message as `Ok`).

## 5. Measurement artifact found on the way
`secp_tps_blast` counts a tx as "submitted" once the bytes reach the kernel send buffer. In ~6 of ~60 runs the validators' application layer received fewer txs than the tool reports (e.g. 4,368,468 received vs 4,601,580 "submitted"); every tx a validator did receive landed on chain (sum of `Added batch to pool ... accepted` == on-chain count) and all validators ended at the same block. A half-close/drain on tool exit did NOT remove the gap, so the cause of the missing in-flight data is still unknown. For loops, use "validator-accepted == on-chain" as the completeness criterion, and report the tool shortfall separately.
Gemini's phase-4 `CATCHUP_ACTIVE (timeout drain)` runs (b_diag_10, b_diag_24) are probably the same artifact, not catch-up.

## 6. Review of `dev` vs `main` (62 commits)
Mostly documentation/evidence (≈45 commits). Product code reviewed:
- **Ingress lossless TCP admission** (`pkg/network/{admission,connection,server,config}.go`, `processors.go`, `transaction_processor.go`, forwarder bookkeeping): reviewed in this session, covered by socket-level tests; idle-deadline restart after backpressure verified in `connection.go`.
- **Sort keys + per-type signer** (`1408ddd6`): equivalence test on 100k txs with a negative control against the production `SortTxsByFromAndNonce`.
- **Commit syncer prefix fetch** (`27330f8d`): 5 unit tests incl. mutation check; independently reproduced on cap 16 MiB (BASE fails 3/6, HEAD 0/6) and at the real cap (BASE 1/6).
- **B1 parallel speculative root derivation** (`8d5aa6cc`): **fork hazard found and fixed** (`4d5b7260`): precomputed txs/receipts roots from the speculative execution were still used after a sequential re-execution.
- **Bounded two-generation mapping cache** (`4221d60a`): read cache only (falls back to DB), TTL removed; no consensus impact found.
- **Raft micro-metrics** (`raftfeed/*`): observability only.
- Log noise: per-block `[PREPARE-TX]` at Warn gated behind `METANODE_FFI_TRACE` (`4d5b7260`).
Open risks before merging to `main`: the NOMT fix is statistically (not mechanically) validated; the watchdog assumes a restarting supervisor; Raft/BFT numbers are single-machine.
