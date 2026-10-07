package transaction

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func createBenchTx(t testing.TB, isEIP1559 bool) (*Transaction, *ecdsa.PrivateKey) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	chainID := big.NewInt(991)
	signer := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")

	var inner types.TxData
	if isEIP1559 {
		inner = &types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     1,
			GasTipCap: big.NewInt(1000),
			GasFeeCap: big.NewInt(100000),
			Gas:       21000,
			To:        &to,
			Value:     big.NewInt(1000000),
			Data:      []byte{1, 2, 3, 4},
		}
	} else {
		inner = &types.LegacyTx{
			Nonce:    1,
			GasPrice: big.NewInt(100000),
			Gas:      21000,
			To:       &to,
			Value:    big.NewInt(1000000),
			Data:     []byte{1, 2, 3, 4},
		}
	}

	ethTx, err := types.SignNewTx(key, signer, inner)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	metaTx, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("new transaction from eth: %v", err)
	}
	return metaTx.(*Transaction), key
}

// BenchmarkValidateProtoEnvelopeBinding_EIP1559 measures end-to-end binding validation (envelope parse + fields match + ecrecover)
func BenchmarkValidateProtoEnvelopeBinding_EIP1559(b *testing.B) {
	metaTx, _ := createBenchTx(b, true)
	protoTx := metaTx.proto

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := ValidateProtoEnvelopeBinding(protoTx); err != nil {
			b.Fatalf("validation failed: %v", err)
		}
	}
}

// BenchmarkValidateProtoEnvelopeBinding_Legacy measures legacy tx binding validation
func BenchmarkValidateProtoEnvelopeBinding_Legacy(b *testing.B) {
	metaTx, _ := createBenchTx(b, false)
	protoTx := metaTx.proto

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := ValidateProtoEnvelopeBinding(protoTx); err != nil {
			b.Fatalf("validation failed: %v", err)
		}
	}
}

// BenchmarkECRecover_Only isolates raw ecrecover cryptographic computation cost
func BenchmarkECRecover_Only(b *testing.B) {
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	msg := crypto.Keccak256([]byte("benchmark-payload-bytes"))
	sig, err := crypto.Sign(msg, key)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pub, err := crypto.Ecrecover(msg, sig)
		if err != nil || len(pub) == 0 {
			b.Fatal("ecrecover failed")
		}
	}
}

// BenchmarkNewTransactionFromEth measures envelope encoding and proto construction
func BenchmarkNewTransactionFromEth(b *testing.B) {
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	chainID := big.NewInt(991)
	signer := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")

	ethTx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000),
		GasFeeCap: big.NewInt(100000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(1000000),
		Data:      []byte{1, 2, 3, 4},
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := NewTransactionFromEth(ethTx)
		if err != nil {
			b.Fatal(err)
		}
	}
}
