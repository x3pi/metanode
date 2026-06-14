// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

// Tests extracted from core.rs for maintainability.

use std::{sync::Arc, vec};

use consensus_config::{local_committee_and_keys, AuthorityIndex, Stake};
use tokio::sync::mpsc::UnboundedReceiver;
use parking_lot::RwLock;
use tokio::sync::broadcast;

use crate::{
    block::{CertifiedBlocksOutput, ExtendedBlock, VerifiedBlock},
    block_manager::BlockManager,
    block_verifier::NoopBlockVerifier,
    commit_observer::CommitObserver,
    context::Context,
    core::{Core, CoreSignals, CoreSignalsReceivers},
    dag_state::DagState,
    error::{ConsensusError, ConsensusResult},
    leader_schedule::LeaderSchedule,
    round_tracker::PeerRoundTracker,
    storage::mem_store::MemStore,
    transaction::{TransactionClient, TransactionConsumer},
    transaction_certifier::TransactionCertifier,
    CommitConsumerArgs, CommittedSubDag,
};

use std::collections::BTreeSet;

/// Creates cores for the specified number of authorities for their corresponding stakes. The method returns the
/// cores and their respective signal receivers are returned in `AuthorityIndex` order asc.
pub(crate) async fn create_cores(
    context: Context,
    authorities: Vec<Stake>,
) -> Vec<CoreTextFixture> {
    let mut cores = Vec::new();

    for index in 0..authorities.len() {
        let own_index = AuthorityIndex::new_for_test(index as u32);
        let core =
            CoreTextFixture::new(context.clone(), authorities.clone(), own_index, false).await;
        cores.push(core);
    }
    cores
}

pub(crate) struct CoreTextFixture {
    pub(crate) core: Core,
    pub(crate) transaction_certifier: TransactionCertifier,
    pub(crate) signal_receivers: CoreSignalsReceivers,
    pub(crate) block_receiver: broadcast::Receiver<ExtendedBlock>,
    pub(crate) _commit_output_receiver: UnboundedReceiver<CommittedSubDag>,
    pub(crate) _blocks_output_receiver: UnboundedReceiver<CertifiedBlocksOutput>,
    pub(crate) dag_state: Arc<RwLock<DagState>>,
    pub(crate) store: Arc<MemStore>,
}

impl CoreTextFixture {
    async fn new(
        context: Context,
        authorities: Vec<Stake>,
        own_index: AuthorityIndex,
        sync_last_known_own_block: bool,
    ) -> Self {
        let (committee, mut signers) = local_committee_and_keys(0, authorities.clone());
        let mut context = context.clone();
        context = context
            .with_committee(committee)
            .with_authority_index(own_index);
        context
            .protocol_config
            .set_consensus_bad_nodes_stake_threshold_for_testing(33);

        let context = Arc::new(context);
        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    
        let mut block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
        let leader_schedule = Arc::new(
            LeaderSchedule::from_store(context.clone(), dag_state.clone())
                .with_num_commits_per_schedule(10),
        );
        let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
        let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());
        let (blocks_sender, _blocks_receiver) =
            tokio::sync::mpsc::unbounded_channel();
        let transaction_certifier = TransactionCertifier::new(
            context.clone(),
            Arc::new(NoopBlockVerifier {}),
            dag_state.clone(),
            blocks_sender,
        );
        let (signals, signal_receivers) = CoreSignals::new(context.clone());
        // Need at least one subscriber to the block broadcast channel.
        let block_receiver = signal_receivers.block_broadcast_receiver();

        let (commit_consumer, commit_output_receiver, blocks_output_receiver) =
            CommitConsumerArgs::new(0, 0, [0; 32], 0);
        let commit_observer = CommitObserver::new(
            context.clone(),
            commit_consumer,
            dag_state.clone(),
            dag_state_writer.clone(),
            transaction_certifier.clone(),
            leader_schedule.clone(),
            0,
        )
        .await;

        let block_signer = signers.remove(own_index.value()).1;

        let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

        let core = Core::new(
            context,
            leader_schedule,
            transaction_consumer,
            transaction_certifier.clone(),
            block_manager,
            commit_observer,
            signals,
            block_signer,
            dag_state.clone(),
            dag_state_writer,
            sync_last_known_own_block,
            round_tracker,
            None,
            None,
            crate::coordination_hub::ConsensusCoordinationHub::new_for_testing(),
        );

        Self {
            core,
            transaction_certifier,
            signal_receivers,
            block_receiver,
            _commit_output_receiver: commit_output_receiver,
            _blocks_output_receiver: blocks_output_receiver,
            dag_state,
            store,
        }
    }

    pub(crate) fn add_blocks(
        &mut self,
        blocks: Vec<VerifiedBlock>,
    ) -> ConsensusResult<BTreeSet<BlockRef>> {
        self.transaction_certifier
            .add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
        self.core.add_blocks(blocks)
    }
}

use std::time::Duration;
use tokio::sync::watch;

use crate::commit::CertifiedCommits;
use consensus_config::Parameters;
use consensus_types::block::{BlockTimestampMs, TransactionIndex};
use futures::{stream::FuturesUnordered, StreamExt};
use meta_protocol_config::ProtocolConfig;
use tokio::sync::mpsc;
use std::iter;
use tokio::time::sleep;

use crate::{
    block::{genesis_blocks, BlockAPI, TestBlock, GENESIS_ROUND},
    commit::CommitAPI,
    leader_scoring::ReputationScores,
    storage::{Store, WriteBatch},
    test_dag_builder::DagBuilder,
    test_dag_parser::parse_dag,
    transaction::BlockStatus,
    CommitIndex,
};
use consensus_types::block::BlockRef;

/// Recover Core and continue proposing from the last round which forms a quorum.

#[cfg(test)]
mod recovery;
#[cfg(test)]
mod proposal;
#[cfg(test)]
mod commits;
#[cfg(test)]
mod ancestors;
