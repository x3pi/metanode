package processor

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

// PrecomputedBlockRoots holds precalculated root data to eliminate Phase 1 calculation on committer thread
type PrecomputedBlockRoots struct {
	TxDB         *transaction_state_db.TransactionStateDB
	TxsRoot      common.Hash
	Receipts     types.Receipts
	ReceiptsRoot common.Hash
	Err          error
}

// ComputeTxsRoot computes the transaction state DB and its Merkle intermediate root.
func (bp *BlockProcessor) ComputeTxsRoot(txs []types.Transaction, blockNum uint64) (*transaction_state_db.TransactionStateDB, common.Hash, error) {
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(common.Hash{}, bp.storageManager.GetStorageTransaction())
	if err != nil {
		return nil, common.Hash{}, err
	}

	if logger.IsDebugEnabled() && len(txs) > 0 {
		var combinedHash common.Hash
		for _, tx := range txs {
			combinedHash = crypto.Keccak256Hash(combinedHash.Bytes(), tx.Hash().Bytes())
		}
		logger.Debug("🔍 [FORENSIC] Block %d: Input %d txs to txDB. Combined Input Hash: %s", blockNum, len(txs), combinedHash.Hex())
	}

	txDB.AddTransactions(txs)
	txsRoot, txsRootErr := txDB.IntermediateRoot()
	if txsRootErr != nil {
		return nil, common.Hash{}, txsRootErr
	}
	return txDB, txsRoot, nil
}

// ComputeReceiptsRoot computes the receipts trie and its Merkle intermediate root.
func (bp *BlockProcessor) ComputeReceiptsRoot(rps []types.Receipt, blockNum uint64, gei uint64) (types.Receipts, common.Hash) {
	if logger.IsDebugEnabled() && len(rps) > 0 {
		var combinedHash common.Hash
		for _, rcp := range rps {
			combinedHash = crypto.Keccak256Hash(combinedHash.Bytes(), rcp.TransactionHash().Bytes(), []byte{byte(rcp.Status())})
		}
		logger.Debug("🔍 [FORENSIC] Block %d: Input %d receipts to calculateReceiptsRoot. Combined Input Hash: %s", blockNum, len(rps), combinedHash.Hex())
	}

	startReceipts := time.Now()
	receipts, receiptsRoot := bp.calculateReceiptsRoot(rps)
	receiptsRootDuration := time.Since(startReceipts)
	logger.Info("🧾 [RECEIPTS-ROOT] Block #%d | GEI: %d | ReceiptsRoot: %s | Count: %d | Duration: %v",
		blockNum, gei, receiptsRoot.Hex(), len(rps), receiptsRootDuration)
	return receipts, receiptsRoot
}

// PrecomputeRoots runs receiptsRoot and txsRoot derivation concurrently in background
func (bp *BlockProcessor) PrecomputeRoots(txs []types.Transaction, rps []types.Receipt, blockNum uint64, gei uint64) *PrecomputedBlockRoots {
	if len(txs) == 0 && len(rps) == 0 {
		return nil
	}

	res := &PrecomputedBlockRoots{}
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		res.Receipts, res.ReceiptsRoot = bp.ComputeReceiptsRoot(rps, blockNum, gei)
	}()

	go func() {
		defer wg.Done()
		res.TxDB, res.TxsRoot, res.Err = bp.ComputeTxsRoot(txs, blockNum)
	}()

	wg.Wait()
	return res
}
