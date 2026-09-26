//go:build !c0spike

package failpoint

// Hit is a no-op in production builds. Compiles away to zero overhead.
func Hit(name string) {}

// SetMarkerDir is a no-op in production builds.
func SetMarkerDir(dir string) {}

// RegisterHook is a no-op in production builds.
func RegisterHook(fn func(name string)) {}

// SetRunContext is a no-op in production builds.
func SetRunContext(id string, num uint64) {}

// GetLastMarkerError is a no-op in production builds.
func GetLastMarkerError() error { return nil }
