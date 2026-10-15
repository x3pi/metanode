package processor

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	// LƯU Ý: Đã xóa các import không cần thiết như 'rate' và 'command'

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	sharedmemory "github.com/meta-node-blockchain/meta-node/pkg/shared_memory"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"

	mt_filters "github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// overloadGate turns the overload flag into backpressure: while the node is overloaded, transaction-submission
// connections wait here (see network.SetTxAdmissionGate) instead of having their batches dropped. open is closed
// while the node is NOT overloaded, so waiting on it returns immediately in the normal case.
var overloadGate = struct {
	mu   sync.Mutex
	open chan struct{}
}{open: func() chan struct{} { c := make(chan struct{}); close(c); return c }()}

func setOverloaded(overloaded bool) {
	overloadGate.mu.Lock()
	defer overloadGate.mu.Unlock()
	select {
	case <-overloadGate.open: // currently open (not overloaded)
		if overloaded {
			overloadGate.open = make(chan struct{})
		}
	default: // currently closed (overloaded)
		if !overloaded {
			close(overloadGate.open)
		}
	}
}

// waitNotOverloaded blocks until the node is no longer overloaded or ctx is done.
func waitNotOverloaded(ctx context.Context) error {
	overloadGate.mu.Lock()
	ch := overloadGate.open
	overloadGate.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func resolveIngressWatermarks() (int, int) {
	high := HighWatermark
	low := LowWatermark
	if raw := os.Getenv("META_INGRESS_HIGH_WATERMARK"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			high = v
		}
	}
	if raw := os.Getenv("META_INGRESS_LOW_WATERMARK"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			low = v
		}
	}
	if low >= high {
		low = high * 2 / 3
	}
	return high, low
}

func computeSystemLoad(tp *TransactionProcessor, bp *BlockProcessor) (currentLoad int, baseLoad int, inFlight int) {
	var poolSize, resultChanSize, virtualChainSize int
	if tp != nil {
		if tp.TxValidatorPool != nil && tp.transactionPool != nil {
			poolSize = tp.transactionPool.CountTransactions()
		}
		resultChanSize = len(tp.ProcessResultChan)
	}
	if bp != nil {
		virtualChainSize = len(bp.ProcessedVirtualTransactionChain)
		if bp.ReceiptTracker != nil {
			inFlight = bp.InFlightTxCount()
		}
	}
	baseLoad = poolSize + resultChanSize + virtualChainSize
	currentLoad = baseLoad
	if inFlight > currentLoad {
		currentLoad = inFlight
	}
	return currentLoad, baseLoad, inFlight
}

// startSystemLoadMonitor theo dõi tải hệ thống và kích hoạt/hủy kích hoạt cơ chế ngắt mạch.
func startSystemLoadMonitor(tp *TransactionProcessor, bp *BlockProcessor) {
	p_network.SetTxAdmissionGate(waitNotOverloaded)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond) // Kiểm tra tải 5 lần mỗi giây
		defer ticker.Stop()

		highWatermark, lowWatermark := resolveIngressWatermarks()
		var overloaded bool
		var lastSeenBlock int64
		var idleOverloadTicks int

		for {
			<-ticker.C

			currentLoad, baseLoad, inFlight := computeSystemLoad(tp, bp)

			// Self-healing guard (design_backlog_bound_20261015.md §4.3.2):
			// If the gate is closed solely due to inFlight while local queues are empty and
			// no new blocks have committed for 25 ticks (5s), reconcile or reset inFlightCount
			// so dropped/untracked transactions can never leave the admission gate stuck closed.
			curBlock := GlobalPipelineStats.LastBlock.Load()
			if overloaded && baseLoad == 0 && curBlock == lastSeenBlock && (bp == nil || len(bp.commitChannel) == 0) {
				idleOverloadTicks++
				if idleOverloadTicks >= 25 && bp != nil && bp.ReceiptTracker != nil {
					bp.inFlightCount.Store(0)
					inFlight = 0
					currentLoad = baseLoad
					idleOverloadTicks = 0
				}
			} else {
				idleOverloadTicks = 0
				lastSeenBlock = curBlock
			}

			// Kích hoạt ngắt mạch nếu vượt ngưỡng cao
			if !overloaded && currentLoad > highWatermark {
				logger.Warn("!!! HỆ THỐNG QUÁ TẢI !!! Giao dịch đang chờ: %d (mempool=%d, in_flight=%d, high=%d). Kích hoạt ngắt mạch.",
					currentLoad, baseLoad, inFlight, highWatermark)
				sharedmemory.GlobalSharedMemory.Write("pendingOverloaded", true)
				setOverloaded(true)
				overloaded = true
			} else if overloaded && currentLoad < lowWatermark {
				// Tắt ngắt mạch nếu tải đã giảm xuống dưới ngưỡng an toàn
				logger.Info("Hệ thống ổn định. Giao dịch đang chờ: %d (mempool=%d, in_flight=%d, low=%d). Hủy kích hoạt ngắt mạch.",
					currentLoad, baseLoad, inFlight, lowWatermark)
				sharedmemory.GlobalSharedMemory.Write("pendingOverloaded", false)
				setOverloaded(false)
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
