// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use std::{
    collections::{BTreeMap, BTreeSet, VecDeque},
    sync::Arc,
};

use consensus_config::Stake;
use consensus_types::block::{BlockRef, Round, TransactionIndex};
use parking_lot::RwLock;
use tokio::task::JoinSet;
use tokio::sync::mpsc::{UnboundedSender, UnboundedReceiver, unbounded_channel};

use crate::{
    commit::DEFAULT_WAVE_LENGTH,
    context::Context,
    dag_state::DagState,
    error::{ConsensusError, ConsensusResult},
    stake_aggregator::{QuorumThreshold, StakeAggregator},
    transaction_certifier::TransactionCertifier,
    BlockAPI, CommitIndex, CommittedSubDag,
};

/// For transaction T committed at leader round R, when a new leader at round >= R + INDIRECT_REJECT_DEPTH
/// commits and T is still not finalized, T is rejected.
/// NOTE: 3 round is the minimum depth possible for indirect finalization and rejection.
pub(crate) const INDIRECT_REJECT_DEPTH: Round = 3;


pub mod types;
#[cfg(test)]
mod tests;

use types::{CommitState, BlockState};

/// Handle to CommitFinalizer, for sending CommittedSubDag.
pub(crate) struct CommitFinalizerHandle {
    sender: UnboundedSender<CommittedSubDag>,
}

impl CommitFinalizerHandle {
    // Sends a CommittedSubDag to CommitFinalizer, which will finalize it before sending it to execution.
    pub(crate) fn send(&self, commit: CommittedSubDag) -> ConsensusResult<()> {
        self.sender.send(commit).map_err(|e| {
            tracing::warn!("Failed to send to commit finalizer, probably due to shutdown: {e:?}");
            ConsensusError::Shutdown
        })
    }
}

/// CommitFinalizer accepts a continuous stream of CommittedSubDag and outputs
/// them when they are finalized.
/// In finalized commits, every transaction is either finalized or rejected.
/// It runs in a separate thread, to reduce the load on the core thread.
///
/// Life of a finalized commit:
///
/// For efficiency, finalization happens first for transactions without reject votes (common case).
/// The pending undecided transactions with reject votes are individually finalized or rejected.
/// When there is no more pending transactions, the commit is finalized.
///
/// This is correct because regardless if a commit leader was directly or indirectly committed,
/// every committed block can be considered finalized, because at least one leader certificate of the commit
/// will be committed, which can also serve as a certificate for the block and its transactions.
///
/// From the earliest buffered commit, pending blocks are checked to see if they are now finalized.
/// New finalized blocks are removed from the pending blocks, and its transactions are moved to the
/// finalized, rejected or pending state. If the commit now has no pending blocks or transactions,
/// the commit is finalized and popped from the buffer. The next earliest commit is then processed
/// similarly, until either the buffer becomes empty or a commit with pending blocks or transactions
/// is encountered.
pub struct CommitFinalizer {
    context: Arc<Context>,
    dag_state: Arc<RwLock<DagState>>,
    transaction_certifier: TransactionCertifier,
    commit_sender: UnboundedSender<CommittedSubDag>,

    // Last commit index processed by CommitFinalizer.
    last_processed_commit: Option<CommitIndex>,
    // Commits pending finalization.
    pending_commits: VecDeque<CommitState>,
    // Blocks in the pending commits.
    blocks: Arc<RwLock<BTreeMap<BlockRef, RwLock<BlockState>>>>,
    // Keeper for internal channel sender to prevent race condition.
    // This ensures the internal channel stays open as long as the CommitFinalizer task is running.
    #[allow(dead_code)]
    internal_sender_keeper: Option<UnboundedSender<CommittedSubDag>>,
}

impl CommitFinalizer {
    pub fn new(
        context: Arc<Context>,
        dag_state: Arc<RwLock<DagState>>,
        transaction_certifier: TransactionCertifier,
        commit_sender: UnboundedSender<CommittedSubDag>,
        last_processed_commit: Option<CommitIndex>,
    ) -> Self {
        Self {
            context,
            dag_state,
            transaction_certifier,
            commit_sender,
            last_processed_commit,
            pending_commits: VecDeque::new(),
            blocks: Arc::new(RwLock::new(BTreeMap::new())),
            internal_sender_keeper: None,
        }
    }

    pub(crate) fn start(
        context: Arc<Context>,
        dag_state: Arc<RwLock<DagState>>,
        transaction_certifier: TransactionCertifier,
        commit_sender: UnboundedSender<CommittedSubDag>,
        last_processed_commit: Option<CommitIndex>,
    ) -> CommitFinalizerHandle {
        let mut processor = Self::new(context, dag_state, transaction_certifier, commit_sender, last_processed_commit);
        let (sender, receiver) = unbounded_channel();
        // Clone the sender and store it in the processor to prevent race condition.
        // This ensures the internal channel stays open until the task starts running.
        processor.internal_sender_keeper = Some(sender.clone());
        let _handle =
            tokio::spawn(processor.run(receiver));
        CommitFinalizerHandle { sender }
    }

    async fn run(mut self, mut receiver: UnboundedReceiver<CommittedSubDag>) {
        tracing::info!("🚀 [COMMIT FINALIZER] RUN LOOP STARTED");
        while let Some(committed_sub_dag) = receiver.recv().await {
            let already_finalized = !self.context.protocol_config.mysticeti_fastpath()
                || committed_sub_dag.recovered_rejected_transactions;
            let finalized_commits = if !already_finalized {
                self.process_commit(committed_sub_dag).await
            } else {
                vec![committed_sub_dag]
            };
            if !finalized_commits.is_empty() {
                // Transaction certifier state should be GC'ed as soon as new commits are finalized.
                // But this is done outside of process_commit(), because during recovery process_commit()
                // is not called to finalize commits, but GC still needs to run.
                self.try_update_gc_round(
                    finalized_commits
                        .last()
                        .expect("finalized_commits is non-empty (checked above)")
                        .leader
                        .round,
                );
                let flush_ticket = {
                    let mut dag_state = self.dag_state.write();
                    if !already_finalized {
                        // Records rejected transactions in newly finalized commits.
                        for commit in &finalized_commits {
                            dag_state.add_finalized_commit(
                                commit.commit_ref,
                                commit.rejected_transactions_by_block.clone(),
                            );
                        }
                    }
                    // Commits and committed blocks must be persisted to storage before sending them to Sui
                    // to execute their finalized transactions.
                    // Commit metadata and uncommitted blocks can be persisted more lazily because they are recoverable.
                    // But for simplicity, all unpersisted commits and blocks are flushed to storage.
                    dag_state.flush()
                };

                if let Some(rx) = flush_ticket {
                    if let Err(e) = rx.await {
                        tracing::warn!("Failed to wait for dag_state flush: {:?}", e);
                    }
                }
            }
            for commit in finalized_commits {
                if let Err(e) = self.commit_sender.send(commit) {
                    tracing::debug!(
                        "Failed to send commit to handler (likely epoch transition active): {e:?}"
                    );
                }
            }
        }
        tracing::info!("❌ [COMMIT FINALIZER] RUN LOOP ENDED (Receiver closed)");
    }

    pub async fn process_commit(
        &mut self,
        committed_sub_dag: CommittedSubDag,
    ) -> Vec<CommittedSubDag> {
        /* let _scope = tracing::info_span!("CommitFinalizer::process_commit").entered(); */

        if let Some(last_processed_commit) = self.last_processed_commit {
            let expected = last_processed_commit + 1;
            if committed_sub_dag.commit_ref.index != expected {
                if committed_sub_dag.commit_ref.index <= last_processed_commit {
                    // Stale/duplicate commit — skip entirely to avoid re-processing
                    tracing::warn!(
                        "⚠️ [COMMIT FINALIZER] Skipping stale commit index {} (last_processed={})",
                        committed_sub_dag.commit_ref.index, last_processed_commit
                    );
                    return vec![];
                }
                // Gap detected — this is EXPECTED during catch-up (FORWARD-JUMP)
                // where empty commits are batch-skipped by CommitProcessor.
                // Previously this was an assert_eq! that killed the entire consensus engine.
                tracing::warn!(
                    "⚠️ [COMMIT FINALIZER] Non-sequential commit: expected index {}, got {}. \
                     Gap of {} commits (likely FORWARD-JUMP catch-up). Proceeding.",
                    expected, committed_sub_dag.commit_ref.index,
                    committed_sub_dag.commit_ref.index.saturating_sub(expected)
                );
            }
        }
        self.last_processed_commit = Some(committed_sub_dag.commit_ref.index);

        self.pending_commits
            .push_back(CommitState::new(committed_sub_dag));

        let mut finalized_commits = vec![];

        // The prerequisite for running direct finalization on a commit is that the commit must
        // have either a quorum of leader certificates in the local DAG, or a committed leader certificate.
        //
        // A leader certificate is a finalization certificate for every block in the commit.
        // When the prerequisite holds, all blocks in the current commit can be considered finalized.
        // And any transaction in the current commit that has not observed reject votes will never be rejected.
        // So these transactions are directly finalized.
        //
        // When a commit is direct, there are a quorum of its leader certificates in the local DAG.
        //
        // When a commit is indirect, it implies one of its leader certificates is in the committed blocks.
        // So a leader certificate must exist in the local DAG as well.
        //
        // When a commit is received through commit sync and processed as certified commit, the commit might
        // not have a leader certificate in the local DAG. So a committed transaction might not observe any reject
        // vote from local DAG, although it will eventually get rejected. To finalize blocks in this commit,
        // there must be another commit with leader round >= 3 (WAVE_LENGTH) rounds above the commit leader.
        // From the indirect commit rule, a leader certificate must exist in committed blocks for the earliest commit.
        for i in 0..self.pending_commits.len() {
            let commit_state = &self.pending_commits[i];
            if commit_state.pending_blocks.is_empty() {
                // The commit has already been processed through direct finalization.
                continue;
            }
            // Direct finalization cannot happen when
            // -  This commit is remote.
            // -  And the latest commit is less than 3 (WAVE_LENGTH) rounds above this commit.
            // In this case, this commit's leader certificate is not guaranteed to be in local DAG.
            if !commit_state.commit.decided_with_local_blocks {
                let last_commit_state = self
                    .pending_commits
                    .back()
                    .expect("pending_commits should not be empty during direct finalization");
                if commit_state.commit.leader.round + DEFAULT_WAVE_LENGTH
                    > last_commit_state.commit.leader.round
                {
                    break;
                }
            }
            self.try_direct_finalize_commit(i);
        }
        let direct_finalized_commits = self.pop_finalized_commits();
        self.context
            .metrics
            .node_metrics
            .finalizer_output_commits
            .with_label_values(&["direct"])
            .inc_by(direct_finalized_commits.len() as u64);
        finalized_commits.extend(direct_finalized_commits);

        // Indirect finalization: one or more commits cannot be directly finalized.
        // So the pending transactions need to be checked for indirect finalization.
        if !self.pending_commits.is_empty() {
            // Initialize the state of the last added commit for computing indirect finalization.
            //
            // As long as there are remaining commits, even if the last commit has been directly finalized,
            // its state still needs to be initialized here to help indirectly finalize previous commits.
            // This is because the last commit may have been directly finalized, but its previous commits
            // may not have been directly finalized.
            self.link_blocks_in_last_commit();
            self.append_origin_descendants_from_last_commit();
            // Try to indirectly finalize a prefix of the buffered commits.
            // If only one commit remains, it cannot be indirectly finalized because there is no commit afterwards,
            // so it is excluded.
            while self.pending_commits.len() > 1 {
                // Stop indirect finalization when the earliest commit has not been processed
                // through direct finalization.
                if !self.pending_commits[0].pending_blocks.is_empty() {
                    break;
                }
                // Otherwise, try to indirectly finalize the earliest commit.
                self.try_indirect_finalize_first_commit().await;
                let indirect_finalized_commits = self.pop_finalized_commits();
                if indirect_finalized_commits.is_empty() {
                    // No additional commits can be indirectly finalized.
                    break;
                }
                self.context
                    .metrics
                    .node_metrics
                    .finalizer_output_commits
                    .with_label_values(&["indirect"])
                    .inc_by(indirect_finalized_commits.len() as u64);
                finalized_commits.extend(indirect_finalized_commits);
            }
        }

        self.context
            .metrics
            .node_metrics
            .finalizer_buffered_commits
            .set(self.pending_commits.len() as i64);

        finalized_commits
    }

    // Tries directly finalizing transactions in the commit.
    fn try_direct_finalize_commit(&mut self, index: usize) {
        let num_commits = self.pending_commits.len();
        let commit_state = self
            .pending_commits
            .get_mut(index)
            .unwrap_or_else(|| panic!("Commit {} does not exist. len = {}", index, num_commits,));
        // Direct commit means every transaction in the commit can be considered to have a quorum of post-commit certificates,
        // unless the transaction has reject votes that do not reach quorum either.
        assert!(!commit_state.pending_blocks.is_empty());

        let metrics = &self.context.metrics.node_metrics;
        let pending_blocks = std::mem::take(&mut commit_state.pending_blocks);
        for (block_ref, num_transactions) in pending_blocks {
            let reject_votes = match self.transaction_certifier.get_reject_votes(&block_ref) {
                Some(votes) => votes,
                None => {
                    // SAFETY: During commit sync (CatchingUp), blocks may arrive via
                    // certified commits without being registered in TransactionCertifier,
                    // or may have been GC'd before CommitFinalizer processes them.
                    // Treat as zero reject votes — all transactions are directly finalized.
                    tracing::warn!(
                        "⚠️ [COMMIT-FINALIZER] No vote info for {block_ref} (likely GC'd or synced remotely). \
                         Treating all {} transactions as finalized.",
                        num_transactions
                    );
                    vec![]
                }
            };
            metrics
                .finalizer_transaction_status
                .with_label_values(&["direct_finalize"])
                .inc_by((num_transactions - reject_votes.len()) as u64);
            let hostname = &self.context.committee.authority(block_ref.author).hostname;
            metrics
                .finalizer_reject_votes
                .with_label_values(&[hostname])
                .inc_by(reject_votes.len() as u64);
            // If a transaction_index does not exist in reject_votes, the transaction has no reject votes.
            // So it is finalized and does not need to be added to pending_transactions.
            for (transaction_index, stake) in reject_votes {
                // If the transaction has > 0 but < 2f+1 reject votes, it is still pending.
                // Otherwise, it is rejected.
                let entry = if stake < self.context.committee.quorum_threshold() {
                    commit_state
                        .pending_transactions
                        .entry(block_ref)
                        .or_default()
                } else {
                    metrics
                        .finalizer_transaction_status
                        .with_label_values(&["direct_reject"])
                        .inc();
                    commit_state
                        .rejected_transactions
                        .entry(block_ref)
                        .or_default()
                };
                entry.insert(transaction_index);
            }
        }
    }

    // Creates an entry in the blocks map for each block in the commit,
    // and have its ancestors link to the block.
    fn link_blocks_in_last_commit(&mut self) {
        let commit_state = self
            .pending_commits
            .back_mut()
            .unwrap_or_else(|| panic!("No pending commit."));

        // Link blocks in ascending order of round, to ensure ancestor block states are created
        // before they are linked from.
        let mut blocks = commit_state.commit.blocks.clone();
        blocks.sort_by_key(|b| b.round());

        let mut blocks_map = self.blocks.write();
        for block in blocks {
            let block_ref = block.reference();
            // Link ancestors to the block.
            for ancestor in block.ancestors() {
                // Ancestor may not exist in the blocks map if it has been finalized or gc'ed.
                // So skip linking if the ancestor does not exist.
                if let Some(ancestor_block) = blocks_map.get(ancestor) {
                    ancestor_block.write().children.insert(block_ref);
                }
            }
            // Initialize the block state.
            blocks_map.entry(block_ref).or_insert_with(|| {
                RwLock::new(BlockState::new(block, commit_state.commit.commit_ref.index))
            });
        }
    }

    /// Updates the set of origin descendants, by appending blocks from the last commit to
    /// origin descendants of previous linked blocks from the same origin.
    ///
    /// The purpose of maintaining the origin descendants per block is to save bandwidth by avoiding to explicitly
    /// list all accept votes on transactions in blocks.
    /// Instead when an ancestor block Ba is first included by a proposed block Bp, reject votes for transactions in Ba
    /// are explicitly listed (if they exist). The rest of non-rejected transactions in Ba are assumed to be accepted by Bp.
    /// This vote compression rule must be applied during vote aggregation as well.
    ///
    /// The above rule is equivalent to saying that transactions in a block can only be voted on by its immediate descendants.
    /// A block Bp is an **immediate descendant** of Ba, if any directed path from Bp to Ba does not contain a block from Bp's own authority.
    ///
    /// This rule implies the following optimization is possible: after collecting votes for Ba from block Bp,
    /// we can skip collecting votes from Bp's **origin descendants** (descendant blocks from the
    /// same authority), because they cannot vote on Ba anyway.
    ///
    /// This vote compression rule is easy to implement when proposing blocks. Reject votes can be gathered against
    /// all the newly included ancestors of the proposed block. But vote decompression is trickier to get right.
    /// One edge case is when a block may not be an immediate descendant, because of GC. In this case votes from the
    /// block should not be counted.
    fn append_origin_descendants_from_last_commit(&mut self) {
        let commit_state = self
            .pending_commits
            .back_mut()
            .unwrap_or_else(|| panic!("No pending commit."));
        let mut committed_blocks = commit_state.commit.blocks.clone();
        committed_blocks.sort_by_key(|b| b.round());
        let blocks_map = self.blocks.read();
        for committed_block in committed_blocks {
            let committed_block_ref = committed_block.reference();
            // Each block must have at least one ancestor.
            // Block verification ensures the first ancestor is from the block's own authority.
            // Also, block verification ensures each authority appears at most once among ancestors.
            let mut origin_ancestor_ref = *blocks_map
                .get(&committed_block_ref)
                .expect("block must exist in blocks_map")
                .read()
                .block
                .ancestors()
                .first()
                .expect("block must have at least one ancestor");
            while origin_ancestor_ref.author == committed_block_ref.author {
                let Some(origin_ancestor_block) = blocks_map.get(&origin_ancestor_ref) else {
                    break;
                };
                origin_ancestor_block
                    .write()
                    .origin_descendants
                    .push(committed_block_ref);
                origin_ancestor_ref = *origin_ancestor_block
                    .read()
                    .block
                    .ancestors()
                    .first()
                    .expect("origin ancestor block must have at least one ancestor");
            }
        }
    }

    // Tries indirectly finalizing the buffered commits at the given index.
    async fn try_indirect_finalize_first_commit(&mut self) {
        // Ensure direct finalization has been attempted for the commit.
        assert!(!self.pending_commits.is_empty());
        assert!(self.pending_commits[0].pending_blocks.is_empty());

        // Optional optimization: re-check pending transactions to see if they are rejected by a quorum now.
        self.check_pending_transactions_in_first_commit();

        // Check if remaining pending transactions can be finalized.
        self.try_indirect_finalize_pending_transactions_in_first_commit()
            .await;

        // Check if remaining pending transactions can be indirectly rejected.
        self.try_indirect_reject_pending_transactions_in_first_commit();
    }

    fn check_pending_transactions_in_first_commit(&mut self) {
        let mut all_rejected_transactions: Vec<(BlockRef, Vec<TransactionIndex>)> = vec![];

        // Collect all rejected transactions without modifying state
        for (block_ref, pending_transactions) in &self.pending_commits[0].pending_transactions {
            let reject_votes: BTreeMap<TransactionIndex, Stake> = match self
                .transaction_certifier
                .get_reject_votes(block_ref)
            {
                Some(votes) => votes.into_iter().collect(),
                None => {
                    // SAFETY: Block may have been GC'd or arrived via commit sync.
                    // Skip re-checking — pending transactions will be resolved by
                    // indirect finalization or indirect rejection instead.
                    tracing::warn!(
                        "⚠️ [COMMIT-FINALIZER] No vote info for {block_ref} during pending tx check. Skipping."
                    );
                    continue;
                }
            };
            let mut rejected_transactions = vec![];
            for &transaction_index in pending_transactions {
                // Pending transactions should always have reject votes.
                let reject_stake = reject_votes
                    .get(&transaction_index)
                    .copied()
                    .expect("pending transaction must have reject vote entry");
                if reject_stake < self.context.committee.quorum_threshold() {
                    // The transaction cannot be rejected yet.
                    continue;
                }
                // Otherwise, mark the transaction for rejection.
                rejected_transactions.push(transaction_index);
            }
            if !rejected_transactions.is_empty() {
                all_rejected_transactions.push((*block_ref, rejected_transactions));
            }
        }

        // Move rejected transactions from pending_transactions.
        for (block_ref, rejected_transactions) in all_rejected_transactions {
            self.context
                .metrics
                .node_metrics
                .finalizer_transaction_status
                .with_label_values(&["direct_late_reject"])
                .inc_by(rejected_transactions.len() as u64);
            let curr_commit_state = &mut self.pending_commits[0];
            curr_commit_state.remove_pending_transactions(&block_ref, &rejected_transactions);
            curr_commit_state
                .rejected_transactions
                .entry(block_ref)
                .or_default()
                .extend(rejected_transactions);
        }
    }

    async fn try_indirect_finalize_pending_transactions_in_first_commit(&mut self) {
        /* let _scope = tracing::info_span!(
            "CommitFinalizer::try_indirect_finalize_pending_transactions_in_first_commit",
        ).entered(); */

        let pending_blocks: Vec<_> = self.pending_commits[0]
            .pending_transactions
            .iter()
            .map(|(k, v)| (*k, v.clone()))
            .collect();

        let gc_rounds = self
            .pending_commits
            .iter()
            .map(|c| {
                (
                    c.commit.commit_ref.index,
                    self.dag_state
                        .read()
                        .calculate_gc_round(c.commit.leader.round),
                )
            })
            .collect::<Vec<_>>();

        // Number of blocks to process in each task.
        const BLOCKS_PER_INDIRECT_COMMIT_TASK: usize = 8;

        // Process chunks in parallel.
        let mut all_finalized_transactions = vec![];
        let mut join_set = JoinSet::new();
        // TODO(fastpath): investigate using a cost based batching,
        // for example each block has cost num authorities + pending_transactions.len().
        for chunk in pending_blocks.chunks(BLOCKS_PER_INDIRECT_COMMIT_TASK) {
            let context = self.context.clone();
            let blocks = self.blocks.clone();
            let gc_rounds = gc_rounds.clone();
            let chunk: Vec<(BlockRef, BTreeSet<TransactionIndex>)> = chunk.to_vec();

            join_set.spawn(tokio::task::spawn_blocking(move || {
                let mut chunk_results = Vec::new();

                for (block_ref, pending_transactions) in chunk {
                    let finalized = Self::try_indirect_finalize_pending_transactions_in_block(
                        &context,
                        &blocks,
                        &gc_rounds,
                        block_ref,
                        pending_transactions,
                    );

                    if !finalized.is_empty() {
                        chunk_results.push((block_ref, finalized));
                    }
                }

                chunk_results
            }));
        }

        // Collect results from all chunks
        while let Some(result) = join_set.join_next().await {
            let e = match result {
                Ok(blocking_result) => match blocking_result {
                    Ok(chunk_results) => {
                        all_finalized_transactions.extend(chunk_results);
                        continue;
                    }
                    Err(e) => e,
                },
                Err(e) => e,
            };
            if e.is_panic() {
                std::panic::resume_unwind(e.into_panic());
            }
            tracing::info!("Process likely shutting down: {:?}", e);
            // Ok to return. No potential inconsistency in state.
            return;
        }

        for (block_ref, finalized_transactions) in all_finalized_transactions {
            self.context
                .metrics
                .node_metrics
                .finalizer_transaction_status
                .with_label_values(&["indirect_finalize"])
                .inc_by(finalized_transactions.len() as u64);
            // Remove finalized transactions from pending transactions.
            self.pending_commits[0]
                .remove_pending_transactions(&block_ref, &finalized_transactions);
        }
    }

    fn try_indirect_reject_pending_transactions_in_first_commit(&mut self) {
        let curr_leader_round = self.pending_commits[0].commit.leader.round;
        let last_commit_leader_round = self
            .pending_commits
            .back()
            .expect("pending_commits should not be empty")
            .commit
            .leader
            .round;
        if curr_leader_round + INDIRECT_REJECT_DEPTH <= last_commit_leader_round {
            let curr_commit_state = &mut self.pending_commits[0];
            // This function is called after trying to indirectly finalize pending blocks.
            // When last commit leader round is INDIRECT_REJECT_DEPTH rounds higher or more,
            // all pending blocks should have been finalized.
            assert!(curr_commit_state.pending_blocks.is_empty());
            // This function is called after trying to indirectly finalize pending transactions.
            // All remaining pending transactions, since they are not finalized, should now be
            // indirectly rejected.
            let pending_transactions = std::mem::take(&mut curr_commit_state.pending_transactions);
            for (block_ref, pending_transactions) in pending_transactions {
                self.context
                    .metrics
                    .node_metrics
                    .finalizer_transaction_status
                    .with_label_values(&["indirect_reject"])
                    .inc_by(pending_transactions.len() as u64);
                curr_commit_state
                    .rejected_transactions
                    .entry(block_ref)
                    .or_default()
                    .extend(pending_transactions);
            }
        }
    }

    // Returns the indices of the requested pending transactions that are indirectly finalized.
    // This function is used for checking finalization of transactions, so it must traverse
    // all blocks which can contribute to the requested transactions' finalizations.
    fn try_indirect_finalize_pending_transactions_in_block(
        context: &Arc<Context>,
        blocks: &Arc<RwLock<BTreeMap<BlockRef, RwLock<BlockState>>>>,
        gc_rounds: &[(CommitIndex, Round)],
        pending_block_ref: BlockRef,
        pending_transactions: BTreeSet<TransactionIndex>,
    ) -> Vec<TransactionIndex> {
        if pending_transactions.is_empty() {
            return vec![];
        }
        let mut accept_votes: BTreeMap<TransactionIndex, StakeAggregator<QuorumThreshold>> =
            pending_transactions
                .into_iter()
                .map(|transaction_index| (transaction_index, StakeAggregator::new()))
                .collect();
        let mut finalized_transactions = vec![];
        let blocks_map = blocks.read();
        // Use BTreeSet for to_visit_blocks, to visit blocks in the earliest round first.
        let (pending_commit_index, mut to_visit_blocks) = {
            let block_state = blocks_map
                .get(&pending_block_ref)
                .expect("pending block must exist in blocks_map")
                .read();
            (block_state.commit_index, block_state.children.clone())
        };
        // Blocks that have been visited.
        let mut visited = BTreeSet::new();
        // Blocks where votes and origin descendants should be ignored for processing.
        let mut ignored = BTreeSet::new();
        // Traverse children blocks breadth-first and accumulate accept votes for pending transactions.
        while let Some(curr_block_ref) = to_visit_blocks.pop_first() {
            if !visited.insert(curr_block_ref) {
                continue;
            }
            let Some(curr_block_entry) = blocks_map.get(&curr_block_ref) else {
                // SAFETY: Block may have been GC'd during commit sync catch-up.
                // Skip this block — its votes won't count towards finalization,
                // which is conservative but correct (transaction will be resolved
                // by indirect rejection at INDIRECT_REJECT_DEPTH).
                tracing::debug!(
                    "⚠️ [COMMIT-FINALIZER] Block {curr_block_ref} missing from blocks_map (GC'd). Skipping."
                );
                continue;
            };
            let curr_block_state = curr_block_entry.read();
            // Check if transaction votes for the pending block are potentially not carried by the
            // current block, because of GC at the current block's proposer.
            // See comment above gced_transaction_votes_for_pending_block() for more details.
            //
            // Implicit transaction votes should only be considered in commit finalizer if they are definitely
            // part of the transactions votes from the current block when it is proposed.
            let votes_gced = Self::gced_transaction_votes_for_pending_block(
                gc_rounds,
                pending_block_ref.round,
                pending_commit_index,
                curr_block_state.commit_index,
            );
            // Skip counting votes from the block if it has been marked to be ignored.
            if ignored.insert(curr_block_ref) {
                // Skip collecting votes from origin descendants of current block.
                // Votes from origin descendants of current block do not count for these transactions.
                // Consider this case: block B is an origin descendant of block A (from the same authority),
                // and both blocks A and B link to another block C.
                // Only B's implicit and explicit transaction votes on C are considered.
                // None of A's implicit or explicit transaction votes on C should be considered.
                //
                // See append_origin_descendants_from_last_commit() for more details.
                ignored.extend(curr_block_state.origin_descendants.iter());
                // Skip counting votes from current block if the votes on pending block could have been
                // casted by an earlier block from the same origin.
                // Note: if the current block casts reject votes on transactions in the pending block,
                // it can be assumed that accept votes are also casted to other transactions in the pending block.
                // But we choose to skip counting the accept votes in this edge case for simplicity.
                if context.protocol_config.consensus_skip_gced_accept_votes() && votes_gced {
                    let hostname = &context.committee.authority(curr_block_ref.author).hostname;
                    context
                        .metrics
                        .node_metrics
                        .finalizer_skipped_voting_blocks
                        .with_label_values(&[hostname])
                        .inc();
                    continue;
                }
                // Get reject votes from current block to the pending block.
                let curr_block_reject_votes = curr_block_state
                    .reject_votes
                    .get(&pending_block_ref)
                    .cloned()
                    .unwrap_or_default();
                // Because of lifetime, first collect finalized transactions, and then remove them from accept_votes.
                let mut newly_finalized = vec![];
                for (index, stake) in &mut accept_votes {
                    // Skip if the transaction has been rejected by the current block.
                    if curr_block_reject_votes.contains(index) {
                        continue;
                    }
                    // Skip if the total stake has not reached quorum.
                    if !stake.add(curr_block_ref.author, &context.committee) {
                        continue;
                    }
                    newly_finalized.push(*index);
                    finalized_transactions.push(*index);
                }
                // There is no need to aggregate additional votes for already finalized transactions.
                for index in newly_finalized {
                    accept_votes.remove(&index);
                }
                // End traversal if all blocks and requested transactions have reached quorum.
                if accept_votes.is_empty() {
                    break;
                }
            }
            // Add additional children blocks to visit.
            to_visit_blocks.extend(
                curr_block_state
                    .children
                    .iter()
                    .filter(|b| !visited.contains(*b)),
            );
        }
        finalized_transactions
    }

    /// Returns true if transaction votes from the current block to the pending block
    /// could have been be GC'ed. If this is the case, the current block cannot be assumed
    /// to have implicitly voted to accept transactions in the pending block.
    ///
    /// When collecting transaction votes during proposal of the current block
    /// (via DagState::link_causal_history()), votes against blocks in the DAG
    /// below the proposer's GC round are skipped. Implicit accept votes cannot be assumed
    /// for these GC'ed blocks. However, blocks do not carry the GC round when they are proposed.
    /// So this function computes the highest possible GC round when proposing the current block,
    /// and use it as the minimum round threshold for implicit accept votes. Even if the computed
    /// GC round here is higher than the actual GC round used by the current block, it is still
    /// correct although less efficient.
    ///
    /// gc_rounds is a list of cached commit indices and the GC rounds resulting from the commits.
    /// It must be a superset of commits in the range [pending_commit_index, current_commit_index].
    /// The first element should have pending_commit_index, because pending commit should be the
    /// first commit buffered in CommitFinalizer.
    fn gced_transaction_votes_for_pending_block(
        gc_rounds: &[(CommitIndex, Round)],
        pending_block_round: Round,
        pending_commit_index: CommitIndex,
        current_commit_index: CommitIndex,
    ) -> bool {
        assert!(
            pending_commit_index <= current_commit_index,
            "Pending {pending_commit_index} should be <= current {current_commit_index}"
        );
        if pending_commit_index == current_commit_index {
            return false;
        }
        // current_commit_index is the commit index which includes the current / voting block.
        // When proposing the current block, the latest possible GC round is the GC round computed
        // from the leader of the previous commit (current_commit_index - 1).
        let (commit_index, gc_round) = *gc_rounds
            .get((current_commit_index - 1 - pending_commit_index) as usize)
            .expect("gc_rounds must contain entry for current_commit_index - 1");
        assert_eq!(
            commit_index,
            current_commit_index - 1,
            "Commit index mismatch {commit_index} != {current_commit_index}"
        );
        pending_block_round <= gc_round
    }

    fn pop_finalized_commits(&mut self) -> Vec<CommittedSubDag> {
        let mut finalized_commits = vec![];

        while let Some(commit_state) = self.pending_commits.front() {
            if !commit_state.pending_blocks.is_empty()
                || !commit_state.pending_transactions.is_empty()
            {
                // The commit is not finalized yet.
                break;
            }

            // Pop the finalized commit and set its rejected transactions.
            let commit_state = self
                .pending_commits
                .pop_front()
                .expect("pending_commits front must exist (checked in while condition)");
            let mut commit = commit_state.commit;
            for (block_ref, rejected_transactions) in commit_state.rejected_transactions {
                commit
                    .rejected_transactions_by_block
                    .insert(block_ref, rejected_transactions.into_iter().collect());
            }

            // Clean up committed blocks.
            let mut blocks_map = self.blocks.write();
            for block in commit.blocks.iter() {
                blocks_map.remove(&block.reference());
            }

            let round_delay = if let Some(last_commit_state) = self.pending_commits.back() {
                last_commit_state.commit.leader.round - commit.leader.round
            } else {
                0
            };
            self.context
                .metrics
                .node_metrics
                .finalizer_round_delay
                .observe(round_delay as f64);

            finalized_commits.push(commit);
        }

        finalized_commits
    }

    fn try_update_gc_round(&mut self, last_finalized_commit_round: Round) {
        // GC TransactionCertifier state only with finalized commits, to ensure unfinalized transactions
        // can access their reject votes from TransactionCertifier.
        let gc_round = self
            .dag_state
            .read()
            .calculate_gc_round(last_finalized_commit_round);
        self.transaction_certifier.run_gc(gc_round);
    }

    #[cfg(test)]
    fn is_empty(&self) -> bool {
        self.pending_commits.is_empty() && self.blocks.read().is_empty()
    }
}
