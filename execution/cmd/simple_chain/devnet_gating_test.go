package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/rpc"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

func pipelineResetCall(t *testing.T, cfg *config.SimpleChainConfig, method, origin, password string) int {
	t.Helper()
	app := &App{config: cfg, blockProcessor: &processor.BlockProcessor{}}
	req := httptest.NewRequest(method, "/pipeline/reset", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if password != "" {
		req.Header.Set("X-Admin-Password", password)
	}
	rec := httptest.NewRecorder()
	pipelineResetHandler(app)(rec, req)
	return rec.Code
}

func TestPipelineReset_RequiresAdminPasswordAndNoBrowserOrigin(t *testing.T) {
	cfg := &config.SimpleChainConfig{Securepassword: "pw"}
	cases := []struct {
		name                     string
		method, origin, password string
		cfg                      *config.SimpleChainConfig
		wantCode                 int
		wantReset                bool
	}{
		{"no password", http.MethodPost, "", "", cfg, http.StatusUnauthorized, false},
		{"wrong password", http.MethodPost, "", "nope", cfg, http.StatusUnauthorized, false},
		{"browser origin even with right password", http.MethodPost, "https://evil.example", "pw", cfg, http.StatusForbidden, false},
		{"GET", http.MethodGet, "", "pw", cfg, http.StatusMethodNotAllowed, false},
		{"password not configured", http.MethodPost, "", "", &config.SimpleChainConfig{}, http.StatusForbidden, false},
		{"correct", http.MethodPost, "", "pw", cfg, http.StatusOK, true},
	}
	for _, c := range cases {
		processor.GlobalPipelineStats.IncrTxsReceived(7)
		before := processor.GlobalPipelineStats.Snapshot().TxsReceived
		if got := pipelineResetCall(t, c.cfg, c.method, c.origin, c.password); got != c.wantCode {
			t.Fatalf("%s: status %d, want %d", c.name, got, c.wantCode)
		}
		after := processor.GlobalPipelineStats.Snapshot().TxsReceived
		if c.wantReset && after != 0 {
			t.Fatalf("%s: counters not reset (%d)", c.name, after)
		}
		if !c.wantReset && after != before {
			t.Fatalf("%s: counters changed by a refused request (%d -> %d)", c.name, before, after)
		}
	}
}

func TestSendCrossChainTransfer_DisabledUnlessDevnetRPC(t *testing.T) {
	// Debug on is not enough: the shipped exec-cluster template runs with debug=true.
	api := &MtnAPI{App: &App{config: &config.SimpleChainConfig{Debug: true}}}
	if _, err := api.SendCrossChainTransfer(context.Background(), "0x0000000000000000000000000000000000000001", "0x1", nil, nil); err != errDevnetRPCDisabled {
		t.Fatalf("want errDevnetRPCDisabled, got %v", err)
	}
}

func TestVerifyHistoricalRoot_NotInPublicEthNamespaceAndAdminProtected(t *testing.T) {
	srv := rpc.NewServer()
	if err := srv.RegisterName("eth", &MetaAPI{}); err != nil {
		t.Fatal(err)
	}
	if err := srv.RegisterName("admin", &AdminApi{App: &App{config: &config.SimpleChainConfig{Securepassword: "pw"}}}); err != nil {
		t.Fatal(err)
	}
	cl := rpc.DialInProc(srv)
	defer cl.Close()

	var out map[string]interface{}
	err := cl.Call(&out, "eth_verifyHistoricalRoot", "0x1")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("eth_verifyHistoricalRoot must not be exposed, got %v", err)
	}
	err = cl.Call(&out, "admin_verifyHistoricalRoot", "wrong", "0x1")
	if err == nil || !strings.Contains(err.Error(), errInvalidCredentials.Error()) {
		t.Fatalf("wrong password must be rejected, got %v", err)
	}
	// Right password reaches the method; with no MetaAPI wired it reports unavailable instead of rebuilding anything.
	err = cl.Call(&out, "admin_verifyHistoricalRoot", "pw", "0x1")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("authenticated call must reach the method, got %v", err)
	}
}
