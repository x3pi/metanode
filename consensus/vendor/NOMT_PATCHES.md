# Local patches on vendored NOMT (`consensus/vendor/nomt`)

Upstream: https://github.com/thrumdev/nomt.git @ `3b64ba527fca26ddbc69d16eea7f6c2d0b4c65cd`.
The vendored tree MUST equal upstream plus ONLY the patches listed here (verify with `git archive 3b64ba5 | diff -r`).

1. `nomt/src/beatree/ops/update/leaf_stage.rs` and `branch_stage.rs`: `filter_leaves_changeset` / `filter_branch_changeset`
   return early when the changeset has fewer than 2 entries. Without it `for i in 0..len() - 1` underflows (usize) on an
   empty changeset, panicking the NOMT worker thread. The early return is a pure no-op for len < 2.

2. `nomt/src/io/mod.rs` `IoHandle::recv`: polls `recv_timeout(100ms)` in a loop instead of one blocking `recv()`.
   Evidence (2026-10-12, `note/evidence/nomt_hang_20261012/`): a BFT validator hung forever in
   `nomt_commit_payload -> write_ht -> IoHandle::recv`, while the diagnostics showed `completion_channel_len=11,
   channel_is_empty=false`, all 36297 I/O packets received/reaped/completed, and every io-worker idle: the completions
   were queued but the receiver was never woken. The loop only re-checks the queue; it waits for the same completions
   indefinitely and decides nothing by time, so it cannot change any committed state. If the workaround ever fires it
   prints `[NOMT-IO] recovered a lost wake-up`.

DO NOT remove fsync/`sync_all`/`sync_data` calls (`io/fsyncer.rs`, `seglog/mod.rs`, `seglog/segment_rw.rs`). An earlier
version of this vendor commit shipped with ALL NOMT fsyncs disabled (copied from a locally hand-edited cargo checkout);
that breaks crash/power-loss consistency (see the 2026-09-24 power-loss incident) and was reverted.
