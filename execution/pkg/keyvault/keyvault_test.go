package keyvault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secret = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b"

func TestRoundTripAndFreshEnvelopes(t *testing.T) {
	a, err := Encrypt(secret, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encrypt(secret, "correct horse battery")
	if a == b {
		t.Fatal("two encryptions of the same secret are identical: salt/nonce are not random")
	}
	if !IsEncrypted(a) || strings.Contains(a, secret[:16]) {
		t.Fatalf("envelope malformed or leaks the plaintext: %s", a)
	}
	got, err := Decrypt(a, "correct horse battery")
	if err != nil || got != secret {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestWrongPasswordAndTamperingAreRejected(t *testing.T) {
	e, _ := Encrypt(secret, "correct horse battery")
	if _, err := Decrypt(e, "wrong password!!"); err == nil {
		t.Fatal("wrong password accepted")
	}
	// flip one character inside the base64 payload
	i := len(prefix) + 30
	bad := e[:i] + string(rune(e[i]^0x01)) + e[i+1:]
	if _, err := Decrypt(bad, "correct horse battery"); err == nil {
		t.Fatal("tampered envelope accepted")
	}
	if _, err := Decrypt("plain-value", "correct horse battery"); err == nil {
		t.Fatal("non-envelope accepted")
	}
	if _, err := Encrypt(secret, "short"); err == nil {
		t.Fatal("weak password accepted")
	}
}

func TestLoadPassword(t *testing.T) {
	t.Setenv(PasswordEnv, "")
	t.Setenv(PasswordFileEnv, "")
	if _, err := LoadPassword(""); err == nil {
		t.Fatal("no password source but no error")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "pw")
	if err := os.WriteFile(f, []byte("from-file-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pw, err := LoadPassword(f); err != nil || pw != "from-file-password" {
		t.Fatalf("file password: %q %v", pw, err)
	}
	if err := os.Chmod(f, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPassword(f); err == nil {
		t.Fatal("a world-readable password file was accepted")
	}
	t.Setenv(PasswordEnv, "from-env-password")
	if pw, err := LoadPassword(f); err != nil || pw != "from-env-password" {
		t.Fatalf("env must take precedence: %q %v", pw, err)
	}
}
