use super::*;

#[tokio::test]
async fn test_smart_ancestor_selection() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (mut context, mut key_pairs) = Context::new_for_test(7);
    context
        .protocol_config
        .set_consensus_bad_nodes_stake_threshold_for_testing(33);
    let context = Arc::new(context.with_parameters(Parameters {
        sync_last_known_own_block_timeout: Duration::from_millis(2_000),
        ..Default::default()
    }));

    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
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
    let mut block_receiver = signal_receivers.block_broadcast_receiver();

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
        round_tracker.clone(),
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

    // Create blocks for the whole network but not for authority 1
    let mut builder = DagBuilder::new(context.clone());
    builder
        .layers(1..=12)
        .authorities(vec![AuthorityIndex::new_for_test(1)])
        .skip_block()
        .build();
    let blocks = builder.blocks(1..=12);
    // Process all the blocks
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());
    core.set_last_known_proposed_round(12);

    round_tracker.write().update_from_probe(
        vec![
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![0, 0, 0, 0, 0, 0, 0],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
        ],
        vec![
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![0, 0, 0, 0, 0, 0, 0],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
            vec![12, 12, 12, 12, 12, 12, 12],
        ],
    );

    let block = core.try_propose(true).expect("No error").unwrap();
    assert_eq!(block.round(), 13);
    assert_eq!(block.ancestors().len(), 7);

    // Build blocks for rest of the network other than own index
    builder
        .layers(13..=14)
        .authorities(vec![AuthorityIndex::new_for_test(0)])
        .skip_block()
        .build();
    let blocks = builder.blocks(13..=14);
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());

    // We now have triggered a leader schedule change so we should have
    // one EXCLUDE authority (1) when we go to select ancestors for the next proposal
    let block = core.try_propose(true).expect("No error").unwrap();
    assert_eq!(block.round(), 15);
    assert_eq!(block.ancestors().len(), 6);

    // Build blocks for a quorum of the network including the EXCLUDE authority (1)
    // which will trigger smart select and we will not propose a block
    let round_14_ancestors = builder.last_ancestors.clone();
    builder
        .layer(15)
        .authorities(vec![
            AuthorityIndex::new_for_test(0),
            AuthorityIndex::new_for_test(5),
            AuthorityIndex::new_for_test(6),
        ])
        .skip_block()
        .build();
    let blocks = builder.blocks(15..=15);
    let authority_1_excluded_block_reference = blocks
        .iter()
        .find(|block| block.author() == AuthorityIndex::new_for_test(1))
        .unwrap()
        .reference();
    // Wait for min round delay to allow blocks to be proposed.
    sleep(context.parameters.min_round_delay).await;
    // Smart select should be triggered and no block should be proposed.
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());
    assert_eq!(core.last_proposed_block().round(), 15);

    builder
        .layer(15)
        .authorities(vec![
            AuthorityIndex::new_for_test(0),
            AuthorityIndex::new_for_test(1),
            AuthorityIndex::new_for_test(2),
            AuthorityIndex::new_for_test(3),
            AuthorityIndex::new_for_test(4),
        ])
        .skip_block()
        .override_last_ancestors(round_14_ancestors)
        .build();
    let blocks = builder.blocks(15..=15);
    let round_15_ancestors: Vec<BlockRef> = blocks
        .iter()
        .filter(|block| block.round() == 15)
        .map(|block| block.reference())
        .collect();
    let included_block_references = iter::once(&core.last_proposed_block())
        .chain(blocks.iter())
        .filter(|block| block.author() != AuthorityIndex::new_for_test(1))
        .map(|block| block.reference())
        .collect::<Vec<_>>();

    // Have enough ancestor blocks to propose now.
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());
    assert_eq!(core.last_proposed_block().round(), 16);

    // Check that a new block has been proposed & signaled.
    let extended_block = loop {
        let extended_block = tokio::time::timeout(Duration::from_secs(1), block_receiver.recv())
            .await
            .unwrap()
            .unwrap();
        if extended_block.block.round() == 16 {
            break extended_block;
        }
    };
    assert_eq!(extended_block.block.round(), 16);
    assert_eq!(extended_block.block.author(), core.context.own_index);
    assert_eq!(extended_block.block.ancestors().len(), 6);
    assert_eq!(extended_block.block.ancestors(), included_block_references);
    assert_eq!(extended_block.excluded_ancestors.len(), 1);
    assert_eq!(
        extended_block.excluded_ancestors[0],
        authority_1_excluded_block_reference
    );

    // Build blocks for a quorum of the network including the EXCLUDE ancestor
    // which will trigger smart select and we will not propose a block.
    // This time we will force propose by hitting the leader timeout after which
    // should cause us to include this EXCLUDE ancestor.
    builder
        .layer(16)
        .authorities(vec![
            AuthorityIndex::new_for_test(0),
            AuthorityIndex::new_for_test(5),
            AuthorityIndex::new_for_test(6),
        ])
        .skip_block()
        .override_last_ancestors(round_15_ancestors)
        .build();
    let blocks = builder.blocks(16..=16);
    // Wait for leader timeout to force blocks to be proposed.
    sleep(context.parameters.min_round_delay).await;
    // Smart select should be triggered and no block should be proposed.
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());
    assert_eq!(core.last_proposed_block().round(), 16);

    // Simulate a leader timeout and a force proposal where we will include
    // one EXCLUDE ancestor when we go to select ancestors for the next proposal
    let block = core.try_propose(true).expect("No error").unwrap();
    assert_eq!(block.round(), 17);
    assert_eq!(block.ancestors().len(), 5);

    // Check that a new block has been proposed & signaled.
    let extended_block = tokio::time::timeout(Duration::from_secs(1), block_receiver.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(extended_block.block.round(), 17);
    assert_eq!(extended_block.block.author(), core.context.own_index);
    assert_eq!(extended_block.block.ancestors().len(), 5);
    assert_eq!(extended_block.excluded_ancestors.len(), 0);

    // Excluded authority is locked until round 20, simulate enough rounds to
    // unlock
    builder
        .layers(17..=22)
        .authorities(vec![AuthorityIndex::new_for_test(0)])
        .skip_block()
        .build();
    let blocks = builder.blocks(17..=22);

    // Simulate updating received and accepted rounds from prober.
    // New quorum rounds for authority can then be computed which will unlock
    // the Excluded authority (1) and then we should be able to create a new
    // layer of blocks which will then all be included as ancestors for the
    // next proposal
    round_tracker.write().update_from_probe(
        vec![
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
        ],
        vec![
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
            vec![22, 22, 22, 22, 22, 22, 22],
        ],
    );

    let included_block_references = iter::once(&core.last_proposed_block())
        .chain(blocks.iter())
        .filter(|block| block.round() == 22 || block.author() == core.context.own_index)
        .map(|block| block.reference())
        .collect::<Vec<_>>();

    // Have enough ancestor blocks to propose now.
    sleep(context.parameters.min_round_delay).await;
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());
    assert_eq!(core.last_proposed_block().round(), 23);

    // Check that a new block has been proposed & signaled.
    let extended_block = tokio::time::timeout(Duration::from_secs(1), block_receiver.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(extended_block.block.round(), 23);
    assert_eq!(extended_block.block.author(), core.context.own_index);
    assert_eq!(extended_block.block.ancestors().len(), 7);
    assert_eq!(extended_block.block.ancestors(), included_block_references);
    assert_eq!(extended_block.excluded_ancestors.len(), 0);
}

#[tokio::test]
async fn test_excluded_ancestor_limit() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (context, mut key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context.with_parameters(Parameters {
        sync_last_known_own_block_timeout: Duration::from_millis(2_000),
        ..Default::default()
    }));

    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
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
    let mut block_receiver = signal_receivers.block_broadcast_receiver();

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

    // Create blocks for the whole network
    let mut builder = DagBuilder::new(context.clone());
    builder.layers(1..=3).build();

    // This will equivocate 9 blocks for authority 1 which will be excluded on
    // the proposal but because of the limits set will be dropped and not included
    // as part of the ExtendedBlock structure sent to the rest of the network
    builder
        .layer(4)
        .authorities(vec![AuthorityIndex::new_for_test(1)])
        .equivocate(9)
        .build();
    let blocks = builder.blocks(1..=4);

    // Process all the blocks
    transaction_certifier.add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
    assert!(core.add_blocks(blocks).unwrap().is_empty());
    core.set_last_known_proposed_round(3);

    let block = core.try_propose(true).expect("No error").unwrap();
    assert_eq!(block.round(), 5);
    assert_eq!(block.ancestors().len(), 4);

    // Check that a new block has been proposed & signaled.
    let extended_block = tokio::time::timeout(Duration::from_secs(1), block_receiver.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(extended_block.block.round(), 5);
    assert_eq!(extended_block.block.author(), core.context.own_index);
    assert_eq!(extended_block.block.ancestors().len(), 4);
    assert_eq!(extended_block.excluded_ancestors.len(), 8);
}


#[tokio::test]
async fn test_core_signals() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let default_params = Parameters::default();

    let (context, _) = Context::new_for_test(4);
    // create the cores and their signals for all the authorities
    let mut cores = create_cores(context, vec![1, 1, 1, 1]).await;

    // Now iterate over a few rounds and ensure the corresponding signals are created while network advances
    let mut last_round_blocks = Vec::new();
    for round in 1..=10 {
        let mut this_round_blocks = Vec::new();

        // Wait for min round delay to allow blocks to be proposed.
        sleep(default_params.min_round_delay).await;

        for core_fixture in &mut cores {
            // add the blocks from last round
            // this will trigger a block creation for the round and a signal should be emitted
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

            // A "new round" signal should be received given that all the blocks of previous round have been processed
            let new_round = receive(
                Duration::from_secs(1),
                core_fixture.signal_receivers.new_round_receiver(),
            )
            .await;
            assert_eq!(new_round, round);

            // Check that a new block has been proposed.
            let extended_block =
                tokio::time::timeout(Duration::from_secs(1), core_fixture.block_receiver.recv())
                    .await
                    .unwrap()
                    .unwrap();
            assert_eq!(extended_block.block.round(), round);
            assert_eq!(
                extended_block.block.author(),
                core_fixture.core.context.own_index
            );

            // append the new block to this round blocks
            this_round_blocks.push(core_fixture.core.last_proposed_block().clone());

            let block = core_fixture.core.last_proposed_block();

            // ensure that produced block is referring to the blocks of last_round
            assert_eq!(
                block.ancestors().len(),
                core_fixture.core.context.committee.size()
            );
            for ancestor in block.ancestors() {
                if block.round() > 1 {
                    // don't bother with round 1 block which just contains the genesis blocks.
                    assert!(
                        last_round_blocks
                            .iter()
                            .any(|block| block.reference() == *ancestor),
                        "Reference from previous round should be added"
                    );
                }
            }
        }

        last_round_blocks = this_round_blocks;
    }

    for core_fixture in cores {
        // Flush the DAG state to storage.
        core_fixture.dag_state.write().flush();
        // Check commits have been persisted to store
        let last_commit = core_fixture
            .store
            .read_last_commit()
            .unwrap()
            .expect("last commit should be set");
        // There are 8 leader rounds with rounds completed up to and including
        // round 9. Round 10 blocks will only include their own blocks, so the
        // 8th leader will not be committed.
        assert_eq!(last_commit.index(), 7);
        let all_stored_commits = core_fixture
            .store
            .scan_commits((0..=CommitIndex::MAX).into())
            .unwrap();
        assert_eq!(all_stored_commits.len(), 7);
    }
}

