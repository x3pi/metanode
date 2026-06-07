package storage

import (
	"bytes"
	"io/ioutil"
	"os"
	"testing"
)

func TestShardelDB_BasicAndIterator(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "shardeldb_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a ShardelDB with 3 shards using PebbleDB backend
	db, err := NewShardelDB(tmpDir, 3, 1, TypePebbleDB, "")
	if err != nil {
		t.Fatalf("failed to create ShardelDB: %v", err)
	}
	if err := db.Open(); err != nil {
		t.Fatalf("failed to open ShardelDB: %v", err)
	}
	defer db.Close()

	// Put some keys
	kvs := map[string]string{
		"apple":  "red",
		"banana": "yellow",
		"cherry": "dark_red",
		"date":   "brown",
	}

	for k, v := range kvs {
		if err := db.Put([]byte(k), []byte(v)); err != nil {
			t.Errorf("failed to Put key %s: %v", k, err)
		}
	}

	// Flush lazy flusher if needed (PebbleDB is wrapped by LazyPebbleDB)
	if err := db.Flush(); err != nil {
		t.Fatalf("failed to Flush: %v", err)
	}

	// Verify Get
	for k, v := range kvs {
		got, err := db.Get([]byte(k))
		if err != nil {
			t.Errorf("failed to Get key %s: %v", k, err)
		}
		if string(got) != v {
			t.Errorf("expected %s, got %s for key %s", v, string(got), k)
		}
	}

	// Verify Has
	for k := range kvs {
		has, err := db.Has([]byte(k))
		if err != nil {
			t.Errorf("failed Has check for key %s: %v", k, err)
		}
		if !has {
			t.Errorf("expected key %s to be present", k)
		}
	}

	// Test Iterator without bounds
	it := db.NewIterator(nil, nil)
	defer it.Release()

	var keys []string
	var vals []string
	for it.Next() {
		keys = append(keys, string(it.Key()))
		vals = append(vals, string(it.Value()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}

	// Results must be sorted globally
	expectedKeys := []string{"apple", "banana", "cherry", "date"}
	if len(keys) != len(expectedKeys) {
		t.Fatalf("expected %d keys, got %d", len(expectedKeys), len(keys))
	}
	for i, k := range expectedKeys {
		if keys[i] != k {
			t.Errorf("expected key at %d to be %s, got %s", i, k, keys[i])
		}
		expectedVal := kvs[k]
		if vals[i] != expectedVal {
			t.Errorf("expected val at %d to be %s, got %s", i, expectedVal, vals[i])
		}
	}

	// Test Iterator with start and end bounds: [banana, date) -> should return banana and cherry
	it2 := db.NewIterator([]byte("banana"), []byte("date"))
	defer it2.Release()

	var boundedKeys []string
	for it2.Next() {
		boundedKeys = append(boundedKeys, string(it2.Key()))
	}
	if err := it2.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}

	expectedBoundedKeys := []string{"banana", "cherry"}
	if len(boundedKeys) != len(expectedBoundedKeys) {
		t.Fatalf("expected %d bounded keys, got %d", len(expectedBoundedKeys), len(boundedKeys))
	}
	for i, k := range expectedBoundedKeys {
		if boundedKeys[i] != k {
			t.Errorf("expected bounded key at %d to be %s, got %s", i, k, boundedKeys[i])
		}
	}

	// Test BatchPut & BatchDelete
	batch := db.NewBatch()
	batch.Put([]byte("fig"), []byte("purple"))
	batch.Put([]byte("grape"), []byte("green"))
	if err := batch.Write(); err != nil {
		t.Fatalf("batch write failed: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("failed to Flush: %v", err)
	}

	gotFig, err := db.Get([]byte("fig"))
	if err != nil || string(gotFig) != "purple" {
		t.Errorf("expected fig to be purple, got %s (err: %v)", string(gotFig), err)
	}

	// BatchDelete
	if err := db.BatchDelete([][]byte{[]byte("banana"), []byte("cherry")}); err != nil {
		t.Fatalf("batch delete failed: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("failed to Flush: %v", err)
	}

	hasBanana, _ := db.Has([]byte("banana"))
	if hasBanana {
		t.Error("expected banana to be deleted")
	}
}

func TestShardelDB_SingleShard(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "shardeldb_single_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a ShardelDB with 1 shard
	db, err := NewShardelDB(tmpDir, 1, 1, TypePebbleDB, "")
	if err != nil {
		t.Fatalf("failed to create ShardelDB: %v", err)
	}
	if err := db.Open(); err != nil {
		t.Fatalf("failed to open ShardelDB: %v", err)
	}
	defer db.Close()

	if err := db.Put([]byte("key1"), []byte("val1")); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	got, err := db.Get([]byte("key1"))
	if err != nil || !bytes.Equal(got, []byte("val1")) {
		t.Fatalf("expected val1, got %s (err: %v)", string(got), err)
	}
}
