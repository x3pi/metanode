package block_signer

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test private keys (from existing bls_test.go)
const (
	testPrivateKey1 = "372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"
	testPrivateKey2 = "4ff2cbecdd0f285fbb3d9b7aa4bf4a31d9b1e01a5dd57e4c5deb0b2dbba91a29"
	testPrivateKey3 = "5e0e5dea64e79d94e2ae9a22f00f91e1aed5c1a16a4483afda4aa3b3e42d8eac"
)

func TestNewBlockSigner(t *testing.T) {
	bls.Init()

	signer, err := NewBlockSigner(testPrivateKey1)
	require.NoError(t, err)
	require.NotNil(t, signer)

	assert.NotEmpty(t, signer.PublicKey(), "public key should not be empty")
	assert.NotEqual(t, common.Address{}, signer.Address(), "address should not be zero")
}

func TestNewBlockSigner_EmptyKey(t *testing.T) {
	signer, err := NewBlockSigner("")
	assert.Error(t, err)
	assert.Nil(t, signer)
}

func TestSignBlockHash(t *testing.T) {
	bls.Init()

	signer, err := NewBlockSigner(testPrivateKey1)
	require.NoError(t, err)

	blockHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	signature := signer.SignBlockHash(blockHash)

	assert.NotEmpty(t, signature, "signature should not be empty")
	assert.Equal(t, 96, len(signature), "BLS compressed signature should be 96 bytes")
}

func TestVerifyBlockSignature_Valid(t *testing.T) {
	bls.Init()

	signer, err := NewBlockSigner(testPrivateKey1)
	require.NoError(t, err)

	blockHash := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000001")
	signature := signer.SignBlockHash(blockHash)

	// Verify with correct pubkey → should pass
	valid := VerifyBlockSignature(blockHash, signature, signer.PublicKey())
	assert.True(t, valid, "signature should be valid with correct public key")
}

func TestVerifyBlockSignature_WrongHash(t *testing.T) {
	bls.Init()

	signer, err := NewBlockSigner(testPrivateKey1)
	require.NoError(t, err)

	blockHash := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000001")
	wrongHash := common.HexToHash("0xcafebabe00000000000000000000000000000000000000000000000000000002")
	signature := signer.SignBlockHash(blockHash)

	// Verify with wrong hash → should fail
	valid := VerifyBlockSignature(wrongHash, signature, signer.PublicKey())
	assert.False(t, valid, "signature should be invalid for wrong block hash")
}

func TestVerifyBlockSignature_WrongPubKey(t *testing.T) {
	bls.Init()

	signer1, err := NewBlockSigner(testPrivateKey1)
	require.NoError(t, err)
	signer2, err := NewBlockSigner(testPrivateKey2)
	require.NoError(t, err)

	blockHash := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000001")
	signature := signer1.SignBlockHash(blockHash)

	// Verify with wrong pubkey → should fail
	valid := VerifyBlockSignature(blockHash, signature, signer2.PublicKey())
	assert.False(t, valid, "signature should be invalid with wrong public key")
}

func TestVerifyBlockSignature_EmptyInputs(t *testing.T) {
	blockHash := common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000001")

	// Empty signature
	assert.False(t, VerifyBlockSignature(blockHash, nil, []byte{1, 2, 3}))
	assert.False(t, VerifyBlockSignature(blockHash, []byte{}, []byte{1, 2, 3}))

	// Empty pubkey
	assert.False(t, VerifyBlockSignature(blockHash, []byte{1, 2, 3}, nil))
	assert.False(t, VerifyBlockSignature(blockHash, []byte{1, 2, 3}, []byte{}))
}

func TestCreateAggregateSignature(t *testing.T) {
	bls.Init()

	signer1, _ := NewBlockSigner(testPrivateKey1)
	signer2, _ := NewBlockSigner(testPrivateKey2)
	signer3, _ := NewBlockSigner(testPrivateKey3)

	blockHash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	sig1 := signer1.SignBlockHash(blockHash)
	sig2 := signer2.SignBlockHash(blockHash)
	sig3 := signer3.SignBlockHash(blockHash)

	// Aggregate 3 signatures into 1
	aggSig, err := CreateAggregateSignature([][]byte{sig1, sig2, sig3})
	require.NoError(t, err)
	assert.NotEmpty(t, aggSig)
	assert.Equal(t, 96, len(aggSig), "aggregate BLS signature should be 96 bytes")
}

func TestCreateAggregateSignature_EmptyInput(t *testing.T) {
	_, err := CreateAggregateSignature(nil)
	assert.Error(t, err)

	_, err = CreateAggregateSignature([][]byte{})
	assert.Error(t, err)
}

func TestNewBlockSignerFromKeyPair(t *testing.T) {
	bls.Init()

	kp := bls.NewKeyPair(common.FromHex(testPrivateKey1))
	signer := NewBlockSignerFromKeyPair(kp)

	assert.NotNil(t, signer)
	assert.Equal(t, kp.Address(), signer.Address())

	// Sign and verify
	blockHash := common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	sig := signer.SignBlockHash(blockHash)
	assert.True(t, VerifyBlockSignature(blockHash, sig, signer.PublicKey()))
}

func TestSignVerify_DifferentBlocks(t *testing.T) {
	bls.Init()

	signer, _ := NewBlockSigner(testPrivateKey1)

	// Sign 10 different block hashes, verify each
	for i := 0; i < 10; i++ {
		hash := common.BigToHash(common.Big1)
		hash[31] = byte(i) // Different hash each time

		sig := signer.SignBlockHash(hash)
		assert.True(t, VerifyBlockSignature(hash, sig, signer.PublicKey()),
			"signature should be valid for block hash variant %d", i)
	}
}
