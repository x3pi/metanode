package transaction

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func u256(v uint64) *uint256.Int { return uint256.NewInt(v) }

// makeSignedProtoTxWithV builds a minimal proto Transaction carrying a fixed,
// nonzero R/S and the given V value (0 included, to exercise the empty-bytes case).
func makeSignedProtoTxWithV(t *testing.T, v int64) *pb.Transaction {
	t.Helper()
	return &pb.Transaction{
		R: big.NewInt(12345).Bytes(),
		S: big.NewInt(67890).Bytes(),
		V: big.NewInt(v).Bytes(),
	}
}

// TestExtractSignature_ZeroVIsNotAbsent guards a real bug: extractSignature used
// to treat a recovery-id V of 0 as "no signature" (big.Int.Bytes() elides leading
// zero bytes, so V==0 encodes to an empty slice, indistinguishable from "unset").
// EIP-2930/1559/4844 signers produce a 0-or-1 recovery-id V, so this silently
// dropped the ENTIRE signature (R and S too, since callers gate on all three
// being non-nil) for roughly half of all such transactions — reconstructing them
// with a zero signature instead, which changes the tx hash and breaks sender
// recovery. Confirmed live: a blob tx landed on 3 different nodes with 3 different
// derived eth-hashes depending on which node happened to reconstruct with V==0.
func TestExtractSignature_ZeroVIsNotAbsent(t *testing.T) {
	pTx := makeSignedProtoTxWithV(t, 0)
	v, r, s := extractSignature(pTx)
	if r == nil || s == nil {
		t.Fatalf("R/S must be present regardless of V's value: r=%v s=%v", r, s)
	}
	if v == nil {
		t.Fatalf("V must not be nil when R/S are present, even if V's value is 0")
	}
	if v.Sign() != 0 {
		t.Fatalf("expected V=0, got %v", v)
	}
}

func TestExtractSignature_OneVStillWorks(t *testing.T) {
	pTx := makeSignedProtoTxWithV(t, 1)
	v, r, s := extractSignature(pTx)
	if r == nil || s == nil || v == nil {
		t.Fatalf("expected all of V/R/S present: v=%v r=%v s=%v", v, r, s)
	}
	if v.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("expected V=1, got %v", v)
	}
}

func TestExtractSignature_TrulyUnsignedStaysNil(t *testing.T) {
	v, r, s := extractSignature(&pb.Transaction{})
	if v != nil || r != nil || s != nil {
		t.Fatalf("expected all nil for a genuinely unsigned tx: v=%v r=%v s=%v", v, r, s)
	}
}

// TestToEthBlobTx_RoundTripsHashRegardlessOfRecoveryID is the end-to-end version
// of the bug above, exercised through the actual EIP-4844 conversion path: signs
// blob txs until both V=0 and V=1 have been observed, and for each one checks
// that reconstructing via NewTransactionFromEth -> (sidecar stripped, exactly as
// the ingestion boundary does before propagation) -> EthHash() reproduces the
// original signed tx's hash.
func TestToEthBlobTx_RoundTripsHashRegardlessOfRecoveryID(t *testing.T) {
	blob, commitment, proof, vh := makeValidBlobSidecar(t)

	seenV := map[int64]bool{}
	for attempt := 0; attempt < 200 && (!seenV[0] || !seenV[1]); attempt++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		to := common.HexToAddress("0x00000000000000000000000000000000001234")
		innerTx := &types.BlobTx{
			ChainID: u256(13370), Nonce: uint64(attempt),
			GasTipCap: u256(1), GasFeeCap: u256(1), Gas: 21000,
			To: to, Value: u256(1), BlobFeeCap: u256(1),
			BlobHashes: []common.Hash{vh},
			Sidecar: &types.BlobTxSidecar{
				Blobs:       []kzg4844.Blob{blob},
				Commitments: []kzg4844.Commitment{commitment},
				Proofs:      []kzg4844.Proof{proof},
			},
		}
		signer := types.NewCancunSigner(big.NewInt(13370))
		ethTx, err := types.SignNewTx(key, signer, innerTx)
		if err != nil {
			t.Fatalf("SignNewTx: %v", err)
		}
		v, _, _ := ethTx.RawSignatureValues()
		if v.Cmp(big.NewInt(0)) != 0 && v.Cmp(big.NewInt(1)) != 0 {
			continue // defensive: only 0/1 expected for a recovery-id V
		}
		seenV[v.Int64()] = true

		originalHash := ethTx.Hash()
		txIface, err := NewTransactionFromEth(ethTx)
		if err != nil {
			t.Fatalf("NewTransactionFromEth: %v", err)
		}
		tx := txIface.(*Transaction)
		tx.proto.Sidecar = nil // exactly what the ingestion boundary does before propagation

		if got := tx.EthHash(); got != originalHash {
			t.Fatalf("V=%v: EthHash after sidecar strip = %s, want %s", v, got.Hex(), originalHash.Hex())
		}
	}
	if !seenV[0] || !seenV[1] {
		t.Fatalf("test did not observe both V=0 and V=1 within attempt budget (seen: %v)", seenV)
	}
}
