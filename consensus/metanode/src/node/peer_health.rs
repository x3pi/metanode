// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Peer Health Tracker - Circuit breaker pattern for peer connections
//!
//! Tracks consecutive failures per peer and applies exponential backoff
//! to prevent overwhelming unhealthy peers with requests.

use std::collections::HashMap;
use std::time::{Duration, Instant};
use tracing::{debug, warn};

/// Peer health state
#[derive(Default)]
struct PeerHealth {
    consecutive_failures: u32,
    backoff_until: Option<Instant>,
}

/// Tracks health of peers with circuit breaker backoff
pub struct PeerHealthTracker {
    peers: HashMap<u32, PeerHealth>,
}

/// Backoff thresholds
const BACKOFF_THRESHOLD_1: u32 = 3; // 3 failures → 10s backoff
const BACKOFF_THRESHOLD_2: u32 = 5; // 5 failures → 30s backoff
const BACKOFF_THRESHOLD_3: u32 = 10; // 10 failures → 60s backoff

impl PeerHealthTracker {
    pub fn new() -> Self {
        Self {
            peers: HashMap::new(),
        }
    }

    /// Check if a peer is healthy (not in backoff)
    pub fn is_healthy(&self, peer_index: u32) -> bool {
        match self.peers.get(&peer_index) {
            None => true, // Unknown peers are healthy by default
            Some(health) => {
                match health.backoff_until {
                    None => true,
                    Some(until) => Instant::now() >= until, // Backoff expired
                }
            }
        }
    }

    /// Record a successful interaction with a peer — resets failure count
    pub fn record_success(&mut self, peer_index: u32) {
        if let Some(health) = self.peers.get_mut(&peer_index) {
            if health.consecutive_failures > 0 {
                debug!(
                    "✅ [PEER-HEALTH] Peer {} recovered after {} failures",
                    peer_index, health.consecutive_failures
                );
            }
            health.consecutive_failures = 0;
            health.backoff_until = None;
        }
    }

    /// Record a failed interaction with a peer — increments failure count and may trigger backoff
    pub fn record_failure(&mut self, peer_index: u32) {
        let health = self.peers.entry(peer_index).or_default();
        health.consecutive_failures += 1;

        let backoff_duration = if health.consecutive_failures >= BACKOFF_THRESHOLD_3 {
            Some(Duration::from_secs(60))
        } else if health.consecutive_failures >= BACKOFF_THRESHOLD_2 {
            Some(Duration::from_secs(30))
        } else if health.consecutive_failures >= BACKOFF_THRESHOLD_1 {
            Some(Duration::from_secs(10))
        } else {
            None
        };

        if let Some(duration) = backoff_duration {
            health.backoff_until = Some(Instant::now() + duration);
            warn!(
                "⏸️ [PEER-HEALTH] Peer {} has {} consecutive failures. Backing off for {}s",
                peer_index,
                health.consecutive_failures,
                duration.as_secs()
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_healthy_by_default() {
        let tracker = PeerHealthTracker::new();
        assert!(tracker.is_healthy(0));
        assert!(tracker.is_healthy(99));
    }

    #[test]
    fn test_backoff_after_failures() {
        let mut tracker = PeerHealthTracker::new();

        // 1-2 failures: still healthy
        tracker.record_failure(1);
        tracker.record_failure(1);
        assert!(tracker.is_healthy(1));

        // 3rd failure: triggers backoff
        tracker.record_failure(1);
        assert!(!tracker.is_healthy(1));

        // Other peers unaffected
        assert!(tracker.is_healthy(2));
    }

    #[test]
    fn test_reset_on_success() {
        let mut tracker = PeerHealthTracker::new();

        // Accumulate failures until backoff
        for _ in 0..5 {
            tracker.record_failure(1);
        }
        assert!(!tracker.is_healthy(1));

        // Success resets everything
        tracker.record_success(1);
        assert!(tracker.is_healthy(1));
    }

    #[test]
    fn test_escalating_backoff() {
        let mut tracker = PeerHealthTracker::new();

        // 3 failures → 10s backoff
        for _ in 0..3 {
            tracker.record_failure(1);
        }
        assert!(!tracker.is_healthy(1));

        // Reset and go to 5 failures → 30s backoff
        tracker.record_success(1);
        for _ in 0..5 {
            tracker.record_failure(1);
        }
        assert!(!tracker.is_healthy(1));

        // Reset and go to 10 failures → 60s backoff
        tracker.record_success(1);
        for _ in 0..10 {
            tracker.record_failure(1);
        }
        assert!(!tracker.is_healthy(1));
    }
}
