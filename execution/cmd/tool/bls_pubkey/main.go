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
	"os"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

func main() {
	secretHex := flag.String("secret", "", "BLS secret scalar hex (with or without 0x prefix)")
	flag.Parse()
	if *secretHex == "" {
		fmt.Fprintln(os.Stderr, "Error: -secret is required")
		os.Exit(1)
	}
	_, pub, _ := bls.GenerateKeyPairFromSecretKey(strings.TrimPrefix(*secretHex, "0x"))
	pubBytes := pub.Bytes()
	if len(pubBytes) != 48 {
		fmt.Fprintf(os.Stderr, "Error: derived public key is %d bytes, expected 48 (invalid secret?)\n", len(pubBytes))
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pubBytes))
}
