package tx_processor

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
)

// The httptest.Server built inline in each test below speaks just enough JSON-RPC (eth_call) to
// be a genuine Root Anchor stand-in — this is the end-to-end proof (Milestone B plan doc,
// verification item 3) that a rootanchor.Client on one chain can read another chain's REAL
// GatewayHandler.HandleOffChainQuery output over the network, not a hand-rolled mock of the wire
// format.
func TestGatewayRegistryMonitor_DetectsDriftAgainstRealGatewayHandler(t *testing.T) {
	// "Root Anchor" — its own real ChainState, running the real GatewayHandler, with a newer
	// (epoch 9) committee for chain 101.
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	kp := bls.GenerateKeyPair()
	raEngine, err := loadGatewayEngine(rootAnchorCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (root anchor seed): %v", err)
	}
	raEngine.ChainRegistry[101] = cross_chain.ChainRegistry{
		ChainID: 101,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 500},
		},
		Epoch:           9,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(rootAnchorCS, raEngine); err != nil {
		t.Fatalf("saveGatewayEngine (root anchor seed): %v", err)
	}

	// A real HTTP JSON-RPC server backed by the real GatewayHandler.HandleOffChainQuery against
	// the "Root Anchor" ChainState above.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "eth_call" {
			writeRPCError(w, req.ID, "unsupported method in this test server: "+req.Method)
			return
		}
		var params []interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params) == 0 {
			writeRPCError(w, req.ID, "bad params")
			return
		}
		callObj, _ := params[0].(map[string]interface{})
		dataHex, _ := callObj["data"].(string)
		calldata, err := hexutil.Decode(dataHex)
		if err != nil {
			writeRPCError(w, req.ID, "bad calldata: "+err.Error())
			return
		}

		sender := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		result, err := h.HandleOffChainQuery(rootAnchorCS, viewTx)
		if err != nil {
			writeRPCError(w, req.ID, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(req.ID),
			"result":  hexutil.Encode(result),
		})
	}))
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("rootanchor.NewClient: %v", err)
	}

	// "Private chain" — its OWN real ChainState, locally committed with an OLDER (epoch 5)
	// committee for chain 101 (simulating it hasn't caught up to Root Anchor yet).
	privateChainCS, _, _, _ := newPersistentTestChainState(t)
	pcEngine, err := loadGatewayEngine(privateChainCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (private chain seed): %v", err)
	}
	pcEngine.ChainRegistry[101] = cross_chain.ChainRegistry{
		ChainID:         101,
		Committee:       []cross_chain.ValidatorEntry{{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 500}},
		Epoch:           5,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(privateChainCS, pcEngine); err != nil {
		t.Fatalf("saveGatewayEngine (private chain seed): %v", err)
	}

	monitor := NewGatewayRegistryMonitor(client, privateChainCS, time.Hour) // long interval: this test drives poll() directly, not the ticker

	if monitor.IsDrifting(101) {
		t.Fatal("expected no drift before any poll has run")
	}
	if _, ok := monitor.Snapshot(101); ok {
		t.Fatal("expected no snapshot before any poll has run")
	}

	monitor.poll(context.Background())

	if !monitor.IsDrifting(101) {
		t.Fatal("expected drift to be detected: local epoch=5, root anchor epoch=9")
	}
	snap, ok := monitor.Snapshot(101)
	if !ok {
		t.Fatal("expected a snapshot after a successful poll")
	}
	if snap.Epoch != 9 {
		t.Fatalf("snapshot epoch = %d, want 9", snap.Epoch)
	}

	// Crucially: the LOCAL, consensus-agreed ChainRegistry must be completely unchanged by the
	// poll — this monitor must never call saveGatewayEngine (see its fork-safety doc comment).
	pcEngineAfter, err := loadGatewayEngine(privateChainCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (private chain, after poll): %v", err)
	}
	if pcEngineAfter.ChainRegistry[101].Epoch != 5 {
		t.Fatalf("local ChainRegistry was mutated by the monitor! epoch = %d, want unchanged 5", pcEngineAfter.ChainRegistry[101].Epoch)
	}
}

// TestGatewayRegistryMonitor_NoDriftWhenEpochsMatch is the negative case: the monitor must not
// cry wolf when the local and remote registries actually agree.
func TestGatewayRegistryMonitor_NoDriftWhenEpochsMatch(t *testing.T) {
	rootAnchorCS, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	kp := bls.GenerateKeyPair()
	raEngine, err := loadGatewayEngine(rootAnchorCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (root anchor seed): %v", err)
	}
	raEngine.ChainRegistry[202] = cross_chain.ChainRegistry{
		ChainID:         202,
		Committee:       []cross_chain.ValidatorEntry{{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 750}},
		Epoch:           3,
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(rootAnchorCS, raEngine); err != nil {
		t.Fatalf("saveGatewayEngine (root anchor seed): %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var params []interface{}
		_ = json.Unmarshal(req.Params, &params)
		callObj, _ := params[0].(map[string]interface{})
		dataHex, _ := callObj["data"].(string)
		calldata, _ := hexutil.Decode(dataHex)
		sender := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
		viewTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, calldata))
		result, err := h.HandleOffChainQuery(rootAnchorCS, viewTx)
		if err != nil {
			writeRPCError(w, req.ID, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": hexutil.Encode(result),
		})
	}))
	defer srv.Close()

	client, err := rootanchor.NewClient([]string{srv.URL}, nil)
	if err != nil {
		t.Fatalf("rootanchor.NewClient: %v", err)
	}

	privateChainCS, _, _, _ := newPersistentTestChainState(t)
	pcEngine, err := loadGatewayEngine(privateChainCS)
	if err != nil {
		t.Fatalf("loadGatewayEngine (private chain seed): %v", err)
	}
	pcEngine.ChainRegistry[202] = cross_chain.ChainRegistry{
		ChainID:         202,
		Committee:       []cross_chain.ValidatorEntry{{PubkeyBLS: kp.PublicKey().Bytes(), Stake: 750}},
		Epoch:           3, // same as root anchor
		QuorumThreshold: 6667,
	}
	if err := saveGatewayEngine(privateChainCS, pcEngine); err != nil {
		t.Fatalf("saveGatewayEngine (private chain seed): %v", err)
	}

	monitor := NewGatewayRegistryMonitor(client, privateChainCS, time.Hour)
	monitor.poll(context.Background())

	if monitor.IsDrifting(202) {
		t.Fatal("expected no drift when local and remote epochs match")
	}
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]interface{}{"code": -32000, "message": msg},
	})
}
