// Package raftfeed is the switch and the (future) block/batch source for consensus_mode = "raft".
//
// C1 STUB: this package currently only answers "is raft mode on?" and fails closed for everything
// else. It exists so the default-off hooks (H1-H6) in the older code have one place to ask, and so
// each Rust-consensus entry point can be guarded before the real Raft cluster is written.
package raftfeed

import (
	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// Enabled reports whether consensus_mode is exactly "raft". With any other value (including empty)
// the node behaves as before: Rust consensus, no raft code path is taken.
func Enabled() bool {
	return config.ConfigApp != nil && config.ConfigApp.ConsensusMode == "raft"
}

// Submit hands a marshalled transaction batch to the Raft leader.
//
// STUB: always returns false (nothing is accepted) until C1/C2 implement the cluster.
//
// WARNING for the real implementation: tx_batch_forwarder retries a false result forever
// (sleep 100ms between attempts). A real Submit must therefore return true only once the batch is
// accepted (queued with a bounded buffer or replicated); it must not be left returning false while
// there is no leader, or the forwarder spins indefinitely and stops draining the mempool.
func Submit(batch []byte) bool {
	return false
}

// Ready reports whether the node may accept transactions: it must know the leader and have caught
// up with the tip.
//
// STUB: always false (fail closed) until the cluster is implemented.
func Ready() bool {
	return false
}
