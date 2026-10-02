package parentchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/protobuf/proto"
)

var defaultHTTPTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 30,
	IdleConnTimeout:     90 * time.Second,
	TLSHandshakeTimeout: 10 * time.Second,
}

var defaultHTTPClient = &http.Client{
	Transport: defaultHTTPTransport,
	Timeout:   45 * time.Second,
}

// httpClient is the read/submit client for a single parent chain node. Signing and nonce handling live in
// QuorumClient; there is no unsigned or JSON transaction path.
type httpClient struct {
	endpoint string
	client   *http.Client
}

func NewHTTPClient(endpoint string) *httpClient {
	return &httpClient{
		endpoint: endpoint,
		client:   defaultHTTPClient,
	}
}

func (c *httpClient) get(path string, resp interface{}) error {
	r, err := c.client.Get(c.endpoint + path)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(r.Body)
		return fmt.Errorf("HTTP error %d: %s", r.StatusCode, string(respBody))
	}
	if resp != nil {
		return json.NewDecoder(r.Body).Decode(resp)
	}
	return nil
}

func (c *httpClient) GetInboundTransfers(pubKey cm.PublicKey, cursor uint64) ([]*TransferEvent, uint64, error) {
	pubKeyHex := hexEncode(pubKey[:])
	var resp struct {
		Events []*TransferEvent `json:"events"`
		Cursor uint64           `json:"cursor"`
	}
	err := c.get(fmt.Sprintf("/inbound?pubkey=%s&cursor=%d", pubKeyHex, cursor), &resp)
	return resp.Events, resp.Cursor, err
}

func (c *httpClient) GetTransferRecord(msgID common.Hash) (FloatTransferRecord, bool, error) {
	var resp struct {
		Record FloatTransferRecord `json:"record"`
		Found  bool                `json:"found"`
	}
	err := c.get(fmt.Sprintf("/record?msg_id=%s", msgID.Hex()), &resp)
	return resp.Record, resp.Found, err
}

func (c *httpClient) GetClaimed(msgID common.Hash) (FloatOutcome, error) {
	var resp struct {
		Outcome FloatOutcome `json:"outcome"`
	}
	err := c.get(fmt.Sprintf("/claimed?msg_id=%s", msgID.Hex()), &resp)
	return resp.Outcome, err
}

// GetNonce returns the committed sender nonce of addr (the nonce its next transaction must carry).
func (c *httpClient) GetNonce(addr common.Address) (uint64, error) {
	var resp struct {
		Nonce uint64 `json:"nonce"`
	}
	err := c.get(fmt.Sprintf("/nonce?address=%s", addr.Hex()), &resp)
	return resp.Nonce, err
}

func (c *httpClient) GetFloatSeq(pubKey cm.PublicKey) (uint64, error) {
	pubKeyHex := hexEncode(pubKey[:])
	var resp struct {
		Seq uint64 `json:"seq"`
	}
	err := c.get(fmt.Sprintf("/seq?pubkey=%s", pubKeyHex), &resp)
	return resp.Seq, err
}

func (c *httpClient) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	var resp struct {
		FloatIdentityKey []byte `json:"float_identity_key"`
		Found            bool   `json:"found"`
	}
	err := c.get(fmt.Sprintf("/account?address=%s", userAddress.Hex()), &resp)
	var pubKey cm.PublicKey
	if len(resp.FloatIdentityKey) > 0 {
		copy(pubKey[:], resp.FloatIdentityKey)
	}
	return pubKey, resp.Found, err
}

func (c *httpClient) GetStateRoot(clusterPubKey cm.PublicKey, epoch uint64) (common.Hash, bool, error) {
	pubKeyHex := hexEncode(clusterPubKey[:])
	var resp struct {
		Root  common.Hash `json:"root"`
		Found bool        `json:"found"`
	}
	err := c.get(fmt.Sprintf("/state_root?pubkey=%s&epoch=%d", pubKeyHex, epoch), &resp)
	return resp.Root, resp.Found, err
}

func (c *httpClient) GetBlockByNumber(number uint64) (BlockRecord, bool, error) {
	var resp struct {
		Record BlockRecord `json:"record"`
		Found  bool        `json:"found"`
	}
	err := c.get(fmt.Sprintf("/block?number=%d", number), &resp)
	return resp.Record, resp.Found, err
}

func (c *httpClient) GetBlockByHash(hash common.Hash) (BlockRecord, bool, error) {
	var resp struct {
		Record BlockRecord `json:"record"`
		Found  bool        `json:"found"`
	}
	err := c.get(fmt.Sprintf("/block?hash=%s", hash.Hex()), &resp)
	return resp.Record, resp.Found, err
}

func (c *httpClient) GetTransaction(txHash common.Hash) (uint64, uint32, bool, error) {
	var resp struct {
		BlockNumber uint64 `json:"block_number"`
		Index       uint32 `json:"index"`
		Found       bool   `json:"found"`
	}
	err := c.get(fmt.Sprintf("/tx?hash=%s", txHash.Hex()), &resp)
	return resp.BlockNumber, resp.Index, resp.Found, err
}

func (c *httpClient) GetReceipt(txHash common.Hash) (*Receipt, bool, error) {
	var resp struct {
		Receipt *Receipt `json:"receipt"`
		Found   bool     `json:"found"`
	}
	err := c.get(fmt.Sprintf("/receipt?hash=%s", txHash.Hex()), &resp)
	return resp.Receipt, resp.Found, err
}

func (c *httpClient) GetStatus() (ChainStatus, error) {
	var resp ChainStatus
	err := c.get("/status", &resp)
	return resp, err
}

func (c *httpClient) GetProof(key [32]byte) (ProofResult, error) {
	var resp ProofResult
	err := c.get(fmt.Sprintf("/proof?key=0x%x", key), &resp)
	return resp, err
}

// SendRawTransaction submits a signed, proto-encoded pb.Transaction (raw bytes body) to the node's ingress.
func (c *httpClient) SendRawTransaction(rawTx []byte) (common.Hash, error) {
	hreq, err := http.NewRequest(http.MethodPost, c.endpoint+"/send_raw_transaction", bytes.NewReader(rawTx))
	if err != nil {
		return common.Hash{}, err
	}
	hreq.Header.Set("Content-Type", "application/octet-stream")
	r, err := c.client.Do(hreq)
	if err != nil {
		return common.Hash{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(r.Body)
		return common.Hash{}, fmt.Errorf("HTTP error %d: %s", r.StatusCode, string(respBody))
	}
	var resp struct {
		TxHash common.Hash `json:"tx_hash"`
	}
	err = json.NewDecoder(r.Body).Decode(&resp)
	return resp.TxHash, err
}

func hexEncode(b []byte) string {
	return common.Bytes2Hex(b)
}

// ---------------------------------------------------------
// HTTPServer is the HTTP server for Parent Chain RPC.
// ---------------------------------------------------------

type HTTPServer struct {
	store        Store
	committer    BlockCommitter
	txChan       chan *pb.Transaction
	syncing      atomic.Bool
	forkDetected atomic.Bool
	validatorsMu sync.RWMutex
	validators   []*pb.ValidatorInfo
}

func NewHTTPServer(store Store) *HTTPServer {
	s := &HTTPServer{store: store}
	if c, ok := store.(BlockCommitter); ok {
		s.committer = c
	}
	return s
}

// SetTxChan sets the bounded queue that accepted signed transactions are pushed into (consumed by TxBatcher).
func (s *HTTPServer) SetTxChan(ch chan *pb.Transaction) {
	s.txChan = ch
}

func (s *HTTPServer) SetSyncing(syncing bool) {
	s.syncing.Store(syncing)
}

func (s *HTTPServer) SetForkDetected(fork bool) {
	s.forkDetected.Store(fork)
}

func (s *HTTPServer) SetValidators(vals []*pb.ValidatorInfo) {
	s.validatorsMu.Lock()
	defer s.validatorsMu.Unlock()
	s.validators = vals
}

func (s *HTTPServer) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tx", s.handleTxLookup)
	mux.HandleFunc("/block", s.handleBlock)
	mux.HandleFunc("/receipt", s.handleReceipt)
	mux.HandleFunc("/proof", s.handleProof)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/validators", s.handleValidators)
	mux.HandleFunc("/send_raw_transaction", s.handleSendRawTransaction)

	mux.HandleFunc("/inbound", s.handleInbound)
	mux.HandleFunc("/record", s.handleRecord)
	mux.HandleFunc("/claimed", s.handleClaimed)
	mux.HandleFunc("/seq", s.handleSeq)
	mux.HandleFunc("/nonce", s.handleNonce)
	mux.HandleFunc("/account", s.handleAccount)
	mux.HandleFunc("/state_root", s.handleStateRoot)
	mux.HandleFunc("/float", s.handleFloat)
	mux.Handle("/metrics", promhttp.Handler())
	// I/O timeouts only bound slow or stalled clients (slowloris); they never influence consensus or dispatch.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *HTTPServer) handleInbound(w http.ResponseWriter, r *http.Request) {
	pubKeyHex := r.URL.Query().Get("pubkey")
	cursorStr := r.URL.Query().Get("cursor")
	cursor, _ := strconv.ParseUint(cursorStr, 10, 64)

	pubKeyBytes := common.FromHex(pubKeyHex)
	var pubKey cm.PublicKey
	copy(pubKey[:], pubKeyBytes)

	destHash := crypto.Keccak256Hash(pubKey[:])
	events, nextCursor, err := s.store.GetInboundTransfers(destHash, cursor)
	if err != nil {
		events = []*TransferEvent{}
		nextCursor = cursor
	}
	if events == nil {
		events = []*TransferEvent{}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"events": events, "cursor": nextCursor})
}

func (s *HTTPServer) handleRecord(w http.ResponseWriter, r *http.Request) {
	msgID := common.HexToHash(r.URL.Query().Get("msg_id"))
	rec, found, _ := s.store.GetTransferRecord(msgID)
	json.NewEncoder(w).Encode(map[string]interface{}{"record": rec, "found": found})
}

func (s *HTTPServer) handleClaimed(w http.ResponseWriter, r *http.Request) {
	msgID := common.HexToHash(r.URL.Query().Get("msg_id"))
	outcome, _ := s.store.GetClaimed(msgID)
	json.NewEncoder(w).Encode(map[string]interface{}{"outcome": outcome})
}

func (s *HTTPServer) handleSeq(w http.ResponseWriter, r *http.Request) {
	pubKeyHex := r.URL.Query().Get("pubkey")
	pubKeyBytes := common.FromHex(pubKeyHex)

	seq, _ := s.store.GetFloatSeq(crypto.Keccak256Hash(pubKeyBytes))
	json.NewEncoder(w).Encode(map[string]interface{}{"seq": seq})
}

// handleNonce returns the committed sender nonce (the number of the next tx this address must use).
func (s *HTTPServer) handleNonce(w http.ResponseWriter, r *http.Request) {
	addr := common.HexToAddress(r.URL.Query().Get("address"))
	nonce, err := s.store.GetNonce(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"nonce": nonce})
}

func (s *HTTPServer) handleAccount(w http.ResponseWriter, r *http.Request) {
	addrHex := r.URL.Query().Get("address")
	addr := common.HexToAddress(addrHex)

	pubKey, found, _ := s.store.GetAccountRegistry(addr)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"float_identity_key": pubKey[:],
		"found":              found,
	})
}

// handleFloat returns the committed float balance of a BLS identity (GET /float?pubkey=<48-byte hex>) together
// with the chain-wide float total supply.
func (s *HTTPServer) handleFloat(w http.ResponseWriter, r *http.Request) {
	pubKeyBytes := common.FromHex(r.URL.Query().Get("pubkey"))
	if len(pubKeyBytes) != 48 {
		http.Error(w, "pubkey must be a 48-byte BLS public key (hex)", http.StatusBadRequest)
		return
	}
	bal, err := s.store.GetFloat(crypto.Keccak256Hash(pubKeyBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, err := s.store.GetFloat(floatTotalSupplyKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"balance":      bal.String(),
		"total_supply": total.String(),
	})
}

func (s *HTTPServer) handleStateRoot(w http.ResponseWriter, r *http.Request) {
	pubKeyHex := r.URL.Query().Get("pubkey")
	pubKeyBytes := common.FromHex(pubKeyHex)

	epochStr := r.URL.Query().Get("epoch")
	epoch, _ := strconv.ParseUint(epochStr, 10, 64)

	root, found, _ := s.store.GetStateRoot(crypto.Keccak256Hash(pubKeyBytes), epoch)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"root":  root,
		"found": found,
	})
}

func (s *HTTPServer) handleBlock(w http.ResponseWriter, r *http.Request) {
	if s.committer == nil {
		http.Error(w, "block storage not available", http.StatusServiceUnavailable)
		return
	}
	numStr := r.URL.Query().Get("number")
	hashStr := r.URL.Query().Get("hash")

	var rec BlockRecord
	var found bool
	var err error

	if hashStr != "" {
		h := common.HexToHash(hashStr)
		rec, found, err = s.committer.GetBlockRecordByHash(h)
	} else if numStr != "" {
		num, pErr := strconv.ParseUint(numStr, 10, 64)
		if pErr != nil {
			http.Error(w, "invalid block number", http.StatusBadRequest)
			return
		}
		rec, found, err = s.committer.GetBlockRecord(num)
	} else {
		prog, pErr := s.committer.LastApplied()
		if pErr != nil || prog.LastBlock == 0 {
			json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
			return
		}
		rec, found, err = s.committer.GetBlockRecord(prog.LastBlock)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"record": rec,
		"found":  found,
	})
}

// handleTxLookup returns where a transaction was included (GET /tx?hash=...). Transactions are submitted only
// through /send_raw_transaction; POST /tx no longer exists.
func (s *HTTPServer) handleTxLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed: submit signed transactions to /send_raw_transaction", http.StatusMethodNotAllowed)
		return
	}
	{
		hashStr := r.URL.Query().Get("hash")
		if hashStr == "" {
			http.Error(w, "missing hash query parameter", http.StatusBadRequest)
			return
		}
		txHash := common.HexToHash(hashStr)
		if s.committer == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
			return
		}
		bNum, idx, found, err := s.committer.GetTxLocation(txHash)
		if err != nil || !found {
			json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"block_number": bNum,
			"index":        idx,
			"found":        true,
		})
		return
	}
}

func (s *HTTPServer) handleReceipt(w http.ResponseWriter, r *http.Request) {
	hashStr := r.URL.Query().Get("hash")
	if hashStr == "" {
		http.Error(w, "missing hash query parameter", http.StatusBadRequest)
		return
	}
	txHash := common.HexToHash(hashStr)
	if s.committer == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"found": false})
		return
	}
	rcpt, found, err := s.committer.GetReceipt(txHash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"receipt": rcpt,
		"found":   found,
	})
}

func (s *HTTPServer) handleProof(w http.ResponseWriter, r *http.Request) {
	if s.committer == nil {
		http.Error(w, "committer not available", http.StatusServiceUnavailable)
		return
	}
	keyStr := r.URL.Query().Get("key")
	if keyStr == "" {
		http.Error(w, "missing key query parameter", http.StatusBadRequest)
		return
	}
	keyBytes := common.FromHex(keyStr)
	var key [32]byte
	copy(key[:], keyBytes)

	proof, err := s.committer.GenerateProof(key)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to generate proof: %v", err), http.StatusInternalServerError)
		return
	}
	prog, _ := s.committer.LastApplied()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"key":        common.BytesToHash(key[:]),
		"proof":      proof,
		"state_root": prog.LastStateRoot,
		"verified":   len(proof) > 0,
	})
}

func (s *HTTPServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	var lastBlock uint64
	var lastHash common.Hash
	var stateRoot common.Hash
	if s.committer != nil {
		prog, _ := s.committer.LastApplied()
		lastBlock = prog.LastBlock
		lastHash = prog.LastHash
		stateRoot = prog.LastStateRoot
	}
	forkDetected := s.forkDetected.Load()
	UpdateMetrics(lastBlock, stateRoot.Hex(), forkDetected)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"last_block":    lastBlock,
		"last_hash":     lastHash,
		"state_root":    stateRoot,
		"syncing":       s.syncing.Load(),
		"fork_detected": forkDetected,
	})
}

func (s *HTTPServer) handleValidators(w http.ResponseWriter, r *http.Request) {
	s.validatorsMu.RLock()
	vals := s.validators
	s.validatorsMu.RUnlock()
	if vals == nil {
		vals = []*pb.ValidatorInfo{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"validators": vals,
	})
}

// maxRawTxBytes bounds the body of /send_raw_transaction.
const maxRawTxBytes = 1 << 20

func (s *HTTPServer) handleSendRawTransaction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.syncing.Load() {
		http.Error(w, "node is syncing, transactions rejected", http.StatusServiceUnavailable)
		return
	}

	rawBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRawTxBytes))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read body: %v", err), http.StatusBadRequest)
		return
	}

	var tx pb.Transaction
	if err := proto.Unmarshal(rawBytes, &tx); err != nil {
		http.Error(w, fmt.Sprintf("failed to unmarshal transaction: %v", err), http.StatusBadRequest)
		return
	}

	if tx.ChainID != ParentChainID {
		http.Error(w, fmt.Sprintf("invalid chain ID: %d", tx.ChainID), http.StatusBadRequest)
		return
	}
	if len(tx.FromAddress) != 20 {
		http.Error(w, "invalid from address", http.StatusBadRequest)
		return
	}

	if common.BytesToAddress(tx.ToAddress) != ParentChainGatewayAddress {
		http.Error(w, "invalid to address", http.StatusBadRequest)
		return
	}
	// Admission filter: unsigned or forged transactions never reach consensus (they would only burn block space
	// and receipt storage). Execution re-verifies deterministically, so this is purely a pre-filter on committed
	// state: a sender must already be registered (or carry its own registration) and the nonce must not be stale.
	if _, err := VerifyTxSignature(&tx, s.store); err != nil {
		http.Error(w, fmt.Sprintf("rejected: %v", err), http.StatusBadRequest)
		return
	}
	if committed, err := s.store.GetNonce(common.BytesToAddress(tx.FromAddress)); err == nil && TxNonceValue(&tx) < committed {
		http.Error(w, fmt.Sprintf("rejected: stale nonce %d < committed %d", TxNonceValue(&tx), committed), http.StatusBadRequest)
		return
	}

	txHash := ComputeTxHash(&tx)

	if s.txChan != nil {
		select {
		case s.txChan <- &tx:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"tx_hash": txHash,
				"status":  "queued",
			})
		default:
			http.Error(w, "transaction queue is full", http.StatusServiceUnavailable)
		}
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"tx_hash": txHash,
		"status":  "accepted",
	})
}

