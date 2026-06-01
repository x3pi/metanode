// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::BTreeSet,
    sync::Arc,
};

use consensus_config::AuthorityIndex;
use consensus_types::block::BlockRef;
use parking_lot::RwLock;
use tokio::sync::broadcast as tokio_broadcast;
use tracing::debug;

use crate::{
    block::{BlockAPI as _, ExtendedBlock, VerifiedBlock},
    block_verifier::BlockVerifier,
    commit_vote_monitor::CommitVoteMonitor,
    context::Context,
    core_thread::CoreThreadDispatcher,
    dag_state::DagState,
    epoch_change_provider::EpochChangeProcessor,
    error::{ConsensusError, ConsensusResult},
    legacy_store::LegacyEpochStoreManager,
    round_tracker::PeerRoundTracker,
    storage::Store,
    synchronizer::SynchronizerHandle,
    transaction_certifier::TransactionCertifier,
};

pub mod handlers;
pub mod broadcast;
#[cfg(test)]
mod tests;

pub(crate) use broadcast::SubscriptionCounter;
pub(crate) use broadcast::BroadcastedBlockStream;

pub(crate) const COMMIT_LAG_MULTIPLIER: u32 = 10000;

/// Authority's network service implementation, agnostic to the actual networking stack used.
pub(crate) struct AuthorityService<C: CoreThreadDispatcher> {
    context: Arc<Context>,
    commit_vote_monitor: Arc<CommitVoteMonitor>,
    block_verifier: Arc<dyn BlockVerifier>,
    synchronizer: Arc<SynchronizerHandle>,
    core_dispatcher: Arc<C>,
    rx_block_broadcast: tokio_broadcast::Receiver<ExtendedBlock>,
    subscription_counter: Arc<SubscriptionCounter>,
    transaction_certifier: TransactionCertifier,
    dag_state: Arc<RwLock<DagState>>,
    store: Arc<dyn Store>,
    round_tracker: Arc<RwLock<PeerRoundTracker>>,
    epoch_change_processor: Arc<RwLock<Option<Box<dyn EpochChangeProcessor>>>>,
    /// Legacy store manager for querying previous epochs (optional for backward compatibility)
    legacy_store_manager: Option<Arc<LegacyEpochStoreManager>>,
    /// Epoch base index (boundary block of the previous epoch). Used when there are no
    /// commits in the store (e.g., snapshot restore cold start) to correctly compute
    /// global_exec_index ranges.
    epoch_base_index: u64,
    /// Cache of recently verified block refs to skip re-verification.
    /// Bounded to prevent unbounded memory growth.
    #[allow(dead_code)]
    recently_verified_blocks: Arc<RwLock<BTreeSet<BlockRef>>>,
}

impl<C: CoreThreadDispatcher> AuthorityService<C> {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        context: Arc<Context>,
        block_verifier: Arc<dyn BlockVerifier>,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
        round_tracker: Arc<RwLock<PeerRoundTracker>>,
        synchronizer: Arc<SynchronizerHandle>,
        core_dispatcher: Arc<C>,
        rx_block_broadcast: tokio_broadcast::Receiver<ExtendedBlock>,
        transaction_certifier: TransactionCertifier,
        dag_state: Arc<RwLock<DagState>>,
        store: Arc<dyn Store>,
        epoch_change_processor: Option<Box<dyn EpochChangeProcessor>>,
        legacy_store_manager: Option<Arc<LegacyEpochStoreManager>>,
        epoch_base_index: u64,
    ) -> Self {
        let subscription_counter = Arc::new(SubscriptionCounter::new(context.clone()));
        Self {
            context,
            block_verifier,
            commit_vote_monitor,
            synchronizer,
            core_dispatcher,
            rx_block_broadcast,
            subscription_counter,
            transaction_certifier,
            dag_state,
            store,
            round_tracker,
            epoch_change_processor: Arc::new(RwLock::new(epoch_change_processor)),
            legacy_store_manager,
            epoch_base_index,
            recently_verified_blocks: Arc::new(RwLock::new(BTreeSet::new())),
        }
    }

    // Parses and validates serialized excluded ancestors.
    pub(super) fn parse_excluded_ancestors(
        &self,
        peer: AuthorityIndex,
        block: &VerifiedBlock,
        mut excluded_ancestors: Vec<Vec<u8>>,
    ) -> ConsensusResult<Vec<BlockRef>> {
        let peer_hostname = &self.context.committee.authority(peer).hostname;

        let excluded_ancestors_limit = self.context.committee.size() * 2;
        if excluded_ancestors.len() > excluded_ancestors_limit {
            debug!(
                "Dropping {} excluded ancestor(s) from {} {} due to size limit",
                excluded_ancestors.len() - excluded_ancestors_limit,
                peer,
                peer_hostname,
            );
            excluded_ancestors.truncate(excluded_ancestors_limit);
        }

        let excluded_ancestors = excluded_ancestors
            .into_iter()
            .map(|serialized| {
                let block_ref: BlockRef =
                    bcs::from_bytes(&serialized).map_err(ConsensusError::MalformedBlock)?;
                if !self.context.committee.is_valid_index(block_ref.author) {
                    return Err(ConsensusError::InvalidAuthorityIndex {
                        index: block_ref.author,
                        max: self.context.committee.size(),
                    });
                }
                if block_ref.round >= block.round() {
                    return Err(ConsensusError::InvalidAncestorRound {
                        ancestor: block_ref.round,
                        block: block.round(),
                    });
                }
                Ok(block_ref)
            })
            .collect::<ConsensusResult<Vec<BlockRef>>>()?;

        for excluded_ancestor in &excluded_ancestors {
            let excluded_ancestor_hostname = &self
                .context
                .committee
                .authority(excluded_ancestor.author)
                .hostname;
            self.context
                .metrics
                .node_metrics
                .network_excluded_ancestors_count_by_authority
                .with_label_values(&[excluded_ancestor_hostname])
                .inc();
        }
        self.context
            .metrics
            .node_metrics
            .network_received_excluded_ancestors_from_authority
            .with_label_values(&[peer_hostname])
            .inc_by(excluded_ancestors.len() as u64);

        Ok(excluded_ancestors)
    }
}

