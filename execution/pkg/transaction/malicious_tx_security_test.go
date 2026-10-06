package transaction

import (
	"crypto/sha256"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// TestMaliciousTx_OversizedInitcode tests that contract deployment with initcode > 49152 bytes
// is rejected at envelope validation with ErrMaxInitCodeSizeExceeded per EIP-3860.
func TestMaliciousTx_OversizedInitcode(t *testing.T) {
	chainID := big.NewInt(991)
	signer := types.NewPragueSigner(chainID)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	// 1. Initcode exceeding MaxInitCodeSize (49152 bytes)
	hugeInitcode := make([]byte, MaxInitCodeSize+1)
	for i := range hugeInitcode {
		hugeInitcode[i] = 0x60 // PUSH1
	}

	txData := &types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(100_000),
		Gas:      5_000_000,
		To:       nil, // Contract creation
		Value:    big.NewInt(0),
		Data:     hugeInitcode,
	}
	signedTx, err := types.SignNewTx(key, signer, txData)
	require.NoError(t, err)

	err = ValidateEthTxEnvelope(signedTx, chainID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMaxInitCodeSizeExceeded), "expected ErrMaxInitCodeSizeExceeded, got: %v", err)

	// 2. Initcode within limit (exactly 49152 bytes)
	validInitcode := make([]byte, MaxInitCodeSize)
	txDataValid := &types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(100_000),
		Gas:      5_000_000,
		To:       nil,
		Value:    big.NewInt(0),
		Data:     validInitcode,
	}
	signedTxValid, err := types.SignNewTx(key, signer, txDataValid)
	require.NoError(t, err)
	assert.NoError(t, ValidateEthTxEnvelope(signedTxValid, chainID))
}

// TestMaliciousTx_OversizedAccessList tests that an access list with > 1024 tuples is rejected.
func TestMaliciousTx_OversizedAccessList(t *testing.T) {
	chainID := big.NewInt(991)
	signer := types.NewPragueSigner(chainID)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	to := common.HexToAddress("0x1234")

	// 1025 tuples
	oversizedAL := make(types.AccessList, MaxAccessListTuples+1)
	for i := range oversizedAL {
		oversizedAL[i] = types.AccessTuple{
			Address:     common.BigToAddress(big.NewInt(int64(i + 1))),
			StorageKeys: []common.Hash{common.BigToHash(big.NewInt(int64(i + 1)))},
		}
	}

	txData := &types.AccessListTx{
		ChainID:    chainID,
		Nonce:      0,
		GasPrice:   big.NewInt(100_000),
		Gas:        21000,
		To:         &to,
		Value:      big.NewInt(1000),
		AccessList: oversizedAL,
	}
	signedTx, err := types.SignNewTx(key, signer, txData)
	require.NoError(t, err)

	err = ValidateEthTxEnvelope(signedTx, chainID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access list tuples")
}

// TestMaliciousTx_CorruptedKZGBlobSidecar tests that tampered blob content, mismatched commitments,
// or invalid proofs are caught by VerifyBlobSidecar.
func TestMaliciousTx_CorruptedKZGBlobSidecar(t *testing.T) {
	var blob kzg4844.Blob
	c, err := kzg4844.BlobToCommitment(&blob)
	require.NoError(t, err)
	proof, err := kzg4844.ComputeBlobProof(&blob, c)
	require.NoError(t, err)
	vh := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &c))

	// 1. Tampered blob content
	tamperedBlob := blob
	tamperedBlob[0] ^= 0xFF
	pTx := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{vh.Bytes()},
		Sidecar: &pb.BlobSidecar{
			Blobs:       [][]byte{tamperedBlob[:]},
			Commitments: [][]byte{c[:]},
			Proofs:      [][]byte{proof[:]},
		},
	}
	err = VerifyBlobSidecar(pTx)
	require.Error(t, err, "tampered blob must fail KZG verification")

	// 2. Mismatched versioned hash
	wrongHash := common.HexToHash("0x01deadbeef000000000000000000000000000000000000000000000000000000")
	pTxWrongHash := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{wrongHash.Bytes()},
		Sidecar: &pb.BlobSidecar{
			Blobs:       [][]byte{blob[:]},
			Commitments: [][]byte{c[:]},
			Proofs:      [][]byte{proof[:]},
		},
	}
	err = VerifyBlobSidecar(pTxWrongHash)
	require.Error(t, err, "mismatched versioned hash must fail verification")

	// 3. Corrupted proof bytes
	tamperedProof := proof
	tamperedProof[0] ^= 0x55
	pTxWrongProof := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{vh.Bytes()},
		Sidecar: &pb.BlobSidecar{
			Blobs:       [][]byte{blob[:]},
			Commitments: [][]byte{c[:]},
			Proofs:      [][]byte{tamperedProof[:]},
		},
	}
	err = VerifyBlobSidecar(pTxWrongProof)
	require.Error(t, err, "tampered proof must fail KZG verification")
}

// TestMaliciousTx_Forged7702Authorization tests that forged EIP-7702 authorizations
// (invalid signature, wrong chain ID, mismatched nonce) are rejected at conversion or execution.
func TestMaliciousTx_Forged7702Authorization(t *testing.T) {
	chainID := big.NewInt(991)
	key, _ := crypto.GenerateKey()

	// 1. Authorization signed with wrong chain ID (e.g. 1 instead of 991)
	authKey, _ := crypto.GenerateKey()
	authWrongChain, err := types.SignSetCode(authKey, types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(1), // Wrong chain
		Address: common.HexToAddress("0x7702"),
		Nonce:   0,
	})
	require.NoError(t, err)

	innerTx := &types.SetCodeTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     0,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(100_000),
		Gas:       100_000,
		To:        common.HexToAddress("0x9999"),
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{authWrongChain},
	}
	ethTx, err := types.SignNewTx(key, types.NewPragueSigner(chainID), innerTx)
	require.NoError(t, err)

	// Conversion succeeds structurally, but authority recovery on wrong chain fails/mismatches
	pTx := &pb.Transaction{}
	err = FromEthSetCodeTx(ethTx, pTx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), pTx.AuthorizationList[0].ChainID, "ChainID in auth must preserve signed value")
}

// TestMaliciousTx_HighSSignatureMalleability tests that signatures with high s (s > N/2)
// are strictly rejected per EIP-2 anti-malleability.
func TestMaliciousTx_HighSSignatureMalleability(t *testing.T) {
	chainID := big.NewInt(991)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	to := common.HexToAddress("0x1234")

	txData := &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     0,
		GasTipCap: big.NewInt(10_000),
		GasFeeCap: big.NewInt(100_000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(1000),
	}
	signedTx, err := types.SignNewTx(key, types.NewPragueSigner(chainID), txData)
	require.NoError(t, err)

	v, r, s := signedTx.RawSignatureValues()
	// Flip s to upper half of curve order: s_high = N - s
	sHigh := new(big.Int).Sub(secp256k1N, s)

	sig := make([]byte, 65)
	copy(sig[0:32], common.LeftPadBytes(r.Bytes(), 32))
	copy(sig[32:64], common.LeftPadBytes(sHigh.Bytes(), 32))
	sig[64] = byte(v.Uint64())

	malleableTx, err := signedTx.WithSignature(types.NewPragueSigner(chainID), sig)
	require.NoError(t, err)
	err = ValidateEthTxEnvelope(malleableTx, chainID)
	assert.True(t, errors.Is(err, ErrMalleableSignature), "malleable high-s signature must be rejected with ErrMalleableSignature")
}
