package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// TestSelfdestructSameTxCollisionFails ensures that if a contract is created and self-destructs
// in the same transaction, a subsequent CREATE2 in the SAME transaction at the same address
// MUST fail (collision within the same transaction), adhering to EVM Cancun semantics.
func TestSelfdestructSameTxCollisionFails(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	execute := func(nonce uint64, to *common.Address, value int64, data []byte) types.Receipt {
		signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    big.NewInt(value),
			Gas:      300000,
			GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
			Data:     data,
		})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		st, err := cs.GetAccountStateDB().AccountState(sender)
		require.NoError(t, err)
		require.Nil(t, VerifyTransaction(tx, cs, st))
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		_, receipts, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
		require.Len(t, receipts, 1)
		return receipts[0]
	}

	beneficiary := common.HexToAddress("0x1234567890123456789012345678901234567890")
	// child initcode: selfdestruct to beneficiary
	childInit := append(append([]byte{0x73}, beneficiary.Bytes()...), 0xff)
	l := byte(len(childInit))

	// Factory that performs CREATE2 twice in the same transaction with the same salt (0):
	// 1st CREATE2 creates child, child selfdestructs.
	// 2nd CREATE2 attempts to create at same address in same tx -> must return 0!
	runtime := []byte{
		0x60, l, 0x60, 0x24, 0x60, 0x80, 0x39,
		0x60, 0x00, 0x60, l, 0x60, 0x80, 0x60, 100, 0xf5,
		0x60, 0x00, 0x52,
		0x60, 0x00, 0x60, l, 0x60, 0x80, 0x60, 100, 0xf5,
		0x60, 0x20, 0x52,
		0x60, 0x40, 0x60, 0x00, 0xf3,
	}
	runtime = append(runtime, childInit...)

	init := append([]byte{0x60, byte(len(runtime)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}, runtime...)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, init).Status())
	factory := crypto.CreateAddress(sender, 1)

	receipt := execute(2, &factory, 0, []byte{1})
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
	ret := receipt.Return()
	require.Len(t, ret, 64)

	firstResult := common.BytesToAddress(ret[:32])
	secondResult := common.BytesToAddress(ret[32:])

	require.NotEqual(t, common.Address{}, firstResult, "first CREATE2 in same tx must succeed")
	require.Equal(t, common.Address{}, secondResult, "second CREATE2 in same tx must fail with address 0 (collision)")

	t.Logf("same-tx first CREATE2=%s second CREATE2=%s", firstResult, secondResult)
}

// TestSelfdestructAncestorRevertRollsBack ensures that if a factory's constructor or frame reverts,
// any child contract creation and selfdestruct within that frame is completely rolled back.
func TestSelfdestructAncestorRevertRollsBack(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	execute := func(nonce uint64, to *common.Address, value int64, data []byte) types.Receipt {
		signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    big.NewInt(value),
			Gas:      300000,
			GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
			Data:     data,
		})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		st, err := cs.GetAccountStateDB().AccountState(sender)
		require.NoError(t, err)
		require.Nil(t, VerifyTransaction(tx, cs, st))
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		_, receipts, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
		require.Len(t, receipts, 1)
		return receipts[0]
	}

	beneficiary := common.HexToAddress("0x1234567890123456789012345678901234567890")
	childInit := append(append([]byte{0x73}, beneficiary.Bytes()...), 0xff)

	// Factory runtime with tailRevert=true
	runtimeReverting := createFactoryRuntime(true, childInit, true)
	initReverting := append([]byte{0x60, byte(len(runtimeReverting)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtimeReverting)), 0x60, 0, 0xf3}, runtimeReverting...)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, initReverting).Status())
	factoryReverting := crypto.CreateAddress(sender, 1)

	// Call factory: CREATE2 executes, child selfdestructs, then factory REVERTs
	rcp := execute(2, &factoryReverting, 0, []byte{1})
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, rcp.Status(), "factory should revert")

	// Beneficiary should have received 0 because transaction reverted
	benSt, err := cs.GetAccountStateDB().AccountState(beneficiary)
	require.NoError(t, err)
	require.Zero(t, benSt.TotalBalance().Sign(), "beneficiary balance must remain 0 when ancestor reverts")

	// Factory with tailRevert=false
	runtimeNormal := createFactoryRuntime(true, childInit, false)
	initNormal := append([]byte{0x60, byte(len(runtimeNormal)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtimeNormal)), 0x60, 0, 0xf3}, runtimeNormal...)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(3, nil, 1000, initNormal).Status())
	factoryNormal := crypto.CreateAddress(sender, 3)

	// Execute normal CREATE2: should succeed cleanly
	normalRcp := execute(4, &factoryNormal, 0, []byte{1})
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, normalRcp.Status())
	child := common.BytesToAddress(normalRcp.Return())
	require.NotEqual(t, common.Address{}, child)

	benSt, err = cs.GetAccountStateDB().AccountState(beneficiary)
	require.NoError(t, err)
	require.Equal(t, "100", benSt.TotalBalance().String(), "beneficiary received 100 wei from normal creation")
}
