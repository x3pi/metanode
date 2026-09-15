# Vendored `parking_lot_core` 0.9.12 with a hand-applied upstream fix

## Why this exists

See project memory "Phuong an A: consensus halt-not-guess" mục 21/22 for the
full incident history. Summary: `GLOBAL_TX_CACHE` and `dag_state`, both
guarded by `parking_lot::RwLock`, froze permanently 3 times on this project's
live 4-node cluster (node-1, node-2, node-0), each time confirmed via two
`sudo gdb -p <pid> -batch -ex "thread apply all bt"` snapshots seconds/tens-
of-seconds apart showing **byte-identical** thread stacks stuck inside
`parking_lot::raw_rwlock::RawRwLock::lock_exclusive_slow`/`lock_shared_slow`/
`wait_for_readers`, well past the stated bound of `try_read_for`/
`try_write_for` (3s, one case 6.6x past it; another case past even a 15s
multi-retry bound). This matches `github.com/Amanieu/parking_lot` issue #212
("A write rwlock deadlocks when there are two shared read locks present on
another thread") — parking_lot 0.12.5 (this project's pinned version) is
confirmed the latest release, and #212's fix PR (#533) is a 51-file / ~1200+
line refactor requiring Rust 1.95 + edition 2024, unmerged and infeasible to
depend on wholesale (this project's toolchain is 1.90.0).

## What's actually patched here

Rather than depend on the whole PR #533 branch, this vendors just
`parking_lot_core` 0.9.12 (the actual crate whose `word_lock.rs` implements
the low-level park/unpark queue that `parking_lot`'s `RwLock` slow paths --
including `wait_for_readers`, the exact function seen stuck in every
incident -- are built on) and hand-applies ONE specific, small, isolated
commit from within PR #533:

**`77e184d` "Fix WordLock queue unlock retries"** (upstream file
`core/src/word_lock.rs`, i.e. this crate's `src/word_lock.rs`) -- confirmed
via `git log` on the PR to land BEFORE the MSRV/edition-2024 commits in the
PR's own history for one of the two hunks, and structurally verified by
direct comparison that our local 0.9.12 source matches the "before" state of
this diff closely enough to apply by hand cleanly (translated from the
upstream's `AtomicPtr::map_addr` style, from a later revision of the file, to
this version's plain `AtomicUsize` bit-op style -- same logic, same memory
orderings, no behavior difference from the translation itself).

The bug this fixes, in `WordLock::unlock_slow`:
1. The queue-lock-acquisition CAS discarded its returned `previous` value and
   kept using the stale pre-CAS `state`, so `state.queue_head()` reads later
   in the function could observe an out-of-date queue head even after
   `QUEUE_LOCKED_BIT` was already successfully set.
2. The retry logic after a failed "remove the tail and unlock" CAS used a
   manual `fence_acquire()` call instead of baking `Ordering::Acquire`
   directly into the CAS itself -- a fence-vs-CAS ordering mismatch that is
   not guaranteed to synchronize-with the write it needs to observe on every
   platform/compiler -- AND decided whether to retry the inner loop or jump
   back to the outer `'outer` loop by checking `state.queue_head().is_null()`,
   which conflates "the queue is now empty" with "a new thread was enqueued
   after we started processing" -- the wrong condition to distinguish those
   two genuinely different cases.
3. The final unpark's own comment claimed "we don't need to worry about any
   races here since the thread is guaranteed to be sleeping right now" --
   upstream's own replacement comment ("It may not have entered `park` yet,
   but `prepare_park` ensures that this unpark cannot be missed") suggests
   this assumption was not actually safe.

Any of these three could plausibly explain a writer stuck in
`wait_for_readers` never being woken even after the readers it's waiting on
have genuinely finished -- exactly this project's observed symptom.

## Honest confidence level

This is the most specific, best-matching, and most surgically isolable fix
found after auditing PR #533's full commit history (22 commits) for anything
touching the actual park/unpark/wait_for_readers path rather than the
upgradable-lock feature (which this project's code never uses -- ruled out
`3386011`/`b188fc6`) or the unrelated `Condvar` primitive (ruled out
`3c420be`). It has NOT been proven to be THE cause of the 3 live incidents --
this bug class has never been reproduced on demand, only observed
organically 3 times, so there is no repeatable before/after test available.
Treat this as the best current hypothesis, verified for structural
correctness and tested against `parking_lot_core`'s own test suite (see
below), not as a confirmed root-cause fix.

## How to verify / re-verify

```
cd consensus/vendor/parking_lot_core-0.9.12
cargo test
```

This crate's own tests should pass identically to the unpatched 0.9.12 (the
fix does not change any public API or documented behavior, only internal
memory-ordering and retry-condition correctness). If parking_lot ever
releases an official fix for issue #212, prefer switching back to the
released, community-tested version over keeping this hand patch indefinitely
-- check `https://github.com/Amanieu/parking_lot/issues/212` and
`https://crates.io/crates/parking_lot_core/versions` periodically.

## Wiring

Applied via `[patch.crates-io]` in the workspace root `Cargo.toml`, pinned to
this local path -- see that file's own comment for the exact stanza. Only
`parking_lot_core` is patched; `parking_lot` itself stays on the normal
crates.io 0.12.5 release (it depends on `parking_lot_core` by version range,
which Cargo's patch mechanism transparently redirects to this local copy).
