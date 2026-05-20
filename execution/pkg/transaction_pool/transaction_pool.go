package transaction_pool

import (
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

type addTxReq struct {
	tx    types.Transaction
	reply chan error
}

type getTxsReq struct {
	reply chan []types.Transaction
}

type countReq struct {
	reply chan int
}

type getTxReq struct {
	hash  common.Hash
	reply chan getTxResp
}

type getTxResp struct {
	tx types.Transaction
	ok bool
}

type TransactionPool struct {
	// Channels for Actor Pattern (lock-free)
	addTxCh       chan addTxReq
	addTxsCh      chan []types.Transaction
	getTxsCh      chan getTxsReq
	countCh       chan countReq
	getTxByHashCh chan getTxReq

	NotifyChan chan struct{} // GO-2: Event channel to notify workers of new transactions
}

func NewTransactionPool() *TransactionPool {
	tp := &TransactionPool{
		addTxCh:       make(chan addTxReq),
		addTxsCh:      make(chan []types.Transaction),
		getTxsCh:      make(chan getTxsReq),
		countCh:       make(chan countReq),
		getTxByHashCh: make(chan getTxReq),
		NotifyChan:    make(chan struct{}, 1),
	}

	go tp.loop()
	return tp
}

// notifyWork sends a non-blocking signal that new work is available (GO-2)
func (tp *TransactionPool) notifyWork() {
	select {
	case tp.NotifyChan <- struct{}{}:
	default: // Channel already has a pending signal, safe to drop
	}
}

// loop runs in a background goroutine and processes all requests sequentially,
// eliminating the need for any sync.Mutex locks and preventing contention.
func (tp *TransactionPool) loop() {
	transactions := make([]types.Transaction, 0)
	transactionKeys := make(map[string]bool)
	txHashMap := make(map[common.Hash]types.Transaction)

	for {
		select {
		case req := <-tp.addTxCh:
			key := req.tx.FromAddress().String() + "-" + strconv.FormatUint(req.tx.GetNonce(), 10)
			if transactionKeys[key] {
				logger.Info("Transaction already exists in pool, skipping", "key", key)
				req.reply <- fmt.Errorf("transaction already exists in pool, skipping")
				continue
			}

			// CROSS-CHAIN DEBUG logic (unchanged)
			if req.tx.ToAddress() == common.HexToAddress("0x00000000000000000000000000000000B429C0B2") {
				logger.Info("📥 [POOL-ARRIVE] Cross-chain TX entered mempool: hash=%s from=%s nonce=%d pool_size=%d",
					req.tx.Hash().Hex()[:16], req.tx.FromAddress().Hex()[:10], req.tx.GetNonce(), len(transactions)+1)
			}

			transactions = append(transactions, req.tx)
			transactionKeys[key] = true
			
			h := req.tx.Hash()
			if h != (common.Hash{}) {
				txHashMap[h] = req.tx
			}

			tp.notifyWork()
			req.reply <- nil

		case txs := <-tp.addTxsCh:
			addedAny := false
			for _, tx := range txs {
				key := tx.FromAddress().String() + "-" + strconv.FormatUint(tx.GetNonce(), 10)
				if !transactionKeys[key] {
					transactions = append(transactions, tx)
					transactionKeys[key] = true
					
					h := tx.Hash()
					if h != (common.Hash{}) {
						txHashMap[h] = tx
					}
					addedAny = true
				}
			}
			if addedAny {
				tp.notifyWork()
			}

		case req := <-tp.getTxsCh:
			// Copy transactions to avoid race condition when returned to caller
			txCopy := make([]types.Transaction, len(transactions))
			copy(txCopy, transactions)
			
			// Clear the internal state
			transactions = make([]types.Transaction, 0)
			transactionKeys = make(map[string]bool)
			txHashMap = make(map[common.Hash]types.Transaction)
			
			req.reply <- txCopy

		case req := <-tp.countCh:
			req.reply <- len(transactions)

		case req := <-tp.getTxByHashCh:
			if req.hash == (common.Hash{}) {
				logger.Warn("GetTransactionByHash called with a zero hash value.")
				req.reply <- getTxResp{tx: nil, ok: false}
				continue
			}
			
			tx, ok := txHashMap[req.hash]
			req.reply <- getTxResp{tx: tx, ok: ok}
		}
	}
}

func (tp *TransactionPool) CountTransactions() int {
	reply := make(chan int, 1)
	tp.countCh <- countReq{reply: reply}
	return <-reply
}

func (tp *TransactionPool) AddTransaction(tx types.Transaction) error {
	reply := make(chan error, 1)
	tp.addTxCh <- addTxReq{tx: tx, reply: reply}
	return <-reply
}

func (tp *TransactionPool) AddTransactions(txs []types.Transaction) {
	tp.addTxsCh <- txs
}

// TransactionsWithAggSign returns transactions and aggregate sign
// and clear transactions
func (tp *TransactionPool) TransactionsWithAggSign() ([]types.Transaction, []byte) {
	reply := make(chan []types.Transaction, 1)
	tp.getTxsCh <- getTxsReq{reply: reply}
	txs := <-reply
	
	// Preserving original behavior: aggregate sign returns nil
	return txs, nil
}

func (tp *TransactionPool) GetTransactionByHash(hashToFind common.Hash) (types.Transaction, bool) {
	reply := make(chan getTxResp, 1)
	tp.getTxByHashCh <- getTxReq{hash: hashToFind, reply: reply}
	resp := <-reply
	return resp.tx, resp.ok
}

