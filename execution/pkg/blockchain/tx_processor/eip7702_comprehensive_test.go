package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func genTestKey(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	return k, crypto.PubkeyToAddress(k.PublicKey)
}

func buildSetCodeTx(
	t *testing.T,
	senderKey *ecdsa.PrivateKey,
	chainID uint64,
	nonce uint64,
	gas uint64,
	to common.Address,
	value uint64,
	authList []types.SetCodeAuthorization,
) mt_types.Transaction {
	t.Helper()
	inner := &types.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       gas,
		To:        to,
		Value:     uint256.NewInt(value),
		AuthList:  authList,
	}
	ethTx, err := types.SignNewTx(senderKey, types.NewPragueSigner(big.NewInt(int64(chainID))), inner)
	require.NoError(t, err)
	eip7702Tx, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)
	return eip7702Tx
}

// ============================================================================
// F1 (High): Sender self-authorization nonce standard alignment
// EIP-7702 increments sender nonce BEFORE processing authorization tuples.
// Therefore, for self-authorization:
// - auth.Nonce must equal tx.Nonce + 1.
// - Upon success, final sender nonce is tx.Nonce + 2 (tx nonce bump + auth nonce bump).
// - If auth.Nonce == tx.Nonce (the old buggy pattern), it is silently skipped.
// ============================================================================

func TestEIP7702_F1_SelfAuthorization_StandardNoncePlusOne_Success(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")
	expectedCodeHash := crypto.Keccak256Hash(types.AddressToDelegation(delegateAddr))

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)

	// auth.Nonce = tx.Nonce + 1 = 0 + 1 = 1 (standard EIP-7702)
	auth := signAuthorization(t, senderKey, chainID, delegateAddr, 1)
	tx := buildSetCodeTx(t, senderKey, chainID, 0, 100_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth})

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	st, err := chainState.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	require.NotNil(t, st)

	// Final nonce must be 2: tx execution (+1) and self-authorization (+1)
	assert.Equal(t, uint64(2), st.Nonce(), "Sender self-authorization must result in final nonce = tx.Nonce + 2")

	// Delegation code must be set
	sc := st.SmartContractState()
	require.NotNil(t, sc, "Sender must have SmartContractState set after self-authorization")
	assert.Equal(t, expectedCodeHash, sc.CodeHash(), "Delegation code hash must match delegate designator")
}

func TestEIP7702_F1_SelfAuthorization_OldNonceMatchesTxNonce_Skipped(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)

	// auth.Nonce = tx.Nonce = 0 (wrong per EIP-7702 standard)
	auth := signAuthorization(t, senderKey, chainID, delegateAddr, 0)
	tx := buildSetCodeTx(t, senderKey, chainID, 0, 100_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth})

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	st, err := chainState.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	require.NotNil(t, st)

	// Authorization was skipped, so final nonce is only incremented once by tx = 1
	assert.Equal(t, uint64(1), st.Nonce(), "Skipped authorization must not increment authority nonce")

	// No delegation code set
	sc := st.SmartContractState()
	assert.True(t, sc == nil || sc.CodeHash() == (common.Hash{}), "Skipped authorization must not set codeHash")
}

// ============================================================================
// F2 (High): Fee never exceeds signed MaxGas (Fraud Proof Invariant I1)
// If MaxGas < intrinsicGas, transaction must fail and consume at most MaxGas.
// Under no circumstances should gasUsed exceed MaxGas.
// ============================================================================

func TestEIP7702_F2_MaxGasExceeded_RejectsWithoutOverbilling(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	authKey, authAddr := genTestKey(t)
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	initialBalance := big.NewInt(1_000_000_000_000)
	seedAccount(t, chainState, senderAddr, initialBalance, 0)
	seedAccount(t, chainState, authAddr, big.NewInt(0), 0)

	auth := signAuthorization(t, authKey, chainID, delegateAddr, 0)

	// 1 authorization tuple requires intrinsic gas = TRANSFER_GAS_COST (20,000) + 25,000 = 45,000.
	// But we set Gas limit = 21,000.
	const signedGasLimit = 21_000
	tx := buildSetCodeTx(t, senderKey, chainID, 0, signedGasLimit, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth})

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

	require.Len(t, rcps, 1)
	// Must fail receipt status due to intrinsic gas too low
	assert.NotEqual(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status(), "Tx with MaxGas < intrinsicGas must fail")
	// GasUsed must be capped at signedGasLimit
	assert.Equal(t, uint64(signedGasLimit), rcps[0].GasUsed(), "Gas used must never exceed signed MaxGas")

	st, err := chainState.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	deductedFee := new(big.Int).Sub(initialBalance, st.TotalBalance())
	// EffectiveGasPrice is 1
	assert.Equal(t, int64(signedGasLimit), deductedFee.Int64(), "Fraud proof I1: Deducted fee must not exceed signed MaxGas * GasPrice")
}

// ============================================================================
// F3 (High): Invalid tuples MUST cost 25,000 intrinsic gas each (anti-DDoS)
// Senders cannot spam invalid tuples for free CPU signature verification.
// ============================================================================

func TestEIP7702_F3_InvalidTuples_ChargedIntrinsicGas(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	authKey, authAddr := genTestKey(t)
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)
	seedAccount(t, chainState, authAddr, big.NewInt(0), 0) // authority nonce is 0

	// Create 3 invalid tuples with wrong nonces (99, 100, 101)
	authList := []types.SetCodeAuthorization{
		signAuthorization(t, authKey, chainID, delegateAddr, 99),
		signAuthorization(t, authKey, chainID, delegateAddr, 100),
		signAuthorization(t, authKey, chainID, delegateAddr, 101),
	}

	// Intrinsic gas = 20,000 + 3 * 25,000 = 95,000
	const signedGasLimit = 150_000
	toAddr := common.HexToAddress("0x9999")
	tx := buildSetCodeTx(t, senderKey, chainID, 0, signedGasLimit, toAddr, 0, authList)

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	// Surcharge: 20,000 + 3 * 25,000 + (25,000 for toAddr if new account)
	minExpectedGas := uint64(mt_common.TRANSFER_GAS_COST + 3*25000)
	assert.GreaterOrEqual(t, rcps[0].GasUsed(), minExpectedGas, "Every tuple (valid or invalid) must consume 25,000 gas")

	// Authority nonce and code must be untouched because all 3 tuples were invalid
	authSt, err := chainState.GetAccountStateDB().AccountState(authAddr)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), authSt.Nonce(), "Invalid tuples must not advance authority nonce")
	assert.Nil(t, authSt.SmartContractState(), "Invalid tuples must not set authority code")
}

// ============================================================================
// F5 (Low): 12,500 gas refund for existing authority accounts
// When an authority already exists, 12,500 gas is refunded.
// ============================================================================

func TestEIP7702_F5_GasRefund_ExistingAuthority(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	authKey, authAddr := genTestKey(t)
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)
	// Seed authority account with existing balance and non-zero nonce (5)
	seedAccount(t, chainState, authAddr, big.NewInt(500), 5)
	toAddr := authAddr // Transfer to existing account (no newAccountGas)

	auth := signAuthorization(t, authKey, chainID, delegateAddr, 5)
	tx := buildSetCodeTx(t, senderKey, chainID, 0, 100_000, toAddr, 0, []types.SetCodeAuthorization{auth})

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	// Intrinsic gas = 20,000 + 25,000 = 45,000.
	// The authority already existed (nonce=5, balance=500), so 12,500 is refunded, but every EVM refund is
	// capped at gasUsed/5 (EIP-3529; go-ethereum calcRefund): min(12,500, 45,000/5 = 9,000) = 9,000.
	// Net gasUsed = 45,000 - 9,000 = 36,000.
	intrinsic := uint64(mt_common.TRANSFER_GAS_COST + 25000)
	expectedGasUsed := intrinsic - intrinsic/5
	assert.Equal(t, expectedGasUsed, rcps[0].GasUsed(), "Existing authority refund must be capped at gasUsed/5")
}

// ============================================================================
// F7 (Medium): ChainID validation matrix (Wildcard 0 vs Matching vs Mismatch)
// ============================================================================

func TestEIP7702_F7_ChainIDMatrix(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	authKey1, authAddr1 := genTestKey(t)
	authKey2, authAddr2 := genTestKey(t)
	authKey3, authAddr3 := genTestKey(t)
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")
	expectedCodeHash := crypto.Keccak256Hash(types.AddressToDelegation(delegateAddr))

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)
	seedAccount(t, chainState, authAddr1, big.NewInt(0), 0)
	seedAccount(t, chainState, authAddr2, big.NewInt(0), 0)
	seedAccount(t, chainState, authAddr3, big.NewInt(0), 0)

	// Auth 1: Wildcard chainID = 0 (valid)
	auth1 := signAuthorization(t, authKey1, 0, delegateAddr, 0)
	// Auth 2: Exact matching chainID = 991 (valid)
	auth2 := signAuthorization(t, authKey2, chainID, delegateAddr, 0)
	// Auth 3: Mismatched chainID = 101 (invalid on chain 991)
	auth3 := signAuthorization(t, authKey3, 101, delegateAddr, 0)

	tx := buildSetCodeTx(t, senderKey, chainID, 0, 150_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth1, auth2, auth3})

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	// Auth 1 (Wildcard 0) -> Applied
	st1, err := chainState.GetAccountStateDB().AccountState(authAddr1)
	require.NoError(t, err)
	require.NotNil(t, st1.SmartContractState())
	assert.Equal(t, expectedCodeHash, st1.SmartContractState().CodeHash(), "Wildcard ChainID=0 must be accepted")
	assert.Equal(t, uint64(1), st1.Nonce())

	// Auth 2 (ChainID=991) -> Applied
	st2, err := chainState.GetAccountStateDB().AccountState(authAddr2)
	require.NoError(t, err)
	require.NotNil(t, st2.SmartContractState())
	assert.Equal(t, expectedCodeHash, st2.SmartContractState().CodeHash(), "Matching ChainID=991 must be accepted")
	assert.Equal(t, uint64(1), st2.Nonce())

	// Auth 3 (ChainID=101) -> Skipped
	st3, err := chainState.GetAccountStateDB().AccountState(authAddr3)
	require.NoError(t, err)
	assert.Nil(t, st3.SmartContractState(), "Mismatched ChainID=101 must be skipped")
	assert.Equal(t, uint64(0), st3.Nonce())
}

// ============================================================================
// Revocation & Re-delegation
// EIP-7702 allows re-pointing to a new delegate, and revoking by delegating to 0x0.
// ============================================================================

func TestEIP7702_RevocationAndRedelegation(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	authKey, authAddr := genTestKey(t)
	delegateA := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	delegateB := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	zeroAddr := common.Address{}

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)
	seedAccount(t, chainState, authAddr, big.NewInt(0), 0)

	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	// Step 1: Delegate to A
	auth1 := signAuthorization(t, authKey, chainID, delegateA, 0)
	tx1 := buildSetCodeTx(t, senderKey, chainID, 0, 100_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth1})
	stm1 := NewTrueBlockSTM([]mt_types.Transaction{tx1})
	_, rcps1, _, _ := stm1.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps1[0].Status())

	st, _ := chainState.GetAccountStateDB().AccountState(authAddr)
	assert.Equal(t, crypto.Keccak256Hash(types.AddressToDelegation(delegateA)), st.SmartContractState().CodeHash())
	assert.Equal(t, uint64(1), st.Nonce())

	// Step 2: Re-delegate to B (allowed: overwriting existing delegation)
	auth2 := signAuthorization(t, authKey, chainID, delegateB, 1)
	tx2 := buildSetCodeTx(t, senderKey, chainID, 1, 100_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth2})
	stm2 := NewTrueBlockSTM([]mt_types.Transaction{tx2})
	_, rcps2, _, _ := stm2.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12346)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps2[0].Status())

	st, _ = chainState.GetAccountStateDB().AccountState(authAddr)
	assert.Equal(t, crypto.Keccak256Hash(types.AddressToDelegation(delegateB)), st.SmartContractState().CodeHash())
	assert.Equal(t, uint64(2), st.Nonce())

	// Step 3: Revoke delegation (delegate to 0x0)
	auth3 := signAuthorization(t, authKey, chainID, zeroAddr, 2)
	tx3 := buildSetCodeTx(t, senderKey, chainID, 2, 100_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth3})
	stm3 := NewTrueBlockSTM([]mt_types.Transaction{tx3})
	_, rcps3, _, _ := stm3.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12347)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps3[0].Status())

	st, _ = chainState.GetAccountStateDB().AccountState(authAddr)
	assert.Equal(t, common.Hash{}, st.SmartContractState().CodeHash(), "Delegating to 0x0 must clear codeHash back to empty (revocation)")
	assert.Equal(t, uint64(3), st.Nonce())
}

func buildSetCodeTxWithData(
	t *testing.T,
	senderKey *ecdsa.PrivateKey,
	chainID uint64,
	nonce uint64,
	gas uint64,
	to common.Address,
	value uint64,
	data []byte,
	authList []types.SetCodeAuthorization,
) mt_types.Transaction {
	t.Helper()
	inner := &types.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       gas,
		To:        to,
		Value:     uint256.NewInt(value),
		Data:      data,
		AuthList:  authList,
	}
	ethTx, err := types.SignNewTx(senderKey, types.NewPragueSigner(big.NewInt(int64(chainID))), inner)
	require.NoError(t, err)
	eip7702Tx, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)
	return eip7702Tx
}

func newStandardCallTx(from, to common.Address, nonce uint64, gas uint64, data []byte) mt_types.Transaction {
	cd := transaction.NewCallData(data)
	bData, _ := cd.Marshal()
	tx := transaction.NewTransaction(
		from, to, big.NewInt(0),
		gas, 1, 1000,
		bData,
		nil, common.Hash{}, common.Hash{},
		nonce, 991,
	)
	return tx
}

// ============================================================================
// F4 (Medium): Contract execution path must not double-charge authorization gas.
// core.IntrinsicGas folds in 25,000 * len(authList); true_block_stm must NOT
// add another 25,000 * len(authList).
// ============================================================================

func TestEIP7702_F4_ContractExecution_NoDoubleCharge(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	authKey, authAddr := genTestKey(t)
	targetAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	delegateAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)
	seedAccount(t, chainState, authAddr, big.NewInt(0), 0)
	seedAccount(t, chainState, targetAddr, big.NewInt(0), 0)

	callData := []byte{0x01, 0x02, 0x03}
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	// Tx 0: Standard contract call WITHOUT authorization list
	tx0 := newStandardCallTx(senderAddr, targetAddr, 0, 100_000, callData)
	stm0 := NewTrueBlockSTM([]mt_types.Transaction{tx0})
	_, rcps0, _, _ := stm0.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps0, 1)
	gasUsed0 := rcps0[0].GasUsed()

	// Tx 1: Contract call WITH 1 authorization tuple (SetCodeTx)
	auth := signAuthorization(t, authKey, chainID, delegateAddr, 0)
	tx1 := buildSetCodeTxWithData(t, senderKey, chainID, 1, 100_000, targetAddr, 0, callData, []types.SetCodeAuthorization{auth})
	stm1 := NewTrueBlockSTM([]mt_types.Transaction{tx1})
	_, rcps1, _, _ := stm1.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12346)
	require.Len(t, rcps1, 1)
	gasUsed1 := rcps1[0].GasUsed()

	// The difference must be exactly 25,000 (params.CallNewAccountGas).
	// If double charging occurred, the difference would be 50,000.
	delta := gasUsed1 - gasUsed0
	assert.Equal(t, uint64(25000), delta, "Contract call with 1 auth tuple must cost exactly 25,000 more gas, not 50,000")
}

// ============================================================================
// F8 (Medium): MVM account access cost for resolving EIP-7702 delegates
// Verifies that when an account delegates to another account, MVM resolves
// the delegation and tracks touched address gas.
// ============================================================================

func TestEIP7702_F8_DelegationResolutionGas(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegatedKey, delegatedAddr := genTestKey(t)
	_, delegateAddr := genTestKey(t)

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)
	seedAccount(t, chainState, delegatedAddr, big.NewInt(0), 0)
	seedAccount(t, chainState, delegateAddr, big.NewInt(0), 0)

	// Set delegate's code to return 0x42
	// Bytecode: PUSH1 0x42, PUSH1 0x00, MSTORE, PUSH1 0x20, PUSH1 0x00, RETURN
	runtimeCode := []byte{0x60, 0x42, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	codeHash := crypto.Keccak256Hash(runtimeCode)
	chainState.GetSmartContractDB().SetCode(delegateAddr, codeHash, runtimeCode)
	chainState.GetAccountStateDB().SetCodeHash(delegateAddr, codeHash)

	// Step 1: Delegate delegatedAddr -> delegateAddr via EIP-7702
	auth := signAuthorization(t, delegatedKey, chainID, delegateAddr, 0)
	authTx := buildSetCodeTx(t, senderKey, chainID, 0, 100_000, common.HexToAddress("0x9999"), 0, []types.SetCodeAuthorization{auth})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	stm := NewTrueBlockSTM([]mt_types.Transaction{authTx})
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	// Step 2: Call delegatedAddr directly with a standard contract call.
	// Since delegatedAddr's code is delegation designator (0xef0100 || delegateAddr),
	// MVM resolves it to delegateAddr's runtime code, charges 100 gas for resolving delegate,
	// and returns 0x42.
	callTx := newStandardCallTx(senderAddr, delegatedAddr, 1, 100_000, []byte{0x00})
	stm2 := NewTrueBlockSTM([]mt_types.Transaction{callTx})
	_, rcps2, _, _ := stm2.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12346)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps2[0].Status())

	// Step 3: call the delegate directly with the identical calldata. Same code, so the only difference in
	// gas is the account-access charge for resolving the delegation designator: exactly 100 (warm access).
	directTx := newStandardCallTx(senderAddr, delegateAddr, 2, 100_000, []byte{0x00})
	stm3 := NewTrueBlockSTM([]mt_types.Transaction{directTx})
	_, rcps3, _, _ := stm3.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12347)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps3[0].Status())
	assert.Equal(t, rcps3[0].GasUsed()+100, rcps2[0].GasUsed(),
		"calling a delegated account must cost exactly 100 more gas than calling its delegate directly")
}

// Contract path (EVM), self-authorization: the sender authorises itself while calling a contract.
// Per EIP-7702 the sender nonce is bumped before the authorization list is processed, so auth.Nonce must be
// tx.Nonce+1 and the final nonce tx.Nonce+2. This exercises the MapNonce override in the VM branch, which the
// native-transfer F1 tests do not reach.
func TestEIP7702_F1_SelfAuthorization_ContractPath(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	targetAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	delegateAddr := common.HexToAddress("0x1111222233334444555566667777888899990000")
	expectedCodeHash := crypto.Keccak256Hash(types.AddressToDelegation(delegateAddr))

	chainState := newTestChainState(t)
	chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(chainID)})
	seedAccount(t, chainState, senderAddr, big.NewInt(1_000_000_000_000), 0)

	// Target runs real code (PUSH1 0x42 PUSH1 0 MSTORE PUSH1 0x20 PUSH1 0 RETURN) so the VM branch is taken.
	runtimeCode := []byte{0x60, 0x42, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	codeHash := crypto.Keccak256Hash(runtimeCode)
	chainState.GetSmartContractDB().SetCode(targetAddr, codeHash, runtimeCode)
	seedAccount(t, chainState, targetAddr, big.NewInt(0), 0)
	chainState.GetAccountStateDB().SetCodeHash(targetAddr, codeHash)

	auth := signAuthorization(t, senderKey, chainID, delegateAddr, 1) // tx.Nonce (0) + 1
	tx := buildSetCodeTxWithData(t, senderKey, chainID, 0, 200_000, targetAddr, 0, []byte{0x00}, []types.SetCodeAuthorization{auth})

	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	_, rcps, _, _ := stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	st, err := chainState.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), st.Nonce(), "contract path: self-authorization must end at tx.Nonce+2 (no double increment by the VM)")
	sc := st.SmartContractState()
	require.NotNil(t, sc)
	assert.Equal(t, expectedCodeHash, sc.CodeHash(), "contract path: sender must be delegated")
}
