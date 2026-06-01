// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

mod tests {
use std::{collections::BTreeSet, sync::Arc};

use consensus_config::AuthorityIndex;
use consensus_types::block::{BlockDigest, BlockRef, Round};
use parking_lot::RwLock;
use rand::{prelude::StdRng, seq::SliceRandom, SeedableRng};
use rstest::rstest;

use crate::{
    block::{BlockAPI, VerifiedBlock},
    block_manager::BlockManager,
    commit::TrustedCommit,
    context::Context,
    dag_state::DagState,
    storage::mem_store::MemStore,
    test_dag_builder::DagBuilder,
    test_dag_parser::parse_dag,
    CommitDigest,
};

#[tokio::test]
async fn suspend_blocks_with_missing_ancestors() {
    // GIVEN
    let (context, _key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder
        .layers(1..=2) // 2 rounds
        .authorities(vec![
            AuthorityIndex::new_for_test(0),
            AuthorityIndex::new_for_test(2),
        ]) // Create equivocating blocks for 2 authorities
        .equivocate(3)
        .build();

    // Take only the blocks of round 2 and try to accept them
    let round_2_blocks = dag_builder
        .blocks
        .into_iter()
        .filter_map(|(_, block)| (block.round() == 2).then_some(block))
        .collect::<Vec<VerifiedBlock>>();

    // WHEN
    let (accepted_blocks, missing) = block_manager.try_accept_blocks(round_2_blocks.clone());

    // THEN
    assert!(accepted_blocks.is_empty());

    // AND the returned missing ancestors should be the same as the provided block ancestors
    let missing_block_refs = round_2_blocks.first().unwrap().ancestors();
    let missing_block_refs = missing_block_refs.iter().cloned().collect::<BTreeSet<_>>();
    assert_eq!(missing, missing_block_refs);

    // AND the missing blocks are the parents of the round 2 blocks. Since this is a fully connected DAG taking the
    // ancestors of the first element suffices.
    assert_eq!(block_manager.missing_blocks(), missing_block_refs);

    // AND suspended blocks should return the round_2_blocks
    assert_eq!(
        block_manager.suspended_blocks(),
        round_2_blocks
            .into_iter()
            .map(|block| block.reference())
            .collect::<Vec<_>>()
    );
}

#[tokio::test]
async fn try_accept_block_returns_missing_blocks() {
    let (context, _key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder
        .layers(1..=4) // 4 rounds
        .authorities(vec![
            AuthorityIndex::new_for_test(0),
            AuthorityIndex::new_for_test(2),
        ]) // Create equivocating blocks for 2 authorities
        .equivocate(3) // Use 3 equivocations blocks per authority
        .build();

    // Take the blocks from round 4 up to 2 (included). Only the first block of each round should return missing
    // ancestors when try to accept
    for (_, block) in dag_builder
        .blocks
        .into_iter()
        .rev()
        .take_while(|(_, block)| block.round() >= 2)
    {
        // WHEN
        let (accepted_blocks, missing) = block_manager.try_accept_blocks(vec![block.clone()]);

        // THEN
        assert!(accepted_blocks.is_empty());

        let block_ancestors = block.ancestors().iter().cloned().collect::<BTreeSet<_>>();
        assert_eq!(missing, block_ancestors);
    }
}

#[tokio::test]
async fn accept_blocks_with_complete_causal_history() {
    // GIVEN
    let (context, _key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG of 2 rounds
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder.layers(1..=2).build();

    let all_blocks = dag_builder.blocks.values().cloned().collect::<Vec<_>>();

    // WHEN
    let (accepted_blocks, missing) = block_manager.try_accept_blocks(all_blocks.clone());

    // THEN
    assert_eq!(accepted_blocks.len(), 8);
    assert_eq!(
        accepted_blocks,
        all_blocks
            .iter()
            .filter(|block| block.round() > 0)
            .cloned()
            .collect::<Vec<VerifiedBlock>>()
    );
    assert!(missing.is_empty());
    assert!(block_manager.is_empty());

    // WHEN trying to accept same blocks again, then none will be returned as those have been already accepted
    let (accepted_blocks, _) = block_manager.try_accept_blocks(all_blocks);
    assert!(accepted_blocks.is_empty());
}

/// Tests that the block manager accepts blocks when some or all of their causal history is below or equal to the GC round.
#[tokio::test]
async fn accept_blocks_with_causal_history_below_gc_round() {
    // GIVEN
    let (mut context, _key_pairs) = Context::new_for_test(4);

    // We set the gc depth to 4
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    // We "fake" the commit for round 10, so we can test the GC round 6 (commit_round - gc_depth = 10 - 4 = 6)
    let last_commit = TrustedCommit::new_for_test(
        10,
        CommitDigest::MIN,
        context.clock.timestamp_utc_ms(),
        BlockRef::new(10, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
        vec![],
        10, // global_exec_index for test
    );
    dag_state.write().set_last_commit(last_commit);
    assert_eq!(
        dag_state.read().gc_round(),
        6,
        "GC round should have moved to round 6"
    );

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG of 10 rounds with some weak links for the blocks of round 9
    let dag_str = "DAG {
        Round 0 : { 4 },
        Round 1 : { * },
        Round 2 : { * },
        Round 3 : { * },
        Round 4 : { * },
        Round 5 : { * },
        Round 6 : { * },
        Round 7 : {
            A -> [*],
            B -> [*],
            C -> [*],
        }
        Round 8 : {
            A -> [*],
            B -> [*],
            C -> [*],
        },
        Round 9 : {
            A -> [A8, B8, C8, D6],
            B -> [A8, B8, C8, D6],
            C -> [A8, B8, C8, D6],
            D -> [A8, B8, C8, D6],
        },
        Round 10 : { * },
    }";

    let (_, dag_builder) = parse_dag(dag_str).expect("Invalid dag");

    // Now take all the blocks for round 7 & 8 , which are above the gc_round = 6.
    // All those blocks should eventually be returned as accepted. Pay attention that without GC none of those blocks should get accepted.
    let blocks_ranges = vec![7..=8 as Round, 9..=10 as Round];

    for rounds_range in blocks_ranges {
        let all_blocks = dag_builder
            .blocks
            .values()
            .filter(|block| rounds_range.contains(&block.round()))
            .cloned()
            .collect::<Vec<_>>();

        // WHEN
        let mut reversed_blocks = all_blocks.clone();
        reversed_blocks.sort_by_key(|b| std::cmp::Reverse(b.reference()));
        let (mut accepted_blocks, missing) = block_manager.try_accept_blocks(reversed_blocks);
        accepted_blocks.sort_by_key(|a| a.reference());

        // THEN
        assert_eq!(accepted_blocks, all_blocks.to_vec());
        assert!(missing.is_empty());
        assert!(block_manager.is_empty());

        let (accepted_blocks, _) = block_manager.try_accept_blocks(all_blocks);
        assert!(accepted_blocks.is_empty());
    }
}

/// Blocks that are attempted to be accepted but are <= gc_round they will be skipped for processing. Nothing
/// should be stored or trigger any unsuspension etc.
#[tokio::test]
async fn skip_accepting_blocks_below_gc_round() {
    // GIVEN
    let (mut context, _key_pairs) = Context::new_for_test(4);
    // We set the gc depth to 4
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    // We "fake" the commit for round 10, so we can test the GC round 6 (commit_round - gc_depth = 10 - 4 = 6)
    let last_commit = TrustedCommit::new_for_test(
        10,
        CommitDigest::MIN,
        context.clock.timestamp_utc_ms(),
        BlockRef::new(10, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
        vec![],
        10, // global_exec_index for test
    );
    dag_state.write().set_last_commit(last_commit);
    assert_eq!(
        dag_state.read().gc_round(),
        6,
        "GC round should have moved to round 6"
    );

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG of 6 rounds
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder.layers(1..=6).build();

    let all_blocks = dag_builder.blocks.values().cloned().collect::<Vec<_>>();

    // WHEN
    let (accepted_blocks, missing) = block_manager.try_accept_blocks(all_blocks.clone());

    // THEN
    assert!(accepted_blocks.is_empty());
    assert!(missing.is_empty());
    assert!(block_manager.is_empty());
}

/// The test generate blocks for a well connected DAG and feed them to block manager in random order. In the end all the
/// blocks should be uniquely suspended and no missing blocks should exist. We set a high gc_depth value so in this test gc_round will be 0.
#[tokio::test]
async fn accept_blocks_unsuspend_children_blocks() {
    // GIVEN
    let (mut context, _key_pairs) = Context::new_for_test(4);
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(10);

    let context = Arc::new(context);

    // create a DAG of rounds 1 ~ 3
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder.layers(1..=3).build();

    let mut all_blocks = dag_builder.blocks.values().cloned().collect::<Vec<_>>();

    // Now randomize the sequence of sending the blocks to block manager. In the end all the blocks should be uniquely
    // suspended and no missing blocks should exist.
    for seed in 0..100u8 {
        all_blocks.shuffle(&mut StdRng::from_seed([seed; 32]));

        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

        let mut block_manager = BlockManager::new(context.clone(), dag_state);

        // WHEN
        let mut all_accepted_blocks = vec![];
        for block in &all_blocks {
            let (accepted_blocks, _) = block_manager.try_accept_blocks(vec![block.clone()]);

            all_accepted_blocks.extend(accepted_blocks);
        }

        // THEN
        all_accepted_blocks.sort_by_key(|b| b.reference());
        all_blocks.sort_by_key(|b| b.reference());

        assert_eq!(
            all_accepted_blocks, all_blocks,
            "Failed acceptance sequence for seed {}",
            seed
        );
        assert!(block_manager.is_empty());
    }
}

#[rstest]
#[tokio::test]
async fn unsuspend_blocks_for_latest_gc_round(#[values(5, 10, 14)] gc_depth: u32) {
    // // // // telemetry_subscribers::init_for_testing();
    // GIVEN
    let (mut context, _key_pairs) = Context::new_for_test(4);
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(gc_depth);

    let context = Arc::new(context);

    // create a DAG of rounds 1 ~ gc_depth * 2
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder.layers(1..=gc_depth * 2).build();

    // Pay attention that we start from round 2. Round 1 will always be missing so no matter what we do we can't unsuspend it unless
    // gc_round has advanced to round >= 1.
    let mut all_blocks = dag_builder
        .blocks
        .values()
        .filter(|block| block.round() > 1)
        .cloned()
        .collect::<Vec<_>>();

    // Now randomize the sequence of sending the blocks to block manager. In the end all the blocks should be uniquely
    // suspended and no missing blocks should exist.
    for seed in 0..100u8 {
        all_blocks.shuffle(&mut StdRng::from_seed([seed; 32]));

        let store = Arc::new(MemStore::new());
        let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

        let mut block_manager = BlockManager::new(context.clone(), dag_state.clone());

        // WHEN
        for block in &all_blocks {
            let (accepted_blocks, _) = block_manager.try_accept_blocks(vec![block.clone()]);
            assert!(accepted_blocks.is_empty());
        }
        assert!(!block_manager.is_empty());

        // AND also call the try_to_find method with some non existing block refs. Those should be cleaned up as well once GC kicks in.
        let non_existing_refs = (1..=3)
            .map(|round| {
                BlockRef::new(round, AuthorityIndex::new_for_test(0), BlockDigest::MIN)
            })
            .collect::<Vec<_>>();
        assert_eq!(block_manager.try_find_blocks(non_existing_refs).len(), 3);

        // AND
        // Trigger a commit which will advance GC round
        let last_commit = TrustedCommit::new_for_test(
            gc_depth * 2,
            CommitDigest::MIN,
            context.clock.timestamp_utc_ms(),
            BlockRef::new(
                gc_depth * 2,
                AuthorityIndex::new_for_test(0),
                BlockDigest::MIN,
            ),
            vec![],
            (gc_depth * 2) as u64, // global_exec_index for test
        );
        dag_state.write().set_last_commit(last_commit);

        // AND
        block_manager.try_unsuspend_blocks_for_latest_gc_round();

        // THEN
        assert!(block_manager.is_empty());

        // AND ensure that all have been accepted to the DAG
        for block in &all_blocks {
            assert!(dag_state.read().contains_block(&block.reference()));
        }
    }
}

#[rstest]
#[tokio::test]
async fn try_accept_committed_blocks() {
    // GIVEN
    let (mut context, _key_pairs) = Context::new_for_test(4);
    // We set the gc depth to 4
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    // We "fake" the commit for round 6, so GC round moves to (commit_round - gc_depth = 6 - 4 = 2)
    let last_commit = TrustedCommit::new_for_test(
        10,
        CommitDigest::MIN,
        context.clock.timestamp_utc_ms(),
        BlockRef::new(6, AuthorityIndex::new_for_test(0), BlockDigest::MIN),
        vec![],
        10, // global_exec_index for test
    );
    dag_state.write().set_last_commit(last_commit);
    assert_eq!(
        dag_state.read().gc_round(),
        2,
        "GC round should have moved to round 2"
    );

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG of 12 rounds
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder.layers(1..=12).build();

    // Now try to accept via the normal acceptance block path the blocks of rounds 7 ~ 12. None of them should be accepted
    let blocks = dag_builder.blocks(7..=12);
    let (accepted_blocks, missing) = block_manager.try_accept_blocks(blocks.clone());
    assert!(accepted_blocks.is_empty());
    assert_eq!(missing.len(), 4);

    // Now try to accept via the committed blocks path the blocks of rounds 3 ~ 6. All of them should be accepted and also the blocks
    // of rounds 7 ~ 12 should be unsuspended and accepted as well.
    let blocks = dag_builder.blocks(3..=6);

    // WHEN
    let mut accepted_blocks = block_manager.try_accept_committed_blocks(blocks);

    // THEN
    accepted_blocks.sort_by_key(|b| b.reference());

    let mut all_blocks = dag_builder.blocks(3..=12);
    all_blocks.sort_by_key(|b| b.reference());

    assert_eq!(accepted_blocks, all_blocks);
    assert!(block_manager.is_empty());
}

#[tokio::test]
async fn try_find_blocks() {
    // GIVEN
    let (context, _key_pairs) = Context::new_for_test(4);
    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state);

    // create a DAG
    let mut dag_builder = DagBuilder::new(context.clone());
    dag_builder
        .layers(1..=2) // 2 rounds
        .authorities(vec![
            AuthorityIndex::new_for_test(0),
            AuthorityIndex::new_for_test(2),
        ]) // Create equivocating blocks for 2 authorities
        .equivocate(3)
        .build();

    // Take only the blocks of round 2 and try to accept them
    let round_2_blocks = dag_builder
        .blocks
        .iter()
        .filter_map(|(_, block)| (block.round() == 2).then_some(block.clone()))
        .collect::<Vec<VerifiedBlock>>();

    // All blocks should be missing
    let missing_block_refs_from_find =
        block_manager.try_find_blocks(round_2_blocks.iter().map(|b| b.reference()).collect());
    assert_eq!(missing_block_refs_from_find.len(), 10);
    assert!(missing_block_refs_from_find
        .iter()
        .all(|block_ref| block_ref.round == 2));

    // Try accept blocks which will cause blocks to be suspended and added to missing
    // in block manager.
    let (accepted_blocks, missing) = block_manager.try_accept_blocks(round_2_blocks.clone());
    assert!(accepted_blocks.is_empty());

    let missing_block_refs = round_2_blocks.first().unwrap().ancestors();
    let missing_block_refs_from_accept =
        missing_block_refs.iter().cloned().collect::<BTreeSet<_>>();
    assert_eq!(missing, missing_block_refs_from_accept);
    assert_eq!(
        block_manager.missing_blocks(),
        missing_block_refs_from_accept
    );

    // No blocks should be accepted and block manager should have made note
    // of the missing & suspended blocks.
    // Now we can check get the result of try find block with all of the blocks
    // from newly created but not accepted round 3.
    dag_builder.layer(3).build();

    let round_3_blocks = dag_builder
        .blocks
        .iter()
        .filter_map(|(_, block)| (block.round() == 3).then_some(block.reference()))
        .collect::<Vec<BlockRef>>();

    let missing_block_refs_from_find = block_manager.try_find_blocks(
        round_2_blocks
            .iter()
            .map(|b| b.reference())
            .chain(round_3_blocks.into_iter())
            .collect(),
    );

    assert_eq!(missing_block_refs_from_find.len(), 4);
    assert!(missing_block_refs_from_find
        .iter()
        .all(|block_ref| block_ref.round == 3));
    assert_eq!(
        block_manager.missing_blocks(),
        missing_block_refs_from_accept
            .into_iter()
            .chain(missing_block_refs_from_find.into_iter())
            .collect()
    );
}

#[rstest]
#[tokio::test]
async fn test_verify_block_timestamps_and_accept() {
    // // // // telemetry_subscribers::init_for_testing();
    let (context, _key_pairs) = Context::new_for_test(4);

    let context = Arc::new(context);
    let store = Arc::new(MemStore::new());
    let dag_state = Arc::new(RwLock::new(DagState::new(context.clone(), store.clone())));

    let mut block_manager = BlockManager::new(context.clone(), dag_state.clone());

    // create a DAG where authority 0 timestamp is always higher than the others.
    let mut dag_builder = DagBuilder::new(context.clone());
    let authorities = context
        .committee
        .authorities()
        .map(|(index, _)| index)
        .collect::<Vec<_>>();
    dag_builder
        .layers(1..=1)
        .authorities(authorities.clone())
        .with_timestamps(vec![1000, 500, 550, 580])
        .build();
    dag_builder
        .layers(2..=2)
        .authorities(authorities.clone())
        .with_timestamps(vec![2000, 600, 650, 680])
        .build();
    dag_builder
        .layers(3..=3)
        .authorities(authorities)
        .with_timestamps(vec![3000, 700, 750, 780])
        .build();

    // take all the blocks and try to accept them.
    let all_blocks = dag_builder.blocks.values().cloned().collect::<Vec<_>>();

    // All blocks should get accepted
    let (accepted_blocks, missing) = block_manager.try_accept_blocks(all_blocks.clone());

    // If the median based timestamp is enabled then all the blocks should be accepted
    assert_eq!(all_blocks, accepted_blocks);
    assert!(missing.is_empty());
}
}
