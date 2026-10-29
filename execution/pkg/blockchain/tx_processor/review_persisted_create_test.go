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
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/require"
)

// Authorities are warmed only for authorizations that pass go-ethereum's pre-warm checks
// (chain id 0 or equal to the chain, no nonce overflow, recoverable signature). A wrong chain id
// must leave the authority cold (2600 gas on first CALL), otherwise MVM undercharges by 2500.
func TestReviewPersistedDelegationCreateAddress(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	authKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	authority = sender
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)
	// Caller performs CALL(authority) once; delegate runtime is STOP.
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50, 0x00})
	seedContract(t, cs, sender, big.NewInt(1_000_000_000_000_000), eth.AddressToDelegation(delegate))
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        &caller,
		Gas:       200000,
		GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     big.NewInt(0),
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)
	st, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx, cs, st))
	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, authority, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetNonce(authority, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(sender, eth.AddressToDelegation(delegate))
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50, 0x00})
	reference.SetNonce(delegate, 1, tracing.NonceChangeUnspecified)
	random := common.Hash{}
	cfg := *params.AllDevChainProtocolChanges
	cfg.ChainID = chainID
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
	}, reference, &cfg, vm.Config{})
	msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
	require.NoError(t, err)
	g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.NoError(t, g.Err)
	t.Logf("geth_gas=%d MVM_gas=%d delta=%d status=%v", g.UsedGas, r[0].GasUsed(), int64(r[0].GasUsed())-int64(g.UsedGas), r[0].Status())
	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	t.Logf("sender_balance_geth=%s sender_balance_mvm=%s", reference.GetBalance(sender), actual.Balance())
	t.Logf("sender_nonce_geth=%d sender_nonce_mvm=%d", reference.GetNonce(sender), actual.Nonce())
	require.Equal(t, reference.GetNonce(sender), actual.Nonce(), "internal CREATE must persist sender nonce")
	expectedChild := crypto.CreateAddress(sender, 2)
	wrongChild := crypto.CreateAddress(sender, 1)
	child, err := cs.GetAccountStateDB().AccountState(expectedChild)
	require.NoError(t, err)
	wrong, err := cs.GetAccountStateDB().AccountState(wrongChild)
	require.NoError(t, err)
	t.Logf("expected_child=%s geth_nonce=%d mvm_nonce=%d prior_nonce_child=%s mvm_nonce=%d", expectedChild, reference.GetNonce(expectedChild), child.Nonce(), wrongChild, wrong.Nonce())
	require.Equal(t, reference.GetNonce(expectedChild), child.Nonce(), "CREATE must use sender nonce after transaction increment")
}

// TestReviewPersistedDelegationCreateAddress_RevertRollback verifies that when an outer call
// reverts after an internal CREATE, the child contract is not committed and the sender nonce
// is rolled back to tx.Nonce + 1 (2), matching go-ethereum.
func TestReviewPersistedDelegationCreateAddress_RevertRollback(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	// Caller performs CALL(sender), then immediately REVERTs (PUSH1 0, PUSH1 0, REVERT).
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, sender.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x60, 0x00, 0x60, 0x00, 0xfd)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50, 0x00})
	seedContract(t, cs, sender, big.NewInt(1_000_000_000_000_000), eth.AddressToDelegation(delegate))

	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        &caller,
		Gas:       200000,
		GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     big.NewInt(0),
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Geth reference
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(sender, eth.AddressToDelegation(delegate))
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50, 0x00})
	reference.SetNonce(delegate, 1, tracing.NonceChangeUnspecified)

	random := common.Hash{}
	cfg := *params.AllDevChainProtocolChanges
	cfg.ChainID = chainID
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
	}, reference, &cfg, vm.Config{})
	msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
	require.NoError(t, err)
	g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.Error(t, g.Err, "outer call must revert")

	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetNonce(sender), actual.Nonce(), "reverted call sender nonce must match geth (2)")
	require.Equal(t, uint64(2), actual.Nonce())

	// Child contract must NOT exist
	childAddr := crypto.CreateAddress(sender, 2)
	child, err := cs.GetAccountStateDB().AccountState(childAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(0), child.Nonce())
}

// TestTopLevelDeploy_ContractAddressNotShifted verifies that top-level contract deployment
// transactions (To == nil) continue to create contracts using the initial tx.Nonce (e.g. 1),
// and sender nonce finishes at tx.Nonce + 1 (2), matching go-ethereum.
func TestTopLevelDeploy_ContractAddressNotShifted(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	leader := common.HexToAddress(auditLeader)

	// Deploy simple runtime code [0x42]
	initCode := []byte{0x60, 0x42, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x1f, 0xf3}

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        nil, // contract deployment
		Gas:       200000,
		GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     big.NewInt(0),
		Data:      initCode,
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)
	require.True(t, tx.IsDeployContract())

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Geth reference
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)

	random := common.Hash{}
	cfg := *params.AllDevChainProtocolChanges
	cfg.ChainID = chainID
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
	}, reference, &cfg, vm.Config{})
	msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
	require.NoError(t, err)
	g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.NoError(t, g.Err)

	expectedContract := crypto.CreateAddress(sender, 1)
	actualSender, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetNonce(sender), actualSender.Nonce(), "deploy sender nonce must match geth (2)")

	deployed, err := cs.GetAccountStateDB().AccountState(expectedContract)
	require.NoError(t, err)
	require.Equal(t, uint64(1), deployed.Nonce(), "new contract starts with nonce 1 per EIP-161")
	codeHash := deployed.SmartContractState().CodeHash()
	require.Equal(t, []byte{0x42}, cs.GetSmartContractDB().GetCodeByCodeHash(expectedContract, codeHash))
}

// TestReviewPersistedDelegationCreate_RevertThenNextTxSameContract verifies that if a transaction
// containing internal CREATE reverts, the internal contract/creator nonce is NOT left corrupted
// in the shared State cache, so the very next transaction from the same sender/contract succeeds
// at the correct nonce and address, matching geth.
func TestReviewPersistedDelegationCreate_RevertThenNextTxSameContract(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authority := sender
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	callerRevert := common.HexToAddress("0x1111111111111111111111111111111111111111")
	callerSuccess := common.HexToAddress("0x2222222222222222222222222222222222222222")
	leader := common.HexToAddress(auditLeader)

	// callerRevert calls authority (which does CREATE), then REVERTs
	callerRevertCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerRevertCode = append(callerRevertCode, authority.Bytes()...)
	callerRevertCode = append(callerRevertCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x60, 0x00, 0x60, 0x00, 0xfd)

	// callerSuccess calls authority (which does CREATE), then returns successfully
	callerSuccessCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerSuccessCode = append(callerSuccessCode, authority.Bytes()...)
	callerSuccessCode = append(callerSuccessCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	seedContract(t, cs, callerRevert, big.NewInt(0), callerRevertCode)
	seedContract(t, cs, callerSuccess, big.NewInt(0), callerSuccessCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50, 0x00})
	seedContract(t, cs, sender, big.NewInt(1_000_000_000_000_000), eth.AddressToDelegation(delegate))

	signer := eth.LatestSignerForChainID(chainID)

	// Tx 1: calls callerRevert (reverts)
	signed1, err := eth.SignNewTx(senderKey, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        &callerRevert,
		Gas:       200000,
		GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     big.NewInt(0),
	})
	require.NoError(t, err)
	tx1, err := transaction.NewTransactionFromEth(signed1)
	require.NoError(t, err)

	items1 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx1), callerRevert, authority, delegate), Tx: tx1}}
	_, r1, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items1, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r1, 1)

	// Verify sender nonce after Tx 1 is 2 (tx nonce + 1, internal CREATE rolled back)
	actual1, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, uint64(2), actual1.Nonce(), "reverted tx must leave sender nonce at tx.Nonce + 1 = 2")

	// Tx 2: calls callerSuccess (succeeds, internal CREATE creates contract at sender nonce = 2 + 1 = 3)
	signed2, err := eth.SignNewTx(senderKey, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     2,
		To:        &callerSuccess,
		Gas:       200000,
		GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     big.NewInt(0),
	})
	require.NoError(t, err)
	tx2, err := transaction.NewTransactionFromEth(signed2)
	require.NoError(t, err)

	items2 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx2), callerSuccess, authority, delegate), Tx: tx2}}
	_, r2, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r2, 1)

	// Geth reference for sequential execution
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(sender, eth.AddressToDelegation(delegate))
	reference.SetCode(callerRevert, callerRevertCode)
	reference.SetNonce(callerRevert, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(callerSuccess, callerSuccessCode)
	reference.SetNonce(callerSuccess, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0x60, 0, 0x60, 0, 0x60, 0, 0xf0, 0x50, 0x00})
	reference.SetNonce(delegate, 1, tracing.NonceChangeUnspecified)

	random := common.Hash{}
	cfg := *params.AllDevChainProtocolChanges
	cfg.ChainID = chainID
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
	}, reference, &cfg, vm.Config{})

	// Apply Tx 1 on geth
	msg1, err := core.TransactionToMessage(signed1, signer, big.NewInt(0))
	require.NoError(t, err)
	_, _ = core.ApplyMessage(evm, msg1, new(core.GasPool).AddGas(1000000))

	// Apply Tx 2 on geth
	msg2, err := core.TransactionToMessage(signed2, signer, big.NewInt(0))
	require.NoError(t, err)
	_, _ = core.ApplyMessage(evm, msg2, new(core.GasPool).AddGas(1000000))

	// Final verification
	actual2, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetNonce(sender), actual2.Nonce(), "final sender nonce must match geth across tx sequence")

	// Tx 2 created child using sender nonce = 3 (nonce 2 was tx2 start, bumped to 3 by Call, CREATE used 3)
	expectedChild := crypto.CreateAddress(sender, 3)
	child, err := cs.GetAccountStateDB().AccountState(expectedChild)
	require.NoError(t, err)
	require.Equal(t, reference.GetNonce(expectedChild), child.Nonce(), "child contract must be created at nonce 3 matching geth")
}
