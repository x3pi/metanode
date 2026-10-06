package transaction

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestValidateEthTxEnvelope_ValidTypes(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)

	// 1. EIP-155 Legacy
	legacyTx := e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x01},
		Value:    big.NewInt(100),
		Data:     nil,
	})
	signedLegacy, err := e_types.SignTx(legacyTx, signer, key)
	require.NoError(t, err)
	require.NoError(t, ValidateEthTxEnvelope(signedLegacy, chainID))

	// 2. EIP-2930 AccessList
	eip2930Tx := e_types.NewTx(&e_types.AccessListTx{
		ChainID:  chainID,
		Nonce:    2,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x02},
		Value:    big.NewInt(200),
	})
	signed2930, err := e_types.SignTx(eip2930Tx, signer, key)
	require.NoError(t, err)
	require.NoError(t, ValidateEthTxEnvelope(signed2930, chainID))

	// 3. EIP-1559 DynamicFee
	eip1559Tx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     3,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x03},
		Value:     big.NewInt(300),
	})
	signed1559, err := e_types.SignTx(eip1559Tx, signer, key)
	require.NoError(t, err)
	require.NoError(t, ValidateEthTxEnvelope(signed1559, chainID))
}

func TestValidateEthTxEnvelope_Nil(t *testing.T) {
	err := ValidateEthTxEnvelope(nil, big.NewInt(991))
	require.Error(t, err)
	require.Contains(t, err.Error(), "transaction is nil")
}

func TestValidateEthTxEnvelope_PreEIP155(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	legacyTx := e_types.NewTx(&e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(100000),
		Gas:      21000,
		To:       &common.Address{0x01},
		Value:    big.NewInt(100),
	})
	// Sign with HomesteadSigner (no EIP-155 chain ID)
	signedHomestead, err := e_types.SignTx(legacyTx, e_types.HomesteadSigner{}, key)
	require.NoError(t, err)
	require.False(t, signedHomestead.Protected())

	err = ValidateEthTxEnvelope(signedHomestead, big.NewInt(991))
	require.Error(t, err)
	require.Contains(t, err.Error(), "pre-EIP-155 unprotected")
}

func TestValidateEthTxEnvelope_WrongChainID(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(991)
	wrongChainID := big.NewInt(992)
	signer := e_types.LatestSignerForChainID(wrongChainID)

	eip1559Tx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   wrongChainID,
		Nonce:     1,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x01},
		Value:     big.NewInt(100),
	})
	signed, err := e_types.SignTx(eip1559Tx, signer, key)
	require.NoError(t, err)

	err = ValidateEthTxEnvelope(signed, chainID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid chain ID")
}

func TestValidateEthTxEnvelope_MalleableSignature(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(991)
	signer := e_types.LatestSignerForChainID(chainID)

	eip1559Tx := e_types.NewTx(&e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(10000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &common.Address{0x01},
		Value:     big.NewInt(100),
	})
	signed, err := e_types.SignTx(eip1559Tx, signer, key)
	require.NoError(t, err)

	v, r, s := signed.RawSignatureValues()
	// Create high s: s' = N - s > N/2
	highS := new(big.Int).Sub(secp256k1N, s)
	if highS.Cmp(secp256k1halfN) <= 0 {
		highS = new(big.Int).Add(secp256k1halfN, big.NewInt(1))
	}

	malleableTx, err := signed.WithSignature(signer, packSig(r, highS, v))
	if err == nil {
		err = ValidateEthTxEnvelope(malleableTx, chainID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "malleable signature")
	}
}

func packSig(r, s, v *big.Int) []byte {
	sig := make([]byte, 65)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	sig[64] = byte(v.Uint64())
	return sig
}

var _ *ecdsa.PrivateKey
