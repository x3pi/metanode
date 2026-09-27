//go:build rollup_faults

package raftfeed

import (
	"os"

	"github.com/ethereum/go-ethereum/common"
)

// wrapBlockHash (fault-injection builds only, tag rollup_faults): while the file named by the environment
// variable RAFT_FAULT_BLOCKHASH_FILE exists this replica reports a wrong header hash to its peers and to itself,
// simulating a replica that executed a different state. Used by the live divergence test; never part of a release binary.
func wrapBlockHash(f BlockHashFunc) BlockHashFunc {
	if f == nil {
		return nil
	}
	return func(n uint64) (common.Hash, bool) {
		h, ok := f(n)
		if ok && faultActive() {
			h[0] ^= 0xff
		}
		return h, ok
	}
}

func faultActive() bool {
	p := os.Getenv("RAFT_FAULT_BLOCKHASH_FILE")
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
