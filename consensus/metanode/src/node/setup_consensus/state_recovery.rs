use crate::node::executor_client::ExecutorClient;
use std::sync::Arc;

/// Triggers Fast-Sync State Reconciliation when a fork is detected.
/// Fetches the correct block from a quorum of peers and forces the Go engine to revert and replay.
pub async fn trigger_fast_sync(
    client: Arc<ExecutorClient>,
    peers: Vec<String>,
    target_block: u64,
) {
    tracing::info!("🔄 [FAST-SYNC] Triggered fast-sync for block {}", target_block);
    
    // Fetch from peers
    let peer_results = crate::network::peer_rpc::query_block_from_all_peers(
        &peers, target_block,
    ).await;
    
    let mut correct_block = None;
    let mut max_count = 0;
    
    use std::collections::HashMap;
    let mut groups: HashMap<Vec<u8>, (usize, crate::node::executor_client::proto::BlockData)> = HashMap::new();
    
    for (_, result) in peer_results {
        if let Ok(block) = result {
            let hash = block.block_hash.clone();
            let entry = groups.entry(hash).or_insert((0, block));
            entry.0 += 1;
            if entry.0 > max_count {
                max_count = entry.0;
                correct_block = Some(entry.1.clone());
            }
        }
    }
    
    if let Some(block) = correct_block {
        tracing::info!("✅ [FAST-SYNC] Found majority block {} ({} peers). Pushing to Go Master for State Reconciliation...", target_block, max_count);
        
        // Push the correct block to Go with execute_mode = true and preserve_own_commit_index = true
        // This leverages Go's block rewind mechanism to fix the divergence dynamically.
        match client.sync_and_execute_blocks(vec![block], true).await {
            Ok((_, _, _)) => {
                tracing::info!("✅ [FAST-SYNC] Successfully re-executed block {}. State is now reconciled.", target_block);
            }
            Err(e) => {
                tracing::error!("🚨 [FAST-SYNC] Failed to reconcile block {}: {}", target_block, e);
            }
        }
    } else {
        tracing::error!("🚨 [FAST-SYNC] No valid blocks returned from peers for block {}", target_block);
    }
}
