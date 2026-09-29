package parentchain

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func TestDepositToFloat_ConsecutiveDepositsUniqueMsgID(t *testing.T) {
	store := NewMemoryStore()
	txChan := make(chan *ParentChainTx, 10)
	server := NewHTTPServer(store, txChan)

	mux := http.NewServeMux()
	mux.HandleFunc("/tx", server.handleTx)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewHTTPClient(ts.URL)

	// Background worker responding to txChan
	go func() {
		for tx := range txChan {
			server.NotifyTxResult(tx.MsgID, nil)
		}
	}()

	kp := bls.GenerateKeyPair()
	pubKey := kp.PublicKey()
	clusterID := uint64(101)
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := big.NewInt(1000)

	// Deposit 1
	msgID1, err := client.SendDepositToFloat(pubKey, clusterID, sender, target, amount)
	assert.NoError(t, err)
	assert.NotEqual(t, common.Hash{}, msgID1)

	// Small pause so UnixNano ticks
	time.Sleep(1 * time.Millisecond)

	// Deposit 2: Same parameters exactly
	msgID2, err := client.SendDepositToFloat(pubKey, clusterID, sender, target, amount)
	assert.NoError(t, err)
	assert.NotEqual(t, common.Hash{}, msgID2)

	// Verify msgIDs are distinct!
	assert.NotEqual(t, msgID1, msgID2, "consecutive identical deposits must have distinct MsgIDs")

	// Verify both deposits can be successfully recorded in parent chain state
	err1 := DepositToFloat(store, pubKey, clusterID, sender, target, amount, msgID1, 100)
	assert.NoError(t, err1)

	err2 := DepositToFloat(store, pubKey, clusterID, sender, target, amount, msgID2, 101)
	assert.NoError(t, err2, "second identical deposit must not fail with ErrFloatAlreadyResolved")

	// Total balance in float account must be 2000
	hash := crypto.Keccak256Hash(pubKey[:])
	bal, err := store.GetFloat(hash)
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(2000), bal)
}
