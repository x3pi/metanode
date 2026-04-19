package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"golang.org/x/crypto/scrypt"
)

const (
	privateKeyDBPathEncrypted = "privatekey_db_encrypted"
	kdfSaltKey                = "_kdf_salt_"
	kdfSaltSize               = 32
	aesKeySize                = 32
	// nonceSize = 12 — not used directly, GCM provides NonceSize()
)

// PrivateKeyStore manages encrypted BLS private keys in LevelDB,
// keyed by Ethereum address. Encryption uses AES-256-GCM with a
// Scrypt-derived key (master password + salt + pepper).
type PrivateKeyStore struct {
	db             *leveldb.DB
	kdfSalt        []byte
	masterPassword string
	appPepper      string
}

// NewPrivateKeyStore opens (or creates) the encrypted key store at basePath.
func NewPrivateKeyStore(basePath, masterPassword, appPepper string) (*PrivateKeyStore, error) {
	if masterPassword == "" || appPepper == "" {
		return nil, fmt.Errorf("masterPassword and appPepper must not be empty")
	}
	fullPath := filepath.Join(basePath, privateKeyDBPathEncrypted)
	logger.Info("Opening encrypted private key DB at: %s", fullPath)

	db, err := leveldb.OpenFile(fullPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open key store DB: %w", err)
	}

	var kdfSalt []byte
	kdfSaltHex, err := db.Get([]byte(kdfSaltKey), nil)
	if err != nil {
		if err == errors.ErrNotFound {
			kdfSalt = make([]byte, kdfSaltSize)
			if _, err := io.ReadFull(rand.Reader, kdfSalt); err != nil {
				db.Close()
				return nil, fmt.Errorf("failed to generate salt: %w", err)
			}
			if err := db.Put([]byte(kdfSaltKey), []byte(hex.EncodeToString(kdfSalt)), nil); err != nil {
				db.Close()
				return nil, fmt.Errorf("failed to store salt: %w", err)
			}
		} else {
			db.Close()
			return nil, fmt.Errorf("failed to read salt: %w", err)
		}
	} else {
		kdfSalt, err = hex.DecodeString(string(kdfSaltHex))
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to decode salt: %w", err)
		}
	}

	return &PrivateKeyStore{
		db:             db,
		kdfSalt:        kdfSalt,
		masterPassword: masterPassword,
		appPepper:      appPepper,
	}, nil
}

func (pks *PrivateKeyStore) Close() error {
	if pks.db != nil {
		return pks.db.Close()
	}
	return nil
}

// SetPrivateKey encrypts and stores a BLS private key for the given address.
func (pks *PrivateKeyStore) SetPrivateKey(address common.Address, privateKey string) error {
	if privateKey == "" {
		return fmt.Errorf("private key must not be empty")
	}
	encKey, err := deriveKey(pks.masterPassword, pks.kdfSalt, pks.appPepper)
	if err != nil {
		return err
	}
	encrypted, err := encryptAESGCM([]byte(privateKey), encKey)
	if err != nil {
		return err
	}
	return pks.db.Put(address.Bytes(), encrypted, nil)
}

// GetPrivateKey retrieves and decrypts the BLS private key for the given address.
func (pks *PrivateKeyStore) GetPrivateKey(address common.Address) (string, error) {
	encrypted, err := pks.db.Get(address.Bytes(), nil)
	if err != nil {
		if err == errors.ErrNotFound {
			return "", fmt.Errorf("private key not found for %s", address.Hex())
		}
		return "", err
	}
	encKey, err := deriveKey(pks.masterPassword, pks.kdfSalt, pks.appPepper)
	if err != nil {
		return "", err
	}
	plaintext, err := decryptAESGCM(encrypted, encKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// HasPrivateKey checks whether a key exists for the given address.
func (pks *PrivateKeyStore) HasPrivateKey(address common.Address) (bool, error) {
	return pks.db.Has(address.Bytes(), nil)
}

// DeletePrivateKey removes the stored key for the given address.
func (pks *PrivateKeyStore) DeletePrivateKey(address common.Address) error {
	return pks.db.Delete(address.Bytes(), nil)
}

// ---------- crypto helpers ----------

func deriveKey(password string, salt []byte, pepper string) ([]byte, error) {
	salted := password + pepper
	dk, err := scrypt.Key([]byte(salted), salt, 32768, 8, 1, aesKeySize)
	if err != nil {
		return nil, fmt.Errorf("scrypt key derivation failed: %w", err)
	}
	return dk, nil
}

func encryptAESGCM(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES cipher creation failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM mode creation failed: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce generation failed: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decryptAESGCM(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES cipher creation failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM mode creation failed: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("encrypted data too short")
	}
	nonce, ciphertext := data[:ns], data[ns:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
