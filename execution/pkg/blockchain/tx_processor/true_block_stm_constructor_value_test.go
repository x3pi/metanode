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

// TestConstructorCallValueStoragePersistence checks that:
// 1. The constructor reads CALLVALUE (msg.value) and stores it in storage slot 0 with SSTORE.
// 2. A later transaction calls the deployed child to SLOAD slot 0 and return it.
// 3. The returned value equals exactly the 100 wei endowment passed by CREATE / CREATE2.
func TestConstructorCallValueStoragePersistence(t *testing.T) {
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

			// childInit:
			// Constructor (16 bytes): CALLVALUE (34), PUSH1 0 (6000), SSTORE (55),
			//   PUSH1 11 (600b), PUSH1 16 (6010), PUSH1 0 (6000), CODECOPY (39),
			//   PUSH1 11 (600b), PUSH1 0 (6000), RETURN (f3)
			// Runtime (11 bytes): PUSH1 0 (6000), SLOAD (54), PUSH1 0 (6000), MSTORE (52),
			//   PUSH1 32 (6020), PUSH1 0 (6000), RETURN (f3)
			childInit := common.FromHex("0x34600055600b6010600039600b6000f360005460005260206000f3")
			require.Equal(t, 27, len(childInit))

			var runtimeFactory []byte
			if opcode == "CREATE" {
				// PUSH1 27, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 27, PUSH1 0, PUSH1 100, CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x601b6016600039601b60006064f060005260206000f3")
			} else {
				// CREATE2: PUSH1 27, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 27, PUSH1 0, PUSH1 100, CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x601b60186000396000601b60006064f560005260206000f3")
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

			// Call Factory to invoke CREATE / CREATE2 with 100 wei
			receiptCreate := execute(2, &factory, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receiptCreate.Status())
			createdAddr := common.BytesToAddress(receiptCreate.Return())
			require.Equal(t, expectedChild, createdAddr)

			// Tx 3: Call the newly created child contract directly; it should SLOAD slot 0 and return 100 wei
			receiptCallChild := execute(3, &createdAddr, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receiptCallChild.Status())

			storedValue := new(big.Int).SetBytes(receiptCallChild.Return())
			t.Logf("[%s storage] storedValue=%s expected=100", opcode, storedValue.String())
			require.Equal(t, big.NewInt(100), storedValue, "constructor must have persisted msg.value == 100 into storage slot 0")

			stFactory, err := cs.GetAccountStateDB().AccountState(factory)
			require.NoError(t, err)
			stChild, err := cs.GetAccountStateDB().AccountState(createdAddr)
			require.NoError(t, err)

			require.Equal(t, "900", stFactory.TotalBalance().String())
			require.Equal(t, "100", stChild.TotalBalance().String())
		})
	}
}

// TestConstructorCallValueZeroEndowment checks that when a factory runs CREATE or CREATE2 with
// endowment == 0, the constructor sees msg.value == 0 (no stack garbage, ISZERO returns 1).
func TestConstructorCallValueZeroEndowment(t *testing.T) {
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

			// childInit:
			// CALLVALUE (34), ISZERO (15), PUSH1 10 (600a), JUMPI (57)
			// PUSH1 0 (6000), PUSH1 0 (6000), REVERT (fd)
			// JUMPDEST (5b), PUSH1 0 (6000), PUSH1 0 (6000), MSTORE8 (53)
			// PUSH1 1 (6001), PUSH1 0 (6000), RETURN (f3)
			childInit := common.FromHex("0x3415600a5760006000fd5b600060005360016000f3")

			var runtimeFactory []byte
			if opcode == "CREATE" {
				// PUSH1 21, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 21, PUSH1 0, PUSH1 0 (value=0), CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x60156016600039601560006000f060005260206000f3")
			} else {
				// CREATE2: PUSH1 21, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 21, PUSH1 0, PUSH1 0 (value=0), CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x601560186000396000601560006000f560005260206000f3")
			}
			runtimeFactory = append(runtimeFactory, childInit...)

			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeFactory)).Status())
			factory := crypto.CreateAddress(sender, 1)

			var expectedChild common.Address
			if opcode == "CREATE" {
				expectedChild = crypto.CreateAddress(factory, 0)
			} else {
				expectedChild = crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(childInit))
			}

			receipt := execute(2, &factory, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			createdAddr := common.BytesToAddress(receipt.Return())

			require.Equal(t, expectedChild, createdAddr, "constructor must see msg.value == 0 when value is 0")
			require.True(t, cs.HasCode(expectedChild))
		})
	}
}

// TestConstructorCallValueMismatchRevertsAndRefunds checks that when the factory sends 50 wei but
// the constructor requires msg.value == 100 wei:
// 1. The constructor detects the wrong value and REVERTs.
// 2. CREATE / CREATE2 returns 0.
// 3. The 50 wei are fully refunded to the factory (nothing is lost).
// 4. The child has no code and a zero balance.
func TestConstructorCallValueMismatchRevertsAndRefunds(t *testing.T) {
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

			// Child constructor requires msg.value == 100 (0x64), reverts if mismatch
			childInit := common.FromHex("0x34606414600c5760006000fd5b600060005360016000f3") // 23 bytes

			var runtimeFactory []byte
			if opcode == "CREATE" {
				// Factory sends value = 50 (0x32):
				// PUSH1 23, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 23, PUSH1 0, PUSH1 50 (0x32), CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x60176016600039601760006032f060005260206000f3")
			} else {
				// CREATE2 sends value = 50 (0x32):
				// PUSH1 23, PUSH1 24, PUSH1 0, CODECOPY, PUSH1 0 (salt), PUSH1 23, PUSH1 0, PUSH1 50 (0x32), CREATE2, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
				runtimeFactory = common.FromHex("0x601760186000396000601760006032f560005260206000f3")
			}
			runtimeFactory = append(runtimeFactory, childInit...)

			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeFactory)).Status())
			factory := crypto.CreateAddress(sender, 1)

			var expectedChild common.Address
			if opcode == "CREATE" {
				expectedChild = crypto.CreateAddress(factory, 0)
			} else {
				expectedChild = crypto.CreateAddress2(factory, [32]byte{}, crypto.Keccak256(childInit))
			}

			receipt := execute(2, &factory, 0, nil)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
			createdRet := new(big.Int).SetBytes(receipt.Return())

			// 1. CREATE / CREATE2 returns 0
			require.Zero(t, createdRet.Sign(), "%s must return 0 on constructor revert", opcode)

			stFactory, err := cs.GetAccountStateDB().AccountState(factory)
			require.NoError(t, err)
			stChild, err := cs.GetAccountStateDB().AccountState(expectedChild)
			require.NoError(t, err)

			t.Logf("[%s mismatch] createRet=%s factory_balance=%s child_balance=%s hasCode=%v",
				opcode, createdRet.String(), stFactory.TotalBalance(), stChild.TotalBalance(), cs.HasCode(expectedChild))

			// 2. Factory's 50 wei endowment is 100% refunded (balance stays 1000)
			require.Equal(t, "1000", stFactory.TotalBalance().String(), "factory balance must remain 1000")
			// 3. Child has 0 balance and no code
			require.Zero(t, stChild.TotalBalance().Sign())
			require.False(t, cs.HasCode(expectedChild))
		})
	}
}

// TestConstructorForwardsEndowmentToBeneficiary checks a constructor that receives 100 wei from CREATE
// and, while running, executes CALL to forward 40 wei to a beneficiary. Afterwards:
// - Factory: 900 wei (1000 - 100).
// - Beneficiary: 40 wei.
// - Child contract: 60 wei (100 - 40).
func TestConstructorForwardsEndowmentToBeneficiary(t *testing.T) {
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

	beneficiary := common.HexToAddress("0xbbbb000000000000000000000000000000000001")
	seedAccount(t, cs, beneficiary, big.NewInt(0), 0)

	execute := func(nonce uint64, to *common.Address, value int64, data []byte) types.Receipt {
		signed, err := eth.SignNewTx(key, eth.NewEIP155Signer(big.NewInt(991)), &eth.LegacyTx{
			Nonce:    nonce,
			To:       to,
			Value:    big.NewInt(value),
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

	// Child constructor:
	// CALL(gas=100000, to=beneficiary, value=40, in=0, insize=0, out=0, outsize=0):
	// PUSH1 0, PUSH1 0, PUSH1 0, PUSH1 0, PUSH1 40 (0x28), PUSH20 <beneficiary>, PUSH3 0x0186a0, CALL, POP
	// PUSH1 0, PUSH1 0, MSTORE8, PUSH1 1, PUSH1 0, RETURN (deploy 1 byte 0x00)
	childInit := common.FromHex("0x6000600060006000602873")
	childInit = append(childInit, beneficiary.Bytes()...)
	childInit = append(childInit, common.FromHex("0x620186a0f150600060005360016000f3")...)
	childLen := byte(len(childInit)) // 48 bytes (0x30)

	// Factory:
	// PUSH1 childLen, PUSH1 22, PUSH1 0, CODECOPY, PUSH1 childLen, PUSH1 0, PUSH1 100, CREATE, PUSH1 0, MSTORE, PUSH1 32, PUSH1 0, RETURN
	runtimeFactory := []byte{
		0x60, childLen, 0x60, 0x16, 0x60, 0x00, 0x39,
		0x60, childLen, 0x60, 0x00, 0x60, 0x64, 0xf0,
		0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3,
	}
	runtimeFactory = append(runtimeFactory, childInit...)

	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execute(1, nil, 1000, wrap(runtimeFactory)).Status())
	factory := crypto.CreateAddress(sender, 1)

	receipt := execute(2, &factory, 0, nil)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipt.Status())
	childAddr := common.BytesToAddress(receipt.Return())
	require.NotEqual(t, common.Address{}, childAddr)

	stFactory, err := cs.GetAccountStateDB().AccountState(factory)
	require.NoError(t, err)
	stChild, err := cs.GetAccountStateDB().AccountState(childAddr)
	require.NoError(t, err)
	stBeneficiary, err := cs.GetAccountStateDB().AccountState(beneficiary)
	require.NoError(t, err)

	t.Logf("Balances: Factory=%s Child=%s Beneficiary=%s",
		stFactory.TotalBalance(), stChild.TotalBalance(), stBeneficiary.TotalBalance())

	require.Equal(t, "900", stFactory.TotalBalance().String(), "factory balance must be 900 (1000 - 100)")
	require.Equal(t, "60", stChild.TotalBalance().String(), "child balance must be 60 (100 - 40)")
	require.Equal(t, "40", stBeneficiary.TotalBalance().String(), "beneficiary balance must be 40")
	require.True(t, cs.HasCode(childAddr), "child contract must be successfully deployed")
}
