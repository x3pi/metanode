package processor

import (
	"sync"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/types/network"
)

// ============================================================================
// TestNewSubscribeProcessor
// ============================================================================
func TestNewSubscribeProcessor(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}
	require.NotNil(t, sp)
	assert.NotNil(t, sp.messageSender)
}

// ============================================================================
// TestProcessSubscribeToAddress_SingleSubscription
// ============================================================================
func TestProcessSubscribeToAddress_SingleSubscription(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	addr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn := NewMockConnection(addr)
	subscribeAddr := e_common.HexToAddress("0x1111000000000000000000000000000000000001")

	msg := NewMockMessage("subscribe", subscribeAddr.Bytes())
	req := NewMockRequest(conn, msg)

	err := sp.ProcessSubscribeToAddress(req)
	require.NoError(t, err)

	// Verify subscriber was stored
	val, ok := sp.subscribers.Load(subscribeAddr)
	require.True(t, ok, "subscriber address should be stored")
	conns := val.([]network.Connection)
	assert.Len(t, conns, 1, "should have exactly 1 subscriber")
}

// ============================================================================
// TestProcessSubscribeToAddress_DuplicateSubscription
// ============================================================================
func TestProcessSubscribeToAddress_DuplicateSubscription(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	addr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn := NewMockConnection(addr)
	subscribeAddr := e_common.HexToAddress("0x1111000000000000000000000000000000000001")

	msg := NewMockMessage("subscribe", subscribeAddr.Bytes())
	req := NewMockRequest(conn, msg)

	// Subscribe twice with same connection
	err := sp.ProcessSubscribeToAddress(req)
	require.NoError(t, err)
	err = sp.ProcessSubscribeToAddress(req)
	require.NoError(t, err)

	// Should still have only one connection for this address (dedup)
	val, ok := sp.subscribers.Load(subscribeAddr)
	require.True(t, ok)
	conns := val.([]network.Connection)
	assert.Len(t, conns, 1, "duplicate subscription should not add another connection")
}

// ============================================================================
// TestProcessSubscribeToAddress_MultipleAddresses
// ============================================================================
func TestProcessSubscribeToAddress_MultipleAddresses(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	connAddr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn := NewMockConnection(connAddr)

	addr1 := e_common.HexToAddress("0x1111000000000000000000000000000000000001")
	addr2 := e_common.HexToAddress("0x2222000000000000000000000000000000000002")

	// Subscribe to two different addresses
	err := sp.ProcessSubscribeToAddress(NewMockRequest(conn, NewMockMessage("subscribe", addr1.Bytes())))
	require.NoError(t, err)
	err = sp.ProcessSubscribeToAddress(NewMockRequest(conn, NewMockMessage("subscribe", addr2.Bytes())))
	require.NoError(t, err)

	// Both addresses should have subscribers
	_, ok1 := sp.subscribers.Load(addr1)
	_, ok2 := sp.subscribers.Load(addr2)
	assert.True(t, ok1, "addr1 should have subscribers")
	assert.True(t, ok2, "addr2 should have subscribers")

	// Reverse map should track both addresses for this connection
	val, ok := sp.mapConnectionSubcribeAddresses.Load(conn)
	require.True(t, ok)
	addresses := val.([]e_common.Address)
	assert.Len(t, addresses, 2, "connection should be subscribed to 2 addresses")
}

// ============================================================================
// TestRemoveSubscriber_Single
// ============================================================================
func TestRemoveSubscriber_Single(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	addr1 := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn1 := NewMockConnection(addr1)
	subscribeAddr := e_common.HexToAddress("0x1111000000000000000000000000000000000001")

	msg := NewMockMessage("subscribe", subscribeAddr.Bytes())
	req := NewMockRequest(conn1, msg)

	// Subscribe
	err := sp.ProcessSubscribeToAddress(req)
	require.NoError(t, err)

	// Remove subscriber
	sp.RemoveSubcriber(conn1)

	// Verify subscriber was removed
	_, ok := sp.subscribers.Load(subscribeAddr)
	assert.False(t, ok, "subscriber should be removed after RemoveSubcriber")

	// Verify reverse map was cleaned up
	_, ok = sp.mapConnectionSubcribeAddresses.Load(conn1)
	assert.False(t, ok, "reverse map entry should be removed")
}

// ============================================================================
// TestRemoveSubscriber_OneOfMultiple
// ============================================================================
func TestRemoveSubscriber_OneOfMultiple(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	addr1 := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	addr2 := e_common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	conn1 := NewMockConnection(addr1)
	conn2 := NewMockConnection(addr2)
	subscribeAddr := e_common.HexToAddress("0x1111000000000000000000000000000000000001")

	msg := NewMockMessage("subscribe", subscribeAddr.Bytes())

	// Both connections subscribe to same address
	err := sp.ProcessSubscribeToAddress(NewMockRequest(conn1, msg))
	require.NoError(t, err)
	err = sp.ProcessSubscribeToAddress(NewMockRequest(conn2, msg))
	require.NoError(t, err)

	// Remove first connection only
	sp.RemoveSubcriber(conn1)

	// Second connection should still be subscribed
	val, ok := sp.subscribers.Load(subscribeAddr)
	require.True(t, ok, "subscriber should still exist for second connection")
	conns := val.([]network.Connection)
	assert.Len(t, conns, 1, "should have exactly 1 subscriber remaining")

	// First connection's reverse map should be gone
	_, ok = sp.mapConnectionSubcribeAddresses.Load(conn1)
	assert.False(t, ok, "first connection's reverse map should be removed")

	// Second connection's reverse map should still exist
	_, ok = sp.mapConnectionSubcribeAddresses.Load(conn2)
	assert.True(t, ok, "second connection's reverse map should remain")
}

// ============================================================================
// TestRemoveSubscriber_NonExistent
// ============================================================================
func TestRemoveSubscriber_NonExistent(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	addr := e_common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	conn := NewMockConnection(addr)

	// Removing non-existent should not panic
	sp.RemoveSubcriber(conn)
}

// ============================================================================
// TestSubscribeProcessor_ConcurrentAccess
// ============================================================================
func TestSubscribeProcessor_ConcurrentAccess(t *testing.T) {
	sender := NewMockMessageSender()
	sp := &SubscribeProcessor{
		messageSender: sender,
	}

	var wg sync.WaitGroup
	const goroutines = 20

	subscribeAddr := e_common.HexToAddress("0x1111000000000000000000000000000000000001")

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			addr := e_common.BigToAddress(e_common.Big1)
			conn := NewMockConnection(addr)

			msg := NewMockMessage("subscribe", subscribeAddr.Bytes())
			req := NewMockRequest(conn, msg)

			_ = sp.ProcessSubscribeToAddress(req)
			sp.RemoveSubcriber(conn)
		}(i)
	}
	wg.Wait()
	// If we got here without race/panic, the test passes
}


