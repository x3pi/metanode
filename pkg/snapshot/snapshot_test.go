package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupAndRestore(t *testing.T) {
	// Create temp directories
	sourceDir := t.TempDir()
	destDir := t.TempDir()
	restoreDir := t.TempDir()

	// Create test files in source directory
	subDir := filepath.Join(sourceDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	testFiles := map[string]string{
		"file1.txt":         "hello world",
		"file2.txt":         "another file",
		"subdir/nested.txt": "nested content",
	}
	for name, content := range testFiles {
		path := filepath.Join(sourceDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file %s: %v", name, err)
		}
	}

	// Test Backup
	fileName := "test_backup.tar.gz"
	if err := Backup(sourceDir, destDir, fileName); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Verify archive was created
	archivePath := filepath.Join(destDir, fileName)
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("archive file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("archive file is empty")
	}

	// Test Restore
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify restored files
	for name, expectedContent := range testFiles {
		path := filepath.Join(restoreDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("failed to read restored file %s: %v", name, err)
			continue
		}
		if string(data) != expectedContent {
			t.Errorf("file %s: got %q, want %q", name, string(data), expectedContent)
		}
	}
}

func TestBackup_NonExistentSource(t *testing.T) {
	destDir := t.TempDir()
	err := Backup("/nonexistent/source", destDir, "test.tar.gz")
	if err == nil {
		t.Error("expected error for non-existent source directory")
	}
}

func TestBackup_CreatesDestDir(t *testing.T) {
	sourceDir := t.TempDir()
	// Write a test file
	if err := os.WriteFile(filepath.Join(sourceDir, "test.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "new_subdir")
	if err := Backup(sourceDir, destDir, "test.tar.gz"); err != nil {
		t.Fatalf("Backup should create dest dir: %v", err)
	}

	// Verify archive exists in newly created dir
	if _, err := os.Stat(filepath.Join(destDir, "test.tar.gz")); err != nil {
		t.Errorf("archive not found in created dir: %v", err)
	}
}

func TestRestore_InvalidFile(t *testing.T) {
	destDir := t.TempDir()
	err := Restore("/nonexistent/file.tar.gz", destDir)
	if err == nil {
		t.Error("expected error for non-existent source file")
	}
}

func TestRestore_InvalidGzip(t *testing.T) {
	// Create a file that's not a valid gzip
	tmpFile := filepath.Join(t.TempDir(), "invalid.tar.gz")
	if err := os.WriteFile(tmpFile, []byte("not gzip data"), 0644); err != nil {
		t.Fatalf("failed to create invalid file: %v", err)
	}

	destDir := t.TempDir()
	err := Restore(tmpFile, destDir)
	if err == nil {
		t.Error("expected error for invalid gzip file")
	}
}

func TestBackupRestore_EmptyDir(t *testing.T) {
	sourceDir := t.TempDir()
	destDir := t.TempDir()
	restoreDir := t.TempDir()

	// Backup empty directory
	if err := Backup(sourceDir, destDir, "empty.tar.gz"); err != nil {
		t.Fatalf("Backup of empty dir failed: %v", err)
	}

	// Restore
	archivePath := filepath.Join(destDir, "empty.tar.gz")
	if err := Restore(archivePath, restoreDir); err != nil {
		t.Fatalf("Restore of empty archive failed: %v", err)
	}
}
