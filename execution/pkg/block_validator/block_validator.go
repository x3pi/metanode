package block_validator

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

// BlockValidator cung cấp các phương thức để xác thực và xử lý một block.
type BlockValidator struct {
	storageManager *storage.StorageManager
	chainState     *blockchain.ChainState
}

// NewBlockValidator tạo một BlockValidator mới.
func NewBlockValidator(
	storageManager *storage.StorageManager,
	chainState *blockchain.ChainState,
	freeFeeAddresses []common.Address,
) *BlockValidator {
	return &BlockValidator{
		storageManager: storageManager,
		chainState:     chainState,
	}
}

// ValidateAndGetParentBlock thực hiện các kiểm tra xác thực trên block được cung cấp
// và lấy block cha của nó.
func (bv *BlockValidator) ValidateAndGetParentBlock(blockData block.Block) (types.Block, error) {
	blockNumber := blockData.Header().BlockNumber()
	if blockNumber == 0 {
		return nil, fmt.Errorf("cannot process genesis block (block number 0)")
	}

	// Lấy hash và dữ liệu của block trước đó
	oldBlockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber - 1)
	if !ok {
		return nil, fmt.Errorf("cannot find previous block hash for block number %d", blockNumber-1)
	}

	oldBlockData, err := bv.chainState.GetBlockDatabase().GetBlockByHash(oldBlockHash)
	if err != nil {
		logger.Warn("ValidateAndGetParentBlock: error loading previous block %d from file: %v", blockNumber-1, err)
		return nil, fmt.Errorf("failed to load previous block with hash %s: %w", oldBlockHash.Hex(), err)
	}

	return oldBlockData, nil
}

// ProcessBlock xác thực và xử lý các giao dịch trong một block.
// Nó nhận blockData làm tham số và trả về kết quả xử lý.
func (bv *BlockValidator) ProcessBlock(ctx context.Context, blockData block.Block) (tx_processor.ProcessResult, error) {
	oldBlockData, err := bv.ValidateAndGetParentBlock(blockData)
	if err != nil {
		return tx_processor.ProcessResult{}, fmt.Errorf("ProcessBlock: validation and parent block retrieval failed: %w", err)
	}
	blockNumber := blockData.Header().BlockNumber()

	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), bv.storageManager.GetStorageTransaction())
	if err != nil {
		return tx_processor.ProcessResult{}, fmt.Errorf("ProcessBlock: failed to create TransactionStateDB for block %d: %w", blockNumber, err)
	}

	transactionHashes := blockData.Transactions()
	txs := make([]types.Transaction, 0, len(transactionHashes))
	logger.Info("Fetching %d transactions for block %d...", len(transactionHashes), blockNumber)

	for _, txHash := range transactionHashes {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			logger.Error("ProcessBlock: failed to get transaction %s from state DB for block %d: %v", txHash.Hex(), blockNumber, err)
			return tx_processor.ProcessResult{}, fmt.Errorf("cannot get transaction %s from state DB: %w", txHash.Hex(), err)
		}
		txs = append(txs, tx)
	}

	items := make([]grouptxns.Item, 0, len(txs))
	for i, tx := range txs {
		items = append(items, grouptxns.Item{
			ID:        i,
			Array:     tx.RelatedAddresses(),
			GroupID:   0,
			Tx:        tx,
			TimeStart: time.Now(),
		})
	}

	groupedGroups, _, err := grouptxns.GroupAndLimitTransactionsOptimized(items, mt_common.MAX_GROUP_GAS, mt_common.MAX_TOTAL_GAS, mt_common.MAX_GROUP_TIME, mt_common.MAX_TOTAL_TIME)
	if err != nil {
		return tx_processor.ProcessResult{}, fmt.Errorf("ProcessBlock: failed to create grouptxns for block %d: %w", blockNumber, err)
	}

	blockDatabase := block.NewBlockDatabase(bv.storageManager.GetStorageBlock())

	chainState, err := blockchain.NewChainState(bv.storageManager, blockDatabase, oldBlockData.Header(), bv.chainState.GetConfig(), bv.chainState.GetFreeFeeAddress(), "") // Empty backupPath for temporary chain state
	if err != nil {
		return tx_processor.ProcessResult{}, fmt.Errorf("ProcessBlock: failed to create chainState for block %d: %w", blockNumber, err)
	}

	// Use the block's stored timestamp for deterministic replay during validation
	blockTimeSec := blockData.Header().TimeStamp() / 1000 // Convert ms→s
	processResult, err := tx_processor.ProcessTransactions(ctx, chainState, groupedGroups, true, false, blockTimeSec)
	if err != nil {
		return tx_processor.ProcessResult{}, fmt.Errorf("ProcessBlock: failed to process transactions for block %d: %w", blockNumber, err)
	}

	return processResult, nil
}
