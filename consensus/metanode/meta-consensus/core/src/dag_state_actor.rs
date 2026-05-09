// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! DagState Single-Writer Actor
//!
//! ## Architecture
//!
//! This module implements the CQRS (Command Query Responsibility Segregation)
//! pattern for `DagState`:
//!
//! - **Reads** (`dag_state.read()`) remain unchanged — all modules continue to
//!   use `Arc<RwLock<DagState>>` for zero-overhead synchronous reads.
//! - **Writes** are routed through `DagStateWriter`, which sends commands to a
//!   dedicated `DagStateActor` thread. Only this thread calls `dag_state.write()`.
//!
//! ## Why?
//!
//! `parking_lot::RwLock` is non-reentrant: a thread holding a write-guard that
//! tries to acquire a read-guard (even transitively) will deadlock. With 17+
//! modules sharing `Arc<RwLock<DagState>>`, accidental write→read nesting is
//! inevitable as the codebase grows (we hit this exact deadlock in May 2026).
//!
//! By funneling all writes through a single actor, we guarantee:
//! 1. **No write-side deadlocks** — only one thread ever calls `.write()`
//! 2. **Deterministic write ordering** — FIFO command queue
//! 3. **Zero read-path overhead** — reads bypass the actor entirely
//!
//! ## Threading Model
//!
//! The actor runs on `std::thread::spawn` (not tokio) because `CoreThread`
//! operates in a synchronous loop. `std::sync::mpsc::Sender::send()` never
//! blocks the caller (unbounded channel) and doesn't require `.await`.

use std::sync::{mpsc, Arc};

use consensus_config::AuthorityIndex;
use consensus_types::block::BlockTimestampMs;
use parking_lot::RwLock;

use crate::{
    commit::CommitDigest,
    dag_state::DagState,
};

/// Commands that mutate `DagState`. Each variant corresponds to a public
/// `&mut self` method on `DagState`.
///
/// Variants without a `reply` field are fire-and-forget (the caller does not
/// wait for the write to complete). Variants with a reply channel provide
/// request-reply semantics for callers that need the result.
pub(crate) enum DagWriteCommand {
    /// Inject baseline reputation scores into `DagState` for LeaderSchedule
    /// recovery after snapshot restore.
    /// Replaces: `dag_state.write().baseline_reputation_scores = Some(scores)`
    InjectBaselineScores {
        scores: Vec<(AuthorityIndex, u64)>,
    },

    /// Take (consume) baseline reputation scores from `DagState`.
    /// Replaces: `dag_state.write().take_baseline_reputation_scores()`
    TakeBaselineScores {
        reply: std::sync::mpsc::Sender<Option<Vec<(AuthorityIndex, u64)>>>,
    },

    /// Reset the DAG to a network baseline (synthetic commit injection).
    /// Replaces: `dag_state.write().reset_to_network_baseline(...)`
    ResetToNetworkBaseline {
        leader_round: consensus_types::block::Round,
        commit_index: u32,
        digest: CommitDigest,
        timestamp_ms: BlockTimestampMs,
        reputation_scores: Option<Vec<(AuthorityIndex, u64)>>,
    },
}

/// Handle for sending write commands to the `DagStateActor`.
///
/// This is the public API that replaces direct `dag_state.write()` calls
/// in the critical recovery path. It is `Clone + Send + Sync`.
///
/// ## Usage
///
/// ```ignore
/// // Fire-and-forget (no waiting)
/// writer.inject_baseline_scores(scores);
///
/// // Request-reply (synchronous blocking wait)
/// let scores = writer.take_baseline_scores();
/// ```
#[derive(Clone)]
pub(crate) struct DagStateWriter {
    tx: mpsc::Sender<DagWriteCommand>,
}

impl DagStateWriter {
    /// Inject baseline reputation scores into DagState.
    /// Fire-and-forget: returns immediately without waiting for the write.
    pub(crate) fn inject_baseline_scores(&self, scores: Vec<(AuthorityIndex, u64)>) {
        if let Err(e) = self.tx.send(DagWriteCommand::InjectBaselineScores { scores }) {
            tracing::error!(
                "🔴 [DAG-WRITER] Failed to send InjectBaselineScores: actor thread dead? {}",
                e
            );
        }
    }

    /// Take (consume) baseline reputation scores from DagState.
    /// Request-reply: blocks the calling thread until the actor processes the command.
    pub(crate) fn take_baseline_scores(&self) -> Option<Vec<(AuthorityIndex, u64)>> {
        let (reply_tx, reply_rx) = mpsc::channel();
        if let Err(e) = self.tx.send(DagWriteCommand::TakeBaselineScores { reply: reply_tx }) {
            tracing::error!(
                "🔴 [DAG-WRITER] Failed to send TakeBaselineScores: actor thread dead? {}",
                e
            );
            return None;
        }
        match reply_rx.recv() {
            Ok(result) => result,
            Err(e) => {
                tracing::error!(
                    "🔴 [DAG-WRITER] Failed to receive TakeBaselineScores reply: {}",
                    e
                );
                None
            }
        }
    }

    /// Reset DagState to a network baseline (synthetic commit injection).
    /// Fire-and-forget: returns immediately without waiting for the write.
    pub(crate) fn reset_to_network_baseline(
        &self,
        leader_round: consensus_types::block::Round,
        commit_index: u32,
        digest: CommitDigest,
        timestamp_ms: BlockTimestampMs,
        reputation_scores: Option<Vec<(AuthorityIndex, u64)>>,
    ) {
        if let Err(e) = self.tx.send(DagWriteCommand::ResetToNetworkBaseline {
            leader_round,
            commit_index,
            digest,
            timestamp_ms,
            reputation_scores,
        }) {
            tracing::error!(
                "🔴 [DAG-WRITER] Failed to send ResetToNetworkBaseline: actor thread dead? {}",
                e
            );
        }
    }
}

/// The actor that processes write commands on a dedicated thread.
///
/// Only this thread ever calls `dag_state.write()`, eliminating all
/// write-side deadlock risks.
pub(crate) struct DagStateActor;

impl DagStateActor {
    /// Spawn the actor on a dedicated OS thread and return the writer handle.
    ///
    /// The actor thread runs until the `DagStateWriter` (and all its clones)
    /// are dropped, which causes `rx.recv()` to return `Err` and the loop exits.
    pub(crate) fn spawn(dag_state: Arc<RwLock<DagState>>) -> DagStateWriter {
        let (tx, rx) = mpsc::channel::<DagWriteCommand>();

        std::thread::Builder::new()
            .name("dag-state-actor".to_string())
            .spawn(move || {
                tracing::info!("🟢 [DAG-STATE-ACTOR] Started on dedicated thread");
                Self::run_loop(rx, dag_state);
                tracing::info!("🔴 [DAG-STATE-ACTOR] Stopped (all writers dropped)");
            })
            .expect("Failed to spawn DagStateActor thread");

        DagStateWriter { tx }
    }

    fn run_loop(rx: mpsc::Receiver<DagWriteCommand>, dag_state: Arc<RwLock<DagState>>) {
        while let Ok(cmd) = rx.recv() {
            match cmd {
                DagWriteCommand::InjectBaselineScores { scores } => {
                    tracing::info!(
                        "🔄 [DAG-STATE-ACTOR] InjectBaselineScores: {} authorities",
                        scores.len()
                    );
                    dag_state.write().baseline_reputation_scores = Some(scores);
                }

                DagWriteCommand::TakeBaselineScores { reply } => {
                    let scores = dag_state.write().take_baseline_reputation_scores();
                    tracing::info!(
                        "🔄 [DAG-STATE-ACTOR] TakeBaselineScores: {}",
                        if scores.is_some() { "found" } else { "none" }
                    );
                    // Ignore send error — caller may have timed out
                    let _ = reply.send(scores);
                }

                DagWriteCommand::ResetToNetworkBaseline {
                    leader_round,
                    commit_index,
                    digest,
                    timestamp_ms,
                    reputation_scores,
                } => {
                    tracing::info!(
                        "🔄 [DAG-STATE-ACTOR] ResetToNetworkBaseline: round={}, index={}, has_scores={}",
                        leader_round,
                        commit_index,
                        reputation_scores.is_some()
                    );
                    dag_state.write().reset_to_network_baseline(
                        leader_round,
                        commit_index,
                        digest,
                        timestamp_ms,
                        reputation_scores,
                    );
                }
            }
        }
    }
}
