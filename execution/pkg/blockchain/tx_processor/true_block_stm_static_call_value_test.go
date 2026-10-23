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

// TestStaticCallCannotTransferValue checks EIP-214: a CALL with value > 0 inside a STATICCALL frame
// fails with a write-protection error, so STATICCALL returns 0 and no value moves.
func TestStaticCallCannotTransferValue(t *testing.T) {
	for _, static := range []bool{false, true} {
		name := "CALL_control"
		if static {
			name = "STATICCALL"
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
			wrap := func(runtime []byte) []byte {
				return append([]byte{0x60, byte(len(runtime)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}, runtime...)
			}
			recipient := common.HexToAddress("0x1234567890123456789012345678901234567890")
			// B attempts a CALL transferring 100 wei and returns its success flag.
			runtimeB := common.FromHex("6000600060006000606473")
			runtimeB = append(runtimeB, recipient.Bytes()...)
			runtimeB = append(runtimeB, common.FromHex("61fffff160005260206000f3")...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeB)).Status())
			b := crypto.CreateAddress(sender, 1)
			runtimeA := common.FromHex("6000600060006000")
			if !static {
				runtimeA = append(runtimeA, 0x60, 0)
			}
			runtimeA = append(runtimeA, 0x73)
			runtimeA = append(runtimeA, b.Bytes()...)
			runtimeA = append(runtimeA, 0x62, 1, 0x86, 0xa0)
			if static {
				runtimeA = append(runtimeA, 0xfa)
			} else {
				runtimeA = append(runtimeA, 0xf1)
			}
			runtimeA = append(runtimeA, common.FromHex("60005260206000f3")...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(2, nil, 0, wrap(runtimeA)).Status())
			a := crypto.CreateAddress(sender, 2)
			receipt := execute(3, &a, 0, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			bs, err := cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			rs, err := cs.GetAccountStateDB().AccountState(recipient)
			require.NoError(t, err)
			result := new(big.Int).SetBytes(receipt.Return()).Uint64()
			t.Logf("static=%v call_result=%d B_balance=%s recipient_balance=%s", static, result, bs.TotalBalance(), rs.TotalBalance())
			if static {
				require.Equal(t, "1000", bs.TotalBalance().String(), "STATICCALL must not transfer value")
				require.Zero(t, rs.TotalBalance().Sign())
				require.Zero(t, result)
			} else {
				require.Equal(t, "900", bs.TotalBalance().String())
				require.Equal(t, "100", rs.TotalBalance().String())
				require.Equal(t, uint64(1), result)
			}
		})
	}
}

// TestStaticCallCannotTStore checks EIP-1153: TSTORE inside a STATICCALL frame fails with a
// write-protection error, so STATICCALL returns 0.
func TestStaticCallCannotTStore(t *testing.T) {
	for _, static := range []bool{false, true} {
		name := "CALL_control"
		if static {
			name = "STATICCALL"
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
			wrap := func(runtime []byte) []byte {
				return append([]byte{0x60, byte(len(runtime)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}, runtime...)
			}
			// B executes TSTORE (key=1, value=42), then returns 1
			// PUSH1 42, PUSH1 1, TSTORE (0x5d), PUSH1 1, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
			runtimeB := common.FromHex("602a60015d600160005260206000f3")
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 0, wrap(runtimeB)).Status())
			b := crypto.CreateAddress(sender, 1)

			// A calls B via CALL or STATICCALL and returns the call success flag
			runtimeA := common.FromHex("6000600060006000")
			if !static {
				runtimeA = append(runtimeA, 0x60, 0)
			}
			runtimeA = append(runtimeA, 0x73)
			runtimeA = append(runtimeA, b.Bytes()...)
			runtimeA = append(runtimeA, 0x62, 1, 0x86, 0xa0)
			if static {
				runtimeA = append(runtimeA, 0xfa)
			} else {
				runtimeA = append(runtimeA, 0xf1)
			}
			runtimeA = append(runtimeA, common.FromHex("60005260206000f3")...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(2, nil, 0, wrap(runtimeA)).Status())
			a := crypto.CreateAddress(sender, 2)
			receipt := execute(3, &a, 0, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			result := new(big.Int).SetBytes(receipt.Return()).Uint64()
			t.Logf("static=%v TSTORE call_result=%d", static, result)
			if static {
				require.Equal(t, uint64(0), result, "TSTORE inside STATICCALL must fail with ErrWriteProtection")
			} else {
				require.Equal(t, uint64(1), result, "TSTORE inside normal CALL must succeed")
			}
		})
	}
}

