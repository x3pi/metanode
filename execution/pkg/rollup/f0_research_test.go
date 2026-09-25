package rollup_test

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"math/big"
	"testing"
)

// BenchmarkEthSignCheck measures ValidEthSign on a FRESH transaction object per
// iteration. Transaction.ToEthTransaction and go-ethereum's Sender both cache
// their result on the object, so re-checking one object only measures a cache
// hit (~1us) and hides the real secp256k1 recovery cost.
func BenchmarkEthSignCheck(b *testing.B) {
	privateKey, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x0000000000000000000000000000000000000000")
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &to,
		Value:    big.NewInt(10),
		Gas:      21000,
		GasPrice: big.NewInt(1),
		Data:     nil,
	})
	signer := types.LatestSignerForChainID(big.NewInt(1))
	signedTx, _ := types.SignTx(tx, signer, privateKey)

	metaTxs := make([]*mt_transaction.Transaction, b.N)
	for i := range metaTxs {
		metaTxIface, err := mt_transaction.NewTransactionFromEth(signedTx)
		if err != nil {
			b.Fatalf("NewTransactionFromEth failed: %v", err)
		}
		metaTxs[i] = metaTxIface.(*mt_transaction.Transaction)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !metaTxs[i].ValidEthSign() {
			b.Fatal("ValidEthSign returned false")
		}
	}
}

func TestEthSendRawTransaction_RSV(t *testing.T) {
	privateKey, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x1230000000000000000000000000000000000000")
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		To:       &to,
		Value:    big.NewInt(100),
		Gas:      21000,
		GasPrice: big.NewInt(1),
		Data:     nil,
	})
	signer := types.LatestSignerForChainID(big.NewInt(1))
	signedTx, _ := types.SignTx(tx, signer, privateKey)

	metaTxIface, err := mt_transaction.NewTransactionFromEth(signedTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth failed: %v", err)
	}
	metaTx := metaTxIface.(*mt_transaction.Transaction)

	if !metaTx.ValidEthSign() {
		t.Fatalf("ValidEthSign returned false, expected true")
	}

	// Print sizes to demonstrate RSV are preserved
	t.Logf("ValidEthSign succeeded!")
}
