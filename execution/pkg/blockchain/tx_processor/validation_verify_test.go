package tx_processor

import (
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Helper to create a ChainState for testing
func setupTestChainState(t *testing.T) *blockchain.ChainState {
	t.Helper()
	
	// Ensure SKIP_MEMPOOL_SIG_VERIFY is disabled for tests so validation actually runs
	os.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)

	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("failed to create test chain state: %v", err)
	}

	cs.SetConfig(&config.SimpleChainConfig{
		ChainId: big.NewInt(1),
	})

	return cs
}

// Helper to create a basic transaction
func createTestTx(from, to common.Address, amount *big.Int, maxGas, maxGasPrice uint64, nonce uint64) (types.Transaction, []byte) {
	tx := transaction.NewTransaction(
		from, to, amount, maxGas, maxGasPrice, maxGas,
		[]byte{}, nil, common.Hash{}, common.Hash{}, nonce, 1,
	)

	keyPair := bls.GenerateKeyPair()
	pub := keyPair.PublicKey()
	priv := keyPair.PrivateKey()

	sign := bls.Sign(priv, tx.Hash().Bytes())
	tx.(*transaction.Transaction).SetSignBytes(sign.Bytes())

	return tx, pub.Bytes()
}

func TestVerifyTransaction_Success(t *testing.T) {
	cs := setupTestChainState(t)

	from := common.HexToAddress("0x123")
	to := common.HexToAddress("0x456")
	amount := big.NewInt(100)

	// Valid max gas (>= 21000) and max gas price (>= 1000000)
	tx, pubKey := createTestTx(from, to, amount, p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)

	// Create account state with enough balance
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetPublicKeyBls(pubKey) // Need length for bls check bypass
	as.SetNonce(1)

	// Bypass skip mempool verification
	err := VerifyTransaction(tx, cs, as)
	
	// Since we now generate a real BLS signature, it should completely pass and return nil
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
	}
}

// func TestVerifyTransaction_InvalidMaxGasPrice_SpamDoS(t *testing.T) {
// 	cs := setupTestChainState(t)
// 	from := common.HexToAddress("0x123")
// 	// Max gas price <= 0
// 	invalidGasPrice := uint64(0)
// 	tx, pubKey := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(100), p_common.TRANSFER_GAS_COST, invalidGasPrice, 1)

// 	as := state.NewAccountState(from)
// 	as.AddBalance(big.NewInt(1000000000000000))
// 	as.SetPublicKeyBls(pubKey)
// 	as.SetNonce(1)

// 	// err := VerifyTransaction(tx, cs, as)
// 	// if err != transaction.InvalidMaxGasPrice {
// 	// 	t.Errorf("Expected InvalidMaxGasPrice for spam DoS protection, got %v", err)
// 	// }
// }

func TestVerifyTransaction_InvalidMaxGas_LowerBound(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x123")
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetNonce(1)

	// Max gas < TRANSFER_GAS_COST
	tx, _ := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(100), p_common.TRANSFER_GAS_COST-1, p_common.MINIMUM_BASE_FEE, 1)

	err := VerifyTransaction(tx, cs, as)
	if err != transaction.InvalidMaxGas {
		t.Errorf("Expected InvalidMaxGas (too low), got %v", err)
	}
}

func TestVerifyTransaction_InvalidMaxGas_UpperBound(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x123")
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1000000000000000))
	as.SetNonce(1)

	// Max gas > BLOCK_GAS_LIMIT
	tx, _ := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(100), p_common.BLOCK_GAS_LIMIT+1, p_common.MINIMUM_BASE_FEE, 1)

	err := VerifyTransaction(tx, cs, as)
	if err != transaction.InvalidMaxGas {
		t.Errorf("Expected InvalidMaxGas (too high), got %v", err)
	}
}

func TestVerifyTransaction_FreeFeeBypass(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x123")
	to := common.HexToAddress("0x456")
	
	// Add 'to' address to free fee addresses
	cs.GetFreeFeeAddress()[to] = struct{}{}

	// Set extremely high gas limit and 0 gas price (normally fails)
	tx, pubKey := createTestTx(from, to, big.NewInt(100), p_common.BLOCK_GAS_LIMIT, 0, 1)

	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(10000)) // Low balance
	as.SetPublicKeyBls(pubKey)
	as.SetNonce(1)

	err := VerifyTransaction(tx, cs, as)
	// It should bypass MaxFee and MaxGasPrice checks because to is free,
	// and pass BLS signature check, so it should return nil
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
	}
}

func TestVerifyTransaction_LaggingSubnode(t *testing.T) {
	cs := setupTestChainState(t)
	from := common.HexToAddress("0x123")
	// Because lagging, it skips many checks, including BLS verification!
	tx, _ := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(100), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)

	as := state.NewAccountState(from)
	
	// Simulate lagging subnode: has nonce > 0, but no PublicKeyBls
	as.SetNonce(1)
	
	err := VerifyTransaction(tx, cs, as)
	
	// If it is lagging, it doesn't fail on BLS verify, and it skips fee checks
	// So it should return nil
	if err != nil {
		t.Errorf("Expected nil, got %v", err)
	}

	// Now set balance
	as.AddBalance(big.NewInt(1000000000000000))
	err2 := VerifyTransaction(tx, cs, as)
	// Should pass everything since BLS is bypassed
	if err2 != nil {
		t.Errorf("Expected nil (Lagging subnode bypasses signature), got %v", err2)
	}
}
