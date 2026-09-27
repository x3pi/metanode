package processor

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

const maxTxPerBatch = 2000

// TxBatcher collects ParentChainTxs from RPC, batches them, and submits them to Rust consensus.
type TxBatcher struct {
	txChan chan *parentchain.ParentChainTx
	stop   chan struct{}
}

func NewTxBatcher(queueSize int) *TxBatcher {
	return &TxBatcher{
		txChan: make(chan *parentchain.ParentChainTx, queueSize),
		stop:   make(chan struct{}),
	}
}

func (tb *TxBatcher) SubmitTx(tx *parentchain.ParentChainTx) error {
	select {
	case tb.txChan <- tx:
		return nil
	default:
		return fmt.Errorf("parent chain tx queue is full")
	}
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

	var pending []*parentchain.ParentChainTx

	for {
		select {
		case <-tb.stop:
			return
		case tx := <-tb.txChan:
			pending = append(pending, tx)
			if len(pending) >= maxTxPerBatch {
				tb.submitBatch(pending)
				pending = nil
			}
		case <-ticker.C:
			if len(pending) > 0 {
				tb.submitBatch(pending)
				pending = nil
			}
		}
	}
}

func (tb *TxBatcher) submitBatch(txs []*parentchain.ParentChainTx) {
	if len(txs) == 0 {
		return
	}

	// We wrap the marshaled ParentChainTxs into pb.Transactions so that Rust consensus
	// (tx_socket_server.rs) can parse it properly using prost::encoding::decode_varint.
	var batch pb.Transactions
	for _, tx := range txs {
		raw, err := json.Marshal(tx)
		if err != nil {
			log.Printf("Failed to marshal ParentChainTx: %v", err)
			continue
		}
		// In Metanode, pb.Transactions is a list of raw bytes in protobuf encoding.
		// Actually, pb.Transactions is repeated pb.Transaction. We need to create a pb.Transaction
		// or just append to Transactions. Let's look at how MarshalTransactions works.
		// To match transaction.MarshalTransactions, we wrap it in pb.Transaction.
		batch.Transactions = append(batch.Transactions, &pb.Transaction{
			Data: raw, // We put the raw JSON in Data field, which Rust consensus extracts
		})
	}

	if len(batch.Transactions) == 0 {
		return
	}

	batchBytes, err := proto.Marshal(&batch)
	if err != nil {
		log.Printf("Failed to marshal pb.Transactions: %v", err)
		return
	}

	// Submit to Rust consensus core
	success := executor.SubmitTransactionBatch(batchBytes)
	if !success {
		log.Printf("Warning: SubmitTransactionBatch returned false for %d txs", len(batch.Transactions))
		// In a real implementation we might requeue, but for Parent Chain we just log.
		// Bounded wait logic in Rust handles retry.
	}
}
