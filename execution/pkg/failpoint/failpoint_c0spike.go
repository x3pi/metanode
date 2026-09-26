//go:build c0spike

package failpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	mu            sync.RWMutex
	markerDir     string
	hookFn        func(name string)
	runID         string
	blockNum      uint64
	lastMarkerErr error
)

// SetMarkerDir sets the directory where failpoint markers are created.
func SetMarkerDir(dir string) {
	mu.Lock()
	defer mu.Unlock()
	markerDir = dir
}

// RegisterHook sets a programmatic callback when a failpoint is hit.
func RegisterHook(fn func(name string)) {
	mu.Lock()
	defer mu.Unlock()
	hookFn = fn
}

// SetRunContext sets the run ID and current block number for marker files.
func SetRunContext(id string, num uint64) {
	mu.Lock()
	defer mu.Unlock()
	runID = id
	blockNum = num
}

// GetLastMarkerError returns any error from writing the last marker file.
func GetLastMarkerError() error {
	mu.RLock()
	defer mu.RUnlock()
	return lastMarkerErr
}

// Hit signals a named failpoint event.
func Hit(name string) {
	mu.RLock()
	dir := markerDir
	fn := hookFn
	rID := runID
	bNum := blockNum
	mu.RUnlock()

	if fn != nil {
		fn(name)
	}

	if dir != "" {
		markerFile := filepath.Join(dir, fmt.Sprintf("failpoint_%s.marker", name))
		content := fmt.Sprintf("failpoint: %s\nrun_id: %s\nblock_num: %d\ntimestamp_ns: %d\n", name, rID, bNum, time.Now().UnixNano())
		if err := os.WriteFile(markerFile, []byte(content), 0644); err != nil {
			mu.Lock()
			lastMarkerErr = err
			mu.Unlock()
			fmt.Fprintf(os.Stderr, "❌ [FAILPOINT] Failed to write marker %s: %v\n", markerFile, err)
		} else {
			mu.Lock()
			lastMarkerErr = nil
			mu.Unlock()
		}
	}
}
