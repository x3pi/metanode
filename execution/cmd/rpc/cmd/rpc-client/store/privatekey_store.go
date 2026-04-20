package store

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
	nonceSize                 = 12
)

func deriveKeyInternal(password string, salt []byte, pepper string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("password không được rỗng để dẫn xuất khóa")
	}
	if len(salt) == 0 {
		return nil, fmt.Errorf("salt không được rỗng để dẫn xuất khóa")
	}
	if pepper == "" {
		return nil, fmt.Errorf("pepper không được rỗng để dẫn xuất khóa")
	}

	saltedPassword := password + pepper
	dk, err := scrypt.Key([]byte(saltedPassword), salt, 32768, 8, 1, aesKeySize)
	if err != nil {
		return nil, fmt.Errorf("lỗi dẫn xuất khóa bằng Scrypt: %w", err)
	}
	return dk, nil
}

func encryptInternal(plaintext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("lỗi tạo AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("lỗi tạo GCM mode: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("lỗi tạo nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

func decryptInternal(encryptedData []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("lỗi tạo AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("lỗi tạo GCM mode: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(encryptedData) < nonceSize {
		return nil, fmt.Errorf("dữ liệu mã hóa quá ngắn")
	}
	nonce, ciphertext := encryptedData[:nonceSize], encryptedData[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("lỗi giải mã dữ liệu: %w", err)
	}
	return plaintext, nil
}

// PrivateKeyStore quản lý các cặp address-privateKey đã được mã hóa trong LevelDB.
type PrivateKeyStore struct {
	db             *leveldb.DB
	kdfSalt        []byte
	masterPassword string
	appPepper      string
}

func NewPrivateKeyStore(masterPassword, appPepper string) (*PrivateKeyStore, error) {
	if masterPassword == "" || appPepper == "" {
		return nil, fmt.Errorf("masterPassword và appPepper không được rỗng")
	}
	fullPath := filepath.Join(".", privateKeyDBPathEncrypted)
	logger.Info("Khởi tạo CSDL private key đã mã hóa tại: %s", fullPath)
	db, err := leveldb.OpenFile(fullPath, nil)
	if err != nil {
		return nil, fmt.Errorf("lỗi mở CSDL: %w", err)
	}

	var kdfSalt []byte
	kdfSaltHex, err := db.Get([]byte(kdfSaltKey), nil)
	if err != nil {
		if err == errors.ErrNotFound {
			kdfSalt = make([]byte, kdfSaltSize)
			if _, err := io.ReadFull(rand.Reader, kdfSalt); err != nil {
				db.Close()
				return nil, fmt.Errorf("lỗi tạo salt: %w", err)
			}
			err = db.Put([]byte(kdfSaltKey), []byte(hex.EncodeToString(kdfSalt)), nil)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("lỗi lưu salt: %w", err)
			}
		} else {
			db.Close()
			return nil, fmt.Errorf("lỗi lấy salt: %w", err)
		}
	} else {
		kdfSalt, err = hex.DecodeString(string(kdfSaltHex))
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("lỗi giải mã salt: %w", err)
		}
	}

	return &PrivateKeyStore{db: db, kdfSalt: kdfSalt, masterPassword: masterPassword, appPepper: appPepper}, nil
}

func (pks *PrivateKeyStore) Close() error {
	if pks.db != nil {
		return pks.db.Close()
	}
	return nil
}

func (pks *PrivateKeyStore) SetPrivateKey(address common.Address, privateKey string) error {
	if privateKey == "" {
		return fmt.Errorf("private key không được rỗng")
	}
	encryptionKey, err := deriveKeyInternal(pks.masterPassword, pks.kdfSalt, pks.appPepper)
	if err != nil {
		return err
	}
	encryptedPrivateKey, err := encryptInternal([]byte(privateKey), encryptionKey)
	if err != nil {
		return err
	}
	return pks.db.Put(address.Bytes(), encryptedPrivateKey, nil)
}

func (pks *PrivateKeyStore) GetPrivateKey(address common.Address) (string, error) {
	encryptedPrivateKey, err := pks.db.Get(address.Bytes(), nil)
	if err != nil {
		if err == errors.ErrNotFound {
			return "", fmt.Errorf("không tìm thấy private key")
		}
		return "", err
	}
	encryptionKey, err := deriveKeyInternal(pks.masterPassword, pks.kdfSalt, pks.appPepper)
	if err != nil {
		return "", err
	}
	plaintext, err := decryptInternal(encryptedPrivateKey, encryptionKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (pks *PrivateKeyStore) DeletePrivateKey(address common.Address) error {
	return pks.db.Delete(address.Bytes(), nil)
}

func (pks *PrivateKeyStore) ListAddressPrivateKeyPairs() (map[string]string, error) {
	results := make(map[string]string)
	iter := pks.db.NewIterator(nil, nil)
	defer iter.Release()

	encryptionKey, err := deriveKeyInternal(pks.masterPassword, pks.kdfSalt, pks.appPepper)
	if err != nil {
		return nil, err
	}

	for iter.Next() {
		if string(iter.Key()) == kdfSaltKey {
			continue
		}
		addr := common.BytesToAddress(iter.Key())
		val := iter.Value()
		plaintext, err := decryptInternal(val, encryptionKey)
		if err != nil {
			results[addr.Hex()] = "[LỖI GIẢI MÃ]"
		} else {
			results[addr.Hex()] = string(plaintext)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return results, nil
}

func (pks *PrivateKeyStore) HasPrivateKey(address common.Address) (bool, error) {
	return pks.db.Has(address.Bytes(), nil)
}
