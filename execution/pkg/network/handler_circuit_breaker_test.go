package network

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

func newCmdRequest(cmd string) network.Request {
	return NewRequest(nil, NewMessage(&pb.Message{Header: &pb.Header{Command: cmd}}))
}

func newBreakerTestHandler(timeout time.Duration, routes map[string]func(network.Request) error) *Handler {
	return &Handler{
		routes: routes,
		circuitBreaker: NewCircuitBreaker(&CircuitBreakerConfig{
			MaxFailures: 3,
			MaxRequests: 1,
			Interval:    timeout,
			Timeout:     timeout,
		}),
	}
}

// Regression (node-0 TCP latch, 2026-09-25): a request rejected by an OPEN breaker used to be
// recorded as a failure, refreshing lastFailureTime. Any client polling faster than the OPEN
// timeout kept the breaker OPEN forever.
func TestHandleRequest_RejectionsDoNotExtendOpenState(t *testing.T) {
	const timeout = 200 * time.Millisecond
	failing := errors.New("boom")
	h := newBreakerTestHandler(timeout, map[string]func(network.Request) error{
		"Flaky": func(network.Request) error { return failing },
		"Query": func(network.Request) error { return nil },
	})

	for i := 0; i < 3; i++ {
		assert.ErrorIs(t, h.HandleRequest(newCmdRequest("Flaky")), failing)
	}
	assert.Equal(t, StateOpen, h.circuitBreaker.GetState())

	// A client keeps polling faster than the timeout while the breaker is OPEN.
	deadline := time.Now().Add(2 * timeout)
	for time.Now().Before(deadline) {
		_ = h.HandleRequest(newCmdRequest("Query"))
		time.Sleep(timeout / 10)
	}

	// The breaker must have recovered (HALF_OPEN probe succeeded -> CLOSED) despite the polling.
	assert.NoError(t, h.HandleRequest(newCmdRequest("Query")))
	assert.Equal(t, StateClosed, h.circuitBreaker.GetState())
}

// Regression: an unknown command is a client protocol error, not a server health signal. Ten
// eth_call frames from one client used to open the breaker for every client.
func TestHandleRequest_UnknownCommandDoesNotTripBreaker(t *testing.T) {
	h := newBreakerTestHandler(time.Minute, map[string]func(network.Request) error{
		"Query": func(network.Request) error { return nil },
	})

	for i := 0; i < 20; i++ {
		assert.Error(t, h.HandleRequest(newCmdRequest("eth_call")))
	}

	assert.Equal(t, StateClosed, h.circuitBreaker.GetState())
	assert.NoError(t, h.HandleRequest(newCmdRequest("Query")))
}
