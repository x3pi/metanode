package tx_processor

import (
	"math/big"
	"testing"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

func TestBlobFeeOrReject_NonBlobTxIsFree(t *testing.T) {
	tx := transaction.TransactionFromProto(&pb.Transaction{Type: 2}) // EIP-1559, no blobs
	fee, err := blobFeeOrReject(tx, big.NewInt(1000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fee.Sign() != 0 {
		t.Fatalf("expected zero fee for a non-blob tx, got %s", fee)
	}
}

func TestBlobFeeOrReject_ChargesBlobGasPerBlobTimesBaseFee(t *testing.T) {
	tx := transaction.TransactionFromProto(&pb.Transaction{
		Type:                3,
		BlobVersionedHashes: [][]byte{{0x01}, {0x02}}, // 2 blobs
		MaxFeePerBlobGas:    big.NewInt(500).Bytes(),
	})
	blobBaseFee := big.NewInt(100)
	fee, err := blobFeeOrReject(tx, blobBaseFee)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 blobs * 131072 gas/blob * 100 = 26214400
	want := big.NewInt(2 * 131072 * 100)
	if fee.Cmp(want) != 0 {
		t.Fatalf("fee = %s, want %s", fee, want)
	}
}

func TestBlobFeeOrReject_RejectsUnderpricedMaxFeePerBlobGas(t *testing.T) {
	tx := transaction.TransactionFromProto(&pb.Transaction{
		Type:                3,
		BlobVersionedHashes: [][]byte{{0x01}},
		MaxFeePerBlobGas:    big.NewInt(50).Bytes(),
	})
	_, err := blobFeeOrReject(tx, big.NewInt(100))
	if err == nil {
		t.Fatalf("expected rejection when MaxFeePerBlobGas (50) < blob base fee (100)")
	}
}

func TestBlobFeeOrReject_ExactlyAtBaseFeeIsAccepted(t *testing.T) {
	tx := transaction.TransactionFromProto(&pb.Transaction{
		Type:                3,
		BlobVersionedHashes: [][]byte{{0x01}},
		MaxFeePerBlobGas:    big.NewInt(100).Bytes(),
	})
	_, err := blobFeeOrReject(tx, big.NewInt(100))
	if err != nil {
		t.Fatalf("expected MaxFeePerBlobGas == blob base fee to be accepted, got: %v", err)
	}
}
