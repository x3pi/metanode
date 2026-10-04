package processor

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
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

func TestTxValidatorPool_Type0xFF_IngressValidation(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(1337),
		TxSignatureMode: config.TxSignatureModeSecp,
	})
	vp := &TxValidatorPool{
		chainState: cs,
	}

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x1234")

	// Case 1: Wrong ChainID (e.g. 999 vs 1337)
	badChainTx := &transaction.Transaction{}
	badChainTx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		ChainID:     999,
		Type:        0xFF,
	})
	badChainTx.SetNonce(0)
	require.NoError(t, badChainTx.SignSecpProto(privKey))

	code, err := vp.AddTransactionToPool(badChainTx)
	assert.Equal(t, transaction.InvalidChainId.Code, code)
	assert.ErrorContains(t, err, "does not match node chain ID")

	// Case 2: Sign field is not empty (e.g. raw proto bytes injected into Sign field)
	badSignTx := &transaction.Transaction{}
	badSignTx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		ChainID:     1337,
		Type:        0xFF,
		Sign:        []byte{0x01, 0x02, 0x03},
	})
	badSignTx.SetNonce(0)
	code, err = vp.AddTransactionToPool(badSignTx)
	assert.Equal(t, transaction.InvalidSign.Code, code)
	assert.ErrorContains(t, err, "must not contain Sign bytes")
}


// The single and batch ingress paths share checkSecpProtoIngress; non-0xFF txs must be untouched.
func TestTxValidatorPool_checkSecpProtoIngress(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337), TxSignatureMode: config.TxSignatureModeSecp})
	vp := &TxValidatorPool{chainState: cs}

	mk := func(typ, chainID uint64, sign []byte) *transaction.Transaction {
		tx := &transaction.Transaction{}
		tx.FromProto(&pb.Transaction{Type: typ, ChainID: chainID, Sign: sign})
		return tx
	}

	code, err := vp.checkSecpProtoIngress(mk(0xFF, 1337, nil))
	assert.NoError(t, err)
	assert.Equal(t, int64(0), code)

	code, err = vp.checkSecpProtoIngress(mk(0xFF, 999, nil))
	assert.ErrorContains(t, err, "does not match node chain ID")
	assert.Equal(t, transaction.InvalidChainId.Code, code)

	code, err = vp.checkSecpProtoIngress(mk(0xFF, 1337, []byte{1}))
	assert.ErrorContains(t, err, "must not contain Sign bytes")
	assert.Equal(t, transaction.InvalidSign.Code, code)

	// Non-0xFF transactions are never touched by this check (BLS Sign / other chain IDs stay valid here).
	code, err = vp.checkSecpProtoIngress(mk(2, 999, []byte{1}))
	assert.NoError(t, err)
	assert.Equal(t, int64(0), code)

	// Legacy chain (default mode): 0xFF is disabled, every other type is untouched.
	legacy := &blockchain.ChainState{}
	legacy.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	vpLegacy := &TxValidatorPool{chainState: legacy}
	code, err = vpLegacy.checkSecpProtoIngress(mk(0xFF, 1337, nil))
	assert.ErrorContains(t, err, "disabled on this chain")
	assert.Equal(t, transaction.InvalidSign.Code, code)
	_, err = vpLegacy.checkSecpProtoIngress(mk(2, 1337, []byte{1}))
	assert.NoError(t, err)

	// Unconfigured node chain ID: 0xFF txs fail closed, everything else is untouched.
	noChain := &blockchain.ChainState{}
	noChain.SetConfig(&config.SimpleChainConfig{TxSignatureMode: config.TxSignatureModeSecp})
	vp2 := &TxValidatorPool{chainState: noChain}
	code, err = vp2.checkSecpProtoIngress(mk(0xFF, 1337, nil))
	assert.ErrorContains(t, err, "node chain ID is not configured")
	assert.Equal(t, transaction.InvalidChainId.Code, code)
	_, err = vp2.checkSecpProtoIngress(mk(2, 1337, nil))
	assert.NoError(t, err)
}
