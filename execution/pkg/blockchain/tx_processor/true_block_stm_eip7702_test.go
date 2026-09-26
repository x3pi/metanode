package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
    "math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

func signAuthorization(t *testing.T, key *ecdsa.PrivateKey, chainID uint64, delegate common.Address, nonce uint64) types.SetCodeAuthorization {
	t.Helper()
	auth, err := types.SignSetCode(key, types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(chainID),
		Address: delegate,
		Nonce:   nonce,
	})
	if err != nil {
		t.Fatalf("SignSetCode: %v", err)
	}
	return auth
}

func TestTrueBlockSTM_EIP7702_NativeTransfer_EstimateHit(t *testing.T) {
	iters := stressIterations(40)
	procsChoices := []int{1, 2, 4, 8, 16}
	if v := os.Getenv("STM_STRESS_PROCS"); v != "" {
		procsChoices = nil
		for _, f := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
				procsChoices = append(procsChoices, n)
			}
		}
	}
	maxWorkers, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXWORKERS"))
	prevProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })

	// R is the hot authority
	rKey, _ := crypto.GenerateKey()
	rAddr := crypto.PubkeyToAddress(*rKey.Public().(*ecdsa.PublicKey))

	// Sender 1: normal native transfer to R
	senderKey, _ := crypto.GenerateKey()
	senderAddr := crypto.PubkeyToAddress(*senderKey.Public().(*ecdsa.PublicKey))

	// Sender 2: sends the EIP-7702 tx (authorizing R -> delegate)
	sender2Key, _ := crypto.GenerateKey()
	sender2Addr := crypto.PubkeyToAddress(*sender2Key.Public().(*ecdsa.PublicKey))

	delegateAddr := common.HexToAddress("0x0000000000000000000000000000000000001234")
	designator := types.AddressToDelegation(delegateAddr)
	expectedCodeHash := crypto.Keccak256Hash(designator)

	const (
		numSendTxs = 30
	)

	maxFail, _ := strconv.Atoi(os.Getenv("STM_STRESS_MAXFAIL"))
	if maxFail <= 0 {
		maxFail = 5
	}
	failures := 0

    leaderAddr := common.HexToAddress("0xAAAA000000000000000000000000000000000001")

	for _, procs := range procsChoices {
		runtime.GOMAXPROCS(procs)
		for it := 0; it < iters; it++ {
			if failures >= maxFail {
				t.Fatalf("Too many failures (%d), aborting", failures)
			}
			t.Logf("Testing GOMAXPROCS=%d, iteration=%d...", procs, it)

			chainState := newTestChainState(t)
			chainState.SetConfig(&config.SimpleChainConfig{ChainId: big.NewInt(991)})

			seedAccount(t, chainState, senderAddr, big.NewInt(1000000000000), 0)
			seedAccount(t, chainState, sender2Addr, big.NewInt(1000000000000), 0)
			seedAccount(t, chainState, rAddr, big.NewInt(0), 0) // R starts with 0

			var txs []mt_types.Transaction

			// Txs sending to R
			for i := 0; i < numSendTxs; i++ {
				tx := newTx(senderAddr, rAddr, uint64(i), big.NewInt(1), nil)
				txs = append(txs, tx)
			}

			// EIP-7702 tx: Native transfer from sender2Addr to some address, with auth list for R
			auth := signAuthorization(t, rKey, 991, delegateAddr, 0)
			innerTx := &types.SetCodeTx{
				ChainID:   uint256.NewInt(991),
				Nonce:     0,
				GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(1),
				Gas:       100000,
				To:        common.HexToAddress("0x9999"), // Native transfer
				Value:     uint256.NewInt(0),
				AuthList:  []types.SetCodeAuthorization{auth},
			}
			ethTx, _ := types.SignNewTx(sender2Key, types.NewPragueSigner(big.NewInt(991)), innerTx)
			eip7702Tx, _ := transaction.NewTransactionFromEth(ethTx)
			// Insert EIP-7702 tx at a random position to trigger overlaps
            rnd := rand.New(rand.NewSource(int64(it) + 1))
            insertPos := rnd.Intn(len(txs) + 1)
			txs = append(txs[:insertPos], append([]mt_types.Transaction{eip7702Tx}, txs[insertPos:]...)...)

			stm := NewTrueBlockSTM(txs)
            if maxWorkers > 0 {
                    stm = stm.WithMaxExecWorkers(maxWorkers)
            }
			stm.Process(context.Background(), chainState, leaderAddr, blankHeader(), 12345)

			// Check results
			rState, err := chainState.GetAccountStateDB().AccountState(rAddr)
			if err != nil {
				t.Fatalf("AccountState error: %v", err)
			}
			if rState == nil {
				t.Fatalf("R account state is nil")
			}

			if rState.Balance().Int64() != numSendTxs {
				failures++
				t.Errorf("[GOMAXPROCS=%d it=%d] R balance mismatch: got %v, want %d", procs, it, rState.Balance(), numSendTxs)
			}
			if rState.SmartContractState() == nil || rState.SmartContractState().CodeHash() != expectedCodeHash {
				failures++
				t.Errorf("[GOMAXPROCS=%d it=%d] R CodeHash mismatch: got %x, want %x", procs, it, rState.SmartContractState().CodeHash(), expectedCodeHash)
			}
		}
	}
}
