package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// blobTx builds a minimal blob tx (type 3) carrying n versioned hashes, with
// a distinct nonce so its Hash() is unique from any other tx built this way
// in the same test.
func blobTx(t *testing.T, nonce uint64, n int) types.Transaction {
	t.Helper()
	hashes := make([][]byte, n)
	for i := range hashes {
		hashes[i] = []byte{byte(i + 1)}
	}
	nonceBytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		nonceBytes[7-i] = byte(nonce >> (8 * i))
	}
	return transaction.TransactionFromProto(&pb.Transaction{
		Type:                3,
		Nonce:               nonceBytes,
		BlobVersionedHashes: hashes,
		MaxFeePerBlobGas:    big.NewInt(100).Bytes(),
	})
}

func TestBlobFeeOrReject_NonBlobTxIsFree(t *testing.T) {
	tx := transaction.TransactionFromProto(&pb.Transaction{Type: 2}) // EIP-1559, no blobs
	fee, err := blobFeeOrReject(tx, big.NewInt(1000), nil)
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
	fee, err := blobFeeOrReject(tx, blobBaseFee, nil)
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
	_, err := blobFeeOrReject(tx, big.NewInt(100), nil)
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
	_, err := blobFeeOrReject(tx, big.NewInt(100), nil)
	if err != nil {
		t.Fatalf("expected MaxFeePerBlobGas == blob base fee to be accepted, got: %v", err)
	}
}

func TestBlobFeeOrReject_RejectsWhenInBudgetRejectedSet(t *testing.T) {
	tx := blobTx(t, 0, 1)
	rejected := map[common.Hash]struct{}{tx.Hash(): {}}
	_, err := blobFeeOrReject(tx, big.NewInt(100), rejected)
	if err == nil {
		t.Fatalf("expected rejection for a tx in the blob-budget-rejected set")
	}
}

func TestComputeBlobBudgetRejections_UnderBudgetAcceptsAll(t *testing.T) {
	// MAX_BLOBS_PER_BLOCK == 6: three 2-blob txs = 6 total, exactly at budget.
	txs := []types.Transaction{blobTx(t, 0, 2), blobTx(t, 1, 2), blobTx(t, 2, 2)}
	rejected := computeBlobBudgetRejections(txs)
	if len(rejected) != 0 {
		t.Fatalf("expected no rejections at exactly the budget, got %d", len(rejected))
	}
}

func TestComputeBlobBudgetRejections_RejectsOnlyTheOverflowingTail(t *testing.T) {
	if mt_common.MAX_BLOBS_PER_BLOCK != 6 {
		t.Fatalf("test assumes MAX_BLOBS_PER_BLOCK == 6, got %d", mt_common.MAX_BLOBS_PER_BLOCK)
	}
	// 4 + 4 blobs = 8 > 6: the first tx (4 blobs) fits, the second (4 more,
	// would bring the running total to 8) does not and is rejected outright
	// (no partial acceptance of a single tx's blobs).
	first := blobTx(t, 0, 4)
	second := blobTx(t, 1, 4)
	rejected := computeBlobBudgetRejections([]types.Transaction{first, second})
	if _, over := rejected[first.Hash()]; over {
		t.Fatalf("first tx (4 blobs, within budget) should not be rejected")
	}
	if _, over := rejected[second.Hash()]; !over {
		t.Fatalf("second tx (would push total to 8 > 6) should be rejected")
	}
	if len(rejected) != 1 {
		t.Fatalf("expected exactly 1 rejection, got %d", len(rejected))
	}
}

func TestComputeBlobBudgetRejections_LaterInBudgetTxStillAcceptedAfterAnEarlierRejection(t *testing.T) {
	// 5 blobs (fits, leaves 1 remaining), 3 blobs (would push to 8, rejected),
	// 1 blob (5+1=6, still fits — a later tx can be accepted even after an
	// earlier one was rejected, since rejected txs don't consume budget).
	a := blobTx(t, 0, 5)
	b := blobTx(t, 1, 3)
	c := blobTx(t, 2, 1)
	rejected := computeBlobBudgetRejections([]types.Transaction{a, b, c})
	if _, over := rejected[a.Hash()]; over {
		t.Fatalf("tx a (5 blobs) should fit")
	}
	if _, over := rejected[b.Hash()]; !over {
		t.Fatalf("tx b (would push total to 8) should be rejected")
	}
	if _, over := rejected[c.Hash()]; over {
		t.Fatalf("tx c (5+1=6, still within budget) should not be rejected")
	}
}

func TestComputeBlobBudgetRejections_NonBlobTxsIgnored(t *testing.T) {
	regular := transaction.TransactionFromProto(&pb.Transaction{Type: 2})
	rejected := computeBlobBudgetRejections([]types.Transaction{regular, nil})
	if len(rejected) != 0 {
		t.Fatalf("expected no rejections for non-blob/nil txs, got %d", len(rejected))
	}
}
