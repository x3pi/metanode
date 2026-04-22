package processor

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	// LƯU Ý: Đã xóa các import không cần thiết như 'rate' và 'command'

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	sharedmemory "github.com/meta-node-blockchain/meta-node/pkg/shared_memory"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// startSystemLoadMonitor theo dõi tải hệ thống và kích hoạt/hủy kích hoạt cơ chế ngắt mạch.
func startSystemLoadMonitor(tp *TransactionProcessor, bp *BlockProcessor) {
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond) // Kiểm tra tải 5 lần mỗi giây
		defer ticker.Stop()

		const highWatermark = HighWatermark
		const lowWatermark = LowWatermark
		var overloaded bool = false

		for {
			<-ticker.C

			// Tính tổng số giao dịch đang chờ xử lý
			poolSize := tp.transactionPool.CountTransactions()
			resultChanSize := len(tp.ProcessResultChan)
			virtualChainSize := len(bp.ProcessedVirtualTransactionChain)

			currentLoad := poolSize + resultChanSize + virtualChainSize

			// Kích hoạt ngắt mạch nếu vượt ngưỡng cao
			if !overloaded && currentLoad > highWatermark {
				logger.Warn("!!! HỆ THỐNG QUÁ TẢI !!! Giao dịch đang chờ: %d. Kích hoạt ngắt mạch.", currentLoad)
				sharedmemory.GlobalSharedMemory.Write("pendingOverloaded", true)
				overloaded = true
			} else if overloaded && currentLoad < lowWatermark {
				// Tắt ngắt mạch nếu tải đã giảm xuống dưới ngưỡng an toàn
				logger.Info("Hệ thống ổn định. Giao dịch đang chờ: %d. Hủy kích hoạt ngắt mạch.", currentLoad)
				sharedmemory.GlobalSharedMemory.Write("pendingOverloaded", false)
				overloaded = false
			}
		}
	}()
}

// InitProcessors khởi tạo tất cả các processor cần thiết cho hệ thống.
func InitProcessors(
	connectionsManager network.ConnectionsManager,
	messageSender network.MessageSender,
	transactionPool *transaction_pool.TransactionPool,
	freeFeeAddress map[common.Address]struct{},
	lastBlock types.Block,
	validatorAddress common.Address,
	transactionStateDB *transaction_state_db.TransactionStateDB,
	eventSystem *mt_filters.EventSystem,
	serviceType mt_common.ServiceType,
	smartContractStorageDBPath string,
	chainId string,
	node *node.HostNode,
	storageManager *storage.StorageManager,
	chainState *blockchain.ChainState,
	genesisPath string,
	config *config.SimpleChainConfig,

) (
	*ConnectionProcessor,
	*StateProcessor,
	*TransactionProcessor,
	*BlockProcessor,
	*SubscribeProcessor,
) {
	transactionProcessor := NewTransactionProcessor(
		messageSender,
		transactionPool,
		freeFeeAddress,
		eventSystem,
		smartContractStorageDBPath,
		chainId,
		storageManager,
		chainState,
	)

	subscribeProcessor := NewSubscribeProcessor(
		messageSender,
	)

	blockProcessor := NewBlockProcessor(
		lastBlock,
		transactionProcessor,
		subscribeProcessor,
		validatorAddress,
		connectionsManager,
		messageSender,
		eventSystem,
		serviceType,
		node,
		storageManager,
		chainState,
		genesisPath,
		config,
	)
	transactionProcessor.SetEnvironment(blockProcessor)

	// GỌI HÀM GIÁM SÁT TẠI ĐÂY
	startSystemLoadMonitor(transactionProcessor, blockProcessor)

	connProcessor := NewConnectionProcessor(
		connectionsManager,
	)
	connProcessor.SetBlockProcessor(blockProcessor)

	return connProcessor,
		NewStateProcessor(
			messageSender,
			chainState.GetAccountStateDB(),
			blockProcessor,
			storageManager,
		),
		transactionProcessor,
		blockProcessor,
		subscribeProcessor
}
