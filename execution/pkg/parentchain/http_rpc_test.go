package parentchain

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func TestDepositToFloat_ConsecutiveDepositsUniqueMsgID(t *testing.T) {
	t.Setenv("PARENT_CHAIN_RPC_TOKEN", "test-token")
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

func TestPadTo32_NilSafety(t *testing.T) {
	// Must not panic on nil
	var res []byte
	assert.NotPanics(t, func() {
		res = padTo32(nil)
	})
	assert.Equal(t, 32, len(res))
	assert.Equal(t, make([]byte, 32), res)
}

func TestHTTPRPC_InputValidation(t *testing.T) {
	t.Setenv("PARENT_CHAIN_RPC_TOKEN", "test-token")
	store := NewMemoryStore()
	txChan := make(chan *ParentChainTx, 10)
	server := NewHTTPServer(store, txChan)

	mux := http.NewServeMux()
	mux.HandleFunc("/tx", server.handleTx)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewHTTPClient(ts.URL)

	kp := bls.GenerateKeyPair()
	pubKey := kp.PublicKey()

	// 1. TransferFloat with nil amount -> must be rejected
	reqBadTransfer := ParentChainTx{
		Type:     TxTypeTransferFloat,
		PubKey:   pubKey[:],
		ToPubKey: pubKey[:],
		Amount:   nil, // nil amount!
	}
	var resp struct{}
	err := client.(*httpClient).post("/tx", reqBadTransfer, &resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid amount")

	// 2. TransferFloat with negative/zero amount -> must be rejected
	reqZeroTransfer := ParentChainTx{
		Type:     TxTypeTransferFloat,
		PubKey:   pubKey[:],
		ToPubKey: pubKey[:],
		Amount:   big.NewInt(0),
	}
	err = client.(*httpClient).post("/tx", reqZeroTransfer, &resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid amount")

	// 3. DepositToFloat with nil amount -> must be rejected
	reqBadDeposit := ParentChainTx{
		Type:   TxTypeDepositToFloat,
		PubKey: pubKey[:],
		Amount: nil,
	}
	err = client.(*httpClient).post("/tx", reqBadDeposit, &resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid amount")

	// 4. DepositToFloat with invalid pubkey length -> must be rejected
	reqShortKeyDeposit := ParentChainTx{
		Type:   TxTypeDepositToFloat,
		PubKey: []byte("too short"),
		Amount: big.NewInt(100),
	}
	err = client.(*httpClient).post("/tx", reqShortKeyDeposit, &resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid public key length")

	// 5. SubmitStateRoot with invalid signature length -> must be rejected
	reqBadCertLen := ParentChainTx{
		Type:      TxTypeSubmitStateRoot,
		PubKey:    pubKey[:],
		Epoch:     1,
		StateRoot: common.HexToHash("0x1111"),
		Cert:      []byte("short-cert"),
	}
	err = client.(*httpClient).post("/tx", reqBadCertLen, &resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid signature length")

	// 6. SubmitStateRoot with forged/invalid signature -> must be rejected with 401 Unauthorized
	otherKp := bls.GenerateKeyPair()
	badDigest := ComputeSubmitStateRootMessage(pubKey, 1, common.HexToHash("0x1111"))
	forgedSig := bls.Sign(otherKp.PrivateKey(), badDigest) // Signed with different key!
	reqBadSig := ParentChainTx{
		Type:      TxTypeSubmitStateRoot,
		PubKey:    pubKey[:],
		Epoch:     1,
		StateRoot: common.HexToHash("0x1111"),
		Cert:      forgedSig[:],
	}
	err = client.(*httpClient).post("/tx", reqBadSig, &resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid signature")
}

func TestDepositToFloat_RequiresToken(t *testing.T) {
	t.Setenv("PARENT_CHAIN_RPC_TOKEN", "secret")
	server := NewHTTPServer(NewMemoryStore(), make(chan *ParentChainTx, 1))
	mux := http.NewServeMux()
	mux.HandleFunc("/tx", server.handleTx)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := `{"type":"DepositToFloat","amount":1}`
	for name, auth := range map[string]string{"missing": "", "wrong": "Bearer nope"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/tx", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		assert.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
	}
}
