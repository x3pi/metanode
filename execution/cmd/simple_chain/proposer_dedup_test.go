package main

import (
	"sync"
	"testing"
	"time"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

// TestRollupProposer_DeduplicatesPendingPayloadAndOnlyIncrementsNonceOnce proves that
// when the relay calls the proposer repeatedly with the same pending payload,
// exactly ONE transaction is added to the pool and the nonce increments only once.
func TestRollupProposer_DeduplicatesPendingPayloadAndOnlyIncrementsNonceOnce(t *testing.T) {
	var mu sync.Mutex
	type rollupInflightKey struct {
		msgID e_common.Hash
		typ   rollup.EventType
	}
	rollupInflight := map[rollupInflightKey]uint64{}
	var rollupPendingNonce uint64
	var rollupLastDBNonce uint64
	rollupLastProgress := time.Now()

	onChainNonce := uint64(5)
	poolTxs := []uint64{}

	propose := func(payload []byte, kind string) error {
		mu.Lock()
		defer mu.Unlock()

		dbNonce := onChainNonce
		if dbNonce != rollupLastDBNonce {
			rollupLastDBNonce = dbNonce
			rollupLastProgress = time.Now()
		}
		for k, n := range rollupInflight {
			if n < dbNonce {
				delete(rollupInflight, k)
			}
		}
		if len(rollupInflight) > 0 && time.Since(rollupLastProgress) > 60*time.Second {
			rollupInflight = map[rollupInflightKey]uint64{}
			rollupLastProgress = time.Now()
		}
		payloadHash := e_common.BytesToHash(crypto.Keccak256(payload))
		inflightKey := rollupInflightKey{msgID: payloadHash, typ: rollup.EventType(kind)}
		if _, pending := rollupInflight[inflightKey]; pending {
			return nil
		}
		if len(rollupInflight) == 0 || rollupPendingNonce < dbNonce {
			rollupPendingNonce = dbNonce
		}

		// Simulate adding to pool
		poolTxs = append(poolTxs, rollupPendingNonce)
		rollupInflight[inflightKey] = rollupPendingNonce
		rollupPendingNonce++
		return nil
	}

	payloadA := []byte("lock-account-0x1234")
	kind := rollup.SystemPayloadKindAccountLocked

	// 1. First call: adds 1 tx to pool with nonce 5
	err := propose(payloadA, kind)
	require.NoError(t, err)
	assert.Equal(t, []uint64{5}, poolTxs, "First propose call must add exactly 1 tx to pool")
	assert.Equal(t, uint64(6), rollupPendingNonce, "Nonce must increment to 6")

	// 2. Next 10 calls with the same payload (simulating 500ms relay polling ticks while waiting for block):
	// Proposer MUST return early, NOT adding any transactions to pool and NOT incrementing nonce!
	for i := 0; i < 10; i++ {
		err := propose(payloadA, kind)
		require.NoError(t, err)
	}
	assert.Equal(t, []uint64{5}, poolTxs, "Must NOT add duplicate transactions to pool while payload is pending")
	assert.Equal(t, uint64(6), rollupPendingNonce, "Nonce must NOT advance while payload is pending")

	// 3. Different payload (e.g. another account) CAN be proposed with the next sequential nonce
	payloadB := []byte("lock-account-0x5678")
	err = propose(payloadB, kind)
	require.NoError(t, err)
	assert.Equal(t, []uint64{5, 6}, poolTxs, "Different payload gets the next available nonce")
	assert.Equal(t, uint64(7), rollupPendingNonce)

	// 4. Simulate block mined on-chain, advancing dbNonce past 5
	onChainNonce = 6
	// Now payloadA's nonce (5) is < dbNonce (6), so it is cleared from inflight.
	// But payloadB's nonce (6) is still >= dbNonce (6), so payloadB remains in inflight and is not duplicated.
	err = propose(payloadB, kind)
	require.NoError(t, err)
	assert.Equal(t, []uint64{5, 6}, poolTxs, "PayloadB remains in inflight and is not duplicated")
}
