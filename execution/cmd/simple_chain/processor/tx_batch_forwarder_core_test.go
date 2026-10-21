package processor

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func TestForwardBatchesRespectEncodedLimitAndPreserveOrder(t *testing.T) {
	txs := []types.Transaction{makeTestTx(1, 1), makeTestTx(1, 2), makeTestTx(1, 3), makeTestTx(1, 4), makeTestTx(1, 5)}
	one, err := transaction.MarshalTransactions(txs[:1])
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{len(one), 2 * len(one), 0} {
		batches := splitForwardBatches(txs, 2, limit)
		seen := 0
		for _, batch := range batches {
			encoded, err := transaction.MarshalTransactions(batch)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch) > 2 || (limit > 0 && len(encoded) > limit) {
				t.Fatalf("batch count=%d bytes=%d limit=%d", len(batch), len(encoded), limit)
			}
			for _, tx := range batch {
				if tx.Hash() != txs[seen].Hash() {
					t.Fatal("input order changed")
				}
				seen++
			}
		}
		if seen != len(txs) {
			t.Fatalf("retained %d of %d transactions", seen, len(txs))
		}
	}
}

type forwardOversizeTx struct{ types.Transaction }

func (tx forwardOversizeTx) Marshal() ([]byte, error) { return make([]byte, 1024), nil }

func TestForwardBatchesIsolateOversizeInsteadOfDroppingNeighbours(t *testing.T) {
	txs := []types.Transaction{makeTestTx(1, 1), forwardOversizeTx{makeTestTx(1, 2)}, makeTestTx(1, 3)}
	one, err := transaction.MarshalTransactions(txs[:1])
	if err != nil {
		t.Fatal(err)
	}
	batches := splitForwardBatches(txs, 2000, len(one))
	if len(batches) != 3 {
		t.Fatalf("want three isolated batches, got %d", len(batches))
	}
	for i, batch := range batches {
		if len(batch) != 1 || batch[0].Hash() != txs[i].Hash() {
			t.Fatalf("input %d lost or reordered", i)
		}
	}
}
