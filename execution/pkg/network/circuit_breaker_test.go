package network

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker_StateTransitionsAndReset(t *testing.T) {
	cfg := &CircuitBreakerConfig{
		Disabled:    false,
		MaxFailures: 3,
		MaxRequests: 2,
		Interval:    50 * time.Millisecond,
		Timeout:     50 * time.Millisecond,
	}

	cb := NewCircuitBreaker(cfg)
	assert.Equal(t, StateClosed, cb.state)
	assert.True(t, cb.CanExecute())

	// Record failures until circuit opens
	cb.RecordFailure()
	assert.Equal(t, StateClosed, cb.state)
	cb.RecordFailure()
	assert.Equal(t, StateClosed, cb.state)
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.state)

	// In OPEN state, CanExecute should reject
	assert.False(t, cb.CanExecute())

	// Test manual Reset()
	cb.Reset()
	assert.Equal(t, StateClosed, cb.state)
	assert.Equal(t, 0, cb.failures)
	assert.Equal(t, 0, cb.requests)
	assert.True(t, cb.CanExecute())

	// Re-trip circuit to OPEN
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.state)
	assert.False(t, cb.CanExecute())

	// Wait for Timeout to transition to HALF_OPEN
	time.Sleep(60 * time.Millisecond)
	assert.True(t, cb.CanExecute())
	assert.Equal(t, StateHalfOpen, cb.state)

	// HALF_OPEN requires MaxRequests (2) CONSECUTIVE successful trial calls to close -- a single
	// lucky success must not be enough (that would let a still-flaky endpoint's traffic reopen
	// fully on one lucky response). Each trial is a real CanExecute()+RecordSuccess() pair,
	// mirroring how every actual call site in this codebase uses the breaker; the transition call
	// above already consumed the first HALF_OPEN entry without counting as a trial.
	cb.RecordSuccess()
	assert.Equal(t, StateHalfOpen, cb.state, "one success alone must not close the circuit")
	assert.True(t, cb.CanExecute())
	cb.RecordSuccess()
	assert.Equal(t, StateHalfOpen, cb.state, "still not enough consecutive successes yet")
	assert.True(t, cb.CanExecute())
	cb.RecordSuccess()
	assert.Equal(t, StateClosed, cb.state, "MaxRequests consecutive successes must close the circuit")
	assert.True(t, cb.CanExecute())
}

// TestCircuitBreaker_HalfOpenSingleFailureReopens is the regression test for the review finding on
// PR #102 (2026-09-05): that PR's original diff made RecordSuccess() close the circuit
// unconditionally on the FIRST success while HALF_OPEN, an unscoped change to this shared package
// affecting every consumer (not just RelayerDaemon, whose actual bug is fully fixed by the new
// `Disabled` flag alone -- see its own doc comment). Reverted back to requiring MaxRequests
// consecutive successes; this test locks that back in, and also confirms a single failure among
// the trial requests still immediately reopens the circuit (unrelated to and unaffected by the
// revert, but worth locking in alongside it).
func TestCircuitBreaker_HalfOpenSingleFailureReopens(t *testing.T) {
	cfg := &CircuitBreakerConfig{
		MaxFailures: 1,
		MaxRequests: 5,
		Interval:    10 * time.Millisecond,
		Timeout:     10 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.state)

	time.Sleep(20 * time.Millisecond)
	assert.True(t, cb.CanExecute())
	assert.Equal(t, StateHalfOpen, cb.state)

	cb.RecordSuccess()
	assert.Equal(t, StateHalfOpen, cb.state, "1 success out of MaxRequests=5 must not close the circuit")

	assert.True(t, cb.CanExecute())
	cb.RecordFailure()
	assert.Equal(t, StateOpen, cb.state, "a single failure during HALF_OPEN must immediately reopen it")
}

func TestCircuitBreaker_Disabled(t *testing.T) {
	cfg := &CircuitBreakerConfig{
		Disabled:    true,
		MaxFailures: 1,
	}
	cb := NewCircuitBreaker(cfg)
	cb.RecordFailure()
	cb.RecordFailure()
	assert.True(t, cb.CanExecute())
}
