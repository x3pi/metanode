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
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/require"
)

// TestCreateAndCreate2RejectedCodeRefundsEndowment covers both CREATE and CREATE2.
// When the child returns runtime code starting with 0xEF (rejected by EIP-3541):
// 1. CREATE / CREATE2 must return 0 (failure).
// 2. The whole endowment must be refunded to the parent contract.
// 3. The child address must not keep any balance.
// A second CREATE2 to an address that already holds a contract (EIP-684/1014) must also fail
// without moving the endowment.
func TestCreateAndCreate2RejectedCodeRefundsEndowment(t *testing.T) {
	for _, opcode := range []string{"CREATE", "CREATE2"} {
		for _, rejected := range []bool{false, true} {
			name := opcode + "_valid_RETURN"
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

// TestNestedRevertRestoresCreatorNonceCREATE checks that the creator's nonce is rolled back when its frame reverts.
// Contract A calls B and B runs CREATE, which bumps B's nonce (0 -> 1). If B then REVERTs:
// 1. B's whole frame is rolled back, including its nonce (back to 0).
// 2. A catches the failure and the transaction completes successfully.
func TestNestedRevertRestoresCreatorNonceCREATE(t *testing.T) {
	for _, revert := range []bool{false, true} {
		name := "success_control"
		if revert {
			name = "caught_revert"
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
			execute := func(nonce uint64, to *common.Address, data []byte) types.Receipt {
				signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
					Nonce:    nonce,
					To:       to,
					Value:    big.NewInt(0),
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
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				return append(init, runtime...)
			}
			// B creates an empty-runtime child, then reverts (or returns for control).
			runtimeB := common.FromHex("0x60056014600039600560006000f05060006000fd60006000f3")
			if !revert {
				runtimeB[19] = 0xf3
			}
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, wrap(runtimeB)).Status())
			b := crypto.CreateAddress(sender, 1)
			stB, err := cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			before := stB.Nonce()
			// A calls B and returns CALL's boolean result, handling failure normally.
			runtimeA := common.FromHex("0x6000600060006000600073")
			runtimeA = append(runtimeA, b.Bytes()...)
			runtimeA = append(runtimeA, common.FromHex("0x620186a0f160005260206000f3")...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(2, nil, wrap(runtimeA)).Status())
			a := crypto.CreateAddress(sender, 2)
			receipt := execute(3, &a, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			result := new(big.Int).SetBytes(receipt.Return()).Uint64()
			stB, err = cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			t.Logf("revert=%v CALL_result=%d B_nonce_before=%d B_nonce_after=%d", revert, result, before, stB.Nonce())
			if revert {
				require.Zero(t, result, "A must catch B's revert")
				require.Equal(t, before, stB.Nonce(), "caught nested revert must restore B's CREATE nonce")
			} else {
				require.Equal(t, uint64(1), result)
				require.Equal(t, before+1, stB.Nonce())
			}
		})
	}
}

// TestNestedRevertRestoresCreatorNonceCREATE2 is the CREATE2 variant of the test above.
// Contract A calls B and B runs CREATE2, which bumps B's nonce (0 -> 1). If B REVERTs:
// 1. B's whole frame is rolled back, including its nonce (back to 0).
// 2. The child created by CREATE2 is gone from state (no code).
// 3. A catches the failure from CALL and the transaction completes normally.
// If B succeeds (success_control):
// 1. B keeps the nonce bump (0 -> 1).
// 2. The child exists and has code.
func TestNestedRevertRestoresCreatorNonceCREATE2(t *testing.T) {
	for _, revert := range []bool{false, true} {
		name := "success_control"
		if revert {
			name = "caught_revert"
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
			execute := func(nonce uint64, to *common.Address, data []byte) types.Receipt {
				signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
					Nonce:    nonce,
					To:       to,
					Value:    big.NewInt(0),
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
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				return append(init, runtime...)
			}

			// childInit: returns 1 byte (0x00) -> 10 bytes: 600060005360016000f3
			childInit := common.FromHex("0x600060005360016000f3")

			// B creates child using CREATE2 (salt=0), then either reverts (0xfd) or returns (0xf3).
			// PUSH1 10, PUSH1 22, PUSH1 0, CODECOPY
			// PUSH1 0 (salt), PUSH1 10 (size), PUSH1 0 (offset), PUSH1 0 (value), CREATE2, POP
			// PUSH1 0, PUSH1 0, REVERT/RETURN
			runtimeB := common.FromHex("0x600a60166000396000600a60006000f55060006000fd")
			runtimeB = append(runtimeB, childInit...)
			if !revert {
				runtimeB[21] = 0xf3
			}

			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, wrap(runtimeB)).Status())
			b := crypto.CreateAddress(sender, 1)
			stB, err := cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			before := stB.Nonce()

			child := crypto.CreateAddress2(b, [32]byte{}, crypto.Keccak256(childInit))

			// A calls B and returns CALL's boolean result (1 on success, 0 on revert)
			runtimeA := common.FromHex("0x6000600060006000600073")
			runtimeA = append(runtimeA, b.Bytes()...)
			runtimeA = append(runtimeA, common.FromHex("0x620186a0f160005260206000f3")...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(2, nil, wrap(runtimeA)).Status())
			a := crypto.CreateAddress(sender, 2)

			receipt := execute(3, &a, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			result := new(big.Int).SetBytes(receipt.Return()).Uint64()

			stB, err = cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			t.Logf("[CREATE2] revert=%v CALL_result=%d B_nonce_before=%d B_nonce_after=%d child_hasCode=%v",
				revert, result, before, stB.Nonce(), cs.HasCode(child))

			if revert {
				require.Zero(t, result, "A must catch B's revert")
				require.Equal(t, before, stB.Nonce(), "caught nested revert must restore B's CREATE2 nonce")
				require.False(t, cs.HasCode(child), "reverted CREATE2 must not persist child contract code")
			} else {
				require.Equal(t, uint64(1), result)
				require.Equal(t, before+1, stB.Nonce(), "successful CREATE2 must increment B's nonce")
				require.True(t, cs.HasCode(child), "successful CREATE2 must persist child contract code")
			}
		})
	}
}

// TestChildConstructorRevertKeepsCreatorNonceIncrement is the EIP-161 control case.
// When B runs CREATE or CREATE2 and the CHILD constructor REVERTs (0xFD):
// 1. CREATE / CREATE2 returns 0 (failure).
// 2. The whole endowment is refunded to B.
// 3. The child has balance 0 and no code.
// 4. B's nonce STILL goes up (0 -> 1) because B's own frame is not reverted.
func TestChildConstructorRevertKeepsCreatorNonceIncrement(t *testing.T) {
	for _, opcode := range []string{"CREATE", "CREATE2"} {
		t.Run(opcode, func(t *testing.T) {
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
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				return append(init, runtime...)
			}

			// childInit: constructor explicitly reverts: PUSH1 0, PUSH1 0, REVERT (5 bytes)
			childInit := common.FromHex("0x60006000fd")

			var runtimeB []byte
			if opcode == "CREATE" {
				// PUSH1 5, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 5, PUSH1 0, PUSH1 100, CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeB = common.FromHex("0x60056016600039600560006064f060005260206000f3")
			} else {
				// CREATE2: PUSH1 5, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 5, PUSH1 0, PUSH1 100, CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeB = common.FromHex("0x600560186000396000600560006064f560005260206000f3")
			}
			runtimeB = append(runtimeB, childInit...)

			// Deploy B with initial balance = 1000
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeB)).Status())
			b := crypto.CreateAddress(sender, 1)

			stBBefore, err := cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			nonceBefore := stBBefore.Nonce()
			require.Equal(t, uint64(0), nonceBefore)
			require.Equal(t, "1000", stBBefore.TotalBalance().String())

			var child common.Address
			if opcode == "CREATE" {
				child = crypto.CreateAddress(b, nonceBefore)
			} else {
				child = crypto.CreateAddress2(b, [32]byte{}, crypto.Keccak256(childInit))
			}

			// Call B to invoke creation. B does not revert, so transaction returns normally.
			receipt := execute(2, &b, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())

			createRet := new(big.Int).SetBytes(receipt.Return())
			stBAfter, err := cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			stChild, err := cs.GetAccountStateDB().AccountState(child)
			require.NoError(t, err)

			t.Logf("[%s child revert] createRet=%s B_nonce=%d B_balance=%s child_balance=%s hasCode=%v",
				opcode, createRet, stBAfter.Nonce(), stBAfter.TotalBalance(), stChild.TotalBalance(), cs.HasCode(child))

			// 1. CREATE/CREATE2 returned address 0
			require.Zero(t, createRet.Sign(), "%s must return 0 on child constructor revert", opcode)
			// 2. Creator B's nonce MUST remain incremented (0 -> 1) per EIP-161
			require.Equal(t, nonceBefore+1, stBAfter.Nonce(), "EIP-161: %s creator nonce must remain incremented even if child reverts", opcode)
			// 3. Endowment 100 refunded to B
			require.Equal(t, "1000", stBAfter.TotalBalance().String(), "%s failed child must refund endowment to B", opcode)
			// 4. Child receives 0 and has no code
			require.Zero(t, stChild.TotalBalance().Sign(), "child must receive no endowment")
			require.False(t, cs.HasCode(child), "child code must not exist")
		})
	}
}

// TestDeeplyNestedRevertRestoresAllAncestorsNonces checks the rollback across several nested frames.
// Call chain: Transaction -> A -> B -> C -> CREATE (childC); B also runs CREATE for childB (B nonce 0 -> 1)
// and C runs CREATE for childC (C nonce 0 -> 1). When B REVERTs:
// 1. A catches the failure of CALL(B) and returns normally (status = RETURNED).
// 2. Everything done by B and below is rolled back:
//    - B's nonce is back to 0, C's nonce is back to 0;
//    - childB and childC have no code.
// When B succeeds (success_control): B nonce = 1 with childB present, C nonce = 1 with childC present.
func TestDeeplyNestedRevertRestoresAllAncestorsNonces(t *testing.T) {
	for _, revert := range []bool{false, true} {
		name := "success_control"
		if revert {
			name = "caught_revert"
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

			execute := func(nonce uint64, to *common.Address, data []byte) types.Receipt {
				signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
					Nonce:    nonce,
					To:       to,
					Value:    big.NewInt(0),
					Gas:      400000,
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
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				return append(init, runtime...)
			}

			// childInit: returns 1 byte (0x00) -> 10 bytes: 600060005360016000f3
			childInit := common.FromHex("0x600060005360016000f3")

			// Deploy C:
			// PUSH1 10, PUSH1 20, PUSH1 0, CODECOPY, PUSH1 10, PUSH1 0, PUSH1 0, CREATE, POP, PUSH1 0, PUSH1 0, RETURN
			// 600a 6014 6000 39 600a 6000 6000 f0 50 6000 6000 f3 (20 bytes) + childInit (10 bytes)
			runtimeC := common.FromHex("0x600a6014600039600a60006000f05060006000f3")
			runtimeC = append(runtimeC, childInit...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, wrap(runtimeC)).Status())
			c := crypto.CreateAddress(sender, 1)

			// Deploy B:
			// 1. PUSH1 10, PUSH1 57, PUSH1 0, CODECOPY, PUSH1 10, PUSH1 0, PUSH1 0, CREATE, POP (15 bytes)
			// 2. PUSH1 0, PUSH1 0, PUSH1 0, PUSH1 0, PUSH1 0, PUSH20 <c>, PUSH3 0x0186a0, CALL, POP (37 bytes, total 52)
			// 3. PUSH1 0, PUSH1 0, REVERT (5 bytes, total 57)
			// 4. childInit (10 bytes)
			runtimeB := common.FromHex("0x600a6039600039600a60006000f0506000600060006000600073")
			runtimeB = append(runtimeB, c.Bytes()...)
			runtimeB = append(runtimeB, common.FromHex("0x620186a0f15060006000fd")...)
			runtimeB = append(runtimeB, childInit...)
			if !revert {
				// If not revert, change 0xfd (REVERT) at index 56 to 0xf3 (RETURN)
				runtimeB[56] = 0xf3
			}
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(2, nil, wrap(runtimeB)).Status())
			b := crypto.CreateAddress(sender, 2)

			// Deploy A: calls B and returns CALL's status (1 on success, 0 on revert)
			runtimeA := common.FromHex("0x6000600060006000600073")
			runtimeA = append(runtimeA, b.Bytes()...)
			runtimeA = append(runtimeA, common.FromHex("0x620186a0f160005260206000f3")...)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(3, nil, wrap(runtimeA)).Status())
			a := crypto.CreateAddress(sender, 3)

			childB := crypto.CreateAddress(b, 0)
			childC := crypto.CreateAddress(c, 0)

			receipt := execute(4, &a, []byte{1})
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			result := new(big.Int).SetBytes(receipt.Return()).Uint64()

			stB, err := cs.GetAccountStateDB().AccountState(b)
			require.NoError(t, err)
			stC, err := cs.GetAccountStateDB().AccountState(c)
			require.NoError(t, err)

			t.Logf("[DeeplyNested] revert=%v CALL_result=%d B_nonce=%d C_nonce=%d hasChildB=%v hasChildC=%v",
				revert, result, stB.Nonce(), stC.Nonce(), cs.HasCode(childB), cs.HasCode(childC))

			if revert {
				require.Zero(t, result, "A must catch B's revert")
				require.Equal(t, uint64(0), stB.Nonce(), "B's nonce must rollback to 0")
				require.Equal(t, uint64(0), stC.Nonce(), "C's nonce must cascade-rollback to 0")
				require.False(t, cs.HasCode(childB), "childB must not exist")
				require.False(t, cs.HasCode(childC), "childC must not exist")
			} else {
				require.Equal(t, uint64(1), result)
				require.Equal(t, uint64(1), stB.Nonce(), "B's nonce must be 1")
				require.Equal(t, uint64(1), stC.Nonce(), "C's nonce must be 1")
				require.True(t, cs.HasCode(childB), "childB must exist")
				require.True(t, cs.HasCode(childC), "childC must exist")
			}
		})
	}
}

// TestCreate2AddressCollisionRejected checks the EIP-684 / EIP-1014 address collision rule.
// CREATE2 must fail (return 0) and move no endowment when the destination
// 1. already has nonce > 0, or
// 2. already has non-empty code.
func TestCreate2AddressCollisionRejected(t *testing.T) {
	for _, scenario := range []string{"collision_existing_nonce", "collision_existing_code"} {
		t.Run(scenario, func(t *testing.T) {
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
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				return append(init, runtime...)
			}

			childInit := common.FromHex("0x600060005360016000f3") // 10 bytes

			// Factory code:
			// PUSH1 10, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 10, PUSH1 0, PUSH1 200, CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
			// 600a 6018 6000 39 6000 600a 6000 60c8 f5 6000 52 6020 6000 f3 (24 bytes) + childInit (10 bytes)
			runtimeFactory := common.FromHex("0x600a60186000396000600a600060c8f560005260206000f3")
			runtimeFactory = append(runtimeFactory, childInit...)

			// Deploy Factory with balance 1000
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeFactory)).Status())
			factory := crypto.CreateAddress(sender, 1)

			target := crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(childInit))

			if scenario == "collision_existing_nonce" {
				// Target already has nonce = 1, balance = 500
				seedAccount(t, cs, target, big.NewInt(500), 1)
			} else {
				// Target already has deployed code, balance = 500
				as := mt_state.NewAccountState(target)
				as.AddBalance(big.NewInt(500))
				codeBytes := []byte{0x60, 0x00}
				codeHash := common.BytesToHash(crypto.Keccak256(codeBytes))
				as.SetCodeHash(codeHash)
				cs.GetAccountStateDB().SetState(as)
				cs.GetSmartContractDB().SetCode(target, codeHash, codeBytes)
				cs.GetSmartContractDB().Commit()
			}

			// Factory attempts CREATE2 to target with value = 200
			receipt := execute(2, &factory, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			result := new(big.Int).SetBytes(receipt.Return())

			stFactory, err := cs.GetAccountStateDB().AccountState(factory)
			require.NoError(t, err)
			stTarget, err := cs.GetAccountStateDB().AccountState(target)
			require.NoError(t, err)

			t.Logf("[%s] CREATE2_result=%s factory_balance=%s target_balance=%s",
				scenario, result, stFactory.TotalBalance(), stTarget.TotalBalance())

			// 1. CREATE2 must return 0 (collision rejected)
			require.Zero(t, result.Sign(), "CREATE2 must return 0 on collision")
			// 2. Factory balance must remain 1000 (200 was not transferred)
			require.Equal(t, "1000", stFactory.TotalBalance().String(), "factory balance must not be transferred")
			// 3. Target balance must remain 500 (did not receive the 200)
			require.Equal(t, "500", stTarget.TotalBalance().String(), "target balance must not be increased")
			// 4. Factory nonce incremented per EIP-161
			require.Equal(t, uint64(1), stFactory.Nonce(), "factory nonce must be incremented per EIP-161")
		})
	}
}

// TestCreateAndCreate2ConstructorReceivesCallValue kiểm tra chuẩn EVM:
// Khi factory gọi CREATE hoặc CREATE2 kèm theo endowment (100 wei):
// Constructor của contract con phải nhận được chính xác msg.value == 100 wei qua opcode CALLVALUE (0x34).
// Nếu CALLVALUE bị truyền là 0 (lỗi trước khi fix), constructor sẽ REVERT.
func TestCreateAndCreate2ConstructorReceivesCallValue(t *testing.T) {
	for _, opcode := range []string{"CREATE", "CREATE2"} {
		t.Run(opcode, func(t *testing.T) {
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
				init := []byte{0x60, byte(len(runtime)), 0x60, 0x0c, 0x60, 0, 0x39, 0x60, byte(len(runtime)), 0x60, 0, 0xf3}
				return append(init, runtime...)
			}

			// childInit (23 bytes):
			// CALLVALUE (34), PUSH1 100 (6064), EQ (14), PUSH1 12 (600c), JUMPI (57)
			// PUSH1 0 (6000), PUSH1 0 (6000), REVERT (fd)
			// JUMPDEST (5b), PUSH1 0 (6000), PUSH1 0 (6000), MSTORE8 (53)
			// PUSH1 1 (6001), PUSH1 0 (6000), RETURN (f3)
			childInit := common.FromHex("0x34606414600c5760006000fd5b600060005360016000f3")

			var runtimeFactory []byte
			if opcode == "CREATE" {
				// PUSH1 23, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 23, PUSH1 0, PUSH1 100, CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x60176016600039601760006064f060005260206000f3")
			} else {
				// CREATE2: PUSH1 23, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 23, PUSH1 0, PUSH1 100, CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x601760186000396000601760006064f560005260206000f3")
			}
			runtimeFactory = append(runtimeFactory, childInit...)

			// Deploy Factory with balance 1000
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeFactory)).Status())
			factory := crypto.CreateAddress(sender, 1)

			var expectedChild common.Address
			if opcode == "CREATE" {
				expectedChild = crypto.CreateAddress(factory, 0)
			} else {
				expectedChild = crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(childInit))
			}

			// Call Factory to invoke creation with 100 wei endowment
			receipt := execute(2, &factory, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			createdAddr := common.BytesToAddress(receipt.Return())

			stFactory, err := cs.GetAccountStateDB().AccountState(factory)
			require.NoError(t, err)
			stChild, err := cs.GetAccountStateDB().AccountState(expectedChild)
			require.NoError(t, err)

			t.Logf("[%s callvalue] createdAddr=%s expectedChild=%s factory_balance=%s child_balance=%s hasCode=%v",
				opcode, createdAddr.Hex(), expectedChild.Hex(), stFactory.TotalBalance(), stChild.TotalBalance(), cs.HasCode(expectedChild))

			// 1. Constructor must NOT revert (meaning CALLVALUE == 100 matched!)
			require.Equal(t, expectedChild, createdAddr, "constructor must see msg.value == 100 and succeed")
			// 2. Child has code
			require.True(t, cs.HasCode(expectedChild), "child contract must be deployed")
			// 3. Balances are correctly transferred
			require.Equal(t, "900", stFactory.TotalBalance().String(), "factory balance must be 900")
			require.Equal(t, "100", stChild.TotalBalance().String(), "child balance must be 100")
		})
	}
}


