package parentchain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T-C1: 4 nodes, 1 lies/diverges => QuorumClient filters out the minority and accepts honest majority.
func TestQuorumClient_T_C1_OneNodeLies(t *testing.T) {
	bls.Init()

	honestStatus := ChainStatus{
		LastBlock: 10,
		LastHash:  common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		StateRoot: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
	}
	dishonestStatus := ChainStatus{
		LastBlock: 9,
		LastHash:  common.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999"),
		StateRoot: common.HexToHash("0x8888888888888888888888888888888888888888888888888888888888888888"),
	}

	createMockServer := func(st ChainStatus) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(st)
		})
		return httptest.NewServer(mux)
	}

	s0 := createMockServer(honestStatus)
	defer s0.Close()
	s1 := createMockServer(honestStatus)
	defer s1.Close()
	s2 := createMockServer(honestStatus)
	defer s2.Close()
	s3 := createMockServer(dishonestStatus) // 1 lying node
	defer s3.Close()

	qc := NewQuorumClient([]string{s0.URL, s1.URL, s2.URL, s3.URL}, cm.PrivateKey{}, cm.PublicKey{})
	st, err := qc.GetStatus()
	require.NoError(t, err)
	assert.Equal(t, honestStatus.LastBlock, st.LastBlock)
	assert.Equal(t, honestStatus.LastHash, st.LastHash)
	assert.Equal(t, honestStatus.StateRoot, st.StateRoot)
}

// T-C2: 4 nodes, 2 lie/diverge (split/no quorum >= 2f+1 or >= f+1 agreement) => returns error, never accepts falsehood.
func TestQuorumClient_T_C2_TwoNodesLieNoQuorum(t *testing.T) {
	statusA := ChainStatus{LastBlock: 10, LastHash: common.HexToHash("0xaaaa")}
	statusB := ChainStatus{LastBlock: 11, LastHash: common.HexToHash("0xbbbb")}
	statusC := ChainStatus{LastBlock: 12, LastHash: common.HexToHash("0xcccc")}
	statusD := ChainStatus{LastBlock: 13, LastHash: common.HexToHash("0xdddd")}

	createMockServer := func(st ChainStatus) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(st)
		})
		return httptest.NewServer(mux)
	}

	s0 := createMockServer(statusA)
	defer s0.Close()
	s1 := createMockServer(statusB)
	defer s1.Close()
	s2 := createMockServer(statusC)
	defer s2.Close()
	s3 := createMockServer(statusD)
	defer s3.Close()

	qc := NewQuorumClient([]string{s0.URL, s1.URL, s2.URL, s3.URL}, cm.PrivateKey{}, cm.PublicKey{})
	_, err := qc.GetStatus()
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrQuorumNotReached)
}

// T-C3: Fake/invalid Merkle proof is rejected cryptographically.
func TestQuorumClient_T_C3_FakeProofRejected(t *testing.T) {
	fakeProof := ProofResult{
		Key:       common.HexToHash("0x1234"),
		Proof:     []byte("invalid-garbage-proof-bytes"),
		StateRoot: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Verified:  false,
	}

	createMockServer := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/proof", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(fakeProof)
		})
		return httptest.NewServer(mux)
	}

	s0 := createMockServer()
	defer s0.Close()
	s1 := createMockServer()
	defer s1.Close()

	qc := NewQuorumClient([]string{s0.URL, s1.URL}, cm.PrivateKey{}, cm.PublicKey{})
	var key [32]byte
	copy(key[:], common.HexToHash("0x1234").Bytes())
	_, err := qc.GetProof(key)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrProofVerification)
}

// TestQuorumClient_ConcurrentNonceOrdering: N concurrent goroutines sending transactions
// must receive strictly sequential nonces without duplicates or gaps.
func TestQuorumClient_ConcurrentNonceOrdering(t *testing.T) {
	kp := bls.GenerateKeyPair()
	pub := kp.PublicKey()
	priv := kp.PrivateKey()

	var mu sync.Mutex
	var receivedNonces []uint64

	mux := http.NewServeMux()
	mux.HandleFunc("/seq", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"seq": 0})
	})
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"nonce": 0})
	})
	mux.HandleFunc("/send_raw_transaction", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RawTx string `json:"raw_tx"`
			Data  string `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		rawHex := req.RawTx
		if rawHex == "" {
			rawHex = req.Data
		}
		_, _ = hex.DecodeString(rawHex[2:])

		mu.Lock()
		// Mock accept
		mu.Unlock()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"tx_hash": "0x1234",
			"status":  "queued",
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	qc := NewQuorumClient([]string{ts.URL}, priv, pub)

	// Test sequential nonces via getNextNonce
	numTxs := 50
	var wg sync.WaitGroup
	wg.Add(numTxs)

	var recordedNonces sync.Map

	for i := 0; i < numTxs; i++ {
		go func() {
			defer wg.Done()
			n, err := qc.getNextNonce()
			if err == nil {
				recordedNonces.Store(n, true)
			}
		}()
	}
	wg.Wait()

	// Verify all nonces from 0 to 49 exist exactly once
	for i := uint64(0); i < uint64(numTxs); i++ {
		_, ok := recordedNonces.Load(i)
		assert.True(t, ok, fmt.Sprintf("Nonce %d must exist without missing gaps", i))
	}
	_ = receivedNonces
}

// nonceServer serves /nonce (fixed value) and /receipt (found=true with the given status when failedReceipt).
func nonceServer(nonce uint64, failedReceipt bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"nonce": nonce})
	})
	mux.HandleFunc("/receipt", func(w http.ResponseWriter, r *http.Request) {
		if !failedReceipt {
			json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"found": true, "receipt": map[string]interface{}{"Status": 0, "ErrorCode": 103}})
	})
	return httptest.NewServer(mux)
}

// The next nonce must come from the committed sender nonce (not FloatSeq), and pipelined sends continue from it.
func TestQuorumClient_NonceFromChainAndPipelining(t *testing.T) {
	kp := bls.GenerateKeyPair()
	ts := nonceServer(5, false)
	defer ts.Close()
	qc := NewQuorumClient([]string{ts.URL}, kp.PrivateKey(), kp.PublicKey())

	for want := uint64(5); want < 8; want++ {
		got, err := qc.getNextNonce()
		assert.NoError(t, err)
		assert.Equal(t, want, got, "chain nonce is 5 and nothing committed yet: nonces must pipeline 5,6,7")
	}
}

// A failed last transaction (status 0 receipt) must make the client fall back to the committed nonce.
func TestQuorumClient_NonceResetsAfterFailedTx(t *testing.T) {
	kp := bls.GenerateKeyPair()
	ts := nonceServer(5, true)
	defer ts.Close()
	qc := NewQuorumClient([]string{ts.URL}, kp.PrivateKey(), kp.PublicKey())

	n, _ := qc.getNextNonce()
	assert.Equal(t, uint64(5), n)
	qc.nonceMu.Lock()
	qc.lastTxHash = common.HexToHash("0x01")
	qc.nonceMu.Unlock()
	n, _ = qc.getNextNonce()
	assert.Equal(t, uint64(5), n, "last tx failed on chain: must restart from the committed nonce, not 6")
}

// One lying node reporting a huge nonce must not inflate the result (f=1 of 4: use the (f+1)-th largest).
func TestQuorumClient_NonceIgnoresLyingNode(t *testing.T) {
	kp := bls.GenerateKeyPair()
	var urls []string
	for _, v := range []uint64{7, 7, 7, 999999} {
		ts := nonceServer(v, false)
		defer ts.Close()
		urls = append(urls, ts.URL)
	}
	qc := NewQuorumClient(urls, kp.PrivateKey(), kp.PublicKey())
	n, err := qc.getNextNonce()
	assert.NoError(t, err)
	assert.Equal(t, uint64(7), n)
}

// Q1: Quorum verification tests for GetInboundAccountRegistrations

func TestQuorumClient_AccountRegistrations_OneNodeLies(t *testing.T) {
	clusterKp := bls.GenerateKeyPair()
	_, userAddr1 := generateECDSAKey()
	_, userAddr2 := generateECDSAKey()

	honestEvents := []*AccountRegisteredEvent{
		{Seq: 0, UserAddress: userAddr1, ClusterKey: clusterKp.PublicKey()},
	}
	dishonestEvents := []*AccountRegisteredEvent{
		{Seq: 0, UserAddress: userAddr2, ClusterKey: clusterKp.PublicKey()},
	}

	createMockRegServer := func(events []*AccountRegisteredEvent, cursor uint64) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/inbound_registrations", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"events": events,
				"cursor": cursor,
			})
		})
		return httptest.NewServer(mux)
	}

	s0 := createMockRegServer(honestEvents, 1)
	defer s0.Close()
	s1 := createMockRegServer(honestEvents, 1)
	defer s1.Close()
	s2 := createMockRegServer(honestEvents, 1)
	defer s2.Close()
	s3 := createMockRegServer(dishonestEvents, 5) // 1 lying node
	defer s3.Close()

	qc := NewQuorumClient([]string{s0.URL, s1.URL, s2.URL, s3.URL}, clusterKp.PrivateKey(), clusterKp.PublicKey())
	events, nextCur, err := qc.GetInboundAccountRegistrations(clusterKp.PublicKey(), 0)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), nextCur)
	require.Len(t, events, 1)
	assert.Equal(t, userAddr1, events[0].UserAddress)
}

func TestQuorumClient_AccountRegistrations_DisagreementNoQuorum(t *testing.T) {
	clusterKp := bls.GenerateKeyPair()
	_, userAddr1 := generateECDSAKey()
	_, userAddr2 := generateECDSAKey()

	eventsA := []*AccountRegisteredEvent{
		{Seq: 0, UserAddress: userAddr1, ClusterKey: clusterKp.PublicKey()},
	}
	eventsB := []*AccountRegisteredEvent{
		{Seq: 0, UserAddress: userAddr2, ClusterKey: clusterKp.PublicKey()},
	}

	createMockRegServer := func(events []*AccountRegisteredEvent, cursor uint64) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/inbound_registrations", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"events": events,
				"cursor": cursor,
			})
		})
		return httptest.NewServer(mux)
	}

	// 4 nodes: 1 says A, 1 says B, 1 says C, 1 says D -> 0 reaches threshold of 2 (f+1)
	s0 := createMockRegServer(eventsA, 1)
	defer s0.Close()
	s1 := createMockRegServer(eventsB, 1)
	defer s1.Close()
	s2 := createMockRegServer(nil, 0)
	defer s2.Close()
	s3 := createMockRegServer(nil, 2)
	defer s3.Close()

	qc := NewQuorumClient([]string{s0.URL, s1.URL, s2.URL, s3.URL}, clusterKp.PrivateKey(), clusterKp.PublicKey())
	events, nextCur, err := qc.GetInboundAccountRegistrations(clusterKp.PublicKey(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrQuorumNotReached)
	assert.Nil(t, events)
	assert.Equal(t, uint64(0), nextCur, "cursor must NOT advance when quorum is not reached")
}

func TestQuorumClient_AccountRegistrations_MissingNodesNoQuorum(t *testing.T) {
	clusterKp := bls.GenerateKeyPair()
	_, userAddr1 := generateECDSAKey()

	honestEvents := []*AccountRegisteredEvent{
		{Seq: 0, UserAddress: userAddr1, ClusterKey: clusterKp.PublicKey()},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/inbound_registrations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"events": honestEvents,
			"cursor": 1,
		})
	})
	s0 := httptest.NewServer(mux)
	defer s0.Close()

	// 4 nodes, but 3 are invalid/offline URLs
	qc := NewQuorumClient([]string{s0.URL, "http://127.0.0.1:1", "http://127.0.0.1:2", "http://127.0.0.1:3"}, clusterKp.PrivateKey(), clusterKp.PublicKey())
	events, nextCur, err := qc.GetInboundAccountRegistrations(clusterKp.PublicKey(), 0)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.Equal(t, uint64(0), nextCur, "cursor must NOT advance when responses < threshold")
}
