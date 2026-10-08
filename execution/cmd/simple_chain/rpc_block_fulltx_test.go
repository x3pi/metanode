package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Helper to sign an authorization tuple for EIP-7702 test
func signTestAuth(t *testing.T, key *ecdsa.PrivateKey, chainID uint64, delegate common.Address, nonce uint64) e_types.SetCodeAuthorization {
	t.Helper()
	auth, err := e_types.SignSetCode(key, e_types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(chainID),
		Address: delegate,
		Nonce:   nonce,
	})
	require.NoError(t, err)
	return auth
}

func TestMarshalBlockToMapWithGas_FullTx_AllTxTypes(t *testing.T) {
	require.NoError(t, trie.InitNomtDB(t.TempDir(), 1, 16, 16))
	defer trie.CloseNomtDB()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(key.PublicKey)
	targetAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")
	chainID := big.NewInt(991)
	signerLondon := e_types.NewLondonSigner(chainID)
	signerPrague := e_types.NewPragueSigner(chainID)

	// 1. Legacy Tx (Type 0)
	legacyEth, err := e_types.SignNewTx(key, e_types.NewEIP155Signer(chainID), &e_types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20_000_000_000),
		Gas:      21000,
		To:       &targetAddr,
		Value:    big.NewInt(1_000_000),
	})
	require.NoError(t, err)
	legacyMt, err := transaction.NewTransactionFromEth(legacyEth)
	require.NoError(t, err)

	// 2. EIP-2930 Tx (Type 1)
	eip2930Eth, err := e_types.SignNewTx(key, signerLondon, &e_types.AccessListTx{
		ChainID:  chainID,
		Nonce:    1,
		GasPrice: big.NewInt(20_000_000_000),
		Gas:      25000,
		To:       &targetAddr,
		Value:    big.NewInt(2_000_000),
		AccessList: e_types.AccessList{
			{Address: targetAddr, StorageKeys: []common.Hash{common.HexToHash("0x01")}},
		},
	})
	require.NoError(t, err)
	eip2930Mt, err := transaction.NewTransactionFromEth(eip2930Eth)
	require.NoError(t, err)

	// 3. EIP-1559 Tx (Type 2) with tip > 0
	eip1559Eth, err := e_types.SignNewTx(key, signerLondon, &e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     2,
		GasTipCap: big.NewInt(2_000_000_000),
		GasFeeCap: big.NewInt(30_000_000_000),
		Gas:       30000,
		To:        &targetAddr,
		Value:     big.NewInt(3_000_000),
	})
	require.NoError(t, err)
	eip1559Mt, err := transaction.NewTransactionFromEth(eip1559Eth)
	require.NoError(t, err)

	// 4. EIP-1559 Tx (Type 2) with tip == 0
	eip1559ZeroTipEth, err := e_types.SignNewTx(key, signerLondon, &e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     3,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(30_000_000_000),
		Gas:       30000,
		To:        &targetAddr,
		Value:     big.NewInt(4_000_000),
	})
	require.NoError(t, err)
	eip1559ZeroTipMt, err := transaction.NewTransactionFromEth(eip1559ZeroTipEth)
	require.NoError(t, err)

	// 5. EIP-7702 Tx (Type 4)
	authKey, _ := crypto.GenerateKey()
	delegateAddr := common.HexToAddress("0x7702770277027702770277027702770277027702")
	authTuple := signTestAuth(t, authKey, chainID.Uint64(), delegateAddr, 0)
	eip7702Eth, err := e_types.SignNewTx(key, signerPrague, &e_types.SetCodeTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     4,
		GasTipCap: uint256.NewInt(1_000_000_000),
		GasFeeCap: uint256.NewInt(25_000_000_000),
		Gas:       50000,
		To:        targetAddr,
		Value:     uint256.NewInt(5_000_000),
		AuthList:  []e_types.SetCodeAuthorization{authTuple},
	})
	require.NoError(t, err)
	eip7702Mt, err := transaction.NewTransactionFromEth(eip7702Eth)
	require.NoError(t, err)

	// 6. Contract Deployment Tx (Legacy with To = nil)
	deployEth, err := e_types.SignNewTx(key, e_types.NewEIP155Signer(chainID), &e_types.LegacyTx{
		Nonce:    5,
		GasPrice: big.NewInt(20_000_000_000),
		Gas:      100000,
		To:       nil,
		Value:    big.NewInt(0),
		Data:     []byte{0x60, 0x80, 0x60, 0x40},
	})
	require.NoError(t, err)
	deployMt, err := transaction.NewTransactionFromEth(deployEth)
	require.NoError(t, err)

	txList := []mt_types.Transaction{
		legacyMt,
		eip2930Mt,
		eip1559Mt,
		eip1559ZeroTipMt,
		eip7702Mt,
		deployMt,
	}

	txMap := make(map[common.Hash]mt_types.Transaction)
	txHashes := make([]common.Hash, len(txList))
	for i, tx := range txList {
		h := tx.Hash()
		txHashes[i] = h
		txMap[h] = tx
	}

	blockNumber := uint64(555)

	// Create receipts stored in memory DB to stamp groupId and transactionIndex
	memDb := storage.NewMemoryDb()
	rcpDb, err := receipt.NewReceipts(memDb)
	require.NoError(t, err)

	for i, tx := range txList {
		rcp := receipt.NewReceipt(
			tx.Hash(),
			fromAddr,
			targetAddr,
			big.NewInt(0),
			pb.RECEIPT_STATUS_RETURNED,
			nil,
			pb.EXCEPTION_NONE,
			21000,
			21000,
			nil,
			uint64(i),
			common.Hash{},
			blockNumber,
		)
		rcp.SetGroupIndex(uint64(i % 3))
		rcp.SetTransactionIndex(uint64(i))
		require.NoError(t, rcpDb.AddReceipt(rcp))
	}
	rcpRoot, err := rcpDb.Commit()
	require.NoError(t, err)

	blockHeader := block.NewBlockHeader(
		common.HexToHash("0x1111"),
		blockNumber,
		common.HexToHash("0x2222"),
		common.HexToHash("0x3333"),
		rcpRoot,
		fromAddr,
		1,
		common.HexToHash("0x4444"),
		0,
	)
	testBlock := makeTestBlock(blockHeader, txHashes)

	fetchTx := func(h common.Hash) (mt_types.Transaction, error) {
		return txMap[h], nil
	}

	// Call MarshalBlockToMapWithGas with fullTx = true
	resMap, err := MarshalBlockToMapWithGas(testBlock, true, fetchTx, memDb, nil)
	require.NoError(t, err)

	txsRaw, ok := resMap["transactions"].([]interface{})
	require.True(t, ok, "transactions field must be []interface{}")
	require.Len(t, txsRaw, len(txList))

	// Re-marshal to JSON and unmarshal to check exact JSON representation
	jsonBytes, err := json.Marshal(resMap)
	require.NoError(t, err)

	var parsedBlock struct {
		Hash         string                   `json:"hash"`
		Number       string                   `json:"number"`
		Transactions []map[string]interface{} `json:"transactions"`
	}
	require.NoError(t, json.Unmarshal(jsonBytes, &parsedBlock))
	require.Len(t, parsedBlock.Transactions, len(txList))

	expectedBlockHash := testBlock.Header().Hash().Hex()
	expectedBlockNumberHex := hexutil.EncodeUint64(blockNumber)

	for i, txObj := range parsedBlock.Transactions {
		t.Logf("Checking tx #%d type=%v hash=%v", i, txObj["type"], txObj["hash"])

		// 1. Ethereum standard fields MUST be present
		assert.Equal(t, expectedBlockHash, txObj["blockHash"], "tx %d blockHash must match parent block", i)
		assert.Equal(t, expectedBlockNumberHex, txObj["blockNumber"], "tx %d blockNumber must match parent block", i)
		assert.NotEmpty(t, txObj["hash"], "tx %d hash must not be empty", i)
		assert.NotEmpty(t, txObj["from"], "tx %d from must not be empty", i)
		assert.NotEmpty(t, txObj["gas"], "tx %d gas must not be empty", i)
		assert.NotEmpty(t, txObj["gasPrice"], "tx %d gasPrice must not be empty", i)
		assert.NotEmpty(t, txObj["nonce"], "tx %d nonce must not be empty", i)
		assert.NotEmpty(t, txObj["value"], "tx %d value must not be empty", i)
		assert.NotEmpty(t, txObj["v"], "tx %d v must not be empty", i)
		assert.NotEmpty(t, txObj["r"], "tx %d r must not be empty", i)
		assert.NotEmpty(t, txObj["s"], "tx %d s must not be empty", i)

		// 2. Transaction index and Group ID MUST match receipt
		assert.Equal(t, hexutil.EncodeUint64(uint64(i)), txObj["transactionIndex"], "tx %d transactionIndex", i)
		assert.Equal(t, hexutil.EncodeUint64(uint64(i%3)), txObj["groupId"], "tx %d groupId", i)

		// 3. Type-specific checks
		txTypeHex, ok := txObj["type"].(string)
		require.True(t, ok, "tx %d type must be string hex", i)

		switch i {
		case 0: // Legacy (Type 0)
			assert.Equal(t, "0x0", txTypeHex)
			assert.Nil(t, txObj["maxFeePerGas"], "Legacy tx should have null/nil maxFeePerGas")
			assert.Nil(t, txObj["maxPriorityFeePerGas"], "Legacy tx should have null/nil maxPriorityFeePerGas")
			assert.Nil(t, txObj["accessList"], "Legacy tx should have null accessList")
			assert.Nil(t, txObj["yParity"], "Legacy tx should have null yParity")

		case 1: // EIP-2930 (Type 1)
			assert.Equal(t, "0x1", txTypeHex)
			assert.NotNil(t, txObj["accessList"], "EIP-2930 must have accessList")
			assert.NotNil(t, txObj["yParity"], "EIP-2930 must have yParity")

		case 2: // EIP-1559 (Type 2, tip > 0)
			assert.Equal(t, "0x2", txTypeHex)
			assert.NotNil(t, txObj["maxFeePerGas"], "EIP-1559 must have maxFeePerGas")
			assert.NotNil(t, txObj["maxPriorityFeePerGas"], "EIP-1559 must have maxPriorityFeePerGas")
			assert.Equal(t, hexutil.EncodeBig(big.NewInt(2_000_000_000)), txObj["maxPriorityFeePerGas"])
			assert.NotNil(t, txObj["accessList"], "EIP-1559 must have accessList (even if empty)")
			assert.NotNil(t, txObj["yParity"], "EIP-1559 must have yParity")

		case 3: // EIP-1559 (Type 2, tip == 0)
			assert.Equal(t, "0x2", txTypeHex)
			assert.NotNil(t, txObj["maxFeePerGas"], "EIP-1559 must have maxFeePerGas")
			assert.NotNil(t, txObj["maxPriorityFeePerGas"], "EIP-1559 tip=0 MUST be preserved as 0x0")
			assert.Equal(t, "0x0", txObj["maxPriorityFeePerGas"])
			assert.NotNil(t, txObj["accessList"], "EIP-1559 must have accessList")
			assert.NotNil(t, txObj["yParity"], "EIP-1559 must have yParity")

		case 4: // EIP-7702 (Type 4)
			assert.Equal(t, "0x4", txTypeHex)
			assert.NotNil(t, txObj["authorizationList"], "EIP-7702 must have authorizationList")
			authListRaw, ok := txObj["authorizationList"].([]interface{})
			require.True(t, ok)
			require.Len(t, authListRaw, 1)

		case 5: // Contract Deployment (To == nil)
			expectedDeployed := crypto.CreateAddress(fromAddr, 5)
			assert.Equal(t, expectedDeployed, common.HexToAddress(txObj["to"].(string)), "contract deployment to address must match deployed contract address")
		}
	}
}

func TestMarshalBlockToMapWithGas_NegativeControl(t *testing.T) {
	// Negative control: verify that if a client consumes JSON and requires blockNumber/blockHash/type,
	// missing any of these fields causes strict client verification to fail.
	clientVerifyTx := func(txMap map[string]interface{}) error {
		required := []string{"blockHash", "blockNumber", "hash", "type", "from", "gas", "gasPrice", "nonce", "value"}
		for _, req := range required {
			val, exists := txMap[req]
			if !exists || val == nil {
				return assert.AnError
			}
		}
		return nil
	}

	// 1. Complete tx passes
	validTx := map[string]interface{}{
		"blockHash":   "0xabc",
		"blockNumber": "0x1",
		"hash":        "0x123",
		"type":        "0x2",
		"from":        "0xaaa",
		"gas":         "0x5208",
		"gasPrice":    "0x10",
		"nonce":       "0x0",
		"value":       "0x0",
	}
	require.NoError(t, clientVerifyTx(validTx))

	// 2. Tampered tx missing blockNumber fails
	invalidTxNoBlockNum := make(map[string]interface{})
	for k, v := range validTx {
		invalidTxNoBlockNum[k] = v
	}
	delete(invalidTxNoBlockNum, "blockNumber")
	require.Error(t, clientVerifyTx(invalidTxNoBlockNum), "negative control: missing blockNumber must fail client verification")

	// 3. Tampered tx missing type fails
	invalidTxNoType := make(map[string]interface{})
	for k, v := range validTx {
		invalidTxNoType[k] = v
	}
	delete(invalidTxNoType, "type")
	require.Error(t, clientVerifyTx(invalidTxNoType), "negative control: missing type must fail client verification")
}

func TestMarshalBlockToMapWithGas_EmptyBlock_NoReceiptStorage(t *testing.T) {
	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		100,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		common.HexToHash("0xrcproot"),
		common.HexToAddress("0xleader"),
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, nil)

	// Call with fullTx = true, but nil fetchTx and nil storageReceipt
	resMap, err := MarshalBlockToMapWithGas(blk, true, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, resMap)

	txs, ok := resMap["transactions"].([]interface{})
	require.True(t, ok)
	assert.Empty(t, txs, "empty block must return empty transactions slice")
}

func TestMarshalBlockToMapWithGas_Performance_100Txs(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(key.PublicKey)
	targetAddr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	chainID := big.NewInt(991)
	signerLondon := e_types.NewLondonSigner(chainID)

	n := 100
	txList := make([]mt_types.Transaction, n)
	txHashes := make([]common.Hash, n)
	txMap := make(map[common.Hash]mt_types.Transaction, n)

	for i := 0; i < n; i++ {
		ethTx, err := e_types.SignNewTx(key, signerLondon, &e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: big.NewInt(1_000_000_000),
			GasFeeCap: big.NewInt(20_000_000_000),
			Gas:       21000,
			To:        &targetAddr,
			Value:     big.NewInt(int64(i)),
		})
		require.NoError(t, err)
		mtTx, err := transaction.NewTransactionFromEth(ethTx)
		require.NoError(t, err)

		h := mtTx.Hash()
		txList[i] = mtTx
		txHashes[i] = h
		txMap[h] = mtTx
	}

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		200,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		common.HexToHash("0xrcproot"),
		fromAddr,
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, txHashes)

	fetchTx := func(h common.Hash) (mt_types.Transaction, error) {
		return txMap[h], nil
	}

	start := time.Now()
	resMap, err := MarshalBlockToMapWithGas(blk, true, fetchTx, nil, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	txs, ok := resMap["transactions"].([]interface{})
	require.True(t, ok)
	require.Len(t, txs, n)

	t.Logf("MarshalBlockToMapWithGas for %d full txs took %v (average %v/tx)", n, elapsed, elapsed/time.Duration(n))
	assert.Less(t, elapsed, 100*time.Millisecond, "marshaling 100 in-memory transactions must be fast (<100ms)")
}

func TestMarshalBlockToMapWithGas_Performance_5000Txs(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(key.PublicKey)
	targetAddr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	chainID := big.NewInt(991)
	signerLondon := e_types.NewLondonSigner(chainID)

	n := 5000
	txList := make([]mt_types.Transaction, n)
	txHashes := make([]common.Hash, n)
	txMap := make(map[common.Hash]mt_types.Transaction, n)

	for i := 0; i < n; i++ {
		ethTx, err := e_types.SignNewTx(key, signerLondon, &e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: big.NewInt(1_000_000_000),
			GasFeeCap: big.NewInt(20_000_000_000),
			Gas:       21000,
			To:        &targetAddr,
			Value:     big.NewInt(int64(i)),
		})
		require.NoError(t, err)
		mtTx, err := transaction.NewTransactionFromEth(ethTx)
		require.NoError(t, err)

		h := mtTx.Hash()
		txList[i] = mtTx
		txHashes[i] = h
		txMap[h] = mtTx
	}

	header := block.NewBlockHeader(
		common.HexToHash("0xprev"),
		300,
		common.HexToHash("0xstate"),
		common.HexToHash("0xstake"),
		common.HexToHash("0xrcproot"),
		fromAddr,
		1,
		common.HexToHash("0xtxroot"),
		0,
	)
	blk := makeTestBlock(header, txHashes)

	fetchTx := func(h common.Hash) (mt_types.Transaction, error) {
		return txMap[h], nil
	}

	start := time.Now()
	resMap, err := MarshalBlockToMapWithGas(blk, true, fetchTx, nil, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	txs, ok := resMap["transactions"].([]interface{})
	require.True(t, ok)
	require.Len(t, txs, n)

	t.Logf("MarshalBlockToMapWithGas for %d full txs took %v (average %v/tx)", n, elapsed, elapsed/time.Duration(n))
	assert.Less(t, elapsed, 500*time.Millisecond, "marshaling 5000 in-memory transactions must be fast (<500ms)")
}

