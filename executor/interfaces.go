// Package executor — interfaces.go defines Go interfaces for the Rust↔Go IPC
// boundary layer. These interfaces allow unit testing of code that depends on
// executor functionality without requiring a running Rust consensus node.
//
// This mirrors the TExecutorClient trait defined on the Rust side in
// metanode/src/node/executor_client/traits.rs.
package executor

import (
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// CommitReceiver represents the receive-side of the Rust→Go commit channel.
// Implementations include the real SocketExecutor listener and test mocks.
type CommitReceiver interface {
	// ReceiveCommits returns a read-only channel that yields committed blocks
	// from Rust consensus in GlobalExecIndex order.
	ReceiveCommits() <-chan *pb.ExecutableBlock
}

// EpochQueryService defines the Go-side interface for epoch-related queries
// that Rust calls via the Unix Domain Socket RPC protocol.
//
// In production, these are handled by unix_socket_handler_epoch.go.
// In tests, a mock can be injected to verify correct epoch transition behavior.
type EpochQueryService interface {
	// GetLastBlockNumber returns the current chain height known to Go.
	GetLastBlockNumber() (uint64, error)

	// GetEpochBoundaryData returns epoch boundary information for the given epoch.
	GetEpochBoundaryData(epoch uint64) (*pb.EpochBoundaryData, error)

	// AdvanceEpoch notifies Go to advance to the given epoch number.
	AdvanceEpoch(epoch uint64, timestampMs uint64) error
}

// BlockSyncService defines the interface for bulk block synchronization
// from Rust to Go (used by SyncOnly nodes to receive blocks from peers).
type BlockSyncService interface {
	// HandleSyncBlocks processes a batch of blocks received from peer nodes
	// during catch-up synchronization.
	HandleSyncBlocks(blocks []*pb.ExecutableBlock) error
}
