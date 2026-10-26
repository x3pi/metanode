package tx_processor

import (
	"context"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	eth "github.com/ethereum/go-ethereum/core/types"
	evmruntime "github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
	"math/big"
	"testing"
)

func TestRootDeploymentInitialNonce(t *testing.T) {
	for _, create2 := range []bool{false} {
		name := "top_level_factory_first_CREATE"
		t.Run(name, func(t *testing.T) {
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
				signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{Nonce: nonce, To: to, Value: big.NewInt(value), Gas: 300000, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data})
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
			// Constructor returns one byte containing CALLVALUE as its deployed code.
			childInit := common.FromHex("346000526001601ff3")
			runtime := createFactoryRuntime(create2, childInit, false)
			init := append([]byte{0x60, byte(len(runtime)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}, runtime...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, init).Status())
			factory := crypto.CreateAddress(sender, 1)
			fa, err := cs.GetAccountStateDB().AccountState(factory)
			require.NoError(t, err)
			t.Logf("newly deployed factory nonce=%d", fa.Nonce())
			if fa.Nonce() != 1 {
				t.Errorf("root contract nonce: want 1, got %d", fa.Nonce())
			}
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			cfg := &evmruntime.Config{State: reference, Origin: sender, Value: big.NewInt(1000), GasLimit: 300000, BlockNumber: big.NewInt(1)}
			_, gethFactory, _, err := evmruntime.Create(init, cfg)
			require.NoError(t, err)
			require.Equal(t, factory, gethFactory)
			t.Logf("geth deployed factory nonce=%d", reference.GetNonce(factory))
			require.Equal(t, uint64(1), reference.GetNonce(factory))
			receipt := execute(2, &factory, 0, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			child := common.BytesToAddress(receipt.Return())
			require.NotEqual(t, common.Address{}, child)
			expectedChild := crypto.CreateAddress(factory, 1)
			t.Logf("first CREATE child=%s expected=%s child_using_nonce_zero=%s", child, expectedChild, crypto.CreateAddress(factory, 0))
			if child != expectedChild {
				t.Errorf("first CREATE address uses wrong initial nonce")
			}
			st, err := cs.GetAccountStateDB().AccountState(child)
			require.NoError(t, err)
			require.Equal(t, "100", st.TotalBalance().String())
			require.NotNil(t, st.SmartContractState())
			actual := st.SmartContractState().CodeHash()
			t.Logf("child balance=%s code_hash=%s hash_of_zero=%s hash_of_100=%s", st.TotalBalance(), actual, crypto.Keccak256Hash([]byte{0}), crypto.Keccak256Hash([]byte{100}))
			require.Equal(t, crypto.Keccak256Hash([]byte{100}), actual, "constructor CALLVALUE must equal transferred endowment")
		})
	}
}
