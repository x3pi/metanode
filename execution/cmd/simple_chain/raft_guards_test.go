package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// These tests prove that, in raft mode, the RPC entry points that used to call into the Rust
// consensus library return a clear error and never reach the FFI (which is not initialised).

func setRaftMode(t *testing.T, mode string) {
	t.Helper()
	prev := config.ConfigApp
	config.ConfigApp = &config.SimpleChainConfig{ConsensusMode: mode, Securepassword: "pw"}
	t.Cleanup(func() { config.ConfigApp = prev })
}

func requireUnsupportedInRaft(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "unsupported in raft mode") {
		t.Fatalf("expected an 'unsupported in raft mode' error, got %v", err)
	}
}

func TestRaftMode_MetaAPIVoteRPCsAreUnsupported(t *testing.T) {
	setRaftMode(t, "raft")
	api := &MetaAPI{}
	_, err := api.GetConsensusVotes(context.Background())
	requireUnsupportedInRaft(t, err)
	_, err = api.GetCommitVotes(context.Background(), 1)
	requireUnsupportedInRaft(t, err)
}

func TestRaftMode_MtnAPIVoteRPCsAreUnsupported(t *testing.T) {
	setRaftMode(t, "raft")
	api := &MtnAPI{}
	_, err := api.GetConsensusVotes(context.Background())
	requireUnsupportedInRaft(t, err)
	_, err = api.GetCommitVotes(context.Background(), 1)
	requireUnsupportedInRaft(t, err)
}

func TestRaftMode_AdminAttestPayloadLossIsUnsupportedAfterAuth(t *testing.T) {
	setRaftMode(t, "raft")
	api := &AdminApi{App: &App{config: &config.SimpleChainConfig{Securepassword: "pw"}}}

	// Authentication still comes first: a wrong password must be rejected as before.
	if _, err := api.AttestPayloadLoss(context.Background(), "wrong", 1, "00"); err != errInvalidCredentials {
		t.Fatalf("wrong password must yield errInvalidCredentials, got %v", err)
	}
	if _, err := api.AttestPayloadLossForCommit(context.Background(), "wrong", 1); err != errInvalidCredentials {
		t.Fatalf("wrong password must yield errInvalidCredentials, got %v", err)
	}

	_, err := api.AttestPayloadLoss(context.Background(), "pw", 1, "00")
	requireUnsupportedInRaft(t, err)
	_, err = api.AttestPayloadLossForCommit(context.Background(), "pw", 1)
	requireUnsupportedInRaft(t, err)
}

// ConsensusReady must report not-ready in raft mode without asking the Rust layer.
func TestRaftMode_ConsensusReadyIsFalseUntilFeedIsReady(t *testing.T) {
	setRaftMode(t, "raft")
	res := (&MetaAPI{}).ConsensusReady()
	if ready, _ := res["ready"].(bool); ready {
		t.Fatalf("ConsensusReady must be false in raft mode until the feed is ready, got %v", res)
	}
	if res["note"] == nil || res["note"] == "" {
		t.Fatal("a not-ready response must explain why")
	}
}

func TestIssue103_SkipMempoolSigVerifyProductionGuard(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	cfgContent := `{"chainId": 991, "consensus_mode": "raft", "Databases": {"RootPath": "` + tmpDir + `"}}`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")

	// Case 1: Fail-closed: No METANODE_DEVNET=true set, even if NODE_ENV is unset/empty
	t.Setenv("METANODE_DEVNET", "")
	t.Setenv("NODE_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("METANODE_ENV", "")
	config.ResetConfigForTesting()
	_, err := NewApp(cfgPath, 0)
	if err == nil || !strings.Contains(err.Error(), "FATAL SECURITY VIOLATION (Issue #103)") {
		t.Fatalf("expected fatal security violation error when METANODE_DEVNET is not set, got: %v", err)
	}

	// Case 2: METANODE_DEVNET=true but in production environment -> Must still fail
	t.Setenv("METANODE_DEVNET", "true")
	t.Setenv("NODE_ENV", "production")
	config.ResetConfigForTesting()
	_, err = NewApp(cfgPath, 0)
	if err == nil || !strings.Contains(err.Error(), "FATAL SECURITY VIOLATION (Issue #103)") {
		t.Fatalf("expected fatal security violation error for SKIP_MEMPOOL_SIG_VERIFY in production, got: %v", err)
	}

	// Case 3a: Explicit devnet (METANODE_DEVNET=true) but consensus_mode="raft" -> Must fail
	t.Setenv("METANODE_DEVNET", "true")
	t.Setenv("NODE_ENV", "development")
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("METANODE_ENV", "development")
	cfgRaftDevPath := filepath.Join(tmpDir, "config_raft_dev.json")
	cfgRaftDevContent := `{"chainId": 1337, "consensus_mode": "raft", "Databases": {"RootPath": "` + tmpDir + `"}}`
	if err := os.WriteFile(cfgRaftDevPath, []byte(cfgRaftDevContent), 0644); err != nil {
		t.Fatalf("failed to write raft dev config: %v", err)
	}
	config.ResetConfigForTesting()
	_, err = NewApp(cfgRaftDevPath, 0)
	if err == nil || !strings.Contains(err.Error(), "strictly forbidden when consensus_mode is 'raft'") {
		t.Fatalf("expected fatal security violation error for raft mode with SKIP_MEMPOOL_SIG_VERIFY, got: %v", err)
	}

	// Case 3b: Explicit devnet, consensus_mode="rust", but privacy_mode=true -> Must fail
	cfgPrivacyPath := filepath.Join(tmpDir, "config_privacy.json")
	cfgPrivacyContent := `{"chainId": 1337, "consensus_mode": "rust", "privacy_mode": true, "Databases": {"RootPath": "` + tmpDir + `"}}`
	if err := os.WriteFile(cfgPrivacyPath, []byte(cfgPrivacyContent), 0644); err != nil {
		t.Fatalf("failed to write privacy config: %v", err)
	}
	config.ResetConfigForTesting()
	_, err = NewApp(cfgPrivacyPath, 0)
	if err == nil || !strings.Contains(err.Error(), "strictly forbidden when consensus_mode is 'raft' or privacy_mode is true") {
		t.Fatalf("expected fatal security violation error for privacy mode with SKIP_MEMPOOL_SIG_VERIFY, got: %v", err)
	}

	// Case 3c: Explicit devnet (METANODE_DEVNET=true), non-production, non-991 chainId (e.g. 1337), consensus_mode="rust", privacy_mode=false -> Should pass startup guard
	cfgDevPath := filepath.Join(tmpDir, "config_dev.json")
	cfgDevContent := `{"chainId": 1337, "consensus_mode": "rust", "privacy_mode": false, "Databases": {"RootPath": "` + tmpDir + `"}}`
	if err := os.WriteFile(cfgDevPath, []byte(cfgDevContent), 0644); err != nil {
		t.Fatalf("failed to write dev config: %v", err)
	}
	config.ResetConfigForTesting()
	_, err = NewApp(cfgDevPath, 0)
	if err != nil && strings.Contains(err.Error(), "FATAL SECURITY VIOLATION (Issue #103)") {
		t.Fatalf("unexpected Issue #103 security violation in explicit devnet mode: %v", err)
	}

	// Case 3d: Explicit devnet (METANODE_DEVNET=true) on production chain (chainId: 991) -> MUST FAIL (P0-2)
	cfgProdChainPath := filepath.Join(tmpDir, "config_prod_chain.json")
	cfgProdChainContent := `{"chainId": 991, "consensus_mode": "rust", "privacy_mode": false, "Databases": {"RootPath": "` + tmpDir + `"}}`
	if err := os.WriteFile(cfgProdChainPath, []byte(cfgProdChainContent), 0644); err != nil {
		t.Fatalf("failed to write prod chain config: %v", err)
	}
	config.ResetConfigForTesting()
	_, err = NewApp(cfgProdChainPath, 0)
	if err == nil || !strings.Contains(err.Error(), "strictly forbidden on production chain (Chain ID 991)") {
		t.Fatalf("expected fatal security violation on chain 991 even with METANODE_DEVNET=true, got: %v", err)
	}
}
