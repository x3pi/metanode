package processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestChainState(secpMode bool, chainID int64) *blockchain.ChainState {
	cs := &blockchain.ChainState{}
	mode := config.TxSignatureModeBLSLegacy
	if secpMode {
		mode = config.TxSignatureModeSecp
	}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(chainID),
		TxSignatureMode: mode,
	})
	return cs
}

func newTestTransactionProcessor(cs *blockchain.ChainState, sender network.MessageSender) *TransactionProcessor {
	return &TransactionProcessor{
		chainState:        cs,
		messageSender:     sender,
		TxVirtualExecutor: &TxVirtualExecutor{messageSender: sender},
		injectionQueue:    make(chan injectionRequest, 100),
	}
}

func signTestTx(t *testing.T, tx *e_types.Transaction, chainID *big.Int) []byte {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	signer := e_types.LatestSignerForChainID(chainID)
	signedTx, err := e_types.SignTx(tx, signer, key)
	require.NoError(t, err)

	binary, err := signedTx.MarshalBinary()
	require.NoError(t, err)
	return binary
}

// ============================================================================
// TestSendRawTransaction_RoundTrip
// Tests Types 0x00, 0x01, 0x02 round-trip via ProcessRawTransactionFromClient
// ============================================================================
func TestSendRawTransaction_RoundTrip(t *testing.T) {
	chainID := big.NewInt(991)
	cs := createTestChainState(true, 991)

	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)

	mockConn := NewMockConnection(common.HexToAddress("0x1234"))

	// 1. Legacy EIP-155 (Type 0x00)
	legacyTx := e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x01},
		Value:    big.NewInt(100),
	})
	legacyRaw := signTestTx(t, legacyTx, chainID)
	req1 := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransaction, legacyRaw))
	err := tp.ProcessRawTransactionFromClient(req1)
	require.NoError(t, err)
	require.Len(t, tp.injectionQueue, 1)
	injected1 := <-tp.injectionQueue
	assert.True(t, injected1.rawEth)
	assert.Equal(t, legacyRaw, injected1.rawBody)

	// 2. EIP-2930 AccessList (Type 0x01)
	eip2930Tx := e_types.NewTx(&e_types.AccessListTx{
		ChainID:  chainID,
		Nonce:    2,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x02},
		Value:    big.NewInt(200),
	})
	eip2930Raw := signTestTx(t, eip2930Tx, chainID)
	req2 := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransaction, eip2930Raw))
	err = tp.ProcessRawTransactionFromClient(req2)
	require.NoError(t, err)
	require.Len(t, tp.injectionQueue, 1)
	injected2 := <-tp.injectionQueue
	assert.True(t, injected2.rawEth)
	assert.Equal(t, eip2930Raw, injected2.rawBody)

	// 3. EIP-1559 DynamicFee (Type 0x02)
	eip1559Tx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     3,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x03},
		Value:     big.NewInt(300),
	})
	eip1559Raw := signTestTx(t, eip1559Tx, chainID)
	req3 := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransaction, eip1559Raw))
	err = tp.ProcessRawTransactionFromClient(req3)
	require.NoError(t, err)
	require.Len(t, tp.injectionQueue, 1)
	injected3 := <-tp.injectionQueue
	assert.True(t, injected3.rawEth)
	assert.Equal(t, eip1559Raw, injected3.rawBody)
}

// ============================================================================
// TestSendRawTransaction_Rejections
// Tests rejection of invalid envelopes (empty, pre-EIP155, wrong chain ID, malleable s, corrupt)
// ============================================================================
func TestSendRawTransaction_Rejections(t *testing.T) {
	cs := createTestChainState(true, 991)

	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)
	mockConn := NewMockConnection(common.HexToAddress("0x1234"))

	// 1. Empty body
	reqEmpty := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransaction, nil))
	err := tp.ProcessRawTransactionFromClient(reqEmpty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty raw transaction body")

	// 2. Pre-EIP-155 (unprotected legacy) -> conversion fails
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	legacyUnprotected := e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x01},
		Value:    big.NewInt(100),
	})
	signedHomestead, err := e_types.SignTx(legacyUnprotected, e_types.HomesteadSigner{}, key)
	require.NoError(t, err)
	homesteadRaw, err := signedHomestead.MarshalBinary()
	require.NoError(t, err)

	_, _, err = tp.convertRawEth(homesteadRaw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-EIP-155 unprotected")

	// 3. Wrong Chain ID
	wrongChainID := big.NewInt(999)
	wrongChainTx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   wrongChainID,
		Nonce:     1,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x01},
		Value:     big.NewInt(100),
	})
	wrongRaw := signTestTx(t, wrongChainTx, wrongChainID)
	_, _, err = tp.convertRawEth(wrongRaw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid chain ID")

	// 4. Corrupt bytes
	_, _, err = tp.convertRawEth([]byte{0x02, 0xde, 0xad, 0xbe, 0xef})
	require.Error(t, err)
}

// ============================================================================
// TestSendRawTransactions_Batch
// Tests ProcessRawTransactionsFromClient with RLP-encoded envelope slice
// ============================================================================
func TestSendRawTransactions_Batch(t *testing.T) {
	chainID := big.NewInt(991)
	cs := createTestChainState(true, 991)

	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)
	mockConn := NewMockConnection(common.HexToAddress("0x1234"))

	// Create 2 valid transactions
	tx1 := signTestTx(t, e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x01},
		Value:     big.NewInt(100),
	}), chainID)

	tx2 := signTestTx(t, e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     2,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x02},
		Value:     big.NewInt(200),
	}), chainID)

	batchRLP, err := rlp.EncodeToBytes([][]byte{tx1, tx2})
	require.NoError(t, err)

	var decodedEnvelopes [][]byte
	err = rlp.DecodeBytes(batchRLP, &decodedEnvelopes)
	require.NoError(t, err)
	require.Len(t, decodedEnvelopes, 2)

	meta1, eth1, err := tp.convertRawEth(decodedEnvelopes[0])
	require.NoError(t, err)
	assert.NotNil(t, meta1)
	assert.NotNil(t, eth1)

	meta2, eth2, err := tp.convertRawEth(decodedEnvelopes[1])
	require.NoError(t, err)
	assert.NotNil(t, meta2)
	assert.NotNil(t, eth2)

	// Rejection of invalid RLP
	reqInvalid := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransactions, []byte{0xff, 0xff}))
	err = tp.ProcessRawTransactionsFromClient(reqInvalid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid batch RLP format")

	// Rejection of empty batch
	emptyBatchRLP, err := rlp.EncodeToBytes([][]byte{})
	require.NoError(t, err)
	reqEmpty := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransactions, emptyBatchRLP))
	err = tp.ProcessRawTransactionsFromClient(reqEmpty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty raw transaction batch")
}

// ============================================================================

// ============================================================================
// TestP0_3_RejectedTx_NoStorageOrCachePollution
// Verifies that a transaction rejected by mempool does NOT leave any entries
// in ethHashMapBlsHash or txsCache.
// ============================================================================
func TestP0_3_RejectedTx_NoStorageOrCachePollution(t *testing.T) {
	blockchain.InitBlockChain(10, nil, nil)
	bc := blockchain.GetBlockChainInstance()
	require.NotNil(t, bc)

	chainID := big.NewInt(991)
	cs := createTestChainState(true, 991)
	sender := NewMockMessageSender()
	tp := newTestTransactionProcessor(cs, sender)
	// tp.TxValidatorPool is nil, so AddTransactionToPool will reject all transactions
	mockConn := NewMockConnection(common.HexToAddress("0x1234"))

	legacyTx := e_types.NewTx(&e_types.LegacyTx{
		Nonce:    10,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x01},
		Value:    big.NewInt(100),
	})
	legacyRaw := signTestTx(t, legacyTx, chainID)
	parsedEthTx := new(e_types.Transaction)
	require.NoError(t, parsedEthTx.UnmarshalBinary(legacyRaw))
	ethHash := parsedEthTx.Hash()

	// 1. Single injection via executeAndAddTx (which handles async rawEth)
	tp.executeAndAddTx(injectionRequest{
		conn:    mockConn,
		rawBody: legacyRaw,
		rawEth:  true,
		msgID:   "msg-test-1",
	})

	// Verify no pollution in ethHashMapBlsHash
	_, ok := bc.GetEthHashMapblsHash(ethHash)
	assert.False(t, ok, "Rejected transaction must NOT have entry in ethHashMapBlsHash")

	// Verify no pollution in txsCache
	_, ok = bc.GetTxFromCache(ethHash)
	assert.False(t, ok, "Rejected transaction must NOT have entry in txsCache")

	// 2. Batch injection via ProcessRawTransactionsFromClient
	batchRLP, err := rlp.EncodeToBytes([][]byte{legacyRaw})
	require.NoError(t, err)
	reqBatch := NewMockRequest(mockConn, NewMockMessage(command.SendRawTransactions, batchRLP))
	_ = tp.ProcessRawTransactionsFromClient(reqBatch)

	_, ok = bc.GetEthHashMapblsHash(ethHash)
	assert.False(t, ok, "Rejected batch transaction must NOT have entry in ethHashMapBlsHash")

	_, ok = bc.GetTxFromCache(ethHash)
	assert.False(t, ok, "Rejected batch transaction must NOT have entry in txsCache")
}


