package processor

import (
	"fmt"
	"net"
	"reflect"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

// In raft mode the peer-discovery TCP executor (a Rust-consensus-era component) must not be started.
func TestRaftMode_PeerDiscoverySocketIsNotStarted(t *testing.T) {
	prev := config.ConfigApp
	config.ConfigApp = &config.SimpleChainConfig{ConsensusMode: "raft"}
	t.Cleanup(func() { config.ConfigApp = prev })

	// Pick a free port, release it, then check that the guarded call does not bind it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	bp := &BlockProcessor{}
	bp.runPeerDiscoverySocket(port) // must return immediately in raft mode

	l2, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		t.Fatalf("port %d was bound by runPeerDiscoverySocket in raft mode: %v", port, err)
	}
	_ = l2.Close()
}

// The forwarder must send batches to the raft feed ONLY in raft mode, and to the Rust FFI otherwise (default-off).
func TestBatchSubmitter_RaftFeedOnlyInRaftMode(t *testing.T) {
	prev := config.ConfigApp
	t.Cleanup(func() { config.ConfigApp = prev })

	config.ConfigApp = &config.SimpleChainConfig{ConsensusMode: "raft"}
	if got, want := reflect.ValueOf(batchSubmitter()).Pointer(), reflect.ValueOf(raftfeed.Submit).Pointer(); got != want {
		t.Fatal("in raft mode the forwarder must submit to raftfeed.Submit")
	}

	for _, cfg := range []*config.SimpleChainConfig{nil, {}, {ConsensusMode: "rust"}} {
		config.ConfigApp = cfg
		if got, raft := reflect.ValueOf(batchSubmitter()).Pointer(), reflect.ValueOf(raftfeed.Submit).Pointer(); got == raft {
			t.Fatalf("with consensus_mode %+v the forwarder must NOT use the raft feed", cfg)
		}
	}
}

// With no feed running, the raft path must refuse (return false) so the forwarder keeps and retries the batch.
func TestBatchSubmitter_RaftModeWithoutFeedRefusesInsteadOfLosingTheBatch(t *testing.T) {
	prev := config.ConfigApp
	config.ConfigApp = &config.SimpleChainConfig{ConsensusMode: "raft"}
	t.Cleanup(func() { config.ConfigApp = prev })
	if batchSubmitter()([]byte{0x01}) {
		t.Fatal("no feed is running: the batch must not be reported as accepted")
	}
}
