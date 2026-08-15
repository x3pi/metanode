// Package blob_store persists EIP-4844 blob sidecars (raw blob + KZG commitment +
// proof) separately from the transaction that references them.
//
// A blob tx only ever commits to its blobs' versioned hashes (see
// Transaction.Hash()/RHash() and the comment on Transaction.Sidecar in
// transaction.proto) — the raw ~128KiB-per-blob payload is "network
// representation", KZG-verified once on ingestion, and expendable after a
// retention window. That makes blob pruning fundamentally simpler than state
// history pruning: there is no "floor value that must remain queryable
// forever" the way an account's current balance is (see
// state_changelog.PruneBeforeBlock and its compact-to-floor fix) — once a
// blob is outside the retention window, there is nothing else that ever
// needs it, so a plain delete-before-cutoff is correct here.
package blob_store

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

const (
	// Fixed KZG sizes (see crypto/kzg4844.Blob/Commitment/Proof in go-ethereum).
	blobSize       = 131072
	commitmentSize = 48
	proofSize      = 48

	dataPrefix  = "d:" // d:<versionedHash>                -> commitment || proof || blob
	indexPrefix = "i:" // i:<blockNumber BE><versionedHash> -> (empty, existence only)
)

// BlobStore is a Pebble-backed store for blob sidecars, keyed by the blob's
// versioned hash (the same value carried in Transaction.BlobVersionedHashes).
type BlobStore struct {
	db        *pebble.DB
	namespace string
	mu        sync.RWMutex
}

// NewBlobStore opens (or creates) a blob store at path.
func NewBlobStore(path string, namespace string) (*BlobStore, error) {
	opts := &pebble.Options{
		DisableWAL: false,
		Cache:      storage.GetSharedPebbleCache(),
		TableCache: storage.GetSharedPebbleTableCache(),
	}
	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open blob store pebble db at %s: %w", path, err)
	}
	logger.Info("🫧 [BLOB-STORE] Initialized at %s for namespace %s", path, namespace)
	return &BlobStore{db: db, namespace: namespace}, nil
}

// Close gracefully shuts down the database.
func (s *BlobStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *BlobStore) dataKey(versionedHash []byte) []byte {
	return append([]byte(dataPrefix), versionedHash...)
}

func (s *BlobStore) indexKey(blockNumber uint64, versionedHash []byte) []byte {
	key := make([]byte, 0, len(indexPrefix)+8+len(versionedHash))
	key = append(key, []byte(indexPrefix)...)
	blockBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockBytes, blockNumber)
	key = append(key, blockBytes...)
	key = append(key, versionedHash...)
	return key
}

// Put persists one blob's sidecar content, indexed by blockNumber for later
// PruneBeforeBlock range deletion. blob/commitment/proof must already be
// KZG-verified against versionedHash by the caller (see
// transaction.VerifyBlobSidecar) — this function does no cryptographic checks
// of its own, it only stores bytes.
func (s *BlobStore) Put(blockNumber uint64, versionedHash, commitment, proof, blob []byte) error {
	if len(commitment) != commitmentSize {
		return fmt.Errorf("blob_store.Put: invalid commitment length %d, want %d", len(commitment), commitmentSize)
	}
	if len(proof) != proofSize {
		return fmt.Errorf("blob_store.Put: invalid proof length %d, want %d", len(proof), proofSize)
	}
	if len(blob) != blobSize {
		return fmt.Errorf("blob_store.Put: invalid blob length %d, want %d", len(blob), blobSize)
	}

	value := make([]byte, 0, commitmentSize+proofSize+blobSize)
	value = append(value, commitment...)
	value = append(value, proof...)
	value = append(value, blob...)

	s.mu.RLock()
	defer s.mu.RUnlock()

	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(s.dataKey(versionedHash), value, nil); err != nil {
		return err
	}
	if err := batch.Set(s.indexKey(blockNumber, versionedHash), nil, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// BlobRecord is one blob's full sidecar content.
type BlobRecord struct {
	Commitment []byte
	Proof      []byte
	Blob       []byte
}

// Get returns the blob record for versionedHash, or found=false if it was
// never stored or has already been pruned.
func (s *BlobStore) Get(versionedHash []byte) (rec BlobRecord, found bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	val, closer, getErr := s.db.Get(s.dataKey(versionedHash))
	if getErr != nil {
		if getErr == pebble.ErrNotFound {
			return BlobRecord{}, false, nil
		}
		return BlobRecord{}, false, getErr
	}
	defer closer.Close()

	if len(val) != commitmentSize+proofSize+blobSize {
		return BlobRecord{}, false, fmt.Errorf("blob_store.Get: corrupt record for %x: length %d", versionedHash, len(val))
	}
	rec.Commitment = append([]byte(nil), val[:commitmentSize]...)
	rec.Proof = append([]byte(nil), val[commitmentSize:commitmentSize+proofSize]...)
	rec.Blob = append([]byte(nil), val[commitmentSize+proofSize:]...)
	return rec, true, nil
}

// PruneBeforeBlock deletes every blob (and its index entry) recorded strictly
// before targetBlock. Unlike state_changelog's history, there is no floor
// value to preserve here — each blob belongs to exactly one already-executed
// transaction and is never "the current value" of anything, so a plain
// range delete is correct.
func (s *BlobStore) PruneBeforeBlock(targetBlock uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	startKey := []byte(indexPrefix)
	endKey := s.indexKeyPrefixForBlock(targetBlock)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return err
	}
	defer iter.Close()

	batch := s.db.NewBatch()
	defer batch.Close()

	count := 0
	for iter.SeekGE(startKey); iter.Valid(); iter.Next() {
		key := iter.Key()
		versionedHash := key[len(indexPrefix)+8:]
		if err := batch.Delete(s.dataKey(versionedHash), nil); err != nil {
			return err
		}
		if err := batch.Delete(append([]byte(nil), key...), nil); err != nil {
			return err
		}
		count++
		if count%10000 == 0 {
			if err := batch.Commit(pebble.Sync); err != nil {
				return err
			}
			batch.Reset()
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	if count%10000 != 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return err
		}
	}

	logger.Info("🧹 [BLOB-STORE] Pruned %d blobs before block %d in namespace %s", count, targetBlock, s.namespace)
	return nil
}

// indexKeyPrefixForBlock returns the index key upper bound for a range-delete
// covering all blocks strictly before targetBlock (exclusive upper bound).
func (s *BlobStore) indexKeyPrefixForBlock(targetBlock uint64) []byte {
	key := make([]byte, 0, len(indexPrefix)+8)
	key = append(key, []byte(indexPrefix)...)
	blockBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(blockBytes, targetBlock)
	key = append(key, blockBytes...)
	return key
}
