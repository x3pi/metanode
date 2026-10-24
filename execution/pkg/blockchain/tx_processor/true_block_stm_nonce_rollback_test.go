package tx_processor

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// A contract that CREATEs a child and then either returns or reverts. The child's initcode is NOT empty: an empty-initcode CREATE
// ends the calling frame in this MVM, which would hide what happens after the CREATE.
//
//	runtime = PUSH13 <child initcode> PUSH1 0 MSTORE; CREATE(value 0, offset 19, size 13); POP; <tail>
const factoryCreatePrefix = "6c" + "6001600c60003960016000f300" + "600052" + "600d60136000f0" + "50"

// runFactoryCall deploys a factory whose runtime is factoryCreatePrefix+tail, calls it in the next block and returns the
// call receipt, the factory's final nonce and the sender's final nonce.
func runFactoryCall(t *testing.T, tail string) (mt_types.Receipt, uint64, uint64) {
	t.Helper()
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs, _, _, _ := newPersistentTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)
	seedAccount(t, cs, sender, new(big.Int).Mul(big.NewInt(1_000_000_000_000), big.NewInt(1000)), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	factory := crypto.CreateAddress(sender, 1)
	price := new(big.Int).SetUint64(mt_common.MINIMUM_BASE_FEE)

	run := func(nonce uint64, to *common.Address, data []byte) mt_types.Receipt {
		signed, err := eth_types.SignNewTx(key, eth_types.NewEIP155Signer(chainID), &eth_types.LegacyTx{Nonce: nonce, To: to, Value: big.NewInt(0), Gas: 400000, GasPrice: price, Data: data})
		require.NoError(t, err)
		tx, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
		groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
		_, rcps, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
		require.Len(t, rcps, 1)
		return rcps[0]
	}
	deploy := run(1, nil, deployInitCode(common.FromHex("0x"+factoryCreatePrefix+tail)))
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, deploy.Status())
	call := run(2, &factory, []byte{1})
	nonce := func(a common.Address) uint64 {
		st, err := cs.GetAccountStateDB().AccountState(a)
		require.NoError(t, err)
		return st.Nonce()
	}
	return call, nonce(factory), nonce(sender)
}

// Control: the same CREATE that succeeds keeps the factory's nonce increment.
func TestFactoryCreateThatSucceedsKeepsItsNonce(t *testing.T) {
	call, factoryNonce, senderNonce := runFactoryCall(t, "602a60005260206000f3")
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, call.Status())
	require.Len(t, call.Return(), 32, "execution must continue after CREATE")
	require.Equal(t, uint64(2), factoryNonce, "a deployed contract starts at nonce 1 (EIP-161) and the CREATE makes it 2")
	require.Equal(t, uint64(3), senderNonce)
}

// A CREATE inside a frame that later reverts is undone with the frame: the factory's nonce increment must not be committed,
// while the sender's nonce still advances (the transaction is included, so it cannot be replayed).
func TestFactoryRevertAfterCreateDoesNotPersistItsNonce(t *testing.T) {
	call, factoryNonce, senderNonce := runFactoryCall(t, "60006000fd")
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, call.Status())
	require.Equal(t, pb.EXCEPTION_ERR_EXECUTION_REVERTED, call.Exception())
	require.Equal(t, uint64(1), factoryNonce, "a reverted CREATE must not leave the factory nonce incremented (it stays at its initial 1)")
	require.Equal(t, uint64(3), senderNonce, "the sender nonce is consumed even though the execution reverted")
}
