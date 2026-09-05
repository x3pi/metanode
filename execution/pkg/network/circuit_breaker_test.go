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

	// In HALF_OPEN, successes should close the circuit
	cb.RecordSuccess()
	cb.RecordSuccess()
	assert.Equal(t, StateClosed, cb.state)
	assert.True(t, cb.CanExecute())
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
