package goxapian

import (
	"fmt"
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

func TestIndexDocumentAddAndReplace(t *testing.T) {
	dir, err := os.MkdirTemp("", "goxapian-indexdoc-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := NewWritableDatabase(dir)
	if err != nil {
		t.Fatalf("NewWritableDatabase: %v", err)
	}
	defer db.Close()

	terms := "H0xabc\nF0x111\nT0x222\nB1\nRH0xdef"
	if err := db.IndexDocument("H0xabc", `{"hash":"0xabc","blockNumber":1}`, terms, false); err != nil {
		t.Fatalf("IndexDocument (add): %v", err)
	}
	if err := db.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := db.GetDocCount(); got != 1 {
		t.Fatalf("expected 1 document after add, got %d", got)
	}

	// Replace the same document by unique term
	if err := db.IndexDocument("H0xabc", `{"hash":"0xabc","blockNumber":2}`, terms, true); err != nil {
		t.Fatalf("IndexDocument (replace): %v", err)
	}
	if err := db.Commit(); err != nil {
		t.Fatalf("Commit after replace: %v", err)
	}
	if got := db.GetDocCount(); got != 1 {
		t.Fatalf("expected 1 document after replace, got %d", got)
	}
}

func TestIndexDocumentsBatchAddAndReplace(t *testing.T) {
	dir, err := os.MkdirTemp("", "goxapian-batch-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := NewWritableDatabase(dir)
	if err != nil {
		t.Fatalf("NewWritableDatabase: %v", err)
	}
	defer db.Close()

	// 1. Batch add 5 docs
	var docs []BatchDocumentInput
	for i := 1; i <= 5; i++ {
		docs = append(docs, BatchDocumentInput{
			UniqueTerm:      fmt.Sprintf("H0x%d", i),
			Data:            fmt.Sprintf(`{"hash":"0x%d","blockNumber":%d}`, i, i),
			TermsPacked:     fmt.Sprintf("H0x%d\nF0xaaa\nT0xbbb\nB%d", i, i),
			ReplaceExisting: false,
		})
	}

	if err := db.IndexDocumentsBatch(docs); err != nil {
		t.Fatalf("IndexDocumentsBatch (add): %v", err)
	}
	if err := db.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := db.GetDocCount(); got != 5 {
		t.Fatalf("expected 5 documents after batch add, got %d", got)
	}

	// 2. Batch replace doc 1 and add doc 6
	replaceDocs := []BatchDocumentInput{
		{
			UniqueTerm:      "H0x1",
			Data:            `{"hash":"0x1","blockNumber":99}`,
			TermsPacked:     "H0x1\nF0xaaa\nT0xbbb\nB99",
			ReplaceExisting: true,
		},
		{
			UniqueTerm:      "H0x6",
			Data:            `{"hash":"0x6","blockNumber":6}`,
			TermsPacked:     "H0x6\nF0xaaa\nT0xbbb\nB6",
			ReplaceExisting: false,
		},
	}
	if err := db.IndexDocumentsBatch(replaceDocs); err != nil {
		t.Fatalf("IndexDocumentsBatch (replace+add): %v", err)
	}
	if err := db.Commit(); err != nil {
		t.Fatalf("Commit after replace: %v", err)
	}
	if got := db.GetDocCount(); got != 6 {
		t.Fatalf("expected 6 documents, got %d", got)
	}
}


