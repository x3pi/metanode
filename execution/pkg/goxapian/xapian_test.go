package goxapian

import (
	"os"
	"testing"
)

// 2026-09, Phuong an A mục 22 -- "check lại toàn bộ cgo của xapian": this
// package previously had zero test coverage. These tests exercise the two
// real fixes made in this pass: Commit()/ReplaceDocumentByTerm() actually
// reporting failure (rather than being unconditionally-successful `void`
// calls), and Close() being safe to call more than once concurrently
// (previously a check-then-act race on a bare pointer, now an atomic CAS).

func TestCommitAndReplaceDocumentByTermReportSuccess(t *testing.T) {
	dir, err := os.MkdirTemp("", "goxapian-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := NewWritableDatabase(dir)
	if err != nil {
		t.Fatalf("NewWritableDatabase: %v", err)
	}
	defer db.Close()

	doc := NewDocument()
	if doc == nil {
		t.Fatal("NewDocument returned nil")
	}
	defer doc.Close()
	doc.SetData("hello world")
	doc.AddTerm("Qunique1")

	if err := db.ReplaceDocumentByTerm("Qunique1", doc); err != nil {
		t.Fatalf("ReplaceDocumentByTerm on a healthy database should succeed, got: %v", err)
	}
	if err := db.Commit(); err != nil {
		t.Fatalf("Commit on a healthy database should succeed, got: %v", err)
	}
	if got := db.GetDocCount(); got != 1 {
		t.Fatalf("expected 1 document after commit, got %d", got)
	}
}

func TestOperationsOnClosedDatabaseFailCleanly(t *testing.T) {
	dir, err := os.MkdirTemp("", "goxapian-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := NewWritableDatabase(dir)
	if err != nil {
		t.Fatalf("NewWritableDatabase: %v", err)
	}
	db.Close()

	doc := NewDocument()
	defer doc.Close()

	if err := db.ReplaceDocumentByTerm("Qx", doc); err == nil {
		t.Fatal("ReplaceDocumentByTerm on a closed database should return an error, got nil")
	}
	if err := db.Commit(); err == nil {
		t.Fatal("Commit on a closed database should return an error, got nil")
	}
	if got := db.GetDocCount(); got != 0 {
		t.Fatalf("GetDocCount on a closed database should be 0, got %d", got)
	}
}

// TestConcurrentCloseIsSafe exercises the exact TOCTOU race this pass fixed:
// manual Close() calls racing each other (standing in for manual Close()
// racing the GC's runtime.SetFinalizer callback, which can't be triggered
// deterministically from a test). Before the atomic.CompareAndSwapInt32 fix,
// two goroutines could both observe a non-nil ptr and both call
// C.database_close on it -- a double-free. Run under `go test -race` for a
// stronger signal; without -race this at least proves no crash under
// concurrent Close() (a double-free here reliably segfaults the whole test
// binary rather than failing gracefully, so "the test process is still
// alive to report PASS" is itself part of what's being checked).
func TestConcurrentCloseIsSafe(t *testing.T) {
	dir, err := os.MkdirTemp("", "goxapian-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := NewWritableDatabase(dir)
	if err != nil {
		t.Fatalf("NewWritableDatabase: %v", err)
	}

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			db.Close()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	// If we get here without the process crashing (double-free), the fix
	// held. A further Close() must also stay a safe no-op.
	db.Close()
}
