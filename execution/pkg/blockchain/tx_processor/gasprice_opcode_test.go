package tx_processor

import (
	"context"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	mt "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/require"
)

// TestGasPriceOpcode_AllTransactionTypes verifies that opcode GASPRICE (0x3a) inside MVM
// returns the actual effective execution gas price for ALL transaction types (Legacy, EIP-2930,
// EIP-1559, EIP-4844, and EIP-7702) rather than 0 for fee-cap-based types, and that the value
// is correctly persisted into storage slot 0 and charged to the sender.
func TestGasPriceOpcode_AllTransactionTypes(t *testing.T) {
	chainID := big.NewInt(991)
	signer := eth.LatestSignerForChainID(chainID)

	testCases := []struct {
		name          string
		buildTxData   func(t *testing.T, target common.Address) eth.TxData
		expectedPrice *big.Int
	}{
		{
			name: "legacy_type0",
			buildTxData: func(_ *testing.T, target common.Address) eth.TxData {
				return &eth.LegacyTx{
					Nonce:    1,
					To:       &target,
					Value:    big.NewInt(0),
					Gas:      100000,
					GasPrice: big.NewInt(115000),
				}
			},
			expectedPrice: big.NewInt(115000),
		},
		{
			name: "eip2930_type1",
			buildTxData: func(_ *testing.T, target common.Address) eth.TxData {
				return &eth.AccessListTx{
					ChainID:  chainID,
					Nonce:    1,
					To:       &target,
					Value:    big.NewInt(0),
					Gas:      100000,
					GasPrice: big.NewInt(115000),
				}
			},
			expectedPrice: big.NewInt(115000),
		},
		{
			name: "eip1559_type2",
			buildTxData: func(_ *testing.T, target common.Address) eth.TxData {
				return &eth.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     1,
					To:        &target,
					Value:     big.NewInt(0),
					Gas:       100000,
					GasFeeCap: big.NewInt(200000),
					GasTipCap: big.NewInt(15000),
				}
			},
			expectedPrice: big.NewInt(115000), // MINIMUM_BASE_FEE (100,000) + tip (15,000)
		},
		{
			name: "eip4844_type3",
			buildTxData: func(_ *testing.T, target common.Address) eth.TxData {
				var blob kzg4844.Blob
				c, err := kzg4844.BlobToCommitment(&blob)
				if err != nil {
					panic(err)
				}
				proof, err := kzg4844.ComputeBlobProof(&blob, c)
				if err != nil {
					panic(err)
				}
				vh := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &c))
				return &eth.BlobTx{
					ChainID:    uint256.MustFromBig(chainID),
					Nonce:      1,
					GasTipCap:  uint256.NewInt(15000),
					GasFeeCap:  uint256.NewInt(200000),
					Gas:        100000,
					To:         target,
					Value:      uint256.NewInt(0),
					BlobFeeCap: uint256.NewInt(1),
					BlobHashes: []common.Hash{vh},
					Sidecar: &eth.BlobTxSidecar{
						Blobs:       []kzg4844.Blob{blob},
						Commitments: []kzg4844.Commitment{c},
						Proofs:      []kzg4844.Proof{proof},
					},
				}
			},
			expectedPrice: big.NewInt(115000), // MINIMUM_BASE_FEE (100,000) + tip (15,000)
		},
		{
			name: "eip7702_type4",
			buildTxData: func(t *testing.T, target common.Address) eth.TxData {
				authKey, err := crypto.GenerateKey()
				require.NoError(t, err)
				delegate := common.HexToAddress("0xdddddddddddddddddddddddddddddddddddddddd")
				auth := signAuthorization(t, authKey, 991, delegate, 0)
				return &eth.SetCodeTx{
					ChainID:   uint256.MustFromBig(chainID),
					Nonce:     1,
					To:        target,
					Gas:       150000,
					GasFeeCap: uint256.NewInt(200000),
					GasTipCap: uint256.NewInt(15000),
					Value:     uint256.NewInt(0),
					AuthList:  []eth.SetCodeAuthorization{auth},
				}
			},
			expectedPrice: big.NewInt(115000), // MINIMUM_BASE_FEE (100,000) + tip (15,000)
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID, TxSignatureMode: config.TxSignatureModeSecp})

			key, err := crypto.GenerateKey()
			require.NoError(t, err)
			sender := crypto.PubkeyToAddress(key.PublicKey)
			target := common.HexToAddress("0x1234567890123456789012345678901234567890")
			leader := common.HexToAddress(auditLeader)

			initialBalance := big.NewInt(1_000_000_000_000_000)
			seedAccount(t, cs, sender, initialBalance, 1)
			seedAccount(t, cs, leader, big.NewInt(0), 0)

			// Bytecode: 3a 80 6000 55 6000 52 6020 6000 f3
			// GASPRICE; DUP1; PUSH1 0; SSTORE (slot 0); PUSH1 0; MSTORE; PUSH1 32; PUSH1 0; RETURN
			contractCode := common.FromHex("3a8060005560005260206000f3")
			seedContract(t, cs, target, big.NewInt(0), contractCode)

			txData := tc.buildTxData(t, target)
			signed, err := eth.SignNewTx(key, signer, txData)
			require.NoError(t, err)

			tx, err := transaction.NewTransactionFromEth(signed)
			require.NoError(t, err)

			st, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			require.Nil(t, VerifyTransaction(tx, cs, st))

			items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
			_, receipts, _, _ := ProcessTransactionsOptimistic(
				context.Background(),
				cs,
				grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
				blankHeader(),
				false,
				false,
				999,
				leader,
				false,
			)
			require.Len(t, receipts, 1)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipts[0].Status())

			observedPrice := new(big.Int).SetBytes(receipts[0].Return())
			require.Equal(t, tc.expectedPrice.String(), observedPrice.String(),
				"opcode GASPRICE must match expected effective gas price")
			require.Equal(t, tx.EffectiveGasPrice().String(), observedPrice.String(),
				"opcode GASPRICE must match tx.EffectiveGasPrice()")

			// Verify storage slot 0 committed in smart contract DB
			storedVal, exists := cs.GetSmartContractDB().StorageValue(target, common.Hash{}.Bytes())
			require.True(t, exists)
			storedPrice := new(big.Int).SetBytes(storedVal)
			require.Equal(t, tc.expectedPrice.String(), storedPrice.String(),
				"persisted storage slot 0 must match expected effective gas price")

			// Verify fee debited from sender matches gasUsed * EffectiveGasPrice (+ blobFee if any)
			afterState, err := cs.GetAccountStateDB().AccountState(sender)
			require.NoError(t, err)
			actualDeducted := new(big.Int).Sub(initialBalance, afterState.TotalBalance())
			expectedExecFee := new(big.Int).Mul(new(big.Int).SetUint64(receipts[0].GasUsed()), tx.EffectiveGasPrice())

			// For EIP-4844 blob tx, blob fee is charged separately
			if tx.Type() == eth.BlobTxType {
				blobFee := new(big.Int).Mul(new(big.Int).SetUint64(131072), big.NewInt(1))
				expectedTotalFee := new(big.Int).Add(expectedExecFee, blobFee)
				require.Equal(t, expectedTotalFee.String(), actualDeducted.String(),
					"total deducted sender balance must match exec fee + blob fee")
			} else {
				require.Equal(t, expectedExecFee.String(), actualDeducted.String(),
					"total deducted sender balance must match gasUsed * EffectiveGasPrice")
			}

			t.Logf("[%s] status=%s GASPRICE=%s slot0=%s gasUsed=%d sender_fee=%s",
				tc.name, receipts[0].Status(), observedPrice, storedPrice, receipts[0].GasUsed(), actualDeducted)
		})
	}
}

// TestGasPriceOpcode_FeeCapSaturation tests the EIP-1559 case where baseFee + tip exceeds feeCap,
// ensuring that GASPRICE opcode reflects the capped price min(FeeCap, baseFee + tip) = FeeCap.
func TestGasPriceOpcode_FeeCapSaturation(t *testing.T) {
	chainID := big.NewInt(991)
	signer := eth.LatestSignerForChainID(chainID)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID, TxSignatureMode: config.TxSignatureModeSecp})

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	target := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	initialBalance := big.NewInt(1_000_000_000_000_000)
	seedAccount(t, cs, sender, initialBalance, 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Contract stores GASPRICE to slot 0 and returns it
	contractCode := common.FromHex("3a8060005560005260206000f3")
	seedContract(t, cs, target, big.NewInt(0), contractCode)

	// BaseFee = 100,000; Tip = 50,000 (sum = 150,000); FeeCap = 120,000 (< 150,000)
	// Expected EffectiveGasPrice = 120,000 (capped at FeeCap)
	signed, err := eth.SignNewTx(key, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        &target,
		Value:     big.NewInt(0),
		Gas:       100000,
		GasFeeCap: big.NewInt(120000),
		GasTipCap: big.NewInt(50000),
	})
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	st, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx, cs, st))

	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	_, receipts, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, receipts, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipts[0].Status())

	observedPrice := new(big.Int).SetBytes(receipts[0].Return())
	require.Equal(t, "120000", observedPrice.String(), "GASPRICE opcode must be saturated at FeeCap")
	require.Equal(t, tx.EffectiveGasPrice().String(), observedPrice.String())

	storedVal, exists := cs.GetSmartContractDB().StorageValue(target, common.Hash{}.Bytes())
	require.True(t, exists)
	require.Equal(t, "120000", new(big.Int).SetBytes(storedVal).String())
}

// TestGasPriceOpcode_DeployConstructor verifies that the GASPRICE opcode inside the contract
// constructor (initcode during contract creation/deploy) also sees the effective gas price.
func TestGasPriceOpcode_DeployConstructor(t *testing.T) {
	chainID := big.NewInt(991)
	signer := eth.LatestSignerForChainID(chainID)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID, TxSignatureMode: config.TxSignatureModeSecp})

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress(auditLeader)

	initialBalance := big.NewInt(1_000_000_000_000_000)
	seedAccount(t, cs, sender, initialBalance, 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Initcode: 3a 6000 55 6000 6000 f3
	// Reads GASPRICE, writes to storage slot 0, then returns empty runtime code
	initCode := common.FromHex("3a60005560006000f3")

	// EIP-1559 contract creation (To = nil)
	signed, err := eth.SignNewTx(key, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        nil,
		Value:     big.NewInt(0),
		Gas:       150000,
		GasFeeCap: big.NewInt(200000),
		GasTipCap: big.NewInt(15000),
		Data:      initCode,
	})
	require.NoError(t, err)

	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)

	st, err := cs.GetAccountStateDB().AccountState(sender)
	require.NoError(t, err)
	require.Nil(t, VerifyTransaction(tx, cs, st))

	contractAddr := crypto.CreateAddress(sender, 1)
	items := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx), contractAddr), Tx: tx}}
	_, receipts, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, receipts, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipts[0].Status())

	// Verify that constructor executed and stored the effective gas price (115,000) in slot 0
	storedVal, exists := cs.GetSmartContractDB().StorageValue(contractAddr, common.Hash{}.Bytes())
	require.True(t, exists, "deployed contract must have storage slot 0 written")
	storedPrice := new(big.Int).SetBytes(storedVal)
	require.Equal(t, "115000", storedPrice.String(), "initcode constructor must have observed effective gas price 115000")
}

// TestGasPriceOpcode_GethDifferential runs the same EIP-1559 contract call on go-ethereum's EVM
// and MetaNode's MVM, proving exact equivalence of opcode GASPRICE and state commitments.
func TestGasPriceOpcode_GethDifferential(t *testing.T) {
	chainID := big.NewInt(991)
	signer := eth.LatestSignerForChainID(chainID)
	cs := newTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{ChainId: chainID, TxSignatureMode: config.TxSignatureModeSecp})

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	target := common.HexToAddress("0x1234567890123456789012345678901234567890")
	leader := common.HexToAddress(auditLeader)

	initialBalance := big.NewInt(1_000_000_000_000_000)
	seedAccount(t, cs, sender, initialBalance, 1)
	seedAccount(t, cs, leader, big.NewInt(0), 0)

	// Contract code: GASPRICE; DUP1; PUSH1 0; SSTORE; PUSH1 0; MSTORE; PUSH1 32; PUSH1 0; RETURN
	contractCode := common.FromHex("3a8060005560005260206000f3")
	seedContract(t, cs, target, big.NewInt(0), contractCode)

	signed, err := eth.SignNewTx(key, signer, &eth.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		To:        &target,
		Value:     big.NewInt(0),
		Gas:       100000,
		GasFeeCap: big.NewInt(200000),
		GasTipCap: big.NewInt(15000),
	})
	require.NoError(t, err)

	// 1. Run MetaNode MVM
	tx, err := transaction.NewTransactionFromEth(signed)
	require.NoError(t, err)
	items := []grouptxns.Item{{ID: 0, Array: grouptxns.BuildDeterministicGroupAddrs(tx), Tx: tx}}
	_, receipts, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, receipts, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, receipts[0].Status())
	mvmResultReturn := receipts[0].Return()

	// 2. Run Reference go-ethereum EVM
	reference, err := state.New(eth.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	reference.AddBalance(sender, uint256.MustFromBig(initialBalance), tracing.BalanceChangeUnspecified)
	reference.SetNonce(sender, 1, tracing.NonceChangeUnspecified)
	reference.SetCode(target, contractCode)
	reference.SetNonce(target, 1, tracing.NonceChangeUnspecified)

	random := common.Hash{}
	cfg := *params.AllDevChainProtocolChanges
	cfg.ChainID = chainID
	baseFee := big.NewInt(mt.MINIMUM_BASE_FEE) // 100,000 wei
	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		GasLimit:    1000000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     baseFee,
		Random:      &random,
	}, reference, &cfg, vm.Config{})

	msg, err := core.TransactionToMessage(signed, signer, baseFee)
	require.NoError(t, err)
	gethResult, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(1000000))
	require.NoError(t, err)
	require.NoError(t, gethResult.Err)

	// 3. Compare GASPRICE returned bytes
	require.Equal(t, gethResult.ReturnData, mvmResultReturn,
		"MVM opcode GASPRICE return bytes must be identical to go-ethereum EVM")

	// 4. Compare storage slot 0 committed
	mvmStored, _ := cs.GetSmartContractDB().StorageValue(target, common.Hash{}.Bytes())
	gethStored := reference.GetState(target, common.Hash{})
	require.Equal(t, gethStored.Bytes(), mvmStored,
		"MVM storage slot 0 must match go-ethereum EVM storage slot 0 exactly")

	t.Logf("Geth differential PASS: GASPRICE=%s", new(big.Int).SetBytes(mvmResultReturn))
}
