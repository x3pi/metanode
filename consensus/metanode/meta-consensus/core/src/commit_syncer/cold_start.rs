// Copyright (c) Mysten Labs, Inc.
// SPDX-License-Identifier: Apache-2.0

use super::{CommitSyncer, PhaseStateInput, PhaseTransitionDecision, CATCHING_UP_ENTER_THRESHOLD};
use crate::network::NetworkClient;

impl<C: NetworkClient> CommitSyncer<C> {
    /// Pure logic: determine what phase transition should happen.
    /// Returns a TransitionDecision describing the action, with NO side effects.
    pub(super) fn determine_phase(input: &PhaseStateInput) -> PhaseTransitionDecision {
        use crate::coordination_hub::NodeConsensusPhase::*;

        // ═══ INITIALIZING / BOOTSTRAPPING: Special exit logic ═══
        if matches!(input.current_phase, Initializing | Bootstrapping) {
            return Self::determine_bootstrap_exit(input);
        }

        // ═══ ALIGNING: Managed externally, don't touch ═══
        if matches!(input.current_phase, Aligning) {
            return PhaseTransitionDecision::Hold {
                reason: "Phase managed externally (Aligning)",
            };
        }

        // ═══ STATE SYNCING: Deep lag detected ═══
        if input.lag > 50_000 {
            return PhaseTransitionDecision::Transition { to: StateSyncing };
        }

        match input.current_phase {
            // ─── CATCHING UP during startup recovery ───
            CatchingUp if input.startup_sync_active => {
                Self::determine_startup_sync_exit(input)
            }

            // ─── CATCHING UP (normal): Stay until lag=0 ───
            CatchingUp if input.lag > 0 => PhaseTransitionDecision::Hold {
                reason: "CatchingUp — lag > 0, still syncing",
            },

            // ─── CATCHING UP (normal): Lag resolved but barrier still active → Hold ───
            CatchingUp if !input.recovery_barrier_can_propose => {
                tracing::debug!(
                    "🛡️ [STATE-MACHINE] CatchingUp lag=0 but RecoveryBarrier={}. Holding.",
                    input.recovery_barrier_phase
                );
                PhaseTransitionDecision::Hold {
                    reason: "CatchingUp — lag=0 but RecoveryBarrier not ready",
                }
            }

            // ─── CATCHING UP (normal): Lag resolved + barrier clear → Healthy ───
            CatchingUp => PhaseTransitionDecision::Transition { to: Healthy },

            // ─── HEALTHY: Enter CatchingUp if significant lag ───
            Healthy if input.lag > CATCHING_UP_ENTER_THRESHOLD => {
                PhaseTransitionDecision::Transition { to: CatchingUp }
            }

            // ─── HEALTHY: Stay healthy (lag within tolerance) ───
            Healthy => PhaseTransitionDecision::Hold {
                reason: "Healthy — lag within threshold",
            },

            // ─── Catch-all: StateSyncing stays (managed by external trigger) ───
            _ => PhaseTransitionDecision::Hold {
                reason: "Phase managed externally",
            },
        }
    }

    /// Bootstrap exit logic — determines when to leave Initializing/Bootstrapping.
    ///
    /// Two distinct scenarios:
    ///   1. GENESIS START: Go has no state, DAG empty → propose block 1
    ///   2. SNAPSHOT RESTART: Go has state, DAG wiped → wait for quorum then catch up
    pub(super) fn determine_bootstrap_exit(input: &PhaseStateInput) -> PhaseTransitionDecision {
        use crate::coordination_hub::NodeConsensusPhase::*;

        // SAFETY: If recovery barrier is active, ALWAYS go to CatchingUp (never Healthy)
        // regardless of lag. The barrier ensures the node stays in CatchingUp until
        // all recovery phases complete.
        let next_phase_for_lag = if input.lag > 0 || !input.recovery_barrier_can_propose {
            CatchingUp
        } else {
            Healthy
        };

        match (input.highest_handled, input.quorum_commit, input.go_sync_completed) {
            // ── Case 1: No local state, quorum exists → NOT genesis, DAG wipe ──
            (0, quorum, _) if quorum > 0 => {
                tracing::info!(
                    "🚀 [BOOTSTRAP] highest_handled=0 but quorum={} found. \
                     NOT genesis — DAG wipe detected. Transitioning to {:?}.",
                    quorum, next_phase_for_lag
                );
                PhaseTransitionDecision::Transition { to: next_phase_for_lag }
            }

            // ── Case 2: No local state, no quorum, network polled → GENESIS ──
            (0, 0, true) => {
                tracing::info!(
                    "🚀 [BOOTSTRAP] Genesis detected (highest_handled=0, no quorum after network poll). \
                     Transitioning to {:?} to allow block 1 proposal.",
                    next_phase_for_lag
                );
                PhaseTransitionDecision::TransitionAndClearStartup { to: next_phase_for_lag }
            }

            // ── Case 3: No local state, no quorum, still polling → WAIT ──
            (0, 0, false) => PhaseTransitionDecision::Hold {
                reason: "Bootstrap: highest_handled=0, quorum=0, waiting for network poll",
            },

            // ── Case 4: Has local state, quorum exists → SNAPSHOT RESTART ──
            (_, quorum, _) if quorum > 0 => {
                tracing::info!(
                    "🚀 [BOOTSTRAP] Snapshot restore complete. quorum={}, transitioning to {:?}.",
                    quorum, next_phase_for_lag
                );
                PhaseTransitionDecision::Transition { to: next_phase_for_lag }
            }

            // ── Case 5: Has local state, no quorum, network polled → SEED ──
            (handled, 0, true) => PhaseTransitionDecision::SeedQuorum {
                commit_index: handled,
            },

            // ── Case 6: Has local state, no quorum, still polling → WAIT ──
            (_, 0, false) => PhaseTransitionDecision::Hold {
                reason: "Bootstrap: has state but quorum=0, waiting for network poll",
            },

            // ── Unreachable: quorum can't be negative ──
            _ => PhaseTransitionDecision::Hold {
                reason: "Bootstrap: unexpected state",
            },
        }
    }

    /// Startup sync exit logic — determines when CatchingUp can transition to Healthy
    /// during post-snapshot recovery (startup_sync_active=true).
    ///
    /// Guards (all must pass):
    ///   1. lag == 0 (mathematical parity with quorum)
    ///   2. quorum_commit > 0 (real quorum, not stale)
    ///   3. quorum_commit >= highest_handled (quorum not behind execution)
    ///   4. network_synced_commits > 0 (real commits fetched, not baseline-seeded)
    ///   5. schedule_recovery_pending == false (LeaderSchedule confirmed)
    pub(super) fn determine_startup_sync_exit(input: &PhaseStateInput) -> PhaseTransitionDecision {
        // DETECT EMPTY NETWORK / WIPE:
        // If quorum is 0 but we've successfully polled the network (go_sync_completed),
        // it means all peers are starting fresh. We must bypass standard $>0$ checks.
        let is_empty_network = input.quorum_commit == 0 && input.go_sync_completed;

        // Gate 1: Mathematical parity
        //
        // Loosened from lag==0 to lag<=1 (2026-08-25 — see
        // note/cross_chain_production_readiness_plan.md Phase 0.7 / all_remaining_fixes_plan.md
        // Mục 6): on a continuously-live cluster with no idle/quiescent point (e.g. a devnet
        // timer-producing empty blocks forever), quorum_commit is a perpetually moving target —
        // observed lag stuck at exactly 1 indefinitely (synced/local commit count advancing in
        // lockstep with quorum_commit, always exactly one behind at the instant sampled), never
        // landing on the exact lag==0 this gate required, livelocking the node in CatchingUp
        // forever. This ONLY widens Gate 1, a liveness gate; Gate 5 (block_hash_verified, below)
        // is the actual fork-safety gate and is completely unchanged — a node still cannot exit
        // CatchingUp without its tip block hash being independently verified against peers.
        // Validated: (a) live on a fresh devnet, log changed from permanent "lag > 0" to
        // reaching Gate 5; (b) unit test `test_gate1_lag_tolerance_and_gate5_invariant`
        // (commit_syncer/mod.rs) proves lag==1 exits only when block_hash_verified is true, lag==1
        // with block_hash_verified==false still Holds (Gate 5 intact), and lag==2 still Holds
        // (Gate 1 doesn't over-widen). Not yet stress-tested on a continuously-live cluster
        // under real network load/latency (only a clean-genesis 4-validator run, where lag hit 0
        // immediately and this widened path was never exercised) — worth doing before relying on
        // this under sustained production load, but the decision logic itself is proven correct.
        let has_parity = input.lag <= 1
            && (input.quorum_commit > 0 || is_empty_network)
            && input.quorum_commit >= input.highest_handled;

        if !has_parity {
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: parity not reached (lag > 0 or quorum stale)",
            };
        }

        // Gate 2: Network-validated commits (prevent baseline-only false parity)
        // We bypass this requirement if:
        // 1. The network is completely empty (no commits exist to fetch).
        // 2. We already had the full DAG locally before starting (local_commit == quorum_commit).
        let needs_network_sync = !is_empty_network && input.local_commit < input.quorum_commit;
        
        if needs_network_sync && input.network_synced_commits == 0 {
            tracing::warn!(
                "⚠️ [COMMIT-SYNCER] Mathematical parity reached (synced={} >= quorum={}), \
                 but network_synced_commits=0 — no actual commits fetched from peers yet. \
                 Blocking CatchingUp→Healthy to prevent baseline-only false parity.",
                input.synced_commit_index, input.quorum_commit
            );
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: no network-validated commits yet",
            };
        }

        // Gate 3 (REMOVED): LeaderSchedule confirmed check has been removed here.
        // It now only blocks the local committer in `commit_manager.rs`. Node is allowed to
        // transition to Healthy and propose blocks to break cluster deadlocks during recovery.

        // Gate 4 (NEW — DEFINITIVE): Recovery Barrier must be Ready or Inactive.
        // This is the ARCHITECTURAL INVARIANT — it cannot be bypassed by any
        // edge case (epoch resets, threshold checks, etc.) because it tracks
        // the ACTUAL phase progression, not derived conditions.
        if !input.recovery_barrier_can_propose {
            tracing::warn!(
                "⚠️ [COMMIT-SYNCER] Mathematical parity reached and schedule confirmed, \
                 but RecoveryBarrier is NOT ready (phase={}). \
                 Node MUST NOT exit CatchingUp — recovery is still in progress.",
                input.recovery_barrier_phase
            );
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: RecoveryBarrier not ready",
            };
        }

        // Gate 5 (STRUCTURAL FIX — May 2026): Block hash at tip must be verified
        // against peers. This prevents the node from transitioning to Healthy
        // when its state has diverged from the network. Without this gate,
        // a node can achieve mathematical parity (lag=0) but still have
        // different block content at the same height — the root cause of
        // ALL recurring fork patterns (timestamp/txRoot/leader divergence).
        if !input.block_hash_verified {
            tracing::warn!(
                "⚠️ [COMMIT-SYNCER] All gates (1-4) passed but block hash NOT yet verified \
                 against peers (Gate 5). Node MUST NOT exit CatchingUp until POST-GATE-VERIFY \
                 in consensus_node.rs confirms bit-perfect block parity."
            );
            return PhaseTransitionDecision::Hold {
                reason: "Startup sync: block hash not verified against peers",
            };
        }

        // All gates passed — safe to exit startup sync
        tracing::info!(
            "✅ [COMMIT-SYNCER] Mathematical parity reached (synced={} >= quorum={}, \
             network_synced={}) and RecoveryBarrier=Ready. \
             Clearing startup_sync. Local committer will unlock after DAG density confirmed.",
            input.synced_commit_index, input.quorum_commit, input.network_synced_commits
        );
        PhaseTransitionDecision::TransitionAndClearStartup {
            to: crate::coordination_hub::NodeConsensusPhase::Healthy,
        }
    }
}


