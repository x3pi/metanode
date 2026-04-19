package executor

import (
	"testing"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ──────────────────────────────────────────────────────────────────────────────
// Tests for MockCommitReceiver
// ──────────────────────────────────────────────────────────────────────────────

func TestMockCommitReceiver_SendAndReceive(t *testing.T) {
	mock := NewMockCommitReceiver(10)
	defer mock.Close()

	block := &pb.ExecutableBlock{
		GlobalExecIndex: 42,
	}
	mock.SendBlock(block)

	select {
	case received := <-mock.ReceiveCommits():
		if received.GlobalExecIndex != 42 {
			t.Errorf("expected GEI 42, got %d", received.GlobalExecIndex)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for block")
	}
}

func TestMockCommitReceiver_MultipleBlocks(t *testing.T) {
	mock := NewMockCommitReceiver(10)
	defer mock.Close()

	for i := uint64(1); i <= 5; i++ {
		mock.SendBlock(&pb.ExecutableBlock{GlobalExecIndex: i})
	}

	for i := uint64(1); i <= 5; i++ {
		select {
		case received := <-mock.ReceiveCommits():
			if received.GlobalExecIndex != i {
				t.Errorf("expected GEI %d, got %d", i, received.GlobalExecIndex)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("timeout waiting for block %d", i)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests for MockEpochQueryService
// ──────────────────────────────────────────────────────────────────────────────

func TestMockEpochQueryService_GetLastBlockNumber(t *testing.T) {
	mock := &MockEpochQueryService{LastBlockNumber: 100}

	n, err := mock.GetLastBlockNumber()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 100 {
		t.Errorf("expected 100, got %d", n)
	}
	if mock.GetLastBlockNumberCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.GetLastBlockNumberCalls)
	}
}

func TestMockEpochQueryService_AdvanceEpoch(t *testing.T) {
	mock := &MockEpochQueryService{}

	err := mock.AdvanceEpoch(5, 1700000000000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.AdvanceEpochCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.AdvanceEpochCalls))
	}
	call := mock.AdvanceEpochCalls[0]
	if call.Epoch != 5 {
		t.Errorf("expected epoch 5, got %d", call.Epoch)
	}
	if call.TimestampMs != 1700000000000 {
		t.Errorf("expected timestamp 1700000000000, got %d", call.TimestampMs)
	}
}

func TestMockEpochQueryService_GetEpochBoundaryData(t *testing.T) {
	mock := &MockEpochQueryService{
		BoundaryData: &pb.EpochBoundaryData{},
	}

	_, err := mock.GetEpochBoundaryData(3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.GetEpochBoundaryDataCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.GetEpochBoundaryDataCalls))
	}
	if mock.GetEpochBoundaryDataCalls[0] != 3 {
		t.Errorf("expected epoch 3, got %d", mock.GetEpochBoundaryDataCalls[0])
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests for MockBlockSyncService
// ──────────────────────────────────────────────────────────────────────────────

func TestMockBlockSyncService_HandleSyncBlocks(t *testing.T) {
	mock := &MockBlockSyncService{}

	blocks := []*pb.ExecutableBlock{
		{GlobalExecIndex: 10},
		{GlobalExecIndex: 11},
	}
	err := mock.HandleSyncBlocks(blocks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.HandleSyncBlocksCalls != 1 {
		t.Errorf("expected 1 call, got %d", mock.HandleSyncBlocksCalls)
	}
	if len(mock.ReceivedBlocks) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(mock.ReceivedBlocks))
	}
	if len(mock.ReceivedBlocks[0]) != 2 {
		t.Errorf("expected 2 blocks in batch, got %d", len(mock.ReceivedBlocks[0]))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Interface compliance tests (compile-time checks)
// ──────────────────────────────────────────────────────────────────────────────

var _ CommitReceiver = (*MockCommitReceiver)(nil)
var _ EpochQueryService = (*MockEpochQueryService)(nil)
var _ BlockSyncService = (*MockBlockSyncService)(nil)
