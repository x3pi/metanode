package tx_processor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
)

// fakeRootAnchorRPC is a minimal JSON-RPC server for signAndSubmit's retry semantics: it serves eth_chainId and the
// pending nonce, and answers eth_sendRawTransaction from a scripted list of admission errors (then accepts).
type fakeRootAnchorRPC struct {
	mu          sync.Mutex
	chainID     uint64
	pendingNone uint64 // next pending nonce reported
	sendErrors  []string
	onSend      func(attempt int) // called under lock before answering the attempt-th send (0-based)
	sent        []*ethtypes.Transaction
}

func (f *fakeRootAnchorRPC) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	reply := func(result any, rpcErr string) {
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != "" {
			resp["error"] = map[string]any{"code": -32000, "message": rpcErr}
		} else {
			resp["result"] = result
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Method {
	case "eth_chainId":
		reply(hexutil.EncodeUint64(f.chainID), "")
	case "eth_getTransactionCount":
		reply(hexutil.EncodeUint64(f.pendingNone), "")
	case "eth_sendRawTransaction":
		var rawHex string
		_ = json.Unmarshal(req.Params[0], &rawHex)
		raw, _ := hexutil.Decode(rawHex)
		tx := new(ethtypes.Transaction)
		if err := tx.UnmarshalBinary(raw); err != nil {
			reply(nil, "bad raw tx: "+err.Error())
			return
		}
		attempt := len(f.sent)
		f.sent = append(f.sent, tx)
		if f.onSend != nil {
			f.onSend(attempt)
		}
		if attempt < len(f.sendErrors) {
			reply(nil, f.sendErrors[attempt])
			return
		}
		reply(tx.Hash().Hex(), "")
	default:
		reply(nil, "unexpected method "+req.Method)
	}
}

func newSubmitTestWorker(t *testing.T, f *fakeRootAnchorRPC) (*CommitAttestationWorker, common.Address) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	require.NoError(t, err)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	w := NewCommitAttestationWorker(nil, client, 1, common.Address{}, "", hex.EncodeToString(crypto.FromECDSA(key)))
	return w, crypto.PubkeyToAddress(key.PublicKey)
}

func TestSignAndSubmit_HappyPath_SignedForGatewayWithCalldata(t *testing.T) {
	f := &fakeRootAnchorRPC{chainID: 9099, pendingNone: 7}
	w, submitter := newSubmitTestWorker(t, f)
	calldata := []byte{0xde, 0xad, 0xbe, 0xef}

	hash, err := w.signAndSubmit(context.Background(), calldata)
	require.NoError(t, err)
	require.Len(t, f.sent, 1)
	tx := f.sent[0]
	assert.Equal(t, tx.Hash(), hash)
	assert.Equal(t, uint64(7), tx.Nonce())
	assert.Equal(t, mt_common.GATEWAY_CONTRACT_ADDRESS, *tx.To())
	assert.Equal(t, calldata, tx.Data())
	assert.Equal(t, uint64(9099), tx.ChainId().Uint64(), "must be EIP-155 signed for the Root Anchor chain")
	from, err := ethtypes.Sender(ethtypes.NewEIP155Signer(tx.ChainId()), tx)
	require.NoError(t, err)
	assert.Equal(t, submitter, from, "must be signed by the configured submitter key")
}

// A mempool nonce race (another validator submitted first) is an admission error: the next attempt must re-read the
// pending nonce, not reuse the stale one.
func TestSignAndSubmit_NonceRace_RetriesWithFreshNonce(t *testing.T) {
	f := &fakeRootAnchorRPC{chainID: 9099, pendingNone: 5, sendErrors: []string{"nonce too low"}}
	f.onSend = func(attempt int) {
		if attempt == 0 {
			f.pendingNone = 6 // somebody else's tx took nonce 5
		}
	}
	w, _ := newSubmitTestWorker(t, f)

	_, err := w.signAndSubmit(context.Background(), []byte{1})
	require.NoError(t, err)
	require.Len(t, f.sent, 2)
	assert.Equal(t, uint64(5), f.sent[0].Nonce())
	assert.Equal(t, uint64(6), f.sent[1].Nonce(), "retry must use a fresh pending nonce")
}

// Contract-level messages never come back from eth_sendRawTransaction (they are receipt results), and no error text
// is treated as permanent: even a message that looks like one is retried like any admission error. This pins the
// design so string matching is not re-introduced.
func TestSignAndSubmit_ErrorTextIsNeverTreatedAsPermanent(t *testing.T) {
	for _, msg := range []string{"pubkey already submitted a share", "not a member", "epoch mismatch for source chain"} {
		t.Run(msg, func(t *testing.T) {
			f := &fakeRootAnchorRPC{chainID: 9099, pendingNone: 1, sendErrors: []string{msg}}
			w, _ := newSubmitTestWorker(t, f)
			_, err := w.signAndSubmit(context.Background(), []byte{1})
			require.NoError(t, err, "must retry and succeed on the second attempt")
			assert.Len(t, f.sent, 2)
		})
	}
}

func TestSignAndSubmit_ContextCancelStopsRetrying(t *testing.T) {
	always := make([]string, 20)
	for i := range always {
		always[i] = fmt.Sprintf("transient %d", i)
	}
	f := &fakeRootAnchorRPC{chainID: 9099, pendingNone: 1, sendErrors: always}
	w, _ := newSubmitTestWorker(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	f.onSend = func(attempt int) {
		if attempt == 0 {
			cancel()
		}
	}
	done := make(chan error, 1)
	go func() { _, err := w.signAndSubmit(ctx, []byte{1}); done <- err }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("signAndSubmit must stop promptly when the context is cancelled")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.LessOrEqual(t, len(f.sent), 2, "no further submissions after cancellation")
}

func TestSignAndSubmit_InvalidSubmitterKeyFailsBeforeAnyRPC(t *testing.T) {
	f := &fakeRootAnchorRPC{chainID: 9099}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	require.NoError(t, err)
	w := NewCommitAttestationWorker(nil, client, 1, common.Address{}, "", "not-hex")
	_, err = w.signAndSubmit(context.Background(), []byte{1})
	require.Error(t, err)
	assert.Empty(t, f.sent)
}
