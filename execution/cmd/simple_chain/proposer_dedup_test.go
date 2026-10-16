package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

// TestRollupProposer_DeduplicatesPendingPayloadAndOnlyIncrementsNonceOnce tests rollupNonceManager
// directly to verify:
// 1. Calling repeatedly with the same pending payload returns ok=false (skips proposing).
// 2. Exactly one transaction is added to the pool and pendingNonce only advances once.
// 3. Different payloads receive sequential nonces.
// 4. Committed transactions (nonce < dbNonce) are pruned without affecting pending transactions.
// 5. OnFailure prunes committed nonces without losing or regressing valid in-flight nonces.
func TestRollupProposer_DeduplicatesPendingPayloadAndOnlyIncrementsNonceOnce(t *testing.T) {
	mgr := newRollupNonceManager()
	onChainNonce := uint64(5)
	poolTxs := []uint64{}

	propose := func(payload []byte, kind string) error {
		mgr.Lock()
		defer mgr.Unlock()

		payloadHash := e_common.BytesToHash(crypto.Keccak256(payload))
		key := rollupInflightKey{msgID: payloadHash, typ: rollup.EventType(kind)}
		assignedNonce, ok := mgr.PrepareNonce(key, onChainNonce)
		if !ok {
			return nil
		}
		poolTxs = append(poolTxs, assignedNonce)
		mgr.OnSuccess(key, assignedNonce)
		return nil
	}

	payloadA := []byte("lock-account-0x1234")
	kind := rollup.SystemPayloadKindAccountLocked

	// 1. First call: adds 1 tx to pool with nonce 5
	err := propose(payloadA, kind)
	require.NoError(t, err)
	assert.Equal(t, []uint64{5}, poolTxs, "First propose call must add exactly 1 tx to pool")

	// 2. Next 10 calls with the same payload (simulating 500ms relay polling ticks while waiting for block):
	// Proposer MUST return early, NOT adding any transactions to pool and NOT incrementing nonce!
	for i := 0; i < 10; i++ {
		err := propose(payloadA, kind)
		require.NoError(t, err)
	}
	assert.Equal(t, []uint64{5}, poolTxs, "Must NOT add duplicate transactions to pool while payload is pending")

	// 3. Different payload (e.g. another account) CAN be proposed with the next sequential nonce
	payloadB := []byte("lock-account-0x5678")
	err = propose(payloadB, kind)
	require.NoError(t, err)
	assert.Equal(t, []uint64{5, 6}, poolTxs, "Different payload gets the next available nonce")

	// 4. Simulate block mined on-chain, advancing dbNonce past 5
	onChainNonce = 6
	// Now payloadA's nonce (5) is < dbNonce (6), so it is cleared from inflight.
	// But payloadB's nonce (6) is still >= dbNonce (6), so payloadB remains in inflight and is not duplicated.
	err = propose(payloadB, kind)
	require.NoError(t, err)
	assert.Equal(t, []uint64{5, 6}, poolTxs, "PayloadB remains in inflight and is not duplicated")

	// 5. Test pool error: OnFailure preserves valid in-flight nonces
	mgr.Lock()
	mgr.OnFailure(6)
	assert.Equal(t, uint64(7), mgr.pendingNonce, "OnFailure must not regress pendingNonce below inflight highest")
	mgr.Unlock()
}

// TestRollupProposer_ConcurrentWorkersNeverReceiveSameNonce proves that concurrent workers
// never receive duplicate nonces because the lock covers the entire proposal process.
func TestRollupProposer_ConcurrentWorkersNeverReceiveSameNonce(t *testing.T) {
	mgr := newRollupNonceManager()
	onChainNonce := uint64(5)
	var mu sync.Mutex
	allocatedNonces := []uint64{}

	workerPropose := func(payload []byte, kind string) {
		mgr.Lock()
		defer mgr.Unlock()

		payloadHash := e_common.BytesToHash(crypto.Keccak256(payload))
		key := rollupInflightKey{msgID: payloadHash, typ: rollup.EventType(kind)}
		assignedNonce, ok := mgr.PrepareNonce(key, onChainNonce)
		if !ok {
			return
		}
		// Simulate pool addition delay to stress interleaving
		time.Sleep(2 * time.Millisecond)

		mu.Lock()
		allocatedNonces = append(allocatedNonces, assignedNonce)
		mu.Unlock()

		mgr.OnSuccess(key, assignedNonce)
	}

	var wg sync.WaitGroup
	// 10 concurrent workers proposing distinct payloads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			payload := []byte(fmt.Sprintf("tx-payload-%d", workerID))
			workerPropose(payload, rollup.SystemPayloadKindAccountLocked)
		}()
	}
	wg.Wait()

	assert.Equal(t, 10, len(allocatedNonces), "All 10 workers must have added a tx")
	seen := map[uint64]bool{}
	for _, n := range allocatedNonces {
		assert.False(t, seen[n], "Nonce was assigned to more than one worker (P1 race condition)!")
		seen[n] = true
	}
	for expected := uint64(5); expected < 15; expected++ {
		assert.True(t, seen[expected], "Nonce must have been assigned")
	}
}
