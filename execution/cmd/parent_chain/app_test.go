package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitteeFingerprint_Deterministic(t *testing.T) {
	v1 := parentchain.GenesisValidator{
		Name:         "val0",
		ProtocolKey:  "0x1111",
		NetworkKey:   "0x2222",
		AuthorityKey: "0x3333",
		Address:      "0xaaaa",
		Stake:        "1000",
	}
	v2 := parentchain.GenesisValidator{
		ProtocolKey:  "0x4444",
		NetworkKey:   "0x5555",
		AuthorityKey: "0x6666",
		Address:      "0xbbbb",
		Stake:        "2000",
	}

	g1 := parentchain.Genesis{
		ChainID:    991,
		Validators: []parentchain.GenesisValidator{v1, v2},
	}
	g2 := parentchain.Genesis{
		ChainID:    991,
		Validators: []parentchain.GenesisValidator{v2, v1}, // reversed order
	}

	fp1 := g1.CommitteeFingerprint()
	fp2 := g2.CommitteeFingerprint()

	assert.NotEmpty(t, fp1)
	assert.Equal(t, fp1, fp2, "Fingerprint must be deterministic regardless of validator ordering")

	// Changing a key must produce a different fingerprint
	g3 := parentchain.Genesis{
		ChainID:    991,
		Validators: []parentchain.GenesisValidator{v1, {ProtocolKey: "0x9999", NetworkKey: "0x5555", AuthorityKey: "0x6666", Address: "0xbbbb", Stake: "2000"}},
	}
	assert.NotEqual(t, fp1, g3.CommitteeFingerprint())
}

func TestNewApp_CommitteeFingerprintMismatch_Fails(t *testing.T) {
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "parentchain_db")
	require.NoError(t, os.MkdirAll(dbDir, 0755))

	v1 := parentchain.GenesisValidator{
		Name:         "val0",
		ProtocolKey:  "0x0101010101010101010101010101010101010101010101010101010101010101",
		NetworkKey:   "0x0202020202020202020202020202020202020202020202020202020202020202",
		AuthorityKey: "0x030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303030303",
		Address:      "0x0000000000000000000000000000000000000001",
		P2PAddress:   "/ip4/127.0.0.1/tcp/19001",
		Stake:        "1000000000000000000",
	}
	gen := parentchain.Genesis{
		ChainID:    991,
		Validators: []parentchain.GenesisValidator{v1},
	}
	genData, err := json.Marshal(gen)
	require.NoError(t, err)

	genPath := filepath.Join(tmpDir, "parent_genesis.json")
	require.NoError(t, os.WriteFile(genPath, genData, 0644))

	// Write conflicting fingerprint to simulate existing DAG with different keys
	fpPath := filepath.Join(tmpDir, "genesis_committee_fingerprint")
	require.NoError(t, os.WriteFile(fpPath, []byte("different_fingerprint_hash\n"), 0644))

	_, err = NewApp("config.json", "node_parent.toml", tmpDir, ":8545", genPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FATAL: committee in")
	assert.Contains(t, err.Error(), "does not match existing consensus data")
	assert.Contains(t, err.Error(), "Invalid signature")
}
