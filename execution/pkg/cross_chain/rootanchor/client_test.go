package rootanchor

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
)

type jsonRPCRequest struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type jsonRPCResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mockRootAnchorServer serves a minimal eth_call/eth_sendRawTransaction JSON-RPC surface backed
// by a fixed cross_chain.ChainRegistry-shaped fixture, entirely independent of GatewayHandler —
// this package must not import execution/pkg/blockchain/tx_processor (that package imports
// pkg/config which would cycle back through this package's own deps in a test binary). The
// integration test in gateway_registry_sync_test.go (tx_processor package) covers the real
// GatewayHandler <-> Client round trip end to end; this file covers the RPC/circuit-breaker
// mechanics in isolation.
type mockRootAnchorServer struct {
	requests   int32
	gatewayABI abi.ABI
	fail       bool
}

func newMockRootAnchorServer(t *testing.T) *mockRootAnchorServer {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		t.Fatalf("parse GatewayABI: %v", err)
	}
	return &mockRootAnchorServer{gatewayABI: parsed}
}

func (m *mockRootAnchorServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&m.requests, 1)

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if m.fail {
		writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32000, Message: "simulated failure"})
		return
	}

	switch req.Method {
	case "eth_call":
		var params []interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params) == 0 {
			writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32602, Message: "bad params"})
			return
		}
		callObj, _ := params[0].(map[string]interface{})
		dataHex, _ := callObj["data"].(string)
		calldata, err := hexutil.Decode(dataHex)
		if err != nil || len(calldata) < 4 {
			writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32602, Message: "bad calldata"})
			return
		}
		method, err := m.gatewayABI.MethodById(calldata[:4])
		if err != nil {
			writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32601, Message: "unknown method"})
			return
		}
		if method.Name != "getChainRegistry" {
			writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32601, Message: "unhandled method"})
			return
		}
		args, err := method.Inputs.Unpack(calldata[4:])
		if err != nil {
			writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32602, Message: err.Error()})
			return
		}
		chainID := args[0].(*big.Int).Uint64()

		var out []byte
		if chainID == 101 {
			out, _ = method.Outputs.Pack(
				true,
				[][]byte{{0x01, 0x02}}, []uint64{1000}, [][]byte{{0xAA}},
				uint64(7), uint64(6667), common.HexToAddress("0x1234567890123456789012345678901234567890"),
				[32]byte{0xBE, 0xEF}, [32]byte{0xCA, 0xFE}, "https://example.com/archive", uint64(42),
			)
		} else {
			out, _ = method.Outputs.Pack(
				false,
				[][]byte{}, []uint64{}, [][]byte{},
				uint64(0), uint64(0), common.Address{}, [32]byte{}, [32]byte{}, "", uint64(0),
			)
		}
		writeJSONRPC(w, req.ID, hexutil.Encode(out), nil)

	case "eth_sendRawTransaction":
		writeJSONRPC(w, req.ID, "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", nil)

	case "eth_getTransactionCount":
		writeJSONRPC(w, req.ID, "0x2a", nil) // 42

	case "eth_chainId":
		writeJSONRPC(w, req.ID, "0x238b", nil) // 9099

	default:
		writeJSONRPC(w, req.ID, nil, &jsonRPCError{Code: -32601, Message: "method not found"})
	}
}

func writeJSONRPC(w http.ResponseWriter, id json.RawMessage, result interface{}, rpcErr *jsonRPCError) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{Jsonrpc: "2.0", Result: result, Error: rpcErr, ID: id})
}

func TestClient_GetChainRegistry_Found(t *testing.T) {
	mock := newMockRootAnchorServer(t)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	c, err := NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	registry, exists, err := c.GetChainRegistry(context.Background(), 101)
	if err != nil {
		t.Fatalf("GetChainRegistry: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
	if registry.Epoch != 7 || registry.QuorumThreshold != 6667 {
		t.Fatalf("unexpected registry: %+v", registry)
	}
	if registry.StateRoot != (common.Hash{0xBE, 0xEF}) || registry.AccountTreeRoot != (common.Hash{0xCA, 0xFE}) {
		t.Fatalf("stateRoot/accountTreeRoot not round-tripped correctly: %+v", registry)
	}
	if len(registry.Committee) != 1 || registry.Committee[0].Stake != 1000 {
		t.Fatalf("unexpected committee: %+v", registry.Committee)
	}
	if atomic.LoadInt32(&mock.requests) != 1 {
		t.Fatalf("expected exactly 1 HTTP request, got %d", mock.requests)
	}
}

func TestClient_GetChainRegistry_NotFound(t *testing.T) {
	mock := newMockRootAnchorServer(t)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	c, err := NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	registry, exists, err := c.GetChainRegistry(context.Background(), 999999)
	if err != nil {
		t.Fatalf("GetChainRegistry: %v", err)
	}
	if exists || registry != nil {
		t.Fatalf("expected exists=false, nil registry; got exists=%v registry=%+v", exists, registry)
	}
}

func TestClient_GetTransactionCount(t *testing.T) {
	mock := newMockRootAnchorServer(t)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	c, err := NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	nonce, err := c.GetTransactionCount(context.Background(), common.HexToAddress("0x1111111111111111111111111111111111111111"))
	if err != nil {
		t.Fatalf("GetTransactionCount: %v", err)
	}
	if nonce != 42 {
		t.Fatalf("nonce = %d, want 42", nonce)
	}
}

func TestClient_ChainID(t *testing.T) {
	mock := newMockRootAnchorServer(t)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	c, err := NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	chainID, err := c.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}
	if chainID.Uint64() != 9099 {
		t.Fatalf("chainID = %s, want 9099", chainID)
	}
}

func TestClient_SubmitTransaction(t *testing.T) {
	mock := newMockRootAnchorServer(t)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	c, err := NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	hash, err := c.SubmitTransaction(context.Background(), []byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatalf("SubmitTransaction: %v", err)
	}
	want := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if hash != want {
		t.Fatalf("hash = %s, want %s", hash.Hex(), want.Hex())
	}
}

func TestClient_NewClient_RequiresURL(t *testing.T) {
	if _, err := NewClient(nil, nil); err == nil {
		t.Fatal("expected error for empty URL list")
	}
}

// TestClient_CircuitBreakerOpensAndShortCircuits is the whole point of wrapping the client with
// pkg/network.CircuitBreaker: repeated failures must eventually stop hitting the network at all,
// and reopen automatically once the cooldown elapses (Zero-Fork: cross-chain paused, not fatal).
func TestClient_CircuitBreakerOpensAndShortCircuits(t *testing.T) {
	mock := newMockRootAnchorServer(t)
	mock.fail = true
	srv := httptest.NewServer(mock)
	defer srv.Close()

	c, err := NewClient([]string{srv.URL}, &network.CircuitBreakerConfig{
		MaxFailures: 2,
		MaxRequests: 1,
		Interval:    10 * time.Millisecond,
		Timeout:     200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// First 2 calls fail against the real (failing) server and trip the breaker.
	for i := 0; i < 2; i++ {
		if _, _, err := c.GetChainRegistry(context.Background(), 101); err == nil {
			t.Fatalf("call %d: expected failure from mock server", i)
		}
	}
	reqsBeforeOpen := atomic.LoadInt32(&mock.requests)
	if reqsBeforeOpen != 2 {
		t.Fatalf("expected 2 requests to have reached the server, got %d", reqsBeforeOpen)
	}

	// Breaker should now be open: the next call must fail WITHOUT reaching the server.
	_, _, err = c.GetChainRegistry(context.Background(), 101)
	if err != ErrCircuitOpen {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if got := atomic.LoadInt32(&mock.requests); got != reqsBeforeOpen {
		t.Fatalf("circuit was open but request count still grew: %d -> %d", reqsBeforeOpen, got)
	}
	if c.State() != network.StateOpen {
		t.Fatalf("expected breaker state Open, got %v", c.State())
	}

	// After the server recovers and the cooldown elapses, the breaker must let traffic through
	// again (half-open -> closed on success) — proving this is a temporary pause, not permanent.
	mock.fail = false
	time.Sleep(250 * time.Millisecond)

	registry, exists, err := c.GetChainRegistry(context.Background(), 101)
	if err != nil {
		t.Fatalf("expected recovery call to succeed, got: %v", err)
	}
	if !exists || registry.Epoch != 7 {
		t.Fatalf("unexpected registry after recovery: exists=%v registry=%+v", exists, registry)
	}
}

func TestClient_AllURLsFail_TriesEachInOrder(t *testing.T) {
	c, err := NewClient([]string{"http://127.0.0.1:1", "http://127.0.0.1:2"}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = c.GetChainRegistry(context.Background(), 101)
	if err == nil {
		t.Fatal("expected error when all URLs are unreachable")
	}
	t.Logf("got expected error: %v", err)
}
