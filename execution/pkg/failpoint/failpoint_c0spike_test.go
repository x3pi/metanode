//go:build c0spike

package failpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailpointC0Spike(t *testing.T) {
	tmp := t.TempDir()
	SetMarkerDir(tmp)
	SetRunContext("spike-run-1", 100)

	var hitName string
	RegisterHook(func(name string) {
		hitName = name
	})

	Hit("test-spike-point")

	if hitName != "test-spike-point" {
		t.Fatalf("expected hook to receive 'test-spike-point', got %q", hitName)
	}

	markerFile := filepath.Join(tmp, "failpoint_test-spike-point.marker")
	data, err := os.ReadFile(markerFile)
	if err != nil {
		t.Fatalf("expected marker file to exist, got err=%v", err)
	}

	content := string(data)
	if !strings.Contains(content, "failpoint: test-spike-point") {
		t.Fatalf("expected marker content to contain failpoint name, got:\n%s", content)
	}
	if !strings.Contains(content, "run_id: spike-run-1") {
		t.Fatalf("expected marker content to contain run_id, got:\n%s", content)
	}
	if !strings.Contains(content, "block_num: 100") {
		t.Fatalf("expected marker content to contain block_num, got:\n%s", content)
	}

	if err := GetLastMarkerError(); err != nil {
		t.Fatalf("expected nil lastMarkerErr on successful write, got %v", err)
	}

	// Verify error tracking on invalid directory
	SetMarkerDir(filepath.Join(tmp, "non_existent_subdir", "deep_dir"))
	Hit("failing-point")
	if err := GetLastMarkerError(); err == nil {
		t.Fatalf("expected non-nil lastMarkerErr when writing marker to invalid directory")
	}
}
