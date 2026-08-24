package ceremony

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
)

// CanonicalGenesisBytes returns the exact bytes Assemble writes to genesis.json
// (2-space indented JSON with Go's fixed struct field order and pre-sorted
// arrays). Digest() is computed over these bytes. Kept as an exported,
// side-effect-free function so D3 (verify) can recompute the same digest from
// bytes already on disk without going through Assemble again.
func CanonicalGenesisBytes(genesis *GenesisOutput) ([]byte, error) {
	data, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling genesis.json: %w", err)
	}
	return data, nil
}

// Digest hashes canonical genesis.json bytes with keccak256 — the same hash
// primitive already used throughout the codebase (e.g. address derivation in
// execution/pkg/bls/bls.go), for consistency rather than any new dependency.
func Digest(genesisBytes []byte) [32]byte {
	return crypto.Keccak256Hash(genesisBytes)
}

// VerifyGenesisFile recomputes the digest of an on-disk genesis.json (raw
// bytes, NOT re-parsed/re-marshaled — this checks the file as-is, since a
// round-trip through Go structs could silently normalize away a real
// discrepancy) and compares it against an expected digest. This is the ONLY
// bootstrap-time defense against divergent genesis files across operators:
// the consensus layer itself performs no such check at epoch 0 (see Milestone
// D plan, gap #5).
func VerifyGenesisFile(path string, expectedDigest [32]byte) (actual [32]byte, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return actual, false, err
	}
	actual = Digest(raw)
	return actual, actual == expectedDigest, nil
}
