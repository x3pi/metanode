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
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
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
	privKey := kp.PrivateKey()
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
	_ = store.SetChainRegistry(crypto.Keccak256Hash(pubKey[:]), ChainRegistryEntry{FloatIdentityKey: pubKey, ClusterIDDescriptive: clusterID})
	dig1 := ComputeDepositFloatMessage(pubKey, clusterID, sender, target, amount, msgID1)
	cert1 := bls.Sign(privKey, dig1)
	err1 := DepositToFloat(store, pubKey, pubKey, clusterID, sender, target, amount, msgID1, cert1, 100)
	assert.NoError(t, err1)

	dig2 := ComputeDepositFloatMessage(pubKey, clusterID, sender, target, amount, msgID2)
	cert2 := bls.Sign(privKey, dig2)
	err2 := DepositToFloat(store, pubKey, pubKey, clusterID, sender, target, amount, msgID2, cert2, 101)
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

// T-P7..T-P10: RPC endpoints for blocks, txs, receipts, proofs, status, and raw transactions.
func TestHTTPRPC_FullChainEndpoints(t *testing.T) {
	dir := t.TempDir()
	store, err := NewDBStore(dir)
	assert.NoError(t, err)
	defer store.Close()

	// Setup cluster & create a valid deposit transaction
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	priv := kp.PrivateKey()

	regData := EncodeRegisterClusterCallData(pub, 101)
	regTx, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 0, regData)
	assert.NoError(t, err)
	regBytes, _ := proto.Marshal(regTx)

	msgID := common.HexToHash("0x1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff")
	dig := ComputeDepositFloatMessage(pub, 101, common.Address{}, common.Address{}, big.NewInt(500), msgID)
	cert := bls.Sign(priv, dig)
	callData := EncodeDepositToFloatCallData(pub, pub, 101, common.Address{}, common.Address{}, big.NewInt(500), msgID, cert)
	tx, err := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 1, callData)
	assert.NoError(t, err)
	txBytes, _ := proto.Marshal(tx)

	// Apply block 1
	in := BlockInput{
		Number:        1,
		GEI:           100,
		CommitIndex:   1,
		Epoch:         1,
		TimestampMs:   1000,
		LeaderAddress: common.HexToAddress("0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5"),
		CommitDigest:  common.HexToHash("0xabcd"),
		Txs:           [][]byte{regBytes, txBytes},
	}
	res, err := store.ApplyBlock(in, func(st Store, idx int, rawTx []byte) (*Receipt, error) {
		var pTx pb.Transaction
		_ = proto.Unmarshal(rawTx, &pTx)
		return ExecuteTx(st, &pTx, 1)
	})
	assert.NoError(t, err)

	protoTxChan := make(chan *pb.Transaction, 10)
	server := NewHTTPServer(store, make(chan *ParentChainTx, 10))
	server.SetProtoTxChan(protoTxChan)
	server.SetValidators([]*pb.ValidatorInfo{
		{Name: "validator-0", Address: "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/block", server.handleBlock)
	mux.HandleFunc("/tx", server.handleTxGetOrPost)
	mux.HandleFunc("/receipt", server.handleReceipt)
	mux.HandleFunc("/proof", server.handleProof)
	mux.HandleFunc("/status", server.handleStatus)
	mux.HandleFunc("/validators", server.handleValidators)
	mux.HandleFunc("/send_raw_transaction", server.handleSendRawTransaction)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewHTTPClient(ts.URL)

	// 1. GetBlockByNumber
	rec, found, err := client.GetBlockByNumber(1)
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), rec.Header.Number)
	assert.Equal(t, res.Record.BlockHash, rec.BlockHash)

	// 2. GetBlockByHash
	recHash, found, err := client.GetBlockByHash(res.Record.BlockHash)
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, rec.Header.Number, recHash.Header.Number)

	// 3. GetTransaction
	txHash := ComputeTxHash(tx)
	bNum, idx, found, err := client.GetTransaction(txHash)
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), bNum)
	assert.Equal(t, uint32(1), idx)

	// 4. GetReceipt
	rcpt, found, err := client.GetReceipt(txHash)
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint8(1), rcpt.Status)
	assert.Equal(t, txHash, rcpt.TxHash)

	// 5. GetStatus
	status, err := client.GetStatus()
	assert.NoError(t, err)
	assert.Equal(t, uint64(1), status.LastBlock)
	assert.Equal(t, res.Record.BlockHash, status.LastHash)
	assert.False(t, status.Syncing)
	assert.False(t, status.ForkDetected)

	// 6. GetProof
	key := TreeKey(NamespaceFloat, crypto.Keccak256Hash(pub[:]).Bytes())
	proofRes, err := client.GetProof(key)
	assert.NoError(t, err)
	assert.True(t, proofRes.Verified)

	// 7. SendRawTransaction when syncing => 503 rejected
	server.SetSyncing(true)
	tx2, _ := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 1, callData)
	tx2Bytes, _ := proto.Marshal(tx2)
	_, errSync := client.SendRawTransaction(tx2Bytes)
	assert.Error(t, errSync)
	assert.Contains(t, errSync.Error(), "503")

	// 8. SendRawTransaction when not syncing => accepted
	server.SetSyncing(false)
	sentHash, errSent := client.SendRawTransaction(tx2Bytes)
	assert.NoError(t, errSent)
	assert.Equal(t, ComputeTxHash(tx2), sentHash)
}

