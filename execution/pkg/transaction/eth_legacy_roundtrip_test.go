package transaction

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// These three tests close the gap the EIP-4844/7702 plan called for but never
// got for the pre-existing tx types: a real SignTx -> protobuf ->
// ToEthTransaction round-trip (hash + sender match) for each of
// Legacy(0x00)/EIP-2930(0x01)/EIP-1559(0x02), the same safety net
// eth_eip4844_test.go/eth_eip7702_test.go already have for the 2 new types —
// this is what should catch a future go-ethereum bump breaking one of the
// older types silently.

func TestFromEthLegacyTx_RoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000001234")

	innerTx := &types.LegacyTx{
		Nonce:    3,
		GasPrice: big.NewInt(1000),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(5000),
	}
	ethTx, err := types.SignNewTx(key, types.NewEIP155Signer(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	originalHash := ethTx.Hash()

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)

	if tx.FromAddress() != from {
		t.Fatalf("FromAddress mismatch: got %s, want %s", tx.FromAddress().Hex(), from.Hex())
	}
	if tx.ToAddress() != to {
		t.Fatalf("ToAddress mismatch: got %s, want %s", tx.ToAddress().Hex(), to.Hex())
	}

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil for a valid legacy tx")
	}
	if rebuilt.Type() != types.LegacyTxType {
		t.Fatalf("rebuilt tx type = %d, want LegacyTxType", rebuilt.Type())
	}
	if got := rebuilt.Hash(); got != originalHash {
		t.Fatalf("rebuilt hash = %s, want %s", got.Hex(), originalHash.Hex())
	}
}

func TestFromEthEIP2930Tx_RoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000005678")

	innerTx := &types.AccessListTx{
		ChainID:  big.NewInt(1337),
		Nonce:    7,
		GasPrice: big.NewInt(2000),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(1000),
		AccessList: types.AccessList{
			{
				Address:     common.HexToAddress("0x00000000000000000000000000000000009999"),
				StorageKeys: []common.Hash{common.HexToHash("0x01")},
			},
		},
	}
	ethTx, err := types.SignNewTx(key, types.NewLondonSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	originalHash := ethTx.Hash()

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)

	if tx.FromAddress() != from {
		t.Fatalf("FromAddress mismatch: got %s, want %s", tx.FromAddress().Hex(), from.Hex())
	}
	if tx.ToAddress() != to {
		t.Fatalf("ToAddress mismatch: got %s, want %s", tx.ToAddress().Hex(), to.Hex())
	}

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil for a valid EIP-2930 tx")
	}
	if rebuilt.Type() != types.AccessListTxType {
		t.Fatalf("rebuilt tx type = %d, want AccessListTxType", rebuilt.Type())
	}
	if len(rebuilt.AccessList()) != 1 {
		t.Fatalf("rebuilt AccessList len = %d, want 1", len(rebuilt.AccessList()))
	}
	if got := rebuilt.Hash(); got != originalHash {
		t.Fatalf("rebuilt hash = %s, want %s", got.Hex(), originalHash.Hex())
	}
}

func TestFromEthEIP1559Tx_RoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x0000000000000000000000000000000000ABCD")

	innerTx := &types.DynamicFeeTx{
		ChainID:   big.NewInt(1337),
		Nonce:     11,
		GasTipCap: big.NewInt(100),
		GasFeeCap: big.NewInt(1000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(2500),
	}
	ethTx, err := types.SignNewTx(key, types.NewLondonSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	originalHash := ethTx.Hash()

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)

	if tx.FromAddress() != from {
		t.Fatalf("FromAddress mismatch: got %s, want %s", tx.FromAddress().Hex(), from.Hex())
	}
	if tx.ToAddress() != to {
		t.Fatalf("ToAddress mismatch: got %s, want %s", tx.ToAddress().Hex(), to.Hex())
	}

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil for a valid EIP-1559 tx")
	}
	if rebuilt.Type() != types.DynamicFeeTxType {
		t.Fatalf("rebuilt tx type = %d, want DynamicFeeTxType", rebuilt.Type())
	}
	if got := rebuilt.Hash(); got != originalHash {
		t.Fatalf("rebuilt hash = %s, want %s", got.Hex(), originalHash.Hex())
	}
}
