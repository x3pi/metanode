//go:build !rollup_faults

package raftfeed

// wrapBlockHash is the identity in release builds: no fault-injection code is compiled in (T-OFF-05).
func wrapBlockHash(f BlockHashFunc) BlockHashFunc { return f }
