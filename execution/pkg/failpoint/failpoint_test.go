//go:build !c0spike

package failpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailpoint(t *testing.T) {
	tmp := t.TempDir()
	SetMarkerDir(tmp)

	var hitName string
	RegisterHook(func(name string) {
		hitName = name
	})

	Hit("test-point")

	// In default build (!c0spike), Hit is a no-op: hook is not called and marker file is not created
	markerFile := filepath.Join(tmp, "failpoint_test-point.marker")
	_, err := os.Stat(markerFile)
	if !os.IsNotExist(err) {
		t.Fatalf("expected marker file not to exist in default build, got err=%v", err)
	}
	if hitName != "" {
		t.Fatalf("expected hook not to be called in default build, got hitName=%q", hitName)
	}
	if err := GetLastMarkerError(); err != nil {
		t.Fatalf("expected nil marker error in default build, got %v", err)
	}
}
