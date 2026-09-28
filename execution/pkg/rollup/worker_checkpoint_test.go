package rollup

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

type mockStateProvider struct {
	epoch     uint64
	stateRoot common.Hash
}

func (m *mockStateProvider) GetLatestEpoch() (uint64, common.Hash) {
	return m.epoch, m.stateRoot
}

func TestCheckpointWorker_SubmitSuccess(t *testing.T) {
	kp := bls.GenerateKeyPair()
	client := &mockParentChainClient{}
	stateProv := &mockStateProvider{
		epoch:     1,
		stateRoot: common.HexToHash("0x123abc"),
	}

	worker := NewCheckpointWorker(client, stateProv, kp, kp.PublicKey(), 10*time.Millisecond)
	worker.Start()

	// Wait for worker to submit
	time.Sleep(20 * time.Millisecond)
	worker.Stop()

	// Verify client received the call
	client.mu.Lock()
	defer client.mu.Unlock()
	
	if client.submitStateRootCount != 1 {
		t.Errorf("Expected 1 submit state root call, got %d", client.submitStateRootCount)
	}
	
	if worker.lastEpoch != 1 {
		t.Errorf("Expected worker lastEpoch to be 1, got %d", worker.lastEpoch)
	}
}

func TestCheckpointWorker_RetryOnFailure(t *testing.T) {
	kp := bls.GenerateKeyPair()
	client := &mockParentChainClient{}
	client.shouldFailSubmit = true // Force failure

	stateProv := &mockStateProvider{
		epoch:     2,
		stateRoot: common.HexToHash("0xdef456"),
	}

	worker := NewCheckpointWorker(client, stateProv, kp, kp.PublicKey(), 10*time.Millisecond)
	worker.Start()

	// Wait for worker to attempt submit (and fail)
	time.Sleep(20 * time.Millisecond)
	
	client.mu.Lock()
	if client.submitStateRootCount == 0 {
		t.Errorf("Expected worker to attempt submission")
	}
	if worker.lastEpoch == 2 {
		t.Errorf("Worker should not advance lastEpoch on failure")
	}
	client.shouldFailSubmit = false // Fix failure
	client.mu.Unlock()

	// Wait for next tick to retry
	time.Sleep(20 * time.Millisecond)
	worker.Stop()
	
	if worker.lastEpoch != 2 {
		t.Errorf("Expected worker lastEpoch to be 2 after successful retry, got %d", worker.lastEpoch)
	}
}

func TestCheckpointWorker_WakeupDoesNotBlock(t *testing.T) {
	kp := bls.GenerateKeyPair()
	client := &mockParentChainClient{}
	stateProv := &mockStateProvider{
		epoch:     3,
		stateRoot: common.HexToHash("0x789xyz"),
	}
	
	worker := NewCheckpointWorker(client, stateProv, kp, kp.PublicKey(), 1*time.Hour)
	
	// Call wakeup repeatedly to ensure it doesn't block
	for i := 0; i < 10; i++ {
		worker.Wakeup()
	}
	
	worker.Start()
	time.Sleep(20 * time.Millisecond)
	worker.Stop()
	
	if worker.lastEpoch != 3 {
		t.Errorf("Expected worker to submit on wakeup, lastEpoch=%d", worker.lastEpoch)
	}
}
