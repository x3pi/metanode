package tx_processor

import (
	"bytes"
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func signTestTx(tx types.Transaction) []byte {
	keyPair := bls.GenerateKeyPair()
	pub := keyPair.PublicKey()
	priv := keyPair.PrivateKey()

	tx.SetSign(priv)
	return pub.Bytes()
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// UNIT SECURITY TEST SUITE: Core Ingress & Verification Hardening
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

// TestSecurity_Unit_StaleNonceReplay ensures transactions with nonce < on-chain nonce are rejected
func TestSecurity_Unit_StaleNonceReplay(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	// Transaction with nonce = 1
	tx, pubKey := createTestTx(from, to, amount, p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)

	// Account state has higher nonce = 5
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(5)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject stale nonce")
	assert.Equal(t, transaction.InvalidNonce.Code, err.Code, "Expected InvalidNonce error code")
}

// TestSecurity_Unit_CrossChainReplay ensures transactions with mismatched ChainID are rejected
func TestSecurity_Unit_CrossChainReplay(t *testing.T) {
	cs := setupTestChainState(t) // Config has ChainId = 1
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	// Build transaction with foreign ChainId = 999
	tx := transaction.NewTransaction(
		from, to, amount, p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		[]byte{}, nil, common.Hash{}, common.Hash{}, 1, 999, // Foreign ChainId
	)
	pubKey := signTestTx(tx)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject mismatched ChainID")
	assert.Equal(t, transaction.InvalidChainId.Code, err.Code, "Expected InvalidChainId error code")
}

// TestSecurity_Unit_InsufficientBalance_Overdraft ensures transactions transferring more than balance are rejected
func TestSecurity_Unit_InsufficientBalance_Overdraft(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")

	// Attempt to transfer 1,000,000,000
	amount := big.NewInt(1000000000)
	tx, pubKey := createTestTx(from, to, amount, p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)

	// Account balance is only 500 (less than amount + fee)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(500))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject overdraft")
	assert.True(t, err.Code == transaction.InvalidAmount.Code || err.Code == transaction.InvalidMaxFee.Code,
		"Expected InvalidAmount or InvalidMaxFee, got %v", err)
}

// TestSecurity_Unit_UnderpricedSpam ensures gasPrice below MINIMUM_BASE_FEE is rejected
func TestSecurity_Unit_UnderpricedSpam(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	// Gas price = 1 (way below MINIMUM_BASE_FEE)
	tx, pubKey := createTestTx(from, to, amount, p_common.TRANSFER_GAS_COST, 1, 1)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject underpriced transaction")
	assert.Equal(t, transaction.InvalidMaxGasPrice.Code, err.Code)
}

// TestSecurity_Unit_OversizedCallData ensures transaction with data > 6MB is rejected
func TestSecurity_Unit_OversizedCallData(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	hugeData := bytes.Repeat([]byte{0xAB}, 6*1024*1024+10)
	tx := transaction.NewTransaction(
		from, to, amount, p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		hugeData, nil, common.Hash{}, common.Hash{}, 1, 1,
	)
	pubKey := signTestTx(tx)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject oversized data")
	assert.True(t, err.Code == transaction.InvalidData.Code || err.Code == transaction.InvalidCallData.Code,
		"Expected InvalidData (30) or InvalidCallData (41), got %v", err)
}

// TestSecurity_Unit_AccountSetting_UnauthorizedSelector ensures invalid selectors to AccountSetting are blocked
func TestSecurity_Unit_AccountSetting_UnauthorizedSelector(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	accountSettingAddr := utils.GetAddressSelector(p_common.ACCOUNT_SETTING_ADDRESS_SELECT)

	// Sending fake selector to account setting contract
	fakeData := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	tx := transaction.NewTransaction(
		from, accountSettingAddr, big.NewInt(0), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		fakeData, nil, common.Hash{}, common.Hash{}, 1, 1,
	)
	pubKey := signTestTx(tx)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject unauthorized selector")
	assert.Equal(t, transaction.InvalidData.Code, err.Code)
}

// TestSecurity_Unit_FastPath_ZeroGasTxRejectedEvenWithCachedSignature verifies that
// a transaction with MaxGas=0 is rejected in the native fast path,
// even if its signature is already present in the verified signature cache.
func TestSecurity_Unit_FastPath_ZeroGasTxRejectedEvenWithCachedSignature(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	// Create transaction with MaxGas = 0
	tx := transaction.NewTransaction(
		from, to, amount, 0, p_common.MINIMUM_BASE_FEE, 1000,
		nil, nil, common.Hash{}, common.Hash{}, 1, 1,
	)
	pubKey := signTestTx(tx)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	// Simulate signature filter pre-caching the valid BLS signature
	cacheKey := sigCacheKey(tx, pubKey)
	StoreVerifiedSignature(cacheKey)
	require.True(t, LoadVerifiedSignature(cacheKey), "signature should be cached")

	// Group and execute via ProcessTransactionsOptimistic (fast path) with skipSignatureVerify=false
	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)

	leaderAddr := common.HexToAddress("0x999")
	outTxs, outRcps, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, groups, blankHeader(),
		false, false, 12345, leaderAddr, false,
	)

	// Must be rejected: no txs processed, no receipts returned
	assert.Empty(t, outTxs, "Fast path must reject tx with MaxGas=0 even if signature is cached")
	assert.Empty(t, outRcps, "Fast path must return no receipts for rejected tx")
}

// TestSecurity_Unit_FastPath_ZeroGasPriceTxRejectedEvenWithCachedSignature verifies that
// a transaction with MaxGasPrice=0 is rejected in the native fast path,
// even if its signature is already present in the verified signature cache.
func TestSecurity_Unit_FastPath_ZeroGasPriceTxRejectedEvenWithCachedSignature(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	// Create transaction with MaxGasPrice = 0 (below MINIMUM_BASE_FEE)
	tx := transaction.NewTransaction(
		from, to, amount, p_common.TRANSFER_GAS_COST, 0, 1000,
		nil, nil, common.Hash{}, common.Hash{}, 1, 1,
	)
	pubKey := signTestTx(tx)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	// Simulate signature filter pre-caching the valid BLS signature
	cacheKey := sigCacheKey(tx, pubKey)
	StoreVerifiedSignature(cacheKey)
	require.True(t, LoadVerifiedSignature(cacheKey), "signature should be cached")

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)

	leaderAddr := common.HexToAddress("0x999")
	outTxs, outRcps, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, groups, blankHeader(),
		false, false, 12345, leaderAddr, false,
	)

	// Must be rejected: no txs processed, no receipts returned
	assert.Empty(t, outTxs, "Fast path must reject tx with MaxGasPrice=0 even if signature is cached")
	assert.Empty(t, outRcps, "Fast path must return no receipts for rejected tx")
}

// TestSecurity_Unit_SkipSignatureVerify_RejectsInvalidGasAndFee verifies that
// VerifyTransactionWithOptions with skipSignatureVerify=true STILL rejects
// invalid gas, zero gas price, overdrafts, and stale nonces.
func TestSecurity_Unit_SkipSignatureVerify_RejectsInvalidGasAndFee(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetNonce(5)

	// 1. MaxGas = 0 must be rejected
	txZeroGas := transaction.NewTransaction(
		from, to, big.NewInt(100), 0, p_common.MINIMUM_BASE_FEE, 0,
		nil, nil, common.Hash{}, common.Hash{}, 5, 1,
	)
	err := VerifyTransactionWithOptions(txZeroGas, cs, as, true)
	require.NotNil(t, err, "skipSignatureVerify=true must reject MaxGas=0")
	assert.Equal(t, transaction.InvalidMaxGas.Code, err.Code)

	// 2. GasPrice = 0 must be rejected
	txZeroFee := transaction.NewTransaction(
		from, to, big.NewInt(100), p_common.TRANSFER_GAS_COST, 0, 1000,
		nil, nil, common.Hash{}, common.Hash{}, 5, 1,
	)
	err = VerifyTransactionWithOptions(txZeroFee, cs, as, true)
	require.NotNil(t, err, "skipSignatureVerify=true must reject GasPrice=0")
	assert.Equal(t, transaction.InvalidMaxGasPrice.Code, err.Code)

	// 3. Stale nonce must be rejected
	txStaleNonce := transaction.NewTransaction(
		from, to, big.NewInt(100), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		nil, nil, common.Hash{}, common.Hash{}, 1, 1, // nonce 1 < state nonce 5
	)
	err = VerifyTransactionWithOptions(txStaleNonce, cs, as, true)
	require.NotNil(t, err, "skipSignatureVerify=true must reject stale nonce")
	assert.Equal(t, transaction.InvalidNonce.Code, err.Code)

	// 4. Overdraft must be rejected
	txOverdraft := transaction.NewTransaction(
		from, to, big.NewInt(2000000000000000), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		nil, nil, common.Hash{}, common.Hash{}, 5, 1,
	)
	err = VerifyTransactionWithOptions(txOverdraft, cs, as, true)
	require.NotNil(t, err, "skipSignatureVerify=true must reject overdraft")
	assert.True(t, err.Code == transaction.InvalidAmount.Code || err.Code == transaction.InvalidMaxFee.Code)
}

// TestSecurity_Unit_FastPath_RejectsInvalidTxWhenSkipSignatureVerifyIsTrue verifies that
// the production speculative executor path (skipSignatureVerify=true) does NOT execute
// transactions with zero gas or zero fee.
func TestSecurity_Unit_FastPath_RejectsInvalidTxWhenSkipSignatureVerifyIsTrue(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x111")
	to := common.HexToAddress("0x222")
	amount := big.NewInt(100)

	// Zero gas price transaction
	tx := transaction.NewTransaction(
		from, to, amount, p_common.TRANSFER_GAS_COST, 0, 1000,
		nil, nil, common.Hash{}, common.Hash{}, 1, 1,
	)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)

	leaderAddr := common.HexToAddress("0x999")
	// Run with skipSignatureVerify = true (the speculative executor mode)
	outTxs, outRcps, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, groups, blankHeader(),
		false, false, 12345, leaderAddr, true,
	)

	// Must be rejected: no txs processed, no receipts returned
	assert.Empty(t, outTxs, "Fast path with skipSignatureVerify=true must reject tx with MaxGasPrice=0")
	assert.Empty(t, outRcps, "Fast path with skipSignatureVerify=true must return no receipts for rejected tx")

	// Balances must remain unchanged
	asAfter, errState := cs.GetAccountStateDB().AccountState(from)
	require.NoError(t, errState)
	assert.Equal(t, big.NewInt(1000000000000000), asAfter.Balance(), "Balance must not be modified by rejected tx")
}

