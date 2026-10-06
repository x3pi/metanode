package main

import (
	"encoding/base64"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/require"
)

func TestBLSPubKey_HexAndBase64(t *testing.T) {
	// Generate a known test key pair
	secHex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	_, pub, addr := bls.GenerateKeyPairFromSecretKey(secHex)
	expectedB64 := base64.StdEncoding.EncodeToString(pub.Bytes())
	expectedHex := "0x" + hex.EncodeToString(pub.Bytes())
	expectedAddr := addr.Hex()

	// 1. Test standard base64 output
	cmdB64 := exec.Command("go", "run", ".", "-secret", secHex)
	outB64, err := cmdB64.CombinedOutput()
	require.NoError(t, err, "go run with -secret must succeed: %s", string(outB64))
	require.Equal(t, expectedB64, strings.TrimSpace(string(outB64)))

	// 2. Test -hex output
	cmdHex := exec.Command("go", "run", ".", "-secret", secHex, "-hex")
	outHex, err := cmdHex.CombinedOutput()
	require.NoError(t, err, "go run with -hex must succeed: %s", string(outHex))
	require.Equal(t, expectedHex, strings.TrimSpace(string(outHex)))
	require.Equal(t, 2+96, len(strings.TrimSpace(string(outHex))), "hex pubkey must be 0x + 96 hex chars")

	// 3. Test -hex with -with-address
	cmdBoth := exec.Command("go", "run", ".", "-secret", secHex, "-hex", "-with-address")
	outBoth, err := cmdBoth.CombinedOutput()
	require.NoError(t, err, "go run with -hex -with-address must succeed: %s", string(outBoth))
	lines := strings.Split(strings.TrimSpace(string(outBoth)), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, expectedHex, strings.TrimSpace(lines[0]))
	require.Equal(t, expectedAddr, strings.TrimSpace(lines[1]))
}
