package processor

import (
	"fmt"
	"net"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
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
