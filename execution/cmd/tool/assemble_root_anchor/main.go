// assemble_root_anchor is the coordinator-side tool for the Root Anchor
// genesis ceremony (Milestone D of note/cross_chain_root_anchor_architecture.md,
// section 1.3 #5 / 5.2.1 / 14 P1.1-P1.2).
//
// Subcommands:
//
//	assemble --entries <dir> --chain-id <new root anchor chain id> [flags] --out-dir <dir>
//	    Validates >= 4 founding_entry.json submissions (schema, PoP, stake cap,
//	    duplicate/collision checks — see execution/pkg/cross_chain/ceremony) and
//	    deterministically writes genesis.json, root_anchor_committee.json and
//	    genesis_digest.txt.
//
//	verify --genesis <file> --expect-digest <hex>
//	    Recomputes the digest of an on-disk genesis.json and compares it against
//	    an expected value. Every founding-chain operator MUST run this before
//	    starting their node — this is the only defense against a divergent
//	    genesis.json, since the consensus layer performs no such check at
//	    epoch 0 (see the Milestone D plan, gap #5).
//
// See note/runbook_root_anchor_genesis_ceremony.md for the full ceremony.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/ceremony"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "assemble":
		err = runAssemble(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "assemble_root_anchor:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: assemble_root_anchor <assemble|verify> [flags]")
}

func runAssemble(args []string) error {
	fs := flag.NewFlagSet("assemble", flag.ExitOnError)
	entriesDir := fs.String("entries", "", "directory containing founding_entry.json files (non-recursive glob *.json) (required)")
	chainID := fs.Uint64("chain-id", 0, "the NEW Root Anchor network's own chain ID (required, must not collide with any founding chain ID)")
	epochTimestampMs := fs.Uint64("epoch-timestamp-ms", 0, "genesis epoch start, unix ms (required)")
	epochDurationSeconds := fs.Uint64("epoch-duration-seconds", 600, "epoch duration in seconds")
	attestationInterval := fs.Uint64("attestation-interval", 10, "blocks between state attestations")
	maxStakeCapPercent := fs.Uint64("max-stake-cap-percent", uint64(0), "max stake share per founding chain, percent (0 => package default, 33)")
	outDir := fs.String("out-dir", ".", "output directory for genesis.json / root_anchor_committee.json / genesis_digest.txt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *entriesDir == "" {
		return fmt.Errorf("--entries is required")
	}
	if *chainID == 0 {
		return fmt.Errorf("--chain-id is required and must be non-zero")
	}
	if *epochTimestampMs == 0 {
		return fmt.Errorf("--epoch-timestamp-ms is required (unix ms genesis epoch start)")
	}
	if *maxStakeCapPercent > 100 {
		return fmt.Errorf("--max-stake-cap-percent must be <= 100")
	}

	entries, err := ceremony.LoadFoundingEntriesFromDir(*entriesDir)
	if err != nil {
		return fmt.Errorf("loading founding_entry.json files from %s: %w", *entriesDir, err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no *.json files found in %s", *entriesDir)
	}

	result, err := ceremony.Assemble(entries, ceremony.AssembleOptions{
		ChainID:              *chainID,
		EpochTimestampMs:     *epochTimestampMs,
		EpochDurationSeconds: *epochDurationSeconds,
		AttestationInterval:  *attestationInterval,
		MaxStakeCapPercent:   uint8(*maxStakeCapPercent),
	})
	if err != nil {
		return fmt.Errorf("assembly failed: %w", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	genesisPath := filepath.Join(*outDir, "genesis.json")
	committeePath := filepath.Join(*outDir, "root_anchor_committee.json")
	digestPath := filepath.Join(*outDir, "genesis_digest.txt")

	if err := ceremony.SaveGenesisFile(genesisPath, result.Genesis); err != nil {
		return fmt.Errorf("writing %s: %w", genesisPath, err)
	}
	if err := ceremony.SaveRootAnchorCommittee(committeePath, result.Committee); err != nil {
		return fmt.Errorf("writing %s: %w", committeePath, err)
	}
	if err := ceremony.SaveDigestFile(digestPath, result.Digest); err != nil {
		return fmt.Errorf("writing %s: %w", digestPath, err)
	}

	fmt.Printf("assembled %d founding chains, %d validators, chain_id=%d\n",
		len(result.Committee.FoundingChains), len(result.Genesis.Validators), *chainID)
	fmt.Printf("  wrote %s\n", genesisPath)
	fmt.Printf("  wrote %s\n", committeePath)
	fmt.Printf("  wrote %s\n", digestPath)
	fmt.Printf("genesis digest: 0x%x\n", result.Digest)
	fmt.Println()
	fmt.Println("Publish genesis_digest.txt to every founding-chain operator NOW, out of band from")
	fmt.Println("the genesis.json file itself. Every operator MUST run `assemble_root_anchor verify`")
	fmt.Println("against this digest before starting their node.")
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	genesisPath := fs.String("genesis", "", "path to genesis.json to verify (required)")
	expectDigest := fs.String("expect-digest", "", "expected digest, 0x-prefixed hex (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *genesisPath == "" {
		return fmt.Errorf("--genesis is required")
	}
	if *expectDigest == "" {
		return fmt.Errorf("--expect-digest is required")
	}

	var expected [32]byte
	raw := strings.TrimPrefix(*expectDigest, "0x")
	if len(raw) != 64 {
		return fmt.Errorf("--expect-digest must be a 32-byte hex value (64 hex chars, optionally 0x-prefixed), got %d chars", len(raw))
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("--expect-digest: invalid hex: %w", err)
	}
	copy(expected[:], decoded)

	actual, ok, err := ceremony.VerifyGenesisFile(*genesisPath, expected)
	if err != nil {
		return fmt.Errorf("reading %s: %w", *genesisPath, err)
	}
	if !ok {
		fmt.Printf("MISMATCH: %s does not match the expected digest.\n", *genesisPath)
		fmt.Printf("  expected: 0x%x\n", expected)
		fmt.Printf("  actual:   0x%x\n", actual)
		fmt.Println("DO NOT start a node from this file. Re-fetch genesis.json from the coordinator.")
		os.Exit(1)
	}
	fmt.Printf("OK: %s matches digest 0x%x\n", *genesisPath, actual)
	return nil
}
