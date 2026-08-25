package ceremony

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// SaveFoundingEntry writes a FoundingEntry to path. Contains no private key
// material — safe to publish/share with the ceremony coordinator.
func SaveFoundingEntry(path string, fe FoundingEntry) error {
	data, err := json.MarshalIndent(fe, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling founding_entry.json: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// SaveGenesisFile writes the EXACT canonical bytes (see CanonicalGenesisBytes)
// to path, so a later VerifyGenesisFile digest check over the raw file matches.
func SaveGenesisFile(path string, genesis *GenesisOutput) error {
	data, err := CanonicalGenesisBytes(genesis)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// SaveRootAnchorCommittee writes the aggregated committee — later used to seed
// ChainRegistry (see execution/pkg/blockchain/tx_processor/gateway_handler.go,
// Milestone A) instead of the ad-hoc test-only seeding used today.
func SaveRootAnchorCommittee(path string, committee *cross_chain.RootAnchorCommittee) error {
	data, err := json.MarshalIndent(committee, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling root_anchor_committee.json: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// SaveDigestFile writes the hex-encoded digest, newline-terminated, to path.
func SaveDigestFile(path string, digest [32]byte) error {
	return os.WriteFile(path, []byte("0x"+hex.EncodeToString(digest[:])+"\n"), 0o644)
}
