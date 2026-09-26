package raftfeed

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

func withConsensusMode(t *testing.T, cfg *config.SimpleChainConfig) {
	t.Helper()
	prev := config.ConfigApp
	config.ConfigApp = cfg
	t.Cleanup(func() { config.ConfigApp = prev })
}

// The raft feed must be OFF unless consensus_mode is exactly "raft" (default-off, T-OFF-*).
func TestEnabled_OnlyForExactRaftMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.SimpleChainConfig
		want bool
	}{
		{"nil config", nil, false},
		{"empty mode (default Rust consensus)", &config.SimpleChainConfig{}, false},
		{"raft", &config.SimpleChainConfig{ConsensusMode: "raft"}, true},
		{"unknown mode", &config.SimpleChainConfig{ConsensusMode: "rust"}, false},
		{"case differs", &config.SimpleChainConfig{ConsensusMode: "Raft"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withConsensusMode(t, tc.cfg)
			if got := Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Until C1 wires a real Raft cluster, the feed must fail closed: never report ready and never
// accept a batch (so nothing can be dispatched into an unreplicated void).
func TestSubmitAndReady_FailClosedUntilC1(t *testing.T) {
	withConsensusMode(t, &config.SimpleChainConfig{ConsensusMode: "raft"})
	if Ready() {
		t.Fatal("Ready() must be false until the Raft cluster is implemented")
	}
	if Submit([]byte{0x01}) {
		t.Fatal("Submit() must not accept a batch until the Raft cluster is implemented")
	}
}
