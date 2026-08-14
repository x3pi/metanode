use super::*;

#[tokio::test]
async fn test_core_propose_after_genesis() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let _guard = ProtocolConfig::apply_overrides_for_testing(|_, mut config| {
        config.set_consensus_max_transaction_size_bytes_for_testing(2_000);
        config.set_consensus_max_transactions_in_block_bytes_for_testing(2_000);
        config
    });

    let (context, mut key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
    let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());

    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let (transaction_client, tx_receiver) = TransactionClient::new(context.clone());
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
    let mut block_receiver = signal_receivers.block_broadcast_receiver();
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));

    let (commit_consumer, _commit_receiver, _transaction_receiver) = CommitConsumerArgs::new(0, 0, [0; 32], 0);
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

    // Send some transactions
    let mut total = 0;
    let mut index = 0;
    loop {
        let transaction = bcs::to_bytes(&format!("Transaction {index}")).expect("Shouldn't fail");
        total += transaction.len();
        index += 1;
        let _w = transaction_client
            .submit_no_wait(vec![transaction])
            .await
            .unwrap();

        // Create total size of transactions up to 1KB
        if total >= 1_000 {
            break;
        }
    }

    // a new block should have been created during recovery.
    let extended_block = block_receiver
        .recv()
        .await
        .expect("A new block should have been created");

    // A new block created - assert the details
    assert_eq!(extended_block.block.round(), 1);
    assert_eq!(extended_block.block.author().value(), 0);
    assert_eq!(extended_block.block.ancestors().len(), 4);

    let mut total = 0;
    for (i, transaction) in extended_block.block.transactions().iter().enumerate() {
        total += transaction.data().len() as u64;
        let transaction: String = bcs::from_bytes(transaction.data()).unwrap();
        assert_eq!(format!("Transaction {i}"), transaction);
    }
    assert!(total <= context.protocol_config.max_transactions_in_block_bytes());

    // genesis blocks should be referenced
    let all_genesis = genesis_blocks(&context);

    for ancestor in extended_block.block.ancestors() {
        all_genesis
            .iter()
            .find(|block| block.reference() == *ancestor)
            .expect("Block should be found amongst genesis blocks");
    }

    // Try to propose again - with or without ignore leaders check, it will not return any block
    assert!(core.try_propose(false).unwrap().is_none());
    assert!(core.try_propose(true).unwrap().is_none());

    // Flush the DAG state to storage.
    dag_state.write().flush();

    // Check no commits have been persisted to dag_state & store
    let last_commit = store.read_last_commit().unwrap();
    assert!(last_commit.is_none());
    assert_eq!(dag_state.read().last_commit_index(), 0);
}

#[tokio::test]
async fn test_core_propose_once_receiving_a_quorum() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (context, _key_pairs) = Context::new_for_test(4);
    let mut core_fixture = CoreTextFixture::new(
        context.clone(),
        vec![1, 1, 1, 1],
        AuthorityIndex::new_for_test(0),
        false,
    )
    .await;
    let transaction_certifier = &core_fixture.transaction_certifier;
    let store = &core_fixture.store;
    let dag_state = &core_fixture.dag_state;
    let core = &mut core_fixture.core;

    let mut expected_ancestors = BTreeSet::new();

    // Adding one block now will trigger the creation of new block for round 1
    let block_1 = VerifiedBlock::new_for_test(TestBlock::new(1, 1).build());
    expected_ancestors.insert(block_1.reference());
    // Wait for min round delay to allow blocks to be proposed. Core also enforces a
    // MIN_PROPOSAL_AGGREGATION_DELAY floor (see core/proposer.rs::try_new_block) even when
    // `min_round_delay` is configured lower, so wait for whichever is longer.
    sleep(context.parameters.min_round_delay.max(crate::core::MIN_PROPOSAL_AGGREGATION_DELAY)).await;
    // add blocks to trigger proposal.
    transaction_certifier.add_voted_blocks(vec![(block_1.clone(), vec![])]);
    _ = core.add_blocks(vec![block_1]);

    assert_eq!(core.last_proposed_round(), 1);
    expected_ancestors.insert(core.last_proposed_block().reference());
    // attempt to create a block - none will be produced.
    assert!(core.try_propose(false).unwrap().is_none());

    // Adding another block now forms a quorum for round 1, so block at round 2 will proposed
    let block_2 = VerifiedBlock::new_for_test(TestBlock::new(1, 2).build());
    expected_ancestors.insert(block_2.reference());
    // Wait for min round delay to allow blocks to be proposed. Core also enforces a
    // MIN_PROPOSAL_AGGREGATION_DELAY floor (see core/proposer.rs::try_new_block) even when
    // `min_round_delay` is configured lower, so wait for whichever is longer.
    sleep(context.parameters.min_round_delay.max(crate::core::MIN_PROPOSAL_AGGREGATION_DELAY)).await;
    // add blocks to trigger proposal.
    transaction_certifier.add_voted_blocks(vec![(block_2.clone(), vec![1, 4])]);
    _ = core.add_blocks(vec![block_2.clone()]);

    assert_eq!(core.last_proposed_round(), 2);

    let proposed_block = core.last_proposed_block();
    assert_eq!(proposed_block.round(), 2);
    assert_eq!(proposed_block.author(), context.own_index);
    assert_eq!(proposed_block.ancestors().len(), 3);
    let ancestors = proposed_block.ancestors();
    let ancestors = ancestors.iter().cloned().collect::<BTreeSet<_>>();
    assert_eq!(ancestors, expected_ancestors);

    let transaction_votes = proposed_block.transaction_votes();
    assert_eq!(transaction_votes.len(), 1);
    let transaction_vote = transaction_votes.first().unwrap();
    assert_eq!(transaction_vote.block_ref, block_2.reference());
    assert_eq!(transaction_vote.rejects, vec![1, 4]);

    // Flush the DAG state to storage.
    dag_state.write().flush();

    // Check no commits have been persisted to dag_state & store
    let last_commit = store.read_last_commit().unwrap();
    assert!(last_commit.is_none());
    assert_eq!(dag_state.read().last_commit_index(), 0);
}


#[tokio::test]
async fn test_core_set_min_propose_round() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (context, mut key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context.with_parameters(Parameters {
        sync_last_known_own_block_timeout: Duration::from_millis(2_000),
        ..Default::default()
    }));

    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
    let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());

    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));

    let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
    let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());
    let (signals, signal_receivers) = CoreSignals::new(context.clone());
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );
    // Need at least one subscriber to the block broadcast channel.
    let _block_receiver = signal_receivers.block_broadcast_receiver();

    let (commit_consumer, _commit_receiver, _transaction_receiver) = CommitConsumerArgs::new(0, 0, [0; 32], 0);
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

    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

    let mut core = Core::new(
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
        true,
        round_tracker,
        None,
        None,
        crate::coordination_hub::ConsensusCoordinationHub::new_for_testing(), // quorum_ready - always ready in tests
    );

    // No new block should have been produced
    assert_eq!(
        core.last_proposed_round(),
        GENESIS_ROUND,
        "No block should have been created other than genesis"
    );

    // Trying to explicitly propose a block will not produce anything
    assert!(core.try_propose(true).unwrap().is_none());

    // Create blocks for the whole network - even "our" node in order to replicate an "amnesia" recovery.
    let mut builder = DagBuilder::new(context.clone());
    builder.layers(1..=10).build();

    let blocks = builder.blocks.values().cloned().collect::<Vec<_>>();

    // Process all the blocks
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());

    core.round_tracker.write().update_from_probe(
        vec![
            vec![10, 10, 10, 10],
            vec![10, 10, 10, 10],
            vec![10, 10, 10, 10],
            vec![10, 10, 10, 10],
        ],
        vec![
            vec![10, 10, 10, 10],
            vec![10, 10, 10, 10],
            vec![10, 10, 10, 10],
            vec![10, 10, 10, 10],
        ],
    );

    // Try to propose - no block should be produced.
    assert!(core.try_propose(true).unwrap().is_none());

    // Now set the last known proposed round which is the highest round for which the network informed
    // us that we do have proposed a block about.
    core.set_last_known_proposed_round(10);

    let block = core.try_propose(true).expect("No error").unwrap();
    assert_eq!(block.round(), 11);
    assert_eq!(block.ancestors().len(), 4);

    let our_ancestor_included = block.ancestors()[0];
    assert_eq!(our_ancestor_included.author, context.own_index);
    assert_eq!(our_ancestor_included.round, 10);
}

/// Polls `store` for a persisted commit at or above `expected_index`, giving real (non-virtual)
/// wall-clock time to any in-flight `DagState::flush()` write between attempts.
///
/// `Core::try_new_block()` calls `dag_state.write().flush()` internally and hands the resulting
/// ticket off to an async task (the block broadcaster) that awaits it before sending — but that
/// ticket is never surfaced to callers, so tests have no handle to await it directly. A test-level
/// `dag_state.write().flush()` called afterward reliably finds nothing pending (the internal call
/// already drained the write queues) and returns `None`, so there is nothing to await — checking
/// the store immediately races the still in-flight `spawn_blocking` write. Under
/// `start_paused = true`, `tokio::time::sleep` doesn't reliably help either: the paused clock can
/// auto-advance virtual timers near-instantly without giving the OS any real time to run the
/// write thread. `spawn_blocking` + `std::thread::sleep` forces a genuine (small) wall-clock
/// delay, which is what's actually needed here.
async fn wait_for_commit_persisted(
    store: &Arc<MemStore>,
    expected_index: CommitIndex,
) -> crate::commit::TrustedCommit {
    for _ in 0..500 {
        if let Ok(Some(commit)) = store.read_last_commit() {
            if commit.index() >= expected_index {
                return commit;
            }
        }
        tokio::task::spawn_blocking(|| std::thread::sleep(Duration::from_millis(1)))
            .await
            .unwrap();
    }
    panic!(
        "Timed out waiting for commit index {expected_index} to be persisted to store \
         (last seen: {:?})",
        store.read_last_commit()
    );
}

#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_core_try_new_block_leader_timeout() {
    // // // // // // telemetry_subscribers::init_for_testing();

    // Since we run the test with started_paused = true, any time-dependent operations using Tokio's time
    // facilities, such as tokio::time::sleep or tokio::time::Instant, will not advance. So practically each
    // Core's clock will have initialised potentially with different values but it never advances.
    // To ensure that blocks won't get rejected by cores we'll need to manually wait for the time
    // diff before processing them. By calling the `tokio::time::sleep` we implicitly also advance the
    // tokio clock.
    async fn wait_blocks(blocks: &[VerifiedBlock], context: &Context) {
        // Simulate the time wait before processing a block to ensure that block.timestamp <= now
        let now = context.clock.timestamp_utc_ms();
        let max_timestamp = blocks
            .iter()
            .max_by_key(|block| block.timestamp_ms() as BlockTimestampMs)
            .map(|block| block.timestamp_ms())
            .unwrap_or(0);

        let wait_time = Duration::from_millis(max_timestamp.saturating_sub(now));
        sleep(wait_time).await;
    }

    let (context, _) = Context::new_for_test(4);
    // Create the cores for all authorities
    let mut all_cores = create_cores(context, vec![1, 1, 1, 1]).await;

    // Create blocks for rounds 1..=3 from all Cores except last Core of authority 3, so we miss the block from it. As
    // it will be the leader of round 3 then no-one will be able to progress to round 4 unless we explicitly trigger
    // the block creation.
    // create the cores and their signals for all the authorities
    let (_last_core, cores) = all_cores.split_last_mut().unwrap();

    // Now iterate over a few rounds and ensure the corresponding signals are created while network advances
    let mut last_round_blocks = Vec::<VerifiedBlock>::new();
    for round in 1..=3 {
        let mut this_round_blocks = Vec::new();

        for core_fixture in cores.iter_mut() {
            wait_blocks(&last_round_blocks, &core_fixture.core.context).await;

            core_fixture.add_blocks(last_round_blocks.clone()).unwrap();

            // Only when round > 1 and using non-genesis parents.
            if let Some(r) = last_round_blocks.first().map(|b| b.round()) {
                assert_eq!(round - 1, r);
                if core_fixture.core.last_proposed_round() == r {
                    // Force propose new block regardless of min round delay.
                    core_fixture
                        .core
                        .try_propose(true)
                        .unwrap()
                        .unwrap_or_else(|| {
                            panic!("Block should have been proposed for round {}", round)
                        });
                }
            }

            assert_eq!(core_fixture.core.last_proposed_round(), round);

            this_round_blocks.push(core_fixture.core.last_proposed_block());
        }

        last_round_blocks = this_round_blocks;
    }

    // Try to create the blocks for round 4 by calling the try_propose() method. No block should be created as the
    // leader - authority 3 - hasn't proposed any block.
    for core_fixture in cores.iter_mut() {
        wait_blocks(&last_round_blocks, &core_fixture.core.context).await;

        core_fixture.add_blocks(last_round_blocks.clone()).unwrap();
        assert!(core_fixture.core.try_propose(false).unwrap().is_none());
    }

    // Now try to create the blocks for round 4 via the leader timeout method which should
    // ignore any leader checks or min round delay.
    for core_fixture in cores.iter_mut() {
        assert!(core_fixture.core.new_block(4, true).unwrap().is_some());
        assert_eq!(core_fixture.core.last_proposed_round(), 4);

        // NOTE: try_new_block() (called internally by new_block() above) already ran its own
        // dag_state.write().flush() before returning — capturing the commit decided a moment
        // earlier via the add_blocks() call in the loop above, along with this round's blocks —
        // and handed that ticket off to an internal broadcast task we have no handle to. A flush()
        // called here would reliably find nothing pending (that internal call already drained
        // everything) and its ticket would be a no-op; awaiting it does NOT wait for the real
        // write. Poll the store instead, so this doesn't race the still in-flight spawn_blocking
        // write.
        let last_commit = wait_for_commit_persisted(&core_fixture.store, 1).await;
        // There are 1 leader rounds with rounds completed up to and including
        // round 4
        assert_eq!(last_commit.index(), 1);
        let all_stored_commits = core_fixture
            .store
            .scan_commits((0..=CommitIndex::MAX).into())
            .unwrap();
        assert_eq!(all_stored_commits.len(), 1);
    }
}

#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_core_try_new_block_with_leader_timeout_and_low_scoring_authority() {
    // // // // // // telemetry_subscribers::init_for_testing();

    // Since we run the test with started_paused = true, any time-dependent operations using Tokio's time
    // facilities, such as tokio::time::sleep or tokio::time::Instant, will not advance. So practically each
    // Core's clock will have initialised potentially with different values but it never advances.
    // To ensure that blocks won't get rejected by cores we'll need to manually wait for the time
    // diff before processing them. By calling the `tokio::time::sleep` we implicitly also advance the
    // tokio clock.
    async fn wait_blocks(blocks: &[VerifiedBlock], context: &Context) {
        // Simulate the time wait before processing a block to ensure that block.timestamp <= now
        let now = context.clock.timestamp_utc_ms();
        let max_timestamp = blocks
            .iter()
            .max_by_key(|block| block.timestamp_ms() as BlockTimestampMs)
            .map(|block| block.timestamp_ms())
            .unwrap_or(0);

        let wait_time = Duration::from_millis(max_timestamp.saturating_sub(now));
        sleep(wait_time).await;
    }

    let (mut context, _) = Context::new_for_test(5);
    context
        .protocol_config
        .set_consensus_bad_nodes_stake_threshold_for_testing(33);

    // Create the cores for all authorities
    let mut all_cores = create_cores(context, vec![1, 1, 1, 1, 1]).await;
    let (_last_core, cores) = all_cores.split_last_mut().unwrap();

    // Create blocks for rounds 1..=30 from all Cores except last Core of authority 4.
    let mut last_round_blocks = Vec::<VerifiedBlock>::new();
    for round in 1..=30 {
        let mut this_round_blocks = Vec::new();

        for core_fixture in cores.iter_mut() {
            wait_blocks(&last_round_blocks, &core_fixture.core.context).await;

            core_fixture.add_blocks(last_round_blocks.clone()).unwrap();

            core_fixture.core.round_tracker.write().update_from_probe(
                vec![
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![0, 0, 0, 0, 0],
                ],
                vec![
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![0, 0, 0, 0, 0],
                ],
            );

            // Only when round > 1 and using non-genesis parents.
            if let Some(r) = last_round_blocks.first().map(|b| b.round()) {
                assert_eq!(round - 1, r);
                if core_fixture.core.last_proposed_round() == r {
                    // Force propose new block regardless of min round delay.
                    core_fixture
                        .core
                        .try_propose(true)
                        .unwrap()
                        .unwrap_or_else(|| {
                            panic!("Block should have been proposed for round {}", round)
                        });
                }
            }

            assert_eq!(core_fixture.core.last_proposed_round(), round);

            this_round_blocks.push(core_fixture.core.last_proposed_block().clone());
        }

        last_round_blocks = this_round_blocks;
    }

    // Now produce blocks for all Cores
    for round in 31..=40 {
        let mut this_round_blocks = Vec::new();

        for core_fixture in all_cores.iter_mut() {
            wait_blocks(&last_round_blocks, &core_fixture.core.context).await;

            core_fixture.add_blocks(last_round_blocks.clone()).unwrap();

            // Don't update probed rounds for authority 3 so it will remain
            // excluded
            core_fixture.core.round_tracker.write().update_from_probe(
                vec![
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![0, 0, 0, 0, 0],
                ],
                vec![
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![round, round, round, round, 0],
                    vec![0, 0, 0, 0, 0],
                ],
            );

            // Only when round > 1 and using non-genesis parents.
            if let Some(r) = last_round_blocks.first().map(|b| b.round()) {
                assert_eq!(round - 1, r);
                if core_fixture.core.last_proposed_round() == r {
                    // Force propose new block regardless of min round delay.
                    core_fixture
                        .core
                        .try_propose(true)
                        .unwrap()
                        .unwrap_or_else(|| {
                            panic!("Block should have been proposed for round {}", round)
                        });
                }
            }

            this_round_blocks.push(core_fixture.core.last_proposed_block().clone());

            for block in this_round_blocks.iter() {
                if block.author() != AuthorityIndex::new_for_test(4) {
                    // Assert blocks created include only 4 ancestors per block as one
                    // should be excluded
                    assert_eq!(block.ancestors().len(), 4);
                } else {
                    // Authority 3 is the low scoring authority so it will still include
                    // its own blocks.
                    assert_eq!(block.ancestors().len(), 5);
                }
            }
        }

        last_round_blocks = this_round_blocks;
    }
}


#[tokio::test]
async fn test_core_set_propagation_delay_per_authority() {
    // TODO: create helper to avoid the duplicated code here.
    // // // // // // telemetry_subscribers::init_for_testing();
    let (context, mut key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
    let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());

    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));

    let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
    let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());
    let (signals, signal_receivers) = CoreSignals::new(context.clone());
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );
    // Need at least one subscriber to the block broadcast channel.
    let _block_receiver = signal_receivers.block_broadcast_receiver();

    let (commit_consumer, _commit_receiver, _transaction_receiver) = CommitConsumerArgs::new(0, 0, [0; 32], 0);
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

    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

    let mut core = Core::new(
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
        round_tracker.clone(),
        None,
        None,
        crate::coordination_hub::ConsensusCoordinationHub::new_for_testing(), // quorum_ready - always ready in tests
    );

    // Use a large propagation delay to disable proposing.
    // This is done by accepting an own block at round 1000 to dag state and
    // then simulating updating round tracker received rounds from probe where
    // low quorum round for own index should get calculated to round 0.
    let test_block = VerifiedBlock::new_for_test(TestBlock::new(1000, 0).build());
    transaction_certifier.add_voted_blocks(vec![(test_block.clone(), vec![])]);
    // Force accepting the block to dag state because its causal history is incomplete.
    dag_state.write().accept_block(test_block);

    round_tracker.write().update_from_probe(
        vec![
            vec![0, 0, 0, 0],
            vec![0, 0, 0, 0],
            vec![0, 0, 0, 0],
            vec![0, 0, 0, 0],
        ],
        vec![
            vec![0, 0, 0, 0],
            vec![0, 0, 0, 0],
            vec![0, 0, 0, 0],
            vec![0, 0, 0, 0],
        ],
    );

    // There is no proposal even with forced proposing.
    assert!(core.try_propose(true).unwrap().is_none());

    // Let Core know there is no propagation delay.
    // This is done by simulating updating round tracker recieved rounds from probe
    // where low quorum round for own index should get calculated to round 1000.
    round_tracker.write().update_from_probe(
        vec![
            vec![1000, 1000, 1000, 1000],
            vec![1000, 1000, 1000, 1000],
            vec![1000, 1000, 1000, 1000],
            vec![1000, 1000, 1000, 1000],
        ],
        vec![
            vec![1000, 1000, 1000, 1000],
            vec![1000, 1000, 1000, 1000],
            vec![1000, 1000, 1000, 1000],
            vec![1000, 1000, 1000, 1000],
        ],
    );

    // Also add the necessary blocks from round 1000 so core will propose for
    // round 1001
    for author in 1..4 {
        let block = VerifiedBlock::new_for_test(TestBlock::new(1000, author).build());
        transaction_certifier.add_voted_blocks(vec![(block.clone(), vec![])]);
        // Force accepting the block to dag state because its causal history is incomplete.
        dag_state.write().accept_block(block);
    }

    // Proposing now would succeed.
    assert!(core.try_propose(true).unwrap().is_some());
}


#[tokio::test]
async fn test_core_compress_proposal_references() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let default_params = Parameters::default();

    let (context, _) = Context::new_for_test(4);
    // create the cores and their signals for all the authorities
    let mut cores = create_cores(context, vec![1, 1, 1, 1]).await;

    let mut last_round_blocks = Vec::new();
    let mut all_blocks = Vec::new();

    let excluded_authority = AuthorityIndex::new_for_test(3);

    for round in 1..=10 {
        let mut this_round_blocks = Vec::new();

        for core_fixture in &mut cores {
            // do not produce any block for authority 3
            if core_fixture.core.context.own_index == excluded_authority {
                continue;
            }

            // try to propose to ensure that we are covering the case where we miss the leader authority 3
            core_fixture.add_blocks(last_round_blocks.clone()).unwrap();
            core_fixture.core.round_tracker.write().update_from_probe(
                vec![
                    vec![round, round, round, round],
                    vec![round, round, round, round],
                    vec![round, round, round, round],
                    vec![round, round, round, round],
                ],
                vec![
                    vec![round, round, round, round],
                    vec![round, round, round, round],
                    vec![round, round, round, round],
                    vec![round, round, round, round],
                ],
            );
            core_fixture.core.new_block(round, true).unwrap();

            let block = core_fixture.core.last_proposed_block();
            assert_eq!(block.round(), round);

            // append the new block to this round blocks
            this_round_blocks.push(block.clone());
        }

        last_round_blocks = this_round_blocks.clone();
        all_blocks.extend(this_round_blocks);
    }

    // Now send all the produced blocks to core of authority 3. It should produce a new block. If no compression would
    // be applied the we should expect all the previous blocks to be referenced from round 0..=10. However, since compression
    // is applied only the last round's (10) blocks should be referenced + the authority's block of round 0.
    let core_fixture = &mut cores[excluded_authority];
    // Wait for min round delay to allow blocks to be proposed. Core also enforces a
    // MIN_PROPOSAL_AGGREGATION_DELAY floor (see core/proposer.rs::try_new_block) even when
    // `min_round_delay` is configured lower, so wait for whichever is longer.
    sleep(default_params.min_round_delay.max(crate::core::MIN_PROPOSAL_AGGREGATION_DELAY)).await;
    // add blocks to trigger proposal.
    core_fixture.add_blocks(all_blocks).unwrap();

    // Assert that a block has been created for round 11 and it references to blocks of round 10 for the other peers, and
    // to round 1 for its own block (created after recovery).
    let block = core_fixture.core.last_proposed_block();
    assert_eq!(block.round(), 11);
    assert_eq!(block.ancestors().len(), 4);
    for block_ref in block.ancestors() {
        if block_ref.author == excluded_authority {
            assert_eq!(block_ref.round, 1);
        } else {
            assert_eq!(block_ref.round, 10);
        }
    }

    // Flush the DAG state to storage.
    core_fixture.dag_state.write().flush();

    // Check commits have been persisted to store
    let last_commit = core_fixture
        .store
        .read_last_commit()
        .unwrap()
        .expect("last commit should be set");
    // There are 8 leader rounds with rounds completed up to and including
    // round 10. However because there were no blocks produced for authority 3
    // 2 leader rounds will be skipped.
    assert_eq!(last_commit.index(), 6);
    let all_stored_commits = core_fixture
        .store
        .scan_commits((0..=CommitIndex::MAX).into())
        .unwrap();
    assert_eq!(all_stored_commits.len(), 6);
}

#[tokio::test]
async fn try_select_certified_leaders() {
    // GIVEN
    // // // // // // telemetry_subscribers::init_for_testing();

    let (context, _) = Context::new_for_test(4);

    let authority_index = AuthorityIndex::new_for_test(0);
    let core = CoreTextFixture::new(context.clone(), vec![1, 1, 1, 1], authority_index, true).await;
    let mut core = core.core;

    let mut dag_builder = DagBuilder::new(Arc::new(context.clone()));
    dag_builder.layers(1..=12).build();

    let limit = 2;

    let blocks = dag_builder.blocks(1..=12);

    for block in blocks {
        core.dag_state.write().accept_block(block);
    }

    // WHEN
    let sub_dags_and_commits = dag_builder.get_sub_dag_and_certified_commits(1..=4);
    let mut certified_commits = sub_dags_and_commits
        .into_iter()
        .map(|(_, commit)| commit)
        .collect::<Vec<_>>();

    let leaders = core.try_select_certified_leaders(&mut certified_commits, limit);

    // THEN
    assert_eq!(leaders.len(), 2);
    assert_eq!(certified_commits.len(), 2);
}

pub(crate) async fn receive<T: Copy>(timeout: Duration, mut receiver: watch::Receiver<T>) -> T {
    tokio::time::timeout(timeout, receiver.changed())
        .await
        .expect("Timeout while waiting to read from receiver")
        .expect("Signal receive channel shouldn't be closed");
    *receiver.borrow_and_update()
}
