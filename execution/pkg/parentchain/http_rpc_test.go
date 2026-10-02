package parentchain

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

func TestPadTo32_NilSafety(t *testing.T) {
	// Must not panic on nil
	var res []byte
	assert.NotPanics(t, func() {
		res = padTo32(nil)
	})
	assert.Equal(t, 32, len(res))
	assert.Equal(t, make([]byte, 32), res)
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

	txChan := make(chan *pb.Transaction, 10)
	server := NewHTTPServer(store)
	server.SetTxChan(txChan)
	server.SetValidators([]*pb.ValidatorInfo{
		{Name: "validator-0", Address: "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/block", server.handleBlock)
	mux.HandleFunc("/tx", server.handleTxLookup)
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
	tx2, _ := BuildAndSignBLSTx(priv, pub, ParentChainGatewayAddress, 2, callData) // nonce 1 is already committed
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

// The JSON transaction path no longer exists: POST /tx is rejected and /send_raw_transaction accepts only raw
// proto bytes (a JSON body is not a valid pb.Transaction).
func TestHTTPRPC_LegacyJSONPathRemoved(t *testing.T) {
	txChan := make(chan *pb.Transaction, 1)
	server := NewHTTPServer(NewMemoryStore())
	server.SetTxChan(txChan)
	mux := http.NewServeMux()
	mux.HandleFunc("/tx", server.handleTxLookup)
	mux.HandleFunc("/send_raw_transaction", server.handleSendRawTransaction)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/tx", "application/json", strings.NewReader(`{"type":"DepositToFloat","amount":1}`))
	assert.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "POST /tx must not exist")

	resp, err = http.Post(ts.URL+"/send_raw_transaction", "application/json", strings.NewReader(`{"raw_tx":"0x0a0b"}`))
	assert.NoError(t, err)
	resp.Body.Close()
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "a JSON-wrapped body must not be accepted as a transaction")
	assert.Equal(t, 0, len(txChan), "nothing may be queued from the JSON path")
}

// Oversized and wrong-chain transactions are rejected at ingress and never queued.
func TestHTTPRPC_SendRawTransactionValidation(t *testing.T) {
	txChan := make(chan *pb.Transaction, 4)
	server := NewHTTPServer(NewMemoryStore())
	server.SetTxChan(txChan)
	mux := http.NewServeMux()
	mux.HandleFunc("/send_raw_transaction", server.handleSendRawTransaction)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := NewHTTPClient(ts.URL)

	kp := bls.GenerateKeyPair()
	tx, _ := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), ParentChainGatewayAddress, 0, EncodeRegisterClusterCallData(kp.PublicKey(), 1))
	tx.ChainID = 1
	raw, _ := proto.Marshal(tx)
	_, err := client.SendRawTransaction(raw)
	assert.Error(t, err, "wrong chain id must be rejected")

	_, err = client.SendRawTransaction(make([]byte, maxRawTxBytes+1))
	assert.Error(t, err, "oversized body must be rejected")
	assert.Equal(t, 0, len(txChan))

	good, _ := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), ParentChainGatewayAddress, 0, EncodeRegisterClusterCallData(kp.PublicKey(), 1))
	goodRaw, _ := proto.Marshal(good)
	h, err := client.SendRawTransaction(goodRaw)
	assert.NoError(t, err)
	assert.Equal(t, ComputeTxHash(good), h)
	assert.Equal(t, 1, len(txChan))
}

// Admission filter: forged, unknown-sender and stale-nonce transactions never reach the consensus queue.
func TestHTTPRPC_AdmissionFilter(t *testing.T) {
	txChan := make(chan *pb.Transaction, 8)
	store := NewMemoryStore()
	server := NewHTTPServer(store)
	server.SetTxChan(txChan)
	mux := http.NewServeMux()
	mux.HandleFunc("/send_raw_transaction", server.handleSendRawTransaction)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := NewHTTPClient(ts.URL)
	send := func(tx *pb.Transaction) error {
		raw, _ := proto.Marshal(tx)
		_, err := client.SendRawTransaction(raw)
		return err
	}

	kp := bls.GenerateKeyPair()
	other := bls.GenerateKeyPair()
	register := EncodeRegisterClusterCallData(kp.PublicKey(), 1)

	// Forged signature (signed by a different key) on a registration tx.
	forged, _ := BuildAndSignBLSTx(other.PrivateKey(), other.PublicKey(), ParentChainGatewayAddress, 0, register)
	forged.FromAddress = common.BytesToAddress(crypto.Keccak256(kp.PublicKey().Bytes())[12:]).Bytes()
	assert.Error(t, send(forged), "forged signature must be rejected")

	// Unsigned tx from a random unregistered sender (the spam shape).
	spam := &pb.Transaction{FromAddress: make([]byte, 20), ToAddress: ParentChainGatewayAddress.Bytes(),
		Nonce: make([]byte, 8), ChainID: ParentChainID, Sign: make([]byte, 96)}
	assert.Error(t, send(spam), "unknown sender must be rejected")

	// Wrong destination address.
	wrongTo, _ := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), common.Address{1}, 0, register)
	assert.Error(t, send(wrongTo), "non-gateway destination must be rejected")
	assert.Equal(t, 0, len(txChan))

	// Valid registration passes; once the nonce is committed, replaying nonce 0 is stale.
	good, _ := BuildAndSignBLSTx(kp.PrivateKey(), kp.PublicKey(), ParentChainGatewayAddress, 0, register)
	assert.NoError(t, send(good))
	assert.Equal(t, 1, len(txChan))
	sender := common.BytesToAddress(good.FromAddress)
	assert.NoError(t, store.SetNonce(sender, 1))
	assert.Error(t, send(good), "stale nonce must be rejected")
	assert.Equal(t, 1, len(txChan))
}

func TestHTTPRPC_FloatEndpoint(t *testing.T) {
	store := NewMemoryStore()
	server := NewHTTPServer(store)
	mux := http.NewServeMux()
	mux.HandleFunc("/float", server.handleFloat)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	kp := bls.GenerateKeyPair()
	pk := kp.PublicKey()
	assert.NoError(t, store.SetFloat(crypto.Keccak256Hash(pk[:]), big.NewInt(777)))

	resp, err := http.Get(ts.URL + "/float?pubkey=" + common.Bytes2Hex(pk[:]))
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var d struct {
		Balance string `json:"balance"`
	}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&d))
	assert.Equal(t, "777", d.Balance)

	bad, err := http.Get(ts.URL + "/float?pubkey=0x1234")
	assert.NoError(t, err)
	defer bad.Body.Close()
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode)
}
