// bls_pubkey derives the real pkg/bls (min-pk, 48-byte G1) public key from a given secret scalar
// hex string, and prints it as base64 to stdout.
//
// Exists to close a real genesis-generation bug found 2026-08-26 while live-testing the P4
// relayer automation: gen_single_chain.py generates one BLS12-381 secret scalar per validator
// via `metanode keytool generate validator` (Rust, bls12381::min_sig -- 96-byte G2 public keys,
// used for real consensus authority identity) and reused that SAME secret for TWO purposes:
// consensus authority_key (correctly min_sig/G2) AND the genesis account's publicKeyBls field
// (which AccountState.SetPublicKeyBls's own validation proves must be exactly 48 bytes -- the
// pkg/bls min-pk/G1 convention every cross-chain BLS call in this Go codebase uses, e.g.
// CommitteeAttestationWorker/CommitAttestationWorker reading Databases.BLSPrivateKey). Writing
// the min_sig pubkey into that field made a validator's own on-chain identity never match the
// min-pk pubkey it (and register_chains) actually sign with -- committeeContains() never found
// a match, so validators silently never submitted a single real commit/committee attestation
// share. Same secret scalar, but min-pk and min-sig derive genuinely different, incompatible
// public key encodings from it; there is no way to convert one to the other after the fact, only
// to derive BOTH from the secret separately (this tool does the min-pk half; keytool already did
// the min_sig half).
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	secretHex := flag.String("secret", "", "BLS secret scalar hex (with or without 0x prefix)")
	fromStdin := flag.Bool("stdin", false, "read BLS secret scalar hex from stdin (avoids exposing secret in process argv/ps)")
	// Opt-in, defaults false: keeps stdout single-line (just the base64 pubkey) for existing
	// callers (e.g. gen_root_anchor_chain.py's own copy of derive_min_pk_pubkey) that parse
	// exactly one line and would otherwise silently break on this tool's stdout format changing
	// out from under them.
	withAddress := flag.Bool("with-address", false, "also print this secret's bls.KeyPair.Address() as a second line")
	flag.Parse()

	var rawSecret string
	if *fromStdin || *secretHex == "-" {
		stdinBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read secret from stdin: %v\n", err)
			os.Exit(1)
		}
		rawSecret = strings.TrimSpace(string(stdinBytes))
	} else if *secretHex != "" {
		rawSecret = strings.TrimSpace(*secretHex)
	} else if envSecret := os.Getenv("BLS_SECRET_HEX"); envSecret != "" {
		rawSecret = strings.TrimSpace(envSecret)
	}

	if rawSecret == "" {
		fmt.Fprintln(os.Stderr, "Error: BLS secret scalar is required (via -stdin, BLS_SECRET_HEX environment variable, or -secret flag)")
		os.Exit(1)
	}
	_, pub, addr := bls.GenerateKeyPairFromSecretKey(strings.TrimPrefix(rawSecret, "0x"))
	pubBytes := pub.Bytes()
	if len(pubBytes) != 48 {
		fmt.Fprintf(os.Stderr, "Error: derived public key is %d bytes, expected 48 (invalid secret?)\n", len(pubBytes))
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pubBytes))
	if *withAddress {
		// This secret's own bls.KeyPair.Address() (keccak256(compressed pubkey)[12:]), i.e. what
		// cmd/simple_chain's app.keyPair.Address() resolves to when this secret is used as
		// config.json's top-level "private_key". Needed by gen_single_chain.py to register a
		// genesis alloc entry for THIS address (see its own call site's doc comment for the live
		// incident this closes: that address never had one, so any cross-chain rollup system tx
		// it signs fails "invalid sign" -- config.json's separate "address" field is an unrelated
		// ECDSA identity and registering a pubkey there does not help this address at all).
		fmt.Println(addr.Hex())
	}
}
