package tx_processor

import (
	"context"
	"crypto/ecdsa"
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
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/require"
)

func seedContract(t *testing.T, cs *blockchain.ChainState, addr common.Address, balance *big.Int, code []byte) {
	t.Helper()
	seedAccount(t, cs, addr, balance, 1)
	codeHash := crypto.Keccak256Hash(code)
	as, err := cs.GetAccountStateDB().AccountState(addr)
	require.NoError(t, err)
	as.SetCodeHash(codeHash)
	cs.GetAccountStateDB().SetState(as)
	cs.GetSmartContractDB().SetCode(addr, codeHash, code)
	cs.GetSmartContractDB().Commit()
}

// TestEIP7702_DelegationChainMustNotActAsEmptyCode tests edge cases of EIP-7702 delegation resolution:
// 1. single_hop_control: A -> C (C is an external contract). Single hop succeeds.
// 2. chained_delegation_2_hops: A -> B -> C. EVM halts exceptionally on B's 0xef designator.
// 3. chained_delegation_3_hops: A -> B -> C -> D. Must halt exceptionally, rollback value.
// 4. self_delegation: A -> A. Calling A resolves to A's designator, halts exceptionally.
// 5. circular_delegation_loop: A -> B and B -> A. Calling A loads B's designator, halts without infinite loop.
// 6. delegation_to_eoa_empty: A -> EOA (where EOA has empty code). Acts as empty code, value transfer succeeds.
func TestEIP7702_DelegationChainMustNotActAsEmptyCode(t *testing.T) {
	testCases := []struct {
		name        string
		setupAuth   func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address)
		shouldFail  bool
		expectValue uint64
	}{
		{
			name: "single_hop_control",
			setupAuth: func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address) {
				return []eth.SetCodeAuthorization{
					signAuthorization(t, ka, chainID, c, 0),
				}, a
			},
			shouldFail:  false,
			expectValue: 100,
		},
		{
			name: "chained_delegation_2_hops",
			setupAuth: func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address) {
				return []eth.SetCodeAuthorization{
					signAuthorization(t, ka, chainID, b, 0),
					signAuthorization(t, kb, chainID, c, 0),
				}, a
			},
			shouldFail:  true,
			expectValue: 0,
		},
		{
			name: "chained_delegation_3_hops",
			setupAuth: func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address) {
				return []eth.SetCodeAuthorization{
					signAuthorization(t, ka, chainID, b, 0),
					signAuthorization(t, kb, chainID, c, 0),
					signAuthorization(t, kc, chainID, d, 0),
				}, a
			},
			shouldFail:  true,
			expectValue: 0,
		},
		{
			name: "self_delegation",
			setupAuth: func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address) {
				return []eth.SetCodeAuthorization{
					signAuthorization(t, ka, chainID, a, 0),
				}, a
			},
			shouldFail:  true,
			expectValue: 0,
		},
		{
			name: "circular_delegation_loop",
			setupAuth: func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address) {
				return []eth.SetCodeAuthorization{
					signAuthorization(t, ka, chainID, b, 0),
					signAuthorization(t, kb, chainID, a, 0),
				}, a
			},
			shouldFail:  true,
			expectValue: 0,
		},
		{
			name: "delegation_to_eoa_empty",
			setupAuth: func(t *testing.T, ka, kb, kc *ecdsa.PrivateKey, a, b, c, d common.Address, chainID uint64) ([]eth.SetCodeAuthorization, common.Address) {
				return []eth.SetCodeAuthorization{
					signAuthorization(t, ka, chainID, d, 0),
				}, a
			},
			shouldFail:  false,
			expectValue: 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
			t.Setenv("METANODE_DEVNET", "false")
			cs := newTestChainState(t)
			chainID := big.NewInt(991)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

			keySender, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyA, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyB, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyC, err := crypto.GenerateKey()
			require.NoError(t, err)

			sender := crypto.PubkeyToAddress(keySender.PublicKey)
			addrA := crypto.PubkeyToAddress(keyA.PublicKey)
			addrB := crypto.PubkeyToAddress(keyB.PublicKey)
			addrC := crypto.PubkeyToAddress(keyC.PublicKey)
			addrD := common.HexToAddress("0xdddd4444dddd4444dddd4444dddd4444dddd4444")
			leader := common.HexToAddress(auditLeader)

			seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			seedAccount(t, cs, addrA, big.NewInt(0), 0)
			seedAccount(t, cs, addrB, big.NewInt(0), 0)
			seedAccount(t, cs, addrC, big.NewInt(0), 0)
			seedAccount(t, cs, addrD, big.NewInt(0), 0)

			authList, targetCall := tc.setupAuth(t, keyA, keyB, keyC, addrA, addrB, addrC, addrD, 991)

			signer := eth.LatestSignerForChainID(chainID)
			signed, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
				ChainID:   uint256.NewInt(991),
				Nonce:     1,
				To:        targetCall,
				Value:     uint256.NewInt(100),
				Gas:       200000,
				GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				Data:      []byte{1},
				AuthList:  authList,
			})
			require.NoError(t, err)
			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)

			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))

			items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
			require.Len(t, r, 1)

			// Reference execution in go-ethereum EVM
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			random := common.Hash{}
			chainConfig := *params.AllDevChainProtocolChanges
			chainConfig.ChainID = chainID
			evm := vm.NewEVM(vm.BlockContext{
				CanTransfer: core.CanTransfer,
				Transfer:    core.Transfer,
				GetHash:     func(uint64) common.Hash { return common.Hash{} },
				GasLimit:    1000000,
				BlockNumber: big.NewInt(1),
				Time:        1,
				Difficulty:  big.NewInt(0),
				BaseFee:     big.NewInt(0),
				Random:      &random,
			}, reference, &chainConfig, vm.Config{})

			msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
			require.NoError(t, err)
			g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
			require.NoError(t, err)

			actualA, err := cs.GetAccountStateDB().AccountState(addrA)
			require.NoError(t, err)

			t.Logf("[%s] geth_gas=%d MVM_gas=%d delta=%d geth_error=%v geth_A_balance=%s MVM_status=%v MVM_A_balance=%s",
				tc.name, g.UsedGas, r[0].GasUsed(), int64(r[0].GasUsed())-int64(g.UsedGas), g.Err, reference.GetBalance(addrA), r[0].Status(), actualA.TotalBalance())

			if tc.shouldFail {
				require.Error(t, g.Err, "Geth must report execution error for %s", tc.name)
				require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r[0].Status(), "MVM must fail for %s", tc.name)
				require.Equal(t, "0", actualA.TotalBalance().String(), "Balance of A must remain 0 on failure")
			} else {
				require.NoError(t, g.Err, "Geth must succeed for %s", tc.name)
				require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status(), "MVM must succeed for %s", tc.name)
				require.Equal(t, "100", actualA.TotalBalance().String(), "A must receive 100 wei on success")
			}

			// Invariant: Balance of A in Geth must EXACTLY match Balance of A in MVM
			require.Equal(t, reference.GetBalance(addrA).String(), actualA.TotalBalance().String(),
				"MVM state must match Geth state exactly")
		})
	}
}

// TestReviewDelegation_ContractSubcallChainedDelegationRollback tests that when a contract
// invokes CALL to an account with chained delegation:
// 1. The CALL opcode fails (returns 0 on stack).
// 2. The 100 wei sent with CALL is NOT transferred to the target; it is refunded to the caller contract.
// 3. The caller contract does not revert (handles status 0 and stores it in slot 0).
// 4. MVM and go-ethereum EVM match exactly.
func TestEIP7702_ContractSubcallChainedDelegationRollback(t *testing.T) {
	testCases := []struct {
		name          string
		chained       bool
		expectCallRet uint64 // 0 for failure, 1 for success
		expectABal    uint64 // 0 if call failed, 100 if call succeeded
	}{
		{
			name:          "chained_delegation_subcall_reverts_and_refunds",
			chained:       true,
			expectCallRet: 0,
			expectABal:    0,
		},
		{
			name:          "single_hop_eoa_subcall_succeeds",
			chained:       false,
			expectCallRet: 1,
			expectABal:    100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
			t.Setenv("METANODE_DEVNET", "false")
			cs := newTestChainState(t)
			chainID := big.NewInt(991)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

			keySender, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyA, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyB, err := crypto.GenerateKey()
			require.NoError(t, err)

			sender := crypto.PubkeyToAddress(keySender.PublicKey)
			addrA := crypto.PubkeyToAddress(keyA.PublicKey)
			addrB := crypto.PubkeyToAddress(keyB.PublicKey)
			addrC := common.HexToAddress("0xcccc3333cccc3333cccc3333cccc3333cccc3333")
			leader := common.HexToAddress(auditLeader)

			callerAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

			// Caller contract bytecode: calls addrA with 100 wei, stores status in slot 0
			// stack for CALL: gas, addr, value, argsOffset, argsLength, retOffset, retLength
			callerCode := []byte{
				0x60, 0x00, // PUSH1 0 (retLength)
				0x60, 0x00, // PUSH1 0 (retOffset)
				0x60, 0x00, // PUSH1 0 (argsLength)
				0x60, 0x00, // PUSH1 0 (argsOffset)
				0x60, 0x64, // PUSH1 100 (value = 100 wei)
				0x73,       // PUSH20
			}
			callerCode = append(callerCode, addrA.Bytes()...)
			callerCode = append(callerCode,
				0x61, 0xff, 0xff, // PUSH2 0xffff (gas)
				0xf1,             // CALL
				0x60, 0x00,       // PUSH1 0 (slot 0)
				0x55,             // SSTORE
				0x00,             // STOP
			)

			// Seed accounts in MVM
			seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			seedAccount(t, cs, addrA, big.NewInt(0), 0)
			seedAccount(t, cs, addrB, big.NewInt(0), 0)
			seedAccount(t, cs, addrC, big.NewInt(0), 0)

			// Seed Caller contract with 10_000 wei and callerCode
			seedContract(t, cs, callerAddr, big.NewInt(10_000), callerCode)

			var authList []eth.SetCodeAuthorization
			if tc.chained {
				// A -> B and B -> C
				authList = []eth.SetCodeAuthorization{
					signAuthorization(t, keyA, 991, addrB, 0),
					signAuthorization(t, keyB, 991, addrC, 0),
				}
			} else {
				// A -> C (C has empty code, so A acts as EOA)
				authList = []eth.SetCodeAuthorization{
					signAuthorization(t, keyA, 991, addrC, 0),
				}
			}

			signer := eth.LatestSignerForChainID(chainID)
			signed, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
				ChainID:   uint256.NewInt(991),
				Nonce:     1,
				To:        callerAddr,
				Value:     uint256.NewInt(0),
				Gas:       500000,
				GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				Data:      []byte{},
				AuthList:  authList,
			})
			require.NoError(t, err)
			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)

			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))

			groupAddrs := append(grouptxns.BuildDeterministicGroupAddrs(tx), callerAddr, addrA)
			items := []grouptxns.Item{{ID: 0, Array: groupAddrs, Tx: tx}}
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
			require.Len(t, r, 1)

			// Reference execution in go-ethereum EVM
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			reference.AddBalance(callerAddr, uint256.NewInt(10_000), tracing.BalanceChangeUnspecified)
			reference.SetCode(callerAddr, callerCode)

			random := common.Hash{}
			chainConfig := *params.AllDevChainProtocolChanges
			chainConfig.ChainID = chainID
			evm := vm.NewEVM(vm.BlockContext{
				CanTransfer: core.CanTransfer,
				Transfer:    core.Transfer,
				GetHash:     func(uint64) common.Hash { return common.Hash{} },
				GasLimit:    1000000,
				BlockNumber: big.NewInt(1),
				Time:        1,
				Difficulty:  big.NewInt(0),
				BaseFee:     big.NewInt(0),
				Random:      &random,
			}, reference, &chainConfig, vm.Config{})

			msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
			require.NoError(t, err)
			g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
			require.NoError(t, err)

			actualA, err := cs.GetAccountStateDB().AccountState(addrA)
			require.NoError(t, err)
			actualCaller, err := cs.GetAccountStateDB().AccountState(callerAddr)
			require.NoError(t, err)

			t.Logf("[%s] geth_err=%v caller_slot0=%s caller_bal=%s A_bal=%s MVM_status=%v MVM_caller_bal=%s MVM_A_bal=%s",
				tc.name, g.Err, reference.GetState(callerAddr, common.Hash{}), reference.GetBalance(callerAddr),
				reference.GetBalance(addrA), r[0].Status(), actualCaller.TotalBalance(), actualA.TotalBalance())

			require.NoError(t, g.Err, "Top-level tx must succeed in geth")
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status(), "Top-level tx must succeed in MVM")

			// Verify slot 0 in caller contract: 0 for failure, 1 for success
			expectedSlotVal := common.BigToHash(big.NewInt(int64(tc.expectCallRet)))
			require.Equal(t, expectedSlotVal, reference.GetState(callerAddr, common.Hash{}), "Geth slot 0 must reflect CALL status")

			// Check MVM slot 0
			mvmSlotBytes, _ := cs.GetSmartContractDB().StorageValue(callerAddr, common.Hash{}.Bytes())
			require.Equal(t, expectedSlotVal, common.BytesToHash(mvmSlotBytes), "MVM slot 0 must match Geth slot 0")

			// Verify balances match between Geth and MVM
			require.Equal(t, reference.GetBalance(addrA).String(), actualA.TotalBalance().String())
			require.Equal(t, reference.GetBalance(callerAddr).String(), actualCaller.TotalBalance().String())
			require.Equal(t, tc.expectABal, actualA.TotalBalance().Uint64())
		})
	}
}

// TestReviewDelegation_ContractSubcallStaticAndDelegateCall tests that STATICCALL and DELEGATECALL
// to an account with chained delegation fail gracefully and push 0 onto the stack.
func TestEIP7702_ContractSubcallStaticAndDelegateCall(t *testing.T) {
	testCases := []struct {
		name   string
		opcode byte // 0xfa for STATICCALL, 0xf4 for DELEGATECALL
	}{
		{
			name:   "staticcall_to_chained_delegation",
			opcode: 0xfa,
		},
		{
			name:   "delegatecall_to_chained_delegation",
			opcode: 0xf4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
			t.Setenv("METANODE_DEVNET", "false")
			cs := newTestChainState(t)
			chainID := big.NewInt(991)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

			keySender, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyA, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyB, err := crypto.GenerateKey()
			require.NoError(t, err)

			sender := crypto.PubkeyToAddress(keySender.PublicKey)
			addrA := crypto.PubkeyToAddress(keyA.PublicKey)
			addrB := crypto.PubkeyToAddress(keyB.PublicKey)
			addrC := common.HexToAddress("0xcccc3333cccc3333cccc3333cccc3333cccc3333")
			leader := common.HexToAddress(auditLeader)

			callerAddr := common.HexToAddress("0x2222333344445555666677778888999900001111")

			// Bytecode: calls addrA via STATICCALL or DELEGATECALL, stores status in slot 0
			// stack: gas, addr, in_offset, in_size, out_offset, out_size
			callerCode := []byte{
				0x60, 0x00, // PUSH1 0 (out_size)
				0x60, 0x00, // PUSH1 0 (out_offset)
				0x60, 0x00, // PUSH1 0 (in_size)
				0x60, 0x00, // PUSH1 0 (in_offset)
				0x73,       // PUSH20
			}
			callerCode = append(callerCode, addrA.Bytes()...)
			callerCode = append(callerCode,
				0x61, 0xff, 0xff, // PUSH2 0xffff (gas)
				tc.opcode,        // STATICCALL or DELEGATECALL
				0x60, 0x00,       // PUSH1 0 (slot 0)
				0x55,             // SSTORE
				0x00,             // STOP
			)

			seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			seedAccount(t, cs, addrA, big.NewInt(0), 0)
			seedAccount(t, cs, addrB, big.NewInt(0), 0)
			seedAccount(t, cs, addrC, big.NewInt(0), 0)

			seedContract(t, cs, callerAddr, big.NewInt(10_000), callerCode)

			// A -> B and B -> C (chained delegation)
			authList := []eth.SetCodeAuthorization{
				signAuthorization(t, keyA, 991, addrB, 0),
				signAuthorization(t, keyB, 991, addrC, 0),
			}

			signer := eth.LatestSignerForChainID(chainID)
			signed, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
				ChainID:   uint256.NewInt(991),
				Nonce:     1,
				To:        callerAddr,
				Value:     uint256.NewInt(0),
				Gas:       500000,
				GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				Data:      []byte{},
				AuthList:  authList,
			})
			require.NoError(t, err)
			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)

			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))

			groupAddrs := append(grouptxns.BuildDeterministicGroupAddrs(tx), callerAddr, addrA)
			items := []grouptxns.Item{{ID: 0, Array: groupAddrs, Tx: tx}}
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
			require.Len(t, r, 1)

			// Reference execution in go-ethereum EVM
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			reference.AddBalance(callerAddr, uint256.NewInt(10_000), tracing.BalanceChangeUnspecified)
			reference.SetCode(callerAddr, callerCode)

			random := common.Hash{}
			chainConfig := *params.AllDevChainProtocolChanges
			chainConfig.ChainID = chainID
			evm := vm.NewEVM(vm.BlockContext{
				CanTransfer: core.CanTransfer,
				Transfer:    core.Transfer,
				GetHash:     func(uint64) common.Hash { return common.Hash{} },
				GasLimit:    1000000,
				BlockNumber: big.NewInt(1),
				Time:        1,
				Difficulty:  big.NewInt(0),
				BaseFee:     big.NewInt(0),
				Random:      &random,
			}, reference, &chainConfig, vm.Config{})

			msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
			require.NoError(t, err)
			g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
			require.NoError(t, err)

			require.NoError(t, g.Err, "Top-level tx must succeed in geth")
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status(), "Top-level tx must succeed in MVM")

			// In both Geth and MVM, the subcall must fail, pushing 0 to stack, which is stored in slot 0
			expectedSlotVal := common.Hash{} // 0
			require.Equal(t, expectedSlotVal, reference.GetState(callerAddr, common.Hash{}), "Geth slot 0 must be 0")

			mvmSlotBytes, _ := cs.GetSmartContractDB().StorageValue(callerAddr, common.Hash{}.Bytes())
			require.Equal(t, expectedSlotVal, common.BytesToHash(mvmSlotBytes), "MVM slot 0 must be 0")
		})
	}
}

// TestReviewDelegation_StateStorageAndCallerContext verifies that when A delegates to contract C:
// 1. C's code executes in the context of A (ADDRESS opcode is A, NOT C).
// 2. CALLER is the sender.
// 3. Storage writes via SSTORE are isolated to A's storage (C's storage remains empty).
func TestEIP7702_StateStorageAndCallerContext(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	chainID := big.NewInt(991)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	keySender, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyA, err := crypto.GenerateKey()
	require.NoError(t, err)

	sender := crypto.PubkeyToAddress(keySender.PublicKey)
	addrA := crypto.PubkeyToAddress(keyA.PublicKey)
	addrC := common.HexToAddress("0x3333444455556666777788889999000011112222")
	leader := common.HexToAddress(auditLeader)

	// Contract C bytecode:
	// slot 0 = CALLER
	// slot 1 = ADDRESS
	// STOP
	codeC := []byte{
		0x33,       // CALLER
		0x60, 0x00, // PUSH1 0
		0x55,       // SSTORE
		0x30,       // ADDRESS
		0x60, 0x01, // PUSH1 1
		0x55,       // SSTORE
		0x00,       // STOP
	}

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, addrA, big.NewInt(0), 0)
	seedContract(t, cs, addrC, big.NewInt(0), codeC)

	// Authorization: A delegates to C
	authList := []eth.SetCodeAuthorization{
		signAuthorization(t, keyA, 991, addrC, 0),
	}

	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        addrA,
		Value:     uint256.NewInt(0),
		Gas:       500000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Data:      []byte{1},
		AuthList:  authList,
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	st, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx, cs, st))

	groupAddrs := append(grouptxns.BuildDeterministicGroupAddrs(tx), addrA, addrC)
	items := []grouptxns.Item{{ID: 0, Array: groupAddrs, Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status())

	// Reference execution in go-ethereum EVM
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(addrC, codeC)

	random := common.Hash{}
	chainConfig := *params.AllDevChainProtocolChanges
	chainConfig.ChainID = chainID
	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		GasLimit:    1000000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
		Random:      &random,
	}, reference, &chainConfig, vm.Config{})

	msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
	require.NoError(t, err)
	g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.NoError(t, g.Err)

	slot0 := common.Hash{}
	slot1 := common.BigToHash(big.NewInt(1))

	// In Geth: A's storage has CALLER in slot 0 and ADDRESS in slot 1. C's storage has 0.
	require.Equal(t, common.BytesToHash(sender.Bytes()), reference.GetState(addrA, slot0))
	require.Equal(t, common.BytesToHash(addrA.Bytes()), reference.GetState(addrA, slot1))
	require.Equal(t, common.Hash{}, reference.GetState(addrC, slot0))
	require.Equal(t, common.Hash{}, reference.GetState(addrC, slot1))

	// In MVM: Must match Geth exactly!
	mvmSlot0Bytes, _ := cs.GetSmartContractDB().StorageValue(addrA, slot0.Bytes())
	mvmSlot1Bytes, _ := cs.GetSmartContractDB().StorageValue(addrA, slot1.Bytes())
	require.Equal(t, common.BytesToHash(sender.Bytes()), common.BytesToHash(mvmSlot0Bytes), "A's slot 0 must be CALLER")
	require.Equal(t, common.BytesToHash(addrA.Bytes()), common.BytesToHash(mvmSlot1Bytes), "A's slot 1 must be ADDRESS (A, not C)")

	// C's storage must NOT have been written to
	mvmCSlot0Bytes, _ := cs.GetSmartContractDB().StorageValue(addrC, slot0.Bytes())
	require.Equal(t, common.Hash{}, common.BytesToHash(mvmCSlot0Bytes), "C's slot 0 must be untouched")
}

// TestReviewDelegation_ExplicitRevertRollback verifies that when a delegate contract explicitly
// REVERTs, the transaction fails and the transferred value is rolled back.
func TestEIP7702_ExplicitRevertRollback(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	chainID := big.NewInt(991)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	keySender, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyA, err := crypto.GenerateKey()
	require.NoError(t, err)

	sender := crypto.PubkeyToAddress(keySender.PublicKey)
	addrA := crypto.PubkeyToAddress(keyA.PublicKey)
	addrC := common.HexToAddress("0x5555666677778888999900001111222233334444")
	leader := common.HexToAddress(auditLeader)

	// Contract C bytecode: REVERT(0, 0)
	codeC := []byte{
		0x60, 0x00, // PUSH1 0
		0x60, 0x00, // PUSH1 0
		0xfd,       // REVERT
	}

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, addrA, big.NewInt(0), 0)
	seedContract(t, cs, addrC, big.NewInt(0), codeC)

	authList := []eth.SetCodeAuthorization{
		signAuthorization(t, keyA, 991, addrC, 0),
	}

	signer := eth.LatestSignerForChainID(chainID)
	signed, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        addrA,
		Value:     uint256.NewInt(100), // send 100 wei
		Gas:       500000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Data:      []byte{1},
		AuthList:  authList,
	})
	require.NoError(t, err)
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	st, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx, cs, st))

	groupAddrs := append(grouptxns.BuildDeterministicGroupAddrs(tx), addrA, addrC)
	items := []grouptxns.Item{{ID: 0, Array: groupAddrs, Tx: tx}}
	_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r, 1)

	// MVM must report TRANSACTION_ERROR
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r[0].Status())

	// Reference execution in go-ethereum EVM
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(addrC, codeC)

	random := common.Hash{}
	chainConfig := *params.AllDevChainProtocolChanges
	chainConfig.ChainID = chainID
	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		GasLimit:    1000000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
		Random:      &random,
	}, reference, &chainConfig, vm.Config{})

	msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
	require.NoError(t, err)
	g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.Error(t, g.Err, "Geth must report revert error")

	// Balance of A must remain 0 in both Geth and MVM (value rolled back)
	actualA, err := cs.GetAccountStateDB().AccountState(addrA)
	require.NoError(t, err)
	require.Equal(t, "0", actualA.TotalBalance().String())
	require.Equal(t, reference.GetBalance(addrA).String(), actualA.TotalBalance().String())
}

// TestReviewDelegation_CrossTxPersistence tests that delegation configuration persists
// across transactions and subsequent legacy/Type-2 calls to the delegated account
// still encounter 0xef and rollback appropriately.
func TestEIP7702_CrossTxPersistence(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	cs := newTestChainState(t)
	chainID := big.NewInt(991)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

	keySender, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyA, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyB, err := crypto.GenerateKey()
	require.NoError(t, err)

	sender := crypto.PubkeyToAddress(keySender.PublicKey)
	addrA := crypto.PubkeyToAddress(keyA.PublicKey)
	addrB := crypto.PubkeyToAddress(keyB.PublicKey)
	addrC := common.HexToAddress("0xcccc3333cccc3333cccc3333cccc3333cccc3333")
	leader := common.HexToAddress(auditLeader)

	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, addrA, big.NewInt(0), 0)
	seedAccount(t, cs, addrB, big.NewInt(0), 0)
	seedAccount(t, cs, addrC, big.NewInt(0), 0)

	// Setup reference Geth EVM
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)

	random := common.Hash{}
	chainConfig := *params.AllDevChainProtocolChanges
	chainConfig.ChainID = chainID
	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		GasLimit:    1000000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
		Random:      &random,
	}, reference, &chainConfig, vm.Config{})

	signer := eth.LatestSignerForChainID(chainID)

	// Tx 1: SetCodeTx sets A -> B and B -> C (chained delegation). Destination is leader with value 0.
	signed1, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        leader,
		Value:     uint256.NewInt(0),
		Gas:       200000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Data:      []byte{},
		AuthList: []eth.SetCodeAuthorization{
			signAuthorization(t, keyA, 991, addrB, 0),
			signAuthorization(t, keyB, 991, addrC, 0),
		},
	})
	require.NoError(t, err)
	tx1, err := transaction.NewTransactionFromEth(signed1)
	require.NoError(t, err)

	items1 := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx1), Tx: tx1}}
	_, r1, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items1, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r1, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r1[0].Status())

	msg1, err := core.TransactionToMessage(signed1, signer, big.NewInt(0))
	require.NoError(t, err)
	g1, err := core.ApplyMessage(evm, msg1, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.NoError(t, g1.Err)

	// Tx 2: Type 0 legacy tx: sender sends 50 wei to addrA (WITHOUT ANY AuthList)
	signed2, err := eth.SignNewTx(keySender, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &addrA,
		Value:    big.NewInt(50),
		Gas:      100000,
		GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
		Data:     []byte{},
	})
	require.NoError(t, err)
	tx2, err := transaction.NewTransactionFromEth(signed2)
	require.NoError(t, err)

	stSender, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx2, cs, stSender))

	items2 := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx2), Tx: tx2}}
	_, r2, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode), blankHeader(), false, false, 999, leader, false)
	require.Len(t, r2, 1)

	msg2, err := core.TransactionToMessage(signed2, signer, big.NewInt(0))
	require.NoError(t, err)
	g2, err := core.ApplyMessage(evm, msg2, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)

	// Both Geth and MVM must fail because A still resolves to chained B, encountering 0xef
	require.Error(t, g2.Err, "Geth must fail when calling A with persisted chained delegation")
	require.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, r2[0].Status(), "MVM must fail when calling A with persisted chained delegation")

	actualA, err := cs.GetAccountStateDB().AccountState(addrA)
	require.NoError(t, err)
	require.Equal(t, "0", actualA.TotalBalance().String(), "Balance of A must remain 0")
	require.Equal(t, reference.GetBalance(addrA).String(), actualA.TotalBalance().String())
}

// TestReviewDelegation_MultipleAuthUpdatesInSameTx tests that when an authorization list has multiple
// authorizations for the same authority:
// 1. A newer authorization with higher nonce overrides the previous one.
// 2. An overwrite from chained -> valid succeeds.
// 3. An overwrite from chained -> revocation (0x0) succeeds as standard EOA transfer.
func TestEIP7702_MultipleAuthUpdatesInSameTx(t *testing.T) {
	testCases := []struct {
		name       string
		secondAuth func(t *testing.T, keyA *ecdsa.PrivateKey, validContract, zeroAddr common.Address) eth.SetCodeAuthorization
		isContract bool
	}{
		{
			name: "chained_overwritten_by_valid_contract",
			secondAuth: func(t *testing.T, keyA *ecdsa.PrivateKey, validContract, zeroAddr common.Address) eth.SetCodeAuthorization {
				return signAuthorization(t, keyA, 991, validContract, 1)
			},
			isContract: true,
		},
		{
			name: "chained_overwritten_by_revocation",
			secondAuth: func(t *testing.T, keyA *ecdsa.PrivateKey, validContract, zeroAddr common.Address) eth.SetCodeAuthorization {
				return signAuthorization(t, keyA, 991, zeroAddr, 1)
			},
			isContract: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
			t.Setenv("METANODE_DEVNET", "false")
			cs := newTestChainState(t)
			chainID := big.NewInt(991)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})

			keySender, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyA, err := crypto.GenerateKey()
			require.NoError(t, err)
			keyB, err := crypto.GenerateKey()
			require.NoError(t, err)

			sender := crypto.PubkeyToAddress(keySender.PublicKey)
			addrA := crypto.PubkeyToAddress(keyA.PublicKey)
			addrB := crypto.PubkeyToAddress(keyB.PublicKey)
			addrC := common.HexToAddress("0xcccc3333cccc3333cccc3333cccc3333cccc3333")
			leader := common.HexToAddress(auditLeader)

			contractValid := common.HexToAddress("0x9999888877776666555544443333222211110000")
			zeroAddr := common.Address{}

			// Code for contractValid: STOP (0x00)
			codeValid := []byte{0x00}

			seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			seedAccount(t, cs, addrA, big.NewInt(0), 0)
			seedAccount(t, cs, addrB, big.NewInt(0), 0)
			seedAccount(t, cs, addrC, big.NewInt(0), 0)
			seedContract(t, cs, contractValid, big.NewInt(0), codeValid)

			// AuthList:
			// 1. A -> B (nonce 0) (chained delegation)
			// 2. B -> C (nonce 0)
			// 3. secondAuth: A -> contractValid (nonce 1) OR A -> 0x0 (nonce 1)
			authList := []eth.SetCodeAuthorization{
				signAuthorization(t, keyA, 991, addrB, 0),
				signAuthorization(t, keyB, 991, addrC, 0),
				tc.secondAuth(t, keyA, contractValid, zeroAddr),
			}

			signer := eth.LatestSignerForChainID(chainID)
			signed, err := eth.SignNewTx(keySender, signer, &eth.SetCodeTx{
				ChainID:   uint256.NewInt(991),
				Nonce:     1,
				To:        addrA,
				Value:     uint256.NewInt(100),
				Gas:       500000,
				GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				Data:      []byte{},
				AuthList:  authList,
			})
			require.NoError(t, err)
			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)

			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))

			groupAddrs := append(grouptxns.BuildDeterministicGroupAddrs(tx), addrA, contractValid)
			items := []grouptxns.Item{{ID: 0, Array: groupAddrs, Tx: tx}}
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
			require.Len(t, r, 1)

			// Reference execution in go-ethereum EVM
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			reference.SetCode(contractValid, codeValid)

			random := common.Hash{}
			chainConfig := *params.AllDevChainProtocolChanges
			chainConfig.ChainID = chainID
			evm := vm.NewEVM(vm.BlockContext{
				CanTransfer: core.CanTransfer,
				Transfer:    core.Transfer,
				GetHash:     func(uint64) common.Hash { return common.Hash{} },
				GasLimit:    1000000,
				BlockNumber: big.NewInt(1),
				Time:        1,
				Difficulty:  big.NewInt(0),
				BaseFee:     big.NewInt(0),
				Random:      &random,
			}, reference, &chainConfig, vm.Config{})

			msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
			require.NoError(t, err)
			g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
			require.NoError(t, err)

			// Both must succeed
			require.NoError(t, g.Err, "Geth must succeed when chained delegation is overridden")
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status(), "MVM must succeed when chained delegation is overridden")

			actualA, err := cs.GetAccountStateDB().AccountState(addrA)
			require.NoError(t, err)
			require.Equal(t, "100", actualA.TotalBalance().String(), "A must receive 100 wei")
			require.Equal(t, reference.GetBalance(addrA).String(), actualA.TotalBalance().String())
		})
	}
}

func TestEIP7702_NestedDelegationTargetColdGas(t *testing.T) {
	testCases := []struct {
		name      string
		authChain uint64
		authNonce uint64
	}{
		{name: "valid_chain_id", authChain: 991, authNonce: 0},
		{name: "wrong_chain_id_12345", authChain: 12345, authNonce: 0},
		{name: "wildcard_chain_id_0", authChain: 0, authNonce: 0},
		{name: "wrong_nonce", authChain: 991, authNonce: 5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
			t.Setenv("METANODE_DEVNET", "false")
			chainID := big.NewInt(991)
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
			senderKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			authKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			sender := crypto.PubkeyToAddress(senderKey.PublicKey)
			authority := crypto.PubkeyToAddress(authKey.PublicKey)
			delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
			caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
			leader := common.HexToAddress(auditLeader)
			// Caller performs CALL(authority) once; delegate runtime is STOP.
			callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
			callerCode = append(callerCode, authority.Bytes()...)
			callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
			seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			seedAccount(t, cs, authority, big.NewInt(0), 0)
			seedContract(t, cs, caller, big.NewInt(0), callerCode)
			seedContract(t, cs, delegate, big.NewInt(0), []byte{0})
			auth := signAuthorization(t, authKey, tc.authChain, delegate, tc.authNonce)
			signer := eth.LatestSignerForChainID(chainID)
			signed, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
				ChainID:   uint256.NewInt(991),
				Nonce:     1,
				To:        caller,
				Gas:       200000,
				GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
				Value:     uint256.NewInt(0),
				AuthList:  []eth.SetCodeAuthorization{auth},
			})
			require.NoError(t, err)
			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)
			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))
			items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, authority, delegate), Tx: tx}}
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
			require.Len(t, r, 1)
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
			reference.SetCode(caller, callerCode)
			reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
			reference.SetCode(delegate, []byte{0})
			reference.SetNonce(delegate, 1, tracing.NonceChangeUnspecified)
			random := common.Hash{}
			cfg := *params.AllDevChainProtocolChanges
			cfg.ChainID = chainID
			evm := vm.NewEVM(vm.BlockContext{
				CanTransfer: core.CanTransfer,
				Transfer:    core.Transfer,
				GetHash:     func(uint64) common.Hash { return common.Hash{} },
				GasLimit:    1000000,
				BlockNumber: big.NewInt(1),
				Time:        1,
				Difficulty:  big.NewInt(0),
				BaseFee:     big.NewInt(0),
				Random:      &random,
			}, reference, &cfg, vm.Config{})
			msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
			require.NoError(t, err)
			g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
			require.NoError(t, err)
			require.NoError(t, g.Err)
			t.Logf("[%s] geth_gas=%d MVM_gas=%d delta=%d status=%v", tc.name, g.UsedGas, r[0].GasUsed(), int64(r[0].GasUsed())-int64(g.UsedGas), r[0].Status())
			require.Equal(t, g.UsedGas, r[0].GasUsed(), "gas used must match go-ethereum exactly for %s", tc.name)
		})
	}
}

func TestEIP7702_PersistedDelegationTargetColdGas(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")
	chainID := big.NewInt(991)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID})
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	authKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
	caller := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)
	callerCode := []byte{0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x60, 0, 0x73}
	callerCode = append(callerCode, authority.Bytes()...)
	callerCode = append(callerCode, 0x61, 0xff, 0xff, 0xf1, 0x50, 0x00)
	seedAccount(t, cs, sender, big.NewInt(1_000_000_000_000_000), 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)
	seedAccount(t, cs, authority, big.NewInt(0), 0)
	seedContract(t, cs, caller, big.NewInt(0), callerCode)
	seedContract(t, cs, delegate, big.NewInt(0), []byte{0})
	signer := eth.LatestSignerForChainID(chainID)
	authTx, err := eth.SignNewTx(senderKey, signer, &eth.SetCodeTx{
		ChainID:   uint256.NewInt(991),
		Nonce:     1,
		To:        common.HexToAddress("0x9999"),
		Gas:       100000,
		GasFeeCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		GasTipCap: uint256.NewInt(mt.MINIMUM_BASE_FEE),
		Value:     uint256.NewInt(0),
		AuthList:  []eth.SetCodeAuthorization{signAuthorization(t, authKey, 991, delegate, 0)},
	})
	require.NoError(t, err)
	callTx, err := eth.SignNewTx(senderKey, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &caller,
		Gas:      200000,
		GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
	})
	require.NoError(t, err)
	execMVM := func(signed *eth.Transaction) uint64 {
		tx, err := transaction.NewTransactionFromEth(signed)
		require.NoError(t, err)
		st, err := cs.GetAccountStateDB().AccountState(sender)
		require.NoError(t, err)
		require.Nil(t, VerifyTransaction(tx, cs, st))
		items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), caller, authority, delegate), Tx: tx}}
		_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
		require.Len(t, r, 1)
		require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status())
		return r[0].GasUsed()
	}
	_ = execMVM(authTx)
	mvmGas := execMVM(callTx)
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(caller, callerCode)
	reference.SetNonce(caller, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(delegate, []byte{0})
	reference.SetNonce(delegate, 1, tracing.NonceChangeUnspecified)
	random := common.Hash{}
	cfg := *params.AllDevChainProtocolChanges
	cfg.ChainID = chainID
	runGeth := func(signed *eth.Transaction) uint64 {
		evm := vm.NewEVM(vm.BlockContext{
			CanTransfer: core.CanTransfer,
			Transfer:    core.Transfer,
			GetHash:     func(uint64) common.Hash { return common.Hash{} },
			GasLimit:    1000000,
			BlockNumber: big.NewInt(1),
			Time:        1,
			Difficulty:  big.NewInt(0),
			BaseFee:     big.NewInt(0),
			Random:      &random,
		}, reference, &cfg, vm.Config{})
		msg, err := core.TransactionToMessage(signed, signer, big.NewInt(0))
		require.NoError(t, err)
		g, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
		require.NoError(t, err)
		require.NoError(t, g.Err)
		reference.Finalise(true)
		return g.UsedGas
	}
	_ = runGeth(authTx)
	gethGas := runGeth(callTx)
	t.Logf("persisted delegation: geth_gas=%d MVM_gas=%d delta=%d", gethGas, mvmGas, int64(mvmGas)-int64(gethGas))
	require.Equal(t, gethGas, mvmGas, "delegate target is cold at the start of the later transaction")
}
