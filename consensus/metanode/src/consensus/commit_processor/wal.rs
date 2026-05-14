// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Lightweight Write-Ahead Log for FFI commit tracking.
//!
//! ABSOLUTE RULE: A commit is ONLY considered safe after Go confirms execution.
//! The WAL records:
//!   1. PENDING — written BEFORE sending commit to Go via FFI
//!   2. COMMITTED — written AFTER Go returns success
//!
//! On restart, WAL entries still in PENDING state indicate a crash between
//! FFI send and Go confirmation. The recovery logic in processor.rs compares
//! pending WAL entries against Go's actual state to detect and log drift.

use std::collections::BTreeMap;
use std::fs::{self, File, OpenOptions};
use std::io::{self, BufReader, BufWriter, Read, Write};
use std::path::{Path, PathBuf};
use tracing::{info, warn};

/// Status byte for WAL entries
const STATUS_PENDING: u8 = 0;
const STATUS_COMMITTED: u8 = 1;

/// Each WAL entry is a fixed-size record:
/// [4 bytes: commit_index (u32 BE)]
/// [8 bytes: global_exec_index (u64 BE)]
/// [8 bytes: epoch (u64 BE)]
/// [1 byte: status (0=PENDING, 1=COMMITTED)]
/// Total: 21 bytes per entry
const WAL_ENTRY_SIZE: usize = 21;

/// Maximum entries before compaction (remove all COMMITTED entries)
const MAX_ENTRIES_BEFORE_COMPACT: usize = 10000;

#[derive(Debug, Clone)]
pub struct WalEntry {
    pub commit_index: u32,
    pub global_exec_index: u64,
    pub epoch: u64,
    pub status: u8,
}

impl WalEntry {
    fn to_bytes(&self) -> [u8; WAL_ENTRY_SIZE] {
        let mut buf = [0u8; WAL_ENTRY_SIZE];
        buf[0..4].copy_from_slice(&self.commit_index.to_be_bytes());
        buf[4..12].copy_from_slice(&self.global_exec_index.to_be_bytes());
        buf[12..20].copy_from_slice(&self.epoch.to_be_bytes());
        buf[20] = self.status;
        buf
    }

    fn from_bytes(buf: &[u8; WAL_ENTRY_SIZE]) -> Self {
        Self {
            commit_index: u32::from_be_bytes([buf[0], buf[1], buf[2], buf[3]]),
            global_exec_index: u64::from_be_bytes([buf[4], buf[5], buf[6], buf[7], buf[8], buf[9], buf[10], buf[11]]),
            epoch: u64::from_be_bytes([buf[12], buf[13], buf[14], buf[15], buf[16], buf[17], buf[18], buf[19]]),
            status: buf[20],
        }
    }
}

/// Lightweight Write-Ahead Log for FFI commit tracking.
pub struct CommitWAL {
    path: PathBuf,
    /// In-memory index: commit_index → latest entry for fast lookup
    entries: BTreeMap<u32, WalEntry>,
    /// Total writes since last compaction
    writes_since_compact: usize,
}

impl CommitWAL {
    /// Open or create a WAL file at the given storage path.
    pub fn open(storage_dir: &Path) -> io::Result<Self> {
        let path = storage_dir.join("commit_wal.bin");

        // Ensure parent directory exists
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent)?;
        }

        // Read existing entries
        let entries = if path.exists() {
            Self::read_all_entries(&path)?
        } else {
            BTreeMap::new()
        };

        let entry_count = entries.len();
        let pending_count = entries.values().filter(|e| e.status == STATUS_PENDING).count();

        if entry_count > 0 {
            info!(
                "📋 [WAL] Opened: {} entries ({} pending, {} committed) at {:?}",
                entry_count, pending_count, entry_count - pending_count, path
            );
        }

        Ok(Self {
            path,
            entries,
            writes_since_compact: 0,
        })
    }

    /// Write a PENDING entry before sending commit to Go via FFI.
    /// Must be called BEFORE dispatch_commit().
    pub fn write_pending(&mut self, commit_index: u32, global_exec_index: u64, epoch: u64) -> io::Result<()> {
        let entry = WalEntry {
            commit_index,
            global_exec_index,
            epoch,
            status: STATUS_PENDING,
        };

        // Append to file
        self.append_entry(&entry)?;
        self.entries.insert(commit_index, entry);
        self.writes_since_compact += 1;

        // Auto-compact if too many entries
        if self.writes_since_compact >= MAX_ENTRIES_BEFORE_COMPACT {
            if let Err(e) = self.compact() {
                warn!("⚠️ [WAL] Compaction failed (non-critical): {}", e);
            }
        }

        Ok(())
    }

    /// Mark a commit as COMMITTED after Go returns success.
    /// Must be called AFTER dispatch_commit() returns Ok.
    pub fn mark_committed(&mut self, commit_index: u32) -> io::Result<()> {
        if let Some(entry) = self.entries.get_mut(&commit_index) {
            entry.status = STATUS_COMMITTED;
            let updated = entry.clone();
            // Append updated status to file (latest entry for same commit_index wins)
            self.append_entry(&updated)?;
            self.writes_since_compact += 1;
        }
        // If entry doesn't exist, silently ignore (commit may have been compacted)
        Ok(())
    }

    /// Get all entries still in PENDING state.
    /// Used during recovery to detect crashes between FFI send and Go confirmation.
    pub fn get_pending_entries(&self) -> Vec<WalEntry> {
        self.entries
            .values()
            .filter(|e| e.status == STATUS_PENDING)
            .cloned()
            .collect()
    }

    /// Get the highest committed commit_index (for diagnostics).
    pub fn last_committed_index(&self) -> Option<u32> {
        self.entries
            .values()
            .filter(|e| e.status == STATUS_COMMITTED)
            .map(|e| e.commit_index)
            .max()
    }

    /// Compact the WAL: rewrite file with only non-committed (PENDING) entries.
    /// Removes all COMMITTED entries to save disk space.
    fn compact(&mut self) -> io::Result<()> {
        let pending: Vec<WalEntry> = self.get_pending_entries();
        let old_count = self.entries.len();

        // Rewrite file with only pending entries
        let tmp_path = self.path.with_extension("tmp");
        {
            let file = File::create(&tmp_path)?;
            let mut writer = BufWriter::new(file);
            for entry in &pending {
                writer.write_all(&entry.to_bytes())?;
            }
            writer.flush()?;
        }
        fs::rename(&tmp_path, &self.path)?;

        // Rebuild in-memory index
        self.entries.clear();
        for entry in pending {
            self.entries.insert(entry.commit_index, entry);
        }
        self.writes_since_compact = 0;

        info!(
            "🗜️ [WAL] Compacted: {} → {} entries (removed {} committed)",
            old_count, self.entries.len(), old_count - self.entries.len()
        );
        Ok(())
    }

    /// Append a single entry to the WAL file.
    fn append_entry(&self, entry: &WalEntry) -> io::Result<()> {
        let file = OpenOptions::new()
            .create(true)
            .append(true)
            .open(&self.path)?;
        let mut writer = BufWriter::new(file);
        writer.write_all(&entry.to_bytes())?;
        writer.flush()?;
        Ok(())
    }

    /// Read all entries from WAL file, deduplicating by commit_index (last write wins).
    fn read_all_entries(path: &Path) -> io::Result<BTreeMap<u32, WalEntry>> {
        let file = File::open(path)?;
        let mut reader = BufReader::new(file);
        let mut entries = BTreeMap::new();
        let mut buf = [0u8; WAL_ENTRY_SIZE];

        loop {
            match reader.read_exact(&mut buf) {
                Ok(()) => {
                    let entry = WalEntry::from_bytes(&buf);
                    // Last write wins — mark_committed overwrites PENDING
                    entries.insert(entry.commit_index, entry);
                }
                Err(ref e) if e.kind() == io::ErrorKind::UnexpectedEof => break,
                Err(e) => {
                    warn!("⚠️ [WAL] Read error (truncated entry?): {}. Stopping read.", e);
                    break;
                }
            }
        }

        Ok(entries)
    }
}
