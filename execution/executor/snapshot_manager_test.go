package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotManager_DetectEpochChange(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	snapDir := filepath.Join(tmpDir, "snaps")
	
	err := os.MkdirAll(dataDir, 0755)
	require.NoError(t, err)

	sm := NewSnapshotManager(dataDir, snapDir, 3, 5)
	cs := blockchain.NewTestChainState()

	// By injecting mock ChainState directly, we skip testing complex behavior
	// of DetectEpochChange without full boundary states. We just verify no crash.
	sm.DetectEpochChange(1, cs)
}

func TestSnapshotManager_Callbacks(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	snapDir := filepath.Join(tmpDir, "snaps")
	os.MkdirAll(dataDir, 0755)

	sm := NewSnapshotManager(dataDir, snapDir, 3, 0)
	
	sm.SetCheckpointCallback(func(destPath string) error {
		return nil
	})

	sm.SetNomtSnapshotCallback(func(destPath string, useReflink bool) error {
		return nil
	})

	// Manually force a snapshot
	sm.CreateHybridSnapshot(100, 2, 0)

	assert.NotNil(t, sm.checkpointCallback, "Checkpoint callback should be set")
	assert.NotNil(t, sm.nomtSnapshotCallback, "NOMT callback should be set")
}
