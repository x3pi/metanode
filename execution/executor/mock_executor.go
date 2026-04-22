// Package executor — mock_executor.go provides mock implementations of the
// executor interfaces for use in unit and integration tests.
//
// These mocks allow testing Go code that interacts with Rust consensus
// without requiring a running Rust node. They record calls for verification
// and return configurable responses.
package executor

import (
	"sync"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// MockCommitReceiver implements CommitReceiver for testing.
type MockCommitReceiver struct {
	ch chan *pb.ExecutableBlock
}

// NewMockCommitReceiver creates a new MockCommitReceiver with the given buffer size.
func NewMockCommitReceiver(bufSize int) *MockCommitReceiver {
	return &MockCommitReceiver{
		ch: make(chan *pb.ExecutableBlock, bufSize),
	}
}

// ReceiveCommits returns the mock channel.
func (m *MockCommitReceiver) ReceiveCommits() <-chan *pb.ExecutableBlock {
	return m.ch
}

// SendBlock sends a block into the mock channel (used by test code).
func (m *MockCommitReceiver) SendBlock(block *pb.ExecutableBlock) {
	m.ch <- block
}

// Close closes the mock channel.
func (m *MockCommitReceiver) Close() {
	close(m.ch)
}

// MockEpochQueryService implements EpochQueryService for testing.
// Records all calls for later verification.
type MockEpochQueryService struct {
	mu sync.Mutex

	// Configurable return values
	LastBlockNumber    uint64
	LastBlockNumberErr error
	BoundaryData       *pb.EpochBoundaryData
	BoundaryDataErr    error
	AdvanceEpochErr    error

	// Call recording
	GetLastBlockNumberCalls    int
	GetEpochBoundaryDataCalls  []uint64 // epoch numbers requested
	AdvanceEpochCalls          []AdvanceEpochCall
}

// AdvanceEpochCall records the arguments of an AdvanceEpoch call.
type AdvanceEpochCall struct {
	Epoch       uint64
	TimestampMs uint64
}

// GetLastBlockNumber returns the configured last block number.
func (m *MockEpochQueryService) GetLastBlockNumber() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetLastBlockNumberCalls++
	return m.LastBlockNumber, m.LastBlockNumberErr
}

// GetEpochBoundaryData returns the configured boundary data.
func (m *MockEpochQueryService) GetEpochBoundaryData(epoch uint64) (*pb.EpochBoundaryData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GetEpochBoundaryDataCalls = append(m.GetEpochBoundaryDataCalls, epoch)
	return m.BoundaryData, m.BoundaryDataErr
}

// AdvanceEpoch records the call and returns the configured error.
func (m *MockEpochQueryService) AdvanceEpoch(epoch uint64, timestampMs uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AdvanceEpochCalls = append(m.AdvanceEpochCalls, AdvanceEpochCall{
		Epoch:       epoch,
		TimestampMs: timestampMs,
	})
	return m.AdvanceEpochErr
}

// MockBlockSyncService implements BlockSyncService for testing.
type MockBlockSyncService struct {
	mu sync.Mutex

	HandleSyncBlocksErr   error
	HandleSyncBlocksCalls int
	ReceivedBlocks        [][]*pb.ExecutableBlock
}

// HandleSyncBlocks records the call and returns the configured error.
func (m *MockBlockSyncService) HandleSyncBlocks(blocks []*pb.ExecutableBlock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HandleSyncBlocksCalls++
	m.ReceivedBlocks = append(m.ReceivedBlocks, blocks)
	return m.HandleSyncBlocksErr
}
