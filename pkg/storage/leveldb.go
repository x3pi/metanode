package storage

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	sharedmemory "github.com/meta-node-blockchain/meta-node/pkg/shared_memory"
)

type LevelDBManager struct {
	instances      sync.Map
	writeInstances sync.Map
	mu             sync.RWMutex
	maxOpenFiles   int
	activeFiles    int
	idleTimeout    time.Duration
}

// GetStats trả về thống kê về số lượng instances đang mở
func (mgr *LevelDBManager) GetStats() (readOnlyCount int, writeCount int, totalCount int) {
	mgr.instances.Range(func(key, value interface{}) bool {
		readOnlyCount++
		return true
	})
	mgr.writeInstances.Range(func(key, value interface{}) bool {
		writeCount++
		return true
	})
	totalCount = readOnlyCount + writeCount
	return
}

type LevelDB struct {
	db             *leveldb.DB
	closed         bool
	path           string
	lastActive     time.Time
	mu             sync.Mutex
	levelDBManager *LevelDBManager
}

func NewLevelDBManager(maxOpenFiles int, idleTimeout time.Duration) *LevelDBManager {
	mgr := &LevelDBManager{
		instances:      sync.Map{},
		writeInstances: sync.Map{},
		maxOpenFiles:   maxOpenFiles,
		idleTimeout:    idleTimeout,
	}
	return mgr
}

type LevelDBSnapShot struct {
	snapShot *leveldb.Snapshot
}

func (mgr *LevelDBManager) GetOrCreate(path string, isReadOnly bool) (*LevelDB, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if path == "" {
		logger.Error("Path for LevelDB is empty")
		return nil, fmt.Errorf("invalid path: path is empty")
	}
	if isReadOnly {
		if db, exists := mgr.instances.Load(path); exists {
			return db.(*LevelDB), nil
		}
	} else {
		if db, exists := mgr.writeInstances.Load(path); exists {
			return db.(*LevelDB), nil
		}
	}
	if mgr.activeFiles >= mgr.maxOpenFiles {
		logger.Warn("Maximum open files reached. Attempting to close idle databases.")
		// mgr.closeIdleDatabases()
		if mgr.activeFiles >= mgr.maxOpenFiles {
			return nil, fmt.Errorf("cannot open more LevelDB instances, maximum limit reached")
		}
	}

	const maxRetries = 5
	var db *leveldb.DB
	var err error

	for i := 0; i < maxRetries; i++ {
		options := &opt.Options{
			BlockCacheCapacity:  256 * opt.MiB,
			WriteBuffer:         64 * opt.MiB,
			CompactionTableSize: 32 * opt.MiB,
		}
		if isReadOnly {
			options.ReadOnly = true
		}
		db, err = leveldb.OpenFile(path, options)

		if err == nil {
			break
		}

		if !strings.Contains(err.Error(), "too many open files") {
			logger.Error("Failed to open LevelDB at path: %s, error: %v (not retrying)", path, err)
			return nil, err
		}

		sharedmemory.GlobalSharedMemory.Write("overloaded", true)
		logger.Warn("Failed to open LevelDB (attempt %d/%d): %v", i+1, maxRetries, err)
	}

	if db == nil {
		logger.Error("LevelDB instance is nil after OpenFile")
		return nil, fmt.Errorf("failed to initialize LevelDB: db is nil")
	}

	lvDb := &LevelDB{
		db:             db,
		closed:         false,
		path:           path,
		lastActive:     time.Now(),
		levelDBManager: mgr,
	}

	if isReadOnly {
		mgr.instances.LoadOrStore(path, lvDb)
	} else {
		mgr.writeInstances.LoadOrStore(path, lvDb)
	}
	mgr.activeFiles++

	// Log khi tạo instance mới
	readOnlyCount, writeCount, totalCount := mgr.GetStats()
	logger.Info("LevelDBManager: Tạo instance mới - Path: %s, ReadOnly: %v, Total instances: %d (read: %d, write: %d), ActiveFiles: %d/%d",
		path, isReadOnly, totalCount, readOnlyCount, writeCount, mgr.activeFiles, mgr.maxOpenFiles)

	return lvDb, nil
}

func (lvDb *LevelDB) ensureOpen() error {
	lvDb.mu.Lock()
	defer lvDb.mu.Unlock()

	if lvDb.closed {
		var err error
		lvDb.db, err = leveldb.OpenFile(lvDb.path, nil)
		if err != nil {
			return err
		}
		lvDb.closed = false
	}
	return nil
}

func (lvDb *LevelDB) updateLastActive() {
	lvDb.mu.Lock()
	defer lvDb.mu.Unlock()
	lvDb.lastActive = time.Now()
}

func (lvDb *LevelDB) Get(key []byte) ([]byte, error) {
	if err := lvDb.ensureOpen(); err != nil {
		return nil, err
	}
	lvDb.updateLastActive()
	return lvDb.db.Get(key, nil)
}

func (lvDb *LevelDB) Put(key, value []byte) error {
	if err := lvDb.ensureOpen(); err != nil {
		return err
	}
	lvDb.updateLastActive()
	return lvDb.db.Put(key, value, nil)
}

func (lvDb *LevelDB) Flush() error {
	// LevelDB does not have an explicit "flush" operation like some other databases.
	// Writes are typically buffered and flushed automatically.
	// The closest equivalent might be a manual compaction, but that's not a direct flush.
	// For now, we'll return nil, indicating no explicit flush is needed or possible.
	return nil
}

func (lvDb *LevelDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	return nil, fmt.Errorf("PrefixScan not implemented for LevelDB")
}

func (lvDb *LevelDB) Close() error {
	lvDb.mu.Lock()
	defer lvDb.mu.Unlock()

	if !lvDb.closed {
		closeChan := make(chan struct{}) // Create a channel to signal completion
		done := make(chan struct{})      // Channel để đảm bảo goroutine đã hoàn thành

		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic trong LevelDB.Close() goroutine: %v", r)
				}
				close(done) // Đảm bảo done luôn được close
			}()

			// Đóng database trực tiếp, không cần iterator
			// Iterator chỉ làm chậm và không cần thiết khi đóng database
			if err := lvDb.db.Close(); err != nil {
				logger.Error("Failed to close LevelDB at path %s: %v", lvDb.path, err)
			} else {
				logger.Info("LDBM Close: %s", lvDb.path)
			}
			lvDb.closed = true
			close(closeChan) // Signal completion
		}()

		// Đợi goroutine hoàn thành hoặc timeout
		select {
		case <-closeChan:
			// Đợi goroutine thực sự kết thúc
			<-done
			lvDb.levelDBManager.removeInstance(lvDb.path)
			runtime.GC()
			debug.FreeOSMemory()
			return nil
		case <-time.After(5 * time.Second): // Timeout 5 giây
			logger.Warn("Timeout khi đóng LevelDB tại path %s, goroutine có thể vẫn đang chạy", lvDb.path)
			// Đánh dấu là closed để tránh gọi lại
			lvDb.closed = true
			lvDb.levelDBManager.removeInstance(lvDb.path)
			// Không return error để tránh block, nhưng log warning
			return nil
		}
	}
	return nil
}

func (mgr *LevelDBManager) removeInstance(path string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	mgr.instances.Delete(path)
	mgr.writeInstances.Delete(path)
	logger.Info("LDBM delete path: %s", path)
	if mgr.activeFiles > 0 {
		mgr.activeFiles--
	}

	// Log sau khi xóa instance
	readOnlyCount, writeCount, totalCount := mgr.GetStats()
	logger.Info("LevelDBManager: Xóa instance - Path: %s, Total instances: %d (read: %d, write: %d), ActiveFiles: %d/%d",
		path, totalCount, readOnlyCount, writeCount, mgr.activeFiles, mgr.maxOpenFiles)
}

func (ldb *LevelDB) Compact() error {
	var isExistOverloaded bool
	value, exists := sharedmemory.GlobalSharedMemory.Read("overloaded")

	if !exists {
		isExistOverloaded = false
	} else {
		var ok bool
		isExistOverloaded, ok = value.(bool) // Type assertion
		if !ok {
			err := fmt.Errorf("Error: cannot convert 'overloaded' to bool")
			return err
		}
	}
	if isExistOverloaded {
		return nil
	}

	return ldb.db.CompactRange(util.Range{
		Start: nil,
		Limit: nil,
	})
}

func (ldb *LevelDB) Has(key []byte) bool {
	has, _ := ldb.db.Has(key, nil)
	return has
}

func (ldb *LevelDB) Delete(key []byte) error {
	return ldb.db.Delete(key, nil)
}

func (ldb *LevelDB) BatchPut(kvs [][2][]byte) error {
	if err := ldb.ensureOpen(); err != nil {
		return err
	}
	ldb.updateLastActive()
	batch := new(leveldb.Batch)
	for i := range kvs {
		batch.Put(kvs[i][0], kvs[i][1])
	}
	return ldb.db.Write(batch, nil)
}

func (ldb *LevelDB) Open() error {
	var err error
	if ldb.closed {
		ldb.db, err = leveldb.OpenFile(ldb.path, nil)
		if err != nil {
			return err
		}
		ldb.closed = false
	}
	return nil
}

func (ldb *LevelDB) GetIterator() IIterator {
	return ldb.db.NewIterator(nil, nil)

}

func (ldb *LevelDB) GetSnapShot() SnapShot {
	snapShot, _ := ldb.db.GetSnapshot()
	return NewLevelDBSnapShot(snapShot)
}

func (ldb *LevelDB) Stats() *pb.LevelDBStats {
	stats := &leveldb.DBStats{}
	ldb.db.Stats(stats)
	levelSizes := make([]uint64, len(stats.LevelSizes))
	for i, v := range stats.LevelSizes {
		levelSizes[i] = uint64(v)
	}
	levelRead := make([]uint64, len(stats.LevelRead))
	for i, v := range stats.LevelRead {
		levelRead[i] = uint64(v)
	}
	levelTablesCounts := make([]uint64, len(stats.LevelTablesCounts))
	for i, v := range stats.LevelTablesCounts {
		levelTablesCounts[i] = uint64(v)
	}
	levelWrite := make([]uint64, len(stats.LevelWrite))
	for i, v := range stats.LevelWrite {
		levelWrite[i] = uint64(v)
	}
	levelDurations := make([]uint64, len(stats.LevelDurations))
	for i, v := range stats.LevelDurations {
		levelDurations[i] = uint64(v)
	}
	pbStat := &pb.LevelDBStats{
		LevelSizes:        levelSizes,
		LevelTablesCounts: levelTablesCounts,
		LevelRead:         levelRead,
		LevelWrite:        levelWrite,
		LevelDurations:    levelDurations,
		MemComp:           stats.MemComp,
		Level0Comp:        stats.Level0Comp,
		NonLevel0Comp:     stats.NonLevel0Comp,
		SeekComp:          stats.SeekComp,
		AliveSnapshots:    stats.AliveSnapshots,
		AliveIterators:    stats.AliveIterators,
		IOWrite:           stats.IOWrite,
		IORead:            stats.IORead,
		BlockCacheSize:    int32(stats.BlockCacheSize),
		OpenedTablesCount: int32(stats.OpenedTablesCount),
		Path:              ldb.path,
	}
	return pbStat
}

func NewLevelDBSnapShot(snapShot *leveldb.Snapshot) *LevelDBSnapShot {
	return &LevelDBSnapShot{
		snapShot: snapShot,
	}
}

func (snapShot *LevelDBSnapShot) GetIterator() IIterator {
	return snapShot.snapShot.NewIterator(nil, nil)
}

func (snapShot *LevelDBSnapShot) Release() {
	snapShot.snapShot.Release()
}
