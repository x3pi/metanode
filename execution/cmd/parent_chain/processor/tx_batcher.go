package processor

import (
	"log"
	"time"

	"github.com/meta-node-blockchain/meta-node/executor"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

const maxTxPerBatch = 2000

// TxBatcher collects signed transactions accepted by the RPC ingress, batches them, and submits them to the
// Rust consensus core. Ordering and execution happen after consensus, identically on every validator.
type TxBatcher struct {
	txChan chan *pb.Transaction
	stop   chan struct{}
}

// NewTxBatcher creates a batcher whose ingress queue holds at most queueSize transactions.
func NewTxBatcher(queueSize int) *TxBatcher {
	return &TxBatcher{
		txChan: make(chan *pb.Transaction, queueSize),
		stop:   make(chan struct{}),
	}
}

// Chan returns the bounded channel the HTTP server pushes accepted transactions into.
func (tb *TxBatcher) Chan() chan *pb.Transaction {
	return tb.txChan
}

func (tb *TxBatcher) Start() {
	go tb.batchingLoop()
}

func (tb *TxBatcher) Stop() {
	close(tb.stop)
}

func (tb *TxBatcher) batchingLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var pending []*pb.Transaction
	flush := func() {
		if len(pending) > 0 {
			tb.submitBatch(pending)
			pending = nil
		}
	}

	for {
		select {
		case <-tb.stop:
			return
		case tx := <-tb.txChan:
			if tx != nil {
				pending = append(pending, tx)
			}
		drain:
			for len(pending) < maxTxPerBatch {
				select {
				case extra := <-tb.txChan:
					if extra != nil {
						pending = append(pending, extra)
					}
				default:
					break drain
				}
			}
			flush()
		case <-ticker.C:
			flush()
		}
	}
}

func (tb *TxBatcher) submitBatch(txs []*pb.Transaction) {
	var batch pb.Transactions
	batch.Transactions = txs

	batchBytes, err := proto.Marshal(&batch)
	if err != nil {
		log.Printf("Failed to marshal pb.Transactions: %v", err)
		return
	}

	// Rust returns false when its bounded FFI channel is full: keep the batch and retry (never drop an accepted
	// tx). While we retry the bounded ingress queue fills and the RPC answers 503, which is the backpressure.
	for !executor.SubmitTransactionBatch(batchBytes) {
		select {
		case <-tb.stop:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
