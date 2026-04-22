package loggerfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewLogCleaner(t *testing.T) {
	lc := NewLogCleaner("/tmp/test-logs")
	if lc == nil {
		t.Fatal("NewLogCleaner returned nil")
	}
	if lc.logDir != "/tmp/test-logs" {
		t.Errorf("logDir = %q, want /tmp/test-logs", lc.logDir)
	}
	if lc.maxEpochsToKeep != 3 {
		t.Errorf("maxEpochsToKeep = %d, want 3", lc.maxEpochsToKeep)
	}
	if lc.disableCleanup {
		t.Error("disableCleanup should default to false")
	}
}

func TestSetMaxEpochsToKeep(t *testing.T) {
	lc := NewLogCleaner("/tmp/test")

	// Archive mode
	lc.SetMaxEpochsToKeep(0)
	if !lc.disableCleanup {
		t.Error("SetMaxEpochsToKeep(0) should enable archive mode")
	}

	// Reset
	lc.disableCleanup = false

	// Normal
	lc.SetMaxEpochsToKeep(5)
	if lc.maxEpochsToKeep != 5 {
		t.Errorf("maxEpochsToKeep = %d, want 5", lc.maxEpochsToKeep)
	}

	// Negative → default to 3
	lc.SetMaxEpochsToKeep(-1)
	if lc.maxEpochsToKeep != 3 {
		t.Errorf("maxEpochsToKeep = %d, want 3 (default for negative)", lc.maxEpochsToKeep)
	}
}

func TestSetCleanInterval(t *testing.T) {
	lc := NewLogCleaner("/tmp/test")

	// Minimum 10 minutes
	lc.SetCleanInterval(1 * time.Minute)
	if lc.cleanInterval != 10*time.Minute {
		t.Errorf("cleanInterval = %v, want 10m (minimum)", lc.cleanInterval)
	}

	// Above minimum
	lc.SetCleanInterval(30 * time.Minute)
	if lc.cleanInterval != 30*time.Minute {
		t.Errorf("cleanInterval = %v, want 30m", lc.cleanInterval)
	}
}

func TestSetDisableCleanup(t *testing.T) {
	lc := NewLogCleaner("/tmp/test")

	lc.SetDisableCleanup(true)
	if !lc.IsCleanupDisabled() {
		t.Error("cleanup should be disabled")
	}

	lc.SetDisableCleanup(false)
	if lc.IsCleanupDisabled() {
		t.Error("cleanup should be enabled")
	}
}

func TestCleanOldEpochLogs_DisabledCleanup(t *testing.T) {
	lc := NewLogCleaner("/tmp/test-nonexistent")
	lc.SetDisableCleanup(true)

	err := lc.CleanOldEpochLogs()
	if err != nil {
		t.Fatalf("CleanOldEpochLogs with disabled cleanup should return nil, got: %v", err)
	}
}

func TestCleanOldEpochLogs_NonexistentDir(t *testing.T) {
	lc := NewLogCleaner("/tmp/test-nonexistent-dir-abc123")
	err := lc.CleanOldEpochLogs()
	if err != nil {
		t.Fatalf("CleanOldEpochLogs for nonexistent dir should not error, got: %v", err)
	}
}

func TestCleanOldEpochLogs_CleanupWorks(t *testing.T) {
	// Create temp dir with epoch subdirectories
	tmpDir := t.TempDir()

	// Create epoch_0 through epoch_4
	for i := 0; i <= 4; i++ {
		epochDir := filepath.Join(tmpDir, "epoch_"+string(rune('0'+i)))
		os.MkdirAll(epochDir, 0755)
		// Write a dummy file in each epoch dir
		os.WriteFile(filepath.Join(epochDir, "test.log"), []byte("log data"), 0644)
	}

	lc := NewLogCleaner(tmpDir)
	lc.SetMaxEpochsToKeep(2)

	err := lc.CleanOldEpochLogs()
	if err != nil {
		t.Fatalf("CleanOldEpochLogs error: %v", err)
	}

	// Check that only the 2 newest epochs remain
	entries, _ := os.ReadDir(tmpDir)
	epochCount := 0
	for _, e := range entries {
		if e.IsDir() {
			epochCount++
		}
	}
	if epochCount != 2 {
		t.Errorf("expected 2 epoch dirs remaining, got %d", epochCount)
	}
}

func TestCleanOldEpochLogs_FewerThanMax(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "epoch_0"), 0755)

	lc := NewLogCleaner(tmpDir)
	lc.SetMaxEpochsToKeep(3)

	err := lc.CleanOldEpochLogs()
	if err != nil {
		t.Fatalf("CleanOldEpochLogs error: %v", err)
	}

	// epoch_0 should still exist
	if _, err := os.Stat(filepath.Join(tmpDir, "epoch_0")); os.IsNotExist(err) {
		t.Error("epoch_0 should not have been deleted (fewer than max)")
	}
}

func TestDirSize(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty dir
	size := dirSize(tmpDir)
	if size != 0 {
		t.Errorf("empty dir size = %d, want 0", size)
	}

	// Write files
	content := make([]byte, 1024) // 1KB
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), content, 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), content, 0644)

	size = dirSize(tmpDir)
	if size != 2048 {
		t.Errorf("dir size = %d, want 2048", size)
	}
}

func TestGlobalLogCleaner(t *testing.T) {
	// Default should be nil
	lc := GetGlobalLogCleaner()
	// it may be nil or set from previous tests, just verify set/get works
	newLC := NewLogCleaner("/tmp/test-global")
	SetGlobalLogCleaner(newLC)

	got := GetGlobalLogCleaner()
	if got != newLC {
		t.Error("GetGlobalLogCleaner should return what was set")
	}

	// Clear
	SetGlobalLogCleaner(lc)
}
