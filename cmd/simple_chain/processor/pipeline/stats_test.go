package pipeline

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestPipelineStats_IncrAndSnapshot(t *testing.T) {
	ps := &PipelineStats{}
	ps.SetNodeRole("master")
	ps.IncrTxsReceived(100)
	ps.IncrTxsForwarded(90)
	ps.IncrBlocksReceived(5)
	ps.IncrTxsCommitted(80)
	ps.SetPoolSize(10)
	ps.SetPendingSize(3)
	ps.SetLastBlock(42)
	ps.SetLastCommitTimeUs(1500)

	snap := ps.Snapshot()
	if snap.NodeRole != "master" {
		t.Errorf("expected master, got %s", snap.NodeRole)
	}
	if snap.TxsReceived != 100 {
		t.Errorf("expected 100 received, got %d", snap.TxsReceived)
	}
	if snap.TxsForwarded != 90 {
		t.Errorf("expected 90 forwarded, got %d", snap.TxsForwarded)
	}
	if snap.TxsCommitted != 80 {
		t.Errorf("expected 80 committed, got %d", snap.TxsCommitted)
	}
	if snap.LastBlock != 42 {
		t.Errorf("expected last block 42, got %d", snap.LastBlock)
	}
}

func TestPipelineStats_Reset(t *testing.T) {
	ps := &PipelineStats{}
	ps.IncrTxsReceived(999)
	ps.SetLastBlock(50)
	ps.Reset()

	snap := ps.Snapshot()
	if snap.TxsReceived != 0 {
		t.Errorf("expected 0 after reset, got %d", snap.TxsReceived)
	}
	if snap.LastBlock != 0 {
		t.Errorf("expected 0 after reset, got %d", snap.LastBlock)
	}
}

func TestPipelineStats_SnapshotJSON(t *testing.T) {
	ps := &PipelineStats{}
	ps.SetNodeRole("sub")
	ps.IncrTxsReceived(1)

	data, err := ps.SnapshotJSON()
	if err != nil {
		t.Fatalf("SnapshotJSON error: %v", err)
	}

	var parsed PipelineSnapshot
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if parsed.NodeRole != "sub" {
		t.Errorf("expected sub, got %s", parsed.NodeRole)
	}
}

func TestPipelineStats_ConcurrentAccess(t *testing.T) {
	ps := &PipelineStats{}
	var wg sync.WaitGroup
	n := 100

	// Spawn n goroutines incrementing, n goroutines reading
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ps.IncrTxsReceived(1)
			ps.IncrTxsCommitted(1)
			ps.SetLastBlock(int64(i))
		}()
		go func() {
			defer wg.Done()
			_ = ps.Snapshot()
		}()
	}
	wg.Wait()

	// All increments should be accounted for
	snap := ps.Snapshot()
	if snap.TxsReceived != int64(n) {
		t.Errorf("expected %d received, got %d", n, snap.TxsReceived)
	}
	if snap.TxsCommitted != int64(n) {
		t.Errorf("expected %d committed, got %d", n, snap.TxsCommitted)
	}
}
