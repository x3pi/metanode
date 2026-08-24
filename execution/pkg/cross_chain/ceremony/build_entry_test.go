package ceremony

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// writeFixtureKeysDir writes a synthetic key directory in the same shape
// `metanode keytool generate validator --out-dir <dir>` produces, using a
// REAL BLS secret scalar (so BuildFoundingEntry's key derivation is exercised
// against real cryptography) and placeholder Ed25519/eth values (only their
// presence, not their internal validity, is checked by BuildFoundingEntry).
// ethAddress must be a well-formed "0x"+40-hex-char address; callers that need
// distinct operators use distinct addresses (see fixtureEthAddress).
func writeFixtureKeysDir(t *testing.T, blsPrivHex string, ethAddress string) string {
	t.Helper()
	dir := t.TempDir()

	writeJSON := func(name string, v interface{}) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// authority_key/protocol_key/network_key public values are NOT
	// cryptographically checked by BuildFoundingEntry or by
	// ceremony.Assemble beyond byte length (96B / 32B / 32B respectively,
	// matching the real fastcrypto min-sig/Ed25519 key sizes) — fill with
	// fixed-length placeholder bytes rather than a real curve point.
	writeJSON("authority_key.json", keytoolKeyFile{
		PrivateKeyHex:   blsPrivHex,
		PublicKeyBase64: fixturePlaceholderBase64(96),
	})
	writeJSON("protocol_key.json", keytoolKeyFile{
		PrivateKeyHex:   fixturePlaceholderHex(32),
		PublicKeyBase64: fixturePlaceholderBase64(32),
	})
	writeJSON("network_key.json", keytoolKeyFile{
		PrivateKeyHex:   fixturePlaceholderHex(32),
		PublicKeyBase64: fixturePlaceholderBase64(32),
	})
	writeJSON("eth_key.json", keytoolEthKeyFile{
		EthPrivateKey: "0xcc33",
		EthAddress:    ethAddress,
	})

	return dir
}

// fixtureEthAddress deterministically builds a distinct, well-formed
// "0x"+40-hex address for test fixture N, so callers that need multiple
// distinct operators (e.g. Assemble tests) don't accidentally collide.
func fixtureEthAddress(n int) string {
	return fmt.Sprintf("0x%040x", n+1)
}

// fixturePlaceholderBase64 returns nBytes of random data, base64-encoded —
// used for protocol_key/network_key/authority_key placeholder public values
// so distinct calls (distinct validators in a test) never accidentally
// collide the way a fixed constant would.
func fixturePlaceholderBase64(nBytes int) string {
	return base64.StdEncoding.EncodeToString(fixtureRandomBytes(nBytes))
}

// fixturePlaceholderHex returns nBytes of random data, hex-encoded — same
// purpose as fixturePlaceholderBase64 but for private_key_hex fields.
func fixturePlaceholderHex(nBytes int) string {
	return hex.EncodeToString(fixtureRandomBytes(nBytes))
}

func fixtureRandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func newValidParams(t *testing.T, keysDir string) BuildFoundingEntryParams {
	t.Helper()
	return BuildFoundingEntryParams{
		KeysDir:     keysDir,
		ChainID:     101,
		ChainName:   "Alpha Chain",
		Hostname:    "node-0",
		IP:          "203.0.113.10",
		P2PPort:     9100,
		PrimaryPort: 6200,
		WorkerPort:  4012,
		StakeWei:    "1000000000000000000000", // 1000 * 1e18
		Commission:  5,
	}
}

func realBLSPrivHex(t *testing.T) string {
	t.Helper()
	kp := bls.GenerateKeyPair()
	return hex.EncodeToString(kp.BytesPrivateKey())
}

func TestBuildFoundingEntry_HappyPath(t *testing.T) {
	privHex := realBLSPrivHex(t)
	dir := writeFixtureKeysDir(t, privHex, fixtureEthAddress(0))

	fe, err := BuildFoundingEntry(newValidParams(t, dir))
	if err != nil {
		t.Fatalf("BuildFoundingEntry: %v", err)
	}

	if fe.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", fe.SchemaVersion, SchemaVersion)
	}
	if fe.ChainID != 101 {
		t.Errorf("chain_id = %d, want 101", fe.ChainID)
	}
	if fe.CrossChain.Stake != 1000 {
		t.Errorf("cross_chain.stake = %d, want 1000 (1000e18 / 1e18)", fe.CrossChain.Stake)
	}
	if fe.GenesisAlloc.PublicKeyBls != fe.CrossChain.PubkeyBLS {
		t.Errorf("genesis_alloc.publicKeyBls (%s) != cross_chain.pubkey_bls (%s)", fe.GenesisAlloc.PublicKeyBls, fe.CrossChain.PubkeyBLS)
	}

	// The whole point of ValidateCommitteeEntry() being called inside
	// BuildFoundingEntry is fail-closed self-verification; re-verify here as
	// an end-to-end check that the PoP this produced is actually valid.
	pub, err := decodeHex0x(fe.CrossChain.PubkeyBLS, 48)
	if err != nil {
		t.Fatalf("decode pubkey: %v", err)
	}
	sig, err := decodeHex0x(fe.CrossChain.PopSignature, 96)
	if err != nil {
		t.Fatalf("decode pop signature: %v", err)
	}
	ok, err := cross_chain.PopVerify(pub, sig)
	if err != nil || !ok {
		t.Fatalf("PopVerify failed on BuildFoundingEntry's own output: ok=%v err=%v", ok, err)
	}
}

func TestBuildFoundingEntry_NoPrivateKeyLeakage(t *testing.T) {
	privHex := realBLSPrivHex(t)
	dir := writeFixtureKeysDir(t, privHex, fixtureEthAddress(0))

	fe, err := BuildFoundingEntry(newValidParams(t, dir))
	if err != nil {
		t.Fatalf("BuildFoundingEntry: %v", err)
	}

	out, err := json.Marshal(fe)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	outStr := string(out)

	secrets := []string{privHex}
	for _, name := range []string{"protocol_key.json", "network_key.json", "eth_key.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var f struct {
			PrivateKeyHex string `json:"private_key_hex"`
			EthPrivateKey string `json:"ETH_PRIVATE_KEY"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if f.PrivateKeyHex != "" {
			secrets = append(secrets, f.PrivateKeyHex)
		}
		if f.EthPrivateKey != "" {
			secrets = append(secrets, f.EthPrivateKey, strings.TrimPrefix(f.EthPrivateKey, "0x"))
		}
	}

	for _, s := range secrets {
		if strings.Contains(outStr, s) {
			t.Errorf("founding_entry.json output leaks a private key substring %q", s)
		}
	}
}

// TestBuildFoundingEntry_ToleratesGenValidatorEntryPyRewrite covers a real
// interoperability gap: deploy/systemd/gen_validator_entry.py rewrites
// protocol_key.json/network_key.json in place into a raw base64 string of
// priv(32B)||pub(32B) (needed by the Rust node's node.toml at runtime) BEFORE
// this tool would ever see them if an operator runs gen_validator_entry.py
// --founding-entry (which shells out to founding_entry after key
// generation). BuildFoundingEntry must still work against that on-disk shape.
func TestBuildFoundingEntry_ToleratesGenValidatorEntryPyRewrite(t *testing.T) {
	privHex := realBLSPrivHex(t)
	dir := writeFixtureKeysDir(t, privHex, fixtureEthAddress(0))

	rewriteAsRawBase64 := func(name string) {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var f keytoolKeyFile
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		privBytes, err := hex.DecodeString(f.PrivateKeyHex)
		if err != nil {
			t.Fatalf("decode %s private_key_hex: %v", name, err)
		}
		pubBytes, err := base64.StdEncoding.DecodeString(f.PublicKeyBase64)
		if err != nil {
			t.Fatalf("decode %s public_key_base64: %v", name, err)
		}
		combined := append(append([]byte{}, privBytes...), pubBytes...)
		rewritten := base64.StdEncoding.EncodeToString(combined)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(rewritten), 0o600); err != nil {
			t.Fatalf("rewrite %s: %v", name, err)
		}
	}
	rewriteAsRawBase64("protocol_key.json")
	rewriteAsRawBase64("network_key.json")

	fe, err := BuildFoundingEntry(newValidParams(t, dir))
	if err != nil {
		t.Fatalf("BuildFoundingEntry against gen_validator_entry.py-rewritten key files: %v", err)
	}
	if err := requireKeyLen(fe.GenesisValidator.ProtocolKey, true, 32, "protocol_key"); err != nil {
		t.Errorf("recovered protocol_key is malformed: %v", err)
	}
	if err := requireKeyLen(fe.GenesisValidator.NetworkKey, true, 32, "network_key"); err != nil {
		t.Errorf("recovered network_key is malformed: %v", err)
	}
}

func TestBuildFoundingEntry_RequiredFields(t *testing.T) {
	privHex := realBLSPrivHex(t)
	dir := writeFixtureKeysDir(t, privHex, fixtureEthAddress(0))

	cases := []struct {
		name   string
		mutate func(p *BuildFoundingEntryParams)
	}{
		{"missing keys dir", func(p *BuildFoundingEntryParams) { p.KeysDir = "" }},
		{"missing chain id", func(p *BuildFoundingEntryParams) { p.ChainID = 0 }},
		{"missing chain name", func(p *BuildFoundingEntryParams) { p.ChainName = "" }},
		{"missing hostname", func(p *BuildFoundingEntryParams) { p.Hostname = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newValidParams(t, dir)
			tc.mutate(&p)
			if _, err := BuildFoundingEntry(p); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestNormalizeStakeToCommitteeUnits(t *testing.T) {
	cases := []struct {
		name    string
		wei     string
		want    uint64
		wantErr bool
	}{
		{"typical validator stake", "1000000000000000000000", 1000, false},
		{"minimum representable", "1000000000000000000", 1, false},
		{"below minimum normalizes to zero", "999999999999999999", 0, true},
		{"invalid decimal", "not-a-number", 0, true},
		{"negative", "-1000000000000000000", 0, true},
		{"absurdly large overflows uint64 after scaling", "100000000000000000000000000000000000000", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeStakeToCommitteeUnits(tc.wei)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}
