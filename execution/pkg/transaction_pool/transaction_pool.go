package transaction_pool

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

const NumShards = 256

type txPoolKey struct {
	addr  common.Address
	nonce uint64
}

type poolShard struct {
	mu              sync.RWMutex
	transactions    []types.Transaction
	transactionKeys map[txPoolKey]bool
	txHashMap       map[common.Hash]types.Transaction
}

func newPoolShard() *poolShard {
	return &poolShard{
		transactions:    make([]types.Transaction, 0),
		transactionKeys: make(map[txPoolKey]bool),
		txHashMap:       make(map[common.Hash]types.Transaction),
	}
}

type TransactionPool struct {
	shards     [NumShards]*poolShard
	NotifyChan chan struct{} // GO-2: Event channel to notify workers of new transactions
	count      int64         // Atomic counter for lock-free count queries
}

func NewTransactionPool() *TransactionPool {
	tp := &TransactionPool{
		NotifyChan: make(chan struct{}, 1),
	}
	for i := 0; i < NumShards; i++ {
		tp.shards[i] = newPoolShard()
	}
	return tp
}

// notifyWork sends a non-blocking signal that new work is available (GO-2)
func (tp *TransactionPool) notifyWork() {
	select {
	case tp.NotifyChan <- struct{}{}:
	default: // Channel already has a pending signal, safe to drop
	}
}

func (tp *TransactionPool) getShardIndex(addr common.Address) uint8 {
	return addr[0] // 0-255 based on the first byte of the address
}

func (tp *TransactionPool) CountTransactions() int {
	return int(atomic.LoadInt64(&tp.count))
}

func (tp *TransactionPool) EvictLowestGasPrice(countToEvict int) int {
	if countToEvict <= 0 {
		return 0
	}

	type txInfo struct {
		shardIdx int
		index    int
		gas      uint64
		hash     common.Hash
	}

	var allInfos []txInfo

	for i := 0; i < NumShards; i++ {
		shard := tp.shards[i]
		shard.mu.RLock()
		for idx, tx := range shard.transactions {
			allInfos = append(allInfos, txInfo{
				shardIdx: i,
				index:    idx,
				gas:      tx.MaxGasPrice(),
				hash:     tx.Hash(),
			})
		}
		shard.mu.RUnlock()
	}

	if len(allInfos) <= countToEvict {
		// Evict all
		var totalEvicted int
		for i := 0; i < NumShards; i++ {
			shard := tp.shards[i]
			shard.mu.Lock()
			totalEvicted += len(shard.transactions)
			shard.transactions = make([]types.Transaction, 0)
			shard.transactionKeys = make(map[txPoolKey]bool)
			shard.txHashMap = make(map[common.Hash]types.Transaction)
			shard.mu.Unlock()
		}
		atomic.StoreInt64(&tp.count, 0)
		return totalEvicted
	}

	// Sort by GasPrice ascending
	sort.Slice(allInfos, func(i, j int) bool {
		return allInfos[i].gas < allInfos[j].gas
	})

	// Group removals by shard
	toRemoveByShard := make(map[int]map[common.Hash]bool)
	for i := 0; i < countToEvict; i++ {
		info := allInfos[i]
		if toRemoveByShard[info.shardIdx] == nil {
			toRemoveByShard[info.shardIdx] = make(map[common.Hash]bool)
		}
		toRemoveByShard[info.shardIdx][info.hash] = true
	}

	var totalEvicted int
	for i, hashesToRemove := range toRemoveByShard {
		shard := tp.shards[i]
		shard.mu.Lock()

		newTxs := make([]types.Transaction, 0, len(shard.transactions)-len(hashesToRemove))
		for _, tx := range shard.transactions {
			h := tx.Hash()
			if hashesToRemove[h] {
				key := txPoolKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
				delete(shard.transactionKeys, key)
				delete(shard.txHashMap, h)
				totalEvicted++
			} else {
				newTxs = append(newTxs, tx)
			}
		}
		shard.transactions = newTxs
		shard.mu.Unlock()
	}

	atomic.AddInt64(&tp.count, int64(-totalEvicted))
	return totalEvicted
}

func (tp *TransactionPool) AddTransaction(tx types.Transaction) error {
	shardIdx := tp.getShardIndex(tx.FromAddress())
	shard := tp.shards[shardIdx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	key := txPoolKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
	if shard.transactionKeys[key] {
		logger.Info("Transaction already exists in pool, skipping key addr=%s nonce=%d", key.addr.Hex(), key.nonce)
		return fmt.Errorf("transaction already exists in pool, skipping")
	}

	// CROSS-CHAIN DEBUG logic (unchanged)
	if tx.ToAddress() == common.HexToAddress("0x00000000000000000000000000000000B429C0B2") {
		logger.Info("📥 [POOL-ARRIVE] Cross-chain TX entered mempool: hash=%s from=%s nonce=%d pool_size=%d",
			tx.Hash().Hex()[:16], tx.FromAddress().Hex()[:10], tx.GetNonce(), atomic.LoadInt64(&tp.count)+1)
	}

	shard.transactions = append(shard.transactions, tx)
	shard.transactionKeys[key] = true

	h := tx.Hash()
	if h != (common.Hash{}) {
		shard.txHashMap[h] = tx
	}

	atomic.AddInt64(&tp.count, 1)
	tp.notifyWork()
	return nil
}

func (tp *TransactionPool) AddTransactions(txs []types.Transaction) {
	// Group transactions by shard to minimize lock contention
	txsByShard := make(map[uint8][]types.Transaction)
	for _, tx := range txs {
		idx := tp.getShardIndex(tx.FromAddress())
		txsByShard[idx] = append(txsByShard[idx], tx)
	}

	addedAny := false
	var totalAdded int64

	for idx, shardTxs := range txsByShard {
		shard := tp.shards[idx]
		shard.mu.Lock()

		for _, tx := range shardTxs {
			key := txPoolKey{addr: tx.FromAddress(), nonce: tx.GetNonce()}
			if !shard.transactionKeys[key] {
				shard.transactions = append(shard.transactions, tx)
				shard.transactionKeys[key] = true

				h := tx.Hash()
				if h != (common.Hash{}) {
					shard.txHashMap[h] = tx
				}
				totalAdded++
				addedAny = true
			}
		}
		shard.mu.Unlock()
	}

	if addedAny {
		atomic.AddInt64(&tp.count, totalAdded)
		tp.notifyWork()
	}
}

// TransactionsWithAggSign returns transactions and aggregate sign
// and clear transactions
func (tp *TransactionPool) TransactionsWithAggSign() ([]types.Transaction, []byte) {
	var allTxs []types.Transaction

	for i := 0; i < NumShards; i++ {
		shard := tp.shards[i]
		shard.mu.Lock()
		if len(shard.transactions) > 0 {
			allTxs = append(allTxs, shard.transactions...)
			shard.transactions = make([]types.Transaction, 0)
			shard.transactionKeys = make(map[txPoolKey]bool)
			shard.txHashMap = make(map[common.Hash]types.Transaction)
		}
		shard.mu.Unlock()
	}

	atomic.StoreInt64(&tp.count, 0)

	// Preserving original behavior: aggregate sign returns nil
	return allTxs, nil
}

func (tp *TransactionPool) GetTransactionByHash(hashToFind common.Hash) (types.Transaction, bool) {
	if hashToFind == (common.Hash{}) {
		logger.Warn("GetTransactionByHash called with a zero hash value.")
		return nil, false
	}

	for i := 0; i < NumShards; i++ {
		shard := tp.shards[i]
		shard.mu.RLock()
		tx, ok := shard.txHashMap[hashToFind]
		shard.mu.RUnlock()
		if ok {
			return tx, true
		}
	}

	return nil, false
}
