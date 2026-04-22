# CommitSyncer State Machine Architecture Proposal

## Problem Analysis

Current architecture uses scattered boolean flags:
- `is_sync_mode: bool` - aggressive sync mode
- `is_severe_lag: bool` - severe lag detection  
- `cold_start_fast_poll_active: bool` - cold-start fast poll
- Multiple cold-start checks in different methods

This leads to:
1. **Implicit state transitions** - hard to track what state node is in
2. **Conflicting conditions** - flags can be inconsistent
3. **Deadlocks** - like the `highest_handled_index` vs `local_commit_index` issue
4. **Hard to debug** - logs show symptoms but not root cause

## Proposed State Machine

```rust
/// Explicit states for node synchronization lifecycle
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SyncState {
    /// Node just started, DAG is empty or minimal
    /// - Fast poll (200ms) to detect quorum quickly
    /// - Allow proposals despite lag (cold-start exemption)
    ColdStart {
        /// When cold-start began
        started_at: Instant,
        /// Whether we've scheduled first fetch
        first_fetch_scheduled: bool,
    },
    
    /// Node is significantly behind quorum
    /// - Aggressive sync mode (turbo/fast intervals)
    /// - Skip proposing to prioritize sync
    /// - Larger batch sizes
    CatchingUp {
        /// Lag percentage when entered this state
        lag_pct: f64,
        /// Whether lag is severe (>200 commits or >10%)
        is_severe: bool,
    },
    
    /// Node is nearly caught up
    /// - Normal sync interval (2s)
    /// - Resume proposing
    /// - Standard batch sizes
    Recovering {
        /// How many commits until fully caught up
        remaining_lag: u32,
    },
    
    /// Node is in sync with quorum
    /// - Normal operation
    /// - Standard 2s poll interval
    Healthy,
    
    /// Node is ahead of quorum (shouldn't happen normally)
    /// - Continue normal operation
    /// - Log warning for investigation
    Ahead,
}

impl SyncState {
    /// Compute next state based on current metrics
    pub fn transition(
        &self,
        local_commit: CommitIndex,
        quorum_commit: CommitIndex,
        initial_commit: CommitIndex,
        clock_round: Round,
    ) -> Self {
        let lag = quorum_commit.saturating_sub(local_commit);
        let lag_pct = if quorum_commit > 0 {
            (lag as f64 / quorum_commit as f64) * 100.0
        } else { 0.0 };
        
        // Cold-start: no local progress since startup
        let is_cold_start = local_commit <= initial_commit && quorum_commit > 0;
        
        match self {
            SyncState::ColdStart { first_fetch_scheduled, .. } => {
                if *first_fetch_scheduled && local_commit > initial_commit {
                    // First commit achieved - transition to appropriate state
                    if lag > 50 || lag_pct > 5.0 {
                        SyncState::CatchingUp {
                            lag_pct,
                            is_severe: lag > 200 || lag_pct > 10.0,
                        }
                    } else if lag > 0 {
                        SyncState::Recovering { remaining_lag: lag }
                    } else {
                        SyncState::Healthy
                    }
                } else {
                    // Stay in cold-start until first fetch or progress
                    *self
                }
            }
            
            SyncState::CatchingUp { .. } => {
                if lag == 0 {
                    SyncState::Healthy
                } else if lag <= 10 && lag_pct <= 1.0 {
                    // Nearly caught up
                    SyncState::Recovering { remaining_lag: lag }
                } else {
                    // Re-evaluate severity
                    SyncState::CatchingUp {
                        lag_pct,
                        is_severe: lag > 200 || lag_pct > 10.0,
                    }
                }
            }
            
            SyncState::Recovering { .. } => {
                if lag == 0 {
                    SyncState::Healthy
                } else if lag > 50 || lag_pct > 5.0 {
                    // Fell behind again
                    SyncState::CatchingUp {
                        lag_pct,
                        is_severe: lag > 200 || lag_pct > 10.0,
                    }
                } else {
                    SyncState::Recovering { remaining_lag: lag }
                }
            }
            
            SyncState::Healthy => {
                if is_cold_start {
                    // Shouldn't happen in healthy state, but handle gracefully
                    SyncState::ColdStart {
                        started_at: Instant::now(),
                        first_fetch_scheduled: false,
                    }
                } else if lag > 50 || lag_pct > 5.0 {
                    SyncState::CatchingUp {
                        lag_pct,
                        is_severe: lag > 200 || lag_pct > 10.0,
                    }
                } else if lag > 0 {
                    SyncState::Recovering { remaining_lag: lag }
                } else if local_commit > quorum_commit {
                    SyncState::Ahead
                } else {
                    SyncState::Healthy
                }
            }
            
            SyncState::Ahead => {
                if local_commit <= quorum_commit {
                    SyncState::Healthy
                } else {
                    SyncState::Ahead
                }
            }
        }
    }
    
    /// Get polling interval for this state
    pub fn poll_interval(&self) -> Duration {
        match self {
            SyncState::ColdStart { .. } => Duration::from_millis(200),
            SyncState::CatchingUp { is_severe: true, .. } => Duration::from_millis(150),
            SyncState::CatchingUp { .. } => Duration::from_millis(500),
            SyncState::Recovering { .. } => Duration::from_secs(1),
            SyncState::Healthy | SyncState::Ahead => Duration::from_secs(2),
        }
    }
    
    /// Whether to allow proposals in this state
    pub fn allow_proposals(&self) -> bool {
        match self {
            SyncState::ColdStart { .. } => true,  // Critical for joining
            SyncState::CatchingUp { .. } => false, // Prioritize sync
            SyncState::Recovering { .. } => true,
            SyncState::Healthy | SyncState::Ahead => true,
        }
    }
    
    /// Get batch size multiplier for this state
    pub fn batch_multiplier(&self) -> u32 {
        match self {
            SyncState::ColdStart { .. } => 1,
            SyncState::CatchingUp { is_severe: true, .. } => 4,
            SyncState::CatchingUp { .. } => 2,
            SyncState::Recovering { .. } => 1,
            SyncState::Healthy | SyncState::Ahead => 1,
        }
    }
    
    /// Whether to skip scheduling due to handler lag
    pub fn skip_on_handler_lag(&self) -> bool {
        match self {
            SyncState::ColdStart { .. } => false, // Never skip during cold-start
            SyncState::CatchingUp { .. } => false, // Need to catch up
            SyncState::Recovering { .. } | SyncState::Healthy | SyncState::Ahead => true,
        }
    }
}
```

## Benefits

1. **Explicit State** - One field tells you exactly what node is doing
2. **Centralized Logic** - All transitions in one place, easy to audit
3. **Consistent Behavior** - No conflicting flags
4. **Better Debugging** - State name in logs shows intent clearly
5. **Easier Testing** - Can unit test state transitions

## Implementation Plan

### Phase 1: Add State Machine (Backward Compatible)
1. Add `SyncState` enum
2. Add `state: SyncState` field to CommitSyncer
3. Keep existing flags for compatibility, compute from state
4. Add state to logs

### Phase 2: Migrate Logic
1. Replace flag checks with `self.state.xxx()` calls
2. Move cold-start logic into state transitions
3. Simplify `try_schedule_once()` to use state methods

### Phase 3: Remove Legacy Flags
1. Remove `is_sync_mode`, `is_severe_lag`, `cold_start_fast_poll_active`
2. Use state machine exclusively
3. Clean up scattered cold-start checks

## Example Log Output (Current vs Proposed)

### Current (scattered flags)
```
Checking to schedule: synced=10, highest_handled=10, local=10, scheduled=None
Skip scheduling: consensus handler lagging. highest_handled=10, scheduled=None
Cold-start fast poll complete. First fetch scheduled at 80
```

### Proposed (clear state)
```
[COMMIT-SYNCER] State=ColdStart{first_fetch_scheduled=false}, synced=10, lag=70
[COMMIT-SYNCER] Transition: ColdStart → CatchingUp{is_severe=false}, lag=70, local=10
[COMMIT-SYNCER] State=CatchingUp, poll=500ms, batch=2x, proposals=SKIPPED
[COMMIT-SYNCER] Transition: CatchingUp → Recovering{remaining=5}, lag=5
[COMMIT-SYNCER] State=Recovering, poll=1s, batch=1x, proposals=ALLOWED
[COMMIT-SYNCER] Transition: Recovering → Healthy
[COMMIT-SYNCER] State=Healthy, poll=2s, normal operation
```

## Additional Improvements

### Sync Progress Tracking
```rust
pub struct SyncProgress {
    /// When sync started
    started_at: Instant,
    /// Initial lag
    initial_lag: u32,
    /// Current lag
    current_lag: u32,
    /// Commits synced so far
    commits_synced: u32,
    /// Estimated time to catch up
    eta_seconds: Option<u64>,
}
```

### Metrics Per State
```rust
metrics.sync_state_changes.with_label_values(["ColdStart", "CatchingUp"]).inc();
metrics.time_in_state.with_label_values(["CatchingUp"]).observe(duration);
```
