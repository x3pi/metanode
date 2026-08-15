package blob_store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *BlobStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "blobs")
	s, err := NewBlobStore(dir, "test")
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func fill(size int, b byte) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestBlobStore_PutGet(t *testing.T) {
	s := newTestStore(t)

	vh := []byte("versioned-hash-32-bytes-padded!")
	commitment := fill(commitmentSize, 0xAA)
	proof := fill(proofSize, 0xBB)
	blob := fill(blobSize, 0xCC)

	if err := s.Put(10, vh, commitment, proof, blob); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rec, found, err := s.Get(vh)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatalf("expected to find blob")
	}
	if string(rec.Commitment) != string(commitment) {
		t.Fatalf("commitment mismatch")
	}
	if string(rec.Proof) != string(proof) {
		t.Fatalf("proof mismatch")
	}
	if string(rec.Blob) != string(blob) {
		t.Fatalf("blob mismatch")
	}

	_, found, err = s.Get([]byte("never-stored-hash-32-bytes-pad!!"))
	if err != nil {
		t.Fatalf("Get(missing): %v", err)
	}
	if found {
		t.Fatalf("expected not found for a hash never stored")
	}
}

func TestBlobStore_Put_RejectsWrongSizes(t *testing.T) {
	s := newTestStore(t)
	vh := []byte("vh")

	if err := s.Put(1, vh, []byte("short"), fill(proofSize, 1), fill(blobSize, 1)); err == nil {
		t.Fatalf("expected error for short commitment")
	}
	if err := s.Put(1, vh, fill(commitmentSize, 1), []byte("short"), fill(blobSize, 1)); err == nil {
		t.Fatalf("expected error for short proof")
	}
	if err := s.Put(1, vh, fill(commitmentSize, 1), fill(proofSize, 1), []byte("short")); err == nil {
		t.Fatalf("expected error for short blob")
	}
}

func TestBlobStore_PruneBeforeBlock(t *testing.T) {
	s := newTestStore(t)

	vhOld := []byte("old-versioned-hash-32-bytes-pad")
	vhKept := []byte("kept-versioned-hash-32-bytes-pa")

	if err := s.Put(50, vhOld, fill(commitmentSize, 1), fill(proofSize, 1), fill(blobSize, 1)); err != nil {
		t.Fatalf("Put(old): %v", err)
	}
	if err := s.Put(150, vhKept, fill(commitmentSize, 2), fill(proofSize, 2), fill(blobSize, 2)); err != nil {
		t.Fatalf("Put(kept): %v", err)
	}

	if err := s.PruneBeforeBlock(100); err != nil {
		t.Fatalf("PruneBeforeBlock: %v", err)
	}

	if _, found, _ := s.Get(vhOld); found {
		t.Fatalf("expected old blob (block 50) to be pruned before cutoff 100")
	}
	if _, found, _ := s.Get(vhKept); !found {
		t.Fatalf("expected kept blob (block 150) to survive prune before cutoff 100")
	}
}
