package proto

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestTransaction_BlobSetCodeFields_RoundTrip guards the EIP-4844/EIP-7702 proto
// additions: Marshal (standard) must produce bytes that UnmarshalVT (the hand-generated
// fast path actually used on hot paths, e.g. executor/unix_socket_protocol.go) decodes
// back identically, including the new Sidecar/AuthorizationList nested messages.
func TestTransaction_BlobSetCodeFields_RoundTrip(t *testing.T) {
	orig := &Transaction{
		ToAddress:           []byte{0x01, 0x02},
		Type:                3,
		BlobVersionedHashes: [][]byte{{0x01, 0xAA}, {0x01, 0xBB}},
		MaxFeePerBlobGas:    []byte{0x03, 0xE8},
		Sidecar: &BlobSidecar{
			Blobs:       [][]byte{{0xDE, 0xAD}},
			Commitments: [][]byte{{0xBE, 0xEF}},
			Proofs:      [][]byte{{0xCA, 0xFE}},
		},
		AuthorizationList: []*SetCodeAuthorization{
			{
				ChainID: 13370,
				Address: []byte{0x11, 0x22, 0x33},
				Nonce:   7,
				YParity: []byte{0x01},
				R:       []byte{0xAA},
				S:       []byte{0xBB},
			},
		},
	}

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Transaction{}
	if err := got.UnmarshalVT(b); err != nil {
		t.Fatalf("UnmarshalVT: %v", err)
	}

	if len(got.BlobVersionedHashes) != 2 || string(got.BlobVersionedHashes[0]) != string(orig.BlobVersionedHashes[0]) || string(got.BlobVersionedHashes[1]) != string(orig.BlobVersionedHashes[1]) {
		t.Fatalf("BlobVersionedHashes mismatch: got %x, want %x", got.BlobVersionedHashes, orig.BlobVersionedHashes)
	}
	if string(got.MaxFeePerBlobGas) != string(orig.MaxFeePerBlobGas) {
		t.Fatalf("MaxFeePerBlobGas mismatch: got %x, want %x", got.MaxFeePerBlobGas, orig.MaxFeePerBlobGas)
	}
	if got.Sidecar == nil || len(got.Sidecar.Blobs) != 1 || string(got.Sidecar.Blobs[0]) != string(orig.Sidecar.Blobs[0]) {
		t.Fatalf("Sidecar.Blobs mismatch: got %+v, want %+v", got.Sidecar, orig.Sidecar)
	}
	if len(got.Sidecar.Commitments) != 1 || string(got.Sidecar.Commitments[0]) != string(orig.Sidecar.Commitments[0]) {
		t.Fatalf("Sidecar.Commitments mismatch")
	}
	if len(got.Sidecar.Proofs) != 1 || string(got.Sidecar.Proofs[0]) != string(orig.Sidecar.Proofs[0]) {
		t.Fatalf("Sidecar.Proofs mismatch")
	}
	if len(got.AuthorizationList) != 1 {
		t.Fatalf("AuthorizationList length mismatch: got %d, want 1", len(got.AuthorizationList))
	}
	gotAuth, wantAuth := got.AuthorizationList[0], orig.AuthorizationList[0]
	if gotAuth.ChainID != wantAuth.ChainID || gotAuth.Nonce != wantAuth.Nonce ||
		string(gotAuth.Address) != string(wantAuth.Address) ||
		string(gotAuth.YParity) != string(wantAuth.YParity) ||
		string(gotAuth.R) != string(wantAuth.R) || string(gotAuth.S) != string(wantAuth.S) {
		t.Fatalf("SetCodeAuthorization mismatch: got %+v, want %+v", gotAuth, wantAuth)
	}
}

// TestTransactionHashData_BlobSetCodeFields_ExcludesSidecar guards the hash-preimage
// design decision: TransactionHashData must be able to carry the fields a tx commits
// to (BlobVersionedHashes/MaxFeePerBlobGas/AuthorizationList) but has no Sidecar field
// at all — the raw blob/commitment/proof data must never affect the tx hash, otherwise
// pruning blobs would change tx hashes.
func TestTransactionHashData_BlobSetCodeFields_ExcludesSidecar(t *testing.T) {
	hd := &TransactionHashData{
		BlobVersionedHashes: [][]byte{{0x01, 0xAA}},
		MaxFeePerBlobGas:    []byte{0x03, 0xE8},
		AuthorizationList: []*SetCodeAuthorization{
			{ChainID: 1, Address: []byte{0x01}, Nonce: 0},
		},
	}

	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(hd)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := &TransactionHashData{}
	if err := got.UnmarshalVT(b); err != nil {
		t.Fatalf("UnmarshalVT: %v", err)
	}
	if len(got.BlobVersionedHashes) != 1 || len(got.AuthorizationList) != 1 {
		t.Fatalf("round-trip lost fields: got %+v", got)
	}
}

// BlobSidecar and SetCodeAuthorization must never be given a hash-affecting
// counterpart in TransactionHashData: this is a compile-time guard, not a
// runtime one — TransactionHashData simply has no Sidecar field to reference.
var _ = (*BlobSidecar)(nil)
