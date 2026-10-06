package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// TestFeeDeduction_AllTypesAndWallets explicitly verifies ADR D2 fee semantics:
// EffectiveGasPrice = min(maxFee, F + tip) where F = MINIMUM_BASE_FEE (100,000) for EIP-1559/4844/7702,
// and flat gasPrice for Legacy / EIP-2930.
// Verifies that:
// 1. EffectiveGasPrice matches ADR D2 formula.
// 2. Sender balance is deducted by exactly (value + gasUsed * EffectiveGasPrice).
// 3. Sender is NOT overcharged by maxFee when maxFee > EffectiveGasPrice.
// 4. Leader receives exactly (gasUsed * EffectiveGasPrice).
func TestFeeDeduction_AllTypesAndWallets(t *testing.T) {
	chainID := big.NewInt(991)
	signer := eth_types.NewPragueSigner(chainID)
	cancunSigner := eth_types.NewCancunSigner(chainID)

	testCases := []struct {
		name                string
		buildTx             func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction
		expectedEffGasPrice *big.Int
		expectedBlobFee     *big.Int
	}{
		{
			name: "Type 0 (Legacy): flat gasPrice",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				txData := &eth_types.LegacyTx{
					Nonce:    nonce,
					GasPrice: big.NewInt(125_000),
					Gas:      mt_common.TRANSFER_GAS_COST,
					To:       &to,
					Value:    value,
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(125_000),
		},
		{
			name: "Type 1 (EIP-2930 AccessList): flat gasPrice",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				txData := &eth_types.AccessListTx{
					ChainID:  chainID,
					Nonce:    nonce,
					GasPrice: big.NewInt(150_000),
					Gas:      mt_common.TRANSFER_GAS_COST,
					To:       &to,
					Value:    value,
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(150_000),
		},
		{
			name: "Type 2 (EIP-1559): tip below cap (F + tip < maxFee)",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				// F = 100,000, Tip = 15,000 => F + tip = 115,000 < MaxFee = 200,000
				txData := &eth_types.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(15_000),
					GasFeeCap: big.NewInt(200_000),
					Gas:       mt_common.TRANSFER_GAS_COST,
					To:        &to,
					Value:     value,
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(115_000), // 100,000 + 15,000
		},
		{
			name: "Type 2 (EIP-1559): tip capped by maxFee (F + tip > maxFee)",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				// F = 100,000, Tip = 50,000, MaxFee = 108,000 => min(108,000, 150,000) = 108,000
				txData := &eth_types.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(50_000),
					GasFeeCap: big.NewInt(108_000),
					Gas:       mt_common.TRANSFER_GAS_COST,
					To:        &to,
					Value:     value,
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(108_000),
		},
		{
			name: "Type 2 (viem default): zero tip => pays exactly F = 100,000",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				// viem default: tip = 0, maxFee = 200,000
				txData := &eth_types.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(0),
					GasFeeCap: big.NewInt(200_000),
					Gas:       mt_common.TRANSFER_GAS_COST,
					To:        &to,
					Value:     value,
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(mt_common.MINIMUM_BASE_FEE), // 100,000
		},
		{
			name: "Type 2 (ethers default): tip = 1e8, maxFee = 1e9 => pays min(maxFee, F + tip)",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				// ethers default: tip = 100,000,000, maxFee = 1,000,000,000 => F + tip = 100,100,000
				txData := &eth_types.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(100_000_000),
					GasFeeCap: big.NewInt(1_000_000_000),
					Gas:       mt_common.TRANSFER_GAS_COST,
					To:        &to,
					Value:     value,
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(100_100_000), // 100,000 + 100,000,000
		},
		{
			name: "Type 3 (EIP-4844 BlobTx): tip = 20,000, maxFee = 300,000 => pays F + tip",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				var blob kzg4844.Blob
				c, err := kzg4844.BlobToCommitment(&blob)
				require.NoError(t, err)
				proof, err := kzg4844.ComputeBlobProof(&blob, c)
				require.NoError(t, err)
				vh := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &c))

				txData := &eth_types.BlobTx{
					ChainID:    uint256.MustFromBig(chainID),
					Nonce:      nonce,
					GasTipCap:  uint256.NewInt(20_000),
					GasFeeCap:  uint256.NewInt(300_000),
					Gas:        100_000,
					To:         to,
					Value:      uint256.MustFromBig(value),
					BlobFeeCap: uint256.NewInt(1),
					BlobHashes: []common.Hash{vh},
					Sidecar: &eth_types.BlobTxSidecar{
						Blobs:       []kzg4844.Blob{blob},
						Commitments: []kzg4844.Commitment{c},
						Proofs:      []kzg4844.Proof{proof},
					},
				}
				tx, err := eth_types.SignNewTx(key, cancunSigner, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(120_000), // 100,000 + 20,000
			expectedBlobFee:     big.NewInt(131072),  // 1 blob * 131,072 gas * 1 wei blobBaseFee (burned)
		},
		{
			name: "Type 4 (EIP-7702 SetCode): tip = 5,000, maxFee = 250,000 => pays F + tip",
			buildTx: func(t *testing.T, key *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int) *eth_types.Transaction {
				authKey, _ := crypto.GenerateKey()
				auth, err := eth_types.SignSetCode(authKey, eth_types.SetCodeAuthorization{
					ChainID: *uint256.MustFromBig(chainID),
					Address: common.HexToAddress("0x7702000000000000000000000000000000007702"),
					Nonce:   0,
				})
				require.NoError(t, err)

				txData := &eth_types.SetCodeTx{
					ChainID:   uint256.MustFromBig(chainID),
					Nonce:     nonce,
					GasTipCap: uint256.NewInt(5_000),
					GasFeeCap: uint256.NewInt(250_000),
					Gas:       100_000, // EIP-7702 has 46,000+ intrinsic gas
					To:        to,
					Value:     uint256.MustFromBig(value),
					AuthList:  []eth_types.SetCodeAuthorization{auth},
				}
				tx, err := eth_types.SignNewTx(key, signer, txData)
				require.NoError(t, err)
				return tx
			},
			expectedEffGasPrice: big.NewInt(105_000), // 100,000 + 5,000
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestChainState(t)
			cs.SetConfig(&config.SimpleChainConfig{
				ChainId:         chainID,
				TxSignatureMode: config.TxSignatureModeSecp,
			})

			senderKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			senderAddr := crypto.PubkeyToAddress(senderKey.PublicKey)

			recipientKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			recipientAddr := crypto.PubkeyToAddress(recipientKey.PublicKey)

			leaderAddr := common.HexToAddress("0xFEED00000000000000000000000000000000FEED")

			initialSenderBalance := big.NewInt(100_000_000_000_000)
			transferAmount := big.NewInt(1_000_000_000)

			seedAccount(t, cs, senderAddr, initialSenderBalance, 0)
			seedAccount(t, cs, recipientAddr, big.NewInt(0), 0)
			seedAccount(t, cs, leaderAddr, big.NewInt(0), 0)

			// Build and sign eth tx
			ethTx := tc.buildTx(t, senderKey, 0, recipientAddr, transferAmount)

			// Convert to MetaTx
			metaTx, err := transaction.NewTransactionFromEth(ethTx)
			require.NoError(t, err)

			// 1. Verify EffectiveGasPrice formula
			require.Equal(t, tc.expectedEffGasPrice, metaTx.EffectiveGasPrice(), "EffectiveGasPrice mismatch for %s", tc.name)

			// 2. Process via TrueBlockSTM
			stm := NewTrueBlockSTM([]mt_types.Transaction{metaTx})
			execTxs, execRcps, _, _ := stm.Process(context.Background(), cs, leaderAddr, blankHeader(), 1)
			require.Len(t, execTxs, 1)
			require.Len(t, execRcps, 1)
			require.Equal(t, pb.RECEIPT_STATUS_RETURNED, execRcps[0].Status())

			// 3. Verify balance deductions
			gasUsed := execRcps[0].GasUsed()
			require.Greater(t, gasUsed, uint64(0))

			expectedGasFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), tc.expectedEffGasPrice)

			// Sender balance = initial - transferAmount - expectedGasFee
			senderState, err := cs.GetAccountStateDB().AccountStateReadOnly(senderAddr)
			require.NoError(t, err)
			expectedSenderBalance := new(big.Int).Sub(initialSenderBalance, transferAmount)
			expectedSenderBalance.Sub(expectedSenderBalance, expectedGasFee)
			if tc.expectedBlobFee != nil {
				expectedSenderBalance.Sub(expectedSenderBalance, tc.expectedBlobFee)
			}
			assert.Equal(t, expectedSenderBalance, senderState.TotalBalance(), "Sender balance must match (initial - transfer - gasFee)")

			// Recipient balance = transferAmount
			recipientState, err := cs.GetAccountStateDB().AccountStateReadOnly(recipientAddr)
			require.NoError(t, err)
			assert.Equal(t, transferAmount, recipientState.TotalBalance(), "Recipient balance must match transferAmount")

			// Leader balance = expectedGasFee
			leaderState, err := cs.GetAccountStateDB().AccountStateReadOnly(leaderAddr)
			require.NoError(t, err)
			assert.Equal(t, expectedGasFee, leaderState.TotalBalance(), "Leader must receive exactly the effective gas fee")
		})
	}
}
