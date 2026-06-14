// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::{BTreeMap, BTreeSet},
    sync::Arc,
    time::Duration,
};

use async_trait::async_trait;
use bytes::Bytes;
use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockDigest, BlockRef, Round};
use tokio::sync::mpsc;
use parking_lot::{Mutex, RwLock};
use tokio::{sync::broadcast, time::sleep};

use crate::{
    authority_service::AuthorityService,
    block::{BlockAPI, SignedBlock, TestBlock, VerifiedBlock},
    commit::{CertifiedCommits, CommitRange},
    commit_vote_monitor::CommitVoteMonitor,
    context::Context,
    core_thread::{CoreError, CoreThreadDispatcher},
    dag_state::DagState,
    error::ConsensusResult,
    network::{BlockStream, ExtendedSerializedBlock, NetworkClient, NetworkService},
    round_tracker::PeerRoundTracker,
    storage::mem_store::MemStore,
    synchronizer::Synchronizer,
    test_dag_builder::DagBuilder,
    transaction_certifier::TransactionCertifier,
};
struct FakeCoreThreadDispatcher {
    blocks: Mutex<Vec<VerifiedBlock>>,
}

impl FakeCoreThreadDispatcher {
    fn new() -> Self {
        Self {
            blocks: Mutex::new(vec![]),
        }
    }

    fn get_blocks(&self) -> Vec<VerifiedBlock> {
        self.blocks.lock().clone()
    }
}

#[async_trait]
impl CoreThreadDispatcher for FakeCoreThreadDispatcher {
    async fn add_blocks(
        &self,
        blocks: Vec<VerifiedBlock>,
    ) -> Result<BTreeSet<BlockRef>, CoreError> {
        let block_refs = blocks.iter().map(|b| b.reference()).collect();
        self.blocks.lock().extend(blocks);
        Ok(block_refs)
    }

    async fn check_block_refs(
        &self,
        _block_refs: Vec<BlockRef>,
    ) -> Result<BTreeSet<BlockRef>, CoreError> {
        Ok(BTreeSet::new())
    }

    async fn add_certified_commits(
        &self,
        _commits: CertifiedCommits,
    ) -> Result<BTreeSet<BlockRef>, CoreError> {
        todo!()
    }

    async fn new_block(&self, _round: Round, _force: bool) -> Result<(), CoreError> {
        Ok(())
    }

    async fn get_missing_blocks(&self) -> Result<BTreeSet<BlockRef>, CoreError> {
        Ok(Default::default())
    }

    fn set_propagation_delay(&self, _propagation_delay: Round) -> Result<(), CoreError> {
        todo!()
    }

    fn set_last_known_proposed_round(&self, _round: Round) -> Result<(), CoreError> {
        todo!()
    }

    fn highest_received_rounds(&self) -> Vec<Round> {
        todo!()
    }
}

#[derive(Default)]
struct FakeNetworkClient {}

#[async_trait]
impl NetworkClient for FakeNetworkClient {
    async fn send_block(
        &self,
        _peer: AuthorityIndex,
        _block: &VerifiedBlock,
        _timeout: Duration,
    ) -> ConsensusResult<()> {
        unimplemented!("Unimplemented")
    }

    async fn subscribe_blocks(
        &self,
        _peer: AuthorityIndex,
        _last_received: Round,
        _timeout: Duration,
    ) -> ConsensusResult<BlockStream> {
        unimplemented!("Unimplemented")
    }

    async fn fetch_blocks(
        &self,
        _peer: AuthorityIndex,
        _block_refs: Vec<BlockRef>,
        _highest_accepted_rounds: Vec<Round>,
        _breadth_first: bool,
        _timeout: Duration,
    ) -> ConsensusResult<Vec<Bytes>> {
        unimplemented!("Unimplemented")
    }

    async fn fetch_commits(
        &self,
        _peer: AuthorityIndex,
        _commit_range: CommitRange,
        _timeout: Duration,
    ) -> ConsensusResult<(Vec<Bytes>, Vec<Bytes>, Vec<Bytes>)> {
        unimplemented!("Unimplemented")
    }

    async fn fetch_commits_by_global_range(
        &self,
        _peer: AuthorityIndex,
        _start_global_index: u64,
        _max_global_index: u64,
        _timeout: Duration,
    ) -> ConsensusResult<Vec<crate::network::tonic_network::GlobalCommitInfo>> {
        unimplemented!("Unimplemented")
    }

    async fn send_epoch_change_proposal(
        &self,
        _peer: AuthorityIndex,
        _proposal: &crate::epoch_change::EpochChangeProposal,
        _timeout: Duration,
    ) -> ConsensusResult<()> {
        unimplemented!("Unimplemented")
    }

    async fn send_epoch_change_vote(
        &self,
        _peer: AuthorityIndex,
        _vote: &crate::epoch_change::EpochChangeVote,
        _timeout: Duration,
    ) -> ConsensusResult<()> {
        unimplemented!("Unimplemented")
    }

    async fn fetch_latest_blocks(
        &self,
        _peer: AuthorityIndex,
        _authorities: Vec<AuthorityIndex>,
        _timeout: Duration,
    ) -> ConsensusResult<Vec<Bytes>> {
        unimplemented!("Unimplemented")
    }

    async fn get_latest_rounds(
        &self,
        _peer: AuthorityIndex,
        _timeout: Duration,
    ) -> ConsensusResult<(Vec<Round>, Vec<Round>)> {
        unimplemented!("Unimplemented")
    }
}

#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_handle_send_block() {
    let (context, _keys) = Context::new_for_test(4);
    let context = Arc::new(context);
    let block_verifier = Arc::new(crate::block_verifier::NoopBlockVerifier {});
    let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
    let core_dispatcher = Arc::new(FakeCoreThreadDispatcher::new());
    let (_tx_block_broadcast, rx_block_broadcast) = broadcast::channel(100);
    let network_client = Arc::new(FakeNetworkClient::default());
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        block_verifier.clone(),
        dag_state.clone(),
        blocks_sender,
    );
    let synchronizer = Synchronizer::start(
        network_client,
        context.clone(),
        core_dispatcher.clone(),
        commit_vote_monitor.clone(),
        block_verifier.clone(),
        transaction_certifier.clone(),
        dag_state.clone(),
        false,
    );
    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));
    let authority_service = Arc::new(AuthorityService::new(
        context.clone(),
        block_verifier,
        commit_vote_monitor,
        round_tracker,
        synchronizer,
        core_dispatcher.clone(),
        rx_block_broadcast,
        transaction_certifier,
        dag_state,
        store,
        None,
        None, // legacy_store_manager
        0,    // epoch_base_index (tests start at epoch 0)
    ));

    // Test delaying blocks with time drift.
    let now = context.clock.timestamp_utc_ms();
    let max_drift = context.parameters.max_forward_time_drift;
    let input_block = VerifiedBlock::new_for_test(
        TestBlock::new(9, 0)
            .set_timestamp_ms(now + max_drift.as_millis() as u64)
            .build(),
    );

    let service = authority_service.clone();
    let serialized = ExtendedSerializedBlock {
        block: input_block.serialized().clone(),
        excluded_ancestors: vec![],
    };

    tokio::spawn({
        let service = service.clone();
        let context = context.clone();
        async move {
            service
                .handle_send_block(context.committee.to_authority_index(0).unwrap(), serialized)
                .await
                .unwrap();
        }
    });

    sleep(max_drift / 2).await;

    let blocks = core_dispatcher.get_blocks();
    assert_eq!(blocks.len(), 1);
    assert_eq!(blocks[0], input_block);

    // Test invalid block.
    let invalid_block =
        VerifiedBlock::new_for_test(TestBlock::new(10, 1000).set_timestamp_ms(10).build());
    let extended_block = ExtendedSerializedBlock {
        block: invalid_block.serialized().clone(),
        excluded_ancestors: vec![],
    };
    service
        .handle_send_block(
            context.committee.to_authority_index(0).unwrap(),
            extended_block,
        )
        .await
        .unwrap_err();

    // Test invalid excluded ancestors.
    let invalid_excluded_ancestors = vec![
        bcs::to_bytes(&BlockRef::new(
            10,
            AuthorityIndex::new_for_test(1000),
            BlockDigest::MIN,
        ))
        .unwrap(),
        vec![3u8; 40],
        bcs::to_bytes(&invalid_block.reference()).unwrap(),
    ];
    let extended_block = ExtendedSerializedBlock {
        block: input_block.serialized().clone(),
        excluded_ancestors: invalid_excluded_ancestors,
    };
    service
        .handle_send_block(
            context.committee.to_authority_index(0).unwrap(),
            extended_block,
        )
        .await
        .unwrap_err();
}

#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_handle_fetch_blocks() {
    // GIVEN
    // Use NUM_AUTHORITIES and NUM_ROUNDS higher than max_blocks_per_sync to test limits.
    const NUM_AUTHORITIES: usize = 40;
    const NUM_ROUNDS: usize = 40;
    let (context, _keys) = Context::new_for_test(NUM_AUTHORITIES);
    let context = Arc::new(context);
    let block_verifier = Arc::new(crate::block_verifier::NoopBlockVerifier {});
    let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
    let core_dispatcher = Arc::new(FakeCoreThreadDispatcher::new());
    let (_tx_block_broadcast, rx_block_broadcast) = broadcast::channel(100);
    let network_client = Arc::new(FakeNetworkClient::default());
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        block_verifier.clone(),
        dag_state.clone(),
        blocks_sender,
    );
    let synchronizer = Synchronizer::start(
        network_client,
        context.clone(),
        core_dispatcher.clone(),
        commit_vote_monitor.clone(),
        block_verifier.clone(),
        transaction_certifier.clone(),
        dag_state.clone(),
        false,
    );
    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));
    let authority_service = Arc::new(AuthorityService::new(
        context.clone(),
        block_verifier,
        commit_vote_monitor,
        round_tracker,
        synchronizer,
        core_dispatcher.clone(),
        rx_block_broadcast,
        transaction_certifier,
        dag_state.clone(),
        store,
        None,
        None, // legacy_store_manager
        0,    // epoch_base_index (tests start at epoch 0)
    ));

    // GIVEN: 40 rounds of blocks in the dag state.
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder
        .layers(1..=(NUM_ROUNDS as u32))
        .build()
        .persist_layers(dag_state.clone());
    let all_blocks = dag_builder.all_blocks();

    // WHEN: Request 2 blocks from round 40, get ancestors breadth first.
    let missing_block_refs: Vec<BlockRef> = all_blocks
        .iter()
        .rev()
        .take(2)
        .map(|b| b.reference())
        .collect();
    let highest_accepted_rounds: Vec<Round> = vec![1; NUM_AUTHORITIES];
    let results = authority_service
        .handle_fetch_blocks(
            AuthorityIndex::new_for_test(0),
            missing_block_refs.clone(),
            highest_accepted_rounds,
            true,
        )
        .await
        .unwrap();

    // THEN: the expected number of unique blocks are returned.
    let blocks: BTreeMap<BlockRef, VerifiedBlock> = results
        .iter()
        .map(|b| {
            let signed = bcs::from_bytes(b).unwrap();
            let block = VerifiedBlock::new_verified(signed, b.clone());
            (block.reference(), block)
        })
        .collect();
    assert_eq!(blocks.len(), context.parameters.max_blocks_per_sync);
    // All missing blocks are returned.
    for b in &missing_block_refs {
        assert!(blocks.contains_key(b));
    }
    let num_missing_ancestors = blocks
        .keys()
        .filter(|b| b.round == NUM_ROUNDS as Round - 1)
        .count();
    assert_eq!(
        num_missing_ancestors,
        context.parameters.max_blocks_per_sync - missing_block_refs.len()
    );

    // WHEN: Request 2 blocks from round 37, get ancestors depth first.
    let missing_round = NUM_ROUNDS as Round - 3;
    let missing_block_refs: Vec<BlockRef> = all_blocks
        .iter()
        .filter(|b| b.reference().round == missing_round)
        .map(|b| b.reference())
        .take(2)
        .collect();
    let mut highest_accepted_rounds: Vec<Round> = vec![1; NUM_AUTHORITIES];
    // Try to fill up the blocks from the 1st authority in missing_block_refs.
    highest_accepted_rounds[missing_block_refs[0].author] = missing_round - 5;
    let results = authority_service
        .handle_fetch_blocks(
            AuthorityIndex::new_for_test(0),
            missing_block_refs.clone(),
            highest_accepted_rounds,
            false,
        )
        .await
        .unwrap();

    // THEN: the expected number of unique blocks are returned.
    let blocks: BTreeMap<BlockRef, VerifiedBlock> = results
        .iter()
        .map(|b| {
            let signed = bcs::from_bytes(b).unwrap();
            let block = VerifiedBlock::new_verified(signed, b.clone());
            (block.reference(), block)
        })
        .collect();
    assert_eq!(blocks.len(), context.parameters.max_blocks_per_sync);
    // All missing blocks are returned.
    for b in &missing_block_refs {
        assert!(blocks.contains_key(b));
    }
    // Ancestor blocks are from the expected rounds and authorities.
    let expected_authors = [missing_block_refs[0].author, missing_block_refs[1].author];
    for b in blocks.keys() {
        assert!(b.round <= missing_round);
        assert!(expected_authors.contains(&b.author));
    }

    // WHEN: Request 5 block from round 40, not getting ancestors.
    let missing_block_refs: Vec<BlockRef> = all_blocks
        .iter()
        .filter(|b| b.reference().round == NUM_ROUNDS as Round - 10)
        .map(|b| b.reference())
        .take(5)
        .collect();
    let results = authority_service
        .handle_fetch_blocks(
            AuthorityIndex::new_for_test(0),
            missing_block_refs.clone(),
            vec![],
            false,
        )
        .await
        .unwrap();

    // THEN: the expected number of unique blocks are returned.
    let blocks: BTreeMap<BlockRef, VerifiedBlock> = results
        .iter()
        .map(|b| {
            let signed = bcs::from_bytes(b).unwrap();
            let block = VerifiedBlock::new_verified(signed, b.clone());
            (block.reference(), block)
        })
        .collect();
    assert_eq!(blocks.len(), 5);
    for b in &missing_block_refs {
        assert!(blocks.contains_key(b));
    }
}

#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_handle_fetch_latest_blocks() {
    // GIVEN
    let (context, _keys) = Context::new_for_test(4);
    let context = Arc::new(context);
    let block_verifier = Arc::new(crate::block_verifier::NoopBlockVerifier {});
    let commit_vote_monitor = Arc::new(CommitVoteMonitor::new(context.clone()));
    let core_dispatcher = Arc::new(FakeCoreThreadDispatcher::new());
    let (_tx_block_broadcast, rx_block_broadcast) = broadcast::channel(100);
    let network_client = Arc::new(FakeNetworkClient::default());
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        block_verifier.clone(),
        dag_state.clone(),
        blocks_sender,
    );
    let synchronizer = Synchronizer::start(
        network_client,
        context.clone(),
        core_dispatcher.clone(),
        commit_vote_monitor.clone(),
        block_verifier.clone(),
        transaction_certifier.clone(),
        dag_state.clone(),
        true,
    );
    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));
    let authority_service = Arc::new(AuthorityService::new(
        context.clone(),
        block_verifier,
        commit_vote_monitor,
        round_tracker,
        synchronizer,
        core_dispatcher.clone(),
        rx_block_broadcast,
        transaction_certifier,
        dag_state.clone(),
        store,
        None,
        None, // legacy_store_manager
        0,    // epoch_base_index (tests start at epoch 0)
    ));

    // Create some blocks for a few authorities. Create some equivocations as well and store in dag state.
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder
        .layers(1..=10)
        .authorities(vec![AuthorityIndex::new_for_test(2)])
        .equivocate(1)
        .build()
        .persist_layers(dag_state);

    // WHEN
    let authorities_to_request = vec![
        AuthorityIndex::new_for_test(1),
        AuthorityIndex::new_for_test(2),
    ];
    let results = authority_service
        .handle_fetch_latest_blocks(AuthorityIndex::new_for_test(1), authorities_to_request)
        .await;

    // THEN
    let serialised_blocks = results.unwrap();
    for serialised_block in serialised_blocks {
        let signed_block: SignedBlock =
            bcs::from_bytes(&serialised_block).expect("Error while deserialising block");
        let verified_block = VerifiedBlock::new_verified(signed_block, serialised_block);

        assert_eq!(verified_block.round(), 10);
    }
}

