use anyhow::Result;
use prost::Message;
use tracing::info;

use super::proto;
use super::ExecutorClient;
use crate::node::executor_client::block_store;

impl ExecutorClient {
    /// Get a range of blocks from Native Rust Store
    /// Used by validators to serve blocks to SyncOnly nodes
    pub async fn get_blocks_range(
        &self,
        from_block: u64,
        to_block: u64,
    ) -> Result<Vec<proto::BlockData>> {
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        info!(
            "📤 [BLOCK SYNC] Requesting blocks {} to {} from Rust Store",
            from_block, to_block
        );

        let storage_path = match self.storage_path() {
            Some(p) => p,
            None => return Err(anyhow::anyhow!("No storage path configured")),
        };

        let blocks_raw = block_store::load_executable_blocks_range(storage_path, from_block, to_block).await?;
        
        let mut block_data_list = Vec::with_capacity(blocks_raw.len());
        for (_, data) in blocks_raw {
            match proto::ExecutableBlock::decode(&data[..]) {
                Ok(b) => {
                    // Convert ExecutableBlock to BlockData so it can be returned
                    let mut bdata = proto::BlockData::default();
                    bdata.block_number = b.global_exec_index;
                    bdata.epoch = b.epoch;
                    bdata.block_hash = b.commit_hash;
                    bdata.timestamp_ms = b.commit_timestamp_ms;
                    bdata.state_root = b.state_root;
                    block_data_list.push(bdata);
                },
                Err(e) => {
                    tracing::warn!("⚠️ Failed to decode ExecutableBlock: {}", e);
                }
            }
        }

        // Reconstruct parent hashes sequentially
        for i in 1..block_data_list.len() {
            let prev_hash = block_data_list[i - 1].block_hash.clone();
            block_data_list[i].parent_hash = prev_hash;
        }

        info!(
            "✅ [BLOCK SYNC] Received {} blocks from Rust Store",
            block_data_list.len()
        );
        Ok(block_data_list)
    }

    /// Sync blocks to local Rust Store (store-only mode)
    /// Used by SyncOnly nodes to write blocks received from peers
    pub async fn sync_blocks(&self, blocks: Vec<proto::BlockData>) -> Result<(u64, u64)> {
        let (count, last_block, _) = self.sync_blocks_inner(blocks, false).await?;
        Ok((count, last_block))
    }

    /// Sync AND EXECUTE blocks (currently we just store them here, BlockStmScheduler executes)
    /// Returns (synced_count, last_block, last_executed_gei).
    pub async fn sync_and_execute_blocks(
        &self,
        blocks: Vec<proto::BlockData>,
    ) -> Result<(u64, u64, u64)> {
        self.sync_blocks_inner(blocks, true).await
    }

    /// Internal: sync blocks natively
    async fn sync_blocks_inner(
        &self,
        blocks: Vec<proto::BlockData>,
        _execute_mode: bool,
    ) -> Result<(u64, u64, u64)> {
        if !self.is_enabled() {
            return Err(anyhow::anyhow!("Executor client is not enabled"));
        }

        if blocks.is_empty() {
            return Ok((0, 0, 0));
        }

        let total_blocks = blocks.len();
        let first_block = blocks.first().map(|b| b.block_number).unwrap_or(0);
        let last_block = blocks.last().map(|b| b.block_number).unwrap_or(0);

        info!(
            "📤 [BLOCK SYNC] Syncing {} blocks ({} to {}) natively to Rust Store",
            total_blocks, first_block, last_block
        );

        let storage_path = match self.storage_path() {
            Some(p) => p,
            None => return Err(anyhow::anyhow!("No storage path configured")),
        };

        let mut store_batch = Vec::with_capacity(blocks.len());
        let mut encoded_blocks = Vec::with_capacity(blocks.len());
        
        // Encode each block and prepare for batch storage
        for block in &blocks {
            let encoded = block.encode_to_vec();
            encoded_blocks.push((block.block_number, encoded));
        }

        for (gei, data) in &encoded_blocks {
            store_batch.push((*gei, data.as_slice()));
        }

        block_store::store_executable_blocks_batch(storage_path, &store_batch).await?;

        let total_synced_count = total_blocks as u64;
        let final_synced_block = last_block;
        let final_executed_gei = last_block; // Sync mode logic

        info!(
            "✅ [BLOCK SYNC] Successfully synced {} blocks (last: {}, last_gei: {}) natively",
            total_synced_count, final_synced_block, final_executed_gei
        );
        Ok((total_synced_count, final_synced_block, final_executed_gei))
    }
}
