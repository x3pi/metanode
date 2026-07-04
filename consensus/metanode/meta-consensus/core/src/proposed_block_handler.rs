// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;

use tokio::sync::broadcast;
use tracing::warn;

use crate::{block::ExtendedBlock, context::Context, transaction_certifier::TransactionCertifier, commit_vote_monitor::CommitVoteMonitor};

/// Runs async processing logic for proposed blocks.
/// Currently it only call transaction certifier with proposed blocks.
/// In future, more logic related to proposing should be moved here, for example
/// flushing dag state.
pub(crate) struct ProposedBlockHandler {
    context: Arc<Context>,
    rx_block_broadcast: broadcast::Receiver<ExtendedBlock>,
    transaction_certifier: TransactionCertifier,
    coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
    commit_vote_monitor: Arc<CommitVoteMonitor>,
}

impl ProposedBlockHandler {
    pub(crate) fn new(
        context: Arc<Context>,
        rx_block_broadcast: broadcast::Receiver<ExtendedBlock>,
        transaction_certifier: TransactionCertifier,
        coordination_hub: crate::coordination_hub::ConsensusCoordinationHub,
        commit_vote_monitor: Arc<CommitVoteMonitor>,
    ) -> Self {
        Self {
            context,
            rx_block_broadcast,
            transaction_certifier,
            coordination_hub,
            commit_vote_monitor,
        }
    }

    pub(crate) async fn run(&mut self) {
        tracing::info!("🚀 [PROPOSED BLOCK HANDLER] RUN LOOP STARTED");
        loop {
            match self.rx_block_broadcast.recv().await {
                Ok(extended_block) => self.handle_proposed_block(extended_block),
                Err(broadcast::error::RecvError::Closed) => {
                    tracing::info!(
                        "❌ [PROPOSED BLOCK HANDLER] Broadcast channel CLOSED - shutting down"
                    );
                    return;
                }
                Err(broadcast::error::RecvError::Lagged(e)) => {
                    warn!("Handler is lagging! {e}");
                    // Re-run the loop to receive again.
                    continue;
                }
            };
        }
    }

    fn handle_proposed_block(&self, extended_block: ExtendedBlock) {
        // ALWAYS observe our own proposed blocks for commit votes, regardless of fastpath or health!
        // This is critical because our own votes must be counted in our local CommitVoteMonitor
        // for DIGEST-GATE quorum verification to function when network size is reduced (e.g. 3/4 nodes).
        self.commit_vote_monitor.observe_block(&extended_block.block);

        if !self.context.protocol_config.mysticeti_fastpath() {
            return;
        }
        
        let phase = self.coordination_hub.get_phase();
        use crate::coordination_hub::NodeConsensusPhase;
        if matches!(phase, NodeConsensusPhase::Initializing | NodeConsensusPhase::StateSyncing) {
            tracing::debug!("⏳ [PROPOSED BLOCK HANDLER] Ignoring proposed block because phase is {:?}", phase);
            return;
        }
        /* let _scope = tracing::info_span!("handle_proposed_block").entered(); */
        self.transaction_certifier
            .add_proposed_block(extended_block.block.clone());
    }
}
