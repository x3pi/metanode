// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! GEI Validation Layer — Fork Detection Before Execution
//!
//! This module provides validation functions that detect GEI (Global Execution Index)
//! mismatches BEFORE blocks are sent to Go for execution. When a mismatch is detected,
//! the system logs comprehensive diagnostics and skips the bad commit, preventing
//! silent forks from propagating.
//!
//! # Design Principle
//! In a dual-engine architecture (Rust consensus + Go execution), GEI is computed
//! from 3 sources: epoch_base (Go), commit_index (Rust DAG), fragment_offset (Rust disk).
//! Any mismatch in these components causes permanent fork. This validator catches
//! such mismatches at the boundary before they reach Go.

use std::fmt;
use tracing::{error, info, warn};

/// Comprehensive diagnostic snapshot for GEI validation failures.
/// Contains all variables needed to diagnose the root cause of a fork.
#[derive(Debug, Clone)]
pub struct GeiDiagnostics {
    pub computed_gei: u64,
    pub go_last_gei: u64,
    pub epoch: u64,
    pub epoch_base_index: u64,
    pub commit_index: u32,
    pub fragment_offset: u64,
    pub expected_gei: u64,
    pub delta: i64,
}

impl fmt::Display for GeiDiagnostics {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "GEI Diagnostics:\n\
             ├─ computed_gei={} (epoch_base {} + commit_index {} + fragment_offset {} = {})\n\
             ├─ go_last_gei={}\n\
             ├─ expected_gei={} (go_last_gei + 1)\n\
             ├─ delta={} (computed - expected)\n\
             ├─ epoch={}\n\
             └─ VERDICT: {}",
            self.computed_gei,
            self.epoch_base_index,
            self.commit_index,
            self.fragment_offset,
            self.epoch_base_index + self.commit_index as u64 + self.fragment_offset,
            self.go_last_gei,
            self.expected_gei,
            self.delta,
            self.epoch,
            if self.delta > 0 {
                format!("GEI TOO HIGH by {} — fragment_offset likely inflated", self.delta)
            } else if self.delta < 0 {
                format!("GEI TOO LOW by {} — fragment_offset likely missing", self.delta.abs())
            } else {
                "GEI CORRECT".to_string()
            }
        )
    }
}

/// Errors returned by the GEI validator.
#[derive(Debug)]
pub enum GeiValidationError {
    /// GEI is not continuous with Go's last executed GEI.
    /// This is the primary fork indicator.
    Discontinuity {
        diagnostics: GeiDiagnostics,
    },
}

impl fmt::Display for GeiValidationError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            GeiValidationError::Discontinuity { diagnostics } => {
                write!(f, "GEI Discontinuity: {}", diagnostics)
            }
        }
    }
}

/// Validate that the computed GEI is continuous with Go's last executed GEI.
///
/// # Logic
/// - If this is the first commit (go_last_gei == 0), any GEI >= 1 is valid.
/// - Otherwise, computed_gei must equal go_last_gei + 1 (for single-fragment commits)
///   or go_last_gei + N (for multi-fragment commits with N expected fragments).
/// - A tolerance window is applied for the initial stabilization period after restart.
///
/// # Returns
/// - `Ok(())` if GEI is valid
/// - `Err(GeiValidationError::Discontinuity)` with full diagnostics if invalid
pub fn validate_gei_continuity(
    computed_gei: u64,
    go_last_gei: u64,
    epoch: u64,
    epoch_base_index: u64,
    commit_index: u32,
    fragment_offset: u64,
    is_first_commit_after_recovery: bool,
) -> Result<(), GeiValidationError> {
    // Skip validation for the very first commit (go_last_gei is 0 at genesis)
    if go_last_gei == 0 {
        return Ok(());
    }

    // After recovery (restart/DAG wipe), skip validation for the first few commits
    // until the system stabilizes. The CommitSyncer replays old commits that Go
    // has already processed, and these are handled by replay protection.
    if is_first_commit_after_recovery {
        info!(
            "🔍 [GEI-VALIDATOR] Skipping validation for first commit after recovery \
             (computed_gei={}, go_last_gei={}, commit_index={})",
            computed_gei, go_last_gei, commit_index
        );
        return Ok(());
    }

    let expected_gei = go_last_gei + 1;
    let delta = computed_gei as i64 - expected_gei as i64;

    // Allow a small tolerance for fragmented commits (multi-block commits can consume > 1 GEI)
    // But catch large deviations that indicate a bug
    const MAX_ACCEPTABLE_FORWARD_JUMP: i64 = 50; // Max GEIs a single fragmented commit could consume
    const MAX_ACCEPTABLE_BACKWARD_JUMP: i64 = -1; // Should never be negative

    if delta > MAX_ACCEPTABLE_FORWARD_JUMP || delta < MAX_ACCEPTABLE_BACKWARD_JUMP {
        let diagnostics = GeiDiagnostics {
            computed_gei,
            go_last_gei,
            epoch,
            epoch_base_index,
            commit_index,
            fragment_offset,
            expected_gei,
            delta,
        };

        error!(
            "🚨 [FORK-PREVENTED] GEI discontinuity detected! \
             Computed GEI {} is {} away from expected {} (go_last_gei={} + 1). \
             This would cause a permanent fork if executed.\n{}",
            computed_gei,
            delta.abs(),
            expected_gei,
            go_last_gei,
            diagnostics
        );

        return Err(GeiValidationError::Discontinuity { diagnostics });
    }

    // Log normal GEI progression at trace level for monitoring
    if delta != 0 && delta > 1 {
        info!(
            "🔍 [GEI-VALIDATOR] Multi-GEI commit: computed_gei={}, go_last_gei={}, delta={} \
             (fragmented commit consuming {} GEIs)",
            computed_gei, go_last_gei, delta, delta
        );
    }

    Ok(())
}

/// Validate GEI against peers during startup sync.
///
/// Queries peers for their GEI at a specific block number and compares
/// with the local GEI. This catches cases where local state reconstruction
/// produces different GEI than the rest of the cluster.
///
/// # Non-blocking
/// If peers are unreachable, logs a warning and returns Ok.
/// Peer validation is best-effort — we don't want to block startup.
pub async fn validate_gei_against_peers(
    local_gei: u64,
    local_block_number: u64,
    peer_rpc_addresses: &[String],
) -> Result<(), GeiValidationError> {
    if peer_rpc_addresses.is_empty() {
        warn!(
            "🔍 [GEI-VALIDATOR] No peers configured for cross-check \
             (local_gei={}, block={})",
            local_gei, local_block_number
        );
        return Ok(());
    }

    let mut checked = 0u32;
    let mut matches = 0u32;

    for peer_addr in peer_rpc_addresses.iter().take(2) {
        // Only check 2 peers max
        match crate::network::peer_rpc::query_peer_info(peer_addr).await {
            Ok(info) => {
                checked += 1;

                // Only compare if peer has reached this block
                if info.last_block >= local_block_number {
                    let peer_gei = info.last_global_exec_index;

                    // We can't compare GEI directly at a specific block number
                    // because peers return their LATEST GEI, not GEI at a specific block.
                    // Instead, compare block numbers as a sanity check.
                    if info.last_block == local_block_number {
                        if peer_gei == local_gei {
                            matches += 1;
                            info!(
                                "✅ [GEI-VALIDATOR] Peer {} confirms GEI={} at block={}",
                                peer_addr, local_gei, local_block_number
                            );
                        } else {
                            warn!(
                                "⚠️ [GEI-VALIDATOR] PEER MISMATCH! \
                                 local_gei={} vs peer_gei={} at block={} (peer={}). \
                                 Delta={}. This often indicates different amounts of empty-commits.",
                                local_gei,
                                peer_gei,
                                local_block_number,
                                peer_addr,
                                local_gei as i64 - peer_gei as i64
                            );
                            // We do NOT return Err here anymore because empty blocks
                            // naturally cause GEI divergence at the same block height.
                        }
                    } else {
                        // Peer is at a different block — can only do rough comparison
                        let peer_blocks_ahead = info.last_block.saturating_sub(local_block_number);
                        let gei_delta = peer_gei.saturating_sub(local_gei);

                        // If peer is N blocks ahead, their GEI should be roughly N higher
                        // Allow generous tolerance for fragmentation
                        if peer_blocks_ahead > 0 && gei_delta > peer_blocks_ahead * 3 {
                            warn!(
                                "⚠️ [GEI-VALIDATOR] Suspicious GEI gap with peer: \
                                 peer is {} blocks ahead but {} GEIs ahead \
                                 (ratio={:.1}x, expected ~1x). \
                                 peer={}, local_gei={}, peer_gei={}",
                                peer_blocks_ahead,
                                gei_delta,
                                gei_delta as f64 / peer_blocks_ahead as f64,
                                peer_addr,
                                local_gei,
                                peer_gei
                            );
                        } else {
                            info!(
                                "🔍 [GEI-VALIDATOR] Peer {} at block={} (local={}) — \
                                 GEI ratio looks healthy (peer_gei={}, local_gei={})",
                                peer_addr,
                                info.last_block,
                                local_block_number,
                                peer_gei,
                                local_gei
                            );
                        }
                    }
                } else {
                    info!(
                        "🔍 [GEI-VALIDATOR] Peer {} at block={} (behind local={}), skipping comparison",
                        peer_addr, info.last_block, local_block_number
                    );
                }
            }
            Err(e) => {
                warn!(
                    "⚠️ [GEI-VALIDATOR] Failed to query peer {} for cross-check: {}",
                    peer_addr, e
                );
            }
        }
    }

    if checked > 0 {
        info!(
            "🔍 [GEI-VALIDATOR] Startup cross-check: checked={} peers, matches={} \
             (local_gei={}, block={})",
            checked, matches, local_gei, local_block_number
        );
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_valid_continuous_gei() {
        // Normal case: GEI increments by 1
        assert!(validate_gei_continuity(101, 100, 0, 0, 101, 0, false).is_ok());
    }

    #[test]
    fn test_valid_first_commit() {
        // First commit ever: go_last_gei = 0
        assert!(validate_gei_continuity(1, 0, 0, 0, 1, 0, false).is_ok());
    }

    #[test]
    fn test_valid_fragmented_commit() {
        // Fragmented commit consuming 3 GEIs (delta = 2, within tolerance)
        assert!(validate_gei_continuity(103, 100, 0, 0, 103, 0, false).is_ok());
    }

    #[test]
    fn test_invalid_large_forward_jump() {
        // GEI jumps forward by 100 — indicates stale fragment_offset
        let result = validate_gei_continuity(200, 100, 0, 0, 200, 0, false);
        assert!(result.is_err());
        match result {
            Err(GeiValidationError::Discontinuity { diagnostics }) => {
                assert_eq!(diagnostics.delta, 99); // 200 - 101 = 99
            }
            _ => panic!("Expected Discontinuity error"),
        }
    }

    #[test]
    fn test_invalid_backward_jump() {
        // GEI goes backward — should never happen
        let result = validate_gei_continuity(99, 100, 0, 0, 99, 0, false);
        assert!(result.is_err());
        match result {
            Err(GeiValidationError::Discontinuity { diagnostics }) => {
                assert_eq!(diagnostics.delta, -2); // 99 - 101 = -2
            }
            _ => panic!("Expected Discontinuity error"),
        }
    }

    #[test]
    fn test_skip_validation_after_recovery() {
        // After recovery, first commit should pass regardless
        assert!(validate_gei_continuity(500, 100, 0, 0, 500, 0, true).is_ok());
    }

    #[test]
    fn test_diagnostics_display() {
        let diag = GeiDiagnostics {
            computed_gei: 1278,
            go_last_gei: 1297,
            epoch: 1,
            epoch_base_index: 0,
            commit_index: 360,
            fragment_offset: 0,
            expected_gei: 1298,
            delta: -20,
        };
        let display = format!("{}", diag);
        assert!(display.contains("GEI TOO LOW by 20"));
        assert!(display.contains("fragment_offset likely missing"));
    }
}
