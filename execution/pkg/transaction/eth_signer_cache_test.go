package transaction

import (
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestCachedSigners_CorrectnessAndCaching(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainIDs := []*big.Int{big.NewInt(991), big.NewInt(1), big.NewInt(1337)}

	for _, cid := range chainIDs {
		// London
		entryBefore := cachedLondon.Load()
		s1 := CachedLondonSigner(cid)
		entryAfter1 := cachedLondon.Load()
		s2 := CachedLondonSigner(cid)
		entryAfter2 := cachedLondon.Load()
		require.Equal(t, s1, s2)
		require.Same(t, entryAfter1, entryAfter2, "cachedLondon entry pointer must be reused without reallocation")
		_ = entryBefore

		// Cancun
		c1 := CachedCancunSigner(cid)
		c2 := CachedCancunSigner(cid)
		require.Equal(t, c1, c2)

		// Prague
		p1 := CachedPragueSigner(cid)
		p2 := CachedPragueSigner(cid)
		require.Equal(t, p1, p2)

		// EIP-155
		e1 := CachedEIP155Signer(cid)
		e2 := CachedEIP155Signer(cid)
		require.Equal(t, e1, e2)

		// Verify signature and sender recovery correctness
		txData := &e_types.DynamicFeeTx{
			ChainID:   cid,
			Nonce:     1,
			GasTipCap: big.NewInt(100),
			GasFeeCap: big.NewInt(1000),
			Gas:       21000,
			To:        &common.Address{0x42},
			Value:     big.NewInt(1000),
		}
		rawTx := e_types.NewTx(txData)
		signedTx, err := e_types.SignTx(rawTx, s1, key)
		require.NoError(t, err)

		sender1, err := e_types.Sender(s1, signedTx)
		require.NoError(t, err)

		freshSigner := e_types.NewLondonSigner(cid)
		senderFresh, err := e_types.Sender(freshSigner, signedTx)
		require.NoError(t, err)

		expectedAddr := crypto.PubkeyToAddress(key.PublicKey)
		require.Equal(t, expectedAddr, sender1)
		require.Equal(t, senderFresh, sender1)
	}
}

func TestCachedSigners_ConcurrentRace(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			cid := big.NewInt(int64(990 + (workerID % 3)))
			s := CachedLondonSigner(cid)
			txData := &e_types.DynamicFeeTx{
				ChainID:   cid,
				Nonce:     uint64(workerID),
				GasTipCap: big.NewInt(100),
				GasFeeCap: big.NewInt(1000),
				Gas:       21000,
				To:        &common.Address{byte(workerID)},
				Value:     big.NewInt(500),
			}
			signed, err := e_types.SignTx(e_types.NewTx(txData), s, key)
			if err != nil {
				t.Errorf("sign error: %v", err)
				return
			}
			sender, err := e_types.Sender(s, signed)
			if err != nil {
				t.Errorf("sender error: %v", err)
				return
			}
			_ = sender
		}(i)
	}
	wg.Wait()
}
