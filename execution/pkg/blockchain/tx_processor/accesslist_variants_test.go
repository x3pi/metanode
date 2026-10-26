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
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// TestAccessListVariantsMatchGeth compares total gas used against go-ethereum for access
// lists that differ in shape: address-only entries, duplicates, the sender and a precompile
// listed explicitly, and an empty list.
func TestAccessListVariantsMatchGeth(t *testing.T) {
	other := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	// BALANCE(other) POP; SLOAD(0) POP; STOP
	code := append(append([]byte{0x73}, other.Bytes()...), common.FromHex("31506000545000")...)
	cases := map[string]func(contract, sender common.Address) eth.AccessList{
		"address_only": func(c, s common.Address) eth.AccessList { return eth.AccessList{{Address: other}} },
		"duplicates": func(c, s common.Address) eth.AccessList {
			return eth.AccessList{
				{Address: c, StorageKeys: []common.Hash{{}, {}}},
				{Address: c, StorageKeys: []common.Hash{{}}},
				{Address: other},
				{Address: other},
			}
		},
		"sender_and_precompile": func(c, s common.Address) eth.AccessList {
			return eth.AccessList{{Address: s}, {Address: common.HexToAddress("0x01")}, {Address: other}}
		},
		"empty": func(c, s common.Address) eth.AccessList { return eth.AccessList{} },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
			t.Setenv("METANODE_DEVNET", "false")
			cs := newTestChainState(t)
			chainID := big.NewInt(991)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
			key, err := crypto.GenerateKey()
			require.NoError(t, err)
			sender := crypto.PubkeyToAddress(key.PublicKey)
			leader := common.HexToAddress(auditLeader)
			seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			signer := eth.LatestSignerForChainID(chainID)
			execute := func(signed *eth.Transaction) types.Receipt {
				tx, err := transaction.NewTransactionFromEth(signed)
				require.NoError(t, err)
				st, err := cs.GetAccountStateDB().AccountState(sender)
				require.NoError(t, err)
				require.Nil(t, VerifyTransaction(tx, cs, st))
				items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
				_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
				require.Len(t, r, 1)
				return r[0]
			}
			deploy, err := eth.SignNewTx(key, signer, &eth.LegacyTx{Nonce: 1, Gas: 300000, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: confDeployInit(code)})
			require.NoError(t, err)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(deploy).Status())
			contract := crypto.CreateAddress(sender, 1)
			signed, err := eth.SignNewTx(key, signer, &eth.AccessListTx{ChainID: chainID, Nonce: 2, To: &contract, Value: big.NewInt(0), Gas: 200000, GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), AccessList: mk(contract, sender)})
			require.NoError(t, err)
			m := execute(signed)

			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, 2, tracing.NonceChangeUnspecified)
			reference.SetNonce(contract, 1, tracing.NonceChangeUnspecified)
			reference.SetCode(contract, code)
			random := common.Hash{}
			evm := vm.NewEVM(vm.BlockContext{CanTransfer: core.CanTransfer, Transfer: core.Transfer, GetHash: func(uint64) common.Hash { return common.Hash{} }, GasLimit: 1000000, BlockNumber: big.NewInt(1), Time: 1, Difficulty: big.NewInt(0), BaseFee: big.NewInt(0), Random: &random}, reference, params.AllDevChainProtocolChanges, vm.Config{})
			msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
			require.NoError(t, err)
			g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
			require.NoError(t, err)
			require.Equal(t, g.UsedGas, m.GasUsed())
		})
	}
}
