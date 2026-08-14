use super::*;
use super::proposal::receive;

#[tokio::test]
async fn test_commit_and_notify_for_block_status() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (mut context, mut key_pairs) = Context::new_for_test(4);
    const GC_DEPTH: u32 = 2;

    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(GC_DEPTH);

    let context = Arc::new(context);

    let store = Arc::new(MemStore::new());
    let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
    let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());
    let mut block_status_subscriptions = FuturesUnordered::new();

    let dag_str = "DAG {
        Round 0 : { 4 },
        Round 1 : { * },
        Round 2 : { * },
        Round 3 : {
            A -> [*],
            B -> [-A2],
            C -> [-A2],
            D -> [-A2],
        },
        Round 4 : { 
            B -> [-A3],
            C -> [-A3],
            D -> [-A3],
        },
        Round 5 : { 
            A -> [A3, B4, C4, D4]
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 6 : { * },
        Round 7 : { * },
        Round 8 : { * },
    }";

    let (_, dag_builder) = parse_dag(dag_str).expect("Invalid dag");
    dag_builder.print();

    // Subscribe to all created "own" blocks. We know that for our node (A) we'll be able to commit up to round 5.
    for block in dag_builder.blocks(1..=5) {
        if block.author() == context.own_index {
            let subscription =
                transaction_consumer.subscribe_for_block_status_testing(block.reference());
            block_status_subscriptions.push(subscription);
        }
    }

    // write them in store
    store
        .write(WriteBatch::default().blocks(dag_builder.blocks(1..=8)))
        .expect("Storage error");

    // create dag state after all blocks have been written to store
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
    let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
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
        dag_state_writer.clone(),
        transaction_certifier.clone(),
        leader_schedule.clone(),
        0,
    )
    .await;

    // Flush the DAG state to storage.
    dag_state.write().flush();

    // Check no commits have been persisted to dag_state or store.
    let last_commit = store.read_last_commit().unwrap();
    assert!(last_commit.is_none());
    assert_eq!(dag_state.read().last_commit_index(), 0);

    // Now recover Core and other components.
    let (signals, signal_receivers) = CoreSignals::new(context.clone());
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );
    transaction_certifier.recover_blocks_after_round(dag_state.read().gc_round());
    // Need at least one subscriber to the block broadcast channel.
    let _block_receiver = signal_receivers.block_broadcast_receiver();
    let round_tracker = Arc::new(RwLock::new(PeerRoundTracker::new(context.clone())));

    let _core = Core::new(
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

    // Flush the DAG state to storage.
    dag_state.write().flush();

    let last_commit = store
        .read_last_commit()
        .unwrap()
        .expect("last commit should be set");

    assert_eq!(last_commit.index(), 5);

    while let Some(result) = block_status_subscriptions.next().await {
        let status = result.unwrap();

        match status {
            BlockStatus::Sequenced(block_ref) => {
                assert!(block_ref.round == 1 || block_ref.round == 5);
            }
            BlockStatus::GarbageCollected(block_ref) => {
                assert!(block_ref.round == 2 || block_ref.round == 3);
            }
        }
    }
}

// Tests that the threshold clock advances when blocks get unsuspended due to GC'ed blocks and newly created blocks are always higher
// than the last advanced gc round.
#[tokio::test]
async fn test_multiple_commits_advance_threshold_clock() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let (mut context, mut key_pairs) = Context::new_for_test(4);
    const GC_DEPTH: u32 = 2;

    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(GC_DEPTH);

    let context = Arc::new(context);

    let store = Arc::new(MemStore::new());
    let (_transaction_client, tx_receiver) = TransactionClient::new(context.clone());
    let transaction_consumer = TransactionConsumer::new(tx_receiver, context.clone());

    // On round 1 we do produce the block for authority D but we do not link it until round 6. This is making round 6 unable to get processed
    // until leader of round 3 is committed where round 1 gets garbage collected.
    // Then we add more rounds so we can trigger a commit for leader of round 9 which will move the gc round to 7.
    let dag_str = "DAG {
        Round 0 : { 4 },
        Round 1 : { * },
        Round 2 : { 
            B -> [-D1],
            C -> [-D1],
            D -> [-D1],
        },
        Round 3 : {
            B -> [*],
            C -> [*]
            D -> [*],
        },
        Round 4 : { 
            A -> [*],
            B -> [*],
            C -> [*]
            D -> [*],
        },
        Round 5 : { 
            A -> [*],
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 6 : { 
            B -> [A5, B5, C5, D1],
            C -> [A5, B5, C5, D1],
            D -> [A5, B5, C5, D1],
        },
        Round 7 : { 
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 8 : { 
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 9 : { 
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 10 : { 
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 11 : { 
            B -> [*],
            C -> [*],
            D -> [*],
        },
    }";

    let (_, dag_builder) = parse_dag(dag_str).expect("Invalid dag");
    dag_builder.print();

    // create dag state after all blocks have been written to store
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
    let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(LeaderSchedule::from_store(
        context.clone(),
        dag_state.clone(),
    ));
    let (blocks_sender, _blocks_receiver) =
        tokio::sync::mpsc::unbounded_channel();
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
        dag_state_writer.clone(),
        transaction_certifier.clone(),
        leader_schedule.clone(),
        0,
    )
    .await;

    // Flush the DAG state to storage.
    dag_state.write().flush();

    // Check no commits have been persisted to dag_state or store.
    let last_commit = store.read_last_commit().unwrap();
    assert!(last_commit.is_none());
    assert_eq!(dag_state.read().last_commit_index(), 0);

    // Now spin up core
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
    // We set the last known round to 4 so we avoid creating new blocks until then - otherwise it will crash as the already created DAG contains blocks for this
    // authority.
    core.set_last_known_proposed_round(4);

    // We add all the blocks except D1. The only ones we can immediately accept are the ones up to round 5 as they don't have a dependency on D1. Rest of blocks do have causal dependency
    // to D1 so they can't be processed until the leader of round 3 can get committed and gc round moves to 1. That will make all the blocks that depend to D1 get accepted.
    // However, our threshold clock is now at round 6 as the last quorum that we managed to process was the round 5.
    // As commits happen blocks of later rounds get accepted and more leaders get committed. Eventually the leader of round 9 gets committed and gc is moved to 9 - 2 = 7.
    // If our node attempts to produce a block for the threshold clock 6, that will make the acceptance checks fail as now gc has moved far past this round.
    let mut all_blocks = dag_builder.blocks(1..=11);
    all_blocks.sort_by_key(|b| b.round());
    let voted_blocks: Vec<(VerifiedBlock, Vec<TransactionIndex>)> =
        all_blocks.iter().map(|b| (b.clone(), vec![])).collect();
    transaction_certifier.add_voted_blocks(voted_blocks);
    let blocks: Vec<VerifiedBlock> = all_blocks
        .into_iter()
        .filter(|b| !(b.round() == 1 && b.author() == AuthorityIndex::new_for_test(3)))
        .collect();
    core.add_blocks(blocks).expect("Should not fail");

    assert_eq!(core.last_proposed_round(), 12);
}


#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_leader_schedule_change() {
    // // // // // // telemetry_subscribers::init_for_testing();
    let default_params = Parameters::default();

    let (context, _) = Context::new_for_test(4);
    // create the cores and their signals for all the authorities
    let mut cores = create_cores(context, vec![1, 1, 1, 1]).await;

    // Now iterate over a few rounds and ensure the corresponding signals are created while network advances
    let mut last_round_blocks = Vec::new();
    for round in 1..=30 {
        let mut this_round_blocks = Vec::new();

        // Wait for min round delay to allow blocks to be proposed. Core also enforces a
        // MIN_PROPOSAL_AGGREGATION_DELAY floor (see core/proposer.rs::try_new_block) even when
        // `min_round_delay` is configured lower, so wait for whichever is longer.
        sleep(default_params.min_round_delay.max(crate::core::MIN_PROPOSAL_AGGREGATION_DELAY))
            .await;

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
        // There are 28 leader rounds with rounds completed up to and including
        // round 29. Round 30 blocks will only include their own blocks, so the
        // 28th leader will not be committed.
        assert_eq!(last_commit.index(), 27);
        let all_stored_commits = core_fixture
            .store
            .scan_commits((0..=CommitIndex::MAX).into())
            .unwrap();
        assert_eq!(all_stored_commits.len(), 27);
        assert_eq!(
            core_fixture
                .core
                .leader_schedule
                .leader_swap_table
                .read()
                .bad_nodes
                .len(),
            1
        );
        assert_eq!(
            core_fixture
                .core
                .leader_schedule
                .leader_swap_table
                .read()
                .good_nodes
                .len(),
            1
        );
        let expected_reputation_scores =
            ReputationScores::new((11..=20).into(), vec![29, 29, 29, 29]);
        assert_eq!(
            core_fixture
                .core
                .leader_schedule
                .leader_swap_table
                .read()
                .reputation_scores,
            expected_reputation_scores
        );
    }
}

#[tokio::test]
async fn test_filter_new_commits() {
    // // // // // // telemetry_subscribers::init_for_testing();

    let (context, _key_pairs) = Context::new_for_test(4);
    let context = context.with_parameters(Parameters {
        sync_last_known_own_block_timeout: Duration::from_millis(2_000),
        ..Default::default()
    });

    let authority_index = AuthorityIndex::new_for_test(0);
    let core = CoreTextFixture::new(context, vec![1, 1, 1, 1], authority_index, true).await;
    let mut core = core.core;

    // No new block should have been produced
    assert_eq!(
        core.last_proposed_round(),
        GENESIS_ROUND,
        "No block should have been created other than genesis"
    );

    // create a DAG of 12 rounds
    let mut dag_builder = DagBuilder::new(core.context.clone());
    dag_builder.layers(1..=12).build();

    // Store all blocks up to round 6 which should be enough to decide up to leader 4
    dag_builder.print();
    let blocks = dag_builder.blocks(1..=6);

    for block in blocks {
        core.dag_state.write().accept_block(block);
    }

    // Get all the committed sub dags up to round 10
    let sub_dags_and_commits = dag_builder.get_sub_dag_and_certified_commits(1..=10);

    // Now try to commit up to the latest leader (round = 4). Do not provide any certified commits.
    let committed_sub_dags = core.try_commit(vec![]).unwrap();

    // We should have committed up to round 4
    assert_eq!(committed_sub_dags.len(), 4);

    // filter_new_commits() falls back to checking the persisted store (not just the
    // in-memory last_commit_index) for whether a commit index is already locally
    // committed, so the just-committed rounds 1-4 need to actually be flushed before
    // the checks below can see them as already-committed.
    let flush_rx = core.dag_state.write().flush();
    if let Some(rx) = flush_rx {
        rx.await.unwrap();
    }

    // Now validate the certified commits. We'll try 3 different scenarios:
    println!("Case 1. Provide certified commits that are all before the last committed round.");

    // Highest certified commit should be for leader of round 4.
    let certified_commits = sub_dags_and_commits
        .iter()
        .take(4)
        .map(|(_, c)| c)
        .cloned()
        .collect::<Vec<_>>();
    assert!(
        certified_commits.last().unwrap().index()
            <= committed_sub_dags.last().unwrap().commit_ref.index,
        "Highest certified commit should older than the highest committed index."
    );

    let certified_commits = core.filter_new_commits(certified_commits).unwrap();

    // No commits should be processed
    assert!(certified_commits.is_empty());

    println!("Case 2. Provide certified commits that are all after the last committed round.");

    // Highest certified commit should be for leader of round 4.
    let certified_commits = sub_dags_and_commits
        .iter()
        .take(5)
        .map(|(_, c)| c.clone())
        .collect::<Vec<_>>();

    let certified_commits = core.filter_new_commits(certified_commits.clone()).unwrap();

    // The certified commit of index 5 should be processed.
    assert_eq!(certified_commits.len(), 1);
    assert_eq!(certified_commits.first().unwrap().reference().index, 5);

    println!(
        "Case 3. Provide certified commits where the first certified commit index is not the last_commited_index + 1."
    );

    // Highest certified commit should be for leader of round 4.
    let certified_commits = sub_dags_and_commits
        .iter()
        .skip(5)
        .take(1)
        .map(|(_, c)| c.clone())
        .collect::<Vec<_>>();

    // COLD-START (see filter_new_commits()): a gap between the last committed index
    // and the first certified commit's index used to be a hard error
    // (UnexpectedCertifiedCommitIndex), but that's expected to happen during
    // snapshot restore when the node jumps forward, so it's now just a
    // `tracing::warn!` and the gapped commit is returned anyway instead of rejected.
    let certified_commits = core
        .filter_new_commits(certified_commits.clone())
        .unwrap();
    assert_eq!(certified_commits.len(), 1);
    assert_eq!(certified_commits.first().unwrap().reference().index, 6);
}

#[tokio::test]
async fn test_add_certified_commits() {
    // // // // // // telemetry_subscribers::init_for_testing();

    let (context, _key_pairs) = Context::new_for_test(4);
    let context = context.with_parameters(Parameters {
        sync_last_known_own_block_timeout: Duration::from_millis(2_000),
        ..Default::default()
    });

    let authority_index = AuthorityIndex::new_for_test(0);
    let core = CoreTextFixture::new(context, vec![1, 1, 1, 1], authority_index, true).await;
    let store = core.store.clone();
    let mut core = core.core;

    // No new block should have been produced
    assert_eq!(
        core.last_proposed_round(),
        GENESIS_ROUND,
        "No block should have been created other than genesis"
    );

    // create a DAG of 12 rounds
    let mut dag_builder = DagBuilder::new(core.context.clone());
    dag_builder.layers(1..=12).build();

    // Store all blocks up to round 6 which should be enough to decide up to leader 4
    dag_builder.print();
    let blocks = dag_builder.blocks(1..=6);

    for block in blocks {
        core.dag_state.write().accept_block(block);
    }

    // Get all the committed sub dags up to round 10
    let sub_dags_and_commits = dag_builder.get_sub_dag_and_certified_commits(1..=10);

    // Now try to commit up to the latest leader (round = 4). Do not provide any certified commits.
    let committed_sub_dags = core.try_commit(vec![]).unwrap();

    // We should have committed up to round 4
    assert_eq!(committed_sub_dags.len(), 4);

    // Flush the DAG state to storage, and wait for the (async, spawn_blocking-backed)
    // write to actually land before reading it back through `store` below.
    let flush_rx = core.dag_state.write().flush();
    if let Some(rx) = flush_rx {
        rx.await.unwrap();
    }

    println!("Case 1. Provide no certified commits. No commit should happen.");

    let last_commit = store
        .read_last_commit()
        .unwrap()
        .expect("Last commit should be set");
    assert_eq!(last_commit.reference().index, 4);

    println!(
        "Case 2. Provide certified commits that before and after the last committed round and also there are additional blocks so can run the direct decide rule as well."
    );

    // The commits of leader rounds 5-8 should be committed via the certified commits.
    let certified_commits = sub_dags_and_commits
        .iter()
        .skip(3)
        .take(5)
        .map(|(_, c)| c.clone())
        .collect::<Vec<_>>();

    // Now only add the blocks of rounds 8..=12. The blocks up to round 7 should be accepted via the certified commits processing.
    let blocks = dag_builder.blocks(8..=12);
    for block in blocks {
        core.dag_state.write().accept_block(block);
    }

    // The corresponding blocks of the certified commits should be accepted and stored before linearizing and committing the DAG.
    core.add_certified_commits(CertifiedCommits::new(certified_commits.clone(), vec![]))
        .expect("Should not fail");

    // FORK-SAFETY (May 2026, try_commit()): once in "sync mode" (certified commits were
    // provided), Core deliberately breaks out instead of opportunistically falling back to
    // the local direct-decide rule for anything beyond the certified commits — mixing the
    // two within the same call was deemed unsafe (own comment: "Fallback to the local
    // committer during fast-forwarding is extremely dangerous"). So the direct-decide of
    // leader rounds 9-10 from the extra accepted blocks now needs its own, separate,
    // non-sync-mode try_commit() call.
    core.try_commit(vec![]).unwrap();

    // Flush the DAG state to storage, and wait for the (async, spawn_blocking-backed)
    // write to actually land before reading it back through `store` below.
    let flush_rx = core.dag_state.write().flush();
    if let Some(rx) = flush_rx {
        rx.await.unwrap();
    }

    let commits = store.scan_commits((6..=10).into()).unwrap();

    // We expect all the sub dags up to leader round 10 to be committed.
    assert_eq!(commits.len(), 5);

    for i in 6..=10 {
        let commit = &commits[i - 6];
        assert_eq!(commit.reference().index, i as u32);
    }
}

#[tokio::test]
async fn try_commit_with_certified_commits_gced_blocks() {
    const GC_DEPTH: u32 = 3;
    // // // // // // telemetry_subscribers::init_for_testing();

    let (mut context, mut key_pairs) = Context::new_for_test(5);
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(GC_DEPTH);
    let context = Arc::new(context.with_parameters(Parameters {
        sync_last_known_own_block_timeout: Duration::from_millis(2_000),
        ..Default::default()
    }));

    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));
    let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());

    let block_manager = BlockManager::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
    let leader_schedule = Arc::new(
        LeaderSchedule::from_store(context.clone(), dag_state.clone())
            .with_num_commits_per_schedule(10),
    );

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

    let hub = crate::coordination_hub::ConsensusCoordinationHub::new_for_testing();
    hub.set_phase(crate::coordination_hub::NodeConsensusPhase::Bootstrapping);
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
        hub.clone(), // quorum_ready - always ready in tests
    );

    // No new block should have been produced
    assert_eq!(
        core.last_proposed_round(),
        GENESIS_ROUND,
        "No block should have been created other than genesis"
    );
    hub.set_phase(crate::coordination_hub::NodeConsensusPhase::Healthy);

    let dag_str = "DAG {
        Round 0 : { 5 },
        Round 1 : { * },
        Round 2 : { 
            A -> [-E1],
            B -> [-E1],
            C -> [-E1],
            D -> [-E1],
        },
        Round 3 : {
            A -> [*],
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 4 : { 
            A -> [*],
            B -> [*],
            C -> [*],
            D -> [*],
        },
        Round 5 : { 
            A -> [*],
            B -> [*],
            C -> [*],
            D -> [*],
            E -> [A4, B4, C4, D4, E1]
        },
        Round 6 : { * },
        Round 7 : { * },
    }";

    let (_, mut dag_builder) = parse_dag(dag_str).expect("Invalid dag");
    dag_builder.print();

    // Now get all the committed sub dags from the DagBuilder
    let (_sub_dags, certified_commits): (Vec<_>, Vec<_>) = dag_builder
        .get_sub_dag_and_certified_commits(1..=5)
        .into_iter()
        .unzip();

    // Now try to commit up to the latest leader (round = 5) with the provided certified commits. Not that we have not accepted any
    // blocks. That should happen during the commit process.
    let committed_sub_dags = core.try_commit(certified_commits).unwrap();

    // We should have committed up to round 4
    assert_eq!(committed_sub_dags.len(), 4);
    for (index, committed_sub_dag) in committed_sub_dags.iter().enumerate() {
        assert_eq!(committed_sub_dag.commit_ref.index as usize, index + 1);

        // ensure that block from E1 node has not been committed
        for block in committed_sub_dag.blocks.iter() {
            if block.round() == 1 && block.author() == AuthorityIndex::new_for_test(5) {
                panic!("Did not expect to commit block E1");
            }
        }
    }
}


#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_commit_on_leader_schedule_change_boundary_without_multileader() {
    parameterized_test_commit_on_leader_schedule_change_boundary(Some(1)).await;
}

#[tokio::test(flavor = "current_thread", start_paused = true)]
async fn test_commit_on_leader_schedule_change_boundary_with_multileader() {
    parameterized_test_commit_on_leader_schedule_change_boundary(None).await;
}

async fn parameterized_test_commit_on_leader_schedule_change_boundary(
    num_leaders_per_round: Option<usize>,
) {
    // // // // // // telemetry_subscribers::init_for_testing();
    let default_params = Parameters::default();

    let (mut context, _) = Context::new_for_test(6);
    context
        .protocol_config
        .set_mysticeti_num_leaders_per_round_for_testing(num_leaders_per_round);
    // create the cores and their signals for all the authorities
    let mut cores = create_cores(context, vec![1, 1, 1, 1, 1, 1]).await;

    // Now iterate over a few rounds and ensure the corresponding signals are created while network advances
    let mut last_round_blocks: Vec<VerifiedBlock> = Vec::new();
    for round in 1..=33 {
        let mut this_round_blocks = Vec::new();

        // Wait for min round delay to allow blocks to be proposed. Core also enforces a
        // MIN_PROPOSAL_AGGREGATION_DELAY floor (see core/proposer.rs::try_new_block) even when
        // `min_round_delay` is configured lower, so wait for whichever is longer.
        sleep(default_params.min_round_delay.max(crate::core::MIN_PROPOSAL_AGGREGATION_DELAY))
            .await;

        for core_fixture in &mut cores {
            // add the blocks from last round
            // this will trigger a block creation for the round and a signal should be emitted
            core_fixture.add_blocks(last_round_blocks.clone()).unwrap();

            core_fixture.core.round_tracker.write().update_from_probe(
                vec![
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                ],
                vec![
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
                    vec![round, round, round, round, round, round],
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
        // There are 31 leader rounds with rounds completed up to and including
        // round 33. Round 33 blocks will only include their own blocks, so there
        // should only be 30 commits.
        // Historically, on a leader schedule change boundary it was possible for a
        // new leader to get selected for the same round if the leader elected
        // got swapped, allowing for multiple leaders to be committed at a round
        // (30 with multi leader per round explicitly set to 1, otherwise 31).
        // FORK-SAFETY (May 2026, leader_schedule.rs::elect_leader()): reputation-based
        // swaps are now permanently disabled (restoring nodes after a mid-epoch
        // snapshot can't recompute identical reputation scores, so applying swaps
        // risks a fork), so the boundary-swap scenario this test was built to trigger
        // can no longer happen — the schedule boundary alone no longer produces an
        // extra commit, regardless of `num_leaders_per_round`.
        let expected_commit_count = 30;

        // Flush the DAG state to storage.
        core_fixture.dag_state.write().flush();

        // Check commits have been persisted to store
        let last_commit = core_fixture
            .store
            .read_last_commit()
            .unwrap()
            .expect("last commit should be set");
        assert_eq!(last_commit.index(), expected_commit_count);
        let all_stored_commits = core_fixture
            .store
            .scan_commits((0..=CommitIndex::MAX).into())
            .unwrap();
        assert_eq!(all_stored_commits.len(), expected_commit_count as usize);
        assert_eq!(
            core_fixture
                .core
                .leader_schedule
                .leader_swap_table
                .read()
                .bad_nodes
                .len(),
            1
        );
        assert_eq!(
            core_fixture
                .core
                .leader_schedule
                .leader_swap_table
                .read()
                .good_nodes
                .len(),
            1
        );
        let expected_reputation_scores =
            ReputationScores::new((21..=30).into(), vec![43, 43, 43, 43, 43, 43]);
        assert_eq!(
            core_fixture
                .core
                .leader_schedule
                .leader_swap_table
                .read()
                .reputation_scores,
            expected_reputation_scores
        );
    }
}

