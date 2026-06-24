use super::*;

    use consensus_config::AuthorityIndex;
    use rstest::rstest;

    use super::*;
    use crate::{
        commit::{CommitAPI as _, CommitDigest, DEFAULT_WAVE_LENGTH},
        context::Context,
        leader_schedule::{LeaderSchedule, LeaderSwapTable},
        storage::mem_store::MemStore,
        test_dag_builder::DagBuilder,
        test_dag_parser::parse_dag,
        CommitIndex, TestBlock,
    };

    #[rstest]
    #[tokio::test]
    async fn test_handle_commit() {
        // // // // telemetry_subscribers::init_for_testing();
        let num_authorities = 4;
        let (context, _keys) = Context::new_for_test(num_authorities);
        let context = Arc::new(context);

        let dag_state = Arc::new(RwLock::new(DagState::new(
            context.clone(),
            Arc::new(MemStore::new()),
        )));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
        let mut linearizer = Linearizer::new(context.clone(), dag_state.clone(), dag_state_writer.clone());

        // Populate fully connected test blocks for round 0 ~ 10, authorities 0 ~ 3.
        let num_rounds: u32 = 10;
        let mut dag_builder = DagBuilder::new(context.clone());
        dag_builder
            .layers(1..=num_rounds)
            .build()
            .persist_layers(dag_state.clone());

        let leaders = dag_builder
            .leader_blocks(1..=num_rounds)
            .into_iter()
            .map(Option::unwrap)
            .collect::<Vec<_>>();

        let commits = linearizer.handle_commit(leaders.clone(), None);
        for (idx, subdag) in commits.into_iter().enumerate() {
            tracing::info!("{subdag:?}");
            assert_eq!(subdag.leader, leaders[idx].reference());

            let expected_ts = {
                let block_refs = leaders[idx]
                    .ancestors()
                    .iter()
                    .filter(|block_ref| block_ref.round == leaders[idx].round() - 1)
                    .cloned()
                    .collect::<Vec<_>>();
                let blocks = dag_state
                    .read()
                    .get_blocks(&block_refs)
                    .into_iter()
                    .map(|block_opt| block_opt.expect("We should have all blocks in dag state."));

                median_timestamp_by_stake(&context, blocks).unwrap()
            };
            assert_eq!(subdag.timestamp_ms, expected_ts);

            if idx == 0 {
                // First subdag includes the leader block only
                assert_eq!(subdag.blocks.len(), 1);
            } else {
                // Every subdag after will be missing the leader block from the previous
                // committed subdag
                assert_eq!(subdag.blocks.len(), num_authorities);
            }
            for block in subdag.blocks.iter() {
                assert!(block.round() <= leaders[idx].round());
            }
            assert_eq!(subdag.commit_ref.index, idx as CommitIndex + 1);
        }
    }

    #[rstest]
    #[tokio::test]
    async fn test_handle_already_committed() {
        // // // // telemetry_subscribers::init_for_testing();
        let num_authorities = 4;
        let (context, _) = Context::new_for_test(num_authorities);
        let context = Arc::new(context);

        let dag_state = Arc::new(RwLock::new(DagState::new(
            context.clone(),
            Arc::new(MemStore::new()),
        )));
        let leader_schedule = Arc::new(LeaderSchedule::new(
            context.clone(),
            LeaderSwapTable::default(),
        ));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
        let mut linearizer = Linearizer::new(context.clone(), dag_state.clone(), dag_state_writer.clone());
        let wave_length = DEFAULT_WAVE_LENGTH;

        let leader_round_wave_1 = 3;
        let leader_round_wave_2 = leader_round_wave_1 + wave_length;

        // Build a Dag from round 1..=6
        let mut dag_builder = DagBuilder::new(context.clone());
        dag_builder.layers(1..=leader_round_wave_2).build();

        // Now retrieve all the blocks up to round leader_round_wave_1 - 1
        // And then only the leader of round leader_round_wave_1
        // Also store those to DagState
        let mut blocks = dag_builder.blocks(0..=leader_round_wave_1 - 1);
        blocks.push(
            dag_builder
                .leader_block(leader_round_wave_1)
                .expect("Leader block should have been found"),
        );
        dag_state_writer.accept_blocks(blocks.clone());

        let first_leader = dag_builder
            .leader_block(leader_round_wave_1)
            .expect("Wave 1 leader round block should exist");
        let mut last_commit_index = 1;
        let first_commit_data = TrustedCommit::new_for_test(
            last_commit_index,
            CommitDigest::MIN,
            0,
            first_leader.reference(),
            blocks.iter().map(|block| block.reference()).collect(),
            last_commit_index as u64, // global_exec_index for test
        );
        dag_state_writer.add_commit(first_commit_data);

        // Mark the blocks as committed in DagState. This will allow to correctly detect the committed blocks when the new linearizer logic is enabled.
        for block in blocks.iter() {
            dag_state_writer.set_committed(block.reference());
        }

        // Now take all the blocks from round `leader_round_wave_1` up to round `leader_round_wave_2-1`
        let mut blocks = dag_builder.blocks(leader_round_wave_1..=leader_round_wave_2 - 1);
        // Filter out leader block of round `leader_round_wave_1`
        blocks.retain(|block| {
            !(block.round() == leader_round_wave_1
                && block.author() == leader_schedule.elect_leader(leader_round_wave_1, 0))
        });
        // Add the leader block of round `leader_round_wave_2`
        blocks.push(
            dag_builder
                .leader_block(leader_round_wave_2)
                .expect("Leader block should have been found"),
        );
        // Write them in dag state
        dag_state_writer.accept_blocks(blocks.clone());

        let mut blocks: Vec<_> = blocks.into_iter().map(|block| block.reference()).collect();

        // Now get the latest leader which is the leader round of wave 2
        let leader = dag_builder
            .leader_block(leader_round_wave_2)
            .expect("Leader block should exist");

        last_commit_index += 1;
        let expected_second_commit = TrustedCommit::new_for_test(
            last_commit_index,
            CommitDigest::MIN,
            0,
            leader.reference(),
            blocks.clone(),
            last_commit_index as u64, // global_exec_index for test
        );

        let commit = linearizer.handle_commit(vec![leader.clone()], None);
        assert_eq!(commit.len(), 1);

        let subdag = &commit[0];
        tracing::info!("{subdag:?}");
        assert_eq!(subdag.leader, leader.reference());
        assert_eq!(subdag.commit_ref.index, expected_second_commit.index());

        let expected_ts = median_timestamp_by_stake(
            &context,
            subdag.blocks.iter().filter_map(|block| {
                if block.round() == subdag.leader.round - 1 {
                    Some(block.clone())
                } else {
                    None
                }
            }),
        )
        .unwrap();
        assert_eq!(subdag.timestamp_ms, expected_ts);

        // Using the same sorting as used in CommittedSubDag::sort
        blocks.sort_by(|a, b| a.round.cmp(&b.round).then_with(|| a.author.cmp(&b.author)));
        assert_eq!(
            subdag
                .blocks
                .clone()
                .into_iter()
                .map(|b| b.reference())
                .collect::<Vec<_>>(),
            blocks
        );
        for block in subdag.blocks.iter() {
            assert!(block.round() <= expected_second_commit.leader().round);
        }
    }

    /// This test will run the linearizer with gc_depth = 3 and make
    /// sure that for the exact same DAG the linearizer will commit different blocks according to the rules.
    #[tokio::test]
    async fn test_handle_commit_with_gc_simple() {
        // // // // telemetry_subscribers::init_for_testing();

        const GC_DEPTH: u32 = 3;

        let num_authorities = 4;
        let (mut context, _keys) = Context::new_for_test(num_authorities);
        context
            .protocol_config
            .set_consensus_gc_depth_for_testing(GC_DEPTH);

        let context = Arc::new(context);
        let dag_state = Arc::new(RwLock::new(DagState::new(
            context.clone(),
            Arc::new(MemStore::new()),
        )));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
        let mut linearizer = Linearizer::new(context.clone(), dag_state.clone(), dag_state_writer.clone());

        // Authorities of index 0->2 will always creates blocks that see each other, but until round 5 they won't see the blocks of authority 3.
        // For authority 3 we create blocks that connect to all the other authorities.
        // On round 5 we finally make the other authorities see the blocks of authority 3.
        // Practically we "simulate" here a long chain created by authority 3 that is visible in round 5, but due to GC blocks of only round >=2 will
        // be committed, when GC is enabled. When GC is disabled all blocks will be committed for rounds >= 1.
        let dag_str = "DAG {
                Round 0 : { 4 },
                Round 1 : { * },
                Round 2 : {
                    A -> [-D1],
                    B -> [-D1],
                    C -> [-D1],
                    D -> [*],
                },
                Round 3 : {
                    A -> [-D2],
                    B -> [-D2],
                    C -> [-D2],
                },
                Round 4 : { 
                    A -> [-D3],
                    B -> [-D3],
                    C -> [-D3],
                    D -> [A3, B3, C3, D2],
                },
                Round 5 : { * },
            }";

        let (_, dag_builder) = parse_dag(dag_str).expect("Invalid dag");
        dag_builder.print();
        dag_builder.persist_all_blocks(dag_state.clone());

        let leaders = dag_builder
            .leader_blocks(1..=6)
            .into_iter()
            .flatten()
            .collect::<Vec<_>>();

        let commits = linearizer.handle_commit(leaders.clone(), None);
        for (idx, subdag) in commits.into_iter().enumerate() {
            tracing::info!("{subdag:?}");
            assert_eq!(subdag.leader, leaders[idx].reference());

            let expected_ts = {
                let block_refs = leaders[idx]
                    .ancestors()
                    .iter()
                    .filter(|block_ref| block_ref.round == leaders[idx].round() - 1)
                    .cloned()
                    .collect::<Vec<_>>();
                let blocks = dag_state
                    .read()
                    .get_blocks(&block_refs)
                    .into_iter()
                    .map(|block_opt| block_opt.expect("We should have all blocks in dag state."));

                median_timestamp_by_stake(&context, blocks).unwrap()
            };
            assert_eq!(subdag.timestamp_ms, expected_ts);

            if idx == 0 {
                // First subdag includes the leader block only
                assert_eq!(subdag.blocks.len(), 1);
            } else if idx == 1 {
                assert_eq!(subdag.blocks.len(), 3);
            } else if idx == 2 {
                // We commit:
                // * 1 block on round 4, the leader block
                // * 3 blocks on round 3, as no commit happened on round 3 since the leader was missing
                // * 2 blocks on round 2, again as no commit happened on round 3, we commit the "sub dag" of leader of round 3, which will be another 2 blocks
                assert_eq!(subdag.blocks.len(), 6);
            } else {
                // Now it's going to be the first time that a leader will see the blocks of authority 3 and will attempt to commit
                // the long chain. However, due to GC it will only commit blocks of round > 1. That's because it will commit blocks
                // up to previous leader's round (round = 4) minus the gc_depth = 3, so that will be gc_round = 4 - 3 = 1. So we expect
                // to see on the sub dag committed blocks of round >= 2.
                assert_eq!(subdag.blocks.len(), 5);

                assert!(
                    subdag.blocks.iter().all(|block| block.round() >= 2),
                    "Found blocks that are of round < 2."
                );

                // Also ensure that gc_round has advanced with the latest committed leader
                assert_eq!(dag_state.read().gc_round(), subdag.leader.round - GC_DEPTH);
            }
            for block in subdag.blocks.iter() {
                assert!(block.round() <= leaders[idx].round());
            }
            assert_eq!(subdag.commit_ref.index, idx as CommitIndex + 1);
        }
    }

    #[tokio::test]
    async fn test_handle_commit_below_highest_committed_round() {
        // // // // telemetry_subscribers::init_for_testing();

        const GC_DEPTH: u32 = 3;

        let num_authorities = 4;
        let (mut context, _keys) = Context::new_for_test(num_authorities);
        context
            .protocol_config
            .set_consensus_gc_depth_for_testing(GC_DEPTH);

        let context = Arc::new(context);
        let dag_state = Arc::new(RwLock::new(DagState::new(
            context.clone(),
            Arc::new(MemStore::new()),
        )));
        let dag_state_writer = crate::dag_state_actor::DagStateActor::spawn(dag_state.clone());
        let mut linearizer = Linearizer::new(context.clone(), dag_state.clone(), dag_state_writer.clone());

        // Authority D will create an "orphaned" block on round 1 as it won't reference to it on the block of round 2. Similar, no other authority will reference to it on round 2.
        // Then on round 3 the authorities A, B & C will link to block D1. Once the DAG gets committed we should see the block D1 getting committed as well. Normally ,as block D2 would
        // have been committed first block D1 should be ommitted. With the new logic this is no longer true.
        let dag_str = "DAG {
                Round 0 : { 4 },
                Round 1 : { * },
                Round 2 : {
                    A -> [-D1],
                    B -> [-D1],
                    C -> [-D1],
                    D -> [-D1],
                },
                Round 3 : {
                    A -> [A2, B2, C2, D1],
                    B -> [A2, B2, C2, D1],
                    C -> [A2, B2, C2, D1],
                    D -> [A2, B2, C2, D2]
                },
                Round 4 : { * },
            }";

        let (_, dag_builder) = parse_dag(dag_str).expect("Invalid dag");
        dag_builder.print();
        dag_builder.persist_all_blocks(dag_state.clone());

        let leaders = dag_builder
            .leader_blocks(1..=4)
            .into_iter()
            .flatten()
            .collect::<Vec<_>>();

        let commits = linearizer.handle_commit(leaders.clone(), None);
        for (idx, subdag) in commits.into_iter().enumerate() {
            tracing::info!("{subdag:?}");
            assert_eq!(subdag.leader, leaders[idx].reference());

            let expected_ts = {
                let block_refs = leaders[idx]
                    .ancestors()
                    .iter()
                    .filter(|block_ref| block_ref.round == leaders[idx].round() - 1)
                    .cloned()
                    .collect::<Vec<_>>();
                let blocks = dag_state
                    .read()
                    .get_blocks(&block_refs)
                    .into_iter()
                    .map(|block_opt| block_opt.expect("We should have all blocks in dag state."));

                median_timestamp_by_stake(&context, blocks).unwrap()
            };
            assert_eq!(subdag.timestamp_ms, expected_ts);

            if idx == 0 {
                // First subdag includes the leader block only B1
                assert_eq!(subdag.blocks.len(), 1);
            } else if idx == 1 {
                // We commit:
                // * 1 block on round 2, the leader block C2
                // * 2 blocks on round 1, A1, C1
                assert_eq!(subdag.blocks.len(), 3);
            } else if idx == 2 {
                // We commit:
                // * 1 block on round 3, the leader block D3
                // * 3 blocks on round 2, A2, B2, D2
                assert_eq!(subdag.blocks.len(), 4);

                assert!(
                    subdag.blocks.iter().any(|block| block.round() == 2
                        && block.author() == AuthorityIndex::new_for_test(3)),
                    "Block D2 should have been committed."
                );
            } else if idx == 3 {
                // We commit:
                // * 1 block on round 4, the leader block A4
                // * 3 blocks on round 3, A3, B3, C3
                // * 1 block of round 1, D1
                assert_eq!(subdag.blocks.len(), 5);
                assert!(
                    subdag.blocks.iter().any(|block| block.round() == 1
                        && block.author() == AuthorityIndex::new_for_test(3)),
                    "Block D1 should have been committed."
                );
            } else {
                panic!("Unexpected subdag with index {:?}", idx);
            }

            for block in subdag.blocks.iter() {
                assert!(block.round() <= leaders[idx].round());
            }
            assert_eq!(subdag.commit_ref.index, idx as CommitIndex + 1);
        }
    }

    #[rstest]
    #[case(3_000, 3_000, 6_000)]
    #[tokio::test]
    async fn test_calculate_commit_timestamp(
        #[case] timestamp_1: u64,
        #[case] timestamp_2: u64,
        #[case] timestamp_3: u64,
    ) {
        // GIVEN
        // // // // telemetry_subscribers::init_for_testing();

        let num_authorities = 4;
        let (context, _keys) = Context::new_for_test(num_authorities);

        let context = Arc::new(context);
        let store = Arc::new(MemStore::new());
        let dag_state_lock = Arc::new(RwLock::new(DagState::new(context.clone(), store)));

        let ancestors = vec![
            VerifiedBlock::new_for_test(TestBlock::new(4, 0).set_timestamp_ms(1_000).build()),
            VerifiedBlock::new_for_test(TestBlock::new(4, 1).set_timestamp_ms(2_000).build()),
            VerifiedBlock::new_for_test(TestBlock::new(4, 2).set_timestamp_ms(3_000).build()),
            VerifiedBlock::new_for_test(TestBlock::new(4, 3).set_timestamp_ms(4_000).build()),
        ];

        let leader_block = VerifiedBlock::new_for_test(
            TestBlock::new(5, 0)
                .set_timestamp_ms(5_000)
                .set_ancestors(
                    ancestors
                        .iter()
                        .map(|block| block.reference())
                        .collect::<Vec<_>>(),
                )
                .build(),
        );

        {
            let mut dag_state = dag_state_lock.write();
            for block in &ancestors {
                dag_state.accept_block(block.clone());
            }
        }

        let dag_state = dag_state_lock.read();
        let last_commit_timestamp_ms = 0;

        // WHEN
        let timestamp = Linearizer::calculate_commit_timestamp(
            &context,
            &dag_state,
            &leader_block,
            last_commit_timestamp_ms,
        );
        assert_eq!(timestamp, timestamp_1);

        // AND skip the block of authority 0 and round 4.
        let leader_block = VerifiedBlock::new_for_test(
            TestBlock::new(5, 0)
                .set_timestamp_ms(5_000)
                .set_ancestors(
                    ancestors
                        .iter()
                        .skip(1)
                        .map(|block| block.reference())
                        .collect::<Vec<_>>(),
                )
                .build(),
        );

        let timestamp = Linearizer::calculate_commit_timestamp(
            &context,
            &dag_state,
            &leader_block,
            last_commit_timestamp_ms,
        );
        assert_eq!(timestamp, timestamp_2);

        // AND set the `last_commit_timestamp_ms` to 6_000
        let last_commit_timestamp_ms = 6_000;
        let timestamp = Linearizer::calculate_commit_timestamp(
            &context,
            &dag_state,
            &leader_block,
            last_commit_timestamp_ms,
        );
        assert_eq!(timestamp, timestamp_3);

        // AND there is only one ancestor block to commit
        let (context, _) = Context::new_for_test(1);
        let leader_block = VerifiedBlock::new_for_test(
            TestBlock::new(5, 0)
                .set_timestamp_ms(5_000)
                .set_ancestors(
                    ancestors
                        .iter()
                        .take(1)
                        .map(|block| block.reference())
                        .collect::<Vec<_>>(),
                )
                .build(),
        );
        let last_commit_timestamp_ms = 0;
        let timestamp = Linearizer::calculate_commit_timestamp(
            &context,
            &dag_state,
            &leader_block,
            last_commit_timestamp_ms,
        );
        assert_eq!(timestamp, 1_000);
    }

    #[test]
    fn test_median_timestamps_by_stake() {
        // One total stake.
        let timestamps = vec![(1_000, 1)];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 1), 1_000);

        // Odd number of total stakes.
        let timestamps = vec![(1_000, 1), (2_000, 1), (3_000, 1)];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 3), 2_000);

        // Even number of total stakes.
        let timestamps = vec![(1_000, 1), (2_000, 1), (3_000, 1), (4_000, 1)];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 4), 3_000);

        // Even number of total stakes, different order.
        let timestamps = vec![(4_000, 1), (3_000, 1), (1_000, 1), (2_000, 1)];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 4), 3_000);

        // Unequal stakes.
        let timestamps = vec![(2_000, 2), (4_000, 2), (1_000, 3), (3_000, 3)];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 10), 3_000);

        // Unequal stakes.
        let timestamps = vec![
            (500, 2),
            (4_000, 2),
            (2_500, 3),
            (1_000, 5),
            (3_000, 3),
            (2_000, 4),
        ];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 19), 2_000);

        // One authority dominates.
        let timestamps = vec![(1_000, 1), (2_000, 1), (3_000, 1), (4_000, 1), (5_000, 10)];
        assert_eq!(median_timestamps_by_stake_inner(timestamps, 14), 5_000);
    }

    #[tokio::test]
    async fn test_median_timestamps_by_stake_errors() {
        let num_authorities = 4;
        let (context, _keys) = Context::new_for_test(num_authorities);
        let context = Arc::new(context);

        // No blocks provided
        let err = median_timestamp_by_stake(&context, vec![].into_iter()).unwrap_err();
        assert_eq!(err, "No blocks provided");

        // Blocks provided but total stake is less than quorum threshold
        let block = VerifiedBlock::new_for_test(TestBlock::new(5, 0).build());
        let err = median_timestamp_by_stake(&context, vec![block].into_iter()).unwrap_err();
        assert_eq!(err, "Total stake 1 < quorum threshold 3");
    }
