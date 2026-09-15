// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0
use std::{collections::BTreeMap, sync::Arc};

use consensus_config::Epoch;
use consensus_types::block::{
    BlockRef, Round, TransactionIndex, NUM_RESERVED_TRANSACTION_INDICES, PING_TRANSACTION_INDEX,
};
use parking_lot::{Mutex, RwLock};
use tap::TapFallible;
use thiserror::Error;
use tokio::sync::mpsc::{channel, Receiver, Sender};
use tokio::sync::oneshot;
use tracing::{error, warn};

use crate::{block::Transaction, context::Context};
use consensus_types::block::TxDigest;
use std::collections::VecDeque;
use std::sync::OnceLock;

/// Previously configured the on-disk directory for per-transaction payload
/// persistence (one file per TX digest) in TxPayloadCache, so BlockV3
/// (compact block) reconstruction could recover a TX body after a restart
/// even if it had been evicted from the in-memory cache. Removed: writing
/// one file per TX synchronously (via spawn_blocking) made every submitted
/// transaction do its own disk I/O, and at burst volume this queued tens of
/// thousands of blocking-pool writes at once — a measured, primary
/// contributor to multi-second consensus stalls under load.
///
/// Trade-off accepted by removing this: TxPayloadCache is now purely
/// in-memory (bounded, LRU-evicted). If a node restarts and needs to
/// reconstruct a compact block whose TX bodies were evicted or never cached
/// locally, that TX body is unrecoverable from disk. In the normal
/// operating case (no restart), this has no effect. If a TX body actually
/// goes missing this way, the affected client's TX is simply not included /
/// resolvable in that block and can be resubmitted — consistent with how
/// this codebase already treats other transient TX loss (see tx_recycler).
/// No longer read or written; kept only so any external code still calling
/// `.set()` on it continues to compile without effect.
pub static TX_PAYLOAD_DIR: OnceLock<std::path::PathBuf> = OnceLock::new();

pub struct TxPayloadCache {
    map: BTreeMap<TxDigest, Transaction>,
    queue: VecDeque<TxDigest>,
    capacity: usize,
}

impl TxPayloadCache {
    pub fn new(capacity: usize) -> Self {
        Self {
            map: BTreeMap::new(),
            queue: VecDeque::new(),
            capacity,
        }
    }

    pub fn insert(&mut self, digest: TxDigest, tx: Transaction) {
        if self.map.contains_key(&digest) {
            return;
        }
        if self.queue.len() >= self.capacity {
            if let Some(oldest) = self.queue.pop_front() {
                self.map.remove(&oldest);
            }
        }
        self.queue.push_back(digest);

        // DISK PERSISTENCE REMOVED (see TX_PAYLOAD_DIR doc comment): this
        // used to spawn_blocking a std::fs::write per TX here, unconditionally,
        // on every submitted transaction. Under a TX burst that meant tens of
        // thousands of individual one-file-per-tx writes queued onto the
        // tokio blocking pool at once — traced (via perf, sudo-authorized by
        // the operator) to real multi-second stalls in consensus round
        // advancement on this repo's single-box multi-node test rigs, visible
        // as std::fs::write::inner dominating on-CPU samples during bursts.
        // In-memory cache only now; see TX_PAYLOAD_DIR for the durability
        // trade-off this accepts.

        self.map.insert(digest, tx);
    }

    pub fn get(&self, digest: &TxDigest) -> Option<Transaction> {
        self.map.get(digest).cloned()
    }
}

/// ARCHITECTURAL FIX (2026-09-15, mục 19 bug #6 architecture review): this cache used to
/// be ONE `RwLock<TxPayloadCache>` behind `GLOBAL_TX_CACHE`, with bounded
/// (`try_read_for`/`try_write_for`) acquisition -- see `TX_CACHE_LOCK_TIMEOUT`'s doc
/// comment for that earlier fix. That fix was real and necessary (it turns an unbounded
/// freeze into a bounded one) but was PROVEN INSUFFICIENT ON ITS OWN, live, via `sudo
/// gdb` on a node genuinely wedged during a large (3000+ commit) whole-cluster-restart
/// catch-up: dozens of concurrent `verify_block_inner` calls (one per in-flight block)
/// all still serialize on the exact same single lock. Bounding each individual wait to
/// 3s does not change that only one of them can ever hold it at a time -- the node still
/// made no forward progress for the whole catch-up window, just via many bounded waits
/// back-to-back instead of one unbounded one. Per direct user feedback after this was
/// found live ("kinh nghiệm tôi nó thường giảm thiểu lỗi chưa bao giờ giải quyết được
/// 100% vấn đề" -- experience shows timeouts usually just reduce the damage, never fully
/// solve the problem) the actual fix is architectural, not a smaller timeout: shard the
/// cache by digest into `NUM_TX_CACHE_SHARDS` independent locks, so calls for DIFFERENT
/// transactions (the overwhelmingly common case -- distinct blocks reference distinct
/// digests) never contend with each other at all, no matter how many run concurrently.
/// A cryptographic digest's leading byte is already uniformly distributed, so
/// `digest.0[0] % NUM_TX_CACHE_SHARDS` spreads load evenly with no extra hashing. This
/// mirrors how real high-throughput systems (Sui's own per-object locking, researched at
/// the user's explicit request earlier this session) avoid a single global lock across
/// independent keys in the first place, rather than tuning contention on one lock down
/// to some hopefully-acceptable level.
const NUM_TX_CACHE_SHARDS: usize = 32;

/// CATCH-THE-CULPRIT INSTRUMENTATION (2026-09-15, same day as the sharding fix above):
/// sharding alone did NOT fix the live incident -- redeployed and re-tested, the same
/// node stalled again with the same `verify_block_inner` / `lock_exclusive_slow`
/// signature, DAG commit index frozen for 5+ minutes, not self-recovering. This matches
/// -- and may finally explain -- the still-unresolved mystery flagged in
/// `TX_CACHE_LOCK_TIMEOUT`'s own doc comment below: a genuinely leaked guard (one that's
/// acquired but never released) would explain why bounding the WAIT doesn't help
/// (waiting bounds how long a *new* acquisition attempt blocks, but does nothing if the
/// lock itself will never become free again) and why sharding doesn't help either (it
/// just confines the permanently-wedged state to whichever one shard the leaked guard's
/// digest happened to land on, instead of the whole cache). Per user direction after
/// this was found: instead of guessing at another timeout/architecture tweak, catch the
/// actual holder red-handed. Each shard now tracks who currently holds it (thread,
/// call-site, and acquisition time) via a small side `Mutex` -- negligible overhead
/// (held for nanoseconds around the real lock's own critical section) -- so that the
/// NEXT time any acquisition times out, it can immediately log exactly which thread has
/// been holding this shard, from which call site, and for how long. If that duration is
/// large (seconds/minutes) and the thread is still alive, that is direct proof of a
/// leaked guard, and the `site` string pinpoints which of the ~19 call sites is at fault
/// -- something no amount of after-the-fact `sudo gdb` stack-walking could pin down on
/// its own, since a `RwLock` guard carries no metadata about who is holding it.
#[derive(Clone, Copy, Debug)]
struct TxCacheHolderInfo {
    id: u64,
    thread: std::thread::ThreadId,
    site: &'static str,
    acquired_at: std::time::Instant,
}

static NEXT_TX_CACHE_HOLDER_ID: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

struct TxCacheShard {
    lock: RwLock<TxPayloadCache>,
    write_holder: Mutex<Option<TxCacheHolderInfo>>,
    read_holders: Mutex<Vec<TxCacheHolderInfo>>,
}

impl TxCacheShard {
    fn new(capacity: usize) -> Self {
        Self {
            lock: RwLock::new(TxPayloadCache::new(capacity)),
            write_holder: Mutex::new(None),
            read_holders: Mutex::new(Vec::new()),
        }
    }

    /// Logs exactly who is (or recently was) holding this shard, for the timeout branch
    /// of `get`/`insert` below to call right before giving up. Reads the diagnostic
    /// mutexes, not the real `RwLock` -- so this itself never blocks on the stuck lock.
    fn log_current_holders(&self, shard_idx: usize, waiting_site: &'static str, op: &str) {
        if let Some(w) = *self.write_holder.lock() {
            tracing::warn!(
                "🚨🔍 [TX-CACHE-LOCK-STUCK-HOLDER] {}() at {} timed out waiting for shard {} \
                 -- currently WRITE-HELD by thread {:?} (site=\"{}\") for {:?} so far. If this \
                 duration keeps growing across repeated timeouts and that thread is still \
                 alive, this is a genuinely leaked guard at that call site, not transient \
                 contention.",
                op, waiting_site, shard_idx, w.thread, w.site, w.acquired_at.elapsed()
            );
        }
        let readers = self.read_holders.lock();
        if !readers.is_empty() {
            for r in readers.iter() {
                tracing::warn!(
                    "🚨🔍 [TX-CACHE-LOCK-STUCK-HOLDER] {}() at {} timed out waiting for shard {} \
                     -- currently READ-HELD by thread {:?} (site=\"{}\") for {:?} so far \
                     ({} reader(s) total on this shard right now).",
                    op, waiting_site, shard_idx, r.thread, r.site, r.acquired_at.elapsed(), readers.len()
                );
            }
        }
        if self.write_holder.lock().is_none() && readers.is_empty() {
            tracing::warn!(
                "🚨🔍 [TX-CACHE-LOCK-STUCK-HOLDER] {}() at {} timed out waiting for shard {}, \
                 but no holder is currently recorded -- the holder released between the \
                 timeout and this check (a genuine race, harmless for diagnostics) or \
                 contention is from parking_lot's writer-preference fairness rather than an \
                 actual current holder.",
                op, waiting_site, shard_idx
            );
        }
    }
}

/// RAII marker: records this thread as the shard's write holder for exactly the
/// lifetime of the real `RwLockWriteGuard` it accompanies, and always clears itself on
/// drop (including on an unwinding panic) so a diagnostic mistake can never itself look
/// like a second, independent leak.
struct WriteHolderMarker<'a> {
    shard: &'a TxCacheShard,
}
impl<'a> WriteHolderMarker<'a> {
    fn new(shard: &'a TxCacheShard, site: &'static str) -> Self {
        *shard.write_holder.lock() = Some(TxCacheHolderInfo {
            id: NEXT_TX_CACHE_HOLDER_ID.fetch_add(1, std::sync::atomic::Ordering::Relaxed),
            thread: std::thread::current().id(),
            site,
            acquired_at: std::time::Instant::now(),
        });
        Self { shard }
    }
}
impl<'a> Drop for WriteHolderMarker<'a> {
    fn drop(&mut self) {
        *self.shard.write_holder.lock() = None;
    }
}

/// Read-side counterpart of [`WriteHolderMarker`]. Multiple readers can hold a shard at
/// once, so each carries a unique id to remove exactly itself (not another reader) on drop.
struct ReadHolderMarker<'a> {
    shard: &'a TxCacheShard,
    id: u64,
}
impl<'a> ReadHolderMarker<'a> {
    fn new(shard: &'a TxCacheShard, site: &'static str) -> Self {
        let id = NEXT_TX_CACHE_HOLDER_ID.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        shard.read_holders.lock().push(TxCacheHolderInfo {
            id,
            thread: std::thread::current().id(),
            site,
            acquired_at: std::time::Instant::now(),
        });
        Self { shard, id }
    }
}
impl<'a> Drop for ReadHolderMarker<'a> {
    fn drop(&mut self) {
        self.shard.read_holders.lock().retain(|h| h.id != self.id);
    }
}

static GLOBAL_TX_CACHE: OnceLock<Vec<TxCacheShard>> = OnceLock::new();

fn tx_cache_shards() -> &'static Vec<TxCacheShard> {
    GLOBAL_TX_CACHE.get_or_init(|| {
        let per_shard_capacity = (tx_payload_cache_capacity() / NUM_TX_CACHE_SHARDS).max(1);
        (0..NUM_TX_CACHE_SHARDS)
            .map(|_| TxCacheShard::new(per_shard_capacity))
            .collect()
    })
}

/// Selects the one shard a given digest belongs to. Same digest always maps to the same
/// shard (needed so a later `get()` finds what an earlier `insert()` put there).
fn shard_index(digest: &TxDigest) -> usize {
    digest.0[0] as usize % NUM_TX_CACHE_SHARDS
}

fn shard_for(digest: &TxDigest) -> &'static TxCacheShard {
    &tx_cache_shards()[shard_index(digest)]
}

#[cfg(test)]
pub(crate) fn tx_cache_insert_for_test(digest: TxDigest, tx: Transaction) {
    shard_for(&digest).lock.write().insert(digest, tx);
}

/// CONFIRMED ROOT CAUSE (2026-09-02/03 investigation) OF SILENT TX LOSS AT
/// EXTREME BURST SCALE: capacity here was hardcoded at 500_000, and
/// `TxPayloadCache::insert()` evicts the OLDEST entry by pure insertion
/// order whenever the cache is full -- with zero regard for whether that
/// entry's transaction has actually reached a dispatched (sent-to-Go)
/// block yet. Under an injection burst larger than capacity (e.g.
/// 1,000,000 txs in ~22s, ~44,700 tx/s), transactions submitted early in
/// the burst got evicted before consensus could propose+commit+dispatch
/// their containing block, permanently and silently dropping them --
/// surfacing only as a "Missing transaction for digest" warn! in
/// build_sorted_transactions (block_sending.rs). Live counters confirmed
/// the mechanism exactly: at 1,000,000 offered, ~509,000 confirmed
/// on-chain in the same run, matching the ~500,000 capacity almost
/// precisely, and Rust's own "dispatched to Go" counters showed ~972,000
/// succeeding at the dispatch step -- i.e. the loss was specifically
/// cache eviction between submission and dispatch, not a consensus or
/// execution failure.
///
/// Fix: raise capacity 10x (500_000 -> 5_000_000), comfortably exceeding
/// every burst size tested so far (up to 4,000,000 txs) with headroom.
/// Memory cost is small (Transaction bodies are typically ~100-300 bytes;
/// 5,000,000 entries costs roughly 1-2 GB resident) and acceptable on
/// this box. Overridable via METANODE_TX_CACHE_CAPACITY for further
/// tuning without a rebuild.
///
/// This is a capacity increase, not an eviction-policy fix -- an
/// arbitrarily large enough burst could in principle still outrun any
/// fixed capacity. A more robust fix (only ever evict entries already
/// consumed/dispatched, instead of blind insertion-order FIFO) was
/// considered but deferred: this same cache is also read by
/// multi-validator code paths (block_verifier, synchronizer,
/// authority_service peer-block handling) that may need to re-read a
/// transaction body after the local dispatch path has already read it,
/// so removing entries eagerly on first read risks breaking peer
/// verification/sync correctness in a multi-validator deployment --
/// not proven safe under current single-validator-only test coverage.
fn tx_payload_cache_capacity() -> usize {
    std::env::var("METANODE_TX_CACHE_CAPACITY")
        .ok()
        .and_then(|v| v.parse::<usize>().ok())
        .filter(|v| *v > 0)
        .unwrap_or(5_000_000)
}

/// ROOT FIX (2026-09-14, mục 19 bug #4 investigation): every acquisition of
/// `GLOBAL_TX_CACHE` used to go through a raw, unbounded `parking_lot::RwLock`
/// `.read()`/`.write()` call. Live-reproduced (via `sudo gdb -p <pid> -batch
/// -ex "thread apply all bt"` on a genuinely wedged validator, see the
/// project memory for the full incident) a permanent, 40+ minute freeze where
/// a writer (`block_verifier::verify_block_inner`, on the P2P block-receive
/// critical path for EVERY block) sat forever in
/// `RawRwLock::wait_for_readers()`, and 6+ other threads queued behind it in
/// `lock_exclusive_slow`/`lock_shared_slow` -- confirmed via a raw memory
/// read of the lock's own state word (value 0x10, exactly one reader's worth
/// of count with no writer bit) that a single reader guard was never
/// released, for reasons not fully pinned down (exhaustive audit of every
/// non-test call site found no explicit `.await` held across the guard, no
/// `unsafe`/`mem::forget`/`ManuallyDrop` -- the leak mechanism itself remains
/// an open question, flagged for follow-up).
///
/// PROVEN INEFFECTIVE: wrapping this kind of call in `tokio::time::timeout`
/// does NOT help -- a thread blocked in parking_lot's underlying futex wait
/// never returns control to the tokio executor, so the timer that would fire
/// the timeout is never polled (confirmed live: a `TransactionClient::submit`
/// call already wrapped in `tokio::time::timeout` was found stuck in the
/// exact same raw lock wait during this same incident).
///
/// Root fix: use parking_lot's OWN native timed acquisition
/// (`try_read_for`/`try_write_for`), which times out via the same underlying
/// futex-with-timeout primitive the kernel enforces directly -- this is NOT
/// an external cooperative timeout layered on top of a blocking call (which
/// is what just proved ineffective above), it is the lock itself giving up.
/// On timeout, every caller treats it exactly like a cache miss/skip: this
/// cache's whole design already tolerates that (eviction under load, a
/// never-cached payload) via existing recovery paths (peer re-fetch,
/// quorum-certified payload-loss attestation), so degrading a stuck lock into
/// a miss is safe, not a new correctness risk -- the alternative (the
/// previous behavior) was an unbounded, silent, unrecoverable freeze of the
/// entire node.
const TX_CACHE_LOCK_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(3);

/// A short-lived handle for bounded reads of the (sharded, see `NUM_TX_CACHE_SHARDS`)
/// global TX cache. Deliberately holds no lock itself: each `get()` call independently
/// locks -- with the same bounded, native-timeout acquisition as before -- only the ONE
/// shard that specific digest maps to, and releases it immediately after. This is what
/// makes sharding actually pay off: two `get()` calls for two different digests, even
/// from the same handle, can proceed fully in parallel (or one can be stuck without
/// affecting the other) instead of both serializing on one lock for the handle's whole
/// lifetime.
pub struct TxCacheReadHandle {
    site: &'static str,
}

impl TxCacheReadHandle {
    pub fn get(&self, digest: &TxDigest) -> Option<Transaction> {
        let shard_idx = shard_index(digest);
        let shard = shard_for(digest);
        match shard.lock.try_read_for(TX_CACHE_LOCK_TIMEOUT) {
            Some(guard) => {
                let _marker = ReadHolderMarker::new(shard, self.site);
                guard.get(digest)
            }
            None => {
                // WARN, not error!: see TX_CACHE_LOCK_TIMEOUT's doc comment -- error!/fatal!
                // route through Go's deliberately-synchronous, writeMu-locked log path (the
                // exact FFI-blocking class of bug this whole fix exists to avoid), while
                // warn!/info!/debug! use the async, non-blocking log queue. This can fire
                // from multiple contended threads at once, so it must stay on the safe side.
                shard.log_current_holders(shard_idx, self.site, "read");
                tracing::warn!(
                    "🚨 [TX-CACHE-LOCK-STUCK] read() at {} did not acquire this digest's \
                     GLOBAL_TX_CACHE shard within {:?} -- treating as a cache miss instead of \
                     blocking forever. Only the one shard holding this digest is affected; \
                     every other digest (the other {} shards) is unaffected.",
                    self.site,
                    TX_CACHE_LOCK_TIMEOUT,
                    NUM_TX_CACHE_SHARDS - 1
                );
                None
            }
        }
    }
}

/// Bounded read acquisition of the global TX cache. Always returns `Some` -- unlike the
/// pre-sharding version, no lock is actually taken until a specific digest is looked up
/// via the returned handle's `get()`, so there is nothing to time out on yet at this
/// point. Kept as `Option` so existing `if let Some(cache) = try_tx_cache_read(...)`
/// call sites did not need to change when this was rearchitected.
pub fn try_tx_cache_read(site: &'static str) -> Option<TxCacheReadHandle> {
    Some(TxCacheReadHandle { site })
}

/// Write-side counterpart of [`TxCacheReadHandle`]. Same no-lock-until-first-use design:
/// `insert()` locks only the one shard the digest being inserted maps to.
pub struct TxCacheWriteHandle {
    site: &'static str,
}

impl TxCacheWriteHandle {
    pub fn insert(&mut self, digest: TxDigest, tx: Transaction) {
        let shard_idx = shard_index(&digest);
        let shard = shard_for(&digest);
        match shard.lock.try_write_for(TX_CACHE_LOCK_TIMEOUT) {
            Some(mut guard) => {
                let _marker = WriteHolderMarker::new(shard, self.site);
                guard.insert(digest, tx);
            }
            None => {
                // WARN, not error! -- see the identical comment in TxCacheReadHandle::get above.
                shard.log_current_holders(shard_idx, self.site, "write");
                tracing::warn!(
                    "🚨 [TX-CACHE-LOCK-STUCK] write() at {} did not acquire this digest's \
                     GLOBAL_TX_CACHE shard within {:?} -- skipping this one cache update \
                     instead of blocking forever. Only this digest's shard is affected; \
                     every other digest (the other {} shards) is unaffected.",
                    self.site,
                    TX_CACHE_LOCK_TIMEOUT,
                    NUM_TX_CACHE_SHARDS - 1
                );
            }
        }
    }

    /// ARCHITECTURAL FIX (2026-09-15, same-day follow-up to the sharding fix above): a
    /// caller inserting MANY (digest, tx) pairs at once (chiefly `verify_block_inner`,
    /// once per transaction in a block) MUST use this instead of calling `insert()` in a
    /// loop. Live-reproduced: after sharding first shipped, redeploying under the exact
    /// same restart-catch-up scenario the sharding fix was built for showed NO
    /// individual acquisition ever timing out (zero `TX-CACHE-LOCK-STUCK` log lines the
    /// whole time -- ruled out via direct grep) yet the node still made no visible
    /// forward progress for minutes, CPU only ~1-1.6 cores busy (not CPU-bound), and
    /// `sudo gdb` kept catching 20+ threads inside the shard-lock's brief acquire path
    /// at every snapshot. Root cause: calling `insert()` once PER TRANSACTION turns a
    /// block with N transactions into N separate lock acquisitions where the
    /// pre-sharding code (and the original mục-19-bug-#4 fix) acquired the cache's lock
    /// only ONCE for the whole block. With ~20 blocks verified concurrently during
    /// catch-up, that is a many-times-N increase in total lock-acquisition *count*
    /// system-wide -- not a deadlock (nothing waits past `TX_CACHE_LOCK_TIMEOUT`), but
    /// per-acquisition overhead (parking_lot's CAS/futex bookkeeping, compounded by
    /// occasional brief same-shard collisions among 20 concurrent callers) accumulating
    /// into a severe THROUGHPUT regression at this volume. This method restores
    /// per-block-sized acquisition counts (one acquisition per shard actually touched by
    /// the batch, i.e. at most `NUM_TX_CACHE_SHARDS`, typically far fewer for a small
    /// block) while keeping sharding's real benefit: a batch destined for shard A never
    /// blocks a concurrent caller whose batch only touches shard B.
    pub fn insert_batch(&mut self, items: impl IntoIterator<Item = (TxDigest, Transaction)>) {
        let mut by_shard: Vec<Vec<(TxDigest, Transaction)>> =
            (0..NUM_TX_CACHE_SHARDS).map(|_| Vec::new()).collect();
        for (digest, tx) in items {
            by_shard[shard_index(&digest)].push((digest, tx));
        }
        for (shard_idx, batch) in by_shard.into_iter().enumerate() {
            if batch.is_empty() {
                continue;
            }
            let shard = &tx_cache_shards()[shard_idx];
            match shard.lock.try_write_for(TX_CACHE_LOCK_TIMEOUT) {
                Some(mut guard) => {
                    let _marker = WriteHolderMarker::new(shard, self.site);
                    for (digest, tx) in batch {
                        guard.insert(digest, tx);
                    }
                }
                None => {
                    shard.log_current_holders(shard_idx, self.site, "write_batch");
                    tracing::warn!(
                        "🚨 [TX-CACHE-LOCK-STUCK] write_batch() at {} did not acquire shard {} \
                         within {:?} -- skipping {} cache update(s) destined for this shard \
                         instead of blocking forever. Every other shard in this same batch (and \
                         this shard's own {} peer shards) is unaffected.",
                        self.site,
                        shard_idx,
                        TX_CACHE_LOCK_TIMEOUT,
                        batch.len(),
                        NUM_TX_CACHE_SHARDS - 1
                    );
                }
            }
        }
    }
}

/// Bounded write acquisition of the global TX cache. See [`try_tx_cache_read`] for why
/// this always returns `Some` now.
pub fn try_tx_cache_write(site: &'static str) -> Option<TxCacheWriteHandle> {
    Some(TxCacheWriteHandle { site })
}

/// Higher-stakes read handle for callers where treating a stuck shard as "not found" is
/// fork-relevant (currently: extracting system transactions, in particular EndOfEpoch,
/// from an already-committed subdag — see `CommittedSubDag::extract_system_transactions`/
/// `extract_end_of_epoch_transaction`). `get()` retries with real backoff (well past a
/// single `TX_CACHE_LOCK_TIMEOUT` window) before giving up on a digest, since reaching
/// this call at all means that digest's shard got stuck again after the same transaction
/// already passed a real cache check once during block verification -- a genuinely
/// abnormal, most likely transient condition.
pub struct TxCacheReadHandleForCommit {
    site: &'static str,
}

impl TxCacheReadHandleForCommit {
    pub fn get(&self, digest: &TxDigest) -> Option<Transaction> {
        const MAX_ATTEMPTS: u32 = 5; // 5 x 3s = 15s total before giving up on this digest
        let shard_idx = shard_index(digest);
        let shard = shard_for(digest);
        for attempt in 1..=MAX_ATTEMPTS {
            if let Some(guard) = shard.lock.try_read_for(TX_CACHE_LOCK_TIMEOUT) {
                let _marker = ReadHolderMarker::new(shard, self.site);
                return guard.get(digest);
            }
            shard.log_current_holders(shard_idx, self.site, "read (for-commit)");
            tracing::warn!(
                "🚨 [TX-CACHE-LOCK-STUCK] read() at {} attempt {}/{} did not acquire this \
                 digest's GLOBAL_TX_CACHE shard within {:?} -- retrying (this path retries \
                 because a miss here can be fork-relevant, unlike ordinary block-verification \
                 cache reads). Other digests in other shards are unaffected and not retried \
                 unnecessarily.",
                self.site,
                attempt,
                MAX_ATTEMPTS,
                TX_CACHE_LOCK_TIMEOUT
            );
        }
        // WARN, not error! -- see the identical comment in TxCacheReadHandle::get above.
        tracing::warn!(
            "🚨 [TX-CACHE-LOCK-STUCK] read() at {} gave up after {} attempts ({:?} total) on \
             this digest's shard -- treating as not found. That one shard is genuinely \
             wedged; any EndOfEpoch transaction whose digest hashes to it may have been \
             missed. Other digests (other shards) are unaffected.",
            self.site,
            MAX_ATTEMPTS,
            TX_CACHE_LOCK_TIMEOUT * MAX_ATTEMPTS
        );
        None
    }
}

/// See [`TxCacheReadHandleForCommit`]. Always returns `Some` -- see [`try_tx_cache_read`]
/// for why.
pub fn retry_tx_cache_read_for_commit(site: &'static str) -> Option<TxCacheReadHandleForCommit> {
    Some(TxCacheReadHandleForCommit { site })
}

/// The maximum number of transactions pending to the queue to be pulled for block proposal
const MAX_PENDING_TRANSACTIONS: usize = 200_000;

/// The guard acts as an acknowledgment mechanism for the inclusion of the transactions to a block.
/// When its last transaction is included to a block then `included_in_block_ack` will be signalled.
/// If the guard is dropped without getting acknowledged that means the transactions have not been
/// included to a block and the consensus is shutting down.
pub(crate) struct TransactionsGuard {
    // Holds a list of transactions to be included in the block.
    // A TransactionsGuard may be partially consumed by `TransactionConsumer`, in which case, this holds the remaining transactions.
    transactions: Vec<Transaction>,

    // When the transactions are included in a block, this will be signalled with
    // the following information
    included_in_block_ack: oneshot::Sender<(
        // The block reference in which the transactions have been included
        BlockRef,
        // The indices of the transactions that have been included in the block
        Vec<TransactionIndex>,
        // A receiver to notify the submitter about the block status
        oneshot::Receiver<BlockStatus>,
    )>,
}

/// The TransactionConsumer is responsible for fetching the next transactions to be included for the block proposals.
/// The transactions are submitted to a channel which is shared between the TransactionConsumer and the TransactionClient
/// and are pulled every time the `next` method is called.
pub(crate) struct TransactionConsumer {
    tx_receiver: Receiver<TransactionsGuard>,
    max_transactions_in_block_bytes: u64,
    max_num_transactions_in_block: u64,
    pending_transactions: Option<TransactionsGuard>,
    block_status_subscribers: Arc<Mutex<BTreeMap<BlockRef, Vec<oneshot::Sender<BlockStatus>>>>>,
    /// See Context::oldest_pending_tx_at_ms's doc comment. Kept here (rather than reaching
    /// through some other handle) so `next()` can clear it in the same place it already
    /// determines "queue is now fully empty".
    context: Arc<Context>,
}

#[derive(Debug, Clone, Eq, PartialEq)]
#[allow(unused)]
pub enum BlockStatus {
    /// The block has been sequenced as part of a committed sub dag. That means that any transaction that has been included in the block
    /// has been committed as well.
    Sequenced(BlockRef),
    /// The block has been garbage collected and will never be committed. Any transactions that have been included in the block should also
    /// be considered as impossible to be committed as part of this block and might need to be retried
    GarbageCollected(BlockRef),
}

#[derive(Debug, Clone, Eq, PartialEq)]
pub enum LimitReached {
    // The maximum number of transactions have been included
    MaxNumOfTransactions,
    // The maximum number of bytes have been included
    MaxBytes,
    // All available transactions have been included
    AllTransactionsIncluded,
}

pub(crate) type NextTransactions = (Vec<Transaction>, Box<dyn FnOnce(BlockRef)>, LimitReached);

impl TransactionConsumer {
    pub(crate) fn new(tx_receiver: Receiver<TransactionsGuard>, context: Arc<Context>) -> Self {
        // max_num_transactions_in_block - 1 is the max possible transaction index in a block.
        // TransactionIndex::MAX is reserved for the ping transaction.
        // Indexes down to TransactionIndex::MAX - 8 are also reserved for future use.
        // This check makes sure they do not overlap.
        assert!(
            context
                .protocol_config
                .max_num_transactions_in_block()
                .saturating_sub(1)
                < TransactionIndex::MAX.saturating_sub(NUM_RESERVED_TRANSACTION_INDICES) as u64,
            "Unsupported max_num_transactions_in_block: {}",
            context.protocol_config.max_num_transactions_in_block()
        );

        let max_transactions_in_block_bytes =
            context.protocol_config.max_transactions_in_block_bytes();
        let max_num_transactions_in_block = context.protocol_config.max_num_transactions_in_block();
        tracing::info!(
            "TransactionConsumer initialized with max_num_transactions_in_block: {}, max_transactions_in_block_bytes: {}",
            max_num_transactions_in_block,
            max_transactions_in_block_bytes
        );

        Self {
            tx_receiver,
            max_transactions_in_block_bytes,
            max_num_transactions_in_block,
            pending_transactions: None,
            block_status_subscribers: Arc::new(Mutex::new(BTreeMap::new())),
            context,
        }
    }

    // Checks if there are enough pending transactions to skip the aggregation delay
    // (MIN_PROPOSAL_AGGREGATION_DELAY in core.rs). Threshold is the block's own
    // max_num_transactions_in_block: once a full block's worth is already waiting,
    // there is no aggregation benefit left to wait for, so proposing immediately is
    // strictly better than idling out the rest of the floor.
    //
    // Previously hardcoded to 45_000 (and, before that per a stale comment, "25k") —
    // a value disconnected from the actual block capacity and, since
    // max_num_transactions_in_block defaults to far less than 45_000, one this could
    // never reach in practice. That silently defeated this bypass under any real
    // sustained load, forcing every proposal to eat the full aggregation floor
    // regardless of how much was already pending (measured: a rock-steady ~100-110ms
    // per block even with a large backlog and sub-block capacity of Go execution
    // capacity to spare).
    pub(crate) fn has_sufficient_transactions(&self) -> bool {
        let pending_len = self
            .pending_transactions
            .as_ref()
            .map(|g| g.transactions.len())
            .unwrap_or(0);
        pending_len as u64 >= self.max_num_transactions_in_block
    }

    // How long (ms) the oldest currently-unproposed transaction has been waiting, or 0 if the
    // queue is empty. See Context::oldest_pending_tx_at_ms's doc comment for the full mechanism
    // (stamped by TransactionClient::submit_no_wait, cleared here in next() once fully drained).
    // Used by proposer.rs to bound worst-case latency: a batch that's still young can keep
    // aggregating (good for throughput), but once the oldest entry has waited too long, propose
    // now regardless of how full the batch is.
    pub(crate) fn oldest_pending_wait_ms(&self) -> u64 {
        let stamped_at = self
            .context
            .oldest_pending_tx_at_ms
            .load(std::sync::atomic::Ordering::Relaxed);
        if stamped_at == 0 {
            return 0;
        }
        self.context
            .clock
            .timestamp_utc_ms()
            .saturating_sub(stamped_at)
    }

    // Attempts to fetch the next transactions that have been submitted for sequence. Respects the `max_transactions_in_block_bytes`
    // and `max_num_transactions_in_block` parameters specified via protocol config.
    // This returns one or more transactions to be included in the block and a callback to acknowledge the inclusion of those transactions.
    // Also returns a `LimitReached` enum to indicate which limit type has been reached.
    pub(crate) fn next(&mut self) -> NextTransactions {
        let mut transactions = Vec::new();
        let mut acks = Vec::new();
        let mut total_bytes = 0;
        let mut limit_reached = LimitReached::AllTransactionsIncluded;
        // FIX: Increase max_group_size from 2 to 500 (MAX_BUNDLE_SIZE) so that FFI batches are not dropped by TX-DROP-GUARD.
        let mut group_verifier = crate::tx_group_filter::IncrementalGroupVerifier::new(
            crate::tx_group_filter::MAX_TRANSACTION_GROUP_SIZE,
            self.max_num_transactions_in_block as usize,
        );

        // Handle one batch of incoming transactions from TransactionGuard.
        // The method will return `None` if all the transactions can be included in the block. Otherwise some or all of the transactions will be
        // excluded from the block and the method will return a new TransactionGuard with the remaining transactions.
        let mut handle_txs = |t: TransactionsGuard| -> Option<TransactionsGuard> {
            let transactions_num = t.transactions.len() as u64;
            if transactions_num == 0 {
                acks.push((t.included_in_block_ack, vec![PING_TRANSACTION_INDEX]));
                return None;
            }

            let mut accepted_txs = Vec::new();
            let mut remaining_txs = Vec::new();
            let mut local_total_bytes = 0;

            let mut iter = t.transactions.into_iter();
            while let Some(tx) = iter.next() {
                let tx_bytes = tx.data().len() as u64;

                if total_bytes + local_total_bytes + tx_bytes > self.max_transactions_in_block_bytes
                {
                    limit_reached = LimitReached::MaxBytes;
                    remaining_txs.push(tx);
                    remaining_txs.extend(iter);
                    break;
                }

                if transactions.len() as u64 + accepted_txs.len() as u64 + 1
                    > self.max_num_transactions_in_block
                {
                    limit_reached = LimitReached::MaxNumOfTransactions;
                    remaining_txs.push(tx);
                    remaining_txs.extend(iter);
                    break;
                }

                if !group_verifier.add_tx(&tx) {
                    limit_reached = LimitReached::MaxNumOfTransactions;
                    remaining_txs.push(tx);
                    continue; // Skip this tx but continue checking others in the batch
                }

                local_total_bytes += tx_bytes;
                accepted_txs.push(tx);
            }

            if accepted_txs.is_empty() {
                // Not a single transaction from this batch fits the limits!
                // Just return the whole thing back.
                return Some(TransactionsGuard {
                    transactions: remaining_txs,
                    included_in_block_ack: t.included_in_block_ack,
                });
            }

            total_bytes += local_total_bytes;

            // Calculate indices for this batch
            let start_idx = transactions.len() as TransactionIndex;
            let indices: Vec<TransactionIndex> =
                (start_idx..start_idx + accepted_txs.len() as TransactionIndex).collect();

            // The transactions can be consumed, register its ack and transaction
            // indices to be sent with the ack.
            acks.push((t.included_in_block_ack, indices));
            transactions.extend(accepted_txs);

            if !remaining_txs.is_empty() {
                // Some transactions were accepted, some were left over.
                // Create a dummy ack for the remaining ones.
                let (dummy_tx, _) = tokio::sync::oneshot::channel();
                return Some(TransactionsGuard {
                    transactions: remaining_txs,
                    included_in_block_ack: dummy_tx,
                });
            }

            None
        };

        if let Some(t) = self.pending_transactions.take() {
            let t_len = t.transactions.len();
            if let Some(pending_transactions) = handle_txs(t) {
                if pending_transactions.transactions.len() == t_len {
                    // ⚠️ TX-DROP-GUARD: This batch is too large to fit even in an EMPTY block.
                    // Root cause: Caller sent a single TransactionGuard where even the first transaction
                    // exceeds max_num_transactions_in_block OR max_transactions_in_block_bytes OR group_limit.
                    let drop_count = pending_transactions.transactions.len();
                    let drop_bytes: usize = pending_transactions
                        .transactions
                        .iter()
                        .map(|tx| tx.data().len())
                        .sum();
                    tracing::error!(
                        "🚫 [TX-DROP-GUARD] Dropping {} transactions ({} bytes) — transaction exceeds empty block limits \
                         (max_txs={}, max_bytes={}).",
                        drop_count,
                        drop_bytes,
                        self.max_num_transactions_in_block,
                        self.max_transactions_in_block_bytes
                    );
                    panic!(
                        "Previously pending transaction(s) should fit into an empty block! Dropping: {:?}",
                        pending_transactions.transactions
                    );
                } else {
                    self.pending_transactions = Some(pending_transactions);
                }
            }
        }

        // Until we have reached the limit for the pull.
        // We may have already reached limit in the first iteration above, in which case we stop immediately.
        let mut recv_count = 0;
        while self.pending_transactions.is_none() {
            if let Ok(t) = self.tx_receiver.try_recv() {
                tracing::debug!("🔥 [DEBUG] transaction_consumer.next() pulled a TransactionsGuard with {} txs!", t.transactions.len());
                recv_count += 1;
                self.pending_transactions = handle_txs(t);
            } else {
                break;
            }
        }
        drop(handle_txs);

        // LATENCY: the loop above only stops once pending_transactions is Some (a remainder
        // exists) or the channel is genuinely empty (the `else { break; }` case) -- so reaching
        // here with pending_transactions still None means nothing is left waiting anywhere.
        // Clear the shared timestamp so a future arrival stamps a fresh wait, not this drained
        // batch's already-served one. See Context::oldest_pending_tx_at_ms's doc comment.
        if self.pending_transactions.is_none() {
            self.context
                .oldest_pending_tx_at_ms
                .store(0, std::sync::atomic::Ordering::Relaxed);
        }

        if transactions.len() > 0 || recv_count > 0 {
            tracing::debug!("🔥 [DEBUG] transaction_consumer.next() returning {} txs. recv_count: {}, limit_reached: {:?}", transactions.len(), recv_count, limit_reached);
        }

        if !transactions.is_empty() {
            tracing::info!(
                "Proposing block with {} transactions ({} bytes). Limit reached: {:?}",
                transactions.len(),
                total_bytes,
                limit_reached
            );
        }

        let block_status_subscribers = self.block_status_subscribers.clone();
        (
            transactions,
            Box::new(move |block_ref: BlockRef| {
                let mut block_status_subscribers = block_status_subscribers.lock();

                for (ack, tx_indices) in acks {
                    let (status_tx, status_rx) = oneshot::channel();

                    block_status_subscribers
                        .entry(block_ref)
                        .or_default()
                        .push(status_tx);

                    let _ = ack.send((block_ref, tx_indices, status_rx));
                }
            }),
            limit_reached,
        )
    }

    /// Notifies all the transaction submitters who are waiting to receive an update on the status of the block.
    /// The `committed_blocks` are the blocks that have been committed and the `gc_round` is the round up to which the blocks have been garbage collected.
    /// First we'll notify for all the committed blocks, and then for all the blocks that have been garbage collected.
    pub(crate) fn notify_own_blocks_status(
        &self,
        committed_blocks: Vec<BlockRef>,
        gc_round: Round,
    ) {
        // Notify for all the committed blocks first
        let mut block_status_subscribers = self.block_status_subscribers.lock();
        for block_ref in committed_blocks {
            if let Some(subscribers) = block_status_subscribers.remove(&block_ref) {
                subscribers.into_iter().for_each(|s| {
                    let _ = s.send(BlockStatus::Sequenced(block_ref));
                });
            }
        }

        // Now notify everyone <= gc_round that their block has been garbage collected and clean up the entries
        while let Some((block_ref, subscribers)) = block_status_subscribers.pop_first() {
            if block_ref.round <= gc_round {
                subscribers.into_iter().for_each(|s| {
                    let _ = s.send(BlockStatus::GarbageCollected(block_ref));
                });
            } else {
                block_status_subscribers.insert(block_ref, subscribers);
                break;
            }
        }
    }

    #[cfg(test)]
    pub(crate) fn subscribe_for_block_status_testing(
        &self,
        block_ref: BlockRef,
    ) -> oneshot::Receiver<BlockStatus> {
        let (tx, rx) = oneshot::channel();
        let mut block_status_subscribers = self.block_status_subscribers.lock();
        block_status_subscribers
            .entry(block_ref)
            .or_default()
            .push(tx);
        rx
    }

    #[cfg(test)]
    fn is_empty(&mut self) -> bool {
        if self.pending_transactions.is_some() {
            return false;
        }
        if let Ok(t) = self.tx_receiver.try_recv() {
            self.pending_transactions = Some(t);
            return false;
        }
        true
    }
}

#[derive(Clone)]
pub struct TransactionClient {
    context: Arc<Context>,
    sender: Sender<TransactionsGuard>,
    max_transaction_size: u64,
    max_transactions_in_block_bytes: u64,
    max_transactions_in_block_count: u64,
}

#[derive(Debug, Error)]
pub enum ClientError {
    #[error("Failed to submit transaction, consensus is shutting down: {0}")]
    ConsensusShuttingDown(String),

    #[error("Transaction size ({0}B) is over limit ({1}B)")]
    OversizedTransaction(u64, u64),

    #[error("Transaction bundle size ({0}B) is over limit ({1}B)")]
    OversizedTransactionBundleBytes(u64, u64),

    #[error("Transaction bundle count ({0}) is over limit ({1})")]
    OversizedTransactionBundleCount(u64, u64),
}

impl TransactionClient {
    pub(crate) fn new(context: Arc<Context>) -> (Self, Receiver<TransactionsGuard>) {
        Self::new_with_max_pending_transactions(context, MAX_PENDING_TRANSACTIONS)
    }

    fn new_with_max_pending_transactions(
        context: Arc<Context>,
        max_pending_transactions: usize,
    ) -> (Self, Receiver<TransactionsGuard>) {
        let (sender, receiver) = channel(max_pending_transactions);
        (
            Self {
                sender,
                max_transaction_size: context.protocol_config.max_transaction_size_bytes(),

                max_transactions_in_block_bytes: context
                    .protocol_config
                    .max_transactions_in_block_bytes(),
                max_transactions_in_block_count: context
                    .protocol_config
                    .max_num_transactions_in_block(),
                context: context.clone(),
            },
            receiver,
        )
    }

    /// Returns the current epoch of this client.
    pub fn epoch(&self) -> Epoch {
        self.context.committee.epoch()
    }

    /// Submits a list of transactions to be sequenced. The method returns when all the transactions have been successfully included
    /// to next proposed blocks.
    ///
    /// If `transactions` is empty, then this will be interpreted as a "ping" signal from the client in order to get information about the next
    /// block and simulate a transaction inclusion to the next block. In this an empty vector of the transaction index will be returned as response
    /// and the block status receiver.
    pub async fn submit(
        &self,
        transactions: Vec<Vec<u8>>,
    ) -> Result<
        (
            BlockRef,
            Vec<TransactionIndex>,
            oneshot::Receiver<BlockStatus>,
        ),
        ClientError,
    > {
        let included_in_block = self.submit_no_wait(transactions).await?;
        included_in_block
            .await
            .tap_err(|e| warn!("Transaction acknowledge failed with {:?}", e))
            .map_err(|e| ClientError::ConsensusShuttingDown(e.to_string()))
    }

    /// Submits a list of transactions to be sequenced.
    /// If any transaction's length exceeds `max_transaction_size`, no transaction will be submitted.
    /// That shouldn't be the common case as sizes should be aligned between consensus and client. The method returns
    /// a receiver to wait on until the transactions has been included in the next block to get proposed. The consumer should
    /// wait on it to consider as inclusion acknowledgement. If the receiver errors then consensus is shutting down and transaction
    /// has not been included to any block.
    /// If multiple transactions are submitted, the method will attempt to bundle them together in a single block. If the total size of
    /// the transactions exceeds `max_transactions_in_block_bytes`, no transaction will be submitted and an error will be returned instead.
    /// Similar if transactions exceed `max_transactions_in_block_count` an error will be returned.
    pub async fn submit_no_wait(
        &self,
        transactions: Vec<Vec<u8>>,
    ) -> Result<
        oneshot::Receiver<(
            BlockRef,
            Vec<TransactionIndex>,
            oneshot::Receiver<BlockStatus>,
        )>,
        ClientError,
    > {
        let (included_in_block_ack_send, included_in_block_ack_receive) = oneshot::channel();

        let mut bundle_size = 0;

        if transactions.len() as u64 > self.max_transactions_in_block_count {
            return Err(ClientError::OversizedTransactionBundleCount(
                transactions.len() as u64,
                self.max_transactions_in_block_count,
            ));
        }

        for transaction in &transactions {
            if transaction.len() as u64 > self.max_transaction_size {
                return Err(ClientError::OversizedTransaction(
                    transaction.len() as u64,
                    self.max_transaction_size,
                ));
            }
            bundle_size += transaction.len() as u64;

            if bundle_size > self.max_transactions_in_block_bytes {
                return Err(ClientError::OversizedTransactionBundleBytes(
                    bundle_size,
                    self.max_transactions_in_block_bytes,
                ));
            }
        }

        let txs: Vec<Transaction> = transactions.into_iter().map(Transaction::new).collect();
        if let Some(mut cache) = try_tx_cache_write("TransactionClient::submit_no_wait") {
            // insert_batch: see its doc comment (mục 19 bug #6 throughput-regression
            // follow-up) -- avoids one lock acquisition per transaction in a bundle.
            cache.insert_batch(txs.iter().map(|tx| (tx.digest(), tx.clone())));
        }
        let t = TransactionsGuard {
            transactions: txs,
            included_in_block_ack: included_in_block_ack_send,
        };
        // LATENCY: stamp the arrival time of the oldest pending tx, but only if the queue was
        // previously empty (0) -- an already-nonzero value means something older is still
        // waiting, and that older arrival time is what should govern the aggregation-delay
        // bypass in proposer.rs, not this (later) submission. See Context::
        // oldest_pending_tx_at_ms's own doc comment for the full mechanism.
        let now_ms = self.context.clock.timestamp_utc_ms();
        let _ = self.context.oldest_pending_tx_at_ms.compare_exchange(
            0,
            now_ms,
            std::sync::atomic::Ordering::Relaxed,
            std::sync::atomic::Ordering::Relaxed,
        );

        self.sender
            .send(t)
            .await
            .tap_err(|e| error!("Submit transactions failed with {:?}", e))
            .map_err(|e| ClientError::ConsensusShuttingDown(e.to_string()))?;
        Ok(included_in_block_ack_receive)
    }
}

/// `TransactionVerifier` implementation is supplied by Sui to validate transactions in a block,
/// before acceptance of the block.
pub trait TransactionVerifier: Send + Sync + 'static {
    /// Determines if this batch of transactions is valid.
    /// Fails if any one of the transactions is invalid.
    fn verify_batch(&self, batch: &[&[u8]]) -> Result<(), ValidationError>;

    /// Returns indices of transactions to reject, or a transaction validation error.
    /// Currently only uncertified user transactions can be voted to reject, which are created
    /// by Mysticeti fastpath client.
    /// Honest validators may disagree on voting for uncertified user transactions.
    /// The other types of transactions are implicitly voted to be accepted if they pass validation.
    ///
    /// Honest validators should produce the same validation outcome on the same batch of
    /// transactions. So if a batch from a peer fails validation, the peer is equivocating.
    fn verify_and_vote_batch(
        &self,
        block_ref: &BlockRef,
        batch: &[&[u8]],
    ) -> Result<Vec<TransactionIndex>, ValidationError>;
}

#[derive(Debug, Error)]
pub enum ValidationError {
    #[error("Invalid transaction: {0}")]
    InvalidTransaction(String),
}

/// `NoopTransactionVerifier` accepts all transactions.
#[cfg(any(test, msim))]
pub struct NoopTransactionVerifier;

#[cfg(any(test, msim))]
impl TransactionVerifier for NoopTransactionVerifier {
    fn verify_batch(&self, _batch: &[&[u8]]) -> Result<(), ValidationError> {
        Ok(())
    }

    fn verify_and_vote_batch(
        &self,
        _block_ref: &BlockRef,
        _batch: &[&[u8]],
    ) -> Result<Vec<TransactionIndex>, ValidationError> {
        Ok(vec![])
    }
}

#[cfg(test)]
mod tests {
    use std::{sync::Arc, time::Duration};

    use consensus_config::AuthorityIndex;
    use consensus_types::block::{
        BlockDigest, BlockRef, TransactionIndex, NUM_RESERVED_TRANSACTION_INDICES,
        PING_TRANSACTION_INDEX,
    };
    use futures::{stream::FuturesUnordered, StreamExt};
    use meta_protocol_config::ProtocolConfig;
    use tokio::time::timeout;

    use crate::transaction::NoopTransactionVerifier;
    use crate::{
        block::Transaction,
        block_verifier::SignedBlockVerifier,
        context::Context,
        transaction::{
            shard_for, BlockStatus, LimitReached, TransactionClient, TransactionConsumer,
            TxCacheReadHandle, TxCacheWriteHandle, NUM_TX_CACHE_SHARDS, TX_CACHE_LOCK_TIMEOUT,
        },
    };
    use consensus_types::block::TxDigest;

    /// Regression test for the mục-19-bug-#4 fix: proves the actual mechanism
    /// `try_tx_cache_read`/`try_tx_cache_write` rely on -- parking_lot's own
    /// native `try_write_for`/`try_read_for` -- genuinely bounds a wait
    /// rather than blocking forever, even against a lock held by another OS
    /// thread with no cooperation from any async runtime. This is the
    /// opposite of `tokio::time::timeout` wrapping a blocking call (proven
    /// live to NOT work, see the doc comment on `TX_CACHE_LOCK_TIMEOUT`) --
    /// deliberately a plain `#[test]`, not `#[tokio::test]`, since the
    /// mechanism under test has nothing to do with any async runtime.
    #[test]
    fn bounded_lock_acquisition_times_out_against_a_real_stuck_holder() {
        use std::thread;

        let lock: Arc<parking_lot::RwLock<i32>> = Arc::new(parking_lot::RwLock::new(0));
        let holder_lock = lock.clone();
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();

        let holder = thread::spawn(move || {
            let _guard = holder_lock.write();
            // Hold the write lock until the test explicitly says to let go --
            // simulating exactly the "someone never releases it" scenario
            // this fix is meant to survive.
            let _ = release_rx.recv();
        });

        // Give the holder thread a moment to actually acquire the lock first.
        thread::sleep(Duration::from_millis(50));

        // While held: both a write and a read attempt must time out quickly,
        // not hang -- this is the core guarantee the whole fix depends on.
        let write_attempt = lock.try_write_for(Duration::from_millis(100));
        assert!(
            write_attempt.is_none(),
            "try_write_for must time out (return None) against a genuinely held lock, \
             not block forever"
        );
        let read_attempt = lock.try_read_for(Duration::from_millis(100));
        assert!(
            read_attempt.is_none(),
            "try_read_for must time out (return None) against a genuinely held write lock, \
             not block forever"
        );

        // Release the holder and confirm acquisition succeeds normally afterward --
        // proves the bounded path isn't just always returning None.
        release_tx.send(()).unwrap();
        holder.join().unwrap();
        assert!(
            lock.try_write_for(Duration::from_secs(1)).is_some(),
            "after the holder releases, a bounded acquisition must succeed"
        );
    }

    /// Regression test for the mục-19-bug-#5 self-deadlock: proves that a
    /// SINGLE thread holding a read guard on a `parking_lot::RwLock`, then
    /// trying to acquire a SECOND read guard on the exact same lock while a
    /// writer is queued in between, genuinely gets stuck (parking_lot is
    /// fair -- a queued writer blocks new readers, including reentrant ones
    /// from a thread that already holds an outstanding read guard, to avoid
    /// writer starvation). This is exactly the class of bug found live via
    /// `sudo gdb` in `compute_commit_gei_and_valid_txs`, which used to hold
    /// its own `GLOBAL_TX_CACHE` read guard across a call to
    /// `extract_end_of_epoch_transaction()` (a second, independent read
    /// acquisition on the same lock) -- fixed by reordering so the two
    /// acquisitions never overlap on one thread. This test locks in *why*
    /// that reordering is necessary, not just that the specific function
    /// happens to be fixed today.
    #[test]
    fn reentrant_read_with_a_queued_writer_blocks_the_same_thread() {
        use std::thread;

        let lock: Arc<parking_lot::RwLock<i32>> = Arc::new(parking_lot::RwLock::new(0));

        // Step 1: this thread (simulating compute_commit_gei_and_valid_txs)
        // takes the first read guard, like the old `let cache = ...read();`.
        let guard1 = lock.read();

        // Step 2: a writer (simulating a concurrent block_verifier V1/V2
        // insert) queues behind it on another thread.
        let writer_lock = lock.clone();
        let (writer_started_tx, writer_started_rx) = std::sync::mpsc::channel::<()>();
        let writer = thread::spawn(move || {
            writer_started_tx.send(()).unwrap();
            let _guard = writer_lock.write();
        });
        writer_started_rx.recv().unwrap();
        // Give the writer a moment to actually queue on the lock.
        thread::sleep(Duration::from_millis(100));

        // Step 3: THIS SAME thread, still holding guard1, tries a second
        // (reentrant) read acquisition -- exactly what the old, buggy code
        // did via the nested extract_end_of_epoch_transaction() call. With a
        // writer already queued, parking_lot's fairness means this MUST NOT
        // succeed immediately.
        let reentrant_attempt = lock.try_read_for(Duration::from_millis(200));
        assert!(
            reentrant_attempt.is_none(),
            "a reentrant read on a thread that already holds a read guard must be blocked \
             once a writer has queued -- if this ever starts succeeding, parking_lot's \
             fairness policy changed and the ordering fix in compute_commit_gei_and_valid_txs \
             may no longer be load-bearing (though it would still be correct to keep it)"
        );

        // Cleanup: release guard1, let the writer through, join it.
        drop(guard1);
        writer.join().unwrap();
    }

    /// Regression test for the mục-19-bug-#6 architectural fix: proves sharding actually
    /// isolates contention across digests, not just bounds it. Live-reproduced: under a
    /// large catch-up, dozens of concurrent `verify_block_inner` writers all serialized on
    /// ONE global lock even though each individual acquisition was bounded to 3s -- the
    /// node still made zero forward progress for the whole window. This test proves that
    /// class of stall cannot happen anymore for two DIFFERENT digests: holding one
    /// digest's shard lock indefinitely must never block a write to a digest that hashes
    /// to a different shard, while it correctly DOES still block (and time out) a write to
    /// a digest that hashes to the SAME shard -- i.e. this is testing real per-shard
    /// isolation, not accidentally testing "everything always succeeds".
    #[test]
    fn sharded_cache_isolates_contention_across_different_shards() {
        use std::thread;

        // Find two digests that provably hash to different shards, and a third that
        // hashes to the same shard as the first -- shard_for() only depends on
        // digest.0[0] (`byte % NUM_TX_CACHE_SHARDS`), so this is trivial and
        // deterministic: byte 0 and byte 1 land in different shards, and byte
        // `NUM_TX_CACHE_SHARDS` wraps back to the same shard as byte 0.
        let digest_a = TxDigest([0u8; consensus_config::DIGEST_LENGTH]);
        let digest_b = TxDigest([1u8; consensus_config::DIGEST_LENGTH]);
        assert!(
            !std::ptr::eq(shard_for(&digest_a), shard_for(&digest_b)),
            "test setup assumption broken: digest_a and digest_b must map to different shards"
        );
        let mut digest_a2_bytes = [0u8; consensus_config::DIGEST_LENGTH];
        digest_a2_bytes[0] = NUM_TX_CACHE_SHARDS as u8;
        let digest_a2 = TxDigest(digest_a2_bytes);
        assert!(
            std::ptr::eq(shard_for(&digest_a), shard_for(&digest_a2)),
            "test setup assumption broken: digest_a and digest_a2 must map to the SAME shard \
             (digest_a2's leading byte is exactly NUM_TX_CACHE_SHARDS ahead of digest_a's)"
        );

        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        let holder = thread::spawn(move || {
            let _guard = shard_for(&digest_a).lock.write();
            // Hold digest_a's shard write lock until told to let go.
            let _ = release_rx.recv();
        });
        thread::sleep(Duration::from_millis(50));

        // A write to a DIFFERENT shard (digest_b) must succeed immediately, completely
        // unaffected by digest_a's shard being held -- this is the whole point of
        // sharding: it must not just be "bounded", it must not contend at all.
        let start = std::time::Instant::now();
        let mut write_handle = TxCacheWriteHandle { site: "test" };
        write_handle.insert(digest_b, Transaction::new(b"b".to_vec()));
        assert!(
            start.elapsed() < Duration::from_millis(500),
            "a write to a different shard took {:?} -- sharding is not actually isolating \
             contention (it should return in microseconds, not wait on digest_a's shard at all)",
            start.elapsed()
        );
        let read_handle = TxCacheReadHandle { site: "test" };
        assert!(
            read_handle.get(&digest_b).is_some(),
            "the write to the other shard must have actually landed"
        );

        // A write to the SAME shard (digest_a2) must still correctly block and time out --
        // proving this isn't a broken test that would pass even with no locking at all.
        let start = std::time::Instant::now();
        let mut same_shard_handle = TxCacheWriteHandle { site: "test" };
        same_shard_handle.insert(digest_a2, Transaction::new(b"a2".to_vec()));
        assert!(
            start.elapsed() >= TX_CACHE_LOCK_TIMEOUT,
            "a write to the SAME shard as a held lock returned in {:?}, faster than the {:?} \
             bound -- it should have genuinely waited/timed out, not skipped contention",
            start.elapsed(),
            TX_CACHE_LOCK_TIMEOUT
        );
        assert!(
            read_handle.get(&digest_a2).is_none(),
            "the same-shard write should have been skipped (timed out), not landed"
        );

        release_tx.send(()).unwrap();
        holder.join().unwrap();
        // After release, the same-shard digest can now be inserted normally.
        let mut retry_handle = TxCacheWriteHandle { site: "test" };
        retry_handle.insert(digest_a2, Transaction::new(b"a2-retry".to_vec()));
        assert!(read_handle.get(&digest_a2).is_some());
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    async fn basic_submit_and_consume() {
        let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
            config.set_consensus_max_transaction_size_bytes_for_testing(2_000); // 2KB
            config.set_consensus_max_transactions_in_block_bytes_for_testing(2_000);
            config
        });

        let context = Arc::new(Context::new_for_test(4).0);
        let (client, tx_receiver) = TransactionClient::new(context.clone());
        let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());

        // submit asynchronously the transactions and keep the waiters
        let mut included_in_block_waiters = FuturesUnordered::new();
        for i in 0..3 {
            let transaction =
                bcs::to_bytes(&format!("transaction {i}")).expect("Serialization should not fail.");
            let w = client
                .submit_no_wait(vec![transaction])
                .await
                .expect("Shouldn't submit successfully transaction");
            included_in_block_waiters.push(w);
        }

        // now pull the transactions from the consumer
        let (transactions, ack_transactions, _limit_reached) = consumer.next();
        assert_eq!(transactions.len(), 3);

        for (i, t) in transactions.iter().enumerate() {
            let t: String = bcs::from_bytes(t.data()).unwrap();
            assert_eq!(format!("transaction {i}").to_string(), t);
        }

        assert!(
            timeout(Duration::from_secs(1), included_in_block_waiters.next())
                .await
                .is_err(),
            "We should expect to timeout as none of the transactions have been acknowledged yet"
        );

        // Now acknowledge the inclusion of transactions
        ack_transactions(BlockRef::MIN);

        // Now make sure that all the waiters have returned
        while let Some(result) = included_in_block_waiters.next().await {
            assert!(result.is_ok());
        }

        // try to pull again transactions, result should be empty
        assert!(consumer.is_empty());
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    async fn block_status_update() {
        let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
            config.set_consensus_max_transaction_size_bytes_for_testing(2_000); // 2KB
            config.set_consensus_max_transactions_in_block_bytes_for_testing(2_000);
            config.set_consensus_gc_depth_for_testing(10);
            config
        });

        let context = Arc::new(Context::new_for_test(4).0);
        let (client, tx_receiver) = TransactionClient::new(context.clone());
        let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());

        // submit the transactions and include 2 of each on a new block
        let mut included_in_block_waiters = FuturesUnordered::new();
        for i in 1..=10 {
            let transaction =
                bcs::to_bytes(&format!("transaction {i}")).expect("Serialization should not fail.");
            let w = client
                .submit_no_wait(vec![transaction])
                .await
                .expect("Shouldn't submit successfully transaction");
            included_in_block_waiters.push(w);

            // Every 2 transactions simulate the creation of a new block and acknowledge the inclusion of the transactions
            if i % 2 == 0 {
                let (transactions, ack_transactions, _limit_reached) = consumer.next();
                assert_eq!(transactions.len(), 2);
                ack_transactions(BlockRef::new(
                    i,
                    AuthorityIndex::new_for_test(0),
                    BlockDigest::MIN,
                ));
            }
        }

        let mut transaction_count = 0;
        // Now iterate over all the waiters. Everyone should have been acknowledged.
        let mut block_status_waiters = Vec::new();
        while let Some(result) = included_in_block_waiters.next().await {
            let (block_ref, tx_indices, block_status_waiter) =
                result.expect("Block inclusion waiter shouldn't fail");
            // tx is submitted one at a time so tx acks should only return one tx index
            assert_eq!(tx_indices.len(), 1);
            // The first transaction in the block should have index 0, the second one 1, etc.
            // because we submit 2 transactions per block, the index should be 0 then 1 and then
            // reset back to 0 for the next block.
            assert_eq!(tx_indices[0], transaction_count % 2);
            transaction_count += 1;

            block_status_waiters.push((block_ref, block_status_waiter));
        }

        // Now acknowledge the commit of the blocks 6, 8, 10 and set gc_round = 5, which should trigger the garbage collection of blocks 1..=5
        let gc_round = 5;
        consumer.notify_own_blocks_status(
            vec![
                BlockRef::new(6, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
                BlockRef::new(8, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
                BlockRef::new(10, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
            ],
            gc_round,
        );

        // Now iterate over all the block status waiters. Everyone should have been notified.
        for (block_ref, waiter) in block_status_waiters {
            let block_status = waiter.await.expect("Block status waiter shouldn't fail");

            if block_ref.round <= gc_round {
                assert!(matches!(block_status, BlockStatus::GarbageCollected(_)))
            } else {
                assert!(matches!(block_status, BlockStatus::Sequenced(_)));
            }
        }

        // Ensure internal structure is clear
        assert!(consumer.block_status_subscribers.lock().is_empty());
    }

    #[tokio::test]
    async fn submit_over_max_fetch_size_and_consume() {
        let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
            config.set_consensus_max_transaction_size_bytes_for_testing(100);
            config.set_consensus_max_transactions_in_block_bytes_for_testing(100);
            config
        });

        let context = Arc::new(Context::new_for_test(4).0);
        let (client, tx_receiver) = TransactionClient::new(context.clone());
        let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());

        // submit some transactions
        for i in 0..10 {
            let transaction =
                bcs::to_bytes(&format!("transaction {i}")).expect("Serialization should not fail.");
            let _w = client
                .submit_no_wait(vec![transaction])
                .await
                .expect("Shouldn't submit successfully transaction");
        }

        // now pull the transactions from the consumer
        let mut all_transactions = Vec::new();
        let (transactions, _ack_transactions, _limit_reached) = consumer.next();
        assert_eq!(transactions.len(), 7);

        // ensure their total size is less than `max_bytes_to_fetch`
        let total_size: u64 = transactions.iter().map(|t| t.data().len() as u64).sum();
        assert!(
            total_size <= context.protocol_config.max_transactions_in_block_bytes(),
            "Should have fetched transactions up to {}",
            context.protocol_config.max_transactions_in_block_bytes()
        );
        all_transactions.extend(transactions);

        // try to pull again transactions, next should be provided
        let (transactions, _ack_transactions, _limit_reached) = consumer.next();
        assert_eq!(transactions.len(), 3);

        // ensure their total size is less than `max_bytes_to_fetch`
        let total_size: u64 = transactions.iter().map(|t| t.data().len() as u64).sum();
        assert!(
            total_size <= context.protocol_config.max_transactions_in_block_bytes(),
            "Should have fetched transactions up to {}",
            context.protocol_config.max_transactions_in_block_bytes()
        );
        all_transactions.extend(transactions);

        // try to pull again transactions, result should be empty
        assert!(consumer.is_empty());

        for (i, t) in all_transactions.iter().enumerate() {
            let t: String = bcs::from_bytes(t.data()).unwrap();
            assert_eq!(format!("transaction {i}").to_string(), t);
        }
    }

    #[tokio::test]
    async fn submit_large_batch_and_ack() {
        let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
            config.set_consensus_max_transaction_size_bytes_for_testing(15);
            config.set_consensus_max_transactions_in_block_bytes_for_testing(200);
            config
        });

        let context = Arc::new(Context::new_for_test(4).0);
        let (client, tx_receiver) = TransactionClient::new(context.clone());
        let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());
        let mut all_receivers = Vec::new();
        // submit a few transactions individually.
        for i in 0..10 {
            let transaction =
                bcs::to_bytes(&format!("transaction {i}")).expect("Serialization should not fail.");
            let w = client
                .submit_no_wait(vec![transaction])
                .await
                .expect("Should submit successfully transaction");
            all_receivers.push(w);
        }

        // construct an acceptable batch and submit, it should be accepted
        {
            let transactions: Vec<_> = (10..15)
                .map(|i| {
                    bcs::to_bytes(&format!("transaction {i}"))
                        .expect("Serialization should not fail.")
                })
                .collect();
            let w = client
                .submit_no_wait(transactions)
                .await
                .expect("Should submit successfully transaction");
            all_receivers.push(w);
        }

        // submit another individual transaction.
        {
            let i = 15;
            let transaction =
                bcs::to_bytes(&format!("transaction {i}")).expect("Serialization should not fail.");
            let w = client
                .submit_no_wait(vec![transaction])
                .await
                .expect("Shouldn't submit successfully transaction");
            all_receivers.push(w);
        }

        // construct a over-size-limit batch and submit, it should not be accepted
        {
            let transactions: Vec<_> = (16..32)
                .map(|i| {
                    bcs::to_bytes(&format!("transaction {i}"))
                        .expect("Serialization should not fail.")
                })
                .collect();
            let result = client.submit_no_wait(transactions).await.unwrap_err();
            assert_eq!(
                result.to_string(),
                "Transaction bundle size (210B) is over limit (200B)"
            );
        }

        // now pull the transactions from the consumer.
        // we expect all transactions are fetched in order, not missing any, and not exceeding the size limit.
        let mut all_acks: Vec<Box<dyn FnOnce(BlockRef)>> = Vec::new();
        let mut batch_index = 0;
        while !consumer.is_empty() {
            let (transactions, ack_transactions, _limit_reached) = consumer.next();

            assert!(
                transactions.len() as u64
                    <= context.protocol_config.max_num_transactions_in_block(),
                "Should have fetched transactions up to {}",
                context.protocol_config.max_num_transactions_in_block()
            );

            let total_size: u64 = transactions.iter().map(|t| t.data().len() as u64).sum();
            assert!(
                total_size <= context.protocol_config.max_transactions_in_block_bytes(),
                "Should have fetched transactions up to {}",
                context.protocol_config.max_transactions_in_block_bytes()
            );

            // `next()`'s packing (see handle_txs() above) is greedy per-transaction, not
            // atomic per-submit_no_wait()-call: a multi-tx submission can be split across
            // batches, with as many of its transactions as fit going into the current one
            // and the rest carried over as `pending_transactions`. So the 5-tx bundle
            // (10..15) isn't parked as a whole once it doesn't fully fit after 0..10 —
            // transactions 10-13 (4 more, 56 bytes) still fit under the 200-byte limit
            // (140 + 56 = 196), only transaction 14 doesn't and gets carried over.
            if batch_index == 0 {
                assert_eq!(transactions.len(), 14);
                for (i, transaction) in transactions.iter().enumerate() {
                    let t: String = bcs::from_bytes(transaction.data()).unwrap();
                    assert_eq!(format!("transaction {}", i).to_string(), t);
                }
            // second batch contains the carried-over end of the bundle (transaction 14)
            // and the additional individually-submitted transaction 15.
            } else if batch_index == 1 {
                assert_eq!(transactions.len(), 2);
                for (i, transaction) in transactions.iter().enumerate() {
                    let t: String = bcs::from_bytes(transaction.data()).unwrap();
                    assert_eq!(format!("transaction {}", i + 14).to_string(), t);
                }
            } else {
                panic!("Unexpected batch index");
            }

            batch_index += 1;

            all_acks.push(ack_transactions);
        }

        // now acknowledge the inclusion of all transactions.
        for ack in all_acks {
            ack(BlockRef::MIN);
        }

        // expect all receivers to be resolved.
        for w in all_receivers {
            let r = w.await;
            assert!(r.is_ok());
        }
    }

    #[tokio::test]
    async fn test_submit_over_max_block_size_and_validate_block_size() {
        // submit transactions individually so we make sure that we have reached the block size limit of 10
        {
            let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
                config.set_consensus_max_transaction_size_bytes_for_testing(100);
                config.set_consensus_max_num_transactions_in_block_for_testing(10);
                config.set_consensus_max_transactions_in_block_bytes_for_testing(300);
                config
            });

            let context = Arc::new(Context::new_for_test(4).0);
            let (client, tx_receiver) = TransactionClient::new(context.clone());
            let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());
            let mut all_receivers = Vec::new();

            // create enough transactions
            let max_num_transactions_in_block =
                context.protocol_config.max_num_transactions_in_block();
            for i in 0..2 * max_num_transactions_in_block {
                let transaction = bcs::to_bytes(&format!("transaction {i}"))
                    .expect("Serialization should not fail.");
                let w = client
                    .submit_no_wait(vec![transaction])
                    .await
                    .expect("Should submit successfully transaction");
                all_receivers.push(w);
            }

            // Fetch the next transactions to be included in a block
            let (transactions, _ack_transactions, limit) = consumer.next();
            assert_eq!(limit, LimitReached::MaxNumOfTransactions);
            assert_eq!(transactions.len() as u64, max_num_transactions_in_block);

            // Now create a block and verify that transactions are within the size limits
            let block_verifier =
                SignedBlockVerifier::new(context.clone(), Arc::new(NoopTransactionVerifier {}));

            let batch: Vec<_> = transactions.iter().map(|t| t.data()).collect();
            assert!(
                block_verifier.check_transactions(&batch).is_ok(),
                "Number of transactions limit verification failed"
            );
        }

        // submit transactions individually so we make sure that we have reached the block size bytes 300
        {
            let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
                config.set_consensus_max_transaction_size_bytes_for_testing(100);
                config.set_consensus_max_num_transactions_in_block_for_testing(1_000);
                config.set_consensus_max_transactions_in_block_bytes_for_testing(300);
                config
            });

            let context = Arc::new(Context::new_for_test(4).0);
            let (client, tx_receiver) = TransactionClient::new(context.clone());
            let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());
            let mut all_receivers = Vec::new();

            let max_transactions_in_block_bytes =
                context.protocol_config.max_transactions_in_block_bytes();
            let mut total_size = 0;
            loop {
                let transaction = bcs::to_bytes(&"transaction".to_string())
                    .expect("Serialization should not fail.");
                total_size += transaction.len() as u64;
                let w = client
                    .submit_no_wait(vec![transaction])
                    .await
                    .expect("Should submit successfully transaction");
                all_receivers.push(w);

                // create enough transactions to reach the block size limit
                if total_size >= 2 * max_transactions_in_block_bytes {
                    break;
                }
            }

            // Fetch the next transactions to be included in a block
            let (transactions, _ack_transactions, limit) = consumer.next();
            let batch: Vec<_> = transactions.iter().map(|t| t.data()).collect();
            let size = batch.iter().map(|t| t.len() as u64).sum::<u64>();

            assert_eq!(limit, LimitReached::MaxBytes);
            assert!(
                batch.len()
                    < context
                        .protocol_config
                        .consensus_max_num_transactions_in_block() as usize,
                "Should have submitted less than the max number of transactions in a block"
            );
            assert!(size <= max_transactions_in_block_bytes);

            // Now create a block and verify that transactions are within the size limits
            let block_verifier =
                SignedBlockVerifier::new(context.clone(), Arc::new(NoopTransactionVerifier {}));

            assert!(
                block_verifier.check_transactions(&batch).is_ok(),
                "Total size of transactions limit verification failed"
            );
        }
    }

    // This is the case where the client submits a "ping" signal to the consensus to get information about the next block and simulate a transaction inclusion to the next block.
    #[tokio::test]
    async fn submit_with_no_transactions() {
        let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
            config.set_consensus_max_transaction_size_bytes_for_testing(15);
            config.set_consensus_max_transactions_in_block_bytes_for_testing(200);
            config
        });

        let context = Arc::new(Context::new_for_test(4).0);
        let (client, tx_receiver) = TransactionClient::new(context.clone());
        let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());

        let w_no_transactions = client
            .submit_no_wait(vec![])
            .await
            .expect("Should submit successfully empty array of transactions");

        let transaction =
            bcs::to_bytes(&"transaction".to_string()).expect("Serialization should not fail.");
        let w_with_transactions = client
            .submit_no_wait(vec![transaction])
            .await
            .expect("Should submit successfully transaction");

        let (transactions, ack_transactions, _limit_reached) = consumer.next();
        assert_eq!(transactions.len(), 1);

        // Acknowledge the inclusion of the transactions
        ack_transactions(BlockRef::MIN);

        {
            let r = w_no_transactions.await;
            let (block_ref, indices, _status) = r.unwrap();
            assert_eq!(block_ref, BlockRef::MIN);
            assert_eq!(indices, vec![PING_TRANSACTION_INDEX]);
        }

        {
            let r = w_with_transactions.await;
            let (block_ref, indices, _status) = r.unwrap();
            assert_eq!(block_ref, BlockRef::MIN);
            assert_eq!(indices, vec![0]);
        }
    }

    #[tokio::test]
    async fn ping_transaction_index_never_reached() {
        // Set the max number of transactions in a block to the max value of u16.
        static MAX_NUM_TRANSACTIONS_IN_BLOCK: u64 =
            (TransactionIndex::MAX - NUM_RESERVED_TRANSACTION_INDICES) as u64;

        // Ensure that enough space is allocated in the channel for the pending transactions, so we don't end up consuming the transactions in chunks.
        static MAX_PENDING_TRANSACTIONS: usize = 2 * MAX_NUM_TRANSACTIONS_IN_BLOCK as usize;

        let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
            config.set_consensus_max_transaction_size_bytes_for_testing(200_000);
            config.set_consensus_max_transactions_in_block_bytes_for_testing(1_000_000);
            config.set_consensus_max_num_transactions_in_block_for_testing(
                MAX_NUM_TRANSACTIONS_IN_BLOCK,
            );
            config
        });

        let context = Arc::new(Context::new_for_test(4).0);
        let (client, tx_receiver) = TransactionClient::new_with_max_pending_transactions(
            context.clone(),
            MAX_PENDING_TRANSACTIONS,
        );
        let mut consumer = TransactionConsumer::new(tx_receiver, context.clone());

        // Add 10 more transactions than the max number of transactions in a block.
        for i in 0..MAX_NUM_TRANSACTIONS_IN_BLOCK + 10 {
            println!("Submitting transaction {i}");
            let transaction =
                bcs::to_bytes(&format!("t {i}")).expect("Serialization should not fail.");
            let _w = client
                .submit_no_wait(vec![transaction])
                .await
                .expect("Shouldn't submit successfully transaction");
        }

        // now pull the transactions from the consumer
        let (transactions, _ack_transactions, _limit_reached) = consumer.next();
        assert_eq!(transactions.len() as u64, MAX_NUM_TRANSACTIONS_IN_BLOCK);

        let t: String = bcs::from_bytes(transactions.last().unwrap().data()).unwrap();
        assert_eq!(
            t,
            format!(
                "t {}",
                PING_TRANSACTION_INDEX - NUM_RESERVED_TRANSACTION_INDICES - 1
            )
        );
    }
}
