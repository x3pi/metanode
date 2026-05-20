// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::{Context, Result};
use std::fs::{File, OpenOptions};
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use tracing::info;

/// Simple file-based Write-Ahead Log to ensure crash-safety around Go FFI boundary.
pub struct CommitWAL {
    file: File,
    path: PathBuf,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PendingCommitRecord {
    pub commit_index: u32,
    pub global_exec_index: u64,
    pub epoch: u64,
}

impl CommitWAL {
    pub fn open<P: AsRef<Path>>(storage_path: P) -> Result<Self> {
        let wal_path = storage_path.as_ref().join("commit_ffi_wal.log");
        
        let file = OpenOptions::new()
            .create(true)
            .append(true)
            .read(true)
            .open(&wal_path)
            .with_context(|| format!("Failed to open WAL file at {:?}", wal_path))?;

        info!("📝 [WAL] Initialized FFI Write-Ahead Log at {:?}", wal_path);

        Ok(Self {
            file,
            path: wal_path,
        })
    }

    /// Read the WAL to find if there is an uncommitted (pending) FFI dispatch.
    pub fn get_pending_entries(&self) -> Vec<PendingCommitRecord> {
        let mut entries = Vec::new();
        
        let file = match File::open(&self.path) {
            Ok(f) => f,
            Err(_) => return entries,
        };
        
        let reader = BufReader::new(file);
        let mut current_pending = None;

        for line_res in reader.lines() {
            let line = match line_res {
                Ok(l) => l,
                Err(_) => continue,
            };
            
            if line.is_empty() {
                continue;
            }

            let parts: Vec<&str> = line.split(':').collect();
            if parts.len() < 2 {
                continue;
            }

            let action = parts[0];
            let data: Vec<&str> = parts[1].split(',').collect();

            match action {
                "PENDING" => {
                    if data.len() == 3 {
                        if let (Ok(ci), Ok(gei), Ok(ep)) = (
                            data[0].parse::<u32>(),
                            data[1].parse::<u64>(),
                            data[2].parse::<u64>(),
                        ) {
                            current_pending = Some(PendingCommitRecord {
                                commit_index: ci,
                                global_exec_index: gei,
                                epoch: ep,
                            });
                        }
                    }
                }
                "COMMITTED" => {
                    if data.len() == 1 {
                        if let Ok(ci) = data[0].parse::<u32>() {
                            if let Some(ref pending) = current_pending {
                                if pending.commit_index == ci {
                                    // Successfully completed before crash
                                    current_pending = None;
                                }
                            }
                        }
                    }
                }
                _ => {}
            }
        }

        if let Some(pending) = current_pending {
            entries.push(pending);
        }

        entries
    }

    /// Mark that we are about to pass a commit across the FFI boundary to Go.
    pub fn write_pending(&mut self, commit_index: u32, global_exec_index: u64, epoch: u64) -> Result<()> {
        writeln!(self.file, "PENDING:{},{},{}", commit_index, global_exec_index, epoch)?;
        Ok(())
    }

    /// Mark that Go has successfully returned from the FFI call.
    pub fn mark_committed(&mut self, commit_index: u32) -> Result<()> {
        writeln!(self.file, "COMMITTED:{}", commit_index)?;
        Ok(())
    }
}


