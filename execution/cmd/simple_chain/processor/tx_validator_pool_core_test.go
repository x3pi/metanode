package processor

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// makeTestTx builds a minimal signed-shape transaction usable purely for its
// FromAddress()/GetNonce() accessors -- these tests never touch signature
// verification or execution, only nonce-cache bookkeeping.
func makeTestTx(fromByte byte, nonce uint64) types.Transaction {
	from := common.Address{}
	from[0] = fromByte
	to := common.Address{}
	to[0] = 0xFF

	return transaction.NewTransaction(
		from,
		to,
		big.NewInt(100),
		21000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		nonce,
		1,
	)
}

// TestAdvanceNoncesCacheForForwarded_AdvancesToHighestNoncePlusOne covers the
// common, single-batch case: after forwarding nonces 5,6,7 for one address,
// the cache should expect nonce 8 next.
func TestAdvanceNoncesCacheForForwarded_AdvancesToHighestNoncePlusOne(t *testing.T) {
	vp := &TxValidatorPool{}
	txs := []types.Transaction{
		makeTestTx(0x01, 5),
		makeTestTx(0x01, 6),
		makeTestTx(0x01, 7),
	}

	vp.AdvanceNoncesCacheForForwarded(txs)

	addr := txs[0].FromAddress()
	cacheVal := vp.noncesCache.Load()
	require.NotNil(t, cacheVal, "AdvanceNoncesCacheForForwarded must initialize noncesCache if it was never set")
	cache := cacheVal.(*sync.Map)
	val, ok := cache.Load(addr)
	require.True(t, ok, "expected an entry for the forwarded address")
	assert.Equal(t, uint64(8), val.(uint64), "expected-next-nonce should be highest forwarded (7) + 1")
}

// TestAdvanceNoncesCacheForForwarded_NeverRegresses is the exact regression this
// fix targets (2026-09-02's abandoned "localNonceFloor"/nonceMap-sync attempts,
// see this function's doc comment): a batch delivered out of order, or a stale
// re-delivery of an already-advanced address, must never move the cache backwards
// -- doing so would let an already-confirmed-forwarded nonce be reclassified and
// potentially reprocessed.
func TestAdvanceNoncesCacheForForwarded_NeverRegresses(t *testing.T) {
	vp := &TxValidatorPool{}
	addr := common.Address{}
	addr[0] = 0x02

	// Seed the cache as if nonce 10 had already been forwarded (expecting 11 next).
	vp.AdvanceNoncesCacheForForwarded([]types.Transaction{makeTestTx(0x02, 10)})

	cacheVal := vp.noncesCache.Load()
	cache := cacheVal.(*sync.Map)
	val, _ := cache.Load(addr)
	require.Equal(t, uint64(11), val.(uint64))

	// A late/duplicate/out-of-order batch for an EARLIER nonce must not regress it.
	vp.AdvanceNoncesCacheForForwarded([]types.Transaction{makeTestTx(0x02, 3)})

	cacheVal = vp.noncesCache.Load()
	cache = cacheVal.(*sync.Map)
	val, ok := cache.Load(addr)
	require.True(t, ok)
	assert.Equal(t, uint64(11), val.(uint64), "cache must never regress to an earlier expected-nonce")
}

// TestAdvanceNoncesCacheForForwarded_MultipleAddressesIndependent ensures one
// address's advancement never leaks into another's -- a real risk given the
// method processes an arbitrary FFI batch that can span many senders.
func TestAdvanceNoncesCacheForForwarded_MultipleAddressesIndependent(t *testing.T) {
	vp := &TxValidatorPool{}
	addrA := common.Address{}
	addrA[0] = 0xAA
	addrB := common.Address{}
	addrB[0] = 0xBB

	vp.AdvanceNoncesCacheForForwarded([]types.Transaction{
		makeTestTx(0xAA, 0),
		makeTestTx(0xAA, 1),
		makeTestTx(0xBB, 40),
	})

	cacheVal := vp.noncesCache.Load()
	cache := cacheVal.(*sync.Map)

	valA, okA := cache.Load(addrA)
	require.True(t, okA)
	assert.Equal(t, uint64(2), valA.(uint64))

	valB, okB := cache.Load(addrB)
	require.True(t, okB)
	assert.Equal(t, uint64(41), valB.(uint64))
}

// TestAdvanceNoncesCacheForForwarded_EmptyIsNoop guards against a nil-map panic
// and against accidentally initializing an empty cache when there is nothing to
// advance (StartForwardingLoop calls this once per successfully-forwarded batch,
// which should never be empty, but a defensive no-op is cheap and correct).
func TestAdvanceNoncesCacheForForwarded_EmptyIsNoop(t *testing.T) {
	vp := &TxValidatorPool{}
	vp.AdvanceNoncesCacheForForwarded(nil)
	assert.Nil(t, vp.noncesCache.Load(), "an empty call must not touch noncesCache at all")
}

// TestAddressesSuspectedStaleCache_Basic exercises the forced-refresh decision
// (StaleFutureCacheThreshold, live-reproduced fix on 234/230): an address stuck
// "future" past the threshold gets flagged; one still comfortably under it does
// not, and an address with no futureTxTimeMap entry at all (never classified
// future, or already cleaned up on becoming valid/past) is never flagged.
func TestAddressesSuspectedStaleCache_Basic(t *testing.T) {
	now := time.Now()
	threshold := 2 * time.Second

	stuckTx := makeTestTx(0x10, 44)   // stuck well past threshold
	freshTx := makeTestTx(0x11, 5)    // future, but only just now
	untrackedTx := makeTestTx(0x12, 0) // no futureTxTimeMap entry at all

	futureTxTimeMap := map[common.Hash]time.Time{
		stuckTx.Hash(): now.Add(-10 * time.Second),
		freshTx.Hash(): now.Add(-1 * time.Millisecond),
	}

	suspect := addressesSuspectedStaleCache(
		[]types.Transaction{stuckTx, freshTx, untrackedTx},
		futureTxTimeMap,
		threshold,
		now,
	)

	_, stuckFlagged := suspect[stuckTx.FromAddress()]
	assert.True(t, stuckFlagged, "a tx stuck future for 10s past a 2s threshold must be flagged")

	_, freshFlagged := suspect[freshTx.FromAddress()]
	assert.False(t, freshFlagged, "a tx only just classified future must not be flagged yet")

	_, untrackedFlagged := suspect[untrackedTx.FromAddress()]
	assert.False(t, untrackedFlagged, "a tx never recorded as future must not be flagged")
}

// TestAddressesSuspectedStaleCache_ExactBoundaryNotFlagged pins the boundary
// semantics (strictly greater than, matching ProcessTransactionsInPoolSub's own
// `time.Since(insertTime) > StaleFutureCacheThreshold` check) so a future
// refactor can't silently flip it to >= and force an extra DB read every single
// tick once a tx has been future for exactly the threshold.
func TestAddressesSuspectedStaleCache_ExactBoundaryNotFlagged(t *testing.T) {
	now := time.Now()
	threshold := 2 * time.Second
	tx := makeTestTx(0x20, 1)

	suspect := addressesSuspectedStaleCache(
		[]types.Transaction{tx},
		map[common.Hash]time.Time{tx.Hash(): now.Add(-threshold)},
		threshold,
		now,
	)

	_, flagged := suspect[tx.FromAddress()]
	assert.False(t, flagged, "exactly at the threshold should not yet be flagged (strictly greater-than)")
}

// TestAdvanceNoncesCacheForForwarded_SurvivesClearNoncesCache documents the
// safety property this fix relies on instead of a separate never-cleared
// "floor" (the approach already tried and reverted 2026-09-03): an optimistic
// advance for a batch that never actually lands on-chain is bounded to at most
// one commit cycle, because it lives in the exact same cache ClearNoncesCache()
// already wipes wholesale on every commit.
func TestAdvanceNoncesCacheForForwarded_SurvivesClearNoncesCache(t *testing.T) {
	vp := &TxValidatorPool{}
	addr := common.Address{}
	addr[0] = 0x03

	vp.AdvanceNoncesCacheForForwarded([]types.Transaction{makeTestTx(0x03, 9)})
	cacheVal := vp.noncesCache.Load()
	cache := cacheVal.(*sync.Map)
	val, ok := cache.Load(addr)
	require.True(t, ok)
	assert.Equal(t, uint64(10), val.(uint64))

	// Simulate the next commit landing (from any source), exactly like
	// block_processor_commit.go's real call after CommitBlockState succeeds.
	vp.ClearNoncesCache()

	cacheVal = vp.noncesCache.Load()
	cache = cacheVal.(*sync.Map)
	_, ok = cache.Load(addr)
	assert.False(t, ok, "ClearNoncesCache must wipe an optimistic advance just like any other cache entry")
}
