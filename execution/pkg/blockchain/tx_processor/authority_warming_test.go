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
func TestEIP7702_AuthorityWarmingWrongChainMatchesGeth(t *testing.T) {
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
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)
	// Caller performs CALL(authority) once; delegate runtime is STOP.
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})
	auth := signAuthorization(t, authKey, 12345, delegate, 0)
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        caller,
		Gas:       200000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth},
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
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0})
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
	require.Equal(t, g.UsedGas, r[0].GasUsed(), "variant gas parity")
}

func TestEIP7702_AuthorityWarmingWrongNonceMatchesGeth(t *testing.T) {
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
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)
	// Caller performs CALL(authority) once; delegate runtime is STOP.
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})
	auth := signAuthorization(t, authKey, 991, delegate, 7)
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        caller,
		Gas:       200000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth},
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
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0})
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
	require.Equal(t, g.UsedGas, r[0].GasUsed(), "variant gas parity")
}

func TestEIP7702_AuthorityWarmingChainZeroMatchesGeth(t *testing.T) {
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
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)
	// Caller performs CALL(authority) once; delegate runtime is STOP.
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})
	auth := signAuthorization(t, authKey, 0, delegate, 0)
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        caller,
		Gas:       200000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth},
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
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0})
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
	require.Equal(t, g.UsedGas, r[0].GasUsed(), "variant gas parity")
}

func TestEIP7702_AuthorityWarmingNonceOverflowMatchesGeth(t *testing.T) {
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
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)
	// Caller performs CALL(authority) once; delegate runtime is STOP.
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})
	auth := signAuthorization(t, authKey, 991, delegate, ^uint64(0))
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        caller,
		Gas:       200000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth},
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
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0})
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
	require.Equal(t, g.UsedGas, r[0].GasUsed(), "variant gas parity")
}
