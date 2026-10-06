package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/keyvault"
)

const testKey = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"

func pwFile(t *testing.T, pw string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(f, []byte(pw), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestResolveSecrets_DecryptsEnvelopesInPlace(t *testing.T) {
	t.Setenv(keyvault.PasswordEnv, "")
	t.Setenv(keyvault.PasswordFileEnv, "")
	e, _ := keyvault.Encrypt(testKey, "unit-test-password")
	e2, _ := keyvault.Encrypt("sender-key", "unit-test-password")
	c := &SimpleChainConfig{PrivateKey: e, RewardSenderPrivateKey: e2, KeyPasswordFile: pwFile(t, "unit-test-password\n"), Address: "0x1"}
	c.Databases.BLSPrivateKey = "plain-bls" // mixing is allowed unless require_encrypted_keys
	if err := resolveSecrets(c); err != nil {
		t.Fatal(err)
	}
	if c.PrivateKey != testKey || c.RewardSenderPrivateKey != "sender-key" || c.Databases.BLSPrivateKey != "plain-bls" || c.Address != "0x1" {
		t.Fatalf("unexpected result: %+v", c)
	}
}

func TestResolveSecrets_FailsClosed(t *testing.T) {
	t.Setenv(keyvault.PasswordEnv, "")
	t.Setenv(keyvault.PasswordFileEnv, "")
	e, _ := keyvault.Encrypt(testKey, "unit-test-password")

	if err := resolveSecrets(&SimpleChainConfig{PrivateKey: e}); err == nil || !strings.Contains(err.Error(), "private_key") {
		t.Fatalf("no password source must fail naming the field: %v", err)
	}
	if err := resolveSecrets(&SimpleChainConfig{PrivateKey: e, KeyPasswordFile: pwFile(t, "another-password")}); err == nil {
		t.Fatal("wrong password accepted")
	}
	c := &SimpleChainConfig{PrivateKey: testKey, RequireEncryptedKeys: true}
	if err := resolveSecrets(c); err == nil || !strings.Contains(err.Error(), "private_key") {
		t.Fatalf("require_encrypted_keys must refuse a clear-text key: %v", err)
	}
	// plain keys stay allowed by default (no behaviour change for existing configs)
	if err := resolveSecrets(&SimpleChainConfig{PrivateKey: testKey}); err != nil {
		t.Fatalf("default config with a plain key must still load: %v", err)
	}
}

func TestResolveSecrets_OverrideCanBeAnEnvelopeToo(t *testing.T) {
	e, _ := keyvault.Encrypt(testKey, "unit-test-password")
	t.Setenv(keyvault.PasswordEnv, "unit-test-password")
	c := &SimpleChainConfig{PrivateKey: e, RequireEncryptedKeys: true}
	if err := resolveSecrets(c); err != nil || c.PrivateKey != testKey {
		t.Fatalf("%v %q", err, c.PrivateKey)
	}
}
