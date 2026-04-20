package ldb_storage

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type LevelDBStorage struct {
	db    *leveldb.DB
	Mutex sync.Mutex
}

func NewLevelDBStorage(path string) (*LevelDBStorage, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		// Kiểm tra nếu lỗi có chứa từ "corrupted" hoặc "missing"
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "corrupted") || strings.Contains(errMsg, "missing") {
			return nil, fmt.Errorf("lỗi mở LevelDB tại '%s': database entry point either missing or corrupted. Hãy xóa thư mục và tạo lại: rm -rf %s", path, path)
		}
		return nil, fmt.Errorf("lỗi mở LevelDB tại '%s': %w", path, err)
	}
	return &LevelDBStorage{db: db}, nil
}

// NewLevelDBStorageWithRecovery tạo LevelDB storage với khả năng tự động recover nếu corrupted
func NewLevelDBStorageWithRecovery(path string, recoverOnCorrupted bool) (*LevelDBStorage, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		isCorrupted := strings.Contains(errMsg, "corrupted") || strings.Contains(errMsg, "missing")
		
		// Nếu database bị corrupted và cho phép recover
		if recoverOnCorrupted && isCorrupted {
			// Thử recover
			recoveredDB, recoverErr := leveldb.RecoverFile(path, nil)
			if recoverErr != nil {
				// Nếu recover thất bại, xóa và tạo mới
				if removeErr := os.RemoveAll(path); removeErr != nil {
					return nil, fmt.Errorf("không thể xóa database corrupted tại '%s': %w (lỗi gốc: %v)", path, removeErr, err)
				}
				// Tạo lại database mới
				newDB, newErr := leveldb.OpenFile(path, nil)
				if newErr != nil {
					return nil, fmt.Errorf("không thể tạo lại database tại '%s': %w (lỗi gốc: %v)", path, newErr, err)
				}
				return &LevelDBStorage{db: newDB}, nil
			}
			return &LevelDBStorage{db: recoveredDB}, nil
		}
		return nil, fmt.Errorf("lỗi mở LevelDB tại '%s': %w", path, err)
	}
	return &LevelDBStorage{db: db}, nil
}

func (s *LevelDBStorage) Put(key, value []byte) error {
	return s.db.Put(key, value, nil)
}

func (s *LevelDBStorage) Get(key []byte) ([]byte, error) {
	return s.db.Get(key, nil)
}

func (s *LevelDBStorage) Delete(key []byte) error {
	return s.db.Delete(key, nil)
}
func (s *LevelDBStorage) Has(key []byte, ro *opt.ReadOptions) (bool, error) {
	_, err := s.db.Get(key, ro)

	if err == nil {
		return true, nil
	}
	if errors.Is(err, leveldb.ErrNotFound) {
		return false, nil
	}
	return false, err
}

// Thêm phương thức này để hỗ trợ ghi theo lô
func (s *LevelDBStorage) WriteBatch(batch *leveldb.Batch, wo *opt.WriteOptions) error {
	return s.db.Write(batch, wo)
}
func (s *LevelDBStorage) NewIterator(slice *util.Range, ro *opt.ReadOptions) iterator.Iterator {
	return s.db.NewIterator(slice, ro)
}
func (s *LevelDBStorage) Close() {
	if s.db != nil {
		s.db.Close()
	}
}
