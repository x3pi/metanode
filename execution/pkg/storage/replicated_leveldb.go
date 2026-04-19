package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Cấu trúc quản lý một LevelDB với snapshot
type ReplicatedLevelDB struct {
	primaryDB *leveldb.DB
	snapshot  *leveldb.Snapshot
	path      string
}

// Tạo mới `ReplicatedLevelDB`
func NewReplicatedLevelDB(path string) *ReplicatedLevelDB {
	return &ReplicatedLevelDB{path: path}
}

// hasExistingDB checks if the directory contains an existing LevelDB database
func hasExistingDB(path string) bool {
	currentFile := filepath.Join(path, "CURRENT")
	_, err := os.Stat(currentFile)
	return err == nil
}

// Mở LevelDB và tạo snapshot ban đầu
// Sử dụng RecoverFile khi phát hiện database đã tồn tại để tránh mất data
// sau kill -9 (OpenFile silently tạo DB mới khi MANIFEST/WAL bị hỏng)
func (r *ReplicatedLevelDB) Open(parallelism int) error {
	if err := createDirIfNotExists(r.path); err != nil {
		logger.Error("Không thể tạo thư mục DB:", err)
		return err
	}

	var err error
	maxRetries := 5
	retryDelay := 1000

	// Kiểm tra xem database đã tồn tại chưa
	existingDB := hasExistingDB(r.path)
	if existingDB {
		logger.Info("📂 [LEVELDB] Existing database detected, using RecoverFile for safe open: %s", r.path)
	}

	for i := 0; i < maxRetries; i++ {
		if existingDB {
			// Khi database đã tồn tại, LUÔN dùng RecoverFile trước
			// RecoverFile sẽ replay WAL và khôi phục data đã bị mất khi kill -9
			// OpenFile sẽ silently tạo DB mới nếu MANIFEST bị hỏng → mất toàn bộ data
			r.primaryDB, err = leveldb.RecoverFile(r.path, nil)
			if err == nil {
				logger.Info("✅ [LEVELDB] Recovered existing database: %s", r.path)
				break
			}
			logger.Warn("🔧 [LEVELDB] RecoverFile failed (attempt %d), trying OpenFile: %s, error: %v", i+1, r.path, err)
			// Fallback: thử OpenFile nếu RecoverFile thất bại
			r.primaryDB, err = leveldb.OpenFile(r.path, nil)
			if err == nil {
				logger.Warn("⚠️ [LEVELDB] Opened with OpenFile fallback (possible data loss): %s", r.path)
				break
			}
		} else {
			// Database mới, dùng OpenFile bình thường
			r.primaryDB, err = leveldb.OpenFile(r.path, nil)
			if err == nil {
				break
			}
		}
		logger.Error("Lỗi mở LevelDB thử lại lần: ", i+1)
		logger.Error("Error:", err, r.path)
		time.Sleep(time.Duration(retryDelay) * time.Millisecond)
	}

	if err != nil {
		logger.Error("Lỗi mở LevelDB sau nhiều lần thử:", err, r.path)
		return err
	}
	// Tạo snapshot ban đầu
	r.snapshot, err = r.primaryDB.GetSnapshot()
	if err != nil {
		logger.Error("Lỗi tạo snapshot ban đầu:", err)
		return err
	}

	logger.Info("✅ [LEVELDB] Database opened: %s", r.path)
	return nil
}

// Ghi dữ liệu vào Primary và cập nhật snapshot
func (r *ReplicatedLevelDB) Put(key, value []byte) error {
	syncOpts := &opt.WriteOptions{} // Removed Sync: true to speed up async background flushes
	err := r.primaryDB.Put(key, value, syncOpts)
	if err != nil {
		return fmt.Errorf("LevelDB Put failed (path=%s): %w", r.path, err)
	}

	// Cập nhật snapshot sau khi ghi
	return r.updateSnapshot()
}

func (r *ReplicatedLevelDB) BatchPut(kvs [][2][]byte) error {
	batch := new(leveldb.Batch)
	for _, kv := range kvs {
		batch.Put(kv[0], kv[1])
	}
	writeOptions := &opt.WriteOptions{} // Removed Sync: true to speed up async background flushes
	// Ghi batch vào primary database
	err := r.primaryDB.Write(batch, writeOptions)
	if err != nil {
		return err
	}

	// Cập nhật snapshot sau khi batch write
	return r.updateSnapshot()
}

// Ưu tiên đọc dữ liệu từ snapshot nếu lỗi đọc từ db
func (r *ReplicatedLevelDB) Get(key []byte) ([]byte, error) {
	// r.mu.RLock() // Chờ đến khi không có updateSnapshot() đang chạy
	// defer r.mu.RUnlock()
	if r.snapshot == nil {
		logger.Info("Snapshot chưa được khởi tạo")

		return nil, fmt.Errorf("snapshot chưa được khởi tạo")
	}
	value, err := r.snapshot.Get(key, nil)
	if err != nil {
		// Debug
		value, err = r.primaryDB.Get(key, nil)
		if err != nil {
			logger.Debug("Get from primaryDB err", r.path, err)
		}
		// panic(fmt.Sprintf("Dừng chương trình do Get db thất bại: key=%s", hex.EncodeToString(key)))
	}
	// Thêm lệnh debug ở đây
	return value, err
}

// Xóa key khỏi Primary và cập nhật snapshot
func (r *ReplicatedLevelDB) Delete(key []byte) error {

	err := r.primaryDB.Delete(key, nil)
	if err != nil {
		return err
	}

	// Cập nhật snapshot sau khi xóa
	return r.updateSnapshot()
}

// Kiểm tra key có tồn tại không
func (r *ReplicatedLevelDB) Has(key []byte) bool {
	if r.snapshot == nil {
		return false
	}
	exists, _ := r.snapshot.Has(key, nil)
	return exists
}

// Lấy tất cả key trong database (chỉ dùng để debug)
func (r *ReplicatedLevelDB) GetAllKeys() ([]string, error) {

	var keys []string
	iter := r.primaryDB.NewIterator(nil, nil)
	for iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	iter.Release()

	if err := iter.Error(); err != nil {
		return nil, err
	}
	return keys, nil
}

// PrefixScan iterates all keys with the given prefix and returns key-value pairs.
// Keys in results have the prefix stripped (matching PebbleDB convention).
func (r *ReplicatedLevelDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	if r.primaryDB == nil {
		return nil, fmt.Errorf("ReplicatedLevelDB: database not opened")
	}

	iter := r.primaryDB.NewIterator(util.BytesPrefix(prefix), nil)
	defer iter.Release()

	var results [][2][]byte
	for iter.Next() {
		// Strip prefix from key
		key := make([]byte, len(iter.Key())-len(prefix))
		copy(key, iter.Key()[len(prefix):])
		value := make([]byte, len(iter.Value()))
		copy(value, iter.Value())
		results = append(results, [2][]byte{key, value})
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("ReplicatedLevelDB PrefixScan error: %w", err)
	}
	return results, nil
}

func (r *ReplicatedLevelDB) Close() error {

	var err error
	if r.snapshot != nil {
		r.snapshot.Release()
	}
	if r.primaryDB != nil {
		err = r.primaryDB.Close()
	}
	return err
}

// Flush ensures any pending writes are synced to disk (no-op for standard LevelDB usage without buffers)
func (r *ReplicatedLevelDB) Flush() error {
	return nil
}

// Checkpoint creates a point-in-time snapshot of the database using hardlinks.
// It hardlinks immutable SST files and performs a regular copy for metadata files.
func (r *ReplicatedLevelDB) Checkpoint(destDir string) error {
	logger.Info("📸 [LEVELDB] Creating native checkpoint: %s → %s", r.path, destDir)
	return hardlinkCopyLevelDB(r.path, destDir)
}

// hardlinkCopyLevelDB creates an atomic-like snapshot via hardlinks.
// SST files are hardlinked (instant, zero space), while mutable files (MANIFEST, LOG) are copied.
func hardlinkCopyLevelDB(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dst, err)
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		lower := strings.ToLower(info.Name())
		isImmutable := strings.HasSuffix(lower, ".ldb") || strings.HasSuffix(lower, ".sst")

		if isImmutable {
			// Hardlink immutable SST files
			if err := os.Link(path, dstPath); err != nil {
				logger.Warn("📸 [LEVELDB] Hardlink failed for %s, falling back to copy: %v", relPath, err)
				return regularCopyFile(path, dstPath, info.Mode())
			}
		} else {
			// Deep-copy mutable metadata/WAL files (MANIFEST, CURRENT, LOG, vLog)
			if err := regularCopyFile(path, dstPath, info.Mode()); err != nil {
				return fmt.Errorf("failed to copy %s: %w", relPath, err)
			}
		}

		return nil
	})
}

// regularCopyFile safely copies a file byte-by-byte.
func regularCopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func (r *ReplicatedLevelDB) updateSnapshot() error {
	if r.snapshot != nil {
		r.snapshot.Release()
	}

	var err error
	r.snapshot, err = r.primaryDB.GetSnapshot()
	if err != nil {
		logger.Error("Lỗi cập nhật snapshot:", err)
		return err
	}

	return nil
}

// Tạo thư mục nếu chưa tồn tại
func createDirIfNotExists(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, os.ModePerm)
	}
	return nil
}
