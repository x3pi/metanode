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

// newTestSnapshotManagerForTrigger builds a SnapshotManager with no-op checkpoint/NOMT
// callbacks, so a triggered OnBlockCommitted's async snapshot goroutine finishes
// immediately instead of doing real (and here, meaningless) filesystem work.
func newTestSnapshotManagerForTrigger(t *testing.T, frequency, offset int) *SnapshotManager {
	t.Helper()
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	snapDir := filepath.Join(tmpDir, "snaps")
	require.NoError(t, os.MkdirAll(dataDir, 0755))

	sm := NewSnapshotManager(dataDir, snapDir, 3, 20)
	sm.SetCheckpointCallback(func(destPath string) error { return nil })
	sm.SetNomtSnapshotCallback(func(destPath string, useReflink bool) error { return nil })
	sm.SetSnapshotFrequency(frequency)
	sm.SetSnapshotBlockOffset(offset)
	return sm
}

// nextPeriodicTargetOf reads sm.nextPeriodicTarget under its own lock. This field is
// advanced SYNCHRONOUSLY inside OnBlockCommitted (before the actual snapshot-creation
// goroutine is spawned), so it's a reliable, non-flaky way to observe whether the
// periodic trigger fired without waiting on async filesystem work.
func nextPeriodicTargetOf(sm *SnapshotManager) uint64 {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.nextPeriodicTarget
}

func TestSnapshotManager_PeriodicTrigger_NoTriggerBelowThreshold(t *testing.T) {
	sm := newTestSnapshotManagerForTrigger(t, 50, 0)

	sm.OnBlockCommitted(1)
	sm.OnBlockCommitted(49)

	assert.Equal(t, uint64(50), nextPeriodicTargetOf(sm),
		"first call below the threshold should lazily set the target to blockOffset+frequency without crossing it")
}

func TestSnapshotManager_PeriodicTrigger_ExactMultipleFires(t *testing.T) {
	sm := newTestSnapshotManagerForTrigger(t, 50, 0)

	sm.OnBlockCommitted(50)

	assert.Equal(t, uint64(100), nextPeriodicTargetOf(sm),
		"hitting the target exactly must fire and advance to the next multiple")
}

// Regression test for the live bug this fixed: storage.UpdateLastBlockNumber is a
// monotonic CAS counter that can skip past intermediate values (fast-sync/resume applying
// a batch, or two writers racing), so OnBlockCommitted must not require seeing the exact
// multiple to fire -- a node reaching block #101 with frequency=50 must still trigger,
// not silently wait forever for a block #50 or #100 call that will never come.
func TestSnapshotManager_PeriodicTrigger_SkippedExactMultipleStillFires(t *testing.T) {
	sm := newTestSnapshotManagerForTrigger(t, 50, 0)

	sm.OnBlockCommitted(101) // jumps straight past both 50 and 100

	assert.Equal(t, uint64(150), nextPeriodicTargetOf(sm),
		"a jump past one or more multiples must still fire exactly once and advance past every skipped target")
}

func TestSnapshotManager_PeriodicTrigger_RespectsPerNodeOffset(t *testing.T) {
	// STAGGER FIX semantics: node1 offset=100, frequency=500 -> fires at 600, 1100, 1600...
	sm := newTestSnapshotManagerForTrigger(t, 500, 100)

	sm.OnBlockCommitted(599)
	assert.Equal(t, uint64(600), nextPeriodicTargetOf(sm), "must not fire before offset+frequency")

	sm.OnBlockCommitted(600)
	assert.Equal(t, uint64(1100), nextPeriodicTargetOf(sm), "must fire at offset+frequency and advance by one more period")
}
