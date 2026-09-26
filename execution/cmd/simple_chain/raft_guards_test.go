package main

import (
	"context"
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
