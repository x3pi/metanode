package main

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/protocolparams"
)

func report(mutate func(*config.SimpleChainConfig)) protocolparams.Report {
	cfg := &config.SimpleChainConfig{TxSignatureMode: "secp", AccountGate: "parent_registered", FreeFeeAddresses: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if mutate != nil {
		mutate(cfg)
	}
	return protocolparams.FromConfig(cfg, &config.GenesisConfig{ChainId: big.NewInt(991)}).Report(false)
}

func ok(r protocolparams.Report) nodeResult { return nodeResult{Report: r} }

func TestCompareAllAgree(t *testing.T) {
	text, code := compare(map[string]nodeResult{"m0": ok(report(nil)), "m1": ok(report(nil)), "m2": ok(report(nil))})
	if code != 0 || !strings.Contains(text, "All 3 node(s) agree") {
		t.Fatalf("code=%d\n%s", code, text)
	}
}

func TestCompareNamesTheNodeAndTheSetting(t *testing.T) {
	odd := report(func(c *config.SimpleChainConfig) { c.AccountGate = ""; c.CrossChain.BondLeverage = 7 })
	text, code := compare(map[string]nodeResult{"m0": ok(report(nil)), "m1": ok(report(nil)), "m2": ok(report(nil)), "m3": ok(odd)})
	if code != 1 {
		t.Fatalf("code=%d\n%s", code, text)
	}
	for _, want := range []string{"SETTINGS DIFFER", "m3 have fingerprint", "account_gate", "reference=parent_registered", "this node=off", "cross_chain.bond_leverage", "this node=7"} {
		if !strings.Contains(text, want) {
			t.Errorf("report should contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tx_signature_mode") {
		t.Errorf("a setting that does not differ must not be listed:\n%s", text)
	}
}

func TestCompareUnreachableNodeIsNotAgreement(t *testing.T) {
	text, code := compare(map[string]nodeResult{"m0": ok(report(nil)), "m1": {Err: errors.New("connection refused")}})
	if code != 2 || !strings.Contains(text, "could not be asked: m1") || strings.Contains(text, "agree:") {
		t.Fatalf("an unreachable node must never be reported as agreeing; code=%d\n%s", code, text)
	}
	// a real difference outranks an unreachable node
	odd := report(func(c *config.SimpleChainConfig) { c.AccountGate = "" })
	_, code = compare(map[string]nodeResult{"m0": ok(report(nil)), "m1": ok(odd), "m2": {Err: errors.New("x")}})
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
}

func TestCompareTieIsDeterministic(t *testing.T) {
	a, b := report(nil), report(func(c *config.SimpleChainConfig) { c.AccountGate = "" })
	first, _ := compare(map[string]nodeResult{"m0": ok(a), "m1": ok(b)})
	for i := 0; i < 20; i++ {
		again, _ := compare(map[string]nodeResult{"m0": ok(a), "m1": ok(b)})
		if again != first {
			t.Fatalf("verdict changes between runs:\n%s\n---\n%s", first, again)
		}
	}
}

// the real thing over HTTP: three fake nodes, one of them with a different setting
func TestQueryAllOverHTTP(t *testing.T) {
	serve := func(r protocolparams.Report) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var in struct{ Method string }
			_ = json.NewDecoder(req.Body).Decode(&in)
			if in.Method != "mtn_protocolFingerprint" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": r})
		}))
	}
	good1, good2 := serve(report(nil)), serve(report(nil))
	odd := serve(report(func(c *config.SimpleChainConfig) { c.CrossChain.VelocityLimitDisabled = true }))
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer func() { good1.Close(); good2.Close(); odd.Close(); old.Close() }()

	res := queryAll(map[string]string{"m0": good1.URL, "m1": good2.URL, "m2": odd.URL}, 3*time.Second)
	text, code := compare(res)
	if code != 1 || !strings.Contains(text, "cross_chain.velocity_limit_disabled") || !strings.Contains(text, "m2 have fingerprint") {
		t.Fatalf("code=%d\n%s", code, text)
	}

	res = queryAll(map[string]string{"m0": good1.URL, "legacy": old.URL}, 3*time.Second)
	if res["legacy"].Err == nil || !strings.Contains(res["legacy"].Err.Error(), "node too old") {
		t.Fatalf("a node without the method must be reported as an error, got %+v", res["legacy"])
	}
}

func TestLoadNodesBothShapes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for body, want := range map[string]int{`{"nodes":{"a":"http://x","b":"http://y"},"roles":{"a":"validator"}}`: 2, `{"a":"http://x"}`: 1} {
		got, err := loadNodes(write("n.json", body))
		if err != nil || len(got) != want {
			t.Fatalf("%s: %v %v", body, got, err)
		}
	}
	if _, err := loadNodes(write("bad.json", `[]`)); err == nil {
		t.Fatal("a file with no nodes must be an error")
	}
}

func TestCompareLabelsTheRaftLimitAsOperational(t *testing.T) {
	odd := report(func(c *config.SimpleChainConfig) { c.Raft = &config.RaftConfig{MaxBatchBytes: 1 << 20} })
	ref := report(func(c *config.SimpleChainConfig) { c.Raft = &config.RaftConfig{MaxBatchBytes: 2 << 20} })
	odd2 := report(func(c *config.SimpleChainConfig) {
		c.AccountGate = ""
		c.Raft = &config.RaftConfig{MaxBatchBytes: 2 << 20}
	})
	text, code := compare(map[string]nodeResult{"m0": ok(ref), "m1": ok(ref), "m2": ok(odd)})
	if code != 1 || !strings.Contains(text, "raft_max_batch_bytes") || !strings.Contains(text, "operational") {
		t.Fatalf("a differing raft limit must be reported and labelled operational: code=%d\n%s", code, text)
	}
	text, _ = compare(map[string]nodeResult{"m0": ok(ref), "m1": ok(ref), "m2": ok(odd2)})
	if strings.Contains(text, "operational") || !strings.Contains(text, "account_gate") {
		t.Fatalf("a consensus-relevant setting must not carry the operational label:\n%s", text)
	}
}
