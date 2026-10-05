package tx_processor

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
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

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
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

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	require.NotNil(t, err, "Expected VerifyTransaction to reject unauthorized selector")
	assert.Equal(t, transaction.InvalidData.Code, err.Code)
}
