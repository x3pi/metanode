package transaction_state_db

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	// Import types
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	lastTransactionStateRootHashKey common.Hash = common.BytesToHash(crypto.Keccak256([]byte("lastTransactionStateHashKey")))
)

type TransactionStateDB struct {
	trie              p_trie.StateTrie
	originRootHash    common.Hash
	db                storage.Storage
	dirtyTransactions map[common.Hash]types.Transaction
	txBatchPut        []byte
}

func NewTransactionStateDB(
	trie p_trie.StateTrie,
	db storage.Storage,
) *TransactionStateDB {
	return &TransactionStateDB{
		trie:              trie,
		db:                db,
		originRootHash:    trie.Hash(),
		dirtyTransactions: make(map[common.Hash]types.Transaction),
	}
}

func (txdb *TransactionStateDB) SetTxBatchPut(batch []byte) {
	txdb.txBatchPut = batch
}

func (txdb *TransactionStateDB) GetTxBatchPut() []byte {
	batch := txdb.txBatchPut
	txdb.txBatchPut = nil
	return batch
}
func NewTransactionStateDBFromRoot(
	rootHash common.Hash,
	db storage.Storage,
) (*TransactionStateDB, error) {
	trie, err := p_trie.NewStateTrie(rootHash, db, true)
	if err != nil {
		return nil, err
	}

	return &TransactionStateDB{
		trie:              trie,
		db:                db,
		originRootHash:    rootHash,
		dirtyTransactions: make(map[common.Hash]types.Transaction),
	}, nil
}

// NewTransactionStateDBFromLastRoot retrieves the last transaction state root hash from the database
// and creates a new TransactionStateDB from that root hash.
func NewTransactionStateDBFromLastRoot(db storage.Storage) (*TransactionStateDB, error) {

	rootHash := common.Hash{}
	trie, err := p_trie.NewStateTrie(rootHash, db, true)
	if err != nil {
		return nil, err
	}

	return &TransactionStateDB{
		trie:              trie,
		db:                db,
		originRootHash:    rootHash,
		dirtyTransactions: make(map[common.Hash]types.Transaction),
	}, nil
}
func NewTransactionStateDBFromSpecificRoot(
	rootHash common.Hash,
	db storage.Storage,
) (*TransactionStateDB, error) {
	trie, err := p_trie.NewStateTrie(rootHash, db, true)
	if err != nil {
		return nil, err
	}

	return &TransactionStateDB{
		trie:              trie,
		db:                db,
		originRootHash:    rootHash,
		dirtyTransactions: make(map[common.Hash]types.Transaction),
	}, nil
}

func (db *TransactionStateDB) GetAll() (map[common.Hash]types.Transaction, error) {
	allTransactions := make(map[common.Hash]types.Transaction)
	allData, err := db.trie.GetAll()
	if err != nil {
		return nil, err
	}
	for hashStr, transactionBytes := range allData {
		hash := common.HexToHash(hashStr)
		transaction := &transaction.Transaction{} // Bạn cần implement hàm này để tạo transaction phù hợp
		err := transaction.Unmarshal(transactionBytes)
		if err != nil {
			return nil, err
		}
		allTransactions[hash] = transaction
	}
	return allTransactions, nil
}

// ReloadLastRoot reloads the last transaction state root hash from the database and updates the TransactionStateDB.
func (db *TransactionStateDB) ReloadLastRoot(rootHash common.Hash) error {

	newTrie, err := p_trie.NewStateTrie(rootHash, db.db, true)
	if err != nil {
		return err
	}

	db.trie = newTrie
	db.originRootHash = rootHash
	db.dirtyTransactions = make(map[common.Hash]types.Transaction) // Reset dirty transactions

	return nil
}

func (db *TransactionStateDB) ReturnDB() storage.Storage {
	return db.db
}

func (db *TransactionStateDB) GetTransaction(hash common.Hash) (types.Transaction, error) {
	tx, ok := db.dirtyTransactions[hash]
	if ok {
		return tx, nil // Trả về con trỏ thay vì giá trị trực tiếp
	}

	// if not exist in dirty then get from trie
	bData, _ := db.trie.Get(hash.Bytes())
	if len(bData) == 0 {
		logger.Error("TransactionStateDB GetTransaction transaction not found", hash)
		return nil, errors.New("TransactionStateDB GetTransaction transaction not found") // Trả về nil hợp lệ cho con trỏ
	}

	// exist in trie, unmarshal
	txData := &transaction.Transaction{} // Bạn cần implement hàm này để tạo transaction phù hợp

	err := txData.Unmarshal(bData)

	if err != nil {
		logger.Error("err: ", err)
		return nil, err
	}

	return txData, nil // Trả về con trỏ đến struct
}

func (db *TransactionStateDB) SetTransaction(tx types.Transaction) {
	db.setDirtyTransaction(tx)

}

func (db *TransactionStateDB) AddTransactions(txs []types.Transaction) {
	if len(db.dirtyTransactions) == 0 && len(txs) > 0 {
		db.dirtyTransactions = make(map[common.Hash]types.Transaction, len(txs))
	}
	for _, tx := range txs {
		db.setDirtyTransaction(tx)
	}
}

// Commit là phiên bản đã được tối ưu hóa và sửa lỗi.
// Nó xử lý các giao dịch 'dirty' nếu có, sau đó commit trạng thái hiện tại của trie vào DB.
// Hàm này hoạt động đúng ngay cả khi IntermediateRoot() đã được gọi trước đó.
func (db *TransactionStateDB) Commit() (common.Hash, error) {
	totalTimeStart := time.Now()

	// Giai đoạn 1: Xử lý các giao dịch 'dirty' (nếu có)
	// Đây là phần tối ưu hóa chính, giúp song song hóa việc marshal.
	if len(db.dirtyTransactions) > 0 {
		marshalTimeStart := time.Now()
		logger.Debug(fmt.Sprintf("Commit: Found %d dirty transactions. Processing...", len(db.dirtyTransactions)))

		type marshalResult struct {
			hash common.Hash
			data []byte
			err  error
		}

		numJobs := len(db.dirtyTransactions)
		jobs := make(chan types.Transaction, numJobs)
		results := make(chan marshalResult, numJobs)

		numWorkers := runtime.NumCPU()
		var wg sync.WaitGroup

		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for tx := range jobs {
					b, err := tx.Marshal()
					results <- marshalResult{hash: tx.Hash(), data: b, err: err}
				}
			}()
		}

		for _, tx := range db.dirtyTransactions {
			jobs <- tx
		}
		close(jobs)

		wg.Wait()
		close(results)

		// Thu thập kết quả và cập nhật trie
		batchKeys := make([][]byte, 0, numJobs)
		batchValues := make([][]byte, 0, numJobs)

		for res := range results {
			if res.err != nil {
				return common.Hash{}, fmt.Errorf("failed to marshal transaction %s: %w", res.hash.Hex(), res.err)
			}
			
			// FORK-SAFETY & PERF: Loại bỏ db.trie.Get tuần tự.
			// Mọi hash giao dịch là duy nhất hoặc Update nội dung giống hệt
			// là idempotent (không đổi Trie Root hash).
			batchKeys = append(batchKeys, res.hash.Bytes())
			batchValues = append(batchValues, res.data)
		}

		if len(batchKeys) > 0 {
			if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
				oldValues := make([][]byte, len(batchKeys))
				if err := nomtTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, oldValues); err != nil {
					return common.Hash{}, fmt.Errorf("failed to batch update nomt trie: %w", err)
				}
			} else {
				if err := db.trie.BatchUpdate(batchKeys, batchValues); err != nil {
					return common.Hash{}, fmt.Errorf("failed to batch update trie: %w", err)
				}
			}
		}
		// Sau khi đã cập nhật trie, xóa danh sách dirty
		db.dirtyTransactions = make(map[common.Hash]types.Transaction)
		logger.Info("✅ [txDB Phase 1] Marshalling & Trie BatchUpdate completed in %v", time.Since(marshalTimeStart))
	} else {
		logger.Debug("Commit: No dirty transactions to process. Proceeding to commit current trie state.")
	}

	// Giai đoạn 2: Commit trie và chuẩn bị batch ghi vào DB
	// Giai đoạn này luôn chạy để đảm bảo trạng thái trie trong bộ nhớ được ghi xuống DB.
	trieCommitTimeStart := time.Now()
	hash, nodeSet, _, err := db.trie.Commit(true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to commit trie: %w", err)
	}

	// Commit NOMT payload since TransactionStateDB commits sequentially
	if nomtTrie, isNomt := db.trie.(*p_trie.NomtStateTrie); isNomt {
		if err := nomtTrie.CommitPayload(); err != nil {
			return common.Hash{}, fmt.Errorf("failed to commit NOMT payload: %w", err)
		}
	}

	// Build batch from trie nodes (MPT) — for FlatStateTrie, nodeSet is nil
	batch := [][2][]byte{}
	if nodeSet != nil {
		for _, node := range nodeSet.Nodes {
			batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
		}
	}
	batch = append(batch, [2][]byte{lastTransactionStateRootHashKey.Bytes(), hash.Bytes()})
	logger.Info("✅ [txDB Phase 2] Trie Commit (FFI) & Batch Prep completed in %v. Root hash: %s", time.Since(trieCommitTimeStart), hash.Hex())

	// FlatStateTrie/VerkleStateTrie FIX: Retrieve flat entries for replication to Sub nodes.
	// Trie.Commit() writes flat entries to local DB async and returns nil NodeSet.
	// Without this, TxBatchPut only contains the root hash key → Sub nodes can't find transactions.
	var flatBatch [][2][]byte
	if flatTrie, ok := db.trie.(*p_trie.FlatStateTrie); ok {
		flatBatch = flatTrie.GetCommitBatch()
	} else if verkleTrie, ok := db.trie.(*p_trie.VerkleStateTrie); ok {
		flatBatch = verkleTrie.GetCommitBatch()
	} else if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
		flatBatch = nomtTrie.GetCommitBatch()
	}

	// Giai đoạn 3: Ghi DB và tuần tự hóa batch
	dbWriteTimeStart := time.Now()
	if len(batch) > 0 {
		if err := db.db.BatchPut(batch); err != nil {
			return common.Hash{}, fmt.Errorf("failed to batch put to db: %w", err)
		}
		if config.ConfigApp != nil && config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
			serStart := time.Now()
			data, serErr := storage.SerializeBatch(flatBatch)
			if serErr != nil {
				return common.Hash{}, fmt.Errorf("failed to serialize flat batch: %w", serErr)
			}
			logger.Info("✅ [txDB Phase 3] SerializeBatch (%d entries) completed in %v", len(flatBatch), time.Since(serStart))
			db.SetTxBatchPut(data)
		}
	}
	logger.Info("✅ [Phase 3] DB BatchPut & Serialize completed in %v", time.Since(dbWriteTimeStart))

	// Giai đoạn 4: Dọn dẹp và Reset
	// Reset trie về trạng thái rỗng để chuẩn bị cho block tiếp theo, giống logic của hàm gốc.
	cleanupTimeStart := time.Now()
	db.trie, err = p_trie.NewStateTrie(trie.EmptyRootHash, db.db, true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to reset trie to empty state: %w", err)
	}
	db.originRootHash = trie.EmptyRootHash
	logger.Debug(fmt.Sprintf("✅ [Phase 4] Cleanup and Reset completed in %v", time.Since(cleanupTimeStart)))

	logger.Debug(fmt.Sprintf("🚀 Total Commit execution time: %v", time.Since(totalTimeStart)))
	return hash, nil
}

func (db *TransactionStateDB) IntermediateRoot() (common.Hash, error) {
	numDirty := len(db.dirtyTransactions)
	if numDirty == 0 {
		return db.trie.Hash(), nil
	}

	// PERF OPTIMIZATION: Parallelize Marshal (CPU-intensive) then batch Update trie (sequential).
	// For 60K TX this reduces IntermediateRoot from ~1.5s to ~0.3s.
	// FORK-SAFETY: trie.Hash() is deterministic regardless of Update insertion order
	// because MPT keys are tx hashes (fixed) and the trie structure is determined by keys, not insertion order.
	type marshalResult struct {
		hash common.Hash
		data []byte
		err  error
	}

	results := make([]marshalResult, 0, numDirty)
	resultsChan := make(chan marshalResult, numDirty)

	// Use worker pool for parallel marshaling
	numWorkers := runtime.NumCPU()
	if numWorkers > numDirty {
		numWorkers = numDirty
	}

	jobs := make(chan types.Transaction, numDirty)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for tx := range jobs {
				b, err := tx.Marshal()
				resultsChan <- marshalResult{hash: tx.Hash(), data: b, err: err}
			}
		}()
	}

	for _, tx := range db.dirtyTransactions {
		jobs <- tx
	}
	close(jobs)

	wg.Wait()
	close(resultsChan)

	for res := range resultsChan {
		if res.err != nil {
			return common.Hash{}, res.err
		}
		results = append(results, res)
	}

	// PARALLEL TRIE UPDATE: Use BatchUpdate for multi-core scaling
	batchKeys := make([][]byte, len(results))
	batchValues := make([][]byte, len(results))
	for i, res := range results {
		batchKeys[i] = res.hash.Bytes()
		batchValues[i] = res.data
	}

	if len(batchKeys) > 0 {
		if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
			oldValues := make([][]byte, len(batchKeys))
			if err := nomtTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, oldValues); err != nil {
				return common.Hash{}, err
			}
		} else if flatTrie, ok := db.trie.(*p_trie.FlatStateTrie); ok {
			oldValues := make([][]byte, len(batchKeys))
			if err := flatTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, oldValues); err != nil {
				return common.Hash{}, err
			}
		} else {
			if err := db.trie.BatchUpdate(batchKeys, batchValues); err != nil {
				return common.Hash{}, err
			}
		}
	}

	db.dirtyTransactions = make(map[common.Hash]types.Transaction)
	return db.trie.Hash(), nil
}

func (db *TransactionStateDB) setDirtyTransaction(tx types.Transaction) {
	db.dirtyTransactions[tx.Hash()] = tx

}

func (db *TransactionStateDB) Discard() (err error) {
	db.dirtyTransactions = make(map[common.Hash]types.Transaction)
	db.trie, err = p_trie.NewStateTrie(db.originRootHash, db.db, true)
	return err
}
