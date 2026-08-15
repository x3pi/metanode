package mvm

import (
	"bytes"
	"testing"
)

// TestBlobHashAt_WithinRange verifies BLOBHASH resolves each committed
// versioned hash by index.
func TestBlobHashAt_WithinRange(t *testing.T) {
	a := &MVMApi{}
	h0 := []byte{0x01, 0xAA}
	h1 := []byte{0x01, 0xBB}
	a.SetBlobContext([][]byte{h0, h1}, nil)

	got, ok := a.blobHashAt(0)
	if !ok || !bytes.Equal(got, h0) {
		t.Fatalf("blobHashAt(0) = (%x, %v), want (%x, true)", got, ok, h0)
	}
	got, ok = a.blobHashAt(1)
	if !ok || !bytes.Equal(got, h1) {
		t.Fatalf("blobHashAt(1) = (%x, %v), want (%x, true)", got, ok, h1)
	}
}

// TestBlobHashAt_OutOfRange guards the EIP-4844 rule that BLOBHASH must
// resolve to 0 (ok=false here, mapped to a 0 push by the C++ side) for any
// index at or beyond the tx's blob count — never an error, never a stale/
// garbage value from a previous tx's context.
func TestBlobHashAt_OutOfRange(t *testing.T) {
	a := &MVMApi{}
	a.SetBlobContext([][]byte{{0x01, 0xAA}, {0x01, 0xBB}}, nil)

	for _, index := range []uint64{2, 3, 1000, 1 << 32} {
		got, ok := a.blobHashAt(index)
		if ok || got != nil {
			t.Fatalf("blobHashAt(%d) = (%x, %v), want (nil, false)", index, got, ok)
		}
	}
}

// TestBlobHashAt_NonBlobTxAlwaysOutOfRange guards the non-blob-tx case
// (SetBlobContext never called, or called with a nil slice): index 0 must
// still resolve to "out of range" (0), not panic on a nil slice.
func TestBlobHashAt_NonBlobTxAlwaysOutOfRange(t *testing.T) {
	a := &MVMApi{}
	got, ok := a.blobHashAt(0)
	if ok || got != nil {
		t.Fatalf("blobHashAt(0) on a fresh MVMApi = (%x, %v), want (nil, false)", got, ok)
	}

	a.SetBlobContext(nil, nil)
	got, ok = a.blobHashAt(0)
	if ok || got != nil {
		t.Fatalf("blobHashAt(0) after SetBlobContext(nil, nil) = (%x, %v), want (nil, false)", got, ok)
	}
}
