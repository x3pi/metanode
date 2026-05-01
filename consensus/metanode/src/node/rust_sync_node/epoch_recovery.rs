// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Queue processing and deferred epoch transition handling.

use super::RustSyncNode;
use anyhow::Result;
use consensus_core::{CommitAPI, CommitRef, CommittedSubDag};
use std::sync::atomic::Ordering;
use std::time::{Duration, Instant};
use tracing::{debug, error, info, warn};

use crate::network::peer_rpc::query_peer_epoch_boundary_data;

impl RustSyncNode {
    /// Process queue - drain ready commits and send to Go sequentially (Phase 3)
    #[allow(dead_code)]
    pub(super) async fn process_queue(&self) -> Result<u32> {
        let pq_timer = self.metrics.process_queue_total_seconds.start_timer();

        let mut ready_commits = {
            let drain_start = Instant::now();
            let mut queue = self.block_queue.lock().await;
            let drained = queue.drain_ready();
            self.metrics
                .queue_drain_duration_seconds
                .observe(drain_start.elapsed().as_secs_f64());
            drained
        };

        if ready_commits.is_empty() {
            pq_timer.observe_duration();
            return Ok(0);
        }

        let mut blocks_sent = 0u32;
        let mut failed_at: Option<usize> = None;

        for (idx, commit_data) in ready_commits.iter().enumerate() {
            // Construct CommittedSubDag
            let mut subdag = CommittedSubDag::new(
                commit_data.commit.leader(),
                commit_data.blocks.clone(),
                commit_data.commit.timestamp_ms(),
                CommitRef::new(
                    commit_data.commit.index(),
                    consensus_core::CommitDigest::MIN,
                ),
                commit_data.commit.global_exec_index(),
            );

            // Resolve leader ETH address and embed into subdag (same pattern as CommitProcessor)
            let epoch = commit_data.epoch;
            let leader_author_index = subdag.leader.author.value();

            let resolve_start = Instant::now();
            let mut retry_count = 0u32;
            loop {
                {
                    let mut cache = self.epoch_eth_addresses.write().await;

                    // Load from peers/Go if missing
                    if !cache.contains_key(&epoch) {
                        info!(
                            "📥 [RUST-SYNC] Cache miss for epoch {}. Trying PEERS FIRST...",
                            epoch
                        );
                        let mut loaded = false;

                        // Try peers first (authoritative for SyncOnly nodes)
                        if !self.config.peer_rpc_addresses.is_empty() {
                            for peer_addr in &self.config.peer_rpc_addresses {
                                match query_peer_epoch_boundary_data(peer_addr, epoch).await {
                                    Ok(response) => {
                                        if response.error.is_none()
                                            && !response.validators.is_empty()
                                        {
                                            let mut sorted = response.validators.clone();
                                            sorted.sort_by(|a, b| {
                                                a.authority_key.cmp(&b.authority_key)
                                            });
                                            let addr_list: Vec<Vec<u8>> = sorted
                                                .iter()
                                                .map(|v| {
                                                    hex::decode(v.address.trim_start_matches("0x"))
                                                        .unwrap_or_default()
                                                })
                                                .collect();
                                            cache.insert(epoch, addr_list);
                                            loaded = true;
                                            break;
                                        }
                                    }
                                    Err(e) => {
                                        warn!(
                                            "⚠️ [RUST-SYNC] Peer {} query failed for epoch {}: {}",
                                            peer_addr, epoch, e
                                        );
                                    }
                                }
                            }
                        }

                        // Fallback to local Go
                        if !loaded {
                            match self.executor_client.get_epoch_boundary_data(epoch).await {
                                Ok((_e, _ts, _boundary, validators, _, _)) => {
                                    let mut sorted_validators = validators.clone();
                                    sorted_validators
                                        .sort_by(|a, b| a.authority_key.cmp(&b.authority_key));
                                    let addr_list: Vec<Vec<u8>> = sorted_validators
                                        .iter()
                                        .map(|v| {
                                            hex::decode(v.address.trim_start_matches("0x"))
                                                .unwrap_or_default()
                                        })
                                        .collect();
                                    cache.insert(epoch, addr_list);
                                }
                                Err(e) => {
                                    warn!(
                                        "⚠️ [RUST-SYNC] All sources failed for epoch {}: {}",
                                        epoch, e
                                    );
                                }
                            }
                        }
                    }

                    // Lookup and embed into subdag
                    if let Some(addrs) = cache.get(&epoch) {
                        if leader_author_index < addrs.len() {
                            let addr = &addrs[leader_author_index];
                            if addr.len() == 20 {
                                subdag.leader_address = addr.clone();
                                break; // SUCCESS
                            }
                        }
                    }
                }

                // Backoff and Retry
                retry_count += 1;
                if retry_count % 10 == 0 {
                    warn!("⏳ [RUST-SYNC] Still waiting for leader address... (epoch={}, index={}, retry={})", 
                        epoch, leader_author_index, retry_count);
                }
                tokio::time::sleep(Duration::from_millis(1000)).await;
            }
            self.metrics
                .leader_resolve_duration_seconds
                .observe(resolve_start.elapsed().as_secs_f64());

            // Send to Go executor - DIRECT SEND (bypass buffer)
            let send_start = Instant::now();
            match self
                .executor_client
                .send_committed_subdag_direct(
                    &subdag,
                    commit_data.epoch,
                    commit_data.commit.global_exec_index(),
                    subdag.leader_address.clone(), // Pre-resolved and embedded in subdag
                )
                .await
            {
                Ok(_) => {
                    self.metrics
                        .go_send_per_commit_seconds
                        .observe(send_start.elapsed().as_secs_f64());
                    debug!(
                        "✅ [RUST-SYNC] Sent block {} (commit {}) to Go",
                        commit_data.commit.global_exec_index(),
                        commit_data.commit.index()
                    );
                    blocks_sent += 1;
                }
                Err(e) => {
                    self.metrics
                        .go_send_per_commit_seconds
                        .observe(send_start.elapsed().as_secs_f64());
                    warn!(
                        "⚠️ [RUST-SYNC] Failed to send block {} to Go: {}. Re-queuing {} commits.",
                        commit_data.commit.global_exec_index(),
                        e,
                        ready_commits.len() - idx
                    );
                    failed_at = Some(idx);
                    break;
                }
            }
        }

        // Re-queue all unprocessed commits (from failed_at to end)
        if let Some(start_idx) = failed_at {
            let mut queue = self.block_queue.lock().await;
            // Drain unprocessed commits and re-queue them
            for commit_data in ready_commits.drain(start_idx..) {
                queue.push(commit_data);
            }
        }
        // Successfully processed commits are now dropped, freeing memory

        // Update metrics
        self.metrics
            .blocks_sent_to_go_total
            .inc_by(blocks_sent as f64);

        Ok(blocks_sent)
    }

    /// Check and process pending epoch transitions (from deferred AdvanceEpoch)
    /// This is called after processing sync queue - if Go has synced up to a pending
    /// transition's boundary, we can now safely advance the epoch.
    pub(super) async fn check_and_process_pending_epoch_transitions(&self, go_last_block: u64) {
        // Access the global ConsensusNode to check pending transitions
        use crate::node::get_transition_handler_node;

        if let Some(node) = get_transition_handler_node().await {
            let mut pending_to_process = Vec::new();

            // Scope the lock to avoid holding it across the advance_epoch call
            {
                let node_guard = node.lock().await;
                let mut pending = node_guard.pending_epoch_transitions.lock().await;

                // Check each pending transition
                let mut processed_indices = Vec::new();
                for (idx, trans) in pending.iter().enumerate() {
                    // =============================================================================
                    // CRITICAL FIX: Update epoch_base IMMEDIATELY from pending queue!
                    // =============================================================================
                    let current_base = self.epoch_base_index.load(Ordering::SeqCst);
                    let current_epoch = self.current_epoch.load(Ordering::SeqCst);

                    if trans.epoch > current_epoch || trans.boundary_block > current_base {
                        info!(
                            "🔄 [DEFERRED EPOCH] Updating RustSyncNode to epoch {} (base {} -> {}) for fetching",
                            trans.epoch, current_base, trans.boundary_block
                        );
                        self.current_epoch.store(trans.epoch, Ordering::SeqCst);
                        self.epoch_base_index
                            .store(trans.boundary_block, Ordering::SeqCst);
                    }

                    if go_last_block >= trans.boundary_block {
                        info!(
                            "✅ [DEFERRED EPOCH] Sync complete! Go block {} >= boundary {}. \
                             Processing epoch {} transition.",
                            go_last_block, trans.boundary_block, trans.epoch
                        );
                        pending_to_process.push(trans.clone());
                        processed_indices.push(idx);
                    } else {
                        info!(
                            "⏳ [DEFERRED EPOCH] Still waiting for sync. Go block {} < boundary {} for epoch {}.",
                            go_last_block, trans.boundary_block, trans.epoch
                        );
                    }
                }

                // Remove processed transitions (in reverse order to preserve indices)
                for idx in processed_indices.into_iter().rev() {
                    pending.remove(idx);
                }
            }

            // Process transitions outside the lock
            for trans in pending_to_process {
                let epoch_timer = self.metrics.epoch_transition_duration_seconds.start_timer();
                info!(
                    "📤 [DEFERRED EPOCH] Now calling advance_epoch for epoch {} (boundary: {})",
                    trans.epoch, trans.boundary_block
                );

                if let Err(e) = self
                    .executor_client
                    .advance_epoch(
                        trans.epoch,
                        trans.timestamp_ms,
                        trans.boundary_block,
                        trans.boundary_gei,
                    )
                    .await
                {
                    warn!(
                        "⚠️ [DEFERRED EPOCH] Failed to advance epoch {}: {}. Will retry next sync cycle.",
                        trans.epoch, e
                    );

                    // Re-queue if failed
                    if let Some(node) = get_transition_handler_node().await {
                        let node_guard = node.lock().await;
                        let mut pending = node_guard.pending_epoch_transitions.lock().await;
                        pending.push(trans);
                    }
                } else {
                    info!(
                        "✅ [DEFERRED EPOCH] Successfully advanced to epoch {} with boundary {}",
                        trans.epoch, trans.boundary_block
                    );

                    // Update Rust epoch tracker
                    self.current_epoch.store(trans.epoch, Ordering::SeqCst);
                    self.epoch_base_index
                        .store(trans.boundary_block, Ordering::SeqCst);

                    // CRITICAL FIX: Trigger full epoch transition to check committee
                    info!(
                        "🔄 [DEFERRED EPOCH] Triggering full transition to check committee and potentially promote to Validator"
                    );

                    // Send epoch transition signal
                    if let Err(e) = self.epoch_transition_sender.send((
                        trans.epoch,
                        trans.timestamp_ms,
                        trans.boundary_block,
                        trans.boundary_gei,
                    )) {
                        warn!(
                            "⚠️ [DEFERRED EPOCH] Failed to send epoch transition signal: {}",
                            e
                        );
                    } else {
                        info!(
                            "✅ [DEFERRED EPOCH] Sent epoch transition signal for epoch {} to trigger mode check",
                            trans.epoch
                        );
                    }
                }
                epoch_timer.observe_duration();
            }
        }
    }
}
