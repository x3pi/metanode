package block

import "testing"

// TestNextExcessBlobGas locks in the EIP-4844 formula this chain relies on:
// excess = max(0, parentExcess + parentUsed - targetGas). Both the local
// block-build path and the peer-sync path call NextExcessBlobGas, so a
// regression here would silently desync the blob-gas market across nodes.
//
// targetGas here is 6 blobs/block (786432), not Cancun's usual 3: since this
// chain has no hardfork-activation mechanism, NextExcessBlobGas uses
// params.AllDevChainProtocolChanges, which activates Prague (target=6) from
// genesis — Prague takes precedence over Cancun (target=3) in go-ethereum's
// fork-priority resolution because it's the later fork. This also matches
// MAX_BLOBS_PER_BLOCK in pkg/common/constant.go, our own independent
// admission cap enforced elsewhere.
func TestNextExcessBlobGas(t *testing.T) {
	const targetGas = 6 * 131072 // Prague target used via AllDevChainProtocolChanges

	cases := []struct {
		name         string
		parentExcess uint64
		parentUsed   uint64
		want         uint64
	}{
		{"empty parent stays at zero", 0, 0, 0},
		{"below target decays to zero", 0, 131072, 0}, // 1 blob < target(6), excess stays 0
		{"exactly at target stays at zero", 0, targetGas, 0},
		{"above target by one blob's worth", 0, 7 * 131072, 131072},
		{"nonzero excess plus zero usage decays toward target", 1000000, 0, 1000000 - targetGas},
		{"excess below target never goes negative", 100, 0, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &BlockHeader{}
			h.SetExcessBlobGas(c.parentExcess)
			h.SetBlobGasUsed(c.parentUsed)
			got := NextExcessBlobGas(h, 1_700_000_000_000) // arbitrary ms timestamp
			if got != c.want {
				t.Fatalf("NextExcessBlobGas(excess=%d, used=%d) = %d, want %d", c.parentExcess, c.parentUsed, got, c.want)
			}
		})
	}
}
