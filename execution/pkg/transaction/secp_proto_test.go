package transaction

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secp256k1 curve order N
var secp256k1N = crypto.S256().Params().N

func TestSigningHash_InvariantWithSignature(t *testing.T) {
	tx := &Transaction{
		proto: &pb.Transaction{
			FromAddress: common.HexToAddress("0x1111111111111111111111111111111111111111").Bytes(),
			ToAddress:   common.HexToAddress("0x2222222222222222222222222222222222222222").Bytes(),
			Amount:      big.NewInt(1000).Bytes(),
			Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 1},
			ChainID:     1337,
			Type:        0xFF,
		},
	}

	// SigningHash before filling R, S, V
	h1 := tx.SigningHash()
	require.NotEmpty(t, h1)

	// Hash before filling R, S, V equals SigningHash because R,S,V are nil
	require.Equal(t, h1, tx.Hash())

	// Now populate R, S, V
	tx.proto.R = make([]byte, 32)
	tx.proto.R[31] = 0xAA
	tx.proto.S = make([]byte, 32)
	tx.proto.S[31] = 0xBB
	tx.proto.V = []byte{1}
	tx.ClearCacheHash()

	// SigningHash MUST remain invariant
	h2 := tx.SigningHash()
	assert.Equal(t, h1, h2, "SigningHash must remain identical after filling R, S, V")

	// Hash MUST change because it commits to the signature
	hashWithSig := tx.Hash()
	assert.NotEqual(t, h1, hashWithSig, "Hash() must differ from SigningHash() once signature is present")
}

func TestValidSecpProtoSign_Success(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(privKey.PublicKey)
	toAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")

	tx := &Transaction{
		proto: &pb.Transaction{
			FromAddress: fromAddr.Bytes(),
			ToAddress:   toAddr.Bytes(),
			Amount:      big.NewInt(5000000000).Bytes(),
			Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 0},
			MaxGas:      21000,
			MaxGasPrice: 20000000000,
			ChainID:     1337,
			Type:        0xFF,
		},
	}

	err = tx.SignSecpProto(privKey)
	require.NoError(t, err)

	assert.Equal(t, uint64(0xFF), tx.Type())
	assert.Len(t, tx.proto.R, 32)
	assert.Len(t, tx.proto.S, 32)
	assert.Len(t, tx.proto.V, 1)
	assert.Empty(t, tx.proto.Sign)

	assert.True(t, tx.ValidSecpProtoSign(), "ValidSecpProtoSign must succeed for valid signature")
	assert.True(t, tx.ValidSecpSign(), "ValidSecpSign must succeed for valid Type 0xFF signature")
}

func TestValidSecpProtoSign_TamperFailures(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(privKey.PublicKey)
	toAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")

	makeTx := func() *Transaction {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ToAddress:   toAddr.Bytes(),
				Amount:      big.NewInt(100).Bytes(),
				Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 1},
				MaxGas:      21000,
				MaxGasPrice: 20000000000,
				ChainID:     1337,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		return tx
	}

	t.Run("Tamper Amount", func(t *testing.T) {
		tx := makeTx()
		tx.proto.Amount = big.NewInt(200).Bytes()
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper Nonce", func(t *testing.T) {
		tx := makeTx()
		tx.proto.Nonce = []byte{0, 0, 0, 0, 0, 0, 0, 2}
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper Data", func(t *testing.T) {
		tx := makeTx()
		tx.proto.Data = []byte{0xDE, 0xAD, 0xBE, 0xEF}
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper ToAddress", func(t *testing.T) {
		tx := makeTx()
		tx.proto.ToAddress = common.HexToAddress("0x8888888888888888888888888888888888888888").Bytes()
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper FromAddress", func(t *testing.T) {
		tx := makeTx()
		tx.proto.FromAddress = common.HexToAddress("0x7777777777777777777777777777777777777777").Bytes()
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper ChainID", func(t *testing.T) {
		tx := makeTx()
		tx.proto.ChainID = 9999
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper Signature R", func(t *testing.T) {
		tx := makeTx()
		tx.proto.R[0] ^= 0xFF
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper Signature S", func(t *testing.T) {
		tx := makeTx()
		tx.proto.S[0] ^= 0xFF
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Tamper Signature V", func(t *testing.T) {
		tx := makeTx()
		tx.proto.V[0] ^= 1
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})
}

func TestValidSecpProtoSign_StructuralRejections(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	t.Run("Type not 0xFF", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     1337,
				Type:        0x00,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		tx.proto.Type = 0x00
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("Sign field not empty", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     1337,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		tx.proto.Sign = []byte{1, 2, 3} // Non-empty BLS field
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign(), "Type 0xFF must reject non-empty Sign field")
	})

	t.Run("R not 32 bytes", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     1337,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		tx.proto.R = tx.proto.R[1:] // 31 bytes
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("S not 32 bytes", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     1337,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		tx.proto.S = append(tx.proto.S, 0x00) // 33 bytes
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("V not 1 byte", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     1337,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		tx.proto.V = []byte{0, 1} // 2 bytes
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("V greater than 1", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     1337,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		tx.proto.V = []byte{27} // Ethereum legacy recovery ID format is invalid for proto
		tx.ClearCacheHash()
		assert.False(t, tx.ValidSecpProtoSign())
	})

	t.Run("ChainID is zero", func(t *testing.T) {
		tx := &Transaction{
			proto: &pb.Transaction{
				FromAddress: fromAddr.Bytes(),
				ChainID:     0,
				Type:        0xFF,
			},
		}
		require.NoError(t, tx.SignSecpProto(privKey))
		assert.False(t, tx.ValidSecpProtoSign(), "Zero ChainID must be rejected")
	})
}

func TestValidSecpProtoSign_HighSRejection(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	tx := &Transaction{
		proto: &pb.Transaction{
			FromAddress: fromAddr.Bytes(),
			ChainID:     1337,
			Type:        0xFF,
		},
	}
	require.NoError(t, tx.SignSecpProto(privKey))
	require.True(t, tx.ValidSecpProtoSign())

	// Flip s to high-s: s' = N - s, v' = v ^ 1
	origS := new(big.Int).SetBytes(tx.proto.S)
	highS := new(big.Int).Sub(secp256k1N, origS)
	highSBytes := highS.Bytes()
	if len(highSBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(highSBytes):], highSBytes)
		highSBytes = padded
	}

	tx.proto.S = highSBytes
	tx.proto.V[0] ^= 1
	tx.ClearCacheHash()

	// Even though ECDSA mathematically recovers fromAddr with (N-s, v^1),
	// high-s is malleable and must be rejected per EIP-2 / homestead rule.
	assert.False(t, tx.ValidSecpProtoSign(), "High-s signature must be rejected to prevent malleability")
}

func TestRoundTripProto_MaintainsHashAndValidity(t *testing.T) {
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	fromAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	tx := &Transaction{
		proto: &pb.Transaction{
			FromAddress: fromAddr.Bytes(),
			ToAddress:   common.HexToAddress("0xabcdef1234567890abcdef1234567890abcdef12").Bytes(),
			Amount:      big.NewInt(12345678).Bytes(),
			Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 5},
			MaxGas:      50000,
			MaxGasPrice: 30000000000,
			ChainID:     42,
			Type:        0xFF,
		},
	}
	require.NoError(t, tx.SignSecpProto(privKey))

	origHash := tx.Hash()
	origSigningHash := tx.SigningHash()

	rawBytes, err := tx.Marshal()
	require.NoError(t, err)

	unmarshaledTx := &Transaction{}
	err = unmarshaledTx.Unmarshal(rawBytes)
	require.NoError(t, err)

	assert.Equal(t, origHash, unmarshaledTx.Hash(), "Hash must survive round-trip marshal/unmarshal")
	assert.Equal(t, origSigningHash, unmarshaledTx.SigningHash(), "SigningHash must survive round-trip")
	assert.True(t, unmarshaledTx.ValidSecpProtoSign(), "Unmarshaled transaction must remain valid")
}
