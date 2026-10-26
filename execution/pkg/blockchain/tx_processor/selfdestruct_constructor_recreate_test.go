package tx_processor

import (
	"context"
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
	"math/big"
	"testing"
)

func TestConstructorSelfdestructAllowsRecreation(t *testing.T) {
	for _, destroyed := range []bool{false, true} {
		name := "live_contract_control"
		if destroyed {
			name = "destroyed_in_constructor"
		}
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
			childInit := common.FromHex("600060005360016000f3")
			beneficiary := common.HexToAddress("0x1234567890123456789012345678901234567890")
			if destroyed {
				childInit = append(append([]byte{0x73}, beneficiary.Bytes()...), 0xff)
			}
			runtime := createFactoryRuntime(true, childInit, false)
			init := append([]byte{0x60, byte(len(runtime)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}, runtime...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, init).Status())
			factory := crypto.CreateAddress(sender, 1)
			receipt := execute(2, &factory, 0, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			child := common.BytesToAddress(receipt.Return())
			require.NotEqual(t, common.Address{}, child)
			st, err := cs.GetAccountStateDB().AccountState(child)
			require.NoError(t, err)
			if destroyed {
				require.Zero(t, st.TotalBalance().Sign())
			} else {
				require.Equal(t, "100", st.TotalBalance().String())
			}
			t.Logf("first CREATE2 child=%s nonce=%d balance=%s", child, st.Nonce(), st.TotalBalance())
			again := execute(3, &factory, 0, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, again.Status())
			st, err = cs.GetAccountStateDB().AccountState(child)
			require.NoError(t, err)
			result := new(big.Int).SetBytes(again.Return())
			t.Logf("second CREATE2 result=%x child_nonce=%d child_balance=%s", again.Return(), st.Nonce(), st.TotalBalance())
			if destroyed {
				receiver, err := cs.GetAccountStateDB().AccountState(beneficiary)
				require.NoError(t, err)
				t.Logf("beneficiary_balance=%s", receiver.TotalBalance())
				require.Equal(t, child, common.BytesToAddress(again.Return()), "account destroyed in creation transaction must be reusable in a later transaction")
				require.Equal(t, "200", receiver.TotalBalance().String())
			} else {
				require.Zero(t, result.Sign(), "live contract must reject recreation")
				require.Equal(t, "100", st.TotalBalance().String())
			}
		})
	}
}
