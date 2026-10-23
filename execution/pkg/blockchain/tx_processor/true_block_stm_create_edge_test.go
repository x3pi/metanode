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

// createFactoryRuntime builds a factory whose runtime code copies `child` (appended after the
// factory code) into memory and runs CREATE / CREATE2 with an endowment of 100 wei. With
// tailRevert the factory REVERTs right after the create instead of returning its result.
// The CODECOPY offset (0x16 for CREATE, 0x18 for CREATE2) is the length of the code before the tail.
func createFactoryRuntime(create2 bool, child []byte, tailRevert bool) []byte {
	l := byte(len(child))
	var code []byte
	if create2 {
		code = []byte{0x60, l, 0x60, 0x18, 0x60, 0, 0x39, 0x60, 0, 0x60, l, 0x60, 0, 0x60, 100, 0xf5}
	} else {
		code = []byte{0x60, l, 0x60, 0x16, 0x60, 0, 0x39, 0x60, l, 0x60, 0, 0x60, 100, 0xf0}
	}
	if tailRevert {
		code = append(code, 0x60, 0, 0x60, 0, 0xfd) // PUSH1 0 PUSH1 0 REVERT
		pad := 0x16 + 3
		if create2 {
			pad = 0x18 + 3
		}
		for len(code) < pad {
			code = append(code, 0x00)
		}
	} else {
		// PUSH1 0 MSTORE PUSH1 32 PUSH1 0 RETURN: hand the CREATE result back to the caller.
		code = append(code, 0x60, 0, 0x52, 0x60, 0x20, 0x60, 0, 0xf3)
	}
	return append(code, child...)
}

// TestCreateAndCreate2EdgeCases pins the CREATE / CREATE2 behaviour around failing constructors,
// reverting ancestors and pre-existing destination accounts (EIP-684 / EIP-1014 / EIP-161 balance rule).
func TestCreateAndCreate2EdgeCases(t *testing.T) {
	okChild := common.FromHex("0x600060005360016000f3") // returns the single runtime byte 0x00
	revertChild := common.FromHex("0x60006000fd")        // constructor REVERTs

	cases := []struct {
		name         string
		child        []byte
		tailRevert   bool
		prefund      int64
		prefundNonce uint64
		wantStatus   pb.RECEIPT_STATUS
		wantCreated  bool  // CREATE result is the child address (else 0)
		wantFactory  int64 // factory balance after the call
		wantChild    int64 // child balance after the call
		wantCode     bool  // child has code after the call
	}{
		// A reverting constructor fails only the CREATE: the parent keeps running and gets 0.
		{name: "ctor_REVERT", child: revertChild, wantStatus: pb.RECEIPT_STATUS_RETURNED, wantFactory: 1000},
		// A successful create undone by a reverting ancestor leaves nothing behind.
		{name: "ancestor_REVERT", child: okChild, tailRevert: true, wantStatus: pb.RECEIPT_STATUS_TRANSACTION_ERROR, wantFactory: 1000},
		// A destination that only holds a balance is allowed (EIP-684): the endowment is added to it.
		{name: "prefunded_balance_only", child: okChild, prefund: 50, wantStatus: pb.RECEIPT_STATUS_RETURNED, wantCreated: true, wantFactory: 900, wantChild: 150, wantCode: true},
		// A destination with a non-zero nonce is a collision: no endowment moves, balance untouched.
		{name: "prefunded_nonzero_nonce", child: okChild, prefund: 50, prefundNonce: 1, wantStatus: pb.RECEIPT_STATUS_RETURNED, wantFactory: 1000, wantChild: 50},
	}

	for _, create2 := range []bool{false, true} {
		opcode := "CREATE"
		if create2 {
			opcode = "CREATE2"
		}
		for _, tc := range cases {
			tc, create2 := tc, create2
			t.Run(opcode+"_"+tc.name, func(t *testing.T) {
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

				factory := crypto.CreateAddress(sender, 1)
				var child common.Address
				if create2 {
					child = crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(tc.child))
				} else {
					child = crypto.CreateAddress(factory, 0)
				}
				if tc.prefund > 0 {
					seedAccount(t, cs, child, big.NewInt(tc.prefund), tc.prefundNonce)
				}

				makeTx := func(nonce uint64, to *common.Address, value int64, data []byte) types.Transaction {
					signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
						Nonce: nonce, To: to, Value: big.NewInt(value), Gas: 300000,
						GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data,
					})
					require.NoError(t, err)
					tx, err := transaction.NewTransactionFromEth(signed)
					require.NoError(t, err)
					return tx
				}
				execute := func(tx types.Transaction) types.Receipt {
					items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
					groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
					_, receipts, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, groups, blankHeader(), false, false, 999, leader, false)
					require.Len(t, receipts, 1)
					return receipts[0]
				}

				runtime := createFactoryRuntime(create2, tc.child, tc.tailRevert)
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				init = append(init, runtime...)
				require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(makeTx(1, nil, 1000, init)).Status())

				call := execute(makeTx(2, &factory, 0, nil))
				require.Equal(t, tc.wantStatus, call.Status())

				fa, err := cs.GetAccountStateDB().AccountState(factory)
				require.NoError(t, err)
				ch, err := cs.GetAccountStateDB().AccountState(child)
				require.NoError(t, err)
				require.Equal(t, big.NewInt(tc.wantFactory).String(), fa.TotalBalance().String(), "factory balance")
				require.Equal(t, big.NewInt(tc.wantChild).String(), ch.TotalBalance().String(), "child balance")
				require.Equal(t, tc.wantCode, cs.HasCode(child), "child code")

				if tc.wantStatus == pb.RECEIPT_STATUS_RETURNED {
					result := new(big.Int).SetBytes(call.Return())
					if tc.wantCreated {
						require.Equal(t, new(big.Int).SetBytes(child.Bytes()), result)
					} else {
						require.Zero(t, result.Sign(), "failed create must return 0")
					}
				} else {
					// The tx failed as a whole: the factory nonce bump must not survive either.
					require.Zero(t, fa.Nonce(), "factory nonce after failed tx")
				}
			})
		}
	}
}

// TestReviewConstructorFallthrough kiểm tra:
// Constructor con chạy hết bytecode mà không có STOP/RETURN tường minh (fallthrough):
// 1. Máy ảo phải coi đó là implicit STOP, hoàn thành frame con với empty code.
// 2. Trả quyền điều khiển về cho factory để factory thực thi các opcode tiếp theo.
// 3. Nếu factory có REVERT ở phía sau, factory phải REVERT chính xác (Status = TRANSACTION_ERROR, nonce = 0).
func TestReviewConstructorFallthrough(t *testing.T) {
	for _, create2 := range []bool{false, true} {
		for _, explicitStop := range []bool{true, false} {
			name := "CREATE"
			if create2 {
				name = "CREATE2"
			}
			if explicitStop {
				name += "_STOP_control"
			} else {
				name += "_fallthrough"
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
						Nonce: nonce, To: to, Value: big.NewInt(value), Gas: 300000,
						GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE), Data: data,
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

				// Both constructors terminate successfully with empty runtime code.
				childInit := []byte{0x5b} // JUMPDEST, no explicit STOP or RETURN
				if explicitStop {
					childInit = []byte{0x00} // STOP
				}
				runtime := createFactoryRuntime(create2, childInit, false)
				// Revert the factory after CREATE, keeping the child offset unchanged.
				runtime[len(runtime)-len(childInit)-1] = 0xfd
				init := append([]byte{0x60, byte(len(runtime)), 0x60, 12, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}, runtime...)
				require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, init).Status())
				factory := crypto.CreateAddress(sender, 1)
				receipt := execute(2, &factory, 0, []byte{1})
				child := crypto.CreateAddress(factory, 0)
				if create2 {
					child = crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(childInit))
				}
				st, err := cs.GetAccountStateDB().AccountState(child)
				require.NoError(t, err)
				fa, err := cs.GetAccountStateDB().AccountState(factory)
				require.NoError(t, err)
				t.Logf("explicitStop=%v receipt_status=%v factory_nonce=%d factory_balance=%s child_balance=%s",
					explicitStop, receipt.Status(), fa.Nonce(), fa.TotalBalance(), st.TotalBalance())
				require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, receipt.Status(), "factory REVERT after child execution must run")
				require.Equal(t, "1000", fa.TotalBalance().String())
				require.Zero(t, fa.Nonce(), "factory revert must restore CREATE nonce")
			})
		}
	}
}

