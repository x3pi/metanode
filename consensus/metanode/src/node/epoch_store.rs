// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Epoch storage helpers — local epoch detection and legacy store loading.
//!
//! These are free functions used during startup to detect local epoch state
//! and optionally load previous-epoch RocksDB stores for historical sync.

use consensus_core::storage::rocksdb_store::RocksDBStore;
use tracing::{info, warn};

/// Detect the highest epoch stored locally
/// Returns 0 if no epoch data found
pub(super) fn detect_local_epoch(storage_path: &std::path::Path) -> u64 {
    let epochs_dir = storage_path.join("epochs");
    if !epochs_dir.exists() {
        return 0;
    }

    let mut max_epoch = 0u64;
    if let Ok(entries) = std::fs::read_dir(&epochs_dir) {
        for entry in entries.flatten() {
            if let Some(name) = entry.file_name().to_str() {
                if let Some(epoch_str) = name.strip_prefix("epoch_") {
                    if let Ok(epoch) = epoch_str.parse::<u64>() {
                        max_epoch = max_epoch.max(epoch);
                    }
                }
            }
        }
    }

    max_epoch
}

/// Load previous epoch RocksDB stores into LegacyEpochStoreManager
/// This enables nodes to serve historical commits when starting directly in a later epoch
#[allow(dead_code)]
pub(super) fn load_legacy_epoch_stores(
    legacy_manager: &std::sync::Arc<consensus_core::LegacyEpochStoreManager>,
    storage_path: &std::path::Path,
    current_epoch: u64,
    max_epochs: usize,
) {
    if current_epoch == 0 {
        // No previous epochs to load
        return;
    }

    let epochs_dir = storage_path.join("epochs");
    if !epochs_dir.exists() {
        info!("📦 [LEGACY STORE] No epochs directory found, skipping legacy store loading");
        return;
    }

    let max_to_load = if max_epochs == 0 {
        usize::MAX // Archive mode: load all
    } else {
        max_epochs
    };

    info!(
        "📦 [LEGACY STORE] Loading up to {} previous epoch stores (current_epoch={})",
        if max_epochs == 0 {
            "ALL".to_string()
        } else {
            max_to_load.to_string()
        },
        current_epoch
    );

    // Load previous epochs (up to max_epochs)
    let mut loaded_count = 0;
    for epoch in (0..current_epoch).rev() {
        let epoch_db_path = epochs_dir
            .join(format!("epoch_{}", epoch))
            .join("consensus_db");

        if epoch_db_path.exists() {
            // CRITICAL FIX (ANTI-FORK GUARD): Convert to absolute path so RocksDB correctly registers
            // the database. We use a retry loop because NFS mounts or high disk I/O
            // can temporarily cause canonicalize to fail even if the path exists.
            let mut absolute_path_opt = None;
            for attempt in 1..=5 {
                match std::fs::canonicalize(&epoch_db_path) {
                    Ok(path) => {
                        absolute_path_opt = Some(path);
                        break;
                    }
                    Err(e) => {
                        tracing::warn!(
                            "⚠️ [LEGACY STORE] Canonicalize failed for {:?} on attempt {}/5: {}. Retrying in 1s...",
                            epoch_db_path, attempt, e
                        );
                        std::thread::sleep(std::time::Duration::from_secs(1));
                    }
                }
            }

            let absolute_path = match absolute_path_opt {
                Some(path) => path,
                None => {
                    tracing::error!("🚨 [LEGACY STORE] Failed to canonicalize legacy epoch DB path {:?} after 5 attempts. Skipping this epoch store.", epoch_db_path);
                    continue;
                }
            };
            
            let absolute_path_str = match absolute_path.to_str() {
                Some(s) => s,
                None => {
                    tracing::error!("🚨 [LEGACY STORE] Legacy epoch DB path contains invalid UTF-8: {:?}. Skipping this epoch store.", absolute_path);
                    continue;
                }
            };

            info!(
                "📦 [LEGACY STORE] Found previous epoch {} database at {}",
                epoch, absolute_path_str
            );

            // Create read-write store for the legacy epoch
            // Note: RocksDB supports concurrent access from the same process
            let legacy_store =
                std::sync::Arc::new(RocksDBStore::new(absolute_path_str));

            legacy_manager.add_store(epoch, legacy_store);
            loaded_count += 1;

            info!(
                "✅ [LEGACY STORE] Loaded epoch {} store for historical sync ({}/{})",
                epoch, loaded_count, max_to_load
            );

            if loaded_count >= max_to_load {
                break;
            }
        } else {
            info!(
                "⚠️ [LEGACY STORE] Epoch {} database not found at {:?}",
                epoch, epoch_db_path
            );
        }
    }

    if loaded_count > 0 {
        info!(
            "📦 [LEGACY STORE] Successfully loaded {} previous epoch store(s) for sync",
            loaded_count
        );
    } else {
        warn!(
            "⚠️ [LEGACY STORE] No previous epoch stores found. SyncOnly nodes may not be able to fetch historical commits."
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_detect_local_epoch_no_dir() {
        let tmp = tempfile::tempdir().unwrap();
        let result = detect_local_epoch(tmp.path());
        assert_eq!(result, 0, "No epochs dir should return 0");
    }

    #[test]
    fn test_detect_local_epoch_empty_dir() {
        let tmp = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(tmp.path().join("epochs")).unwrap();
        let result = detect_local_epoch(tmp.path());
        assert_eq!(result, 0, "Empty epochs dir should return 0");
    }

    #[test]
    fn test_detect_local_epoch_finds_max() {
        let tmp = tempfile::tempdir().unwrap();
        let epochs_dir = tmp.path().join("epochs");
        std::fs::create_dir_all(&epochs_dir).unwrap();

        // Create epoch directories
        std::fs::create_dir_all(epochs_dir.join("epoch_0")).unwrap();
        std::fs::create_dir_all(epochs_dir.join("epoch_3")).unwrap();
        std::fs::create_dir_all(epochs_dir.join("epoch_7")).unwrap();
        std::fs::create_dir_all(epochs_dir.join("epoch_2")).unwrap();

        let result = detect_local_epoch(tmp.path());
        assert_eq!(result, 7, "Should find highest epoch number");
    }

    #[test]
    fn test_detect_local_epoch_ignores_non_epoch() {
        let tmp = tempfile::tempdir().unwrap();
        let epochs_dir = tmp.path().join("epochs");
        std::fs::create_dir_all(&epochs_dir).unwrap();

        // Non-epoch directories should be ignored
        std::fs::create_dir_all(epochs_dir.join("not_epoch")).unwrap();
        std::fs::create_dir_all(epochs_dir.join("epoch_abc")).unwrap();
        std::fs::create_dir_all(epochs_dir.join("epoch_5")).unwrap();

        let result = detect_local_epoch(tmp.path());
        assert_eq!(result, 5, "Should ignore non-epoch directories");
    }
}
