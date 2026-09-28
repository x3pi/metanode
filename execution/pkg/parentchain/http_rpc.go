package parentchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// httpClient implements Client
type httpClient struct {
	endpoint string
}

func NewHTTPClient(endpoint string) Client {
	return &httpClient{endpoint: endpoint}
}

func (c *httpClient) post(path string, req interface{}, resp interface{}) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.Post(c.endpoint+path, "application/json", bytes.NewReader(body))
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
	r, err := http.Get(c.endpoint + path)
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

func (c *httpClient) SendTransferFloat(
	pubKey, destPubKey cm.PublicKey,
	destChainID uint64,
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
		ChainID:   destChainID,
		Sender:    sender,
		Target:    target,
		Amount:    amount,
		Nonce:     nonce,
		Cert:      cert,
		IsRefund:  isRefund,
		Payload:   nil,
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

func hexEncode(b []byte) string {
	return common.Bytes2Hex(b)
}

// ---------------------------------------------------------
// HTTPServer is the HTTP server for Parent Chain RPC.
// ---------------------------------------------------------

type HTTPServer struct {
	store      Store
	txChan     chan *ParentChainTx
	pendingTxs sync.Map // map[common.Hash]chan error
}

func NewHTTPServer(store Store, txChan chan *ParentChainTx) *HTTPServer {
	return &HTTPServer{
		store:  store,
		txChan: txChan,
	}
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
	mux.HandleFunc("/tx", s.handleTx)
	mux.HandleFunc("/inbound", s.handleInbound)
	mux.HandleFunc("/record", s.handleRecord)
	mux.HandleFunc("/claimed", s.handleClaimed)
	mux.HandleFunc("/seq", s.handleSeq)
	mux.HandleFunc("/account", s.handleAccount)
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
		var fromKey, toKey cm.PublicKey
		copy(fromKey[:], tx.PubKey)
		copy(toKey[:], tx.ToPubKey)
		
		payloadHash := crypto.Keccak256Hash(tx.Payload)
		digest := ComputeTransferFloatMessage(fromKey, toKey, tx.Sender, tx.Target, tx.Amount, payloadHash, tx.Nonce)
		msgID = crypto.Keccak256Hash(digest)
		tx.MsgID = msgID
	} else if tx.Type == TxTypeRegisterAccount {
		var floatIdentityKey cm.PublicKey
		copy(floatIdentityKey[:], tx.PubKey)
		digest := ComputeRegisterAccountMessage(tx.UserAddress, floatIdentityKey)
		msgID = crypto.Keccak256Hash(digest)
		tx.MsgID = msgID
	} else {
		msgID = tx.MsgID
	}

	resultCh := make(chan error, 1)
	s.pendingTxs.Store(msgID, resultCh)
	defer s.pendingTxs.Delete(msgID)

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
		case <-time.After(10 * time.Second):
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

func (s *HTTPServer) handleAccount(w http.ResponseWriter, r *http.Request) {
	addrHex := r.URL.Query().Get("address")
	addr := common.HexToAddress(addrHex)
	
	pubKey, found, _ := s.store.GetAccountRegistry(addr)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"float_identity_key": pubKey[:],
		"found":              found,
	})
}

