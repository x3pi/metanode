// Package keyvault keeps secrets out of configuration files in clear text.
//
// A secret is stored as a self-describing envelope string
//
//	enc:v1:<base64( salt(16) | nonce(12) | AES-256-GCM(ciphertext+tag) )>
//
// with a key derived from a password by scrypt (N=2^15, r=8, p=1). The envelope can replace the plain value of
// any secret field of config.json; the node decrypts it once at startup, in memory. The password is supplied at
// startup from an environment variable or a file (mode 0600), never from config.json itself, so a leaked or
// backed-up config.json does not reveal the keys.
//
// This protects the key at rest and in configuration backups. It does not protect against an attacker who can
// read the password source and the config on the running host.
package keyvault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	prefix    = "enc:v1:"
	saltLen   = 16
	scryptN   = 1 << 15
	scryptR   = 8
	scryptP   = 1
	keyLen    = 32
	minPwdLen = 8

	// PasswordEnv names the environment variable that carries the password; PasswordFileEnv the one that names a file.
	PasswordEnv     = "META_KEY_PASSWORD"
	PasswordFileEnv = "META_KEY_PASSWORD_FILE"
)

// aad binds a ciphertext to this scheme so it cannot be replayed as another kind of encrypted blob.
var aad = []byte("metanode-config-secret-v1")

// IsEncrypted reports whether s is an envelope produced by Encrypt.
func IsEncrypted(s string) bool { return strings.HasPrefix(s, prefix) }

// Encrypt seals plain under password.
func Encrypt(plain, password string) (string, error) {
	if len(password) < minPwdLen {
		return "", fmt.Errorf("keyvault: password must be at least %d characters", minPwdLen)
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	gcm, err := newGCM(password, salt)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	blob := append(append(append([]byte(nil), salt...), nonce...), gcm.Seal(nil, nonce, []byte(plain), aad)...)
	return prefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt opens an envelope. A wrong password and a corrupted envelope are indistinguishable by design.
func Decrypt(envelope, password string) (string, error) {
	if !IsEncrypted(envelope) {
		return "", errors.New("keyvault: not an encrypted envelope")
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(envelope, prefix))
	if err != nil {
		return "", fmt.Errorf("keyvault: malformed envelope: %w", err)
	}
	if len(blob) < saltLen+12+16 {
		return "", errors.New("keyvault: envelope too short")
	}
	salt := blob[:saltLen]
	gcm, err := newGCM(password, salt)
	if err != nil {
		return "", err
	}
	nonce := blob[saltLen : saltLen+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, blob[saltLen+gcm.NonceSize():], aad)
	if err != nil {
		return "", errors.New("keyvault: wrong password or corrupted envelope")
	}
	return string(plain), nil
}

func newGCM(password string, salt []byte) (cipher.AEAD, error) {
	k, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// LoadPassword returns the password from META_KEY_PASSWORD, else from the file named by file or by
// META_KEY_PASSWORD_FILE. A password file that is readable by group or others is refused: it is the one secret
// that must not be shared.
func LoadPassword(file string) (string, error) {
	if v := os.Getenv(PasswordEnv); v != "" {
		return v, nil
	}
	if file == "" {
		file = os.Getenv(PasswordFileEnv)
	}
	if file == "" {
		return "", fmt.Errorf("keyvault: no password: set %s, or %s / key_password_file", PasswordEnv, PasswordFileEnv)
	}
	st, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("keyvault: password file: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("keyvault: password file %s is accessible by group/others (mode %o): chmod 600", file, st.Mode().Perm())
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}
