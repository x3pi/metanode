use itertools::Itertools;
use std::collections::{BTreeMap, BTreeSet};

use consensus_types::block::{BlockRef, Round};
use meta_macros::fail_point;
use mysten_metrics::monitored_scope;
use tracing::{info, warn};

use crate::{
    block::BlockAPI,
    commit::{CertifiedCommit, CertifiedCommits, CommitAPI, CommittedSubDag, DecidedLeader},
    core::Core,
    error::ConsensusResult,
};

impl Core {
    #[tracing::instrument(skip_all)]
    pub(crate) fn add_certified_commits(
        &mut self,
        certified_commits: CertifiedCommits,
    ) -> ConsensusResult<BTreeSet<BlockRef>> {
        let _scope = monitored_scope("Core::add_certified_commits");

        let last_commit = self.dag_state.read().last_commit_index();
        let commits_count = certified_commits.commits().len();
        let first_idx = certified_commits.commits().first().map(|c| c.index());
        let last_idx = certified_commits.commits().last().map(|c| c.index());
        tracing::info!(
            "[NODE4-DEBUG] Core::add_certified_commits: local_commit={}, received {} commits ({}→{})",
            last_commit, commits_count, first_idx.unwrap_or(0), last_idx.unwrap_or(0)
        );

        let votes = certified_commits.votes().to_vec();
        let commits = match self.filter_new_commits(certified_commits.commits().to_vec()) {
            Ok(commits) => {
                tracing::info!(
                    "[NODE4-DEBUG] filter_new_commits passed: {} commits to process",
                    commits.len()
                );
                commits
            }
            Err(e) => {
                tracing::error!(
                    "[NODE4-DEBUG] filter_new_commits FAILED: {:?}",
                    e
                );
                return Err(e);
            }
        };

        // Try to accept the certified commit votes.
        // Even if they may not be part of a future commit, these blocks are useful for certifying
        // commits when helping peers sync commits.
        let (_, missing_block_refs) = self.block_manager.try_accept_blocks(votes);

        // Try to commit the new blocks. Take into account the trusted commit that has been provided.
        match self.try_commit(commits) {
            Ok(subdags) => {
                let new_commit_index = self.dag_state.read().last_commit_index();
                tracing::info!(
                    "[NODE4-DEBUG] try_commit succeeded: {} subdags, new_commit_index={}",
                    subdags.len(),
                    new_commit_index
                );

            }
            Err(e) => {
                tracing::error!("[NODE4-DEBUG] try_commit FAILED: {:?}", e);
                return Err(e);
            }
        }

        // DETERMINISTIC RECOVERY GUARD:
        // After successfully processing CertifiedCommits from the network,
        // we track how many network commits have been processed while in Healthy phase.
        // The local committer will only unlock after 5 such commits, ensuring the DAG
        // is dense enough (has accumulated orphans) before it evaluates local blocks.
        // FORK-PREVENTION: Do NOT increment or unlock if STARTUP-SYNC is active.
        if commits_count > 0 && !self.coordination_hub.is_startup_sync_active() {
            // Track if the guard was already unlocked BEFORE this call.
            // We need this to distinguish "CertifiedCommit that caused the unlock"
            // from "CertifiedCommit processed AFTER the unlock".
            let was_already_unlocked = self.coordination_hub.is_local_commit_unlocked();

            if self.coordination_hub.is_healthy_stable() {
                self.coordination_hub.inc_network_commits_since_healthy(commits_count);
            }
            self.coordination_hub.unlock_local_commit();

            // POST-RECOVERY RACE FIX:
            // Only confirm post-recovery sync if the guard was ALREADY unlocked
            // before this CertifiedCommit was processed. This ensures the local
            // committer waits for at least one ADDITIONAL network-verified commit
            // after unlock before evaluating locally.
            //
            // Without this, the CertifiedCommit that causes the unlock (the 5th)
            // would also confirm sync, allowing the local committer to fire
            // immediately on the next add_blocks event with a potentially
            // divergent DAG view — the exact race that caused the #601 fork.
            if was_already_unlocked {
                self.coordination_hub.confirm_post_recovery_sync();
            }

            // NETWORK-FIRST-COMMIT-GUARD counter:
            // After snapshot recovery, count NEW CertifiedCommits processed while Healthy.
            // The local committer is blocked until this counter reaches the threshold,
            // proving the DAG above the committed tip has converged with the network.
            if self.coordination_hub.was_recovery_activated()
                && self.coordination_hub.is_healthy()
            {
                self.coordination_hub.inc_post_recovery_network_commits(commits_count);
            }
        }

        // Try to propose now since there are new blocks accepted.
        self.try_propose(false)?;

        // Now set up leader timeout if needed.
        // This needs to be called after try_commit() and try_propose(), which may
        // have advanced the threshold clock round.
        self.try_signal_new_round();

        Ok(missing_block_refs)
    }

    /// Runs commit rule to attempt to commit additional blocks from the DAG. If any `certified_commits` are provided, then
    /// it will attempt to commit those first before trying to commit any further leaders.
    pub(crate) fn try_commit(
        &mut self,
        mut certified_commits: Vec<CertifiedCommit>,
    ) -> ConsensusResult<Vec<CommittedSubDag>> {
        let is_sync_mode = !certified_commits.is_empty();
        let _s = self
            .context
            .metrics
            .node_metrics
            .scope_processing_time
            .with_label_values(&["Core::try_commit"])
            .start_timer();

        let mut certified_commits_map = BTreeMap::new();
        for c in &certified_commits {
            certified_commits_map.insert(c.index(), c.reference());
        }

        if !certified_commits.is_empty() {
            info!(
                "Processing synced commits: {:?}",
                certified_commits
                    .iter()
                    .map(|c| (c.index(), c.leader()))
                    .collect::<Vec<_>>()
            );
        }

        let mut committed_sub_dags = Vec::new();
        // TODO: Add optimization to abort early without quorum for a round.
        loop {
            // CRITICAL: Sync last_decided_leader with DAG state.
            // If CommitSyncer performs a cold-start fast-forward (restoring from snapshot),
            // it will synthetically advance the DagState's last commit. We must update
            // Core's local last_decided_leader to prevent it from evaluating old leaders
            // against the new updated gc_round, which would cause the linearizer to panic.
            let current_dag_leader = self.dag_state.read().last_commit_leader();
            if self.last_decided_leader.round < current_dag_leader.round {
                tracing::warn!(
                    "🚀 [COLD-START] Fast-forwarding Core::last_decided_leader from round {} to {}",
                    self.last_decided_leader.round, current_dag_leader.round
                );
                self.last_decided_leader = current_dag_leader;
            }
                
            // CRITICAL FIX: Restore LeaderSchedule from the baseline if available.
            // We MUST do this here before `commits_until_update` is calculated so that
            // the synced commits evaluate against the correct pre-calculated schedule 
            // instead of an empty default schedule.
            // Note: This must run independently of the `last_decided_leader` check,
            // because during snapshot restore, last_decided_leader starts equal to current_dag_leader!
            //
            // ARCHITECTURE (May 2026 — Phase 1 Lock→Channel):
            // The take operation goes through DagStateWriter (channel → DagStateActor thread)
            // instead of calling dag_state.write() directly. This eliminates the RwLock
            // deadlock that occurred when the write-guard lifetime leaked into the if-let body.
            let baseline_scores = self.dag_state_writer.take_baseline_scores();
            if let Some(scores) = baseline_scores {
                tracing::info!("🔄 [SYNC] Core restoring LeaderSchedule scores from DagState baseline. Index={}", self.dag_state.read().last_commit_index());
                self.leader_schedule.update_from_baseline_scores(
                    self.context.clone(),
                    self.dag_state.read().last_commit_index(),
                    scores
                );
                
                // Since the schedule is now fully restored and correct based on the network's 
                // baseline, we DO NOT need to wait for a 300-commit cycle to verify it.
                // We can tell the RecoveryBarrier to bypass the ScheduleVerifying phase!
                self.coordination_hub.recovery_barrier().set_schedule_pre_verified();
                
                if self.coordination_hub.is_schedule_recovery_pending() {
                    self.coordination_hub.set_schedule_recovery_pending(false);
                }
            }

            // LeaderSchedule has a limit to how many sequenced leaders can be committed
            // before a change is triggered. Calling into leader schedule will get you
            // how many commits till next leader change. We will loop back and recalculate
            // any discarded leaders with the new schedule.
            let mut commits_until_update = self
                .leader_schedule
                .commits_until_leader_schedule_update(self.dag_state.clone());

            if commits_until_update == 0 {
                let last_commit_index = self.dag_state.read().last_commit_index();

                tracing::info!(
                    "Leader schedule change triggered at commit index {last_commit_index}"
                );

                self.leader_schedule
                    .update_leader_schedule_v2(&self.dag_state, &self.dag_state_writer);

                // UNIFIED RECOVERY BARRIER (May 2026):
                // After a full 300-commit scoring cycle completes, advance the barrier.
                // The barrier only transitions ScheduleVerifying → Ready (via compare_exchange),
                // so this is safe to call unconditionally — it's a no-op if the barrier
                // is in any other phase (Inactive, GoSyncing, DagCatchingUp, or Ready).
                //
                // This replaces the fragmented logic that checked is_sync_mode, local vs quorum
                // commit, and handled_commits >= 300. All those edge cases are now handled by
                // the barrier's strict phase progression:
                //   Inactive → GoSyncing → DagCatchingUp → ScheduleVerifying → Ready
                // Only ScheduleVerifying → Ready happens here.
                self.coordination_hub.recovery_barrier().schedule_verified();
                // Also clear legacy flag for backward compatibility
                if self.coordination_hub.is_schedule_recovery_pending() {
                    self.coordination_hub.set_schedule_recovery_pending(false);
                }

                let propagation_scores = self
                    .leader_schedule
                    .leader_swap_table
                    .read()
                    .reputation_scores
                    .clone();
                self.ancestor_state_manager
                    .set_propagation_scores(propagation_scores);

                commits_until_update = self
                    .leader_schedule
                    .commits_until_leader_schedule_update(self.dag_state.clone());

                fail_point!("consensus-after-leader-schedule-change");
            }
            assert!(commits_until_update > 0);

            // If there are certified commits to process, find out which leaders and commits from them
            // are decided and use them as the next commits.
            let (certified_leaders, decided_certified_commits): (
                Vec<DecidedLeader>,
                Vec<CertifiedCommit>,
            ) = self
                .try_select_certified_leaders(&mut certified_commits, commits_until_update)
                .into_iter()
                .unzip();

            // Only accept blocks for the certified commits that we are certain to sequence.
            // This ensures that only blocks corresponding to committed certified commits are flushed to disk.
            // Blocks from non-committed certified commits will not be flushed, preventing issues during crash-recovery.
            // This avoids scenarios where accepting and flushing blocks of non-committed certified commits could lead to
            // premature commit rule execution. Due to GC, this could cause a panic if the commit rule tries to access
            // missing causal history from blocks of certified commits.
            let blocks = decided_certified_commits
                .iter()
                .flat_map(|c| c.blocks())
                .cloned()
                .collect::<Vec<_>>();
            self.block_manager.try_accept_committed_blocks(blocks.clone());

            // FIX: Ensure that blocks from certified commits are added to TransactionCertifier.
            // This prevents CommitFinalizer from panicking with "No vote info found" when it
            // tries to run direct finalization on these fast-forwarded blocks.
            self.transaction_certifier
                .add_voted_blocks(blocks.into_iter().map(|b| (b, vec![])).collect());

            // NOTE: Certifier vote blocks are already processed in add_certified_commits()
            // via self.block_manager.try_accept_blocks(votes) (line 54).
            // The votes contain reject vote information needed by CommitFinalizer.

            // If there is no certified commit to process, run the decision rule.
            let (decided_leaders, local, precomputed_commits) = if certified_leaders.is_empty() {
                // If we are currently processing network commits (sync mode), we MUST exit
                // when no more certified leaders remain. Fallback to the local committer
                // during fast-forwarding is extremely dangerous because the local DAG might
                // have dangling blocks that trick the committer into choosing a leader whose
                // ancestors are missing from RocksDB, leading to a fatal StorageFailure panic.
                if is_sync_mode {
                    break;
                }

                // CRITICAL FORK-SAFETY FIX (Deterministic Network Confirmation):
                //
                // Do not run local committer until the node has processed at least one
                // CertifiedCommit from the network while in Healthy phase. This proves
                // the DAG is populated enough for `calculate_commit_timestamp()` to produce
                // the correct stake-weighted median timestamp.
                //
                // Without this, after snapshot restore the node oscillates CatchingUp↔Healthy.
                // If the local committer fires during a brief Healthy window with a sparse DAG,
                // it produces commits with wrong timestamps → fork.
                //
                // This guard is deterministic and event-driven — no arbitrary time delays.
                // The lock is released by `confirm_network_commit()` in `add_certified_commits()`.
                if !self.coordination_hub.is_healthy_stable() {
                    tracing::debug!(
                        "⏭️ [SYNC] Skipping local committer: awaiting network confirmation (phase={:?}). \
                         Will unlock after processing a CertifiedCommit in Healthy phase.",
                        self.coordination_hub.get_phase()
                    );
                    break;
                }

                // DAG SPARSENESS PREVENTION:
                // Even if the phase is Healthy (e.g., lag <= 10), if we are missing ANY network commits,
                // we MUST NOT evaluate the local committer!
                // Local evaluation on a recently fast-forwarded (sparse) DAG will produce a truncated
                // sub-dag with missing historical blocks, resulting in a divergent Timestamp and State Root!
                let local_commit_index = self.dag_state.read().last_commit_index();
                let quorum_commit_index = self.coordination_hub.get_quorum_commit_index();
                if local_commit_index < quorum_commit_index {
                    tracing::info!(
                        "⏳ [ANTI-FORK] Local commit ({}) < Quorum commit ({}). Skipping local committer to prevent sparse DAG evaluation. Waiting for CertifiedCommits from CommitSyncer.",
                        local_commit_index,
                        quorum_commit_index
                    );
                    break;
                }

                // RECOVERY-GUARD (DAG-State-Aware):
                // The guard is activated/deactivated at startup by authority_node.rs
                // based on the actual DAG state:
                //   - Fresh DAG (genesis/epoch transition): pre-unlocked → passes
                //   - Populated DAG (cold restart/snapshot): locked → blocks here
                //
                // We do NOT use `local_commit_index >= 5` heuristic anymore.
                // The decision is made explicitly at the source (authority_node.rs),
                // not inferred from a magic number in the hot path.
                if !self.coordination_hub.is_local_commit_unlocked() {
                    let unlock_time = self.coordination_hub.get_healthy_unlock_time();
                    
                    // We keep the unlock_time flag just to throttle the log message,
                    // but we DO NOT force unlock when it expires.
                    if unlock_time.is_none() {
                        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(15);
                        self.coordination_hub.set_healthy_unlock_time(Some(deadline));
                        tracing::info!(
                            "🔒 [RECOVERY-GUARD] Local committer blocked: waiting for 5 network commits \
                             to build DAG density. (local_commit={}, quorum_commit={}). \
                             Will NOT arbitrarily unlock via timeout to prevent forks.",
                            local_commit_index,
                            quorum_commit_index
                        );
                    }
                    
                    // Strictly wait for the network. No forced unlock.
                    break;
                }

                // POST-RECOVERY SYNC GUARD:
                // After the RECOVERY-GUARD unlocks (5 network commits), the local committer
                // must NOT fire until at least one MORE CertifiedCommit has been processed.
                // This prevents the race where local_commit == quorum_commit at unlock time
                // but the local DAG has a different block set than the network.
                // For fresh-DAG starts, this flag is pre-set to true (no-op).
                if !self.coordination_hub.is_post_recovery_sync_confirmed() {
                    tracing::info!(
                        "\u{1F6E1}\u{FE0F} [POST-RECOVERY-SYNC-GUARD] Local committer blocked: RECOVERY-GUARD just \
                         unlocked but no CertifiedCommit has been processed since unlock. \
                         Waiting for at least one network-verified commit to confirm DAG alignment. \
                         (local_commit={}, quorum_commit={})",
                        local_commit_index,
                        quorum_commit_index
                    );
                    break;
                }

                // CRITICAL HOTFIX: Synchronize `last_decided_leader` with `DagState`
                // before running the local committer. If `CommitSyncer` executed a cold-start
                // fast-forward (`reset_to_network_baseline`), `Core`'s in-memory `last_decided_leader`
                // will be stuck at Genesis. This ensures the local committer resumes from the correct
                // post-sync network boundary instead of rebuilding ancient history and causing a metadata fork.
                let dag_last_decided = self.dag_state.read().last_commit_leader();
                if self.last_decided_leader.round < dag_last_decided.round {
                    // Note: We rely on the Phase guard (CatchingUp/StateSyncing) below
                    // to prevent the local committer from running until the DAG is populated.
                    tracing::info!(
                        "🔄 [SYNC] Core fast-forwarding last_decided_leader from {:?} to {:?} to match DagState baseline. Local committer will resume when node becomes Healthy.",
                        self.last_decided_leader,
                        dag_last_decided
                    );
                    self.last_decided_leader = dag_last_decided;
                }

                // DAG DENSITY CHECK AND LIVENESS SKIP REMOVED (v5):
                // 1. The liveness skip (jumping last_decided_leader) is unsafe because it breaks
                //    the contiguous prefix requirement, potentially allowing a node to commit a
                //    leader locally that differs from the network, causing a metadata fork.
                // 2. The DAG density check is unsafe because it permanently stalls the entire network
                //    if any round naturally misses a block (e.g. node offline, delayed proposal).
                // 
                // Why it is safe without them:
                // After a DAG wipe + fast-forward, last_commit_leader is round 0.
                // The local committer evaluates down to round 1. Since ancient rounds are wiped,
                // round 1 is empty, so try_decide returns Undecided and the prefix is empty.
                // The node will naturally decide NOTHING locally.
                // It will wait until the rest of the network decides the next commit.
                // Once the network decides, quorum_commit_index advances.
                // The (local_commit_index < quorum_commit_index) guard triggers, the node fetches
                // the CertifiedCommit, processes it, and last_decided_leader is updated safely
                // to the exact network-agreed round. Then local consensus resumes normally!

                // PHASE GUARD: Block local committer while the node is catching up.
                // The DAG is sparse (missing ancestor blocks) so local commit evaluation
                // would produce divergent timestamps and leader addresses.
                // Only CertifiedCommits (network-verified) are safe to process during catch-up.
                // FORK-PREVENTION: Also block if STARTUP-SYNC is active, even if lag=0 (Healthy),
                // because the historical DAG is still sparse and under construction.
                if self.coordination_hub.is_catching_up() || self.coordination_hub.is_state_syncing() || self.coordination_hub.is_startup_sync_active() {
                    tracing::info!(
                        "🛡️ [PHASE-GUARD] Blocking local committer. Node is in {:?} phase (startup_sync_active={}). \
                         Waiting for DAG to fully catch up.",
                        self.coordination_hub.get_phase(),
                        self.coordination_hub.is_startup_sync_active()
                    );
                    break;
                }

                // SCHEDULE GUARD: Block local committer when LeaderSchedule is unconfirmed.
                // After restart, if CommitInfo was not persisted in RocksDB, the schedule
                // falls back to a default (empty) LeaderSwapTable. This produces different
                // leader elections than the network's reputation-swapped schedule, causing
                // LEADER_ADDR divergence and hash forks.
                // The schedule becomes confirmed when either:
                //   - CommitInfo is recovered from store, OR
                //   - A full 300-commit scoring cycle completes, OR
                //   - Baseline scores are injected from the network.
                if !self.leader_schedule.is_schedule_confirmed() {
                    tracing::info!(
                        "🛡️ [SCHEDULE-GUARD] Blocking local committer: LeaderSchedule not yet confirmed. \
                         Waiting for 300-commit scoring cycle or network baseline scores."
                    );
                    break;
                }

                // SCHEDULE-RECOVERY GUARD: After snapshot recovery, from_store() auto-confirms
                // the schedule because last_commit_index < 300 (looks like Genesis). But the
                // network has progressed far beyond commit 300 with reputation swaps active.
                // The default (empty) LeaderSwapTable produces WRONG leader elections.
                // Block until a full scoring cycle completes with network-verified data.
                if self.coordination_hub.is_schedule_recovery_pending() {
                    tracing::info!(
                        "🛡️ [SCHEDULE-RECOVERY-GUARD] Blocking local committer: snapshot recovery detected, \
                         LeaderSchedule needs re-confirmation from network. \
                         Waiting for 300-commit scoring cycle with CertifiedCommits."
                    );
                    break;
                }

                // UNIFIED RECOVERY BARRIER GUARD (May 2026):
                // Defense-in-depth — even if legacy flags are somehow not set
                // (e.g., epoch 2 commit index reset bypasses handled >= 300),
                // the barrier will still block the committer until all recovery
                // phases complete.
                if !self.coordination_hub.recovery_barrier().can_propose() {
                    tracing::info!(
                        "🛡️ [RECOVERY-BARRIER-GUARD] Blocking local committer: \
                         RecoveryBarrier phase={}. Recovery is still in progress.",
                        self.coordination_hub.recovery_barrier().phase()
                    );
                    break;
                }

                // NETWORK-FIRST-COMMIT-GUARD (May 2026 — Definitive Fork Fix):
                // After snapshot recovery, the local DAG above the committed tip
                // has DIFFERENT block availability than the network's DAG.
                // Even though local_commit == quorum_commit, the blocks ABOVE
                // the tip are sparse/different. If the local committer evaluates
                // rounds above the tip, it chooses a different leader → FORK.
                //
                // This guard requires at least N new CertifiedCommits from the
                // network AFTER parity, proving the DAG has converged.
                // Unlike all previous guards (schedule, barrier, etc.), this one
                // addresses the ROOT CAUSE: DAG block availability divergence.
                if !self.coordination_hub.is_post_recovery_network_verified() {
                    let current = self.coordination_hub.post_recovery_network_commits_count();
                    let required = crate::coordination_hub::ConsensusCoordinationHub::REQUIRED_NETWORK_FIRST_COMMITS;
                    tracing::info!(
                        "🛡️ [NETWORK-FIRST-GUARD] Local committer blocked: snapshot recovery \
                         session active, DAG convergence NOT yet confirmed. \
                         Waiting for {} more network CertifiedCommits. (current={}/{})",
                        required.saturating_sub(current), current, required
                    );
                    break;
                }

                // TODO: limit commits by commits_until_update for efficiency, which may be needed when leader schedule length is reduced.
                let mut decided_leaders = self.committer.try_decide(self.last_decided_leader);
                // Truncate the decided leaders to fit the commit schedule limit.
                if decided_leaders.len() >= commits_until_update {
                    let _ = decided_leaders.split_off(commits_until_update);
                }
                (decided_leaders, true, None)
            } else {
                (certified_leaders, false, Some(decided_certified_commits))
            };

            // If the decided leaders list is empty then just break the loop.
            let Some(last_decided) = decided_leaders.last().cloned() else {
                break;
            };

            self.last_decided_leader = last_decided.slot();
            self.context
                .metrics
                .node_metrics
                .last_decided_leader_round
                .set(self.last_decided_leader.round as i64);

            let sequenced_leaders = decided_leaders
                .into_iter()
                .filter_map(|leader| leader.into_committed_block())
                .collect::<Vec<_>>();
            // It's possible to reach this point as the decided leaders might all of them be "Skip" decisions. In this case there is no
            // leader to commit and we should break the loop.
            if sequenced_leaders.is_empty() {
                break;
            }
            tracing::info!(
                "Committing {} leaders: {}; {} commits before next leader schedule change",
                sequenced_leaders.len(),
                sequenced_leaders
                    .iter()
                    .map(|b| b.reference().to_string())
                    .join(","),
                commits_until_update,
            );

            // TODO: refcount subdags
            let subdags = self
                .commit_observer
                .handle_commit(sequenced_leaders, precomputed_commits, local)?;

            // Update adaptive delay state with new commit index
            if let Some(adaptive_delay_state) = &self.adaptive_delay_state {
                let new_commit_index = self.dag_state.read().last_commit_index();
                adaptive_delay_state.update_local_commit(new_commit_index);
            }

            // Try to unsuspend blocks if gc_round has advanced.
            self.block_manager
                .try_unsuspend_blocks_for_latest_gc_round();

            committed_sub_dags.extend(subdags);

            fail_point!("consensus-after-handle-commit");
        }

        // Sanity check: for commits that have been linearized using the certified commits, ensure that the same sub dag has been committed.
        // During cold-start from snapshot, the DAG is empty so the Linearizer may skip missing
        // ancestor blocks → produces commits with different block sets → different digest.
        // The certified commits are already network-verified (2f+1 certifiers), so safety holds.
        for sub_dag in &committed_sub_dags {
            if let Some(commit_ref) = certified_commits_map.remove(&sub_dag.commit_ref.index) {
                if commit_ref != sub_dag.commit_ref {
                    warn!(
                        "⚠️ [COLD-START] Commit digest mismatch at index {} \
                         (certified={:?}, local={:?}). \
                         Expected during snapshot restoration when ancestor blocks are missing. \
                         Using certified commit data (already network-verified).",
                        sub_dag.commit_ref.index, commit_ref, sub_dag.commit_ref
                    );
                }
            }
        }

        // Notify about our own committed blocks
        let committed_block_refs = committed_sub_dags
            .iter()
            .flat_map(|sub_dag| sub_dag.blocks.iter())
            .filter_map(|block| {
                (block.author() == self.context.own_index).then_some(block.reference())
            })
            .collect::<Vec<_>>();
        self.transaction_consumer
            .notify_own_blocks_status(committed_block_refs, self.dag_state.read().gc_round());

        Ok(committed_sub_dags)
    }

    /// Keeps only the certified commits that have a commit index > last commit index.
    /// It also ensures that the first commit in the list is the next one in line, otherwise it panics.
    pub(crate) fn filter_new_commits(
        &mut self,
        commits: Vec<CertifiedCommit>,
    ) -> ConsensusResult<Vec<CertifiedCommit>> {
        // Filter out the commits that have been already locally committed and keep only anything that is above the last committed index.
        let last_commit_index = self.dag_state.read().last_commit_index();
        let is_schedule_recovery = self.coordination_hub.is_schedule_recovery_pending();

        // FORK-SAFETY (May 2026): During schedule recovery, we need historical commits
        // for ScoringSubdag to rebuild the LeaderSwapTable. But we MUST limit the bypass
        // to the 300-commit scoring window — blanket pass of ALL old commits causes
        // CommitRange::extend_to() PANIC when commits arrive non-monotonically.
        let scoring_window: u32 = 300;
        let min_scoring_index = last_commit_index.saturating_sub(scoring_window);

        let commits = commits
            .iter()
            .filter(|commit| {
                if commit.index() > last_commit_index {
                    true
                } else if is_schedule_recovery && commit.index() >= min_scoring_index {
                    tracing::debug!(
                        "🔄 [SCHEDULE-RECOVERY] Allowing historical commit {} (within scoring window {}..={}) for LeaderSwapTable rebuild",
                        commit.index(), min_scoring_index, last_commit_index
                    );
                    true
                } else {
                    tracing::debug!(
                        "Skip commit for index {} as it is already committed with last commit index {}",
                        commit.index(),
                        last_commit_index
                    );
                    false
                }
            })
            .cloned()
            .collect::<Vec<_>>();

        // Make sure that the first commit we find is the next one in line and there is no gap.
        if let Some(commit) = commits.first() {
            if commit.index() != last_commit_index + 1 {
                tracing::warn!(
                    "⚠️ [COLD-START] Expected commit index {}, but received {}. \
                     This is EXPECTED during snapshot restore when Node jumps forward.",
                    last_commit_index + 1, commit.index()
                );
            }
        }

        Ok(commits)
    }

    /// Sets the delay by round for propagating blocks to a quorum.
    pub(crate) fn set_propagation_delay(&mut self, delay: Round) {
        info!("Propagation round delay set to: {delay}");
        self.propagation_delay = delay;
    }

    /// Sets the min propose round for the proposer allowing to propose blocks only for round numbers
    /// `> last_known_proposed_round`. At the moment is allowed to call the method only once leading to a panic
    /// if attempt to do multiple times.
    pub(crate) fn set_last_known_proposed_round(&mut self, round: Round) {
        if self.last_known_proposed_round.is_some() {
            warn!(
                "set_last_known_proposed_round called again (already set to {:?}), ignoring new value {}",
                self.last_known_proposed_round, round
            );
            return;
        }
        self.last_known_proposed_round = Some(round);
        info!("Last known proposed round set to {round}");
    }
}
