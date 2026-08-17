package loggerfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewFileLogger_DailyNaming(t *testing.T) {
	tmpDir := t.TempDir()
	SetGlobalLogDir(tmpDir)

	fl, err := NewFileLogger("execution.log")
	if err != nil {
		t.Fatalf("Failed to create FileLogger: %v", err)
	}
	defer fl.Close()

	today := time.Now().Format("2006-01-02")
	expectedFullPath := filepath.Join(tmpDir, today, "execution.log")

	if fl.CurrentDate() != today {
		t.Errorf("CurrentDate() = %q, want %q", fl.CurrentDate(), today)
	}

	if fl.FilePath() != expectedFullPath {
		t.Errorf("FilePath() = %q, want %q", fl.FilePath(), expectedFullPath)
	}

	// Verify file was actually created on disk
	if _, err := os.Stat(expectedFullPath); os.IsNotExist(err) {
		t.Errorf("Expected log file does not exist on disk: %s", expectedFullPath)
	}

	// Write a log entry
	fl.Log("Test log entry message")
	content, err := os.ReadFile(expectedFullPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if !strings.Contains(string(content), "Test log entry message") {
		t.Errorf("Log file content does not contain expected message: %s", string(content))
	}
}
