package processor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The gate must be open by default and while the node is not overloaded.
func TestOverloadGate_OpenByDefault(t *testing.T) {
	setOverloaded(false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, waitNotOverloaded(ctx))
}

// While overloaded a waiter blocks (it is not rejected or dropped) and is released as soon as the node recovers.
func TestOverloadGate_BlocksUntilRecovered(t *testing.T) {
	setOverloaded(true)
	t.Cleanup(func() { setOverloaded(false) })

	done := make(chan error, 1)
	go func() { done <- waitNotOverloaded(context.Background()) }()

	select {
	case err := <-done:
		t.Fatalf("waiter returned while overloaded: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	setOverloaded(false)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waiter was not released after the overload cleared")
	}
}

// A waiter must give up when its context ends (server shutdown), never leak.
func TestOverloadGate_ContextCancelReleasesWaiter(t *testing.T) {
	setOverloaded(true)
	t.Cleanup(func() { setOverloaded(false) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitNotOverloaded(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("waiter was not released by context cancel")
	}
}

// Repeated transitions must not panic (double close) or leave the gate closed.
func TestOverloadGate_RepeatedTransitions(t *testing.T) {
	for i := 0; i < 5; i++ {
		setOverloaded(true)
		setOverloaded(true)
		setOverloaded(false)
		setOverloaded(false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, waitNotOverloaded(ctx))
}
