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

// TestCreateAndCreate2RejectedCodeRefundsEndowment kiểm tra toàn diện cả CREATE và CREATE2:
// Khi contract con trả về mã bắt đầu bằng 0xEF (bị từ chối theo chuẩn EIP-3541):
// 1. Lệnh CREATE / CREATE2 phải trả về 0 (thất bại).
// 2. Toàn bộ tiền gửi kèm (endowment / value) phải được hoàn lại 100% cho contract cha.
// 3. Địa chỉ contract con phải không nhận được tiền (balance = 0).
func TestCreateAndCreate2RejectedCodeRefundsEndowment(t *testing.T) {
	for _, opcode := range []string{"CREATE", "CREATE2"} {
		for _, rejected := range []bool{false, true} {
			name := opcode + "_valid_STOP"
			if rejected {
				name = opcode + "_rejected_EF"
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

				makeTx := func(nonce uint64, to *common.Address, value int64, data []byte) types.Transaction {
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
					return tx
				}

				execute := func(tx types.Transaction) types.Receipt {
					items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
					groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
					_, receipts, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
					require.Len(t, receipts, 1)
					return receipts[0]
				}

				var runtime []byte
				if opcode == "CREATE" {
					// PUSH1 10, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 10, PUSH1 0, PUSH1 100, CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
					runtime = common.FromHex("0x600a6016600039600a60006064f060005260206000f3")
				} else {
					// CREATE2: PUSH1 10, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 10, PUSH1 0, PUSH1 100, CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
					runtime = common.FromHex("0x600a60186000396000600a60006064f560005260206000f3")
				}

				childInit := common.FromHex("0x600060005360016000f3")
				if rejected {
					childInit[1] = 0xef
				}
				runtime = append(runtime, childInit...)

				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				init = append(init, runtime...)

				deploy := execute(makeTx(1, nil, 1000, init))
				require.Equal(t, pb.RECEIPT_STATUS_RETURNED, deploy.Status())

				factory := crypto.CreateAddress(sender, 1)
				before, err := cs.GetAccountStateDB().AccountState(factory)
				require.NoError(t, err)

				var child common.Address
				if opcode == "CREATE" {
					child = crypto.CreateAddress(factory, before.Nonce())
				} else {
					child = crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(childInit))
				}

				call := execute(makeTx(2, &factory, 0, nil))
				require.Equal(t, pb.RECEIPT_STATUS_RETURNED, call.Status())

				fa, err := cs.GetAccountStateDB().AccountState(factory)
				require.NoError(t, err)
				ch, err := cs.GetAccountStateDB().AccountState(child)
				require.NoError(t, err)

				result := new(big.Int).SetBytes(call.Return())
				t.Logf("[%s] rejected=%v CREATE_return=%s factory_balance=%s child_balance=%s",
					opcode, rejected, result, fa.TotalBalance(), ch.TotalBalance())

				if rejected {
					require.Zero(t, result.Sign(), "%s must report failure (return 0)", opcode)
					require.Equal(t, "1000", fa.TotalBalance().String(), "failed %s must refund endowment to factory", opcode)
					require.Zero(t, ch.TotalBalance().Sign(), "failed child must receive no endowment")
				} else {
					require.Equal(t, new(big.Int).SetBytes(child.Bytes()), result)
					require.Equal(t, "900", fa.TotalBalance().String())
					require.Equal(t, "100", ch.TotalBalance().String())
					if opcode == "CREATE2" {
						require.True(t, cs.HasCode(child), "first CREATE2 must persist child code")
						second := execute(makeTx(3, &factory, 0, nil))
						require.Equal(t, pb.RECEIPT_STATUS_RETURNED, second.Status())
						secondResult := new(big.Int).SetBytes(second.Return())
						fa2, err := cs.GetAccountStateDB().AccountState(factory)
						require.NoError(t, err)
						ch2, err := cs.GetAccountStateDB().AccountState(child)
						require.NoError(t, err)
						t.Logf("duplicate CREATE2: return=%s factory=%s child=%s", secondResult, fa2.TotalBalance(), ch2.TotalBalance())
						require.Zero(t, secondResult.Sign(), "CREATE2 at an existing contract address must fail")
						require.Equal(t, "900", fa2.TotalBalance().String(), "duplicate CREATE2 must not transfer endowment")
						require.Equal(t, "100", ch2.TotalBalance().String(), "duplicate CREATE2 must not add funds to child")
					}
				}
			})
		}
	}
}
