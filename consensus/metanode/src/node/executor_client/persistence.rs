// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Persistence helpers for crash recovery of executor state.

use anyhow::Result;
use std::path::Path;
use tracing::{info, trace, warn};

/// Write uvarint to buffer (Go's binary.ReadUvarint format)
pub fn write_uvarint(buf: &mut Vec<u8>, mut value: u64) -> Result<()> {
    use std::io::Write;
    loop {
        let mut b = (value & 0x7F) as u8;
        value >>= 7;
        if value != 0 {
            b |= 0x80;
        }
        Write::write_all(buf, &[b])?;
        if value == 0 {
            break;
        }
    }
    Ok(())
}

// Persist last successfully sent index AND commit_index to file for crash recovery
// Uses atomic write (temp file + rename) to prevent corruption
pub async fn persist_last_sent_index(
    storage_path: &Path,
    index: u64,
    commit_index: u32,
) -> Result<()> {
    use tokio::io::AsyncWriteExt;

    let persist_dir = storage_path.join("executor_state");
    std::fs::create_dir_all(&persist_dir)?;

    let temp_path = persist_dir.join("last_sent_index.tmp");
    let final_path = persist_dir.join("last_sent_index.bin");

    // Write to temp file
    let mut file = tokio::fs::File::create(&temp_path).await?;
    // Format: [global_exec_index: u64][commit_index: u32]
    file.write_all(&index.to_le_bytes()).await?;
    file.write_all(&commit_index.to_le_bytes()).await?;
    file.flush().await?;
    file.sync_all().await?;
    drop(file);

    // Atomic rename
    std::fs::rename(&temp_path, &final_path)?;

    trace!(
        "💾 [PERSIST] Saved last_sent_index={}, commit_index={} to {:?}",
        index,
        commit_index,
        final_path
    );
    Ok(())
}

// Persist the last block number retrieved from Go for crash recovery
pub async fn persist_last_block_number(storage_path: &Path, block_number: u64) -> Result<()> {
    use tokio::io::AsyncWriteExt;
    let persist_dir = storage_path.join("executor_state");
    std::fs::create_dir_all(&persist_dir)?;
    let temp_path = persist_dir.join("last_block_number.tmp");
    let final_path = persist_dir.join("last_block_number.bin");
    let mut file = tokio::fs::File::create(&temp_path).await?;
    file.write_all(&block_number.to_le_bytes()).await?;
    file.flush().await?;
    file.sync_all().await?;
    drop(file);
    std::fs::rename(&temp_path, &final_path)?;
    trace!(
        "💾 [PERSIST] Saved last_block_number={} to {:?}",
        block_number,
        final_path
    );
    Ok(())
}

// Read persisted last block number, if any
pub async fn read_last_block_number(storage_path: &Path) -> Result<u64> {
    use tokio::io::AsyncReadExt;
    let persist_dir = storage_path.join("executor_state");
    let final_path = persist_dir.join("last_block_number.bin");
    let mut file = tokio::fs::File::open(&final_path).await?;
    let mut buf = [0u8; 8];
    file.read_exact(&mut buf).await?;
    Ok(u64::from_le_bytes(buf))
}

/// PHASE-B DEPRECATED: Fragment offset tracking is no longer used.
/// Go assigns GEI exclusively via GEIAuthority.
#[allow(dead_code)]
pub async fn persist_fragment_offset(storage_path: &Path, offset: u64) -> Result<()> {
    use tokio::io::AsyncWriteExt;
    let persist_dir = storage_path.join("executor_state");
    std::fs::create_dir_all(&persist_dir)?;
    let temp_path = persist_dir.join("fragment_offset.tmp");
    let final_path = persist_dir.join("fragment_offset.bin");
    let mut file = tokio::fs::File::create(&temp_path).await?;
    file.write_all(&offset.to_le_bytes()).await?;
    file.flush().await?;
    file.sync_all().await?;
    drop(file);
    std::fs::rename(&temp_path, &final_path)?;
    trace!(
        "💾 [PERSIST] Saved fragment_offset={} to {:?}",
        offset,
        final_path
    );
    Ok(())
}

#[allow(dead_code)]
pub async fn reset_fragment_offset(storage_path: &Path) -> Result<()> {
    let persist_path = storage_path
        .join("executor_state")
        .join("fragment_offset.bin");

    if persist_path.exists() {
        tokio::fs::remove_file(&persist_path).await?;
        info!("📂 [PERSIST] Reset/Removed fragment_offset file for new epoch.");
    }
    Ok(())
}

#[allow(dead_code)]
pub fn load_fragment_offset(storage_path: &Path) -> u64 {
    let persist_path = storage_path
        .join("executor_state")
        .join("fragment_offset.bin");

    if !persist_path.exists() {
        return 0;
    }

    match std::fs::read(&persist_path) {
        Ok(bytes) if bytes.len() == 8 => {
            let offset = u64::from_le_bytes([
                bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
            ]);
            info!(
                "📂 [PERSIST] Loaded persisted fragment_offset={} from {:?}",
                offset, persist_path
            );
            offset
        }
        Ok(bytes) => {
            warn!(
                "⚠️ [PERSIST] Corrupted fragment_offset file: {} bytes (expected 8)",
                bytes.len()
            );
            0
        }
        Err(e) => {
            warn!("⚠️ [PERSIST] Failed to read fragment_offset: {}", e);
            0
        }
    }
}

/// Load persisted last sent index from file
/// Returns None if file doesn't exist or is corrupted
/// Returns (global_exec_index, commit_index)
pub fn load_persisted_last_index(storage_path: &Path) -> Option<(u64, u32)> {
    let persist_path = storage_path
        .join("executor_state")
        .join("last_sent_index.bin");

    if !persist_path.exists() {
        return None;
    }

    match std::fs::read(&persist_path) {
        Ok(bytes) => {
            if bytes.len() == 12 {
                // New format: u64 + u32
                let index = u64::from_le_bytes([
                    bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
                ]);
                let commit = u32::from_le_bytes([bytes[8], bytes[9], bytes[10], bytes[11]]);
                info!(
                    "📂 [PERSIST] Loaded persisted last_sent_index={}, commit_index={} from {:?}",
                    index, commit, persist_path
                );
                Some((index, commit))
            } else if bytes.len() == 8 {
                // Legacy format: u64 only (commit_index assumed 0 or unknown)
                let index = u64::from_le_bytes([
                    bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
                ]);
                warn!(
                    "⚠️ [PERSIST] Legacy format detected (only u64). Defaulting commit_index to 0."
                );
                Some((index, 0))
            } else {
                warn!(
                    "⚠️ [PERSIST] Corrupted last_sent_index file: {} bytes (expected 8 or 12)",
                    bytes.len()
                );
                None
            }
        }
        Err(e) => {
            warn!("⚠️ [PERSIST] Failed to read last_sent_index: {}", e);
            None
        }
    }
}

/// Get a wipe-safe storage path that survives DAG wipes.
/// DAG wipe deletes `config/storage/node_{id}/` but this creates
/// `config/storage/wipe_safe_node_{id}/` as a sibling directory.
pub fn wipe_safe_path(storage_path: &Path) -> std::path::PathBuf {
    // storage_path is like: config/storage/node_1
    // We want: config/storage/wipe_safe_node_1
    if let Some(parent) = storage_path.parent() {
        if let Some(dir_name) = storage_path.file_name().and_then(|n| n.to_str()) {
            return parent.join(format!("wipe_safe_{}", dir_name));
        }
    }
    // Fallback: use storage_path itself
    storage_path.to_path_buf()
}

#[allow(dead_code)]
pub async fn persist_fragment_offset_wipe_safe(storage_path: &Path, offset: u64) -> Result<()> {
    use tokio::io::AsyncWriteExt;
    let safe_dir = wipe_safe_path(storage_path).join("executor_state");
    std::fs::create_dir_all(&safe_dir)?;
    let temp_path = safe_dir.join("fragment_offset.tmp");
    let final_path = safe_dir.join("fragment_offset.bin");
    let mut file = tokio::fs::File::create(&temp_path).await?;
    file.write_all(&offset.to_le_bytes()).await?;
    file.flush().await?;
    file.sync_all().await?;
    drop(file);
    std::fs::rename(&temp_path, &final_path)?;
    trace!(
        "💾 [PERSIST-SAFE] Saved fragment_offset={} to {:?}",
        offset, final_path
    );
    Ok(())
}

#[allow(dead_code)]
pub fn load_fragment_offset_wipe_safe(storage_path: &Path) -> u64 {
    let persist_path = wipe_safe_path(storage_path)
        .join("executor_state")
        .join("fragment_offset.bin");

    if !persist_path.exists() {
        return 0;
    }

    match std::fs::read(&persist_path) {
        Ok(bytes) if bytes.len() == 8 => {
            let offset = u64::from_le_bytes([
                bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
            ]);
            info!(
                "📂 [PERSIST-SAFE] Loaded wipe-safe fragment_offset={} from {:?}",
                offset, persist_path
            );
            offset
        }
        Ok(bytes) => {
            warn!(
                "⚠️ [PERSIST-SAFE] Corrupted wipe-safe fragment_offset file: {} bytes",
                bytes.len()
            );
            0
        }
        Err(e) => {
            warn!("⚠️ [PERSIST-SAFE] Failed to read wipe-safe fragment_offset: {}", e);
            0
        }
    }
}

#[allow(dead_code)]
pub async fn reset_fragment_offset_wipe_safe(storage_path: &Path) -> Result<()> {
    let persist_path = wipe_safe_path(storage_path)
        .join("executor_state")
        .join("fragment_offset.bin");

    if persist_path.exists() {
        tokio::fs::remove_file(&persist_path).await?;
        info!("📂 [PERSIST-SAFE] Reset wipe-safe fragment_offset file for new epoch.");
    }
    
    // Also reset the JSON map
    let json_path = wipe_safe_path(storage_path)
        .join("executor_state")
        .join("fragment_offsets.json");
    if json_path.exists() {
        tokio::fs::remove_file(&json_path).await?;
        info!("📂 [PERSIST-SAFE] Reset wipe-safe fragment_offsets JSON file for new epoch.");
    }
    Ok(())
}

#[allow(dead_code)]
pub async fn persist_recent_fragment_offsets_wipe_safe(
    storage_path: &Path,
    commit_index: u32,
    offset: u64,
) -> Result<()> {
    use std::collections::BTreeMap;
    use tokio::io::AsyncWriteExt;

    let safe_dir = wipe_safe_path(storage_path).join("executor_state");
    std::fs::create_dir_all(&safe_dir)?;
    let final_path = safe_dir.join("fragment_offsets.json");
    let temp_path = safe_dir.join("fragment_offsets.tmp");

    let mut map: BTreeMap<u32, u64> = BTreeMap::new();
    if final_path.exists() {
        if let Ok(contents) = tokio::fs::read_to_string(&final_path).await {
            if let Ok(parsed) = serde_json::from_str(&contents) {
                map = parsed;
            }
        }
    }

    map.insert(commit_index, offset);

    // Prune old entries to keep bounded size
    while map.len() > 100 {
        if let Some(first_key) = map.keys().next().copied() {
            map.remove(&first_key);
        }
    }

    let json = serde_json::to_string(&map)?;
    let mut file = tokio::fs::File::create(&temp_path).await?;
    file.write_all(json.as_bytes()).await?;
    file.flush().await?;
    file.sync_all().await?;
    drop(file);
    std::fs::rename(&temp_path, &final_path)?;
    Ok(())
}

#[allow(dead_code)]
pub fn load_recent_fragment_offsets_wipe_safe(storage_path: &Path) -> std::collections::BTreeMap<u32, u64> {
    let safe_dir = wipe_safe_path(storage_path).join("executor_state");
    let final_path = safe_dir.join("fragment_offsets.json");

    if let Ok(contents) = std::fs::read_to_string(&final_path) {
        if let Ok(parsed) = serde_json::from_str(&contents) {
            return parsed;
        }
    }
    std::collections::BTreeMap::new()
}

/// Persist last_sent_index to a WIPE-SAFE location.
pub async fn persist_last_sent_index_wipe_safe(
    storage_path: &Path,
    index: u64,
    commit_index: u32,
) -> Result<()> {
    use tokio::io::AsyncWriteExt;
    let safe_dir = wipe_safe_path(storage_path).join("executor_state");
    std::fs::create_dir_all(&safe_dir)?;
    let temp_path = safe_dir.join("last_sent_index.tmp");
    let final_path = safe_dir.join("last_sent_index.bin");
    let mut file = tokio::fs::File::create(&temp_path).await?;
    file.write_all(&index.to_le_bytes()).await?;
    file.write_all(&commit_index.to_le_bytes()).await?;
    file.flush().await?;
    file.sync_all().await?;
    drop(file);
    std::fs::rename(&temp_path, &final_path)?;
    trace!(
        "💾 [PERSIST-SAFE] Saved last_sent_index={}, commit_index={} to {:?}",
        index, commit_index, final_path
    );
    Ok(())
}

/// Load last_sent_index from wipe-safe location.
pub fn load_persisted_last_index_wipe_safe(storage_path: &Path) -> Option<(u64, u32)> {
    let persist_path = wipe_safe_path(storage_path)
        .join("executor_state")
        .join("last_sent_index.bin");

    if !persist_path.exists() {
        return None;
    }

    match std::fs::read(&persist_path) {
        Ok(bytes) if bytes.len() == 12 => {
            let index = u64::from_le_bytes([
                bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
            ]);
            let commit = u32::from_le_bytes([bytes[8], bytes[9], bytes[10], bytes[11]]);
            info!(
                "📂 [PERSIST-SAFE] Loaded wipe-safe last_sent_index={}, commit_index={} from {:?}",
                index, commit, persist_path
            );
            Some((index, commit))
        }
        Ok(bytes) if bytes.len() == 8 => {
            let index = u64::from_le_bytes([
                bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
            ]);
            warn!("⚠️ [PERSIST-SAFE] Legacy format detected. Defaulting commit_index to 0.");
            Some((index, 0))
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn test_write_uvarint_small_value() {
        let mut buf = Vec::new();
        write_uvarint(&mut buf, 42).unwrap();
        assert_eq!(buf, vec![42u8]); // Values < 128 fit in one byte
    }

    #[test]
    fn test_write_uvarint_large_value() {
        let mut buf = Vec::new();
        write_uvarint(&mut buf, 300).unwrap();
        // 300 = 0b100101100
        // First byte: 0b00101100 | 0x80 = 0xAC
        // Second byte: 0b00000010 = 0x02
        assert_eq!(buf, vec![0xAC, 0x02]);
    }

    #[tokio::test]
    async fn test_persist_and_load_last_index() {
        let dir = tempdir().unwrap();
        let path = dir.path();

        persist_last_sent_index(path, 12345, 67).await.unwrap();

        let result = load_persisted_last_index(path);
        assert_eq!(result, Some((12345, 67)));
    }

    #[test]
    fn test_load_corrupted_file() {
        let dir = tempdir().unwrap();
        let persist_dir = dir.path().join("executor_state");
        std::fs::create_dir_all(&persist_dir).unwrap();
        let file_path = persist_dir.join("last_sent_index.bin");
        std::fs::write(&file_path, [1, 2, 3]).unwrap(); // 3 bytes = corrupted

        let result = load_persisted_last_index(dir.path());
        assert_eq!(result, None);
    }

    #[test]
    fn test_load_legacy_format() {
        let dir = tempdir().unwrap();
        let persist_dir = dir.path().join("executor_state");
        std::fs::create_dir_all(&persist_dir).unwrap();
        let file_path = persist_dir.join("last_sent_index.bin");
        std::fs::write(&file_path, 999u64.to_le_bytes()).unwrap(); // 8 bytes = legacy

        let result = load_persisted_last_index(dir.path());
        assert_eq!(result, Some((999, 0))); // commit_index defaults to 0
    }

    #[tokio::test]
    async fn test_persist_and_read_block_number() {
        let dir = tempdir().unwrap();
        let path = dir.path();

        persist_last_block_number(path, 42069).await.unwrap();

        let result = read_last_block_number(path).await.unwrap();
        assert_eq!(result, 42069);
    }
}
