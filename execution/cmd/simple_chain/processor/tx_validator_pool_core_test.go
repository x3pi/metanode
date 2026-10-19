package processor

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
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

// TestTxValidatorPool_MinGasPrice_EIP1559 verifies that EIP-1559 transactions
// (which have MaxGasPrice=0 and fee defined via GasFeeCap) are correctly admitted
// when GasFeeCap >= MinGasPrice, and rejected when GasFeeCap < MinGasPrice.
func TestTxValidatorPool_MinGasPrice_EIP1559(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:     big.NewInt(1337),
		MinGasPrice: 1000,
	})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{
		chainState:      cs,
		transactionPool: tp,
	}

	// 1. EIP-1559 transaction with GasFeeCap = 1500 (>= MinGasPrice 1000)
	txGood := &transaction.Transaction{}
	txGood.FromProto(&pb.Transaction{
		Type:      2,
		ChainID:   1337,
		GasFeeCap: new(big.Int).SetUint64(1500).Bytes(),
		GasTipCap: new(big.Int).SetUint64(100).Bytes(),
	})

	// skipVerification = true to test minGasPrice check in isolation without account trie
	code, err := vp.addTransactionToPoolInternalCap(txGood, true, false)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), code)

	// 2. EIP-1559 transaction with GasFeeCap = 500 (< MinGasPrice 1000)
	txLow := &transaction.Transaction{}
	txLow.FromProto(&pb.Transaction{
		Type:      2,
		ChainID:   1337,
		GasFeeCap: new(big.Int).SetUint64(500).Bytes(),
		GasTipCap: new(big.Int).SetUint64(100).Bytes(),
	})

	code, err = vp.addTransactionToPoolInternalCap(txLow, true, false)
	assert.ErrorContains(t, err, "is below node minimum")
	assert.Equal(t, transaction.InvalidTransaction.Code, code)
}

// TestTxValidatorPool_MinGasPrice_EIP1559_Batch verifies that EIP-1559 transactions
// in the batch processing path (addTransactionsToPoolInternalCap) correctly use
// GasPriceCap() instead of MaxGasPrice().
func TestTxValidatorPool_MinGasPrice_EIP1559_Batch(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:     big.NewInt(1337),
		MinGasPrice: 1000,
	})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{
		chainState:      cs,
		transactionPool: tp,
	}

	txGood := &transaction.Transaction{}
	txGood.FromProto(&pb.Transaction{
		Type:      2,
		ChainID:   1337,
		GasFeeCap: new(big.Int).SetUint64(1500).Bytes(),
		GasTipCap: new(big.Int).SetUint64(100).Bytes(),
	})

	txLow := &transaction.Transaction{}
	txLow.FromProto(&pb.Transaction{
		Type:      2,
		ChainID:   1337,
		GasFeeCap: new(big.Int).SetUint64(500).Bytes(),
		GasTipCap: new(big.Int).SetUint64(100).Bytes(),
	})

	// Run batch validation with skipVerification=true
	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{txGood, txLow}, true, false)
	require.Len(t, errs, 2)
	assert.NoError(t, errs[0], "Valid EIP-1559 batch tx must not be rejected by minGasPrice")
	assert.ErrorContains(t, errs[1], "is below node minimum", "Underpriced EIP-1559 batch tx must be rejected")
}

// TestTxValidatorPool_BatchEviction_DoesNotEvictOnInvalidBatch verifies that
// when enforceCap=true, invalid transactions in a batch do NOT trigger eviction of
// legitimate transactions already in the pool.
func TestTxValidatorPool_BatchEviction_DoesNotEvictOnInvalidBatch(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:     big.NewInt(1337),
		MinGasPrice: 1000,
	})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{
		chainState:      cs,
		transactionPool: tp,
	}

	// 1. Seed pool with a legitimate transaction
	txExisting := &transaction.Transaction{}
	txExisting.FromProto(&pb.Transaction{
		Type:        0,
		ChainID:     1337,
		MaxGasPrice: 2000,
		MaxGas:      21000,
		FromAddress: common.HexToAddress("0x111").Bytes(),
	})
	tp.AddTransaction(txExisting)
	assert.Equal(t, 1, tp.CountTransactions())

	// 2. Incoming batch with an invalid transaction (underpriced, will fail validation)
	txInvalid := &transaction.Transaction{}
	txInvalid.FromProto(&pb.Transaction{
		Type:        0,
		ChainID:     1337,
		MaxGasPrice: 100, // below minGasPrice 1000
		MaxGas:      21000,
		FromAddress: common.HexToAddress("0x222").Bytes(),
	})

	// Call with enforceCap=true
	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{txInvalid}, true, true)
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "is below node minimum")

	// 3. Existing legitimate transaction must STILL be in the pool!
	assert.Equal(t, 1, tp.CountTransactions(), "Existing pool transactions must not be evicted by invalid batch")
}



// When the pool is saturated AND an eviction pass is already running, a caller would only be rejected after paying for
// signature verification. It must be rejected before any verification (cheap early rejection), while eviction itself
// stays after verification so invalid spam cannot evict legitimate txs.
func TestTxValidatorPool_FullPoolWithEvictionInProgress_RejectsBeforeVerification(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	fill := make([]types.Transaction, 0, MaxMempoolSize)
	for i := 0; i < MaxMempoolSize; i++ {
		tx := &transaction.Transaction{}
		var from common.Address
		from[0], from[1], from[2], from[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		from[19] = 0xAB
		tx.FromProto(&pb.Transaction{Type: 0, ChainID: 1337, MaxGasPrice: 2000, MaxGas: 21000, FromAddress: from.Bytes()})
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)
	require.GreaterOrEqual(t, tp.CountTransactions(), MaxMempoolSize)

	probe := &transaction.Transaction{}
	probe.FromProto(&pb.Transaction{Type: 0, ChainID: 1337, MaxGasPrice: 2000, MaxGas: 21000, FromAddress: common.HexToAddress("0x999").Bytes()})

	// Eviction in flight: rejected up front, and the single/batch entry points agree.
	vp.evictionInProgress.Store(true)
	code, err := vp.addTransactionToPoolInternalCap(probe, true, true)
	require.Error(t, err)
	assert.Equal(t, transaction.AddToPoolError.Code, code)
	assert.ErrorContains(t, err, "eviction already in progress")
	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{probe}, true, true)
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "eviction already in progress")

	// allowEviction=false (callers that bypass eviction, like TCP) bypasses the eviction-in-progress check,
	// but still authoritatively enforces the hard capacity limit.
	_, err = vp.addTransactionToPoolInternalCap(probe, true, false)
	assert.ErrorIs(t, err, transaction.ErrPoolFull, "allowEviction=false must still enforce hard cap when pool is completely full")
	assert.NotContains(t, err.Error(), "eviction already in progress", "must bypass early eviction-in-progress rejection")

	vp.evictionInProgress.Store(false)
	assert.NoError(t, vp.rejectIfPoolFullAndEvicting(1), "no eviction running: proceed (verification, then evict-or-reject)")
}

func TestTxValidatorPool_BatchResendDuplicate_Idempotent(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	tx := transaction.NewTransaction(
		common.HexToAddress("0x1234"),
		common.HexToAddress("0xFF"),
		big.NewInt(10),
		21000,
		2000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	// First batch send: succeeds
	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{tx}, true, true)
	require.Len(t, errs, 1)
	assert.NoError(t, errs[0])
	assert.Equal(t, 1, tp.CountTransactions())

	// Second batch send with the same transaction: idempotent success, pool count remains 1
	errs = vp.addTransactionsToPoolInternalCap([]types.Transaction{tx}, true, true)
	require.Len(t, errs, 1)
	assert.NoError(t, errs[0], "Resending duplicate tx in batch must be treated as idempotent success")
	assert.Equal(t, 1, tp.CountTransactions())
}

func TestTxValidatorPool_BatchFullPoolLowFee_FloorRespected(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	fill := make([]types.Transaction, 0, MaxMempoolSize)
	for i := 0; i < MaxMempoolSize; i++ {
		var from common.Address
		from[0], from[1], from[2], from[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		from[19] = 0xAA
		tx := transaction.NewTransaction(
			from,
			common.HexToAddress("0xFF"),
			big.NewInt(1),
			21000,
			2000,
			0,
			nil,
			nil,
			common.Hash{},
			common.Hash{},
			0,
			1337,
		)
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)
	require.Equal(t, MaxMempoolSize, tp.CountTransactions())

	// Incoming batch pays gas price 1000 (< existing 2000)
	lowFeeTx := transaction.NewTransaction(
		common.HexToAddress("0xDEAD"),
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		1000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{lowFeeTx}, true, true)
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "transactions underpriced to evict")
	// No high-paying transactions in pool should have been evicted
	assert.Equal(t, MaxMempoolSize, tp.CountTransactions())
}

func TestTxValidatorPool_BatchReplacement_PriceBumpSatisfied(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	sender := common.HexToAddress("0x7777")
	txOrig := transaction.NewTransaction(
		sender,
		common.HexToAddress("0xFF"),
		big.NewInt(10),
		21000,
		1000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{txOrig}, true, true)
	require.Len(t, errs, 1)
	assert.NoError(t, errs[0])
	assert.Equal(t, 1, tp.CountTransactions())

	// Replacement with < 10% price bump (gas 1050) -> underpriced
	txUnderpriced := transaction.NewTransaction(
		sender,
		common.HexToAddress("0xFF"),
		big.NewInt(20),
		21000,
		1050,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	errs = vp.addTransactionsToPoolInternalCap([]types.Transaction{txUnderpriced}, true, true)
	require.Len(t, errs, 1)
	assert.Error(t, errs[0])
	assert.ErrorContains(t, errs[0], fmt.Sprintf("[code:%d]", transaction.ErrReplacementUnderpriced.Code))

	// Replacement with >= 10% price bump (gas 2000) -> success
	txReplacement := transaction.NewTransaction(
		sender,
		common.HexToAddress("0xFF"),
		big.NewInt(30),
		21000,
		2000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	errs = vp.addTransactionsToPoolInternalCap([]types.Transaction{txReplacement}, true, true)
	require.Len(t, errs, 1)
	assert.NoError(t, errs[0])
	assert.Equal(t, 1, tp.CountTransactions(), "Replacement must not increase pool size")
}

func TestTxValidatorPool_BatchPartialEviction_DoesNotExceedMaxCap(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	fill := make([]types.Transaction, 0, MaxMempoolSize)
	// 1 transaction with low gas (500)
	txLow := transaction.NewTransaction(
		common.HexToAddress("0x0001"),
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		500,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	fill = append(fill, txLow)

	// The remaining (MaxMempoolSize - 1) transactions with high gas (2000)
	for i := 1; i < MaxMempoolSize; i++ {
		var from common.Address
		from[0], from[1], from[2], from[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		from[19] = 0xBB
		tx := transaction.NewTransaction(
			from,
			common.HexToAddress("0xFF"),
			big.NewInt(1),
			21000,
			2000,
			0,
			nil,
			nil,
			common.Hash{},
			common.Hash{},
			0,
			1337,
		)
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)
	require.Equal(t, MaxMempoolSize, tp.CountTransactions())

	// An incoming batch has 2 net-new transactions paying 1000 (> 500, but < 2000)
	// Eviction will evict the single 500-gas tx (freeing 1 slot, mempool drops to 199,999).
	// Only 1 of the 2 incoming transactions can fit; the second must be rejected.
	txNew1 := transaction.NewTransaction(
		common.HexToAddress("0xCAFE01"),
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		1000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	txNew2 := transaction.NewTransaction(
		common.HexToAddress("0xCAFE02"),
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		1000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{txNew1, txNew2}, true, true)
	require.Len(t, errs, 2)
	assert.NoError(t, errs[0], "First tx must fit into the 1 freed slot")
	assert.ErrorContains(t, errs[1], "transactions underpriced to evict", "Second tx must be rejected because no more slots are available")
	assert.Equal(t, MaxMempoolSize, tp.CountTransactions(), "Pool size must never exceed MaxMempoolSize (e.g. 200001)")
}

func TestTxValidatorPool_MixedBatchDuplicateAndUnderpriced_DuplicateSucceeds(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	fill := make([]types.Transaction, 0, MaxMempoolSize)
	var firstTx types.Transaction
	for i := 0; i < MaxMempoolSize; i++ {
		var from common.Address
		from[0], from[1], from[2], from[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		from[19] = 0xCC
		tx := transaction.NewTransaction(
			from,
			common.HexToAddress("0xFF"),
			big.NewInt(1),
			21000,
			2000,
			0,
			nil,
			nil,
			common.Hash{},
			common.Hash{},
			0,
			1337,
		)
		if i == 0 {
			firstTx = tx
		}
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)
	require.Equal(t, MaxMempoolSize, tp.CountTransactions())

	// Batch has:
	// 1. firstTx (duplicate, already in pool)
	// 2. txLow (underpriced new tx paying 500 < 2000)
	txLow := transaction.NewTransaction(
		common.HexToAddress("0xDEADBEEF"),
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		500,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{firstTx, txLow}, true, true)
	require.Len(t, errs, 2)
	assert.NoError(t, errs[0], "Duplicate transaction in mixed batch must succeed idempotently")
	assert.ErrorContains(t, errs[1], "transactions underpriced to evict", "Underpriced transaction must fail")
	assert.Equal(t, MaxMempoolSize, tp.CountTransactions())
}

func TestTxValidatorPool_BatchReplacementTargetEvicted_DoesNotExceedMaxCap(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	fill := make([]types.Transaction, 0, MaxMempoolSize)
	targetAddr := common.HexToAddress("0xAAAA")
	txTarget := transaction.NewTransaction(
		targetAddr,
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		500,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	fill = append(fill, txTarget)

	for i := 1; i < MaxMempoolSize; i++ {
		var from common.Address
		from[0], from[1], from[2], from[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		from[19] = 0xDD
		tx := transaction.NewTransaction(
			from,
			common.HexToAddress("0xFF"),
			big.NewInt(1),
			21000,
			2000,
			0,
			nil,
			nil,
			common.Hash{},
			common.Hash{},
			0,
			1337,
		)
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)
	require.Equal(t, MaxMempoolSize, tp.CountTransactions())

	txReplace := transaction.NewTransaction(
		targetAddr,
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		1000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	txNew := transaction.NewTransaction(
		common.HexToAddress("0xBBBB"),
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		1000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	errs := vp.addTransactionsToPoolInternalCap([]types.Transaction{txReplace, txNew}, true, true)
	require.Len(t, errs, 2)
	assert.NoError(t, errs[0], "Replacement (now net-new) takes the 1 freed slot")
	assert.ErrorContains(t, errs[1], "transactions underpriced to evict", "New transaction cannot fit and must be rejected")
	assert.Equal(t, MaxMempoolSize, tp.CountTransactions(), "Mempool must not exceed MaxMempoolSize (never 200001)")
}

func TestTxValidatorPool_ConcurrentAdmission_NeverExceedsMaxCap(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	// Pre-fill pool to near capacity (MaxMempoolSize - 5)
	const initial = MaxMempoolSize - 5
	fill := make([]types.Transaction, 0, initial)
	for i := 0; i < initial; i++ {
		var from common.Address
		from[0], from[1], from[2], from[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		from[19] = 0xEE
		tx := transaction.NewTransaction(
			from,
			common.HexToAddress("0xFF"),
			big.NewInt(1),
			21000,
			2000,
			0,
			nil,
			nil,
			common.Hash{},
			common.Hash{},
			0,
			1337,
		)
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)

	// Concurrently attempt to add 20 workers (total 100 txs trying to fit into 5 slots)
	var wg sync.WaitGroup
	const workers = 20
	wg.Add(workers)

	var totalAdmitted atomic.Int64

	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			if workerID%2 == 0 {
				for j := 0; j < 5; j++ {
					var from common.Address
					from[0] = byte(workerID)
					from[1] = byte(j)
					from[18] = 0xFE
					from[19] = 0xED
					tx := transaction.NewTransaction(
						from,
						common.HexToAddress("0xFF"),
						big.NewInt(1),
						21000,
						1500, // lower fee than existing 2000, so cannot evict
						0,
						nil,
						nil,
						common.Hash{},
						common.Hash{},
						0,
						1337,
					)
					_, err := vp.addTransactionToPoolInternalCap(tx, true, true)
					if err == nil {
						totalAdmitted.Add(1)
					}
				}
			} else {
				batch := make([]types.Transaction, 0, 5)
				for j := 0; j < 5; j++ {
					var from common.Address
					from[0] = byte(workerID)
					from[1] = byte(j)
					from[18] = 0xFE
					from[19] = 0xED
					tx := transaction.NewTransaction(
						from,
						common.HexToAddress("0xFF"),
						big.NewInt(1),
						21000,
						1500, // lower fee than existing 2000, so cannot evict
						0,
						nil,
						nil,
						common.Hash{},
						common.Hash{},
						0,
						1337,
					)
					batch = append(batch, tx)
				}
				errs := vp.addTransactionsToPoolInternalCap(batch, true, true)
				for _, err := range errs {
					if err == nil {
						totalAdmitted.Add(1)
					}
				}
			}
		}(w)
	}

	wg.Wait()
	assert.Equal(t, int64(5), totalAdmitted.Load(), "Exactly 5 transactions must be admitted to fill the remaining 5 slots")
	assert.Equal(t, MaxMempoolSize, tp.CountTransactions(), "Pool count must reach exactly MaxMempoolSize and never exceed it")
}

// ── 5 COMPREHENSIVE TESTS FOR REVIEW PROPOSAL ──────────────────────────────

// Test 1: Hai request cùng sender/nonce: giao dịch underpriced phải nhận lỗi, không báo thành công giả.
func TestTxValidatorPool_ConcurrentSameSenderNonce_UnderpricedGetsError(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	from := common.HexToAddress("0xAAAA1111")
	txBase := transaction.NewTransaction(
		from,
		common.HexToAddress("0xFF"),
		big.NewInt(1),
		21000,
		2000,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)
	// Underpriced replacement: 2050 is only a 2.5% bump over 2000 (< 10% requirement)
	txUnderpriced := transaction.NewTransaction(
		from,
		common.HexToAddress("0xFF"),
		big.NewInt(2),
		21000,
		2050,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		0,
		1337,
	)

	var wg sync.WaitGroup
	wg.Add(2)
	var codeBase, codeUnderpriced int64
	var errBase, errUnderpriced error

	startGate := make(chan struct{})

	go func() {
		defer wg.Done()
		<-startGate
		codeBase, errBase = vp.addTransactionToPoolInternalCap(txBase, true, true)
	}()

	go func() {
		defer wg.Done()
		<-startGate
		codeUnderpriced, errUnderpriced = vp.addTransactionToPoolInternalCap(txUnderpriced, true, true)
	}()

	close(startGate)
	wg.Wait()

	// Exactly one transaction must succeed, and the underpriced one must receive ErrReplacementUnderpriced
	assert.Equal(t, 1, tp.CountTransactions(), "Exactly one transaction must reside in pool")
	if errBase == nil {
		assert.Equal(t, int64(0), codeBase)
		assert.ErrorIs(t, errUnderpriced, transaction.ErrReplacementUnderpriced, "Underpriced tx must receive ErrReplacementUnderpriced, not false success")
		assert.Equal(t, transaction.ErrReplacementUnderpriced.Code, codeUnderpriced)
	} else {
		// If txUnderpriced entered first (at 2050), txBase (at 2000) was underpriced compared to it
		assert.Equal(t, int64(0), codeUnderpriced)
		assert.ErrorIs(t, errBase, transaction.ErrReplacementUnderpriced, "Lower gas tx must receive ErrReplacementUnderpriced")
		assert.Equal(t, transaction.ErrReplacementUnderpriced.Code, codeBase)
	}
}

// Test 2: Batch chứa duplicate/replacement nội bộ (sequential evaluation under shard lock).
func TestTxValidatorPool_IntraBatchDuplicateAndReplacement(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	fromA := common.HexToAddress("0xAAAA2222")
	fromB := common.HexToAddress("0xBBBB2222")

	// TxA1: base tx at gas price 2000
	txA1 := transaction.NewTransaction(
		fromA, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
	)
	// TxA2: exact duplicate of TxA1
	txA2 := txA1
	// TxA3: underpriced replacement (2100 is 5% bump < 10%)
	txA3 := transaction.NewTransaction(
		fromA, common.HexToAddress("0xFF"), big.NewInt(3), 21000, 2100, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
	)
	// TxA4: valid replacement (2500 is 25% bump >= 10%)
	txA4 := transaction.NewTransaction(
		fromA, common.HexToAddress("0xFF"), big.NewInt(4), 21000, 2500, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
	)
	// TxB1: independent sender B
	txB1 := transaction.NewTransaction(
		fromB, common.HexToAddress("0xFF"), big.NewInt(5), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
	)

	batch := []types.Transaction{txA1, txA2, txA3, txA4, txB1}
	errs := vp.addTransactionsToPoolInternalCap(batch, true, true)
	require.Len(t, errs, 5)

	// 1. txA1 succeeds
	assert.NoError(t, errs[0], "txA1 must be accepted")
	// 2. txA2 duplicate succeeds idempotently
	assert.NoError(t, errs[1], "txA2 duplicate must succeed idempotently")
	// 3. txA3 underpriced rejected
	assert.Error(t, errs[2], "txA3 underpriced must be rejected")
	assert.ErrorIs(t, errs[2], transaction.ErrReplacementUnderpriced)
	// 4. txA4 valid replacement succeeds
	assert.NoError(t, errs[3], "txA4 valid replacement must succeed")
	// 5. txB1 succeeds
	assert.NoError(t, errs[4], "txB1 must succeed")

	// Pool must contain exactly 2 transactions: txA4 (replacing txA1) and txB1
	assert.Equal(t, 2, tp.CountTransactions())

	txAInPool, existsA := tp.GetTransactionByHash(txA4.Hash())
	assert.True(t, existsA, "txA4 must exist in pool")
	assert.Equal(t, txA4.Hash(), txAInPool.Hash())

	_, existsA1 := tp.GetTransactionByHash(txA1.Hash())
	assert.False(t, existsA1, "txA1 was replaced in place and must not exist in pool")

	_, existsB := tp.GetTransactionByHash(txB1.Hash())
	assert.True(t, existsB, "txB1 must exist in pool")
}

// Test 3: Drain → admission đồng thời → requeue: giữ đúng cap và không mất giao dịch đã nhận.
func TestTxValidatorPool_DrainConcurrentAdmissionRequeue_PreservesCapAndNoLoss(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	const testCap = 200
	tp.SetMaxCapacity(testCap)
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	// 1. Fill pool to capacity (distribute across shards)
	fill := make([]types.Transaction, 0, testCap)
	for i := 0; i < testCap; i++ {
		var from common.Address
		from[0] = byte(i >> 8)
		from[1] = byte(i)
		from[19] = 0x11
		tx := transaction.NewTransaction(
			from, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
		)
		fill = append(fill, tx)
	}
	tp.AddTransactions(fill)
	require.Equal(t, testCap, tp.CountTransactions())
	require.Equal(t, testCap, tp.UsedSlots())
	require.Equal(t, 0, tp.AvailableSlots())

	// 2. Drain 50 transactions
	const drainCount = 50
	drained, _ := tp.TransactionsWithAggSign(drainCount)
	require.Len(t, drained, drainCount)
	assert.Equal(t, testCap-drainCount, tp.CountTransactions())
	assert.Equal(t, drainCount, tp.InFlightTransactions())
	assert.Equal(t, testCap, tp.UsedSlots(), "UsedSlots must remain at testCap during in-flight drain")
	assert.Equal(t, 0, tp.AvailableSlots(), "AvailableSlots must remain 0 while transactions are in-flight")

	// 3. Concurrently:
	//    - New admissions arrive attempting to steal slots
	//    - Drained transactions are requeued
	var wg sync.WaitGroup
	const numAttackers = 20
	var attackersAdmitted atomic.Int64

	startGate := make(chan struct{})

	// Attackers attempting to insert net-new txs with equal gas price
	for i := 0; i < numAttackers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-startGate
			var from common.Address
			from[0], from[1], from[19] = 0xEE, byte(id), 0x99
			tx := transaction.NewTransaction(
				from, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
			)
			code, err := vp.addTransactionToPoolInternalCap(tx, true, true)
			if err == nil && code == 0 {
				attackersAdmitted.Add(1)
			}
		}(i)
	}

	// Requeue the drained transactions
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startGate
		vp.RequeueTransactions(drained)
	}()

	close(startGate)
	wg.Wait()

	// 4. Verify invariants:
	assert.Equal(t, int64(0), attackersAdmitted.Load(), "Attackers must not steal reserved in-flight slots")
	assert.Equal(t, testCap, tp.CountTransactions(), "Pool must return to exactly testCap")
	assert.Equal(t, 0, tp.InFlightTransactions(), "All in-flight reservations must be resolved")
	assert.Equal(t, testCap, tp.UsedSlots())

	// Every single drained transaction must be safely restored in the pool (zero loss)
	for _, tx := range drained {
		found, exists := tp.GetTransactionByHash(tx.Hash())
		assert.True(t, exists, "Drained transaction must be safely present in pool after requeue")
		assert.NotNil(t, found)
	}
}

// Test 4: Forwarding lỗi hoặc requeue nhiều lần: không rò rỉ hoặc giải phóng chỗ hai lần.
func TestTxValidatorPool_ForwardingErrorAndDoubleRequeue_NoLeakOrDoubleRelease(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	const testCap = 50
	tp.SetMaxCapacity(testCap)
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	// Add 20 transactions
	txs := make([]types.Transaction, 0, 20)
	for i := 0; i < 20; i++ {
		var from common.Address
		from[0], from[19] = byte(i), 0x44
		tx := transaction.NewTransaction(
			from, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
		)
		txs = append(txs, tx)
	}
	tp.AddTransactions(txs)
	require.Equal(t, 20, tp.CountTransactions())

	// Drain 10 transactions
	drained, _ := tp.TransactionsWithAggSign(10)
	require.Len(t, drained, 10)
	assert.Equal(t, 10, tp.CountTransactions())
	assert.Equal(t, 10, tp.InFlightTransactions())
	assert.Equal(t, 20, tp.UsedSlots())

	// Part A: Requeue 5 transactions due to forwarding error
	requeueBatch := drained[:5]
	vp.RequeueTransactions(requeueBatch)
	assert.Equal(t, 15, tp.CountTransactions())
	assert.Equal(t, 5, tp.InFlightTransactions())
	assert.Equal(t, 20, tp.UsedSlots())

	// Double requeue the SAME 5 transactions: their reservations were already consumed in Part A.
	// Per spec: requeue/release a second time is idempotent and must not alter counters.
	vp.RequeueTransactions(requeueBatch)
	assert.Equal(t, 15, tp.CountTransactions(), "Double requeue must not increase in-pool count")
	assert.Equal(t, 5, tp.InFlightTransactions(), "Second requeue of already-consumed reservations must not alter in-flight count")
	assert.Equal(t, 20, tp.UsedSlots())

	// Part B: Release remaining 5 transactions (forwarding success or permanent TTL drop)
	tp.ReleaseInFlight(5)
	assert.Equal(t, 0, tp.InFlightTransactions(), "InFlightTransactions must become 0")
	assert.Equal(t, 15, tp.UsedSlots())
	assert.Equal(t, 15, tp.CountTransactions())
}

// Test 5: Kiểm tra cả lỗi trả về, giao dịch thực sự còn trong pool và số chỗ đã sử dụng.
func TestTxValidatorPool_CapacityAndStatusVerification(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	tp := transaction_pool.NewTransactionPool()
	tp.SetMaxCapacity(5)
	vp := &TxValidatorPool{chainState: cs, transactionPool: tp}

	// 1. Initial empty state
	assert.Equal(t, 0, tp.CountTransactions())
	assert.Equal(t, 0, tp.InFlightTransactions())
	assert.Equal(t, 0, tp.UsedSlots())
	assert.Equal(t, 5, tp.AvailableSlots())

	// 2. Add 5 transactions to fill pool
	var filledTxs []types.Transaction
	for i := 0; i < 5; i++ {
		var from common.Address
		from[0], from[19] = byte(i), 0x55
		tx := transaction.NewTransaction(
			from, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
		)
		code, err := vp.addTransactionToPoolInternalCap(tx, true, true)
		require.NoError(t, err)
		require.Equal(t, int64(0), code)
		filledTxs = append(filledTxs, tx)
	}

	assert.Equal(t, 5, tp.CountTransactions())
	assert.Equal(t, 0, tp.InFlightTransactions())
	assert.Equal(t, 5, tp.UsedSlots())
	assert.Equal(t, 0, tp.AvailableSlots())

	// 3. Attempt to add 6th transaction with same gas price -> must fail with pool full error
	var fromExtra common.Address
	fromExtra[0], fromExtra[19] = 0x99, 0x55
	txExtra := transaction.NewTransaction(
		fromExtra, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 2000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
	)
	code, err := vp.addTransactionToPoolInternalCap(txExtra, true, true)
	assert.Error(t, err)
	assert.Equal(t, transaction.ErrPoolFull.Code, code)
	assert.True(t, errors.Is(err, transaction.ErrPoolFull))
	assert.Contains(t, err.Error(), "transaction pool is full")

	// 4. Verify all 5 original transactions remain in pool
	for _, tx := range filledTxs {
		found, exists := tp.GetTransactionByHash(tx.Hash())
		assert.True(t, exists)
		assert.NotNil(t, found)
	}
	// Extra tx must NOT be in pool
	_, existsExtra := tp.GetTransactionByHash(txExtra.Hash())
	assert.False(t, existsExtra)

	// 5. In-place replacement of filledTxs[0] with 20% higher fee (2400 >= 2000 * 1.1)
	txReplace := transaction.NewTransaction(
		filledTxs[0].FromAddress(), common.HexToAddress("0xFF"), big.NewInt(2), 21000, 2400, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337,
	)
	codeRep, errRep := vp.addTransactionToPoolInternalCap(txReplace, true, true)
	assert.NoError(t, errRep)
	assert.Equal(t, int64(0), codeRep)

	// Count and used slots remain strictly at 5
	assert.Equal(t, 5, tp.CountTransactions())
	assert.Equal(t, 5, tp.UsedSlots())

	// Old tx is gone, new tx is present
	_, oldExists := tp.GetTransactionByHash(filledTxs[0].Hash())
	assert.False(t, oldExists)
	foundNew, newExists := tp.GetTransactionByHash(txReplace.Hash())
	assert.True(t, newExists)
	assert.NotNil(t, foundNew)
}

func TestTransactionProcessor_TCPPartialRetry_PreservesOrderAndNoInfiniteLoop(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	pool := transaction_pool.NewTransactionPool()
	pool.SetMaxCapacity(3)

	vp := &TxValidatorPool{chainState: cs, transactionPool: pool}
	tp := &TransactionProcessor{
		TxValidatorPool: vp,
		chainState:      cs,
	}

	// Pre-fill pool with 2 transactions: used = 2/4 (capacity = 4)
	txA := makeTestTx(0x01, 0)
	txB := makeTestTx(0x02, 0)
	require.NoError(t, pool.AddTransaction(txA))
	require.NoError(t, pool.AddTransaction(txB))
	assert.Equal(t, 2, pool.CountTransactions())

	// Batch has 3 transactions:
	// Index 0: tx1 (valid, new)
	// Index 1: txDup (duplicate of txB, which remains in pool after txA is drained)
	// Index 2: tx2 (valid, new)
	tx1 := makeTestTx(0x10, 0)
	txDup := makeTestTx(0x02, 0) // exact duplicate of txB still in pool
	tx2 := makeTestTx(0x20, 0)

	// Since pool has 2 txs and capacity is 4:
	// Asking to admit 3 txs initially: 2 + 3 = 5 > 4.
	// In background after 30ms, drain txA and release its reservation -> used drops to 1 (only txB remains).
	// Now 1 + 3 = 4 <= 4! waitForPoolRoom unblocks!
	go func() {
		time.Sleep(30 * time.Millisecond)
		drained, dID, _ := pool.TransactionsWithDrainID(1)
		pool.ReleaseInFlightWithDrainID(drained, dID)
	}()

	chunkTxs := []types.Transaction{tx1, txDup, tx2}
	chunkErrs := make([]error, len(chunkTxs))
	pendingIndices := make([]int, len(chunkTxs))
	for k := range pendingIndices {
		pendingIndices[k] = k
	}

	start := time.Now()
	for len(pendingIndices) > 0 {
		txsToTry := make([]types.Transaction, len(pendingIndices))
		for k, idx := range pendingIndices {
			txsToTry[k] = chunkTxs[idx]
		}
		tp.admitMu.Lock()
		errs := tp.addTransactionsToPoolInternalCap(txsToTry, true, false)
		tp.admitMu.Unlock()

		var nextPending []int
		for k, err := range errs {
			origIdx := pendingIndices[k]
			if err != nil && errors.Is(err, transaction.ErrPoolFull) {
				nextPending = append(nextPending, origIdx)
			} else {
				chunkErrs[origIdx] = err
			}
		}
		if len(nextPending) > 0 {
			tp.waitForPoolRoom(1)
		}
		pendingIndices = nextPending
	}
	duration := time.Since(start)

	// Verify exact index mapping
	assert.NoError(t, chunkErrs[0], "tx1 at index 0 must succeed")
	// txDup at index 1 is idempotent duplicate (nil error in AddAdmittedTransactionsToPool)
	assert.NoError(t, chunkErrs[2], "tx2 at index 2 must succeed")

	// Must finish quickly without spinning infinitely on duplicate
	assert.Less(t, duration, 2*time.Second)
	// Final count: txB + tx1 + tx2 = 3 (txDup did not increase count)
	assert.Equal(t, 3, pool.CountTransactions())
}

// TestTransactionProcessor_TCPChunkRetry_PartialFullHandling tests the chunk retry path
// where a subset of transactions in a batch fail with ErrPoolFull due to capacity,
// are queued into nextPending, and are successfully retried on the next iteration after room is freed.
func TestTransactionProcessor_TCPChunkRetry_PartialFullHandling(t *testing.T) {
	cs := &blockchain.ChainState{}
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})
	pool := transaction_pool.NewTransactionPool()
	pool.SetMaxCapacity(3)

	vp := &TxValidatorPool{chainState: cs, transactionPool: pool}
	tp := &TransactionProcessor{
		TxValidatorPool: vp,
		chainState:      cs,
	}

	// Pool pre-fill: txA (addr 0x01) -> 1/3 used
	txA := makeTestTx(0x01, 0)
	require.NoError(t, pool.AddTransaction(txA))
	require.Equal(t, 1, pool.CountTransactions())

	// Batch has 4 transactions:
	// Index 0: tx1 (valid, new) -> occupies slot 2/3
	// Index 1: txDup (duplicate of txA) -> idempotent, occupies 0 slots
	// Index 2: tx2 (valid, new) -> occupies slot 3/3
	// Index 3: tx3 (valid, new) -> capacity reached (3/3), will return ErrPoolFull on iteration 1
	tx1 := makeTestTx(0x10, 0)
	txDup := makeTestTx(0x01, 0)
	tx2 := makeTestTx(0x20, 0)
	tx3 := makeTestTx(0x30, 0)

	// After 20ms, drain txA to create room for tx3 on retry
	go func() {
		time.Sleep(20 * time.Millisecond)
		drained, dID, _ := pool.TransactionsWithDrainID(1)
		pool.ReleaseInFlightWithDrainID(drained, dID)
	}()

	chunkTxs := []types.Transaction{tx1, txDup, tx2, tx3}
	chunkErrs := make([]error, len(chunkTxs))
	pendingIndices := make([]int, len(chunkTxs))
	for k := range pendingIndices {
		pendingIndices[k] = k
	}

	iterations := 0
	start := time.Now()
	for len(pendingIndices) > 0 {
		iterations++
		txsToTry := make([]types.Transaction, len(pendingIndices))
		for k, idx := range pendingIndices {
			txsToTry[k] = chunkTxs[idx]
		}

		tp.admitMu.Lock()
		errs := tp.addTransactionsToPoolInternalCap(txsToTry, true, false)
		tp.admitMu.Unlock()

		var nextPending []int
		for k, err := range errs {
			origIdx := pendingIndices[k]
			if err != nil && errors.Is(err, transaction.ErrPoolFull) {
				nextPending = append(nextPending, origIdx)
			} else {
				chunkErrs[origIdx] = err
			}
		}
		if len(nextPending) > 0 {
			tp.waitForPoolRoom(1)
		}
		pendingIndices = nextPending
	}
	duration := time.Since(start)

	// Verify that retry loop executed across multiple iterations
	assert.GreaterOrEqual(t, iterations, 2, "must have required at least 2 iterations to admit tx3 after drain")
	assert.Less(t, duration, 2*time.Second)

	// All transactions must eventually succeed
	for idx, err := range chunkErrs {
		assert.NoError(t, err, "tx at index %d must succeed", idx)
	}

	// Final pool contains tx1, tx2, tx3 (txA was drained and released, txDup was duplicate) = 3
	assert.Equal(t, 3, pool.CountTransactions())
}

// TestTxValidatorPool_ClassificationInvariants_NoOrphanInFlightReservations tests that
// across all 4 classification branches in ProcessTransactionsInPoolSub:
// 1. Valid tx -> returned for forwarding, reservation maintained in-flight until forwarder releases
// 2. Future tx -> requeued into mempool, reservation consumed & converted back to in-pool count
// 3. Past tx -> dropped permanently, reservation released
// 4. TTL-dropped future tx -> dropped permanently, reservation released (BLOCKER REGRESSION GUARD)
// Invariant check: only forwarded txs retain reservations, and usedSlots == len(txs) + count.
func TestTxValidatorPool_ClassificationInvariants_NoOrphanInFlightReservations(t *testing.T) {
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")
	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	require.NoError(t, err)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(1337)})

	pool := transaction_pool.NewTransactionPool()
	vp := &TxValidatorPool{
		chainState:      cs,
		transactionPool: pool,
		futureTxTimeMap: make(map[common.Hash]time.Time),
	}

	addrValid := common.Address{0x01}
	addrFuture := common.Address{0x02}
	addrTTL := common.Address{0x03}
	addrPast := common.Address{0x04}

	// addrPast has current nonce 10 in account state
	asPast, err := cs.GetAccountStateDB().AccountState(addrPast)
	require.NoError(t, err)
	asPast.SetNonce(10)
	cs.GetAccountStateDB().PublicSetDirtyAccountState(asPast)

	// Create transactions for the 4 classification branches:
	// 1. txValid: nonce 0, state nonce is 0 -> contiguous/valid
	txValid := transaction.NewTransaction(addrValid, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 1000, 0, nil, nil, common.Hash{}, common.Hash{}, 0, 1337)
	// 2. txFuture: nonce 5, state nonce is 0 -> future nonce, inserted now
	txFuture := transaction.NewTransaction(addrFuture, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 1000, 0, nil, nil, common.Hash{}, common.Hash{}, 5, 1337)
	// 3. txTTL: nonce 5, state nonce is 0 -> future nonce, but inserted earlier than FutureTxTimeout
	txTTL := transaction.NewTransaction(addrTTL, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 1000, 0, nil, nil, common.Hash{}, common.Hash{}, 5, 1337)
	vp.futureTxTimeMap[txTTL.Hash()] = time.Now().Add(-2 * FutureTxTimeout)
	// 4. txPast: nonce 2, state nonce is 10 -> past nonce (actual < expected)
	txPast := transaction.NewTransaction(addrPast, common.HexToAddress("0xFF"), big.NewInt(1), 21000, 1000, 0, nil, nil, common.Hash{}, common.Hash{}, 2, 1337)

	require.NoError(t, pool.AddTransaction(txValid))
	require.NoError(t, pool.AddTransaction(txFuture))
	require.NoError(t, pool.AddTransaction(txTTL))
	require.NoError(t, pool.AddTransaction(txPast))

	require.Equal(t, 4, pool.CountTransactions())
	require.Equal(t, 4, pool.UsedSlots())
	require.Equal(t, 0, pool.InFlightTransactions())

	// Process transactions through validator pool
	txs, drainID := vp.ProcessTransactionsInPoolSub(false, 100)

	// Invariant 1: Only the valid tx is returned for forwarding
	require.Len(t, txs, 1, "Only valid tx must be returned for forwarding")
	assert.Equal(t, txValid.Hash(), txs[0].Hash())

	// Invariant 2: Only txs returned for forwarding retain in-flight reservations
	require.Equal(t, len(txs), pool.InFlightTransactions(), "Only forwarded txs must hold in-flight reservations")

	// Invariant 3: Only the non-expired future tx was requeued in the pool
	require.Equal(t, 1, pool.CountTransactions(), "Only non-expired future tx must be requeued in pool")

	// Invariant 4: No orphaned in-flight slots across all 4 branches
	require.Equal(t, len(txs)+pool.CountTransactions(), pool.UsedSlots(), "UsedSlots must exactly equal in-flight + in-pool count")

	// Forwarder simulates successful block submission and releases in-flight reservation
	released := pool.ReleaseInFlightWithDrainID(txs, drainID)
	assert.Equal(t, 1, released)
	assert.Equal(t, 0, pool.InFlightTransactions(), "All in-flight reservations released after forwarder completes")
	assert.Equal(t, 1, pool.UsedSlots())
	assert.Equal(t, 1, pool.CountTransactions())
}

