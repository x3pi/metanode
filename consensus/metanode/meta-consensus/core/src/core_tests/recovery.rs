use super::*;

#[tokio::test]
async fn test_core_recover_from_store_for_full_round() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (context, mut key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
    let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());
    let mut block_status_subscriptions = FuturesUnordered::new();

    // Create test blocks for all the authorities for 4 rounds and populate them in store
    let mut last_round_blocks = genesis_blocks(&context);
    let mut all_blocks: Vec<VerifiedBlock> = last_round_blocks.clone();
    for round in 1..=4 {
        let mut this_round_blocks = Vec::new();
        for (index, _authority) in context.committee.authorities() {
            let block = VerifiedBlock::new_for_test(
                TestBlock::new(round, index.value() as u32)
                    .set_ancestors(last_round_blocks.iter().map(|b| b.reference()).collect())
                    .build(),
            );

            // If it's round 1, that one will be committed later on, and it's our "own" block, then subscribe to listen for the block status.
            if round == 1 && index == context.own_index {
                let subscription =
                    transaction_consumer.subscribe_for_block_status_testing(block.reference());
                block_status_subscriptions.push(subscription);
            }

            this_round_blocks.push(block);
        }
        all_blocks.extend(this_round_blocks.clone());
        last_round_blocks = this_round_blocks;
    }
    // write them in store
    store
        .write(WriteBatch::default().blocks(all_blocks))
        .expect("Storage error");

    // create dag state after all blocks have been written to store
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));
    let (blocks_sender, _blocks_receiver) =
        monitored_mpsc::unbounded_channel("consensus_block_output");
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );

    let (commit_consumer, _commit_receiver, _transaction_receiver) = CommitConsumerArgs::new(0, 0, [0; 32], 0);
    let commit_observer = CommitObserver::new(
        context.clone(),
        commit_consumer,
        dag_state.clone(),
        transaction_certifier.clone(),
        leader_schedule.clone(),
        0,
    )
    .await;

    // Check no commits have been persisted to dag_state or store.
    let last_commit = store.read_last_commit().unwrap();
    assert!(last_commit.is_none());
    assert_eq!(dag_state.read().last_commit_index(), 0);

    // Now spin up core
    let (signals, signal_receivers) = CoreSignals::new(context.clone());
    let (blocks_sender, _blocks_receiver) =
        monitored_mpsc::unbounded_channel("consensus_block_output");
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );
    transaction_certifier.recover_blocks_after_round(dag_state.read().gc_round());
    // Need at least one subscriber to the block broadcast channel.
    let mut block_receiver = signal_receivers.block_broadcast_receiver();
    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

    let _core = Core::new(
        context.clone(),
        leader_schedule,
        transaction_consumer,
        transaction_certifier.clone(),
        block_manager,
        commit_observer,
        signals,
        key_pairs.remove(context.own_index.value()).1,
        dag_state.clone(),
        dag_state_writer,
        false,
        round_tracker,
        None,
        None,
        crate::coordination_hub::ConsensusCoordinationHub::new_for_testing(), // quorum_ready - always ready in tests
    );

    // New round should be 5
    let mut new_round = signal_receivers.new_round_receiver();
    assert_eq!(*new_round.borrow_and_update(), 5);

    // Block for round 5 should have been proposed.
    let proposed_block = block_receiver
        .recv()
        .await
        .expect("A block should have been created");
    assert_eq!(proposed_block.block.round(), 5);
    let ancestors = proposed_block.block.ancestors();

    // Only ancestors of round 4 should be included.
    assert_eq!(ancestors.len(), 4);
    for ancestor in ancestors {
        assert_eq!(ancestor.round, 4);
    }

    // Flush the DAG state to storage.
    dag_state.write().flush();

    // There were no commits prior to the core starting up but there was completed
    // rounds up to and including round 4. So we should commit leaders in round 1 & 2
    // as soon as the new block for round 5 is proposed.
    let last_commit = store
        .read_last_commit()
        .unwrap()
        .expect("last commit should be set");
    assert_eq!(last_commit.index(), 2);
    assert_eq!(dag_state.read().last_commit_index(), 2);
    let all_stored_commits = store.scan_commits((0..=CommitIndex::MAX).into()).unwrap();
    assert_eq!(all_stored_commits.len(), 2);

    // And ensure that our "own" block 1 sent to TransactionConsumer as notification alongside with gc_round
    while let Some(result) = block_status_subscriptions.next().await {
        let status = result.unwrap();
        assert!(matches!(status, BlockStatus::Sequenced(_)));
    }
}

/// Recover Core and continue proposing when having a partial last round which doesn't form a quorum and we haven't
/// proposed for that round yet.
#[tokio::test]
async fn test_core_recover_from_store_for_partial_round() {
    // // // // // // telemetry_subscribers::init_for_testing();

    let (context, mut key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
    let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());

    // Create test blocks for all authorities except our's (index = 0).
    let mut last_round_blocks = genesis_blocks(&context);
    let mut all_blocks = last_round_blocks.clone();
    for round in 1..=4 {
        let mut this_round_blocks = Vec::new();

        // For round 4 only produce f+1 blocks. Skip our validator 0 and that of position 1 from creating blocks.
        let authorities_to_skip = if round == 4 {
            context.committee.validity_threshold() as usize
        } else {
            // otherwise always skip creating a block for our authority
            1
        };

        for (index, _authority) in context.committee.authorities().skip(authorities_to_skip) {
            let block = TestBlock::new(round, index.value() as u32)
                .set_ancestors(last_round_blocks.iter().map(|b| b.reference()).collect())
                .build();
            this_round_blocks.push(VerifiedBlock::new_for_test(block));
        }
        all_blocks.extend(this_round_blocks.clone());
        last_round_blocks = this_round_blocks;
    }

    // write them in store
    store
        .write(WriteBatch::default().blocks(all_blocks))
        .expect("Storage error");

    // create dag state after all blocks have been written to store
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));
    let (blocks_sender, _blocks_receiver) =
        monitored_mpsc::unbounded_channel("consensus_block_output");
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );

    let (commit_consumer, _commit_receiver, _transaction_receiver) = CommitConsumerArgs::new(0, 0, [0; 32], 0);
    let commit_observer = CommitObserver::new(
        context.clone(),
        commit_consumer,
        dag_state.clone(),
        transaction_certifier.clone(),
        leader_schedule.clone(),
        0,
    )
    .await;

    // Check no commits have been persisted to dag_state & store
    let last_commit = store.read_last_commit().unwrap();
    assert!(last_commit.is_none());
    assert_eq!(dag_state.read().last_commit_index(), 0);

    // Now spin up core
    let (signals, signal_receivers) = CoreSignals::new(context.clone());
    let (blocks_sender, _blocks_receiver) =
        monitored_mpsc::unbounded_channel("consensus_block_output");
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );
    transaction_certifier.recover_blocks_after_round(dag_state.read().gc_round());
    // Need at least one subscriber to the block broadcast channel.
    let mut block_receiver = signal_receivers.block_broadcast_receiver();
    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

    let mut core = Core::new(
        context.clone(),
        leader_schedule,
        transaction_consumer,
        transaction_certifier,
        block_manager,
        commit_observer,
        signals,
        key_pairs.remove(context.own_index.value()).1,
        dag_state.clone(),
        dag_state_writer,
        false,
        round_tracker,
        None,
        None,
        crate::coordination_hub::ConsensusCoordinationHub::new_for_testing(), // quorum_ready - always ready in tests
    );

    // Clock round should have advanced to 5 during recovery because
    // a quorum has formed in round 4.
    let mut new_round = signal_receivers.new_round_receiver();
    assert_eq!(*new_round.borrow_and_update(), 5);

    // During recovery, round 4 block should have been proposed.
    let proposed_block = block_receiver
        .recv()
        .await
        .expect("A block should have been created");
    assert_eq!(proposed_block.block.round(), 4);
    let ancestors = proposed_block.block.ancestors();

    assert_eq!(ancestors.len(), 4);
    for ancestor in ancestors {
        if ancestor.author == context.own_index {
            assert_eq!(ancestor.round, 0);
        } else {
            assert_eq!(ancestor.round, 3);
        }
    }

    // Run commit rule.
    core.try_commit(vec![]).ok();

    // Flush the DAG state to storage.
    core.dag_state.write().flush();

    // There were no commits prior to the core starting up but there was completed
    // rounds up to round 4. So we should commit leaders in round 1 & 2 as soon
    // as the new block for round 4 is proposed.
    let last_commit = store
        .read_last_commit()
        .unwrap()
        .expect("last commit should be set");
    assert_eq!(last_commit.index(), 2);
    assert_eq!(dag_state.read().last_commit_index(), 2);
    let all_stored_commits = store.scan_commits((0..=CommitIndex::MAX).into()).unwrap();
    assert_eq!(all_stored_commits.len(), 2);
}

