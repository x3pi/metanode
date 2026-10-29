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

// TestEIP7702GasRefund_ExistingAuthority_EVMCall_GethParity verifies that an existing
// authority account in an EIP-7702 transaction correctly receives the 12,500 gas refund,
// pooled with EVM gas and capped at 20% (EIP-3529). Both gasUsed and sender balance must
// match go-ethereum down to the exact unit.
func TestEIP7702GasRefund_ExistingAuthority_EVMCall_GethParity(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	senderKey, _ := genTestKey(t)
	authKey, _ := genTestKey(t)
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
	seedAccount(t, cs, authority, big.NewInt(0), 1) // existing authority (nonce > 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})

	auth := signAuthorization(t, authKey, 991, delegate, 1)
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

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, authority, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Reference geth state transition
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetNonce(authority, 1, tracing.NonceChangeUnspecified)
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

	require.Equal(t, g.UsedGas, r[0].GasUsed(), "gas used must match go-ethereum exactly (38,979)")
	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetBalance(sender).ToBig(), actual.Balance(), "sender balance must match go-ethereum exactly")
}

// TestEIP7702GasRefund_NewAuthority_EVMCall_GethParity verifies that an authorization for a
// non-existent account pays the full 25,000 intrinsic gas without any refund.
func TestEIP7702GasRefund_NewAuthority_EVMCall_GethParity(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	senderKey, _ := genTestKey(t)
	authKey, _ := genTestKey(t)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authority := crypto.PubkeyToAddress(authKey.PublicKey) // Fresh account (not seeded)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})

	// Auth nonce is 0 because account is new
	auth := signAuthorization(t, authKey, 991, delegate, 0)
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

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, authority, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Geth reference
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

	require.Equal(t, g.UsedGas, r[0].GasUsed(), "gas used for new authority must match go-ethereum exactly (no refund)")
	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetBalance(sender).ToBig(), actual.Balance(), "sender balance must match go-ethereum exactly")
}

// TestEIP7702GasRefund_MultipleAuthorities_Mixed verifies a tx with multiple authorizations:
// one for an existing account (refund 12,500) and one for a fresh account (no refund).
func TestEIP7702GasRefund_MultipleAuthorities_Mixed(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	senderKey, _ := genTestKey(t)
	authKey1, _ := genTestKey(t) // existing
	authKey2, _ := genTestKey(t) // new
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	auth1Addr := crypto.PubkeyToAddress(authKey1.PublicKey)
	auth2Addr := crypto.PubkeyToAddress(authKey2.PublicKey)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, auth1Addr.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, auth1Addr, big.NewInt(0), 1) // auth1 exists
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})

	auth1 := signAuthorization(t, authKey1, 991, delegate, 1)
	auth2 := signAuthorization(t, authKey2, 991, delegate, 0)
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        caller,
		Gas:       300000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{auth1, auth2},
	})
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, auth1Addr, auth2Addr, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Geth reference
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetNonce(auth1Addr, 1, tracing.NonceChangeUnspecified)
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

	require.Equal(t, g.UsedGas, r[0].GasUsed(), "mixed authority authorizations gas must match go-ethereum")
	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetBalance(sender).ToBig(), actual.Balance(), "sender balance must match go-ethereum")
}

// TestEIP7702GasRefund_SelfAuthorization_ExistingSender_GethParity verifies that when the sender
// authorizes itself (self-authorization), since the sender already exists in state, it qualifies
// for the authorization refund and matches go-ethereum exactly.
func TestEIP7702GasRefund_SelfAuthorization_ExistingSender_GethParity(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	senderKey, _ := genTestKey(t)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, sender.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})

	// Self-authorization: auth.Nonce = tx.Nonce + 1 = 2
	auth := signAuthorization(t, senderKey, 991, delegate, 2)
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

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Geth reference
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

	require.Equal(t, g.UsedGas, r[0].GasUsed(), "self-authorization gas refund must match go-ethereum")
	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetBalance(sender).ToBig(), actual.Balance(), "sender balance must match go-ethereum")
}

// TestEIP7702GasRefund_NativeTransfer_ExistingAuthority_GethParity verifies that a plain transfer
// carrying an authorization tuple for an existing authority correctly applies the 20% capped refund.
func TestEIP7702GasRefund_NativeTransfer_ExistingAuthority_GethParity(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	senderKey, _ := genTestKey(t)
	authKey, _ := genTestKey(t)
	recipientKey, _ := genTestKey(t)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	recipient := crypto.PubkeyToAddress(recipientKey.PublicKey)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	leader := common.HexToAddress(auditLeader)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 1)
	seedAccount(t, cs, recipient, big.NewInt(100), 1) // existing recipient
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})

	auth := signAuthorization(t, authKey, 991, delegate, 1)
	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        recipient,
		Gas:       100000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(500),
		AuthList:  []eth.SetCodeAuthorization{auth},
	})
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), recipient, authority, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// In MetaNode, native transfer base cost is mt_common.TRANSFER_GAS_COST (20,000) rather than Ethereum's 21,000.
	// Therefore:
	// intrinsicGas = 20,000 + 25,000 = 45,000
	// maxRefund = 45,000 / 5 = 9,000
	// expectedGas = 45,000 - 9,000 = 36,000
	expectedGas := uint64(mt.TRANSFER_GAS_COST+params.CallNewAccountGas) - uint64(mt.TRANSFER_GAS_COST+params.CallNewAccountGas)/5
	require.Equal(t, expectedGas, r[0].GasUsed(), "native transfer gas refund must equal 36,000 in MetaNode")

	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	expectedBalance := new(big.Int).Sub(big.NewInt(1_000_000_000_000_000), big.NewInt(500))
	expectedBalance.Sub(expectedBalance, new(big.Int).Mul(new(big.Int).SetUint64(expectedGas), tx.EffectiveGasPrice()))
	require.Equal(t, expectedBalance, actual.Balance(), "sender balance must match expected balance after gas fee deduction")
}

// TestEIP7702GasRefund_EVMRevert_PreservesAuthorizationRefund verifies that when a contract
// call explicitly reverts (REVERT opcode), the authorization refund is still preserved and
// capped at 20% of gas spent up to revert, matching go-ethereum.
func TestEIP7702GasRefund_EVMRevert_PreservesAuthorizationRefund(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	senderKey, _ := genTestKey(t)
	authKey, _ := genTestKey(t)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	// Caller executes REVERT(0, 0): PUSH1 0, PUSH1 0, REVERT
	revertCode := []byte{0x60, 0x00, 0x60, 0x00, 0xfd}

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 1) // existing authority
	seedContract(t, cs, caller, big.NewInt(0), revertCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})

	auth := signAuthorization(t, authKey, 991, delegate, 1)
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

	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, authority, delegate), Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// Geth reference
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetNonce(authority, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(caller, revertCode)
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
	require.Error(t, g.Err, "execution must revert")

	require.Equal(t, g.UsedGas, r[0].GasUsed(), "reverted tx gas must match go-ethereum")
	actual, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Equal(t, reference.GetBalance(sender).ToBig(), actual.Balance(), "reverted tx sender balance must match go-ethereum")
}
