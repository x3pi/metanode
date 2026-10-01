package parentchain

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
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

// httpClient implements Client
type httpClient struct {
	endpoint string
	client   *http.Client
}

func NewHTTPClient(endpoint string) Client {
	return &httpClient{
		endpoint: endpoint,
		client:   defaultHTTPClient,
	}
}

func (c *httpClient) post(path string, req interface{}, resp interface{}) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequest(http.MethodPost, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("PARENT_CHAIN_RPC_TOKEN"); tok != "" {
		hreq.Header.Set("Authorization", "Bearer "+tok)
	}
	r, err := c.client.Do(hreq)
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

func (c *httpClient) SendDepositToFloat(
	pubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount *big.Int,
) (common.Hash, error) {
	req := ParentChainTx{
		Type:      TxTypeDepositToFloat,
		PubKey:    pubKey[:],
		ClusterID: destClusterID,
		ChainID:   destClusterID,
		Sender:    sender,
		Target:    target,
		Amount:    amount,
		Nonce:     uint64(time.Now().UnixNano()),
	}
	var resp struct {
		MsgID common.Hash `json:"msg_id"`
	}
	err := c.post("/tx", req, &resp)
	return resp.MsgID, err
}

func (c *httpClient) SendTransferFloat(
	pubKey, destPubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount, gasFee *big.Int,
	nonce uint64,
	cert []byte,
	isRefund bool,
) (common.Hash, error) {
	req := ParentChainTx{
		Type:      TxTypeTransferFloat,
		PubKey:    pubKey[:],
		ToPubKey:  destPubKey[:],
		ClusterID: destClusterID,
		ChainID:   destClusterID,
		Sender:    sender,
		Target:    target,
		Amount:    amount,
		Nonce:     nonce,
		Cert:      cert,
		IsRefund:  isRefund,
		Payload:   nil,
		Fee:       gasFee,
	}
	var resp struct {
		MsgID common.Hash `json:"msg_id"`
	}
	err := c.post("/tx", req, &resp)
	return resp.MsgID, err
}

func (c *httpClient) SendMarkClaimed(msgID common.Hash, outcome FloatOutcome, cert []byte) (common.Hash, error) {
	req := ParentChainTx{
		Type:    TxTypeMarkClaimed,
		MsgID:   msgID,
		Outcome: outcome,
		Cert:    cert,
	}
	var resp struct {
		MsgID common.Hash `json:"msg_id"`
	}
	err := c.post("/tx", req, &resp)
	return resp.MsgID, err
}

func (c *httpClient) SendReclaimFloat(msgID common.Hash, cert []byte) (common.Hash, error) {
	req := ParentChainTx{
		Type:  TxTypeReclaimFloat,
		MsgID: msgID,
		Cert:  cert,
	}
	var resp struct {
		MsgID common.Hash `json:"msg_id"`
	}
	err := c.post("/tx", req, &resp)
	return resp.MsgID, err
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

func (c *httpClient) SendRegisterAccount(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error) {
	req := ParentChainTx{
		Type:        TxTypeRegisterAccount,
		UserAddress: userAddress,
		PubKey:      floatIdentityKey[:],
		UserSig:     userSig,
		Cert:        clusterSig[:],
	}
	var resp struct {
		MsgID common.Hash `json:"msg_id"`
	}
	err := c.post("/tx", req, &resp)
	return resp.MsgID, err
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

func (c *httpClient) SendSubmitStateRoot(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) (common.Hash, error) {
	req := ParentChainTx{
		Type:      TxTypeSubmitStateRoot,
		PubKey:    clusterPubKey[:],
		Epoch:     epoch,
		StateRoot: stateRoot,
		Cert:      cert[:],
	}
	var resp struct {
		MsgID common.Hash `json:"msg_id"`
	}
	err := c.post("/tx", req, &resp)
	return resp.MsgID, err
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

func (c *httpClient) SendRawTransaction(rawTx []byte) (common.Hash, error) {
	req := map[string]string{
		"raw_tx": "0x" + common.Bytes2Hex(rawTx),
	}
	var resp struct {
		TxHash common.Hash `json:"tx_hash"`
	}
	err := c.post("/send_raw_transaction", req, &resp)
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
	txChan       chan *ParentChainTx
	protoTxChan  chan *pb.Transaction
	pendingTxs   sync.Map // map[common.Hash]chan error
	syncing      atomic.Bool
	forkDetected atomic.Bool
	validatorsMu sync.RWMutex
	validators   []*pb.ValidatorInfo
}

func NewHTTPServer(store Store, txChan chan *ParentChainTx) *HTTPServer {
	s := &HTTPServer{
		store:  store,
		txChan: txChan,
	}
	if c, ok := store.(BlockCommitter); ok {
		s.committer = c
	}
	return s
}

func (s *HTTPServer) SetCommitter(c BlockCommitter) {
	s.committer = c
}

func (s *HTTPServer) SetProtoTxChan(ch chan *pb.Transaction) {
	s.protoTxChan = ch
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

func (s *HTTPServer) NotifyTxResult(msgID common.Hash, err error) {
	if chIntf, ok := s.pendingTxs.Load(msgID); ok {
		ch := chIntf.(chan error)
		if err != nil {
			select {
			case ch <- err:
			default:
			}
		} else {
			close(ch)
		}
		s.pendingTxs.Delete(msgID)
	}
}

func (s *HTTPServer) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tx", s.handleTxGetOrPost)
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
	mux.Handle("/metrics", promhttp.Handler())
	return http.ListenAndServe(addr, mux)
}

func (s *HTTPServer) handleTx(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var tx ParentChainTx
	if err := json.NewDecoder(r.Body).Decode(&tx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var msgID common.Hash
	if tx.Type == TxTypeTransferFloat {
		if tx.Amount == nil || tx.Amount.Sign() <= 0 {
			http.Error(w, "invalid amount", http.StatusBadRequest)
			return
		}
		if len(tx.PubKey) != 48 || len(tx.ToPubKey) != 48 {
			http.Error(w, "invalid public key length", http.StatusBadRequest)
			return
		}
		var fromKey, toKey cm.PublicKey
		copy(fromKey[:], tx.PubKey)
		copy(toKey[:], tx.ToPubKey)

		payloadHash := crypto.Keccak256Hash(tx.Payload)
		digest := ComputeTransferFloatMessage(fromKey, toKey, tx.Sender, tx.Target, tx.Amount, tx.Fee, payloadHash, tx.Nonce)
		if len(tx.Cert) != 96 {
			http.Error(w, "invalid signature length", http.StatusBadRequest)
			return
		}
		if !bls.VerifySign(fromKey, cm.Sign(tx.Cert), digest) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		msgID = crypto.Keccak256Hash(digest)
		tx.MsgID = msgID
	} else if tx.Type == TxTypeDepositToFloat {
		// A deposit carries no user signature (it is relayed by the bridge), so the caller itself must be
		// authenticated: fail-closed unless PARENT_CHAIN_RPC_TOKEN is configured and presented.
		tok := os.Getenv("PARENT_CHAIN_RPC_TOKEN")
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok != "" && subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
			http.Error(w, "unauthorized: DepositToFloat requires PARENT_CHAIN_RPC_TOKEN", http.StatusUnauthorized)
			return
		}
		if tx.Amount == nil || tx.Amount.Sign() <= 0 {
			http.Error(w, "invalid amount", http.StatusBadRequest)
			return
		}
		if len(tx.PubKey) != 48 {
			http.Error(w, "invalid public key length", http.StatusBadRequest)
			return
		}
		if tx.MsgID != (common.Hash{}) {
			msgID = tx.MsgID
		} else {
			var pubKey cm.PublicKey
			copy(pubKey[:], tx.PubKey)
			data := append(append(pubKey[:], tx.Sender.Bytes()...), tx.Target.Bytes()...)
			data = append(data, tx.Amount.Bytes()...)
			nonce := tx.Nonce
			if nonce == 0 {
				nonce = uint64(time.Now().UnixNano())
			}
			var nonceBytes [8]byte
			binary.BigEndian.PutUint64(nonceBytes[:], nonce)
			data = append(data, nonceBytes[:]...)
			msgID = crypto.Keccak256Hash(data)
			tx.MsgID = msgID
		}
	} else if tx.Type == TxTypeSubmitStateRoot {
		if len(tx.PubKey) != 48 {
			http.Error(w, "invalid public key length", http.StatusBadRequest)
			return
		}
		if len(tx.Cert) != 96 {
			http.Error(w, "invalid signature length", http.StatusBadRequest)
			return
		}
		var clusterPubKey cm.PublicKey
		copy(clusterPubKey[:], tx.PubKey)
		digest := ComputeSubmitStateRootMessage(clusterPubKey, tx.Epoch, tx.StateRoot)
		if !bls.VerifySign(clusterPubKey, cm.Sign(tx.Cert), digest) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		msgID = crypto.Keccak256Hash(append(digest, tx.Cert...))
		tx.MsgID = msgID
	} else if tx.Type == TxTypeRegisterAccount {
		var floatIdentityKey cm.PublicKey
		copy(floatIdentityKey[:], tx.PubKey)
		digest := ComputeRegisterAccountMessage(tx.UserAddress, floatIdentityKey)

		if len(tx.Cert) != 96 {
			http.Error(w, "invalid signature length", http.StatusBadRequest)
			return
		}
		if !bls.VerifySign(floatIdentityKey, cm.Sign(tx.Cert), digest) {
			http.Error(w, "invalid cluster signature", http.StatusUnauthorized)
			return
		}
		msgID = crypto.Keccak256Hash(digest)
		tx.MsgID = msgID
	} else if tx.Type == TxTypeMarkClaimed {
		rec, found, err := s.store.GetTransferRecord(tx.MsgID)
		if err != nil || !found {
			http.Error(w, "unknown messageID", http.StatusBadRequest)
			return
		}
		digest := ComputeMarkClaimedMessage(tx.MsgID, tx.Outcome)
		if len(tx.Cert) != 96 {
			http.Error(w, "invalid signature length", http.StatusBadRequest)
			return
		}
		if !bls.VerifySign(rec.DestKey, cm.Sign(tx.Cert), digest) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		msgID = tx.MsgID
	} else if tx.Type == TxTypeReclaimFloat {
		rec, found, err := s.store.GetTransferRecord(tx.MsgID)
		if err != nil || !found {
			http.Error(w, "unknown messageID", http.StatusBadRequest)
			return
		}
		if rec.SourceKey == nil {
			http.Error(w, "cannot reclaim direct deposit", http.StatusBadRequest)
			return
		}
		digest := ComputeReclaimFloatMessage(tx.MsgID, *rec.SourceKey)
		if len(tx.Cert) != 96 {
			http.Error(w, "invalid signature length", http.StatusBadRequest)
			return
		}
		if !bls.VerifySign(*rec.SourceKey, cm.Sign(tx.Cert), digest) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		msgID = tx.MsgID
	} else {
		msgID = tx.MsgID
	}

	async := r.URL.Query().Get("async") == "true" || r.Header.Get("X-Async") == "true"
	if async {
		select {
		case s.txChan <- &tx:
			json.NewEncoder(w).Encode(map[string]interface{}{"msg_id": msgID, "status": "queued"})
			return
		default:
			http.Error(w, "tx queue full", http.StatusServiceUnavailable)
			return
		}
	}

	resultCh := make(chan error, 1)
	s.pendingTxs.Store(msgID, resultCh)
	defer s.pendingTxs.Delete(msgID)

	txTimeout := 30 * time.Second
	if tStr := os.Getenv("PARENT_CHAIN_TX_TIMEOUT_SECS"); tStr != "" {
		if tSec, err := strconv.Atoi(tStr); err == nil && tSec > 0 {
			txTimeout = time.Duration(tSec) * time.Second
		}
	}
	timer := time.NewTimer(txTimeout)
	defer timer.Stop()

	select {
	case s.txChan <- &tx:
		// Transaction successfully queued. Wait for consensus to process it.
		select {
		case err := <-resultCh:
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"msg_id": msgID})
		case <-timer.C:
			http.Error(w, "timeout waiting for consensus", http.StatusGatewayTimeout)
		}
	default:
		http.Error(w, "tx queue full", http.StatusServiceUnavailable)
	}
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

func (s *HTTPServer) handleTxGetOrPost(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
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
	if os.Getenv("PARENT_CHAIN_DISABLE_LEGACY_TX_ENDPOINT") == "true" {
		http.Error(w, "legacy JSON /tx endpoint is disabled; submit signed pb.Transaction via /send_raw_transaction instead", http.StatusForbidden)
		return
	}
	s.handleTx(w, r)
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

func (s *HTTPServer) handleSendRawTransaction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.syncing.Load() {
		http.Error(w, "node is syncing, transactions rejected", http.StatusServiceUnavailable)
		return
	}

	var rawBytes []byte
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var req struct {
			RawTx string `json:"raw_tx"`
			Data  string `json:"data"`
			Tx    string `json:"tx"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid json body: %v", err), http.StatusBadRequest)
			return
		}
		rawHex := req.RawTx
		if rawHex == "" {
			rawHex = req.Data
		}
		if rawHex == "" {
			rawHex = req.Tx
		}
		rawBytes = common.FromHex(rawHex)
	} else {
		var err error
		rawBytes, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read body: %v", err), http.StatusBadRequest)
			return
		}
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

	txHash := ComputeTxHash(&tx)

	if s.protoTxChan != nil {
		select {
		case s.protoTxChan <- &tx:
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

