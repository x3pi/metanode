package storage

import (
	"bytes"
	"testing"
)

// The lazy DBs buffer writes in a memory map where a nil value is a tombstone. These tests drive delete / re-put / batch
// sequences against every flush state (nothing flushed, flushed, deleted after a flush) and check that point reads and
// prefix scans agree, for both backends. A reader that mistook a tombstone for data, or lost a re-put behind one, would
// make two nodes disagree about the same key.

type lazyKV interface {
	Open(parallelism int) error
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
	BatchPut(kvs [][2][]byte) error
	BatchDelete(keys [][]byte) error
	PrefixScan(prefix []byte) ([][2][]byte, error)
	Flush() error
	Close() error
}

func lazyBackends(t *testing.T) map[string]func() lazyKV {
	return map[string]func() lazyKV{
		"pebble":  func() lazyKV { return NewLazyPebbleDB(t.TempDir()) },
		"leveldb": func() lazyKV { return NewLazyLevelDB(t.TempDir()) },
	}
}

func mustGet(t *testing.T, db lazyKV, key string, want string, wantFound bool, ctx string) {
	t.Helper()
	got, err := db.Get([]byte(key))
	if wantFound {
		if err != nil || !bytes.Equal(got, []byte(want)) {
			t.Fatalf("%s: Get(%q) = %q, %v; want %q", ctx, key, got, err, want)
		}
		return
	}
	if err == nil {
		t.Fatalf("%s: Get(%q) = %q, want not found", ctx, key, got)
	}
}

func scanKeys(t *testing.T, db lazyKV, prefix string) map[string]string {
	t.Helper()
	res, err := db.PrefixScan([]byte(prefix))
	if err != nil {
		t.Fatalf("PrefixScan: %v", err)
	}
	m := map[string]string{}
	for _, kv := range res {
		m[string(kv[0])] = string(kv[1])
	}
	return m
}

func TestLazyDB_DeleteThenPutAgain_AllFlushStates(t *testing.T) {
	for name, mk := range lazyBackends(t) {
		for _, flushBefore := range []bool{false, true} {
			for _, flushBetween := range []bool{false, true} {
				db := mk()
				if err := db.Open(1); err != nil {
					t.Fatalf("%s open: %v", name, err)
				}
				ctx := name
				if flushBefore {
					ctx += "/flush-before-delete"
				}
				if flushBetween {
					ctx += "/flush-between"
				}
				if err := db.Put([]byte("p/a"), []byte("v1")); err != nil {
					t.Fatal(err)
				}
				if err := db.Put([]byte("p/b"), []byte("keep")); err != nil {
					t.Fatal(err)
				}
				if flushBefore {
					if err := db.Flush(); err != nil {
						t.Fatal(err)
					}
				}
				mustGet(t, db, "p/a", "v1", true, ctx+" after put")
				if err := db.Delete([]byte("p/a")); err != nil {
					t.Fatal(err)
				}
				mustGet(t, db, "p/a", "", false, ctx+" after delete")
				if got := scanKeys(t, db, "p/"); len(got) != 1 || got["b"] != "keep" {
					t.Fatalf("%s: scan after delete = %v, want only b", ctx, got)
				}
				if flushBetween {
					if err := db.Flush(); err != nil {
						t.Fatal(err)
					}
					mustGet(t, db, "p/a", "", false, ctx+" after delete+flush")
				}
				if err := db.Put([]byte("p/a"), []byte("v2")); err != nil {
					t.Fatal(err)
				}
				mustGet(t, db, "p/a", "v2", true, ctx+" after re-put")
				if got := scanKeys(t, db, "p/"); got["a"] != "v2" || got["b"] != "keep" {
					t.Fatalf("%s: scan after re-put = %v", ctx, got)
				}
				if err := db.Flush(); err != nil {
					t.Fatal(err)
				}
				mustGet(t, db, "p/a", "v2", true, ctx+" after final flush")
				if got := scanKeys(t, db, "p/"); got["a"] != "v2" || got["b"] != "keep" || len(got) != 2 {
					t.Fatalf("%s: final scan = %v", ctx, got)
				}
				_ = db.Close()
			}
		}
	}
}

func TestLazyDB_BatchDeleteAndBatchPutOfTheSameKey(t *testing.T) {
	for name, mk := range lazyBackends(t) {
		db := mk()
		if err := db.Open(1); err != nil {
			t.Fatal(err)
		}
		_ = db.BatchPut([][2][]byte{{[]byte("q/x"), []byte("1")}, {[]byte("q/y"), []byte("2")}})
		_ = db.Flush()
		_ = db.BatchDelete([][]byte{[]byte("q/x"), []byte("q/y")})
		mustGet(t, db, "q/x", "", false, name+" batch-deleted")
		_ = db.BatchPut([][2][]byte{{[]byte("q/y"), []byte("3")}})
		mustGet(t, db, "q/y", "3", true, name+" batch re-put")
		got := scanKeys(t, db, "q/")
		if len(got) != 1 || got["y"] != "3" {
			t.Fatalf("%s: scan = %v, want only y=3", name, got)
		}
		_ = db.Flush()
		mustGet(t, db, "q/x", "", false, name+" after flush")
		mustGet(t, db, "q/y", "3", true, name+" after flush")
		_ = db.Close()
	}
}

// An empty value is a real value, not a tombstone: a nil/empty Put must not make the key look deleted or vice versa.
func TestLazyDB_EmptyValueIsNotATombstone(t *testing.T) {
	for name, mk := range lazyBackends(t) {
		db := mk()
		if err := db.Open(1); err != nil {
			t.Fatal(err)
		}
		_ = db.Put([]byte("e/k"), nil)
		got, err := db.Get([]byte("e/k"))
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: Get after empty Put = %q, %v; want empty and no error", name, got, err)
		}
		if scan := scanKeys(t, db, "e/"); len(scan) != 1 {
			t.Fatalf("%s: scan after empty Put = %v, want the key present", name, scan)
		}
		_ = db.Close()
	}
}
