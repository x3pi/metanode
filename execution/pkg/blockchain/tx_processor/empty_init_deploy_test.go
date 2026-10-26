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
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/require"
)

func TestEmptyInitCodeContractCreationParity(t *testing.T) {
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	t.Setenv("METANODE_DEVNET", "false")

	chainID := big.NewInt(991)
	signer := eth.LatestSignerForChainID(chainID)

	testCases := []struct {
		name    string
		nonce   uint64
		buildTx func(key *ecdsa.PrivateKey, nonce uint64) *eth.Transaction
	}{
		{
			name:  "legacy_empty_data_nonce1",
			nonce: 1,
			buildTx: func(key *ecdsa.PrivateKey, nonce uint64) *eth.Transaction {
				tx, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
					Nonce:    nonce,
					GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
					Gas:      100000,
					To:       nil,
					Value:    big.NewInt(123),
					Data:     nil,
				})
				if err != nil {
					panic(err)
				}
				return tx
			},
		},
		{
			name:  "legacy_empty_data_nonce0",
			nonce: 0,
			buildTx: func(key *ecdsa.PrivateKey, nonce uint64) *eth.Transaction {
				tx, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
					Nonce:    nonce,
					GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
					Gas:      100000,
					To:       nil,
					Value:    big.NewInt(123),
					Data:     nil,
				})
				if err != nil {
					panic(err)
				}
				return tx
			},
		},
		{
			name:  "eip2930_empty_data",
			nonce: 0,
			buildTx: func(key *ecdsa.PrivateKey, nonce uint64) *eth.Transaction {
				tx, err := eth.SignNewTx(key, signer, &eth.AccessListTx{
					ChainID:  chainID,
					Nonce:    nonce,
					GasPrice: big.NewInt(mt.MINIMUM_BASE_FEE),
					Gas:      100000,
					To:       nil,
					Value:    big.NewInt(789),
					Data:     nil,
				})
				if err != nil {
					panic(err)
				}
				return tx
			},
		},
		{
			name:  "eip1559_empty_data_nonce1",
			nonce: 1,
			buildTx: func(key *ecdsa.PrivateKey, nonce uint64) *eth.Transaction {
				tx, err := eth.SignNewTx(key, signer, &eth.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
					GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
					Gas:       100000,
					To:        nil,
					Value:     big.NewInt(456),
					Data:      []byte{},
				})
				if err != nil {
					panic(err)
				}
				return tx
			},
		},
		{
			name:  "eip1559_empty_data_nonce0",
			nonce: 0,
			buildTx: func(key *ecdsa.PrivateKey, nonce uint64) *eth.Transaction {
				tx, err := eth.SignNewTx(key, signer, &eth.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(mt.MINIMUM_BASE_FEE),
					GasFeeCap: big.NewInt(mt.MINIMUM_BASE_FEE),
					Gas:       100000,
					To:        nil,
					Value:     big.NewInt(456),
					Data:      []byte{},
				})
				if err != nil {
					panic(err)
				}
				return tx
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID, TxSignatureMode: config.TxSignatureModeSecp})

			keySender, err := crypto.GenerateKey()
			require.NoError(t, err)
			sender := crypto.PubkeyToAddress(keySender.PublicKey)
			leader := common.HexToAddress(auditLeader)

			initialBalance := big.NewInt(1_000_000_000_000_000)
			seedAccount(t, cs, sender, initialBalance, tc.nonce)
			seedAccount(t, cs, leader, big.NewInt(0), 0)
			seedAccount(t, cs, common.Address{}, big.NewInt(0), 0)

			senderNonce := tc.nonce
			signed := tc.buildTx(keySender, senderNonce)

			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)

			// Classification asserts
			require.True(t, tx.IsDeployContract(), "Tx must be recognized as Deploy Contract")
			require.False(t, tx.IsCallContract())
			require.False(t, tx.IsRegularTransaction())
			require.True(t, tx.ValidDeployData())

			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))

			items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
			_, r, _, _ := ProcessTransactionsOptimistic(context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items, cs.HasCode), blankHeader(), false, false, 999, leader, false)
			require.Len(t, r, 1)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, r[0].Status(), "MVM execution must return success")

			expectedContractAddr := crypto.CreateAddress(sender, senderNonce)

			// Verify MVM state changes
			zeroAs, err := cs.GetAccountStateDB().AccountState(common.Address{})
			require.NoError(t, err)
			zeroBalance := big.NewInt(0)
			if zeroAs != nil {
				zeroBalance = zeroAs.TotalBalance()
			}
			require.Equal(t, "0", zeroBalance.String(), "Zero address MUST NOT receive transferred value")

			createdAs, err := cs.GetAccountStateDB().AccountState(expectedContractAddr)
			require.NoError(t, err)
			require.NotNil(t, createdAs, "Contract account must be created in state")
			require.Equal(t, uint64(1), createdAs.Nonce(), "EIP-161: created contract account nonce must be 1")
			require.Equal(t, signed.Value().String(), createdAs.TotalBalance().String(), "Created contract must receive value")

			// Reference execution in go-ethereum EVM
			reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
			require.NoError(t, err)
			reference.AddBalance(sender, uint256.MustFromBig(initialBalance), tracing.BalanceChangeUnspecified)
			reference.SetNonce(sender, senderNonce, tracing.NonceChangeUnspecified)

			random := common.Hash{}
			chainConfig := *params.AllDevChainProtocolChanges
			chainConfig.ChainID = chainID
			evm := vm.NewEVM(vm.BlockContext{
				CanTransfer: core.CanTransfer,
				Transfer:    core.Transfer,
				GetHash:     func(uint64) common.Hash { return common.Hash{} },
				Coinbase:    leader,
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
			require.NoError(t, g.Err, "Geth must execute without error")

			// Gas conformance: contract creation intrinsic gas is 53,000
			require.Equal(t, uint64(53000), g.UsedGas, "Geth gas used must be 53000 for empty initcode creation")
			require.Equal(t, g.UsedGas, r[0].GasUsed(), "MVM gas used must match Geth exactly (delta = 0)")

			// Account state parity
			require.Equal(t, reference.GetNonce(expectedContractAddr), createdAs.Nonce(), "Nonce must match geth")
			require.Equal(t, reference.GetBalance(expectedContractAddr).ToBig().String(), createdAs.TotalBalance().String(), "Balance must match geth")
			require.Equal(t, "0", reference.GetBalance(common.Address{}).ToBig().String(), "Geth zero address balance is 0")
		})
	}
}
