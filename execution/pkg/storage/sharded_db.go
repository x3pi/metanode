package storage

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"golang.org/x/sync/errgroup"
)

type DBType int

const (
	TypeRocksDB DBType = iota
	TypeLevelDB
	TypePebbleDB
)

// ShardDBInterface is the interface for shard-level database operations.
type ShardDBInterface interface {
	Open(parallelism int) error
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
	BatchPut(keys [][2][]byte) error
	PrefixScan(prefix []byte) ([][2][]byte, error)
	Flush() error
	Close() error
	// Checkpoint creates an atomic, consistent snapshot at destDir.
	// Falls back to Flush() + error for backends that don't support it.
	Checkpoint(destDir string) error
}

// ShardelDB manages multiple database shards with key-based sharding.
type ShardelDB struct {
	shards      []ShardDBInterface
	numShards   int
	parallelism int
	backupPath  string
	dbPath      string
}

// getShardIndex returns the shard index for a given key.
func (s *ShardelDB) getShardIndex(key []byte) uint32 {
	hash := md5.Sum(key)
	index := binary.BigEndian.Uint32(hash[:4]) % uint32(s.numShards)
	return index
}
func (s *ShardelDB) GetBackupPath() string {
	return s.backupPath
}

func (s *ShardelDB) GetDbPath() string {
	return s.dbPath
}

// NewShardelDB creates a new sharded database with the specified backend type.
func NewShardelDB(baseDir string, numShards int, parallelism int, dbType DBType, backupPath string) (*ShardelDB, error) {
	shards := make([]ShardDBInterface, numShards)
	for i := 0; i < numShards; i++ {
		primaryPath := fmt.Sprintf("%s/db_shard_%d", baseDir, i)
		var shard ShardDBInterface
		switch dbType {
		case TypeRocksDB:
			shard = NewReplicatedLevelDB(primaryPath)
		case TypeLevelDB:
			shard = NewLazyLevelDB(primaryPath)
		case TypePebbleDB:
			shard = NewPebbleDB(primaryPath)
		default:
			return nil, fmt.Errorf("unsupported db type: %d", dbType)
		}
		shards[i] = shard
	}

	return &ShardelDB{
		shards:      shards,
		numShards:   numShards,
		parallelism: parallelism,
		backupPath:  backupPath,
		dbPath:      baseDir,
	}, nil
}

// Mở toàn bộ LevelDB shards
func (s *ShardelDB) Open() error {
	for _, shard := range s.shards {
		if err := shard.Open(s.parallelism); err != nil {
			return err
		}
	}
	return nil
}

// Flush forces all shards to write pending memtable buffers to disk.
func (s *ShardelDB) Flush() error {
	var g errgroup.Group
	for i, shard := range s.shards {
		if shard == nil {
			continue
		}
		shardIndex, shard := i, shard
		g.Go(func() error {
			if err := shard.Flush(); err != nil {
				return fmt.Errorf("shard %d flush error: %w", shardIndex, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// Checkpoint creates an atomic snapshot of all shards to destBaseDir.
// Each shard is checkpointed in parallel to destBaseDir/db_shard_N.
func (s *ShardelDB) Checkpoint(destBaseDir string) error {
	var g errgroup.Group
	for i, shard := range s.shards {
		if shard == nil {
			continue
		}
		shardIndex, shard := i, shard
		g.Go(func() error {
			destDir := fmt.Sprintf("%s/db_shard_%d", destBaseDir, shardIndex)
			if err := shard.Checkpoint(destDir); err != nil {
				return fmt.Errorf("shard %d checkpoint error: %w", shardIndex, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// Băm key để quyết định lưu vào shard nào
func (s *ShardelDB) getShard(key []byte) ShardDBInterface {
	hash := md5.Sum(key)
	index := binary.BigEndian.Uint32(hash[:4]) % uint32(s.numShards)
	return s.shards[index]
}

// Ghi dữ liệu vào shard tương ứng
func (s *ShardelDB) Put(key, value []byte) error {
	shard := s.getShard(key)
	return shard.Put(key, value)
}

// Đọc dữ liệu từ snapshot thay vì replica
func (s *ShardelDB) Get(key []byte) ([]byte, error) {
	shard := s.getShard(key)
	return shard.Get(key)
}

// Xóa key khỏi shard tương ứng
func (s *ShardelDB) Delete(key []byte) error {
	shard := s.getShard(key)
	return shard.Delete(key)
}

// Đóng toàn bộ database
func (s *ShardelDB) Close() error {
	for _, shard := range s.shards {
		shard.Close()
	}
	return nil
}

// Thêm method Compact để đáp ứng interface ethdb.KeyValueStore
func (s *ShardelDB) Compact(start, limit []byte) error {
	// Implement logic for compacting the database here.  This is a placeholder.
	// You'll need to adapt this based on the requirements of ethdb.KeyValueStore.
	logger.Warn("Compact method called.  Implementation needed.")
	return nil
}

// Thêm method DeleteRange để đáp ứng interface ethdb.KeyValueStore
func (s *ShardelDB) DeleteRange(start, end []byte) error {
	// Implement logic to delete a range of keys from the database here.
	// This is a placeholder. You need to iterate through shards and delete
	// the relevant keys within each shard.  Consider handling potential errors
	// during deletion.
	logger.Warn("DeleteRange method called. Implementation needed.")
	// for _, shard := range s.shards {
	// 	err := shard.DeleteRange(start, end)
	// 	if err != nil {
	// 		return fmt.Errorf("error deleting range from shard: %w", err)
	// 	}
	// }
	return nil
}

func (s *ShardelDB) Has(key []byte) (bool, error) {
	shard := s.getShard(key)
	value, err := shard.Get(key)
	if err != nil {
		return false, err
	}
	return len(value) > 0, nil
}

// NewBatch trả về một batch mới cho ShardelDB
func (s *ShardelDB) NewBatch() ethdb.Batch {
	return &shardBatch{
		s:     s,
		batch: make(map[int][][2][]byte),
	}
}

type shardBatch struct {
	s     *ShardelDB
	batch map[int][][2][]byte
}

func (b *shardBatch) Put(key, value []byte) error {

	shardIndex := int(b.s.getShardIndex(key))
	b.batch[shardIndex] = append(b.batch[shardIndex], [2][]byte{key, value})
	return nil
}

func (b *shardBatch) Delete(key []byte) error {
	shardIndex := int(b.s.getShardIndex(key))
	for i, kv := range b.batch[shardIndex] {
		if bytes.Equal(kv[0], key) {
			b.batch[shardIndex] = append(b.batch[shardIndex][:i], b.batch[shardIndex][i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *ShardelDB) BatchDelete(keys [][]byte) error {
	var g errgroup.Group
	shardKeys := make(map[int][][]byte)
	// Nhóm keys theo shard
	for _, key := range keys {
		shardIndex := int(s.getShardIndex(key))
		shardKeys[shardIndex] = append(shardKeys[shardIndex], key)
	}

	// Xóa song song trên các shard
	for shardIndex, keys := range shardKeys {
		shardIndex, keys := shardIndex, keys // tránh vấn đề closure dùng chung biến
		g.Go(func() error {
			shard := s.shards[shardIndex]
			if shard == nil {
				return fmt.Errorf("shard %d is nil", shardIndex)
			}
			for _, key := range keys {
				if err := shard.Delete(key); err != nil {
					return fmt.Errorf("shard %d: key %x, error: %w", shardIndex, key, err)
				}
			}
			return nil
		})
	}

	// Chờ tất cả goroutine hoàn thành và trả về lỗi (nếu có)
	return g.Wait()
}

func (s *ShardelDB) BatchPut(kvs [][2][]byte) error {
	var g errgroup.Group
	shardBatches := make(map[int][][2][]byte)

	// Lấy block number trước khi dùng logger
	for _, kv := range kvs {
		shardIndex := int(s.getShardIndex(kv[0]))
		shardBatches[shardIndex] = append(shardBatches[shardIndex], kv)
	}

	// Xử lý batch song song theo shard
	for shardIndex, batch := range shardBatches {
		if len(batch) == 0 {
			continue // Bỏ qua batch rỗng
		}
		shardIndex, batch := shardIndex, batch // Tránh closure bug
		g.Go(func() error {
			shard := s.shards[shardIndex]
			if shard == nil {
				return fmt.Errorf("shard %d is nil", shardIndex)
			}
			if err := shard.BatchPut(batch); err != nil {
				return fmt.Errorf("shard %d: batch put error: %w", shardIndex, err)
			}
			return nil
		})
	}

	// Chờ tất cả goroutine hoàn thành và trả về lỗi nếu có
	e := g.Wait()
	return e
}

// PrefixScan performs a prefix scan across all shards and merges the results.
func (s *ShardelDB) PrefixScan(prefix []byte) ([][2][]byte, error) {
	var allResults [][2][]byte
	var mu sync.Mutex
	var g errgroup.Group

	for i, shard := range s.shards {
		if shard == nil {
			continue
		}
		shardIndex, shard := i, shard
		g.Go(func() error {
			res, err := shard.PrefixScan(prefix)
			if err != nil {
				return fmt.Errorf("shard %d PrefixScan error: %w", shardIndex, err)
			}
			mu.Lock()
			allResults = append(allResults, res...)
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return allResults, nil
}

func (b *shardBatch) Write() error {
	logger.Warn("Write method called. Implementation needed.")
	var wg sync.WaitGroup
	errChan := make(chan error, b.s.numShards)
	for shardIndex, batch := range b.batch {
		wg.Add(1)
		go func(shardIndex int, batch [][2][]byte) {
			defer wg.Done()
			shard := b.s.shards[shardIndex]
			if shard == nil {
				errChan <- fmt.Errorf("shard %d is nil", shardIndex)
				return
			}
			if len(batch) == 0 {
				return
			}
			if err := shard.BatchPut(batch); err != nil {
				errChan <- fmt.Errorf("shard %d: batch: %v, error: %w", shardIndex, batch, err)
			}
		}(shardIndex, batch)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		return err
	}
	return nil
}

func (b *shardBatch) ValueSize() int {
	totalSize := 0
	for _, batch := range b.batch {
		for _, kv := range batch {
			totalSize += len(kv[0]) + len(kv[1])
		}
	}
	return totalSize
}

func (b *shardBatch) Reset() {
	for k := range b.batch {
		b.batch[k] = nil
	}
}

// Replay replays the batch contents against the provided KeyValueWriter.
func (b *shardBatch) Replay(db ethdb.KeyValueWriter) error {
	for _, batch := range b.batch {
		for _, kv := range batch {
			if len(kv[1]) == 0 {
				if err := db.Delete(kv[0]); err != nil {
					return fmt.Errorf("replay delete key %x: %w", kv[0], err)
				}
			} else {
				if err := db.Put(kv[0], kv[1]); err != nil {
					return fmt.Errorf("replay put key %x: %w", kv[0], err)
				}
			}
		}
	}
	return nil
}

// NewBatchWithSize creates a new batch with a pre-allocated capacity hint.
// The size parameter is a hint for initial capacity only (matching go-ethereum convention).
func (s *ShardelDB) NewBatchWithSize(size int) ethdb.Batch {
	batchMap := make(map[int][][2][]byte, s.numShards)
	return &shardBatch{
		s:     s,
		batch: batchMap,
	}
}

// NewIterator returns a new iterator over the database.
// It collects key-value pairs from all shards within the given range [start, end)
// and iterates over them in sorted key order.
func (s *ShardelDB) NewIterator(start, end []byte) ethdb.Iterator {
	return &shardIterator{
		db:    s,
		start: start,
		end:   end,
		pos:   -1,
	}
}

// shardIterator implements ethdb.Iterator across all shards.
// It lazily collects keys on first Next() call since shards are LevelDB-backed
// and don't expose their own iterators through ShardDBInterface.
type shardIterator struct {
	db    *ShardelDB
	start []byte
	end   []byte
	keys  [][]byte
	vals  [][]byte
	pos   int
	err   error
}

func (it *shardIterator) Next() bool {
	it.pos++
	return it.pos < len(it.keys)
}

func (it *shardIterator) Error() error {
	return it.err
}

func (it *shardIterator) Key() []byte {
	if it.pos < 0 || it.pos >= len(it.keys) {
		return nil
	}
	return it.keys[it.pos]
}

func (it *shardIterator) Value() []byte {
	if it.pos < 0 || it.pos >= len(it.vals) {
		return nil
	}
	return it.vals[it.pos]
}

func (it *shardIterator) Release() {
	it.keys = nil
	it.vals = nil
}

// Stat returns a stat string about the database.
func (s *ShardelDB) Stat() (string, error) {
	return fmt.Sprintf("Number of shards: %d", s.numShards), nil
}
