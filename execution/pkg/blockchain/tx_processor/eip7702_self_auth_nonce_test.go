package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReviewMultipleSelfAuthorizationNonce_TwoValidAuthorizations recreates the exact scenario:
// A transaction with nonce 1, carrying two valid self-authorizations with nonces 2 and 3.
// Per EIP-7702:
// - Tx execution bumps sender nonce by 1 (1 -> 2).
// - First self-auth matches nonce 2 and bumps sender nonce by 1 (2 -> 3).
// - Second self-auth matches nonce 3 and bumps sender nonce by 1 (3 -> 4).
// Sender final nonce must be 4, matching go-ethereum reference EVM.
func TestReviewMultipleSelfAuthorizationNonce_TwoValidAuthorizations(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegateA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	delegateB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	expectedCodeHashB := crypto.Keccak256Hash(eth.AddressToDelegation(delegateB))

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(chainID),
		TxSignatureMode: config.TxSignatureModeSecp,
	})

	initialBalance := big.NewInt(1_000_000_000_000)
	seedAccount(t, cs, senderAddr, initialBalance, 1)

	// Auth 1: sender authorizes delegateA with nonce = tx.Nonce + 1 = 2
	auth1 := signAuthorization(t, senderKey, chainID, delegateA, 2)
	// Auth 2: sender authorizes delegateB with nonce = 3
	auth2 := signAuthorization(t, senderKey, chainID, delegateB, 3)

	inner := &eth.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       150_000,
		To:        common.HexToAddress("0x9999"),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2},
	}
	signer := eth.NewPragueSigner(big.NewInt(chainID))
	signed, err := eth.SignNewTx(senderKey, signer, inner)
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	// 1. Execute on MetaNode TrueBlockSTM
	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	senderState, err := cs.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	require.NotNil(t, senderState)

	// 2. Execute on go-ethereum Reference EVM
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(senderAddr, uint256.MustFromBig(initialBalance), tracing.BalanceChangeUnspecified)
	reference.SetNonce(senderAddr, 1, tracing.NonceChangeUnspecified)

	chainConfig := *params.AllDevChainProtocolChanges
	chainConfig.ChainID = big.NewInt(chainID)
	random := common.Hash{}
	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		GasLimit:    1000000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
		Random:      &random,
	}, reference, &chainConfig, vm.Config{})

	msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
	require.NoError(t, err)
	gResult, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.NoError(t, gResult.Err)

	// Invariant Checks:
	gethNonce := reference.GetNonce(senderAddr)
	mvmNonce := senderState.Nonce()
	t.Logf("Comparison: geth_sender_nonce=%d MVM_sender_nonce=%d", gethNonce, mvmNonce)

	assert.Equal(t, uint64(4), gethNonce, "go-ethereum sender nonce must be 4")
	assert.Equal(t, uint64(4), mvmNonce, "MetaNode sender nonce must be 4 (tx bump + 2 self-auth bumps)")
	assert.Equal(t, gethNonce, mvmNonce, "MetaNode sender nonce must match go-ethereum exactly")

	// Delegation code hash must match delegateB (last applied authorization)
	require.NotNil(t, senderState.SmartContractState())
	assert.Equal(t, expectedCodeHashB, senderState.SmartContractState().CodeHash(), "Sender delegation code hash must match delegateB")
	assert.Equal(t, reference.GetCodeHash(senderAddr), senderState.SmartContractState().CodeHash(), "CodeHash must match go-ethereum")
}

// TestReviewMultipleSelfAuthorizationNonce_ThreeValidAuthorizations tests three consecutive
// valid self-authorizations in the same transaction.
func TestReviewMultipleSelfAuthorizationNonce_ThreeValidAuthorizations(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegateA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	delegateB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	delegateC := common.HexToAddress("0x3333333333333333333333333333333333333333")
	expectedCodeHashC := crypto.Keccak256Hash(eth.AddressToDelegation(delegateC))

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(chainID),
		TxSignatureMode: config.TxSignatureModeSecp,
	})

	initialNonce := uint64(10)
	seedAccount(t, cs, senderAddr, big.NewInt(1_000_000_000_000), initialNonce)

	// Auth list nonces: 11, 12, 13
	auth1 := signAuthorization(t, senderKey, chainID, delegateA, 11)
	auth2 := signAuthorization(t, senderKey, chainID, delegateB, 12)
	auth3 := signAuthorization(t, senderKey, chainID, delegateC, 13)

	inner := &eth.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     initialNonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       200_000,
		To:        common.HexToAddress("0x9999"),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2, auth3},
	}
	signer := eth.NewPragueSigner(big.NewInt(chainID))
	signed, err := eth.SignNewTx(senderKey, signer, inner)
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	senderState, err := cs.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	// Initial 10 + 1 (tx) + 3 (three self-auths) = 14
	assert.Equal(t, uint64(14), senderState.Nonce(), "Sender nonce must be 14 after 3 self-authorizations")
	require.NotNil(t, senderState.SmartContractState())
	assert.Equal(t, expectedCodeHashC, senderState.SmartContractState().CodeHash(), "Delegation code hash must match delegateC")
}

// TestReviewMultipleSelfAuthorizationNonce_WithInvalidNonceSkipped tests that an invalid
// self-authorization nonce is skipped, but valid ones before it are committed.
func TestReviewMultipleSelfAuthorizationNonce_WithInvalidNonceSkipped(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegateA := common.HexToAddress("0x1111111111111111111111111111111111111111")
	delegateB := common.HexToAddress("0x2222222222222222222222222222222222222222")
	expectedCodeHashA := crypto.Keccak256Hash(eth.AddressToDelegation(delegateA))

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(chainID),
		TxSignatureMode: config.TxSignatureModeSecp,
	})

	seedAccount(t, cs, senderAddr, big.NewInt(1_000_000_000_000), 1)

	// Auth 1: valid nonce 2
	auth1 := signAuthorization(t, senderKey, chainID, delegateA, 2)
	// Auth 2: invalid nonce 99 (expected 3)
	auth2 := signAuthorization(t, senderKey, chainID, delegateB, 99)

	inner := &eth.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       150_000,
		To:        common.HexToAddress("0x9999"),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2},
	}
	signer := eth.NewPragueSigner(big.NewInt(chainID))
	signed, err := eth.SignNewTx(senderKey, signer, inner)
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	senderState, err := cs.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	// Initial 1 + 1 (tx) + 1 (auth1) = 3 (auth2 skipped)
	assert.Equal(t, uint64(3), senderState.Nonce(), "Sender nonce must be 3 because auth2 was skipped")
	require.NotNil(t, senderState.SmartContractState())
	assert.Equal(t, expectedCodeHashA, senderState.SmartContractState().CodeHash(), "Delegation code hash must match delegateA")
}

// TestReviewMultipleSelfAuthorizationNonce_MixedWithThirdParty tests interleaved
// self-authorizations and third-party authorizations in a single transaction.
func TestReviewMultipleSelfAuthorizationNonce_MixedWithThirdParty(t *testing.T) {
	const chainID = 991
	aliceKey, aliceAddr := genTestKey(t)
	bobKey, bobAddr := genTestKey(t)

	delegate1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	delegate2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	delegate3 := common.HexToAddress("0x3333333333333333333333333333333333333333")
	delegate4 := common.HexToAddress("0x4444444444444444444444444444444444444444")
	expectedCodeHashAlice := crypto.Keccak256Hash(eth.AddressToDelegation(delegate4))
	expectedCodeHashBob := crypto.Keccak256Hash(eth.AddressToDelegation(delegate3))

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(chainID),
		TxSignatureMode: config.TxSignatureModeSecp,
	})

	seedAccount(t, cs, aliceAddr, big.NewInt(1_000_000_000_000), 1)
	seedAccount(t, cs, bobAddr, big.NewInt(500), 10)

	// Auth 1: Bob, nonce 10 -> delegate1
	auth1 := signAuthorization(t, bobKey, chainID, delegate1, 10)
	// Auth 2: Alice, nonce 2 -> delegate2
	auth2 := signAuthorization(t, aliceKey, chainID, delegate2, 2)
	// Auth 3: Bob, nonce 11 -> delegate3
	auth3 := signAuthorization(t, bobKey, chainID, delegate3, 11)
	// Auth 4: Alice, nonce 3 -> delegate4
	auth4 := signAuthorization(t, aliceKey, chainID, delegate4, 3)

	inner := &eth.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       250_000,
		To:        common.HexToAddress("0x9999"),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2, auth3, auth4},
	}
	signer := eth.NewPragueSigner(big.NewInt(chainID))
	signed, err := eth.SignNewTx(aliceKey, signer, inner)
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	aliceState, err := cs.GetAccountStateDB().AccountState(aliceAddr)
	require.NoError(t, err)
	bobState, err := cs.GetAccountStateDB().AccountState(bobAddr)
	require.NoError(t, err)

	// Alice: initial 1 + 1 (tx) + 2 (auth2, auth4) = 4
	assert.Equal(t, uint64(4), aliceState.Nonce(), "Alice nonce must be 4")
	assert.Equal(t, expectedCodeHashAlice, aliceState.SmartContractState().CodeHash(), "Alice must delegate to delegate4")

	// Bob: initial 10 + 2 (auth1, auth3) = 12
	assert.Equal(t, uint64(12), bobState.Nonce(), "Bob nonce must be 12")
	assert.Equal(t, expectedCodeHashBob, bobState.SmartContractState().CodeHash(), "Bob must delegate to delegate3")
}

// TestReviewMultipleSelfAuthorizationNonce_EVMContractExecutionPath tests multiple
// self-authorizations where the transaction calls an EVM smart contract (To is a contract).
func TestReviewMultipleSelfAuthorizationNonce_EVMContractExecutionPath(t *testing.T) {
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
	})

	senderKey, senderAddr := genTestKey(t)
	contractAddr := common.HexToAddress("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
	leader := common.HexToAddress(auditLeader)

	seedAccount(t, cs, senderAddr, big.NewInt(1_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Simple contract code: PUSH1 42, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
	contractCode := common.FromHex("602a60005260206000f3")
	seedContract(t, cs, contractAddr, big.NewInt(0), contractCode)

	delegate1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	delegate2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	auth1 := signAuthorization(t, senderKey, chainID.Uint64(), delegate1, 2)
	auth2 := signAuthorization(t, senderKey, chainID.Uint64(), delegate2, 3)

	inner := &eth.SetCodeTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     1,
		GasTipCap: uint256.NewInt(15_000),
		GasFeeCap: uint256.NewInt(200_000),
		Gas:       200_000,
		To:        contractAddr,
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2},
	}
	signer := eth.NewPragueSigner(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, inner)
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	st, err := cs.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx, cs, st))

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	_, receipts, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, receipts, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipts[0].Status())
	require.Equal(t, int64(42), new(big.Int).SetBytes(receipts[0].Return()).Int64())

	senderState, err := cs.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), senderState.Nonce(), "Sender nonce on EVM contract path must be 4")
	expectedCodeHash2 := crypto.Keccak256Hash(eth.AddressToDelegation(delegate2))
	assert.Equal(t, expectedCodeHash2, senderState.SmartContractState().CodeHash())
}

// TestReviewMultipleSelfAuthorizationNonce_RevocationInChain tests delegation then revocation
// within multiple self-authorizations in the same transaction.
func TestReviewMultipleSelfAuthorizationNonce_RevocationInChain(t *testing.T) {
	const chainID = 991
	senderKey, senderAddr := genTestKey(t)
	delegateA := common.HexToAddress("0x1111111111111111111111111111111111111111")

	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         big.NewInt(chainID),
		TxSignatureMode: config.TxSignatureModeSecp,
	})

	seedAccount(t, cs, senderAddr, big.NewInt(1_000_000_000_000), 1)

	// Auth 1: delegate to delegateA (nonce 2)
	auth1 := signAuthorization(t, senderKey, chainID, delegateA, 2)
	// Auth 2: revoke delegation by delegating to 0x0 (nonce 3)
	auth2 := signAuthorization(t, senderKey, chainID, common.Address{}, 3)

	inner := &eth.SetCodeTx{
		ChainID:   uint256.NewInt(chainID),
		Nonce:     1,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       150_000,
		To:        common.HexToAddress("0x9999"),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2},
	}
	signer := eth.NewPragueSigner(big.NewInt(chainID))
	signed, err := eth.SignNewTx(senderKey, signer, inner)
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	stm := NewTrueBlockSTM([]mt_types.Transaction{tx})
	leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")
	_, rcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 12345)
	require.Len(t, rcps, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcps[0].Status())

	senderState, err := cs.GetAccountStateDB().AccountState(senderAddr)
	require.NoError(t, err)
	// Final nonce must be 4
	assert.Equal(t, uint64(4), senderState.Nonce(), "Sender nonce must be 4 after delegation + revocation")
	// Delegation must be revoked (CodeHash cleared to empty hash)
	if senderState.SmartContractState() != nil {
		assert.Equal(t, common.Hash{}, senderState.SmartContractState().CodeHash(), "CodeHash must be cleared to empty hash")
	}
}
