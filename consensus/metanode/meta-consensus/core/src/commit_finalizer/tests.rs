// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

mod tests {
use mysten_metrics::monitored_mpsc;
use parking_lot::RwLock;

use crate::{
    block::{BlockAPI, BlockTransactionVotes}, block_verifier::NoopBlockVerifier, dag_state::DagState,
    linearizer::Linearizer, storage::mem_store::MemStore, test_dag_builder::DagBuilder,
    TestBlock, VerifiedBlock,
};

use super::*;

struct Fixture {
    context: Arc<Context>,
    dag_state: Arc<RwLock<DagState>>,
    transaction_certifier: TransactionCertifier,
    linearizer: Linearizer,
    commit_finalizer: CommitFinalizer,
}

impl Fixture {
    fn add_blocks(&self, blocks: Vec<VerifiedBlock>) {
        self.transaction_certifier
            .add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());
        self.dag_state.write().accept_blocks(blocks);
    }
}

fn create_commit_finalizer_fixture() -> Fixture {
    let (mut context, _keys) = Context::new_for_test(4);
    context
        .protocol_config
        .set_consensus_gc_depth_for_testing(5);
    context
        .protocol_config
        .set_consensus_skip_gced_accept_votes_for_testing(true);
    let context = Arc::new(context);
    let dag_state = Arc::new(RwLock::new(DagState::new(
        context.clone(),
        Arc::new(MemStore::new()),
    )));
    let linearizer = Linearizer::new(context.clone(), dag_state.clone());
    let (blocks_sender, _blocks_receiver) =
        monitored_mpsc::unbounded_channel("consensus_block_output");
    let transaction_certifier = TransactionCertifier::new(
        context.clone(),
        Arc::new(NoopBlockVerifier {}),
        dag_state.clone(),
        blocks_sender,
    );
    let (commit_sender, _commit_receiver) = unbounded_channel("consensus_commit_output");
    let commit_finalizer = CommitFinalizer::new(
        context.clone(),
        dag_state.clone(),
        transaction_certifier.clone(),
        commit_sender,
        None,
    );
    Fixture {
        context,
        dag_state,
        transaction_certifier,
        linearizer,
        commit_finalizer,
    }
}

fn create_block(
    round: Round,
    authority: u32,
    mut ancestors: Vec<BlockRef>,
    num_transactions: usize,
    reject_votes: Vec<BlockTransactionVotes>,
) -> VerifiedBlock {
    // Move own authority ancestor to the front of the ancestors.
    let i = ancestors
        .iter()
        .position(|b| b.author.value() == authority as usize)
        .unwrap_or_else(|| {
            panic!("Authority {authority} (round {round}) not found in {ancestors:?}")
        });
    let b = ancestors.remove(i);
    ancestors.insert(0, b);
    // Create test block.
    let block = TestBlock::new(round, authority)
        .set_ancestors(ancestors)
        .set_transactions(vec![crate::Transaction::new(vec![1; 16]); num_transactions])
        .set_transaction_votes(reject_votes)
        .build();
    VerifiedBlock::new_for_test(block)
}

#[tokio::test]
async fn test_direct_finalize_no_reject_votes() {
    let mut fixture = create_commit_finalizer_fixture();

    // Create round 1-4 blocks with 10 transactions each. Add these blocks to transaction certifier.
    let mut dag_builder = DagBuilder::new(fixture.context.clone());
    dag_builder
        .layers(1..=4)
        .num_transactions(10)
        .build()
        .persist_layers(fixture.dag_state.clone());
    let blocks = dag_builder.all_blocks();
    fixture
        .transaction_certifier
        .add_voted_blocks(blocks.iter().map(|b| (b.clone(), vec![])).collect());

    // Select a round 2 block as the leader and create CommittedSubDag.
    let leader = blocks.iter().find(|b| b.round() == 2).unwrap();
    let committed_sub_dags = fixture.linearizer.handle_commit(vec![leader.clone()], None);
    assert_eq!(committed_sub_dags.len(), 1);
    let committed_sub_dag = &committed_sub_dags[0];

    // This committed sub-dag can be directly finalized.
    let finalized_commits = fixture
        .commit_finalizer
        .process_commit(committed_sub_dag.clone())
        .await;
    assert_eq!(finalized_commits.len(), 1);
    let finalized_commit = &finalized_commits[0];
    assert_eq!(committed_sub_dag, finalized_commit);

    // CommitFinalizer should be empty.
    assert!(fixture.commit_finalizer.is_empty());
}

// Commits can be directly finalized if when they are added to commit finalizer,
// the rejected votes reach quorum if they exist on any transaction.
#[tokio::test]
async fn test_direct_finalize_with_reject_votes() {
    let mut fixture = create_commit_finalizer_fixture();

    // Create round 1 blocks with 10 transactions each.
    let mut dag_builder = DagBuilder::new(fixture.context.clone());
    dag_builder
        .layer(1)
        .num_transactions(10)
        .build()
        .persist_layers(fixture.dag_state.clone());
    let round_1_blocks = dag_builder.all_blocks();
    fixture.transaction_certifier.add_voted_blocks(
        round_1_blocks
            .iter()
            .map(|b| {
                if b.author().value() != 3 {
                    (b.clone(), vec![])
                } else {
                    (b.clone(), vec![0, 3])
                }
            })
            .collect(),
    );

    // Select the block with rejected transaction.
    let block_with_rejected_txn = round_1_blocks[3].clone();
    let reject_vote = BlockTransactionVotes {
        block_ref: block_with_rejected_txn.reference(),
        rejects: vec![0, 3],
    };

    // Create round 2 blocks without authority 3's block from round 1.
    let ancestors: Vec<BlockRef> = round_1_blocks[0..3].iter().map(|b| b.reference()).collect();
    // Leader links to block_with_rejected_txn, but other blocks do not.
    let round_2_blocks = vec![
        create_block(
            2,
            0,
            round_1_blocks.iter().map(|b| b.reference()).collect(),
            10,
            vec![reject_vote.clone()],
        ),
        create_block(2, 1, ancestors.clone(), 10, vec![]),
        create_block(2, 2, ancestors.clone(), 10, vec![]),
    ];
    fixture.add_blocks(round_2_blocks.clone());

    // Select round 2 authority 0 block as the leader and create CommittedSubDag.
    let leader = round_2_blocks[0].clone();
    let committed_sub_dags = fixture.linearizer.handle_commit(vec![leader.clone()], None);
    assert_eq!(committed_sub_dags.len(), 1);
    let committed_sub_dag = &committed_sub_dags[0];
    assert_eq!(committed_sub_dag.blocks.len(), 5);

    // Create round 3 blocks voting on the leader.
    let ancestors: Vec<BlockRef> = round_2_blocks.iter().map(|b| b.reference()).collect();
    let round_3_blocks = vec![
        create_block(3, 0, ancestors.clone(), 0, vec![]),
        create_block(3, 1, ancestors.clone(), 0, vec![reject_vote.clone()]),
        create_block(3, 2, ancestors.clone(), 0, vec![reject_vote.clone()]),
        create_block(
            3,
            3,
            std::iter::once(round_1_blocks[3].reference())
                .chain(ancestors.clone())
                .collect(),
            0,
            vec![reject_vote.clone()],
        ),
    ];
    fixture.add_blocks(round_3_blocks.clone());

    // Create round 4 blocks certifying the leader.
    let ancestors: Vec<BlockRef> = round_3_blocks.iter().map(|b| b.reference()).collect();
    let round_4_blocks = vec![
        create_block(4, 0, ancestors.clone(), 0, vec![]),
        create_block(4, 1, ancestors.clone(), 0, vec![]),
        create_block(4, 2, ancestors.clone(), 0, vec![]),
        create_block(4, 3, ancestors.clone(), 0, vec![]),
    ];
    fixture.add_blocks(round_4_blocks.clone());

    // This committed sub-dag can be directly finalized because the rejected transactions
    // have a quorum of votes.
    let finalized_commits = fixture
        .commit_finalizer
        .process_commit(committed_sub_dag.clone())
        .await;
    assert_eq!(finalized_commits.len(), 1);
    let finalized_commit = &finalized_commits[0];
    assert_eq!(committed_sub_dag.commit_ref, finalized_commit.commit_ref);
    assert_eq!(committed_sub_dag.blocks, finalized_commit.blocks);
    assert_eq!(finalized_commit.rejected_transactions_by_block.len(), 1);
    assert_eq!(
        finalized_commit
            .rejected_transactions_by_block
            .get(&block_with_rejected_txn.reference())
            .unwrap()
            .clone(),
        vec![0, 3],
    );

    // CommitFinalizer should be empty.
    assert!(fixture.commit_finalizer.is_empty());
}

// Test indirect finalization when:
// 1. Reject votes on transaction does not reach quorum initially, but reach quorum later.
// 2. Transaction is indirectly rejected.
// 3. Transaction is indirectly finalized.
#[tokio::test]
async fn test_indirect_finalize_with_reject_votes() {
    let mut fixture = create_commit_finalizer_fixture();

    // Create round 1 blocks with 10 transactions each.
    let mut dag_builder = DagBuilder::new(fixture.context.clone());
    dag_builder
        .layer(1)
        .num_transactions(10)
        .build()
        .persist_layers(fixture.dag_state.clone());
    let round_1_blocks = dag_builder.all_blocks();
    fixture.transaction_certifier.add_voted_blocks(
        round_1_blocks
            .iter()
            .map(|b| {
                if b.author().value() != 3 {
                    (b.clone(), vec![])
                } else {
                    (b.clone(), vec![0, 3])
                }
            })
            .collect(),
    );

    // Select the block with rejected transaction.
    let block_with_rejected_txn = round_1_blocks[3].clone();
    // How transactions in this block will be voted:
    // Txn 1 (quorum reject): 1 reject vote at round 2, 1 reject vote at round 3, and 1 at round 4.
    // Txn 4 (indirect reject): 1 reject vote at round 3, and 1 at round 4.
    // Txn 7 (indirect finalize): 1 reject vote at round 3.

    // Create round 2 blocks without authority 3.
    let ancestors: Vec<BlockRef> = round_1_blocks[0..3].iter().map(|b| b.reference()).collect();
    // Leader links to block_with_rejected_txn, but other blocks do not.
    let round_2_blocks = vec![
        create_block(
            2,
            0,
            round_1_blocks.iter().map(|b| b.reference()).collect(),
            10,
            vec![BlockTransactionVotes {
                block_ref: block_with_rejected_txn.reference(),
                rejects: vec![1, 4],
            }],
        ),
        // Use ancestors without authority 3 to avoid voting on its transactions.
        create_block(2, 1, ancestors.clone(), 10, vec![]),
        create_block(2, 2, ancestors.clone(), 10, vec![]),
    ];
    fixture.add_blocks(round_2_blocks.clone());

    // Select round 2 authority 0 block as the a leader.
    let mut leaders = vec![round_2_blocks[0].clone()];

    // Create round 3 blocks voting on the leader and casting reject votes.
    let ancestors: Vec<BlockRef> = round_2_blocks.iter().map(|b| b.reference()).collect();
    let round_3_blocks = vec![
        create_block(3, 0, ancestors.clone(), 0, vec![]),
        create_block(
            3,
            1,
            ancestors.clone(),
            0,
            vec![BlockTransactionVotes {
                block_ref: block_with_rejected_txn.reference(),
                rejects: vec![1, 4, 7],
            }],
        ),
        create_block(
            3,
            3,
            std::iter::once(round_1_blocks[3].reference())
                .chain(ancestors.clone())
                .collect(),
            0,
            vec![],
        ),
    ];
    fixture.add_blocks(round_3_blocks.clone());
    leaders.push(round_3_blocks[2].clone());

    // Create round 4 blocks certifying the leader and casting reject votes.
    let ancestors: Vec<BlockRef> = round_3_blocks.iter().map(|b| b.reference()).collect();
    let round_4_blocks = vec![
        create_block(4, 0, ancestors.clone(), 0, vec![]),
        create_block(4, 1, ancestors.clone(), 0, vec![]),
        create_block(
            4,
            2,
            std::iter::once(round_2_blocks[2].reference())
                .chain(ancestors.clone())
                .collect(),
            0,
            vec![BlockTransactionVotes {
                block_ref: block_with_rejected_txn.reference(),
                rejects: vec![1],
            }],
        ),
        create_block(4, 3, ancestors.clone(), 0, vec![]),
    ];
    fixture.add_blocks(round_4_blocks.clone());
    leaders.push(round_4_blocks[1].clone());

    // Create round 5-7 blocks without casting reject votes.
    // Select the last leader from round 5. It is necessary to have round 5 leader to indirectly finalize
    // transactions committed by round 2 leader.
    let mut last_round_blocks = round_4_blocks.clone();
    for r in 5..=7 {
        let ancestors: Vec<BlockRef> =
            last_round_blocks.iter().map(|b| b.reference()).collect();
        let round_blocks: Vec<_> = (0..4)
            .map(|i| create_block(r, i, ancestors.clone(), 0, vec![]))
            .collect();
        fixture.add_blocks(round_blocks.clone());
        if r == 5 {
            leaders.push(round_blocks[0].clone());
        }
        last_round_blocks = round_blocks;
    }

    // Create CommittedSubDag from leaders.
    assert_eq!(leaders.len(), 4);
    let committed_sub_dags = fixture.linearizer.handle_commit(leaders, None);
    assert_eq!(committed_sub_dags.len(), 4);

    // Buffering the initial 3 commits should not finalize.
    for commit in committed_sub_dags.iter().take(3) {
        let finalized_commits = fixture
            .commit_finalizer
            .process_commit(commit.clone())
            .await;
        assert_eq!(finalized_commits.len(), 0);
    }

    // Buffering the 4th commit should finalize all commits.
    let finalized_commits = fixture
        .commit_finalizer
        .process_commit(committed_sub_dags[3].clone())
        .await;
    assert_eq!(finalized_commits.len(), 4);

    // Check rejected transactions.
    let rejected_transactions = finalized_commits[0].rejected_transactions_by_block.clone();
    assert_eq!(rejected_transactions.len(), 1);
    assert_eq!(
        rejected_transactions
            .get(&block_with_rejected_txn.reference())
            .unwrap(),
        &vec![1, 4]
    );

    // Other commits should have no rejected transactions.
    for commit in finalized_commits.iter().skip(1) {
        assert!(commit.rejected_transactions_by_block.is_empty());
    }

    // CommitFinalizer should be empty.
    assert!(fixture.commit_finalizer.is_empty());
}

// Test indirect finalization when transaction is rejected due to GC.
#[tokio::test]
async fn test_indirect_reject_with_gc() {
    let mut fixture = create_commit_finalizer_fixture();
    assert_eq!(fixture.context.protocol_config.consensus_gc_depth(), 5);

    // Create round 1 blocks with 10 transactions each.
    let mut dag_builder = DagBuilder::new(fixture.context.clone());
    dag_builder
        .layer(1)
        .num_transactions(10)
        .build()
        .persist_layers(fixture.dag_state.clone());
    let round_1_blocks = dag_builder.all_blocks();
    fixture
        .transaction_certifier
        .add_voted_blocks(round_1_blocks.iter().map(|b| (b.clone(), vec![])).collect());

    // Select B1(3) to have a rejected transaction.
    let block_with_rejected_txn = round_1_blocks[3].clone();
    // How transactions in this block will be voted:
    // Txn 1 (GC reject): 1 reject vote at round 2. But the txn will get rejected because there are only
    // 2 accept votes.

    // Create round 2 blocks, with B2(1) rejecting transaction 1 from B1(3).
    // Note that 3 blocks link to B1(3) without rejecting transaction 1.
    let ancestors: Vec<BlockRef> = round_1_blocks.iter().map(|b| b.reference()).collect();
    let round_2_blocks = vec![
        create_block(2, 0, ancestors.clone(), 0, vec![]),
        create_block(
            2,
            1,
            ancestors.clone(),
            0,
            vec![BlockTransactionVotes {
                block_ref: block_with_rejected_txn.reference(),
                rejects: vec![1],
            }],
        ),
        create_block(2, 2, ancestors.clone(), 0, vec![]),
        create_block(2, 3, ancestors.clone(), 0, vec![]),
    ];
    fixture.add_blocks(round_2_blocks.clone());

    // Create round 3-6 blocks without creating or linking to an authority 2 block.
    // The goal is to GC B2(2).
    let mut last_round_blocks: Vec<VerifiedBlock> = round_2_blocks
        .iter()
        .enumerate()
        .filter_map(|(i, b)| if i != 2 { Some(b.clone()) } else { None })
        .collect();
    for r in 3..=6 {
        let ancestors: Vec<BlockRef> =
            last_round_blocks.iter().map(|b| b.reference()).collect();
        last_round_blocks = [0, 1, 3]
            .map(|i| create_block(r, i, ancestors.clone(), 0, vec![]))
            .to_vec();
        fixture.add_blocks(last_round_blocks.clone());
    }

    // Create round 7-10 blocks and add a leader from authority 0 of each round.
    let mut leaders = vec![];
    for r in 7..=10 {
        let mut ancestors: Vec<BlockRef> =
            last_round_blocks.iter().map(|b| b.reference()).collect();
        last_round_blocks = (0..4)
            .map(|i| {
                if r == 7 && i == 2 {
                    // Link to the GC'ed block B2(2).
                    ancestors.push(round_2_blocks[2].reference());
                }
                create_block(r, i, ancestors.clone(), 0, vec![])
            })
            .collect();
        leaders.push(last_round_blocks[0].clone());
        fixture.add_blocks(last_round_blocks.clone());
    }

    // Create CommittedSubDag from leaders.
    assert_eq!(leaders.len(), 4);
    let committed_sub_dags = fixture.linearizer.handle_commit(leaders, None);
    assert_eq!(committed_sub_dags.len(), 4);

    // Ensure 1 reject vote is contained in B2(1) in commit 0.
    assert!(committed_sub_dags[0].blocks.contains(&round_2_blocks[1]));
    // Ensure B2(2) is GC'ed.
    for commit in committed_sub_dags.iter() {
        assert!(!commit.blocks.contains(&round_2_blocks[2]));
    }

    // Buffering the initial 3 commits should not finalize.
    for commit in committed_sub_dags.iter().take(3) {
        assert!(commit.decided_with_local_blocks);
        let finalized_commits = fixture
            .commit_finalizer
            .process_commit(commit.clone())
            .await;
        assert_eq!(finalized_commits.len(), 0);
    }

    // Buffering the 4th commit should finalize all commits.
    let finalized_commits = fixture
        .commit_finalizer
        .process_commit(committed_sub_dags[3].clone())
        .await;
    assert_eq!(finalized_commits.len(), 4);

    // Check rejected transactions.
    // B1(3) txn 1 gets rejected, even though there are has 3 blocks links to B1(3) without rejecting txn 1.
    // This is because there are only 2 accept votes for this transaction, which is less than the quorum threshold.
    let rejected_transactions = finalized_commits[0].rejected_transactions_by_block.clone();
    assert_eq!(rejected_transactions.len(), 1);
    assert_eq!(
        rejected_transactions
            .get(&block_with_rejected_txn.reference())
            .unwrap(),
        &vec![1]
    );

    // Other commits should have no rejected transactions.
    for commit in finalized_commits.iter().skip(1) {
        assert!(commit.rejected_transactions_by_block.is_empty());
    }

    // CommitFinalizer should be empty.
    assert!(fixture.commit_finalizer.is_empty());
}

#[tokio::test]
async fn test_finalize_remote_commits_with_reject_votes() {
    let mut fixture: Fixture = create_commit_finalizer_fixture();
    let mut all_blocks = vec![];

    // Create round 1 blocks with 10 transactions each.
    let mut dag_builder = DagBuilder::new(fixture.context.clone());
    dag_builder.layer(1).num_transactions(10).build();
    let round_1_blocks = dag_builder.all_blocks();
    all_blocks.push(round_1_blocks.clone());

    // Collect leaders from round 1.
    let mut leaders = vec![round_1_blocks[0].clone()];

    // Create round 2-9 blocks and set leaders until round 7.
    let mut last_round_blocks = round_1_blocks.clone();
    for r in 2..=9 {
        let ancestors: Vec<BlockRef> =
            last_round_blocks.iter().map(|b| b.reference()).collect();
        let round_blocks: Vec<_> = (0..4)
            .map(|i| create_block(r, i, ancestors.clone(), 0, vec![]))
            .collect();
        all_blocks.push(round_blocks.clone());
        if r <= 7 && r != 5 {
            leaders.push(round_blocks[r as usize % 4].clone());
        }
        last_round_blocks = round_blocks;
    }

    // Leader rounds: 1, 2, 3, 4, 6, 7.
    assert_eq!(leaders.len(), 6);

    async fn add_blocks_and_process_commit(
        fixture: &mut Fixture,
        leaders: &[VerifiedBlock],
        all_blocks: &[Vec<VerifiedBlock>],
        index: usize,
        local: bool,
    ) -> Vec<CommittedSubDag> {
        let leader = leaders[index].clone();
        // Add blocks related to the commit to DagState and TransactionCertifier.
        if local {
            for round_blocks in all_blocks.iter().take(leader.round() as usize + 2) {
                fixture.add_blocks(round_blocks.clone());
            }
        } else {
            for round_blocks in all_blocks.iter().take(leader.round() as usize) {
                fixture.add_blocks(round_blocks.clone());
            }
        };
        // Generate remote commit from leader.
        let mut committed_sub_dags = fixture.linearizer.handle_commit(vec![leader], None);
        assert_eq!(committed_sub_dags.len(), 1);
        let mut remote_commit = committed_sub_dags.pop().unwrap();
        remote_commit.decided_with_local_blocks = local;
        // Process the remote commit.
        fixture
            .commit_finalizer
            .process_commit(remote_commit.clone())
            .await
    }

    // Add commit 1-3 as remote commits. There should be no finalized commits.
    for i in 0..3 {
        let finalized_commits =
            add_blocks_and_process_commit(&mut fixture, &leaders, &all_blocks, i, false).await;
        assert!(finalized_commits.is_empty());
    }

    // Buffer round 4 commit as a remote commit. This should finalize the 1st commit at round 1.
    let finalized_commits =
        add_blocks_and_process_commit(&mut fixture, &leaders, &all_blocks, 3, false).await;
    assert_eq!(finalized_commits.len(), 1);
    assert_eq!(finalized_commits[0].commit_ref.index, 1);
    assert_eq!(finalized_commits[0].leader.round, 1);

    // Buffer round 6 (5th) commit as local commit. This should help finalize the commits at round 2 and 3.
    let finalized_commits =
        add_blocks_and_process_commit(&mut fixture, &leaders, &all_blocks, 4, true).await;
    assert_eq!(finalized_commits.len(), 2);
    assert_eq!(finalized_commits[0].commit_ref.index, 2);
    assert_eq!(finalized_commits[0].leader.round, 2);
    assert_eq!(finalized_commits[1].commit_ref.index, 3);
    assert_eq!(finalized_commits[1].leader.round, 3);

    // Buffer round 7 (6th) commit as local commit. This should help finalize the commits at round 4, 6 and 7 (itself).
    let finalized_commits =
        add_blocks_and_process_commit(&mut fixture, &leaders, &all_blocks, 5, true).await;
    assert_eq!(finalized_commits.len(), 3);
    assert_eq!(finalized_commits[0].commit_ref.index, 4);
    assert_eq!(finalized_commits[0].leader.round, 4);
    assert_eq!(finalized_commits[1].commit_ref.index, 5);
    assert_eq!(finalized_commits[1].leader.round, 6);
    assert_eq!(finalized_commits[2].commit_ref.index, 6);
    assert_eq!(finalized_commits[2].leader.round, 7);

    // CommitFinalizer should be empty.
    assert!(fixture.commit_finalizer.is_empty());
}
}
