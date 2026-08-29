package ceremony

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// buildTestEntry produces one real, self-consistent FoundingEntry (real BLS
// key derivation + real PoP signature) for the given chain/hostname, with
// a distinct IP so no two test entries ever collide unless the test wants
// them to.
func buildTestEntry(t *testing.T, chainID uint64, chainName, hostname string, port int, stakeWei string) FoundingEntry {
	t.Helper()
	dir := writeFixtureKeysDir(t, realBLSPrivHex(t), fixtureEthAddress(port))
	fe, err := BuildFoundingEntry(BuildFoundingEntryParams{
		KeysDir:     dir,
		ChainID:     chainID,
		ChainName:   chainName,
		Hostname:    hostname,
		IP:          fmt.Sprintf("203.0.113.%d", port%250+1),
		P2PPort:     9100 + port,
		PrimaryPort: 6200 + port,
		WorkerPort:  4012 + port,
		StakeWei:    stakeWei,
		Commission:  5,
	})
	if err != nil {
		t.Fatalf("buildTestEntry(%d,%s): %v", chainID, hostname, err)
	}
	return fe
}

// fourEqualFoundingEntries builds a standard, valid 4-founding-chain input set
// (one validator per chain, equal stake => well under any reasonable cap).
func fourEqualFoundingEntries(t *testing.T) []FoundingEntry {
	t.Helper()
	return []FoundingEntry{
		buildTestEntry(t, 101, "Alpha Chain", "alpha-node-0", 1, "1000000000000000000000"),
		buildTestEntry(t, 102, "Beta Chain", "beta-node-0", 2, "1000000000000000000000"),
		buildTestEntry(t, 103, "Gamma Chain", "gamma-node-0", 3, "1000000000000000000000"),
		buildTestEntry(t, 104, "Delta Chain", "delta-node-0", 4, "1000000000000000000000"),
	}
}

func stdOptions() AssembleOptions {
	return AssembleOptions{
		ChainID:              9001,
		EpochTimestampMs:     1772784000000,
		EpochDurationSeconds: 300,
		AttestationInterval:  10,
	}
}

func TestAssemble_HappyPath(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	result, err := Assemble(entries, stdOptions())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(result.Genesis.Validators) != 4 {
		t.Errorf("got %d validators, want 4", len(result.Genesis.Validators))
	}
	if len(result.Genesis.Alloc) != 4 {
		t.Errorf("got %d alloc entries, want 4", len(result.Genesis.Alloc))
	}
	if result.Genesis.Config.ChainId.Uint64() != 9001 {
		t.Errorf("chainId = %s, want 9001", result.Genesis.Config.ChainId)
	}
	if len(result.Committee.FoundingChains) != 4 {
		t.Errorf("got %d founding chains, want 4", len(result.Committee.FoundingChains))
	}
}

func TestAssemble_InsufficientFoundingChains(t *testing.T) {
	entries := []FoundingEntry{
		buildTestEntry(t, 101, "Alpha Chain", "alpha-node-0", 1, "1000000000000000000000"),
		buildTestEntry(t, 102, "Beta Chain", "beta-node-0", 2, "1000000000000000000000"),
		buildTestEntry(t, 103, "Gamma Chain", "gamma-node-0", 3, "1000000000000000000000"),
	}
	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for < 4 founding chains")
	}
	if !errors.Is(err, cross_chain.ErrInsufficientFoundingChains) {
		t.Errorf("got %v, want wrapping ErrInsufficientFoundingChains", err)
	}
}

func TestAssemble_StakeCapExceeded(t *testing.T) {
	// One chain contributes the vast majority of stake -> must exceed the
	// default 33% cap enforced by cross_chain.NewRootAnchorCommittee.
	entries := []FoundingEntry{
		buildTestEntry(t, 101, "Whale Chain", "whale-node-0", 1, "100000000000000000000000"), // 100000
		buildTestEntry(t, 102, "Beta Chain", "beta-node-0", 2, "1000000000000000000000"),     // 1000
		buildTestEntry(t, 103, "Gamma Chain", "gamma-node-0", 3, "1000000000000000000000"),
		buildTestEntry(t, 104, "Delta Chain", "delta-node-0", 4, "1000000000000000000000"),
	}
	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for stake cap exceeded")
	}
	if !errors.Is(err, cross_chain.ErrStakeCapExceeded) {
		t.Errorf("got %v, want wrapping ErrStakeCapExceeded", err)
	}
}

func TestAssemble_TamperedPopSignatureRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	// Tamper the PoP signature of the first entry -> ValidateCommitteeEntry
	// inside cross_chain.NewRootAnchorCommittee must reject it.
	sig, err := hex.DecodeString(entries[0].CrossChain.PopSignature[2:])
	if err != nil {
		t.Fatalf("decode pop sig: %v", err)
	}
	sig[0] ^= 0xFF
	entries[0].CrossChain.PopSignature = "0x" + hex.EncodeToString(sig)

	_, err = Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for tampered PoP signature")
	}
}

func TestAssemble_DuplicateEthAddressRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	entries[1].GenesisValidator.Address = entries[0].GenesisValidator.Address
	entries[1].GenesisAlloc.Address = entries[0].GenesisValidator.Address

	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for duplicate eth address across operators")
	}
}

func TestAssemble_DuplicateAuthorityKeyRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	entries[1].GenesisValidator.AuthorityKey = entries[0].GenesisValidator.AuthorityKey

	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for duplicate authority_key across operators")
	}
}

func TestAssemble_P2PAddressCollisionRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	entries[1].GenesisValidator.P2PAddress = entries[0].GenesisValidator.P2PAddress

	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for p2p_address collision across operators")
	}
}

func TestAssemble_DuplicateHostnameRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	entries[1].GenesisValidator.Hostname = entries[0].GenesisValidator.Hostname

	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for duplicate hostname across operators")
	}
}

func TestAssemble_RootAnchorChainIDCollidesWithFoundingChain(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	opts := stdOptions()
	opts.ChainID = 102 // same as Beta Chain's founding chain_id
	_, err := Assemble(entries, opts)
	if err == nil {
		t.Fatal("expected error when Root Anchor chain_id collides with a founding chain_id")
	}
}

func TestAssemble_MalformedKeyLengthRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	entries[0].CrossChain.PubkeyBLS = "0x1234" // way too short
	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for malformed pubkey_bls length")
	}
}

func TestAssemble_SchemaVersionMismatchRejected(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	entries[0].SchemaVersion = 999
	_, err := Assemble(entries, stdOptions())
	if err == nil {
		t.Fatal("expected error for unsupported schema_version")
	}
}

// TestAssemble_Deterministic verifies the assembled genesis is byte-identical
// (same digest) regardless of the order founding_entry.json files were
// gathered in — this is the whole point of D3's expected-digest verification
// working the same way for every operator.
func TestAssemble_Deterministic(t *testing.T) {
	base := fourEqualFoundingEntries(t)
	opts := stdOptions()

	first, err := Assemble(base, opts)
	if err != nil {
		t.Fatalf("Assemble (base order): %v", err)
	}

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5; i++ {
		shuffled := append([]FoundingEntry(nil), base...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })

		result, err := Assemble(shuffled, opts)
		if err != nil {
			t.Fatalf("Assemble (shuffle %d): %v", i, err)
		}
		if result.Digest != first.Digest {
			t.Errorf("shuffle %d: digest %x != base digest %x — assembly is not order-independent", i, result.Digest, first.Digest)
		}
	}
}

// TestAssemble_RoundTripsThroughGoLoader checks the assembled genesis.json is
// actually loadable by the real production loader
// (execution/pkg/config.LoadGenesisData) and carries the right validator/alloc
// counts and chain ID — not just structurally similar JSON.
func TestAssemble_RoundTripsThroughGoLoader(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	opts := stdOptions()
	result, err := Assemble(entries, opts)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	dir := t.TempDir()
	genesisPath := dir + "/genesis.json"
	if err := SaveGenesisFile(genesisPath, result.Genesis); err != nil {
		t.Fatalf("SaveGenesisFile: %v", err)
	}

	loaded, err := config.LoadGenesisData(genesisPath)
	if err != nil {
		t.Fatalf("config.LoadGenesisData: %v", err)
	}
	if loaded.Config.ChainId == nil || loaded.Config.ChainId.Uint64() != opts.ChainID {
		t.Errorf("loaded chainId = %v, want %d", loaded.Config.ChainId, opts.ChainID)
	}
	if len(loaded.Validators) != 4 {
		t.Errorf("loaded %d validators, want 4", len(loaded.Validators))
	}
	if len(loaded.Alloc) != 4 {
		t.Errorf("loaded %d alloc entries, want 4", len(loaded.Alloc))
	}
	for _, v := range loaded.Validators {
		if len(v.AuthorityKey) != 96 {
			t.Errorf("validator %s: authority_key decoded to %d bytes, want 96", v.Hostname, len(v.AuthorityKey))
		}
	}
}

// TestAssemble_VerifyGenesisFile_MatchAndMismatch exercises the D3 verify
// flow end to end: correct digest passes, a bit-flip in the on-disk file
// (simulating an operator receiving a tampered/divergent genesis.json) fails.
func TestAssemble_VerifyGenesisFile_MatchAndMismatch(t *testing.T) {
	entries := fourEqualFoundingEntries(t)
	result, err := Assemble(entries, stdOptions())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	dir := t.TempDir()
	genesisPath := dir + "/genesis.json"
	if err := SaveGenesisFile(genesisPath, result.Genesis); err != nil {
		t.Fatalf("SaveGenesisFile: %v", err)
	}

	_, ok, err := VerifyGenesisFile(genesisPath, result.Digest)
	if err != nil {
		t.Fatalf("VerifyGenesisFile: %v", err)
	}
	if !ok {
		t.Error("expected digest match on untouched file")
	}

	// Simulate a divergent genesis.json (e.g. a different operator's chainId typo).
	tampered := []byte(`{"config":{"chainId":9002}}`)
	tamperedPath := dir + "/genesis_tampered.json"
	if err := os.WriteFile(tamperedPath, tampered, 0o644); err != nil {
		t.Fatalf("write tampered file: %v", err)
	}
	_, ok, err = VerifyGenesisFile(tamperedPath, result.Digest)
	if err != nil {
		t.Fatalf("VerifyGenesisFile (tampered): %v", err)
	}
	if ok {
		t.Error("expected digest mismatch on tampered file")
	}
}
