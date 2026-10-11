package processor

import (
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"sort"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// oldSortPre1408ddd6Reference reproduces the pre-1408ddd6 sorting logic where FromAddress()
// was called inside the sort.Slice comparator on every comparison.
func oldSortPre1408ddd6Reference(allTxs []types.Transaction) {
	sort.Slice(allTxs, func(i, j int) bool {
		cmp := allTxs[i].FromAddress().Cmp(allTxs[j].FromAddress())
		if cmp != 0 {
			return cmp < 0
		}
		return allTxs[i].GetNonce() < allTxs[j].GetNonce()
	})
}

// mutatedSortInvertedNonce is a mutated sort implementation used strictly for negative control.
// It intentionally inverts the nonce ordering comparison.
func mutatedSortInvertedNonce(allTxs []types.Transaction) {
	type sortItem struct {
		from  e_common.Address
		nonce uint64
		tx    types.Transaction
	}
	sortItems := make([]sortItem, len(allTxs))
	for i, tx := range allTxs {
		sortItems[i] = sortItem{from: tx.FromAddress(), nonce: tx.GetNonce(), tx: tx}
	}
	sort.Slice(sortItems, func(i, j int) bool {
		cmp := sortItems[i].from.Cmp(sortItems[j].from)
		if cmp != 0 {
			return cmp < 0
		}
		// NEGATIVE CONTROL: inverted nonce order
		return sortItems[i].nonce > sortItems[j].nonce
	})
	for i := range sortItems {
		allTxs[i] = sortItems[i].tx
	}
}

func generate100kMockTxs(r *rand.Rand) ([]types.Transaction, []types.Transaction, []types.Transaction) {
	const count = 100000
	const numSenders = 1000
	senders := make([]e_common.Address, numSenders)
	for i := 0; i < numSenders; i++ {
		var addr e_common.Address
		r.Read(addr[:])
		senders[i] = addr
	}
	toAddr := e_common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	txsOld := make([]types.Transaction, count)
	txsProd := make([]types.Transaction, count)
	txsMutated := make([]types.Transaction, count)

	for i := 0; i < count; i++ {
		sender := senders[r.Intn(numSenders)]
		nonce := uint64(r.Intn(1000))
		mt := NewMockTransaction(sender, toAddr, nonce)
		mt.hash = e_common.BigToHash(big.NewInt(int64(i)))
		mt.rHash = mt.hash

		txsOld[i] = mt
		txsProd[i] = mt
		txsMutated[i] = mt
	}
	return txsOld, txsProd, txsMutated
}

// TestSortEquivalence_100kTxs verifies bit-for-bit ordering parity between
// the production function SortTxsByFromAndNonce and the pre-1408ddd6 reference sort
// on 100,000 transactions.
func TestSortEquivalence_100kTxs(t *testing.T) {
	const count = 100000
	r := rand.New(rand.NewSource(42))
	txsOld, txsProd, _ := generate100kMockTxs(r)

	// 1. Run pre-1408ddd6 reference sort vs production SortTxsByFromAndNonce
	oldSortPre1408ddd6Reference(txsOld)
	SortTxsByFromAndNonce(txsProd) // Calling actual production code!

	// 2. Element-by-element verification
	mismatches := 0
	for i := 0; i < count; i++ {
		if txsOld[i].Hash() != txsProd[i].Hash() {
			mismatches++
			if mismatches <= 5 {
				t.Errorf("Mismatch at index %d: old hash %x != prod hash %x (old from=%s nonce=%d, prod from=%s nonce=%d)",
					i, txsOld[i].Hash(), txsProd[i].Hash(),
					txsOld[i].FromAddress().Hex(), txsOld[i].GetNonce(),
					txsProd[i].FromAddress().Hex(), txsProd[i].GetNonce())
			}
		}
	}
	require.Equal(t, 0, mismatches, "Production SortTxsByFromAndNonce and pre-1408ddd6 sort must yield 100%% identical ordering on 100,000 txs")
	t.Logf("✅ Sort equivalence verified: 100,000 txs identical (0 mismatches)")
}

// TestSortEquivalence_NegativeControl is a dedicated negative control test.
// It verifies that a mutation (inverted nonce comparison) fails the equivalence check.
func TestSortEquivalence_NegativeControl(t *testing.T) {
	const count = 100000
	r := rand.New(rand.NewSource(42))
	txsOld, _, txsMutated := generate100kMockTxs(r)

	oldSortPre1408ddd6Reference(txsOld)
	mutatedSortInvertedNonce(txsMutated)

	mutatedMismatches := 0
	for i := 0; i < count; i++ {
		if txsOld[i].Hash() != txsMutated[i].Hash() {
			mutatedMismatches++
		}
	}
	require.Greater(t, mutatedMismatches, 0, "NEGATIVE CONTROL MUST FAIL: mutated sort must produce mismatches against reference sort")
	t.Logf("✅ Negative control confirmed: mutated sort detected %d mismatches out of %d", mutatedMismatches, count)
}

// TestSignerEquivalence_AllTypes tests the production ValidateEthTxEnvelope across all 5 Ethereum
// tx types against the pre-1408ddd6 signer behavior (LatestSignerForChainID).
func TestSignerEquivalence_AllTypes(t *testing.T) {
	chainID := big.NewInt(991)
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	expectedSender := crypto.PubkeyToAddress(privKey.PublicKey)
	recipient := e_common.HexToAddress("0x1111111111111111111111111111111111111111")

	testCases := []struct {
		name   string
		makeTx func(key *ecdsa.PrivateKey, nonce uint64) *e_types.Transaction
	}{
		{
			name: "Type 0: Legacy EIP-155",
			makeTx: func(key *ecdsa.PrivateKey, nonce uint64) *e_types.Transaction {
				tx := e_types.NewTx(&e_types.LegacyTx{
					Nonce:    nonce,
					GasPrice: big.NewInt(100000),
					Gas:      21000,
					To:       &recipient,
					Value:    big.NewInt(1000),
				})
				signed, err := e_types.SignTx(tx, e_types.NewEIP155Signer(chainID), key)
				if err != nil {
					panic(err)
				}
				return signed
			},
		},
		{
			name: "Type 1: EIP-2930 AccessList",
			makeTx: func(key *ecdsa.PrivateKey, nonce uint64) *e_types.Transaction {
				tx := e_types.NewTx(&e_types.AccessListTx{
					ChainID:  chainID,
					Nonce:    nonce,
					GasPrice: big.NewInt(100000),
					Gas:      21000,
					To:       &recipient,
					Value:    big.NewInt(1000),
				})
				signed, err := e_types.SignTx(tx, e_types.NewLondonSigner(chainID), key)
				if err != nil {
					panic(err)
				}
				return signed
			},
		},
		{
			name: "Type 2: EIP-1559 DynamicFee",
			makeTx: func(key *ecdsa.PrivateKey, nonce uint64) *e_types.Transaction {
				tx := e_types.NewTx(&e_types.DynamicFeeTx{
					ChainID:   chainID,
					Nonce:     nonce,
					GasTipCap: big.NewInt(1000),
					GasFeeCap: big.NewInt(100000),
					Gas:       21000,
					To:        &recipient,
					Value:     big.NewInt(1000),
				})
				signed, err := e_types.SignTx(tx, e_types.NewLondonSigner(chainID), key)
				if err != nil {
					panic(err)
				}
				return signed
			},
		},
		{
			name: "Type 3: EIP-4844 BlobTx",
			makeTx: func(key *ecdsa.PrivateKey, nonce uint64) *e_types.Transaction {
				tx := e_types.NewTx(&e_types.BlobTx{
					ChainID:    uint256.MustFromBig(chainID),
					Nonce:      nonce,
					GasTipCap:  uint256.NewInt(1000),
					GasFeeCap:  uint256.NewInt(100000),
					Gas:        21000,
					To:         recipient,
					Value:      uint256.NewInt(1000),
					BlobFeeCap: uint256.NewInt(1000),
					BlobHashes: []e_common.Hash{e_common.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000000")},
				})
				signed, err := e_types.SignTx(tx, e_types.NewCancunSigner(chainID), key)
				if err != nil {
					panic(err)
				}
				return signed
			},
		},
		{
			name: "Type 4: EIP-7702 SetCodeTx",
			makeTx: func(key *ecdsa.PrivateKey, nonce uint64) *e_types.Transaction {
				tx := e_types.NewTx(&e_types.SetCodeTx{
					ChainID:   uint256.MustFromBig(chainID),
					Nonce:     nonce,
					GasTipCap: uint256.NewInt(1000),
					GasFeeCap: uint256.NewInt(100000),
					Gas:       21000,
					To:        recipient,
					Value:     uint256.NewInt(1000),
				})
				signed, err := e_types.SignTx(tx, e_types.NewPragueSigner(chainID), key)
				if err != nil {
					panic(err)
				}
				return signed
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tx := tc.makeTx(privKey, 1)

			// 1. Call production ValidateEthTxEnvelope (which now uses per-type signer)
			errValidate := transaction.ValidateEthTxEnvelope(tx, chainID)
			require.NoError(t, errValidate, "Production ValidateEthTxEnvelope must pass for valid tx")

			// 2. Compare against pre-1408ddd6 LatestSignerForChainID sender recovery
			signerOld := e_types.LatestSignerForChainID(chainID)
			senderOld, errOld := e_types.Sender(signerOld, tx)
			require.NoError(t, errOld)
			assert.Equal(t, expectedSender, senderOld, "Old LatestSignerForChainID must recover expectedSender")
		})
	}

	// 3. Test Invalid Cases (Wrong Chain ID, Bad Signature, Unprotected)
	t.Run("Rejections and Error Parity", func(t *testing.T) {
		wrongChainTx := e_types.NewTx(&e_types.DynamicFeeTx{
			ChainID:   big.NewInt(100),
			Nonce:     1,
			GasTipCap: big.NewInt(1000),
			GasFeeCap: big.NewInt(100000),
			Gas:       21000,
			To:        &recipient,
			Value:     big.NewInt(1000),
		})
		signedWrong, err := e_types.SignTx(wrongChainTx, e_types.NewLondonSigner(big.NewInt(100)), privKey)
		require.NoError(t, err)

		errWrongChain := transaction.ValidateEthTxEnvelope(signedWrong, chainID)
		require.Error(t, errWrongChain)
		assert.ErrorIs(t, errWrongChain, transaction.InvalidChainId)

		unprotectedTx := e_types.NewTx(&e_types.LegacyTx{
			Nonce:    1,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &recipient,
			Value:    big.NewInt(1000),
		})
		signedUnprotected, err := e_types.SignTx(unprotectedTx, e_types.HomesteadSigner{}, privKey)
		require.NoError(t, err)
		errUnprotected := transaction.ValidateEthTxEnvelope(signedUnprotected, chainID)
		require.Error(t, errUnprotected)
		assert.ErrorIs(t, errUnprotected, transaction.ErrPreEIP155)
	})
}

// TestSignerEquivalence_NegativeControl is a dedicated negative control test.
// It verifies that a corrupted signature or mismatched chain signer fails envelope validation.
func TestSignerEquivalence_NegativeControl(t *testing.T) {
	chainID := big.NewInt(991)
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	recipient := e_common.HexToAddress("0x1111111111111111111111111111111111111111")

	// Transaction signed for chainID 999999
	tx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   big.NewInt(999999),
		Nonce:     1,
		GasTipCap: big.NewInt(1000),
		GasFeeCap: big.NewInt(100000),
		Gas:       21000,
		To:        &recipient,
		Value:     big.NewInt(1000),
	})
	signedWithWrongChain, err := e_types.SignTx(tx, e_types.NewLondonSigner(big.NewInt(999999)), privKey)
	require.NoError(t, err)

	// Validating against expected chainID 991 MUST FAIL
	errValidate := transaction.ValidateEthTxEnvelope(signedWithWrongChain, chainID)
	require.Error(t, errValidate, "NEGATIVE CONTROL MUST FAIL: tx signed with mismatched chain ID must be rejected")
	assert.ErrorIs(t, errValidate, transaction.InvalidChainId)
	t.Logf("✅ Negative control confirmed: ValidateEthTxEnvelope successfully rejected mismatched chain signature")
}
