package storage

import (
	"bytes"
	"testing"
)

func TestLazyPebbleSyncDurableFlushesBufferedWrites(t *testing.T) {
	lp := NewLazyPebbleDB(t.TempDir())
	if err := lp.Open(1); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer lp.Close()

	if err := lp.Put([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := lp.BatchPut([][2][]byte{{[]byte("k2"), []byte("v2")}}); err != nil {
		t.Fatalf("batch put: %v", err)
	}

	// Buffered writes are not in Pebble yet.
	if _, err := lp.db.Get([]byte("k1")); err == nil {
		t.Fatalf("k1 unexpectedly already in Pebble before SyncDurable")
	}

	if err := lp.SyncDurable(); err != nil {
		t.Fatalf("SyncDurable: %v", err)
	}
	for k, want := range map[string]string{"k1": "v1", "k2": "v2"} {
		got, err := lp.db.Get([]byte(k))
		if err != nil || !bytes.Equal(got, []byte(want)) {
			t.Fatalf("after SyncDurable %s: got %q err=%v, want %q", k, got, err, want)
		}
	}

	// Idle shard: no new writes -> nothing to do, and the dirty flag stays clear.
	if err := lp.SyncDurable(); err != nil {
		t.Fatalf("second SyncDurable: %v", err)
	}
	if lp.dirty.Load() {
		t.Fatalf("dirty flag set after SyncDurable with no new writes")
	}
}

func TestSyncDurableThroughPrefixStorageAndShards(t *testing.T) {
	db, err := NewShardelDB(t.TempDir(), 4, 1, TypePebbleDB, "")
	if err != nil {
		t.Fatalf("new sharded db: %v", err)
	}
	if err := db.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	blocks := NewPrefixStorage(db, "blocks:")
	for i := 0; i < 16; i++ {
		if err := blocks.Put([]byte{byte(i)}, []byte{byte(i), 1}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := SyncDurable(blocks); err != nil {
		t.Fatalf("SyncDurable via PrefixStorage: %v", err)
	}

	// Every write of every shard must now be in Pebble itself, not only in the Go-level cache.
	for i := 0; i < 16; i++ {
		key := append([]byte("blocks:"), byte(i))
		found := false
		for _, shard := range db.shards {
			if lp, ok := shard.(*LazyPebbleDB); ok {
				if got, err := lp.db.Get(key); err == nil && bytes.Equal(got, []byte{byte(i), 1}) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("key %d not durable in any shard after SyncDurable", i)
		}
	}
}

func TestSyncDurableIsNoopForStoragesWithoutSupport(t *testing.T) {
	if err := SyncDurable(NewMemoryDb()); err != nil {
		t.Fatalf("SyncDurable on memory db: %v", err)
	}
}
