// Copyright (c) Metanode
// SPDX-License-Identifier: Apache-2.0

//! Durable append-only log of transaction payloads (Layer 1 of the payload-loss fix).
//!
//! Blocks are compact (digests only) and `TxPayloadCache` is memory-only, so a
//! full-cluster restart loses every payload that was proposed but not yet executed
//! (`CONSENSUS-HALT-TX-PAYLOAD-LOST`). This log persists each newly cached payload
//! with ONE sequential append per batch (never one file per tx), written by a single
//! dedicated thread fed through a bounded channel. The hot path only does a
//! non-blocking `try_send`: if the writer falls behind the item is dropped and counted
//! (durability is best-effort; the halt-not-guess safety net is unchanged).
//!
//! On startup all segments are replayed into the cache. Every record is re-verified
//! (`digest == hash(payload)`), so a torn or corrupted tail can never inject a wrong
//! payload; replay of a segment stops at its first bad record.
//!
//! Record: `[u32 LE payload_len][32B digest][payload]`.

use std::{
    fs::{self, File, OpenOptions},
    io::{self, BufWriter, Read, Write},
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicU64, Ordering},
        mpsc::{sync_channel, RecvTimeoutError, SyncSender, TryRecvError},
        OnceLock,
    },
    time::{Duration, Instant},
};

use consensus_types::block::TxDigest;
use tracing::{info, warn};

use crate::block::Transaction;

const QUEUE_BOUND: usize = 262_144;
const SEGMENT_BYTES: u64 = 128 * 1024 * 1024;
const DEFAULT_MAX_TOTAL_MB: u64 = 4096;
const MAX_PAYLOAD_LEN: usize = 16 * 1024 * 1024;
const MAX_DRAIN_PER_ROUND: usize = 4096;
const FSYNC_INTERVAL: Duration = Duration::from_millis(200);

struct PayloadWal {
    tx: SyncSender<(TxDigest, Transaction)>,
    dropped: AtomicU64,
}

static WAL: OnceLock<PayloadWal> = OnceLock::new();

/// Enqueue a newly cached payload for persistence. Never blocks.
pub(crate) fn enqueue(digest: TxDigest, tx: Transaction) {
    if let Some(wal) = WAL.get() {
        if wal.tx.try_send((digest, tx)).is_err() {
            let n = wal.dropped.fetch_add(1, Ordering::Relaxed) + 1;
            if n.is_power_of_two() {
                warn!(
                    "[PAYLOAD-WAL] writer queue full or closed; {} payload(s) not persisted so far",
                    n
                );
            }
        }
    }
}

pub(crate) fn is_enabled() -> bool {
    WAL.get().is_some()
}

fn segment_path(dir: &Path, id: u64) -> PathBuf {
    dir.join(format!("seg-{:016}.wal", id))
}

fn list_segments(dir: &Path) -> io::Result<Vec<(u64, PathBuf, u64)>> {
    let mut v = Vec::new();
    for e in fs::read_dir(dir)? {
        let e = e?;
        let name = e.file_name().to_string_lossy().into_owned();
        if let Some(id) = name
            .strip_prefix("seg-")
            .and_then(|s| s.strip_suffix(".wal"))
            .and_then(|s| s.parse::<u64>().ok())
        {
            v.push((id, e.path(), e.metadata()?.len()));
        }
    }
    v.sort_by_key(|(id, _, _)| *id);
    Ok(v)
}

/// Replays one segment, calling `f` for each valid record. Returns the number of
/// records accepted. Stops at the first torn/corrupt record.
fn replay_segment(path: &Path, f: &mut impl FnMut(TxDigest, Transaction)) -> io::Result<u64> {
    let mut r = io::BufReader::with_capacity(1 << 20, File::open(path)?);
    let mut n = 0u64;
    loop {
        let mut len_buf = [0u8; 4];
        match r.read_exact(&mut len_buf) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => break,
            Err(e) => return Err(e),
        }
        let len = u32::from_le_bytes(len_buf) as usize;
        if len > MAX_PAYLOAD_LEN {
            warn!("[PAYLOAD-WAL] {:?}: absurd record length {}, stopping segment", path, len);
            break;
        }
        let mut digest = [0u8; 32];
        let mut payload = vec![0u8; len];
        if r.read_exact(&mut digest).is_err() || r.read_exact(&mut payload).is_err() {
            break; // torn tail
        }
        let tx = Transaction::new(payload);
        if tx.digest().0 != digest {
            warn!("[PAYLOAD-WAL] {:?}: digest mismatch, stopping segment", path);
            break;
        }
        f(TxDigest(digest), tx);
        n += 1;
    }
    Ok(n)
}

fn prune(dir: &Path, max_total: u64, keep_id: u64) {
    if let Ok(segs) = list_segments(dir) {
        let mut total: u64 = segs.iter().map(|s| s.2).sum();
        for (id, path, len) in segs {
            if total <= max_total || id >= keep_id {
                break;
            }
            if fs::remove_file(&path).is_ok() {
                total -= len;
            }
        }
    }
}

/// Opens (creating if needed) the payload log in `dir`, replays existing segments
/// through `load` (oldest first so the cache's FIFO keeps the newest), then starts the
/// writer thread. Idempotent: later calls are no-ops. Disabled by `METANODE_PAYLOAD_WAL=0`.
pub(crate) fn init(dir: &Path, mut load: impl FnMut(TxDigest, Transaction)) -> io::Result<()> {
    if std::env::var("METANODE_PAYLOAD_WAL").map_or(false, |v| v == "0") {
        info!("[PAYLOAD-WAL] disabled by METANODE_PAYLOAD_WAL=0");
        return Ok(());
    }
    if WAL.get().is_some() {
        return Ok(());
    }
    fs::create_dir_all(dir)?;
    let max_total = std::env::var("METANODE_PAYLOAD_WAL_MAX_MB")
        .ok()
        .and_then(|v| v.parse::<u64>().ok())
        .filter(|v| *v > 0)
        .unwrap_or(DEFAULT_MAX_TOTAL_MB)
        * 1024
        * 1024;

    let started = Instant::now();
    let segs = list_segments(dir)?;
    let mut loaded = 0u64;
    for (_, path, _) in &segs {
        loaded += replay_segment(path, &mut load)?;
    }
    let next_id = segs.last().map_or(0, |s| s.0 + 1);
    info!(
        "[PAYLOAD-WAL] replayed {} payload(s) from {} segment(s) in {:?}",
        loaded,
        segs.len(),
        started.elapsed()
    );

    let opts = WriterOpts {
        fsync: std::env::var("METANODE_PAYLOAD_WAL_FSYNC").map_or(true, |v| v != "0"),
        discard: std::env::var("METANODE_PAYLOAD_WAL_DEBUG_DISCARD").map_or(false, |v| v == "1"),
    };
    let (tx, rx) = sync_channel::<(TxDigest, Transaction)>(QUEUE_BOUND);
    let dir_owned = dir.to_path_buf();
    let first = OpenOptions::new()
        .create_new(true)
        .write(true)
        .open(segment_path(dir, next_id))?;
    if WAL
        .set(PayloadWal { tx, dropped: AtomicU64::new(0) })
        .is_err()
    {
        return Ok(());
    }
    std::thread::Builder::new()
        .name("payload-wal".into())
        .spawn(move || writer_loop(dir_owned, rx, first, next_id, max_total, opts))?;
    Ok(())
}

#[derive(Clone, Copy)]
struct WriterOpts {
    /// Periodic fdatasync (power-loss durability). Off = data only reaches the OS page cache.
    fsync: bool,
    /// Measurement aid: dequeue and drop without writing, to isolate the enqueue-side cost.
    discard: bool,
}

fn writer_loop(
    dir: PathBuf,
    rx: std::sync::mpsc::Receiver<(TxDigest, Transaction)>,
    first: File,
    mut seg_id: u64,
    max_total: u64,
    opts: WriterOpts,
) {
    let mut out = BufWriter::with_capacity(1 << 20, first);
    let mut seg_len = 0u64;
    let mut dirty = false;
    let mut last_sync = Instant::now();
    let write_rec = |out: &mut BufWriter<File>, d: &TxDigest, t: &Transaction| -> io::Result<u64> {
        let p = t.data();
        out.write_all(&(p.len() as u32).to_le_bytes())?;
        out.write_all(&d.0)?;
        out.write_all(p)?;
        Ok(36 + p.len() as u64)
    };
    loop {
        let first_item = match rx.recv_timeout(FSYNC_INTERVAL) {
            Ok(i) => Some(i),
            Err(RecvTimeoutError::Timeout) => None,
            Err(RecvTimeoutError::Disconnected) => break,
        };
        let mut batch = 0usize;
        let mut item = first_item;
        while let Some((d, t)) = item.take() {
            if opts.discard {
                batch += 1;
                if batch >= MAX_DRAIN_PER_ROUND {
                    break;
                }
                item = rx.try_recv().ok();
                continue;
            }
            match write_rec(&mut out, &d, &t) {
                Ok(n) => {
                    seg_len += n;
                    dirty = true;
                }
                Err(e) => warn!("[PAYLOAD-WAL] write failed: {}", e),
            }
            batch += 1;
            if batch >= MAX_DRAIN_PER_ROUND {
                break;
            }
            item = match rx.try_recv() {
                Ok(i) => Some(i),
                Err(TryRecvError::Empty) => None,
                Err(TryRecvError::Disconnected) => None,
            };
        }
        // Hand data to the OS every round: survives a process kill; fsync is periodic.
        if dirty {
            let _ = out.flush();
            if opts.fsync && last_sync.elapsed() >= FSYNC_INTERVAL {
                let _ = out.get_ref().sync_data();
                last_sync = Instant::now();
                dirty = false;
            }
        }
        if seg_len >= SEGMENT_BYTES {
            let _ = out.flush();
            let _ = out.get_ref().sync_data();
            seg_id += 1;
            match OpenOptions::new().create_new(true).write(true).open(segment_path(&dir, seg_id)) {
                Ok(f) => {
                    out = BufWriter::with_capacity(1 << 20, f);
                    seg_len = 0;
                    prune(&dir, max_total, seg_id);
                }
                Err(e) => warn!("[PAYLOAD-WAL] segment rotation failed: {}", e),
            }
        }
    }
    let _ = out.flush();
    let _ = out.get_ref().sync_data();
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmpdir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("payload_wal_{}_{}", name, std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn write_seg(path: &Path, txs: &[Transaction], garbage_tail: &[u8]) {
        let mut f = File::create(path).unwrap();
        for t in txs {
            f.write_all(&(t.data().len() as u32).to_le_bytes()).unwrap();
            f.write_all(&t.digest().0).unwrap();
            f.write_all(t.data()).unwrap();
        }
        f.write_all(garbage_tail).unwrap();
    }

    #[test]
    fn replay_roundtrip_and_torn_tail() {
        let d = tmpdir("torn");
        let txs: Vec<_> = (0..10u8).map(|i| Transaction::new(vec![i; 50 + i as usize])).collect();
        // Torn tail: a header promising more bytes than exist.
        write_seg(&d.join("seg-0000000000000000.wal"), &txs, &[200, 0, 0, 0, 1, 2, 3]);
        let mut got = Vec::new();
        let n = replay_segment(&d.join("seg-0000000000000000.wal"), &mut |dg, t| got.push((dg, t)))
            .unwrap();
        assert_eq!(n, 10);
        for (i, (dg, t)) in got.iter().enumerate() {
            assert_eq!(*dg, txs[i].digest());
            assert_eq!(t.data(), txs[i].data());
        }
    }

    #[test]
    fn replay_rejects_corrupt_record() {
        let d = tmpdir("corrupt");
        let txs: Vec<_> = (0..4u8).map(|i| Transaction::new(vec![i + 1; 40])).collect();
        let p = d.join("seg-0000000000000000.wal");
        write_seg(&p, &txs, &[]);
        let mut bytes = fs::read(&p).unwrap();
        // Flip a payload byte of record #2 (record size = 4 + 32 + 40).
        bytes[2 * 76 + 40] ^= 0xff;
        fs::write(&p, bytes).unwrap();
        let mut count = 0;
        replay_segment(&p, &mut |_, _| count += 1).unwrap();
        assert_eq!(count, 2, "replay must stop before the corrupt record");
    }

    #[test]
    fn prune_removes_oldest_but_keeps_current() {
        let d = tmpdir("prune");
        for id in 0..4u64 {
            fs::write(segment_path(&d, id), vec![0u8; 1000]).unwrap();
        }
        prune(&d, 2500, 3);
        let ids: Vec<u64> = list_segments(&d).unwrap().iter().map(|s| s.0).collect();
        assert_eq!(ids, vec![2, 3]);
    }
}
